// Package storage defines the database-neutral persistence boundary used by
// the control plane and high-volume telemetry paths.
package storage

import (
	"context"
	"errors"
	"time"

	"github.com/komari-monitor/komari/database/models"
)

var (
	ErrNotConfigured = errors.New("storage backend is not configured")
	ErrNotFound      = errors.New("storage record not found")
)

// TelemetryBatch is an immutable-at-call-boundary group of telemetry rows.
// Implementations must either durably commit the complete batch or return an
// error; partial success is forbidden.
type TelemetryBatch struct {
	Records     []models.Record
	GPURecords  []models.GPURecord
	PingRecords []models.PingRecord
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
	SessionCredential(context.Context, string, []byte) (models.Session, error)
	UserByUUID(context.Context, string) (models.User, error)
	Migrate(context.Context) error
	Health(context.Context) (Health, error)
	Close(context.Context) error
}
