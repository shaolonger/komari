package api

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/telemetry"
	"gorm.io/gorm"
)

var (
	Telemetry         = telemetry.NewStore()
	telemetryFlushMu  sync.Mutex
	pendingRecords    []models.Record
	pendingGPURecords []models.GPURecord
)

func SaveClientReportToDB() error {
	telemetryFlushMu.Lock()
	defer telemetryFlushMu.Unlock()
	if len(pendingRecords) == 0 && len(pendingGPURecords) == 0 {
		aggregates := Telemetry.DrainBefore(time.Now())
		pendingRecords = make([]models.Record, 0, len(aggregates))
		for _, aggregate := range aggregates {
			pendingRecords = append(pendingRecords, aggregate.Record)
			pendingGPURecords = append(pendingGPURecords, aggregate.GPURecords...)
		}
	}
	if len(pendingRecords) == 0 && len(pendingGPURecords) == 0 {
		return nil
	}

	db := dbcore.GetDBInstance()
	err := db.Transaction(func(tx *gorm.DB) error {
		if len(pendingRecords) > 0 {
			if err := tx.Model(&models.Record{}).Create(&pendingRecords).Error; err != nil {
				return err
			}
		}
		if len(pendingGPURecords) > 0 {
			if err := tx.Model(&models.GPURecord{}).Create(&pendingGPURecords).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("Failed to save telemetry batch to database: %v", err)
		return err
	}
	pendingRecords = nil
	pendingGPURecords = nil
	return nil
}

type Response struct {
	Status  string      `json:"status"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// Respond sends a standardized JSON response.
func Respond(c *gin.Context, httpStatus int, status string, message string, data interface{}) {
	c.JSON(httpStatus, Response{Status: status, Message: message, Data: data})
}

// RespondSuccess sends a success response with data.
func RespondSuccess(c *gin.Context, data interface{}) {
	Respond(c, http.StatusOK, "success", "", data)
}

// RespondSuccessMessage sends a success response with message and data.
func RespondSuccessMessage(c *gin.Context, message string, data interface{}) {
	Respond(c, http.StatusOK, "success", message, data)
}

// RespondError sends an error response with message.
func RespondError(c *gin.Context, httpStatus int, message string) {
	Respond(c, httpStatus, "error", message, nil)
}
