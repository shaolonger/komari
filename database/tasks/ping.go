package tasks

import (
	"context"
	"errors"
	timepkg "time"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/telemetrywriter"
	"github.com/komari-monitor/komari/internal/historycache"
	"github.com/komari-monitor/komari/utils"
	"gorm.io/gorm"
)

var ErrPingTaskNotAssigned = errors.New("ping task is not assigned to this client")

const maxPingSQLClientFilter = 256

type PingSetQueryResult struct {
	Records    []models.PingRecord
	SQLQueries int
}

// AddPingTask 创建延迟监测任务。defaultOn 表示新加入的服务器是否自动开启此监测。
func AddPingTask(clients []string, defaultOn bool, name string, target, task_type string, interval int) (uint, error) {
	db := dbcore.GetDBInstance()
	normalizedClients := normalizePingClients(models.StringArray(clients))
	task := models.PingTask{
		Clients:   normalizedClients,
		DefaultOn: defaultOn,
		Name:      name,
		Type:      task_type,
		Target:    target,
		Interval:  interval,
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&task).Error; err != nil {
			return err
		}

		// Append by id to avoid races between concurrent create requests.
		result := tx.Model(&models.PingTask{}).Where("id = ?", task.Id).Update("weight", int(task.Id))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}

		return nil
	})
	if err != nil {
		return 0, err
	}
	historycache.Invalidate()
	ReloadPingSchedule()
	return task.Id, nil
}

func DeletePingTask(id []uint) error {
	db := dbcore.GetDBInstance()
	err := db.Transaction(func(tx *gorm.DB) error {
		// PingRecord has historically existed without a database-enforced foreign
		// key on some installations. Delete it explicitly so deleting a task never
		// leaves historical samples that can be mistaken for an active task.
		var taskCount int64
		if err := tx.Model(&models.PingTask{}).Where("id IN ?", id).Count(&taskCount).Error; err != nil {
			return err
		}
		if taskCount == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Where("task_id IN ?", id).Delete(&models.PingRecord{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id IN ?", id).Delete(&models.PingTask{}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	historycache.Invalidate()
	ReloadPingSchedule()
	return nil
}

// EditPingTask 批量更新延迟监测任务配置。
func EditPingTask(tasks []*models.PingTask) error {
	db := dbcore.GetDBInstance()
	for _, task := range tasks {
		task.Clients = normalizePingClients(task.Clients)
		// 使用 map 显式更新，避免 GORM struct Updates 跳过 false/0/空切片等零值。
		updates := map[string]interface{}{
			"name":        task.Name,
			"clients":     task.Clients,
			"all_clients": task.DefaultOn,
			"type":        task.Type,
			"target":      task.Target,
			"interval":    task.Interval,
		}
		result := db.Model(&models.PingTask{}).Where("id = ?", task.Id).Updates(updates)
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
	}
	historycache.Invalidate()
	ReloadPingSchedule()
	return nil
}

// normalizePingClients 保持 clients 字段序列化为 JSON 数组，避免空值变成 null。
func normalizePingClients(clients models.StringArray) models.StringArray {
	if clients == nil {
		return models.StringArray{}
	}
	return clients
}

func GetAllPingTasks() ([]models.PingTask, error) {
	db := dbcore.GetDBInstance()
	var tasks []models.PingTask
	if err := db.Order("weight ASC").Order("id ASC").Find(&tasks).Error; err != nil {
		return nil, err
	}
	return tasks, nil
}

// GetPingTasksByClient 获取指定服务器需要执行的延迟监测任务。
func GetPingTasksByClient(uuid string) []models.PingTask {
	if uuid == "" {
		return nil
	}
	index, err := loadPingAssignmentIndex()
	if err != nil {
		return nil
	}
	return cloneAssignedTasks(index.tasksByClient[uuid])
}

func UpdatePingTaskOrder(order map[uint]int) error {
	db := dbcore.GetDBInstance()
	err := db.Transaction(func(tx *gorm.DB) error {
		for id, weight := range order {
			result := tx.Model(&models.PingTask{}).Where("id = ?", id).Update("weight", weight)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return gorm.ErrRecordNotFound
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	historycache.Invalidate()
	ReloadPingSchedule()
	return nil
}

func SavePingRecord(record models.PingRecord) error {
	index, err := loadPingAssignmentIndex()
	if err != nil {
		return err
	}
	if _, exists := index.taskIDs[record.TaskId]; !exists {
		return gorm.ErrRecordNotFound
	}
	if _, assigned := index.assignments[pingAssignmentKey{client: record.Client, taskID: record.TaskId}]; !assigned {
		return ErrPingTaskNotAssigned
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*timepkg.Second)
	defer cancel()
	return telemetrywriter.Submit(ctx, telemetrywriter.Batch{PingRecords: []models.PingRecord{record}})
}

func DeletePingRecordsBefore(time timepkg.Time) error {
	db := dbcore.GetDBInstance()
	now := timepkg.Now()
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("time < ?", time).Delete(&models.PingRecord{}).Error; err != nil {
			return err
		}
		for _, tier := range []struct {
			resolution int
			retention  timepkg.Duration
		}{
			{60, 7 * 24 * timepkg.Hour},
			{900, 90 * 24 * timepkg.Hour},
			{3600, 730 * 24 * timepkg.Hour},
		} {
			if err := tx.Where("resolution_seconds = ? AND bucket_time < ?", tier.resolution, models.FromTime(now.Add(-tier.retention))).Delete(&models.PingRollup{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		historycache.Invalidate()
	}
	return err
}

func DeletePingRecords(id []uint) error {
	db := dbcore.GetDBInstance()
	var affected int64
	err := db.Transaction(func(tx *gorm.DB) error {
		result := tx.Where("task_id IN ?", id).Delete(&models.PingRecord{})
		if result.Error != nil {
			return result.Error
		}
		affected += result.RowsAffected
		result = tx.Where("task_id IN ?", id).Delete(&models.PingRollup{})
		if result.Error != nil {
			return result.Error
		}
		affected += result.RowsAffected
		return nil
	})
	if err == nil && affected == 0 {
		return gorm.ErrRecordNotFound
	}
	if err == nil {
		historycache.Invalidate()
	}
	return err
}

func DeleteAllPingRecords() error {
	db := dbcore.GetDBInstance()
	var affected int64
	err := db.Transaction(func(tx *gorm.DB) error {
		for _, table := range []string{"ping_records", "ping_rollups"} {
			result := tx.Exec("DELETE FROM " + table)
			if result.Error != nil {
				return result.Error
			}
			affected += result.RowsAffected
		}
		return nil
	})
	if err == nil && affected == 0 {
		return gorm.ErrRecordNotFound
	}
	if err == nil {
		historycache.Invalidate()
	}
	return err
}
func ReloadPingSchedule() error {
	pingTasks, err := GetAllPingTasks()
	if err != nil {
		return err
	}
	publishPingAssignmentIndex(pingTasks)
	return utils.ReloadPingSchedule(pingTasks)
}

// AddDefaultOnClientUUID 在新客户端注册后，把该 UUID 追加到所有 default_on=true 的任务的 clients 中（去重）。
func AddDefaultOnClientUUID(uuid string) error {
	if uuid == "" {
		return nil
	}
	db := dbcore.GetDBInstance()
	var tasks []models.PingTask
	if err := db.Where("all_clients = ?", true).Find(&tasks).Error; err != nil {
		return err
	}
	if len(tasks) == 0 {
		return nil
	}
	changed := false
	for _, task := range tasks {
		exists := false
		for _, c := range task.Clients {
			if c == uuid {
				exists = true
				break
			}
		}
		if exists {
			continue
		}
		next := append(models.StringArray{}, task.Clients...)
		next = append(next, uuid)
		if err := db.Model(&models.PingTask{}).Where("id = ?", task.Id).Update("clients", next).Error; err != nil {
			return err
		}
		changed = true
	}
	if changed {
		historycache.Invalidate()
		return ReloadPingSchedule()
	}
	return nil
}

// MigrateAllClientsExpansion 启动时把旧版 default_on=true 且 clients 为空的任务展开为当前所有客户端 UUID。
// 迁移后 clients 始终是显式列表，调度路径不再依赖 default_on。
func MigrateAllClientsExpansion() error {
	db := dbcore.GetDBInstance()
	var tasks []models.PingTask
	if err := db.Where("all_clients = ?", true).Find(&tasks).Error; err != nil {
		return err
	}
	if len(tasks) == 0 {
		return nil
	}
	var clients []models.Client
	if err := db.Select("uuid").Find(&clients).Error; err != nil {
		return err
	}
	if len(clients) == 0 {
		return nil
	}
	allUUIDs := make(models.StringArray, 0, len(clients))
	for _, c := range clients {
		if c.UUID != "" {
			allUUIDs = append(allUUIDs, c.UUID)
		}
	}
	for _, task := range tasks {
		if len(task.Clients) > 0 {
			continue
		}
		if err := db.Model(&models.PingTask{}).Where("id = ?", task.Id).Update("clients", allUUIDs).Error; err != nil {
			return err
		}
	}
	return nil
}

func GetPingRecords(uuid string, taskId int, start, end timepkg.Time) ([]models.PingRecord, error) {
	db := dbcore.GetDBInstance()
	var records []models.PingRecord
	// Old database versions could leave ping_records behind after their
	// ping_tasks row was removed. An inner join makes those orphaned records
	// invisible immediately, even before the operator's next retention cleanup.
	dbQuery := db.Model(&models.PingRecord{}).Joins("INNER JOIN ping_tasks ON ping_tasks.id = ping_records.task_id")
	if uuid != "" {
		dbQuery = dbQuery.Where("ping_records.client = ?", uuid)
	}
	if taskId >= 0 {
		dbQuery = dbQuery.Where("ping_records.task_id = ?", uint(taskId))
	}
	if err := dbQuery.Where("ping_records.time >= ? AND ping_records.time <= ?", start, end).Order("ping_records.time DESC").Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

// QueryPingRecordsForClients performs one narrow, orphan-safe query for a set
// of authorized clients. nil means all clients; a non-nil empty slice means no
// clients. Large sets are filtered after one scan to avoid SQLite bind limits.
func QueryPingRecordsForClients(ctx context.Context, db *gorm.DB, clientIDs []string, taskID int, start, end timepkg.Time) (PingSetQueryResult, error) {
	result := PingSetQueryResult{}
	if db == nil {
		return result, errors.New("ping database is required")
	}
	if ctx == nil {
		return result, errors.New("ping query context is required")
	}
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return result, errors.New("invalid ping query range")
	}
	if clientIDs != nil && len(clientIDs) == 0 {
		result.Records = []models.PingRecord{}
		return result, nil
	}
	query := db.WithContext(ctx).Model(&models.PingRecord{}).
		Select("ping_records.client,ping_records.task_id,ping_records.time,ping_records.value").
		Joins("INNER JOIN ping_tasks ON ping_tasks.id = ping_records.task_id").
		Where("ping_records.time >= ? AND ping_records.time <= ?", models.FromTime(start), models.FromTime(end))
	filterInSQL := len(clientIDs) > 0 && len(clientIDs) <= maxPingSQLClientFilter
	if filterInSQL {
		query = query.Where("ping_records.client IN ?", clientIDs)
	}
	if taskID >= 0 {
		query = query.Where("ping_records.task_id = ?", uint(taskID))
	}
	result.SQLQueries = 1
	if err := query.Order("ping_records.client ASC,ping_records.task_id ASC,ping_records.time DESC").Find(&result.Records).Error; err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !filterInSQL && len(clientIDs) > 0 {
		allowed := make(map[string]struct{}, len(clientIDs))
		for _, clientID := range clientIDs {
			allowed[clientID] = struct{}{}
		}
		filtered := result.Records[:0]
		for _, record := range result.Records {
			if _, ok := allowed[record.Client]; ok {
				filtered = append(filtered, record)
			}
		}
		result.Records = filtered
	}
	return result, nil
}
