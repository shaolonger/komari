package jsonRpc

import "github.com/komari-monitor/komari/utils/rpc"

func init() {
	rpc.SetCapabilities(map[string]string{
		"metric.definitions": "1",
		"metric.migration":   "1",
		"metric.query":       "1",
		"history.set-query":  "1",
		"ping.overview":      "2",
		"ping.leases":        "1",
		"ping.result-batch":  "1",
		"realtime.delta":     "1",
		"storage.embedded":   "1",
		"telemetry.v2":       "2",
		"telemetry.v3":       "3",
	})
}
