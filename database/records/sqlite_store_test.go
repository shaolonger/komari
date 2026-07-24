package records

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/telemetrywriter"
	"github.com/komari-monitor/komari/internal/storage"
	"github.com/komari-monitor/komari/internal/storage/contracttest"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newSQLiteTelemetryContractStore(t *testing.T) (*SQLiteTelemetryStore, *gorm.DB, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "telemetry.db")
	dsn := fmt.Sprintf("file:%s?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on", path)
	readDB, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	readSQL, err := readDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	readSQL.SetMaxOpenConns(8)
	if err := readDB.AutoMigrate(
		&models.Client{}, &models.Record{}, &models.GPURecord{}, &models.PingTask{}, &models.PingRecord{},
	); err != nil {
		t.Fatal(err)
	}
	if err := readDB.Table("records_long_term").AutoMigrate(&models.Record{}); err != nil {
		t.Fatal(err)
	}
	if err := readDB.Table("gpu_records_long_term").AutoMigrate(&models.GPURecord{}); err != nil {
		t.Fatal(err)
	}
	if err := ensureTierSchema(readDB); err != nil {
		t.Fatal(err)
	}
	if err := ensureCompactionSchema(readDB, recordStream); err != nil {
		t.Fatal(err)
	}
	if err := ensureCompactionSchema(readDB, gpuStream); err != nil {
		t.Fatal(err)
	}
	writeDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	writeDB.SetMaxOpenConns(1)
	store, err := NewSQLiteTelemetryStore(readDB, writeDB, telemetrywriter.Config{
		QueueCapacity: 16, RetryBackoff: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = writeDB.Close()
		_ = readSQL.Close()
	})
	return store, readDB, writeDB
}

func TestSQLiteTelemetryStoreContract(t *testing.T) {
	contracttest.Telemetry(t, func(t *testing.T) storage.TelemetryStore {
		store, _, _ := newSQLiteTelemetryContractStore(t)
		return store
	})
}

func TestSQLiteTelemetryStoreBatchRollbackOnFault(t *testing.T) {
	store, db, _ := newSQLiteTelemetryContractStore(t)
	t.Cleanup(func() { _ = store.Close(context.Background()) })
	if err := db.Migrator().DropTable(&models.GPURecord{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	err := store.WriteBatch(context.Background(), storage.TelemetryBatch{
		Records: []models.Record{{Client: "rollback", Time: models.FromTime(now), Cpu: 1}},
		GPURecords: []models.GPURecord{{
			Client: "rollback", Time: models.FromTime(now), DeviceIndex: 0,
		}},
	})
	if err == nil {
		t.Fatal("faulted batch succeeded")
	}
	var count int64
	if err := db.Table("records").Where("client = ?", "rollback").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("partial batch committed %d record rows", count)
	}
}

func TestSQLiteTelemetryStoreRetentionDeletesOnlyExpiredRawRows(t *testing.T) {
	store, db, _ := newSQLiteTelemetryContractStore(t)
	t.Cleanup(func() { _ = store.Close(context.Background()) })
	now := time.Now().UTC().Truncate(time.Minute)
	rows := []models.Record{
		{Client: "expired", Time: models.FromTime(now.Add(-40 * 24 * time.Hour)), Cpu: 1},
		{Client: "fresh", Time: models.FromTime(now.Add(-time.Hour)), Cpu: 2},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRetention(context.Background(), storage.RetentionPolicy{
		Now: now, FinalCutoff: now.Add(-30 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	var expired, fresh int64
	if err := db.Table("records").Where("client = ?", "expired").Count(&expired).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("records").Where("client = ?", "fresh").Count(&fresh).Error; err != nil {
		t.Fatal(err)
	}
	if expired != 0 || fresh != 1 {
		t.Fatalf("expired=%d fresh=%d", expired, fresh)
	}
}

func TestSQLiteTelemetryStoreHealthReportsClosedPool(t *testing.T) {
	store, _, writeDB := newSQLiteTelemetryContractStore(t)
	if err := store.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := writeDB.Close(); err != nil {
		t.Fatal(err)
	}
	health, err := store.Health(context.Background())
	if err == nil || health.Ready {
		t.Fatalf("health=%+v err=%v", health, err)
	}
}

func TestSQLiteTelemetryStoreRejectsInvalidInputs(t *testing.T) {
	if _, err := NewSQLiteTelemetryStore(nil, nil, telemetrywriter.Config{}); err == nil {
		t.Fatal("nil databases accepted")
	}
	store, _, _ := newSQLiteTelemetryContractStore(t)
	t.Cleanup(func() { _ = store.Close(context.Background()) })
	if _, err := store.AggregateRecords(context.Background(), storage.AggregateQuery{}); err == nil {
		t.Fatal("zero aggregate resolution accepted")
	}
	if _, err := store.ApplyRetention(context.Background(), storage.RetentionPolicy{
		Now: time.Now(), FinalCutoff: time.Now().Add(time.Hour),
	}); err == nil {
		t.Fatal("future retention cutoff accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := store.WriteBatch(ctx, storage.TelemetryBatch{Records: []models.Record{{
		Client: "cancel", Time: models.FromTime(time.Now()),
	}}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("write error=%v, want canceled", err)
	}
}
