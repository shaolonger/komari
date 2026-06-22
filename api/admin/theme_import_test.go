package admin

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestNormalizeThemeArchivePath(t *testing.T) {
	testCases := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{name: "windows separators", input: `dist\index.html`, want: "dist/index.html", ok: true},
		{name: "dot prefix", input: `.\komari-theme.json`, want: "komari-theme.json", ok: true},
		{name: "unix separators", input: "dist/assets/app.js", want: "dist/assets/app.js", ok: true},
		{name: "path traversal", input: `..\evil.txt`, ok: false},
		{name: "empty", input: "", ok: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := normalizeThemeArchivePath(tc.input)
			if ok != tc.ok {
				t.Fatalf("normalizeThemeArchivePath(%q) ok = %v, want %v", tc.input, ok, tc.ok)
			}
			if got != tc.want {
				t.Fatalf("normalizeThemeArchivePath(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestExtractAndValidateThemeRejectsMissingIndex(t *testing.T) {
	zipPath := writeThemeArchive(t, map[string]string{
		themeArchiveConfigPath: `{"name":"Nebula","short":"Nebula"}`,
	})

	withWorkingDir(t, t.TempDir(), func() {
		_, err := extractAndValidateTheme(zipPath)
		if err == nil {
			t.Fatal("extractAndValidateTheme() error = nil, want missing index error")
		}
		if !strings.Contains(err.Error(), themeArchiveIndexPath) {
			t.Fatalf("extractAndValidateTheme() error = %q, want mention of %q", err.Error(), themeArchiveIndexPath)
		}
	})
}

func TestExtractAndValidateThemeNormalizesWindowsSeparators(t *testing.T) {
	zipPath := writeThemeArchive(t, map[string]string{
		themeArchiveConfigPath: `{"name":"Nebula","short":"Nebula"}`,
		`dist\index.html`:      "<html><body>nebula</body></html>",
		`dist\assets\app.js`:   "console.log('nebula')",
		`dist\assets\app.css`:  "body { color: white; }",
	})

	workDir := t.TempDir()
	withWorkingDir(t, workDir, func() {
		themeInfo, err := extractAndValidateTheme(zipPath)
		if err != nil {
			t.Fatalf("extractAndValidateTheme() error = %v", err)
		}
		if themeInfo.Short != "Nebula" {
			t.Fatalf("themeInfo.Short = %q, want %q", themeInfo.Short, "Nebula")
		}

		wantFiles := []string{
			filepath.Join(workDir, "data", "theme", "Nebula", "komari-theme.json"),
			filepath.Join(workDir, "data", "theme", "Nebula", "dist", "index.html"),
			filepath.Join(workDir, "data", "theme", "Nebula", "dist", "assets", "app.js"),
		}
		for _, wantPath := range wantFiles {
			if _, err := os.Stat(wantPath); err != nil {
				t.Fatalf("os.Stat(%q) error = %v", wantPath, err)
			}
		}
	})
}

func TestExtractAndValidateThemeRejectsOversizedZip(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "oversized-theme.zip")
	oversized := bytes.Repeat([]byte("a"), maxThemeZipSize+1)
	if err := os.WriteFile(zipPath, oversized, 0644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", zipPath, err)
	}

	withWorkingDir(t, t.TempDir(), func() {
		_, err := extractAndValidateTheme(zipPath)
		if err == nil {
			t.Fatal("extractAndValidateTheme() error = nil, want oversized zip error")
		}
		if !strings.Contains(err.Error(), themeZipSizeLimitMessage()) {
			t.Fatalf("extractAndValidateTheme() error = %q, want %q", err.Error(), themeZipSizeLimitMessage())
		}
	})
}

func TestUploadThemeRejectsOversizedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPut, "/theme/upload", bytes.NewReader(bytes.Repeat([]byte("a"), maxThemeZipSize+1)))

	UploadTheme(context)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("UploadTheme() status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}

	var response struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if response.Status != "error" {
		t.Fatalf("response.Status = %q, want %q", response.Status, "error")
	}
	if response.Message != themeZipSizeLimitMessage() {
		t.Fatalf("response.Message = %q, want %q", response.Message, themeZipSizeLimitMessage())
	}
}

func withWorkingDir(t *testing.T, dir string, fn func()) {
	t.Helper()

	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("os.Chdir(%q) error = %v", dir, err)
	}
	defer func() {
		if err := os.Chdir(oldDir); err != nil {
			t.Fatalf("restore cwd error = %v", err)
		}
	}()

	fn()
}

func writeThemeArchive(t *testing.T, entries map[string]string) string {
	t.Helper()

	zipPath := filepath.Join(t.TempDir(), "theme.zip")
	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("os.Create(%q) error = %v", zipPath, err)
	}
	defer file.Close()

	writer := zip.NewWriter(file)
	for name, content := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("zip.Create(%q) error = %v", name, err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("entry.Write(%q) error = %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close() error = %v", err)
	}

	return zipPath
}
