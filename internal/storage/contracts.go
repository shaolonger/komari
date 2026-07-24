// Package storage defines the database-neutral persistence boundary used by
// the control plane and high-volume telemetry paths.
package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/komari-monitor/komari/database/models"
)

var (
	ErrNotConfigured = errors.New("storage backend is not configured")
	ErrNotFound      = errors.New("storage record not found")
	ErrQueryLimit    = errors.New("storage query result exceeds the configured point limit")
)

// TelemetryBatch is an immutable-at-call-boundary group of telemetry rows.
// Implementations must either durably commit the complete batch or return an
// error; partial success is forbidden.
type TelemetryBatch struct {
	ID          string
	Records     []models.Record
	GPURecords  []models.GPURecord
	PingRecords []models.PingRecord
}

// BatchID returns the caller supplied idempotency key or a deterministic
// SHA-256 key derived from the persisted batch fields.
func BatchID(batch TelemetryBatch) (string, error) {
	if batch.ID != "" {
		if len(batch.ID) > 128 {
			return "", errors.New("telemetry batch ID exceeds 128 bytes")
		}
		return batch.ID, nil
	}
	payload, err := json.Marshal(struct {
		Records     []models.Record
		GPURecords  []models.GPURecord
		PingRecords []models.PingRecord
	}{batch.Records, batch.GPURecords, batch.PingRecords})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func (batch TelemetryBatch) Rows() int {
	return len(batch.Records) + len(batch.GPURecords) + len(batch.PingRecords)
}

type RecordRange struct {
	Client    string
	Start     time.Time
	End       time.Time
	LoadType  string
	MaxPoints int
}

type GPURange struct {
	Client    string
	Start     time.Time
	End       time.Time
	MaxPoints int
}

type AggregateQuery struct {
	Range      RecordRange
	Resolution time.Duration
}

type RetentionPolicy struct {
	Now         time.Time
	FinalCutoff time.Time
}

type RetentionResult struct {
	FinalCutoff time.Time
	CompletedAt time.Time
}

// Health is deliberately small and contains no DSN, credentials, hostnames or
// other configuration that could leak secrets through diagnostics.
type Health struct {
	Backend   string
	Ready     bool
	CheckedAt time.Time
	Latency   time.Duration
}

type TelemetryStore interface {
	WriteBatch(context.Context, TelemetryBatch) error
	QueryRecords(context.Context, RecordRange) ([]models.Record, error)
	QueryGPURecords(context.Context, GPURange) ([]models.GPURecord, error)
	AggregateRecords(context.Context, AggregateQuery) ([]models.Record, error)
	ApplyRetention(context.Context, RetentionPolicy) (RetentionResult, error)
	Health(context.Context) (Health, error)
	Close(context.Context) error
}

// ControlStore is the strongly consistent source of truth for authentication
// state. Process-local credential caches may accelerate these lookups, but may
// never replace this interface as the authority.
type ControlStore interface {
	ClientCredential(context.Context, string) (models.Client, error)
	ClientByUUID(context.Context, string) (models.Client, error)
	SessionCredential(context.Context, string, []byte) (models.Session, error)
	UserByUUID(context.Context, string) (models.User, error)
	UserByUsername(context.Context, string) (models.User, error)
	UserBySSO(context.Context, string) (models.User, error)
	HasUsers(context.Context) (bool, error)
	Migrate(context.Context) error
	Health(context.Context) (Health, error)
	Close(context.Context) error
}

type TelemetryMigrator interface {
	Migrate(context.Context) error
}

type ControlStateWriter interface {
	UpsertClientAuth(context.Context, models.Client) error
	DeleteClientAuth(context.Context, string) error
	UpsertSessionAuth(context.Context, models.Session) error
	DeleteSessionAuth(context.Context, []byte) error
	DeleteAllSessionsAuth(context.Context) error
	DeleteSessionsByUserAuth(context.Context, string) error
	DeleteExpiredSessionsAuth(context.Context, time.Time) error
	UpsertUserAuth(context.Context, models.User) error
	DeleteUserAuth(context.Context, string) error
}

type ExternalControlStateWriter interface {
	ControlStateWriter
	IsExternalControlStore() bool
}

func ExternalControlWriter() (ControlStateWriter, bool) {
	current, ok := Control()
	if !ok {
		return nil, false
	}
	writer, ok := current.(ExternalControlStateWriter)
	if !ok || !writer.IsExternalControlStore() {
		return nil, false
	}
	return writer, true
}

type ControlBootstrapper interface {
	Bootstrap(context.Context, []models.User, []models.Client, []models.Session) error
	IsEmpty(context.Context) (bool, error)
}
