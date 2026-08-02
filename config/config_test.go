package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type queryCounter struct {
	logger.Interface
	count atomic.Uint64
}

func (l *queryCounter) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	l.count.Add(1)
	l.Interface.Trace(ctx, begin, fc, err)
}

func setupConfigTest(t testing.TB) (*gorm.DB, *queryCounter) {
	t.Helper()
	counter := &queryCounter{Interface: logger.Default.LogMode(logger.Silent)}
	path := filepath.Join(t.TempDir(), "config.db")
	gdb, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: counter})
	if err != nil {
		t.Fatal(err)
	}
	subscribersMu.Lock()
	subscribers = nil
	subscribersMu.Unlock()
	SetDb(gdb)
	counter.count.Store(0)
	return gdb, counter
}

func TestSnapshotHotReadsDoNotTouchDatabase(t *testing.T) {
	_, counter := setupConfigTest(t)
	if err := SetMany(map[string]any{"name": "komari", "enabled": true, "count": 7}); err != nil {
		t.Fatal(err)
	}
	counter.count.Store(0)
	type view struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
		Count   int    `json:"count"`
	}
	for i := 0; i < 1_000; i++ {
		if value, err := Get("name"); err != nil || value != "komari" {
			t.Fatalf("Get = %v, %v", value, err)
		}
		if value, err := GetAs[int]("count"); err != nil || value != 7 {
			t.Fatalf("GetAs = %v, %v", value, err)
		}
		if values, err := GetMany(map[string]any{"enabled": false}); err != nil || values["enabled"] != true {
			t.Fatalf("GetMany = %v, %v", values, err)
		}
		if values, err := GetManyAs[view](); err != nil || values.Name != "komari" || values.Count != 7 {
			t.Fatalf("GetManyAs = %+v, %v", values, err)
		}
		if _, err := GetAll(); err != nil {
			t.Fatal(err)
		}
	}
	if queries := counter.count.Load(); queries != 0 {
		t.Fatalf("hot reads executed %d SQL statements", queries)
	}
}

func TestDefaultsPersistOnceAndPublish(t *testing.T) {
	db, _ := setupConfigTest(t)
	events := make(chan ConfigEvent, 4)
	Subscribe(func(event ConfigEvent) { events <- event })
	value, err := GetAs[int]("missing", 42)
	if err != nil || value != 42 {
		t.Fatalf("default = %d, %v", value, err)
	}
	value, err = GetAs[int]("missing", 99)
	if err != nil || value != 42 {
		t.Fatalf("existing default overwritten: %d, %v", value, err)
	}
	var count int64
	if err := db.Model(&ConfigItem{}).Where("key = ?", "missing").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("default rows = %d, %v", count, err)
	}
	select {
	case event := <-events:
		if changed, got := IsChangedT[int](event, "missing"); !changed || got != 42 {
			t.Fatalf("default event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("default event not published")
	}
	select {
	case event := <-events:
		t.Fatalf("second default unexpectedly published: %+v", event)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestFailedWriteDoesNotPublishOrReplaceSnapshot(t *testing.T) {
	db, _ := setupConfigTest(t)
	if err := Set("stable", "before"); err != nil {
		t.Fatal(err)
	}
	events := make(chan ConfigEvent, 1)
	Subscribe(func(event ConfigEvent) { events <- event })
	injected := errors.New("injected config failure")
	if err := db.Callback().Create().Before("gorm:create").Register("config:fail", func(tx *gorm.DB) {
		if tx.Statement.Table == "configs" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := Set("stable", "after"); !errors.Is(err, injected) {
		t.Fatalf("Set error = %v", err)
	}
	if got, err := GetAs[string]("stable"); err != nil || got != "before" {
		t.Fatalf("failed transaction leaked into snapshot: %q, %v", got, err)
	}
	select {
	case event := <-events:
		t.Fatalf("failed transaction published event: %+v", event)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestSnapshotOwnsMutableValuesAndEvents(t *testing.T) {
	_, _ = setupConfigTest(t)
	events := make(chan ConfigEvent, 1)
	Subscribe(func(event ConfigEvent) {
		event.New["map"].(map[string]any)["secret"] = "event-mutated"
		events <- event
	})
	input := map[string]any{"secret": "original"}
	if err := Set("map", input); err != nil {
		t.Fatal(err)
	}
	input["secret"] = "source-mutated"
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("event not delivered")
	}
	got, err := GetAs[map[string]any]("map")
	if err != nil || got["secret"] != "original" {
		t.Fatalf("snapshot mutation: %v, %v", got, err)
	}
	got["secret"] = "return-mutated"
	again, _ := GetAs[map[string]any]("map")
	if again["secret"] != "original" {
		t.Fatalf("returned map mutated snapshot: %v", again)
	}
}

func TestSetManyAsUsesJSONTagNameWithoutOptions(t *testing.T) {
	db, _ := setupConfigTest(t)
	type settings struct {
		Name string `json:"name,omitempty"`
	}
	if err := SetManyAs(settings{Name: "value"}); err != nil {
		t.Fatal(err)
	}
	if got, err := GetAs[string]("name"); err != nil || got != "value" {
		t.Fatalf("name = %q, %v", got, err)
	}
	var invalid int64
	if err := db.Model(&ConfigItem{}).Where("key = ?", "name,omitempty").Count(&invalid).Error; err != nil || invalid != 0 {
		t.Fatalf("invalid tag rows = %d, %v", invalid, err)
	}
}

func TestConcurrentSnapshotReadsAndWrites(t *testing.T) {
	db, _ := setupConfigTest(t)
	if err := Set("counter", 0); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for writer := 0; writer < 4; writer++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if err := Set("counter", writer*100+i); err != nil {
					t.Errorf("Set: %v", err)
					return
				}
			}
		}(writer)
	}
	for reader := 0; reader < 16; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 2_000; i++ {
				if _, err := GetAs[int]("counter"); err != nil {
					t.Errorf("GetAs: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	got, err := GetAs[int]("counter")
	if err != nil {
		t.Fatal(err)
	}
	var item ConfigItem
	if err := db.First(&item, "key = ?", "counter").Error; err != nil {
		t.Fatal(err)
	}
	var persisted int
	if err := json.Unmarshal([]byte(item.Value), &persisted); err != nil || persisted != got {
		t.Fatalf("snapshot/database mismatch: snapshot=%d database=%d error=%v", got, persisted, err)
	}
}

func BenchmarkConfigRead(b *testing.B) {
	db, counter := setupConfigTest(b)
	if err := Set("benchmark", 42); err != nil {
		b.Fatal(err)
	}
	b.Run("snapshot", func(b *testing.B) {
		counter.count.Store(0)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if _, err := GetAs[int]("benchmark"); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		b.ReportMetric(float64(counter.count.Load())/float64(b.N), "sql/op")
	})
	b.Run("legacy-database", func(b *testing.B) {
		counter.count.Store(0)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			var item ConfigItem
			if err := db.First(&item, "key = ?", "benchmark").Error; err != nil {
				b.Fatal(err)
			}
			var value int
			if err := json.Unmarshal([]byte(item.Value), &value); err != nil || value != 42 {
				b.Fatal(fmt.Errorf("decode value: %d, %w", value, err))
			}
		}
		b.StopTimer()
		b.ReportMetric(float64(counter.count.Load())/float64(b.N), "sql/op")
	})
}
