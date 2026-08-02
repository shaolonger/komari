// Package scheduler provides context-owned periodic work with deterministic
// jitter and bounded execution concurrency.
package scheduler

import (
	"container/heap"
	"context"
	"errors"
	"hash/fnv"
	"sync"
	"time"
)

const (
	DefaultWorkers       = 8
	DefaultQueueCapacity = 256
	MaxWorkers           = 64
	MaxQueueCapacity     = 65_536
)

type Task struct {
	Key      string
	Interval time.Duration
	Run      func(context.Context)
}

type Config struct {
	Workers       int
	QueueCapacity int
	Now           func() time.Time
}

type Engine struct{ config Config }

func New(config Config) (*Engine, error) {
	if config.Workers <= 0 {
		config.Workers = DefaultWorkers
	}
	if config.Workers > MaxWorkers {
		return nil, errors.New("scheduler worker limit exceeded")
	}
	if config.QueueCapacity <= 0 {
		config.QueueCapacity = DefaultQueueCapacity
	}
	if config.QueueCapacity > MaxQueueCapacity {
		return nil, errors.New("scheduler queue limit exceeded")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Engine{config: config}, nil
}

func (engine *Engine) Run(ctx context.Context, tasks []Task) error {
	if ctx == nil {
		return errors.New("scheduler context is required")
	}
	seen := make(map[string]struct{}, len(tasks))
	queue := make(taskHeap, 0, len(tasks))
	now := engine.config.Now()
	for _, task := range tasks {
		if task.Key == "" || task.Interval <= 0 || task.Run == nil {
			return errors.New("invalid scheduled task")
		}
		if _, exists := seen[task.Key]; exists {
			return errors.New("duplicate scheduled task key")
		}
		seen[task.Key] = struct{}{}
		queue = append(queue, &scheduledTask{Task: task, next: StableNextRun(task.Key, task.Interval, now)})
	}
	if len(queue) == 0 {
		<-ctx.Done()
		return ctx.Err()
	}
	heap.Init(&queue)
	jobs := make(chan Task, engine.config.QueueCapacity)
	var workers sync.WaitGroup
	for index := 0; index < engine.config.Workers; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case task, ok := <-jobs:
					if !ok || ctx.Err() != nil {
						return
					}
					func() {
						defer func() { _ = recover() }()
						task.Run(ctx)
					}()
				}
			}
		}()
	}
	defer func() {
		close(jobs)
		workers.Wait()
	}()
	timer := time.NewTimer(time.Until(queue[0].next))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			now = engine.config.Now()
			for len(queue) > 0 && !queue[0].next.After(now) {
				current := queue[0]
				select {
				case jobs <- current.Task:
				case <-ctx.Done():
					return ctx.Err()
				}
				current.next = advanceNext(current.next, current.Interval, now)
				heap.Fix(&queue, 0)
			}
			delay := time.Until(queue[0].next)
			if delay < 0 {
				delay = 0
			}
			timer.Reset(delay)
		}
	}
}

// StableNextRun distributes a task's phase over its interval using a stable
// hash. Restarts and reloads preserve the phase and do not synchronize storms.
func StableNextRun(key string, interval time.Duration, now time.Time) time.Time {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(key))
	phase := time.Duration(hasher.Sum64() % uint64(interval))
	base := now.Truncate(interval)
	next := base.Add(phase)
	if !next.After(now) {
		next = next.Add(interval)
	}
	return next
}

func advanceNext(previous time.Time, interval time.Duration, now time.Time) time.Time {
	next := previous.Add(interval)
	if next.After(now) {
		return next
	}
	missed := now.Sub(next)/interval + 1
	return next.Add(missed * interval)
}

type scheduledTask struct {
	Task
	next  time.Time
	index int
}

type taskHeap []*scheduledTask

func (items taskHeap) Len() int           { return len(items) }
func (items taskHeap) Less(i, j int) bool { return items[i].next.Before(items[j].next) }
func (items taskHeap) Swap(i, j int) {
	items[i], items[j] = items[j], items[i]
	items[i].index, items[j].index = i, j
}
func (items *taskHeap) Push(value any) {
	item := value.(*scheduledTask)
	item.index = len(*items)
	*items = append(*items, item)
}
func (items *taskHeap) Pop() any {
	old := *items
	item := old[len(old)-1]
	old[len(old)-1] = nil
	*items = old[:len(old)-1]
	return item
}
