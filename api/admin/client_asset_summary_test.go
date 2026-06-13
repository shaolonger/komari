package admin

import (
	"math"
	"testing"
	"time"

	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/models"
)

func TestBuildClientAssetSummary(t *testing.T) {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	clients := []models.Client{
		{
			UUID:                 "node-1",
			Name:                 "cloudsilk-hkg-01",
			Group:                "apac",
			Provider:             "CloudSilk",
			BusinessRole:         "Ingress",
			Price:                8.5,
			BillingCycle:         30,
			Currency:             "$",
			CurrencyCode:         "USD",
			ExpiredAt:            models.FromTime(now.Add(28 * 24 * time.Hour)),
			AutoRenewal:          true,
			CapabilityPing:       true,
			CapabilityTerminal:   true,
			CapabilityRemoteExec: true,
			CapabilityAutoUpdate: true,
			MemTotal:             2 * 1024 * 1024 * 1024,
			TrafficLimit:         1000 * 1024 * 1024 * 1024,
			TrafficLimitType:     "sum",
		},
		{
			UUID:                 "node-2",
			Name:                 "racknerd-lax-02",
			Group:                "us-west",
			Provider:             "RackNerd",
			BusinessRole:         "Backup",
			Price:                49.9,
			BillingCycle:         365,
			Currency:             "$",
			CurrencyCode:         "USD",
			ExpiredAt:            models.FromTime(now.Add(5 * 24 * time.Hour)),
			AutoRenewal:          false,
			CapabilityPing:       false,
			CapabilityAutoUpdate: false,
			MemTotal:             1024 * 1024 * 1024,
			TrafficLimit:         100 * 1024 * 1024 * 1024,
			TrafficLimitType:     "sum",
		},
		{
			UUID:                 "node-3",
			Name:                 "saltyfish-idle-03",
			Group:                "lab",
			Provider:             "SaltyFish",
			BusinessRole:         "Lab",
			Price:                35,
			BillingCycle:         30,
			Currency:             "¥",
			CurrencyCode:         "CNY",
			ExpiredAt:            models.FromTime(now.Add(45 * 24 * time.Hour)),
			AutoRenewal:          false,
			AssetIgnored:         true,
			CapabilityPing:       true,
			CapabilityAutoUpdate: false,
			MemTotal:             2 * 1024 * 1024 * 1024,
			TrafficLimit:         200 * 1024 * 1024 * 1024,
			TrafficLimitType:     "sum",
		},
		{
			UUID:                 "node-4",
			Name:                 "idle-paid-04",
			Group:                "us-east",
			Price:                20,
			BillingCycle:         30,
			Currency:             "€",
			ExpiredAt:            models.FromTime(now.Add(60 * 24 * time.Hour)),
			AutoRenewal:          false,
			CapabilityPing:       true,
			CapabilityTerminal:   true,
			CapabilityRemoteExec: true,
			CapabilityAutoUpdate: true,
			MemTotal:             2 * 1024 * 1024 * 1024,
			TrafficLimit:         400 * 1024 * 1024 * 1024,
			TrafficLimitType:     "sum",
		},
	}
	latest := map[string]*common.Report{
		"node-1": {
			CPU: common.CPUReport{Usage: 42},
			Ram: common.RamReport{Used: 768 * 1024 * 1024},
			Network: common.NetworkReport{
				TotalUp:   120 * 1024 * 1024 * 1024,
				TotalDown: 150 * 1024 * 1024 * 1024,
			},
		},
		"node-2": {
			CPU: common.CPUReport{Usage: 8},
			Ram: common.RamReport{Used: 384 * 1024 * 1024},
			Network: common.NetworkReport{
				TotalUp:   12 * 1024 * 1024 * 1024,
				TotalDown: 75 * 1024 * 1024 * 1024,
			},
		},
		"node-3": {
			CPU: common.CPUReport{Usage: 2},
			Ram: common.RamReport{Used: 256 * 1024 * 1024},
			Network: common.NetworkReport{
				TotalUp:   80 * 1024 * 1024,
				TotalDown: 3500 * 1024 * 1024,
			},
		},
		"node-4": {
			CPU: common.CPUReport{Usage: 4},
			Ram: common.RamReport{Used: 200 * 1024 * 1024},
			Network: common.NetworkReport{
				TotalUp:   10 * 1024 * 1024 * 1024,
				TotalDown: 10 * 1024 * 1024 * 1024,
			},
		},
	}
	onlineSet := map[string]bool{
		"node-1": true,
		"node-2": true,
		"node-4": true,
	}

	summary := buildClientAssetSummary(clients, latest, onlineSet, now)

	if summary.TotalAssets != 4 {
		t.Fatalf("TotalAssets = %d, want 4", summary.TotalAssets)
	}
	if summary.BillableAssets != 3 {
		t.Fatalf("BillableAssets = %d, want 3", summary.BillableAssets)
	}
	if summary.IgnoredAssets != 1 {
		t.Fatalf("IgnoredAssets = %d, want 1", summary.IgnoredAssets)
	}
	if summary.HighRiskAssets != 2 {
		t.Fatalf("HighRiskAssets = %d, want 2", summary.HighRiskAssets)
	}
	if summary.Queue.RenewalAttention != 1 {
		t.Fatalf("Queue.RenewalAttention = %d, want 1", summary.Queue.RenewalAttention)
	}
	if summary.Queue.MetadataGap != 1 {
		t.Fatalf("Queue.MetadataGap = %d, want 1", summary.Queue.MetadataGap)
	}
	if summary.Queue.Underused != 1 {
		t.Fatalf("Queue.Underused = %d, want 1", summary.Queue.Underused)
	}

	assertApprox(t, summary.MonthlySpend, 32.601369863, 0.0001, "MonthlySpend")
	assertApprox(t, summary.AnnualizedSpend, 391.216438356, 0.0001, "AnnualizedSpend")
	assertApprox(t, summary.Renewal7dExposure, 49.9, 0.0001, "Renewal7dExposure")
	assertApprox(t, summary.Renewal30dExposure, 58.4, 0.0001, "Renewal30dExposure")

	if summary.Lifecycle.Renewal7d != 1 || summary.Lifecycle.Renewal30d != 1 {
		t.Fatalf("unexpected lifecycle renewal buckets: %+v", summary.Lifecycle)
	}
	if summary.Lifecycle.Active != 2 {
		t.Fatalf("Lifecycle.Active = %d, want 2", summary.Lifecycle.Active)
	}
	if summary.Lifecycle.Ignored != 1 {
		t.Fatalf("Lifecycle.Ignored = %d, want 1", summary.Lifecycle.Ignored)
	}
	if summary.Lifecycle.ManualRenew != 3 {
		t.Fatalf("Lifecycle.ManualRenew = %d, want 3", summary.Lifecycle.ManualRenew)
	}
	if summary.Lifecycle.MetadataGap != 1 {
		t.Fatalf("Lifecycle.MetadataGap = %d, want 1", summary.Lifecycle.MetadataGap)
	}
	if summary.Lifecycle.Underused != 1 {
		t.Fatalf("Lifecycle.Underused = %d, want 1", summary.Lifecycle.Underused)
	}

	if len(summary.Providers) != 3 {
		t.Fatalf("len(Providers) = %d, want 3", len(summary.Providers))
	}
	if summary.Providers[0].Name != "us-east" {
		t.Fatalf("Providers[0].Name = %q, want %q", summary.Providers[0].Name, "us-east")
	}
	if len(summary.IgnoredProviders) != 1 || summary.IgnoredProviders[0].Name != "SaltyFish" {
		t.Fatalf("unexpected ignored providers: %+v", summary.IgnoredProviders)
	}
	if len(summary.Currencies) != 2 {
		t.Fatalf("len(Currencies) = %d, want 2", len(summary.Currencies))
	}
}

func TestFilterAssetSummaryClients(t *testing.T) {
	clients := []models.Client{
		{UUID: "1", Provider: "CloudSilk", BusinessRole: "Ingress", CurrencyCode: "USD"},
		{UUID: "2", Group: "fallback-group", CurrencyCode: "USD", AssetIgnored: true},
		{UUID: "3", Provider: "RackNerd", BusinessRole: "Backup", Currency: "¥"},
	}

	filtered := filterAssetSummaryClients(clients, "CloudSilk", "", "", true)
	if len(filtered) != 1 || filtered[0].UUID != "1" {
		t.Fatalf("provider filter mismatch: %+v", filtered)
	}

	filtered = filterAssetSummaryClients(clients, "", "USD", "", false)
	if len(filtered) != 1 || filtered[0].UUID != "1" {
		t.Fatalf("currency/include_ignored filter mismatch: %+v", filtered)
	}

	filtered = filterAssetSummaryClients(clients, "", "", "Backup", true)
	if len(filtered) != 1 || filtered[0].UUID != "3" {
		t.Fatalf("role filter mismatch: %+v", filtered)
	}
}

func assertApprox(t *testing.T, got, want, epsilon float64, field string) {
	t.Helper()
	if math.Abs(got-want) > epsilon {
		t.Fatalf("%s = %.6f, want %.6f", field, got, want)
	}
}
