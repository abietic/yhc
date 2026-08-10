package session

import (
	"sync"
)

// State represents the current session lifecycle state.
type State string

const (
	StateIdle           State = "idle"
	StateRunning        State = "running"
	StateRequiresAction State = "requires_action"
)

// RequiresActionDetails provides context about what the session is blocked on.
type RequiresActionDetails struct {
	ToolName          string                 `json:"tool_name"`
	ActionDescription string                 `json:"action_description"`
	ToolUseID         string                 `json:"tool_use_id"`
	RequestID         string                 `json:"request_id"`
	Input             map[string]interface{} `json:"input,omitempty"`
}

// ExternalMetadata carries metadata pushed to external observers (bridge, ACP).
type ExternalMetadata struct {
	PermissionMode *string                `json:"permission_mode,omitempty"`
	Model          *string                `json:"model,omitempty"`
	PendingAction  *RequiresActionDetails `json:"pending_action,omitempty"`
	TaskSummary    *string                `json:"task_summary,omitempty"`
}

// StateChangedCallback is called when session state transitions.
type StateChangedCallback func(state State, details *RequiresActionDetails)

// MetadataChangedCallback is called when external metadata changes.
type MetadataChangedCallback func(metadata ExternalMetadata)

// PermissionModeChangedCallback is called when permission mode changes.
type PermissionModeChangedCallback func(mode string)

// StateMachine manages session state transitions with observer notification.
// Thread-safe. Mirrors src/utils/sessionState.ts from the reference.
type StateMachine struct {
	mu               sync.RWMutex
	current          State
	hasPendingAction bool

	stateListeners          []StateChangedCallback
	metadataListeners       []MetadataChangedCallback
	permissionModeListeners []PermissionModeChangedCallback
}

// NewStateMachine creates a new session state machine starting in idle state.
func NewStateMachine() *StateMachine {
	return &StateMachine{
		current: StateIdle,
	}
}

// GetState returns the current session state.
func (sm *StateMachine) GetState() State {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.current
}

// OnStateChanged registers a listener for state transitions.
func (sm *StateMachine) OnStateChanged(cb StateChangedCallback) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.stateListeners = append(sm.stateListeners, cb)
}

// OnMetadataChanged registers a listener for metadata changes.
func (sm *StateMachine) OnMetadataChanged(cb MetadataChangedCallback) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.metadataListeners = append(sm.metadataListeners, cb)
}

// OnPermissionModeChanged registers a listener for permission mode changes.
func (sm *StateMachine) OnPermissionModeChanged(cb PermissionModeChangedCallback) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.permissionModeListeners = append(sm.permissionModeListeners, cb)
}

// NotifyStateChanged transitions to a new state and fires all listeners.
func (sm *StateMachine) NotifyStateChanged(state State, details *RequiresActionDetails) {
	sm.mu.Lock()
	sm.current = state

	stateListeners := make([]StateChangedCallback, len(sm.stateListeners))
	copy(stateListeners, sm.stateListeners)

	metadataListeners := make([]MetadataChangedCallback, len(sm.metadataListeners))
	copy(metadataListeners, sm.metadataListeners)

	hadPending := sm.hasPendingAction

	if state == StateRequiresAction && details != nil {
		sm.hasPendingAction = true
	} else if sm.hasPendingAction {
		sm.hasPendingAction = false
	}

	sm.mu.Unlock()

	for _, cb := range stateListeners {
		cb(state, details)
	}

	if state == StateRequiresAction && details != nil {
		md := ExternalMetadata{PendingAction: details}
		for _, cb := range metadataListeners {
			cb(md)
		}
	} else if hadPending {
		md := ExternalMetadata{PendingAction: nil}
		for _, cb := range metadataListeners {
			cb(md)
		}
	}

	if state == StateIdle {
		md := ExternalMetadata{TaskSummary: nil}
		for _, cb := range metadataListeners {
			cb(md)
		}
	}
}

// NotifyMetadataChanged fires metadata listeners with arbitrary metadata.
func (sm *StateMachine) NotifyMetadataChanged(metadata ExternalMetadata) {
	sm.mu.RLock()
	listeners := make([]MetadataChangedCallback, len(sm.metadataListeners))
	copy(listeners, sm.metadataListeners)
	sm.mu.RUnlock()

	for _, cb := range listeners {
		cb(metadata)
	}
}

// NotifyPermissionModeChanged fires permission mode listeners.
func (sm *StateMachine) NotifyPermissionModeChanged(mode string) {
	sm.mu.RLock()
	listeners := make([]PermissionModeChangedCallback, len(sm.permissionModeListeners))
	copy(listeners, sm.permissionModeListeners)
	sm.mu.RUnlock()

	for _, cb := range listeners {
		cb(mode)
	}
}

// TransitionToRunning is a convenience for state=running.
func (sm *StateMachine) TransitionToRunning() {
	sm.NotifyStateChanged(StateRunning, nil)
}

// TransitionToIdle is a convenience for state=idle.
func (sm *StateMachine) TransitionToIdle() {
	sm.NotifyStateChanged(StateIdle, nil)
}

// TransitionToRequiresAction is a convenience for state=requires_action.
func (sm *StateMachine) TransitionToRequiresAction(details RequiresActionDetails) {
	sm.NotifyStateChanged(StateRequiresAction, &details)
}
