package tasks

import (
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
	return db
}

func createPingTask(t *testing.T, db *gorm.DB, client string) models.PingTask {
	t.Helper()
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
	return task
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
	// cascading to ping_records. Raw SQL intentionally bypasses model-level
	// association checks so the query guard is exercised.
	if err := db.Exec(
		"INSERT INTO ping_records (client, task_id, time, value) VALUES (?, ?, ?, ?)",
		client,
		999_999,
		models.FromTime(now),
		80,
	).Error; err != nil {
		t.Fatalf("create legacy orphan ping record: %v", err)
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
