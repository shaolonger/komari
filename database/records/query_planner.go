package records

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm"
)

type QuerySegment struct {
	Tier       RecordTier
	Table      string
	Start      time.Time
	End        time.Time
	Resolution time.Duration
	Aggregate  time.Duration
}

type RecordQuery struct {
	Client    string
	Clients   []string
	Start     time.Time
	End       time.Time
	LoadType  string
	MaxPoints int
}

func PlanRecordQuery(start, end, now time.Time, maxPoints int) ([]QuerySegment, error) {
	if start.IsZero() || end.IsZero() || !end.After(start) {
		return nil, ErrInvalidQueryRange
	}
	if maxPoints <= 0 {
		maxPoints = adminQueryBudget.MaxPoints
	}
	selected := SelectRecordTier(end.Sub(start), maxPoints, false)
	stable15 := floorCompactionBucket(now.Add(-compactionStableAge))
	stableHour := now.Add(-compactionStableAge).Truncate(time.Hour)
	hourlyRetentionBoundary := now.Add(-fifteenMinuteRetention).Truncate(time.Hour)

	var candidates []QuerySegment
	switch selected.Tier {
	case RecordTierHour:
		candidates = []QuerySegment{
			{Tier: RecordTierHour, Table: "records_hourly", Start: start, End: minTime(end, stableHour), Resolution: time.Hour},
			{Tier: RecordTierMinute, Table: "records", Start: maxTime(start, stableHour), End: end, Resolution: time.Minute, Aggregate: time.Hour},
		}
	default:
		aggregateRecent := time.Duration(0)
		if selected.Tier == RecordTierFifteenMinute {
			aggregateRecent = 15 * time.Minute
		}
		candidates = []QuerySegment{
			{Tier: RecordTierHour, Table: "records_hourly", Start: start, End: minTime(end, hourlyRetentionBoundary), Resolution: time.Hour},
			{Tier: RecordTierFifteenMinute, Table: "records_long_term", Start: maxTime(start, hourlyRetentionBoundary), End: minTime(end, stable15), Resolution: 15 * time.Minute},
			{Tier: RecordTierMinute, Table: "records", Start: maxTime(start, stable15), End: end, Resolution: time.Minute, Aggregate: aggregateRecent},
		}
	}
	segments := make([]QuerySegment, 0, len(candidates))
	for _, segment := range candidates {
		if segment.End.After(segment.Start) {
			segments = append(segments, segment)
		}
	}
	return segments, nil
}

func executeRecordQuery(ctx context.Context, db *gorm.DB, query RecordQuery, now time.Time) ([]models.Record, []QuerySegment, error) {
	projection, err := RecordProjection(query.LoadType)
	if err != nil {
		return nil, nil, err
	}
	segments, err := PlanRecordQuery(query.Start, query.End, now, query.MaxPoints)
	if err != nil {
		return nil, nil, err
	}
	result := make([]models.Record, 0)
	for _, segment := range segments {
		var rows []models.Record
		dbQuery := db.WithContext(ctx).Table(segment.Table).Select(projection).
			Where("time >= ? AND time < ?", segment.Start, segment.End)
		if query.Client != "" {
			dbQuery = dbQuery.Where("client = ?", query.Client)
		} else if query.Clients != nil {
			if len(query.Clients) == 0 {
				continue
			}
			dbQuery = dbQuery.Where("client IN ?", query.Clients)
		}
		if err := dbQuery.Order("time ASC, client ASC").Find(&rows).Error; err != nil {
			return nil, segments, fmt.Errorf("query %s segment [%s,%s): %w", segment.Table, segment.Start, segment.End, err)
		}
		if segment.Aggregate > 0 {
			rows = aggregateRecordRows(rows, segment.Aggregate, query.LoadType)
		}
		result = append(result, rows...)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].Time.ToTime().Equal(result[j].Time.ToTime()) {
			return result[i].Time.ToTime().Before(result[j].Time.ToTime())
		}
		return result[i].Client < result[j].Client
	})
	result = deduplicateRecordRows(result)
	return result, segments, nil
}

func aggregateRecordRows(rows []models.Record, resolution time.Duration, loadType string) []models.Record {
	if resolution <= 0 || len(rows) == 0 {
		return rows
	}
	grouped := make(map[recordBucketKey][]models.Record)
	for _, row := range rows {
		slot := row.Time.ToTime().Truncate(resolution)
		key := recordBucketKey{Client: row.Client, Slot: slot}
		grouped[key] = append(grouped[key], row)
	}
	result := make([]models.Record, 0, len(grouped))
	for key, group := range grouped {
		aggregate := aggregateRecordBucket(key, group)
		preserveRequestedPeak(&aggregate, group, loadType)
		result = append(result, aggregate)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].Time.ToTime().Equal(result[j].Time.ToTime()) {
			return result[i].Time.ToTime().Before(result[j].Time.ToTime())
		}
		return result[i].Client < result[j].Client
	})
	return result
}

func preserveRequestedPeak(aggregate *models.Record, group []models.Record, loadType string) {
	if aggregate == nil || len(group) == 0 {
		return
	}
	peak := group[0]
	for _, candidate := range group[1:] {
		if recordMetric(candidate, loadType) > recordMetric(peak, loadType) {
			peak = candidate
		}
	}
	switch loadType {
	case "gpu":
		aggregate.Gpu = peak.Gpu
	case "ram":
		aggregate.Ram, aggregate.RamTotal = peak.Ram, peak.RamTotal
	case "swap":
		aggregate.Swap, aggregate.SwapTotal = peak.Swap, peak.SwapTotal
	case "load":
		aggregate.Load = peak.Load
	case "temp":
		aggregate.Temp = peak.Temp
	case "disk":
		aggregate.Disk, aggregate.DiskTotal = peak.Disk, peak.DiskTotal
	case "network":
		aggregate.NetIn, aggregate.NetOut = peak.NetIn, peak.NetOut
		aggregate.NetTotalUp, aggregate.NetTotalDown = peak.NetTotalUp, peak.NetTotalDown
	case "process":
		aggregate.Process = peak.Process
	case "connections":
		aggregate.Connections, aggregate.ConnectionsUdp = peak.Connections, peak.ConnectionsUdp
	default:
		aggregate.Cpu = peak.Cpu
	}
}

func deduplicateRecordRows(rows []models.Record) []models.Record {
	if len(rows) < 2 {
		return rows
	}
	write := 1
	for read := 1; read < len(rows); read++ {
		previous := rows[write-1]
		current := rows[read]
		if previous.Client == current.Client && previous.Time.ToTime().Equal(current.Time.ToTime()) {
			rows[write-1] = current
			continue
		}
		rows[write] = current
		write++
	}
	return rows[:write]
}

func QueryRecords(ctx context.Context, db *gorm.DB, query RecordQuery) ([]models.Record, error) {
	rows, _, err := executeRecordQuery(ctx, db, query, time.Now())
	return rows, err
}

type GPUQuery struct {
	Client    string
	Start     time.Time
	End       time.Time
	MaxPoints int
}

func executeGPUQuery(ctx context.Context, db *gorm.DB, query GPUQuery, now time.Time) ([]models.GPURecord, []QuerySegment, error) {
	segments, err := PlanRecordQuery(query.Start, query.End, now, query.MaxPoints)
	if err != nil {
		return nil, nil, err
	}
	result := make([]models.GPURecord, 0)
	for _, segment := range segments {
		table := map[RecordTier]string{
			RecordTierMinute: "gpu_records", RecordTierFifteenMinute: "gpu_records_long_term", RecordTierHour: "gpu_records_hourly",
		}[segment.Tier]
		var rows []models.GPURecord
		dbQuery := db.WithContext(ctx).Table(table).Where("time >= ? AND time < ?", segment.Start, segment.End)
		if query.Client != "" {
			dbQuery = dbQuery.Where("client = ?", query.Client)
		}
		if err := dbQuery.Order("time ASC, client ASC, device_index ASC").Find(&rows).Error; err != nil {
			return nil, segments, fmt.Errorf("query %s segment [%s,%s): %w", table, segment.Start, segment.End, err)
		}
		if segment.Aggregate > 0 {
			rows = aggregateGPURows(rows, segment.Aggregate)
		}
		result = append(result, rows...)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].Time.ToTime().Equal(result[j].Time.ToTime()) {
			return result[i].Time.ToTime().Before(result[j].Time.ToTime())
		}
		if result[i].Client != result[j].Client {
			return result[i].Client < result[j].Client
		}
		return result[i].DeviceIndex < result[j].DeviceIndex
	})
	return deduplicateGPURows(result), segments, nil
}

func aggregateGPURows(rows []models.GPURecord, resolution time.Duration) []models.GPURecord {
	grouped := make(map[gpuBucketKey][]models.GPURecord)
	for _, row := range rows {
		key := gpuBucketKey{Client: row.Client, DeviceIndex: row.DeviceIndex, Slot: row.Time.ToTime().Truncate(resolution)}
		grouped[key] = append(grouped[key], row)
	}
	result := make([]models.GPURecord, 0, len(grouped))
	for key, group := range grouped {
		aggregate := aggregateGPUBucket(key, group)
		for _, record := range group {
			if record.Utilization > aggregate.Utilization {
				aggregate.Utilization = record.Utilization
			}
			if record.Temperature > aggregate.Temperature {
				aggregate.Temperature = record.Temperature
			}
		}
		result = append(result, aggregate)
	}
	return result
}

func deduplicateGPURows(rows []models.GPURecord) []models.GPURecord {
	if len(rows) < 2 {
		return rows
	}
	write := 1
	for read := 1; read < len(rows); read++ {
		previous := rows[write-1]
		current := rows[read]
		if previous.Client == current.Client && previous.DeviceIndex == current.DeviceIndex && previous.Time.ToTime().Equal(current.Time.ToTime()) {
			rows[write-1] = current
			continue
		}
		rows[write] = current
		write++
	}
	return rows[:write]
}

func QueryGPURecords(ctx context.Context, db *gorm.DB, query GPUQuery) ([]models.GPURecord, error) {
	rows, _, err := executeGPUQuery(ctx, db, query, time.Now())
	return rows, err
}
