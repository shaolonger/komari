package api

import (
	"testing"

	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/ws"
)

func TestFilterDashboardUpdatePreservesPermissionsAndOwnership(t *testing.T) {
	input := ws.DashboardUpdate{
		Sequence: 9,
		Reports: map[string]common.Report{
			"public": {UUID: "public", GPU: &common.GPUDetailReport{DetailedInfo: []common.GPUDeviceInfo{{Name: "gpu"}}}},
			"hidden": {UUID: "hidden", CPU: common.CPUReport{Usage: 12}},
		},
		Online:  []string{"hidden", "public"},
		Offline: []string{"hidden", "public"},
		Removed: []string{"hidden", "public"},
	}
	filtered := filterDashboardUpdate(input, map[string]bool{"hidden": true}, false, "")
	if len(filtered.Reports) != 1 || len(filtered.Online) != 1 || len(filtered.Offline) != 1 || len(filtered.Removed) != 1 {
		t.Fatalf("hidden node escaped filter: %#v", filtered)
	}
	report := filtered.Reports["public"]
	if report.UUID != "" || report.CPU.Usage != 0.01 {
		t.Fatalf("public report was not sanitized: %#v", report)
	}
	report.GPU.DetailedInfo[0].Name = "caller-mutated"
	if input.Reports["public"].GPU.DetailedInfo[0].Name != "gpu" {
		t.Fatal("filter output aliases input GPU slice")
	}

	admin := filterDashboardUpdate(input, map[string]bool{"hidden": true}, true, "hidden")
	if len(admin.Reports) != 1 || admin.Reports["hidden"].CPU.Usage != 12 || len(admin.Online) != 1 || admin.Online[0] != "hidden" {
		t.Fatalf("admin/single-node filter mismatch: %#v", admin)
	}
}
