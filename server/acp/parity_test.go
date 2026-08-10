package acp

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

// =============================================================================
// Parity Verification Tests — ACP Subsystem
//
// These tests verify behavioral parity with the reference implementation's
// session lifecycle, permission flow, event streaming, cancellation handling,
// streaming backpressure, session migration, and protocol error handling.
// =============================================================================

// --- Session lifecycle: create -> close -> verify cleanup ---

func TestParity_SessionLifecycle_CreateAndClose(t *testing.T) {
	agent, err := NewAgent(Config{
		CWD:          t.TempDir(),
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	agent.mockModel = &mockChatModel{responses: nil}

	ctx := context.Background()

	// Create a new session
	resp, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
		return
	}
	if resp.SessionId == "" {
		t.Fatal("expected non-empty session ID")
	}

	// Verify session is tracked
	agent.mu.Lock()
	_, exists := agent.sessions[resp.SessionId]
	agent.mu.Unlock()
	if !exists {
		t.Fatal("session not found in agent.sessions after creation")
	}

	// Close the session
	_, err = agent.CloseSession(ctx, acpsdk.CloseSessionRequest{SessionId: resp.SessionId})
	if err != nil {
		t.Fatalf("CloseSession: %v", err)
		return
	}

	// Verify session is removed
	agent.mu.Lock()
	_, exists = agent.sessions[resp.SessionId]
	agent.mu.Unlock()
	if exists {
		t.Fatal("session still present after CloseSession")
	}
}

// --- Session lifecycle: multiple sessions are independent ---

func TestParity_SessionLifecycle_MultipleSessions(t *testing.T) {
	agent, err := NewAgent(Config{
		CWD:          t.TempDir(),
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	agent.mockModel = &mockChatModel{responses: nil}

	ctx := context.Background()

	// Create two sessions
	resp1, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
		return
	}
	resp2, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
		return
	}

	if resp1.SessionId == resp2.SessionId {
		t.Fatal("expected different session IDs")
	}

	// Close one should not affect the other
	_, _ = agent.CloseSession(ctx, acpsdk.CloseSessionRequest{SessionId: resp1.SessionId})

	agent.mu.Lock()
	_, exists2 := agent.sessions[resp2.SessionId]
	agent.mu.Unlock()
	if !exists2 {
		t.Fatal("closing session 1 should not affect session 2")
	}
}

// --- Permission flow: approval manager request/resolve ---

func TestParity_Permission_ApprovalRequestAndResolve(t *testing.T) {
	tam := NewToolApprovalManager(5 * time.Second)
	ctx := context.Background()

	sessionID := acpsdk.SessionId("test-session")
	toolCallID := acpsdk.ToolCallId("call-1")

	// Request approval
	resultCh := tam.RequestApproval(ctx, sessionID, toolCallID, "bash", map[string]any{"command": "ls"})

	// Verify pending
	if !tam.HasPending(sessionID) {
		t.Fatal("expected pending approval for session")
	}

	// Resolve with approval
	resolved := tam.Resolve(sessionID, true, "")
	if !resolved {
		t.Fatal("expected Resolve to return true")
	}

	// Read result
	select {
	case result := <-resultCh:
		if !result.Approved {
			t.Error("expected approval")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for approval result")
	}

	// No longer pending
	if tam.HasPending(sessionID) {
		t.Error("expected no pending approval after resolve")
	}
}

// --- Permission flow: rejection ---

func TestParity_Permission_Rejection(t *testing.T) {
	tam := NewToolApprovalManager(5 * time.Second)
	ctx := context.Background()

	sessionID := acpsdk.SessionId("test-session")
	resultCh := tam.RequestApproval(ctx, sessionID, "call-2", "write", nil)

	tam.Resolve(sessionID, false, "user denied")

	select {
	case result := <-resultCh:
		if result.Approved {
			t.Error("expected rejection")
		}
		if result.Reason != "user denied" {
			t.Errorf("expected reason 'user denied', got %q", result.Reason)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for rejection result")
	}
}

// --- Permission flow: timeout ---

func TestParity_Permission_Timeout(t *testing.T) {
	tam := NewToolApprovalManager(100 * time.Millisecond)
	ctx := context.Background()

	sessionID := acpsdk.SessionId("timeout-session")
	resultCh := tam.RequestApproval(ctx, sessionID, "call-3", "bash", nil)

	// Don't resolve — should timeout
	select {
	case result := <-resultCh:
		if result.Approved {
			t.Error("expected denial on timeout")
		}
		if !result.TimedOut {
			t.Error("expected TimedOut=true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("test timeout waiting for approval timeout")
	}
}

// --- Streaming: buffer with backpressure ---

func TestParity_Streaming_BufferBackpressure(t *testing.T) {
	// Create a buffer with a small max size
	sb := NewStreamBuffer("sess-1", nil, 100) // 100 bytes max

	// Write within capacity
	sb.Write("hello") // 5 bytes
	sb.Write("world") // 5 bytes

	sb.mu.Lock()
	totalBefore := sb.totalBuffered
	droppedBefore := sb.dropped
	sb.mu.Unlock()

	if totalBefore != 10 {
		t.Errorf("expected 10 bytes buffered, got %d", totalBefore)
	}
	if droppedBefore != 0 {
		t.Errorf("expected 0 drops, got %d", droppedBefore)
	}

	// Write more than capacity to trigger backpressure
	sb.Write(string(make([]byte, 95))) // This plus existing 10 = 105, exceeds 100

	sb.mu.Lock()
	droppedAfter := sb.dropped
	sb.mu.Unlock()

	if droppedAfter == 0 {
		t.Error("expected drops after exceeding buffer capacity")
	}
}

// --- Streaming: closed buffer discards writes ---

func TestParity_Streaming_ClosedBufferDiscards(t *testing.T) {
	sb := NewStreamBuffer("sess-2", nil, 1024)
	sb.Write("before close")

	sb.Close()
	sb.Write("after close") // Should be silently discarded

	sb.mu.Lock()
	count := len(sb.buffer)
	sb.mu.Unlock()

	if count != 1 {
		t.Errorf("expected 1 buffered item (before close), got %d", count)
	}
}

// --- Protocol errors: correct error codes ---

func TestParity_ProtocolErrors_Codes(t *testing.T) {
	testCases := []struct {
		name     string
		err      *ProtocolError
		wantCode int
	}{
		{"SessionNotFound", NewSessionNotFoundError("sess-1"), CodeSessionNotFound},
		{"ApprovalTimeout", NewApprovalTimeoutError("bash"), CodeApprovalTimeout},
		{"ProtocolMismatch", NewProtocolMismatchError(1, 2), CodeProtocolMismatch},
		{"InvalidParams", NewInvalidParamsError("missing field"), CodeInvalidParams},
		{"InternalError", NewInternalProtocolError("panic"), CodeInternalError},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err.Code != tc.wantCode {
				t.Errorf("expected code %d, got %d", tc.wantCode, tc.err.Code)
			}
			// Error() should include the code
			errStr := tc.err.Error()
			if errStr == "" {
				t.Error("expected non-empty error string")
			}
		})
	}
}

// --- Protocol errors: malformed request handling ---

func TestParity_ProtocolErrors_MalformedRequest(t *testing.T) {
	agent, _ := NewAgent(Config{
		CWD:          t.TempDir(),
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
	})
	agent.mockModel = &mockChatModel{responses: nil}
	ctx := context.Background()

	// Test extension method with invalid JSON
	_, err := agent.HandleExtensionMethod(ctx, "_session/status", json.RawMessage(`{invalid`))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
		return
	}
	protErr, ok := err.(*ProtocolError)
	if !ok {
		t.Fatalf("expected ProtocolError, got %T", err)
	}
	if protErr.Code != CodeInvalidParams {
		t.Errorf("expected CodeInvalidParams (%d), got %d", CodeInvalidParams, protErr.Code)
	}
}

// --- Protocol errors: unknown extension method ---

func TestParity_ProtocolErrors_UnknownMethod(t *testing.T) {
	agent, _ := NewAgent(Config{
		CWD:          t.TempDir(),
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
	})
	agent.mockModel = &mockChatModel{responses: nil}
	ctx := context.Background()

	_, err := agent.HandleExtensionMethod(ctx, "_session/nonexistent", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for unknown method")
		return
	}
}

// --- Cancellation: cancel context cleans up approval ---

func TestParity_Cancellation_ContextCancelsApproval(t *testing.T) {
	tam := NewToolApprovalManager(5 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	sessionID := acpsdk.SessionId("cancel-session")

	resultCh := tam.RequestApproval(ctx, sessionID, "call-4", "bash", nil)

	// Cancel the context
	cancel()

	select {
	case result := <-resultCh:
		if result.Approved {
			t.Error("expected denial on cancel")
		}
		if result.Reason != "context cancelled" {
			t.Errorf("expected reason 'context cancelled', got %q", result.Reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for cancellation result")
	}
}

// --- Concurrent session creation ---

func TestParity_ConcurrentSessionCreation(t *testing.T) {
	agent, _ := NewAgent(Config{
		CWD:          t.TempDir(),
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
	})
	agent.mockModel = &mockChatModel{responses: nil}

	ctx := context.Background()
	var wg sync.WaitGroup
	sessions := make([]acpsdk.SessionId, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resp, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{})
			if err != nil {
				t.Errorf("concurrent NewSession %d failed: %v", idx, err)
				return
			}
			sessions[idx] = resp.SessionId
		}(i)
	}
	wg.Wait()

	// All sessions should be unique and present
	seen := make(map[acpsdk.SessionId]bool)
	for _, sid := range sessions {
		if sid == "" {
			continue
		}
		if seen[sid] {
			t.Error("duplicate session ID")
		}
		seen[sid] = true
	}

	agent.mu.Lock()
	sessionCount := len(agent.sessions)
	agent.mu.Unlock()
	if sessionCount != 10 {
		t.Errorf("expected 10 sessions, got %d", sessionCount)
	}
}
