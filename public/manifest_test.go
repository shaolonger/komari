package public

import (
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

func manifestTestFS() fs.FS {
	javascript := []byte(strings.Repeat("console.log('komari');", 200))
	return fstest.MapFS{
		"dist/index.html":                   {Data: []byte("<html><head><title>Komari Monitor</title></head><body>A simple server monitor tool.</body></html>")},
		"dist/assets/app-abcdef12.js":       {Data: javascript},
		"dist/assets/app-abcdef12.js.br":    {Data: []byte("precompressed-brotli")},
		"dist/assets/plain.css":             {Data: []byte(strings.Repeat(".node{color:#123456}", 200))},
		"dist/assets/fallback-12345678.css": {Data: []byte("body{color:black}")},
	}
}

func serveAssetForTest(asset *manifestAsset, acceptEncoding, ifNoneMatch string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodGet, "/asset", nil)
	request.Header.Set("Accept-Encoding", acceptEncoding)
	request.Header.Set("If-None-Match", ifNoneMatch)
	context.Request = request
	serveManifestAsset(context, asset)
	context.Writer.WriteHeaderNow()
	return recorder
}

func TestManifestServesImmutableETagBrotliGzipAnd304(t *testing.T) {
	manager := newManifestManager(manifestTestFS(), t.TempDir())
	javascript, exists := manager.asset(DefaultTheme, "dist/assets/app-abcdef12.js")
	if !exists {
		t.Fatal("hashed javascript missing")
	}
	if javascript.cacheControl != "public, max-age=31536000, immutable" {
		t.Fatalf("cache control=%q", javascript.cacheControl)
	}

	brotli := serveAssetForTest(javascript, "gzip;q=0.8, br;q=1", "")
	if brotli.Code != http.StatusOK || brotli.Header().Get("Content-Encoding") != "br" || brotli.Body.String() != "precompressed-brotli" {
		t.Fatalf("brotli response: code=%d headers=%v body=%q", brotli.Code, brotli.Header(), brotli.Body.String())
	}
	if brotli.Header().Get("Vary") != "Accept-Encoding" || brotli.Header().Get("ETag") == "" {
		t.Fatalf("missing variant headers: %v", brotli.Header())
	}

	gzipped := serveAssetForTest(javascript, "br;q=0, gzip;q=1", "")
	if gzipped.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("encoding=%q", gzipped.Header().Get("Content-Encoding"))
	}
	reader, err := gzip.NewReader(gzipped.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	if string(decoded) != strings.Repeat("console.log('komari');", 200) {
		t.Fatal("generated gzip content differs from identity")
	}

	notModified := serveAssetForTest(javascript, "gzip", gzipped.Header().Get("ETag"))
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 {
		t.Fatalf("conditional response: code=%d body=%q", notModified.Code, notModified.Body.String())
	}
	identity := serveAssetForTest(javascript, "", "")
	if identity.Header().Get("Content-Encoding") != "" || identity.Header().Get("ETag") == gzipped.Header().Get("ETag") {
		t.Fatalf("identity representation headers=%v", identity.Header())
	}
}

func TestManifestThemeInvalidationFallbackAndTraversalSafety(t *testing.T) {
	themesRoot := t.TempDir()
	themeRoot := filepath.Join(themesRoot, "custom")
	if err := os.MkdirAll(filepath.Join(themeRoot, "dist", "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	customPath := filepath.Join(themeRoot, "dist", "assets", "custom-abcdef12.js")
	if err := os.WriteFile(customPath, []byte("version-one"), 0o644); err != nil {
		t.Fatal(err)
	}
	overriddenPath := filepath.Join(themeRoot, "dist", "assets", "app-abcdef12.js")
	overriddenContent := []byte(strings.Repeat("custom-theme;", 300))
	if err := os.WriteFile(overriddenPath, overriddenContent, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themeRoot, "dist", "index.html"), []byte("custom-index"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(themesRoot, "outside-secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(themeRoot, "dist", "assets", "escape.js")); err != nil {
		t.Fatal(err)
	}

	manager := newManifestManager(manifestTestFS(), themesRoot)
	first, exists := manager.asset("custom", "dist/assets/custom-abcdef12.js")
	if !exists || string(first.identity.content) != "version-one" {
		t.Fatalf("first custom asset=%v exists=%v", first, exists)
	}
	if _, exists := manager.asset("custom", "../outside-secret"); exists {
		t.Fatal("path traversal entered manifest")
	}
	if _, exists := manager.asset("custom", "dist/assets/escape.js"); exists {
		t.Fatal("symbolic link entered manifest")
	}
	if _, exists := manager.asset("../custom", "dist/index.html"); exists {
		t.Fatal("invalid theme id accepted")
	}
	if fallback, exists := manager.asset("custom", "dist/assets/fallback-12345678.css"); !exists || string(fallback.identity.content) != "body{color:black}" {
		t.Fatal("default fallback missing")
	}
	overridden, exists := manager.asset("custom", "dist/assets/app-abcdef12.js")
	if !exists || overridden.brotli != nil || overridden.gzip == nil {
		t.Fatal("custom identity inherited an incompatible fallback sidecar")
	}
	reader, err := gzip.NewReader(strings.NewReader(string(overridden.gzip.content)))
	if err != nil {
		t.Fatal(err)
	}
	decompressed, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || string(decompressed) != string(overriddenContent) {
		t.Fatal("custom generated gzip does not represent custom identity")
	}

	if err := os.WriteFile(customPath, []byte("version-two"), 0o644); err != nil {
		t.Fatal(err)
	}
	stillCached, _ := manager.asset("custom", "dist/assets/custom-abcdef12.js")
	if string(stillCached.identity.content) != "version-one" {
		t.Fatal("immutable manifest changed without invalidation")
	}
	manager.invalidate("custom")
	second, exists := manager.asset("custom", "dist/assets/custom-abcdef12.js")
	if !exists || string(second.identity.content) != "version-two" || second.identity.etag == first.identity.etag {
		t.Fatalf("manifest did not rebuild: %#v", second)
	}
}

func TestGeneratedHTMLCacheBuildsOnceAndInvalidates(t *testing.T) {
	manager := newManifestManager(manifestTestFS(), t.TempDir())
	var builds atomic.Int32
	build := func() []byte {
		builds.Add(1)
		return []byte("rendered")
	}
	first := manager.generatedHTML("same-config", build)
	second := manager.generatedHTML("same-config", build)
	if builds.Load() != 1 || first.etag != second.etag || string(second.content) != "rendered" {
		t.Fatalf("cache mismatch: builds=%d first=%#v second=%#v", builds.Load(), first, second)
	}
	manager.invalidate()
	_ = manager.generatedHTML("same-config", build)
	if builds.Load() != 2 {
		t.Fatalf("builds after invalidation=%d", builds.Load())
	}
}

func TestManifestCacheEvictsThemesWithinHardBounds(t *testing.T) {
	themesRoot := t.TempDir()
	manager := newManifestManager(manifestTestFS(), themesRoot)
	for index := 0; index < maxManifestThemes+3; index++ {
		themeID := "theme" + strconv.Itoa(index)
		root := filepath.Join(themesRoot, themeID, "dist")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(themeID), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, exists := manager.asset(themeID, "dist/index.html"); !exists {
			t.Fatalf("theme %s did not load", themeID)
		}
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if len(manager.manifests) > maxManifestThemes || manager.cachedBytes > maxManifestCacheBytes {
		t.Fatalf("cache exceeded bounds: themes=%d bytes=%d", len(manager.manifests), manager.cachedBytes)
	}
	if _, exists := manager.manifests[DefaultTheme]; !exists {
		t.Fatal("default fallback was evicted")
	}
}

func TestManifestConcurrentLookupRenderAndInvalidation(t *testing.T) {
	manager := newManifestManager(manifestTestFS(), t.TempDir())
	var builders atomic.Int32
	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for iteration := 0; iteration < 500; iteration++ {
				asset, exists := manager.asset(DefaultTheme, "dist/assets/app-abcdef12.js")
				if !exists || asset.identity.etag == "" {
					t.Errorf("concurrent asset lookup failed")
					return
				}
				rendered := manager.generatedHTML("shared", func() []byte {
					builders.Add(1)
					return []byte("html")
				})
				if string(rendered.content) != "html" {
					t.Errorf("concurrent HTML cache mismatch")
					return
				}
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		for iteration := 0; iteration < 50; iteration++ {
			manager.invalidate(DefaultTheme)
		}
	}()
	workers.Wait()
	if builders.Load() == 0 {
		t.Fatal("HTML was never generated")
	}
}

func TestAcceptEncodingQualityAndUnsafePaths(t *testing.T) {
	accepted := parseAcceptEncoding("gzip;q=1, br;q=0.4, *;q=0.1")
	if encodingQuality(accepted, "gzip") != 1 || encodingQuality(accepted, "br") != 0.4 || encodingQuality(accepted, "zstd") != 0.1 {
		t.Fatalf("qualities=%v", accepted)
	}
	for _, unsafePath := range []string{"../secret", "/../../secret", `dist\\escape.js`, "\x00asset"} {
		if _, ok := cleanManifestPath(unsafePath); ok {
			t.Fatalf("unsafe path accepted: %q", unsafePath)
		}
	}
}

func BenchmarkManifestAssetLookup(b *testing.B) {
	manager := newManifestManager(manifestTestFS(), b.TempDir())
	if _, exists := manager.asset(DefaultTheme, "dist/assets/app-abcdef12.js"); !exists {
		b.Fatal("fixture missing")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		asset, exists := manager.asset(DefaultTheme, "dist/assets/app-abcdef12.js")
		if !exists || asset.identity.etag == "" {
			b.Fatal("asset lookup failed")
		}
	}
}
