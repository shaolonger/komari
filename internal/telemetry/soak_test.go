package telemetry

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/komari-monitor/komari/common"
)

// This advances the production accumulator through 72 hours without sleeping:
// 30 nodes, one local aggregate every five seconds, and one durable drain per
// minute. The node map and heap must remain flat across all 1,555,200 reports.
func TestSeventyTwoHourEquivalentTelemetrySoakStaysFlat(t *testing.T) {
	const (
		nodes            = 30
		minutes          = 72 * 60
		samplesPerMinute = 12
	)
	base := time.Unix(1_700_000_000, 0).UTC().Truncate(time.Minute)
	current := base
	store := NewStore()
	store.now = func() time.Time { return current }

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for minute := 0; minute < minutes; minute++ {
		window := base.Add(time.Duration(minute) * time.Minute)
		for sample := 0; sample < samplesPerMinute; sample++ {
			at := window.Add(time.Duration(sample*5) * time.Second)
			for node := 0; node < nodes; node++ {
				report := common.Report{
					UpdatedAt: at,
					CPU:       common.CPUReport{Usage: float64(10 + node%80)},
					Ram:       common.RamReport{Total: 4 << 30, Used: int64(1+node%3) << 30},
				}
				if err := store.Add(fmt.Sprintf("soak-node-%02d", node), report); err != nil {
					t.Fatalf("minute=%d sample=%d node=%d: %v", minute, sample, node, err)
				}
			}
		}
		current = window.Add(time.Minute)
		drained := store.DrainBefore(current)
		if len(drained) != nodes {
			t.Fatalf("minute %d drained %d aggregates, want %d", minute, len(drained), nodes)
		}
		if store.NodeCount() != nodes {
			t.Fatalf("minute %d retained %d nodes, want %d", minute, store.NodeCount(), nodes)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if after.HeapInuse > before.HeapInuse+16<<20 {
		t.Fatalf("heap grew by %d bytes during 72h-equivalent soak", after.HeapInuse-before.HeapInuse)
	}
}
