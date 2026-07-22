package cmd

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestProductionHTTPServerLimits(t *testing.T) {
	server := newHTTPServer("127.0.0.1:0", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), productionHTTPServerLimits())
	if server.ReadHeaderTimeout != 5*time.Second || server.ReadTimeout != 5*time.Minute || server.WriteTimeout != 5*time.Minute || server.IdleTimeout != 90*time.Second {
		t.Fatalf("unexpected timeouts: %#v", server)
	}
	if server.MaxHeaderBytes != 64<<10 {
		t.Fatalf("MaxHeaderBytes=%d", server.MaxHeaderBytes)
	}
}

func TestHTTPServerDropsSlowlorisHeaders(t *testing.T) {
	limits := productionHTTPServerLimits()
	limits.ReadHeaderTimeout = 40 * time.Millisecond
	limits.ReadTimeout = 100 * time.Millisecond
	limits.WriteTimeout = 100 * time.Millisecond
	server := newHTTPServer("", http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}), limits)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		<-done
	})

	connection, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := io.WriteString(connection, "GET / HTTP/1.1\r\nHost: localhost\r\nX-Slow: "); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := connection.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(connection)
	if elapsed := time.Since(started); elapsed >= 400*time.Millisecond {
		t.Fatalf("partial headers occupied connection for %s", elapsed)
	}
}

func TestHTTPServerGracefulShutdownDrainsActiveRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := newHTTPServer("", http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = writer.Write([]byte("complete"))
	}), productionHTTPServerLimits())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	responseDone := make(chan error, 1)
	go func() {
		response, requestErr := http.Get("http://" + listener.Addr().String())
		if requestErr != nil {
			responseDone <- requestErr
			return
		}
		defer response.Body.Close()
		_, requestErr = io.ReadAll(response.Body)
		responseDone <- requestErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		shutdownDone <- server.Shutdown(ctx)
	}()
	close(release)
	if err := <-responseDone; err != nil {
		t.Fatal(err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	if err := <-serveDone; err != http.ErrServerClosed {
		t.Fatalf("Serve error=%v", err)
	}
}
