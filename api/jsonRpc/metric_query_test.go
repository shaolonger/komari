package jsonRpc

import (
	"testing"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/utils/rpc"
)

func TestMetricProjectionHandlesCountersAndConnections(t *testing.T) {
	previous := models.Record{NetTotalUp: 1000, NetTotalDown: 2000}
	current := models.Record{NetTotalUp: 1200, NetTotalDown: 100, Connections: 15, ConnectionsUdp: 4}
	checks := map[string]float64{
		"traffic.up": 200, "traffic.down": 100,
		"connections.tcp": 11, "connections.udp": 4,
	}
	for key, want := range checks {
		value := recordMetricValue(current, key, &previous)
		if value == nil || *value != want {
			t.Fatalf("%s = %v, want %v", key, value, want)
		}
	}
	if value := recordMetricValue(current, "traffic.up", nil); value != nil {
		t.Fatalf("first counter delta = %v, want nil", *value)
	}
}

func TestMetricSeriesBudgetIsGlobal(t *testing.T) {
	points := make([]metricPoint, 100)
	series := []metricSeries{{Points: append([]metricPoint(nil), points...)}, {Points: append([]metricPoint(nil), points...)}, {Points: append([]metricPoint(nil), points...)}}
	limited := enforceMetricSeriesBudget(series, 30)
	total := 0
	for _, item := range limited {
		total += len(item.Points)
		if !item.Downsampled || item.DownsampleAlgorithm == "" {
			t.Fatalf("series was not marked downsampled: %+v", item)
		}
	}
	if total != 30 {
		t.Fatalf("total points = %d, want 30", total)
	}
}

func TestWeightedPingPercentile(t *testing.T) {
	values := []weightedPingValue{{value: 10, weight: 8}, {value: 100, weight: 2}}
	if got := weightedPercentile(values, .5); got != 10 {
		t.Fatalf("p50 = %d, want 10", got)
	}
	if got := weightedPercentile(values, .99); got != 100 {
		t.Fatalf("p99 = %d, want 100", got)
	}
}

func TestOfficialFrontendMetricRPCMethodsAreRegistered(t *testing.T) {
	registered := make(map[string]struct{})
	for _, method := range rpc.ListMethods() {
		registered[method] = struct{}{}
	}
	for _, method := range []string{
		"admin:listMetricDefinitions", "admin:updateMetricDefinition",
		"admin:getMetricMigrationStatus", "admin:startMetricMigration", "admin:cancelMetricMigration",
		"public:listMetricDefinitions", "public:queryMetrics", "public:getPingMetricStats",
	} {
		if _, ok := registered[method]; !ok {
			t.Fatalf("required frontend method %s is not registered", method)
		}
	}
}
