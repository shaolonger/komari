package ws

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/common"
)

var (
	connectedClients  = make(map[string]*SafeConn)
	ConnectedUsers    = []*websocket.Conn{}
	latestReport      = make(map[string]common.Report)
	telemetryProtocol = make(map[string]uint8)
	// presenceOnly stores online state for non-WebSocket agents (e.g., Nezha gRPC)
	// value keeps connectionID and a soft expiration to avoid flicker
	presenceOnly = make(map[string]struct {
		id     int64
		expire time.Time
	})
	mu = sync.RWMutex{}
)

func GetConnectedClients() map[string]*SafeConn {
	mu.RLock()
	defer mu.RUnlock()
	clientsCopy := make(map[string]*SafeConn)
	for k, v := range connectedClients {
		clientsCopy[k] = v
	}
	return clientsCopy
}

func GetConnectedClient(uuid string) (*SafeConn, bool) {
	mu.RLock()
	defer mu.RUnlock()
	connection, ok := connectedClients[uuid]
	return connection, ok
}

func SetConnectedClients(uuid string, conn *SafeConn) {
	mu.Lock()
	defer mu.Unlock()
	wasOnline := isOnlineLocked(uuid, time.Now())
	connectedClients[uuid] = conn
	if !wasOnline {
		appendDashboardChangeLocked(uuid, dashboardOnlineChange)
	}
}

func SetClientTelemetryProtocol(uuid string, conn *SafeConn, version uint8) {
	mu.Lock()
	defer mu.Unlock()
	if current, exists := connectedClients[uuid]; exists && current == conn {
		telemetryProtocol[uuid] = version
	}
}

func GetClientTelemetryProtocol(uuid string) uint8 {
	mu.RLock()
	defer mu.RUnlock()
	return telemetryProtocol[uuid]
}
func DeleteClientConditionally(uuid string, connToRemove *SafeConn) {
	mu.Lock()
	defer mu.Unlock()

	// 检查当前 map 里的 conn 是否就是要删除的这一个
	wasOnline := isOnlineLocked(uuid, time.Now())
	if currentConn, exists := connectedClients[uuid]; exists && currentConn == connToRemove {
		delete(connectedClients, uuid)
		delete(telemetryProtocol, uuid)
	}
	if wasOnline && !isOnlineLocked(uuid, time.Now()) {
		appendDashboardChangeLocked(uuid, dashboardOnlineChange)
	}
}
func DeleteConnectedClients(uuid string) {
	mu.Lock()
	defer mu.Unlock()
	wasOnline := isOnlineLocked(uuid, time.Now())
	// 只从 map 中删除，不再负责关闭连接
	delete(connectedClients, uuid)
	delete(telemetryProtocol, uuid)
	if wasOnline && !isOnlineLocked(uuid, time.Now()) {
		appendDashboardChangeLocked(uuid, dashboardOnlineChange)
	}
}

// CloseAllAgentConnections stops new telemetry before the persistence writer is drained.
func CloseAllAgentConnections() {
	mu.Lock()
	connections := make([]*SafeConn, 0, len(connectedClients))
	for uuid, connection := range connectedClients {
		connections = append(connections, connection)
		delete(connectedClients, uuid)
		delete(telemetryProtocol, uuid)
		if !isOnlineLocked(uuid, time.Now()) {
			appendDashboardChangeLocked(uuid, dashboardOnlineChange)
		}
	}
	mu.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
}

// SetPresence sets or clears presence for non-WebSocket agents.
// When present=false, it only clears if the connectionID matches current one.
// KeepAlivePresence sets presence with TTL for non-WebSocket agents.
func KeepAlivePresence(uuid string, connectionID int64, ttl time.Duration) {
	mu.Lock()
	defer mu.Unlock()
	now := time.Now()
	wasOnline := isOnlineLocked(uuid, now)
	presenceOnly[uuid] = struct {
		id     int64
		expire time.Time
	}{id: connectionID, expire: now.Add(ttl)}
	if !wasOnline {
		appendDashboardChangeLocked(uuid, dashboardOnlineChange)
	}
}

var defaultPresenceTTL = 20 * time.Second

// SetPresence keeps compatibility with existing callers.
func SetPresence(uuid string, connectionID int64, present bool) {
	mu.Lock()
	defer mu.Unlock()
	now := time.Now()
	wasOnline := isOnlineLocked(uuid, now)
	if present {
		presenceOnly[uuid] = struct {
			id     int64
			expire time.Time
		}{id: connectionID, expire: now.Add(defaultPresenceTTL)}
		if !wasOnline {
			appendDashboardChangeLocked(uuid, dashboardOnlineChange)
		}
		return
	}
	if cur, ok := presenceOnly[uuid]; ok && cur.id == connectionID {
		delete(presenceOnly, uuid)
	}
	if wasOnline && !isOnlineLocked(uuid, now) {
		appendDashboardChangeLocked(uuid, dashboardOnlineChange)
	}
}

// GetAllOnlineUUIDs returns a de-duplicated list of online UUIDs from both WebSocket and non-WebSocket agents.
func GetAllOnlineUUIDs() []string {
	mu.RLock()
	defer mu.RUnlock()
	return onlineUUIDsLocked(time.Now())
}
func GetLatestReport() map[string]*common.Report {
	mu.RLock()
	defer mu.RUnlock()
	reportCopy := make(map[string]*common.Report, len(latestReport))
	for uuid, report := range latestReport {
		cloned := cloneReport(report)
		reportCopy[uuid] = &cloned
	}
	return reportCopy
}
func SetLatestReport(uuid string, report *common.Report) {
	mu.Lock()
	defer mu.Unlock()
	if report == nil {
		if _, exists := latestReport[uuid]; exists {
			delete(latestReport, uuid)
			appendDashboardChangeLocked(uuid, dashboardReportChange)
		}
		return
	}
	latestReport[uuid] = cloneReport(*report)
	appendDashboardChangeLocked(uuid, dashboardReportChange)
}
func DeleteLatestReport(uuid string) {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := latestReport[uuid]; exists {
		delete(latestReport, uuid)
		appendDashboardChangeLocked(uuid, dashboardReportChange)
	}
}
