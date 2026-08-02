package client

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm"
)

const maxPingResultBatch = 32

type pingResultInput struct {
	TaskID     uint      `json:"task_id"`
	Value      int       `json:"value"`
	PingType   string    `json:"ping_type"`
	FinishedAt time.Time `json:"finished_at"`
}

func acceptPingResultBatch(
	ctx context.Context,
	db *gorm.DB,
	client string,
	sequence uint64,
	inputs []pingResultInput,
	save func([]models.PingRecord, models.PingResultSequence) error,
) (telemetryAcceptance, error) {
	if db == nil || client == "" || sequence == 0 || sequence > math.MaxInt64 || save == nil || len(inputs) == 0 || len(inputs) > maxPingResultBatch {
		return telemetryAcceptance{}, errors.New("invalid Ping result batch")
	}
	now := time.Now()
	records := make([]models.PingRecord, len(inputs))
	for index, input := range inputs {
		if input.TaskID == 0 || input.FinishedAt.IsZero() || input.FinishedAt.After(now.Add(5*time.Minute)) || input.FinishedAt.Before(now.Add(-25*time.Hour)) {
			return telemetryAcceptance{}, errors.New("invalid Ping result")
		}
		records[index] = models.PingRecord{Client: client, TaskId: input.TaskID, Value: input.Value, Time: models.FromTime(input.FinishedAt)}
	}
	lock := telemetrySequenceLock("ping:" + client)
	lock.Lock()
	defer lock.Unlock()
	var checkpoint models.PingResultSequence
	err := db.WithContext(ctx).Where("client = ?", client).Take(&checkpoint).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return telemetryAcceptance{}, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		checkpoint.ThroughSequence = sequence - 1
	}
	through := checkpoint.ThroughSequence
	if sequence <= through {
		return telemetryAcceptance{Through: through, Duplicate: true}, nil
	}
	if sequence != through+1 {
		return telemetryAcceptance{Through: through}, ErrTelemetrySequenceGap
	}
	checkpoint = models.PingResultSequence{Client: client, ThroughSequence: sequence, UpdatedAt: now}
	if err := save(records, checkpoint); err != nil {
		return telemetryAcceptance{Through: through}, err
	}
	return telemetryAcceptance{Through: sequence}, nil
}
