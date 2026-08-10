// Package state provides a centralized application state store for managing
// runtime state across the agent engine. This mirrors the state management
// patterns found in the reference implementation's src/state/* modules.
package state

import (
	"sync"
	"time"
)

// AgentState represents the state of a registered sub-agent or teammate.
type AgentState struct {
	ID        string
	Name      string
	Status    string // "running", "idle", "stopped"
	TaskID    string
	StartTime time.Time
}

// StateChangeHandler is a callback invoked when a state field changes.
type StateChangeHandler func(field string, oldValue, newValue any)

// StateSnapshot provides a readonly point-in-time view of key state fields.
type StateSnapshot struct {
	SessionID        string
	Model            string
	PermissionMode   string
	PlanMode         bool
	TurnCount        int
	IsProcessing     bool
	InputTokens      int
	OutputTokens     int
	ActiveAgentCount int
}

// AppState is the main state container for the agent engine runtime.
// All field access must go through the thread-safe accessor methods.
type AppState struct {
	mu sync.RWMutex

	// Session state
	SessionID string
	CWD       string
	StartTime time.Time

	// Model state
	Model         string
	FallbackModel string

	// Mode state
	PermissionMode string
	PlanMode       bool
	NonInteractive bool

	// Runtime state
	TurnCount    int
	IsProcessing bool
	IsCompacting bool

	// Token tracking
	InputTokensUsed  int
	OutputTokensUsed int

	// Tool state
	DisabledTools map[string]bool

	// Sub-agent/teammate state
	ActiveAgents map[string]*AgentState

	// Custom metadata
	Extra map[string]any

	// Change listeners
	changeHandlers []StateChangeHandler
}

// NewAppState creates a new AppState initialized with the given session parameters.
func NewAppState(sessionID, cwd, model string) *AppState {
	return &AppState{
		SessionID:      sessionID,
		CWD:            cwd,
		Model:          model,
		StartTime:      time.Now(),
		PermissionMode: "default",
		DisabledTools:  make(map[string]bool),
		ActiveAgents:   make(map[string]*AgentState),
		Extra:          make(map[string]any),
	}
}

// GetModel returns the current active model.
func (s *AppState) GetModel() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Model
}

// SetModel updates the active model.
func (s *AppState) SetModel(model string) {
	s.mu.Lock()
	old := s.Model
	s.Model = model
	s.mu.Unlock()
	s.notifyChange("Model", old, model)
}

// GetPermissionMode returns the current permission mode.
func (s *AppState) GetPermissionMode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.PermissionMode
}

// SetPermissionMode updates the permission mode.
func (s *AppState) SetPermissionMode(mode string) {
	s.mu.Lock()
	old := s.PermissionMode
	s.PermissionMode = mode
	s.mu.Unlock()
	s.notifyChange("PermissionMode", old, mode)
}

// GetPlanMode returns whether plan mode is enabled.
func (s *AppState) GetPlanMode() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.PlanMode
}

// SetPlanMode enables or disables plan mode.
func (s *AppState) SetPlanMode(enabled bool) {
	s.mu.Lock()
	old := s.PlanMode
	s.PlanMode = enabled
	s.mu.Unlock()
	s.notifyChange("PlanMode", old, enabled)
}

// IncrementTurn atomically increments the turn counter.
func (s *AppState) IncrementTurn() {
	s.mu.Lock()
	old := s.TurnCount
	s.TurnCount++
	newVal := s.TurnCount
	s.mu.Unlock()
	s.notifyChange("TurnCount", old, newVal)
}

// GetTurnCount returns the current turn count.
func (s *AppState) GetTurnCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.TurnCount
}

// SetProcessing sets whether the engine is currently processing a query.
func (s *AppState) SetProcessing(v bool) {
	s.mu.Lock()
	old := s.IsProcessing
	s.IsProcessing = v
	s.mu.Unlock()
	s.notifyChange("IsProcessing", old, v)
}

// IsCurrentlyProcessing returns whether the engine is processing.
func (s *AppState) IsCurrentlyProcessing() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.IsProcessing
}

// AddTokenUsage adds token usage counts to the running totals.
func (s *AppState) AddTokenUsage(input, output int) {
	s.mu.Lock()
	s.InputTokensUsed += input
	s.OutputTokensUsed += output
	s.mu.Unlock()
}

// GetTokenUsage returns the cumulative input and output token counts.
func (s *AppState) GetTokenUsage() (input, output int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.InputTokensUsed, s.OutputTokensUsed
}

// RegisterAgent adds a new agent to the active agents registry.
func (s *AppState) RegisterAgent(id, name, taskID string) {
	s.mu.Lock()
	s.ActiveAgents[id] = &AgentState{
		ID:        id,
		Name:      name,
		Status:    "running",
		TaskID:    taskID,
		StartTime: time.Now(),
	}
	s.mu.Unlock()
	s.notifyChange("ActiveAgents", nil, id)
}

// UnregisterAgent removes an agent from the active agents registry.
func (s *AppState) UnregisterAgent(id string) {
	s.mu.Lock()
	delete(s.ActiveAgents, id)
	s.mu.Unlock()
	s.notifyChange("ActiveAgents", id, nil)
}

// GetActiveAgents returns a shallow copy of the active agents map.
func (s *AppState) GetActiveAgents() map[string]*AgentState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]*AgentState, len(s.ActiveAgents))
	for k, v := range s.ActiveAgents {
		copied := *v
		result[k] = &copied
	}
	return result
}

// SetAgentStatus updates the status of a registered agent.
func (s *AppState) SetAgentStatus(id, status string) {
	s.mu.Lock()
	if agent, ok := s.ActiveAgents[id]; ok {
		old := agent.Status
		agent.Status = status
		s.mu.Unlock()
		s.notifyChange("AgentStatus:"+id, old, status)
		return
	}
	s.mu.Unlock()
}

// Snapshot returns a readonly point-in-time snapshot of key state fields.
func (s *AppState) Snapshot() StateSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return StateSnapshot{
		SessionID:        s.SessionID,
		Model:            s.Model,
		PermissionMode:   s.PermissionMode,
		PlanMode:         s.PlanMode,
		TurnCount:        s.TurnCount,
		IsProcessing:     s.IsProcessing,
		InputTokens:      s.InputTokensUsed,
		OutputTokens:     s.OutputTokensUsed,
		ActiveAgentCount: len(s.ActiveAgents),
	}
}

// OnChange registers a handler that will be called when state fields change.
func (s *AppState) OnChange(handler StateChangeHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changeHandlers = append(s.changeHandlers, handler)
}

// notifyChange invokes all registered change handlers for a field update.
func (s *AppState) notifyChange(field string, oldValue, newValue any) {
	s.mu.RLock()
	handlers := make([]StateChangeHandler, len(s.changeHandlers))
	copy(handlers, s.changeHandlers)
	s.mu.RUnlock()

	for _, h := range handlers {
		h(field, oldValue, newValue)
	}
}
