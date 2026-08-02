package log

import (
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
)

const DefaultTelemetrySuccessSampleRate uint64 = 256

type GinLogPolicy struct {
	TelemetrySuccessSampleRate uint64
}

func GinLogger() gin.HandlerFunc {
	return GinLoggerWithPolicy(GinLogPolicy{TelemetrySuccessSampleRate: DefaultTelemetrySuccessSampleRate})
}

// GinLoggerWithPolicy keeps all errors, security responses and ordinary API
// operations while deterministically sampling only successful high-frequency
// telemetry POSTs. Query values and request headers are never logged.
func GinLoggerWithPolicy(policy GinLogPolicy) gin.HandlerFunc {
	if policy.TelemetrySuccessSampleRate == 0 {
		policy.TelemetrySuccessSampleRate = DefaultTelemetrySuccessSampleRate
	}
	var telemetrySuccesses atomic.Uint64
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		status := c.Writer.Status()
		isTelemetry := isHighFrequencyAgentWrite(c.Request.Method, c.Request.URL.Path)
		if isTelemetry && status < 400 && len(c.Errors) == 0 {
			sequence := telemetrySuccesses.Add(1)
			if (sequence-1)%policy.TelemetrySuccessSampleRate != 0 {
				return
			}
		}

		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError
		case status >= 400:
			level = slog.LevelWarn
		}
		record := slog.NewRecord(time.Now(), level, "HTTP request", 0)
		record.AddAttrs(
			slog.String("_group", "GIN"),
			slog.String("method", sanitizeLogText(c.Request.Method, 16)),
			slog.String("path", sanitizeLogText(c.Request.URL.Path, 512)),
			slog.Int("status", status),
			slog.Duration("latency", time.Since(started)),
			slog.String("remote_ip", sanitizeLogText(c.ClientIP(), 128)),
		)
		if c.Request.URL.RawQuery != "" {
			record.AddAttrs(slog.String("query", "<redacted>"))
		}
		if len(c.Errors) > 0 {
			// Handler errors can contain user input or credentials. Preserve the
			// failure signal without copying arbitrary secret-bearing text.
			record.AddAttrs(slog.Int("error_count", len(c.Errors)))
		}
		if isTelemetry {
			record.AddAttrs(slog.Uint64("sample_rate", policy.TelemetrySuccessSampleRate))
		}
		_ = slog.Default().Handler().Handle(c.Request.Context(), record)
	}
}

func isHighFrequencyAgentWrite(method, path string) bool {
	if method != "POST" {
		return false
	}
	switch path {
	case "/api/clients/report", "/api/clients/ping/result", "/api/clients/task/result":
		return true
	default:
		return false
	}
}

func GinRecovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				record := slog.NewRecord(time.Now(), slog.LevelError, "HTTP panic recovered", 0)
				record.AddAttrs(
					slog.String("_group", "GIN"),
					slog.String("panic_type", fmt.Sprintf("%T", recovered)),
					slog.String("method", sanitizeLogText(c.Request.Method, 16)),
					slog.String("path", sanitizeLogText(c.Request.URL.Path, 512)),
				)
				_ = slog.Default().Handler().Handle(c.Request.Context(), record)
				c.AbortWithStatus(500)
			}
		}()
		c.Next()
	}
}

func sanitizeLogText(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	if len(value) > limit {
		value = value[:limit]
	}
	return value
}
