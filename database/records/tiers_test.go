package records

import (
	"context"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm/clause"
)

func TestSelectRecordTierFitsPointBudget(t *testing.T) {
	tests := []struct {
		name        string
		window      time.Duration
		maxPoints   int
		includeLive bool
		want        RecordTier
	}{
		{"live", time.Minute, 100, true, RecordTierRaw},
		{"minute", 2 * time.Hour, 120, false, RecordTierMinute},
		{"fifteen minute", 24 * time.Hour, 100, false, RecordTierFifteenMinute},
		{"hour", 90 * 24 * time.Hour, 2_500, false, RecordTierHour},
		{"invalid budget is bounded", 10 * time.Minute, 0, false, RecordTierFifteenMinute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SelectRecordTier(tt.window, tt.maxPoints, tt.includeLive); got.Tier != tt.want {
				t.Fatalf("SelectRecordTier()=%+v, want %s", got, tt.want)
			}
		})
	}
}

func TestHourlyRollupPreservesTrafficResetsPeaksAndGPUDevices(t *testing.T) {
	db := newCompactionTestDB(t)
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	hour := time.Date(2026, 6, 13, 6, 0, 0, 0, time.UTC)
	raw := make([]models.Record, 0, 60)
	up, down := int64(1_000), int64(2_000)
	for minute := 0; minute < 60; minute++ {
		if minute == 35 {
			up, down = 5, 7
		} else {
			up += int64(10 + minute)
			down += int64(20 + minute)
		}
		raw = append(raw, models.Record{
			Client: "rollup-node", Time: models.FromTime(hour.Add(time.Duration(minute) * time.Minute)),
			Cpu: float32(minute), Load: float32(minute * 2), Temp: float32(30 + minute),
			NetIn: int64(20 + minute), NetOut: int64(10 + minute), NetTotalUp: up, NetTotalDown: down,
		})
	}
	if err := db.Create(&raw).Error; err != nil {
		t.Fatal(err)
	}
	gpus := make([]models.GPURecord, 0, 120)
	for minute := 0; minute < 60; minute++ {
		for device := 0; device < 2; device++ {
			gpus = append(gpus, models.GPURecord{
				Client: "rollup-node", Time: models.FromTime(hour.Add(time.Duration(minute) * time.Minute)),
				DeviceIndex: device, DeviceName: "GPU", MemUsed: int64(minute + device*100), Utilization: float32(minute + device),
			})
		}
	}
	if err := db.Create(&gpus).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := compactRecordStreamAt(context.Background(), db, now, compactionRunOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := compactGPUStreamAt(context.Background(), db, now, compactionRunOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := buildHourlyRollupsAt(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}

	var hourly []models.Record
	if err := db.Table("records_hourly").Where("client = ?", "rollup-node").Find(&hourly).Error; err != nil {
		t.Fatal(err)
	}
	if len(hourly) != 1 {
		t.Fatalf("hourly records=%d, want 1", len(hourly))
	}
	var summary recordRollupSummary
	if err := db.Where("client = ? AND resolution_seconds = ?", "rollup-node", hourResolution).First(&summary).Error; err != nil {
		t.Fatal(err)
	}
	want := SummarizeTrafficRecords(raw, hour, hour.Add(time.Hour), 0)
	if summary.SampleCount != len(raw) || summary.TrafficUp != want.Up || summary.TrafficDown != want.Down || summary.CounterResets != want.Resets {
		t.Fatalf("hour summary=%+v, want traffic=%+v", summary, want)
	}
	if summary.CPUPeak != 59 || summary.LoadPeak != 118 || summary.TemperaturePeak != 89 {
		t.Fatalf("peak summary=%+v", summary)
	}
	var hourlyGPUs []models.GPURecord
	if err := db.Table("gpu_records_hourly").Where("client = ?", "rollup-node").Order("device_index").Find(&hourlyGPUs).Error; err != nil {
		t.Fatal(err)
	}
	if len(hourlyGPUs) != 2 || hourlyGPUs[0].DeviceIndex != 0 || hourlyGPUs[1].DeviceIndex != 1 {
		t.Fatalf("hourly GPUs=%+v", hourlyGPUs)
	}
}

func TestHourlyRollupRebuildsLateOverlap(t *testing.T) {
	db := newCompactionTestDB(t)
	now := time.Date(2026, 6, 13, 12, 7, 0, 0, time.UTC)
	first := models.Record{Client: "late-hour", Time: models.FromTime(time.Date(2026, 6, 13, 7, 31, 0, 0, time.UTC)), Cpu: 10}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := compactRecordStreamAt(context.Background(), db, now, compactionRunOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := buildHourlyRollupsAt(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	late := models.Record{Client: "late-hour", Time: models.FromTime(time.Date(2026, 6, 13, 7, 41, 0, 0, time.UTC)), Cpu: 30}
	if err := db.Create(&late).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := compactRecordStreamAt(context.Background(), db, now, compactionRunOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := buildHourlyRollupsAt(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	var summary recordRollupSummary
	if err := db.Where("client = ? AND resolution_seconds = ?", "late-hour", hourResolution).First(&summary).Error; err != nil {
		t.Fatal(err)
	}
	if summary.SampleCount != 2 || summary.CPUPeak != 30 {
		t.Fatalf("late hourly summary=%+v", summary)
	}
}

func TestTierRetentionWaitsForHourlyWatermark(t *testing.T) {
	db := newCompactionTestDB(t)
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rowTime := now.Add(-10 * 24 * time.Hour).Truncate(time.Hour)
	record := models.Record{Client: "retention-node", Time: models.FromTime(rowTime), Cpu: 10}
	if err := db.Table("records_long_term").Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	summary := summarizeRecordBucket(recordBucketKey{Client: record.Client, Slot: rowTime}, []models.Record{record}, fifteenMinuteResolution)
	if err := db.Create(&summary).Error; err != nil {
		t.Fatal(err)
	}
	state := compactionState{
		Stream: hourlyRecordStream, Watermark: models.FromTime(rowTime.Add(-time.Hour)), UpdatedAt: models.FromTime(now),
	}
	if err := db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyTierRetentionAt(context.Background(), db, now, now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var count, summaryCount int64
	_ = db.Table("records_long_term").Where("client = ?", record.Client).Count(&count).Error
	_ = db.Model(&recordRollupSummary{}).Where("client = ? AND resolution_seconds = ?", record.Client, fifteenMinuteResolution).Count(&summaryCount).Error
	if count != 1 || summaryCount != 1 {
		t.Fatalf("retention deleted unrolled data: rows=%d summaries=%d", count, summaryCount)
	}
	if err := db.Model(&compactionState{}).Where("stream = ?", hourlyRecordStream).
		Updates(map[string]any{"watermark": models.FromTime(rowTime.Add(time.Hour))}).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyTierRetentionAt(context.Background(), db, now, now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	_ = db.Table("records_long_term").Where("client = ?", record.Client).Count(&count).Error
	_ = db.Model(&recordRollupSummary{}).Where("client = ? AND resolution_seconds = ?", record.Client, fifteenMinuteResolution).Count(&summaryCount).Error
	if count != 0 || summaryCount != 0 {
		t.Fatalf("rolled data was not expired: rows=%d summaries=%d", count, summaryCount)
	}
}
