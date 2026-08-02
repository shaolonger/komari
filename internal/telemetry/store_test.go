package telemetry

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/protocol/telemetryv3"
	"github.com/komari-monitor/komari/utils"
)

func sampleReport(at time.Time, value int) common.Report {
	return common.Report{
		UpdatedAt: at,
		CPU:       common.CPUReport{Usage: float64(value)},
		Ram:       common.RamReport{Total: 16 << 30, Used: int64(value) << 20},
		Swap:      common.RamReport{Total: 4 << 30, Used: int64(value) << 18},
		Load:      common.LoadReport{Load1: float64(value)},
		Disk:      common.DiskReport{Total: 256 << 30, Used: int64(value) << 21},
		Network: common.NetworkReport{
			Up: int64(value * 101), Down: int64(value * 211),
			TotalUp: int64(value) << 30, TotalDown: int64(value) << 31,
		},
		Connections: common.ConnectionsReport{TCP: value, UDP: value / 2},
		Process:     value * 2,
		GPU: &common.GPUDetailReport{
			Count: 2, AverageUsage: float64(value),
			DetailedInfo: []common.GPUDeviceInfo{
				{Name: "GPU-0", MemoryTotal: 24 << 30, MemoryUsed: int64(value) << 20, Utilization: float64(value), Temperature: 40 + value},
				{Name: "GPU-1", MemoryTotal: 48 << 30, MemoryUsed: int64(value) << 21, Utilization: float64(value + 1), Temperature: 41 + value},
			},
		},
	}
}

func TestAccumulatorMatchesLegacyAggregation(t *testing.T) {
	store := NewStore()
	base := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	reports := make([]common.Report, 60)
	for i := range reports {
		reports[i] = sampleReport(base.Add(time.Duration(i)*time.Second), i+1)
		if err := store.Add("node", reports[i]); err != nil {
			t.Fatal(err)
		}
	}
	aggregates := store.DrainBefore(base.Add(time.Minute))
	if len(aggregates) != 1 {
		t.Fatalf("aggregates = %d, want 1", len(aggregates))
	}
	at := base.Add(time.Minute)
	legacyReports := append([]common.Report(nil), reports...)
	wantRecord := utils.AverageReport("node", at, legacyReports, topFraction)
	wantGPU := utils.AverageGPUReports("node", at, reports, topFraction)
	sort.Slice(wantGPU, func(i, j int) bool { return wantGPU[i].DeviceIndex < wantGPU[j].DeviceIndex })
	if !reflect.DeepEqual(aggregates[0].Record, wantRecord) {
		t.Fatalf("record mismatch\nnew:  %+v\nlegacy: %+v", aggregates[0].Record, wantRecord)
	}
	if !reflect.DeepEqual(aggregates[0].GPURecords, wantGPU) {
		t.Fatalf("GPU mismatch\nnew:  %+v\nlegacy: %+v", aggregates[0].GPURecords, wantGPU)
	}
}

func TestConcurrentSameAndDifferentUUID(t *testing.T) {
	store := NewStore()
	base := time.Now().Truncate(time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < MaxSamplesPerMinute; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := store.Add("shared", sampleReport(base.Add(time.Duration(i)*time.Millisecond), i)); err != nil {
				t.Errorf("shared add: %v", err)
			}
		}(i)
	}
	for i := 0; i < 512; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := store.Add(fmt.Sprintf("node-%04d", i), sampleReport(base, i)); err != nil {
				t.Errorf("node add: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if store.NodeCount() != 513 {
		t.Fatalf("node count = %d, want 513", store.NodeCount())
	}
	if got := len(store.DrainBefore(base.Add(time.Minute))); got != 513 {
		t.Fatalf("aggregate count = %d, want 513", got)
	}
}

func TestMinuteBoundaryAndBoundedBacklog(t *testing.T) {
	store := NewStore()
	base := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	if err := store.Add("node", sampleReport(base.Add(59*time.Second), 1)); err != nil {
		t.Fatal(err)
	}
	if err := store.Add("node", sampleReport(base.Add(time.Minute), 2)); err != nil {
		t.Fatal(err)
	}
	first := store.DrainBefore(base.Add(time.Minute))
	if len(first) != 1 || !first[0].WindowStart.Equal(base) {
		t.Fatalf("unexpected first drain: %+v", first)
	}
	second := store.DrainBefore(base.Add(2 * time.Minute))
	if len(second) != 1 || !second[0].WindowStart.Equal(base.Add(time.Minute)) {
		t.Fatalf("unexpected second drain: %+v", second)
	}

	backlogged := NewStore()
	for index := 0; index < MaxPendingWindowsPerNode; index++ {
		if err := backlogged.Add("node", sampleReport(base.Add(time.Duration(index)*time.Minute), index)); err != nil {
			t.Fatalf("window %d: %v", index, err)
		}
	}
	if err := backlogged.Add("node", sampleReport(base.Add(MaxPendingWindowsPerNode*time.Minute), 999)); !errors.Is(err, ErrWindowBacklog) {
		t.Fatalf("overflow undrained window error = %v", err)
	}
}

func TestSnapshotIsImmutableAndExpires(t *testing.T) {
	store := NewStore()
	now := time.Date(2026, 7, 22, 12, 0, 30, 0, time.UTC)
	store.now = func() time.Time { return now }
	report := sampleReport(now, 5)
	if err := store.Add("node", report); err != nil {
		t.Fatal(err)
	}
	report.GPU.DetailedInfo[0].Name = "mutated-source"
	first, ok := store.Latest("node")
	if !ok || first.GPU.DetailedInfo[0].Name != "GPU-0" {
		t.Fatalf("source mutation escaped into snapshot: %+v", first.GPU)
	}
	first.GPU.DetailedInfo[0].Name = "mutated-return"
	second, ok := store.Latest("node")
	if !ok || second.GPU.DetailedInfo[0].Name != "GPU-0" {
		t.Fatalf("returned mutation escaped into snapshot: %+v", second.GPU)
	}
	now = now.Add(SnapshotTTL + time.Nanosecond)
	if _, ok := store.Latest("node"); ok {
		t.Fatal("expired snapshot remained visible")
	}
	store.DrainBefore(now.Add(time.Minute))
	if store.NodeCount() != 0 {
		t.Fatal("expired, drained node was not pruned")
	}
}

func TestRemoveDiscardsSnapshotAndPendingWindows(t *testing.T) {
	store := NewStore()
	base := time.Now().Truncate(time.Minute)
	if err := store.Add("deleted-node", sampleReport(base, 1)); err != nil {
		t.Fatal(err)
	}
	store.Remove("deleted-node")
	if _, ok := store.Latest("deleted-node"); ok || store.NodeCount() != 0 {
		t.Fatal("deleted node remained in the telemetry store")
	}
	if aggregates := store.DrainBefore(base.Add(time.Minute)); len(aggregates) != 0 {
		t.Fatalf("deleted node produced %d durable aggregates", len(aggregates))
	}
}

func TestV3EnvelopePreservesCounterDeltasAcrossAgentCounterReset(t *testing.T) {
	store := NewStore()
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	first := sampleReport(base.Add(5*time.Second), 1)
	first.Network.Up, first.Network.Down = 0, 0
	first.Network.TotalUp, first.Network.TotalDown = 1_000, 2_000
	if err := store.AddV3("node", first, telemetryv3.Envelope{
		Count: 1, CPUMin: 1, CPUMax: 1, CPUSum: 1, RAMUsedMin: 1 << 20, RAMUsedMax: 1 << 20, RAMUsedSum: 1 << 20,
	}, 1); err != nil {
		t.Fatal(err)
	}
	second := sampleReport(base.Add(10*time.Second), 2)
	// The physical counters reset to small values, but the envelope retains all
	// bytes transferred since the preceding v3 frame.
	second.Network.TotalUp, second.Network.TotalDown = 50, 70
	if err := store.AddV3("node", second, telemetryv3.Envelope{
		Count: 1, CPUMin: 2, CPUMax: 2, CPUSum: 2, RAMUsedMin: 2 << 20, RAMUsedMax: 2 << 20, RAMUsedSum: 2 << 20,
		NetworkUpDelta: 500, NetworkDownDelta: 750,
	}, 2); err != nil {
		t.Fatal(err)
	}
	aggregates := store.DrainBefore(base.Add(time.Minute))
	if len(aggregates) != 1 {
		t.Fatalf("aggregates = %d", len(aggregates))
	}
	record := aggregates[0].Record
	if record.NetTotalUp != 1_500 || record.NetTotalDown != 2_750 || record.NetOut != 100 || record.NetIn != 150 || aggregates[0].Sequence != 2 {
		t.Fatalf("v3 network aggregate = %+v sequence=%d", record, aggregates[0].Sequence)
	}
}

func TestAccumulatorLimits(t *testing.T) {
	store := NewStore()
	base := time.Now().Truncate(time.Minute)
	for i := 0; i < MaxSamplesPerMinute; i++ {
		if err := store.Add("node", sampleReport(base, i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Add("node", sampleReport(base, 999)); !errors.Is(err, ErrSampleLimit) {
		t.Fatalf("sample limit error = %v", err)
	}
	report := sampleReport(base, 1)
	report.GPU.DetailedInfo = make([]common.GPUDeviceInfo, MaxGPUDevices+1)
	if err := store.Add("gpu-node", report); !errors.Is(err, ErrTooManyGPUDevices) {
		t.Fatalf("GPU limit error = %v", err)
	}
}

var benchmarkAggregate []Aggregate

func BenchmarkShardedMinuteAccumulator(b *testing.B) {
	store := NewStore()
	base := time.Unix(1_700_000_000, 0).Truncate(time.Minute)
	reports := make([]common.Report, 60)
	for i := range reports {
		reports[i] = sampleReport(base.Add(time.Duration(i)*time.Second), i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		window := base.Add(time.Duration(i) * time.Minute)
		for j := range reports {
			report := reports[j]
			report.UpdatedAt = window.Add(time.Duration(j) * time.Second)
			if err := store.Add("benchmark-node", report); err != nil {
				b.Fatal(err)
			}
		}
		benchmarkAggregate = store.DrainBefore(window.Add(time.Minute))
	}
}

func BenchmarkLegacyMinuteSliceAggregation(b *testing.B) {
	base := time.Unix(1_700_000_000, 0).Truncate(time.Minute)
	reports := make([]common.Report, 60)
	for i := range reports {
		reports[i] = sampleReport(base.Add(time.Duration(i)*time.Second), i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		copyForSort := append([]common.Report(nil), reports...)
		_ = utils.AverageReport("benchmark-node", base.Add(time.Minute), copyForSort, topFraction)
		_ = utils.AverageGPUReports("benchmark-node", base.Add(time.Minute), reports, topFraction)
	}
}
