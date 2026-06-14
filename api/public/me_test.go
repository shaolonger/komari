package public

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/accounts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetMe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_ = accounts.DeleteAccountByUsername("testuser")
	_ = accounts.DeleteAllSessions()

	user, err := accounts.CreateAccount("testuser", "password")
	require.NoError(t, err)
	uuid := user.UUID
	sessionToken, err := accounts.CreateSession(uuid, 2592000, "test_user_agent", "127.0.0.1", "oauth")
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = accounts.DeleteAccountByUsername("testuser")
		_ = accounts.DeleteAllSessions()
	})

	tests := []struct {
		name           string
		sessionToken   string
		expectedStatus int
		expectedBody   map[string]interface{}
	}{
		{
			name:           "有效的会话",
			sessionToken:   sessionToken,
			expectedStatus: http.StatusOK,
			expectedBody: map[string]interface{}{
				"username":    "testuser",
				"logged_in":   true,
				"uuid":        uuid,
				"sso_type":    "",
				"sso_id":      "",
				"2fa_enabled": false,
			},
		},
		{
			name:           "无效的会话",
			sessionToken:   "invalid_token",
			expectedStatus: http.StatusOK,
			expectedBody: map[string]interface{}{
				"username":  "Guest",
				"logged_in": false,
			},
		},
		{
			name:           "无会话",
			sessionToken:   "",
			expectedStatus: http.StatusOK,
			expectedBody: map[string]interface{}{
				"username":  "Guest",
				"logged_in": false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.GET("/me", GetMe)

			req, _ := http.NewRequest("GET", "/me", nil)
			if tt.sessionToken != "" {
				req.AddCookie(&http.Cookie{
					Name:  "session_token",
					Value: tt.sessionToken,
				})
			}

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			var response map[string]interface{}
			err := json.Unmarshal(w.Body.Bytes(), &response)
			assert.NoError(t, err)

			assert.Equal(t, tt.expectedBody, response)
		})
	}
}
