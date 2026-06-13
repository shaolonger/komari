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
