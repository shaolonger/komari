package admin

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
)

// 只有一个备份恢复操作在进行
var restoreMutex sync.Mutex

// UploadBackup 用于接收上传的备份文件并将其内容恢复到原始位置
func UploadBackup(c *gin.Context) {
	// 尝试获取锁，如果已有恢复操作在进行，则立即返回错误
	if !restoreMutex.TryLock() {
		api.RespondError(c, http.StatusConflict, "Another restore operation is already in progress")
		return
	}
	defer restoreMutex.Unlock()

	// 获取上传的文件
	file, header, err := c.Request.FormFile("backup")
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, fmt.Sprintf("Error getting uploaded file: %v", err))
		return
	}
	defer file.Close()

	// 检查文件是否为zip格式
	if !strings.HasSuffix(strings.ToLower(header.Filename), ".zip") {
		api.RespondError(c, http.StatusBadRequest, "Uploaded file must be a ZIP archive")
		return
	}

	// P1-08 安全整改：对备份上传文件大小限制为最大 100MB，防止大文件耗尽磁盘
	const MaxBackupSize = 100 * 1024 * 1024 // 100MB
	if header.Size > MaxBackupSize {
		api.RespondError(c, http.StatusRequestEntityTooLarge, "Backup file size exceeds the 100MB limit")
		return
	}

	// 确保data目录存在
	if err := os.MkdirAll("./data", 0755); err != nil {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error creating data directory: %v", err))
		return
	}

	// 创建临时文件保存上传的zip（先校验，再落地到固定位置）
	tempFile, err := os.CreateTemp("", "backup-upload-*.zip")
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error creating temporary file: %v", err))
		return
	}
	tempFilePath := tempFile.Name()
	defer os.Remove(tempFilePath) // 确保临时文件最终被删除

	// 将上传的文件内容复制到临时文件，并加 LimitReader 限制以防篡改或 chunked 漏洞绕过
	limitedReader := io.LimitReader(file, MaxBackupSize+1)
	written, err := io.Copy(tempFile, limitedReader)
	if err != nil {
		tempFile.Close()
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error saving uploaded file: %v", err))
		return
	}
	if written > MaxBackupSize {
		tempFile.Close()
		api.RespondError(c, http.StatusRequestEntityTooLarge, "Backup file size exceeds the 100MB limit")
		return
	}
	tempFile.Close() // 关闭文件以便后续操作

	// P1-08 安全整改：在打开并解析 ZIP 时执行文件数与解压后体积校验，防止 Zip Bomb 攻击
	const (
		MaxFileCount      = 1000
		MaxSingleFileSize = 100 * 1024 * 1024 // 单个文件 100MB
		MaxTotalUnzipSize = 300 * 1024 * 1024 // 总解压 300MB
	)

	if zr, err := zip.OpenReader(tempFilePath); err == nil {
		defer zr.Close()

		if len(zr.File) > MaxFileCount {
			api.RespondError(c, http.StatusBadRequest, "Invalid backup: file count inside zip exceeds 1000 limit")
			return
		}

		var totalUnzipSize int64
		hasMarkup := false

		for _, f := range zr.File {
			if f.Name == "komari-backup-markup" {
				hasMarkup = true
			}
			if f.FileInfo().IsDir() {
				continue
			}
			if f.UncompressedSize64 > uint64(MaxSingleFileSize) {
				api.RespondError(c, http.StatusBadRequest, fmt.Sprintf("Invalid backup: single file uncompressed size exceeds limit: %s", f.Name))
				return
			}
			totalUnzipSize += int64(f.UncompressedSize64)
		}

		if !hasMarkup {
			api.RespondError(c, http.StatusBadRequest, "Invalid backup file: missing komari-backup-markup file")
			return
		}

		if totalUnzipSize > MaxTotalUnzipSize {
			api.RespondError(c, http.StatusBadRequest, "Invalid backup: total uncompressed size exceeds 300MB limit")
			return
		}
	} else {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error opening zip file: %v", err))
		return
	}

	// 将校验通过的临时文件移动到固定路径 ./data/backup.zip
	finalPath := filepath.Join(".", "data", "backup.zip")
	// 如存在旧文件，先删除
	_ = os.Remove(finalPath)
	if err := os.Rename(tempFilePath, finalPath); err != nil {
		// fallback：拷贝
		in, err2 := os.Open(tempFilePath)
		if err2 != nil {
			api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error preparing backup file: %v", err))
			return
		}
		defer in.Close()
		out, err2 := os.Create(finalPath)
		if err2 != nil {
			api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error creating target backup file: %v", err2))
			return
		}
		if _, err2 = io.Copy(out, in); err2 != nil {
			out.Close()
			api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("Error writing target backup file: %v", err2))
			return
		}
		out.Close()
	}

	// 返回：已保存备份，重启后将自动恢复
	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "Backup uploaded successfully. The service will restart and apply the backup.",
		"path":    "./data/backup.zip",
	})

	go func() {
		log.Println("Backup uploaded, restarting service in 2 seconds to apply on startup...")
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}()
}
