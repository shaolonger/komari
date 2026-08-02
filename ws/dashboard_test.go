package ws

import (
	"strconv"
	"testing"
	"time"

	"github.com/komari-monitor/komari/common"
)

func resetDashboardStateForTest() {
	mu.Lock()
	defer mu.Unlock()
	connectedClients = make(map[string]*SafeConn)
	telemetryProtocol = make(map[string]uint8)
	presenceOnly = make(map[string]struct {
		id     int64
		expire time.Time
	})
	latestReport = make(map[string]common.Report)
	dashboardSequence = 0
	dashboardChanges = nil
	dashboardStart = 0
	close(dashboardNotify)
	dashboardNotify = make(chan struct{})
}

func TestLatestReportOwnershipIsDeeplyImmutable(t *testing.T) {
	resetDashboardStateForTest()
	t.Cleanup(resetDashboardStateForTest)
	original := common.Report{
		UUID: "private", CPU: common.CPUReport{Usage: 42},
		GPU: &common.GPUDetailReport{DetailedInfo: []common.GPUDeviceInfo{{Name: "owned"}}},
	}
	SetLatestReport("node-a", &original)
	original.CPU.Usage = 99
	original.GPU.DetailedInfo[0].Name = "mutated-input"

	first := GetLatestReport()
	if first["node-a"].CPU.Usage != 42 || first["node-a"].GPU.DetailedInfo[0].Name != "owned" {
		t.Fatalf("stored report aliases caller input: %#v", first["node-a"])
	}
	first["node-a"].UUID = "leaked"
	first["node-a"].CPU.Usage = 7
	first["node-a"].GPU.DetailedInfo[0].Name = "mutated-output"
	second := GetLatestReport()
	if second["node-a"].UUID != "private" || second["node-a"].CPU.Usage != 42 || second["node-a"].GPU.DetailedInfo[0].Name != "owned" {
		t.Fatalf("returned report aliases store: %#v", second["node-a"])
	}
}

func TestDashboardSnapshotDeltaSequenceAndResync(t *testing.T) {
	resetDashboardStateForTest()
	t.Cleanup(resetDashboardStateForTest)
	SetLatestReport("node-a", &common.Report{CPU: common.CPUReport{Usage: 1}})
	SetPresence("node-a", 1, true)
	snapshot, notify := DashboardStateSince(0)
	if !snapshot.Snapshot || snapshot.Resync || snapshot.Sequence != 2 {
		t.Fatalf("unexpected snapshot metadata: %#v", snapshot)
	}
	if len(snapshot.Online) != 1 || snapshot.Online[0] != "node-a" || snapshot.Reports["node-a"].CPU.Usage != 1 {
		t.Fatalf("unexpected snapshot content: %#v", snapshot)
	}

	SetLatestReport("node-a", &common.Report{CPU: common.CPUReport{Usage: 2}})
	select {
	case <-notify:
	case <-time.After(time.Second):
		t.Fatal("dashboard waiter missed update")
	}
	delta, _ := DashboardStateSince(snapshot.Sequence)
	if delta.Snapshot || delta.Sequence != 3 || delta.Reports["node-a"].CPU.Usage != 2 {
		t.Fatalf("unexpected delta: %#v", delta)
	}
	DeleteLatestReport("node-a")
	SetPresence("node-a", 1, false)
	delta, _ = DashboardStateSince(3)
	if delta.Sequence != 5 || len(delta.Removed) != 1 || delta.Removed[0] != "node-a" || len(delta.Offline) != 1 || delta.Offline[0] != "node-a" {
		t.Fatalf("delete/offline delta=%#v", delta)
	}

	for index := 0; index < dashboardChangeCapacity+2; index++ {
		SetLatestReport("journal-node", &common.Report{Process: index})
	}
	resync, _ := DashboardStateSince(1)
	if !resync.Snapshot || !resync.Resync || resync.Reports["journal-node"].Process != dashboardChangeCapacity+1 {
		t.Fatalf("journal gap did not force current snapshot: %#v", resync)
	}
}

func BenchmarkDashboardSnapshot10000Nodes(b *testing.B) {
	resetDashboardStateForTest()
	for index := 0; index < 10_000; index++ {
		uuid := "node-" + strconv.Itoa(index)
		SetLatestReport(uuid, &common.Report{
			CPU: common.CPUReport{Usage: float64(index % 100)},
			GPU: &common.GPUDetailReport{DetailedInfo: []common.GPUDeviceInfo{{Name: "gpu"}}},
		})
	}
	b.Cleanup(resetDashboardStateForTest)
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		update, _ := DashboardStateSince(0)
		if len(update.Reports) != 10_000 {
			b.Fatalf("reports=%d", len(update.Reports))
		}
	}
}

func BenchmarkDashboardSingleDelta10000Nodes(b *testing.B) {
	resetDashboardStateForTest()
	for index := 0; index < 10_000; index++ {
		uuid := "node-" + strconv.Itoa(index)
		SetLatestReport(uuid, &common.Report{CPU: common.CPUReport{Usage: float64(index % 100)}})
	}
	b.Cleanup(resetDashboardStateForTest)
	initial, _ := DashboardStateSince(0)
	sequence := initial.Sequence
	report := common.Report{CPU: common.CPUReport{Usage: 50}}
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		report.Process = iteration
		SetLatestReport("node-5000", &report)
		update, _ := DashboardStateSince(sequence)
		sequence = update.Sequence
		if len(update.Reports) != 1 {
			b.Fatalf("reports=%d", len(update.Reports))
		}
	}
}
