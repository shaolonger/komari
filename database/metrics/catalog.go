package metrics

import (
	"context"
	"errors"
	"sort"

	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	MinRetentionDays = 1
	MaxRetentionDays = 3650
)

type Definition struct {
	Name          string            `json:"name"`
	Description   string            `json:"description,omitempty"`
	Type          string            `json:"type"`
	Unit          string            `json:"unit,omitempty"`
	RetentionDays int               `json:"retention_days"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

var catalog = []Definition{
	{Name: "cpu.usage", Type: "gauge", Unit: "%", RetentionDays: 30},
	{Name: "gpu.usage", Type: "gauge", Unit: "%", RetentionDays: 30},
	{Name: "gpu.device.usage", Type: "gauge", Unit: "%", RetentionDays: 30},
	{Name: "gpu.memory.used", Type: "gauge", Unit: "bytes", RetentionDays: 30},
	{Name: "gpu.memory.total", Type: "gauge", Unit: "bytes", RetentionDays: 30},
	{Name: "gpu.temperature", Type: "gauge", Unit: "degC", RetentionDays: 30},
	{Name: "memory.used", Type: "gauge", Unit: "bytes", RetentionDays: 30},
	{Name: "memory.total", Type: "gauge", Unit: "bytes", RetentionDays: 30},
	{Name: "swap.used", Type: "gauge", Unit: "bytes", RetentionDays: 30},
	{Name: "swap.total", Type: "gauge", Unit: "bytes", RetentionDays: 30},
	{Name: "load.average", Type: "gauge", RetentionDays: 30},
	{Name: "temperature", Type: "gauge", Unit: "degC", RetentionDays: 30},
	{Name: "disk.used", Type: "gauge", Unit: "bytes", RetentionDays: 30},
	{Name: "disk.total", Type: "gauge", Unit: "bytes", RetentionDays: 30},
	{Name: "net.in.rate", Type: "gauge", Unit: "bytes/s", RetentionDays: 30},
	{Name: "net.out.rate", Type: "gauge", Unit: "bytes/s", RetentionDays: 30},
	{Name: "net.total.up", Type: "counter", Unit: "bytes", RetentionDays: 30},
	{Name: "net.total.down", Type: "counter", Unit: "bytes", RetentionDays: 30},
	{Name: "traffic.up", Type: "counter_delta", Unit: "bytes", RetentionDays: 30},
	{Name: "traffic.down", Type: "counter_delta", Unit: "bytes", RetentionDays: 30},
	{Name: "process.count", Type: "gauge", Unit: "count", RetentionDays: 30},
	{Name: "connections.tcp", Type: "gauge", Unit: "count", RetentionDays: 30},
	{Name: "connections.udp", Type: "gauge", Unit: "count", RetentionDays: 30},
	{Name: "ping.latency_ms", Type: "gauge", Unit: "ms", RetentionDays: 730, Metadata: map[string]string{"tag": "task_id"}},
}

func cloneDefinition(source Definition) Definition {
	result := source
	if source.Metadata != nil {
		result.Metadata = make(map[string]string, len(source.Metadata))
		for key, value := range source.Metadata {
			result.Metadata[key] = value
		}
	}
	return result
}

func catalogDefinition(name string) (Definition, bool) {
	position := sort.Search(len(catalog), func(index int) bool { return catalog[index].Name >= name })
	if position < len(catalog) && catalog[position].Name == name {
		return catalog[position], true
	}
	// The declaration order mirrors the UI, not lexical order. Keep lookup
	// correct if that display order changes.
	for _, definition := range catalog {
		if definition.Name == name {
			return definition, true
		}
	}
	return Definition{}, false
}

func Lookup(name string) (Definition, bool) {
	definition, ok := catalogDefinition(name)
	return cloneDefinition(definition), ok
}

func List(ctx context.Context, db *gorm.DB) ([]Definition, error) {
	if ctx == nil || db == nil {
		return nil, errors.New("metric catalog context and database are required")
	}
	var policies []models.MetricRetentionPolicy
	if err := db.WithContext(ctx).Find(&policies).Error; err != nil {
		return nil, err
	}
	overrides := make(map[string]int, len(policies))
	for _, policy := range policies {
		overrides[policy.Name] = policy.RetentionDays
	}
	result := make([]Definition, len(catalog))
	for index, definition := range catalog {
		result[index] = cloneDefinition(definition)
		if days, ok := overrides[definition.Name]; ok {
			result[index].RetentionDays = days
		}
	}
	return result, nil
}

func UpdateRetention(ctx context.Context, db *gorm.DB, name string, days int) (Definition, error) {
	definition, ok := catalogDefinition(name)
	if !ok {
		return Definition{}, errors.New("unknown metric definition")
	}
	if days < MinRetentionDays || days > MaxRetentionDays {
		return Definition{}, errors.New("retention_days must be between 1 and 3650")
	}
	policy := models.MetricRetentionPolicy{Name: name, RetentionDays: days}
	if err := db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{"retention_days", "updated_at"}),
	}).Create(&policy).Error; err != nil {
		return Definition{}, err
	}
	definition = cloneDefinition(definition)
	definition.RetentionDays = days
	return definition, nil
}
