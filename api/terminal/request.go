package terminal

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/utils"
	"github.com/komari-monitor/komari/ws"
)

func RequestTerminal(c *gin.Context) {
	uuid := c.Param("uuid")
	user_uuid, _ := c.Get("uuid")
	_, err := clients.GetClientByUUID(uuid)
	if err != nil {
		c.JSON(400, gin.H{
			"status":  "error",
			"message": "Client not found",
		})
		return
	}
	// 建立ws
	if !websocket.IsWebSocketUpgrade(c.Request) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Require WebSocket upgrade"})
		return
	}
	upgrader := websocket.Upgrader{
		CheckOrigin: ws.CheckOrigin,
	}
	unsafeConn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	conn := ws.NewSafeConnWithConfig(unsafeConn, ws.ConnConfig{ReadLimit: 256 << 10, QueueCapacity: 128})
	// 新建一个终端连接
	id := utils.GenerateRandomString(32)
	session := &TerminalSession{
		UserUUID:    user_uuid.(string),
		UUID:        uuid,
		Browser:     conn,
		Agent:       nil,
		RequesterIp: c.ClientIP(),
	}

	TerminalSessionsMutex.Lock()
	TerminalSessions[id] = session
	TerminalSessionsMutex.Unlock()
	conn.SetCloseHandler(func(code int, text string) error {
		log.Println("Terminal connection closed:", code, text)
		removeTerminalSession(id, session)
		session.close()
		return nil
	})

	agentConnection, online := ws.GetConnectedClient(uuid)
	if !online || agentConnection == nil {
		_ = conn.WriteMessageAndWait(websocket.TextMessage, []byte("Client offline!\n被控端离线!\n"))
		session.close()
		removeTerminalSession(id, session)
		return
	}
	err = agentConnection.WriteJSON(gin.H{
		"message":    "terminal",
		"request_id": id,
	})
	if err != nil {
		session.close()
		removeTerminalSession(id, session)
		return
	}
	_ = conn.WriteMessage(websocket.TextMessage, []byte("等待被控端连接 waiting for agent...\n"))
	// 如果没有连接上，则关闭连接
	time.AfterFunc(30*time.Second, func() {
		browser, agent := session.connections()
		if agent == nil {
			if browser != nil {
				_ = browser.WriteMessageAndWait(websocket.TextMessage, []byte("被控端连接超时 timeout\n"))
			}
			session.close()
			removeTerminalSession(id, session)
		}
	})
	//auditlog.Log(c.ClientIP(), user_uuid.(string), "request, terminal id:"+id+",client:"+session.UUID, "terminal")
}
