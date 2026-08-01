package jsonRpc

import (
	"context"
	"sort"
	"time"

	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/utils/rpc"
)

type pingOverviewTask struct {
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Interval int    `json:"interval"`
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
	stats, _ := getPingStatsForNodesAt(ctx, dbcore.GetDBInstance(), selected, pingTasks, end)
	taskList := make([]pingOverviewTask, 0, len(pingTasks))
	for _, task := range pingTasks {
		taskList = append(taskList, pingOverviewTask{ID: task.Id, Name: task.Name, Type: task.Type, Interval: task.Interval})
	}
	return struct {
		From  models.LocalTime               `json:"from"`
		To    models.LocalTime               `json:"to"`
		Tasks []pingOverviewTask             `json:"tasks"`
		Stats map[string]map[string]pingStat `json:"stats"`
	}{
		From: models.FromTime(end.Add(-time.Hour)), To: models.FromTime(end),
		Tasks: taskList, Stats: stats,
	}, nil
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
