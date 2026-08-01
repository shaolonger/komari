package jsonRpc

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestGetPingStatsForNodesUsesOneQueryForAllCacheMisses(t *testing.T) {
	pingStatsCache.Flush()
	dsn := fmt.Sprintf("file:%s-%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&models.Client{}, &models.PingTask{}, &models.PingRecord{}); err != nil {
		t.Fatal(err)
	}
	clients := []models.Client{
		{UUID: "ping-stats-a", Token: "ping-stats-token-a"},
		{UUID: "ping-stats-b", Token: "ping-stats-token-b"},
		{UUID: "ping-stats-unrequested", Token: "ping-stats-token-unrequested", Hidden: true},
	}
	if err := db.Create(&clients).Error; err != nil {
		t.Fatal(err)
	}
	task := models.PingTask{
		Name: "latency", Clients: models.StringArray{"ping-stats-a", "ping-stats-b", "ping-stats-unrequested"}, Type: "icmp", Target: "127.0.0.1", Interval: 60,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	records := []models.PingRecord{
		{Client: "ping-stats-a", TaskId: task.Id, Time: models.FromTime(now.Add(-3 * time.Minute)), Value: 10},
		{Client: "ping-stats-a", TaskId: task.Id, Time: models.FromTime(now.Add(-2 * time.Minute)), Value: 20},
		{Client: "ping-stats-a", TaskId: task.Id, Time: models.FromTime(now.Add(-time.Minute)), Value: -1},
		{Client: "ping-stats-b", TaskId: task.Id, Time: models.FromTime(now.Add(-time.Minute)), Value: 30},
		{Client: "ping-stats-unrequested", TaskId: task.Id, Time: models.FromTime(now.Add(-time.Minute)), Value: 999},
	}
	if err := db.Create(&records).Error; err != nil {
		t.Fatal(err)
	}

	result, queries := getPingStatsForNodesAt(context.Background(), db, []string{"ping-stats-a", "ping-stats-b"}, []models.PingTask{task}, now)
	if queries != 1 {
		t.Fatalf("ping stats SQL=%d, want 1", queries)
	}
	if _, ok := result["ping-stats-unrequested"]; ok {
		t.Fatal("unrequested hidden node leaked into ping stats")
	}
	statsA := result["ping-stats-a"][fmt.Sprintf("%d", task.Id)]
	if statsA.Total != 3 || statsA.Lost != 1 || statsA.Latest != 20 || statsA.Avg != 15 || statsA.Min != 10 || statsA.Max != 20 || math.Abs(statsA.Loss-100.0/3.0) > 0.0001 {
		t.Fatalf("node-a stats=%+v", statsA)
	}
	if statsB := result["ping-stats-b"][fmt.Sprintf("%d", task.Id)]; statsB.Latest != 30 || statsB.Avg != 30 {
		t.Fatalf("node-b stats=%+v", statsB)
	}

	cached, queries := getPingStatsForNodesAt(context.Background(), db, []string{"ping-stats-a", "ping-stats-b"}, []models.PingTask{task}, now)
	if queries != 0 || len(cached) != 2 {
		t.Fatalf("cached results=%+v queries=%d, want two/zero", cached, queries)
	}
}
