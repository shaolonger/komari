package public

import (
	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/database/assetfx"
)

func GetAssetFxSnapshot(c *gin.Context) {
	snapshot, err := assetfx.GetSnapshot()
	if err != nil || snapshot.UpdatedAt.IsZero() || len(snapshot.Rates) <= 1 {
		refreshed, refreshErr := assetfx.RefreshSnapshot()
		if refreshErr == nil {
			snapshot = refreshed
		} else if len(snapshot.Rates) == 0 {
			api.RespondError(c, 502, "Failed to load asset FX snapshot: "+refreshErr.Error())
			return
		}
	}
	api.RespondSuccess(c, snapshot)
}
