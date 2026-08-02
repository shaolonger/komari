package jsonRpc

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/utils/rpc"
	"gorm.io/gorm"
)

type pingOverviewTask struct {
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Interval int    `json:"interval"`
}

type pingOverviewPoint struct {
	Time        models.LocalTime `json:"time"`
	Value       int              `json:"value"`
	SampleCount int64            `json:"sample_count"`
	LossCount   int64            `json:"loss_count"`
	Loss        float64          `json:"loss"`
}

func getPingOverview(ctx context.Context, request *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		UUIDs []string `json:"uuids"`
	}
	request.BindParams(&params)
	if len(params.UUIDs) > 10_000 {
		return nil, rpc.MakeError(rpc.InvalidParams, "too many UUIDs", len(params.UUIDs))
	}

	clientInfo, err := clients.GetAllClientBasicInfo()
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to get client info", err.Error())
	}
	meta := rpc.MetaFromContext(ctx)
	visible := make(map[string]struct{}, len(clientInfo))
	for _, client := range clientInfo {
		if meta.Permission != "admin" && client.Hidden {
			continue
		}
		visible[client.UUID] = struct{}{}
	}
	selected := make([]string, 0, len(visible))
	seen := make(map[string]struct{}, len(params.UUIDs))
	if len(params.UUIDs) == 0 {
		for uuid := range visible {
			selected = append(selected, uuid)
		}
	} else {
		for _, uuid := range params.UUIDs {
			if _, ok := visible[uuid]; !ok {
				continue
			}
			if _, duplicate := seen[uuid]; duplicate {
				continue
			}
			seen[uuid] = struct{}{}
			selected = append(selected, uuid)
		}
	}
	sort.Strings(selected)

	pingTasks, err := tasks.GetAllPingTasks()
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to fetch ping tasks", err.Error())
	}
	end := time.Now()
	stats, _, err := getPingStatsForNodesAt(ctx, dbcore.GetDBInstance(), selected, pingTasks, end)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to fetch Ping overview statistics", err.Error())
	}
	series, err := getPingOverviewSeries(ctx, dbcore.GetDBInstance(), selected, pingTasks, end)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to fetch Ping overview series", err.Error())
	}
	taskList := make([]pingOverviewTask, 0, len(pingTasks))
	for _, task := range pingTasks {
		taskList = append(taskList, pingOverviewTask{ID: task.Id, Name: task.Name, Type: task.Type, Interval: task.Interval})
	}
	return struct {
		From   models.LocalTime                          `json:"from"`
		To     models.LocalTime                          `json:"to"`
		Tasks  []pingOverviewTask                        `json:"tasks"`
		Stats  map[string]map[string]pingStat            `json:"stats"`
		Series map[string]map[string][]pingOverviewPoint `json:"series"`
	}{
		From: models.FromTime(end.Add(-time.Hour)), To: models.FromTime(end),
		Tasks: taskList, Stats: stats, Series: series,
	}, nil
}

func getPingOverviewSeries(ctx context.Context, db *gorm.DB, clients []string, pingTasks []models.PingTask, end time.Time) (map[string]map[string][]pingOverviewPoint, error) {
	result := make(map[string]map[string][]pingOverviewPoint)
	seriesCount := 0
	for _, client := range clients {
		for _, task := range pingTasks {
			if task.AppliesToClient(client) {
				seriesCount++
			}
		}
	}
	if seriesCount == 0 {
		return result, nil
	}
	pointBudget := min(max(seriesCount*60, seriesCount), tasks.MaxPingQueryPoints)
	query, err := tasks.QueryPingSeries(ctx, db, tasks.PingQuery{
		Clients: clients, TaskID: -1, Start: end.Add(-time.Hour), End: end, MaxPoints: pointBudget,
	})
	if err != nil {
		return nil, err
	}
	type bucket struct {
		samples int64
		valid   int64
		lost    int64
		sum     int64
	}
	const bucketSeconds = int64(150)
	buckets := make(map[string]map[uint]map[int64]*bucket)
	for position, record := range query.Records {
		clientBuckets := buckets[record.Client]
		if clientBuckets == nil {
			clientBuckets = make(map[uint]map[int64]*bucket)
			buckets[record.Client] = clientBuckets
		}
		taskBuckets := clientBuckets[record.TaskId]
		if taskBuckets == nil {
			taskBuckets = make(map[int64]*bucket)
			clientBuckets[record.TaskId] = taskBuckets
		}
		bucketUnix := record.Time.ToTime().Unix() / bucketSeconds * bucketSeconds
		item := taskBuckets[bucketUnix]
		if item == nil {
			item = &bucket{}
			taskBuckets[bucketUnix] = item
		}
		item.samples += query.SampleCounts[position]
		item.valid += query.ValidCounts[position]
		item.lost += query.LossCounts[position]
		item.sum += query.SumValues[position]
	}
	for client, clientBuckets := range buckets {
		result[client] = make(map[string][]pingOverviewPoint, len(clientBuckets))
		for taskID, taskBuckets := range clientBuckets {
			points := make([]pingOverviewPoint, 0, len(taskBuckets))
			for bucketUnix, item := range taskBuckets {
				value := -1
				if item.valid > 0 {
					value = int(item.sum / item.valid)
				}
				loss := 0.0
				if item.samples > 0 {
					loss = float64(item.lost) / float64(item.samples) * 100
				}
				points = append(points, pingOverviewPoint{
					Time: models.FromTime(time.Unix(bucketUnix+bucketSeconds, 0)), Value: value,
					SampleCount: item.samples, LossCount: item.lost, Loss: loss,
				})
			}
			sort.Slice(points, func(left, right int) bool { return points[left].Time.ToTime().Before(points[right].Time.ToTime()) })
			result[client][fmt.Sprint(taskID)] = points
		}
	}
	return result, nil
}

func init() {
	RegisterWithGroupAndMeta("getPingOverview", "common", getPingOverview, &rpc.MethodMeta{
		Name:        "getPingOverview",
		Summary:     "Get one-hour Ping statistics for a set of nodes in one storage query.",
		Description: "Returns no task targets or hidden-node data to unauthenticated callers.",
		Params:      []rpc.ParamMeta{{Name: "uuids", Description: "Optional node UUID set", Required: false, Type: "string[]"}},
		Returns:     "{ from, to, tasks, stats }",
	})
}
