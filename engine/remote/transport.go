// Package remote provides network-based session transport for running agent
// sessions over network connections. It is used by IDE extensions, web UIs,
// and other remote clients to communicate with the agent engine.
package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Message type constants define the kinds of messages exchanged between
// the agent engine and remote clients over a SessionTransport.
const (
	MsgTypeUserMessage    = "user_message"
	MsgTypeAssistantChunk = "assistant_chunk"
	MsgTypeAssistantDone  = "assistant_done"
	MsgTypeToolCall       = "tool_call"
	MsgTypeToolResult     = "tool_result"
	MsgTypeError          = "error"
	MsgTypeStatus         = "status"
	MsgTypeControl        = "control"
	MsgTypePing           = "ping"
	MsgTypePong           = "pong"
)

// TransportMessage is the wire-format envelope for all messages sent and
// received over a SessionTransport.
type TransportMessage struct {
	Type      string         `json:"type"`
	SessionID string         `json:"session_id,omitempty"`
	Content   string         `json:"content,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	ID        string         `json:"id"`
}

// NewTransportMessage creates a new TransportMessage with a generated ID and
// the current timestamp.
func NewTransportMessage(msgType, content string) *TransportMessage {
	return &TransportMessage{
		Type:      msgType,
		Content:   content,
		Timestamp: time.Now(),
		ID:        uuid.New().String(),
	}
}

// SessionTransport defines the interface for bidirectional communication
// between the agent engine and a remote client.
type SessionTransport interface {
	// Send sends a message to the remote client.
	Send(ctx context.Context, msg *TransportMessage) error
	// Receive receives a message from the remote client.
	Receive(ctx context.Context) (*TransportMessage, error)
	// Close closes the transport connection.
	Close() error
	// IsConnected returns the connection status.
	IsConnected() bool
}

// --------------------------------------------------------------------------
// WebSocketTransport
// --------------------------------------------------------------------------

// WebSocketTransport implements SessionTransport over a network connection.
// In a production deployment this would use gorilla/websocket or nhooyr.io/websocket;
// here we use net.Conn as a placeholder with newline-delimited JSON framing.
type WebSocketTransport struct {
	mu        sync.Mutex
	conn      net.Conn
	addr      string
	connected bool
	readCh    chan *TransportMessage
	doneCh    chan struct{}
	doneOnce  sync.Once
}

// NewWebSocketTransport creates a new WebSocketTransport that will connect to
// the given address. Call Connect to establish the connection.
func NewWebSocketTransport(addr string) *WebSocketTransport {
	return &WebSocketTransport{
		addr:   addr,
		readCh: make(chan *TransportMessage, 64),
		doneCh: make(chan struct{}),
	}
}

// Connect establishes the underlying network connection and starts the
// background read loop.
func (t *WebSocketTransport) Connect(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.connected {
		return errors.New("remote: already connected")
	}

	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", t.addr)
	if err != nil {
		return fmt.Errorf("remote: dial %s: %w", t.addr, err)
	}

	t.conn = conn
	t.connected = true

	go t.readLoop()

	return nil
}

// readLoop continuously reads newline-delimited JSON messages from the
// connection and pushes them into readCh until the connection is closed.
func (t *WebSocketTransport) readLoop() {
	decoder := json.NewDecoder(t.conn)
	for {
		var msg TransportMessage
		if err := decoder.Decode(&msg); err != nil {
			// Connection closed or read error — signal done.
			t.mu.Lock()
			t.connected = false
			t.mu.Unlock()
			t.closeDone()
			return
		}
		select {
		case t.readCh <- &msg:
		case <-t.doneCh:
			return
		}
	}
}

// Send sends a message to the remote peer as newline-delimited JSON.
func (t *WebSocketTransport) Send(ctx context.Context, msg *TransportMessage) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.connected {
		return errors.New("remote: not connected")
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("remote: marshal: %w", err)
	}
	data = append(data, '\n')

	if deadline, ok := ctx.Deadline(); ok {
		if err := t.conn.SetWriteDeadline(deadline); err != nil {
			return fmt.Errorf("remote: set write deadline: %w", err)
		}
	}

	if _, err := t.conn.Write(data); err != nil {
		t.connected = false
		return fmt.Errorf("remote: write: %w", err)
	}
	return nil
}

// Receive waits for the next message from the remote peer. It respects
// context cancellation and transport closure.
func (t *WebSocketTransport) Receive(ctx context.Context) (*TransportMessage, error) {
	select {
	case msg, ok := <-t.readCh:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	case <-t.doneCh:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close shuts down the transport, closing the underlying connection and
// stopping the read loop.
func (t *WebSocketTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.connected {
		return nil
	}
	t.connected = false

	// Signal readLoop to stop.
	t.closeDone()

	return t.conn.Close()
}

// IsConnected reports whether the transport is currently connected.
func (t *WebSocketTransport) IsConnected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connected
}

func (t *WebSocketTransport) closeDone() {
	t.doneOnce.Do(func() {
		close(t.doneCh)
	})
}

// --------------------------------------------------------------------------
// StdioTransport
// --------------------------------------------------------------------------

// StdioTransport implements SessionTransport over stdin/stdout using
// newline-delimited JSON. It is designed for local CLI and SDK communication
// where the agent runs as a subprocess.
type StdioTransport struct {
	reader *bufio.Reader
	writer *bufio.Writer
	mu     sync.Mutex
	closed bool
}

// NewStdioTransport creates a StdioTransport over the given reader and writer
// (typically os.Stdin and os.Stdout).
func NewStdioTransport(r io.Reader, w io.Writer) *StdioTransport {
	return &StdioTransport{
		reader: bufio.NewReader(r),
		writer: bufio.NewWriter(w),
	}
}

// Send writes a JSON-encoded message followed by a newline to the writer.
func (t *StdioTransport) Send(ctx context.Context, msg *TransportMessage) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return errors.New("remote: transport closed")
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("remote: marshal: %w", err)
	}
	data = append(data, '\n')

	if _, err := t.writer.Write(data); err != nil {
		return fmt.Errorf("remote: write: %w", err)
	}
	return t.writer.Flush()
}

// Receive reads and decodes the next JSON message from the reader. It blocks
// until a message arrives or the context is cancelled.
func (t *StdioTransport) Receive(ctx context.Context) (*TransportMessage, error) {
	// Use a goroutine to make the blocking read cancellable via context.
	type result struct {
		msg *TransportMessage
		err error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := t.reader.ReadBytes('\n')
		if err != nil {
			ch <- result{nil, err}
			return
		}
		var msg TransportMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			ch <- result{nil, fmt.Errorf("remote: unmarshal: %w", err)}
			return
		}
		ch <- result{&msg, nil}
	}()

	select {
	case r := <-ch:
		return r.msg, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close marks the transport as closed. Since stdio streams are not owned by
// the transport, it does not close the underlying reader/writer.
func (t *StdioTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	return nil
}

// IsConnected reports whether the transport is open.
func (t *StdioTransport) IsConnected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.closed
}

// --------------------------------------------------------------------------
// SessionServer
// --------------------------------------------------------------------------

// SessionServer manages active remote sessions and their associated
// transports. It provides session registration, lookup, and broadcast.
type SessionServer struct {
	mu        sync.RWMutex
	sessions  map[string]SessionTransport
	onConnect func(sessionID string, transport SessionTransport)
}

// NewSessionServer creates a new SessionServer with an empty session map.
func NewSessionServer() *SessionServer {
	return &SessionServer{
		sessions: make(map[string]SessionTransport),
	}
}

// OnConnect sets a callback that is invoked whenever a new session is
// registered. It is safe to call before any sessions are registered.
func (s *SessionServer) OnConnect(fn func(sessionID string, transport SessionTransport)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onConnect = fn
}

// RegisterSession associates a transport with a session ID. If an onConnect
// callback is set, it is invoked after registration.
func (s *SessionServer) RegisterSession(sessionID string, transport SessionTransport) {
	s.mu.Lock()
	s.sessions[sessionID] = transport
	cb := s.onConnect
	s.mu.Unlock()

	if cb != nil {
		cb(sessionID, transport)
	}
}

// UnregisterSession removes a session from the server and closes its
// transport.
func (s *SessionServer) UnregisterSession(sessionID string) {
	s.mu.Lock()
	transport, ok := s.sessions[sessionID]
	if ok {
		delete(s.sessions, sessionID)
	}
	s.mu.Unlock()

	if ok && transport != nil {
		_ = transport.Close()
	}
}

// GetTransport returns the transport for the given session ID, or nil if the
// session is not registered.
func (s *SessionServer) GetTransport(sessionID string) SessionTransport {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[sessionID]
}

// Broadcast sends a message to all currently connected sessions. Sessions
// that fail to receive the message are silently skipped.
func (s *SessionServer) Broadcast(msg *TransportMessage) {
	s.mu.RLock()
	targets := make([]SessionTransport, 0, len(s.sessions))
	for _, t := range s.sessions {
		if t.IsConnected() {
			targets = append(targets, t)
		}
	}
	s.mu.RUnlock()

	ctx := context.Background()
	for _, t := range targets {
		_ = t.Send(ctx, msg)
	}
}
