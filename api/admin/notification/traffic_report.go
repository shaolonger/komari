package notification

import (
	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm/clause"
)

func ListTrafficReportNotifications(c *gin.Context) {
	var notifications []models.TrafficReportNotification
	err := dbcore.GetDBInstance().
		Model(&models.TrafficReportNotification{}).
		Order("client ASC").
		Find(&notifications).Error
	if err != nil {
		api.RespondError(c, 500, "Failed to list traffic report notifications: "+err.Error())
		return
	}
	api.RespondSuccess(c, notifications)
}

func EditTrafficReportNotifications(c *gin.Context) {
	var notifications []models.TrafficReportNotification
	if err := c.ShouldBindJSON(&notifications); err != nil {
		api.RespondError(c, 400, "Invalid request body: "+err.Error())
		return
	}
	if len(notifications) == 0 {
		api.RespondError(c, 400, "At least one notification is required")
		return
	}
	for _, notification := range notifications {
		if notification.Client == "" {
			api.RespondError(c, 400, "Client UUID cannot be empty")
			return
		}
		if notification.Enable && !notification.Daily && !notification.Weekly && !notification.Monthly {
			api.RespondError(c, 400, "At least one report cadence must be selected")
			return
		}
	}

	err := dbcore.GetDBInstance().
		Model(&models.TrafficReportNotification{}).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "client"}},
			DoUpdates: clause.AssignmentColumns([]string{"enable", "daily", "weekly", "monthly"}),
		}).
		Select("client", "enable", "daily", "weekly", "monthly").
		Create(notifications).Error
	if err != nil {
		api.RespondError(c, 500, "Failed to edit traffic report notifications: "+err.Error())
		return
	}
	api.RespondSuccess(c, nil)
}
