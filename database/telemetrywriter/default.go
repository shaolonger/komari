package telemetrywriter

import (
	"context"
	"sync"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/storage"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	defaultMu     sync.Mutex
	defaultWriter *Writer
	defaultClosed bool
)

func Default() (*Writer, error) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultClosed {
		return nil, ErrClosed
	}
	if defaultWriter != nil {
		return defaultWriter, nil
	}
	sqlDB, err := dbcore.GetWriterDBInstance()
	if err != nil {
		return nil, err
	}
	defaultWriter, err = New(sqlDB, Config{})
	return defaultWriter, err
}

func Submit(ctx context.Context, batch Batch) error {
	if store, ok := storage.Telemetry(); ok {
		storageBatch := storage.TelemetryBatch{
			ID:                 batch.ID,
			Records:            batch.Records,
			GPURecords:         batch.GPURecords,
			PingRecords:        batch.PingRecords,
			TelemetrySequences: batch.TelemetrySequences,
			PingSequences:      batch.PingSequences,
		}
		if err := store.WriteBatch(ctx, storageBatch); err != nil {
			return err
		}
		if atomic, ok := store.(storage.AtomicCheckpointStore); ok && atomic.WritesCheckpointsAtomically() {
			return nil
		}
		return persistExternalCheckpoints(ctx, batch.TelemetrySequences, batch.PingSequences)
	}
	writer, err := Default()
	if err != nil {
		return err
	}
	return writer.Submit(ctx, batch)
}

func persistExternalCheckpoints(ctx context.Context, telemetry []models.TelemetryV3Sequence, ping []models.PingResultSequence) error {
	if len(telemetry) == 0 && len(ping) == 0 {
		return nil
	}
	return dbcore.GetDBInstance().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(telemetry) > 0 {
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "client"}},
				DoUpdates: clause.Assignments(map[string]any{
					"through_sequence": gorm.Expr("MAX(through_sequence,excluded.through_sequence)"),
					"updated_at":       gorm.Expr("CASE WHEN excluded.through_sequence>=through_sequence THEN excluded.updated_at ELSE updated_at END"),
				}),
			}).Create(&telemetry).Error; err != nil {
				return err
			}
		}
		if len(ping) > 0 {
			return tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "client"}},
				DoUpdates: clause.Assignments(map[string]any{
					"through_sequence": gorm.Expr("MAX(through_sequence,excluded.through_sequence)"),
					"updated_at":       gorm.Expr("CASE WHEN excluded.through_sequence>=through_sequence THEN excluded.updated_at ELSE updated_at END"),
				}),
			}).Create(&ping).Error
		}
		return nil
	})
}

func CloseDefault(ctx context.Context) error {
	if store, ok := storage.Telemetry(); ok {
		return store.Close(ctx)
	}
	defaultMu.Lock()
	writer := defaultWriter
	defaultWriter = nil
	defaultClosed = true
	defaultMu.Unlock()
	if writer == nil {
		return nil
	}
	return writer.Close(ctx)
}
