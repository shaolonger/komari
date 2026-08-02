package metrics

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openCatalogTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s-%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.MetricRetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMetricMigrationBackfillsOnceAndCheckpoints(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s-%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Client{}, &models.PingTask{}, &models.PingRecord{}, &models.PingRollup{}, &models.MetricMigrationState{}); err != nil {
		t.Fatal(err)
	}
	client := models.Client{UUID: "migration-node", Token: "migration-token"}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	task := models.PingTask{Name: "migration", Clients: models.StringArray{client.UUID}, Type: "icmp", Target: "127.0.0.1", Interval: 60}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Minute)
	rows := []models.PingRecord{
		{Client: client.UUID, TaskId: task.Id, Time: models.FromTime(base.Add(1 * time.Second)), Value: 10},
		{Client: client.UUID, TaskId: task.Id, Time: models.FromTime(base.Add(2 * time.Second)), Value: -1},
		{Client: client.UUID, TaskId: task.Id, Time: models.FromTime(base.Add(3 * time.Second)), Value: 20},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	// A partial/stale destination is cleared under the captured source cutoff.
	if err := db.Create(&models.PingRollup{Client: client.UUID, TaskId: task.Id, ResolutionSeconds: 60, BucketTime: models.FromTime(base), SampleCount: 99, ValidCount: 99, SumValue: 99, LastTime: models.FromTime(base)}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := StartMigration(db, "user:secret@tcp(example)/metrics"); err == nil {
		t.Fatal("external credential-bearing DSN was accepted")
	}
	if _, err := StartMigration(db, ""); err != nil {
		t.Fatal(err)
	}
	waitCompleted := func() MigrationStatus {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			status, err := GetMigrationStatus(context.Background(), db)
			if err != nil {
				t.Fatal(err)
			}
			if status.Status != "running" {
				return status
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("migration did not finish")
		return MigrationStatus{}
	}
	status := waitCompleted()
	if status.Status != "completed" || status.MigratedPoints != 3 || status.MetricsDone != 1 {
		t.Fatalf("migration status = %+v", status)
	}
	var minute models.PingRollup
	if err := db.Where("client = ? AND task_id = ? AND resolution_seconds = 60", client.UUID, task.Id).First(&minute).Error; err != nil {
		t.Fatal(err)
	}
	if minute.SampleCount != 3 || minute.ValidCount != 2 || minute.LossCount != 1 || minute.SumValue != 30 {
		t.Fatalf("backfilled rollup = %+v", minute)
	}

	// A completed rerun rebuilds from a fresh cutoff instead of double-counting.
	if _, err := StartMigration(db, ""); err != nil {
		t.Fatal(err)
	}
	if status := waitCompleted(); status.Status != "completed" || status.MigratedPoints != 3 {
		t.Fatalf("repeated migration status = %+v", status)
	}
	if err := db.Where("client = ? AND task_id = ? AND resolution_seconds = 60", client.UUID, task.Id).First(&minute).Error; err != nil {
		t.Fatal(err)
	}
	if minute.SampleCount != 3 {
		t.Fatalf("repeated migration double-counted samples: %+v", minute)
	}
}

func TestPersistMigrationPageRetriesSQLiteTableLock(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s-%d?mode=memory&cache=shared&_busy_timeout=1", t.Name(), time.Now().UnixNano())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.PingRollup{}, &models.MetricMigrationState{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.MetricMigrationState{ID: 1, Status: "running"}).Error; err != nil {
		t.Fatal(err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(2)
	blocker, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	blockerTx, err := blocker.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var status string
	if err := blockerTx.QueryRowContext(context.Background(), "SELECT status FROM metric_migration_states WHERE id = 1").Scan(&status); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		result <- persistMigrationPage(context.Background(), db, nil, 42, 3)
	}()
	time.Sleep(20 * time.Millisecond)
	if err := blockerTx.Rollback(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("migration page retry remained blocked")
	}
	var state models.MetricMigrationState
	if err := db.First(&state, 1).Error; err != nil {
		t.Fatal(err)
	}
	if state.CheckpointRowID != 42 || state.MigratedPoints != 3 {
		t.Fatalf("migration checkpoint after retry = %+v", state)
	}
}

func TestMetricCatalogRetentionIsAllowlistedAndPersistent(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	definitions, err := List(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 24 {
		t.Fatalf("definitions = %d, want 24", len(definitions))
	}
	updated, err := UpdateRetention(ctx, db, "ping.latency_ms", 365)
	if err != nil || updated.RetentionDays != 365 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	plan, err := LoadRetentionPlan(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.PingOverridden || plan.RecordOverridden || plan.PingDays != 365 || plan.RecordDays != 30 {
		t.Fatalf("Ping-only retention plan = %+v", plan)
	}
	if _, err := UpdateRetention(ctx, db, "cpu.usage", 14); err != nil {
		t.Fatal(err)
	}
	plan, err = LoadRetentionPlan(ctx, db)
	if err != nil || !plan.RecordOverridden || !plan.PingOverridden || plan.RecordDays != 30 {
		t.Fatalf("family retention plan = %+v err=%v", plan, err)
	}
	after, err := List(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, definition := range after {
		if definition.Name == "ping.latency_ms" {
			found = definition.RetentionDays == 365
		}
	}
	if !found {
		t.Fatal("retention override was not returned")
	}
	if _, err := UpdateRetention(ctx, db, "injected.metric", 30); err == nil {
		t.Fatal("unknown metric was accepted")
	}
	if _, err := UpdateRetention(ctx, db, "cpu.usage", 0); err == nil {
		t.Fatal("unsafe retention was accepted")
	}
}
