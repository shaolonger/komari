package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/auditlog"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/records"
	"github.com/komari-monitor/komari/ws"
)

var getClientTokenStatusFunc = clients.GetClientTokenStatusByUUID
var rotateClientTokenFunc = clients.RotateClientToken
var reissueClientTokenFunc = clients.ReissueClientToken
var revokeClientTokenFunc = clients.RevokeClientToken
var auditLogFunc = auditlog.Log
var saveClientFunc = clients.SaveClient

type clientTokenLifecycleRequest struct {
	ExpiresInHours int64 `json:"expires_in_hours"`
}

func writeClientTokenStatusResponse(c *gin.Context, status clients.ClientTokenStatus) {
	c.JSON(http.StatusOK, gin.H{
		"status":     "success",
		"token":      status.Token,
		"issued_at":  status.IssuedAt,
		"expires_at": status.ExpiresAt,
		"revoked_at": status.RevokedAt,
		"active":     status.Active,
	})
}

func parseClientTokenLifecycleRequest(c *gin.Context) (clientTokenLifecycleRequest, bool) {
	req := clientTokenLifecycleRequest{}
	if c.Request.ContentLength <= 0 {
		return req, true
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
		return req, false
	}
	if req.ExpiresInHours < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "expires_in_hours must be zero or positive"})
		return req, false
	}
	return req, true
}

func AddClient(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		uuid, token, err := clients.CreateClient()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "uuid": uuid, "token": token})
		return
	}
	uuid, token, err := clients.CreateClientWithName(req.Name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": err.Error()})
		return
	}
	user_uuid, _ := c.Get("uuid")
	auditLogFunc(c.ClientIP(), user_uuid.(string), "create client:"+uuid, "info")
	c.JSON(http.StatusOK, gin.H{"status": "success", "uuid": uuid, "token": token, "message": ""})
}

func EditClient(c *gin.Context) {
	var req = make(map[string]interface{})
	uuid := c.Param("uuid")
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
		return
	}
	if uuid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid or missing UUID"})
		return
	}
	req["uuid"] = uuid
	err := saveClientFunc(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": err.Error()})
		return
	}
	user_uuid, _ := c.Get("uuid")
	auditLogFunc(c.ClientIP(), user_uuid.(string), "edit client:"+uuid, "info")
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func BatchEditClientAssets(c *gin.Context) {
	var req struct {
		UUIDs   []string               `json:"uuids"`
		Changes map[string]interface{} `json:"changes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
		return
	}
	if len(req.UUIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "uuids is required"})
		return
	}
	if len(req.Changes) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "changes is required"})
		return
	}
	if _, exists := req.Changes["uuid"]; exists {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "uuid cannot be edited in batch"})
		return
	}

	seen := make(map[string]struct{}, len(req.UUIDs))
	updated := 0
	for _, uuid := range req.UUIDs {
		if uuid == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "uuids must not contain empty values"})
			return
		}
		if _, exists := seen[uuid]; exists {
			continue
		}
		seen[uuid] = struct{}{}

		payload := make(map[string]interface{}, len(req.Changes)+1)
		for key, value := range req.Changes {
			payload[key] = value
		}
		payload["uuid"] = uuid
		if err := saveClientFunc(payload); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"status":  "error",
				"message": err.Error(),
				"uuid":    uuid,
			})
			return
		}
		updated++
	}

	userUUID, _ := c.Get("uuid")
	auditLogFunc(c.ClientIP(), userUUID.(string), "batch edit clients", "info")
	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"updated": updated,
	})
}

func RemoveClient(c *gin.Context) {
	uuid := c.Param("uuid")
	err := clients.DeleteClient(uuid)
	if err != nil {
		c.JSON(500, gin.H{
			"status": "error",
			"error":  "Failed to delete client" + err.Error(),
		})
		return
	}
	user_uuid, _ := c.Get("uuid")
	auditLogFunc(c.ClientIP(), user_uuid.(string), "delete client:"+uuid, "warn")
	c.JSON(200, gin.H{"status": "success"})
	ws.DeleteConnectedClients(uuid)
	ws.DeleteLatestReport(uuid)
}

func ClearRecord(c *gin.Context) {
	if err := records.DeleteAll(); err != nil {
		c.JSON(500, gin.H{
			"status":  "error",
			"message": "Failed to delete Record" + err.Error(),
		})
		return
	}
	user_uuid, _ := c.Get("uuid")
	auditLogFunc(c.ClientIP(), user_uuid.(string), "clear records", "warn")
	c.JSON(200, gin.H{"status": "success"})
}

func GetClient(c *gin.Context) {
	uuid := c.Param("uuid")
	if uuid == "" {
		c.JSON(400, gin.H{
			"status":  "error",
			"message": "Invalid or missing UUID",
		})
		return
	}

	result, err := clients.GetClientByUUID(uuid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status":  "error",
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, result)
}

func ListClients(c *gin.Context) {
	cls, err := clients.GetAllClientBasicInfo()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, cls)
}

func GetClientToken(c *gin.Context) {
	uuid := c.Param("uuid")
	if uuid == "" {
		c.JSON(400, gin.H{
			"status":  "error",
			"message": "Invalid or missing UUID",
		})
		return
	}

	status, err := getClientTokenStatusFunc(uuid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": err.Error()})
		return
	}

	writeClientTokenStatusResponse(c, status)
}

func RotateClientToken(c *gin.Context) {
	uuid := c.Param("uuid")
	if uuid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid or missing UUID"})
		return
	}
	req, ok := parseClientTokenLifecycleRequest(c)
	if !ok {
		return
	}
	status, err := rotateClientTokenFunc(uuid, req.ExpiresInHours)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": err.Error()})
		return
	}
	userUUID, _ := c.Get("uuid")
	auditLogFunc(c.ClientIP(), userUUID.(string), "rotate client token:"+uuid, "warn")
	writeClientTokenStatusResponse(c, status)
}

func ReissueClientToken(c *gin.Context) {
	uuid := c.Param("uuid")
	if uuid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid or missing UUID"})
		return
	}
	req, ok := parseClientTokenLifecycleRequest(c)
	if !ok {
		return
	}
	status, err := reissueClientTokenFunc(uuid, req.ExpiresInHours)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": err.Error()})
		return
	}
	userUUID, _ := c.Get("uuid")
	auditLogFunc(c.ClientIP(), userUUID.(string), "reissue client token:"+uuid, "warn")
	writeClientTokenStatusResponse(c, status)
}

func RevokeClientToken(c *gin.Context) {
	uuid := c.Param("uuid")
	if uuid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Invalid or missing UUID"})
		return
	}
	status, err := revokeClientTokenFunc(uuid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "message": err.Error()})
		return
	}
	userUUID, _ := c.Get("uuid")
	auditLogFunc(c.ClientIP(), userUUID.(string), "revoke client token:"+uuid, "warn")
	writeClientTokenStatusResponse(c, status)
	return
}
