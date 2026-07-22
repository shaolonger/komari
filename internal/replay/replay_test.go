package replay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestRunHTTP(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-0" && r.Header.Get("Authorization") != "Bearer secret-1" {
			t.Errorf("unexpected authorization header")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected content type")
		}
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	result, err := Run(context.Background(), Config{
		Mode: ModeHTTP, Endpoint: server.URL, TokenTemplate: "secret-{index}",
		Nodes: 2, ReportsPerNode: 3, RequestTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 6 || result.Succeeded != 6 || result.Failed != 0 {
		t.Fatalf("calls/result = %d/%+v", calls.Load(), result)
	}
	if result.P99 <= 0 || result.PeakHeapBytes == 0 || result.BytesSent == 0 {
		t.Fatalf("missing replay metrics: %+v", result)
	}
}

func TestRunWebSocket(t *testing.T) {
	var received atomic.Int64
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
			received.Add(1)
		}
	}))
	defer server.Close()
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http")

	result, err := Run(context.Background(), Config{Mode: ModeWS, Endpoint: endpoint, Nodes: 2, ReportsPerNode: 4, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if result.Succeeded != 8 || result.Failed != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	deadline := time.Now().Add(time.Second)
	for received.Load() != 8 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if received.Load() != 8 {
		t.Fatalf("received %d messages, want 8", received.Load())
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []Config{
		{Mode: "invalid", Endpoint: "http://localhost", Nodes: 1, ReportsPerNode: 1},
		{Mode: ModeHTTP, Endpoint: "ws://localhost", Nodes: 1, ReportsPerNode: 1},
		{Mode: ModeWS, Endpoint: "http://localhost", Nodes: 1, ReportsPerNode: 1},
		{Mode: ModeHTTP, Endpoint: "http://localhost", Nodes: 0, ReportsPerNode: 1},
		{Mode: ModeHTTP, Endpoint: "http://localhost", Nodes: 1},
	}
	for _, cfg := range tests {
		if _, err := Run(context.Background(), cfg); err == nil {
			t.Fatalf("expected validation failure for %+v", cfg)
		}
	}
}

func TestCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, Config{Mode: ModeHTTP, Endpoint: "http://localhost", Nodes: 1, ReportsPerNode: 1})
	if err == nil {
		t.Fatal("expected context cancellation")
	}
}
