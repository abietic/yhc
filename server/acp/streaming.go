// Package acp implements streaming and protocol compliance extensions for the
// ACP agent.
package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/abietic/yhc/engine"
)

// --- Streaming Response Support ---

// StreamBuffer provides buffered streaming with backpressure for delivering
// tokens to clients. If the client cannot keep up, the buffer accumulates
// up to maxBufferSize bytes before dropping oldest content.
type StreamBuffer struct {
	mu            sync.Mutex
	sessionID     acpsdk.SessionId
	conn          *acpsdk.AgentSideConnection
	buffer        []string
	maxBufferSize int
	totalBuffered int
	dropped       int
	closed        bool
}

// DefaultMaxStreamBufferSize is the default max buffer (64KB).
const DefaultMaxStreamBufferSize = 64 * 1024

// NewStreamBuffer creates a streaming buffer for a session.
func NewStreamBuffer(sessionID acpsdk.SessionId, conn *acpsdk.AgentSideConnection, maxSize int) *StreamBuffer {
	if maxSize <= 0 {
		maxSize = DefaultMaxStreamBufferSize
	}
	return &StreamBuffer{
		sessionID:     sessionID,
		conn:          conn,
		maxBufferSize: maxSize,
	}
}

// Write adds a text chunk to the buffer. If the buffer is full, it drops
// the oldest chunk and increments the drop counter.
func (sb *StreamBuffer) Write(text string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.closed {
		return
	}
	// Check backpressure
	if sb.totalBuffered+len(text) > sb.maxBufferSize && len(sb.buffer) > 0 {
		// Drop oldest chunks until we have room
		for sb.totalBuffered+len(text) > sb.maxBufferSize && len(sb.buffer) > 0 {
			dropped := sb.buffer[0]
			sb.buffer = sb.buffer[1:]
			sb.totalBuffered -= len(dropped)
			sb.dropped++
		}
	}
	sb.buffer = append(sb.buffer, text)
	sb.totalBuffered += len(text)
}

// Flush sends all buffered content to the client and clears the buffer.
// Returns an error if the connection fails (client disconnected).
func (sb *StreamBuffer) Flush(ctx context.Context) error {
	sb.mu.Lock()
	if sb.closed || sb.conn == nil || len(sb.buffer) == 0 {
		sb.mu.Unlock()
		return nil
	}
	// Collect all buffered text
	chunks := make([]string, len(sb.buffer))
	copy(chunks, sb.buffer)
	sb.buffer = sb.buffer[:0]
	sb.totalBuffered = 0
	sb.mu.Unlock()

	// Send each chunk as a session update
	for _, chunk := range chunks {
		if err := sb.conn.SessionUpdate(ctx, acpsdk.SessionNotification{
			SessionId: sb.sessionID,
			Update:    acpsdk.UpdateAgentMessageText(chunk),
		}); err != nil {
			return err
		}
	}
	return nil
}

// Close marks the buffer as closed; further writes are silently discarded.
func (sb *StreamBuffer) Close() {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.closed = true
}

// Stats returns the number of dropped chunks due to backpressure.
func (sb *StreamBuffer) Stats() (dropped int) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.dropped
}

// --- Tool Approval Flow ---

// ToolApprovalRequest represents a pending tool approval request that pauses
// streaming until the client responds.
type ToolApprovalRequest struct {
	SessionID   acpsdk.SessionId
	ToolCallID  acpsdk.ToolCallId
	ToolName    string
	Input       map[string]any
	RequestedAt time.Time
	// result is set when the client responds
	result   chan ToolApprovalResult
	resolved bool
	mu       sync.Mutex
}

// ToolApprovalResult holds the client's response to a tool approval request.
type ToolApprovalResult struct {
	Approved bool
	Reason   string
	TimedOut bool
}

// ToolApprovalManager tracks pending tool approval requests per session,
// supporting the pause/resume streaming flow.
type ToolApprovalManager struct {
	mu      sync.Mutex
	pending map[acpsdk.SessionId]*ToolApprovalRequest
	timeout time.Duration
}

// NewToolApprovalManager creates a new approval manager with the given timeout.
func NewToolApprovalManager(timeout time.Duration) *ToolApprovalManager {
	if timeout <= 0 {
		timeout = PermissionTimeout
	}
	return &ToolApprovalManager{
		pending: make(map[acpsdk.SessionId]*ToolApprovalRequest),
		timeout: timeout,
	}
}

// RequestApproval creates a pending approval request and returns a channel
// that will receive the result (either client response or timeout).
func (tam *ToolApprovalManager) RequestApproval(
	ctx context.Context,
	sessionID acpsdk.SessionId,
	toolCallID acpsdk.ToolCallId,
	toolName string,
	input map[string]any,
) <-chan ToolApprovalResult {
	req := &ToolApprovalRequest{
		SessionID:   sessionID,
		ToolCallID:  toolCallID,
		ToolName:    toolName,
		Input:       input,
		RequestedAt: time.Now(),
		result:      make(chan ToolApprovalResult, 1),
	}

	tam.mu.Lock()
	tam.pending[sessionID] = req
	tam.mu.Unlock()

	// Start timeout goroutine
	go func() {
		timer := time.NewTimer(tam.timeout)
		defer timer.Stop()
		select {
		case <-timer.C:
			tam.completeApproval(sessionID, req, ToolApprovalResult{
				Approved: false,
				Reason:   "permission request timed out",
				TimedOut: true,
			})
		case <-ctx.Done():
			tam.completeApproval(sessionID, req, ToolApprovalResult{
				Approved: false,
				Reason:   "context cancelled",
			})
		}
	}()

	return req.result
}

// Resolve resolves a pending approval request with the given decision.
// Returns false if no pending request exists for the session.
func (tam *ToolApprovalManager) Resolve(sessionID acpsdk.SessionId, approved bool, reason string) bool {
	return tam.completeApproval(sessionID, nil, ToolApprovalResult{
		Approved: approved,
		Reason:   reason,
	})
}

// completeApproval removes the request from the pending set before making the
// buffered result observable. All completion paths use the same lock order so
// timeout, cancellation, and a client decision cannot race a stale pending row.
func (tam *ToolApprovalManager) completeApproval(
	sessionID acpsdk.SessionId,
	expected *ToolApprovalRequest,
	result ToolApprovalResult,
) bool {
	tam.mu.Lock()
	req, ok := tam.pending[sessionID]
	if !ok || (expected != nil && req != expected) {
		tam.mu.Unlock()
		return false
	}

	req.mu.Lock()
	if req.resolved {
		delete(tam.pending, sessionID)
		req.mu.Unlock()
		tam.mu.Unlock()
		return false
	}
	req.resolved = true
	delete(tam.pending, sessionID)
	req.mu.Unlock()
	tam.mu.Unlock()

	req.result <- result
	return true
}

// HasPending returns true if there is a pending approval for the session.
func (tam *ToolApprovalManager) HasPending(sessionID acpsdk.SessionId) bool {
	tam.mu.Lock()
	defer tam.mu.Unlock()
	_, ok := tam.pending[sessionID]
	return ok
}

// --- Protocol Error Handling ---

// ProtocolError codes for JSON-RPC error responses.
const (
	// Standard JSON-RPC error codes
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603

	// ACP-specific error codes
	CodeRequestCancelled            = -32800
	CodeAuthRequired                = -32000
	CodeSessionNotFound             = -32001
	CodeSessionConflict             = -32002
	CodeLegacySessionImportRequired = -32003
	CodeApprovalTimeout             = -32004
	CodeProtocolMismatch            = -32005
	CodeGoalConflict                = -32007
)

// ProtocolError represents a structured JSON-RPC protocol error with code and data.
type ProtocolError struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

func (e *ProtocolError) Error() string {
	if e.Data != nil {
		data, _ := json.Marshal(e.Data)
		return fmt.Sprintf("code %d: %s (data: %s)", e.Code, e.Message, string(data))
	}
	return fmt.Sprintf("code %d: %s", e.Code, e.Message)
}

// NewSessionNotFoundError creates a protocol error for missing sessions.
func NewSessionNotFoundError(sessionID string) *ProtocolError {
	return &ProtocolError{
		Code:    CodeSessionNotFound,
		Message: "Session not found",
		Data:    map[string]any{"sessionId": sessionID},
	}
}

// NewApprovalTimeoutError creates a protocol error for approval timeouts.
func NewApprovalTimeoutError(toolName string) *ProtocolError {
	return &ProtocolError{
		Code:    CodeApprovalTimeout,
		Message: "Tool approval timed out",
		Data:    map[string]any{"tool": toolName},
	}
}

// NewProtocolMismatchError creates a protocol error for version mismatches.
func NewProtocolMismatchError(clientVersion, serverVersion int) *ProtocolError {
	return &ProtocolError{
		Code:    CodeProtocolMismatch,
		Message: "Protocol version mismatch",
		Data:    map[string]any{"clientVersion": clientVersion, "serverVersion": serverVersion},
	}
}

// NewInvalidParamsError creates a protocol error for invalid parameters.
func NewInvalidParamsError(detail string) *ProtocolError {
	return &ProtocolError{
		Code:    CodeInvalidParams,
		Message: "Invalid params",
		Data:    map[string]any{"detail": detail},
	}
}

// NewInternalProtocolError creates a protocol error for internal failures.
func NewInternalProtocolError(detail string) *ProtocolError {
	return &ProtocolError{
		Code:    CodeInternalError,
		Message: "Internal error",
		Data:    map[string]any{"detail": detail},
	}
}

// --- Extension Method Handler ---

// HandleExtensionMethod implements the acpsdk.ExtensionMethodHandler interface.
func (a *Agent) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "_session/status":
		return a.handleSessionStatus(ctx, params)
	default:
		return a.handleGoalExtension(ctx, method, params)
	}
}

type sessionStatusParams struct {
	SessionID string `json:"sessionId"`
}

type sessionStatusResponse struct {
	Active       bool      `json:"active"`
	MessageCount int       `json:"message_count"`
	Model        string    `json:"model,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	CWD          string    `json:"cwd"`
}

func (a *Agent) handleSessionStatus(_ context.Context, params json.RawMessage) (any, error) {
	var p sessionStatusParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &ProtocolError{Code: CodeInvalidParams, Message: "invalid params", Data: map[string]any{"error": err.Error()}}
	}
	if p.SessionID == "" {
		return nil, NewInvalidParamsError("sessionId is required")
	}

	a.mu.Lock()
	sess, ok := a.sessions[acpsdk.SessionId(p.SessionID)]
	a.mu.Unlock()
	if !ok {
		return nil, NewSessionNotFoundError(p.SessionID)
	}

	msgs := sess.Engine.GetMessages()
	return &sessionStatusResponse{
		Active:       true,
		MessageCount: len(msgs),
		Model:        sess.Engine.GetModelName(),
		CreatedAt:    sess.CreatedAt,
		CWD:          sess.CWD,
	}, nil
}

// --- UnstableForkSession implementation ---

// UnstableForkSession forks an existing session into a new one, copying
// its message history. This supports the ACP session migration pattern.
func (a *Agent) UnstableForkSession(ctx context.Context, p acpsdk.UnstableForkSessionRequest) (acpsdk.UnstableForkSessionResponse, error) {
	a.sessionLifecycleMu.Lock()
	defer a.sessionLifecycleMu.Unlock()

	sourceID := p.SessionId
	a.mu.Lock()
	sourceSess, ok := a.sessions[sourceID]
	a.mu.Unlock()
	if !ok {
		return acpsdk.UnstableForkSessionResponse{}, fmt.Errorf("source session not found: %s", sourceID)
	}

	if p.Cwd != "" &&
		canonicalACPSessionDirectory(p.Cwd) !=
			canonicalACPSessionDirectory(sourceSess.CWD) {
		return acpsdk.UnstableForkSessionResponse{}, fmt.Errorf(
			"fork cwd %s does not match source cwd %s",
			p.Cwd,
			sourceSess.CWD,
		)
	}

	forkCtx, cancel := context.WithCancel(ctx)
	if !sourceSess.beginPrompt(cancel) {
		cancel()
		return acpsdk.UnstableForkSessionResponse{}, fmt.Errorf(
			"source session %s is closed or busy",
			sourceID,
		)
	}
	defer sourceSess.endPrompt()
	defer cancel()

	created, err := sourceSess.Engine.SessionService().CreateFork(
		forkCtx,
		engine.SessionForkRequest{},
	)
	if err != nil {
		return acpsdk.UnstableForkSessionResponse{}, fmt.Errorf(
			"failed to commit forked session: %w",
			err,
		)
	}
	childID := acpsdk.SessionId(created.Branch.NewSessionID)
	restore := a.restoreForkEngineFn
	if restore == nil {
		restore = a.restoreEngineForSession
	}
	childEngine, resumed, err := restore(
		context.WithoutCancel(forkCtx),
		childID,
		created.Info.CWD,
	)
	if err != nil {
		rollbackErr := sourceSess.Engine.SessionService().DiscardFork(created)
		if rollbackErr != nil {
			return acpsdk.UnstableForkSessionResponse{}, errors.Join(
				fmt.Errorf("failed to restore committed fork: %w", err),
				fmt.Errorf("fork rollback failed: %w", rollbackErr),
			)
		}
		return acpsdk.UnstableForkSessionResponse{}, fmt.Errorf(
			"failed to restore committed fork: %w",
			err,
		)
	}
	childSession := newSession(childID, childEngine, childEngine.GetCWD())
	a.mu.Lock()
	a.sessions[childID] = childSession
	a.mu.Unlock()
	a.startSessionHookRuntime(childSession)
	a.notifyRestoredSession(
		context.WithoutCancel(forkCtx),
		childID,
		childEngine.PlanState(),
		resumed.Warnings,
	)
	if err := a.publishCommandSnapshot(
		context.WithoutCancel(forkCtx),
		childSession,
		true,
	); err != nil {
		a.unregisterAndCloseSession(childSession)
		rollbackErr := sourceSess.Engine.SessionService().DiscardFork(created)
		return acpsdk.UnstableForkSessionResponse{}, errors.Join(
			fmt.Errorf("deliver forked session commands: %w", err),
			rollbackErr,
		)
	}
	a.sessionRoots.remember(childID, childEngine.GetCWD())

	return acpsdk.UnstableForkSessionResponse{
		SessionId: childID,
	}, nil
}

func canonicalACPSessionDirectory(value string) string {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return filepath.Clean(value)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(absolute)
}

// Compile-time interface check for ExtensionMethodHandler.
var _ acpsdk.ExtensionMethodHandler = (*Agent)(nil)
