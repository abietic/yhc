package mcp

import (
	"context"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPToolCallTarget_BindsExactSessionAndGeneration(t *testing.T) {
	first := newGenerationTestSession(t, "first")
	second := newGenerationTestSession(t, "second")
	client := NewMCPClient(ServerConfig{Name: "test", Timeout: time.Second})

	client.mu.Lock()
	client.session = first
	client.connected = true
	client.connectionGeneration = 1
	client.mu.Unlock()
	oldTarget, err := client.BindToolCallTarget()
	if err != nil {
		t.Fatalf("BindToolCallTarget() error = %v", err)
	}

	client.mu.Lock()
	client.session = second
	client.connectionGeneration = 2
	client.mu.Unlock()

	if got := oldTarget.Generation(); got != 1 {
		t.Fatalf("old target generation = %d, want 1", got)
	}
	oldResult, err := oldTarget.CallTool(t.Context(), "identity", nil)
	if err != nil {
		t.Fatalf("old target CallTool() error = %v", err)
	}
	if got := oldResult.Content[0].Text; got != "first" {
		t.Fatalf("old target result = %q, want exact first session", got)
	}

	newResult, err := client.CallTool(t.Context(), "identity", nil)
	if err != nil {
		t.Fatalf("client CallTool() error = %v", err)
	}
	if got := newResult.Content[0].Text; got != "second" {
		t.Fatalf("client CallTool() result = %q, want current second session", got)
	}
}

func TestMCPClient_GenerationCallbacksCaptureNotificationGeneration(t *testing.T) {
	client := NewMCPClient(ServerConfig{Name: "test"})
	gotGeneration := make(chan uint64, 1)
	client.SetOnToolsChangedWithGeneration(func(generation uint64) {
		gotGeneration <- generation
	})

	client.mu.Lock()
	client.connectionGeneration = 2
	client.mu.Unlock()
	client.dispatchToolsChanged(1)
	if got := <-gotGeneration; got != 1 {
		t.Fatalf("tools changed generation = %d, want notification generation 1", got)
	}

	legacyToolsChanged := 0
	legacyClose := 0
	client.SetOnToolsChanged(func() { legacyToolsChanged++ })
	client.SetOnClose(func() { legacyClose++ })
	client.dispatchToolsChanged(2)
	client.mu.Lock()
	onClose := client.onClose
	client.mu.Unlock()
	onClose(2)
	if legacyToolsChanged != 1 || legacyClose != 1 {
		t.Fatalf("legacy callbacks = tools:%d close:%d, want tools:1 close:1", legacyToolsChanged, legacyClose)
	}
}

func TestMCPClientMonitorSession_ReportsExactCurrentGeneration(t *testing.T) {
	session := newGenerationTestSession(t, "current")
	client := NewMCPClient(ServerConfig{Name: "test"})
	gotGeneration := make(chan uint64, 1)
	client.SetOnCloseWithGeneration(func(generation uint64) { gotGeneration <- generation })
	client.mu.Lock()
	client.session = session
	client.connected = true
	client.status = StatusConnected
	client.connectionGeneration = 7
	client.mu.Unlock()

	done := make(chan struct{})
	go client.monitorSession(session, 7, done)
	if err := session.Close(); err != nil {
		t.Fatalf("session Close() error = %v", err)
	}
	select {
	case got := <-gotGeneration:
		if got != 7 {
			t.Fatalf("session close generation = %d, want 7", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for session close callback")
	}
	<-done
	if client.IsConnected() || client.Status() != StatusFailed {
		t.Fatal("current session monitor did not mark the connection failed")
	}
}

func TestMCPClientMonitorSession_IgnoresRetiredSession(t *testing.T) {
	retired := newGenerationTestSession(t, "retired")
	current := newGenerationTestSession(t, "current")
	client := NewMCPClient(ServerConfig{Name: "test"})
	called := make(chan uint64, 1)
	client.SetOnCloseWithGeneration(func(generation uint64) {
		called <- generation
	})
	client.mu.Lock()
	client.session = current
	client.connected = true
	client.status = StatusConnected
	client.connectionGeneration = 2
	client.mu.Unlock()

	done := make(chan struct{})
	go client.monitorSession(retired, 1, done)
	if err := retired.Close(); err != nil {
		t.Fatalf("retired session Close() error = %v", err)
	}
	<-done
	select {
	case generation := <-called:
		t.Fatalf("retired session invoked generation callback %d", generation)
	default:
	}
	if !client.IsConnected() || client.Status() != StatusConnected {
		t.Fatal("retired session monitor changed the current connection")
	}
}

func TestMCPToolCallTarget_PreservesCallTimeout(t *testing.T) {
	session := newGenerationTestSession(t, "slow")
	target := &MCPToolCallTarget{
		session:    session,
		generation: 1,
		timeout:    20 * time.Millisecond,
	}

	_, err := target.CallTool(t.Context(), "wait", nil)
	if err == nil {
		t.Fatal("CallTool() error = nil, want timeout error")
	}
}

func newGenerationTestSession(t *testing.T, identity string) *sdkmcp.ClientSession {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: identity, Version: "1"}, nil)
	server.AddTool(&sdkmcp.Tool{
		Name:        "identity",
		Description: "returns its server identity",
		InputSchema: map[string]any{"type": "object"},
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: identity}}}, nil
	})
	server.AddTool(&sdkmcp.Tool{
		Name:        "wait",
		Description: "waits for cancellation",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, _ *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), serverTransport, nil); err != nil {
		t.Fatalf("server Connect() error = %v", err)
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}
