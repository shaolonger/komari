package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestExtractClientTokenAcceptsBearerHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodGet, "/api/clients/report", nil)
	request.Header.Set("Authorization", "Bearer agent-token")
	context.Request = request

	if got := ExtractClientToken(context); got != "agent-token" {
		t.Fatalf("ExtractClientToken() = %q, want %q", got, "agent-token")
	}
}

func TestExtractClientTokenRejectsQueryToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/clients/report?token=query-token", nil)

	if got := ExtractClientToken(context); got != "" {
		t.Fatalf("ExtractClientToken() = %q, want empty string for query token", got)
	}
}

func TestIdentityMiddlewareRequiresHeaderClientToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	originalLookup := clientTokenLookup
	clientTokenLookup = func(token string) (string, error) {
		if token == "agent-token" {
			return "client-uuid", nil
		}
		return "", nil
	}
	t.Cleanup(func() {
		clientTokenLookup = originalLookup
	})

	router := gin.New()
	router.Use(IdentityMiddleware())
	authorized := router.Group("/api/clients", RequireRole(RoleClient))
	authorized.GET("/report", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	headerRequest := httptest.NewRequest(http.MethodGet, "/api/clients/report", nil)
	headerRequest.Header.Set("Authorization", "Bearer agent-token")
	headerRecorder := httptest.NewRecorder()
	router.ServeHTTP(headerRecorder, headerRequest)
	if headerRecorder.Code != http.StatusOK {
		t.Fatalf("header-auth status = %d, want %d", headerRecorder.Code, http.StatusOK)
	}

	queryRequest := httptest.NewRequest(http.MethodGet, "/api/clients/report?token=agent-token", nil)
	queryRecorder := httptest.NewRecorder()
	router.ServeHTTP(queryRecorder, queryRequest)
	if queryRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("query-auth status = %d, want %d", queryRecorder.Code, http.StatusUnauthorized)
	}
}