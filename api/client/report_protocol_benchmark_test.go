package client

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/komari-monitor/komari/common"
)

var benchmarkTelemetryReport common.Report
var benchmarkTelemetryError error
var benchmarkAgentMessage agentMessage
var benchmarkUntypedReport map[string]any
var benchmarkMessageType struct {
	Type string `json:"type"`
}

func BenchmarkDecodeLegacyHTTPJSONV1(b *testing.B) {
	payload := []byte(telemetryV1Fixture)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for range b.N {
		benchmarkUntypedReport = nil
		benchmarkTelemetryReport = common.Report{}
		_ = json.Unmarshal(payload, &benchmarkUntypedReport)
		_ = json.Unmarshal(payload, &benchmarkTelemetryReport)
	}
}

func BenchmarkDecodeLegacyWebSocketJSONV1(b *testing.B) {
	payload := []byte(telemetryV1Fixture)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for range b.N {
		benchmarkMessageType.Type = ""
		benchmarkTelemetryReport = common.Report{}
		_ = json.Unmarshal(payload, &benchmarkMessageType)
		_ = json.Unmarshal(payload, &benchmarkTelemetryReport)
	}
}

func BenchmarkDecodeAuthenticatedJSONV1(b *testing.B) {
	payload := []byte(telemetryV1Fixture)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for range b.N {
		benchmarkTelemetryError = decodeAuthenticatedJSONReport(payload, "benchmark-node", &benchmarkTelemetryReport)
	}
}

func BenchmarkDecodeWebSocketJSONV1(b *testing.B) {
	payload := []byte(telemetryV1Fixture)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for range b.N {
		benchmarkTelemetryError = decodeAgentMessage(payload, &benchmarkAgentMessage)
	}
}

func BenchmarkDecodeJSONV1(b *testing.B) {
	payload := []byte(telemetryV1Fixture)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for range b.N {
		benchmarkTelemetryReport = common.Report{}
		_ = json.Unmarshal(payload, &benchmarkTelemetryReport)
	}
}

func BenchmarkDecodeTelemetryV2(b *testing.B) {
	fixture, err := os.ReadFile("../../protocol/telemetryv2/testdata/report_v2.hex")
	if err != nil {
		b.Fatal(err)
	}
	payload, err := hex.DecodeString(strings.TrimSpace(string(fixture)))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for range b.N {
		benchmarkTelemetryReport, _ = decodeTelemetryV2Report(payload)
	}
}
