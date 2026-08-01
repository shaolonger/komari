package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/komari-monitor/komari/cmd/flags"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	tempDir, err := os.MkdirTemp("", "komari-ping-task-tests-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tempDir)

	flags.DatabaseType = "sqlite"
	flags.DatabaseFile = filepath.Join(tempDir, "ping-tasks.db")
	os.Exit(m.Run())
}

func resetPingTaskData(t *testing.T) *gorm.DB {
	t.Helper()
	db := dbcore.GetDBInstance()
	if err := db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&models.PingRecord{}).Error; err != nil {
		t.Fatalf("clear ping records: %v", err)
	}
	if err := db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&models.PingTask{}).Error; err != nil {
		t.Fatalf("clear ping tasks: %v", err)
	}
	if err := db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&models.Client{}).Error; err != nil {
		t.Fatalf("clear clients: %v", err)
	}
	publishPingAssignmentIndex(nil)
	return db
}

func createPingTask(t *testing.T, db *gorm.DB, client string) models.PingTask {
	t.Helper()
	if err := db.Create(&models.Client{
		UUID:  client,
		Token: "ping-test-token-" + client,
		Name:  client,
	}).Error; err != nil {
		t.Fatalf("create ping client: %v", err)
	}
	task := models.PingTask{
		Name:     "Delete lifecycle test",
		Clients:  models.StringArray{client},
		Type:     "icmp",
		Target:   "1.1.1.1",
		Interval: 60,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create ping task: %v", err)
	}
	var allTasks []models.PingTask
	if err := db.Order("weight ASC").Order("id ASC").Find(&allTasks).Error; err != nil {
		t.Fatalf("load Ping assignment fixture: %v", err)
	}
	publishPingAssignmentIndex(allTasks)
	return task
}

func TestPingAssignmentIndexIsImmutableAndNormalized(t *testing.T) {
	task := models.PingTask{
		Id: 7, Name: "indexed", Clients: models.StringArray{"node-a", "node-a", "", "node-b"},
		Type: "icmp", Target: "1.1.1.1", Interval: 30,
	}
	index := publishPingAssignmentIndex([]models.PingTask{task})
	task.Name = "mutated source"
	task.Clients[0] = "mutated-source"

	if len(index.assignments) != 2 || len(index.tasksByClient["node-a"]) != 1 {
		t.Fatalf("normalized index = %+v", index)
	}
	first := GetPingTasksByClient("node-a")
	if len(first) != 1 || first[0].Name != "indexed" {
		t.Fatalf("indexed tasks = %+v", first)
	}
	first[0].Name = "mutated reader"
	first[0].Clients[0] = "mutated-reader"
	second := GetPingTasksByClient("node-a")
	if len(second) != 1 || second[0].Name != "indexed" || second[0].Clients[0] != "node-a" {
		t.Fatalf("reader mutated immutable index: %+v", second)
	}
	if index.generation == 0 {
		t.Fatal("assignment generation was not published")
	}
}

func TestDeletePingTaskRemovesRecordsAndRejectsLateResults(t *testing.T) {
	db := resetPingTaskData(t)
	client := "ping-delete-client"
	task := createPingTask(t, db, client)
	record := models.PingRecord{
		Client: client,
		TaskId: task.Id,
		Time:   models.FromTime(time.Now()),
		Value:  42,
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("create ping record: %v", err)
	}

	if err := DeletePingTask([]uint{task.Id}); err != nil {
		t.Fatalf("delete ping task: %v", err)
	}

	var recordCount int64
	if err := db.Model(&models.PingRecord{}).Where("task_id = ?", task.Id).Count(&recordCount).Error; err != nil {
		t.Fatalf("count deleted task records: %v", err)
	}
	if recordCount != 0 {
		t.Fatalf("deleted task records = %d, want 0", recordCount)
	}

	err := SavePingRecord(record)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("late ping result error = %v, want record-not-found", err)
	}
}

func TestGetPingRecordsExcludesLegacyOrphans(t *testing.T) {
	db := resetPingTaskData(t)
	client := "ping-orphan-client"
	activeTask := createPingTask(t, db, client)
	now := time.Now()
	activeRecord := models.PingRecord{Client: client, TaskId: activeTask.Id, Time: models.FromTime(now), Value: 20}
	if err := db.Create(&activeRecord).Error; err != nil {
		t.Fatalf("create active ping record: %v", err)
	}

	// This models records left by versions that deleted the task row without
	// cascading to ping_records. A pinned connection temporarily disables the
	// newly enforced foreign-key check solely to reproduce that legacy state.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get SQL database: %v", err)
	}
	connection, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("get SQL connection: %v", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(context.Background(), "PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatalf("disable foreign keys for legacy fixture: %v", err)
	}
	defer connection.ExecContext(context.Background(), "PRAGMA foreign_keys = ON")
	if _, err := connection.ExecContext(
		context.Background(),
		"INSERT INTO ping_records (client, task_id, time, value) VALUES (?, ?, ?, ?)",
		client,
		999_999,
		models.FromTime(now),
		80,
	); err != nil {
		t.Fatalf("create legacy orphan ping record: %v", err)
	}
	if _, err := connection.ExecContext(context.Background(), "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("restore foreign keys after legacy fixture: %v", err)
	}

	records, err := GetPingRecords(client, -1, now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("get ping records: %v", err)
	}
	if len(records) != 1 || records[0].TaskId != activeTask.Id {
		t.Fatalf("visible ping records = %+v, want only active task %d", records, activeTask.Id)
	}
}

func TestSavePingRecordUsesValidatedWriter(t *testing.T) {
	db := resetPingTaskData(t)
	client := "ping-writer-client"
	task := createPingTask(t, db, client)
	record := models.PingRecord{Client: client, TaskId: task.Id, Time: models.FromTime(time.Now()), Value: 25}
	if err := SavePingRecord(record); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&models.PingRecord{}).Where("client = ? AND task_id = ?", client, task.Id).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("ping records = %d, want 1", count)
	}

	record.Client = "not-assigned"
	if err := SavePingRecord(record); !errors.Is(err, ErrPingTaskNotAssigned) {
		t.Fatalf("unassigned ping result error = %v", err)
	}
}

func TestQueryPingRecordsForClientsUsesOneNarrowAuthorizedQuery(t *testing.T) {
	db := resetPingTaskData(t)
	now := time.Now().UTC().Truncate(time.Second)
	taskA := createPingTask(t, db, "ping-set-a")
	taskB := createPingTask(t, db, "ping-set-b")
	rows := []models.PingRecord{
		{Client: "ping-set-a", TaskId: taskA.Id, Time: models.FromTime(now.Add(-2 * time.Minute)), Value: 10},
		{Client: "ping-set-a", TaskId: taskA.Id, Time: models.FromTime(now.Add(-time.Minute)), Value: 20},
		{Client: "ping-set-b", TaskId: taskB.Id, Time: models.FromTime(now.Add(-time.Minute)), Value: 999},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	result, err := QueryPingRecordsForClients(context.Background(), db, []string{"ping-set-a"}, -1, now.Add(-time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if result.SQLQueries != 1 || len(result.Records) != 2 {
		t.Fatalf("records=%+v queries=%d, want 2/1", result.Records, result.SQLQueries)
	}
	for _, record := range result.Records {
		if record.Client != "ping-set-a" || record.TaskId != taskA.Id {
			t.Fatalf("unauthorized or wrong-task record leaked: %+v", record)
		}
	}

	empty, err := QueryPingRecordsForClients(context.Background(), db, []string{}, -1, now.Add(-time.Hour), now)
	if err != nil || empty.SQLQueries != 0 || len(empty.Records) != 0 {
		t.Fatalf("empty authorized query=%+v err=%v", empty, err)
	}
}
