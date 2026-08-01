package utils

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/runtimeprofile"
)

func TestPingScheduleProfileBounds(t *testing.T) {
	profile, err := runtimeprofile.Current()
	if err != nil {
		t.Fatal(err)
	}
	if profile.PingWorkers < 1 || profile.PingWorkers > 32 {
		t.Fatalf("Ping workers = %d", profile.PingWorkers)
	}
	if profile.PingQueueCapacity < profile.PingWorkers || profile.PingQueueCapacity > 4_096 {
		t.Fatalf("Ping queue = %d", profile.PingQueueCapacity)
	}
}

func TestPingLeaseDefinitionsAreStableBoundedAndDeduplicated(t *testing.T) {
	tasks := []models.PingTask{
		{Id: 2, Type: "tcp", Target: "example.com:443", Interval: 10, Clients: models.StringArray{"node-a", "node-a"}},
		{Id: 1, Type: "http", Target: "https://example.com", Interval: 20, Clients: models.StringArray{"node-a"}},
		{Id: 3, Type: "tcp", Target: "example.com:443", Interval: 3601, Clients: models.StringArray{"node-a"}},
	}
	first := buildPingLeaseDefinitions(tasks)
	second := buildPingLeaseDefinitions(tasks)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("lease revisions are unstable: %#v != %#v", first, second)
	}
	definition := first["node-a"]
	if definition.revision == 0 || len(definition.tasks) != 2 || definition.tasks[0].TaskID != 1 || definition.tasks[1].TaskID != 2 {
		t.Fatalf("lease definition = %#v", definition)
	}
	for _, task := range definition.tasks {
		if task.PhaseMS < 0 || task.PhaseMS >= task.IntervalMS {
			t.Fatalf("invalid phase: %#v", task)
		}
	}
}

func TestPingScheduleReloadTerminatesEveryPreviousGeneration(t *testing.T) {
	manager := &PingTaskManager{}
	generations := make([]chan struct{}, 0, 51)
	for iteration := 0; iteration < 50; iteration++ {
		if err := manager.Reload(nil); err != nil {
			t.Fatal(err)
		}
		manager.mu.Lock()
		generations = append(generations, manager.done)
		manager.mu.Unlock()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	for index, done := range generations {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("ping scheduler generation %d leaked", index)
		}
	}
}

func TestPingScheduleStopIsIdempotent(t *testing.T) {
	manager := &PingTaskManager{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reload(nil); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
