package notification

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/dbcore"
)

func TestGetFleetReportNotificationReturnsDefault(t *testing.T) {
	clearFleetReportNotifications(t)

	router := fleetReportTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/notification/fleet-report/", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Status string                         `json:"status"`
		Data   fleetReportNotificationPayload `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("response is not valid JSON: %v; body = %s", err, rec.Body.String())
	}
	if response.Data.Enable || !response.Data.Daily || !response.Data.Weekly || !response.Data.Monthly || response.Data.TopN != 5 {
		t.Fatalf("unexpected default fleet report notification: %+v", response.Data)
	}
	if response.Data.Timezone != "UTC" || response.Data.SendHour != 9 {
		t.Fatalf("unexpected default time settings: %+v", response.Data)
	}
}

func TestEditFleetReportNotificationUpsertsSettings(t *testing.T) {
	clearFleetReportNotifications(t)

	router := fleetReportTestRouter()
	body := []byte(`{"enable":true,"daily":true,"weekly":false,"monthly":true,"top_n":99,"timezone":"Asia/Shanghai","send_hour":8}`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/notification/fleet-report/edit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Status string                         `json:"status"`
		Data   fleetReportNotificationPayload `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("response is not valid JSON: %v; body = %s", err, rec.Body.String())
	}
	if !response.Data.Enable || !response.Data.Daily || response.Data.Weekly || !response.Data.Monthly || response.Data.TopN != 20 {
		t.Fatalf("fleet report notification not normalized correctly: %+v", response.Data)
	}
	if response.Data.Timezone != "Asia/Shanghai" || response.Data.SendHour != 8 {
		t.Fatalf("fleet report time settings not saved correctly: %+v", response.Data)
	}
}

func TestEditFleetReportNotificationRejectsEnabledWithoutCadence(t *testing.T) {
	clearFleetReportNotifications(t)

	router := fleetReportTestRouter()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/admin/notification/fleet-report/edit",
		bytes.NewReader([]byte(`{"enable":true,"daily":false,"weekly":false,"monthly":false}`)),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
}

func TestEditFleetReportNotificationRejectsInvalidTimezone(t *testing.T) {
	clearFleetReportNotifications(t)

	router := fleetReportTestRouter()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/admin/notification/fleet-report/edit",
		bytes.NewReader([]byte(`{"enable":true,"daily":true,"timezone":"Mars/Olympus","send_hour":9}`)),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
}

func TestFleetReportNotificationTestRejectsDisabledChannel(t *testing.T) {
	clearFleetReportNotifications(t)

	router := fleetReportTestRouter()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/admin/notification/fleet-report/test",
		bytes.NewReader([]byte(`{"cadence":"daily"}`)),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "notification channel is disabled") {
		t.Fatalf("body = %s, want disabled channel error", rec.Body.String())
	}
}

func TestFleetReportNotificationTestRejectsInvalidCadence(t *testing.T) {
	clearFleetReportNotifications(t)

	router := fleetReportTestRouter()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/admin/notification/fleet-report/test",
		bytes.NewReader([]byte(`{"cadence":"yearly"}`)),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid report cadence") {
		t.Fatalf("body = %s, want invalid cadence error", rec.Body.String())
	}
}

func fleetReportTestRouter() *gin.Engine {
	router := gin.New()
	router.GET("/api/admin/notification/fleet-report", GetFleetReportNotification)
	router.GET("/api/admin/notification/fleet-report/", GetFleetReportNotification)
	router.POST("/api/admin/notification/fleet-report/edit", EditFleetReportNotification)
	router.POST("/api/admin/notification/fleet-report/test", TestFleetReportNotification)
	return router
}

func clearFleetReportNotifications(t *testing.T) {
	t.Helper()
	db := dbcore.GetDBInstance()
	if err := db.Exec("DELETE FROM fleet_report_notifications").Error; err != nil {
		t.Fatalf("failed to clear fleet report notifications: %v", err)
	}
	if err := db.Exec("DELETE FROM configs WHERE key IN ('notification_timezone', 'notification_report_send_hour')").Error; err != nil {
		t.Fatalf("failed to clear fleet report config: %v", err)
	}
	if err := db.Exec("DELETE FROM configs WHERE key IN ('notification_enabled', 'notification_method')").Error; err != nil {
		t.Fatalf("failed to clear notification channel config: %v", err)
	}
}
