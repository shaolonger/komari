package scheduler

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStableNextRunIsDeterministicAndDistributed(t *testing.T) {
	now := time.Unix(1_700_000_000, 123)
	interval := time.Minute
	seen := make(map[int64]struct{})
	for index := 0; index < 1_000; index++ {
		key := "task-" + string(rune(index))
		first := StableNextRun(key, interval, now)
		second := StableNextRun(key, interval, now)
		if !first.Equal(second) || !first.After(now) || first.After(now.Add(interval)) {
			t.Fatalf("key=%q first=%s second=%s", key, first, second)
		}
		seen[first.UnixMicro()] = struct{}{}
	}
	if len(seen) < 990 {
		t.Fatalf("stable jitter occupied only %d/1000 microsecond slots", len(seen))
	}
}

func TestEngineBoundsWorkersQueueAndCancels(t *testing.T) {
	engine, err := New(Config{Workers: 3, QueueCapacity: 4})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var running atomic.Int32
	var maximum atomic.Int32
	var completed atomic.Int32
	var once sync.Once
	tasks := make([]Task, 40)
	for index := range tasks {
		tasks[index] = Task{
			Key: "bounded-" + string(rune(index)), Interval: 5 * time.Millisecond,
			Run: func(ctx context.Context) {
				current := running.Add(1)
				for {
					observed := maximum.Load()
					if current <= observed || maximum.CompareAndSwap(observed, current) {
						break
					}
				}
				select {
				case <-time.After(2 * time.Millisecond):
				case <-ctx.Done():
				}
				running.Add(-1)
				if completed.Add(1) >= 20 {
					once.Do(cancel)
				}
			},
		}
	}
	done := make(chan error, 1)
	go func() { done <- engine.Run(ctx, tasks) }()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("Run error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not cancel")
	}
	if maximum.Load() > 3 {
		t.Fatalf("maximum concurrency=%d, want <=3", maximum.Load())
	}
}

func TestEngineRejectsInvalidBoundsAndDuplicateKeys(t *testing.T) {
	if _, err := New(Config{Workers: MaxWorkers + 1}); err == nil {
		t.Fatal("oversized worker pool accepted")
	}
	engine, _ := New(Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := engine.Run(ctx, []Task{{Key: "same", Interval: time.Second, Run: func(context.Context) {}}, {Key: "same", Interval: time.Second, Run: func(context.Context) {}}})
	if err == nil {
		t.Fatal("duplicate task key accepted")
	}
}

func TestEngineIsolatesTaskPanic(t *testing.T) {
	engine, err := New(Config{Workers: 1, QueueCapacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var runs atomic.Int32
	task := Task{
		Key:      "panic-isolation",
		Interval: 2 * time.Millisecond,
		Run: func(context.Context) {
			if runs.Add(1) >= 2 {
				cancel()
			}
			panic("scheduled task failure")
		},
	}
	done := make(chan error, 1)
	go func() { done <- engine.Run(ctx, []Task{task}) }()
	select {
	case runErr := <-done:
		if runErr != context.Canceled {
			t.Fatalf("Run error=%v", runErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not continue after task panic")
	}
	if runs.Load() < 2 {
		t.Fatalf("task ran %d times; worker stopped after panic", runs.Load())
	}
}

func BenchmarkStableNextRun10000Tasks(b *testing.B) {
	keys := make([]string, 10_000)
	for index := range keys {
		keys[index] = "scheduled-task-" + strconv.Itoa(index)
	}
	now := time.Unix(1_700_000_000, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		for _, key := range keys {
			_ = StableNextRun(key, time.Minute, now)
		}
	}
}
