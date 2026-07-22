package log

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
)

type capturedLogRecord struct {
	message string
	attrs   map[string]string
}

type captureHandler struct {
	mu      sync.Mutex
	records []capturedLogRecord
}

func (handler *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (handler *captureHandler) Handle(_ context.Context, record slog.Record) error {
	captured := capturedLogRecord{message: record.Message, attrs: make(map[string]string)}
	record.Attrs(func(attribute slog.Attr) bool {
		captured.attrs[attribute.Key] = fmt.Sprint(attribute.Value.Any())
		return true
	})
	handler.mu.Lock()
	handler.records = append(handler.records, captured)
	handler.mu.Unlock()
	return nil
}
func (handler *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return handler }
func (handler *captureHandler) WithGroup(string) slog.Handler      { return handler }
func (handler *captureHandler) snapshot() []capturedLogRecord {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return append([]capturedLogRecord(nil), handler.records...)
}

func installCaptureLogger(t *testing.T) *captureHandler {
	t.Helper()
	previous := slog.Default()
	handler := &captureHandler{}
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return handler
}

func TestGinLoggerSamplesSuccessfulTelemetryAndRedactsSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := installCaptureLogger(t)
	router := gin.New()
	router.Use(GinLoggerWithPolicy(GinLogPolicy{TelemetrySuccessSampleRate: 10}))
	router.POST("/api/clients/report", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	for index := 0; index < 25; index++ {
		request := httptest.NewRequest(http.MethodPost, "/api/clients/report?token=query-secret&code=oauth-secret", nil)
		request.Header.Set("Authorization", "Bearer header-secret")
		router.ServeHTTP(httptest.NewRecorder(), request)
	}
	records := handler.snapshot()
	if len(records) != 3 {
		t.Fatalf("sampled records=%d, want 3", len(records))
	}
	for _, record := range records {
		serialized := fmt.Sprint(record.message, record.attrs)
		for _, secret := range []string{"query-secret", "oauth-secret", "header-secret"} {
			if strings.Contains(serialized, secret) {
				t.Fatalf("log leaked %q: %s", secret, serialized)
			}
		}
		if record.attrs["query"] != "<redacted>" || record.attrs["sample_rate"] != "10" {
			t.Fatalf("unexpected attrs: %#v", record.attrs)
		}
	}
}

func TestGinLoggerNeverSamplesErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := installCaptureLogger(t)
	router := gin.New()
	router.Use(GinLoggerWithPolicy(GinLogPolicy{TelemetrySuccessSampleRate: 1_000}))
	router.POST("/api/clients/report", func(c *gin.Context) { c.Status(http.StatusUnauthorized) })
	for index := 0; index < 5; index++ {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/clients/report", nil))
	}
	if records := handler.snapshot(); len(records) != 5 {
		t.Fatalf("error records=%d, want 5", len(records))
	}
}

func TestGinRecoveryDoesNotLogPanicValueOrQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := installCaptureLogger(t)
	router := gin.New()
	router.Use(GinRecovery())
	router.GET("/panic", func(*gin.Context) { panic("credential-secret") })
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/panic?token=query-secret", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", recorder.Code)
	}
	records := handler.snapshot()
	if len(records) != 1 {
		t.Fatalf("records=%d", len(records))
	}
	serialized := fmt.Sprint(records[0].message, records[0].attrs)
	if strings.Contains(serialized, "credential-secret") || strings.Contains(serialized, "query-secret") {
		t.Fatalf("panic log leaked secret: %s", serialized)
	}
}
