package notification

import (
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/config"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/utils/notifier"
	"gorm.io/gorm"
)

const fleetReportNotificationID uint = 1

type fleetReportNotificationPayload struct {
	Id                  uint             `json:"id,omitempty"`
	Enable              bool             `json:"enable"`
	Daily               bool             `json:"daily"`
	Weekly              bool             `json:"weekly"`
	Monthly             bool             `json:"monthly"`
	TopN                int              `json:"top_n"`
	Timezone            string           `json:"timezone"`
	SendHour            int              `json:"send_hour"`
	LastDailyNotified   models.LocalTime `json:"last_daily_notified"`
	LastWeeklyNotified  models.LocalTime `json:"last_weekly_notified"`
	LastMonthlyNotified models.LocalTime `json:"last_monthly_notified"`
}

type fleetReportTestRequest struct {
	Cadence string `json:"cadence"`
}

func GetFleetReportNotification(c *gin.Context) {
	notification, err := loadFleetReportNotification()
	if err != nil {
		api.RespondError(c, 500, "Failed to get fleet report notification: "+err.Error())
		return
	}
	payload, err := fleetReportNotificationPayloadFromModel(notification)
	if err != nil {
		api.RespondError(c, 500, "Failed to get fleet report settings: "+err.Error())
		return
	}
	api.RespondSuccess(c, payload)
}

func EditFleetReportNotification(c *gin.Context) {
	var payload fleetReportNotificationPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		api.RespondError(c, 400, "Invalid request body: "+err.Error())
		return
	}

	if err := normalizeFleetReportNotificationPayload(&payload); err != nil {
		api.RespondError(c, 400, err.Error())
		return
	}

	existing, err := loadFleetReportNotification()
	if err != nil {
		api.RespondError(c, 500, "Failed to get fleet report notification: "+err.Error())
		return
	}
	notification := existing
	notification.Id = fleetReportNotificationID
	notification.Enable = payload.Enable
	notification.Daily = payload.Daily
	notification.Weekly = payload.Weekly
	notification.Monthly = payload.Monthly
	notification.TopN = payload.TopN

	if err := config.Set(config.NotificationTimezoneKey, payload.Timezone); err != nil {
		api.RespondError(c, 500, "Failed to save notification timezone: "+err.Error())
		return
	}
	if err := config.Set(config.NotificationReportSendHourKey, payload.SendHour); err != nil {
		api.RespondError(c, 500, "Failed to save notification report send hour: "+err.Error())
		return
	}

	err = dbcore.GetDBInstance().Save(&notification).Error
	if err != nil {
		api.RespondError(c, 500, "Failed to edit fleet report notification: "+err.Error())
		return
	}
	response, err := fleetReportNotificationPayloadFromModel(notification)
	if err != nil {
		api.RespondError(c, 500, "Failed to get fleet report settings: "+err.Error())
		return
	}
	api.RespondSuccess(c, response)
}

func TestFleetReportNotification(c *gin.Context) {
	var request fleetReportTestRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		api.RespondError(c, 400, "Invalid request body: "+err.Error())
		return
	}

	report, err := notifier.SendFleetReportTest(request.Cadence, time.Now())
	if err != nil {
		api.RespondError(c, 500, "Failed to send fleet report test: "+err.Error())
		return
	}
	api.RespondSuccess(c, gin.H{
		"message": "Fleet report test sent successfully",
		"report":  report,
	})
}

func loadFleetReportNotification() (models.FleetReportNotification, error) {
	db := dbcore.GetDBInstance()
	var notification models.FleetReportNotification
	err := db.Model(&models.FleetReportNotification{}).
		Where("id = ?", fleetReportNotificationID).
		First(&notification).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		notification = defaultFleetReportNotification()
		return notification, nil
	}
	if err != nil {
		return models.FleetReportNotification{}, err
	}
	if notification.TopN <= 0 {
		notification.TopN = 5
	}
	return notification, nil
}

func defaultFleetReportNotification() models.FleetReportNotification {
	return models.FleetReportNotification{
		Id:      fleetReportNotificationID,
		Enable:  false,
		Daily:   true,
		Weekly:  true,
		Monthly: true,
		TopN:    5,
	}
}

func fleetReportNotificationPayloadFromModel(notification models.FleetReportNotification) (fleetReportNotificationPayload, error) {
	timezone, err := config.GetAs[string](config.NotificationTimezoneKey, "UTC")
	if err != nil {
		return fleetReportNotificationPayload{}, err
	}
	sendHour, err := config.GetAs[int](config.NotificationReportSendHourKey, 9)
	if err != nil {
		return fleetReportNotificationPayload{}, err
	}
	payload := fleetReportNotificationPayload{
		Id:                  notification.Id,
		Enable:              notification.Enable,
		Daily:               notification.Daily,
		Weekly:              notification.Weekly,
		Monthly:             notification.Monthly,
		TopN:                notification.TopN,
		Timezone:            strings.TrimSpace(timezone),
		SendHour:            normalizeFleetReportSendHour(sendHour),
		LastDailyNotified:   notification.LastDailyNotified,
		LastWeeklyNotified:  notification.LastWeeklyNotified,
		LastMonthlyNotified: notification.LastMonthlyNotified,
	}
	if payload.Timezone == "" {
		payload.Timezone = "UTC"
	}
	return payload, nil
}

func normalizeFleetReportNotificationPayload(payload *fleetReportNotificationPayload) error {
	if payload.Enable && !payload.Daily && !payload.Weekly && !payload.Monthly {
		return errors.New("at least one report cadence must be selected")
	}
	payload.Timezone = strings.TrimSpace(payload.Timezone)
	if payload.Timezone == "" {
		payload.Timezone = "UTC"
	}
	if _, err := notifier.LoadNotificationLocation(payload.Timezone); err != nil {
		return errors.New("invalid timezone: " + payload.Timezone)
	}
	payload.SendHour = normalizeFleetReportSendHour(payload.SendHour)
	if payload.TopN <= 0 {
		payload.TopN = 5
	}
	if payload.TopN > 20 {
		payload.TopN = 20
	}
	return nil
}

func normalizeFleetReportSendHour(hour int) int {
	if hour < 0 {
		return 0
	}
	if hour > 23 {
		return 23
	}
	return hour
}
