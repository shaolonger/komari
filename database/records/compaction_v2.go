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
	compactionBucket             = 15 * time.Minute
	compactionStableAge          = 4 * time.Hour
	compactionRawRetention       = 5 * time.Hour
	compactionLateArrivalOverlap = time.Hour
	compactionGroupPageSize      = 64
	compactionDeleteChunkSize    = 5_000
	compactionMaxRowsPerPage     = 100_000
	compactionWindow             = 24 * time.Hour
)

const (
	recordStream = "records_15m"
	gpuStream    = "gpu_records_15m"
)

type compactionState struct {
	Stream    string           `gorm:"primaryKey;type:text"`
	Watermark models.LocalTime `gorm:"not null"`
	UpdatedAt models.LocalTime `gorm:"not null"`
}

func (compactionState) TableName() string { return "telemetry_compaction_state" }

type pendingCompaction struct {
	Stream    string           `gorm:"primaryKey;type:text"`
	Bucket    models.LocalTime `gorm:"primaryKey;not null"`
	BucketEnd models.LocalTime `gorm:"not null"`
	MaxRowID  int64            `gorm:"not null"`
}

func (pendingCompaction) TableName() string { return "telemetry_compaction_pending" }

type compactionRunOptions struct {
	Failpoint func(stage, stream string, bucket time.Time) error
}

type compactionStats struct {
	Buckets int
	Rows    int64
}

type recordBucketKey struct {
	Client string
	Slot   time.Time
}

type gpuBucketKey struct {
	Client      string
	DeviceIndex int
	Slot        time.Time
}

func ensureCompactionSchema(db *gorm.DB, stream string) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS telemetry_compaction_state (
			stream TEXT PRIMARY KEY NOT NULL,
			watermark DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS telemetry_compaction_pending (
			stream TEXT NOT NULL,
			bucket DATETIME NOT NULL,
			bucket_end DATETIME NOT NULL,
			max_row_id INTEGER NOT NULL,
			PRIMARY KEY(stream, bucket)
		)`,
	}
	switch stream {
	case recordStream:
		statements = append(statements, "CREATE UNIQUE INDEX IF NOT EXISTS idx_record_lt_bucket ON records_long_term(client, time)")
	case gpuStream:
		statements = append(statements, "CREATE UNIQUE INDEX IF NOT EXISTS idx_gpu_record_lt_bucket ON gpu_records_long_term(client, time, device_index)")
	default:
		return fmt.Errorf("unknown compaction stream %q", stream)
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

func runCompactionFailpoint(options compactionRunOptions, stage, stream string, bucket time.Time) error {
	if options.Failpoint == nil {
		return nil
	}
	return options.Failpoint(stage, stream, bucket)
}

func floorCompactionBucket(value time.Time) time.Time {
	return value.Truncate(compactionBucket)
}

func compactionStart(db *gorm.DB, stream, table string, target time.Time) (time.Time, error) {
	var state compactionState
	err := db.Where("stream = ?", stream).First(&state).Error
	if err == nil {
		start := floorCompactionBucket(state.Watermark.ToTime().Add(-compactionLateArrivalOverlap))
		if start.After(target) {
			return target, nil
		}
		return start, nil
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
	return floorCompactionBucket(oldest.ToTime()), nil
}

func nextCompactionBucket(db *gorm.DB, table string, cursor, target time.Time) (time.Time, bool, error) {
	var next models.LocalTime
	if err := db.Table(table).Select("MIN(time)").Where("time >= ? AND time < ?", cursor, target).Scan(&next).Error; err != nil {
		return time.Time{}, false, err
	}
	if next.ToTime().IsZero() {
		return time.Time{}, false, nil
	}
	return floorCompactionBucket(next.ToTime()), true, nil
}

func saveCompactionWatermark(tx *gorm.DB, stream string, watermark, now time.Time) error {
	state := compactionState{
		Stream:    stream,
		Watermark: models.FromTime(watermark),
		UpdatedAt: models.FromTime(now),
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "stream"}},
		DoUpdates: clause.Assignments(map[string]any{
			"watermark":  gorm.Expr("MAX(telemetry_compaction_state.watermark, excluded.watermark)"),
			"updated_at": state.UpdatedAt,
		}),
	}).Create(&state).Error
}

func findPendingCompaction(db *gorm.DB, stream string) (pendingCompaction, bool, error) {
	var pending pendingCompaction
	err := db.Where("stream = ?", stream).Order("bucket ASC").First(&pending).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return pendingCompaction{}, false, nil
	}
	return pending, err == nil, err
}

func maxBucketRowID(db *gorm.DB, table string, start, end time.Time) (int64, error) {
	var maxRowID int64
	err := db.Table(table).Select("COALESCE(MAX(rowid), 0)").Where("time >= ? AND time < ?", start, end).Scan(&maxRowID).Error
	return maxRowID, err
}

func markPendingCompaction(tx *gorm.DB, stream string, bucket, bucketEnd time.Time, maxRowID int64) error {
	pending := pendingCompaction{
		Stream: stream, Bucket: models.FromTime(bucket), BucketEnd: models.FromTime(bucketEnd), MaxRowID: maxRowID,
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "stream"}, {Name: "bucket"}},
		DoUpdates: clause.AssignmentColumns([]string{"bucket_end", "max_row_id"}),
	}).Create(&pending).Error
}

func finishPendingCompaction(ctx context.Context, db *gorm.DB, table string, pending pendingCompaction, now time.Time, options compactionRunOptions) error {
	bucket := pending.Bucket.ToTime()
	end := pending.BucketEnd.ToTime()
	if !end.After(bucket) {
		return fmt.Errorf("invalid pending %s range [%s,%s)", pending.Stream, bucket, end)
	}
	for {
		var deleted int64
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			result := tx.Exec(fmt.Sprintf(
				"DELETE FROM %s WHERE rowid IN (SELECT rowid FROM %s WHERE time >= ? AND time < ? AND rowid <= ? LIMIT %d)",
				table, table, compactionDeleteChunkSize,
			), bucket, end, pending.MaxRowID)
			deleted = result.RowsAffected
			return result.Error
		})
		if err != nil {
			return err
		}
		if deleted == 0 {
			break
		}
		if err := runCompactionFailpoint(options, "after_delete_chunk", pending.Stream, bucket); err != nil {
			return err
		}
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("stream = ? AND bucket = ?", pending.Stream, pending.Bucket).Delete(&pendingCompaction{}).Error; err != nil {
			return err
		}
		return saveCompactionWatermark(tx, pending.Stream, end, now)
	})
}

func resumePendingCompactions(ctx context.Context, db *gorm.DB, stream, table string, now time.Time, options compactionRunOptions) error {
	for {
		pending, found, err := findPendingCompaction(db.WithContext(ctx), stream)
		if err != nil || !found {
			return err
		}
		if err := finishPendingCompaction(ctx, db, table, pending, now, options); err != nil {
			return err
		}
	}
}

func deleteOrAdvanceRange(ctx context.Context, db *gorm.DB, stream, table string, bucket, end, deleteBefore, now time.Time, maxRowID int64, options compactionRunOptions) error {
	if !end.After(deleteBefore) {
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return markPendingCompaction(tx, stream, bucket, end, maxRowID)
		}); err != nil {
			return err
		}
		if err := runCompactionFailpoint(options, "after_aggregate", stream, bucket); err != nil {
			return err
		}
		return finishPendingCompaction(ctx, db, table, pendingCompaction{
			Stream: stream, Bucket: models.FromTime(bucket), BucketEnd: models.FromTime(end), MaxRowID: maxRowID,
		}, now, options)
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return saveCompactionWatermark(tx, stream, end, now)
	})
}

func compactRecordStreamAt(ctx context.Context, db *gorm.DB, now time.Time, options compactionRunOptions) (compactionStats, error) {
	stats := compactionStats{}
	if err := ensureCompactionSchema(db.WithContext(ctx), recordStream); err != nil {
		return stats, err
	}
	if err := ensureTierSchema(db.WithContext(ctx)); err != nil {
		return stats, err
	}
	if err := resumePendingCompactions(ctx, db, recordStream, "records", now, options); err != nil {
		return stats, err
	}
	target := floorCompactionBucket(now.Add(-compactionStableAge))
	deleteBefore := floorCompactionBucket(now.Add(-compactionRawRetention))
	cursor, err := compactionStart(db.WithContext(ctx), recordStream, "records", target)
	if err != nil {
		return stats, err
	}
	for cursor.Before(target) {
		bucket, found, err := nextCompactionBucket(db.WithContext(ctx), "records", cursor, target)
		if err != nil {
			return stats, err
		}
		if !found {
			if err := saveCompactionWatermark(db.WithContext(ctx), recordStream, target, now); err != nil {
				return stats, err
			}
			break
		}
		windowEnd := bucket.Add(compactionWindow)
		if windowEnd.After(target) {
			windowEnd = target
		}
		if bucket.Before(deleteBefore) && windowEnd.After(deleteBefore) {
			windowEnd = deleteBefore
		}
		maxRowID, err := maxBucketRowID(db.WithContext(ctx), "records", bucket, windowEnd)
		if err != nil {
			return stats, err
		}
		rows, buckets, dense, err := compactRecordWindow(ctx, db, bucket, windowEnd, maxRowID, options)
		if err != nil {
			return stats, err
		}
		if dense {
			windowEnd = bucket.Add(compactionBucket)
			maxRowID, err = maxBucketRowID(db.WithContext(ctx), "records", bucket, windowEnd)
			if err != nil {
				return stats, err
			}
			rows, err = compactRecordBucket(ctx, db, bucket, maxRowID, options)
			if err != nil {
				return stats, err
			}
			buckets = 1
		}
		stats.Buckets += buckets
		stats.Rows += rows
		if err := deleteOrAdvanceRange(ctx, db, recordStream, "records", bucket, windowEnd, deleteBefore, now, maxRowID, options); err != nil {
			return stats, err
		}
		cursor = windowEnd
	}
	return stats, nil
}

func compactRecordWindow(ctx context.Context, db *gorm.DB, start, end time.Time, maxRowID int64, options compactionRunOptions) (int64, int, bool, error) {
	var raw []models.Record
	if err := db.WithContext(ctx).Table("records").Where(
		"time >= ? AND time < ? AND rowid <= ?", start, end, maxRowID,
	).Order("time ASC, client ASC").Limit(compactionMaxRowsPerPage + 1).Find(&raw).Error; err != nil {
		return 0, 0, false, err
	}
	if len(raw) > compactionMaxRowsPerPage {
		return 0, 0, true, nil
	}
	grouped := make(map[recordBucketKey][]models.Record)
	buckets := make(map[time.Time]struct{})
	for _, record := range raw {
		slot := floorCompactionBucket(record.Time.ToTime())
		key := recordBucketKey{Client: record.Client, Slot: slot}
		grouped[key] = append(grouped[key], record)
		buckets[slot] = struct{}{}
	}
	aggregates := make([]models.Record, 0, len(grouped))
	summaryByKey := make(map[recordBucketKey]recordRollupSummary, len(grouped))
	for key, records := range grouped {
		aggregates = append(aggregates, aggregateRecordBucket(key, records))
		summaryByKey[key] = summarizeRecordBucket(key, records, fifteenMinuteResolution)
	}
	sort.Slice(aggregates, func(i, j int) bool {
		if !aggregates[i].Time.ToTime().Equal(aggregates[j].Time.ToTime()) {
			return aggregates[i].Time.ToTime().Before(aggregates[j].Time.ToTime())
		}
		return aggregates[i].Client < aggregates[j].Client
	})
	for offset := 0; offset < len(aggregates); offset += compactionGroupPageSize {
		limit := min(offset+compactionGroupPageSize, len(aggregates))
		page := aggregates[offset:limit]
		summaryPage := make([]recordRollupSummary, 0, len(page))
		for _, record := range page {
			key := recordBucketKey{Client: record.Client, Slot: record.Time.ToTime()}
			summaryPage = append(summaryPage, summaryByKey[key])
		}
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return upsertFifteenMinuteRecords(tx, page, summaryPage)
		}); err != nil {
			return 0, 0, false, err
		}
		if err := runCompactionFailpoint(options, "after_upsert_page", recordStream, start); err != nil {
			return 0, 0, false, err
		}
	}
	return int64(len(raw)), len(buckets), false, nil
}

func compactRecordBucket(ctx context.Context, db *gorm.DB, bucket time.Time, maxRowID int64, options compactionRunOptions) (int64, error) {
	end := bucket.Add(compactionBucket)
	afterClient := ""
	var totalRows int64
	for {
		var clients []string
		query := db.WithContext(ctx).Table("records").Distinct("client").
			Where("time >= ? AND time < ? AND rowid <= ?", bucket, end, maxRowID)
		if afterClient != "" {
			query = query.Where("client > ?", afterClient)
		}
		if err := query.Order("client ASC").Limit(compactionGroupPageSize).Pluck("client", &clients).Error; err != nil {
			return totalRows, err
		}
		if len(clients) == 0 {
			return totalRows, nil
		}
		var raw []models.Record
		if err := db.WithContext(ctx).Table("records").Where(
			"time >= ? AND time < ? AND rowid <= ? AND client IN ?", bucket, end, maxRowID, clients,
		).Order("client ASC, time ASC").Limit(compactionMaxRowsPerPage + 1).Find(&raw).Error; err != nil {
			return totalRows, err
		}
		if len(raw) > compactionMaxRowsPerPage {
			return totalRows, fmt.Errorf("record compaction page exceeds %d rows", compactionMaxRowsPerPage)
		}
		grouped := make(map[recordBucketKey][]models.Record, len(clients))
		for _, record := range raw {
			key := recordBucketKey{Client: record.Client, Slot: bucket}
			grouped[key] = append(grouped[key], record)
		}
		aggregates := make([]models.Record, 0, len(grouped))
		summaryByClient := make(map[string]recordRollupSummary, len(grouped))
		for key, records := range grouped {
			aggregates = append(aggregates, aggregateRecordBucket(key, records))
			summaryByClient[key.Client] = summarizeRecordBucket(key, records, fifteenMinuteResolution)
		}
		sort.Slice(aggregates, func(i, j int) bool { return aggregates[i].Client < aggregates[j].Client })
		summaries := make([]recordRollupSummary, 0, len(aggregates))
		for _, record := range aggregates {
			summaries = append(summaries, summaryByClient[record.Client])
		}
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return upsertFifteenMinuteRecords(tx, aggregates, summaries)
		}); err != nil {
			return totalRows, err
		}
		totalRows += int64(len(raw))
		if err := runCompactionFailpoint(options, "after_upsert_page", recordStream, bucket); err != nil {
			return totalRows, err
		}
		afterClient = clients[len(clients)-1]
	}
}

func compactGPUStreamAt(ctx context.Context, db *gorm.DB, now time.Time, options compactionRunOptions) (compactionStats, error) {
	stats := compactionStats{}
	if err := ensureCompactionSchema(db.WithContext(ctx), gpuStream); err != nil {
		return stats, err
	}
	if err := resumePendingCompactions(ctx, db, gpuStream, "gpu_records", now, options); err != nil {
		return stats, err
	}
	target := floorCompactionBucket(now.Add(-compactionStableAge))
	deleteBefore := floorCompactionBucket(now.Add(-compactionRawRetention))
	cursor, err := compactionStart(db.WithContext(ctx), gpuStream, "gpu_records", target)
	if err != nil {
		return stats, err
	}
	for cursor.Before(target) {
		bucket, found, err := nextCompactionBucket(db.WithContext(ctx), "gpu_records", cursor, target)
		if err != nil {
			return stats, err
		}
		if !found {
			if err := saveCompactionWatermark(db.WithContext(ctx), gpuStream, target, now); err != nil {
				return stats, err
			}
			break
		}
		maxRowID, err := maxBucketRowID(db.WithContext(ctx), "gpu_records", bucket, bucket.Add(compactionBucket))
		if err != nil {
			return stats, err
		}
		rows, err := compactGPUBucket(ctx, db, bucket, maxRowID, options)
		if err != nil {
			return stats, err
		}
		stats.Buckets++
		stats.Rows += rows
		if err := deleteOrAdvanceRange(ctx, db, gpuStream, "gpu_records", bucket, bucket.Add(compactionBucket), deleteBefore, now, maxRowID, options); err != nil {
			return stats, err
		}
		cursor = bucket.Add(compactionBucket)
	}
	return stats, nil
}

type gpuPageKey struct {
	Client      string
	DeviceIndex int
}

func compactGPUBucket(ctx context.Context, db *gorm.DB, bucket time.Time, maxRowID int64, options compactionRunOptions) (int64, error) {
	end := bucket.Add(compactionBucket)
	after := gpuPageKey{DeviceIndex: -1}
	var totalRows int64
	for {
		var keys []gpuPageKey
		query := db.WithContext(ctx).Table("gpu_records").Select("client, device_index").
			Where("time >= ? AND time < ? AND rowid <= ?", bucket, end, maxRowID)
		if after.Client != "" {
			query = query.Where("client > ? OR (client = ? AND device_index > ?)", after.Client, after.Client, after.DeviceIndex)
		}
		if err := query.Group("client, device_index").Order("client ASC, device_index ASC").Limit(compactionGroupPageSize).Scan(&keys).Error; err != nil {
			return totalRows, err
		}
		if len(keys) == 0 {
			return totalRows, nil
		}
		clients := make([]string, 0, len(keys))
		for _, key := range keys {
			clients = append(clients, key.Client)
		}
		var raw []models.GPURecord
		if err := db.WithContext(ctx).Table("gpu_records").Where(
			"time >= ? AND time < ? AND rowid <= ? AND client IN ?", bucket, end, maxRowID, clients,
		).Order("client ASC, device_index ASC, time ASC").Limit(compactionMaxRowsPerPage + 1).Find(&raw).Error; err != nil {
			return totalRows, err
		}
		if len(raw) > compactionMaxRowsPerPage {
			return totalRows, fmt.Errorf("GPU compaction page exceeds %d rows", compactionMaxRowsPerPage)
		}
		allowed := make(map[gpuPageKey]struct{}, len(keys))
		for _, key := range keys {
			allowed[key] = struct{}{}
		}
		grouped := make(map[gpuBucketKey][]models.GPURecord, len(keys))
		for _, record := range raw {
			pageKey := gpuPageKey{Client: record.Client, DeviceIndex: record.DeviceIndex}
			if _, ok := allowed[pageKey]; !ok {
				continue
			}
			key := gpuBucketKey{Client: record.Client, DeviceIndex: record.DeviceIndex, Slot: bucket}
			grouped[key] = append(grouped[key], record)
		}
		aggregates := make([]models.GPURecord, 0, len(grouped))
		for key, records := range grouped {
			aggregates = append(aggregates, aggregateGPUBucket(key, records))
		}
		sort.Slice(aggregates, func(i, j int) bool {
			if aggregates[i].Client != aggregates[j].Client {
				return aggregates[i].Client < aggregates[j].Client
			}
			return aggregates[i].DeviceIndex < aggregates[j].DeviceIndex
		})
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if len(aggregates) == 0 {
				return nil
			}
			return tx.Table("gpu_records_long_term").Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "client"}, {Name: "time"}, {Name: "device_index"}},
				DoUpdates: clause.AssignmentColumns([]string{"device_name", "mem_total", "mem_used", "utilization", "temperature"}),
			}).CreateInBatches(&aggregates, compactionGroupPageSize).Error
		}); err != nil {
			return totalRows, err
		}
		totalRows += int64(len(raw))
		if err := runCompactionFailpoint(options, "after_upsert_page", gpuStream, bucket); err != nil {
			return totalRows, err
		}
		after = keys[len(keys)-1]
	}
}

func aggregateRecordBucket(key recordBucketKey, records []models.Record) models.Record {
	group := recordGroupData{}
	for _, record := range records {
		group.add(record)
	}
	return group.aggregate(key)
}

type recordGroupData struct {
	cpu, gpu, load, temp                            []float32
	ram, ramTotal, swap, swapTotal, disk, diskTotal []int64
	netIn, netOut, netTotalUp, netTotalDown         []int64
	process, connections, connectionsUDP            []int
}

func (group *recordGroupData) add(record models.Record) {
	group.cpu = append(group.cpu, record.Cpu)
	group.gpu = append(group.gpu, record.Gpu)
	group.load = append(group.load, record.Load)
	group.temp = append(group.temp, record.Temp)
	group.ram = append(group.ram, record.Ram)
	group.ramTotal = append(group.ramTotal, record.RamTotal)
	group.swap = append(group.swap, record.Swap)
	group.swapTotal = append(group.swapTotal, record.SwapTotal)
	group.disk = append(group.disk, record.Disk)
	group.diskTotal = append(group.diskTotal, record.DiskTotal)
	group.netIn = append(group.netIn, record.NetIn)
	group.netOut = append(group.netOut, record.NetOut)
	group.netTotalUp = append(group.netTotalUp, record.NetTotalUp)
	group.netTotalDown = append(group.netTotalDown, record.NetTotalDown)
	group.process = append(group.process, record.Process)
	group.connections = append(group.connections, record.Connections)
	group.connectionsUDP = append(group.connectionsUDP, record.ConnectionsUdp)
}

func (group *recordGroupData) aggregate(key recordBucketKey) models.Record {
	const high = 0.7
	return models.Record{
		Client: key.Client, Time: models.FromTime(key.Slot),
		Cpu: percentileFloat32(group.cpu, high), Gpu: percentileFloat32(group.gpu, high),
		Load: percentileFloat32(group.load, high), Temp: percentileFloat32(group.temp, high),
		Ram: percentileInt64(group.ram, high), RamTotal: percentileInt64(group.ramTotal, high),
		Swap: percentileInt64(group.swap, high), SwapTotal: percentileInt64(group.swapTotal, high),
		Disk: percentileInt64(group.disk, high), DiskTotal: percentileInt64(group.diskTotal, high),
		NetIn: percentileInt64(group.netIn, 0.2), NetOut: percentileInt64(group.netOut, 0.2),
		NetTotalUp: percentileInt64(group.netTotalUp, high), NetTotalDown: percentileInt64(group.netTotalDown, high),
		Process: percentileInt(group.process, high), Connections: percentileInt(group.connections, high),
		ConnectionsUdp: percentileInt(group.connectionsUDP, high),
	}
}

func aggregateGPUBucket(key gpuBucketKey, records []models.GPURecord) models.GPURecord {
	const high = 0.7
	memTotal := make([]int64, 0, len(records))
	memUsed := make([]int64, 0, len(records))
	utilization := make([]float32, 0, len(records))
	temperature := make([]int, 0, len(records))
	deviceName := ""
	for _, record := range records {
		memTotal = append(memTotal, record.MemTotal)
		memUsed = append(memUsed, record.MemUsed)
		utilization = append(utilization, record.Utilization)
		temperature = append(temperature, record.Temperature)
		if record.DeviceName != "" {
			deviceName = record.DeviceName
		}
	}
	return models.GPURecord{
		Client: key.Client, DeviceIndex: key.DeviceIndex, Time: models.FromTime(key.Slot), DeviceName: deviceName,
		MemTotal: percentileInt64(memTotal, high), MemUsed: percentileInt64(memUsed, high),
		Utilization: percentileFloat32(utilization, high), Temperature: percentileInt(temperature, high),
	}
}

func percentileFloat32(values []float32, percentile float64) float32 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float32(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	position := float64(len(sorted)-1) * percentile
	lower := int(position)
	if lower == len(sorted)-1 {
		return sorted[lower]
	}
	return sorted[lower] + float32(position-float64(lower))*(sorted[lower+1]-sorted[lower])
}

func percentileInt64(values []int64, percentile float64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	position := float64(len(sorted)-1) * percentile
	lower := int(position)
	if lower == len(sorted)-1 {
		return sorted[lower]
	}
	return int64(float64(sorted[lower]) + (position-float64(lower))*float64(sorted[lower+1]-sorted[lower]))
}

func percentileInt(values []int, percentile float64) int {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	position := float64(len(sorted)-1) * percentile
	lower := int(position)
	if lower == len(sorted)-1 {
		return sorted[lower]
	}
	return int(float64(sorted[lower]) + (position-float64(lower))*float64(sorted[lower+1]-sorted[lower]))
}
