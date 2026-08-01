package metrics

import (
	"context"
	"testing"

	"github.com/komari-monitor/komari/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openCatalogTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.MetricRetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMetricCatalogRetentionIsAllowlistedAndPersistent(t *testing.T) {
	db := openCatalogTestDB(t)
	ctx := context.Background()
	definitions, err := List(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 24 {
		t.Fatalf("definitions = %d, want 24", len(definitions))
	}
	updated, err := UpdateRetention(ctx, db, "ping.latency_ms", 365)
	if err != nil || updated.RetentionDays != 365 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	after, err := List(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, definition := range after {
		if definition.Name == "ping.latency_ms" {
			found = definition.RetentionDays == 365
		}
	}
	if !found {
		t.Fatal("retention override was not returned")
	}
	if _, err := UpdateRetention(ctx, db, "injected.metric", 30); err == nil {
		t.Fatal("unknown metric was accepted")
	}
	if _, err := UpdateRetention(ctx, db, "cpu.usage", 0); err == nil {
		t.Fatal("unsafe retention was accepted")
	}
}
