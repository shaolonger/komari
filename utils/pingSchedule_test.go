package utils

import (
	"context"
	"testing"
	"time"
)

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
