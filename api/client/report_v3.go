package client

import (
	"context"
	"errors"
	"hash/fnv"
	"math"
	"sync"
	"time"

	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/protocol/telemetryv3"
	"gorm.io/gorm"
)

var ErrTelemetrySequenceGap = errors.New("telemetry v3 sequence gap")

const telemetrySequenceLockCount = 256

var telemetrySequenceLocks [telemetrySequenceLockCount]sync.Mutex

type telemetryAcceptance struct {
	Through         uint64
	AcceptedThrough uint64
	Expected        uint64
	Duplicate       bool
}

type telemetrySequenceState struct {
	durable     uint64
	accepted    uint64
	refreshedAt time.Time
}

var (
	telemetrySequenceStatesMu sync.Mutex
	telemetrySequenceStates   = make(map[string]telemetrySequenceState)
)

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
	save func(string, common.Report, telemetryv3.Frame) error,
) (telemetryAcceptance, error) {
	if db == nil || client == "" || save == nil || frame.Sequence == 0 || frame.Sequence > math.MaxInt64 {
		return telemetryAcceptance{}, errors.New("invalid telemetry v3 acceptance input")
	}
	lock := telemetrySequenceLock(client)
	lock.Lock()
	defer lock.Unlock()

	state, err := loadTelemetrySequenceState(ctx, db, client)
	if err != nil {
		return telemetryAcceptance{}, err
	}
	if state.accepted == 0 && state.durable == 0 {
		// A freshly created server database may reconnect to an agent whose local
		// high-water mark is already non-zero. With no durable server history to
		// protect, its authenticated first frame establishes the stream base.
		state.accepted = frame.Sequence - 1
	}
	if frame.Sequence <= state.accepted {
		return telemetryAcceptance{Through: state.durable, AcceptedThrough: state.accepted, Duplicate: true}, nil
	}
	if frame.Sequence != state.accepted+1 {
		if !frame.Checkpoint {
			return telemetryAcceptance{Through: state.durable, AcceptedThrough: state.accepted, Expected: state.accepted + 1}, ErrTelemetrySequenceGap
		}
		// Older agents could evict the head of their local spool. A checkpoint is
		// self-contained, arrives over an ordered authenticated WebSocket, and is
		// therefore the only safe point at which an unrecoverable local gap can be
		// rebased without permanently deadlocking that node.
		state.accepted = frame.Sequence - 1
	}
	report.UUID = client
	report.UpdatedAt = frame.SampledAt
	if report.UpdatedAt.IsZero() || report.UpdatedAt.After(time.Now().Add(5*time.Minute)) {
		report.UpdatedAt = time.Now()
	}
	if err := save(client, report, frame); err != nil {
		return telemetryAcceptance{Through: state.durable, AcceptedThrough: state.accepted}, err
	}
	state.accepted = frame.Sequence
	storeTelemetrySequenceState(client, state)
	return telemetryAcceptance{Through: state.durable, AcceptedThrough: state.accepted}, nil
}

func loadTelemetrySequenceState(ctx context.Context, db *gorm.DB, client string) (telemetrySequenceState, error) {
	telemetrySequenceStatesMu.Lock()
	state, exists := telemetrySequenceStates[client]
	telemetrySequenceStatesMu.Unlock()
	if exists {
		if state.accepted == state.durable || time.Since(state.refreshedAt) < time.Second {
			return state, nil
		}
		var checkpoint models.TelemetryV3Sequence
		err := db.WithContext(ctx).Where("client = ?", client).Take(&checkpoint).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return telemetrySequenceState{}, err
		}
		state.durable = max(state.durable, checkpoint.ThroughSequence)
		state.refreshedAt = time.Now()
		storeTelemetrySequenceState(client, state)
		return state, nil
	}
	var checkpoint models.TelemetryV3Sequence
	err := db.WithContext(ctx).Where("client = ?", client).Take(&checkpoint).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return telemetrySequenceState{}, err
	}
	state = telemetrySequenceState{durable: checkpoint.ThroughSequence, accepted: checkpoint.ThroughSequence, refreshedAt: time.Now()}
	storeTelemetrySequenceState(client, state)
	return state, nil
}

func storeTelemetrySequenceState(client string, state telemetrySequenceState) {
	telemetrySequenceStatesMu.Lock()
	telemetrySequenceStates[client] = state
	telemetrySequenceStatesMu.Unlock()
}

func markTelemetrySequencesDurable(checkpoints []models.TelemetryV3Sequence) {
	telemetrySequenceStatesMu.Lock()
	defer telemetrySequenceStatesMu.Unlock()
	for _, checkpoint := range checkpoints {
		state := telemetrySequenceStates[checkpoint.Client]
		state.durable = max(state.durable, checkpoint.ThroughSequence)
		state.accepted = max(state.accepted, state.durable)
		telemetrySequenceStates[checkpoint.Client] = state
	}
}

func forgetTelemetrySequenceState(client string) {
	telemetrySequenceStatesMu.Lock()
	delete(telemetrySequenceStates, client)
	telemetrySequenceStatesMu.Unlock()
}

// ForgetClientTelemetry clears all process-local ingest state after the
// corresponding authenticated client has been deleted.
func ForgetClientTelemetry(client string) {
	forgetTelemetrySequenceState(client)
	api.Telemetry.Remove(client)
}
