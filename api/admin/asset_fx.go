package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/assetfx"
)

func RefreshAssetFxSnapshot(c *gin.Context) {
	snapshot, err := assetfx.RefreshSnapshot()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"status":  "success",
			"data":    snapshot,
			"warning": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "success",
		"data":   snapshot,
	})
}
