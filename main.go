package main

import (
	"log"
	"log/slog"

	"github.com/komari-monitor/komari/cmd"
	"github.com/komari-monitor/komari/internal/runtimeprofile"
	"github.com/komari-monitor/komari/utils"
	logutil "github.com/komari-monitor/komari/utils/log"
)

func main() {
	if utils.VersionHash == "unknown" {
		logutil.SetupGlobalLogger(slog.LevelDebug)
	} else {
		logutil.SetupGlobalLogger(slog.LevelInfo)
	}
	profile, err := runtimeprofile.Apply()
	if err != nil {
		log.Fatalf("Failed to configure runtime profile: %v", err)
	}

	log.Printf("Komari Monitor %s (hash: %s)", utils.CurrentVersion, utils.VersionHash)
	log.Printf("Runtime profile %s (cpus=%d memory_limit=%d go_memory_limit=%d)", profile.Name, profile.DetectedCPUs, profile.DetectedMemoryBytes, profile.GoMemoryLimitBytes)

	cmd.Execute()
}
