package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/internal/observability"
	"github.com/komari-monitor/komari/internal/telemetry"
	"github.com/komari-monitor/komari/protocol/telemetryv2"
	"github.com/komari-monitor/komari/utils/notifier"
	"github.com/komari-monitor/komari/ws"
)

const (
	// 如果超过这个时间没有收到任何消息，则认为连接已死
	// 因为目前server没有存agent的信息上报间隔。只有写一个默认的
	readWait = 11 * time.Second
)

// postPresenceEntry 保存单个客户端的 POST 上报会话状态
type postPresenceEntry struct {
	connID     int64
	timer      *time.Timer
	generation uint64 // 每次 Reset 递增，用于回调中判断是否为过期的旧回调
}

var (
	postPresenceMu     sync.Mutex
	postPresenceStates = make(map[string]*postPresenceEntry)
)

// refreshPostPresence 管理 HTTP POST 上报者的在线/离线状态。
// 每次 POST 刷新 TTL 定时器；定时器到期后触发离线通知。
func refreshPostPresence(uuid string) {
	postPresenceMu.Lock()
	defer postPresenceMu.Unlock()

	if entry, exists := postPresenceStates[uuid]; exists {
		// 已在线：递增 generation 使可能正在执行的旧回调失效
		entry.generation++
		entry.timer.Stop()
		// 重新创建 AfterFunc 以在闭包中捕获新的 generation
		gen := entry.generation
		entry.timer = time.AfterFunc(readWait, func() {
			postPresenceExpired(uuid, entry.connID, gen)
		})
		ws.KeepAlivePresence(uuid, entry.connID, readWait)
		return
	}

	// 新 POST 会话：生成 connID，标记在线，启动超时定时器
	connID := time.Now().UnixNano()
	ws.KeepAlivePresence(uuid, connID, readWait)
	go notifier.OnlineNotification(uuid, connID)

	defaultGeneration := uint64(0)

	entry := &postPresenceEntry{
		connID:     connID,
		generation: defaultGeneration,
	}

	entry.timer = time.AfterFunc(readWait, func() {
		postPresenceExpired(uuid, connID, defaultGeneration)
	})

	postPresenceStates[uuid] = entry
}

// postPresenceExpired 是定时器到期的回调。
// 只有当 connID 和 generation 都与当前 entry 匹配时才执行离线清理，
// 避免 timer.Reset 竞态导致过期回调错误地清除仍活跃的会话。
func postPresenceExpired(uuid string, connID int64, gen uint64) {
	postPresenceMu.Lock()
	e, ok := postPresenceStates[uuid]
	if !ok || e.connID != connID || e.generation != gen {
		postPresenceMu.Unlock()
		return
	}
	delete(postPresenceStates, uuid)
	postPresenceMu.Unlock()

	ws.SetPresence(uuid, connID, false)
	notifier.OfflineNotification(uuid, connID)
}

func UploadReport(c *gin.Context) {
	started := time.Now()
	accepted := false
	bodySize := 0
	defer func() { observability.ObserveReport(bodySize, time.Since(started), accepted) }()
	uuid, ok := authenticatedClientUUID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authenticated client identity is required"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, telemetryv2.MaxFrameSize)
	bodyBytes, err := io.ReadAll(c.Request.Body)
	bodySize = len(bodyBytes)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Report exceeds maximum size"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	var report common.Report
	if err := decodeAuthenticatedJSONReport(bodyBytes, uuid, &report); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	report.UpdatedAt = time.Now()

	err = SaveClientReport(uuid, report)
	if err != nil {
		if errors.Is(err, telemetry.ErrSampleLimit) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("%v", err)})
		return
	}
	accepted = true
	// Update report with method and token

	ws.SetLatestReport(uuid, &report)

	// POST 上报后标记节点在线，超时未收到新 POST 则触发离线
	refreshPostPresence(uuid)

	c.JSON(200, gin.H{"status": "success"})
}

func authenticatedClientUUID(c *gin.Context) (string, bool) {
	value, exists := c.Get("client_uuid")
	if !exists {
		return "", false
	}
	uuid, ok := value.(string)
	return uuid, ok && uuid != ""
}

func decodeAuthenticatedJSONReport(body []byte, uuid string, report *common.Report) error {
	*report = common.Report{}
	if err := json.Unmarshal(body, report); err != nil {
		return err
	}
	report.UUID = uuid
	return nil
}

func WebSocketReport(c *gin.Context) {
	uuid, ok := authenticatedClientUUID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"status": "error", "error": "Authenticated client identity is required"})
		return
	}
	// 升级ws
	if !websocket.IsWebSocketUpgrade(c.Request) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "Require WebSocket upgrade"})
		return
	}
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // 被控
		},
		Subprotocols: []string{telemetryv2.Subprotocol, telemetryv2.LegacySubprotocol},
	}
	// Upgrade the HTTP connection to a WebSocket connection
	unsafeConn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "Failed to upgrade to WebSocket." + err.Error()})
		return
	}
	wireProtocol, err := negotiatedTelemetryProtocol(unsafeConn.Subprotocol())
	if err != nil {
		_ = unsafeConn.Close()
		return
	}
	conn := ws.NewSafeConnWithConfig(unsafeConn, ws.ConnConfig{
		ReadLimit:     telemetryv2.MaxFrameSize,
		PongWait:      readWait,
		PingPeriod:    readWait / 2,
		QueueCapacity: 128,
	})
	defer conn.Close()

	messageType, message, err := conn.ReadMessage()
	if err != nil {
		log.Println("Error reading message:", err)
		return
	}
	// 接受新连接，并处理旧连接
	if oldConn, exists := ws.GetConnectedClient(uuid); exists {
		observability.WSReconnected()
		log.Printf("Client %s is reconnecting. Closing the old connection.", uuid)

		// 强制关闭旧连接。这将导致旧连接的 ReadMessage() 循环出错退出。
		_ = oldConn.Close()
	}
	ws.SetConnectedClients(uuid, conn)
	observability.WSConnected()
	log.Printf("Client %s is reconnect success, connID: %d, telemetry protocol: v%d", uuid, conn.ID, wireProtocol)
	go notifier.OnlineNotification(uuid, conn.ID)
	defer func() {
		observability.WSDisconnected()
		ws.DeleteClientConditionally(uuid, conn)
		notifier.OfflineNotification(uuid, conn.ID)
	}()

	// 首先处理第一次ws conn收到的消息
	processMessage(conn, messageType, message, uuid, wireProtocol)

	for {
		messageType, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("Client %s connection error: %v", uuid, err)
			}
			break // 任何读错误（包括超时）都意味着连接已断开，退出循环
		}
		processMessage(conn, messageType, message, uuid, wireProtocol)
	}
}

// 将消息处理逻辑提取到一个函数中，方便复用
func processMessage(conn *ws.SafeConn, messageType int, message []byte, uuid string, wireProtocol telemetryProtocol) {
	started := time.Now()
	accepted := false
	defer func() { observability.ObserveReport(len(message), time.Since(started), accepted) }()
	if messageType == websocket.BinaryMessage {
		if wireProtocol != telemetryProtocolV2 {
			_ = conn.WriteJSON(gin.H{"status": "error", "error": "Binary telemetry requires protocol v2"})
			return
		}
		report, err := decodeTelemetryV2Report(message)
		if err != nil {
			log.Printf("Rejected invalid telemetry v2 frame for client %s: %v", uuid, err)
			_ = conn.WriteJSON(gin.H{"status": "error", "error": "Invalid telemetry frame"})
			return
		}
		report.UUID = uuid
		report.UpdatedAt = time.Now()
		if err := SaveClientReport(uuid, report); err != nil {
			_ = conn.WriteJSON(gin.H{"status": "error", "error": fmt.Sprintf("%v", err)})
			return
		}
		ws.SetLatestReport(uuid, &report)
		accepted = true
		return
	}
	if messageType != websocket.TextMessage {
		_ = conn.WriteJSON(gin.H{"status": "error", "error": "Unsupported WebSocket message type"})
		return
	}
	var decoded agentMessage
	if err := decodeAgentMessage(message, &decoded); err != nil {
		conn.WriteJSON(gin.H{"status": "error", "error": "Invalid JSON"})
		return
	}

	switch decoded.Type {
	case "", "report":
		report := decoded.Report
		report.UUID = uuid
		report.UpdatedAt = time.Now()
		if err := SaveClientReport(uuid, report); err != nil {
			conn.WriteJSON(gin.H{"status": "error", "error": fmt.Sprintf("%v", err)})
			return
		}
		ws.SetLatestReport(uuid, &report)
		accepted = true
	case "ping_result":
		pingResult := models.PingRecord{
			Client: uuid,
			TaskId: decoded.PingTaskID,
			Value:  decoded.PingResult,
			Time:   models.FromTime(decoded.FinishedAt),
		}
		if err := tasks.SavePingRecord(pingResult); err != nil {
			// A ping can finish after its task has been removed. Do not recreate an
			// orphaned history row; the agent will receive the next schedule from
			// the refreshed task list.
			log.Printf("Discarded ping result for client %s, task %d: %v", uuid, decoded.PingTaskID, err)
		} else {
			accepted = true
		}
	default:
		log.Printf("Unknown message type: %s", decoded.Type)
		conn.WriteJSON(gin.H{"status": "error", "error": "Unknown message type"})
	}
}

type agentMessage struct {
	common.Report
	Type       string    `json:"type"`
	PingTaskID uint      `json:"task_id"`
	PingResult int       `json:"value"`
	PingType   string    `json:"ping_type"`
	FinishedAt time.Time `json:"finished_at"`
}

func decodeAgentMessage(message []byte, decoded *agentMessage) error {
	*decoded = agentMessage{}
	if err := json.Unmarshal(message, decoded); err != nil {
		return err
	}
	return nil
}

func SaveClientReport(uuid string, report common.Report) error {
	if report.CPU.Usage < 0.01 {
		report.CPU.Usage = 0.01
	}
	return api.Telemetry.Add(uuid, report)
}
