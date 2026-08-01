package jsonRpc

import (
	"context"

	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/utils/rpc"
)

type publicPingTask struct {
	ID        uint     `json:"id"`
	Name      string   `json:"name"`
	Clients   []string `json:"clients"`
	DefaultOn bool     `json:"default_on"`
	Type      string   `json:"type"`
	Interval  int      `json:"interval"`
}

func getPublicPingTasks(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	all, err := tasks.GetAllPingTasks()
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to fetch Ping tasks", err.Error())
	}
	result := make([]publicPingTask, len(all))
	for index, task := range all {
		result[index] = publicPingTask{
			ID: task.Id, Name: task.Name, Clients: task.Clients,
			DefaultOn: task.DefaultOn, Type: task.Type, Interval: task.Interval,
		}
	}
	return result, nil
}

func init() {
	RegisterWithGroupAndMeta("getPublicPingTasks", "public", getPublicPingTasks, &rpc.MethodMeta{
		Name: "getPublicPingTasks", Summary: "List public Ping task definitions without targets or credentials.", Returns: "PublicPingTask[]",
	})
}
