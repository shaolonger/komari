package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/clients"
)

func TestUpdateClientHomeFacetsNormalizesAndSaves(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalSave := saveClientHomeFacetsFunc
	originalAudit := auditLogFunc
	defer func() {
		saveClientHomeFacetsFunc = originalSave
		auditLogFunc = originalAudit
	}()

	var savedUUID string
	var savedFacets clients.HomeFacetValues
	saveClientHomeFacetsFunc = func(uuid string, facets clients.HomeFacetValues) error {
		savedUUID = uuid
		savedFacets = facets
		return nil
	}
	auditLogFunc = func(ip, uuid, message, msgType string) {}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "uuid", Value: "node-1"}}
	context.Set("uuid", "admin-uuid")
	request := httptest.NewRequest(http.MethodPost, "/api/admin/client/node-1/facets", strings.NewReader(`{
		"facets":{"line":"CMI; CN2; CMI","provider":["DMIT"]}
	}`))
	request.Header.Set("Content-Type", "application/json")
	context.Request = request

	UpdateClientHomeFacets(context)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if savedUUID != "node-1" {
		t.Fatalf("savedUUID = %q, want node-1", savedUUID)
	}
	if got := savedFacets["line"]; len(got) != 2 || got[0] != "CMI" || got[1] != "CN2" {
		t.Fatalf("saved line facets = %#v", got)
	}
}

func TestUpdateClientHomeFacetsRejectsInvalidPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Params = gin.Params{{Key: "uuid", Value: "node-1"}}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/client/node-1/facets", strings.NewReader(`{
		"facets":{"bad id":"DMIT"}
	}`))
	request.Header.Set("Content-Type", "application/json")
	context.Request = request

	UpdateClientHomeFacets(context)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestBatchUpdateClientHomeFacetsNormalizesAndSaves(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalBatchSave := batchSaveClientHomeFacetsFunc
	originalAudit := auditLogFunc
	defer func() {
		batchSaveClientHomeFacetsFunc = originalBatchSave
		auditLogFunc = originalAudit
	}()

	var saved map[string]clients.HomeFacetValues
	batchSaveClientHomeFacetsFunc = func(items map[string]clients.HomeFacetValues) (int, error) {
		saved = items
		return len(items), nil
	}
	auditLogFunc = func(ip, uuid, message, msgType string) {}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("uuid", "admin-uuid")
	request := httptest.NewRequest(http.MethodPost, "/api/admin/client/facets", strings.NewReader(`{
		"facets":{
			"node-1":{"provider":"DMIT"},
			"node-2":{"line":["CMI","CN2","CMI"]}
		}
	}`))
	request.Header.Set("Content-Type", "application/json")
	context.Request = request

	BatchUpdateClientHomeFacets(context)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if len(saved) != 2 {
		t.Fatalf("len(saved) = %d, want 2", len(saved))
	}
	if got := saved["node-2"]["line"]; len(got) != 2 || got[0] != "CMI" || got[1] != "CN2" {
		t.Fatalf("saved node-2 line facets = %#v", got)
	}
}

func TestListClientHomeFacetsUsesRowsProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalList := getAllClientHomeFacetRowsFunc
	defer func() {
		getAllClientHomeFacetRowsFunc = originalList
	}()

	getAllClientHomeFacetRowsFunc = func() ([]clients.HomeFacetClientRow, error) {
		return []clients.HomeFacetClientRow{
			{
				UUID:       "node-1",
				Name:       "Tokyo",
				HomeFacets: clients.HomeFacetValues{"line": {"CMI"}},
			},
		}, nil
	}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/admin/client/facets", nil)

	ListClientHomeFacets(context)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !strings.Contains(recorder.Body.String(), `"uuid":"node-1"`) {
		t.Fatalf("response body = %s", recorder.Body.String())
	}
}
