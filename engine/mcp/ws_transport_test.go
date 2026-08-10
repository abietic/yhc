package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWebSocketTransportConnectSendReceive(t *testing.T) {
	upgrader := websocket.Upgrader{}
	received := make(chan []byte, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("upgrade error: %v", err)
			return
		}
		defer conn.Close() //nolint:errcheck

		// Echo back any message received
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			received <- msg
			_ = conn.WriteMessage(websocket.TextMessage, msg)
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	transport := NewWebSocketTransport(wsURL, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
		return
	}
	defer transport.Close() //nolint:errcheck

	// Send a message
	msg := json.RawMessage(`{"jsonrpc":"2.0","method":"test","id":1}`)
	if err := transport.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
		return
	}

	// Verify server received it
	select {
	case got := <-received:
		if string(got) != string(msg) {
			t.Errorf("server got %q, want %q", got, msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for server to receive message")
	}

	// Verify client receives the echo
	select {
	case got := <-transport.Receive():
		if string(got) != string(msg) {
			t.Errorf("client got %q, want %q", got, msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for echo")
	}
}

func TestWebSocketTransportUsesEnvironmentProxy(t *testing.T) {
	dialer := newWebSocketDialer()
	if dialer.Proxy == nil {
		t.Fatal("WebSocket dialer must honor HTTP_PROXY, HTTPS_PROXY, and NO_PROXY")
	}
}

func TestWebSocketTransportClose(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	transport := NewWebSocketTransport(wsURL, nil)

	ctx := context.Background()
	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
		return
	}

	// Close should succeed
	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
		return
	}

	// Send after close should fail
	if err := transport.Send(json.RawMessage(`{}`)); err == nil {
		t.Error("Send after Close should fail")
	}

	// Double close should be safe
	if err := transport.Close(); err != nil {
		t.Fatalf("double Close: %v", err)
		return
	}
}

func TestWebSocketTransportHeaders(t *testing.T) {
	gotHeaders := make(chan http.Header, 1)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders <- r.Header
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	transport := NewWebSocketTransport(wsURL, map[string]string{
		"X-Custom-Header": "test-value",
		"Authorization":   "Bearer token123",
	})

	ctx := context.Background()
	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
		return
	}
	defer transport.Close() //nolint:errcheck

	select {
	case h := <-gotHeaders:
		if h.Get("X-Custom-Header") != "test-value" {
			t.Errorf("X-Custom-Header = %q", h.Get("X-Custom-Header"))
		}
		if h.Get("Authorization") != "Bearer token123" {
			t.Errorf("Authorization = %q", h.Get("Authorization"))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for headers")
	}
}
