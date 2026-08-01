package utils

import (
	"context"
	"testing"
	"time"

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
