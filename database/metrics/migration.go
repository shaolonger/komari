package metrics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const metricMigrationBatchRows = 2_000

type MigrationStatus struct {
	Status         string            `json:"status"`
	IsRunning      bool              `json:"is_running"`
	SourceDriver   string            `json:"source_driver"`
	SourceDSN      string            `json:"source_dsn"`
	TargetDriver   string            `json:"target_driver"`
	TargetDSN      string            `json:"target_dsn"`
	TotalMetrics   int               `json:"total_metrics"`
	MetricsDone    int               `json:"metrics_done"`
	CurrentMetric  string            `json:"current_metric"`
	MigratedPoints int64             `json:"migrated_points"`
	StartTime      *models.LocalTime `json:"start_time,omitempty"`
	EndTime        *models.LocalTime `json:"end_time,omitempty"`
	Error          string            `json:"error,omitempty"`
}

var migrationRunner struct {
	sync.Mutex
	running bool
	cancel  context.CancelFunc
}

func migrationStatusFromModel(state models.MetricMigrationState) MigrationStatus {
	result := MigrationStatus{
		Status: state.Status, IsRunning: state.Status == "running",
		SourceDriver: "sqlite", SourceDSN: "[embedded]",
		TargetDriver: "sqlite", TargetDSN: "[embedded]",
		TotalMetrics: state.TotalMetrics, MetricsDone: state.MetricsDone,
		CurrentMetric: state.CurrentMetric, MigratedPoints: state.MigratedPoints,
		Error: state.ErrorMessage,
	}
	if result.Status == "" {
		result.Status = "idle"
	}
	if state.StartedAt != nil {
		value := models.FromTime(*state.StartedAt)
		result.StartTime = &value
	}
	if state.EndedAt != nil {
		value := models.FromTime(*state.EndedAt)
		result.EndTime = &value
	}
	return result
}

func GetMigrationStatus(ctx context.Context, db *gorm.DB) (MigrationStatus, error) {
	if ctx == nil || db == nil {
		return MigrationStatus{}, errors.New("migration context and database are required")
	}
	var state models.MetricMigrationState
	err := db.WithContext(ctx).First(&state, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return MigrationStatus{Status: "idle", SourceDriver: "sqlite", SourceDSN: "[embedded]", TargetDriver: "sqlite", TargetDSN: "[embedded]"}, nil
	}
	return migrationStatusFromModel(state), err
}

func StartMigration(db *gorm.DB, sourceDSN string) (MigrationStatus, error) {
	if db == nil {
		return MigrationStatus{}, errors.New("migration database is required")
	}
	if strings.TrimSpace(sourceDSN) != "" {
		return MigrationStatus{}, errors.New("external source DSNs are disabled; import an authenticated backup before starting the embedded migration")
	}
	migrationRunner.Lock()
	defer migrationRunner.Unlock()
	if migrationRunner.running {
		return MigrationStatus{}, errors.New("metric migration is already running")
	}
	var existing models.MetricMigrationState
	if err := db.First(&existing, 1).Error; err == nil && existing.Status == "running" {
		return MigrationStatus{}, errors.New("metric migration is already running")
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return MigrationStatus{}, err
	}
	now := time.Now().UTC()
	newRun := existing.Status == "" || existing.Status == "idle" || existing.Status == "completed"
	state := models.MetricMigrationState{
		ID: 1, Status: "running", TotalMetrics: 1, CurrentMetric: "ping.latency_ms",
		CheckpointRowID: existing.CheckpointRowID, UpperRowID: existing.UpperRowID,
		MigratedPoints: existing.MigratedPoints,
		StartedAt:      &now,
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if newRun {
			state.CheckpointRowID = 0
			state.MigratedPoints = 0
			if err := tx.Raw("SELECT COALESCE(MAX(rowid),0) FROM ping_records").Scan(&state.UpperRowID).Error; err != nil {
				return err
			}
			// Cutoff capture and reset share one SQLite write transaction. Rows
			// committed later maintain their own rollups and are not backfilled.
			if err := tx.Exec("DELETE FROM ping_rollups").Error; err != nil {
				return err
			}
		}
		state.CancelRequested = false
		state.MetricsDone = 0
		state.EndedAt = nil
		state.ErrorMessage = ""
		return tx.Save(&state).Error
	}); err != nil {
		return MigrationStatus{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	migrationRunner.running = true
	migrationRunner.cancel = cancel
	go runMigration(ctx, db)
	return GetMigrationStatus(context.Background(), db)
}

func ResumeMigration(db *gorm.DB) {
	if db == nil {
		return
	}
	migrationRunner.Lock()
	defer migrationRunner.Unlock()
	if migrationRunner.running {
		return
	}
	var state models.MetricMigrationState
	if err := db.First(&state, 1).Error; err != nil || state.Status != "running" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	migrationRunner.running = true
	migrationRunner.cancel = cancel
	go runMigration(ctx, db)
}

func CancelMigration(ctx context.Context, db *gorm.DB) (MigrationStatus, error) {
	if ctx == nil || db == nil {
		return MigrationStatus{}, errors.New("migration context and database are required")
	}
	if err := db.WithContext(ctx).Model(&models.MetricMigrationState{}).Where("id = ? AND status = ?", 1, "running").Update("cancel_requested", true).Error; err != nil {
		return MigrationStatus{}, err
	}
	migrationRunner.Lock()
	if migrationRunner.cancel != nil {
		migrationRunner.cancel()
	}
	migrationRunner.Unlock()
	return GetMigrationStatus(ctx, db)
}

type migrationPingRow struct {
	RowID  int64 `gorm:"column:row_id"`
	Client string
	TaskID uint `gorm:"column:task_id"`
	Time   models.LocalTime
	Value  int
}

type migrationRollupKey struct {
	client     string
	taskID     uint
	resolution int
	bucketUnix int64
}

func migrationRollups(rows []migrationPingRow) []models.PingRollup {
	grouped := make(map[migrationRollupKey]models.PingRollup, len(rows)*3)
	for _, row := range rows {
		at := row.Time.ToTime().UTC()
		for _, resolution := range []time.Duration{time.Minute, 15 * time.Minute, time.Hour} {
			bucket := at.Truncate(resolution)
			key := migrationRollupKey{row.Client, row.TaskID, int(resolution / time.Second), bucket.Unix()}
			rollup := grouped[key]
			if rollup.SampleCount == 0 {
				rollup = models.PingRollup{Client: row.Client, TaskId: row.TaskID, ResolutionSeconds: key.resolution, BucketTime: models.FromTime(bucket), LastValue: row.Value, LastTime: models.FromTime(at)}
			}
			rollup.SampleCount++
			if row.Value < 0 {
				rollup.LossCount++
			} else {
				if rollup.ValidCount == 0 {
					rollup.MinValue, rollup.MaxValue = row.Value, row.Value
				} else {
					rollup.MinValue = min(rollup.MinValue, row.Value)
					rollup.MaxValue = max(rollup.MaxValue, row.Value)
				}
				rollup.ValidCount++
				rollup.SumValue += int64(row.Value)
			}
			if at.After(rollup.LastTime.ToTime()) {
				rollup.LastTime, rollup.LastValue = models.FromTime(at), row.Value
			}
			grouped[key] = rollup
		}
	}
	result := make([]models.PingRollup, 0, len(grouped))
	for _, rollup := range grouped {
		result = append(result, rollup)
	}
	return result
}

func upsertMigrationRollups(tx *gorm.DB, rows []models.PingRollup) error {
	if len(rows) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "client"}, {Name: "task_id"}, {Name: "resolution_seconds"}, {Name: "bucket_time"}},
		DoUpdates: clause.Assignments(map[string]any{
			"sample_count": gorm.Expr("ping_rollups.sample_count + excluded.sample_count"),
			"valid_count":  gorm.Expr("ping_rollups.valid_count + excluded.valid_count"),
			"loss_count":   gorm.Expr("ping_rollups.loss_count + excluded.loss_count"),
			"sum_value":    gorm.Expr("ping_rollups.sum_value + excluded.sum_value"),
			"min_value":    gorm.Expr("CASE WHEN ping_rollups.valid_count=0 THEN excluded.min_value WHEN excluded.valid_count=0 THEN ping_rollups.min_value ELSE MIN(ping_rollups.min_value,excluded.min_value) END"),
			"max_value":    gorm.Expr("CASE WHEN ping_rollups.valid_count=0 THEN excluded.max_value WHEN excluded.valid_count=0 THEN ping_rollups.max_value ELSE MAX(ping_rollups.max_value,excluded.max_value) END"),
			"last_value":   gorm.Expr("CASE WHEN excluded.last_time>=ping_rollups.last_time THEN excluded.last_value ELSE ping_rollups.last_value END"),
			"last_time":    gorm.Expr("MAX(ping_rollups.last_time,excluded.last_time)"),
		}),
	}).CreateInBatches(rows, 100).Error
}

func finishMigration(db *gorm.DB, status, message string) {
	now := time.Now().UTC()
	updates := map[string]any{"status": status, "cancel_requested": false, "ended_at": &now, "error_message": message}
	if status == "completed" {
		updates["metrics_done"] = 1
		updates["current_metric"] = ""
	}
	_ = db.Model(&models.MetricMigrationState{}).Where("id = ?", 1).Updates(updates).Error
	migrationRunner.Lock()
	migrationRunner.running = false
	migrationRunner.cancel = nil
	migrationRunner.Unlock()
}

func runMigration(ctx context.Context, db *gorm.DB) {
	for {
		if err := ctx.Err(); err != nil {
			finishMigration(db, "canceled", "")
			return
		}
		var state models.MetricMigrationState
		if err := db.First(&state, 1).Error; err != nil {
			finishMigration(db, "failed", "load migration checkpoint")
			return
		}
		if state.CancelRequested {
			finishMigration(db, "canceled", "")
			return
		}
		var rows []migrationPingRow
		err := db.WithContext(ctx).Table("ping_records AS pr").
			Select("pr.rowid AS row_id,pr.client,pr.task_id,pr.time,pr.value").
			Joins("INNER JOIN clients ON clients.uuid = pr.client").
			Joins("INNER JOIN ping_tasks ON ping_tasks.id = pr.task_id").
			Where("pr.rowid > ? AND pr.rowid <= ?", state.CheckpointRowID, state.UpperRowID).
			Order("pr.rowid ASC").Limit(metricMigrationBatchRows).Scan(&rows).Error
		if err != nil {
			message := fmt.Sprintf("read Ping migration page: %v", err)
			if len(message) > 512 {
				message = message[:512]
			}
			finishMigration(db, "failed", message)
			return
		}
		if len(rows) == 0 {
			finishMigration(db, "completed", "")
			return
		}
		lastRowID := rows[len(rows)-1].RowID
		rollups := migrationRollups(rows)
		err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := upsertMigrationRollups(tx, rollups); err != nil {
				return err
			}
			return tx.Model(&models.MetricMigrationState{}).Where("id = ?", 1).Updates(map[string]any{
				"checkpoint_row_id": lastRowID,
				"migrated_points":   gorm.Expr("migrated_points + ?", len(rows)),
			}).Error
		})
		if err != nil {
			if errors.Is(err, context.Canceled) {
				finishMigration(db, "canceled", "")
			} else {
				finishMigration(db, "failed", "persist Ping migration page")
			}
			return
		}
	}
}
