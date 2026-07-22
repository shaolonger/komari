package clients

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/komari-monitor/komari/cmd/flags"
)

func TestMain(m *testing.M) {
	tempDir, err := os.MkdirTemp("", "komari-client-credential-tests-*")
	if err != nil {
		panic(err)
	}
	flags.DatabaseType = "sqlite"
	flags.DatabaseFile = filepath.Join(tempDir, "credentials.db")
	code := m.Run()
	_ = os.RemoveAll(tempDir)
	os.Exit(code)
}
