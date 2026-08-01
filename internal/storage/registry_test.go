package storage

import (
	"context"
	"sync"
	"testing"

	"github.com/komari-monitor/komari/database/models"
)

type registryTelemetry struct{}

func (registryTelemetry) WriteBatch(context.Context, TelemetryBatch) error { return nil }
func (registryTelemetry) QueryRecords(context.Context, RecordRange) ([]models.Record, error) {
	return nil, nil
}
func (registryTelemetry) QueryGPURecords(context.Context, GPURange) ([]models.GPURecord, error) {
	return nil, nil
}
func (registryTelemetry) QueryPingRecords(context.Context, PingRange) ([]models.PingRecord, error) {
	return nil, nil
}
func (registryTelemetry) AggregateRecords(context.Context, AggregateQuery) ([]models.Record, error) {
	return nil, nil
}
func (registryTelemetry) ApplyRetention(context.Context, RetentionPolicy) (RetentionResult, error) {
	return RetentionResult{}, nil
}
func (registryTelemetry) Health(context.Context) (Health, error) { return Health{}, nil }
func (registryTelemetry) Close(context.Context) error            { return nil }

func TestTelemetryRegistryRestoreAndConcurrency(t *testing.T) {
	store := registryTelemetry{}
	restore, err := InstallTelemetry(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	if got, ok := Telemetry(); !ok || got != store {
		t.Fatalf("installed store=%v ok=%v", got, ok)
	}
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 100 {
				if _, ok := Telemetry(); !ok {
					t.Error("store disappeared during concurrent read")
					return
				}
			}
		}()
	}
	wait.Wait()
	restore()
	restore()
}

func TestRegistryRejectsNilBackends(t *testing.T) {
	if _, err := InstallTelemetry(nil); err == nil {
		t.Fatal("InstallTelemetry(nil) succeeded")
	}
	if _, err := InstallControl(nil); err == nil {
		t.Fatal("InstallControl(nil) succeeded")
	}
}
