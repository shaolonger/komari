package notifier

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/komari-monitor/komari/config"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	messageevent "github.com/komari-monitor/komari/database/models/messageEvent"
	recordsdb "github.com/komari-monitor/komari/database/records"
	"github.com/komari-monitor/komari/utils/messageSender"
)

type trafficReportCadence string

const (
	trafficReportDaily   trafficReportCadence = "daily"
	trafficReportWeekly  trafficReportCadence = "weekly"
	trafficReportMonthly trafficReportCadence = "monthly"
)

var trafficReportCadenceOrder = []trafficReportCadence{
	trafficReportDaily,
	trafficReportWeekly,
	trafficReportMonthly,
}

// CheckTrafficReportScheduledWork sends configured daily, weekly and monthly
// traffic reports. Reports are sent after 09:00 in the application timezone.
func CheckTrafficReportScheduledWork() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		CheckTrafficReportOnce(time.Now())
		<-ticker.C
	}
}

func CheckTrafficReportOnce(now time.Time) {
	loc := notificationReportLocation()
	sendHour := notificationReportSendHour()
	localNow := now.In(loc)
	if localNow.Hour() < sendHour {
		return
	}
	if !trafficReportNotificationEnabled() {
		return
	}

	notifications, err := getEnabledTrafficReportNotifications()
	if err != nil {
		slog.Error("failed to load traffic report notifications", "error", err)
		return
	}
	if len(notifications) == 0 {
		return
	}

	allClients, err := clients.GetAllClientBasicInfo()
	if err != nil {
		slog.Error("failed to load clients for traffic report notifications", "error", err)
		return
	}
	clientsByUUID := make(map[string]models.Client, len(allClients))
	for _, client := range allClients {
		clientsByUUID[client.UUID] = client
	}

	type pendingReport struct {
		client models.Client
	}
	pendingByCadence := make(map[trafficReportCadence][]pendingReport, len(trafficReportCadenceOrder))
	for _, notification := range notifications {
		client, ok := clientsByUUID[notification.Client]
		if !ok {
			continue
		}
		for _, cadence := range dueTrafficReportCadences(notification, localNow) {
			pendingByCadence[cadence] = append(pendingByCadence[cadence], pendingReport{client: client})
		}
	}

	for _, cadence := range trafficReportCadenceOrder {
		pending := pendingByCadence[cadence]
		if len(pending) == 0 {
			continue
		}
		start, end := trafficReportWindow(cadence, localNow, loc)
		clientIDs := make([]string, 0, len(pending))
		for _, report := range pending {
			clientIDs = append(clientIDs, report.client.UUID)
		}
		statsResult, err := recordsdb.StreamTrafficStats(context.Background(), dbcore.GetDBInstance(), clientIDs, start, end, 0, false)
		if err != nil {
			slog.Error("failed to query traffic reports", "cadence", cadence, "error", err)
			continue
		}
		sentClients := make([]string, 0, len(pending))
		for _, report := range pending {
			stats, ok := statsResult.ByClient[report.client.UUID]
			if !ok {
				stats = recordsdb.EmptyTrafficStats(start, end, 0, false)
			}
			if err := sendTrafficReportWithStats(report.client, cadence, localNow, loc, start, end, stats); err != nil {
				slog.Error("failed to send traffic report", "client", report.client.UUID, "cadence", cadence, "error", err)
				continue
			}
			sentClients = append(sentClients, report.client.UUID)
		}
		if err := markTrafficReportsNotified(sentClients, cadence, localNow); err != nil {
			slog.Error("failed to mark traffic report notifications", "cadence", cadence, "error", err)
		}
	}
}

func trafficReportNotificationEnabled() bool {
	enabled, err := config.GetAs[bool](config.NotificationEnabledKey, false)
	if err != nil {
		slog.Error("failed to read notification enabled config", "error", err)
		return false
	}
	return enabled
}

func getEnabledTrafficReportNotifications() ([]models.TrafficReportNotification, error) {
	db := dbcore.GetDBInstance()
	var notifications []models.TrafficReportNotification
	err := db.
		Model(&models.TrafficReportNotification{}).
		Where("enable = ?", true).
		Order("client ASC").
		Find(&notifications).Error
	return notifications, err
}

func dueTrafficReportCadences(notification models.TrafficReportNotification, now time.Time) []trafficReportCadence {
	loc := notificationReportLocation()
	sendHour := notificationReportSendHour()
	if !notification.Enable || now.In(loc).Hour() < sendHour {
		return nil
	}

	due := make([]trafficReportCadence, 0, len(trafficReportCadenceOrder))
	for _, cadence := range trafficReportCadenceOrder {
		switch cadence {
		case trafficReportDaily:
			if notification.Daily && trafficReportCadenceDue(notification.LastDailyNotified.ToTime(), cadence, now) {
				due = append(due, cadence)
			}
		case trafficReportWeekly:
			if notification.Weekly && trafficReportCadenceDue(notification.LastWeeklyNotified.ToTime(), cadence, now) {
				due = append(due, cadence)
			}
		case trafficReportMonthly:
			if notification.Monthly && trafficReportCadenceDue(notification.LastMonthlyNotified.ToTime(), cadence, now) {
				due = append(due, cadence)
			}
		}
	}
	return due
}

func trafficReportCadenceDue(lastNotified time.Time, cadence trafficReportCadence, now time.Time) bool {
	loc := notificationReportLocation()
	sendHour := notificationReportSendHour()
	localNow := now.In(loc)
	if localNow.Hour() < sendHour {
		return false
	}
	if lastNotified.IsZero() {
		return cadence == trafficReportDaily ||
			(cadence == trafficReportWeekly && localNow.Weekday() == time.Monday) ||
			(cadence == trafficReportMonthly && localNow.Day() == 1)
	}

	localLast := lastNotified.In(loc)
	switch cadence {
	case trafficReportDaily:
		return !sameLocalDay(localLast, localNow)
	case trafficReportWeekly:
		lastYear, lastWeek := localLast.ISOWeek()
		nowYear, nowWeek := localNow.ISOWeek()
		return localNow.Weekday() == time.Monday && (lastYear != nowYear || lastWeek != nowWeek)
	case trafficReportMonthly:
		return localNow.Day() == 1 && (localLast.Year() != localNow.Year() || localLast.Month() != localNow.Month())
	default:
		return false
	}
}

func sameLocalDay(left, right time.Time) bool {
	return left.Year() == right.Year() && left.Month() == right.Month() && left.Day() == right.Day()
}

func sendTrafficReport(client models.Client, cadence trafficReportCadence, now time.Time, loc *time.Location) error {
	start, end := trafficReportWindow(cadence, now, loc)
	result, err := recordsdb.StreamTrafficStats(context.Background(), dbcore.GetDBInstance(), []string{client.UUID}, start, end, 0, false)
	if err != nil {
		return err
	}
	stats, ok := result.ByClient[client.UUID]
	if !ok {
		stats = recordsdb.EmptyTrafficStats(start, end, 0, false)
	}
	return sendTrafficReportWithStats(client, cadence, now, loc, start, end, stats)
}

func sendTrafficReportWithStats(client models.Client, cadence trafficReportCadence, now time.Time, loc *time.Location, start, end time.Time, stats recordsdb.TrafficStats) error {
	message := buildTrafficReportMessage(client, cadence, start, end, stats, loc)
	return messageSender.SendEvent(models.EventMessage{
		Event:    messageevent.TrafficReport,
		Clients:  []models.Client{client},
		Time:     now,
		Emoji:    "📊",
		Message:  message,
		Timezone: loc.String(),
	})
}

func trafficReportWindow(cadence trafficReportCadence, now time.Time, loc *time.Location) (time.Time, time.Time) {
	localNow := now.In(loc)
	switch cadence {
	case trafficReportWeekly:
		end := startOfLocalWeek(localNow, loc)
		return end.AddDate(0, 0, -7), end
	case trafficReportMonthly:
		end := startOfLocalMonth(localNow, loc)
		return end.AddDate(0, -1, 0), end
	case trafficReportDaily:
		fallthrough
	default:
		end := startOfLocalDay(localNow, loc)
		return end.AddDate(0, 0, -1), end
	}
}

func startOfLocalDay(t time.Time, loc *time.Location) time.Time {
	local := t.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
}

func startOfLocalWeek(t time.Time, loc *time.Location) time.Time {
	day := startOfLocalDay(t, loc)
	offset := (int(day.Weekday()) + 6) % 7
	return day.AddDate(0, 0, -offset)
}

func startOfLocalMonth(t time.Time, loc *time.Location) time.Time {
	local := t.In(loc)
	return time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, local.Location())
}

func markTrafficReportNotified(client string, cadence trafficReportCadence, notifiedAt time.Time) error {
	return markTrafficReportsNotified([]string{client}, cadence, notifiedAt)
}

func markTrafficReportsNotified(clientIDs []string, cadence trafficReportCadence, notifiedAt time.Time) error {
	if len(clientIDs) == 0 {
		return nil
	}
	column := ""
	switch cadence {
	case trafficReportDaily:
		column = "last_daily_notified"
	case trafficReportWeekly:
		column = "last_weekly_notified"
	case trafficReportMonthly:
		column = "last_monthly_notified"
	default:
		return nil
	}
	return dbcore.GetDBInstance().
		Model(&models.TrafficReportNotification{}).
		Where("client IN ?", clientIDs).
		Update(column, models.FromTime(notifiedAt)).Error
}

func buildTrafficReportMessage(client models.Client, cadence trafficReportCadence, start, end time.Time, stats recordsdb.TrafficStats, loc *time.Location) string {
	lines := []string{
		"流量报告",
		"周期: " + trafficReportCadenceLabel(cadence),
		"时区: " + loc.String(),
		"时间范围: " + formatTrafficReportTime(start, loc) + " - " + formatTrafficReportTime(end, loc),
		"上行: " + humanBytes(stats.Up),
		"下行: " + humanBytes(stats.Down),
		"总计: " + humanBytes(stats.Total),
		"平均速率: " + humanBytesPerSecond(stats.AvgBps),
		"峰值速率: " + humanBytesPerSecond(stats.PeakBps),
		"样本数: " + fmt.Sprintf("%d", stats.Samples),
		"覆盖率: " + formatPercent(stats.Coverage*100),
		"数据质量: " + trafficReportQualityLabel(stats.Quality),
	}

	if stats.Resets > 0 {
		lines = append(lines, "计数器重置: "+fmt.Sprintf("%d 次", stats.Resets))
	}
	if stats.FirstSample != "" && stats.LastSample != "" {
		lines = append(lines, "样本范围: "+formatTrafficReportTimeString(stats.FirstSample, loc)+" - "+formatTrafficReportTimeString(stats.LastSample, loc))
	}

	if client.TrafficLimit > 0 {
		used := computeUsedByType(strings.ToLower(client.TrafficLimitType), stats.Up, stats.Down)
		remaining := client.TrafficLimit - used
		if remaining < 0 {
			remaining = 0
		}
		usagePercent := float64(used) / float64(client.TrafficLimit) * 100
		lines = append(lines,
			"额度类型: "+trafficTypeLabelForReport(client.TrafficLimitType),
			"额度用量: "+humanBytes(used)+" / "+humanBytes(client.TrafficLimit)+" ("+formatPercent(usagePercent)+")",
			"剩余额度: "+humanBytes(remaining),
		)
	}

	return strings.Join(lines, "\n")
}

func trafficReportCadenceLabel(cadence trafficReportCadence) string {
	switch cadence {
	case trafficReportWeekly:
		return "周报"
	case trafficReportMonthly:
		return "月报"
	case trafficReportDaily:
		fallthrough
	default:
		return "日报"
	}
}

func trafficReportQualityLabel(quality string) string {
	switch quality {
	case "exact":
		return "完整"
	case "partial":
		return "部分覆盖"
	case "estimated":
		return "估算"
	case "empty":
		return "无样本"
	default:
		return "未知"
	}
}

func trafficTypeLabelForReport(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "up":
		return "上行"
	case "down":
		return "下行"
	case "sum":
		return "上行+下行"
	case "min":
		return "上/下行较小值"
	case "max":
		fallthrough
	default:
		return "上/下行较大值"
	}
}

func formatTrafficReportTime(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("2006-01-02 15:04:05")
}

func formatTrafficReportTimeString(value string, loc *time.Location) string {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return formatTrafficReportTime(parsed, loc)
}

func formatPercent(value float64) string {
	if value < 0 {
		value = 0
	}
	if value > 100 && value < 100.05 {
		value = 100
	}
	return fmt.Sprintf("%.1f%%", value)
}

func humanBytesPerSecond(value float64) string {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return "0 B/s"
	}
	units := []string{"B/s", "KB/s", "MB/s", "GB/s", "TB/s"}
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	digits := 2
	if value >= 100 || unit == 0 {
		digits = 0
	} else if value >= 10 {
		digits = 1
	}
	return fmt.Sprintf("%.*f %s", digits, value, units[unit])
}
