package ws

import (
	"encoding/json"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/internal/observability"
)

const (
	DefaultReadLimit     = int64(1 << 20)
	DefaultWriteWait     = 10 * time.Second
	DefaultPongWait      = 60 * time.Second
	DefaultPingPeriod    = 25 * time.Second
	DefaultQueueCapacity = 64
	MaxQueueCapacity     = 1_024
)

var (
	ErrClosed       = errors.New("websocket connection closed")
	ErrSlowConsumer = errors.New("websocket send queue full")
)

// ConnConfig bounds every per-connection resource. Zero values select secure
// production defaults; negative PingPeriod disables server heartbeats in
// focused tests only.
type ConnConfig struct {
	ReadLimit     int64
	WriteWait     time.Duration
	PongWait      time.Duration
	PingPeriod    time.Duration
	QueueCapacity int
}

type websocketConn interface {
	ReadMessage() (int, []byte, error)
	ReadJSON(any) error
	WriteMessage(int, []byte) error
	WriteControl(int, []byte, time.Time) error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
	SetReadLimit(int64)
	SetPongHandler(func(string) error)
	SetCloseHandler(func(int, string) error)
	Close() error
}

type outboundMessage struct {
	messageType int
	data        []byte
	result      chan error
}

// SafeConn serializes writes through a bounded, connection-local queue. A
// stalled peer can consume at most QueueCapacity messages and one writer
// goroutine; it cannot block or allocate goroutines for any other connection.
type SafeConn struct {
	conn       websocketConn
	config     ConnConfig
	send       chan outboundMessage
	done       chan struct{}
	writerDone chan struct{}
	closeOnce  sync.Once
	closeErr   error
	closed     atomic.Bool
	ID         int64
}

func NewSafeConn(conn *websocket.Conn) *SafeConn {
	return NewSafeConnWithConfig(conn, ConnConfig{})
}

func NewSafeConnWithConfig(conn *websocket.Conn, config ConnConfig) *SafeConn {
	return newSafeConn(conn, config)
}

func newSafeConn(conn websocketConn, config ConnConfig) *SafeConn {
	config = normalizeConnConfig(config)
	connection := &SafeConn{
		conn:       conn,
		config:     config,
		send:       make(chan outboundMessage, config.QueueCapacity),
		done:       make(chan struct{}),
		writerDone: make(chan struct{}),
		ID:         time.Now().UnixNano(),
	}
	conn.SetReadLimit(config.ReadLimit)
	_ = conn.SetReadDeadline(time.Now().Add(config.PongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(config.PongWait))
	})
	go connection.writeLoop()
	return connection
}

func normalizeConnConfig(config ConnConfig) ConnConfig {
	if config.ReadLimit <= 0 {
		config.ReadLimit = DefaultReadLimit
	}
	if config.WriteWait <= 0 {
		config.WriteWait = DefaultWriteWait
	}
	if config.PongWait <= 0 {
		config.PongWait = DefaultPongWait
	}
	if config.PingPeriod == 0 {
		config.PingPeriod = DefaultPingPeriod
	}
	if config.PingPeriod >= config.PongWait {
		config.PingPeriod = config.PongWait / 2
	}
	if config.QueueCapacity <= 0 {
		config.QueueCapacity = DefaultQueueCapacity
	}
	if config.QueueCapacity > MaxQueueCapacity {
		config.QueueCapacity = MaxQueueCapacity
	}
	return config
}

func (connection *SafeConn) WriteMessage(messageType int, data []byte) error {
	payload := append([]byte(nil), data...)
	return connection.enqueue(outboundMessage{messageType: messageType, data: payload})
}

// WriteMessageAndWait is reserved for a final protocol notice immediately
// followed by Close. Ordinary traffic must use WriteMessage so slow peers stay
// isolated from their producers.
func (connection *SafeConn) WriteMessageAndWait(messageType int, data []byte) error {
	result := make(chan error, 1)
	payload := append([]byte(nil), data...)
	if err := connection.enqueue(outboundMessage{messageType: messageType, data: payload, result: result}); err != nil {
		return err
	}
	timer := time.NewTimer(connection.config.WriteWait)
	defer timer.Stop()
	select {
	case err := <-result:
		return err
	case <-connection.done:
		return ErrClosed
	case <-timer.C:
		connection.terminate(websocket.ClosePolicyViolation, "write timeout", true)
		return ErrSlowConsumer
	}
}

func (connection *SafeConn) WriteJSON(value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return connection.enqueue(outboundMessage{messageType: websocket.TextMessage, data: payload})
}

func (connection *SafeConn) enqueue(message outboundMessage) error {
	if connection.closed.Load() {
		return ErrClosed
	}
	select {
	case <-connection.done:
		return ErrClosed
	case connection.send <- message:
		return nil
	default:
		connection.terminate(websocket.ClosePolicyViolation, "slow consumer", true)
		return ErrSlowConsumer
	}
}

func (connection *SafeConn) writeLoop() {
	defer close(connection.writerDone)
	var ping <-chan time.Time
	var ticker *time.Ticker
	if connection.config.PingPeriod > 0 {
		ticker = time.NewTicker(connection.config.PingPeriod)
		defer ticker.Stop()
		ping = ticker.C
	}
	for {
		select {
		case <-connection.done:
			return
		case message := <-connection.send:
			err := connection.write(message.messageType, message.data)
			if message.result != nil {
				message.result <- err
			}
			if err != nil {
				connection.terminate(websocket.CloseAbnormalClosure, "write failed", isTimeout(err))
				return
			}
		case <-ping:
			deadline := time.Now().Add(connection.config.WriteWait)
			if err := connection.conn.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
				connection.terminate(websocket.CloseAbnormalClosure, "heartbeat failed", false)
				return
			}
		}
	}
}

func (connection *SafeConn) write(messageType int, data []byte) error {
	if err := connection.conn.SetWriteDeadline(time.Now().Add(connection.config.WriteWait)); err != nil {
		return err
	}
	return connection.conn.WriteMessage(messageType, data)
}

func (connection *SafeConn) Close() error {
	connection.terminate(websocket.CloseNormalClosure, "", false)
	return connection.closeErr
}

func (connection *SafeConn) terminate(code int, reason string, slow bool) {
	connection.closeOnce.Do(func() {
		connection.closed.Store(true)
		close(connection.done)
		if slow {
			observability.WSSlowConsumer()
		}
		// A slow writer may already own Gorilla's write lock. Closing the
		// transport is the only non-blocking way to isolate it; graceful closes
		// get a deliberately short control-frame deadline.
		if !slow && code == websocket.CloseNormalClosure {
			closeWait := min(connection.config.WriteWait, 250*time.Millisecond)
			_ = connection.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(closeWait))
		}
		connection.closeErr = connection.conn.Close()
	})
}

func isTimeout(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func (connection *SafeConn) ReadMessage() (int, []byte, error) {
	if connection.closed.Load() {
		return 0, nil, ErrClosed
	}
	_ = connection.conn.SetReadDeadline(time.Now().Add(connection.config.PongWait))
	return connection.conn.ReadMessage()
}

func (connection *SafeConn) ReadJSON(value any) error {
	if connection.closed.Load() {
		return ErrClosed
	}
	_ = connection.conn.SetReadDeadline(time.Now().Add(connection.config.PongWait))
	return connection.conn.ReadJSON(value)
}

func (connection *SafeConn) SetReadDeadline(deadline time.Time) error {
	return connection.conn.SetReadDeadline(deadline)
}

func (connection *SafeConn) SetCloseHandler(handler func(int, string) error) {
	connection.conn.SetCloseHandler(handler)
}

func (connection *SafeConn) Done() <-chan struct{} { return connection.done }

func (connection *SafeConn) GetConn() *websocket.Conn {
	conn, _ := connection.conn.(*websocket.Conn)
	return conn
}
