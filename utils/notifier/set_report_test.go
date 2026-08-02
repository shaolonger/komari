package notifier

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newSetReportTestDB(t testing.TB) *gorm.DB {
	t.Helper()
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
	if err := db.AutoMigrate(&models.Client{}, &models.Record{}, &models.PingTask{}, &models.PingRecord{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestQueryFleetReportInputsMatchesLegacyPerClientData(t *testing.T) {
	db := newSetReportTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-15 * time.Minute)
	allClients := []models.Client{
		{UUID: "fleet-set-a", Token: "fleet-set-token-a", Name: "alpha"},
		{UUID: "fleet-set-hidden", Token: "fleet-set-token-hidden", Name: "hidden", Hidden: true},
		{UUID: "fleet-set-empty", Token: "fleet-set-token-empty", Name: "empty"},
	}
	if err := db.Create(&allClients).Error; err != nil {
		t.Fatal(err)
	}
	recordRows := []models.Record{
		fleetRecord("fleet-set-a", start.Add(time.Minute), 10, 100, 1_000, 200, 2_000, 1, 100, 200),
		fleetRecord("fleet-set-a", start.Add(2*time.Minute), 20, 200, 1_000, 400, 2_000, 2, 300, 500),
		fleetRecord("fleet-set-hidden", start.Add(time.Minute), 30, 300, 1_000, 600, 2_000, 3, 400, 600),
	}
	if err := db.Create(&recordRows).Error; err != nil {
		t.Fatal(err)
	}
	task := models.PingTask{Name: "set-ping", Clients: models.StringArray{"fleet-set-a", "fleet-set-hidden"}, Type: "icmp", Target: "127.0.0.1", Interval: 60}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	pingRows := []models.PingRecord{
		{Client: "fleet-set-a", TaskId: task.Id, Time: models.FromTime(start.Add(time.Minute)), Value: 10},
		{Client: "fleet-set-hidden", TaskId: task.Id, Time: models.FromTime(start.Add(time.Minute)), Value: 20},
	}
	if err := db.Create(&pingRows).Error; err != nil {
		t.Fatal(err)
	}

	inputs, err := queryFleetReportInputs(context.Background(), db, allClients, start, now)
	if err != nil {
		t.Fatal(err)
	}
	if inputs.SQLQueries != 2 {
		t.Fatalf("fleet input SQL=%d, want 2 independent of node count", inputs.SQLQueries)
	}
	wantRecords := map[string][]models.Record{
		"fleet-set-a":      recordRows[:2],
		"fleet-set-hidden": recordRows[2:],
	}
	wantPing := map[string][]models.PingRecord{
		"fleet-set-a":      pingRows[:1],
		"fleet-set-hidden": pingRows[1:],
	}
	want := buildFleetReportData(allClients, wantRecords, wantPing, trafficReportDaily, start, now, now, time.UTC, 5)
	got := buildFleetReportData(allClients, inputs.recordsByClient, inputs.pingByClient, trafficReportDaily, start, now, now, time.UTC, 5)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("set report differs from legacy maps\n got: %+v\nwant: %+v", got, want)
	}
	if len(inputs.recordsByClient["fleet-set-hidden"]) != 1 {
		t.Fatal("global fleet report incorrectly excluded a configured hidden node")
	}
}

func TestEvaluateLoadNotificationTasksScansEachIntervalOnce(t *testing.T) {
	db := newSetReportTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	clients := []models.Client{
		{UUID: "load-set-a", Token: "load-set-token-a", Name: "alpha", MemTotal: 1_000},
		{UUID: "load-set-hidden", Token: "load-set-token-hidden", Name: "hidden", Hidden: true, MemTotal: 1_000},
		{UUID: "load-set-empty", Token: "load-set-token-empty", Name: "empty", MemTotal: 1_000},
	}
	if err := db.Create(&clients).Error; err != nil {
		t.Fatal(err)
	}
	recordRows := []models.Record{
		{Client: "load-set-a", Time: models.FromTime(now.Add(-2 * time.Minute)), Cpu: 90, Ram: 900, RamTotal: 1_000},
		{Client: "load-set-a", Time: models.FromTime(now.Add(-time.Minute)), Cpu: 10, Ram: 850, RamTotal: 1_000},
		{Client: "load-set-hidden", Time: models.FromTime(now.Add(-time.Minute)), Cpu: 95, Ram: 950, RamTotal: 1_000},
	}
	if err := db.Create(&recordRows).Error; err != nil {
		t.Fatal(err)
	}
	tasks := []models.LoadNotification{
		{Id: 1, Name: "cpu", Clients: models.StringArray{"load-set-a", "load-set-hidden", "load-set-empty"}, Metric: "cpu", Threshold: 80, Ratio: 0.5, Interval: 15},
		{Id: 2, Name: "ram", Clients: models.StringArray{"load-set-a", "load-set-hidden"}, Metric: "ram", Threshold: 80, Ratio: 1, Interval: 15},
		{Id: 3, Name: "cooldown", Clients: models.StringArray{"load-set-a"}, Metric: "cpu", Threshold: 1, Ratio: 1, Interval: 15, LastNotified: models.FromTime(now.Add(-time.Minute))},
	}

	results, queries, err := evaluateLoadNotificationTasks(context.Background(), db, tasks, now)
	if err != nil {
		t.Fatal(err)
	}
	if queries != 2 {
		t.Fatalf("load notification SQL=%d, want one client + one record query", queries)
	}
	if _, ok := results[3]; ok {
		t.Fatal("cooldown task was evaluated")
	}
	if got := clientUUIDs(results[1]); !reflect.DeepEqual(got, []string{"load-set-a", "load-set-hidden"}) {
		t.Fatalf("CPU matches=%v", got)
	}
	if got := clientUUIDs(results[2]); !reflect.DeepEqual(got, []string{"load-set-a", "load-set-hidden"}) {
		t.Fatalf("RAM matches=%v", got)
	}
	for _, task := range tasks[:2] {
		for _, client := range clients {
			var perClient []models.Record
			for _, record := range recordRows {
				if record.Client == client.UUID {
					perClient = append(perClient, record)
				}
			}
			legacy := checkMetricThresholdWithClients(perClient, task, map[string]models.Client{client.UUID: client})
			setMatched := containsClient(results[task.Id], client.UUID)
			if legacy != setMatched {
				t.Fatalf("task=%d client=%s set=%v legacy=%v", task.Id, client.UUID, setMatched, legacy)
			}
		}
	}
}

func BenchmarkEvaluateLoadNotificationTasks10000Assignments(b *testing.B) {
	db := newSetReportTestDB(b)
	now := time.Now().UTC().Truncate(time.Second)
	const clientsCount = 1_000
	const tasksCount = 10
	clients := make([]models.Client, 0, clientsCount)
	recordRows := make([]models.Record, 0, clientsCount)
	clientIDs := make(models.StringArray, 0, clientsCount)
	for index := 0; index < clientsCount; index++ {
		uuid := fmt.Sprintf("load-bench-%04d", index)
		clientIDs = append(clientIDs, uuid)
		clients = append(clients, models.Client{UUID: uuid, Token: "token-" + uuid, MemTotal: 1_000})
		recordRows = append(recordRows, models.Record{Client: uuid, Time: models.FromTime(now.Add(-time.Minute)), Cpu: 90})
	}
	if err := db.CreateInBatches(&clients, 20).Error; err != nil {
		b.Fatal(err)
	}
	if err := db.CreateInBatches(&recordRows, 250).Error; err != nil {
		b.Fatal(err)
	}
	loadTasks := make([]models.LoadNotification, 0, tasksCount)
	for index := 0; index < tasksCount; index++ {
		loadTasks = append(loadTasks, models.LoadNotification{Id: uint(index + 1), Clients: clientIDs, Metric: "cpu", Threshold: 80, Ratio: 1, Interval: 15})
	}

	b.ReportAllocs()
	b.ReportMetric(2, "sql/op")
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		results, queries, err := evaluateLoadNotificationTasks(context.Background(), db, loadTasks, now)
		if err != nil {
			b.Fatal(err)
		}
		if queries != 2 || len(results) != tasksCount || len(results[1]) != clientsCount {
			b.Fatalf("queries=%d tasks=%d matches=%d", queries, len(results), len(results[1]))
		}
	}
}

func clientUUIDs(clients []models.Client) []string {
	result := make([]string, 0, len(clients))
	for _, client := range clients {
		result = append(result, client.UUID)
	}
	return result
}

func containsClient(clients []models.Client, uuid string) bool {
	for _, client := range clients {
		if client.UUID == uuid {
			return true
		}
	}
	return false
}
