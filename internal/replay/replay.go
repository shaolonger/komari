// Package replay provides a bounded load generator for Komari telemetry endpoints.
package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/common"
)

const (
	maxNodes          = 100_000
	maxReportsPerNode = 10_000_000
	maxLatencySamples = 1_000_000
	maxResponseBytes  = 64 << 10
)

type Mode string

const (
	ModeHTTP Mode = "http"
	ModeWS   Mode = "ws"
)

// Config controls one replay. ReportsPerNode takes precedence over Duration
// when non-zero, which makes integration and CI runs deterministic.
type Config struct {
	Mode           Mode
	Endpoint       string
	TokenTemplate  string
	Nodes          int
	RampUp         time.Duration
	Interval       time.Duration
	Duration       time.Duration
	ReportsPerNode int
	RequestTimeout time.Duration
	SampleLimit    int
	HTTPClient     *http.Client
}

// Result is deliberately JSON-friendly so a run can be archived by CI.
type Result struct {
	Mode           Mode          `json:"mode"`
	Nodes          int           `json:"nodes"`
	Attempted      uint64        `json:"attempted"`
	Succeeded      uint64        `json:"succeeded"`
	Failed         uint64        `json:"failed"`
	BytesSent      uint64        `json:"bytes_sent"`
	Elapsed        time.Duration `json:"elapsed"`
	ReportsPerSec  float64       `json:"reports_per_second"`
	P50            time.Duration `json:"p50"`
	P95            time.Duration `json:"p95"`
	P99            time.Duration `json:"p99"`
	PeakHeapBytes  uint64        `json:"peak_heap_bytes"`
	LatencySamples int           `json:"latency_samples"`
	DroppedSamples uint64        `json:"dropped_latency_samples"`
}

func (c *Config) normalize() error {
	if c.Mode == "" {
		c.Mode = ModeHTTP
	}
	if c.Mode != ModeHTTP && c.Mode != ModeWS {
		return fmt.Errorf("unsupported mode %q", c.Mode)
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" {
		return errors.New("endpoint must be an absolute HTTP(S) or WS(S) URL")
	}
	if c.Mode == ModeHTTP && u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("HTTP mode endpoint must use http or https")
	}
	if c.Mode == ModeWS && u.Scheme != "ws" && u.Scheme != "wss" {
		return errors.New("WS mode endpoint must use ws or wss")
	}
	if c.Nodes <= 0 || c.Nodes > maxNodes {
		return fmt.Errorf("nodes must be between 1 and %d", maxNodes)
	}
	if c.RampUp < 0 {
		return errors.New("ramp-up cannot be negative")
	}
	if c.Interval < 0 {
		return errors.New("interval cannot be negative")
	}
	if c.ReportsPerNode < 0 || c.ReportsPerNode > maxReportsPerNode {
		return fmt.Errorf("reports per node must be between 0 and %d", maxReportsPerNode)
	}
	if c.ReportsPerNode == 0 && c.Duration <= 0 {
		return errors.New("duration must be positive when reports-per-node is zero")
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = 10 * time.Second
	}
	if c.SampleLimit <= 0 {
		c.SampleLimit = 100_000
	}
	if c.SampleLimit > maxLatencySamples {
		return fmt.Errorf("sample limit cannot exceed %d", maxLatencySamples)
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: c.RequestTimeout}
	}
	return nil
}

type collector struct {
	attempted atomic.Uint64
	succeeded atomic.Uint64
	failed    atomic.Uint64
	bytesSent atomic.Uint64
	peakHeap  atomic.Uint64
	dropped   atomic.Uint64
	mu        sync.Mutex
	latencies []time.Duration
	limit     int
}

func (c *collector) observe(latency time.Duration, size int, err error) {
	c.attempted.Add(1)
	c.bytesSent.Add(uint64(size))
	if err != nil {
		c.failed.Add(1)
	} else {
		c.succeeded.Add(1)
	}
	c.mu.Lock()
	if len(c.latencies) < c.limit {
		c.latencies = append(c.latencies, latency)
	} else {
		c.dropped.Add(1)
	}
	c.mu.Unlock()
}

// Run executes a replay until the report count, duration, or parent context
// completes. It never returns an authentication token in errors or results.
func Run(ctx context.Context, cfg Config) (Result, error) {
	if err := cfg.normalize(); err != nil {
		return Result{}, err
	}
	started := time.Now()
	col := &collector{limit: cfg.SampleLimit, latencies: make([]time.Duration, 0, min(cfg.SampleLimit, 4096))}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if cfg.ReportsPerNode == 0 {
		var durationCancel context.CancelFunc
		runCtx, durationCancel = context.WithTimeout(runCtx, cfg.Duration)
		defer durationCancel()
	}

	monitorDone := make(chan struct{})
	go monitorHeap(runCtx, col, monitorDone)

	var wg sync.WaitGroup
	for node := 0; node < cfg.Nodes; node++ {
		wg.Add(1)
		go func(node int) {
			defer wg.Done()
			if !waitForRamp(runCtx, cfg.RampUp, node, cfg.Nodes) {
				return
			}
			if cfg.Mode == ModeWS {
				runWSNode(runCtx, cfg, node, col)
				return
			}
			runHTTPNode(runCtx, cfg, node, col)
		}(node)
	}
	wg.Wait()
	cancel()
	<-monitorDone

	elapsed := time.Since(started)
	result := buildResult(cfg, col, elapsed)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, nil
}

func waitForRamp(ctx context.Context, rampUp time.Duration, node, nodes int) bool {
	if rampUp <= 0 || node <= 0 || nodes <= 1 {
		return ctx.Err() == nil
	}
	steps := time.Duration(nodes - 1)
	delay := (rampUp/steps)*time.Duration(node) +
		time.Duration((int64(rampUp%steps)*int64(node))/int64(steps))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func runHTTPNode(ctx context.Context, cfg Config, node int, col *collector) {
	payload, err := nodePayload(node)
	if err != nil {
		col.observe(0, 0, err)
		return
	}
	token := expandToken(cfg.TokenTemplate, node)
	runSchedule(ctx, cfg, func() {
		reqCtx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, cfg.Endpoint, bytes.NewReader(payload))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
		}
		started := time.Now()
		if err == nil {
			var resp *http.Response
			resp, err = cfg.HTTPClient.Do(req)
			if err == nil {
				_, copyErr := io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes+1))
				closeErr := resp.Body.Close()
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					err = fmt.Errorf("HTTP status %d", resp.StatusCode)
				} else if copyErr != nil {
					err = copyErr
				} else if closeErr != nil {
					err = closeErr
				}
			}
		}
		col.observe(time.Since(started), len(payload), err)
	})
}

func runWSNode(ctx context.Context, cfg Config, node int, col *collector) {
	header := http.Header{}
	if token := expandToken(cfg.TokenTemplate, node); token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	dialCtx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
	conn, _, err := websocket.DefaultDialer.DialContext(dialCtx, cfg.Endpoint, header)
	cancel()
	if err != nil {
		col.observe(0, 0, errors.New("WebSocket connection failed"))
		return
	}
	defer conn.Close()
	payload, err := nodePayload(node)
	if err != nil {
		col.observe(0, 0, err)
		return
	}
	runSchedule(ctx, cfg, func() {
		_ = conn.SetWriteDeadline(time.Now().Add(cfg.RequestTimeout))
		started := time.Now()
		err := conn.WriteMessage(websocket.TextMessage, payload)
		col.observe(time.Since(started), len(payload), err)
	})
}

func runSchedule(ctx context.Context, cfg Config, send func()) {
	for sent := 0; cfg.ReportsPerNode == 0 || sent < cfg.ReportsPerNode; sent++ {
		select {
		case <-ctx.Done():
			return
		default:
		}
		send()
		if cfg.Interval <= 0 {
			continue
		}
		timer := time.NewTimer(cfg.Interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func nodePayload(node int) ([]byte, error) {
	report := common.Report{
		UUID:        fmt.Sprintf("00000000-0000-4000-8000-%012d", node),
		CPU:         common.CPUReport{Name: "virtual-cpu", Cores: 4, Arch: "amd64", Usage: 42.5},
		Ram:         common.RamReport{Total: 8 << 30, Used: 3 << 30},
		Swap:        common.RamReport{Total: 2 << 30, Used: 128 << 20},
		Load:        common.LoadReport{Load1: 0.5, Load5: 0.4, Load15: 0.3},
		Disk:        common.DiskReport{Total: 128 << 30, Used: 48 << 30},
		Network:     common.NetworkReport{Up: 4096, Down: 8192, TotalUp: int64(node+1) * (1 << 30), TotalDown: int64(node+1) * (2 << 30)},
		Connections: common.ConnectionsReport{TCP: 32, UDP: 8},
		Uptime:      int64(24 * time.Hour / time.Second), Process: 128,
		Message: "virtual-agent", Method: "replay",
	}
	return json.Marshal(report)
}

func expandToken(template string, node int) string {
	return strings.ReplaceAll(template, "{index}", fmt.Sprintf("%d", node))
}

func monitorHeap(ctx context.Context, col *collector, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		for old := col.peakHeap.Load(); stats.HeapAlloc > old && !col.peakHeap.CompareAndSwap(old, stats.HeapAlloc); old = col.peakHeap.Load() {
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func buildResult(cfg Config, col *collector, elapsed time.Duration) Result {
	col.mu.Lock()
	latencies := append([]time.Duration(nil), col.latencies...)
	col.mu.Unlock()
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	attempted := col.attempted.Load()
	rate := 0.0
	if elapsed > 0 {
		rate = float64(attempted) / elapsed.Seconds()
	}
	return Result{
		Mode: cfg.Mode, Nodes: cfg.Nodes, Attempted: attempted,
		Succeeded: col.succeeded.Load(), Failed: col.failed.Load(), BytesSent: col.bytesSent.Load(),
		Elapsed: elapsed, ReportsPerSec: rate,
		P50: percentile(latencies, 0.50), P95: percentile(latencies, 0.95), P99: percentile(latencies, 0.99),
		PeakHeapBytes: col.peakHeap.Load(), LatencySamples: len(latencies), DroppedSamples: col.dropped.Load(),
	}
}

func percentile(values []time.Duration, p float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1) * p)
	return values[index]
}
