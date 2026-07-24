//go:build integration && scale

package telemetrystore

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/storage"
	"github.com/komari-monitor/komari/internal/storage/contracttest"
)

func TestClickHouseTelemetryStoreIntegration(t *testing.T) {
	address := os.Getenv("KOMARI_TEST_CLICKHOUSE_ADDR")
	if address == "" {
		t.Skip("KOMARI_TEST_CLICKHOUSE_ADDR is not set")
	}
	prefix := fmt.Sprintf("kt%d_", time.Now().UnixNano()%1_000_000_000)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store, err := NewClickHouse(ctx, ClickHouseConfig{
		Addresses: []string{address},
		Database:  "default", Username: "default",
		TablePrefix: prefix, MaxOpenConns: 8, MaxIdleConns: 4,
		MaxRetries: 2, RetryBackoff: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	contracttest.Telemetry(t, func(*testing.T) storage.TelemetryStore { return store })
	t.Cleanup(func() {
		for _, suffix := range []string{"records", "gpu_records", "ping_records", "schema_migrations"} {
			_ = store.conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+store.table(suffix))
		}
	})

	t.Run("idempotent batch token", func(t *testing.T) {
		now := time.Now().UTC()
		batch := storage.TelemetryBatch{
			ID: "idempotent-integration-batch",
			Records: []models.Record{{
				Client: "idempotent", Time: models.FromTime(now), Cpu: 1,
			}},
		}
		if err := store.WriteBatch(context.Background(), batch); err != nil {
			t.Fatal(err)
		}
		if err := store.WriteBatch(context.Background(), batch); err != nil {
			t.Fatal(err)
		}
		var persisted uint64
		if err := store.conn.QueryRow(context.Background(),
			"SELECT count() FROM "+store.table("records")+" WHERE client = ?",
			"idempotent",
		).Scan(&persisted); err != nil {
			t.Fatal(err)
		}
		if persisted != 1 {
			t.Fatalf("persisted rows=%d, want exactly one after duplicate batch", persisted)
		}
		rows, err := store.QueryRecords(context.Background(), storage.RecordRange{
			Client: "idempotent", Start: now.Add(-time.Second), End: now.Add(time.Second), MaxPoints: 10,
		})
		if err != nil || len(rows) != 1 {
			t.Fatalf("rows=%+v err=%v", rows, err)
		}
	})

	t.Run("query hard limit", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Second)
		batch := storage.TelemetryBatch{ID: "limit-batch"}
		for index := range 4 {
			batch.Records = append(batch.Records, models.Record{
				Client: "limit", Time: models.FromTime(now.Add(time.Duration(index) * time.Millisecond)),
			})
		}
		if err := store.WriteBatch(context.Background(), batch); err != nil {
			t.Fatal(err)
		}
		_, err := store.QueryRecords(context.Background(), storage.RecordRange{
			Client: "limit", Start: now.Add(-time.Second), End: now.Add(time.Second), MaxPoints: 3,
		})
		if !errors.Is(err, storage.ErrQueryLimit) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("idempotent migration", func(t *testing.T) {
		if err := store.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("ten thousand row batch", func(t *testing.T) {
		started := time.Now()
		batch := storage.TelemetryBatch{ID: "stress-10k"}
		for index := range 10_000 {
			batch.Records = append(batch.Records, models.Record{
				Client: "stress",
				Time:   models.FromTime(started.Add(time.Duration(index) * time.Millisecond)),
				Cpu:    float32(index % 100),
			})
		}
		if err := store.WriteBatch(context.Background(), batch); err != nil {
			t.Fatal(err)
		}
		rows, err := store.QueryRecords(context.Background(), storage.RecordRange{
			Client: "stress", Start: started.Add(-time.Second),
			End: started.Add(11 * time.Second), MaxPoints: 10_000,
		})
		if err != nil || len(rows) != 10_000 {
			t.Fatalf("rows=%d err=%v", len(rows), err)
		}
		t.Logf("10k write+read duration=%s", time.Since(started))
	})
}

func TestClickHouseTelemetryStoreTLSIntegration(t *testing.T) {
	address := os.Getenv("KOMARI_TEST_CLICKHOUSE_TLS_ADDR")
	caPath := os.Getenv("KOMARI_TEST_TLS_CA")
	if address == "" || caPath == "" {
		t.Skip("ClickHouse TLS integration environment is not set")
	}
	pem, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		t.Fatal("test CA is invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := NewClickHouse(ctx, ClickHouseConfig{
		Addresses: []string{address}, Database: "default", Username: "default",
		TablePrefix: "kttls_", RequireTLS: true,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12, ServerName: "localhost", RootCAs: roots,
		},
		MaxOpenConns: 2, MaxIdleConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	health, err := store.Health(ctx)
	if err != nil || !health.Ready {
		t.Fatalf("health=%+v err=%v", health, err)
	}
}

func TestClickHouseTelemetryStoreOutageRecovery(t *testing.T) {
	address := os.Getenv("KOMARI_TEST_CLICKHOUSE_ADDR")
	container := os.Getenv("KOMARI_TEST_CLICKHOUSE_CONTAINER")
	if address == "" || container == "" {
		t.Skip("ClickHouse outage integration environment is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := NewClickHouse(ctx, ClickHouseConfig{
		Addresses: []string{address}, Database: "default", Username: "default",
		TablePrefix: "ktrecovery_", MaxOpenConns: 2, MaxIdleConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	if output, err := exec.Command("docker", "stop", "--time", "5", container).CombinedOutput(); err != nil {
		t.Fatalf("stop ClickHouse: %v: %s", err, output)
	}
	failureCtx, failureCancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, healthErr := store.Health(failureCtx)
	failureCancel()
	if healthErr == nil {
		t.Fatal("health remained ready while ClickHouse was stopped")
	}
	if output, err := exec.Command("docker", "start", container).CombinedOutput(); err != nil {
		t.Fatalf("start ClickHouse: %v: %s", err, output)
	}
	deadline := time.Now().Add(30 * time.Second)
	consecutive := 0
	for consecutive < 3 {
		healthCtx, healthCancel := context.WithTimeout(context.Background(), 2*time.Second)
		health, err := store.Health(healthCtx)
		healthCancel()
		if err == nil && health.Ready {
			consecutive++
		} else {
			consecutive = 0
		}
		if time.Now().After(deadline) {
			t.Fatalf("ClickHouse did not recover: health=%+v err=%v", health, err)
		}
		time.Sleep(time.Second)
	}
}
