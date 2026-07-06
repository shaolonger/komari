package notifier

import (
	"strings"
	"time"

	"github.com/komari-monitor/komari/config"
)

const (
	defaultNotificationTimezone       = "UTC"
	defaultNotificationReportSendHour = 9
)

func notificationReportLocation() *time.Location {
	if !config.Ready() {
		return time.UTC
	}
	timezone, err := config.GetAs[string](config.NotificationTimezoneKey, defaultNotificationTimezone)
	if err != nil {
		return time.UTC
	}
	loc, err := loadNotificationLocation(timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

func notificationReportSendHour() int {
	if !config.Ready() {
		return defaultNotificationReportSendHour
	}
	hour, err := config.GetAs[int](config.NotificationReportSendHourKey, defaultNotificationReportSendHour)
	if err != nil {
		return defaultNotificationReportSendHour
	}
	return normalizeNotificationReportSendHour(hour)
}

func loadNotificationLocation(timezone string) (*time.Location, error) {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		timezone = defaultNotificationTimezone
	}
	return time.LoadLocation(timezone)
}

func normalizeNotificationReportSendHour(hour int) int {
	if hour < 0 {
		return 0
	}
	if hour > 23 {
		return 23
	}
	return hour
}
