package tools

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// AgentSteeringState represents the steering state of a background agent.
type AgentSteeringState string

const (
	SteeringStateRunning AgentSteeringState = "running"
	SteeringStatePaused  AgentSteeringState = "paused"
	SteeringStateStopped AgentSteeringState = "stopped"
)

// AgentPriority defines the priority level of a background agent.
type AgentPriority int

const (
	PriorityLow    AgentPriority = 0
	PriorityNormal AgentPriority = 1
	PriorityHigh   AgentPriority = 2
)

// AgentSteeringInfo holds the steering metadata for a background agent.
type AgentSteeringInfo struct {
	AgentID       string
	State         AgentSteeringState
	Priority      AgentPriority
	PausedAt      time.Time
	TotalPausedMs int64
	LastResumedAt time.Time
}

// AgentSteering provides pause/resume/priority/force-stop controls for
// background agents. All operations are thread-safe and idempotent where
// possible to handle concurrent callers safely.
type AgentSteering struct {
	mu      sync.Mutex
	agents  map[string]*steeringEntry
	pauseCh map[string]chan struct{} // per-agent pause gate
}

type steeringEntry struct {
	info AgentSteeringInfo
}

// NewAgentSteering creates a new steering controller.
func NewAgentSteering() *AgentSteering {
	return &AgentSteering{
		agents:  make(map[string]*steeringEntry),
		pauseCh: make(map[string]chan struct{}),
	}
}

// Register registers a new agent for steering. Called when a background
// agent starts execution. If already registered, this is a no-op.
func (s *AgentSteering) Register(agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.agents[agentID]; exists {
		return
	}
	s.agents[agentID] = &steeringEntry{
		info: AgentSteeringInfo{
			AgentID:  agentID,
			State:    SteeringStateRunning,
			Priority: PriorityNormal,
		},
	}
	// Create a closed channel (not paused by default).
	ch := make(chan struct{})
	close(ch)
	s.pauseCh[agentID] = ch
}

// Unregister removes an agent from steering tracking. Called when a
// background agent completes or is aborted.
func (s *AgentSteering) Unregister(agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.agents[agentID]; ok && entry.info.State == SteeringStatePaused {
		if ch := s.pauseCh[agentID]; ch != nil {
			close(ch)
		}
	}
	delete(s.agents, agentID)
	delete(s.pauseCh, agentID)
}

// Pause pauses a running background agent. The agent will stop processing
// at the next pause checkpoint but retains all state for later resumption.
// Returns error if the agent is not running or not found.
//
// Thread-safe: multiple callers can Pause the same agent simultaneously;
// only the first transition takes effect.
func (s *AgentSteering) Pause(agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.agents[agentID]
	if !ok {
		return fmt.Errorf("agent_steering: agent %q not found", agentID)
	}
	if entry.info.State == SteeringStatePaused {
		return nil // Already paused, idempotent.
	}
	if entry.info.State != SteeringStateRunning {
		return fmt.Errorf("agent_steering: agent %q is not running (state: %s)", agentID, entry.info.State)
	}

	entry.info.State = SteeringStatePaused
	entry.info.PausedAt = time.Now()

	// Create a new blocking channel (gate is closed).
	s.pauseCh[agentID] = make(chan struct{})

	return nil
}

// Resume resumes a paused background agent. The agent continues from where
// it left off. Returns error if the agent is not paused or not found.
//
// Thread-safe: multiple callers can Resume the same agent simultaneously;
// only the first transition takes effect.
func (s *AgentSteering) Resume(agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.agents[agentID]
	if !ok {
		return fmt.Errorf("agent_steering: agent %q not found", agentID)
	}
	if entry.info.State == SteeringStateRunning {
		return nil // Already running, idempotent.
	}
	if entry.info.State != SteeringStatePaused {
		return fmt.Errorf("agent_steering: agent %q is not paused (state: %s)", agentID, entry.info.State)
	}

	// Accumulate paused time.
	if !entry.info.PausedAt.IsZero() {
		entry.info.TotalPausedMs += time.Since(entry.info.PausedAt).Milliseconds()
	}

	entry.info.State = SteeringStateRunning
	entry.info.PausedAt = time.Time{}
	entry.info.LastResumedAt = time.Now()

	// Open the gate by closing the channel.
	ch := s.pauseCh[agentID]
	if ch != nil {
		close(ch)
	}

	return nil
}

// SetPriority adjusts the priority of a background agent. This affects
// queue ordering when multiple agents compete for execution slots.
// Returns error if the agent is not found.
func (s *AgentSteering) SetPriority(agentID string, priority AgentPriority) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.agents[agentID]
	if !ok {
		return fmt.Errorf("agent_steering: agent %q not found", agentID)
	}
	if entry.info.State == SteeringStateStopped {
		return fmt.Errorf("agent_steering: agent %q is stopped", agentID)
	}
	entry.info.Priority = priority
	return nil
}

// ForceStop stops a background agent while preserving its state (transcript,
// messages) for potential later resume via SendMessage. Unlike AbortAgent
// which signals context cancellation, ForceStop marks the agent as stopped
// without necessarily cancelling the context immediately, allowing the agent
// to checkpoint its state at the next boundary.
//
// After ForceStop, the agent's transcript is preserved and it can be resumed
// via SendMessage with a new prompt.
func (s *AgentSteering) ForceStop(agentID string, runner *AgentRunner) error {
	s.mu.Lock()
	entry, ok := s.agents[agentID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("agent_steering: agent %q not found", agentID)
	}
	if entry.info.State == SteeringStateStopped {
		s.mu.Unlock()
		return nil // Already stopped, idempotent.
	}

	// If paused, open the gate so the agent can observe the stop.
	wasPaused := entry.info.State == SteeringStatePaused
	if wasPaused {
		if !entry.info.PausedAt.IsZero() {
			entry.info.TotalPausedMs += time.Since(entry.info.PausedAt).Milliseconds()
		}
		ch := s.pauseCh[agentID]
		if ch != nil {
			close(ch)
		}
	}
	entry.info.State = SteeringStateStopped
	entry.info.PausedAt = time.Time{}
	s.mu.Unlock()

	// Use the runner to abort the agent (cancels context, preserves messages).
	if runner != nil {
		_ = runner.AbortAgent(agentID)
	}

	return nil
}

// GetInfo returns the current steering information for an agent.
func (s *AgentSteering) GetInfo(agentID string) (AgentSteeringInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.agents[agentID]
	if !ok {
		return AgentSteeringInfo{}, false
	}
	return entry.info, true
}

// WaitIfPaused blocks until the agent is no longer paused. This should be
// called at tool-round boundaries within the agent execution loop to honor
// pause requests. Returns immediately if not paused.
//
// Returns true if the agent was paused and then resumed, false if it was
// not paused. Returns error if the agent is not registered.
func (s *AgentSteering) WaitIfPaused(agentID string) (bool, error) {
	return s.WaitIfPausedContext(context.Background(), agentID)
}

// WaitIfPausedContext is the cancellation-aware pause checkpoint used by the
// query loop. Cancellation releases the waiter without mutating steering state.
func (s *AgentSteering) WaitIfPausedContext(ctx context.Context, agentID string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	entry, ok := s.agents[agentID]
	if !ok {
		s.mu.Unlock()
		return false, fmt.Errorf("agent_steering: agent %q not found", agentID)
	}
	if entry.info.State != SteeringStatePaused {
		s.mu.Unlock()
		return false, nil
	}
	ch := s.pauseCh[agentID]
	s.mu.Unlock()

	// Block until resumed (channel is closed) or the execution is cancelled.
	select {
	case <-ch:
		if err := ctx.Err(); err != nil {
			return true, err
		}
		return true, nil
	case <-ctx.Done():
		return true, ctx.Err()
	}
}

// IsPaused returns whether an agent is currently paused.
func (s *AgentSteering) IsPaused(agentID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.agents[agentID]
	if !ok {
		return false
	}
	return entry.info.State == SteeringStatePaused
}

// ListAll returns steering info for all registered agents.
func (s *AgentSteering) ListAll() []AgentSteeringInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AgentSteeringInfo, 0, len(s.agents))
	for _, entry := range s.agents {
		out = append(out, entry.info)
	}
	return out
}

// AgentStatus returns a detailed status snapshot for an individual background
// agent, combining steering state with runner progress information.
func (s *AgentSteering) AgentStatus(agentID string, runner *AgentRunner) (*AgentStatusSnapshot, error) {
	s.mu.Lock()
	entry, ok := s.agents[agentID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("agent_steering: agent %q not found", agentID)
	}
	info := entry.info
	s.mu.Unlock()

	snapshot := &AgentStatusSnapshot{
		AgentID:       info.AgentID,
		SteeringState: info.State,
		Priority:      info.Priority,
		TotalPausedMs: info.TotalPausedMs,
	}

	// Enrich with runner progress if available.
	if runner != nil {
		if agentSnap, found := runner.GetAgentSnapshot(agentID); found {
			snapshot.RunnerStatus = agentSnap.Status
			snapshot.Description = agentSnap.Description
			snapshot.StartedAt = agentSnap.StartedAt
			snapshot.ElapsedMs = time.Since(agentSnap.StartedAt).Milliseconds()
			if agentSnap.Progress.LastActivity != nil {
				snapshot.CurrentTool = agentSnap.Progress.LastActivity.ToolName
			}
			snapshot.ToolUseCount = agentSnap.Progress.ToolUseCount
			snapshot.TokenCount = agentSnap.Progress.TokenCount
		}
	}

	return snapshot, nil
}

// AgentStatusSnapshot is the detailed status for a background agent.
type AgentStatusSnapshot struct {
	AgentID       string
	SteeringState AgentSteeringState
	Priority      AgentPriority
	TotalPausedMs int64
	RunnerStatus  string // from AgentRunner: "running", "completed", etc.
	Description   string
	StartedAt     time.Time
	ElapsedMs     int64
	CurrentTool   string
	ToolUseCount  int
	TokenCount    int
}
