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

func getMetricMigrationStatus(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	status, err := metrics.GetMigrationStatus(ctx, dbcore.GetDBInstance())
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to get metric migration status", err.Error())
	}
	return status, nil
}

func startMetricMigration(_ context.Context, request *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		SourceDSN string `json:"source_dsn"`
	}
	request.BindParams(&params)
	status, err := metrics.StartMigration(dbcore.GetDBInstance(), params.SourceDSN)
	if err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	return status, nil
}

func cancelMetricMigration(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	status, err := metrics.CancelMigration(ctx, dbcore.GetDBInstance())
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to cancel metric migration", err.Error())
	}
	return status, nil
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
	RegisterWithGroupAndMeta("getMetricMigrationStatus", "admin", getMetricMigrationStatus, &rpc.MethodMeta{Name: "getMetricMigrationStatus", Summary: "Get durable metric migration progress.", Returns: "MetricMigrationStatus"})
	RegisterWithGroupAndMeta("startMetricMigration", "admin", startMetricMigration, &rpc.MethodMeta{Name: "startMetricMigration", Summary: "Start or resume embedded metric rollup migration.", Returns: "MetricMigrationStatus"})
	RegisterWithGroupAndMeta("cancelMetricMigration", "admin", cancelMetricMigration, &rpc.MethodMeta{Name: "cancelMetricMigration", Summary: "Cancel migration after the current atomic page.", Returns: "MetricMigrationStatus"})
}
