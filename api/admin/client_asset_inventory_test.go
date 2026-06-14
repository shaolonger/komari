package admin

import (
	"testing"
	"time"

	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/models"
)

func TestBuildClientAssetInventoryFiltersAndSorts(t *testing.T) {
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

	response := buildClientAssetInventory(
		clients,
		latest,
		onlineSet,
		now,
		assetInventoryFilters{
			IncludeIgnored: true,
			Filter:         "all",
			Sort:           "risk",
			Order:          "desc",
			Limit:          10,
		},
	)
	if response.Total != 3 {
		t.Fatalf("Total = %d, want 3", response.Total)
	}
	if response.Items[0].UUID != "node-2" {
		t.Fatalf("expected node-2 to lead risk-sorted inventory, got %+v", response.Items[0])
	}

	response = buildClientAssetInventory(
		clients,
		latest,
		onlineSet,
		now,
		assetInventoryFilters{
			IncludeIgnored: true,
			Filter:         "metadata",
			Sort:           "name",
			Order:          "asc",
			Limit:          10,
		},
	)
	if response.Total != 1 || response.Items[0].UUID != "node-4" {
		t.Fatalf("expected only metadata-gap node-4, got %+v", response.Items)
	}

	response = buildClientAssetInventory(
		clients,
		latest,
		onlineSet,
		now,
		assetInventoryFilters{
			IncludeIgnored: true,
			Filter:         "underused",
			Sort:           "monthly",
			Order:          "desc",
			Limit:          10,
		},
	)
	if response.Total != 1 || response.Items[0].UUID != "node-4" {
		t.Fatalf("expected only underused node-4, got %+v", response.Items)
	}
}

func TestBuildClientAssetInventoryRespectsLimit(t *testing.T) {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	clients := []models.Client{
		{
			UUID:         "node-1",
			Name:         "bbb",
			Provider:     "P1",
			BusinessRole: "R",
			Price:        10,
			BillingCycle: 30,
			CurrencyCode: "USD",
			ExpiredAt:    models.FromTime(now.Add(3 * 24 * time.Hour)),
		},
		{
			UUID:         "node-2",
			Name:         "aaa",
			Provider:     "P2",
			BusinessRole: "R",
			Price:        11,
			BillingCycle: 30,
			CurrencyCode: "USD",
			ExpiredAt:    models.FromTime(now.Add(4 * 24 * time.Hour)),
		},
	}

	response := buildClientAssetInventory(
		clients,
		map[string]*common.Report{},
		map[string]bool{},
		now,
		assetInventoryFilters{
			IncludeIgnored: true,
			Filter:         "all",
			Sort:           "name",
			Order:          "asc",
			Limit:          1,
		},
	)
	if response.Total != 2 {
		t.Fatalf("Total = %d, want 2", response.Total)
	}
	if len(response.Items) != 1 || response.Items[0].UUID != "node-2" {
		t.Fatalf("expected only ascending-first node-2, got %+v", response.Items)
	}
}

func TestBuildClientAssetInventoryIncludesGovernanceAndValueSignals(t *testing.T) {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	clients := []models.Client{
		{
			UUID:             "node-1",
			Name:             "edge-node",
			Provider:         "CloudSilk",
			BusinessRole:     "Ingress",
			Price:            24,
			BillingCycle:     30,
			CurrencyCode:     "USD",
			ExpiredAt:        models.FromTime(now.Add(5 * 24 * time.Hour)),
			Version:          "v1.2.2",
			GovernanceStatus: "observe",
			GovernanceNote:   "renew after migration",
			TokenExpiresAt:   models.FromTime(now.Add(48 * time.Hour)),
			MemTotal:         2 * 1024 * 1024 * 1024,
			TrafficLimit:     100 * 1024 * 1024 * 1024,
			TrafficLimitType: "sum",
		},
	}
	latest := map[string]*common.Report{
		"node-1": {
			CPU:       common.CPUReport{Usage: 5},
			Ram:       common.RamReport{Used: 256 * 1024 * 1024},
			Network:   common.NetworkReport{TotalUp: 0, TotalDown: 0},
			UpdatedAt: now.Add(-20 * time.Minute),
		},
	}
	onlineSet := map[string]bool{"node-1": true}
	evaluation := assetEvaluationContext{
		targetAgentVersion:          "v1.2.4",
		offlineNotificationCoverage: map[string]bool{},
		loadNotificationCoverage:    map[string]bool{},
		recentTaskFailures:          map[string]bool{"node-1": true},
	}

	response := buildClientAssetInventoryWithContext(
		clients,
		latest,
		onlineSet,
		now,
		assetInventoryFilters{
			IncludeIgnored: true,
			Filter:         "all",
			Sort:           "risk",
			Order:          "desc",
			Limit:          10,
		},
		evaluation,
	)
	if response.Total != 1 {
		t.Fatalf("Total = %d, want 1", response.Total)
	}

	item := response.Items[0]
	if item.ValueScore <= 0 {
		t.Fatalf("ValueScore = %d, want positive", item.ValueScore)
	}
	if len(item.ValueScoreFactors) == 0 {
		t.Fatal("expected value score factors to be present")
	}
	if item.ObservationQuality != assetObservationStale {
		t.Fatalf("ObservationQuality = %q, want %q", item.ObservationQuality, assetObservationStale)
	}
	if !item.VersionDrift {
		t.Fatal("expected version drift to be surfaced")
	}
	if item.TokenStatus != assetTokenExpiring {
		t.Fatalf("TokenStatus = %q, want %q", item.TokenStatus, assetTokenExpiring)
	}
	if item.GovernanceStatus != "observe" {
		t.Fatalf("GovernanceStatus = %q, want %q", item.GovernanceStatus, "observe")
	}
	if item.GovernanceNote != "renew after migration" {
		t.Fatalf("GovernanceNote = %q, want %q", item.GovernanceNote, "renew after migration")
	}
	if !item.RecentTaskFailure {
		t.Fatal("expected recent task failure flag to be true")
	}
	if !matchesInventoryFilter(item, "token") {
		t.Fatal("expected token filter to match")
	}
	if !matchesInventoryFilter(item, "version") {
		t.Fatal("expected version filter to match")
	}
	if !matchesInventoryFilter(item, "observe") {
		t.Fatal("expected observe filter to match")
	}
}
