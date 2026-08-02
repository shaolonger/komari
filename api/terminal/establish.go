package terminal

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/ws"
)

func EstablishConnection(c *gin.Context) {
	session_id := c.Query("id")
	session, exists := getTerminalSession(session_id)
	if !exists || session == nil {
		c.JSON(404, gin.H{"status": "error", "error": "Session not found"})
		return
	}
	browser, _ := session.connections()
	if browser == nil {
		c.JSON(404, gin.H{"status": "error", "error": "Session not found"})
		return
	}
	// Upgrade the connection to WebSocket
	if !websocket.IsWebSocketUpgrade(c.Request) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "Require WebSocket upgrade"})
		return
	}
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // 被控
		},
	}
	unsafeConn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		session.close()
		removeTerminalSession(session_id, session)
		return
	}
	conn := ws.NewSafeConnWithConfig(unsafeConn, ws.ConnConfig{ReadLimit: 256 << 10, QueueCapacity: 128})
	if !session.attachAgent(conn) {
		_ = conn.Close()
		return
	}
	conn.SetCloseHandler(func(code int, text string) error {
		removeTerminalSession(session_id, session)
		session.close()
		return nil
	})
	go ForwardTerminal(session_id)
}
