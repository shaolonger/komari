package notification

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm"
)

const fleetReportNotificationID uint = 1

func GetFleetReportNotification(c *gin.Context) {
	notification, err := loadFleetReportNotification()
	if err != nil {
		api.RespondError(c, 500, "Failed to get fleet report notification: "+err.Error())
		return
	}
	api.RespondSuccess(c, notification)
}

func EditFleetReportNotification(c *gin.Context) {
	var notification models.FleetReportNotification
	if err := c.ShouldBindJSON(&notification); err != nil {
		api.RespondError(c, 400, "Invalid request body: "+err.Error())
		return
	}

	if err := normalizeFleetReportNotification(&notification); err != nil {
		api.RespondError(c, 400, err.Error())
		return
	}

	notification.Id = fleetReportNotificationID
	err := dbcore.GetDBInstance().Save(&notification).Error
	if err != nil {
		api.RespondError(c, 500, "Failed to edit fleet report notification: "+err.Error())
		return
	}
	api.RespondSuccess(c, notification)
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

func normalizeFleetReportNotification(notification *models.FleetReportNotification) error {
	if notification.Enable && !notification.Daily && !notification.Weekly && !notification.Monthly {
		return errors.New("at least one report cadence must be selected")
	}
	if notification.TopN <= 0 {
		notification.TopN = 5
	}
	if notification.TopN > 20 {
		notification.TopN = 20
	}
	return nil
}
