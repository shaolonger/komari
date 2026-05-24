package utils

import (
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/config"
)

// https://github.com/labstack/echo/blob/98ca08e7dd64075b858e758d6693bf9799340756/context.go#L275-L294
func GetScheme(c *gin.Context) string {
	// Can't use `r.Request.URL.Scheme`
	// See: https://groups.google.com/forum/#!topic/golang-nuts/pMUkBlQBDF0
	if c.Request.TLS != nil {
		return "https"
	}
	if scheme := c.Request.Header.Get("X-Forwarded-Proto"); scheme != "" {
		return scheme
	}
	if scheme := c.Request.Header.Get("X-Forwarded-Protocol"); scheme != "" {
		return scheme
	}
	if ssl := c.Request.Header.Get("X-Forwarded-Ssl"); ssl == "on" {
		return "https"
	}
	if scheme := c.Request.Header.Get("X-Url-Scheme"); scheme != "" {
		return scheme
	}
	return "http"
}

func GetCallbackURL(c *gin.Context) string {
	scheme := GetScheme(c)
	host := c.Request.Host
	return scheme + "://" + host + "/api/oauth_callback"
}

func IsRequestSecure(c *gin.Context) bool {
	return GetScheme(c) == "https"
}

// EnforceHTTPSMiddleware enforces HTTPS redirection if enabled via config or env (P0-02)
func EnforceHTTPSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		enforceHTTPS, _ := config.GetAs[bool]("enforce_https", false)
		if !enforceHTTPS {
			if os.Getenv("KOMARI_ENFORCE_HTTPS") == "true" {
				enforceHTTPS = true
			}
		}

		if enforceHTTPS && !IsRequestSecure(c) {
			targetURL := "https://" + c.Request.Host + c.Request.URL.RequestURI()
			c.Redirect(http.StatusMovedPermanently, targetURL)
			c.Abort()
			return
		}
		c.Next()
	}
}
