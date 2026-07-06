package notifier

import (
	"log/slog"
	"time"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/utils/messageSender"
	"gorm.io/gorm"
)

const fleetReportNotificationID uint = 1

func CheckFleetReportScheduledWork() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		CheckFleetReportOnce(time.Now())
		<-ticker.C
	}
}

func CheckFleetReportOnce(now time.Time) {
	loc := notificationReportLocation()
	localNow := now.In(loc)
	if localNow.Hour() < notificationReportSendHour() {
		return
	}
	if !trafficReportNotificationEnabled() {
		return
	}

	notification, ok, err := getFleetReportNotification()
	if err != nil {
		slog.Error("failed to load fleet report notification", "error", err)
		return
	}
	if !ok || !notification.Enable {
		return
	}

	for _, cadence := range dueFleetReportCadences(notification, localNow) {
		data, message, reportClients, err := buildFleetOperationsReport(cadence, localNow, loc, notification.TopN)
		if err != nil {
			slog.Error("failed to build fleet report", "cadence", cadence, "error", err)
			continue
		}
		event := buildFleetReportEvent(data, message, reportClients, localNow, loc)
		if err := messageSender.SendEvent(event); err != nil {
			slog.Error("failed to send fleet report", "cadence", cadence, "error", err)
			continue
		}
		if err := markFleetReportNotified(cadence, localNow); err != nil {
			slog.Error("failed to mark fleet report notification", "cadence", cadence, "error", err)
		}
	}
}

func getFleetReportNotification() (models.FleetReportNotification, bool, error) {
	db := dbcore.GetDBInstance()
	var notification models.FleetReportNotification
	err := db.Where("id = ?", fleetReportNotificationID).First(&notification).Error
	if err == nil {
		return notification, true, nil
	}
	if err == gorm.ErrRecordNotFound {
		return models.FleetReportNotification{}, false, nil
	}
	return models.FleetReportNotification{}, false, err
}

func dueFleetReportCadences(notification models.FleetReportNotification, now time.Time) []trafficReportCadence {
	loc := notificationReportLocation()
	if !notification.Enable || now.In(loc).Hour() < notificationReportSendHour() {
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

func markFleetReportNotified(cadence trafficReportCadence, notifiedAt time.Time) error {
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
		Model(&models.FleetReportNotification{}).
		Where("id = ?", fleetReportNotificationID).
		Update(column, models.FromTime(notifiedAt)).Error
}
