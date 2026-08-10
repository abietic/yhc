package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"

	"github.com/abietic/yhc/engine"
)

// acpToolLifecycleLedger is the session-local, prompt-scoped delivery owner
// for canonical engine tool facts. Its mutex deliberately covers each SDK
// notification write so a permission request cannot overtake its tool start.
type acpToolLifecycleLedger struct {
	mu          sync.Mutex
	tools       map[string]*acpToolLifecycleState
	deliveryErr error
}

type acpToolLifecycleState struct {
	toolName          string
	startDelivered    bool
	inputDelivered    bool
	terminalObserved  bool
	terminalDelivered bool
	locallySettled    bool
	outcome           engine.CanonicalToolOutcome
}

type acpToolLifecycleSnapshot struct {
	ToolName          string
	StartDelivered    bool
	InputDelivered    bool
	TerminalObserved  bool
	TerminalDelivered bool
	LocallySettled    bool
	Outcome           engine.CanonicalToolOutcome
	DeliveryFailed    bool
}

func newACPToolLifecycleLedger() *acpToolLifecycleLedger {
	return &acpToolLifecycleLedger{
		tools: make(map[string]*acpToolLifecycleState),
	}
}

func (a *Agent) sessionToolLifecycleLedger(
	sessionID acpsdk.SessionId,
) (*acpToolLifecycleLedger, error) {
	if a == nil {
		return nil, errors.New("ACP agent is unavailable")
	}
	a.mu.Lock()
	session := a.sessions[sessionID]
	a.mu.Unlock()
	if session == nil {
		return nil, fmt.Errorf(
			"ACP tool lifecycle session not found: %s",
			sessionID,
		)
	}
	session.mu.Lock()
	ledger := session.toolLedger
	session.mu.Unlock()
	if ledger == nil {
		return nil, fmt.Errorf(
			"ACP tool lifecycle ledger is unavailable for session %s",
			sessionID,
		)
	}
	return ledger, nil
}

func (a *Agent) projectCanonicalProjection(
	ctx context.Context,
	sessionID acpsdk.SessionId,
	projection *engine.CanonicalProjectionEvent,
) error {
	if projection == nil {
		return errors.New("ACP canonical projection is nil")
	}
	if err := projection.Validate(); err != nil {
		return fmt.Errorf("ACP canonical projection is invalid: %w", err)
	}
	if projection.Assistant != nil {
		if a.conn == nil {
			return errors.New("ACP connection is unavailable")
		}
		var messageID *string
		if !a.config.DisableACPAssistantMessageIDs {
			if _, err := uuid.Parse(projection.Assistant.MessageID); err != nil {
				return errors.New("ACP canonical assistant message ID is not a UUID")
			}
			value := projection.Assistant.MessageID
			messageID = &value
		}
		return a.conn.SessionUpdate(ctx, acpsdk.SessionNotification{
			SessionId: sessionID,
			Update: acpsdk.SessionUpdate{
				AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
					Content:   acpsdk.TextBlock(string(projection.Assistant.Delta)),
					MessageId: messageID,
				},
			},
		})
	}
	ledger, err := a.sessionToolLifecycleLedger(sessionID)
	if err != nil {
		return err
	}
	if a.conn == nil {
		return errors.New("ACP connection is unavailable")
	}
	return ledger.project(ctx, sessionID, projection, a.conn.SessionUpdate)
}

func (a *Agent) ensureACPToolStartBeforePermission(
	ctx context.Context,
	sessionID acpsdk.SessionId,
	toolCallID string,
	toolName string,
) error {
	ledger, err := a.sessionToolLifecycleLedger(sessionID)
	if err != nil {
		return err
	}
	if a.conn == nil {
		return errors.New("ACP connection is unavailable")
	}
	return ledger.ensurePermissionVisible(
		ctx,
		sessionID,
		toolCallID,
		toolName,
		a.conn.SessionUpdate,
	)
}

func (a *Agent) settleACPToolLifecycleAfterDeliveryFailure(
	session *Session,
	event engine.QueryEvent,
	cause error,
) {
	if session == nil {
		return
	}
	session.mu.Lock()
	ledger := session.toolLedger
	session.mu.Unlock()
	if ledger == nil {
		return
	}
	ledger.settleAfterDeliveryFailure(event.CanonicalProjection, cause)
}

func (l *acpToolLifecycleLedger) project(
	ctx context.Context,
	sessionID acpsdk.SessionId,
	projection *engine.CanonicalProjectionEvent,
	send func(context.Context, acpsdk.SessionNotification) error,
) error {
	if l == nil {
		return errors.New("ACP tool lifecycle ledger is unavailable")
	}
	if projection == nil {
		return errors.New("ACP tool lifecycle projection is nil")
	}
	if err := projection.Validate(); err != nil {
		return fmt.Errorf("ACP tool lifecycle projection is invalid: %w", err)
	}
	if projection.Tool == nil {
		return nil
	}
	if send == nil {
		return errors.New("ACP tool lifecycle delivery is unavailable")
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.deliveryErr != nil {
		l.observeCanonicalTerminalLocked(projection)
		return l.deliveryErr
	}

	tool := projection.Tool
	switch projection.Kind {
	case engine.CanonicalProjectionToolStart:
		return l.startLocked(
			ctx,
			sessionID,
			tool.ToolCallID,
			tool.ToolName,
			send,
		)
	case engine.CanonicalProjectionToolInput:
		return l.inputLocked(ctx, sessionID, tool, send)
	case engine.CanonicalProjectionToolProgress:
		return l.progressLocked(ctx, sessionID, tool, send)
	case engine.CanonicalProjectionToolTerminal:
		return l.terminalLocked(ctx, sessionID, tool, send)
	default:
		return nil
	}
}

// ensurePermissionVisible delivers or de-duplicates a start synchronously
// before the caller writes a permission request carrying the same tool ID.
func (l *acpToolLifecycleLedger) ensurePermissionVisible(
	ctx context.Context,
	sessionID acpsdk.SessionId,
	toolCallID string,
	toolName string,
	send func(context.Context, acpsdk.SessionNotification) error,
) error {
	if l == nil {
		return errors.New("ACP tool lifecycle ledger is unavailable")
	}
	if strings.TrimSpace(toolCallID) == "" ||
		strings.TrimSpace(toolName) == "" {
		return errors.New("ACP permission tool identity is incomplete")
	}
	if send == nil {
		return errors.New("ACP tool lifecycle delivery is unavailable")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.deliveryErr != nil {
		return l.deliveryErr
	}
	return l.startLocked(ctx, sessionID, toolCallID, toolName, send)
}

func (l *acpToolLifecycleLedger) startLocked(
	ctx context.Context,
	sessionID acpsdk.SessionId,
	toolCallID string,
	toolName string,
	send func(context.Context, acpsdk.SessionNotification) error,
) error {
	state := l.toolStateLocked(toolCallID)
	if state.terminalObserved {
		return fmt.Errorf(
			"ACP tool lifecycle %q cannot start after terminal settlement",
			toolCallID,
		)
	}
	// Invocation identity, not the model-visible alias, owns de-duplication.
	// The permission owner may know the canonical registry name while the
	// committed start still carries the requested alias.
	if state.startDelivered {
		return nil
	}
	if state.toolName != "" && state.toolName != toolName {
		return fmt.Errorf(
			"ACP tool lifecycle %q changed tool identity",
			toolCallID,
		)
	}
	state.toolName = toolName
	update := acpsdk.StartToolCall(
		acpsdk.ToolCallId(toolCallID),
		toolName,
		acpsdk.WithStartKind(acpToolKind(toolName)),
		acpsdk.WithStartStatus(acpsdk.ToolCallStatusPending),
	)
	if err := send(ctx, acpsdk.SessionNotification{
		SessionId: sessionID,
		Update:    update,
	}); err != nil {
		return l.failDeliveryLocked(err)
	}
	state.startDelivered = true
	return nil
}

func (l *acpToolLifecycleLedger) inputLocked(
	ctx context.Context,
	sessionID acpsdk.SessionId,
	tool *engine.CanonicalToolPayload,
	send func(context.Context, acpsdk.SessionNotification) error,
) error {
	state := l.toolStateLocked(tool.ToolCallID)
	if !state.startDelivered {
		return fmt.Errorf(
			"ACP tool lifecycle %q received input before start",
			tool.ToolCallID,
		)
	}
	if state.terminalObserved {
		return nil
	}
	if state.inputDelivered {
		return nil
	}
	rawInput, err := decodeCanonicalRaw(tool.EffectiveInput)
	if err != nil {
		return fmt.Errorf("ACP tool lifecycle input is invalid: %w", err)
	}
	opts := []acpsdk.ToolCallUpdateOpt{
		acpsdk.WithUpdateStatus(acpsdk.ToolCallStatusInProgress),
		acpsdk.WithUpdateRawInput(rawInput),
	}
	if locations := acpToolLocations(rawInput); len(locations) > 0 {
		opts = append(opts, acpsdk.WithUpdateLocations(locations))
	}
	if err := send(ctx, acpsdk.SessionNotification{
		SessionId: sessionID,
		Update: acpsdk.UpdateToolCall(
			acpsdk.ToolCallId(tool.ToolCallID),
			opts...,
		),
	}); err != nil {
		return l.failDeliveryLocked(err)
	}
	state.inputDelivered = true
	return nil
}

func (l *acpToolLifecycleLedger) progressLocked(
	ctx context.Context,
	sessionID acpsdk.SessionId,
	tool *engine.CanonicalToolPayload,
	send func(context.Context, acpsdk.SessionNotification) error,
) error {
	state := l.toolStateLocked(tool.ToolCallID)
	if !state.startDelivered {
		return fmt.Errorf(
			"ACP tool lifecycle %q received progress before start",
			tool.ToolCallID,
		)
	}
	if state.terminalObserved {
		return nil
	}
	if err := send(ctx, acpsdk.SessionNotification{
		SessionId: sessionID,
		Update: acpsdk.UpdateToolCall(
			acpsdk.ToolCallId(tool.ToolCallID),
			acpsdk.WithUpdateStatus(acpsdk.ToolCallStatusInProgress),
			acpsdk.WithUpdateContent([]acpsdk.ToolCallContent{
				acpsdk.ToolContent(acpsdk.TextBlock(tool.Content)),
			}),
		),
	}); err != nil {
		return l.failDeliveryLocked(err)
	}
	return nil
}

func (l *acpToolLifecycleLedger) terminalLocked(
	ctx context.Context,
	sessionID acpsdk.SessionId,
	tool *engine.CanonicalToolPayload,
	send func(context.Context, acpsdk.SessionNotification) error,
) error {
	state := l.toolStateLocked(tool.ToolCallID)
	if state.terminalObserved {
		return nil
	}
	state.terminalObserved = true
	state.locallySettled = true
	state.outcome = tool.Outcome
	// Scheduler-synthesized results for calls that never crossed
	// executeToolCall have no visible start and therefore need no client update.
	if !state.startDelivered {
		return nil
	}
	rawOutput, err := decodeCanonicalRaw(tool.RawOutput)
	if err != nil {
		return fmt.Errorf("ACP tool lifecycle output is invalid: %w", err)
	}
	status := acpsdk.ToolCallStatusCompleted
	if tool.Outcome == engine.CanonicalToolOutcomeFailed {
		status = acpsdk.ToolCallStatusFailed
	}
	content := canonicalOutputText(rawOutput)
	if err := send(ctx, acpsdk.SessionNotification{
		SessionId: sessionID,
		Update: acpsdk.UpdateToolCall(
			acpsdk.ToolCallId(tool.ToolCallID),
			acpsdk.WithUpdateStatus(status),
			acpsdk.WithUpdateContent([]acpsdk.ToolCallContent{
				acpsdk.ToolContent(acpsdk.TextBlock(content)),
			}),
			acpsdk.WithUpdateRawOutput(rawOutput),
		),
	}); err != nil {
		return l.failDeliveryLocked(err)
	}
	state.terminalDelivered = true
	return nil
}

func (l *acpToolLifecycleLedger) settleAfterDeliveryFailure(
	projection *engine.CanonicalProjectionEvent,
	cause error,
) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.deliveryErr == nil {
		l.deliveryErr = fmt.Errorf(
			"ACP tool lifecycle delivery failed: %w",
			cause,
		)
	}
	for _, state := range l.tools {
		if state != nil {
			state.locallySettled = true
		}
	}
	l.observeCanonicalTerminalLocked(projection)
}

func (l *acpToolLifecycleLedger) observeCanonicalTerminalLocked(
	projection *engine.CanonicalProjectionEvent,
) {
	if projection == nil ||
		projection.Kind != engine.CanonicalProjectionToolTerminal ||
		projection.Tool == nil {
		return
	}
	state := l.toolStateLocked(projection.Tool.ToolCallID)
	state.terminalObserved = true
	state.locallySettled = true
	state.outcome = projection.Tool.Outcome
}

func (l *acpToolLifecycleLedger) failDeliveryLocked(err error) error {
	if l.deliveryErr == nil {
		l.deliveryErr = fmt.Errorf(
			"ACP tool lifecycle delivery failed: %w",
			err,
		)
	}
	for _, state := range l.tools {
		if state != nil {
			state.locallySettled = true
		}
	}
	return l.deliveryErr
}

func (l *acpToolLifecycleLedger) toolStateLocked(
	toolCallID string,
) *acpToolLifecycleState {
	state := l.tools[toolCallID]
	if state == nil {
		state = &acpToolLifecycleState{}
		l.tools[toolCallID] = state
	}
	return state
}

func (l *acpToolLifecycleLedger) snapshot(
	toolCallID string,
) acpToolLifecycleSnapshot {
	if l == nil {
		return acpToolLifecycleSnapshot{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.tools[toolCallID]
	if state == nil {
		return acpToolLifecycleSnapshot{DeliveryFailed: l.deliveryErr != nil}
	}
	return acpToolLifecycleSnapshot{
		ToolName:          state.toolName,
		StartDelivered:    state.startDelivered,
		InputDelivered:    state.inputDelivered,
		TerminalObserved:  state.terminalObserved,
		TerminalDelivered: state.terminalDelivered,
		LocallySettled:    state.locallySettled,
		Outcome:           state.outcome,
		DeliveryFailed:    l.deliveryErr != nil,
	}
}

func decodeCanonicalRaw(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func canonicalOutputText(output any) string {
	if text, ok := output.(string); ok {
		return text
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func acpToolKind(toolName string) acpsdk.ToolKind {
	switch toolName {
	case "Read":
		return acpsdk.ToolKindRead
	case "Write", "Edit", "NotebookEdit":
		return acpsdk.ToolKindEdit
	case "Glob", "Grep", "LSP", "WebSearch":
		return acpsdk.ToolKindSearch
	case "WebFetch":
		return acpsdk.ToolKindFetch
	case "EnterPlanMode", "ExitPlanMode":
		return acpsdk.ToolKindSwitchMode
	case "Bash", "Agent", "Task", "TaskCreate", "TaskUpdate", "TaskStop":
		return acpsdk.ToolKindExecute
	default:
		return acpsdk.ToolKindOther
	}
}

func acpToolLocations(rawInput any) []acpsdk.ToolCallLocation {
	input, ok := rawInput.(map[string]any)
	if !ok {
		return nil
	}
	for _, key := range []string{"file_path", "notebook_path", "path"} {
		path, ok := input[key].(string)
		if ok && strings.TrimSpace(path) != "" {
			return []acpsdk.ToolCallLocation{{Path: path}}
		}
	}
	return nil
}
