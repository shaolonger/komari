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

func openTelemetryV3TestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "v3.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.TelemetryV3Sequence{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestTelemetryV3AcceptanceIsContiguousAndOnlyAcknowledgesDurableData(t *testing.T) {
	db := openTelemetryV3TestDB(t)
	frame := telemetryv3.Frame{
		Sequence: 1, SampledAt: time.Now(), Checkpoint: true,
		Envelope: telemetryv3.Envelope{Count: 1, CPUMin: 10, CPUMax: 10, CPUSum: 10},
		Latest:   telemetryv2.Report{CPUUsage: 10},
	}
	saves := 0
	forgetTelemetrySequenceState("node-v3")
	defer forgetTelemetrySequenceState("node-v3")
	save := func(client string, report common.Report, _ telemetryv3.Frame) error {
		saves++
		if client != "node-v3" || report.UUID != "node-v3" || report.CPU.Usage != 10 {
			t.Fatalf("save input = %q %#v", client, report)
		}
		return nil
	}
	accepted, err := acceptTelemetryV3(context.Background(), db, "node-v3", frame, common.Report{CPU: common.CPUReport{Usage: 10}}, save)
	if err != nil || accepted.Through != 0 || accepted.AcceptedThrough != 1 || accepted.Duplicate {
		t.Fatalf("acceptance = %#v, err=%v", accepted, err)
	}
	accepted, err = acceptTelemetryV3(context.Background(), db, "node-v3", frame, common.Report{}, save)
	if err != nil || accepted.Through != 0 || !accepted.Duplicate || saves != 1 {
		t.Fatalf("duplicate acceptance = %#v, saves=%d err=%v", accepted, saves, err)
	}
	frame.Sequence = 3
	frame.Checkpoint = false
	accepted, err = acceptTelemetryV3(context.Background(), db, "node-v3", frame, common.Report{}, save)
	if !errors.Is(err, ErrTelemetrySequenceGap) || accepted.Expected != 2 || saves != 1 {
		t.Fatalf("gap acceptance = %#v, saves=%d err=%v", accepted, saves, err)
	}
	var count int64
	if err := db.Model(&models.TelemetryV3Sequence{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("unflushed checkpoint count = %d, err=%v", count, err)
	}
	checkpoint := models.TelemetryV3Sequence{Client: "node-v3", ThroughSequence: 1, UpdatedAt: time.Now()}
	if err := db.Create(&checkpoint).Error; err != nil {
		t.Fatal(err)
	}
	markTelemetrySequencesDurable([]models.TelemetryV3Sequence{checkpoint})
	frame.Sequence = 1
	accepted, err = acceptTelemetryV3(context.Background(), db, "node-v3", frame, common.Report{}, save)
	if err != nil || accepted.Through != 1 || !accepted.Duplicate {
		t.Fatalf("durable duplicate acceptance = %#v, err=%v", accepted, err)
	}
}

func TestTelemetryV3DoesNotCheckpointFailedSave(t *testing.T) {
	db := openTelemetryV3TestDB(t)
	forgetTelemetrySequenceState("node")
	defer forgetTelemetrySequenceState("node")
	frame := telemetryv3.Frame{Sequence: 1, SampledAt: time.Now(), Envelope: telemetryv3.Envelope{Count: 1}}
	want := errors.New("save failed")
	_, err := acceptTelemetryV3(context.Background(), db, "node", frame, common.Report{}, func(string, common.Report, telemetryv3.Frame) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("save error = %v", err)
	}
	var count int64
	if err := db.Model(&models.TelemetryV3Sequence{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("checkpoint count = %d, err=%v", count, err)
	}
}

func TestTelemetryV3CheckpointRepairsUnrecoverableLegacySpoolGap(t *testing.T) {
	db := openTelemetryV3TestDB(t)
	forgetTelemetrySequenceState("node-v3-gap")
	defer forgetTelemetrySequenceState("node-v3-gap")
	if err := db.Create(&models.TelemetryV3Sequence{Client: "node-v3-gap", ThroughSequence: 4, UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	frame := telemetryv3.Frame{Sequence: 9, SampledAt: time.Now(), Checkpoint: true, Envelope: telemetryv3.Envelope{Count: 1}}
	saved := 0
	acceptance, err := acceptTelemetryV3(context.Background(), db, "node-v3-gap", frame, common.Report{}, func(string, common.Report, telemetryv3.Frame) error {
		saved++
		return nil
	})
	if err != nil || acceptance.AcceptedThrough != 9 || acceptance.Through != 4 || saved != 1 {
		t.Fatalf("checkpoint rebase = %+v saved=%d err=%v", acceptance, saved, err)
	}
	frame.Sequence = 11
	frame.Checkpoint = false
	acceptance, err = acceptTelemetryV3(context.Background(), db, "node-v3-gap", frame, common.Report{}, func(string, common.Report, telemetryv3.Frame) error { return nil })
	if !errors.Is(err, ErrTelemetrySequenceGap) || acceptance.Expected != 10 {
		t.Fatalf("non-checkpoint gap = %+v err=%v", acceptance, err)
	}
}
