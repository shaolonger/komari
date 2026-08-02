package models

import "time"

type PingResultSequence struct {
	Client          string    `gorm:"primaryKey;column:client;type:varchar(36)" json:"client"`
	ThroughSequence uint64    `gorm:"column:through_sequence;not null" json:"through_sequence"`
	UpdatedAt       time.Time `gorm:"column:updated_at;not null" json:"updated_at"`
}

func (PingResultSequence) TableName() string { return "ping_result_sequences" }
