package log

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestGormLoggerNeverInterpolatesParameters(t *testing.T) {
	credential := "must-never-appear-in-log"
	l := NewGormLogger().LogMode(logger.Info)
	filter, ok := l.(interface {
		ParamsFilter(context.Context, string, ...interface{}) (string, []interface{})
	})
	if !ok {
		t.Fatal("GORM logger does not implement parameter filtering")
	}
	sql, params := filter.ParamsFilter(context.Background(), "SELECT * FROM sessions WHERE session = ?", credential)
	if sql != "SELECT * FROM sessions WHERE session = ?" || len(params) != 0 {
		t.Fatalf("ParamsFilter() = (%q, %v), want parameterized SQL and no values", sql, params)
	}

	var output bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(NewHandler(&output, slog.LevelDebug)))
	t.Cleanup(func() { slog.SetDefault(original) })
	l.Trace(context.Background(), time.Now(), func() (string, int64) { return sql, 0 }, nil)
	if strings.Contains(output.String(), credential) {
		t.Fatalf("credential leaked in GORM log: %s", output.String())
	}
	if !strings.Contains(output.String(), "session = ?") {
		t.Fatalf("parameterized query missing from GORM log: %s", output.String())
	}

	output.Reset()
	db, err := gorm.Open(sqlite.Open("file:gorm-redaction?mode=memory&cache=shared"), &gorm.Config{Logger: l})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("SELECT ? AS credential", credential).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), credential) {
		t.Fatalf("credential leaked through GORM integration: %s", output.String())
	}
	if !strings.Contains(output.String(), "SELECT ? AS credential") {
		t.Fatalf("parameterized integration query missing: %s", output.String())
	}
}
