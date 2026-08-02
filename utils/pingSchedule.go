package utils

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/runtimeprofile"
	"github.com/komari-monitor/komari/internal/scheduler"
	"github.com/komari-monitor/komari/ws"
)

type pingMessage struct {
	TaskID  uint   `json:"ping_task_id"`
	Message string `json:"message"`
	Type    string `json:"ping_type"`
	Target  string `json:"ping_target"`
}

const (
	pingLeaseDuration = 3 * time.Minute
	pingLeaseRefresh  = time.Minute
)

type pingLeaseTask struct {
	TaskID     uint   `json:"ping_task_id"`
	Type       string `json:"ping_type"`
	Target     string `json:"ping_target"`
	IntervalMS int64  `json:"interval_ms"`
	PhaseMS    int64  `json:"phase_ms"`
}

type pingLease struct {
	Message   string          `json:"message"`
	Revision  uint64          `json:"revision"`
	IssuedAt  time.Time       `json:"issued_at"`
	ExpiresAt time.Time       `json:"expires_at"`
	Tasks     []pingLeaseTask `json:"tasks"`
}

type pingLeaseDefinition struct {
	revision uint64
	tasks    []pingLeaseTask
}

// PingTaskManager owns exactly one scheduler generation. Each (task,client)
// pair receives a deterministic phase inside the interval, so a large task no
// longer sends to every client in the same second.
type PingTaskManager struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	leases map[string]pingLeaseDefinition
}

var manager = &PingTaskManager{}

func (manager *PingTaskManager) Reload(pingTasks []models.PingTask) error {
	profile, err := runtimeprofile.Current()
	if err != nil {
		return err
	}
	engine, err := scheduler.New(scheduler.Config{Workers: profile.PingWorkers, QueueCapacity: profile.PingQueueCapacity})
	if err != nil {
		return err
	}
	leases := buildPingLeaseDefinitions(pingTasks)
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
					if ws.GetClientTelemetryProtocol(clientID) >= 3 {
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
	for clientID := range leases {
		clientID := clientID
		schedules = append(schedules, scheduler.Task{
			Key: "ping-lease:" + clientID, Interval: pingLeaseRefresh,
			Run: func(ctx context.Context) {
				if ctx.Err() == nil && ws.GetClientTelemetryProtocol(clientID) >= 3 {
					_ = manager.SendLease(clientID)
				}
			},
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	manager.mu.Lock()
	previousCancel, previousDone := manager.cancel, manager.done
	manager.cancel, manager.done, manager.leases = cancel, done, leases
	manager.mu.Unlock()
	if previousCancel != nil {
		previousCancel()
	}
	go func() {
		defer close(done)
		_ = engine.Run(ctx, schedules)
	}()
	for clientID := range leases {
		if ws.GetClientTelemetryProtocol(clientID) >= 3 {
			_ = manager.SendLease(clientID)
		}
	}
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

func buildPingLeaseDefinitions(pingTasks []models.PingTask) map[string]pingLeaseDefinition {
	byClient := make(map[string][]pingLeaseTask)
	for _, task := range pingTasks {
		if task.Id == 0 || task.Interval <= 0 || task.Interval > 3600 {
			continue
		}
		intervalMS := int64(task.Interval) * 1000
		seen := make(map[string]struct{}, len(task.Clients))
		for _, clientID := range task.Clients {
			if clientID == "" {
				continue
			}
			if _, exists := seen[clientID]; exists {
				continue
			}
			seen[clientID] = struct{}{}
			phaseHash := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", task.Id, clientID)))
			phase := int64(binary.LittleEndian.Uint64(phaseHash[:8]) % uint64(intervalMS))
			byClient[clientID] = append(byClient[clientID], pingLeaseTask{
				TaskID: task.Id, Type: task.Type, Target: task.Target, IntervalMS: intervalMS, PhaseMS: phase,
			})
		}
	}
	result := make(map[string]pingLeaseDefinition, len(byClient))
	for clientID, tasks := range byClient {
		sort.Slice(tasks, func(left, right int) bool { return tasks[left].TaskID < tasks[right].TaskID })
		canonical, _ := json.Marshal(tasks)
		digest := sha256.Sum256(canonical)
		revision := binary.LittleEndian.Uint64(digest[:8])
		if revision == 0 {
			revision = 1
		}
		result[clientID] = pingLeaseDefinition{revision: revision, tasks: tasks}
	}
	return result
}

func (manager *PingTaskManager) SendLease(clientID string) error {
	manager.mu.Lock()
	definition, exists := manager.leases[clientID]
	manager.mu.Unlock()
	if !exists {
		return nil
	}
	connection, connected := ws.GetConnectedClient(clientID)
	if !connected || connection == nil || ws.GetClientTelemetryProtocol(clientID) < 3 {
		return nil
	}
	now := time.Now().UTC()
	return connection.WriteJSON(pingLease{
		Message: "ping_lease", Revision: definition.revision, IssuedAt: now,
		ExpiresAt: now.Add(pingLeaseDuration), Tasks: append([]pingLeaseTask(nil), definition.tasks...),
	})
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
func SendPingLease(clientID string) error                  { return manager.SendLease(clientID) }
