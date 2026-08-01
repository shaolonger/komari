// Package telemetrywriter serializes bounded telemetry batches into SQLite.
package telemetrywriter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/historycache"
	"github.com/komari-monitor/komari/internal/observability"
	"github.com/mattn/go-sqlite3"
)

const (
	DefaultQueueCapacity = 64
	DefaultMaxRetries    = 4
	DefaultChunkRows     = 256
	DefaultMaxBatchRows  = 1024
	DefaultMaxBatchDelay = 2 * time.Millisecond
	MaxQueueCapacity     = 1024
	MaxRowsPerBatch      = 100_000
)

var ErrClosed = errors.New("telemetry writer is closed")

type Batch struct {
	ID          string
	Records     []models.Record
	GPURecords  []models.GPURecord
	PingRecords []models.PingRecord
}

func (b Batch) Rows() int { return len(b.Records) + len(b.GPURecords) + len(b.PingRecords) }

type Config struct {
	QueueCapacity int
	MaxRetries    int
	RetryBackoff  time.Duration
	ChunkRows     int
	MaxBatchRows  int
	MaxBatchDelay time.Duration
	beforeAttempt func(attempt int) error
}

type request struct {
	ctx      context.Context
	batch    Batch
	result   chan error
	shutdown bool
}

type Writer struct {
	db         *sql.DB
	config     Config
	queue      chan request
	runCtx     context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	acceptMu   sync.RWMutex
	closeMu    sync.Mutex
	closed     bool
	statements map[statementKey]*sql.Stmt
}

type statementKey struct {
	kind string
	rows int
}

func New(db *sql.DB, config Config) (*Writer, error) {
	if db == nil {
		return nil, errors.New("telemetry writer database is required")
	}
	if config.QueueCapacity <= 0 {
		config.QueueCapacity = DefaultQueueCapacity
	}
	if config.QueueCapacity > MaxQueueCapacity {
		return nil, fmt.Errorf("queue capacity cannot exceed %d", MaxQueueCapacity)
	}
	if config.MaxRetries < 0 {
		return nil, errors.New("max retries cannot be negative")
	}
	if config.MaxRetries == 0 {
		config.MaxRetries = DefaultMaxRetries
	}
	if config.MaxRetries > 10 {
		return nil, errors.New("max retries cannot exceed 10")
	}
	if config.RetryBackoff <= 0 {
		config.RetryBackoff = 10 * time.Millisecond
	}
	if config.ChunkRows <= 0 {
		config.ChunkRows = DefaultChunkRows
	}
	if config.ChunkRows > DefaultChunkRows {
		return nil, fmt.Errorf("chunk rows cannot exceed %d", DefaultChunkRows)
	}
	if config.MaxBatchRows <= 0 {
		config.MaxBatchRows = DefaultMaxBatchRows
	}
	if config.MaxBatchRows > MaxRowsPerBatch {
		return nil, fmt.Errorf("maximum coalesced batch rows cannot exceed %d", MaxRowsPerBatch)
	}
	if config.MaxBatchDelay <= 0 {
		config.MaxBatchDelay = DefaultMaxBatchDelay
	}
	if config.MaxBatchDelay > time.Second {
		return nil, errors.New("maximum coalescing delay cannot exceed one second")
	}
	runCtx, cancel := context.WithCancel(context.Background())
	w := &Writer{
		db: db, config: config, queue: make(chan request, config.QueueCapacity),
		runCtx: runCtx, cancel: cancel, done: make(chan struct{}),
		statements: make(map[statementKey]*sql.Stmt),
	}
	go w.run()
	return w, nil
}

// Submit transfers an immutable copy of batch to the bounded queue and waits
// for durable commit or an explicit error.
func (w *Writer) Submit(ctx context.Context, batch Batch) error {
	if batch.Rows() == 0 {
		return nil
	}
	if batch.Rows() > MaxRowsPerBatch {
		return fmt.Errorf("telemetry batch has %d rows, maximum is %d", batch.Rows(), MaxRowsPerBatch)
	}
	owned := Batch{
		ID:          batch.ID,
		Records:     append([]models.Record(nil), batch.Records...),
		GPURecords:  append([]models.GPURecord(nil), batch.GPURecords...),
		PingRecords: append([]models.PingRecord(nil), batch.PingRecords...),
	}
	req := request{ctx: ctx, batch: owned, result: make(chan error, 1)}
	w.acceptMu.RLock()
	if w.closed {
		w.acceptMu.RUnlock()
		return ErrClosed
	}
	select {
	case w.queue <- req:
		observability.SetFlushQueueDepth(len(w.queue))
		w.acceptMu.RUnlock()
	case <-ctx.Done():
		w.acceptMu.RUnlock()
		return ctx.Err()
	}
	select {
	case err := <-req.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Writer) run() {
	defer close(w.done)
	defer w.closeStatements()
	var pending *request
	for {
		if w.runCtx.Err() != nil {
			w.failQueued(w.runCtx.Err())
			return
		}
		var first request
		if pending != nil {
			first = *pending
			pending = nil
		} else {
			select {
			case <-w.runCtx.Done():
				w.failQueued(w.runCtx.Err())
				return
			case first = <-w.queue:
			}
		}
		observability.SetFlushQueueDepth(len(w.queue))
		if first.shutdown {
			return
		}

		requests, merged, shutdown := w.coalesce(first, &pending)
		err, retries := w.process(w.runCtx, merged)
		if err == nil {
			observability.ObserveBatch(merged.Rows(), retries)
		}
		for _, req := range requests {
			req.result <- err
		}
		if shutdown {
			return
		}
	}
}

// coalesce drains a short bounded window into one transaction. Every caller
// still receives the durable result, while a synchronized fleet burst pays for
// one SQLite fsync instead of one fsync per Ping sample.
func (w *Writer) coalesce(first request, pending **request) ([]request, Batch, bool) {
	requests := make([]request, 0, min(len(w.queue)+1, w.config.MaxBatchRows))
	merged := Batch{}
	appendRequest := func(req request) {
		requests = append(requests, req)
		merged.Records = append(merged.Records, req.batch.Records...)
		merged.GPURecords = append(merged.GPURecords, req.batch.GPURecords...)
		merged.PingRecords = append(merged.PingRecords, req.batch.PingRecords...)
	}
	appendRequest(first)
	timer := time.NewTimer(w.config.MaxBatchDelay)
	defer timer.Stop()
	for merged.Rows() < w.config.MaxBatchRows {
		select {
		case <-w.runCtx.Done():
			return requests, merged, true
		case <-timer.C:
			return requests, merged, false
		case next := <-w.queue:
			observability.SetFlushQueueDepth(len(w.queue))
			if next.shutdown {
				return requests, merged, true
			}
			if next.batch.Rows()+merged.Rows() > w.config.MaxBatchRows {
				copy := next
				*pending = &copy
				return requests, merged, false
			}
			appendRequest(next)
		}
	}
	return requests, merged, false
}

func (w *Writer) process(parent context.Context, batch Batch) (error, int) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(w.runCtx, cancel)
	defer func() {
		stop()
		cancel()
	}()
	for attempt := 0; ; attempt++ {
		var err error
		if w.config.beforeAttempt != nil {
			err = w.config.beforeAttempt(attempt)
		}
		if err == nil {
			err = w.writeBatch(ctx, batch)
		}
		if err == nil {
			return nil, attempt
		}
		if !isTransientSQLite(err) || attempt >= w.config.MaxRetries {
			return err, attempt
		}
		delay := w.config.RetryBackoff << attempt
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err(), attempt
		case <-timer.C:
		}
	}
}

func (w *Writer) writeBatch(ctx context.Context, batch Batch) error {
	pingRollups := aggregatePingRollups(batch.PingRecords)
	if err := w.prepareBatch(ctx, batch, len(pingRollups)); err != nil {
		return err
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := w.writeRecords(ctx, tx, batch.Records); err != nil {
		return err
	}
	if err := w.writeGPURecords(ctx, tx, batch.GPURecords); err != nil {
		return err
	}
	if err := w.writePingRecords(ctx, tx, batch.PingRecords); err != nil {
		return err
	}
	if err := w.writePingRollups(ctx, tx, pingRollups); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	historycache.Invalidate()
	return nil
}

func (w *Writer) prepareBatch(ctx context.Context, batch Batch, pingRollupRows int) error {
	for _, spec := range []struct {
		kind    string
		rows    int
		columns int
		names   string
	}{
		{"records", len(batch.Records), 19, "client,time,cpu,gpu,ram,ram_total,swap,swap_total,load,temp,disk,disk_total,net_in,net_out,net_total_up,net_total_down,process,connections,connections_udp"},
		{"gpu_records", len(batch.GPURecords), 8, "client,time,device_index,device_name,mem_total,mem_used,utilization,temperature"},
		{"ping_records", len(batch.PingRecords), 4, "client,task_id,time,value"},
		{"ping_rollups", pingRollupRows, 12, "client,task_id,resolution_seconds,bucket_time,sample_count,valid_count,loss_count,sum_value,min_value,max_value,last_value,last_time"},
	} {
		for start := 0; start < spec.rows; start += w.config.ChunkRows {
			rows := min(w.config.ChunkRows, spec.rows-start)
			if _, err := w.statement(ctx, spec.kind, rows, spec.columns, spec.names); err != nil {
				return err
			}
		}
	}
	return nil
}

var pingRollupResolutions = [...]time.Duration{time.Minute, 15 * time.Minute, time.Hour}

type pingRollupKey struct {
	client     string
	taskID     uint
	resolution int
	bucketUnix int64
}

func aggregatePingRollups(records []models.PingRecord) []models.PingRollup {
	if len(records) == 0 {
		return nil
	}
	byBucket := make(map[pingRollupKey]models.PingRollup, len(records)*len(pingRollupResolutions))
	for _, record := range records {
		at := record.Time.ToTime().UTC()
		for _, resolution := range pingRollupResolutions {
			bucket := at.Truncate(resolution)
			key := pingRollupKey{
				client: record.Client, taskID: record.TaskId,
				resolution: int(resolution / time.Second), bucketUnix: bucket.Unix(),
			}
			aggregate, exists := byBucket[key]
			if !exists {
				aggregate = models.PingRollup{
					Client: record.Client, TaskId: record.TaskId,
					ResolutionSeconds: key.resolution, BucketTime: models.FromTime(bucket),
					MinValue: record.Value, MaxValue: record.Value,
					LastValue: record.Value, LastTime: models.FromTime(at),
				}
			}
			aggregate.SampleCount++
			if record.Value < 0 {
				aggregate.LossCount++
			} else {
				if aggregate.ValidCount == 0 {
					aggregate.MinValue = record.Value
					aggregate.MaxValue = record.Value
				} else {
					aggregate.MinValue = min(aggregate.MinValue, record.Value)
					aggregate.MaxValue = max(aggregate.MaxValue, record.Value)
				}
				aggregate.ValidCount++
				aggregate.SumValue += int64(record.Value)
			}
			if !exists || at.After(aggregate.LastTime.ToTime()) {
				aggregate.LastTime = models.FromTime(at)
				aggregate.LastValue = record.Value
			}
			byBucket[key] = aggregate
		}
	}
	result := make([]models.PingRollup, 0, len(byBucket))
	for _, aggregate := range byBucket {
		result = append(result, aggregate)
	}
	return result
}

func (w *Writer) writeRecords(ctx context.Context, tx *sql.Tx, records []models.Record) error {
	const columns = 19
	for start := 0; start < len(records); start += w.config.ChunkRows {
		end := min(start+w.config.ChunkRows, len(records))
		stmt, err := w.statement(ctx, "records", end-start, columns, "client,time,cpu,gpu,ram,ram_total,swap,swap_total,load,temp,disk,disk_total,net_in,net_out,net_total_up,net_total_down,process,connections,connections_udp")
		if err != nil {
			return err
		}
		args := make([]any, 0, (end-start)*columns)
		for _, record := range records[start:end] {
			args = append(args, record.Client, record.Time, record.Cpu, record.Gpu, record.Ram, record.RamTotal, record.Swap, record.SwapTotal, record.Load, record.Temp, record.Disk, record.DiskTotal, record.NetIn, record.NetOut, record.NetTotalUp, record.NetTotalDown, record.Process, record.Connections, record.ConnectionsUdp)
		}
		if _, err := tx.StmtContext(ctx, stmt).ExecContext(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) writeGPURecords(ctx context.Context, tx *sql.Tx, records []models.GPURecord) error {
	const columns = 8
	for start := 0; start < len(records); start += w.config.ChunkRows {
		end := min(start+w.config.ChunkRows, len(records))
		stmt, err := w.statement(ctx, "gpu_records", end-start, columns, "client,time,device_index,device_name,mem_total,mem_used,utilization,temperature")
		if err != nil {
			return err
		}
		args := make([]any, 0, (end-start)*columns)
		for _, record := range records[start:end] {
			args = append(args, record.Client, record.Time, record.DeviceIndex, record.DeviceName, record.MemTotal, record.MemUsed, record.Utilization, record.Temperature)
		}
		if _, err := tx.StmtContext(ctx, stmt).ExecContext(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) writePingRecords(ctx context.Context, tx *sql.Tx, records []models.PingRecord) error {
	const columns = 4
	for start := 0; start < len(records); start += w.config.ChunkRows {
		end := min(start+w.config.ChunkRows, len(records))
		stmt, err := w.statement(ctx, "ping_records", end-start, columns, "client,task_id,time,value")
		if err != nil {
			return err
		}
		args := make([]any, 0, (end-start)*columns)
		for _, record := range records[start:end] {
			args = append(args, record.Client, record.TaskId, record.Time, record.Value)
		}
		if _, err := tx.StmtContext(ctx, stmt).ExecContext(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) writePingRollups(ctx context.Context, tx *sql.Tx, records []models.PingRollup) error {
	const columns = 12
	for start := 0; start < len(records); start += w.config.ChunkRows {
		end := min(start+w.config.ChunkRows, len(records))
		stmt, err := w.statement(ctx, "ping_rollups", end-start, columns, "client,task_id,resolution_seconds,bucket_time,sample_count,valid_count,loss_count,sum_value,min_value,max_value,last_value,last_time")
		if err != nil {
			return err
		}
		args := make([]any, 0, (end-start)*columns)
		for _, record := range records[start:end] {
			args = append(args,
				record.Client, record.TaskId, record.ResolutionSeconds, record.BucketTime,
				record.SampleCount, record.ValidCount, record.LossCount, record.SumValue, record.MinValue, record.MaxValue,
				record.LastValue, record.LastTime,
			)
		}
		if _, err := tx.StmtContext(ctx, stmt).ExecContext(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) statement(ctx context.Context, table string, rows, columns int, names string) (*sql.Stmt, error) {
	key := statementKey{kind: table, rows: rows}
	if statement := w.statements[key]; statement != nil {
		return statement, nil
	}
	row := "(" + strings.TrimSuffix(strings.Repeat("?,", columns), ",") + ")"
	query := "INSERT INTO " + table + " (" + names + ") VALUES " + strings.TrimSuffix(strings.Repeat(row+",", rows), ",")
	if table == "ping_rollups" {
		query += ` ON CONFLICT(client,task_id,resolution_seconds,bucket_time) DO UPDATE SET
			sample_count=ping_rollups.sample_count+excluded.sample_count,
			valid_count=ping_rollups.valid_count+excluded.valid_count,
			loss_count=ping_rollups.loss_count+excluded.loss_count,
			sum_value=ping_rollups.sum_value+excluded.sum_value,
			min_value=CASE WHEN ping_rollups.valid_count=0 THEN excluded.min_value WHEN excluded.valid_count=0 THEN ping_rollups.min_value ELSE MIN(ping_rollups.min_value,excluded.min_value) END,
			max_value=CASE WHEN ping_rollups.valid_count=0 THEN excluded.max_value WHEN excluded.valid_count=0 THEN ping_rollups.max_value ELSE MAX(ping_rollups.max_value,excluded.max_value) END,
			last_value=CASE WHEN excluded.last_time>=ping_rollups.last_time THEN excluded.last_value ELSE ping_rollups.last_value END,
			last_time=MAX(ping_rollups.last_time,excluded.last_time)`
	}
	statement, err := w.db.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	w.statements[key] = statement
	return statement, nil
}

func isTransientSQLite(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code == sqlite3.ErrBusy || sqliteErr.Code == sqlite3.ErrLocked
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "database is busy")
}

func (w *Writer) Close(ctx context.Context) error {
	w.closeMu.Lock()
	defer w.closeMu.Unlock()
	w.acceptMu.Lock()
	if !w.closed {
		w.closed = true
		w.acceptMu.Unlock()
		select {
		case w.queue <- request{shutdown: true}:
		case <-ctx.Done():
			w.cancel()
			return ctx.Err()
		}
	} else {
		w.acceptMu.Unlock()
	}
	select {
	case <-w.done:
		w.cancel()
		return nil
	case <-ctx.Done():
		w.cancel()
		return ctx.Err()
	}
}

func (w *Writer) failQueued(err error) {
	for {
		select {
		case req := <-w.queue:
			if !req.shutdown {
				req.result <- err
			}
		default:
			return
		}
	}
}

func (w *Writer) closeStatements() {
	for _, statement := range w.statements {
		_ = statement.Close()
	}
}
