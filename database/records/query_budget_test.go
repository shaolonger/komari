package records

import (
	"errors"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
)

func TestValidateQueryBudgetBoundariesAndPermissions(t *testing.T) {
	end := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		start, end time.Time
		nodes      int
		points     int
		permission QueryPermission
		want       int
		wantErr    error
	}{
		{"public default", end.Add(-time.Hour), end, 1, 0, QueryPermissionPublic, 4_000, nil},
		{"legacy unlimited becomes hard limit", end.Add(-time.Hour), end, 1, -1, QueryPermissionPublic, 20_000, nil},
		{"public exact window", end.Add(-366 * 24 * time.Hour), end, 1, 20_000, QueryPermissionPublic, 20_000, nil},
		{"public window overflow", end.Add(-366*24*time.Hour - time.Nanosecond), end, 1, 1, QueryPermissionPublic, 0, ErrQueryWindow},
		{"admin wider window", end.Add(-2 * 366 * 24 * time.Hour), end, 1, 50_000, QueryPermissionAdmin, 50_000, nil},
		{"point overflow", end.Add(-time.Hour), end, 1, 20_001, QueryPermissionPublic, 0, ErrQueryPointBudget},
		{"node overflow", end.Add(-time.Hour), end, 10_001, 10, QueryPermissionPublic, 0, ErrQueryNodeBudget},
		{"reversed", end, end.Add(-time.Hour), 1, 10, QueryPermissionPublic, 0, ErrInvalidQueryRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateQueryBudget(tt.start, tt.end, tt.nodes, tt.points, tt.permission)
			if got != tt.want || !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateQueryBudget()=(%d,%v), want (%d,%v)", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestRecordProjectionUsesStrictAllowlist(t *testing.T) {
	tests := map[string]string{
		"": "*", "all": "*", "cpu": "client,time,cpu", "ram": "client,time,ram,ram_total",
		"network": "client,time,net_in,net_out,net_total_up,net_total_down",
	}
	for loadType, want := range tests {
		got, err := RecordProjection(loadType)
		if err != nil || got != want {
			t.Fatalf("RecordProjection(%q)=(%q,%v), want %q", loadType, got, err, want)
		}
	}
	for _, invalid := range []string{"unknown", "cpu,passwd", "cpu FROM users", "gpu;DROP TABLE records"} {
		if _, err := RecordProjection(invalid); !errors.Is(err, ErrInvalidLoadType) {
			t.Fatalf("RecordProjection(%q) error=%v", invalid, err)
		}
	}
}

func TestDownsampleRecordsPreservesEndpointsAndSpike(t *testing.T) {
	start := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	records := make([]models.Record, 1_001)
	for index := range records {
		records[index] = models.Record{Client: "node", Time: models.FromTime(start.Add(time.Duration(index) * time.Second)), Cpu: 10}
	}
	records[527].Cpu = 100
	got := DownsampleRecords(records, 50, "cpu")
	if len(got) != 50 {
		t.Fatalf("sampled points=%d, want 50", len(got))
	}
	if !got[0].Time.ToTime().Equal(records[0].Time.ToTime()) || !got[len(got)-1].Time.ToTime().Equal(records[len(records)-1].Time.ToTime()) {
		t.Fatalf("endpoints not preserved: first=%s last=%s", got[0].Time.ToTime(), got[len(got)-1].Time.ToTime())
	}
	foundSpike := false
	for index := range got {
		if got[index].Cpu == 100 {
			foundSpike = true
		}
		if index > 0 && got[index].Time.ToTime().Before(got[index-1].Time.ToTime()) {
			t.Fatal("downsampled records are not ordered")
		}
	}
	if !foundSpike {
		t.Fatal("LTTB discarded the only CPU spike")
	}
}

func TestDownsampleRecordsSmallBudgets(t *testing.T) {
	start := time.Now()
	input := []models.Record{
		{Time: models.FromTime(start), Cpu: 1},
		{Time: models.FromTime(start.Add(time.Minute)), Cpu: 2},
		{Time: models.FromTime(start.Add(2 * time.Minute)), Cpu: 3},
	}
	if got := DownsampleRecords(input, 0, "cpu"); len(got) != 0 {
		t.Fatalf("zero budget returned %d points", len(got))
	}
	if got := DownsampleRecords(input, 1, "cpu"); len(got) != 1 || got[0].Cpu != 3 {
		t.Fatalf("one point=%+v", got)
	}
	if got := DownsampleRecords(input, 2, "cpu"); len(got) != 2 || got[0].Cpu != 1 || got[1].Cpu != 3 {
		t.Fatalf("two points=%+v", got)
	}
}

func BenchmarkLTTBDownsample100kTo2k(b *testing.B) {
	start := time.Unix(1_700_000_000, 0)
	input := make([]models.Record, 100_000)
	for index := range input {
		input[index] = models.Record{
			Time: models.FromTime(start.Add(time.Duration(index) * time.Second)),
			Cpu:  float32(index % 100),
		}
	}
	b.ReportAllocs()
	for range b.N {
		if got := DownsampleRecords(input, 2_000, "cpu"); len(got) != 2_000 {
			b.Fatal(len(got))
		}
	}
}
