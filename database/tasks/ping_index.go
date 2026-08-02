package tasks

import (
	"sync"
	"sync/atomic"

	"github.com/komari-monitor/komari/database/models"
)

type pingAssignmentKey struct {
	client string
	taskID uint
}

// pingAssignmentIndex is immutable after publication. Readers never take a
// lock and therefore Ping ingest and agent schedule requests cannot contend
// with admin edits.
type pingAssignmentIndex struct {
	generation    uint64
	tasksByClient map[string][]models.PingTask
	tasksByID     map[uint]models.PingTask
	taskIDs       map[uint]struct{}
	assignments   map[pingAssignmentKey]struct{}
}

var (
	pingAssignmentCurrent atomic.Pointer[pingAssignmentIndex]
	pingAssignmentLoadMu  sync.Mutex
	pingAssignmentVersion atomic.Uint64
)

func buildPingAssignmentIndex(pingTasks []models.PingTask) *pingAssignmentIndex {
	index := &pingAssignmentIndex{
		generation:    pingAssignmentVersion.Add(1),
		tasksByClient: make(map[string][]models.PingTask),
		tasksByID:     make(map[uint]models.PingTask, len(pingTasks)),
		taskIDs:       make(map[uint]struct{}, len(pingTasks)),
		assignments:   make(map[pingAssignmentKey]struct{}),
	}
	for _, source := range pingTasks {
		task := source
		task.Clients = append(models.StringArray(nil), source.Clients...)
		index.taskIDs[task.Id] = struct{}{}
		index.tasksByID[task.Id] = task
		seen := make(map[string]struct{}, len(task.Clients))
		for _, clientID := range task.Clients {
			if clientID == "" {
				continue
			}
			if _, duplicate := seen[clientID]; duplicate {
				continue
			}
			seen[clientID] = struct{}{}
			index.assignments[pingAssignmentKey{client: clientID, taskID: task.Id}] = struct{}{}
			index.tasksByClient[clientID] = append(index.tasksByClient[clientID], task)
		}
	}
	return index
}

func publishPingAssignmentIndex(pingTasks []models.PingTask) *pingAssignmentIndex {
	index := buildPingAssignmentIndex(pingTasks)
	pingAssignmentCurrent.Store(index)
	return index
}

func loadPingAssignmentIndex() (*pingAssignmentIndex, error) {
	if index := pingAssignmentCurrent.Load(); index != nil {
		return index, nil
	}
	pingAssignmentLoadMu.Lock()
	defer pingAssignmentLoadMu.Unlock()
	if index := pingAssignmentCurrent.Load(); index != nil {
		return index, nil
	}
	pingTasks, err := GetAllPingTasks()
	if err != nil {
		return nil, err
	}
	return publishPingAssignmentIndex(pingTasks), nil
}

func cloneAssignedTasks(tasks []models.PingTask) []models.PingTask {
	if len(tasks) == 0 {
		return []models.PingTask{}
	}
	result := make([]models.PingTask, len(tasks))
	for position, source := range tasks {
		result[position] = source
		result[position].Clients = append(models.StringArray(nil), source.Clients...)
	}
	return result
}
