// Command replay generates bounded virtual Komari Agent telemetry traffic.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/komari-monitor/komari/internal/replay"
)

func main() {
	var cfg replay.Config
	var mode string
	flag.StringVar(&mode, "mode", "http", "transport: http or ws")
	flag.StringVar(&cfg.Endpoint, "endpoint", "http://127.0.0.1:25774/api/clients/report", "full report endpoint URL")
	flag.StringVar(&cfg.TokenTemplate, "token-template", "", "Bearer token; {index} expands to the virtual node number")
	flag.IntVar(&cfg.Nodes, "nodes", 1, "number of virtual agents")
	flag.DurationVar(&cfg.Interval, "interval", time.Second, "delay between reports per agent")
	flag.DurationVar(&cfg.Duration, "duration", time.Minute, "run duration")
	flag.IntVar(&cfg.ReportsPerNode, "reports-per-node", 0, "fixed report count per node (overrides duration)")
	flag.DurationVar(&cfg.RequestTimeout, "request-timeout", 10*time.Second, "per-request/write timeout")
	flag.IntVar(&cfg.SampleLimit, "sample-limit", 100_000, "maximum retained latency samples")
	flag.Parse()
	cfg.Mode = replay.Mode(mode)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := replay.Run(ctx, cfg)
	if encodeErr := json.NewEncoder(os.Stdout).Encode(result); encodeErr != nil {
		fmt.Fprintln(os.Stderr, "encode replay result:", encodeErr)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay failed:", err)
		os.Exit(1)
	}
	if result.Failed != 0 {
		os.Exit(2)
	}
}
