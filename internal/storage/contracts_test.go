package storage

import (
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
)

func TestBatchIDIsDeterministicAndExplicitlyOverrideable(t *testing.T) {
	batch := TelemetryBatch{Records: []models.Record{{
		Client: "node", Time: models.FromTime(time.Unix(123, 0)), Cpu: 42,
	}}}
	first, err := BatchID(batch)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BatchID(batch)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != 64 {
		t.Fatalf("first=%q second=%q", first, second)
	}
	batch.ID = "caller-id"
	if got, err := BatchID(batch); err != nil || got != batch.ID {
		t.Fatalf("id=%q err=%v", got, err)
	}
	batch.ID = string(make([]byte, 129))
	if _, err := BatchID(batch); err == nil {
		t.Fatal("oversized ID accepted")
	}
}
