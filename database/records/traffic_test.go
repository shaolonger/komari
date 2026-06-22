package records

import (
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
)

func TestSummarizeTrafficRecordsUsesCounterDeltas(t *testing.T) {
	start := time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)
	recs := []models.Record{
		trafficRecord(start, 100, 200, 0, 0),
		trafficRecord(start.Add(time.Hour), 1100, 700, 0, 0),
		trafficRecord(start.Add(2*time.Hour), 3100, 1700, 0, 0),
	}

	stats := SummarizeTrafficRecords(recs, start, start.Add(2*time.Hour), time.Hour)

	if stats.Up != 3000 {
		t.Fatalf("up = %d, want 3000", stats.Up)
	}
	if stats.Down != 1500 {
		t.Fatalf("down = %d, want 1500", stats.Down)
	}
	if stats.Quality != "exact" {
		t.Fatalf("quality = %q, want exact", stats.Quality)
	}
	if len(stats.Series) != 2 {
		t.Fatalf("series len = %d, want 2", len(stats.Series))
	}
	if stats.Series[0].Up != 1000 || stats.Series[0].Down != 500 {
		t.Fatalf("bucket 0 = %+v, want up=1000 down=500", stats.Series[0])
	}
	if stats.Series[1].Up != 2000 || stats.Series[1].Down != 1000 {
		t.Fatalf("bucket 1 = %+v, want up=2000 down=1000", stats.Series[1])
	}
}

func TestSummarizeTrafficRecordsHandlesCounterReset(t *testing.T) {
	start := time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)
	recs := []models.Record{
		trafficRecord(start, 5000, 6000, 0, 0),
		trafficRecord(start.Add(time.Hour), 100, 200, 0, 0),
		trafficRecord(start.Add(2*time.Hour), 400, 500, 0, 0),
	}

	stats := SummarizeTrafficRecords(recs, start, start.Add(2*time.Hour), time.Hour)

	if stats.Up != 400 {
		t.Fatalf("up = %d, want 400", stats.Up)
	}
	if stats.Down != 500 {
		t.Fatalf("down = %d, want 500", stats.Down)
	}
	if stats.Resets != 1 {
		t.Fatalf("resets = %d, want 1", stats.Resets)
	}
	if stats.Quality != "partial" {
		t.Fatalf("quality = %q, want partial", stats.Quality)
	}
}

func TestSummarizeTrafficRecordsFallsBackToRates(t *testing.T) {
	start := time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)
	recs := []models.Record{
		trafficRecord(start, 0, 0, 100, 50),
		trafficRecord(start.Add(time.Minute), 0, 0, 300, 150),
	}

	stats := SummarizeTrafficRecords(recs, start, start.Add(time.Minute), time.Minute)

	if stats.Up != 12000 {
		t.Fatalf("up = %d, want 12000", stats.Up)
	}
	if stats.Down != 6000 {
		t.Fatalf("down = %d, want 6000", stats.Down)
	}
	if !stats.Estimated {
		t.Fatalf("estimated = false, want true")
	}
	if stats.Quality != "estimated" {
		t.Fatalf("quality = %q, want estimated", stats.Quality)
	}
}

func trafficRecord(at time.Time, totalUp, totalDown, rateUp, rateDown int64) models.Record {
	return models.Record{
		Client:       uuid,
		Time:         models.FromTime(at),
		NetTotalUp:   totalUp,
		NetTotalDown: totalDown,
		NetOut:       rateUp,
		NetIn:        rateDown,
	}
}
