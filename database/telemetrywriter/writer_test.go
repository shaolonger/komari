package telemetrywriter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/historycache"
	"github.com/mattn/go-sqlite3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openTestDatabase(t testing.TB) (*gorm.DB, *sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "writer.db")
	dsn := path + "?_busy_timeout=1&_journal_mode=WAL&_foreign_keys=on"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Client{}, &models.Record{}, &models.GPURecord{}, &models.PingTask{}, &models.PingRecord{}, &models.PingRollup{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db, sqlDB, dsn
}

func newTestWriter(t testing.TB, sqlDB *sql.DB, config Config) *Writer {
	t.Helper()
	writer, err := New(sqlDB, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = writer.Close(ctx)
	})
	return writer
}

func testBatch(index, rows int) Batch {
	base := time.Unix(1_700_000_000+int64(index*rows), 0)
	batch := Batch{
		Records:    make([]models.Record, rows),
		GPURecords: make([]models.GPURecord, rows),
	}
	for i := 0; i < rows; i++ {
		at := models.FromTime(base.Add(time.Duration(i) * time.Second))
		batch.Records[i] = models.Record{Client: fmt.Sprintf("node-%d", index), Time: at, Cpu: float32(i), Ram: int64(i)}
		batch.GPURecords[i] = models.GPURecord{Client: fmt.Sprintf("node-%d", index), Time: at, DeviceIndex: 0, DeviceName: "GPU", Utilization: float32(i)}
	}
	return batch
}

func TestWriterPersistsAllRecordTypesAtomically(t *testing.T) {
	db, sqlDB, _ := openTestDatabase(t)
	sqlDB.SetMaxOpenConns(1)
	writer := newTestWriter(t, sqlDB, Config{})
	client := models.Client{UUID: "node", Token: "writer-test-token", Name: "node"}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	task := models.PingTask{Name: "ping", Clients: models.StringArray{"node"}, Type: "icmp", Target: "127.0.0.1", Interval: 60}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	batch := testBatch(1, 300)
	batch.PingRecords = []models.PingRecord{{Client: "node", TaskId: task.Id, Time: models.FromTime(time.Now()), Value: 12}}
	generation := historycache.Generation()
	if err := writer.Submit(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if historycache.Generation() <= generation {
		t.Fatal("durable telemetry commit did not invalidate history responses")
	}
	for model, want := range map[any]int64{&models.Record{}: 300, &models.GPURecord{}: 300, &models.PingRecord{}: 1} {
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil || count != want {
			t.Fatalf("%T count = %d, %v; want %d", model, count, err, want)
		}
	}
	var rollups []models.PingRollup
	if err := db.Order("resolution_seconds ASC").Find(&rollups).Error; err != nil {
		t.Fatal(err)
	}
	if len(rollups) != 3 {
		t.Fatalf("Ping rollup tiers = %d, want 3", len(rollups))
	}
	for _, rollup := range rollups {
		if rollup.SampleCount != 1 || rollup.SumValue != 12 || rollup.MinValue != 12 || rollup.MaxValue != 12 || rollup.LastValue != 12 {
			t.Fatalf("invalid Ping rollup: %+v", rollup)
		}
	}
}

func TestWriterRetriesBusyAndRejectsPermanentFailureWithoutPartialCommit(t *testing.T) {
	db, sqlDB, _ := openTestDatabase(t)
	var attempts atomic.Int32
	writer := newTestWriter(t, sqlDB, Config{
		MaxRetries: 3, RetryBackoff: time.Millisecond,
		beforeAttempt: func(attempt int) error {
			attempts.Add(1)
			if attempt < 2 {
				return sqlite3.Error{Code: sqlite3.ErrBusy}
			}
			return nil
		},
	})
	if err := writer.Submit(context.Background(), testBatch(1, 4)); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", attempts.Load())
	}

	if err := db.Migrator().DropTable(&models.GPURecord{}); err != nil {
		t.Fatal(err)
	}
	err := writer.Submit(context.Background(), testBatch(2, 4))
	if err == nil {
		t.Fatal("permanent schema failure was reported as success")
	}
	var records int64
	if err := db.Model(&models.Record{}).Where("client = ?", "node-2").Count(&records).Error; err != nil {
		t.Fatal(err)
	}
	if records != 0 {
		t.Fatalf("partial transaction committed %d records", records)
	}
}

func TestWriterUpsertsPingRollupBucketsAtomically(t *testing.T) {
	db, sqlDB, _ := openTestDatabase(t)
	writer := newTestWriter(t, sqlDB, Config{})
	client := models.Client{UUID: "rollup-node", Token: "rollup-token", Name: "rollup-node"}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	task := models.PingTask{Name: "rollup", Clients: models.StringArray{client.UUID}, Type: "icmp", Target: "127.0.0.1", Interval: 60}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Minute).Add(5 * time.Second)
	for offset, value := range []int{12, 18} {
		batch := Batch{PingRecords: []models.PingRecord{{
			Client: client.UUID, TaskId: task.Id,
			Time: models.FromTime(base.Add(time.Duration(offset) * time.Second)), Value: value,
		}}}
		if err := writer.Submit(context.Background(), batch); err != nil {
			t.Fatal(err)
		}
	}
	var rollup models.PingRollup
	if err := db.Where("client = ? AND task_id = ? AND resolution_seconds = 60", client.UUID, task.Id).First(&rollup).Error; err != nil {
		t.Fatal(err)
	}
	if rollup.SampleCount != 2 || rollup.SumValue != 30 || rollup.MinValue != 12 || rollup.MaxValue != 18 || rollup.LastValue != 18 {
		t.Fatalf("merged Ping rollup = %+v", rollup)
	}
}

func TestWriterQueueBackpressure(t *testing.T) {
	_, sqlDB, _ := openTestDatabase(t)
	blocked := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	writer := newTestWriter(t, sqlDB, Config{
		QueueCapacity: 1,
		beforeAttempt: func(int) error {
			once.Do(func() { close(started) })
			<-blocked
			return nil
		},
	})
	results := make(chan error, 2)
	go func() { results <- writer.Submit(context.Background(), testBatch(1, 1)) }()
	<-started
	go func() { results <- writer.Submit(context.Background(), testBatch(2, 1)) }()
	deadline := time.Now().Add(time.Second)
	for len(writer.queue) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := writer.Submit(ctx, testBatch(3, 1))
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("full queue error = %v, want deadline", err)
	}
	close(blocked)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestWriterCloseDrainsQueuedBatches(t *testing.T) {
	db, sqlDB, _ := openTestDatabase(t)
	blocked := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	writer := newTestWriter(t, sqlDB, Config{
		QueueCapacity: 8,
		beforeAttempt: func(int) error {
			once.Do(func() { close(started) })
			<-blocked
			return nil
		},
	})
	results := make(chan error, 6)
	for i := 0; i < 6; i++ {
		go func(i int) { results <- writer.Submit(context.Background(), testBatch(i, 1)) }(i)
	}
	<-started
	deadline := time.Now().Add(time.Second)
	for len(writer.queue) != 5 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	closed := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		closed <- writer.Close(ctx)
	}()
	close(blocked)
	for i := 0; i < 6; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&models.Record{}).Count(&count).Error; err != nil || count != 6 {
		t.Fatalf("drained records = %d, %v; want 6", count, err)
	}
	if err := writer.Submit(context.Background(), testBatch(9, 1)); !errors.Is(err, ErrClosed) {
		t.Fatalf("submit after close = %v", err)
	}
}

func TestWriterRecoversFromRealSQLiteBusy(t *testing.T) {
	_, sqlDB, dsn := openTestDatabase(t)
	writer := newTestWriter(t, sqlDB, Config{MaxRetries: 6, RetryBackoff: 5 * time.Millisecond})
	locker, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	connection, err := locker.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(35 * time.Millisecond)
		_, _ = connection.ExecContext(context.Background(), "COMMIT")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := writer.Submit(ctx, testBatch(1, 2)); err != nil {
		t.Fatalf("writer did not recover after lock release: %v", err)
	}
}

func TestWriterAllowsConcurrentWALReaders(t *testing.T) {
	db, sqlDB, _ := openTestDatabase(t)
	writer := newTestWriter(t, sqlDB, Config{QueueCapacity: 32})
	results := make(chan error, 32)
	for i := 0; i < 32; i++ {
		go func(i int) {
			batch := testBatch(i, 16)
			batch.GPURecords = nil
			results <- writer.Submit(context.Background(), batch)
		}(i)
	}
	for i := 0; i < 64; i++ {
		var count int64
		if err := db.Model(&models.Record{}).Count(&count).Error; err != nil {
			t.Fatalf("concurrent read %d: %v", i, err)
		}
	}
	for i := 0; i < 32; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := db.Model(&models.Record{}).Count(&count).Error; err != nil || count != 512 {
		t.Fatalf("final count = %d, %v; want 512", count, err)
	}
}

func TestWriterCoalescesFleetBurstIntoFewerTransactions(t *testing.T) {
	db, sqlDB, _ := openTestDatabase(t)
	var transactions atomic.Int32
	writer := newTestWriter(t, sqlDB, Config{
		QueueCapacity: 64,
		MaxBatchRows:  64,
		MaxBatchDelay: 20 * time.Millisecond,
		beforeAttempt: func(int) error {
			transactions.Add(1)
			return nil
		},
	})
	const submissions = 32
	results := make(chan error, submissions)
	ready := make(chan struct{})
	for index := 0; index < submissions; index++ {
		go func(index int) {
			<-ready
			batch := testBatch(index, 1)
			batch.GPURecords = nil
			results <- writer.Submit(context.Background(), batch)
		}(index)
	}
	close(ready)
	for index := 0; index < submissions; index++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if got := transactions.Load(); got >= submissions || got > 4 {
		t.Fatalf("transactions = %d for %d submissions, want <= 4", got, submissions)
	}
	var count int64
	if err := db.Model(&models.Record{}).Count(&count).Error; err != nil || count != submissions {
		t.Fatalf("coalesced rows = %d err=%v, want %d", count, err, submissions)
	}
}

func BenchmarkPreparedBatchWriter(b *testing.B) {
	_, sqlDB, _ := openTestDatabase(b)
	writer := newTestWriter(b, sqlDB, Config{})
	batch := testBatch(1, 256)
	batch.GPURecords = nil
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := writer.Submit(context.Background(), batch); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(1, "sql/op")
}
