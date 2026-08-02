package notifier

import (
	"context"
	"log"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	messageevent "github.com/komari-monitor/komari/database/models/messageEvent"
	"github.com/komari-monitor/komari/database/records"
	"github.com/komari-monitor/komari/internal/scheduler"
	"github.com/komari-monitor/komari/utils/messageSender"
	"gorm.io/gorm"
)

// LoadNotificationService 管理定时器和任务
type LoadNotificationService struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

var LoadNotificationManager = &LoadNotificationService{}

// Reload 重载时间表
func (m *LoadNotificationService) Reload(loadNotifications []models.LoadNotification) error {
	engine, err := scheduler.New(scheduler.Config{Workers: 4, QueueCapacity: 64})
	if err != nil {
		return err
	}
	taskGroups := make(map[int][]models.LoadNotification)
	for _, task := range loadNotifications {
		if task.Interval <= 0 {
			continue
		}
		taskGroups[task.Interval] = append(taskGroups[task.Interval], task)
	}
	schedules := make([]scheduler.Task, 0, len(taskGroups))
	for interval, tasks := range taskGroups {
		tasks := append([]models.LoadNotification(nil), tasks...)
		schedules = append(schedules, scheduler.Task{
			Key: "load-notification:" + strconv.Itoa(interval), Interval: time.Duration(interval) * time.Minute,
			Run: func(ctx context.Context) { executeLoadNotificationTasksContext(ctx, tasks, time.Now()) },
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.mu.Lock()
	previousCancel, previousDone := m.cancel, m.done
	m.cancel, m.done = cancel, done
	m.mu.Unlock()
	if previousCancel != nil {
		previousCancel()
	}
	go func() {
		defer close(done)
		_ = engine.Run(ctx, schedules)
	}()
	if previousDone != nil {
		select {
		case <-previousDone:
		case <-time.After(10 * time.Millisecond):
		}
	}
	return nil
}

func (m *LoadNotificationService) Stop(ctx context.Context) error {
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.cancel, m.done = nil, nil
	m.mu.Unlock()
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

// executeLoadNotificationTask 执行单个LoadNotificationTask
func executeLoadNotificationTask(task models.LoadNotification) {
	executeLoadNotificationTasks([]models.LoadNotification{task})
}

func executeLoadNotificationTasks(loadTasks []models.LoadNotification) {
	executeLoadNotificationTasksContext(context.Background(), loadTasks, time.Now())
}

func executeLoadNotificationTasksContext(ctx context.Context, loadTasks []models.LoadNotification, now time.Time) {
	results, _, err := evaluateLoadNotificationTasks(ctx, dbcore.GetDBInstance(), loadTasks, now)
	if err != nil {
		log.Printf("Failed to evaluate load notification tasks: %v", err)
		return
	}
	dueTaskIDs := make([]uint, 0, len(results))
	for _, task := range loadTasks {
		if ctx.Err() != nil {
			return
		}
		clientsForTask, ok := results[task.Id]
		if !ok {
			continue
		}
		dueTaskIDs = append(dueTaskIDs, task.Id)
		sendLoadNotificationClients(clientsForTask, task)
	}
	updateLastNotifiedTasksContext(ctx, dueTaskIDs, now)
}

// shouldSkipNotification 检查是否应该跳过通知（冷却期检查）
func shouldSkipNotification(task models.LoadNotification) bool {
	return shouldSkipNotificationAt(task, time.Now())
}

func shouldSkipNotificationAt(task models.LoadNotification, now time.Time) bool {
	if task.LastNotified.ToTime().IsZero() {
		return false
	}

	// 计算冷却期（使用 interval 作为冷却期）
	cooldownPeriod := time.Duration(task.Interval) * time.Minute
	timeSinceLastNotified := now.Sub(task.LastNotified.ToTime())

	return timeSinceLastNotified < cooldownPeriod
}

// checkMetricThreshold 检查指标是否达到阈值
func checkMetricThreshold(records []models.Record, task models.LoadNotification) bool {
	return checkMetricThresholdWithClients(records, task, nil)
}

func checkMetricThresholdWithClients(records []models.Record, task models.LoadNotification, clientByUUID map[string]models.Client) bool {
	if len(records) == 0 {
		return false
	}

	// 计算需要达标的最小记录数
	minRequiredRecords := int(float32(len(records)) * task.Ratio)
	if minRequiredRecords == 0 {
		minRequiredRecords = 1
	}

	exceededCount := 0

	for _, record := range records {
		client := clientByUUID[record.Client]
		if clientByUUID == nil {
			client = models.Client{UUID: record.Client, MemTotal: record.RamTotal, SwapTotal: record.SwapTotal, DiskTotal: record.DiskTotal}
		}
		metricValue := getMetricValueForClient(record, task.Metric, client)
		if metricValue >= task.Threshold {
			exceededCount++
		}
	}

	return exceededCount >= minRequiredRecords
}

// getMetricValue 根据指标名称获取记录中的对应值
func getMetricValue(record models.Record, metric string) float32 {
	return getMetricValueForClient(record, metric, models.Client{
		UUID: record.Client, MemTotal: record.RamTotal, SwapTotal: record.SwapTotal, DiskTotal: record.DiskTotal,
	})
}

func getMetricValueForClient(record models.Record, metric string, client models.Client) float32 {
	switch metric {
	case "cpu":
		return record.Cpu
	case "gpu":
		return record.Gpu
	case "net_in", "netin":
		return bytesPerSecondToMbps(record.NetIn)
	case "net_out", "netout":
		return bytesPerSecondToMbps(record.NetOut)
	case "ram":
		if record.RamTotal > 0 && client.MemTotal > 0 {
			return float32(record.Ram) / float32(client.MemTotal) * 100
		}
		return 0
	case "swap":
		if record.SwapTotal > 0 && client.SwapTotal > 0 {
			return float32(record.Swap) / float32(client.SwapTotal) * 100
		}
		return 0
	case "load":
		return record.Load
	case "temp":
		return record.Temp
	case "disk":
		if record.DiskTotal > 0 && client.DiskTotal > 0 {
			return float32(record.Disk) / float32(client.DiskTotal) * 100
		}
		return 0
	default:
		// 尝试通过反射获取字段值
		v := reflect.ValueOf(record)
		field := v.FieldByName(metric)
		if field.IsValid() && field.CanInterface() {
			switch field.Kind() {
			case reflect.Float32:
				return float32(field.Float())
			case reflect.Float64:
				return float32(field.Float())
			case reflect.Int, reflect.Int32, reflect.Int64:
				return float32(field.Int())
			}
		}
		return 0
	}
}

// evaluateLoadNotificationTasks groups tasks by interval, scans each interval
// once, and reuses a single client metadata query for percentage metrics.
func evaluateLoadNotificationTasks(ctx context.Context, db *gorm.DB, loadTasks []models.LoadNotification, now time.Time) (map[uint][]models.Client, int, error) {
	results := make(map[uint][]models.Client)
	dueByInterval := make(map[int][]models.LoadNotification)
	for _, task := range loadTasks {
		if task.Interval <= 0 || shouldSkipNotificationAt(task, now) {
			continue
		}
		dueByInterval[task.Interval] = append(dueByInterval[task.Interval], task)
		results[task.Id] = []models.Client{}
	}
	if len(dueByInterval) == 0 {
		return results, 0, nil
	}
	var allClients []models.Client
	if err := db.WithContext(ctx).Find(&allClients).Error; err != nil {
		return nil, 1, err
	}
	queries := 1
	clientByUUID := make(map[string]models.Client, len(allClients))
	for _, client := range allClients {
		clientByUUID[client.UUID] = client
	}
	for interval, intervalTasks := range dueByInterval {
		clientSet := make(map[string]struct{})
		for _, task := range intervalTasks {
			for _, clientID := range task.Clients {
				if _, ok := clientByUUID[clientID]; ok {
					clientSet[clientID] = struct{}{}
				}
			}
		}
		clientIDs := make([]string, 0, len(clientSet))
		for clientID := range clientSet {
			clientIDs = append(clientIDs, clientID)
		}
		queryResult, err := records.QueryRecordsForClients(ctx, db, clientIDs, now.Add(-time.Duration(interval)*time.Minute), now, "all")
		queries += queryResult.SQLQueries
		if err != nil {
			return nil, queries, err
		}
		recordsByClient := make(map[string][]models.Record, len(clientIDs))
		for _, record := range queryResult.Records {
			recordsByClient[record.Client] = append(recordsByClient[record.Client], record)
		}
		for _, task := range intervalTasks {
			for _, clientID := range task.Clients {
				client, ok := clientByUUID[clientID]
				if !ok {
					continue
				}
				if checkMetricThresholdWithClients(recordsByClient[clientID], task, clientByUUID) {
					results[task.Id] = append(results[task.Id], client)
				}
			}
		}
	}
	return results, queries, nil
}

func bytesPerSecondToMbps(bytesPerSecond int64) float32 {
	if bytesPerSecond <= 0 {
		return 0
	}

	// 采用十进制 Mbps：1 Mbps = 1,000,000 bit/s
	return float32(float64(bytesPerSecond) * 8 / 1_000_000)
}

// sendLoadNotification 发送负载通知
func sendLoadNotificationClients(matchedClients []models.Client, task models.LoadNotification) {
	if len(matchedClients) == 0 {
		return
	}
	_ = messageSender.SendEvent(models.EventMessage{
		Event:   messageevent.Alert,
		Clients: matchedClients,
		Time:    time.Now(),
		Emoji:   "⚠️",
		Message: task.Name,
	})
}

// updateLastNotified 更新最后通知时间
func updateLastNotified(taskId uint, notifyTime time.Time) {
	updateLastNotifiedTasks([]uint{taskId}, notifyTime)
}

func updateLastNotifiedTasks(taskIDs []uint, notifyTime time.Time) {
	updateLastNotifiedTasksContext(context.Background(), taskIDs, notifyTime)
}

func updateLastNotifiedTasksContext(ctx context.Context, taskIDs []uint, notifyTime time.Time) {
	if len(taskIDs) == 0 {
		return
	}
	db := dbcore.GetDBInstance().WithContext(ctx)
	if err := db.Model(&models.LoadNotification{}).Where("id IN ?", taskIDs).Update("last_notified", notifyTime).Error; err != nil {
		log.Printf("Failed to update last_notified for tasks %v: %v", taskIDs, err)
	}
}

// ReloadLoadNotificationSchedule 加载或重载时间表
func ReloadLoadNotificationSchedule(loadNotifications []models.LoadNotification) error {
	return LoadNotificationManager.Reload(loadNotifications)
}

func StopLoadNotificationSchedule(ctx context.Context) error {
	return LoadNotificationManager.Stop(ctx)
}
