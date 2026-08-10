package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
)

func TestYHCProtocolMigrationCharacterizesIdentityIndependentBehavior(t *testing.T) {
	characterizeYHCProtocolMigrationGoalBehavior(t)
}

// --- Streaming Tests ---

func TestStreamBuffer_BasicWriteAndFlush(t *testing.T) {
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
	agent.mockModel = &mockChatModel{}

	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	client := &testClient{}
	agentConn := acpsdk.NewAgentSideConnection(agent, a2cW, c2aR)
	agent.SetConnection(agentConn)
	_ = acpsdk.NewClientSideConnection(client, c2aW, a2cR)

	t.Cleanup(func() {
		agent.Close()
		_ = c2aW.Close()
		_ = a2cW.Close()
	})

	sessionID := acpsdk.SessionId("test-stream-session")
	buf := NewStreamBuffer(sessionID, agentConn, 1024)

	buf.Write("Hello ")
	buf.Write("World")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = buf.Flush(ctx)
	if err != nil {
		t.Fatalf("Flush: %v", err)
		return
	}

	// Wait briefly for notifications to arrive at client
	time.Sleep(50 * time.Millisecond)

	// Verify updates were sent - collect all text content
	updates := client.getUpdates()
	if len(updates) == 0 {
		t.Fatal("expected at least one update after flush")
	}

	var allText string
	for _, u := range updates {
		if u.Update.AgentMessageChunk != nil && u.Update.AgentMessageChunk.Content.Text != nil {
			allText += u.Update.AgentMessageChunk.Content.Text.Text
		}
	}
	if allText != "Hello World" {
		t.Fatalf("expected combined text 'Hello World', got %q", allText)
	}
}

func TestStreamBuffer_Backpressure(t *testing.T) {
	sessionID := acpsdk.SessionId("test-backpressure")
	// Small buffer size of 20 bytes
	buf := NewStreamBuffer(sessionID, nil, 20)

	// Write chunks that exceed the buffer
	buf.Write("aaaaaaaaaa") // 10 bytes
	buf.Write("bbbbbbbbbb") // 10 bytes - now at 20
	buf.Write("cccccccccc") // 10 bytes - triggers backpressure

	dropped := buf.Stats()
	if dropped == 0 {
		t.Fatal("expected at least one dropped chunk due to backpressure")
	}
}

func TestStreamBuffer_CloseDiscardsWrites(t *testing.T) {
	sessionID := acpsdk.SessionId("test-close")
	buf := NewStreamBuffer(sessionID, nil, 1024)

	buf.Write("before close")
	buf.Close()
	buf.Write("after close")

	// Only "before close" should be in the buffer
	buf.mu.Lock()
	count := len(buf.buffer)
	buf.mu.Unlock()
	if count != 1 {
		t.Fatalf("expected 1 buffered item after close, got %d", count)
	}
}

func TestStreamBuffer_FlushWithNilConn(t *testing.T) {
	sessionID := acpsdk.SessionId("test-nil-conn")
	buf := NewStreamBuffer(sessionID, nil, 1024)

	buf.Write("some data")

	ctx := context.Background()
	err := buf.Flush(ctx)
	if err != nil {
		t.Fatalf("Flush with nil conn should not error: %v", err)
		return
	}
}

func TestStreamBuffer_FlushEmptyBuffer(t *testing.T) {
	sessionID := acpsdk.SessionId("test-empty")
	buf := NewStreamBuffer(sessionID, nil, 1024)

	ctx := context.Background()
	err := buf.Flush(ctx)
	if err != nil {
		t.Fatalf("Flush on empty buffer should not error: %v", err)
		return
	}
}

// --- Tool Approval Flow Tests ---

func TestToolApprovalManager_RequestAndResolve(t *testing.T) {
	tam := NewToolApprovalManager(5 * time.Second)
	sessionID := acpsdk.SessionId("approval-session")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resultCh := tam.RequestApproval(ctx, sessionID, "call_1", "Read", map[string]any{"path": "/tmp/test"})

	// Should have pending
	if !tam.HasPending(sessionID) {
		t.Fatal("expected pending approval")
	}

	// Resolve
	ok := tam.Resolve(sessionID, true, "user approved")
	if !ok {
		t.Fatal("Resolve should return true for pending request")
	}

	// Should no longer be pending
	if tam.HasPending(sessionID) {
		t.Fatal("should not have pending after resolve")
	}

	// Read result
	select {
	case result := <-resultCh:
		if !result.Approved {
			t.Fatal("expected approved")
		}
		if result.Reason != "user approved" {
			t.Fatalf("unexpected reason: %q", result.Reason)
		}
		if result.TimedOut {
			t.Fatal("should not be timed out")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for result")
	}
}

func TestToolApprovalManager_Timeout(t *testing.T) {
	tam := NewToolApprovalManager(100 * time.Millisecond)
	sessionID := acpsdk.SessionId("timeout-session")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resultCh := tam.RequestApproval(ctx, sessionID, "call_1", "Write", nil)

	// Wait for timeout
	select {
	case result := <-resultCh:
		if result.Approved {
			t.Fatal("expected denial on timeout")
		}
		if !result.TimedOut {
			t.Fatal("expected TimedOut to be true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for timeout result")
	}

	// Should no longer be pending
	if tam.HasPending(sessionID) {
		t.Fatal("should not have pending after timeout")
	}
}

func TestToolApprovalManager_ContextCancel(t *testing.T) {
	tam := NewToolApprovalManager(60 * time.Second)
	sessionID := acpsdk.SessionId("cancel-session")

	ctx, cancel := context.WithCancel(context.Background())

	resultCh := tam.RequestApproval(ctx, sessionID, "call_1", "Bash", nil)

	// Cancel context
	cancel()

	select {
	case result := <-resultCh:
		if result.Approved {
			t.Fatal("expected denial on context cancel")
		}
		if result.Reason != "context cancelled" {
			t.Fatalf("unexpected reason: %q", result.Reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for cancel result")
	}
	if tam.HasPending(sessionID) {
		t.Fatal("should not have pending after context cancellation")
	}
}

func TestToolApprovalManager_DenyResolve(t *testing.T) {
	tam := NewToolApprovalManager(5 * time.Second)
	sessionID := acpsdk.SessionId("deny-session")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resultCh := tam.RequestApproval(ctx, sessionID, "call_1", "Write", nil)

	// Deny
	ok := tam.Resolve(sessionID, false, "user denied")
	if !ok {
		t.Fatal("Resolve should return true")
	}

	select {
	case result := <-resultCh:
		if result.Approved {
			t.Fatal("expected denial")
		}
		if result.Reason != "user denied" {
			t.Fatalf("unexpected reason: %q", result.Reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for denial result")
	}
}

func TestToolApprovalManager_ResolveNonExistent(t *testing.T) {
	tam := NewToolApprovalManager(5 * time.Second)

	ok := tam.Resolve("nonexistent", true, "")
	if ok {
		t.Fatal("Resolve should return false for non-existent session")
	}
}

func TestToolApprovalManager_DoubleResolve(t *testing.T) {
	tam := NewToolApprovalManager(5 * time.Second)
	sessionID := acpsdk.SessionId("double-session")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = tam.RequestApproval(ctx, sessionID, "call_1", "Read", nil)

	// First resolve
	ok1 := tam.Resolve(sessionID, true, "first")
	if !ok1 {
		t.Fatal("first resolve should succeed")
	}

	// Second resolve should fail
	ok2 := tam.Resolve(sessionID, false, "second")
	if ok2 {
		t.Fatal("second resolve should fail")
	}
}

// --- Protocol Error Tests ---

func TestProtocolErrors_Codes(t *testing.T) {
	tests := []struct {
		name     string
		err      *ProtocolError
		wantCode int
	}{
		{"session not found", NewSessionNotFoundError("test"), CodeSessionNotFound},
		{"approval timeout", NewApprovalTimeoutError("Read"), CodeApprovalTimeout},
		{"protocol mismatch", NewProtocolMismatchError(1, 2), CodeProtocolMismatch},
		{"invalid params", NewInvalidParamsError("missing field"), CodeInvalidParams},
		{"internal error", NewInternalProtocolError("oops"), CodeInternalError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d", tt.err.Code, tt.wantCode)
			}
			if tt.err.Error() == "" {
				t.Fatal("Error() should not return empty string")
			}
		})
	}
}

func TestProtocolError_JSONSerialization(t *testing.T) {
	err := NewSessionNotFoundError("session-123")
	data, jsonErr := json.Marshal(err)
	if jsonErr != nil {
		t.Fatalf("Marshal: %v", jsonErr)
		return
	}

	var decoded ProtocolError
	if jsonErr := json.Unmarshal(data, &decoded); jsonErr != nil {
		t.Fatalf("Unmarshal: %v", jsonErr)
		return
	}

	if decoded.Code != CodeSessionNotFound {
		t.Fatalf("decoded code = %d, want %d", decoded.Code, CodeSessionNotFound)
	}
	if decoded.Message != "Session not found" {
		t.Fatalf("decoded message = %q", decoded.Message)
	}
}

// --- Extension Method Handler Tests ---

func TestExtensionHandlerRemovedSessionMigrationMethodsReturnMethodNotFound(t *testing.T) {
	cwd := t.TempDir()
	agent, err := NewAgent(Config{
		CWD:          cwd,
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.mockModel = &mockChatModel{}
	t.Cleanup(agent.Close)

	sentinelDir := filepath.Join(cwd, "sentinel")
	if err := os.MkdirAll(sentinelDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(sentinelDir, "keep"),
		[]byte("unchanged"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	epoch := time.Unix(0, 0).UTC()
	importParams, err := json.Marshal(map[string]any{
		"token": map[string]any{
			"session_id":    "removed-import",
			"cwd":           cwd,
			"created_at":    epoch,
			"exported_at":   epoch,
			"message_count": 0,
			"checksum":      "2a6bfc68",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	agent.mu.Lock()
	beforeSessions := maps.Clone(agent.sessions)
	agent.mu.Unlock()
	beforeTree := snapshotACPProjectTree(t, cwd)

	tests := []struct {
		method string
		params json.RawMessage
	}{
		{
			method: "_session/export",
			params: json.RawMessage(`{"sessionId":"removed-export"}`),
		},
		{
			method: "_session/import",
			params: importParams,
		},
	}
	for _, test := range tests {
		t.Run(test.method, func(t *testing.T) {
			result, callErr := agent.HandleExtensionMethod(
				t.Context(),
				test.method,
				test.params,
			)
			if result != nil {
				t.Errorf("result = %#v, want nil", result)
			}
			var requestErr *acpsdk.RequestError
			if !errors.As(callErr, &requestErr) || requestErr.Code != CodeMethodNotFound {
				t.Errorf("error = %#v, want MethodNotFound", callErr)
			}
		})
	}

	agent.mu.Lock()
	afterSessions := maps.Clone(agent.sessions)
	agent.mu.Unlock()
	if !maps.Equal(beforeSessions, afterSessions) {
		t.Fatalf("sessions changed from %#v to %#v", beforeSessions, afterSessions)
	}
	afterTree := snapshotACPProjectTree(t, cwd)
	if !maps.Equal(beforeTree, afterTree) {
		t.Fatalf("project tree changed from %#v to %#v", beforeTree, afterTree)
	}
}

func snapshotACPProjectTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := fmt.Sprintf(
			"type=%s mode=%s modified=%d",
			entry.Type(),
			info.Mode(),
			info.ModTime().UnixNano(),
		)
		if info.Mode().IsRegular() {
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += fmt.Sprintf(" contents=%x", contents)
		}
		snapshot[relative] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestExtensionHandler_SessionStatus(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "hello"},
		},
	}

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
	agent.mockModel = mockModel

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a session
	sessResp, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatal(err)
		return
	}

	// Call extension method
	params, _ := json.Marshal(sessionStatusParams{SessionID: string(sessResp.SessionId)})
	result, err := agent.HandleExtensionMethod(ctx, "_session/status", params)
	if err != nil {
		t.Fatalf("HandleExtensionMethod: %v", err)
		return
	}

	status, ok := result.(*sessionStatusResponse)
	if !ok {
		t.Fatalf("expected *sessionStatusResponse, got %T", result)
	}
	if !status.Active {
		t.Fatal("expected session to be active")
	}
	if status.Model != "mock-model" {
		t.Fatalf("expected model 'mock-model', got %q", status.Model)
	}
}

func TestExtensionHandler_UnknownMethod(t *testing.T) {
	agent, err := NewAgent(Config{
		CWD:          t.TempDir(),
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	agent.mockModel = &mockChatModel{}

	_, err = agent.HandleExtensionMethod(context.Background(), "_unknown/method", nil)
	if err == nil {
		t.Fatal("expected error for unknown extension method")
		return
	}
}

func TestExtensionHandler_SessionStatusNonExistent(t *testing.T) {
	agent, err := NewAgent(Config{
		CWD:          t.TempDir(),
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	agent.mockModel = &mockChatModel{}

	params, _ := json.Marshal(sessionStatusParams{SessionID: "nonexistent"})
	_, err = agent.HandleExtensionMethod(context.Background(), "_session/status", params)
	if err == nil {
		t.Fatal("expected error for non-existent session status")
		return
	}
}

// --- Fork Session Tests ---

func TestForkSession(t *testing.T) {
	cwd := t.TempDir()
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "original response"},
			{Role: schema.Assistant, Content: "forked response"},
		},
	}

	agent, err := NewAgent(Config{
		CWD:          cwd,
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
	agent.mockModel = mockModel

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create original session
	sessResp, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: cwd, McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatal(err)
		return
	}

	// Send a prompt to populate history
	_, err = agent.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sessResp.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("hello")},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
		return
	}
	if _, err := agent.UnstableForkSession(ctx, acpsdk.UnstableForkSessionRequest{
		SessionId: sessResp.SessionId,
		Cwd:       t.TempDir(),
	}); err == nil {
		t.Fatal("fork accepted a working directory different from the source")
	}
	agent.mu.Lock()
	sessionCount := len(agent.sessions)
	agent.mu.Unlock()
	if sessionCount != 1 {
		t.Fatalf("rejected fork registered %d ACP sessions", sessionCount)
	}

	// Fork the session
	forkResp, err := agent.UnstableForkSession(ctx, acpsdk.UnstableForkSessionRequest{
		SessionId: sessResp.SessionId,
		Cwd:       cwd,
	})
	if err != nil {
		t.Fatalf("UnstableForkSession: %v", err)
		return
	}

	if forkResp.SessionId == "" {
		t.Fatal("fork should return a new session ID")
	}
	if forkResp.SessionId == sessResp.SessionId {
		t.Fatal("fork should create a different session ID")
	}

	// Verify forked session has the messages from original
	agent.mu.Lock()
	forkedSess, ok := agent.sessions[forkResp.SessionId]
	agent.mu.Unlock()
	if !ok {
		t.Fatal("forked session should exist")
	}

	msgs := forkedSess.Engine.GetMessages()
	if len(msgs) == 0 {
		t.Fatal("forked session should have messages from original")
	}
	agent.mu.Lock()
	sourceSess := agent.sessions[sessResp.SessionId]
	agent.mu.Unlock()
	if sourceSess.Engine.SessionID() != string(sessResp.SessionId) {
		t.Fatalf(
			"fork changed source ACP identity to %q",
			sourceSess.Engine.SessionID(),
		)
	}
	childRecorder := transcript.NewRecorder(
		string(forkResp.SessionId),
		acpSessionTranscriptDir(cwd),
	)
	child, err := childRecorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	metadata := session.ReadSessionMetadataFull(child)
	if metadata == nil ||
		metadata.ParentSessionID != string(sessResp.SessionId) ||
		metadata.SessionID != string(forkResp.SessionId) ||
		metadata.ThreadID != string(forkResp.SessionId) {
		t.Fatalf("forked ACP metadata = %#v", metadata)
	}
	var operationID string
	for _, entry := range child.Metadata {
		if entry.Key == "fork_operation_id" {
			operationID = entry.Value
		}
	}
	if operationID == "" {
		t.Fatal("forked ACP transcript has no operation identity")
	}
	// The protocol work above intentionally shares one bounded context. The
	// final disk-restart proof is a separate operation and needs an independent
	// race-detector budget rather than an already-spent deadline.
	restartCtx, restartCancel := context.WithTimeout(
		context.Background(),
		60*time.Second,
	)
	defer restartCancel()
	restarted, err := session.ResumeSession(restartCtx, session.ResumeOptions{
		SessionID:        string(forkResp.SessionId),
		SessionDir:       acpSessionTranscriptDir(cwd),
		ProjectDir:       cwd,
		ValidateMessages: true,
	})
	if err != nil || len(restarted.Messages) != len(msgs) {
		t.Fatalf("restart forked ACP session = %#v, err=%v", restarted, err)
	}
}

func TestForkSession_NonExistentSource(t *testing.T) {
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
	agent.mockModel = &mockChatModel{}

	ctx := context.Background()
	_, err = agent.UnstableForkSession(ctx, acpsdk.UnstableForkSessionRequest{
		SessionId: "nonexistent",
		Cwd:       t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error when forking non-existent session")
		return
	}
}

func TestForkSessionRestoreFailureCompensatesDurableChild(t *testing.T) {
	cwd := t.TempDir()
	agent, err := NewAgent(Config{
		CWD:          cwd,
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.Close)
	agent.mockModel = &mockChatModel{responses: []*schema.Message{{
		Role: schema.Assistant, Content: "answer",
	}}}
	ctx := t.Context()
	source, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: source.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("question")},
	}); err != nil {
		t.Fatal(err)
	}
	agent.restoreForkEngineFn = func(
		context.Context,
		acpsdk.SessionId,
		string,
	) (*engine.QueryEngine, *session.ResumedSession, error) {
		return nil, nil, fmt.Errorf("injected fork restore failure")
	}

	if _, err := agent.UnstableForkSession(ctx, acpsdk.UnstableForkSessionRequest{
		SessionId: source.SessionId,
		Cwd:       cwd,
	}); err == nil || !strings.Contains(err.Error(), "injected fork restore failure") {
		t.Fatalf("fork restore error = %v", err)
	}
	agent.mu.Lock()
	sessionCount := len(agent.sessions)
	agent.mu.Unlock()
	if sessionCount != 1 {
		t.Fatalf("failed fork registered %d ACP sessions", sessionCount)
	}
	transcripts, err := filepath.Glob(
		filepath.Join(acpSessionTranscriptDir(cwd), "*.jsonl"),
	)
	if err != nil || len(transcripts) != 1 {
		t.Fatalf("failed fork transcripts = %#v, err=%v", transcripts, err)
	}
	if filepath.Base(transcripts[0]) != string(source.SessionId)+".jsonl" {
		t.Fatalf("failed fork retained unexpected transcript %q", transcripts[0])
	}
}

func TestForkSessionSerializesWithAgentClose(t *testing.T) {
	cwd := t.TempDir()
	agent, err := NewAgent(Config{
		CWD:          cwd,
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.Close)
	agent.mockModel = &mockChatModel{responses: []*schema.Message{{
		Role: schema.Assistant, Content: "answer",
	}}}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	source, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: source.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("question")},
	}); err != nil {
		t.Fatal(err)
	}

	lifecycleHeld := make(chan bool, 1)
	allowRestore := make(chan struct{})
	agent.restoreForkEngineFn = func(
		restoreCtx context.Context,
		childID acpsdk.SessionId,
		childCWD string,
	) (*engine.QueryEngine, *session.ResumedSession, error) {
		held := true
		if agent.sessionLifecycleMu.TryLock() {
			held = false
			agent.sessionLifecycleMu.Unlock()
		}
		lifecycleHeld <- held
		<-allowRestore
		return agent.restoreEngineForSession(restoreCtx, childID, childCWD)
	}

	type forkResult struct {
		response acpsdk.UnstableForkSessionResponse
		err      error
	}
	forkDone := make(chan forkResult, 1)
	go func() {
		response, forkErr := agent.UnstableForkSession(
			ctx,
			acpsdk.UnstableForkSessionRequest{
				SessionId: source.SessionId,
				Cwd:       cwd,
			},
		)
		forkDone <- forkResult{response: response, err: forkErr}
	}()

	held := <-lifecycleHeld
	closeStarted := make(chan struct{})
	closeDone := make(chan struct{})
	go func() {
		close(closeStarted)
		agent.Close()
		close(closeDone)
	}()
	<-closeStarted
	close(allowRestore)

	result := <-forkDone
	<-closeDone
	if result.err != nil {
		t.Fatalf("fork during close: %v", result.err)
	}
	if !held {
		t.Fatal("fork restore ran outside the session lifecycle boundary")
	}
	if result.response.SessionId == "" {
		t.Fatal("fork returned an empty child session ID")
	}
	agent.mu.Lock()
	activeCount := len(agent.sessions)
	agent.mu.Unlock()
	if activeCount != 0 {
		t.Fatalf("agent close left %d forked sessions active", activeCount)
	}
}

// --- Streaming Round-trip Test ---

func TestACP_StreamingRoundTrip(t *testing.T) {
	// Model returns multiple token-like responses
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "streaming token 1"},
			{Role: schema.Assistant, Content: "streaming token 2"},
		},
	}

	conn, client := setupTestACP(t, mockModel)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
		return
	}

	sess, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
		return
	}

	resp, err := conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("stream tokens")},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
		return
	}

	if resp.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("expected end_turn, got %q", resp.StopReason)
	}

	// Verify we got at least one text update
	updates := client.getUpdates()
	var textCount int
	for _, u := range updates {
		if u.Update.AgentMessageChunk != nil && u.Update.AgentMessageChunk.Content.Text != nil {
			textCount++
		}
	}
	if textCount == 0 {
		t.Fatal("expected at least one streaming text update")
	}
}

// --- Tool Approval Flow Integration Test ---

type planSelectingPermissionClient struct {
	permissionTrackingClient
	optionID    string
	optionIDs   []string
	optionIndex int
	errAt       int
	err         error
	request     acpsdk.RequestPermissionRequest
	callIDs     []acpsdk.ToolCallId
}

func (c *planSelectingPermissionClient) RequestPermission(
	_ context.Context,
	request acpsdk.RequestPermissionRequest,
) (acpsdk.RequestPermissionResponse, error) {
	c.mu.Lock()
	c.permRequestCount++
	c.request = request
	c.callIDs = append(c.callIDs, request.ToolCall.ToolCallId)
	c.lifecycle = append(
		c.lifecycle,
		"permission:"+string(request.ToolCall.ToolCallId),
	)
	requestCount := c.permRequestCount
	c.mu.Unlock()
	if c.err != nil && requestCount == c.errAt {
		return acpsdk.RequestPermissionResponse{}, c.err
	}
	optionID := c.optionID
	if c.optionIndex < len(c.optionIDs) {
		optionID = c.optionIDs[c.optionIndex]
		c.optionIndex++
	}
	if optionID == "plan_bypass" && len(c.optionIDs) == 0 {
		for _, option := range request.Options {
			if string(option.OptionId) == "plan_bypass_confirm" {
				return acpsdk.RequestPermissionResponse{Outcome: acpsdk.RequestPermissionOutcome{Selected: &acpsdk.RequestPermissionOutcomeSelected{OptionId: option.OptionId}}}, nil
			}
		}
	}
	for _, option := range request.Options {
		if string(option.OptionId) == optionID {
			return acpsdk.RequestPermissionResponse{
				Outcome: acpsdk.RequestPermissionOutcome{
					Selected: &acpsdk.RequestPermissionOutcomeSelected{
						OptionId: option.OptionId,
					},
				},
			}, nil
		}
	}
	return acpsdk.RequestPermissionResponse{
		Outcome: acpsdk.RequestPermissionOutcome{
			Selected: &acpsdk.RequestPermissionOutcomeSelected{OptionId: "unknown"},
		},
	}, nil
}

func TestACPProjectGraphPlanDecisionUsesProductionResolver(t *testing.T) {
	tests := []struct {
		name          string
		optionID      string
		wantOutcome   engine.PlanApprovalOutcome
		wantDecision  engine.PermissionInteractionDecision
		wantExecuted  int64
		wantPhase     engine.PlanPhase
		wantMode      permission.Mode
		wantRequests  int
		wantConfirmed bool
	}{
		{
			name:         "approve",
			optionID:     "plan_manual",
			wantOutcome:  engine.PlanApprovalApprove,
			wantDecision: engine.PermissionAllowOnce,
			wantExecuted: 1,
			wantPhase:    engine.PlanPhaseInactive,
			wantMode:     permission.ModeDefault,
			wantRequests: 1,
		},
		{
			name:          "bypass",
			optionID:      "plan_bypass",
			wantOutcome:   engine.PlanApprovalApprove,
			wantDecision:  engine.PermissionAllowOnce,
			wantExecuted:  1,
			wantPhase:     engine.PlanPhaseInactive,
			wantMode:      permission.ModeBypassPermissions,
			wantRequests:  2,
			wantConfirmed: true,
		},
		{
			name:         "cancel",
			optionID:     "plan_reject",
			wantOutcome:  engine.PlanApprovalCancel,
			wantDecision: engine.PermissionDeny,
			wantExecuted: 0,
			wantPhase:    engine.PlanPhaseActive,
			wantMode:     permission.ModePlan,
			wantRequests: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			sessionID := "acp-plan-" + test.name
			if err := tools.SavePlan(
				sessionID,
				"",
				"# Reviewed ACP Plan\n",
			); err != nil {
				t.Fatal(err)
			}

			var executions atomic.Int64
			registry := tools.NewRegistry()
			registry.Register(tools.ToolImpl{
				Info:                 &schema.ToolInfo{Name: "ExitPlanMode"},
				IsPlanModeTransition: true,
				Execute: func(string) (string, error) {
					executions.Add(1)
					return "exited", nil
				},
			})
			root := t.TempDir()
			query := engine.NewQueryEngine(engine.QueryEngineConfig{
				SessionID:      sessionID,
				ThreadID:       sessionID + "-thread",
				TranscriptDir:  filepath.Join(root, "transcripts"),
				CWD:            root,
				PermissionMode: permission.ModePlan,
				ChatModel: &mockChatModel{responses: []*schema.Message{{
					Role: schema.Assistant,
					ToolCalls: []schema.ToolCall{{
						ID:   sessionID + "-exit",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "ExitPlanMode",
							Arguments: `{}`,
						},
					}},
				}}},
				ToolRegistry:  registry,
				ToolSelection: &tools.ToolSelection{Names: []string{"ExitPlanMode"}},
				MaxTurns:      2,
				PermissionPrompt: func(
					context.Context,
					engine.PermissionPromptRequest,
				) engine.PermissionInteractionResult {
					t.Fatal("ProjectGraph called the blocking ACP Plan adapter")
					return engine.PermissionInteractionResult{
						Decision: engine.PermissionDeny,
					}
				},
			})
			t.Cleanup(query.Close)

			agent := &Agent{
				permissionTimeout: time.Second,
				sessions: map[acpsdk.SessionId]*Session{
					acpsdk.SessionId(sessionID): newSession(
						acpsdk.SessionId(sessionID),
						nil,
						root,
					),
				},
			}
			client := &planSelectingPermissionClient{
				optionID: test.optionID,
			}
			c2aR, c2aW := io.Pipe()
			a2cR, a2cW := io.Pipe()
			agentConn := acpsdk.NewAgentSideConnection(agent, a2cW, c2aR)
			agent.SetConnection(agentConn)
			_ = acpsdk.NewClientSideConnection(client, c2aW, a2cR)
			t.Cleanup(func() {
				_ = c2aW.Close()
				_ = a2cW.Close()
				_ = c2aR.Close()
				_ = a2cR.Close()
			})

			events, _ := query.SubmitMessage(
				context.Background(),
				"review Plan",
			)
			var request *engine.PermissionRequestEvent
			for event := range events {
				if err := agent.streamEvent(
					context.Background(),
					acpsdk.SessionId(sessionID),
					event,
				); err != nil {
					t.Fatalf("stream initial ACP Plan event: %v", err)
				}
				if event.Type == engine.EventPermissionRequest {
					request = event.PermissionRequest
				}
			}
			if request == nil || request.Source != "project_graph" ||
				request.PlanApproval == nil {
				t.Fatalf("ProjectGraph Plan request = %#v", request)
			}

			if err := agent.resolveProjectGraphPermission(
				context.Background(),
				acpsdk.SessionId(sessionID),
				query,
				*request,
			); err != nil {
				t.Fatal(err)
			}
			client.mu.Lock()
			requestCount := client.permRequestCount
			client.mu.Unlock()
			items := query.RuntimeItems()
			if len(items) != 1 ||
				items[0].PermissionDecision == nil ||
				items[0].PermissionDecision.Result.Decision !=
					test.wantDecision ||
				items[0].PermissionDecision.Result.PlanApproval == nil ||
				items[0].PermissionDecision.Result.PlanApproval.Outcome !=
					test.wantOutcome ||
				items[0].PermissionDecision.Result.PlanApproval.Confirmed !=
					test.wantConfirmed ||
				items[0].PermissionDecision.Result.PlanApproval.Approved {
				t.Fatalf("ACP typed Plan runtime item = %#v", items)
			}
			if requestCount != test.wantRequests || executions.Load() != 0 {
				t.Fatalf(
					"before resume requests=%d executions=%d, want requests=%d executions=0",
					requestCount,
					executions.Load(),
					test.wantRequests,
				)
			}

			item, ok, err := query.ClaimNextRuntimeItem()
			if err != nil || !ok {
				t.Fatalf(
					"claim ACP Plan item=%#v ok=%v err=%v",
					item,
					ok,
					err,
				)
			}
			resumed, _ := query.SubmitRuntimeItem(context.Background(), item)
			for event := range resumed {
				if err := agent.streamEvent(
					context.Background(),
					acpsdk.SessionId(sessionID),
					event,
				); err != nil {
					t.Fatalf("stream resumed ACP Plan event: %v", err)
				}
			}
			client.mu.Lock()
			lifecycle := append([]string(nil), client.lifecycle...)
			client.mu.Unlock()
			toolID := sessionID + "-exit"
			wantLifecycle := []string{"start:" + toolID}
			for range test.wantRequests {
				wantLifecycle = append(wantLifecycle, "permission:"+toolID)
			}
			wantLifecycle = append(wantLifecycle, "terminal:"+toolID)
			if len(lifecycle) != len(wantLifecycle) {
				t.Fatalf("ACP Plan lifecycle = %#v, want %#v", lifecycle, wantLifecycle)
			}
			for index := range wantLifecycle {
				if lifecycle[index] != wantLifecycle[index] {
					t.Fatalf("ACP Plan lifecycle = %#v, want %#v", lifecycle, wantLifecycle)
				}
			}
			if executions.Load() != test.wantExecuted ||
				query.PlanState().Phase != test.wantPhase ||
				query.PermissionMode() != test.wantMode ||
				query.GetApprovalTracker().Count() != 0 {
				t.Fatalf(
					"ACP Plan result executions=%d state=%#v mode=%q grants=%d",
					executions.Load(),
					query.PlanState(),
					query.PermissionMode(),
					query.GetApprovalTracker().Count(),
				)
			}
		})
	}
}

func TestACPPlanApprovalReusesToolIdentityAcrossStructuredTargets(t *testing.T) {
	tests := []struct {
		optionID  string
		optionIDs []string
		allowed   bool
		target    permission.Mode
		outcome   engine.PlanApprovalOutcome
	}{
		{optionID: "plan_manual", allowed: true, target: permission.ModeDontAsk, outcome: engine.PlanApprovalApprove},
		{optionID: "plan_accept_edits", allowed: true, target: permission.ModeAcceptEdits, outcome: engine.PlanApprovalApprove},
		{optionID: "plan_bypass", allowed: true, target: permission.ModeBypassPermissions, outcome: engine.PlanApprovalApprove},
		{optionID: "back_then_edits", optionIDs: []string{"plan_bypass", "plan_bypass_back", "plan_accept_edits"}, allowed: true, target: permission.ModeAcceptEdits, outcome: engine.PlanApprovalApprove},
		{optionID: "plan_reject", target: permission.ModePlan, outcome: engine.PlanApprovalCancel},
		{optionID: "unknown", target: permission.ModePlan, outcome: engine.PlanApprovalCancel},
	}
	for _, test := range tests {
		t.Run(test.optionID, func(t *testing.T) {
			planPath := filepath.Join(t.TempDir(), "plan.md")
			planBytes := []byte("# Reviewed Plan\n\nExact ACP bytes")
			if err := os.WriteFile(
				planPath,
				planBytes,
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			agent := &Agent{
				permissionTimeout: time.Second,
				sessions: map[acpsdk.SessionId]*Session{
					"session-1": newSession("session-1", nil, ""),
				},
			}
			client := &planSelectingPermissionClient{optionID: test.optionID, optionIDs: test.optionIDs}
			c2aR, c2aW := io.Pipe()
			a2cR, a2cW := io.Pipe()
			agentConn := acpsdk.NewAgentSideConnection(agent, a2cW, c2aR)
			agent.SetConnection(agentConn)
			_ = acpsdk.NewClientSideConnection(client, c2aW, a2cR)
			t.Cleanup(func() {
				_ = c2aW.Close()
				_ = a2cW.Close()
				_ = c2aR.Close()
				_ = a2cR.Close()
			})

			result := agent.makeACPPermissionPrompt("session-1")(
				context.Background(),
				engine.PermissionPromptRequest{
					ToolName:  "ExitPlanMode",
					ToolUseID: "plan-1",
					PlanApproval: &engine.PlanApprovalRequest{
						RequestID:        "plan-request-1",
						PlanRevision:     5,
						PlanFileIdentity: planPath,
						ReturnMode:       permission.ModeDontAsk,
					},
				},
			)
			if result.PlanApproval == nil ||
				result.PlanApproval.RequestID != "plan-request-1" ||
				result.PlanApproval.PlanRevision != 5 ||
				result.PlanApproval.Approved ||
				result.PlanApproval.TargetMode != test.target ||
				result.PlanApproval.Outcome != test.outcome {
				t.Fatalf("result = %#v", result)
			}
			wantDigest := ""
			if test.allowed {
				wantDigest = engine.PlanBytesDigest(planBytes)
			}
			if result.PlanApproval.ReviewedPlanDigest != wantDigest {
				t.Fatalf(
					"reviewed digest = %q, want %q",
					result.PlanApproval.ReviewedPlanDigest,
					wantDigest,
				)
			}
			if test.allowed &&
				result.Decision != engine.PermissionAllowOnce {
				t.Fatalf("approved result = %#v", result)
			}
			if test.target == permission.ModeBypassPermissions &&
				!result.PlanApproval.Confirmed {
				t.Fatalf("bypass was not explicitly confirmed: %#v", result)
			}
			if !test.allowed && result.Decision != engine.PermissionDeny {
				t.Fatalf("rejected result = %#v", result)
			}
			client.mu.Lock()
			requests := client.permRequestCount
			ids := append([]acpsdk.ToolCallId(nil), client.callIDs...)
			client.mu.Unlock()
			if len(test.optionIDs) > 0 {
				if requests != 3 {
					t.Fatalf("Back request count = %d, want 3", requests)
				}
			}
			if len(ids) == 0 {
				t.Fatal("ACP Plan permission request was not sent")
			}
			for _, id := range ids {
				if id != "plan-1" {
					t.Fatalf("transport IDs = %#v, want only plan-1", ids)
				}
			}

			client.mu.Lock()
			request := client.request
			client.mu.Unlock()
			wantOptions := 4
			if test.target == permission.ModeBypassPermissions {
				wantOptions = 2
			}
			if len(request.Options) != wantOptions {
				t.Fatalf("options = %#v", request.Options)
			}
			if test.target != permission.ModeBypassPermissions && (len(request.ToolCall.Content) != 1 ||
				request.ToolCall.Content[0].Content == nil ||
				request.ToolCall.Content[0].Content.Content.Text == nil ||
				request.ToolCall.Content[0].Content.Content.Text.Text !=
					string(planBytes)) {
				t.Fatalf(
					"ACP Plan bytes = %#v",
					request.ToolCall.Content,
				)
			}
			for _, option := range request.Options {
				if option.OptionId == "allow" || option.OptionId == "allow_always" {
					t.Fatalf("generic grant option leaked into Plan approval: %#v", request.Options)
				}
			}
			if test.target != permission.ModeBypassPermissions && !strings.Contains(
				request.Options[0].Name,
				string(permission.ModeDontAsk),
			) {
				t.Fatalf("previous mode option = %#v", request.Options[0])
			}
		})
	}
}

func TestACPPlanApprovalRejectsMissingToolIdentity(t *testing.T) {
	agent := &Agent{
		permissionTimeout: time.Second,
		sessions: map[acpsdk.SessionId]*Session{
			"session-1": newSession("session-1", nil, ""),
		},
	}
	var permissionCalls atomic.Int64
	agent.planPermissionRequestFn = func(
		context.Context,
		acpsdk.RequestPermissionRequest,
	) (acpsdk.RequestPermissionResponse, error) {
		permissionCalls.Add(1)
		return acpsdk.RequestPermissionResponse{}, nil
	}
	client := &planSelectingPermissionClient{optionID: "plan_manual"}
	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()
	agentConn := acpsdk.NewAgentSideConnection(agent, a2cW, c2aR)
	agent.SetConnection(agentConn)
	_ = acpsdk.NewClientSideConnection(client, c2aW, a2cR)
	t.Cleanup(func() {
		_ = c2aW.Close()
		_ = a2cW.Close()
		_ = c2aR.Close()
		_ = a2cR.Close()
	})

	result := agent.makeACPPermissionPrompt("session-1")(
		context.Background(),
		engine.PermissionPromptRequest{
			ToolName:  "ExitPlanMode",
			ToolUseID: " \t",
			PlanApproval: &engine.PlanApprovalRequest{
				RequestID:        "plan-request-1",
				PlanRevision:     1,
				PlanFileIdentity: filepath.Join(t.TempDir(), "missing-plan.md"),
			},
		},
	)
	client.mu.Lock()
	clientCalls := client.permRequestCount
	client.mu.Unlock()
	if result.Decision != engine.PermissionDeny ||
		result.PlanApproval == nil ||
		result.PlanApproval.RequestID != "plan-request-1" ||
		result.PlanApproval.PlanRevision != 1 ||
		result.PlanApproval.Outcome != engine.PlanApprovalCancel ||
		result.PlanApproval.Confirmed ||
		permissionCalls.Load() != 0 ||
		clientCalls != 0 {
		t.Fatalf(
			"missing identity result=%#v adapter calls=%d client calls=%d",
			result,
			permissionCalls.Load(),
			clientCalls,
		)
	}
}

func TestACPPlanBypassSecondRoundFailureFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want engine.PermissionInteractionDecision
	}{
		{"deadline", context.DeadlineExceeded, engine.PermissionTimedOut},
		{"cancel", context.Canceled, engine.PermissionCancelled},
		{"transport", errors.New("delivery lost"), engine.PermissionDeny},
	} {
		t.Run(test.name, func(t *testing.T) {
			planPath := filepath.Join(t.TempDir(), "plan.md")
			if err := os.WriteFile(planPath, []byte("# Plan"), 0o600); err != nil {
				t.Fatal(err)
			}
			agent := &Agent{
				permissionTimeout: time.Second,
				sessions: map[acpsdk.SessionId]*Session{
					"s": newSession("s", nil, ""),
				},
			}
			client := &planSelectingPermissionClient{optionID: "plan_bypass", optionIDs: []string{"plan_bypass"}, errAt: 2, err: test.err}
			c2aR, c2aW := io.Pipe()
			a2cR, a2cW := io.Pipe()
			agentConn := acpsdk.NewAgentSideConnection(agent, a2cW, c2aR)
			agent.SetConnection(agentConn)
			_ = acpsdk.NewClientSideConnection(client, c2aW, a2cR)
			t.Cleanup(func() { _ = c2aW.Close(); _ = a2cW.Close(); _ = c2aR.Close(); _ = a2cR.Close() })
			got := agent.makeACPPermissionPrompt("s")(context.Background(), engine.PermissionPromptRequest{ToolName: "ExitPlanMode", ToolUseID: "r", PlanApproval: &engine.PlanApprovalRequest{RequestID: "r", PlanRevision: 1, PlanFileIdentity: planPath}})
			if got.Decision != test.want || got.PlanApproval == nil || got.PlanApproval.Outcome != engine.PlanApprovalCancel || got.PlanApproval.Confirmed {
				t.Fatalf("failure result = %#v", got)
			}
		})
	}
}

func TestACPPlanBypassDeadlineIsNotRefreshed(t *testing.T) {
	planPath := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	var deadlines []time.Time
	agent := &Agent{permissionTimeout: 200 * time.Millisecond}
	agent.planPermissionRequestFn = func(ctx context.Context, req acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
		d, ok := ctx.Deadline()
		if !ok {
			t.Fatal("missing deadline")
		}
		deadlines = append(deadlines, d)
		if len(deadlines) == 1 {
			remaining := time.Until(d) - 20*time.Millisecond
			if remaining > 0 {
				<-time.After(remaining)
			}
			return acpsdk.RequestPermissionResponse{Outcome: acpsdk.RequestPermissionOutcome{Selected: &acpsdk.RequestPermissionOutcomeSelected{OptionId: "plan_bypass"}}}, nil
		}
		<-ctx.Done()
		return acpsdk.RequestPermissionResponse{}, ctx.Err()
	}
	got := agent.requestACPPlanApproval(context.Background(), "s", engine.PermissionPromptRequest{ToolName: "ExitPlanMode", ToolUseID: "deadline-plan", PlanApproval: &engine.PlanApprovalRequest{RequestID: "r", PlanRevision: 1, PlanFileIdentity: planPath}})
	if len(deadlines) != 2 || deadlines[0].IsZero() || !deadlines[0].Equal(deadlines[1]) || got.Decision != engine.PermissionTimedOut || got.PlanApproval == nil || got.PlanApproval.Outcome != engine.PlanApprovalCancel || got.PlanApproval.Confirmed {
		t.Fatalf("deadlines=%#v result=%#v", deadlines, got)
	}
}

func TestACPPlanPreviousBypassStillRequiresSecondConfirmation(t *testing.T) {
	planPath := filepath.Join(t.TempDir(), "plan.md")
	planBytes := []byte("# Previous bypass Plan")
	if err := os.WriteFile(planPath, planBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	var (
		callIDs   []acpsdk.ToolCallId
		deadlines []time.Time
	)
	agent := &Agent{permissionTimeout: time.Second}
	agent.planPermissionRequestFn = func(
		ctx context.Context,
		request acpsdk.RequestPermissionRequest,
	) (acpsdk.RequestPermissionResponse, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("missing Plan interaction deadline")
		}
		callIDs = append(callIDs, request.ToolCall.ToolCallId)
		deadlines = append(deadlines, deadline)
		if len(callIDs) == 1 {
			counts := make(map[string]int, len(request.Options))
			for _, option := range request.Options {
				counts[string(option.OptionId)]++
			}
			if len(request.Options) != 3 ||
				counts["plan_bypass"] != 1 ||
				counts["plan_accept_edits"] != 1 ||
				counts["plan_reject"] != 1 {
				t.Fatalf("previous-bypass targets = %#v", request.Options)
			}
			return acpsdk.RequestPermissionResponse{
				Outcome: acpsdk.RequestPermissionOutcome{
					Selected: &acpsdk.RequestPermissionOutcomeSelected{
						OptionId: "plan_bypass",
					},
				},
			}, nil
		}
		if len(request.Options) != 2 {
			t.Fatalf("bypass confirmation options = %#v", request.Options)
		}
		return acpsdk.RequestPermissionResponse{
			Outcome: acpsdk.RequestPermissionOutcome{
				Selected: &acpsdk.RequestPermissionOutcomeSelected{
					OptionId: "plan_bypass_confirm",
				},
			},
		}, nil
	}
	got := agent.requestACPPlanApproval(
		context.Background(),
		"s",
		engine.PermissionPromptRequest{
			ToolName:  "ExitPlanMode",
			ToolUseID: "previous-bypass-tool",
			PlanApproval: &engine.PlanApprovalRequest{
				RequestID:        "previous-bypass",
				PlanRevision:     9,
				PlanFileIdentity: planPath,
				ReturnMode:       permission.ModeBypassPermissions,
			},
		},
	)
	if len(callIDs) != 2 ||
		callIDs[0] != "previous-bypass-tool" ||
		callIDs[1] != "previous-bypass-tool" ||
		len(deadlines) != 2 ||
		!deadlines[0].Equal(deadlines[1]) ||
		got.Decision != engine.PermissionAllowOnce ||
		got.PlanApproval == nil ||
		got.PlanApproval.RequestID != "previous-bypass" ||
		got.PlanApproval.PlanRevision != 9 ||
		got.PlanApproval.Outcome != engine.PlanApprovalApprove ||
		got.PlanApproval.TargetMode != permission.ModeBypassPermissions ||
		!got.PlanApproval.Confirmed ||
		got.PlanApproval.ReviewedPlanDigest != engine.PlanBytesDigest(planBytes) {
		t.Fatalf("callIDs=%#v deadlines=%#v result=%#v", callIDs, deadlines, got)
	}
}

func TestACPPlanBypassParentCancelBetweenRoundsFailsClosed(t *testing.T) {
	planPath := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls int
	agent := &Agent{permissionTimeout: time.Second}
	agent.planPermissionRequestFn = func(
		ctx context.Context,
		_ acpsdk.RequestPermissionRequest,
	) (acpsdk.RequestPermissionResponse, error) {
		calls++
		if calls == 1 {
			cancel()
			return acpsdk.RequestPermissionResponse{
				Outcome: acpsdk.RequestPermissionOutcome{
					Selected: &acpsdk.RequestPermissionOutcomeSelected{
						OptionId: "plan_bypass",
					},
				},
			}, nil
		}
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("confirmation context error = %v, want canceled", ctx.Err())
		}
		return acpsdk.RequestPermissionResponse{}, ctx.Err()
	}
	got := agent.requestACPPlanApproval(
		parent,
		"s",
		engine.PermissionPromptRequest{
			ToolName:  "ExitPlanMode",
			ToolUseID: "parent-cancel-plan",
			PlanApproval: &engine.PlanApprovalRequest{
				RequestID:        "r",
				PlanRevision:     1,
				PlanFileIdentity: planPath,
			},
		},
	)
	if calls != 2 ||
		got.Decision != engine.PermissionCancelled ||
		got.PlanApproval == nil ||
		got.PlanApproval.Outcome != engine.PlanApprovalCancel ||
		got.PlanApproval.Confirmed {
		t.Fatalf("calls=%d result=%#v", calls, got)
	}
}

func TestACPPermissionTerminalResultEmitsTypedPlanCancel(t *testing.T) {
	request := engine.PermissionPromptRequest{
		PlanApproval: &engine.PlanApprovalRequest{
			RequestID: "plan-terminal", PlanRevision: 11,
		},
	}
	for _, decision := range []engine.PermissionInteractionDecision{
		engine.PermissionCancelled,
		engine.PermissionTimedOut,
		engine.PermissionDeny,
	} {
		got := acpPermissionTerminalResult(request, decision, "terminal")
		if got.Decision != decision ||
			got.PlanApproval == nil ||
			got.PlanApproval.RequestID != "plan-terminal" ||
			got.PlanApproval.PlanRevision != 11 ||
			got.PlanApproval.Outcome != engine.PlanApprovalCancel ||
			got.PlanApproval.Approved ||
			got.PlanApproval.TargetMode != permission.ModePlan {
			t.Fatalf("decision %q result = %#v", decision, got)
		}
	}
}

func TestACP_ToolApprovalFlow_Approve(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "call_approval_1",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "Read",
						Arguments: `{"file_path": "/tmp/approval_test.txt"}`,
					},
				}},
			},
			{Role: schema.Assistant, Content: "Tool approved and executed."},
		},
	}

	// Use permission-enabled agent with auto-approving client
	agent, err := NewAgent(Config{
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     false,
		MaxTurns:     10,
		CWD:          t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	agent.mockModel = mockModel

	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	approvalClient := &permissionTrackingClient{}
	agentConn := acpsdk.NewAgentSideConnection(agent, a2cW, c2aR)
	agent.SetConnection(agentConn)
	clientConn := acpsdk.NewClientSideConnection(approvalClient, c2aW, a2cR)

	t.Cleanup(func() {
		agent.Close()
		_ = c2aW.Close()
		_ = a2cW.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = clientConn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
		return
	}

	sess, err := clientConn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
		return
	}

	resp, err := clientConn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("read a file with approval")},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
		return
	}

	if resp.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("expected end_turn, got %q", resp.StopReason)
	}

	// Verify permission was requested and approved
	approvalClient.mu.Lock()
	count := approvalClient.permRequestCount
	approvalClient.mu.Unlock()
	if count == 0 {
		t.Fatal("expected at least one permission request")
	}
}

func TestACP_ToolApprovalFlow_Deny(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "call_deny_1",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "Write",
						Arguments: `{"file_path": "/tmp/deny_test.txt", "content": "x"}`,
					},
				}},
			},
			{Role: schema.Assistant, Content: "Permission was denied."},
		},
	}

	agent, err := NewAgent(Config{
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     false,
		MaxTurns:     10,
		CWD:          t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	agent.mockModel = mockModel

	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	denyClient := &denyingPermissionClient{}
	agentConn := acpsdk.NewAgentSideConnection(agent, a2cW, c2aR)
	agent.SetConnection(agentConn)
	clientConn := acpsdk.NewClientSideConnection(denyClient, c2aW, a2cR)

	t.Cleanup(func() {
		agent.Close()
		_ = c2aW.Close()
		_ = a2cW.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = clientConn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
		return
	}

	sess, err := clientConn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
		return
	}

	// This should complete without error even though permission was denied
	resp, err := clientConn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("write a file (should be denied)")},
	})
	if err != nil {
		t.Fatalf("Prompt should complete on deny: %v", err)
		return
	}
	_ = resp
	denyClient.mu.Lock()
	updates := append(
		[]acpsdk.SessionNotification(nil),
		denyClient.updates...,
	)
	denyClient.mu.Unlock()
	starts := 0
	failedTerminals := 0
	for _, update := range updates {
		if update.Update.ToolCall != nil &&
			update.Update.ToolCall.ToolCallId == "call_deny_1" {
			starts++
		}
		if update.Update.ToolCallUpdate != nil &&
			update.Update.ToolCallUpdate.ToolCallId == "call_deny_1" &&
			update.Update.ToolCallUpdate.Status != nil &&
			*update.Update.ToolCallUpdate.Status ==
				acpsdk.ToolCallStatusFailed {
			failedTerminals++
		}
	}
	if starts != 1 || failedTerminals != 1 {
		t.Fatalf(
			"denied lifecycle starts=%d failed_terminals=%d updates=%#v",
			starts,
			failedTerminals,
			updates,
		)
	}
}

// denyingPermissionClient always denies permission requests.
type denyingPermissionClient struct {
	mu      sync.Mutex
	updates []acpsdk.SessionNotification
}

func (c *denyingPermissionClient) ReadTextFile(ctx context.Context, p acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error) {
	return acpsdk.ReadTextFileResponse{Content: "mock"}, nil
}

func (c *denyingPermissionClient) WriteTextFile(ctx context.Context, p acpsdk.WriteTextFileRequest) (acpsdk.WriteTextFileResponse, error) {
	return acpsdk.WriteTextFileResponse{}, nil
}

func (c *denyingPermissionClient) RequestPermission(ctx context.Context, p acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	// Find the reject option
	for _, opt := range p.Options {
		if opt.Kind == acpsdk.PermissionOptionKindRejectOnce {
			return acpsdk.RequestPermissionResponse{
				Outcome: acpsdk.RequestPermissionOutcome{
					Selected: &acpsdk.RequestPermissionOutcomeSelected{OptionId: opt.OptionId},
				},
			}, nil
		}
	}
	return acpsdk.RequestPermissionResponse{
		Outcome: acpsdk.RequestPermissionOutcome{Cancelled: &acpsdk.RequestPermissionOutcomeCancelled{}},
	}, nil
}

func (c *denyingPermissionClient) SessionUpdate(ctx context.Context, n acpsdk.SessionNotification) error {
	c.mu.Lock()
	c.updates = append(c.updates, n)
	c.mu.Unlock()
	return nil
}

func (c *denyingPermissionClient) CreateTerminal(ctx context.Context, p acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	return acpsdk.CreateTerminalResponse{TerminalId: "test-terminal"}, nil
}

func (c *denyingPermissionClient) KillTerminal(ctx context.Context, p acpsdk.KillTerminalRequest) (acpsdk.KillTerminalResponse, error) {
	return acpsdk.KillTerminalResponse{}, nil
}

func (c *denyingPermissionClient) ReleaseTerminal(ctx context.Context, p acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	return acpsdk.ReleaseTerminalResponse{}, nil
}

func (c *denyingPermissionClient) TerminalOutput(ctx context.Context, p acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	return acpsdk.TerminalOutputResponse{Output: "ok", Truncated: false}, nil
}

func (c *denyingPermissionClient) WaitForTerminalExit(ctx context.Context, p acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error) {
	return acpsdk.WaitForTerminalExitResponse{}, nil
}

// --- Concurrent Session Access Tests ---

func TestACP_ConcurrentPrompts_DifferentSessions(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "response 1"},
			{Role: schema.Assistant, Content: "response 2"},
			{Role: schema.Assistant, Content: "response 3"},
			{Role: schema.Assistant, Content: "response 4"},
		},
	}

	agent, err := NewAgent(Config{
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
		CWD:          t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	agent.mockModel = mockModel

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create multiple sessions
	var sessions []acpsdk.SessionId
	for i := 0; i < 4; i++ {
		resp, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
		if err != nil {
			t.Fatalf("NewSession %d: %v", i, err)
			return
		}
		sessions = append(sessions, resp.SessionId)
	}

	// Prompt all sessions concurrently
	var wg sync.WaitGroup
	errors := make([]error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := agent.Prompt(ctx, acpsdk.PromptRequest{
				SessionId: sessions[idx],
				Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("concurrent prompt")},
			})
			errors[idx] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errors {
		if err != nil {
			t.Errorf("session %d prompt error: %v", i, err)
		}
	}
}

// --- Error Handling: Malformed Request Test ---

func TestACP_MalformedPromptContent(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "should not reach"},
		},
	}

	conn, _ := setupTestACP(t, mockModel)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
		return
	}

	sess, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
		return
	}

	// Prompt with nil content — the ACP SDK validates that prompt is required,
	// so this should return an error (invalid params).
	_, err = conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    nil, // nil prompt triggers SDK validation
	})
	if err == nil {
		t.Fatal("expected error for nil prompt content (SDK validates prompt is required)")
		return
	}

	// Prompt with empty content blocks (empty text results in end_turn)
	resp, err := conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []acpsdk.ContentBlock{}, // empty slice is valid
	})
	if err != nil {
		t.Fatalf("Prompt with empty content blocks should not fail: %v", err)
		return
	}
	if resp.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("expected end_turn for empty prompt, got %q", resp.StopReason)
	}
}

// --- Protocol Version Verification ---

func TestACP_Initialize_ReturnsProtocolVersion(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "hello"},
		},
	}

	conn, _ := setupTestACP(t, mockModel)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
		return
	}

	if resp.ProtocolVersion != acpsdk.ProtocolVersionNumber {
		t.Fatalf("protocol version = %d, want %d", resp.ProtocolVersion, acpsdk.ProtocolVersionNumber)
	}
}

// --- Disconnect Handling Test ---

// TestACP_ClientDisconnectCleanup verifies that when a client disconnects
// (nil connection), the agent processes prompts without blocking or panicking.
// This complements TestACP_DisconnectedClient_NoBlock by verifying with a model
// that returns normally after the connection is gone.
func TestACP_ClientDisconnectCleanup(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "response despite disconnect"},
			{Role: schema.Assistant, Content: "second response"},
		},
	}

	agent, err := NewAgent(Config{
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
		CWD:          t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	agent.mockModel = mockModel
	// No connection — simulates disconnected client
	agent.conn = nil

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sessResp, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatal(err)
		return
	}
	t.Cleanup(agent.Close)

	// Multiple prompts should all succeed without connection
	for i := 0; i < 2; i++ {
		resp, err := agent.Prompt(ctx, acpsdk.PromptRequest{
			SessionId: sessResp.SessionId,
			Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock(fmt.Sprintf("prompt %d", i))},
		})
		if err != nil {
			t.Fatalf("Prompt %d should succeed with nil conn: %v", i, err)
			return
		}
		if resp.StopReason != acpsdk.StopReasonEndTurn {
			t.Fatalf("Prompt %d: expected end_turn, got %q", i, resp.StopReason)
		}
	}
}

// --- ToolApprovalManager Concurrent Access ---

func TestToolApprovalManager_ConcurrentAccess(t *testing.T) {
	tam := NewToolApprovalManager(5 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sessionID := acpsdk.SessionId(fmt.Sprintf("concurrent-%d", idx))
			resultCh := tam.RequestApproval(ctx, sessionID, acpsdk.ToolCallId(fmt.Sprintf("call_%d", idx)), "Read", nil)
			// Resolve immediately
			tam.Resolve(sessionID, true, "ok")
			select {
			case <-resultCh:
			case <-time.After(2 * time.Second):
				t.Errorf("timeout on concurrent approval %d", idx)
			}
		}(i)
	}
	wg.Wait()
}
