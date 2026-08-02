package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
)

func diagnosticsRouter(enabled, authenticated bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	if authenticated {
		router.Use(func(c *gin.Context) {
			c.Set("role", api.RoleAdmin)
			c.Next()
		})
	}
	group := router.Group("/api/admin", api.RequireRole(api.RoleAdmin))
	RegisterDiagnostics(group, enabled)
	return router
}

func TestDiagnosticsRequireAdmin(t *testing.T) {
	for _, path := range []string{"/api/admin/metrics", "/api/admin/debug/pprof/", "/api/admin/debug/pprof/heap"} {
		recorder := httptest.NewRecorder()
		diagnosticsRouter(true, false).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want 401", path, recorder.Code)
		}
	}
}

func TestRuntimeDiagnosticsDefaultOff(t *testing.T) {
	recorder := httptest.NewRecorder()
	diagnosticsRouter(false, true).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/admin/debug/pprof/heap", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

func TestMetricsAuthorizedAndNoStore(t *testing.T) {
	recorder := httptest.NewRecorder()
	diagnosticsRouter(false, true).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/admin/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("metrics response must not be cached")
	}
	if !strings.Contains(recorder.Body.String(), "komari_reports_accepted_total") {
		t.Fatal("metrics body missing report counter")
	}
}
