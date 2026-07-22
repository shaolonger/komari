package notifier

import (
	"context"
	"testing"
	"time"
)

func TestLoadNotificationReloadTerminatesEveryPreviousGeneration(t *testing.T) {
	manager := &LoadNotificationService{}
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
			t.Fatalf("load scheduler generation %d leaked", index)
		}
	}
}

func TestExpireSchedulerHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		CheckExpireScheduledWorkContext(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("expire scheduler ignored cancellation")
	}
}
