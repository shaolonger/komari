package notifier

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	recordsdb "github.com/komari-monitor/komari/database/records"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("TZ", "UTC")
	os.Exit(m.Run())
}

func TestDueTrafficReportCadencesUsesNaturalPeriods(t *testing.T) {
	now := time.Date(2026, 7, 6, defaultNotificationReportSendHour, 0, 0, 0, time.UTC) // Monday
	notification := models.TrafficReportNotification{
		Enable:  true,
		Daily:   true,
		Weekly:  true,
		Monthly: true,
	}

	due := dueTrafficReportCadences(notification, now)
	assertCadences(t, due, []trafficReportCadence{trafficReportDaily, trafficReportWeekly})

	notification.LastDailyNotified = models.FromTime(now)
	notification.LastWeeklyNotified = models.FromTime(now)
	due = dueTrafficReportCadences(notification, now.Add(time.Hour))
	assertCadences(t, due, nil)
}

func TestDueTrafficReportCadencesWaitsForScheduleBoundaries(t *testing.T) {
	beforeSendHour := time.Date(2026, 7, 6, defaultNotificationReportSendHour-1, 59, 0, 0, time.UTC)
	notification := models.TrafficReportNotification{
		Enable:  true,
		Daily:   true,
		Weekly:  true,
		Monthly: true,
	}
	if due := dueTrafficReportCadences(notification, beforeSendHour); len(due) != 0 {
		t.Fatalf("due before send hour = %+v, want none", due)
	}

	monthlyNow := time.Date(2026, 7, 1, defaultNotificationReportSendHour, 0, 0, 0, time.UTC)
	monthlyOnly := models.TrafficReportNotification{Enable: true, Monthly: true}
	assertCadences(t, dueTrafficReportCadences(monthlyOnly, monthlyNow), []trafficReportCadence{trafficReportMonthly})

	notFirstDay := time.Date(2026, 7, 2, defaultNotificationReportSendHour, 0, 0, 0, time.UTC)
	if due := dueTrafficReportCadences(monthlyOnly, notFirstDay); len(due) != 0 {
		t.Fatalf("monthly due on non-first day = %+v, want none", due)
	}
}

func TestTrafficReportWindowUsesCompletedNaturalRanges(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 7, 6, defaultNotificationReportSendHour, 0, 0, 0, time.UTC)

	dailyStart, dailyEnd := trafficReportWindow(trafficReportDaily, now, loc)
	assertTime(t, dailyStart, time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
	assertTime(t, dailyEnd, time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC))

	weeklyStart, weeklyEnd := trafficReportWindow(trafficReportWeekly, now, loc)
	assertTime(t, weeklyStart, time.Date(2026, 6, 29, 0, 0, 0, 0, time.UTC))
	assertTime(t, weeklyEnd, time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC))

	monthlyNow := time.Date(2026, 7, 1, defaultNotificationReportSendHour, 0, 0, 0, time.UTC)
	monthlyStart, monthlyEnd := trafficReportWindow(trafficReportMonthly, monthlyNow, loc)
	assertTime(t, monthlyStart, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	assertTime(t, monthlyEnd, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
}

func TestLoadNotificationLocationAcceptsUTCOffsets(t *testing.T) {
	loc, err := LoadNotificationLocation("UTC+8")
	if err != nil {
		t.Fatalf("LoadNotificationLocation(UTC+8) error = %v", err)
	}
	if loc.String() != "UTC+8" {
		t.Fatalf("location name = %q, want UTC+8", loc.String())
	}
	_, offset := time.Date(2026, 7, 6, 9, 0, 0, 0, loc).Zone()
	if offset != 8*3600 {
		t.Fatalf("offset = %d, want %d", offset, 8*3600)
	}

	halfHour, err := LoadNotificationLocation("GMT+05:30")
	if err != nil {
		t.Fatalf("LoadNotificationLocation(GMT+05:30) error = %v", err)
	}
	_, offset = time.Date(2026, 7, 6, 9, 0, 0, 0, halfHour).Zone()
	if offset != 5*3600+30*60 {
		t.Fatalf("offset = %d, want %d", offset, 5*3600+30*60)
	}

	if _, err := LoadNotificationLocation("UTC+15"); err == nil {
		t.Fatal("LoadNotificationLocation(UTC+15) error = nil, want range error")
	}
}

func TestBuildTrafficReportMessageIncludesOperationalSummary(t *testing.T) {
	start := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	client := models.Client{
		UUID:             "node-a",
		Name:             "node-a",
		TrafficLimit:     4 * 1024,
		TrafficLimitType: "sum",
	}
	stats := recordsdb.TrafficStats{
		Up:       1024,
		Down:     2048,
		Total:    3072,
		AvgBps:   1.5,
		PeakBps:  2048,
		Samples:  42,
		Coverage: 0.95,
		Quality:  "exact",
	}

	message := buildTrafficReportMessage(client, trafficReportDaily, start, end, stats, time.UTC)
	for _, want := range []string{
		"流量报告",
		"周期: 日报",
		"时区: UTC",
		"上行: 1.00 KB",
		"下行: 2.00 KB",
		"总计: 3.00 KB",
		"覆盖率: 95.0%",
		"数据质量: 完整",
		"额度用量: 3.00 KB / 4.00 KB (75.0%)",
		"剩余额度: 1.00 KB",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("message does not contain %q:\n%s", want, message)
		}
	}
}

func assertCadences(t *testing.T, got, want []trafficReportCadence) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("cadences = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cadences = %+v, want %+v", got, want)
		}
	}
}

func assertTime(t *testing.T, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Fatalf("time = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}
