package notification

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/cmd/flags"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
)

func TestMain(m *testing.M) {
	tempDir, err := os.MkdirTemp("", "komari-admin-notification-tests-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tempDir)

	flags.DatabaseType = "sqlite"
	flags.DatabaseFile = filepath.Join(tempDir, "notification-test.db")
	gin.SetMode(gin.TestMode)

	code := m.Run()
	_ = os.RemoveAll(tempDir)
	os.Exit(code)
}

func TestListTrafficReportNotificationsReturnsJSON(t *testing.T) {
	clearTrafficReportNotifications(t)

	router := trafficReportTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/notification/traffic-report/", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "<!DOCTYPE") {
		t.Fatalf("expected JSON response, got HTML: %s", rec.Body.String())
	}

	var response struct {
		Status string                             `json:"status"`
		Data   []models.TrafficReportNotification `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("response is not valid JSON: %v; body = %s", err, rec.Body.String())
	}
	if response.Status != "success" {
		t.Fatalf("status = %q, want success", response.Status)
	}
}

func TestEditTrafficReportNotificationsUpsertsSettings(t *testing.T) {
	clearTrafficReportNotifications(t)
	db := dbcore.GetDBInstance()
	for _, client := range []models.Client{
		{UUID: "node-1", Token: "traffic-report-token-node-1", Name: "node-1"},
		{UUID: "node-2", Token: "traffic-report-token-node-2", Name: "node-2"},
	} {
		if err := db.Create(&client).Error; err != nil {
			t.Fatalf("create traffic report client %s: %v", client.UUID, err)
		}
	}

	router := trafficReportTestRouter()
	body := []byte(`[
		{"client":"node-1","enable":true,"daily":true,"weekly":false,"monthly":true},
		{"client":"node-2","enable":false,"daily":false,"weekly":false,"monthly":false}
	]`)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/notification/traffic-report/edit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/admin/notification/traffic-report/", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var response struct {
		Status string                             `json:"status"`
		Data   []models.TrafficReportNotification `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("response is not valid JSON: %v; body = %s", err, rec.Body.String())
	}
	if len(response.Data) != 2 {
		t.Fatalf("saved notifications = %d, want 2: %+v", len(response.Data), response.Data)
	}
	if response.Data[0].Client != "node-1" || !response.Data[0].Enable || !response.Data[0].Daily || !response.Data[0].Monthly {
		t.Fatalf("node-1 notification not saved correctly: %+v", response.Data[0])
	}
	if response.Data[1].Client != "node-2" || response.Data[1].Enable {
		t.Fatalf("node-2 notification not saved correctly: %+v", response.Data[1])
	}
}

func TestEditTrafficReportNotificationsRejectsEnabledWithoutCadence(t *testing.T) {
	clearTrafficReportNotifications(t)

	router := trafficReportTestRouter()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/admin/notification/traffic-report/edit",
		bytes.NewReader([]byte(`[{"client":"node-1","enable":true}]`)),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
}

func trafficReportTestRouter() *gin.Engine {
	router := gin.New()
	router.GET("/api/admin/notification/traffic-report", ListTrafficReportNotifications)
	router.GET("/api/admin/notification/traffic-report/", ListTrafficReportNotifications)
	router.POST("/api/admin/notification/traffic-report/edit", EditTrafficReportNotifications)
	return router
}

func clearTrafficReportNotifications(t *testing.T) {
	t.Helper()
	db := dbcore.GetDBInstance()
	if err := db.Exec("DELETE FROM traffic_report_notifications").Error; err != nil {
		t.Fatalf("failed to clear traffic report notifications: %v", err)
	}
	if err := db.Where("uuid IN ?", []string{"node-1", "node-2"}).Delete(&models.Client{}).Error; err != nil {
		t.Fatalf("failed to clear traffic report clients: %v", err)
	}
}
