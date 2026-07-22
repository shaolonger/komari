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
	"github.com/komari-monitor/komari/internal/observability"
	"github.com/mattn/go-sqlite3"
)

const (
	DefaultQueueCapacity = 64
	DefaultMaxRetries    = 4
	DefaultChunkRows     = 256
	MaxQueueCapacity     = 1024
	MaxRowsPerBatch      = 100_000
)

var ErrClosed = errors.New("telemetry writer is closed")

type Batch struct {
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
	for {
		if w.runCtx.Err() != nil {
			w.failQueued(w.runCtx.Err())
			return
		}
		var req request
		select {
		case <-w.runCtx.Done():
			w.failQueued(w.runCtx.Err())
			return
		case req = <-w.queue:
		}
		observability.SetFlushQueueDepth(len(w.queue))
		if req.shutdown {
			return
		}
		err, retries := w.process(req.ctx, req.batch)
		if err == nil {
			observability.ObserveBatch(req.batch.Rows(), retries)
		}
		req.result <- err
	}
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
	if err := w.prepareBatch(ctx, batch); err != nil {
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
	return tx.Commit()
}

func (w *Writer) prepareBatch(ctx context.Context, batch Batch) error {
	for _, spec := range []struct {
		kind    string
		rows    int
		columns int
		names   string
	}{
		{"records", len(batch.Records), 19, "client,time,cpu,gpu,ram,ram_total,swap,swap_total,load,temp,disk,disk_total,net_in,net_out,net_total_up,net_total_down,process,connections,connections_udp"},
		{"gpu_records", len(batch.GPURecords), 8, "client,time,device_index,device_name,mem_total,mem_used,utilization,temperature"},
		{"ping_records", len(batch.PingRecords), 4, "client,task_id,time,value"},
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

func (w *Writer) statement(ctx context.Context, table string, rows, columns int, names string) (*sql.Stmt, error) {
	key := statementKey{kind: table, rows: rows}
	if statement := w.statements[key]; statement != nil {
		return statement, nil
	}
	row := "(" + strings.TrimSuffix(strings.Repeat("?,", columns), ",") + ")"
	query := "INSERT INTO " + table + " (" + names + ") VALUES " + strings.TrimSuffix(strings.Repeat(row+",", rows), ",")
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
