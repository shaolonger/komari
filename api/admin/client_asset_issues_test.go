package admin

import (
	"testing"
	"time"

	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/models"
)

func TestBuildClientAssetIssues(t *testing.T) {
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

	issues := buildClientAssetIssues(
		clients,
		latest,
		onlineSet,
		now,
		assetIssueFilters{IncludeIgnored: true, Limit: 10},
	)

	if issues.Counts.RenewalAttention != 1 {
		t.Fatalf("Counts.RenewalAttention = %d, want 1", issues.Counts.RenewalAttention)
	}
	if issues.Counts.MetadataGap != 1 {
		t.Fatalf("Counts.MetadataGap = %d, want 1", issues.Counts.MetadataGap)
	}
	if issues.Counts.Underused != 1 {
		t.Fatalf("Counts.Underused = %d, want 1", issues.Counts.Underused)
	}
	if issues.Counts.HighRisk != 2 {
		t.Fatalf("Counts.HighRisk = %d, want 2", issues.Counts.HighRisk)
	}
	if len(issues.RenewalAttention) != 1 || issues.RenewalAttention[0].UUID != "node-2" {
		t.Fatalf("unexpected renewal attention: %+v", issues.RenewalAttention)
	}
	if len(issues.MetadataGap) != 1 || issues.MetadataGap[0].UUID != "node-4" {
		t.Fatalf("unexpected metadata gap queue: %+v", issues.MetadataGap)
	}
	if len(issues.Underused) != 1 || issues.Underused[0].UUID != "node-4" {
		t.Fatalf("unexpected underused queue: %+v", issues.Underused)
	}
	if issues.HighRisk[0].UUID != "node-2" {
		t.Fatalf("expected node-2 to lead high risk queue, got %+v", issues.HighRisk[0])
	}
	if !containsReason(issues.HighRisk[0].IssueReasons, "renewal_due_7d") {
		t.Fatalf("expected renewal_due_7d reason in %+v", issues.HighRisk[0].IssueReasons)
	}
	if !containsReason(issues.MetadataGap[0].IssueReasons, "metadata_gap") {
		t.Fatalf("expected metadata_gap reason in %+v", issues.MetadataGap[0].IssueReasons)
	}
	if !containsReason(issues.Underused[0].IssueReasons, "underused_spend") {
		t.Fatalf("expected underused_spend reason in %+v", issues.Underused[0].IssueReasons)
	}
}

func TestBuildClientAssetIssuesRespectsLimit(t *testing.T) {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	clients := []models.Client{
		{
			UUID:         "node-1",
			Name:         "soon-1",
			Provider:     "One",
			BusinessRole: "Role",
			Price:        10,
			BillingCycle: 30,
			CurrencyCode: "USD",
			ExpiredAt:    models.FromTime(now.Add(3 * 24 * time.Hour)),
		},
		{
			UUID:         "node-2",
			Name:         "soon-2",
			Provider:     "Two",
			BusinessRole: "Role",
			Price:        11,
			BillingCycle: 30,
			CurrencyCode: "USD",
			ExpiredAt:    models.FromTime(now.Add(4 * 24 * time.Hour)),
		},
	}

	issues := buildClientAssetIssues(
		clients,
		map[string]*common.Report{},
		map[string]bool{},
		now,
		assetIssueFilters{IncludeIgnored: true, Limit: 1},
	)

	if issues.Counts.RenewalAttention != 2 {
		t.Fatalf("Counts.RenewalAttention = %d, want 2", issues.Counts.RenewalAttention)
	}
	if len(issues.RenewalAttention) != 1 {
		t.Fatalf("len(RenewalAttention) = %d, want 1", len(issues.RenewalAttention))
	}
}

func containsReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}
