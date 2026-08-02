package records

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	minuteResolution        = 60
	fifteenMinuteResolution = 15 * minuteResolution
	hourResolution          = 60 * minuteResolution
	fifteenMinuteRetention  = 7 * 24 * time.Hour
)

const (
	hourlyRecordStream = "records_1h"
	hourlyGPUStream    = "gpu_records_1h"
)

type RecordTier string

const (
	RecordTierRaw           RecordTier = "raw"
	RecordTierMinute        RecordTier = "1m"
	RecordTierFifteenMinute RecordTier = "15m"
	RecordTierHour          RecordTier = "1h"
)

type TierSelection struct {
	Tier       RecordTier
	Resolution time.Duration
	Table      string
}

// SelectRecordTier selects the finest persisted tier that fits the point
// budget. Raw refers to the in-memory live snapshot; persisted records are the
// server's one-minute accumulator output.
func SelectRecordTier(window time.Duration, maxPoints int, includeLive bool) TierSelection {
	if includeLive && window <= 2*time.Minute {
		return TierSelection{Tier: RecordTierRaw}
	}
	if maxPoints <= 0 {
		maxPoints = 1
	}
	if pointsForWindow(window, time.Minute) <= maxPoints {
		return TierSelection{Tier: RecordTierMinute, Resolution: time.Minute, Table: "records"}
	}
	if pointsForWindow(window, 15*time.Minute) <= maxPoints {
		return TierSelection{Tier: RecordTierFifteenMinute, Resolution: 15 * time.Minute, Table: "records_long_term"}
	}
	return TierSelection{Tier: RecordTierHour, Resolution: time.Hour, Table: "records_hourly"}
}

func pointsForWindow(window, resolution time.Duration) int {
	if window <= 0 {
		return 0
	}
	return int((window + resolution - 1) / resolution)
}

type recordRollupSummary struct {
	Client            string           `gorm:"primaryKey;type:varchar(36)"`
	Time              models.LocalTime `gorm:"primaryKey;not null"`
	ResolutionSeconds int              `gorm:"primaryKey;not null"`
	SampleCount       int              `gorm:"not null"`
	FirstTime         models.LocalTime `gorm:"not null"`
	LastTime          models.LocalTime `gorm:"not null"`
	FirstNetTotalUp   int64
	LastNetTotalUp    int64
	FirstNetTotalDown int64
	LastNetTotalDown  int64
	FirstNetIn        int64
	LastNetIn         int64
	FirstNetOut       int64
	LastNetOut        int64
	TrafficUp         int64
	TrafficDown       int64
	CounterResets     int
	CPUPeak           float32
	GPUPeak           float32
	LoadPeak          float32
	TemperaturePeak   float32
}

func (recordRollupSummary) TableName() string { return "record_rollup_summaries" }

func ensureTierSchema(db *gorm.DB) error {
	statements := []string{
		"CREATE TABLE IF NOT EXISTS records_hourly AS SELECT * FROM records WHERE 0",
		"CREATE TABLE IF NOT EXISTS gpu_records_hourly AS SELECT * FROM gpu_records WHERE 0",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_records_hourly_bucket ON records_hourly(client, time)",
		"CREATE INDEX IF NOT EXISTS idx_records_hourly_time ON records_hourly(time)",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_gpu_records_hourly_bucket ON gpu_records_hourly(client, time, device_index)",
		"CREATE INDEX IF NOT EXISTS idx_gpu_records_hourly_time ON gpu_records_hourly(time)",
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return db.AutoMigrate(&recordRollupSummary{})
}

func summarizeRecordBucket(key recordBucketKey, records []models.Record, resolution int) recordRollupSummary {
	normalized := normalizeTrafficRecords(records)
	summary := recordRollupSummary{
		Client: key.Client, Time: models.FromTime(key.Slot), ResolutionSeconds: resolution, SampleCount: len(normalized),
	}
	if len(normalized) == 0 {
		return summary
	}
	first := normalized[0]
	last := normalized[len(normalized)-1]
	summary.FirstTime = first.Time
	summary.LastTime = last.Time
	summary.FirstNetTotalUp, summary.LastNetTotalUp = first.NetTotalUp, last.NetTotalUp
	summary.FirstNetTotalDown, summary.LastNetTotalDown = first.NetTotalDown, last.NetTotalDown
	summary.FirstNetIn, summary.LastNetIn = first.NetIn, last.NetIn
	summary.FirstNetOut, summary.LastNetOut = first.NetOut, last.NetOut
	traffic := SummarizeTrafficRecords(normalized, key.Slot, key.Slot.Add(time.Duration(resolution)*time.Second), 0)
	summary.TrafficUp = traffic.Up
	summary.TrafficDown = traffic.Down
	summary.CounterResets = traffic.Resets
	for _, record := range normalized {
		summary.CPUPeak = max(summary.CPUPeak, record.Cpu)
		summary.GPUPeak = max(summary.GPUPeak, record.Gpu)
		summary.LoadPeak = max(summary.LoadPeak, record.Load)
		summary.TemperaturePeak = max(summary.TemperaturePeak, record.Temp)
	}
	return summary
}

func upsertRecordRollupSummaries(tx *gorm.DB, summaries []recordRollupSummary) error {
	if len(summaries) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "client"}, {Name: "time"}, {Name: "resolution_seconds"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"sample_count", "first_time", "last_time", "first_net_total_up", "last_net_total_up",
			"first_net_total_down", "last_net_total_down", "first_net_in", "last_net_in", "first_net_out", "last_net_out",
			"traffic_up", "traffic_down", "counter_resets", "cpu_peak", "gpu_peak", "load_peak", "temperature_peak",
		}),
	}).CreateInBatches(&summaries, compactionGroupPageSize).Error
}

func upsertFifteenMinuteRecords(tx *gorm.DB, records []models.Record, summaries []recordRollupSummary) error {
	if len(records) > 0 {
		if err := tx.Table("records_long_term").Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "client"}, {Name: "time"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"cpu", "gpu", "ram", "ram_total", "swap", "swap_total", "load", "temp", "disk", "disk_total",
				"net_in", "net_out", "net_total_up", "net_total_down", "process", "connections", "connections_udp",
			}),
		}).CreateInBatches(&records, compactionGroupPageSize).Error; err != nil {
			return err
		}
	}
	return upsertRecordRollupSummaries(tx, summaries)
}

func combineRollupSummaries(client string, slot time.Time, summaries []recordRollupSummary) recordRollupSummary {
	if len(summaries) == 0 {
		return recordRollupSummary{Client: client, Time: models.FromTime(slot), ResolutionSeconds: hourResolution}
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].FirstTime.ToTime().Before(summaries[j].FirstTime.ToTime()) })
	combined := recordRollupSummary{
		Client: client, Time: models.FromTime(slot), ResolutionSeconds: hourResolution,
		FirstTime: summaries[0].FirstTime, LastTime: summaries[len(summaries)-1].LastTime,
		FirstNetTotalUp: summaries[0].FirstNetTotalUp, LastNetTotalUp: summaries[len(summaries)-1].LastNetTotalUp,
		FirstNetTotalDown: summaries[0].FirstNetTotalDown, LastNetTotalDown: summaries[len(summaries)-1].LastNetTotalDown,
		FirstNetIn: summaries[0].FirstNetIn, LastNetIn: summaries[len(summaries)-1].LastNetIn,
		FirstNetOut: summaries[0].FirstNetOut, LastNetOut: summaries[len(summaries)-1].LastNetOut,
	}
	for index, summary := range summaries {
		combined.SampleCount += summary.SampleCount
		combined.TrafficUp += summary.TrafficUp
		combined.TrafficDown += summary.TrafficDown
		combined.CounterResets += summary.CounterResets
		combined.CPUPeak = max(combined.CPUPeak, summary.CPUPeak)
		combined.GPUPeak = max(combined.GPUPeak, summary.GPUPeak)
		combined.LoadPeak = max(combined.LoadPeak, summary.LoadPeak)
		combined.TemperaturePeak = max(combined.TemperaturePeak, summary.TemperaturePeak)
		if index == 0 {
			continue
		}
		previous := summaries[index-1]
		seconds := summary.FirstTime.ToTime().Sub(previous.LastTime.ToTime()).Seconds()
		if seconds <= 0 {
			continue
		}
		up, upReset, _ := trafficDelta(previous.LastNetTotalUp, summary.FirstNetTotalUp, previous.LastNetOut, summary.FirstNetOut, seconds)
		down, downReset, _ := trafficDelta(previous.LastNetTotalDown, summary.FirstNetTotalDown, previous.LastNetIn, summary.FirstNetIn, seconds)
		combined.TrafficUp += up
		combined.TrafficDown += down
		if upReset || downReset {
			combined.CounterResets++
		}
	}
	return combined
}

func buildHourlyRollupsAt(ctx context.Context, db *gorm.DB, now time.Time) (compactionStats, error) {
	stats := compactionStats{}
	if err := ensureTierSchema(db.WithContext(ctx)); err != nil {
		return stats, err
	}
	recordStats, err := buildHourlyRecordsAt(ctx, db, now)
	if err != nil {
		return stats, err
	}
	gpuStats, err := buildHourlyGPUsAt(ctx, db, now)
	stats.Buckets = recordStats.Buckets + gpuStats.Buckets
	stats.Rows = recordStats.Rows + gpuStats.Rows
	return stats, err
}

func hourlyStart(db *gorm.DB, stream, table string, target time.Time) (time.Time, error) {
	var state compactionState
	err := db.Where("stream = ?", stream).First(&state).Error
	if err == nil {
		return state.Watermark.ToTime().Add(-time.Hour).Truncate(time.Hour), nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return time.Time{}, err
	}
	var oldest models.LocalTime
	if err := db.Table(table).Select("MIN(time)").Where("time < ?", target).Scan(&oldest).Error; err != nil {
		return time.Time{}, err
	}
	if oldest.ToTime().IsZero() {
		return target, nil
	}
	return oldest.ToTime().Truncate(time.Hour), nil
}

func nextHourlyBucket(db *gorm.DB, table string, cursor, target time.Time) (time.Time, bool, error) {
	var next models.LocalTime
	if err := db.Table(table).Select("MIN(time)").Where("time >= ? AND time < ?", cursor, target).Scan(&next).Error; err != nil {
		return time.Time{}, false, err
	}
	if next.ToTime().IsZero() {
		return time.Time{}, false, nil
	}
	return next.ToTime().Truncate(time.Hour), true, nil
}

func buildHourlyRecordsAt(ctx context.Context, db *gorm.DB, now time.Time) (compactionStats, error) {
	stats := compactionStats{}
	target := now.Add(-compactionStableAge).Truncate(time.Hour)
	cursor, err := hourlyStart(db.WithContext(ctx), hourlyRecordStream, "records_long_term", target)
	if err != nil {
		return stats, err
	}
	for cursor.Before(target) {
		slot, found, err := nextHourlyBucket(db.WithContext(ctx), "records_long_term", cursor, target)
		if err != nil {
			return stats, err
		}
		if !found {
			if err := saveCompactionWatermark(db.WithContext(ctx), hourlyRecordStream, target, now); err != nil {
				return stats, err
			}
			break
		}
		end := slot.Add(time.Hour)
		afterClient := ""
		for {
			var clients []string
			query := db.WithContext(ctx).Table("records_long_term").Distinct("client").Where("time >= ? AND time < ?", slot, end)
			if afterClient != "" {
				query = query.Where("client > ?", afterClient)
			}
			if err := query.Order("client").Limit(compactionGroupPageSize).Pluck("client", &clients).Error; err != nil {
				return stats, err
			}
			if len(clients) == 0 {
				break
			}
			var source []models.Record
			if err := db.WithContext(ctx).Table("records_long_term").Where("time >= ? AND time < ? AND client IN ?", slot, end, clients).
				Order("client,time").Limit(compactionMaxRowsPerPage + 1).Find(&source).Error; err != nil {
				return stats, err
			}
			if len(source) > compactionMaxRowsPerPage {
				return stats, fmt.Errorf("hourly record page exceeds %d rows", compactionMaxRowsPerPage)
			}
			byClient := make(map[string][]models.Record, len(clients))
			for _, record := range source {
				byClient[record.Client] = append(byClient[record.Client], record)
			}
			var sourceSummaries []recordRollupSummary
			if err := db.WithContext(ctx).Where("resolution_seconds = ? AND time >= ? AND time < ? AND client IN ?", fifteenMinuteResolution, slot, end, clients).
				Order("client,time").Find(&sourceSummaries).Error; err != nil {
				return stats, err
			}
			summariesByClient := make(map[string][]recordRollupSummary, len(clients))
			for _, summary := range sourceSummaries {
				summariesByClient[summary.Client] = append(summariesByClient[summary.Client], summary)
			}
			hourly := make([]models.Record, 0, len(byClient))
			hourlySummaries := make([]recordRollupSummary, 0, len(byClient))
			for client, records := range byClient {
				key := recordBucketKey{Client: client, Slot: slot}
				hourly = append(hourly, aggregateRecordBucket(key, records))
				parts := summariesByClient[client]
				if len(parts) == 0 {
					parts = append(parts, summarizeRecordBucket(key, records, fifteenMinuteResolution))
				}
				hourlySummaries = append(hourlySummaries, combineRollupSummaries(client, slot, parts))
			}
			if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				if len(hourly) > 0 {
					if err := tx.Table("records_hourly").Clauses(clause.OnConflict{
						Columns: []clause.Column{{Name: "client"}, {Name: "time"}},
						DoUpdates: clause.AssignmentColumns([]string{
							"cpu", "gpu", "ram", "ram_total", "swap", "swap_total", "load", "temp", "disk", "disk_total",
							"net_in", "net_out", "net_total_up", "net_total_down", "process", "connections", "connections_udp",
						}),
					}).CreateInBatches(&hourly, compactionGroupPageSize).Error; err != nil {
						return err
					}
				}
				return upsertRecordRollupSummaries(tx, hourlySummaries)
			}); err != nil {
				return stats, err
			}
			stats.Rows += int64(len(source))
			afterClient = clients[len(clients)-1]
		}
		if err := saveCompactionWatermark(db.WithContext(ctx), hourlyRecordStream, end, now); err != nil {
			return stats, err
		}
		stats.Buckets++
		cursor = end
	}
	return stats, nil
}

func buildHourlyGPUsAt(ctx context.Context, db *gorm.DB, now time.Time) (compactionStats, error) {
	stats := compactionStats{}
	target := now.Add(-compactionStableAge).Truncate(time.Hour)
	cursor, err := hourlyStart(db.WithContext(ctx), hourlyGPUStream, "gpu_records_long_term", target)
	if err != nil {
		return stats, err
	}
	for cursor.Before(target) {
		slot, found, err := nextHourlyBucket(db.WithContext(ctx), "gpu_records_long_term", cursor, target)
		if err != nil {
			return stats, err
		}
		if !found {
			if err := saveCompactionWatermark(db.WithContext(ctx), hourlyGPUStream, target, now); err != nil {
				return stats, err
			}
			break
		}
		end := slot.Add(time.Hour)
		afterClient := ""
		for {
			var clients []string
			query := db.WithContext(ctx).Table("gpu_records_long_term").Distinct("client").Where("time >= ? AND time < ?", slot, end)
			if afterClient != "" {
				query = query.Where("client > ?", afterClient)
			}
			if err := query.Order("client").Limit(compactionGroupPageSize).Pluck("client", &clients).Error; err != nil {
				return stats, err
			}
			if len(clients) == 0 {
				break
			}
			var source []models.GPURecord
			if err := db.WithContext(ctx).Table("gpu_records_long_term").Where("time >= ? AND time < ? AND client IN ?", slot, end, clients).
				Order("client,device_index,time").Limit(compactionMaxRowsPerPage + 1).Find(&source).Error; err != nil {
				return stats, err
			}
			if len(source) > compactionMaxRowsPerPage {
				return stats, fmt.Errorf("hourly GPU page exceeds %d rows", compactionMaxRowsPerPage)
			}
			grouped := make(map[gpuBucketKey][]models.GPURecord)
			for _, record := range source {
				key := gpuBucketKey{Client: record.Client, DeviceIndex: record.DeviceIndex, Slot: slot}
				grouped[key] = append(grouped[key], record)
			}
			hourly := make([]models.GPURecord, 0, len(grouped))
			for key, records := range grouped {
				hourly = append(hourly, aggregateGPUBucket(key, records))
			}
			if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				if len(hourly) == 0 {
					return nil
				}
				return tx.Table("gpu_records_hourly").Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "client"}, {Name: "time"}, {Name: "device_index"}},
					DoUpdates: clause.AssignmentColumns([]string{"device_name", "mem_total", "mem_used", "utilization", "temperature"}),
				}).CreateInBatches(&hourly, compactionGroupPageSize).Error
			}); err != nil {
				return stats, err
			}
			stats.Rows += int64(len(source))
			afterClient = clients[len(clients)-1]
		}
		if err := saveCompactionWatermark(db.WithContext(ctx), hourlyGPUStream, end, now); err != nil {
			return stats, err
		}
		stats.Buckets++
		cursor = end
	}
	return stats, nil
}

func deleteTableBeforeInChunks(ctx context.Context, db *gorm.DB, table string, before time.Time) error {
	allowed := map[string]bool{
		"records": true, "gpu_records": true, "records_long_term": true, "gpu_records_long_term": true,
		"records_hourly": true, "gpu_records_hourly": true,
	}
	if !allowed[table] {
		return fmt.Errorf("unsupported retention table %q", table)
	}
	for {
		result := db.WithContext(ctx).Exec(fmt.Sprintf(
			"DELETE FROM %s WHERE rowid IN (SELECT rowid FROM %s WHERE time < ? LIMIT %d)", table, table, compactionDeleteChunkSize,
		), before)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
	}
}

// ApplyTierRetentionAt enforces independent minute/15-minute/hour retention.
// A 15-minute row is only removed after the hourly high-watermark proves that
// its hour was durably built. The operator's final cutoff always wins because
// data older than it is outside the configured retention contract.
func ApplyTierRetentionAt(ctx context.Context, db *gorm.DB, now, finalCutoff time.Time) error {
	if err := ensureTierSchema(db.WithContext(ctx)); err != nil {
		return err
	}
	rawCutoff := now.Add(-compactionRawRetention)
	if finalCutoff.After(rawCutoff) {
		rawCutoff = finalCutoff
	}
	for _, table := range []string{"records", "gpu_records"} {
		if err := deleteTableBeforeInChunks(ctx, db, table, rawCutoff); err != nil {
			return err
		}
	}
	recordFifteenMinuteCutoff := finalCutoff
	for _, pair := range []struct{ stream, table string }{
		{hourlyRecordStream, "records_long_term"}, {hourlyGPUStream, "gpu_records_long_term"},
	} {
		var state compactionState
		err := db.WithContext(ctx).Where("stream = ?", pair.stream).First(&state).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		tierCutoff := finalCutoff
		if err == nil {
			tierCutoff = now.Add(-fifteenMinuteRetention)
			if state.Watermark.ToTime().Before(tierCutoff) {
				tierCutoff = state.Watermark.ToTime()
			}
		}
		if finalCutoff.After(tierCutoff) {
			tierCutoff = finalCutoff
		}
		if pair.stream == hourlyRecordStream {
			recordFifteenMinuteCutoff = tierCutoff
		}
		if err := deleteTableBeforeInChunks(ctx, db, pair.table, tierCutoff); err != nil {
			return err
		}
	}
	for _, table := range []string{"records_hourly", "gpu_records_hourly"} {
		if err := deleteTableBeforeInChunks(ctx, db, table, finalCutoff); err != nil {
			return err
		}
	}
	if err := db.WithContext(ctx).Where("resolution_seconds = ? AND time < ?", fifteenMinuteResolution, recordFifteenMinuteCutoff).
		Delete(&recordRollupSummary{}).Error; err != nil {
		return err
	}
	return db.WithContext(ctx).Where("resolution_seconds = ? AND time < ?", hourResolution, finalCutoff).
		Delete(&recordRollupSummary{}).Error
}
