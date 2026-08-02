package jsonRpc

import (
	"context"
	"time"

	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/utils/rpc"
	"github.com/komari-monitor/komari/ws"
)

const (
	maxRealtimeDeltaUUIDs = 256
	maxRealtimeWait       = 25 * time.Second
)

type realtimeDeltaResponse struct {
	Sequence uint64                   `json:"sequence"`
	Snapshot bool                     `json:"snapshot"`
	Resync   bool                     `json:"resync,omitempty"`
	Reports  map[string]common.Report `json:"reports,omitempty"`
	Removed  []string                 `json:"removed,omitempty"`
	Online   []string                 `json:"online,omitempty"`
	Offline  []string                 `json:"offline,omitempty"`
}

func getRealtimeDelta(ctx context.Context, request *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		Since  uint64   `json:"since"`
		UUIDs  []string `json:"uuids"`
		WaitMS int      `json:"wait_ms"`
	}
	if err := request.BindParams(&params); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	if len(params.UUIDs) > maxRealtimeDeltaUUIDs {
		return nil, rpc.MakeError(rpc.InvalidParams, "too many UUIDs", maxRealtimeDeltaUUIDs)
	}
	if params.WaitMS < 0 || time.Duration(params.WaitMS)*time.Millisecond > maxRealtimeWait {
		return nil, rpc.MakeError(rpc.InvalidParams, "wait_ms must be between 0 and 25000", nil)
	}

	update, notify := ws.DashboardStateSince(params.Since)
	if params.Since > 0 && update.Sequence == params.Since && params.WaitMS > 0 {
		timer := time.NewTimer(time.Duration(params.WaitMS) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-notify:
			update, _ = ws.DashboardStateSince(params.Since)
		case <-timer.C:
		case <-ctx.Done():
			return nil, rpc.MakeError(rpc.Cancelled, "request canceled", nil)
		}
	}

	hidden := map[string]bool{}
	meta := rpc.MetaFromContext(ctx)
	if meta == nil || meta.Permission != "admin" {
		all, err := clients.GetAllClientBasicInfo()
		if err != nil {
			return nil, rpc.MakeError(rpc.InternalError, "Failed to load dashboard visibility", err.Error())
		}
		for _, client := range all {
			if client.Hidden {
				hidden[client.UUID] = true
			}
		}
	}
	return filterRealtimeUpdate(update, hidden, params.UUIDs), nil
}

func filterRealtimeUpdate(update ws.DashboardUpdate, hidden map[string]bool, uuids []string) realtimeDeltaResponse {
	selected := make(map[string]struct{}, len(uuids))
	for _, uuid := range uuids {
		if uuid != "" {
			selected[uuid] = struct{}{}
		}
	}
	allowed := func(uuid string) bool {
		if hidden[uuid] {
			return false
		}
		if len(selected) == 0 {
			return true
		}
		_, ok := selected[uuid]
		return ok
	}
	result := realtimeDeltaResponse{
		Sequence: update.Sequence,
		Snapshot: update.Snapshot,
		Resync:   update.Resync,
	}
	for uuid, report := range update.Reports {
		if !allowed(uuid) {
			continue
		}
		if result.Reports == nil {
			result.Reports = make(map[string]common.Report)
		}
		report.UUID = ""
		if report.GPU != nil {
			gpu := *report.GPU
			gpu.DetailedInfo = append([]common.GPUDeviceInfo(nil), report.GPU.DetailedInfo...)
			report.GPU = &gpu
		}
		result.Reports[uuid] = report
	}
	for _, uuid := range update.Removed {
		if allowed(uuid) {
			result.Removed = append(result.Removed, uuid)
		}
	}
	for _, uuid := range update.Online {
		if allowed(uuid) {
			result.Online = append(result.Online, uuid)
		}
	}
	for _, uuid := range update.Offline {
		if allowed(uuid) {
			result.Offline = append(result.Offline, uuid)
		}
	}
	return result
}

func init() {
	RegisterWithGroupAndMeta("getRealtimeDelta", "common", getRealtimeDelta, &rpc.MethodMeta{
		Name: "getRealtimeDelta", Summary: "Resume a bounded dashboard delta stream from a sequence cursor.",
		Params: []rpc.ParamMeta{
			{Name: "since", Type: "uint64", Description: "Last applied sequence; zero requests a snapshot."},
			{Name: "uuids", Type: "string[]", Description: "Optional node subset, maximum 256."},
			{Name: "wait_ms", Type: "integer", Description: "Optional long-poll wait, maximum 25000ms."},
		},
		Returns: "RealtimeDelta",
	})
}
