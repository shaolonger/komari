package client

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPingResultBatchIsAtomicContiguousAndIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ping-batch.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.PingResultSequence{}); err != nil {
		t.Fatal(err)
	}
	inputs := []pingResultInput{{TaskID: 1, Value: 10, FinishedAt: time.Now()}, {TaskID: 2, Value: -1, FinishedAt: time.Now()}}
	saves := 0
	accepted, err := acceptPingResultBatch(context.Background(), db, "node", 1, inputs, func(records []models.PingRecord) error {
		saves++
		if len(records) != 2 || records[0].Client != "node" || records[1].TaskId != 2 {
			t.Fatalf("records = %#v", records)
		}
		return nil
	})
	if err != nil || accepted.Through != 1 {
		t.Fatalf("acceptance=%#v err=%v", accepted, err)
	}
	accepted, err = acceptPingResultBatch(context.Background(), db, "node", 1, inputs, func([]models.PingRecord) error { saves++; return nil })
	if err != nil || !accepted.Duplicate || saves != 1 {
		t.Fatalf("duplicate=%#v saves=%d err=%v", accepted, saves, err)
	}
	accepted, err = acceptPingResultBatch(context.Background(), db, "node", 3, inputs, func([]models.PingRecord) error { return nil })
	if !errors.Is(err, ErrTelemetrySequenceGap) || accepted.Through != 1 {
		t.Fatalf("gap=%#v err=%v", accepted, err)
	}
}

func TestPingResultBatchRejectsBoundsBeforeSave(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "bounds.db")), &gorm.Config{})
	_ = db.AutoMigrate(&models.PingResultSequence{})
	saved := false
	_, err := acceptPingResultBatch(context.Background(), db, "node", 1, make([]pingResultInput, maxPingResultBatch+1), func([]models.PingRecord) error { saved = true; return nil })
	if err == nil || saved {
		t.Fatal("oversized batch reached storage")
	}
}
