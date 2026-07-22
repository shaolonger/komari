package utils

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/scheduler"
	"github.com/komari-monitor/komari/ws"
)

const (
	pingScheduleWorkers = 16
	pingScheduleQueue   = 2_048
)

type pingMessage struct {
	TaskID  uint   `json:"ping_task_id"`
	Message string `json:"message"`
	Type    string `json:"ping_type"`
	Target  string `json:"ping_target"`
}

// PingTaskManager owns exactly one scheduler generation. Each (task,client)
// pair receives a deterministic phase inside the interval, so a large task no
// longer sends to every client in the same second.
type PingTaskManager struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

var manager = &PingTaskManager{}

func (manager *PingTaskManager) Reload(pingTasks []models.PingTask) error {
	engine, err := scheduler.New(scheduler.Config{Workers: pingScheduleWorkers, QueueCapacity: pingScheduleQueue})
	if err != nil {
		return err
	}
	schedules := make([]scheduler.Task, 0)
	for _, task := range pingTasks {
		if task.Interval <= 0 {
			continue
		}
		message := pingMessage{TaskID: task.Id, Message: "ping", Type: task.Type, Target: task.Target}
		seenClients := make(map[string]struct{}, len(task.Clients))
		for _, clientID := range task.Clients {
			if clientID == "" {
				continue
			}
			if _, exists := seenClients[clientID]; exists {
				continue
			}
			seenClients[clientID] = struct{}{}
			clientID := clientID
			schedules = append(schedules, scheduler.Task{
				Key: fmt.Sprintf("ping:%d:%s", task.Id, clientID), Interval: time.Duration(task.Interval) * time.Second,
				Run: func(ctx context.Context) {
					if ctx.Err() != nil {
						return
					}
					connection, ok := ws.GetConnectedClient(clientID)
					if !ok || connection == nil {
						return
					}
					_ = connection.WriteJSON(message)
				},
			})
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	manager.mu.Lock()
	previousCancel, previousDone := manager.cancel, manager.done
	manager.cancel, manager.done = cancel, done
	manager.mu.Unlock()
	if previousCancel != nil {
		previousCancel()
	}
	go func() {
		defer close(done)
		_ = engine.Run(ctx, schedules)
	}()
	// A context-aware old generation normally exits immediately. Do not block a
	// control-plane edit on an already-running network write; K-502 provides the
	// per-connection write deadline that bounds that final operation.
	if previousDone != nil {
		select {
		case <-previousDone:
		case <-time.After(10 * time.Millisecond):
		}
	}
	return nil
}

func (manager *PingTaskManager) Stop(ctx context.Context) error {
	manager.mu.Lock()
	cancel, done := manager.cancel, manager.done
	manager.cancel, manager.done = nil, nil
	manager.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func ReloadPingSchedule(pingTasks []models.PingTask) error { return manager.Reload(pingTasks) }
func StopPingSchedule(ctx context.Context) error           { return manager.Stop(ctx) }
