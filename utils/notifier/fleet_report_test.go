package notifier

import (
	"strings"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	messageevent "github.com/komari-monitor/komari/database/models/messageEvent"
)

func TestBuildFleetReportDataSummarizesRankingsAndAnomalies(t *testing.T) {
	start := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	now := end.Add(9 * time.Hour)
	clients := []models.Client{
		{
			UUID:             "node-a",
			Name:             "alpha",
			Group:            "edge",
			CpuCores:         2,
			MemTotal:         1024,
			DiskTotal:        2048,
			TrafficLimit:     4096,
			TrafficLimitType: "sum",
		},
		{
			UUID:      "node-b",
			Name:      "beta",
			CpuCores:  1,
			MemTotal:  1024,
			DiskTotal: 2048,
		},
	}
	recordsByClient := map[string][]models.Record{
		"node-a": {
			fleetRecord("node-a", start.Add(time.Hour), 70, 950, 1024, 1700, 2048, 2.8, 1000, 1500),
			fleetRecord("node-a", start.Add(2*time.Hour), 92, 960, 1024, 1900, 2048, 3.2, 2500, 3000),
			fleetRecord("node-a", start.Add(3*time.Hour), 96, 970, 1024, 1950, 2048, 3.4, 3500, 4500),
		},
	}
	pingByClient := map[string][]models.PingRecord{
		"node-a": {
			fleetPing("node-a", start.Add(time.Hour), 120),
			fleetPing("node-a", start.Add(2*time.Hour), -1),
			fleetPing("node-a", start.Add(3*time.Hour), 320),
		},
	}

	data := buildFleetReportData(clients, recordsByClient, pingByClient, trafficReportDaily, start, end, now, time.UTC, 3)

	if data.Kind != "fleet_report" {
		t.Fatalf("kind = %q, want fleet_report", data.Kind)
	}
	if data.PeriodLabel != "2026-07-05" {
		t.Fatalf("period label = %q, want previous local day", data.PeriodLabel)
	}
	if data.Summary.TotalNodes != 2 || data.Summary.ReportNodes != 1 || data.Summary.NoDataNodes != 1 {
		t.Fatalf("summary nodes = %+v", data.Summary)
	}
	if data.Summary.CriticalAnomalies == 0 {
		t.Fatalf("critical anomalies = 0, want at least one")
	}
	if data.Rankings[0].Items[0].Name != "alpha" {
		t.Fatalf("cpu top = %+v, want alpha", data.Rankings[0].Items[0])
	}
	if !fleetReportHasAnomaly(data, "beta", "无监控样本") {
		t.Fatalf("anomalies missing beta no-data entry: %+v", data.Anomalies)
	}

	message := buildFleetReportText(data)
	for _, want := range []string{"全局运维报告", "健康分:", "CPU 压力 Top", "异常摘要", "建议动作"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q:\n%s", want, message)
		}
	}
}

func TestBuildFleetReportEventCarriesStructuredPayload(t *testing.T) {
	now := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)
	data := FleetReportData{
		Kind:         "fleet_report",
		Cadence:      string(trafficReportDaily),
		CadenceLabel: "日报",
		Timezone:     "UTC",
	}
	event := buildFleetReportEvent(data, "hello", []models.Client{{UUID: "node-a", Name: "alpha"}}, now, time.UTC)

	if event.Event != messageevent.FleetReport {
		t.Fatalf("event = %q, want FleetReport", event.Event)
	}
	if event.Timezone != "UTC" {
		t.Fatalf("timezone = %q, want UTC", event.Timezone)
	}
	if event.Data == nil {
		t.Fatal("event data is nil")
	}
}

func TestDueFleetReportCadencesAvoidsDuplicatePeriods(t *testing.T) {
	now := time.Date(2026, 7, 6, defaultNotificationReportSendHour, 0, 0, 0, time.UTC) // Monday
	notification := models.FleetReportNotification{
		Enable:  true,
		Daily:   true,
		Weekly:  true,
		Monthly: true,
	}

	assertCadences(t, dueFleetReportCadences(notification, now), []trafficReportCadence{trafficReportDaily, trafficReportWeekly})

	notification.LastDailyNotified = models.FromTime(now)
	notification.LastWeeklyNotified = models.FromTime(now)
	if due := dueFleetReportCadences(notification, now.Add(2*time.Hour)); len(due) != 0 {
		t.Fatalf("due after same-cycle notification = %+v, want none", due)
	}

	monthStart := time.Date(2026, 7, 1, defaultNotificationReportSendHour, 0, 0, 0, time.UTC)
	monthly := models.FleetReportNotification{Enable: true, Monthly: true}
	assertCadences(t, dueFleetReportCadences(monthly, monthStart), []trafficReportCadence{trafficReportMonthly})
	if due := dueFleetReportCadences(monthly, monthStart.AddDate(0, 0, 1)); len(due) != 0 {
		t.Fatalf("monthly due on non-first day = %+v, want none", due)
	}
}

func TestParseFleetReportCadence(t *testing.T) {
	cases := map[string]trafficReportCadence{
		"":        trafficReportDaily,
		"daily":   trafficReportDaily,
		"weekly":  trafficReportWeekly,
		"monthly": trafficReportMonthly,
	}
	for input, want := range cases {
		got, err := parseFleetReportCadence(input)
		if err != nil {
			t.Fatalf("parseFleetReportCadence(%q) error = %v", input, err)
		}
		if got != want {
			t.Fatalf("parseFleetReportCadence(%q) = %q, want %q", input, got, want)
		}
	}
	if _, err := parseFleetReportCadence("yearly"); err == nil {
		t.Fatal("parseFleetReportCadence(yearly) error = nil, want error")
	}
}

func fleetRecord(client string, at time.Time, cpu float32, ram, ramTotal, disk, diskTotal int64, load float32, totalUp, totalDown int64) models.Record {
	return models.Record{
		Client:       client,
		Time:         models.FromTime(at),
		Cpu:          cpu,
		Ram:          ram,
		RamTotal:     ramTotal,
		Disk:         disk,
		DiskTotal:    diskTotal,
		Load:         load,
		NetTotalUp:   totalUp,
		NetTotalDown: totalDown,
	}
}

func fleetPing(client string, at time.Time, value int) models.PingRecord {
	return models.PingRecord{
		Client: client,
		TaskId: 1,
		Time:   models.FromTime(at),
		Value:  value,
	}
}

func fleetReportHasAnomaly(data FleetReportData, name, title string) bool {
	for _, anomaly := range data.Anomalies {
		if anomaly.Name == name && anomaly.Title == title {
			return true
		}
	}
	return false
}
