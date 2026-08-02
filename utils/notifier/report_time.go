package notifier

import (
	"fmt"
	"regexp"
	"strconv"
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
	return LoadNotificationLocation(timezone)
}

func LoadNotificationLocation(timezone string) (*time.Location, error) {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		timezone = defaultNotificationTimezone
	}
	if loc, ok, err := parseUTCOffsetLocation(timezone); ok || err != nil {
		return loc, err
	}
	return time.LoadLocation(timezone)
}

var utcOffsetPattern = regexp.MustCompile(`(?i)^(UTC|GMT)([+-])(\d{1,2})(?::?(\d{2}))?$`)

func parseUTCOffsetLocation(value string) (*time.Location, bool, error) {
	value = strings.TrimSpace(value)
	matches := utcOffsetPattern.FindStringSubmatch(value)
	if matches == nil {
		return nil, false, nil
	}

	hours, err := strconv.Atoi(matches[3])
	if err != nil {
		return nil, true, err
	}
	minutes := 0
	if matches[4] != "" {
		minutes, err = strconv.Atoi(matches[4])
		if err != nil {
			return nil, true, err
		}
	}
	if hours > 14 || minutes > 59 || (hours == 14 && minutes != 0) {
		return nil, true, fmt.Errorf("UTC offset out of range: %s", value)
	}

	offset := hours*3600 + minutes*60
	if matches[2] == "-" {
		offset = -offset
	}
	return time.FixedZone(normalizeUTCOffsetName(matches[2], hours, minutes), offset), true, nil
}

func normalizeUTCOffsetName(sign string, hours, minutes int) string {
	if minutes == 0 {
		return fmt.Sprintf("UTC%s%d", sign, hours)
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, hours, minutes)
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
