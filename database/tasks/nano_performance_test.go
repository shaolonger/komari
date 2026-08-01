package tasks

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
)

const (
	nanoFixtureNodes       = 30
	nanoFixtureAssignments = 105
)

func nanoPingTasks() []models.PingTask {
	clients := make(models.StringArray, nanoFixtureNodes)
	for index := range clients {
		clients[index] = fmt.Sprintf("nano-node-%02d", index)
	}
	tasks := make([]models.PingTask, 4)
	for index := range tasks {
		assigned := clients
		if index == len(tasks)-1 {
			assigned = clients[:15]
		}
		tasks[index] = models.PingTask{
			Id: uint(index + 1), Name: fmt.Sprintf("nano-ping-%d", index+1),
			Clients: append(models.StringArray(nil), assigned...), Type: "tcp",
			Target: "example.com:443", Interval: 60,
		}
	}
	return tasks
}

func TestNanoPingFixtureHasExactAssignmentAndRowScanBudgets(t *testing.T) {
	db := resetPingTaskData(t)
	tasks := nanoPingTasks()
	clients := make([]models.Client, nanoFixtureNodes)
	for index := range clients {
		clients[index] = models.Client{UUID: fmt.Sprintf("nano-node-%02d", index), Token: fmt.Sprintf("nano-token-%02d", index)}
	}
	if err := db.Create(&clients).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	index := publishPingAssignmentIndex(tasks)
	if len(index.tasksByClient) != nanoFixtureNodes || len(index.assignments) != nanoFixtureAssignments {
		t.Fatalf("fixture nodes/assignments = %d/%d", len(index.tasksByClient), len(index.assignments))
	}

	now := time.Now().UTC().Truncate(time.Hour)
	rollups := make([]models.PingRollup, 0, nanoFixtureAssignments*6)
	for assignment := range index.assignments {
		for hour := 1; hour <= 6; hour++ {
			at := now.Add(-time.Duration(hour) * time.Hour)
			rollups = append(rollups, models.PingRollup{
				Client: assignment.client, TaskId: assignment.taskID, ResolutionSeconds: 3600,
				BucketTime: models.FromTime(at), SampleCount: 60, ValidCount: 60,
				SumValue: 1200, MinValue: 10, MaxValue: 30, LastValue: 20, LastTime: models.FromTime(at),
			})
		}
	}
	if err := db.CreateInBatches(rollups, 250).Error; err != nil {
		t.Fatal(err)
	}
	result, err := QueryPingSeries(context.Background(), db, PingQuery{
		TaskID: -1, Start: now.Add(-6 * time.Hour), End: now.Add(time.Second), MaxPoints: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ResolutionSeconds != 3600 || result.RowsScanned != len(rollups) || result.RowsScanned > 1001 {
		t.Fatalf("resolution/rows = %d/%d, fixture rows = %d", result.ResolutionSeconds, result.RowsScanned, len(rollups))
	}
}

func BenchmarkNanoPingAssignmentAndTierPlanning(b *testing.B) {
	tasks := nanoPingTasks()
	query := PingQuery{TaskID: -1, Start: time.Unix(0, 0), End: time.Unix(0, 0).Add(6 * time.Hour), MaxPoints: 1000}
	b.ReportAllocs()
	for b.Loop() {
		index := buildPingAssignmentIndex(tasks)
		if len(index.assignments) != nanoFixtureAssignments || choosePingResolution(index, query) != 3600 {
			b.Fatal("invalid Nano fixture")
		}
	}
}
