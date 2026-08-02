package jsonRpc

import (
	"context"
	"errors"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/metrics"
	"github.com/komari-monitor/komari/database/models"
	recordsdb "github.com/komari-monitor/komari/database/records"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/utils/rpc"
)

type metricPoint struct {
	Time  models.LocalTime `json:"time"`
	Value *float64         `json:"value"`
	Count int64            `json:"count,omitempty"`
}

type metricSeries struct {
	MetricKey           string            `json:"metric_key"`
	EntityID            string            `json:"entity_id"`
	Type                string            `json:"type,omitempty"`
	Unit                string            `json:"unit,omitempty"`
	RetentionDays       int               `json:"retention_days,omitempty"`
	Downsampled         bool              `json:"downsampled,omitempty"`
	DownsampleAlgorithm string            `json:"downsample_algorithm,omitempty"`
	MaxPoints           int               `json:"max_points,omitempty"`
	IntervalSeconds     int               `json:"interval_seconds,omitempty"`
	Tags                map[string]string `json:"tags,omitempty"`
	Count               int               `json:"count"`
	Points              []metricPoint     `json:"points"`
}

type metricRangeParams struct {
	EntityID   string   `json:"entity_id"`
	MetricKeys []string `json:"metric_keys"`
	Hours      int      `json:"hours"`
	Start      string   `json:"start"`
	End        string   `json:"end"`
	MaxPoints  int      `json:"max_points"`
}

func parseMetricRange(params metricRangeParams, permission recordsdb.QueryPermission) (time.Time, time.Time, int, error) {
	end := time.Now()
	var err error
	if params.End != "" {
		end, err = time.Parse(time.RFC3339, params.End)
		if err != nil {
			return time.Time{}, time.Time{}, 0, errors.New("invalid end time")
		}
	}
	start := time.Time{}
	if params.Start != "" {
		start, err = time.Parse(time.RFC3339, params.Start)
		if err != nil {
			return time.Time{}, time.Time{}, 0, errors.New("invalid start time")
		}
	} else {
		hours := params.Hours
		if hours <= 0 {
			hours = 1
		}
		start = end.Add(-time.Duration(hours) * time.Hour)
	}
	maxPoints, err := recordsdb.ValidateQueryBudget(start, end, 1, params.MaxPoints, permission)
	return start, end, maxPoints, err
}

func authorizeMetricEntity(entityID string, permission string) error {
	if entityID == "" {
		return errors.New("entity_id is required")
	}
	client, err := clients.GetClientBasicInfo(entityID)
	if err != nil {
		return errors.New("entity not found")
	}
	if permission != "admin" && client.Hidden {
		return errors.New("entity not found")
	}
	return nil
}

func floatPointer(value float64) *float64 { return &value }

func recordMetricValue(record models.Record, key string, previous *models.Record) *float64 {
	switch key {
	case "cpu.usage":
		return floatPointer(float64(record.Cpu))
	case "gpu.usage":
		return floatPointer(float64(record.Gpu))
	case "memory.used":
		return floatPointer(float64(record.Ram))
	case "memory.total":
		return floatPointer(float64(record.RamTotal))
	case "swap.used":
		return floatPointer(float64(record.Swap))
	case "swap.total":
		return floatPointer(float64(record.SwapTotal))
	case "load.average":
		return floatPointer(float64(record.Load))
	case "temperature":
		return floatPointer(float64(record.Temp))
	case "disk.used":
		return floatPointer(float64(record.Disk))
	case "disk.total":
		return floatPointer(float64(record.DiskTotal))
	case "net.in.rate":
		return floatPointer(float64(record.NetIn))
	case "net.out.rate":
		return floatPointer(float64(record.NetOut))
	case "net.total.up":
		return floatPointer(float64(record.NetTotalUp))
	case "net.total.down":
		return floatPointer(float64(record.NetTotalDown))
	case "traffic.up", "traffic.down":
		if previous == nil {
			return nil
		}
		current, before := record.NetTotalUp, previous.NetTotalUp
		if key == "traffic.down" {
			current, before = record.NetTotalDown, previous.NetTotalDown
		}
		delta := current - before
		if delta < 0 {
			delta = max(current, 0)
		}
		return floatPointer(float64(delta))
	case "process.count":
		return floatPointer(float64(record.Process))
	case "connections.tcp":
		return floatPointer(float64(max(record.Connections-record.ConnectionsUdp, 0)))
	case "connections.udp":
		return floatPointer(float64(record.ConnectionsUdp))
	default:
		return nil
	}
}

func uniformlyLimitPoints(points []metricPoint, target int) []metricPoint {
	if target <= 0 || len(points) == 0 {
		return []metricPoint{}
	}
	if len(points) <= target {
		return points
	}
	if target == 1 {
		return []metricPoint{points[len(points)-1]}
	}
	result := make([]metricPoint, 0, target)
	for index := 0; index < target; index++ {
		position := int(math.Round(float64(index) * float64(len(points)-1) / float64(target-1)))
		result = append(result, points[position])
	}
	return result
}

func enforceMetricSeriesBudget(series []metricSeries, maxPoints int) []metricSeries {
	if len(series) == 0 {
		return series
	}
	target := max(maxPoints/len(series), 1)
	for index := range series {
		before := len(series[index].Points)
		series[index].Points = uniformlyLimitPoints(series[index].Points, target)
		series[index].Count = len(series[index].Points)
		if len(series[index].Points) < before {
			series[index].Downsampled = true
			series[index].DownsampleAlgorithm = "uniform-storage-tier"
		}
	}
	return series
}

func queryMetrics(ctx context.Context, request *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params metricRangeParams
	request.BindParams(&params)
	meta := rpc.MetaFromContext(ctx)
	if err := authorizeMetricEntity(params.EntityID, meta.Permission); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	if len(params.MetricKeys) == 0 || len(params.MetricKeys) > 64 {
		return nil, rpc.MakeError(rpc.InvalidParams, "metric_keys must contain 1 to 64 entries", nil)
	}
	permission := recordsdb.QueryPermissionPublic
	if meta.Permission == "admin" {
		permission = recordsdb.QueryPermissionAdmin
	}
	start, end, maxPoints, err := parseMetricRange(params, permission)
	if err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	keys := make([]string, 0, len(params.MetricKeys))
	seen := make(map[string]struct{}, len(params.MetricKeys))
	definitions := make(map[string]metrics.Definition, len(params.MetricKeys))
	catalog, catalogErr := metrics.List(ctx, dbcore.GetDBInstance())
	if catalogErr != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to load metric definitions", catalogErr.Error())
	}
	catalogByName := make(map[string]metrics.Definition, len(catalog))
	for _, definition := range catalog {
		catalogByName[definition.Name] = definition
	}
	for _, key := range params.MetricKeys {
		definition, ok := catalogByName[key]
		if !ok {
			return nil, rpc.MakeError(rpc.InvalidParams, "unknown metric key", key)
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
		definitions[key] = definition
	}
	perQueryBudget := max(maxPoints/len(keys), 1)
	series := make([]metricSeries, 0, len(keys))

	needsRecords, needsGPU, needsPing := false, false, false
	for _, key := range keys {
		switch key {
		case "gpu.device.usage", "gpu.memory.used", "gpu.memory.total", "gpu.temperature":
			needsGPU = true
		case "ping.latency_ms":
			needsPing = true
		default:
			needsRecords = true
		}
	}
	if needsRecords {
		records, err := recordsdb.QueryRecordsDefault(ctx, recordsdb.RecordQuery{Client: params.EntityID, Start: start, End: end, LoadType: "all", MaxPoints: perQueryBudget})
		if err != nil {
			return nil, rpc.MakeError(rpc.InternalError, "Failed to query metrics", err.Error())
		}
		for _, key := range keys {
			if key == "ping.latency_ms" || key == "gpu.device.usage" || key == "gpu.memory.used" || key == "gpu.memory.total" || key == "gpu.temperature" {
				continue
			}
			definition := definitions[key]
			retentionStart := end.Add(-time.Duration(definition.RetentionDays) * 24 * time.Hour)
			points := make([]metricPoint, 0, len(records))
			for position, record := range records {
				if record.Time.ToTime().Before(retentionStart) {
					continue
				}
				var previous *models.Record
				if position > 0 {
					previous = &records[position-1]
				}
				points = append(points, metricPoint{Time: record.Time, Value: recordMetricValue(record, key, previous), Count: 1})
			}
			series = append(series, metricSeries{MetricKey: key, EntityID: params.EntityID, Type: definition.Type, Unit: definition.Unit, RetentionDays: definition.RetentionDays, MaxPoints: perQueryBudget, Count: len(points), Points: points})
		}
	}
	if needsGPU {
		rows, err := recordsdb.QueryGPURecordsDefault(ctx, recordsdb.GPUQuery{Client: params.EntityID, Start: start, End: end, MaxPoints: perQueryBudget})
		if err != nil {
			return nil, rpc.MakeError(rpc.InternalError, "Failed to query GPU metrics", err.Error())
		}
		for _, key := range keys {
			if key != "gpu.device.usage" && key != "gpu.memory.used" && key != "gpu.memory.total" && key != "gpu.temperature" {
				continue
			}
			definition := definitions[key]
			retentionStart := end.Add(-time.Duration(definition.RetentionDays) * 24 * time.Hour)
			byDevice := make(map[int][]metricPoint)
			names := make(map[int]string)
			for _, row := range rows {
				if row.Time.ToTime().Before(retentionStart) {
					continue
				}
				value := float64(row.Utilization)
				switch key {
				case "gpu.memory.used":
					value = float64(row.MemUsed)
				case "gpu.memory.total":
					value = float64(row.MemTotal)
				case "gpu.temperature":
					value = float64(row.Temperature)
				}
				byDevice[row.DeviceIndex] = append(byDevice[row.DeviceIndex], metricPoint{Time: row.Time, Value: floatPointer(value), Count: 1})
				names[row.DeviceIndex] = row.DeviceName
			}
			indices := make([]int, 0, len(byDevice))
			for index := range byDevice {
				indices = append(indices, index)
			}
			sort.Ints(indices)
			for _, index := range indices {
				points := byDevice[index]
				series = append(series, metricSeries{MetricKey: key, EntityID: params.EntityID, Type: definition.Type, Unit: definition.Unit, RetentionDays: definition.RetentionDays, MaxPoints: perQueryBudget, Tags: map[string]string{"device_index": strconv.Itoa(index), "device_name": names[index]}, Count: len(points), Points: points})
			}
		}
	}
	if needsPing {
		definition := definitions["ping.latency_ms"]
		pingStart := laterMetricTime(start, end.Add(-time.Duration(definition.RetentionDays)*24*time.Hour))
		result, err := tasks.QueryPingSeries(ctx, dbcore.GetDBInstance(), tasks.PingQuery{Client: params.EntityID, TaskID: -1, Start: pingStart, End: end, MaxPoints: perQueryBudget})
		if err != nil {
			return nil, rpc.MakeError(rpc.InternalError, "Failed to query Ping metrics", err.Error())
		}
		byTask := make(map[uint][]metricPoint)
		for position, record := range result.Records {
			value := float64(record.Value)
			byTask[record.TaskId] = append(byTask[record.TaskId], metricPoint{Time: record.Time, Value: floatPointer(value), Count: result.SampleCounts[position]})
		}
		taskList, _ := tasks.GetAllPingTasks()
		taskOrder := make(map[uint]int, len(taskList))
		for position, task := range taskList {
			taskOrder[task.Id] = position
		}
		ids := make([]uint, 0, len(byTask))
		for id := range byTask {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return taskOrder[ids[i]] < taskOrder[ids[j]] })
		for _, id := range ids {
			points := byTask[id]
			series = append(series, metricSeries{MetricKey: "ping.latency_ms", EntityID: params.EntityID, Type: definition.Type, Unit: definition.Unit, RetentionDays: definition.RetentionDays, MaxPoints: perQueryBudget, IntervalSeconds: result.ResolutionSeconds, Tags: map[string]string{"task_id": strconv.FormatUint(uint64(id), 10)}, Count: len(points), Points: points})
		}
	}
	series = enforceMetricSeriesBudget(series, maxPoints)
	total := 0
	for _, item := range series {
		total += len(item.Points)
	}
	return struct {
		Start  models.LocalTime `json:"start"`
		End    models.LocalTime `json:"end"`
		Series []metricSeries   `json:"series"`
		Count  int              `json:"count"`
	}{models.FromTime(start), models.FromTime(end), series, total}, nil
}

type weightedPingValue struct {
	value  int
	weight int64
}

func laterMetricTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

func weightedPercentile(values []weightedPingValue, percentile float64) int {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i].value < values[j].value })
	var total int64
	for _, value := range values {
		total += value.weight
	}
	target := max(int64(math.Ceil(float64(total)*percentile)), 1)
	var current int64
	for _, value := range values {
		current += value.weight
		if current >= target {
			return value.value
		}
	}
	return values[len(values)-1].value
}

func getPingMetricStats(ctx context.Context, request *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params metricRangeParams
	request.BindParams(&params)
	meta := rpc.MetaFromContext(ctx)
	if err := authorizeMetricEntity(params.EntityID, meta.Permission); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	permission := recordsdb.QueryPermissionPublic
	if meta.Permission == "admin" {
		permission = recordsdb.QueryPermissionAdmin
	}
	start, end, maxPoints, err := parseMetricRange(params, permission)
	if err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	result, err := tasks.QueryPingSeries(ctx, dbcore.GetDBInstance(), tasks.PingQuery{Client: params.EntityID, TaskID: -1, Start: start, End: end, MaxPoints: maxPoints})
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to query Ping statistics", err.Error())
	}
	taskList, _ := tasks.GetAllPingTasks()
	taskByID := make(map[uint]models.PingTask, len(taskList))
	for _, task := range taskList {
		taskByID[task.Id] = task
	}
	type accumulator struct {
		total, valid, loss int64
		sum                int64
		min, max, latest   int
		latestAt           time.Time
		values             []weightedPingValue
	}
	byTask := make(map[uint]*accumulator)
	for position, record := range result.Records {
		item := byTask[record.TaskId]
		if item == nil {
			item = &accumulator{latest: -1}
			byTask[record.TaskId] = item
		}
		item.total += result.SampleCounts[position]
		item.valid += result.ValidCounts[position]
		item.loss += result.LossCounts[position]
		item.sum += result.SumValues[position]
		if result.ValidCounts[position] > 0 {
			if item.min == 0 || result.MinValues[position] < item.min {
				item.min = result.MinValues[position]
			}
			item.max = max(item.max, result.MaxValues[position])
			item.values = append(item.values, weightedPingValue{value: record.Value, weight: result.ValidCounts[position]})
			if result.LastTimes[position].ToTime().After(item.latestAt) {
				item.latestAt = result.LastTimes[position].ToTime()
				item.latest = result.LastValues[position]
			}
		}
	}
	type pingMetricStat struct {
		EntityID        string            `json:"entity_id"`
		TaskID          string            `json:"task_id"`
		Name            string            `json:"name,omitempty"`
		Type            string            `json:"type,omitempty"`
		Interval        int               `json:"interval,omitempty"`
		Tags            map[string]string `json:"tags,omitempty"`
		Total           int64             `json:"total"`
		Valid           int64             `json:"valid"`
		Loss            int64             `json:"loss"`
		LossApproximate bool              `json:"loss_approximate,omitempty"`
		Min             int               `json:"min"`
		Max             int               `json:"max"`
		Avg             int               `json:"avg"`
		Latest          int               `json:"latest"`
		P50             int               `json:"p50"`
		P99             int               `json:"p99"`
		P99P50Ratio     float64           `json:"p99_p50_ratio"`
	}
	ids := make([]uint, 0, len(byTask))
	for id := range byTask {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return taskByID[ids[i]].Weight < taskByID[ids[j]].Weight })
	stats := make([]pingMetricStat, 0, len(ids))
	for _, id := range ids {
		item := byTask[id]
		avg := 0
		if item.valid > 0 {
			avg = int(item.sum / item.valid)
		}
		p50 := weightedPercentile(item.values, .5)
		p99 := weightedPercentile(item.values, .99)
		ratio := 0.0
		if p50 > 0 && p99 >= p50 {
			ratio = float64(p99-p50) / float64(p50)
		}
		task := taskByID[id]
		idString := strconv.FormatUint(uint64(id), 10)
		stats = append(stats, pingMetricStat{EntityID: params.EntityID, TaskID: idString, Name: task.Name, Type: task.Type, Interval: task.Interval, Tags: map[string]string{"task_id": idString}, Total: item.total, Valid: item.valid, Loss: item.loss, LossApproximate: result.ResolutionSeconds > 0, Min: item.min, Max: item.max, Avg: avg, Latest: item.latest, P50: p50, P99: p99, P99P50Ratio: ratio})
	}
	return struct {
		Start           models.LocalTime `json:"start"`
		End             models.LocalTime `json:"end"`
		IntervalSeconds int              `json:"interval_seconds,omitempty"`
		Stats           []pingMetricStat `json:"stats"`
		Count           int              `json:"count"`
	}{models.FromTime(start), models.FromTime(end), result.ResolutionSeconds, stats, len(stats)}, nil
}

func init() {
	RegisterWithGroupAndMeta("queryMetrics", "public", queryMetrics, &rpc.MethodMeta{Name: "queryMetrics", Summary: "Query allowlisted metric series with a storage point budget.", Returns: "QueryMetricsResponse"})
	RegisterWithGroupAndMeta("getPingMetricStats", "public", getPingMetricStats, &rpc.MethodMeta{Name: "getPingMetricStats", Summary: "Get weighted Ping statistics without loading unbounded raw history.", Returns: "PingMetricStatsResponse"})
}
