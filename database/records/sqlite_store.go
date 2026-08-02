package records

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/telemetrywriter"
	"github.com/komari-monitor/komari/internal/storage"
	"gorm.io/gorm"
)

// SQLiteTelemetryStore adapts the optimized SQLite query planner and bounded
// single-writer pipeline to the database-neutral telemetry contract.
type SQLiteTelemetryStore struct {
	readDB  *gorm.DB
	writeDB *sql.DB
	writer  *telemetrywriter.Writer
}

var _ storage.TelemetryStore = (*SQLiteTelemetryStore)(nil)

func NewSQLiteTelemetryStore(readDB *gorm.DB, writeDB *sql.DB, config telemetrywriter.Config) (*SQLiteTelemetryStore, error) {
	if readDB == nil {
		return nil, errors.New("SQLite telemetry read database is required")
	}
	if writeDB == nil {
		return nil, errors.New("SQLite telemetry write database is required")
	}
	writer, err := telemetrywriter.New(writeDB, config)
	if err != nil {
		return nil, err
	}
	return &SQLiteTelemetryStore{readDB: readDB, writeDB: writeDB, writer: writer}, nil
}

func (store *SQLiteTelemetryStore) WriteBatch(ctx context.Context, batch storage.TelemetryBatch) error {
	return store.writer.Submit(ctx, telemetrywriter.Batch{
		ID:                 batch.ID,
		Records:            batch.Records,
		GPURecords:         batch.GPURecords,
		PingRecords:        batch.PingRecords,
		TelemetrySequences: batch.TelemetrySequences,
		PingSequences:      batch.PingSequences,
	})
}

func (*SQLiteTelemetryStore) WritesCheckpointsAtomically() bool { return true }

// PreparePingContractFixture is used only by the backend-neutral contract
// suite because embedded SQLite enforces control-plane foreign keys.
func (store *SQLiteTelemetryStore) PreparePingContractFixture(ctx context.Context, client string, taskID uint) error {
	return store.readDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.FirstOrCreate(&models.Client{UUID: client}, "uuid = ?", client).Error; err != nil {
			return err
		}
		return tx.FirstOrCreate(&models.PingTask{Id: taskID}, "id = ?", taskID).Error
	})
}

func (store *SQLiteTelemetryStore) QueryRecords(ctx context.Context, query storage.RecordRange) ([]models.Record, error) {
	return QueryRecords(ctx, store.readDB, RecordQuery{
		Client: query.Client, Start: query.Start, End: query.End,
		LoadType: query.LoadType, MaxPoints: query.MaxPoints,
	})
}

func (store *SQLiteTelemetryStore) QueryGPURecords(ctx context.Context, query storage.GPURange) ([]models.GPURecord, error) {
	return QueryGPURecords(ctx, store.readDB, GPUQuery{
		Client: query.Client, Start: query.Start, End: query.End, MaxPoints: query.MaxPoints,
	})
}

func (store *SQLiteTelemetryStore) QueryPingRecords(ctx context.Context, query storage.PingRange) ([]models.PingRecord, error) {
	limit, err := validateTelemetryStoreRange(query.Start, query.End, query.MaxPoints)
	if err != nil {
		return nil, err
	}
	var rows []models.PingRecord
	dbQuery := store.readDB.WithContext(ctx).Where("time >= ? AND time < ?", query.Start, query.End)
	if query.Client != "" {
		dbQuery = dbQuery.Where("client = ?", query.Client)
	}
	if query.TaskID != 0 {
		dbQuery = dbQuery.Where("task_id = ?", query.TaskID)
	}
	if err := dbQuery.Order("time ASC, client ASC, task_id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) > limit {
		return nil, storage.ErrQueryLimit
	}
	return rows, nil
}

func validateTelemetryStoreRange(start, end time.Time, maxPoints int) (int, error) {
	if start.IsZero() || end.IsZero() || !end.After(start) {
		return 0, ErrInvalidQueryRange
	}
	if maxPoints <= 0 {
		maxPoints = adminQueryBudget.MaxPoints
	}
	if maxPoints > adminQueryBudget.MaxPoints {
		return 0, storage.ErrQueryLimit
	}
	return maxPoints, nil
}

func (store *SQLiteTelemetryStore) AggregateRecords(ctx context.Context, query storage.AggregateQuery) ([]models.Record, error) {
	if query.Resolution <= 0 {
		return nil, errors.New("aggregate resolution must be positive")
	}
	rows, err := store.QueryRecords(ctx, query.Range)
	if err != nil {
		return nil, err
	}
	return aggregateRecordRows(rows, query.Resolution, query.Range.LoadType), nil
}

func (store *SQLiteTelemetryStore) ApplyRetention(ctx context.Context, policy storage.RetentionPolicy) (storage.RetentionResult, error) {
	now := policy.Now
	if now.IsZero() {
		now = time.Now()
	}
	if policy.FinalCutoff.IsZero() || policy.FinalCutoff.After(now) {
		return storage.RetentionResult{}, errors.New("retention cutoff must be set and cannot be in the future")
	}
	if err := ApplyTierRetentionAt(ctx, store.readDB, now, policy.FinalCutoff); err != nil {
		return storage.RetentionResult{}, err
	}
	return storage.RetentionResult{FinalCutoff: policy.FinalCutoff, CompletedAt: time.Now()}, nil
}

func (store *SQLiteTelemetryStore) Health(ctx context.Context) (storage.Health, error) {
	started := time.Now()
	health := storage.Health{Backend: "sqlite", CheckedAt: started}
	if err := store.writeDB.PingContext(ctx); err != nil {
		health.Latency = time.Since(started)
		return health, err
	}
	var value int
	if err := store.readDB.WithContext(ctx).Raw("SELECT 1").Scan(&value).Error; err != nil {
		health.Latency = time.Since(started)
		return health, err
	}
	health.Ready = value == 1
	health.Latency = time.Since(started)
	if !health.Ready {
		return health, errors.New("SQLite telemetry health query returned an unexpected value")
	}
	return health, nil
}

func (store *SQLiteTelemetryStore) Close(ctx context.Context) error {
	return store.writer.Close(ctx)
}
