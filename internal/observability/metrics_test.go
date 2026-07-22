package observability

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMetricsAreBoundedAndSanitized(t *testing.T) {
	ResetForTest()
	ObserveReport(512, 2*time.Millisecond, true)
	ObserveBatch(100, 2)
	ObserveSQLite(time.Millisecond, true)
	ObserveCompression(200, 3*time.Millisecond, false)
	ObserveQuery(10, time.Millisecond, false)
	WSConnected()
	defer WSDisconnected()
	var out bytes.Buffer
	if err := WritePrometheus(&out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"komari_reports_accepted_total 1", "komari_batch_rows_total 100", `le="+Inf"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics missing %q", want)
		}
	}
	for _, secret := range []string{"token", "session", "authorization", "client_uuid", "ip_address"} {
		if strings.Contains(strings.ToLower(text), secret) {
			t.Fatalf("metrics unexpectedly contain sensitive/high-cardinality term %q", secret)
		}
	}
}

func TestConcurrentCollection(t *testing.T) {
	ResetForTest()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1_000; j++ {
				ObserveReport(j, time.Duration(j), j%2 == 0)
				ObserveSQLite(time.Duration(j), false)
				var out bytes.Buffer
				if err := WritePrometheus(&out); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func BenchmarkObserveReport(b *testing.B) {
	ResetForTest()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			ObserveReport(512, 2*time.Millisecond, true)
		}
	})
}
