package bridge

import (
	"sync"
	"sync/atomic"
)

// EngineEvent is a minimal interface for engine events consumed by the adapter.
// It decouples the bridge package from the engine package to avoid circular imports.
// The engine's QueryEvent is translated into this structure at the integration point.
type EngineEvent struct {
	Type string

	// Conversation status fields
	IsStreamStart bool // EventStreamRequestStart
	IsTerminal    bool // EventTerminal

	// Tool execution fields
	ToolName  string // from ToolProgress or PermissionRequest
	ToolUseID string
	IsFinal   bool // tool progress is final (tool completed)

	// Token usage fields (from model responses)
	InputTokens  int
	OutputTokens int

	// Model change fields
	Model string

	// Session fields
	SessionID string
	CWD       string

	// Permission fields
	PermissionMode    string
	PermissionPending bool // a permission request is pending

	// Turn tracking
	TurnCount int

	// Compaction
	HasCompaction bool // true when this event carries compaction state
	IsCompacting  bool
}

// BridgeAdapter translates engine events into StateStore field updates.
// It consumes events synchronously (non-blocking to the caller) and batches
// related field updates atomically.
//
// Usage:
//
//	adapter := NewBridgeAdapter(store)
//	adapter.Start()
//	// ... in event loop:
//	adapter.HandleEvent(event)
//	// ... on shutdown:
//	adapter.Stop()
type BridgeAdapter struct {
	store *StateStore

	// started tracks whether the adapter is accepting events.
	started atomic.Bool

	// stopped tracks whether the adapter has been stopped (irreversible).
	stopped atomic.Bool

	// mu protects activeTools and token tracking state.
	mu           sync.Mutex
	activeTools  map[string]string // toolUseID -> toolName
	inputTokens  int
	outputTokens int
}

// NewBridgeAdapter creates a new adapter that writes to the given StateStore.
func NewBridgeAdapter(store *StateStore) *BridgeAdapter {
	return &BridgeAdapter{
		store:       store,
		activeTools: make(map[string]string),
	}
}

// Start marks the adapter as ready to accept events.
// Calling Start on a stopped adapter has no effect.
func (a *BridgeAdapter) Start() {
	if a.stopped.Load() {
		return
	}
	a.started.Store(true)
}

// Stop marks the adapter as no longer accepting events and cleans up state.
// It is safe to call multiple times.
func (a *BridgeAdapter) Stop() {
	a.stopped.Store(true)
	a.started.Store(false)

	// Clear active tools tracking.
	a.mu.Lock()
	a.activeTools = make(map[string]string)
	a.mu.Unlock()
}

// IsRunning returns whether the adapter is currently accepting events.
func (a *BridgeAdapter) IsRunning() bool {
	return a.started.Load() && !a.stopped.Load()
}

// HandleEvent processes a single engine event and translates it into one or more
// StateStore field updates. This method is non-blocking and safe for concurrent use.
// Events received before Start() or after Stop() are silently ignored.
func (a *BridgeAdapter) HandleEvent(event EngineEvent) {
	if !a.IsRunning() {
		return
	}

	switch {
	case event.IsStreamStart:
		a.handleStreamStart(event)
	case event.IsTerminal:
		a.handleTerminal(event)
	case event.ToolName != "" && !event.IsFinal && event.ToolUseID != "":
		a.handleToolStart(event)
	case event.IsFinal && event.ToolUseID != "":
		a.handleToolComplete(event)
	case event.InputTokens > 0 || event.OutputTokens > 0:
		a.handleTokenUsage(event)
	case event.Model != "":
		a.handleModelChange(event)
	case event.SessionID != "":
		a.handleSessionUpdate(event)
	case event.PermissionMode != "" || event.PermissionPending:
		a.handlePermissionUpdate(event)
	case event.TurnCount > 0:
		a.handleTurnUpdate(event)
	case event.HasCompaction:
		a.handleCompaction(event)
	}
}

// handleStreamStart sets conversation status to "thinking" when a new model
// request begins.
func (a *BridgeAdapter) handleStreamStart(_ EngineEvent) {
	a.store.Set(FieldConversationStatus, StatusThinking)
}

// handleTerminal sets conversation status to "idle" when the query loop ends.
func (a *BridgeAdapter) handleTerminal(_ EngineEvent) {
	a.store.Update(map[StateField]any{
		FieldConversationStatus: StatusIdle,
		FieldIsCompacting:       false,
	})

	// Clear active tools on terminal.
	a.mu.Lock()
	a.activeTools = make(map[string]string)
	a.mu.Unlock()
	a.store.Set(FieldActiveTools, []string{})
}

// handleToolStart records a tool beginning execution and sets conversation
// status to tool_running.
func (a *BridgeAdapter) handleToolStart(event EngineEvent) {
	a.mu.Lock()
	a.activeTools[event.ToolUseID] = event.ToolName
	tools := a.activeToolNames()
	a.mu.Unlock()

	a.store.Update(map[StateField]any{
		FieldConversationStatus: StatusToolRunning,
		FieldActiveTools:        tools,
	})
}

// handleToolComplete removes a tool from the active set. If no tools remain
// active, transitions status back to "thinking".
func (a *BridgeAdapter) handleToolComplete(event EngineEvent) {
	a.mu.Lock()
	delete(a.activeTools, event.ToolUseID)
	tools := a.activeToolNames()
	remaining := len(a.activeTools)
	a.mu.Unlock()

	batch := map[StateField]any{
		FieldActiveTools: tools,
	}
	if remaining == 0 {
		batch[FieldConversationStatus] = StatusThinking
	}
	a.store.Update(batch)
}

// handleTokenUsage updates cumulative token counters atomically.
func (a *BridgeAdapter) handleTokenUsage(event EngineEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.inputTokens += event.InputTokens
	a.outputTokens += event.OutputTokens
	input := a.inputTokens
	output := a.outputTokens

	a.store.Update(map[StateField]any{
		FieldInputTokens:  input,
		FieldOutputTokens: output,
	})
}

// handleModelChange updates the current model field.
func (a *BridgeAdapter) handleModelChange(event EngineEvent) {
	a.store.Set(FieldCurrentModel, event.Model)
}

// handleSessionUpdate updates session-related fields.
func (a *BridgeAdapter) handleSessionUpdate(event EngineEvent) {
	batch := map[StateField]any{
		FieldSessionID: event.SessionID,
	}
	if event.CWD != "" {
		batch[FieldSessionCWD] = event.CWD
	}
	a.store.Update(batch)
}

// handlePermissionUpdate updates permission-related fields.
func (a *BridgeAdapter) handlePermissionUpdate(event EngineEvent) {
	if event.PermissionMode != "" {
		a.store.Set(FieldPermissionMode, event.PermissionMode)
	}
}

// handleTurnUpdate updates the turn count field.
func (a *BridgeAdapter) handleTurnUpdate(event EngineEvent) {
	a.store.Set(FieldTurnCount, event.TurnCount)
}

// handleCompaction updates the compaction status.
func (a *BridgeAdapter) handleCompaction(event EngineEvent) {
	a.store.Set(FieldIsCompacting, event.IsCompacting)
}

// InitializeState sets the initial state fields in the store. This should be
// called once before Start() to populate the store with known initial values.
func (a *BridgeAdapter) InitializeState(opts InitialState) {
	batch := map[StateField]any{
		FieldConversationStatus: StatusIdle,
		FieldActiveTools:        []string{},
		FieldTurnCount:          0,
		FieldInputTokens:        0,
		FieldOutputTokens:       0,
		FieldIsCompacting:       false,
		FieldConnectedClients:   0,
	}
	if opts.Model != "" {
		batch[FieldCurrentModel] = opts.Model
	}
	if opts.FallbackModel != "" {
		batch[FieldFallbackModel] = opts.FallbackModel
	}
	if opts.SessionID != "" {
		batch[FieldSessionID] = opts.SessionID
	}
	if opts.CWD != "" {
		batch[FieldSessionCWD] = opts.CWD
	}
	if opts.PermissionMode != "" {
		batch[FieldPermissionMode] = opts.PermissionMode
	}
	a.store.Update(batch)
}

// InitialState holds the initial values for state population.
type InitialState struct {
	Model          string
	FallbackModel  string
	SessionID      string
	CWD            string
	PermissionMode string
}

// activeToolNames returns a sorted list of active tool names.
// Caller must hold a.mu.
func (a *BridgeAdapter) activeToolNames() []string {
	if len(a.activeTools) == 0 {
		return []string{}
	}
	names := make([]string, 0, len(a.activeTools))
	seen := make(map[string]bool)
	for _, name := range a.activeTools {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// EventFromQueryEvent is a helper that translates common engine event patterns
// into the bridge EngineEvent structure. This lives in the bridge package so
// that callers can build the translation without importing engine internals.
//
// For full translation from engine.QueryEvent, see the BridgeManager which
// provides a convenience method.
func EventFromStreamStart() EngineEvent {
	return EngineEvent{
		Type:          "stream_request_start",
		IsStreamStart: true,
	}
}

// EventFromTerminal creates an EngineEvent for query loop termination.
func EventFromTerminal() EngineEvent {
	return EngineEvent{
		Type:       "terminal",
		IsTerminal: true,
	}
}

// EventFromToolProgress creates an EngineEvent for tool execution progress.
func EventFromToolProgress(toolName, toolUseID string, isFinal bool) EngineEvent {
	return EngineEvent{
		Type:      "tool_progress",
		ToolName:  toolName,
		ToolUseID: toolUseID,
		IsFinal:   isFinal,
	}
}

// EventFromTokenUsage creates an EngineEvent for token consumption.
func EventFromTokenUsage(input, output int) EngineEvent {
	return EngineEvent{
		Type:         "token_usage",
		InputTokens:  input,
		OutputTokens: output,
	}
}

// EventFromModelChange creates an EngineEvent for model switch.
func EventFromModelChange(model string) EngineEvent {
	return EngineEvent{
		Type:  "model_change",
		Model: model,
	}
}

// EventFromSessionUpdate creates an EngineEvent for session info.
func EventFromSessionUpdate(sessionID, cwd string) EngineEvent {
	return EngineEvent{
		Type:      "session_update",
		SessionID: sessionID,
		CWD:       cwd,
	}
}

// EventFromPermissionMode creates an EngineEvent for permission mode change.
func EventFromPermissionMode(mode string) EngineEvent {
	return EngineEvent{
		Type:           "permission_mode",
		PermissionMode: mode,
	}
}

// EventFromTurnCount creates an EngineEvent for turn count update.
func EventFromTurnCount(count int) EngineEvent {
	return EngineEvent{
		Type:      "turn_count",
		TurnCount: count,
	}
}

// EventFromCompaction creates an EngineEvent for compaction status.
func EventFromCompaction(isCompacting bool) EngineEvent {
	return EngineEvent{
		Type:          "compaction",
		HasCompaction: true,
		IsCompacting:  isCompacting,
	}
}
