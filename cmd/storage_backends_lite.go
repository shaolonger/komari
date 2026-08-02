//go:build !scale

package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/komari-monitor/komari/database/controlstore"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/records"
	"github.com/komari-monitor/komari/database/telemetrywriter"
	"github.com/komari-monitor/komari/internal/storage"
)

func installStorageAdapters() {
	controlChoice := strings.ToLower(strings.TrimSpace(os.Getenv("KOMARI_CONTROL_STORE")))
	if controlChoice != "" && controlChoice != "sqlite" {
		panic("PostgreSQL control storage requires a binary built with -tags scale")
	}
	telemetryChoice := strings.ToLower(strings.TrimSpace(os.Getenv("KOMARI_TELEMETRY_STORE")))
	if telemetryChoice != "" && telemetryChoice != "sqlite" {
		panic("ClickHouse telemetry storage requires a binary built with -tags scale")
	}
	db := dbcore.GetDBInstance()
	if _, ok := storage.Control(); !ok {
		control, err := controlstore.NewSQLite(db)
		if err != nil {
			panic(fmt.Errorf("initialize SQLite control store: %w", err))
		}
		if _, err := storage.InstallControl(control); err != nil {
			panic(fmt.Errorf("install SQLite control store: %w", err))
		}
	}
	if _, ok := storage.Telemetry(); !ok {
		writerDB, err := dbcore.GetWriterDBInstance()
		if err != nil {
			panic(fmt.Errorf("initialize SQLite telemetry writer: %w", err))
		}
		telemetry, err := records.NewSQLiteTelemetryStore(db, writerDB, telemetrywriter.Config{})
		if err != nil {
			panic(fmt.Errorf("initialize SQLite telemetry store: %w", err))
		}
		if _, err := storage.InstallTelemetry(telemetry); err != nil {
			panic(fmt.Errorf("install SQLite telemetry store: %w", err))
		}
	}
}
