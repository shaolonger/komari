// Package contracttest contains reusable behavioral contracts that every
// storage adapter must pass.
package contracttest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/storage"
)

type TelemetryFactory func(*testing.T) storage.TelemetryStore

func Telemetry(t *testing.T, factory TelemetryFactory) {
	t.Helper()
	store := factory(t)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := store.Close(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("close telemetry store: %v", err)
		}
	})
	now := time.Now().UTC().Truncate(5 * time.Minute)
	client := fmt.Sprintf("contract-%d", time.Now().UnixNano())

	t.Run("batch range and aggregate", func(t *testing.T) {
		batch := storage.TelemetryBatch{
			Records: []models.Record{
				{Client: client, Time: models.FromTime(now.Add(time.Minute)), Cpu: 10},
				{Client: client, Time: models.FromTime(now.Add(2 * time.Minute)), Cpu: 30},
			},
			GPURecords: []models.GPURecord{{
				Client: client, Time: models.FromTime(now.Add(time.Minute)),
				DeviceIndex: 0, DeviceName: "contract-gpu", Utilization: 50,
			}},
		}
		if err := store.WriteBatch(context.Background(), batch); err != nil {
			t.Fatal(err)
		}
		records, err := store.QueryRecords(context.Background(), storage.RecordRange{
			Client: client, Start: now, End: now.Add(5 * time.Minute), LoadType: "cpu", MaxPoints: 100,
		})
		if err != nil || len(records) != 2 {
			t.Fatalf("records=%+v err=%v", records, err)
		}
		gpus, err := store.QueryGPURecords(context.Background(), storage.GPURange{
			Client: client, Start: now, End: now.Add(5 * time.Minute), MaxPoints: 100,
		})
		if err != nil || len(gpus) != 1 || gpus[0].DeviceName != "contract-gpu" {
			t.Fatalf("gpu records=%+v err=%v", gpus, err)
		}
		aggregate, err := store.AggregateRecords(context.Background(), storage.AggregateQuery{
			Range: storage.RecordRange{
				Client: client, Start: now, End: now.Add(5 * time.Minute), LoadType: "cpu", MaxPoints: 100,
			},
			Resolution: 5 * time.Minute,
		})
		if err != nil || len(aggregate) != 1 || aggregate[0].Cpu != 30 {
			t.Fatalf("aggregate=%+v err=%v", aggregate, err)
		}
	})

	t.Run("health and retention", func(t *testing.T) {
		health, err := store.Health(context.Background())
		if err != nil || !health.Ready || health.Backend == "" || health.CheckedAt.IsZero() {
			t.Fatalf("health=%+v err=%v", health, err)
		}
		cutoff := now.Add(-30 * 24 * time.Hour)
		result, err := store.ApplyRetention(context.Background(), storage.RetentionPolicy{
			Now: now, FinalCutoff: cutoff,
		})
		if err != nil || !result.FinalCutoff.Equal(cutoff) || result.CompletedAt.IsZero() {
			t.Fatalf("retention=%+v err=%v", result, err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := store.QueryRecords(ctx, storage.RecordRange{
			Client: client, Start: now, End: now.Add(5 * time.Minute), MaxPoints: 100,
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("query error=%v, want context canceled", err)
		}
		err = store.WriteBatch(ctx, storage.TelemetryBatch{Records: []models.Record{{
			Client: client, Time: models.FromTime(now.Add(3 * time.Minute)), Cpu: 40,
		}}})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("write error=%v, want context canceled", err)
		}
	})

	t.Run("concurrent writes", func(t *testing.T) {
		concurrentClient := client + "-concurrent"
		var wait sync.WaitGroup
		errs := make(chan error, 32)
		for index := range 32 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				errs <- store.WriteBatch(context.Background(), storage.TelemetryBatch{
					Records: []models.Record{{
						Client: concurrentClient,
						Time:   models.FromTime(now.Add(time.Duration(index) * time.Second)),
						Cpu:    float32(index),
					}},
				})
			}()
		}
		wait.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		rows, err := store.QueryRecords(context.Background(), storage.RecordRange{
			Client: concurrentClient, Start: now.Add(-time.Second), End: now.Add(time.Minute), MaxPoints: 100,
		})
		if err != nil || len(rows) != 32 {
			t.Fatalf("concurrent rows=%d err=%v", len(rows), err)
		}
	})
}
