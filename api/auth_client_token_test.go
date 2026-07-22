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

func TestCredentialMatchesUsesFixedDigestComparison(t *testing.T) {
	tests := []struct {
		name     string
		provided string
		expected string
		want     bool
	}{
		{name: "equal", provided: "0123456789abcdef", expected: "0123456789abcdef", want: true},
		{name: "different same length", provided: "0123456789abcdee", expected: "0123456789abcdef"},
		{name: "short", provided: "x", expected: "0123456789abcdef"},
		{name: "long", provided: "0123456789abcdef-extra", expected: "0123456789abcdef"},
		{name: "empty", provided: "", expected: "0123456789abcdef"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := credentialMatches(tt.provided, tt.expected); got != tt.want {
				t.Fatalf("credentialMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func BenchmarkCredentialMatches(b *testing.B) {
	for _, input := range []string{"x", "0123456789abcdee", "0123456789abcdef-extra"} {
		b.Run(input, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				credentialMatches(input, "0123456789abcdef")
			}
		})
	}
}
