package records

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
)

func TestStreamTrafficStatsMatchesLegacyAggregation(t *testing.T) {
	db := newCompactionTestDB(t)
	now := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	start := now.Add(-4 * time.Minute).In(location)
	end := now.Add(-time.Minute).In(location)
	fixtures := map[string][]models.Record{
		"node-a": {
			trafficQueryRecord("node-a", start.Add(-30*time.Second), 5_000, 8_000, 0, 0),
			trafficQueryRecord("node-a", start.Add(30*time.Second), 5_600, 8_300, 0, 0),
			trafficQueryRecord("node-a", start.Add(90*time.Second), 100, 200, 0, 0),
			trafficQueryRecord("node-a", end.Add(-time.Second), 700, 1_100, 0, 0),
		},
		"node-b": {
			trafficQueryRecord("node-b", start.Add(-10*time.Second), 0, 0, 100, 200),
			trafficQueryRecord("node-b", start.Add(70*time.Second), 0, 0, 300, 400),
			trafficQueryRecord("node-b", end.Add(-time.Second), 0, 0, 500, 600),
		},
	}
	for _, records := range fixtures {
		if err := db.Create(&records).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&models.Record{
		Client: "hidden-node", Time: models.FromTime(start.Add(time.Minute)), NetTotalUp: 9_999, NetTotalDown: 9_999,
	}).Error; err != nil {
		t.Fatal(err)
	}

	result, err := streamTrafficStatsAt(context.Background(), db, []string{"node-a", "node-b"}, start, end, time.Minute, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.SQLQueries != 1 {
		t.Fatalf("SQL queries=%d, want 1", result.SQLQueries)
	}
	if len(result.ByClient) != 2 {
		t.Fatalf("clients=%v, want only node-a and node-b", result.ByClient)
	}
	for client, records := range fixtures {
		for index := range records {
			records[index].Time = models.FromTime(records[index].Time.ToTime().In(models.GetAppLocation()))
		}
		want := SummarizeTrafficRecords(records, start, end, time.Minute)
		if got := result.ByClient[client]; !reflect.DeepEqual(got, want) {
			t.Fatalf("%s stats mismatch\n got: %+v\nwant: %+v", client, got, want)
		}
	}
	if _, ok := result.ByClient["hidden-node"]; ok {
		t.Fatal("hidden client escaped the requested client filter")
	}
}

func TestStreamTrafficStatsEmptyFilterAndCancellation(t *testing.T) {
	db := newCompactionTestDB(t)
	now := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
	start := now.Add(-time.Minute)

	empty, err := streamTrafficStatsAt(context.Background(), db, []string{}, start, now, time.Minute, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if empty.SQLQueries != 0 || len(empty.ByClient) != 0 {
		t.Fatalf("empty authorized set queried data: %+v", empty)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := streamTrafficStatsAt(ctx, db, nil, start, now, time.Minute, false, now)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context.Canceled", err)
	}
	if result.SQLQueries != 1 {
		t.Fatalf("canceled query count=%d, want 1 attempted query", result.SQLQueries)
	}
}

func TestStreamTrafficStatsLargeFilterDoesNotLeakOtherClients(t *testing.T) {
	db := newCompactionTestDB(t)
	now := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
	start := now.Add(-2 * time.Minute)
	clientIDs := make([]string, maxTrafficSQLClientFilter+1)
	for index := range clientIDs {
		clientIDs[index] = fmt.Sprintf("requested-%03d", index)
	}
	rows := []models.Record{
		trafficQueryRecord(clientIDs[len(clientIDs)-1], start, 100, 200, 0, 0),
		trafficQueryRecord(clientIDs[len(clientIDs)-1], now.Add(-time.Second), 500, 700, 0, 0),
		trafficQueryRecord("not-requested", start, 1, 1, 0, 0),
		trafficQueryRecord("not-requested", now.Add(-time.Second), 9_999, 9_999, 0, 0),
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	result, err := streamTrafficStatsAt(context.Background(), db, clientIDs, start, now, time.Minute, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.SQLQueries != 1 || len(result.ByClient) != 1 {
		t.Fatalf("result=%+v, want one requested client from one query", result)
	}
	if _, ok := result.ByClient["not-requested"]; ok {
		t.Fatal("Go-side large-set filter leaked an unrequested client")
	}
}

func BenchmarkStreamTrafficStats10000Nodes(b *testing.B) {
	db := newCompactionTestDB(b)
	now := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
	start := now.Add(-2 * time.Minute)
	const nodes = 10_000
	rows := make([]models.Record, 0, nodes*2)
	clientIDs := make([]string, 0, nodes)
	for index := 0; index < nodes; index++ {
		client := fmt.Sprintf("node-%05d", index)
		clientIDs = append(clientIDs, client)
		rows = append(rows,
			trafficQueryRecord(client, start, int64(index+1), int64(index+2), 0, 0),
			trafficQueryRecord(client, now.Add(-time.Second), int64(index+501), int64(index+1_002), 0, 0),
		)
	}
	if err := db.CreateInBatches(&rows, 250).Error; err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ReportMetric(1, "sql/op")
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		result, err := streamTrafficStatsAt(context.Background(), db, clientIDs, start, now, 0, false, now)
		if err != nil {
			b.Fatal(err)
		}
		if len(result.ByClient) != nodes || result.SQLQueries != 1 {
			b.Fatalf("clients=%d queries=%d", len(result.ByClient), result.SQLQueries)
		}
	}
}

func trafficQueryRecord(client string, at time.Time, totalUp, totalDown, rateUp, rateDown int64) models.Record {
	return models.Record{
		Client: client, Time: models.FromTime(at), NetTotalUp: totalUp, NetTotalDown: totalDown, NetOut: rateUp, NetIn: rateDown,
	}
}
