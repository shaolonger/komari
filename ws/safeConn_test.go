package ws

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakeWebSocket struct {
	mu             sync.Mutex
	readLimit      int64
	readDeadline   time.Time
	writeDeadline  time.Time
	pongHandler    func(string) error
	closeHandler   func(int, string) error
	writeStarted   chan struct{}
	releaseWrite   chan struct{}
	closed         chan struct{}
	closeOnce      sync.Once
	closeCalls     atomic.Int32
	controlCalls   atomic.Int32
	failHeartbeats atomic.Bool
}

func newFakeWebSocket(blockWrites bool) *fakeWebSocket {
	fake := &fakeWebSocket{
		writeStarted: make(chan struct{}, 8),
		closed:       make(chan struct{}),
	}
	if blockWrites {
		fake.releaseWrite = make(chan struct{})
	}
	return fake
}

func (fake *fakeWebSocket) ReadMessage() (int, []byte, error) {
	<-fake.closed
	return 0, nil, io.EOF
}
func (fake *fakeWebSocket) ReadJSON(any) error { <-fake.closed; return io.EOF }
func (fake *fakeWebSocket) WriteMessage(int, []byte) error {
	select {
	case fake.writeStarted <- struct{}{}:
	default:
	}
	if fake.releaseWrite == nil {
		return nil
	}
	select {
	case <-fake.releaseWrite:
		return nil
	case <-fake.closed:
		return io.ErrClosedPipe
	}
}
func (fake *fakeWebSocket) WriteControl(messageType int, _ []byte, _ time.Time) error {
	fake.controlCalls.Add(1)
	if messageType == websocket.PingMessage && fake.failHeartbeats.Load() {
		return io.ErrClosedPipe
	}
	return nil
}
func (fake *fakeWebSocket) SetReadDeadline(deadline time.Time) error {
	fake.mu.Lock()
	fake.readDeadline = deadline
	fake.mu.Unlock()
	return nil
}
func (fake *fakeWebSocket) SetWriteDeadline(deadline time.Time) error {
	fake.mu.Lock()
	fake.writeDeadline = deadline
	fake.mu.Unlock()
	return nil
}
func (fake *fakeWebSocket) SetReadLimit(limit int64) { fake.readLimit = limit }
func (fake *fakeWebSocket) SetPongHandler(handler func(string) error) {
	fake.pongHandler = handler
}
func (fake *fakeWebSocket) SetCloseHandler(handler func(int, string) error) {
	fake.closeHandler = handler
}
func (fake *fakeWebSocket) Close() error {
	fake.closeCalls.Add(1)
	fake.closeOnce.Do(func() { close(fake.closed) })
	return nil
}

func TestSafeConnDisconnectsSlowConsumerWithoutBlocking(t *testing.T) {
	fake := newFakeWebSocket(true)
	connection := newSafeConn(fake, ConnConfig{QueueCapacity: 1, PingPeriod: -1, WriteWait: time.Second})
	if err := connection.WriteMessage(websocket.TextMessage, []byte("first")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fake.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}
	if err := connection.WriteMessage(websocket.TextMessage, []byte("queued")); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := connection.WriteMessage(websocket.TextMessage, []byte("overflow")); !errors.Is(err, ErrSlowConsumer) {
		t.Fatalf("overflow error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("slow-consumer isolation blocked for %s", elapsed)
	}
	select {
	case <-connection.Done():
	case <-time.After(time.Second):
		t.Fatal("slow connection remained open")
	}
}

func TestSafeConnHeartbeatDetectsHalfOpenPeer(t *testing.T) {
	fake := newFakeWebSocket(false)
	fake.failHeartbeats.Store(true)
	connection := newSafeConn(fake, ConnConfig{PingPeriod: time.Millisecond, PongWait: 10 * time.Millisecond})
	select {
	case <-connection.Done():
	case <-time.After(time.Second):
		t.Fatal("failed heartbeat did not close connection")
	}
}

func TestSafeConnAppliesLimitsDeadlinesAndPongRefresh(t *testing.T) {
	fake := newFakeWebSocket(false)
	connection := newSafeConn(fake, ConnConfig{
		ReadLimit: 1234, WriteWait: 75 * time.Millisecond, PongWait: 150 * time.Millisecond, PingPeriod: -1,
	})
	defer connection.Close()
	if fake.readLimit != 1234 {
		t.Fatalf("read limit=%d", fake.readLimit)
	}
	fake.mu.Lock()
	firstReadDeadline := fake.readDeadline
	fake.mu.Unlock()
	if fake.pongHandler == nil {
		t.Fatal("pong handler not installed")
	}
	time.Sleep(time.Millisecond)
	if err := fake.pongHandler("ok"); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	refreshed := fake.readDeadline
	fake.mu.Unlock()
	if !refreshed.After(firstReadDeadline) {
		t.Fatalf("pong did not refresh deadline: first=%s refreshed=%s", firstReadDeadline, refreshed)
	}
	if err := connection.WriteMessage(websocket.TextMessage, []byte("payload")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fake.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("write was not processed")
	}
	fake.mu.Lock()
	writeDeadline := fake.writeDeadline
	fake.mu.Unlock()
	remaining := time.Until(writeDeadline)
	if remaining <= 0 || remaining > 100*time.Millisecond {
		t.Fatalf("unexpected write deadline: %s", remaining)
	}
}

func TestSafeConnConcurrentCloseIsIdempotent(t *testing.T) {
	fake := newFakeWebSocket(false)
	connection := newSafeConn(fake, ConnConfig{PingPeriod: -1})
	var callers sync.WaitGroup
	for index := 0; index < 100; index++ {
		callers.Add(1)
		go func() {
			defer callers.Done()
			_ = connection.Close()
		}()
	}
	callers.Wait()
	if calls := fake.closeCalls.Load(); calls != 1 {
		t.Fatalf("underlying Close called %d times", calls)
	}
	if err := connection.WriteJSON(map[string]string{"late": "message"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("post-close write error=%v", err)
	}
}
