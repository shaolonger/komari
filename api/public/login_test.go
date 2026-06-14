package public

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/accounts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	loginLimiter.Flush()
	_ = accounts.DeleteAccountByUsername("testuser")
	_ = accounts.DeleteAllSessions()

	_, err := accounts.CreateAccount("testuser", "correctpassword")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = accounts.DeleteAccountByUsername("testuser")
		_ = accounts.DeleteAllSessions()
		loginLimiter.Flush()
	})

	tests := []struct {
		name           string
		requestBody    LoginRequest
		expectedStatus int
		expectedBody   map[string]interface{}
		expectCookie   bool
	}{
		{
			name: "成功登录",
			requestBody: LoginRequest{
				Username: "testuser",
				Password: "correctpassword",
			},
			expectedStatus: http.StatusOK,
			expectedBody: map[string]interface{}{
				"status":  "success",
				"message": "",
				"data": map[string]interface{}{
					"status":  "success",
					"message": "logged in successfully",
				},
			},
			expectCookie: true,
		},
		{
			name: "无效的请求体",
			requestBody: LoginRequest{
				Username: "",
				Password: "",
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody: map[string]interface{}{
				"status":  "error",
				"message": "Invalid request body: Username and password are required",
			},
		},
		{
			name: "错误的凭据",
			requestBody: LoginRequest{
				Username: "wronguser",
				Password: "wrongpassword",
			},
			expectedStatus: http.StatusUnauthorized,
			expectedBody: map[string]interface{}{
				"status":  "error",
				"message": "Invalid credentials",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loginLimiter.Flush()

			router := gin.New()
			router.POST("/login", Login)

			jsonBody, _ := json.Marshal(tt.requestBody)
			req, _ := http.NewRequest("POST", "/login", bytes.NewBuffer(jsonBody))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			var response map[string]interface{}
			err := json.Unmarshal(w.Body.Bytes(), &response)
			assert.NoError(t, err)

			assert.Equal(t, tt.expectedBody, response)
			if tt.expectCookie {
				cookies := w.Result().Cookies()
				require.NotEmpty(t, cookies)
				found := false
				for _, cookie := range cookies {
					if cookie.Name == "session_token" && cookie.Value != "" {
						found = true
						break
					}
				}
				assert.True(t, found, "session_token cookie should be set on successful login")
			}
		})
	}
}
