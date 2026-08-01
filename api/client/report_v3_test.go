package client

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/protocol/telemetryv2"
	"github.com/komari-monitor/komari/protocol/telemetryv3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestTelemetryV3AcceptanceIsContiguousDurableAndIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "v3.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.TelemetryV3Sequence{}); err != nil {
		t.Fatal(err)
	}
	frame := telemetryv3.Frame{
		Sequence: 1, SampledAt: time.Now(), Checkpoint: true,
		Envelope: telemetryv3.Envelope{Count: 1, CPUMin: 10, CPUMax: 10, CPUSum: 10},
		Latest:   telemetryv2.Report{CPUUsage: 10},
	}
	saves := 0
	save := func(client string, report common.Report) error {
		saves++
		if client != "node-v3" || report.UUID != "node-v3" || report.CPU.Usage != 10 {
			t.Fatalf("save input = %q %#v", client, report)
		}
		return nil
	}
	accepted, err := acceptTelemetryV3(context.Background(), db, "node-v3", frame, common.Report{CPU: common.CPUReport{Usage: 10}}, save)
	if err != nil || accepted.Through != 1 || accepted.Duplicate {
		t.Fatalf("acceptance = %#v, err=%v", accepted, err)
	}
	accepted, err = acceptTelemetryV3(context.Background(), db, "node-v3", frame, common.Report{}, save)
	if err != nil || !accepted.Duplicate || saves != 1 {
		t.Fatalf("duplicate acceptance = %#v, saves=%d err=%v", accepted, saves, err)
	}
	frame.Sequence = 3
	accepted, err = acceptTelemetryV3(context.Background(), db, "node-v3", frame, common.Report{}, save)
	if !errors.Is(err, ErrTelemetrySequenceGap) || accepted.Through != 1 || saves != 1 {
		t.Fatalf("gap acceptance = %#v, saves=%d err=%v", accepted, saves, err)
	}
	var checkpoint models.TelemetryV3Sequence
	if err := db.Take(&checkpoint, "client = ?", "node-v3").Error; err != nil || checkpoint.ThroughSequence != 1 {
		t.Fatalf("checkpoint = %#v, err=%v", checkpoint, err)
	}
}

func TestTelemetryV3DoesNotCheckpointFailedSave(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "v3-fail.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.AutoMigrate(&models.TelemetryV3Sequence{})
	frame := telemetryv3.Frame{Sequence: 1, SampledAt: time.Now(), Envelope: telemetryv3.Envelope{Count: 1}}
	want := errors.New("save failed")
	_, err = acceptTelemetryV3(context.Background(), db, "node", frame, common.Report{}, func(string, common.Report) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("save error = %v", err)
	}
	var count int64
	if err := db.Model(&models.TelemetryV3Sequence{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("checkpoint count = %d, err=%v", count, err)
	}
}
