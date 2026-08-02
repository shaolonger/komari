package terminal

import (
	"sync"

	"github.com/komari-monitor/komari/ws"
)

type TerminalSession struct {
	UUID        string
	UserUUID    string
	Browser     *ws.SafeConn
	Agent       *ws.SafeConn
	RequesterIp string
	mu          sync.RWMutex
	closed      bool
}

func (session *TerminalSession) connections() (*ws.SafeConn, *ws.SafeConn) {
	session.mu.RLock()
	defer session.mu.RUnlock()
	return session.Browser, session.Agent
}

func (session *TerminalSession) attachAgent(agent *ws.SafeConn) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed || session.Agent != nil {
		return false
	}
	session.Agent = agent
	return true
}

func (session *TerminalSession) close() {
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return
	}
	session.closed = true
	browser, agent := session.Browser, session.Agent
	session.mu.Unlock()
	if agent != nil {
		_ = agent.Close()
	}
	if browser != nil {
		_ = browser.Close()
	}
}

func getTerminalSession(id string) (*TerminalSession, bool) {
	TerminalSessionsMutex.RLock()
	defer TerminalSessionsMutex.RUnlock()
	session, exists := TerminalSessions[id]
	return session, exists
}

func removeTerminalSession(id string, expected *TerminalSession) {
	TerminalSessionsMutex.Lock()
	defer TerminalSessionsMutex.Unlock()
	if current := TerminalSessions[id]; current == expected {
		delete(TerminalSessions, id)
	}
}

var TerminalSessionsMutex = &sync.RWMutex{}
var TerminalSessions = make(map[string]*TerminalSession)
