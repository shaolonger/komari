package client

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/internal/telemetry"
	"github.com/komari-monitor/komari/protocol/telemetryv2"
)

func performUpload(body string, authenticated bool) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/clients/report", strings.NewReader(body))
	if authenticated {
		context.Set("client_uuid", "authenticated-node")
	}
	UploadReport(context)
	return recorder
}

func TestUploadReportRequiresAuthenticatedClientIdentity(t *testing.T) {
	recorder := performUpload(telemetryV1Fixture, false)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestUploadReportRejectsMalformedAndTrailingJSON(t *testing.T) {
	for _, body := range []string{"{", telemetryV1Fixture + `{}`} {
		recorder := performUpload(body, true)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %q status = %d, want 400", body, recorder.Code)
		}
	}
}

func TestUploadReportRejectsOversizedBody(t *testing.T) {
	body := `{"message":"` + strings.Repeat("x", telemetryv2.MaxFrameSize) + `"}`
	recorder := performUpload(body, true)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSaveClientReportEnforcesBoundedMinute(t *testing.T) {
	previous := api.Telemetry
	api.Telemetry = telemetry.NewStore()
	defer func() { api.Telemetry = previous }()
	base := time.Now().Truncate(time.Minute)
	for i := 0; i < telemetry.MaxSamplesPerMinute; i++ {
		if err := SaveClientReport("node", common.Report{UpdatedAt: base, CPU: common.CPUReport{Usage: 0}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveClientReport("node", common.Report{UpdatedAt: base}); !errors.Is(err, telemetry.ErrSampleLimit) {
		t.Fatalf("sample limit error = %v", err)
	}
	aggregates := api.Telemetry.DrainBefore(base.Add(time.Minute))
	if len(aggregates) != 1 || aggregates[0].Record.Cpu != 0.01 {
		t.Fatalf("CPU floor or aggregate changed: %+v", aggregates)
	}
}

func TestDecodeAuthenticatedJSONReportIgnoresForgedUUIDAndAcceptsV1Extensions(t *testing.T) {
	body := strings.TrimSuffix(telemetryV1Fixture, "}") + `,"uuid":"forged-node","future_extension":{"enabled":true}}`
	var report common.Report
	err := decodeAuthenticatedJSONReport([]byte(body), "authenticated-node", &report)
	if err != nil {
		t.Fatal(err)
	}
	if report.UUID != "authenticated-node" {
		t.Fatalf("UUID = %q, want authenticated identity", report.UUID)
	}
	if report.CPU.Usage != 12.5 || report.GPU == nil || len(report.GPU.DetailedInfo) != 1 {
		t.Fatalf("legacy report semantics changed: %+v", report)
	}
}

func TestDecodeAgentMessageOnceSupportsReportAndPing(t *testing.T) {
	var report agentMessage
	err := decodeAgentMessage([]byte(telemetryV1Fixture), &report)
	if err != nil || report.CPU.Usage != 12.5 || report.Type != "" {
		t.Fatalf("report decode = %+v, %v", report, err)
	}
	var ping agentMessage
	err = decodeAgentMessage([]byte(`{"type":"ping_result","task_id":7,"value":42,"ping_type":"icmp","finished_at":"2026-07-22T12:00:00Z"}`), &ping)
	if err != nil || ping.Type != "ping_result" || ping.PingTaskID != 7 || ping.PingResult != 42 || ping.FinishedAt.IsZero() {
		t.Fatalf("ping decode = %+v, %v", ping, err)
	}
}

func TestWebSocketReportReadLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/clients/report", func(c *gin.Context) {
		c.Set("client_uuid", "authenticated-node")
		WebSocketReport(c)
	})
	server := httptest.NewServer(router)
	defer server.Close()

	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/clients/report"
	conn, _, err := websocket.DefaultDialer.Dial(endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, make([]byte, telemetryv2.MaxFrameSize+1)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("oversized WebSocket message was not rejected")
	}
}

func FuzzDecodeAuthenticatedJSONReport(f *testing.F) {
	f.Add([]byte(telemetryV1Fixture))
	f.Add([]byte(`{"uuid":"forged"}`))
	f.Add([]byte("{"))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > telemetryv2.MaxFrameSize {
			t.Skip()
		}
		var report common.Report
		err := decodeAuthenticatedJSONReport(body, "authenticated-node", &report)
		if err == nil && report.UUID != "authenticated-node" {
			t.Fatalf("decoded forged identity %q", report.UUID)
		}
	})
}

func FuzzDecodeAgentMessage(f *testing.F) {
	f.Add([]byte(telemetryV1Fixture))
	f.Add([]byte(`{"type":"ping_result","task_id":1,"value":20}`))
	f.Add([]byte("{"))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > telemetryv2.MaxFrameSize {
			t.Skip()
		}
		var decoded agentMessage
		_ = decodeAgentMessage(body, &decoded)
	})
}
