package client

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/protocol/telemetryv2"
)

func TestTelemetryV2MatchesJSONV1Report(t *testing.T) {
	fixture, err := os.ReadFile("../../protocol/telemetryv2/testdata/report_v2.hex")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	frame, err := hex.DecodeString(strings.TrimSpace(string(fixture)))
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	v2, err := decodeTelemetryV2Report(frame)
	if err != nil {
		t.Fatalf("decodeTelemetryV2Report failed: %v", err)
	}

	v1JSON := []byte(telemetryV1Fixture)
	var v1 common.Report
	if err := json.Unmarshal(v1JSON, &v1); err != nil {
		t.Fatalf("decode JSON v1: %v", err)
	}
	if !reflect.DeepEqual(v2, v1) {
		t.Fatalf("v1/v2 mismatch\nv1: %#v\nv2: %#v", v1, v2)
	}
}

const telemetryV1Fixture = `{"cpu":{"usage":12.5},"ram":{"total":8000,"used":4000},"swap":{"total":2000,"used":100},"load":{"load1":1.1,"load5":1.2,"load15":1.3},"disk":{"total":1000,"used":500},"network":{"up":100,"down":200,"totalUp":1000,"totalDown":2000},"connections":{"tcp":12,"udp":3},"gpu":{"count":1,"average_usage":75,"detailed_info":[{"name":"GPU","memory_total":100,"memory_used":50,"utilization":75,"temperature":65}]},"uptime":999,"process":42,"message":"ok"}`

func TestTelemetryProtocolNegotiationDefaultsToV1AndRejectsUnknown(t *testing.T) {
	tests := map[string]telemetryProtocol{
		"":                            telemetryProtocolV1,
		telemetryv2.LegacySubprotocol: telemetryProtocolV1,
		telemetryv2.Subprotocol:       telemetryProtocolV2,
	}
	for selected, want := range tests {
		got, err := negotiatedTelemetryProtocol(selected)
		if err != nil || got != want {
			t.Fatalf("selected %q: protocol=%d err=%v, want %d", selected, got, err, want)
		}
	}
	if _, err := negotiatedTelemetryProtocol("komari.telemetry.v999"); err == nil {
		t.Fatal("unknown subprotocol was accepted")
	}
}

func TestTelemetryV2ConversionRejectsNativeIntegerOverflow(t *testing.T) {
	if _, err := telemetryV2ToCommon(telemetryv2.Report{
		CPUUsage: 1,
		RAM:      telemetryv2.Memory{Total: math.MaxUint64},
	}); err == nil {
		t.Fatal("uint64 to int64 overflow was accepted")
	}
	if _, err := telemetryUint64ToInt("fixture", math.MaxUint64); err == nil {
		t.Fatal("uint64 to int overflow was accepted")
	}
}

func TestTelemetryV2GPUModelFallbackPreservesCoreReport(t *testing.T) {
	report, err := telemetryV2ToCommon(telemetryv2.Report{
		CPUUsage: 1,
		GPU:      &telemetryv2.GPU{Models: []string{"Fixture GPU"}},
	})
	if err != nil {
		t.Fatalf("telemetryV2ToCommon failed: %v", err)
	}
	if report.CPU.Usage != 1 || report.GPU != nil {
		t.Fatalf("unexpected fallback report: %+v", report)
	}
}
