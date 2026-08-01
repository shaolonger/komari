package client

import (
	"context"
	"errors"
	"hash/fnv"
	"math"
	"sync"
	"time"

	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/protocol/telemetryv3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrTelemetrySequenceGap = errors.New("telemetry v3 sequence gap")

const telemetrySequenceLockCount = 256

var telemetrySequenceLocks [telemetrySequenceLockCount]sync.Mutex

type telemetryAcceptance struct {
	Through   uint64
	Duplicate bool
}

func telemetrySequenceLock(client string) *sync.Mutex {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(client))
	return &telemetrySequenceLocks[hash.Sum32()%telemetrySequenceLockCount]
}

func acceptTelemetryV3(
	ctx context.Context,
	db *gorm.DB,
	client string,
	frame telemetryv3.Frame,
	report common.Report,
	save func(string, common.Report) error,
) (telemetryAcceptance, error) {
	if db == nil || client == "" || save == nil || frame.Sequence == 0 || frame.Sequence > math.MaxInt64 {
		return telemetryAcceptance{}, errors.New("invalid telemetry v3 acceptance input")
	}
	lock := telemetrySequenceLock(client)
	lock.Lock()
	defer lock.Unlock()

	var checkpoint models.TelemetryV3Sequence
	err := db.WithContext(ctx).Where("client = ?", client).Take(&checkpoint).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return telemetryAcceptance{}, err
	}
	through := checkpoint.ThroughSequence
	if frame.Sequence <= through {
		return telemetryAcceptance{Through: through, Duplicate: true}, nil
	}
	if frame.Sequence != through+1 {
		return telemetryAcceptance{Through: through}, ErrTelemetrySequenceGap
	}
	report.UUID = client
	report.UpdatedAt = time.Now()
	if err := save(client, report); err != nil {
		return telemetryAcceptance{Through: through}, err
	}
	checkpoint = models.TelemetryV3Sequence{Client: client, ThroughSequence: frame.Sequence, UpdatedAt: time.Now()}
	if err := db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "client"}},
		DoUpdates: clause.AssignmentColumns([]string{"through_sequence", "updated_at"}),
	}).Create(&checkpoint).Error; err != nil {
		return telemetryAcceptance{Through: through}, err
	}
	return telemetryAcceptance{Through: frame.Sequence}, nil
}
