package records

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var uuid = "7901508c-304f-49aa-b84f-957c33ae6f8a"

func newCompactionTestDB(t testing.TB) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	for _, migrate := range []func() error{
		func() error { return db.AutoMigrate(&models.Record{}, &models.GPURecord{}) },
		func() error { return db.Table("records_long_term").AutoMigrate(&models.Record{}) },
		func() error { return db.Table("gpu_records_long_term").AutoMigrate(&models.GPURecord{}) },
	} {
		if err := migrate(); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureCompactionSchema(db, recordStream); err != nil {
		t.Fatal(err)
	}
	if err := ensureCompactionSchema(db, gpuStream); err != nil {
		t.Fatal(err)
	}
	if err := ensureTierSchema(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestIncrementalRecordCompactionBoundariesAndIdempotence(t *testing.T) {
	db := newCompactionTestDB(t)
	now := time.Date(2026, 6, 13, 12, 7, 0, 0, time.UTC)
	records := []models.Record{
		{Client: "node-a", Time: models.FromTime(time.Date(2026, 6, 13, 6, 46, 0, 0, time.UTC)), Cpu: 10},
		{Client: "node-a", Time: models.FromTime(time.Date(2026, 6, 13, 6, 59, 0, 0, time.UTC)), Cpu: 30},
		{Client: "node-a", Time: models.FromTime(time.Date(2026, 6, 13, 7, 46, 0, 0, time.UTC)), Cpu: 40},
		{Client: "node-a", Time: models.FromTime(time.Date(2026, 6, 13, 7, 59, 59, 0, time.UTC)), Cpu: 60},
		{Client: "node-a", Time: models.FromTime(time.Date(2026, 6, 13, 8, 0, 0, 0, time.UTC)), Cpu: 99},
	}
	if err := db.Create(&records).Error; err != nil {
		t.Fatal(err)
	}
	stats, err := compactRecordStreamAt(context.Background(), db, now, compactionRunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Buckets != 2 || stats.Rows != 4 {
		t.Fatalf("stats=%+v, want 2 buckets/4 rows", stats)
	}
	var long []models.Record
	if err := db.Table("records_long_term").Order("time ASC").Find(&long).Error; err != nil {
		t.Fatal(err)
	}
	if len(long) != 2 || long[0].Cpu != 24 || long[1].Cpu != 54 {
		t.Fatalf("aggregates=%+v", long)
	}
	var raw []models.Record
	if err := db.Order("time ASC").Find(&raw).Error; err != nil {
		t.Fatal(err)
	}
	if len(raw) != 3 || !raw[0].Time.ToTime().Equal(time.Date(2026, 6, 13, 7, 46, 0, 0, time.UTC)) || raw[2].Cpu != 99 {
		t.Fatalf("remaining raw=%+v", raw)
	}
	before := append([]models.Record(nil), long...)
	if _, err := compactRecordStreamAt(context.Background(), db, now, compactionRunOptions{}); err != nil {
		t.Fatal(err)
	}
	long = nil
	if err := db.Table("records_long_term").Order("time ASC").Find(&long).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, long) {
		t.Fatalf("second run changed aggregates: before=%+v after=%+v", before, long)
	}
	var state compactionState
	if err := db.Where("stream = ?", recordStream).First(&state).Error; err != nil {
		t.Fatal(err)
	}
	if got, want := state.Watermark.ToTime(), time.Date(2026, 6, 13, 8, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("watermark=%s, want %s", got, want)
	}
}

func TestIncrementalCompactionReprocessesLateDataInsideOverlap(t *testing.T) {
	db := newCompactionTestDB(t)
	now := time.Date(2026, 6, 13, 12, 7, 0, 0, time.UTC)
	first := models.Record{Client: "late-node", Time: models.FromTime(time.Date(2026, 6, 13, 7, 31, 0, 0, time.UTC)), Cpu: 10}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := compactRecordStreamAt(context.Background(), db, now, compactionRunOptions{}); err != nil {
		t.Fatal(err)
	}
	late := models.Record{Client: "late-node", Time: models.FromTime(time.Date(2026, 6, 13, 7, 39, 0, 0, time.UTC)), Cpu: 30}
	if err := db.Create(&late).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := compactRecordStreamAt(context.Background(), db, now, compactionRunOptions{}); err != nil {
		t.Fatal(err)
	}
	var aggregate models.Record
	if err := db.Table("records_long_term").Where("client = ?", "late-node").First(&aggregate).Error; err != nil {
		t.Fatal(err)
	}
	if aggregate.Cpu != 24 {
		t.Fatalf("late-data aggregate cpu=%v, want 24", aggregate.Cpu)
	}
}

func TestIncrementalCompactionRecoversAfterChunkedDeleteCrash(t *testing.T) {
	db := newCompactionTestDB(t)
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	bucket := time.Date(2026, 6, 13, 5, 0, 0, 0, time.UTC)
	records := make([]models.Record, 6_001)
	for index := range records {
		records[index] = models.Record{Client: "crash-node", Time: models.FromTime(bucket.Add(time.Duration(index%900) * time.Second)), Cpu: float32(index % 100)}
	}
	if err := db.CreateInBatches(&records, 250).Error; err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected crash after committed delete chunk")
	fired := false
	_, err := compactRecordStreamAt(context.Background(), db, now, compactionRunOptions{Failpoint: func(stage, stream string, gotBucket time.Time) error {
		if !fired && stage == "after_delete_chunk" && stream == recordStream && gotBucket.Equal(bucket) {
			fired = true
			return injected
		}
		return nil
	}})
	if !errors.Is(err, injected) {
		t.Fatalf("compaction error=%v, want injected crash", err)
	}
	var rawCount, pendingCount, aggregateCount int64
	_ = db.Model(&models.Record{}).Count(&rawCount).Error
	_ = db.Model(&pendingCompaction{}).Where("stream = ?", recordStream).Count(&pendingCount).Error
	_ = db.Table("records_long_term").Count(&aggregateCount).Error
	if rawCount != 1_001 || pendingCount != 1 || aggregateCount != 1 {
		t.Fatalf("crash state raw=%d pending=%d aggregate=%d", rawCount, pendingCount, aggregateCount)
	}
	var before models.Record
	if err := db.Table("records_long_term").First(&before).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := compactRecordStreamAt(context.Background(), db, now, compactionRunOptions{}); err != nil {
		t.Fatal(err)
	}
	var after models.Record
	if err := db.Table("records_long_term").First(&after).Error; err != nil {
		t.Fatal(err)
	}
	_ = db.Model(&models.Record{}).Count(&rawCount).Error
	_ = db.Model(&pendingCompaction{}).Where("stream = ?", recordStream).Count(&pendingCount).Error
	if rawCount != 0 || pendingCount != 0 || !reflect.DeepEqual(before, after) {
		t.Fatalf("recovery state raw=%d pending=%d before=%+v after=%+v", rawCount, pendingCount, before, after)
	}
}

func TestIncrementalGPUCompactionKeepsDevicesIndependent(t *testing.T) {
	db := newCompactionTestDB(t)
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	bucket := time.Date(2026, 6, 13, 6, 0, 0, 0, time.UTC)
	records := []models.GPURecord{
		{Client: "gpu-node", DeviceIndex: 0, DeviceName: "GPU 0 old", Time: models.FromTime(bucket.Add(time.Minute)), MemUsed: 10, Utilization: 10, Temperature: 40},
		{Client: "gpu-node", DeviceIndex: 0, DeviceName: "GPU 0", Time: models.FromTime(bucket.Add(2 * time.Minute)), MemUsed: 30, Utilization: 30, Temperature: 60},
		{Client: "gpu-node", DeviceIndex: 1, DeviceName: "GPU 1", Time: models.FromTime(bucket.Add(time.Minute)), MemUsed: 100, Utilization: 50, Temperature: 70},
		{Client: "gpu-node", DeviceIndex: 1, DeviceName: "GPU 1", Time: models.FromTime(bucket.Add(2 * time.Minute)), MemUsed: 200, Utilization: 90, Temperature: 80},
	}
	if err := db.Create(&records).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := compactGPUStreamAt(context.Background(), db, now, compactionRunOptions{}); err != nil {
		t.Fatal(err)
	}
	var aggregates []models.GPURecord
	if err := db.Table("gpu_records_long_term").Order("device_index ASC").Find(&aggregates).Error; err != nil {
		t.Fatal(err)
	}
	if len(aggregates) != 2 || aggregates[0].DeviceName != "GPU 0" || aggregates[0].MemUsed != 24 || aggregates[1].MemUsed != 170 {
		t.Fatalf("GPU aggregates=%+v", aggregates)
	}
	var rawCount int64
	if err := db.Model(&models.GPURecord{}).Count(&rawCount).Error; err != nil || rawCount != 0 {
		t.Fatalf("GPU raw rows=%d err=%v", rawCount, err)
	}
}

func TestIncrementalRecordAggregationMatchesLegacySemantics(t *testing.T) {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	newDB := newCompactionTestDB(t)
	legacyDB := newCompactionTestDB(t)
	records := make([]models.Record, 0, 120)
	for index := 0; index < 120; index++ {
		records = append(records, models.Record{
			Client: "semantic-node", Time: models.FromTime(now.Add(-7 * time.Hour).Add(time.Duration(index) * time.Minute)),
			Cpu: float32(index % 97), Gpu: float32(index % 83), Load: float32(index % 71), Temp: float32(index % 67),
			Ram: int64(index * 100), NetIn: int64(index * 10), NetOut: int64(index * 20), Process: index,
		})
	}
	if err := newDB.Create(&records).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacyDB.Create(&records).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := compactRecordStreamAt(context.Background(), newDB, now, compactionRunOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := legacyMigrateOldRecordsAt(legacyDB, now); err != nil {
		t.Fatal(err)
	}
	var got, want []models.Record
	if err := newDB.Table("records_long_term").Order("time ASC").Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	if err := legacyDB.Table("records_long_term").Order("time ASC").Find(&want).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("incremental aggregates differ from legacy\ngot=%+v\nwant=%+v", got, want)
	}
}
