package public

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

const (
	maxManifestFileBytes  = 5 << 20
	maxManifestThemeBytes = 64 << 20
	maxManifestCacheBytes = 128 << 20
	maxManifestThemes     = 8
	maxPrecompressBytes   = 4 << 20
)

var hashedAssetPattern = regexp.MustCompile(`[-.][A-Za-z0-9_-]{8,}\.[A-Za-z0-9]+$`)

type assetRepresentation struct {
	content []byte
	etag    string
}

type manifestAsset struct {
	contentType  string
	identity     assetRepresentation
	gzip         *assetRepresentation
	brotli       *assetRepresentation
	cacheControl string
}

type themeManifest struct {
	theme      string
	assets     map[string]*manifestAsset
	ownedBytes int
	sequence   uint64
}

type generatedAsset struct {
	representation assetRepresentation
	sequence       uint64
}

type manifestManager struct {
	mu          sync.RWMutex
	defaultFS   fs.FS
	themesRoot  string
	manifests   map[string]*themeManifest
	sequence    uint64
	cachedBytes int
	generated   map[[sha256.Size]byte]generatedAsset
}

var themeAssets = newManifestManager(nil, filepath.Join(DataDir, ThemesDir))

func newManifestManager(defaultFS fs.FS, themesRoot string) *manifestManager {
	return &manifestManager{
		defaultFS: defaultFS, themesRoot: themesRoot,
		manifests: make(map[string]*themeManifest), generated: make(map[[sha256.Size]byte]generatedAsset),
	}
}

func (manager *manifestManager) configure(defaultFS fs.FS, themesRoot string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.defaultFS = defaultFS
	manager.themesRoot = themesRoot
	manager.manifests = make(map[string]*themeManifest)
	manager.cachedBytes = 0
	manager.generated = make(map[[sha256.Size]byte]generatedAsset)
}

func InvalidateThemeManifest(themeIDs ...string) {
	themeAssets.invalidate(themeIDs...)
}

// RebuildThemeManifest atomically drops an old generation and eagerly builds
// the replacement when the HTTP static service is configured. Before router
// initialization it safely degrades to invalidation.
func RebuildThemeManifest(themeID string) error {
	themeAssets.invalidate(themeID)
	themeAssets.mu.RLock()
	configured := themeAssets.defaultFS != nil
	themeAssets.mu.RUnlock()
	if !configured {
		return nil
	}
	_, err := themeAssets.load(themeID)
	return err
}

func (manager *manifestManager) invalidate(themeIDs ...string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if len(themeIDs) == 0 {
		manager.manifests = make(map[string]*themeManifest)
		manager.cachedBytes = 0
		manager.generated = make(map[[sha256.Size]byte]generatedAsset)
		return
	}
	for _, themeID := range themeIDs {
		if manifest, exists := manager.manifests[themeID]; exists {
			manager.cachedBytes -= manifest.ownedBytes
			delete(manager.manifests, themeID)
		}
	}
	manager.generated = make(map[[sha256.Size]byte]generatedAsset)
}

func (manager *manifestManager) load(themeID string) (*themeManifest, error) {
	if !validThemeID(themeID) {
		return nil, errors.New("invalid theme id")
	}
	manager.mu.RLock()
	if manifest, exists := manager.manifests[themeID]; exists {
		manager.mu.RUnlock()
		return manifest, nil
	}
	manager.mu.RUnlock()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manifest, exists := manager.manifests[themeID]; exists {
		return manifest, nil
	}
	if manager.defaultFS == nil {
		return nil, errors.New("default theme filesystem is not configured")
	}

	defaultManifest := manager.manifests[DefaultTheme]
	if defaultManifest == nil {
		var err error
		defaultManifest, err = buildEmbeddedManifest(manager.defaultFS)
		if err != nil {
			return nil, err
		}
		manager.sequence++
		defaultManifest.sequence = manager.sequence
		manager.manifests[DefaultTheme] = defaultManifest
		manager.cachedBytes += defaultManifest.ownedBytes
	}
	if themeID == DefaultTheme {
		return defaultManifest, nil
	}
	manifest, err := buildLocalManifest(themeID, filepath.Join(manager.themesRoot, themeID), defaultManifest)
	if err != nil {
		return nil, err
	}
	manager.sequence++
	manifest.sequence = manager.sequence
	manager.manifests[themeID] = manifest
	manager.cachedBytes += manifest.ownedBytes
	manager.evictLocked()
	return manifest, nil
}

func (manager *manifestManager) evictLocked() {
	for len(manager.manifests) > maxManifestThemes || manager.cachedBytes > maxManifestCacheBytes {
		var oldestID string
		var oldest *themeManifest
		for themeID, manifest := range manager.manifests {
			if themeID == DefaultTheme {
				continue
			}
			if oldest == nil || manifest.sequence < oldest.sequence {
				oldestID, oldest = themeID, manifest
			}
		}
		if oldest == nil {
			return
		}
		delete(manager.manifests, oldestID)
		manager.cachedBytes -= oldest.ownedBytes
	}
}

func (manager *manifestManager) asset(themeID, rawPath string) (*manifestAsset, bool) {
	cleanPath, ok := cleanManifestPath(rawPath)
	if !ok {
		return nil, false
	}
	manifest, err := manager.load(themeID)
	if err != nil {
		return nil, false
	}
	asset, exists := manifest.assets[cleanPath]
	return asset, exists
}

func (manager *manifestManager) generatedHTML(cacheKey string, build func() []byte) assetRepresentation {
	digest := sha256.Sum256([]byte(cacheKey))
	manager.mu.RLock()
	if cached, exists := manager.generated[digest]; exists {
		manager.mu.RUnlock()
		return cached.representation
	}
	manager.mu.RUnlock()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.sequence++
	if cached, exists := manager.generated[digest]; exists {
		return cached.representation
	}
	content := append([]byte(nil), build()...)
	representation := assetRepresentation{content: content, etag: contentETag(content)}
	manager.generated[digest] = generatedAsset{representation: representation, sequence: manager.sequence}
	for len(manager.generated) > 64 {
		var oldestKey [sha256.Size]byte
		var oldest generatedAsset
		found := false
		for key, candidate := range manager.generated {
			if !found || candidate.sequence < oldest.sequence {
				oldestKey, oldest, found = key, candidate, true
			}
		}
		if found {
			delete(manager.generated, oldestKey)
		}
	}
	return representation
}

func buildEmbeddedManifest(defaultFS fs.FS) (*themeManifest, error) {
	assets := make(map[string]*manifestAsset)
	ownedBytes := 0
	err := fs.WalkDir(defaultFS, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		cleanName, ok := cleanManifestPath(name)
		if !ok {
			return errors.New("unsafe embedded theme path")
		}
		content, err := fs.ReadFile(defaultFS, name)
		if err != nil {
			return err
		}
		if len(content) > maxManifestFileBytes || ownedBytes+len(content) > maxManifestThemeBytes {
			return errors.New("embedded theme exceeds manifest budget")
		}
		assets[cleanName] = newManifestAsset(cleanName, content)
		ownedBytes += len(content)
		return nil
	})
	if err != nil {
		return nil, err
	}
	ownedBytes += attachCompressedVariants(assets)
	if ownedBytes > maxManifestThemeBytes {
		return nil, errors.New("embedded theme compressed manifest exceeds budget")
	}
	return &themeManifest{theme: DefaultTheme, assets: assets, ownedBytes: ownedBytes}, nil
}

func buildLocalManifest(themeID, root string, fallback *themeManifest) (*themeManifest, error) {
	assets := make(map[string]*manifestAsset, len(fallback.assets))
	for name, asset := range fallback.assets {
		cloned := *asset
		assets[name] = &cloned
	}
	ownedBytes := 0
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, name)
		if err != nil || !isSafePath(root, relative) {
			return errors.New("unsafe local theme path")
		}
		cleanName, ok := cleanManifestPath(filepath.ToSlash(relative))
		if !ok {
			return errors.New("unsafe local theme path")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxManifestFileBytes || info.Size() < 0 || int64(ownedBytes)+info.Size() > maxManifestThemeBytes {
			return errors.New("local theme exceeds manifest budget")
		}
		content, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if len(content) > maxManifestFileBytes || ownedBytes+len(content) > maxManifestThemeBytes {
			return errors.New("local theme changed beyond manifest budget while loading")
		}
		assets[cleanName] = newManifestAsset(cleanName, content)
		if !strings.HasSuffix(cleanName, ".br") && !strings.HasSuffix(cleanName, ".gz") {
			// A custom identity must never inherit a fallback sidecar encoding
			// different bytes. A custom sidecar encountered later reattaches.
			delete(assets, cleanName+".br")
			delete(assets, cleanName+".gz")
		}
		ownedBytes += len(content)
		return nil
	})
	if os.IsNotExist(err) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	ownedBytes += attachCompressedVariants(assets)
	if ownedBytes > maxManifestThemeBytes {
		return nil, errors.New("local theme compressed manifest exceeds budget")
	}
	if _, exists := assets[path.Join(DistDir, IndexFile)]; !exists {
		return nil, errors.New("theme index is missing")
	}
	return &themeManifest{theme: themeID, assets: assets, ownedBytes: ownedBytes}, nil
}

func newManifestAsset(name string, content []byte) *manifestAsset {
	owned := append([]byte(nil), content...)
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	cacheControl := "public, max-age=300, must-revalidate"
	if hashedAssetPattern.MatchString(path.Base(name)) {
		cacheControl = "public, max-age=31536000, immutable"
	}
	return &manifestAsset{
		contentType:  contentType,
		identity:     assetRepresentation{content: owned, etag: contentETag(owned)},
		cacheControl: cacheControl,
	}
}

func attachCompressedVariants(assets map[string]*manifestAsset) int {
	additionalBytes := 0
	names := make([]string, 0, len(assets))
	for name := range assets {
		names = append(names, name)
	}
	sort.Strings(names)
	// Attach supplied sidecars first so generation never performs work that a
	// later .gz entry would immediately replace.
	for _, name := range names {
		asset := assets[name]
		if strings.HasSuffix(name, ".br") {
			if original := assets[strings.TrimSuffix(name, ".br")]; original != nil {
				representation := asset.identity
				original.brotli = &representation
			}
			continue
		}
		if strings.HasSuffix(name, ".gz") {
			if original := assets[strings.TrimSuffix(name, ".gz")]; original != nil {
				representation := asset.identity
				original.gzip = &representation
			}
		}
	}
	for _, name := range names {
		if strings.HasSuffix(name, ".br") || strings.HasSuffix(name, ".gz") {
			continue
		}
		asset := assets[name]
		if asset.gzip == nil && compressibleContentType(asset.contentType) && len(asset.identity.content) >= 256 && len(asset.identity.content) <= maxPrecompressBytes {
			var compressed bytes.Buffer
			writer, _ := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
			_, writeErr := writer.Write(asset.identity.content)
			closeErr := writer.Close()
			if writeErr == nil && closeErr == nil && compressed.Len() < len(asset.identity.content) {
				content := append([]byte(nil), compressed.Bytes()...)
				asset.gzip = &assetRepresentation{content: content, etag: contentETag(content)}
				additionalBytes += len(content)
			}
		}
	}
	return additionalBytes
}

func compressibleContentType(contentType string) bool {
	mediaType := strings.ToLower(strings.SplitN(contentType, ";", 2)[0])
	return strings.HasPrefix(mediaType, "text/") || strings.Contains(mediaType, "javascript") || strings.Contains(mediaType, "json") || strings.Contains(mediaType, "xml") || strings.Contains(mediaType, "svg")
}

func contentETag(content []byte) string {
	digest := sha256.Sum256(content)
	return `"` + hex.EncodeToString(digest[:]) + `"`
}

func validThemeID(themeID string) bool {
	if themeID == DefaultTheme {
		return true
	}
	if themeID == "" {
		return false
	}
	for _, character := range themeID {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-') {
			return false
		}
	}
	return true
}

func cleanManifestPath(rawPath string) (string, bool) {
	if strings.ContainsRune(rawPath, '\x00') || strings.Contains(rawPath, "\\") {
		return "", false
	}
	cleaned := path.Clean(strings.TrimLeft(rawPath, "/"))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", false
	}
	return cleaned, true
}

func (asset *manifestAsset) selectRepresentation(acceptEncoding string) (assetRepresentation, string) {
	accepted := parseAcceptEncoding(acceptEncoding)
	brotliQuality := encodingQuality(accepted, "br")
	gzipQuality := encodingQuality(accepted, "gzip")
	if asset.brotli != nil && brotliQuality > 0 && brotliQuality >= gzipQuality {
		return *asset.brotli, "br"
	}
	if asset.gzip != nil && gzipQuality > 0 {
		return *asset.gzip, "gzip"
	}
	if asset.brotli != nil && brotliQuality > 0 {
		return *asset.brotli, "br"
	}
	return asset.identity, ""
}

func encodingQuality(accepted map[string]float64, encoding string) float64 {
	if quality, exists := accepted[encoding]; exists {
		return quality
	}
	return accepted["*"]
}

func parseAcceptEncoding(header string) map[string]float64 {
	accepted := map[string]float64{"identity": 1}
	for _, item := range strings.Split(strings.ToLower(header), ",") {
		parts := strings.Split(strings.TrimSpace(item), ";")
		name := strings.TrimSpace(parts[0])
		if name == "" {
			continue
		}
		quality := 1.0
		for _, parameter := range parts[1:] {
			parameter = strings.TrimSpace(parameter)
			if strings.HasPrefix(parameter, "q=") {
				parsed, err := strconv.ParseFloat(strings.TrimPrefix(parameter, "q="), 64)
				if err != nil || parsed < 0 || parsed > 1 {
					quality = 0
				} else {
					quality = parsed
				}
			}
		}
		accepted[name] = quality
	}
	return accepted
}

func serveManifestAsset(c *gin.Context, asset *manifestAsset) {
	representation, encoding := asset.selectRepresentation(c.GetHeader("Accept-Encoding"))
	c.Header("Content-Type", asset.contentType)
	c.Header("Cache-Control", asset.cacheControl)
	c.Header("ETag", representation.etag)
	c.Header("X-Content-Type-Options", "nosniff")
	if asset.gzip != nil || asset.brotli != nil {
		c.Header("Vary", "Accept-Encoding")
	}
	if encoding != "" {
		c.Header("Content-Encoding", encoding)
	}
	if etagMatches(c.GetHeader("If-None-Match"), representation.etag) {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, asset.contentType, representation.content)
}

func serveGeneratedContent(c *gin.Context, contentType string, content []byte, cacheControl string) {
	serveGeneratedRepresentation(c, contentType, assetRepresentation{content: content, etag: contentETag(content)}, cacheControl)
}

func serveGeneratedRepresentation(c *gin.Context, contentType string, representation assetRepresentation, cacheControl string) {
	c.Header("Cache-Control", cacheControl)
	c.Header("ETag", representation.etag)
	c.Header("X-Content-Type-Options", "nosniff")
	if etagMatches(c.GetHeader("If-None-Match"), representation.etag) {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, contentType, representation.content)
}

func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag {
			return true
		}
	}
	return false
}
