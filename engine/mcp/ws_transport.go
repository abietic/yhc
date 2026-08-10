package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WebSocketTransport implements MCP transport over WebSocket.
// Provides full-duplex communication between client and MCP server.
//
// Reference: src/cli/transports/ supports WebSocket for MCP connections.
type WebSocketTransport struct {
	mu      sync.Mutex
	url     string
	headers map[string]string
	conn    *websocket.Conn
	msgCh   chan json.RawMessage
	cancel  context.CancelFunc
	closed  bool
	done    chan struct{}
}

// NewWebSocketTransport creates a WebSocket transport for the given MCP server URL.
func NewWebSocketTransport(url string, headers map[string]string) *WebSocketTransport {
	return &WebSocketTransport{
		url:     url,
		headers: headers,
		msgCh:   make(chan json.RawMessage, 64),
		done:    make(chan struct{}),
	}
}

// Connect establishes the WebSocket connection.
func (t *WebSocketTransport) Connect(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return fmt.Errorf("transport is closed")
	}

	ctx, t.cancel = context.WithCancel(ctx)

	header := http.Header{}
	for k, v := range t.headers {
		header.Set(k, v)
	}

	dialer := newWebSocketDialer()
	conn, resp, err := dialer.DialContext(ctx, t.url, header)
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close() //nolint:errcheck
	}
	if err != nil {
		return fmt.Errorf("websocket connect: %w", err)
	}
	t.conn = conn

	go t.readLoop(ctx)
	return nil
}

func newWebSocketDialer() *websocket.Dialer {
	return &websocket.Dialer{
		HandshakeTimeout: 30 * time.Second,
		Proxy:            http.ProxyFromEnvironment,
	}
}

// Send sends a JSON-RPC message over the WebSocket connection.
func (t *WebSocketTransport) Send(msg json.RawMessage) error {
	t.mu.Lock()
	conn := t.conn
	closed := t.closed
	t.mu.Unlock()

	if closed || conn == nil {
		return fmt.Errorf("transport not connected")
	}
	return conn.WriteMessage(websocket.TextMessage, msg)
}

// Receive returns the channel for incoming messages.
func (t *WebSocketTransport) Receive() <-chan json.RawMessage {
	return t.msgCh
}

// Close closes the WebSocket connection.
func (t *WebSocketTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}
	t.closed = true

	if t.cancel != nil {
		t.cancel()
	}
	if t.conn != nil {
		_ = t.conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		)
		_ = t.conn.Close()
	}
	close(t.done)
	return nil
}

// Done returns a channel that is closed when the transport shuts down.
func (t *WebSocketTransport) Done() <-chan struct{} {
	return t.done
}

func (t *WebSocketTransport) readLoop(ctx context.Context) {
	defer close(t.msgCh)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		t.mu.Lock()
		conn := t.conn
		t.mu.Unlock()
		if conn == nil {
			return
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return
			}
			select {
			case <-ctx.Done():
			default:
			}
			return
		}

		select {
		case t.msgCh <- json.RawMessage(msg):
		case <-ctx.Done():
			return
		}
	}
}
