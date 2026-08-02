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

func TestQueryRecordsForClientsMatchesPerClientQueries(t *testing.T) {
	db := newCompactionTestDB(t)
	now := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
	start := now.Add(-10 * time.Minute)
	rows := []models.Record{
		{Client: "node-a", Time: models.FromTime(start.Add(time.Minute)), Cpu: 10, Ram: 100},
		{Client: "node-a", Time: models.FromTime(start.Add(2 * time.Minute)), Cpu: 20, Ram: 200},
		{Client: "node-b", Time: models.FromTime(start.Add(time.Minute)), Cpu: 30, Ram: 300},
		{Client: "hidden", Time: models.FromTime(start.Add(time.Minute)), Cpu: 99, Ram: 999},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	result, err := queryRecordsForClientsAt(context.Background(), db, []string{"node-a", "node-b"}, start, now, "all", now)
	if err != nil {
		t.Fatal(err)
	}
	if result.SQLQueries != 1 || len(result.Records) != 3 {
		t.Fatalf("records=%+v queries=%d, want 3/1", result.Records, result.SQLQueries)
	}
	for _, client := range []string{"node-a", "node-b"} {
		legacy, _, err := executeRecordQuery(context.Background(), db, RecordQuery{Client: client, Start: start, End: now, LoadType: "all"}, now)
		if err != nil {
			t.Fatal(err)
		}
		var setRows []models.Record
		for _, record := range result.Records {
			if record.Client == client {
				setRows = append(setRows, record)
			}
		}
		if !reflect.DeepEqual(setRows, legacy) {
			t.Fatalf("%s set rows=%+v, legacy=%+v", client, setRows, legacy)
		}
	}

	projected, err := queryRecordsForClientsAt(context.Background(), db, []string{"node-a"}, start, now, "cpu", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(projected.Records) != 2 || projected.Records[0].Ram != 0 || projected.Records[1].Cpu != 20 {
		t.Fatalf("projected rows=%+v", projected.Records)
	}
}

func TestQueryRecordsForClientsLargeAndEmptyAuthorizationSets(t *testing.T) {
	db := newCompactionTestDB(t)
	now := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
	start := now.Add(-time.Minute)
	clientIDs := make([]string, maxTrafficSQLClientFilter+1)
	for index := range clientIDs {
		clientIDs[index] = fmt.Sprintf("allowed-%03d", index)
	}
	rows := []models.Record{
		{Client: clientIDs[len(clientIDs)-1], Time: models.FromTime(start), Cpu: 1},
		{Client: "not-allowed", Time: models.FromTime(start), Cpu: 2},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	result, err := queryRecordsForClientsAt(context.Background(), db, clientIDs, start, now, "cpu", now)
	if err != nil {
		t.Fatal(err)
	}
	if result.SQLQueries != 1 || len(result.Records) != 1 || result.Records[0].Client == "not-allowed" {
		t.Fatalf("large authorized result=%+v queries=%d", result.Records, result.SQLQueries)
	}
	empty, err := queryRecordsForClientsAt(context.Background(), db, []string{}, start, now, "all", now)
	if err != nil || empty.SQLQueries != 0 || len(empty.Records) != 0 {
		t.Fatalf("empty result=%+v err=%v", empty, err)
	}
}

func TestQueryRecordsForClientsCancellation(t *testing.T) {
	db := newCompactionTestDB(t)
	now := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := queryRecordsForClientsAt(ctx, db, nil, now.Add(-time.Minute), now, "all", now)
	if !errors.Is(err, context.Canceled) || result.SQLQueries != 1 {
		t.Fatalf("result=%+v err=%v, want canceled one-query attempt", result, err)
	}
}
