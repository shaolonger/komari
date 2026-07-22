package api

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/telemetrywriter"
	"github.com/komari-monitor/komari/internal/telemetry"
)

var (
	Telemetry         = telemetry.NewStore()
	telemetryFlushMu  sync.Mutex
	pendingRecords    []models.Record
	pendingGPURecords []models.GPURecord
)

func SaveClientReportToDB() error {
	return saveClientReportsBefore(time.Now())
}

// FlushClientReports drains the partial current minute during graceful shutdown.
func FlushClientReports() error {
	return saveClientReportsBefore(time.Now().Add(time.Minute))
}

func saveClientReportsBefore(cutoff time.Time) error {
	telemetryFlushMu.Lock()
	defer telemetryFlushMu.Unlock()
	if len(pendingRecords) == 0 && len(pendingGPURecords) == 0 {
		aggregates := Telemetry.DrainBefore(cutoff)
		pendingRecords = make([]models.Record, 0, len(aggregates))
		for _, aggregate := range aggregates {
			pendingRecords = append(pendingRecords, aggregate.Record)
			pendingGPURecords = append(pendingGPURecords, aggregate.GPURecords...)
		}
	}
	if len(pendingRecords) == 0 && len(pendingGPURecords) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := telemetrywriter.Submit(ctx, telemetrywriter.Batch{Records: pendingRecords, GPURecords: pendingGPURecords})
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
