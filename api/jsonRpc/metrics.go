package jsonRpc

import (
	"context"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/metrics"
	"github.com/komari-monitor/komari/utils/rpc"
)

func listMetricDefinitions(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	definitions, err := metrics.List(ctx, dbcore.GetDBInstance())
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to list metric definitions", err.Error())
	}
	return definitions, nil
}

func updateMetricDefinition(ctx context.Context, request *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		Name          string `json:"name"`
		RetentionDays int    `json:"retention_days"`
	}
	request.BindParams(&params)
	definition, err := metrics.UpdateRetention(ctx, dbcore.GetDBInstance(), params.Name, params.RetentionDays)
	if err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	return definition, nil
}

func init() {
	listMeta := &rpc.MethodMeta{
		Name: "listMetricDefinitions", Summary: "List the versioned built-in metric catalog.",
		Returns: "MetricDefinition[]",
	}
	RegisterWithGroupAndMeta("listMetricDefinitions", "public", listMetricDefinitions, listMeta)
	RegisterWithGroupAndMeta("listMetricDefinitions", "admin", listMetricDefinitions, listMeta)
	RegisterWithGroupAndMeta("updateMetricDefinition", "admin", updateMetricDefinition, &rpc.MethodMeta{
		Name: "updateMetricDefinition", Summary: "Update retention for an allowlisted metric.",
		Params: []rpc.ParamMeta{
			{Name: "name", Required: true, Type: "string"},
			{Name: "retention_days", Required: true, Type: "integer"},
		},
		Returns: "MetricDefinition",
	})
}
