package jsonRpc

import (
	"context"
	"testing"

	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/utils/rpc"
	"github.com/komari-monitor/komari/ws"
)

func TestFilterRealtimeUpdateEnforcesVisibilitySubsetAndOwnership(t *testing.T) {
	input := ws.DashboardUpdate{
		Sequence: 7, Snapshot: true,
		Reports: map[string]common.Report{
			"visible": {UUID: "secret-identity", CPU: common.CPUReport{Usage: 12}},
			"other":   {CPU: common.CPUReport{Usage: 22}},
			"hidden":  {CPU: common.CPUReport{Usage: 32}},
		},
		Online: []string{"visible", "other", "hidden"},
	}
	got := filterRealtimeUpdate(input, map[string]bool{"hidden": true}, []string{"visible", "hidden"})
	if len(got.Reports) != 1 || got.Reports["visible"].CPU.Usage != 12 || got.Reports["visible"].UUID != "" {
		t.Fatalf("filtered response = %#v", got)
	}
	if len(got.Online) != 1 || got.Online[0] != "visible" {
		t.Fatalf("online = %#v", got.Online)
	}
	if input.Reports["visible"].UUID != "secret-identity" {
		t.Fatal("filter mutated the shared snapshot")
	}
}

func TestRealtimeDeltaRejectsBudgetsBeforeDatabaseAccess(t *testing.T) {
	request := &rpc.JsonRpcRequest{Params: map[string]any{"wait_ms": 25001}}
	if _, err := getRealtimeDelta(context.Background(), request); err == nil || err.Code != rpc.InvalidParams {
		t.Fatalf("wait budget error = %#v", err)
	}
	request.Params = map[string]any{"uuids": make([]string, maxRealtimeDeltaUUIDs+1)}
	if _, err := getRealtimeDelta(context.Background(), request); err == nil || err.Code != rpc.InvalidParams {
		t.Fatalf("UUID budget error = %#v", err)
	}
}

func TestRealtimeDeltaLongPollHonorsCancellation(t *testing.T) {
	ws.SetLatestReport("realtime-cancel-test", &common.Report{})
	t.Cleanup(func() { ws.DeleteLatestReport("realtime-cancel-test") })
	state, _ := ws.DashboardStateSince(0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := &rpc.JsonRpcRequest{Params: map[string]any{"since": state.Sequence, "wait_ms": 25000}}
	_, err := getRealtimeDelta(rpc.NewContextWithMeta(ctx, &rpc.ContextMeta{Permission: "admin"}), request)
	if err == nil || err.Code != rpc.Cancelled {
		t.Fatalf("cancel error = %#v", err)
	}
}
