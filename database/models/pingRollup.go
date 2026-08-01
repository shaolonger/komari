package models

// PingRollup is a compact, incrementally maintained Ping time bucket. The
// composite primary key makes every update idempotent within the writer's
// transaction and keeps range scans aligned with client/task filters.
type PingRollup struct {
	Client            string    `json:"client" gorm:"type:varchar(36);primaryKey;not null"`
	ClientInfo        *Client   `json:"-" gorm:"foreignKey:Client;references:UUID;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
	TaskId            uint      `json:"task_id" gorm:"primaryKey;not null"`
	Task              *PingTask `json:"-" gorm:"foreignKey:TaskId;references:Id;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
	ResolutionSeconds int       `json:"resolution_seconds" gorm:"primaryKey;not null"`
	BucketTime        LocalTime `json:"time" gorm:"primaryKey;column:bucket_time;not null"`
	SampleCount       int64     `json:"sample_count" gorm:"not null"`
	SumValue          int64     `json:"sum_value" gorm:"not null"`
	MinValue          int       `json:"min_value" gorm:"not null"`
	MaxValue          int       `json:"max_value" gorm:"not null"`
	LastValue         int       `json:"last_value" gorm:"not null"`
	LastTime          LocalTime `json:"last_time" gorm:"not null"`
}
