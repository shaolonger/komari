package records

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
)

func TestRecordQueryPlannerSplitsFiveHourBoundaryWithoutOverlap(t *testing.T) {
	db := newCompactionTestDB(t)
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	rows := []struct {
		table  string
		record models.Record
	}{
		{"records_long_term", models.Record{Client: "node", Time: models.FromTime(now.Add(-5 * time.Hour)), Cpu: 5}},
		{"records_long_term", models.Record{Client: "node", Time: models.FromTime(now.Add(-4*time.Hour - time.Minute)), Cpu: 15}},
		// This overlap row is intentionally different and must be excluded at the
		// exact stable boundary in favor of minute data.
		{"records_long_term", models.Record{Client: "node", Time: models.FromTime(now.Add(-4 * time.Hour)), Cpu: 999}},
		{"records", models.Record{Client: "node", Time: models.FromTime(now.Add(-4*time.Hour - time.Minute)), Cpu: 777}},
		{"records", models.Record{Client: "node", Time: models.FromTime(now.Add(-4 * time.Hour)), Cpu: 20}},
		{"records", models.Record{Client: "node", Time: models.FromTime(now.Add(-time.Hour)), Cpu: 30}},
	}
	for _, row := range rows {
		if err := db.Table(row.table).Create(&row.record).Error; err != nil {
			t.Fatal(err)
		}
	}
	got, segments, err := executeRecordQuery(context.Background(), db, RecordQuery{
		Client: "node", Start: now.Add(-5 * time.Hour), End: now, LoadType: "cpu", MaxPoints: 1_000,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 2 || segments[0].Table != "records_long_term" || segments[1].Table != "records" {
		t.Fatalf("segments=%+v", segments)
	}
	if !segments[0].End.Equal(segments[1].Start) || !segments[0].End.Equal(now.Add(-4*time.Hour)) {
		t.Fatalf("non-contiguous split=%+v", segments)
	}
	if len(got) != 4 {
		t.Fatalf("rows=%+v, want 4", got)
	}
	wantCPU := []float32{5, 15, 20, 30}
	for index := range wantCPU {
		if got[index].Cpu != wantCPU[index] {
			t.Fatalf("row %d cpu=%v, want %v; rows=%+v", index, got[index].Cpu, wantCPU[index], got)
		}
	}
}

func TestRecordQueryPlannerSelectsHourlyTierForLongBudget(t *testing.T) {
	db := newCompactionTestDB(t)
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	old := now.Add(-20 * 24 * time.Hour).Truncate(time.Hour)
	for _, item := range []struct {
		table string
		cpu   float32
	}{{"records_hourly", 40}, {"records_long_term", 999}} {
		record := models.Record{Client: "node", Time: models.FromTime(old), Cpu: item.cpu}
		if err := db.Table(item.table).Create(&record).Error; err != nil {
			t.Fatal(err)
		}
	}
	recent := models.Record{Client: "node", Time: models.FromTime(now.Add(-time.Hour)), Cpu: 50}
	if err := db.Create(&recent).Error; err != nil {
		t.Fatal(err)
	}
	got, segments, err := executeRecordQuery(context.Background(), db, RecordQuery{
		Client: "node", Start: now.Add(-30 * 24 * time.Hour), End: now, LoadType: "cpu", MaxPoints: 100,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 2 || segments[0].Tier != RecordTierHour || segments[1].Aggregate != time.Hour {
		t.Fatalf("segments=%+v", segments)
	}
	if len(got) != 2 || got[0].Cpu != 40 || got[1].Cpu != 50 {
		t.Fatalf("hourly query rows=%+v", got)
	}
}

func TestRecordQueryPlannerEmptySortDeduplicateAndErrors(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	t.Run("empty", func(t *testing.T) {
		db := newCompactionTestDB(t)
		got, _, err := executeRecordQuery(context.Background(), db, RecordQuery{Start: now.Add(-time.Hour), End: now}, now)
		if err != nil || len(got) != 0 {
			t.Fatalf("empty query rows=%+v err=%v", got, err)
		}
	})
	t.Run("sort and deduplicate", func(t *testing.T) {
		db := newCompactionTestDB(t)
		at := now.Add(-time.Hour)
		rows := []models.Record{
			{Client: "b", Time: models.FromTime(at.Add(time.Minute)), Cpu: 1},
			{Client: "a", Time: models.FromTime(at), Cpu: 2},
			{Client: "a", Time: models.FromTime(at), Cpu: 3},
		}
		if err := db.Create(&rows).Error; err != nil {
			t.Fatal(err)
		}
		got, _, err := executeRecordQuery(context.Background(), db, RecordQuery{Start: now.Add(-2 * time.Hour), End: now}, now)
		if err != nil || len(got) != 2 || got[0].Client != "a" || got[1].Client != "b" {
			t.Fatalf("ordered unique rows=%+v err=%v", got, err)
		}
	})
	t.Run("segment error propagates", func(t *testing.T) {
		db := newCompactionTestDB(t)
		if err := db.Exec("DROP TABLE records_long_term").Error; err != nil {
			t.Fatal(err)
		}
		_, _, err := executeRecordQuery(context.Background(), db, RecordQuery{
			Start: now.Add(-6 * time.Hour), End: now, MaxPoints: 1_000,
		}, now)
		if err == nil || !strings.Contains(err.Error(), "records_long_term") {
			t.Fatalf("segment error=%v", err)
		}
	})
}

func TestRecordQueryPlanIsContiguousAcrossDST(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 31, 0, 0, 0, 0, location)
	end := time.Date(2026, 11, 2, 0, 0, 0, 0, location)
	now := end.Add(8 * time.Hour)
	segments, err := PlanRecordQuery(start, end, now, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) == 0 || !segments[0].Start.Equal(start) || !segments[len(segments)-1].End.Equal(end) {
		t.Fatalf("DST plan does not cover range: %+v", segments)
	}
	for index := 1; index < len(segments); index++ {
		if !segments[index-1].End.Equal(segments[index].Start) {
			t.Fatalf("DST gap/overlap between %+v and %+v", segments[index-1], segments[index])
		}
	}
	if end.Sub(start) != 49*time.Hour {
		t.Fatalf("test range duration=%s, want 49h", end.Sub(start))
	}
}

func TestHourlyQueryPlanUsesCompositeIndex(t *testing.T) {
	db := newCompactionTestDB(t)
	var rows []struct{ Detail string }
	if err := db.Raw("EXPLAIN QUERY PLAN SELECT * FROM records_hourly WHERE client=? AND time>=? AND time<? ORDER BY time", "node", "2026-01-01", "2026-02-01").Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	detail := ""
	for _, row := range rows {
		detail += row.Detail
	}
	if !strings.Contains(detail, "idx_records_hourly_bucket") {
		t.Fatalf("query plan=%q", detail)
	}
}

func BenchmarkPlanRecordQuery(b *testing.B) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	start := now.Add(-365 * 24 * time.Hour)
	b.ReportAllocs()
	for range b.N {
		segments, err := PlanRecordQuery(start, now, now, 4_000)
		if err != nil || len(segments) != 2 {
			b.Fatalf("segments=%+v err=%v", segments, err)
		}
	}
}
