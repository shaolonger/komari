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
