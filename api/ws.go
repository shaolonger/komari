package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/config"
	"github.com/komari-monitor/komari/database/accounts"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/ws"
)

type dashboardRequest struct {
	Type  string `json:"type"`
	Since uint64 `json:"since"`
	UUID  string `json:"uuid,omitempty"`
}

type dashboardPayload struct {
	Online  []string                 `json:"online"`
	Offline []string                 `json:"offline,omitempty"`
	Data    map[string]common.Report `json:"data"`
	Removed []string                 `json:"removed,omitempty"`
}

type dashboardFrame struct {
	Status       string           `json:"status"`
	Type         string           `json:"type"`
	FromSequence uint64           `json:"from_sequence,omitempty"`
	Sequence     uint64           `json:"sequence"`
	Resync       bool             `json:"resync,omitempty"`
	Data         dashboardPayload `json:"data"`
}

func GetClients(c *gin.Context) {
	if !websocket.IsWebSocketUpgrade(c.Request) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "Require WebSocket upgrade"})
		return
	}
	allowCORS, _ := config.GetAs[bool](config.AllowCorsKey, false)
	upgrader := websocket.Upgrader{CheckOrigin: func(request *http.Request) bool {
		return allowCORS || ws.CheckOrigin(request)
	}}
	unsafeConn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "Failed to upgrade to WebSocket." + err.Error()})
		return
	}
	conn := ws.NewSafeConnWithConfig(unsafeConn, ws.ConnConfig{ReadLimit: 4 << 10, QueueCapacity: 8})
	defer conn.Close()

	session, _ := c.Cookie("session_token")
	_, sessionErr := accounts.GetUserBySession(session)
	isLogin := sessionErr == nil
	hidden := map[string]bool{}
	if !isLogin {
		hidden, err = loadHiddenDashboardClients()
		if err != nil {
			_ = conn.WriteJSON(gin.H{"status": "error", "error": "Unable to establish dashboard visibility"})
			return
		}
	}

	_, raw, err := conn.ReadMessage()
	if err != nil {
		return
	}
	message := strings.TrimSpace(string(raw))
	if message == "get" || strings.HasPrefix(message, "get ") {
		serveLegacyDashboard(conn, message, hidden, isLogin)
		return
	}
	var request dashboardRequest
	if err := json.Unmarshal(raw, &request); err != nil || (request.Type != "subscribe" && request.Type != "resync") {
		_ = conn.WriteJSON(gin.H{"status": "error", "error": "Invalid message"})
		return
	}
	serveDashboardSubscription(conn, request, hidden, isLogin)
}

func serveLegacyDashboard(conn *ws.SafeConn, firstMessage string, hidden map[string]bool, isLogin bool) {
	message := firstMessage
	for {
		uuid, valid := legacyDashboardUUID(message)
		if !valid {
			_ = conn.WriteJSON(gin.H{"status": "error", "error": "Invalid message"})
		} else {
			update, _ := ws.DashboardStateSince(0)
			filtered := filterDashboardUpdate(update, hidden, isLogin, uuid)
			if err := conn.WriteJSON(gin.H{"status": "success", "data": dashboardPayload{
				Online: filtered.Online,
				Data:   filtered.Reports,
			}}); err != nil {
				return
			}
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		message = strings.TrimSpace(string(raw))
	}
}

func legacyDashboardUUID(message string) (string, bool) {
	if message == "get" {
		return "", true
	}
	if !strings.HasPrefix(message, "get ") {
		return "", false
	}
	uuid := strings.TrimSpace(strings.TrimPrefix(message, "get "))
	return uuid, uuid != ""
}

func serveDashboardSubscription(conn *ws.SafeConn, initial dashboardRequest, hidden map[string]bool, isLogin bool) {
	requests := make(chan dashboardRequest, 1)
	readErrors := make(chan error, 1)
	go readDashboardRequests(conn, requests, readErrors)

	cursor := initial.Since
	delivered := initial.Since
	requestedUUID := strings.TrimSpace(initial.UUID)
	forceSnapshot := initial.Type == "resync"
	firstResponse := true
	for {
		querySince := cursor
		if forceSnapshot {
			querySince = 0
		}
		update, notify := ws.DashboardStateSince(querySince)
		cursor = update.Sequence
		filtered := filterDashboardUpdate(update, hidden, isLogin, requestedUUID)
		visible := update.Snapshot || firstResponse || dashboardUpdateHasChanges(filtered)
		if visible {
			frameType := "delta"
			if update.Snapshot {
				frameType = "snapshot"
			}
			frame := dashboardFrame{
				Status:       "success",
				Type:         frameType,
				FromSequence: delivered,
				Sequence:     update.Sequence,
				Resync:       update.Resync || forceSnapshot,
				Data: dashboardPayload{
					Online:  filtered.Online,
					Offline: filtered.Offline,
					Data:    filtered.Reports,
					Removed: filtered.Removed,
				},
			}
			if err := conn.WriteJSON(frame); err != nil {
				return
			}
			delivered = update.Sequence
		}
		firstResponse = false
		forceSnapshot = false

		select {
		case <-notify:
			continue
		case request := <-requests:
			if request.Type != "resync" && request.Type != "subscribe" {
				_ = conn.WriteJSON(gin.H{"status": "error", "error": "Invalid message"})
				continue
			}
			requestedUUID = strings.TrimSpace(request.UUID)
			if request.Type == "resync" {
				forceSnapshot = true
				cursor = 0
				delivered = 0
			} else {
				cursor = request.Since
				delivered = request.Since
				firstResponse = true
			}
		case <-readErrors:
			return
		case <-conn.Done():
			return
		}
	}
}

func readDashboardRequests(conn *ws.SafeConn, requests chan<- dashboardRequest, readErrors chan<- error) {
	for {
		var request dashboardRequest
		if err := conn.ReadJSON(&request); err != nil {
			select {
			case readErrors <- err:
			default:
			}
			return
		}
		select {
		case requests <- request:
		case <-conn.Done():
			return
		}
	}
}

func filterDashboardUpdate(update ws.DashboardUpdate, hidden map[string]bool, isLogin bool, onlyUUID string) ws.DashboardUpdate {
	filtered := ws.DashboardUpdate{
		Sequence: update.Sequence,
		Snapshot: update.Snapshot,
		Resync:   update.Resync,
		Reports:  make(map[string]common.Report),
	}
	allowed := func(uuid string) bool {
		return (isLogin || !hidden[uuid]) && (onlyUUID == "" || uuid == onlyUUID)
	}
	for uuid, report := range update.Reports {
		if !allowed(uuid) {
			continue
		}
		if report.GPU != nil {
			gpu := *report.GPU
			gpu.DetailedInfo = append([]common.GPUDeviceInfo(nil), report.GPU.DetailedInfo...)
			report.GPU = &gpu
		}
		report.UUID = ""
		if report.CPU.Usage == 0 {
			report.CPU.Usage = 0.01
		}
		filtered.Reports[uuid] = report
	}
	for _, uuid := range update.Online {
		if allowed(uuid) {
			filtered.Online = append(filtered.Online, uuid)
		}
	}
	for _, uuid := range update.Offline {
		if allowed(uuid) {
			filtered.Offline = append(filtered.Offline, uuid)
		}
	}
	for _, uuid := range update.Removed {
		if allowed(uuid) {
			filtered.Removed = append(filtered.Removed, uuid)
		}
	}
	return filtered
}

func dashboardUpdateHasChanges(update ws.DashboardUpdate) bool {
	return len(update.Reports) > 0 || len(update.Removed) > 0 || len(update.Online) > 0 || len(update.Offline) > 0
}

func loadHiddenDashboardClients() (map[string]bool, error) {
	var hiddenClients []models.Client
	if err := dbcore.GetDBInstance().Select("uuid").Where("hidden = ?", true).Find(&hiddenClients).Error; err != nil {
		return nil, err
	}
	hidden := make(map[string]bool, len(hiddenClients))
	for _, client := range hiddenClients {
		hidden[client.UUID] = true
	}
	return hidden, nil
}
