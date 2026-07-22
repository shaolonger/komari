// Package observability contains the low-cardinality server performance metrics.
// It intentionally exposes no API that accepts arbitrary label values.
package observability

import (
	"fmt"
	"io"
	"runtime"
	"sync/atomic"
	"time"
)

var defaultRegistry Registry

type Registry struct {
	reportsAccepted atomic.Uint64
	reportsRejected atomic.Uint64
	reportBytes     atomic.Uint64
	reportLatency   durationHistogram
	flushQueueDepth atomic.Int64
	batches         atomic.Uint64
	batchRows       atomic.Uint64
	batchRetries    atomic.Uint64
	sqliteOps       atomic.Uint64
	sqliteErrors    atomic.Uint64
	sqliteLatency   durationHistogram
	compressionRuns atomic.Uint64
	compressionRows atomic.Uint64
	compressionErrs atomic.Uint64
	compressionTime durationHistogram
	queryOps        atomic.Uint64
	queryRows       atomic.Uint64
	queryErrors     atomic.Uint64
	queryLatency    durationHistogram
	wsConnections   atomic.Int64
	wsReconnects    atomic.Uint64
	wsSlowConsumers atomic.Uint64
	credentialHits  atomic.Uint64
	credentialMiss  atomic.Uint64
	credentialEvict atomic.Uint64
}

var durationBounds = [...]time.Duration{
	100 * time.Microsecond,
	500 * time.Microsecond,
	time.Millisecond,
	5 * time.Millisecond,
	10 * time.Millisecond,
	50 * time.Millisecond,
	100 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	5 * time.Second,
}

type durationHistogram struct {
	buckets [len(durationBounds) + 1]atomic.Uint64
	count   atomic.Uint64
	nanos   atomic.Uint64
}

func (h *durationHistogram) observe(d time.Duration) {
	if d < 0 {
		d = 0
	}
	index := len(durationBounds)
	for i, bound := range durationBounds {
		if d <= bound {
			index = i
			break
		}
	}
	h.buckets[index].Add(1)
	h.count.Add(1)
	h.nanos.Add(uint64(d))
}

func ObserveReport(bytes int, elapsed time.Duration, accepted bool) {
	if accepted {
		defaultRegistry.reportsAccepted.Add(1)
	} else {
		defaultRegistry.reportsRejected.Add(1)
	}
	if bytes > 0 {
		defaultRegistry.reportBytes.Add(uint64(bytes))
	}
	defaultRegistry.reportLatency.observe(elapsed)
}

func SetFlushQueueDepth(depth int) { defaultRegistry.flushQueueDepth.Store(int64(max(depth, 0))) }

func ObserveBatch(rows, retries int) {
	defaultRegistry.batches.Add(1)
	if rows > 0 {
		defaultRegistry.batchRows.Add(uint64(rows))
	}
	if retries > 0 {
		defaultRegistry.batchRetries.Add(uint64(retries))
	}
}

func ObserveSQLite(elapsed time.Duration, failed bool) {
	defaultRegistry.sqliteOps.Add(1)
	if failed {
		defaultRegistry.sqliteErrors.Add(1)
	}
	defaultRegistry.sqliteLatency.observe(elapsed)
}

func ObserveCompression(rows int, elapsed time.Duration, failed bool) {
	defaultRegistry.compressionRuns.Add(1)
	if rows > 0 {
		defaultRegistry.compressionRows.Add(uint64(rows))
	}
	if failed {
		defaultRegistry.compressionErrs.Add(1)
	}
	defaultRegistry.compressionTime.observe(elapsed)
}

func ObserveQuery(rows int, elapsed time.Duration, failed bool) {
	defaultRegistry.queryOps.Add(1)
	if rows > 0 {
		defaultRegistry.queryRows.Add(uint64(rows))
	}
	if failed {
		defaultRegistry.queryErrors.Add(1)
	}
	defaultRegistry.queryLatency.observe(elapsed)
}

func WSConnected()    { defaultRegistry.wsConnections.Add(1) }
func WSDisconnected() { defaultRegistry.wsConnections.Add(-1) }
func WSReconnected()  { defaultRegistry.wsReconnects.Add(1) }
func WSSlowConsumer() { defaultRegistry.wsSlowConsumers.Add(1) }

func CredentialCacheHit()          { defaultRegistry.credentialHits.Add(1) }
func CredentialCacheMiss()         { defaultRegistry.credentialMiss.Add(1) }
func CredentialCacheInvalidation() { defaultRegistry.credentialEvict.Add(1) }

// WritePrometheus writes a stable Prometheus text exposition. Metric names and
// labels are fixed at compile time, preventing credential/high-cardinality leaks.
func WritePrometheus(w io.Writer) error { return defaultRegistry.writePrometheus(w) }

func (r *Registry) writePrometheus(w io.Writer) error {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	values := []struct {
		name  string
		help  string
		type_ string
		value any
	}{
		{"komari_reports_accepted_total", "Accepted telemetry reports.", "counter", r.reportsAccepted.Load()},
		{"komari_reports_rejected_total", "Rejected telemetry reports.", "counter", r.reportsRejected.Load()},
		{"komari_report_bytes_total", "Telemetry payload bytes received.", "counter", r.reportBytes.Load()},
		{"komari_flush_queue_depth", "Current telemetry flush queue depth.", "gauge", max(r.flushQueueDepth.Load(), 0)},
		{"komari_batches_total", "Persisted telemetry batches.", "counter", r.batches.Load()},
		{"komari_batch_rows_total", "Rows submitted in telemetry batches.", "counter", r.batchRows.Load()},
		{"komari_batch_retries_total", "Telemetry batch retry attempts.", "counter", r.batchRetries.Load()},
		{"komari_sqlite_operations_total", "SQLite statements observed by GORM.", "counter", r.sqliteOps.Load()},
		{"komari_sqlite_errors_total", "Failed SQLite statements observed by GORM.", "counter", r.sqliteErrors.Load()},
		{"komari_compression_runs_total", "History compaction runs.", "counter", r.compressionRuns.Load()},
		{"komari_compression_rows_total", "Rows considered by history compaction.", "counter", r.compressionRows.Load()},
		{"komari_compression_errors_total", "Failed history compaction runs.", "counter", r.compressionErrs.Load()},
		{"komari_queries_total", "Instrumented history queries.", "counter", r.queryOps.Load()},
		{"komari_query_rows_total", "Rows returned by instrumented history queries.", "counter", r.queryRows.Load()},
		{"komari_query_errors_total", "Failed instrumented history queries.", "counter", r.queryErrors.Load()},
		{"komari_websocket_connections", "Current authenticated Agent WebSocket connections.", "gauge", max(r.wsConnections.Load(), 0)},
		{"komari_websocket_reconnects_total", "Agent WebSocket replacements.", "counter", r.wsReconnects.Load()},
		{"komari_websocket_slow_consumers_total", "WebSocket slow-consumer disconnects.", "counter", r.wsSlowConsumers.Load()},
		{"komari_credential_cache_hits_total", "Credential cache hits without credential labels.", "counter", r.credentialHits.Load()},
		{"komari_credential_cache_misses_total", "Credential cache misses without credential labels.", "counter", r.credentialMiss.Load()},
		{"komari_credential_cache_invalidations_total", "Credential cache invalidations without credential labels.", "counter", r.credentialEvict.Load()},
		{"komari_runtime_heap_bytes", "Current Go heap allocation.", "gauge", mem.HeapAlloc},
		{"komari_runtime_goroutines", "Current Go goroutine count.", "gauge", runtime.NumGoroutine()},
	}
	for _, metric := range values {
		if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %v\n", metric.name, metric.help, metric.name, metric.type_, metric.name, metric.value); err != nil {
			return err
		}
	}
	for _, hist := range []struct {
		name string
		h    *durationHistogram
	}{
		{"komari_report_duration_seconds", &r.reportLatency},
		{"komari_sqlite_duration_seconds", &r.sqliteLatency},
		{"komari_compression_duration_seconds", &r.compressionTime},
		{"komari_query_duration_seconds", &r.queryLatency},
	} {
		if err := writeHistogram(w, hist.name, hist.h); err != nil {
			return err
		}
	}
	return nil
}

func writeHistogram(w io.Writer, name string, h *durationHistogram) error {
	cumulative := uint64(0)
	for i := range h.buckets {
		cumulative += h.buckets[i].Load()
		le := "+Inf"
		if i < len(durationBounds) {
			le = fmt.Sprintf("%g", durationBounds[i].Seconds())
		}
		if _, err := fmt.Fprintf(w, "%s_bucket{le=%q} %d\n", name, le, cumulative); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "%s_sum %g\n%s_count %d\n", name, float64(h.nanos.Load())/float64(time.Second), name, h.count.Load())
	return err
}

// ResetForTest resets the process registry and must only be used by tests that
// own the registry for their duration.
func ResetForTest() { defaultRegistry = Registry{} }
