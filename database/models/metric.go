package models

import "time"

// MetricRetentionPolicy stores only operator overrides. Metric identity,
// projection and units remain a compile-time allowlist and cannot be injected
// through configuration or RPC.
type MetricRetentionPolicy struct {
	Name          string    `json:"name" gorm:"primaryKey;type:varchar(96)"`
	RetentionDays int       `json:"retention_days" gorm:"not null"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type MetricMigrationState struct {
	ID              uint   `gorm:"primaryKey"`
	Status          string `gorm:"type:varchar(16);not null"`
	CancelRequested bool   `gorm:"not null;default:false"`
	CheckpointRowID int64  `gorm:"not null;default:0"`
	UpperRowID      int64  `gorm:"not null;default:0"`
	TotalMetrics    int    `gorm:"not null;default:0"`
	MetricsDone     int    `gorm:"not null;default:0"`
	CurrentMetric   string `gorm:"type:varchar(96)"`
	MigratedPoints  int64  `gorm:"not null;default:0"`
	StartedAt       *time.Time
	EndedAt         *time.Time
	ErrorMessage    string `gorm:"type:text"`
}
