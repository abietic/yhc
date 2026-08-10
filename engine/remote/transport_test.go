package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestNewTransportMessageSetsTypeContentIDAndTimestamp(t *testing.T) {
	msg := NewTransportMessage(MsgTypeUserMessage, "hello")
	if msg.Type != MsgTypeUserMessage || msg.Content != "hello" {
		t.Fatalf("unexpected message fields: %#v", msg)
	}
	if msg.ID == "" {
		t.Fatal("expected generated message ID")
	}
	if msg.Timestamp.IsZero() {
		t.Fatal("expected timestamp")
	}
}

func TestStdioTransportSendReceiveCloseAndCancel(t *testing.T) {
	in := bytes.NewBufferString(`{"type":"user_message","content":"hi","id":"msg-1","timestamp":"2026-06-13T00:00:00Z"}` + "\n")
	var out bytes.Buffer
	transport := NewStdioTransport(in, &out)

	received, err := transport.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive failed: %v", err)
		return
	}
	if received.Type != MsgTypeUserMessage || received.Content != "hi" || received.ID != "msg-1" {
		t.Fatalf("unexpected received message: %#v", received)
	}

	if err := transport.Send(context.Background(), &TransportMessage{
		Type:      MsgTypeAssistantChunk,
		Content:   "chunk",
		SessionID: "session-1",
		ID:        "msg-2",
		Timestamp: time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("Send failed: %v", err)
		return
	}
	var sent TransportMessage
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &sent); err != nil {
		t.Fatalf("decode sent message: %v; raw=%q", err, out.String())
		return
	}
	if sent.Type != MsgTypeAssistantChunk || sent.Content != "chunk" || sent.SessionID != "session-1" {
		t.Fatalf("unexpected sent message: %#v", sent)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := transport.Receive(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation from Receive, got %v", err)
	}

	if !transport.IsConnected() {
		t.Fatal("transport should be connected before Close")
	}
	if err := transport.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
		return
	}
	if transport.IsConnected() {
		t.Fatal("transport should be disconnected after Close")
	}
	if err := transport.Send(context.Background(), NewTransportMessage(MsgTypePing, "")); err == nil {
		t.Fatal("expected send on closed transport to fail")
		return
	}
}

func TestWebSocketTransportSendReceiveAndClose(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
		return
	}
	defer listener.Close() //nolint:errcheck

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	transport := NewWebSocketTransport(listener.Addr().String())
	if err := transport.Connect(context.Background()); err != nil {
		t.Fatalf("connect failed: %v", err)
		return
	}
	if !transport.IsConnected() {
		t.Fatal("transport should be connected")
	}

	var serverConn net.Conn
	select {
	case serverConn = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("server did not accept connection")
	}
	defer serverConn.Close() //nolint:errcheck

	if err := transport.Send(context.Background(), &TransportMessage{
		Type:      MsgTypeToolResult,
		Content:   "result",
		ID:        "client-msg",
		Timestamp: time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("send failed: %v", err)
		return
	}
	buf := make([]byte, 512)
	_ = serverConn.SetReadDeadline(time.Now().Add(time.Second))
	n, err := serverConn.Read(buf)
	if err != nil {
		t.Fatalf("server read failed: %v", err)
		return
	}
	if got := string(buf[:n]); !strings.Contains(got, `"type":"tool_result"`) || !strings.Contains(got, `"content":"result"`) {
		t.Fatalf("unexpected wire message: %q", got)
	}

	serverMsg := &TransportMessage{
		Type:      MsgTypeAssistantChunk,
		Content:   "from server",
		ID:        "server-msg",
		Timestamp: time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC),
	}
	encoded, _ := json.Marshal(serverMsg)
	if _, err := serverConn.Write(append(encoded, '\n')); err != nil {
		t.Fatalf("server write failed: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := transport.Receive(ctx)
	if err != nil {
		t.Fatalf("receive failed: %v", err)
		return
	}
	if got.Type != MsgTypeAssistantChunk || got.Content != "from server" || got.ID != "server-msg" {
		t.Fatalf("unexpected received websocket message: %#v", got)
	}

	if err := transport.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
		return
	}
	if transport.IsConnected() {
		t.Fatal("transport should be disconnected after close")
	}
	if _, err := transport.Receive(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after close, got %v", err)
	}
}

func TestSessionServerRegisterBroadcastAndUnregister(t *testing.T) {
	server := NewSessionServer()
	first := &fakeTransport{connected: true}
	second := &fakeTransport{connected: false}

	var connectedSession string
	server.OnConnect(func(sessionID string, transport SessionTransport) {
		connectedSession = sessionID
	})
	server.RegisterSession("s1", first)
	server.RegisterSession("s2", second)
	if connectedSession != "s2" {
		t.Fatalf("expected latest onConnect session s2, got %q", connectedSession)
	}
	if server.GetTransport("s1") != first {
		t.Fatal("expected s1 transport")
	}

	msg := NewTransportMessage(MsgTypeStatus, "ready")
	server.Broadcast(msg)
	if len(first.sent) != 1 || first.sent[0].Content != "ready" {
		t.Fatalf("expected broadcast to connected transport, got %#v", first.sent)
	}
	if len(second.sent) != 0 {
		t.Fatalf("did not expect broadcast to disconnected transport, got %#v", second.sent)
	}

	server.UnregisterSession("s1")
	if !first.closed {
		t.Fatal("expected unregister to close transport")
	}
	if server.GetTransport("s1") != nil {
		t.Fatal("expected s1 to be removed")
		return
	}
}

type fakeTransport struct {
	sent      []*TransportMessage
	closed    bool
	connected bool
}

func (f *fakeTransport) Send(ctx context.Context, msg *TransportMessage) error {
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeTransport) Receive(ctx context.Context) (*TransportMessage, error) {
	return nil, context.Canceled
}

func (f *fakeTransport) Close() error {
	f.closed = true
	f.connected = false
	return nil
}

func (f *fakeTransport) IsConnected() bool {
	return f.connected
}
