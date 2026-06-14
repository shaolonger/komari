package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/models"
)

func TestRotateClientTokenPassesExpiryAndReturnsStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalRotate := rotateClientTokenFunc
	originalAudit := auditLogFunc
	defer func() {
		rotateClientTokenFunc = originalRotate
		auditLogFunc = originalAudit
	}()

	var gotUUID string
	var gotHours int64
	rotateClientTokenFunc = func(uuid string, expiresInHours int64) (clients.ClientTokenStatus, error) {
		gotUUID = uuid
		gotHours = expiresInHours
		return clients.ClientTokenStatus{
			Token:    "rotated-token",
			IssuedAt: models.FromTime(time.Date(2026, time.May, 26, 12, 0, 0, 0, time.UTC)),
			Active:   true,
		}, nil
	}
	auditLogFunc = func(ip, uuid, message, msgType string) {}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/api/admin/client/client-uuid/token/rotate", strings.NewReader(`{"expires_in_hours":24}`))
	request.Header.Set("Content-Type", "application/json")
	context.Request = request
	context.Params = gin.Params{{Key: "uuid", Value: "client-uuid"}}
	context.Set("uuid", "admin-uuid")

	RotateClientToken(context)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if gotUUID != "client-uuid" || gotHours != 24 {
		t.Fatalf("rotate args = (%q, %d), want (%q, %d)", gotUUID, gotHours, "client-uuid", 24)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if payload["token"] != "rotated-token" {
		t.Fatalf("token = %v, want %q", payload["token"], "rotated-token")
	}
	if payload["active"] != true {
		t.Fatalf("active = %v, want true", payload["active"])
	}
}

func TestRotateClientTokenRejectsNegativeExpiry(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/api/admin/client/client-uuid/token/rotate", strings.NewReader(`{"expires_in_hours":-1}`))
	request.Header.Set("Content-Type", "application/json")
	context.Request = request
	context.Params = gin.Params{{Key: "uuid", Value: "client-uuid"}}

	RotateClientToken(context)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestRevokeClientTokenReturnsStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalRevoke := revokeClientTokenFunc
	originalAudit := auditLogFunc
	defer func() {
		revokeClientTokenFunc = originalRevoke
		auditLogFunc = originalAudit
	}()

	revokeClientTokenFunc = func(uuid string) (clients.ClientTokenStatus, error) {
		if uuid != "client-uuid" {
			t.Fatalf("uuid = %q, want %q", uuid, "client-uuid")
		}
		return clients.ClientTokenStatus{
			Token:     "revoked-token",
			RevokedAt: models.FromTime(time.Date(2026, time.May, 26, 12, 0, 0, 0, time.UTC)),
			Active:    false,
		}, nil
	}
	auditLogFunc = func(ip, uuid, message, msgType string) {}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/admin/client/client-uuid/token/revoke", nil)
	context.Params = gin.Params{{Key: "uuid", Value: "client-uuid"}}
	context.Set("uuid", "admin-uuid")

	RevokeClientToken(context)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if payload["active"] != false {
		t.Fatalf("active = %v, want false", payload["active"])
	}
}

func TestBatchEditClientAssetsUpdatesUniqueUUIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalSave := saveClientFunc
	originalAudit := auditLogFunc
	defer func() {
		saveClientFunc = originalSave
		auditLogFunc = originalAudit
	}()

	var saved []map[string]interface{}
	saveClientFunc = func(update map[string]interface{}) error {
		copied := make(map[string]interface{}, len(update))
		for key, value := range update {
			copied[key] = value
		}
		saved = append(saved, copied)
		return nil
	}
	auditLogFunc = func(ip, uuid, message, msgType string) {}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/api/admin/client/batch-edit", strings.NewReader(`{
		"uuids":["node-1","node-2","node-1"],
		"changes":{"provider":"CloudSilk","asset_ignored":true}
	}`))
	request.Header.Set("Content-Type", "application/json")
	context.Request = request
	context.Set("uuid", "admin-uuid")

	BatchEditClientAssets(context)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if len(saved) != 2 {
		t.Fatalf("len(saved) = %d, want 2", len(saved))
	}
	if saved[0]["uuid"] != "node-1" || saved[1]["uuid"] != "node-2" {
		t.Fatalf("saved uuids = %+v", saved)
	}
}

func TestBatchEditClientAssetsRejectsUUIDMutation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/api/admin/client/batch-edit", strings.NewReader(`{
		"uuids":["node-1"],
		"changes":{"uuid":"other-node"}
	}`))
	request.Header.Set("Content-Type", "application/json")
	context.Request = request

	BatchEditClientAssets(context)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}
