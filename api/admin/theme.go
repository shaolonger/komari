package admin

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/config"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/public"
)

const (
	themeArchiveConfigPath = "komari-theme.json"
	themeArchiveIndexPath  = "dist/index.html"
)

// UploadTheme 上传主题
func UploadTheme(c *gin.Context) {
	// 读取上传的文件内容
	data, err := io.ReadAll(c.Request.Body)
	if err != nil || len(data) == 0 {
		api.RespondError(c, http.StatusBadRequest, "请选择要上传的主题文件")
		return
	}

	// 临时文件名
	tempFile := filepath.Join(os.TempDir(), "uploaded_theme.zip")
	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		api.RespondError(c, http.StatusInternalServerError, "保存文件失败: "+err.Error())
		return
	}
	defer os.Remove(tempFile)

	// 检查文件扩展名（这里假定上传的就是zip）
	if !strings.HasSuffix(strings.ToLower(tempFile), ".zip") {
		api.RespondError(c, http.StatusBadRequest, "只支持ZIP格式的主题文件")
		return
	}

	// 解压ZIP文件并验证
	themeInfo, err := extractAndValidateTheme(tempFile)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, err.Error())
		return
	}

	api.RespondSuccessMessage(c, "主题上传成功", themeInfo)
}

// ListThemes 列出所有主题
func ListThemes(c *gin.Context) {
	dataDir := "./data/theme"

	// 确保主题目录存在
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		api.RespondSuccess(c, []models.Theme{})
		return
	}

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "读取主题目录失败: "+err.Error())
		return
	}

	var themes []models.Theme
	defaultTheme, err := public.PublicFS.ReadFile("defaultTheme/komari-theme.json")
	if err == nil {
		dt := models.Theme{}
		err := json.Unmarshal(defaultTheme, &dt)
		if err == nil {
			themes = append(themes, dt)
		}

	}
	for _, entry := range entries {
		if entry.IsDir() {
			themeConfigPath := filepath.Join(dataDir, entry.Name(), "komari-theme.json")
			if themeInfo, err := loadThemeConfig(themeConfigPath); err == nil {
				themes = append(themes, themeInfo)
			}
		}
	}

	api.RespondSuccess(c, themes)
}

// DeleteTheme 删除主题
func DeleteTheme(c *gin.Context) {
	var req struct {
		Short string `json:"short" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		api.RespondError(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	if req.Short == "default" {
		api.RespondError(c, http.StatusBadRequest, "默认主题不能删除")
		return
	}

	themeDir := filepath.Join("./data/theme", req.Short)

	// 检查主题是否存在
	if _, err := os.Stat(themeDir); os.IsNotExist(err) {
		api.RespondError(c, http.StatusNotFound, "主题不存在")
		return
	}

	// 删除主题目录
	if err := os.RemoveAll(themeDir); err != nil {
		api.RespondError(c, http.StatusInternalServerError, "删除主题失败: "+err.Error())
		return
	}

	api.RespondSuccessMessage(c, "主题删除成功", nil)
}

// SetTheme 设置主题
func SetTheme(c *gin.Context) {
	themeName := c.Query("theme")
	if themeName == "" {
		api.RespondError(c, http.StatusBadRequest, "主题名称不能为空")
		return
	}

	// 如果不是default主题，检查主题是否存在
	if themeName != "default" {
		themeDir := filepath.Join("./data/theme", themeName)
		themeConfigPath := filepath.Join(themeDir, "komari-theme.json")

		if _, err := os.Stat(themeConfigPath); os.IsNotExist(err) {
			api.RespondError(c, http.StatusNotFound, "主题不存在")
			return
		}
	}

	if err := config.Set("theme", themeName); err != nil {
		api.RespondError(c, http.StatusInternalServerError, "更新主题设置失败: "+err.Error())
		return
	}

	api.RespondSuccessMessage(c, "主题设置成功", gin.H{"theme": themeName})
}

func normalizeThemeArchivePath(name string) (string, bool) {
	normalized := strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	normalized = strings.TrimLeft(normalized, "/")
	if normalized == "" {
		return "", false
	}

	normalized = path.Clean(normalized)
	if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", false
	}

	return normalized, true
}

func findThemeArchiveFile(files []*zip.File, want string) *zip.File {
	for _, f := range files {
		normalized, ok := normalizeThemeArchivePath(f.Name)
		if ok && normalized == want {
			return f
		}
	}

	return nil
}

// extractAndValidateTheme 解压并验证主题
func extractAndValidateTheme(zipPath string) (models.Theme, error) {
	var themeInfo models.Theme

	const (
		MaxZipSize        = 10 * 1024 * 1024 // 10MB
		MaxTotalUnzipSize = 30 * 1024 * 1024 // 30MB
		MaxFileCount      = 200
		MaxSingleFileSize = 5 * 1024 * 1024  // 5MB
	)

	// 1. 验证ZIP文件本身的大小限制
	info, err := os.Stat(zipPath)
	if err != nil {
		return themeInfo, fmt.Errorf("无法获取主题文件信息: %v", err)
	}
	if info.Size() > MaxZipSize {
		return themeInfo, fmt.Errorf("主题包文件大小超过限制 (最大 10MB)")
	}

	// 打开ZIP文件
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return themeInfo, fmt.Errorf("无法打开ZIP文件: %v", err)
	}
	defer r.Close()

	// 2. 限制文件数量
	if len(r.File) > MaxFileCount {
		return themeInfo, fmt.Errorf("主题包内文件数量超过限制 (最大 200 个)")
	}

	// 3. 预先计算总解压大小和单文件解压大小限制
	var totalUnzippedSize int64
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if f.UncompressedSize64 > uint64(MaxSingleFileSize) {
			return themeInfo, fmt.Errorf("主题包内单个文件解压大小超过限制 (最大 5MB): %s", f.Name)
		}
		totalUnzippedSize += int64(f.UncompressedSize64)
	}
	if totalUnzippedSize > MaxTotalUnzipSize {
		return themeInfo, fmt.Errorf("主题包总解压大小超过限制 (最大 30MB)")
	}

	// 查找komari-theme.json文件
	themeConfigFile := findThemeArchiveFile(r.File, themeArchiveConfigPath)

	if themeConfigFile == nil {
		return themeInfo, fmt.Errorf("主题配置文件 komari-theme.json 不存在")
	}
	if findThemeArchiveFile(r.File, themeArchiveIndexPath) == nil {
		return themeInfo, fmt.Errorf("主题缺少必需文件 dist/index.html")
	}

	// 读取主题配置
	rc, err := themeConfigFile.Open()
	if err != nil {
		return themeInfo, fmt.Errorf("无法读取主题配置文件: %v", err)
	}
	defer rc.Close()

	configData, err := io.ReadAll(rc)
	if err != nil {
		return themeInfo, fmt.Errorf("读取主题配置失败: %v", err)
	}

	if err := json.Unmarshal(configData, &themeInfo); err != nil {
		return themeInfo, fmt.Errorf("主题配置格式错误: %v", err)
	}

	// 验证必填字段
	if themeInfo.Name == "" || themeInfo.Short == "" {
		return themeInfo, fmt.Errorf("主题配置缺少必填字段（name、short）")
	}

	// 验证Short字段格式（只允许字母、数字、下划线、连字符）
	if !isValidThemeShort(themeInfo.Short) {
		return themeInfo, fmt.Errorf("主题short字段格式无效，只允许字母、数字、下划线和连字符")
	}

	// 创建主题目录
	themeDir := filepath.Join("./data/theme", themeInfo.Short)

	// 如果目录已存在，先删除
	if _, err := os.Stat(themeDir); err == nil {
		if err := os.RemoveAll(themeDir); err != nil {
			return themeInfo, fmt.Errorf("删除原有主题失败: %v", err)
		}
	}

	if err := os.MkdirAll(themeDir, 0755); err != nil {
		return themeInfo, fmt.Errorf("创建主题目录失败: %v", err)
	}

	// 解压文件到主题目录
	cleanThemeDir := filepath.Clean(themeDir)
	for _, f := range r.File {
		normalizedName, ok := normalizeThemeArchivePath(f.Name)
		if !ok {
			continue
		}

		targetPath := filepath.Join(cleanThemeDir, filepath.FromSlash(normalizedName))

		// 安全检查，防止路径遍历攻击
		if targetPath != cleanThemeDir && !strings.HasPrefix(targetPath, cleanThemeDir+string(os.PathSeparator)) {
			continue
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(targetPath, f.FileInfo().Mode())
			continue
		}

		// 创建目录
		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return themeInfo, fmt.Errorf("创建目录失败: %v", err)
		}

		// 解压文件
		rc, err := f.Open()
		if err != nil {
			return themeInfo, fmt.Errorf("打开压缩文件失败: %v", err)
		}

		outFile, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.FileInfo().Mode())
		if err != nil {
			rc.Close()
			return themeInfo, fmt.Errorf("创建文件失败: %v", err)
		}

		// 使用 LimitReader 保证写入不超过限制
		limitedRc := io.LimitReader(rc, MaxSingleFileSize)
		_, err = io.Copy(outFile, limitedRc)
		outFile.Close()
		rc.Close()

		if err != nil {
			return themeInfo, fmt.Errorf("解压文件失败: %v", err)
		}
	}

	return themeInfo, nil
}

// loadThemeConfig 加载主题配置
func loadThemeConfig(configPath string) (models.Theme, error) {
	var themeInfo models.Theme

	data, err := os.ReadFile(configPath)
	if err != nil {
		return themeInfo, err
	}

	if err := json.Unmarshal(data, &themeInfo); err != nil {
		return themeInfo, err
	}

	return themeInfo, nil
}

// isValidThemeShort 验证主题short字段格式
func isValidThemeShort(short string) bool {
	if short == "" || short == "default" {
		return false
	}

	for _, r := range short {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-') {
			return false
		}
	}

	return true
}

var allowedThemeDomains = []string{
	"github.com",
	"api.github.com",
	"raw.githubusercontent.com",
	"objects.githubusercontent.com",
	"github-releases.githubusercontent.com",
}

func isDomainAllowed(host string) bool {
	host = strings.ToLower(host)
	for _, domain := range allowedThemeDomains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

var themeHttpClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		host := req.URL.Hostname()
		if !isDomainAllowed(host) {
			return fmt.Errorf("redirect target domain not allowed: %s", host)
		}
		if isPrivateIP(host) {
			return fmt.Errorf("redirect target points to a private address")
		}
		return nil
	},
}

// isPrivateIP checks if the resolved IP addresses are private/internal
func isPrivateIP(host string) bool {
	ips, err := net.LookupHost(host)
	if err != nil {
		return true // fail closed
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return true
		}
	}
	return false
}

// downloadThemeFromURL 从URL下载主题文件
func downloadThemeFromURL(rawURL string) ([]byte, error) {
	// SSRF protection: block requests to private/internal IPs and non-whitelisted domains
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %v", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("only http and https schemes are allowed")
	}
	host := parsedURL.Hostname()
	if !isDomainAllowed(host) {
		return nil, fmt.Errorf("domain %s is not in the allowed whitelist", host)
	}
	if isPrivateIP(host) {
		return nil, fmt.Errorf("requests to private/internal addresses are not allowed")
	}

	// 发送HTTP GET请求
	resp, err := themeHttpClient.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("下载主题文件失败: %v", err)
	}
	defer resp.Body.Close()

	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载主题文件失败，HTTP状态码: %d", resp.StatusCode)
	}

	// 读取响应内容
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取主题文件内容失败: %v", err)
	}

	// 检查文件大小
	if len(data) == 0 {
		return nil, errors.New("下载的主题文件为空")
	}

	return data, nil
}

// getGitHubReleaseDownloadURL 从GitHub API获取最新release的下载链接
func getGitHubReleaseDownloadURL(owner, repo string) (string, error) {
	if owner == "" || repo == "" {
		return "", errors.New("GitHub仓库所有者和仓库名称不能为空")
	}

	// 构建GitHub API URL
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)

	// 发送HTTP GET请求
	resp, err := themeHttpClient.Get(apiURL)
	if err != nil {
		return "", fmt.Errorf("获取GitHub release信息失败: %v", err)
	}
	defer resp.Body.Close()

	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("获取GitHub release信息失败，HTTP状态码: %d", resp.StatusCode)
	}

	// 解析JSON响应
	var releaseInfo struct {
		Assets []struct {
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&releaseInfo); err != nil {
		return "", fmt.Errorf("解析GitHub API响应失败: %v", err)
	}

	// 检查是否有可下载的资源
	if len(releaseInfo.Assets) == 0 {
		return "", errors.New("GitHub release中没有可下载的资源")
	}

	return releaseInfo.Assets[0].BrowserDownloadURL, nil
}

// isGitHubRepoURL 检查URL是否是GitHub仓库地址
// 支持的格式:
// - https://github.com/owner/repo
// - https://github.com/owner/repo.git
// - https://www.github.com/owner/repo
// - http://github.com/owner/repo
// 返回:
//   - 是否是GitHub仓库URL
//   - 仓库所有者
//   - 仓库名称
func isGitHubRepoURL(urlStr string) (bool, string, string) {
	if urlStr == "" {
		return false, "", ""
	}

	// 检查URL是否包含github.com
	if !strings.Contains(strings.ToLower(urlStr), "github.com") {
		return false, "", ""
	}

	// 解析URL
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return false, "", ""
	}

	// 检查主机名是否是github.com或www.github.com
	hostname := strings.ToLower(parsedURL.Host)
	if hostname != "github.com" && hostname != "www.github.com" {
		return false, "", ""
	}

	// 解析路径部分，提取owner和repo
	// 路径格式应该是 /owner/repo 或 /owner/repo.git
	path := strings.TrimPrefix(parsedURL.Path, "/")
	parts := strings.Split(path, "/")

	if len(parts) < 2 {
		return false, "", ""
	}

	owner := parts[0]
	repo := parts[1]

	// 如果repo以.git结尾，去掉这个后缀
	repo = strings.TrimSuffix(repo, ".git")

	return true, owner, repo
}

// UpdateTheme 更新主题
// 支持四种更新方式：
// 1. 使用主题原有URL下载更新
// 2. 提供新的直接下载URL进行更新
// 3. 提供GitHub仓库信息，从最新release下载更新
// 4. 如果主题URL是GitHub仓库地址，自动获取最新release
func UpdateTheme(c *gin.Context) {
	api.RespondError(c, http.StatusForbidden, "为了系统安全，远程主题在线下载与更新功能已被禁用。请通过本地重新上传主题 ZIP 包进行更新。")
}

// peekThemeFromZip 仅从ZIP文件中读取komari-theme.json并解析主题信息
// 不执行解压安装，用于preview模式
func peekThemeFromZip(zipPath string) (models.Theme, error) {
	var themeInfo models.Theme

	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return themeInfo, fmt.Errorf("无法打开ZIP文件: %v", err)
	}
	defer r.Close()

	themeConfigFile := findThemeArchiveFile(r.File, themeArchiveConfigPath)

	if themeConfigFile == nil {
		return themeInfo, fmt.Errorf("主题配置文件 komari-theme.json 不存在，不是合法的主题包")
	}

	rc, err := themeConfigFile.Open()
	if err != nil {
		return themeInfo, fmt.Errorf("无法读取主题配置文件: %v", err)
	}
	defer rc.Close()

	configData, err := io.ReadAll(rc)
	if err != nil {
		return themeInfo, fmt.Errorf("读取主题配置失败: %v", err)
	}

	if err := json.Unmarshal(configData, &themeInfo); err != nil {
		return themeInfo, fmt.Errorf("主题配置格式错误: %v", err)
	}

	if themeInfo.Name == "" || themeInfo.Short == "" {
		return themeInfo, fmt.Errorf("主题配置缺少必填字段（name、short）")
	}

	if !isValidThemeShort(themeInfo.Short) {
		return themeInfo, fmt.Errorf("主题short字段格式无效，只允许字母、数字、下划线和连字符")
	}

	return themeInfo, nil
}

// ImportTheme 导入远程主题
// 支持preview查询参数：preview=true时仅返回主题信息，否则下载安装
// 请求body: {"url": "https://..."}
// URL支持GitHub仓库地址（自动取latest release）和直接ZIP下载链接
func ImportTheme(c *gin.Context) {
	api.RespondError(c, http.StatusForbidden, "为了系统安全，远程主题在线下载与导入功能已被禁用。请通过本地上传主题 ZIP 包进行更新。")
}

func UpdateThemeSettings(c *gin.Context) {
	theme := c.Query("theme")
	if theme == "" {
		api.RespondError(c, http.StatusBadRequest, "主题名称不能为空")
		return
	}

	var req map[string]any

	err := c.ShouldBindJSON(&req)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}
	db := dbcore.GetDBInstance()

	data, err := json.Marshal(&req)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "生成主题配置失败: "+err.Error())
		return
	}

	var themeCfg models.ThemeConfiguration
	db.Where("short = ?", theme).
		Assign(models.ThemeConfiguration{Short: theme, Data: string(data)}).
		FirstOrCreate(&themeCfg)
	api.RespondSuccess(c, nil)
}
