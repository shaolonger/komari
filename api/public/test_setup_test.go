package public

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/komari-monitor/komari/cmd/flags"
)

func TestMain(m *testing.M) {
	tempDir, err := os.MkdirTemp("", "komari-public-tests-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tempDir)

	flags.DatabaseType = "sqlite"
	flags.DatabaseFile = filepath.Join(tempDir, "public-test.db")

	os.Exit(m.Run())
}
