package public

import (
	"embed"
	"html"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/config"
	"github.com/komari-monitor/komari/utils"
)

//go:embed defaultTheme
var PublicFS embed.FS

const (
	DataDir      = "./data"
	ThemesDir    = "theme"
	FaviconFile  = "favicon.ico"
	DefaultTheme = "default"
	DistDir      = "dist"
	IndexFile    = "index.html"
)

func init() {
	_ = os.MkdirAll(filepath.Join(DataDir, ThemesDir), 0o755)
}

// isSafePath verifies lexical containment. Manifest construction additionally
// rejects symbolic links, so no file can escape the validated theme root.
func isSafePath(basePath, targetPath string) bool {
	absoluteBase, err := filepath.Abs(basePath)
	if err != nil {
		return false
	}
	absoluteTarget, err := filepath.Abs(filepath.Join(absoluteBase, filepath.Clean(targetPath)))
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(absoluteBase, absoluteTarget)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func Static(router *gin.RouterGroup, noRoute func(handlers ...gin.HandlerFunc)) {
	defaultThemeFS, err := fs.Sub(PublicFS, "defaultTheme")
	if err != nil {
		panic("you may forget to put dist of frontend to public/defaultTheme/dist")
	}
	themeAssets.configure(defaultThemeFS, filepath.Join(DataDir, ThemesDir))

	getConfig := func() map[string]any {
		values, _ := config.GetMany(map[string]any{
			config.DescriptionKey: "A simple server monitor tool.",
			config.CustomHeadKey:  "",
			config.CustomBodyKey:  "",
			config.SitenameKey:    "Komari Monitor",
			config.ThemeKey:       DefaultTheme,
		})
		// Stored arbitrary HTML/JS remains disabled.
		values[config.CustomHeadKey] = ""
		values[config.CustomBodyKey] = ""
		return values
	}

	serveAsset := func(c *gin.Context, themeID, relativePath string) bool {
		asset, exists := themeAssets.asset(themeID, relativePath)
		if !exists {
			return false
		}
		serveManifestAsset(c, asset)
		return true
	}

	serveIndex := func(c *gin.Context) {
		requestPath := c.Request.URL.Path
		values := getConfig()
		themeID := configString(values, config.ThemeKey, DefaultTheme)
		mode := "site"
		if strings.HasPrefix(requestPath, "/admin") || strings.HasPrefix(requestPath, "/terminal") {
			themeID = DefaultTheme
			mode = "system"
		}
		indexAsset, exists := themeAssets.asset(themeID, path.Join(DistDir, IndexFile))
		if !exists {
			c.String(http.StatusNotFound, "Index file missing (checked %s/dist/index.html and default).", themeID)
			return
		}

		siteName := configString(values, config.SitenameKey, "Komari Monitor")
		description := configString(values, config.DescriptionKey, "A simple server monitor tool.")
		cacheKey := strings.Join([]string{
			"index-v2", themeID, mode, indexAsset.identity.etag,
			strconv.Quote(siteName), strconv.Quote(description),
			strconv.FormatBool(strings.HasPrefix(requestPath, "/admin")),
		}, "\x00")
		rendered := themeAssets.generatedHTML(cacheKey, func() []byte {
			content := indexAsset.identity.content
			if mode == "system" {
				if strings.HasPrefix(requestPath, "/admin") {
					return injectAdminFleetReportSettingsLink(content)
				}
				return content
			}
			replacer := strings.NewReplacer(
				"<title>Komari Monitor</title>", "<title>"+html.EscapeString(siteName)+"</title>",
				"A simple server monitor tool.", html.EscapeString(description),
			)
			return []byte(replacer.Replace(string(content)))
		})
		serveGeneratedRepresentation(c, "text/html; charset=utf-8", rendered, "no-cache")
	}

	router.GET("/favicon.ico", func(c *gin.Context) {
		localPath := filepath.Join(DataDir, FaviconFile)
		if content, readErr := os.ReadFile(localPath); readErr == nil && len(content) <= maxManifestFileBytes {
			serveGeneratedContent(c, faviconContentType(content), content, "public, max-age=300, must-revalidate")
			return
		}
		values := getConfig()
		themeID := configString(values, config.ThemeKey, DefaultTheme)
		if !serveAsset(c, themeID, path.Join(DistDir, FaviconFile)) {
			c.Status(http.StatusNotFound)
		}
	})

	router.GET("/themes/:id/*path", func(c *gin.Context) {
		if !serveAsset(c, c.Param("id"), c.Param("path")) {
			c.Status(http.StatusNotFound)
		}
	})

	noRoute(func(c *gin.Context) {
		if c.Request.Method != http.MethodGet {
			c.Status(http.StatusNotFound)
			return
		}
		applyTemporaryShareCookie(c)
		values := getConfig()
		themeID := configString(values, config.ThemeKey, DefaultTheme)
		if serveAsset(c, themeID, path.Join(DistDir, c.Request.URL.Path)) {
			return
		}
		serveIndex(c)
	})
}

func configString(values map[string]any, key, fallback string) string {
	value, ok := values[key].(string)
	if !ok || value == "" {
		return fallback
	}
	return value
}

func faviconContentType(content []byte) string {
	contentType := http.DetectContentType(content)
	if strings.HasPrefix(contentType, "image/") {
		return contentType
	}
	return "image/x-icon"
}

func applyTemporaryShareCookie(c *gin.Context) {
	temporaryKey := c.Query("temp_key")
	if temporaryKey == "" {
		return
	}
	expiresAt, err := config.GetAs[int64]("tempory_share_token_expire_at", 0)
	if err != nil {
		return
	}
	allowedKey, err := config.GetAs[string]("tempory_share_token", "")
	if err != nil || allowedKey == "" || temporaryKey != allowedKey {
		return
	}
	remaining := expiresAt - time.Now().Unix()
	if remaining <= 0 {
		return
	}
	c.SetCookie(
		"temp_key", temporaryKey, int(remaining), "/", "", utils.IsRequestSecure(c), false,
	)
}
