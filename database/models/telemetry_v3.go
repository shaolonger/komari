package models

import "time"

type TelemetryV3Sequence struct {
	Client          string    `gorm:"primaryKey;column:client;type:varchar(36)" json:"client"`
	ThroughSequence uint64    `gorm:"column:through_sequence;not null" json:"through_sequence"`
	UpdatedAt       time.Time `gorm:"column:updated_at;not null" json:"updated_at"`
}

func (TelemetryV3Sequence) TableName() string { return "telemetry_v3_sequences" }
