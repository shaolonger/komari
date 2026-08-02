package admin

import (
	"net/http"
	httppprof "net/http/pprof"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/internal/observability"
)

// RegisterDiagnostics adds the authenticated metrics endpoint and, only when
// explicitly enabled, Go runtime diagnostics. The supplied group must already
// enforce the administrator role.
func RegisterDiagnostics(group *gin.RouterGroup, runtimeEnabled bool) {
	group.GET("/metrics", metricsHandler)
	if !runtimeEnabled {
		return
	}
	debug := group.Group("/debug/pprof")
	debug.GET("/", gin.WrapF(httppprof.Index))
	debug.GET("/cmdline", gin.WrapF(httppprof.Cmdline))
	debug.GET("/profile", gin.WrapF(httppprof.Profile))
	debug.POST("/symbol", gin.WrapF(httppprof.Symbol))
	debug.GET("/symbol", gin.WrapF(httppprof.Symbol))
	debug.GET("/trace", gin.WrapF(httppprof.Trace))
	for _, profile := range []string{"allocs", "block", "goroutine", "heap", "mutex", "threadcreate"} {
		debug.GET("/"+profile, gin.WrapH(httppprof.Handler(profile)))
	}
}

func metricsHandler(c *gin.Context) {
	c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)
	if err := observability.WritePrometheus(c.Writer); err != nil {
		c.Error(err)
	}
}
