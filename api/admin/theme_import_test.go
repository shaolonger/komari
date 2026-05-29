package admin

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		themeArchiveConfigPath:  `{"name":"Nebula","short":"Nebula"}`,
		`dist\index.html`:      "<html><body>nebula</body></html>",
		`dist\assets\app.js`:  "console.log('nebula')",
		`dist\assets\app.css`: "body { color: white; }",
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