package tools

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
)

// AgentDisplayMode represents the execution attachment shown by read-only
// Agent projections.
type AgentDisplayMode string

const (
	DisplayModeForeground AgentDisplayMode = "foreground"
	DisplayModeBackground AgentDisplayMode = "background"
	// DisplayModeBackgrounded is derived after a foreground wait detaches; it
	// does not register a second execution or lifecycle state owner.
	DisplayModeBackgrounded AgentDisplayMode = "backgrounded"
)

// AgentDisplayState tracks the foreground/background display mode for agents.
// Only one agent can be in foreground at a time (the main conversation is
// always foreground; a sub-agent brought to foreground replaces the previous
// foreground agent).
type AgentDisplayState struct {
	mu              sync.Mutex
	modes           map[string]AgentDisplayMode
	foregroundAgent string // ID of the current foreground agent ("" means main conversation)
}

// NewAgentDisplayState creates a new display state tracker.
func NewAgentDisplayState() *AgentDisplayState {
	return &AgentDisplayState{
		modes: make(map[string]AgentDisplayMode),
	}
}

// Register registers an agent with an initial display mode.
func (s *AgentDisplayState) Register(agentID string, mode AgentDisplayMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modes[agentID] = mode
	if mode == DisplayModeForeground {
		s.foregroundAgent = agentID
	}
}

// Unregister removes an agent from display tracking.
func (s *AgentDisplayState) Unregister(agentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.modes, agentID)
	if s.foregroundAgent == agentID {
		s.foregroundAgent = ""
	}
}

// MoveToBackground transitions a running foreground agent to background mode.
// The agent continues executing but its output is no longer streamed to the caller.
// Returns error if the agent is not found or is already in background.
func (s *AgentDisplayState) MoveToBackground(agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	mode, ok := s.modes[agentID]
	if !ok {
		return fmt.Errorf("agent_lifecycle: agent %q not registered for display tracking", agentID)
	}
	if mode == DisplayModeBackground {
		return nil // Already background, idempotent.
	}

	s.modes[agentID] = DisplayModeBackground
	if s.foregroundAgent == agentID {
		s.foregroundAgent = ""
	}
	return nil
}

// MoveToForeground transitions a running background agent to foreground mode.
// The agent's output stream is attached to the caller. Only one agent can be
// foreground at a time; the previous foreground agent (if any) is moved to
// background.
// Returns error if the agent is not found.
func (s *AgentDisplayState) MoveToForeground(agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, ok := s.modes[agentID]
	if !ok {
		return fmt.Errorf("agent_lifecycle: agent %q not registered for display tracking", agentID)
	}

	// Move previous foreground agent to background.
	if s.foregroundAgent != "" && s.foregroundAgent != agentID {
		s.modes[s.foregroundAgent] = DisplayModeBackground
	}

	s.modes[agentID] = DisplayModeForeground
	s.foregroundAgent = agentID
	return nil
}

// GetMode returns the display mode for an agent.
func (s *AgentDisplayState) GetMode(agentID string) (AgentDisplayMode, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mode, ok := s.modes[agentID]
	return mode, ok
}

// ForegroundAgent returns the ID of the current foreground agent.
// Returns empty string if no agent is in foreground (main conversation owns it).
func (s *AgentDisplayState) ForegroundAgent() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.foregroundAgent
}

// IsForeground returns whether the given agent is currently in foreground.
func (s *AgentDisplayState) IsForeground(agentID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.foregroundAgent == agentID
}

// ListModes returns a snapshot of all agent display modes.
func (s *AgentDisplayState) ListModes() map[string]AgentDisplayMode {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]AgentDisplayMode, len(s.modes))
	for id, mode := range s.modes {
		out[id] = mode
	}
	return out
}

// ProgressEventType identifies the kind of progress event.
type ProgressEventType string

const (
	ProgressEventToolStart    ProgressEventType = "tool_start"
	ProgressEventToolEnd      ProgressEventType = "tool_end"
	ProgressEventModelStart   ProgressEventType = "model_start"
	ProgressEventModelChunk   ProgressEventType = "model_chunk"
	ProgressEventModelEnd     ProgressEventType = "model_end"
	ProgressEventTextOutput   ProgressEventType = "text_output"
	ProgressEventStatusChange ProgressEventType = "status_change"
)

// StreamProgressEvent is a structured progress event emitted during agent execution.
// Events are agent-scoped and never leak between agents.
type StreamProgressEvent struct {
	AgentID   string            `json:"agent_id"`
	Type      ProgressEventType `json:"type"`
	Timestamp time.Time         `json:"timestamp"`

	// Tool events
	ToolName  string         `json:"tool_name,omitempty"`
	ToolInput map[string]any `json:"tool_input,omitempty"`
	ToolError string         `json:"tool_error,omitempty"`

	// Model events
	ModelID string `json:"model_id,omitempty"`
	Chunk   string `json:"chunk,omitempty"`

	// Text output events
	Text string `json:"text,omitempty"`

	// Status change events
	OldStatus string `json:"old_status,omitempty"`
	NewStatus string `json:"new_status,omitempty"`
}

// ProgressListener receives streaming progress events from an agent.
type ProgressListener struct {
	ID      string
	AgentID string
	Events  chan StreamProgressEvent
	done    chan struct{}
}

// Close stops the listener from receiving further events.
func (l *ProgressListener) Close() {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
}

// IsClosed returns whether the listener has been closed.
func (l *ProgressListener) IsClosed() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

// AgentProgressStream manages streaming progress events for running agents.
// It supports multiple listeners per agent with buffering for late-joining listeners.
type AgentProgressStream struct {
	mu        sync.Mutex
	streams   map[string]*agentStream
	bufferCap int // max events buffered per agent
}

type agentStream struct {
	agentID   string
	listeners []*ProgressListener
	buffer    []StreamProgressEvent
	bufferCap int
}

// NewAgentProgressStream creates a progress stream manager with the given buffer capacity.
func NewAgentProgressStream(bufferCap int) *AgentProgressStream {
	if bufferCap <= 0 {
		bufferCap = 50
	}
	return &AgentProgressStream{
		streams:   make(map[string]*agentStream),
		bufferCap: bufferCap,
	}
}

// RegisterAgent starts tracking progress for a new agent.
func (ps *AgentProgressStream) RegisterAgent(agentID string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if _, exists := ps.streams[agentID]; exists {
		return
	}
	ps.streams[agentID] = &agentStream{
		agentID:   agentID,
		listeners: make([]*ProgressListener, 0),
		buffer:    make([]StreamProgressEvent, 0, ps.bufferCap),
		bufferCap: ps.bufferCap,
	}
}

// UnregisterAgent stops tracking and closes all listeners for the agent.
func (ps *AgentProgressStream) UnregisterAgent(agentID string) {
	ps.mu.Lock()
	stream, ok := ps.streams[agentID]
	if !ok {
		ps.mu.Unlock()
		return
	}
	delete(ps.streams, agentID)
	ps.mu.Unlock()

	// Close all listeners.
	for _, l := range stream.listeners {
		l.Close()
	}
}

// Emit sends a progress event to all active listeners and buffers it.
// Events for unregistered agents are dropped silently.
func (ps *AgentProgressStream) Emit(event StreamProgressEvent) {
	ps.mu.Lock()
	stream, ok := ps.streams[event.AgentID]
	if !ok {
		ps.mu.Unlock()
		return
	}

	// Buffer the event.
	if len(stream.buffer) >= stream.bufferCap {
		// Evict oldest event.
		stream.buffer = stream.buffer[1:]
	}
	stream.buffer = append(stream.buffer, event)

	// Deliver to active listeners (copy slice to avoid holding lock during send).
	listeners := make([]*ProgressListener, len(stream.listeners))
	copy(listeners, stream.listeners)
	ps.mu.Unlock()

	for _, l := range listeners {
		if l.IsClosed() {
			continue
		}
		select {
		case l.Events <- event:
		default:
			// Listener is not consuming events fast enough; drop.
		}
	}
}

// Subscribe creates a new listener for an agent's progress stream.
// The listener receives buffered events immediately (via the returned channel),
// then receives new events as they arrive.
// Returns nil if the agent is not registered.
func (ps *AgentProgressStream) Subscribe(agentID string, chanSize int) *ProgressListener {
	if chanSize <= 0 {
		chanSize = 100
	}
	ps.mu.Lock()
	stream, ok := ps.streams[agentID]
	if !ok {
		ps.mu.Unlock()
		return nil
	}

	listener := &ProgressListener{
		ID:      fmt.Sprintf("%s-listener-%d", agentID, len(stream.listeners)),
		AgentID: agentID,
		Events:  make(chan StreamProgressEvent, chanSize),
		done:    make(chan struct{}),
	}

	// Replay buffered events.
	for _, event := range stream.buffer {
		select {
		case listener.Events <- event:
		default:
			// Buffer overflow for late-joining listener; skip oldest.
		}
	}

	stream.listeners = append(stream.listeners, listener)
	ps.mu.Unlock()

	return listener
}

// Unsubscribe removes a listener from an agent's progress stream.
func (ps *AgentProgressStream) Unsubscribe(listener *ProgressListener) {
	if listener == nil {
		return
	}
	listener.Close()

	ps.mu.Lock()
	defer ps.mu.Unlock()

	stream, ok := ps.streams[listener.AgentID]
	if !ok {
		return
	}

	for i, l := range stream.listeners {
		if l == listener {
			stream.listeners = append(stream.listeners[:i], stream.listeners[i+1:]...)
			break
		}
	}
}

// BufferedEvents returns a copy of the current event buffer for an agent.
// Returns nil if the agent is not registered.
func (ps *AgentProgressStream) BufferedEvents(agentID string) []StreamProgressEvent {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	stream, ok := ps.streams[agentID]
	if !ok {
		return nil
	}
	out := make([]StreamProgressEvent, len(stream.buffer))
	copy(out, stream.buffer)
	return out
}

// ListenerCount returns the number of active listeners for an agent.
func (ps *AgentProgressStream) ListenerCount(agentID string) int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	stream, ok := ps.streams[agentID]
	if !ok {
		return 0
	}
	// Count non-closed listeners.
	count := 0
	for _, l := range stream.listeners {
		if !l.IsClosed() {
			count++
		}
	}
	return count
}

// AgentTranscriptView provides read-only access to an agent's conversation
// transcript, whether the agent is still running or has completed.
type AgentTranscriptView struct {
	AgentID     string
	Status      string
	Description string
	Messages    []*schema.Message
	StartedAt   time.Time
	CompletedAt time.Time
	IsRunning   bool
	TurnCount   int
	TokenCount  int
}

// AgentTranscriptAccess provides methods for reading agent transcripts.
type AgentTranscriptAccess struct {
	runner *AgentRunner
}

// NewAgentTranscriptAccess creates a transcript accessor backed by the given runner.
func NewAgentTranscriptAccess(runner *AgentRunner) *AgentTranscriptAccess {
	return &AgentTranscriptAccess{runner: runner}
}

// GetTranscript returns a read-only view of an agent's current transcript.
// For running agents, this is a snapshot of the current state.
// For completed agents, this is the full final transcript.
func (a *AgentTranscriptAccess) GetTranscript(agentID string) (*AgentTranscriptView, error) {
	if a.runner == nil {
		return nil, fmt.Errorf("agent_transcript: runner is nil")
	}
	snapshot, ok := a.runner.GetAgentSnapshot(agentID)
	if !ok {
		return nil, fmt.Errorf("agent_transcript: agent %q not found", agentID)
	}

	return &AgentTranscriptView{
		AgentID:     snapshot.ID,
		Status:      snapshot.Status,
		Description: snapshot.Description,
		Messages:    snapshot.Messages,
		StartedAt:   snapshot.StartedAt,
		CompletedAt: snapshot.CompletedAt,
		IsRunning:   snapshot.Status == "running",
		TurnCount:   snapshot.Progress.ToolUseCount,
		TokenCount:  snapshot.Progress.TokenCount,
	}, nil
}

// ExportMarkdown returns the agent's transcript formatted as markdown.
// Suitable for human-readable export similar to session export.
func (a *AgentTranscriptAccess) ExportMarkdown(agentID string) (string, error) {
	view, err := a.GetTranscript(agentID)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Agent Transcript: %s\n\n", view.Description)
	fmt.Fprintf(&sb, "- **Agent ID:** %s\n", view.AgentID)
	fmt.Fprintf(&sb, "- **Status:** %s\n", view.Status)
	if !view.StartedAt.IsZero() {
		fmt.Fprintf(&sb, "- **Started:** %s\n", view.StartedAt.Format(time.RFC3339))
	}
	if !view.CompletedAt.IsZero() {
		fmt.Fprintf(&sb, "- **Completed:** %s\n", view.CompletedAt.Format(time.RFC3339))
	}
	if view.TurnCount > 0 {
		fmt.Fprintf(&sb, "- **Tool Uses:** %d\n", view.TurnCount)
	}
	if view.TokenCount > 0 {
		fmt.Fprintf(&sb, "- **Tokens:** %d\n", view.TokenCount)
	}
	if view.IsRunning {
		sb.WriteString("- **Note:** Agent is still running; this is a snapshot.\n")
	}
	sb.WriteString("\n---\n\n")

	for _, msg := range view.Messages {
		if msg == nil {
			continue
		}
		switch msg.Role {
		case schema.User:
			sb.WriteString("## User\n\n")
			sb.WriteString(msg.Content)
			sb.WriteString("\n\n")
		case schema.Assistant:
			sb.WriteString("## Assistant\n\n")
			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					fmt.Fprintf(&sb, "**Tool Call:** `%s`\n", tc.Function.Name)
					if tc.Function.Arguments != "" {
						fmt.Fprintf(&sb, "```json\n%s\n```\n", tc.Function.Arguments)
					}
					sb.WriteString("\n")
				}
			}
			if msg.Content != "" {
				sb.WriteString(msg.Content)
				sb.WriteString("\n\n")
			}
		case schema.Tool:
			fmt.Fprintf(&sb, "## Tool Result (`%s`)\n\n", msg.ToolName)
			if len(msg.Content) > 500 {
				sb.WriteString(msg.Content[:500])
				sb.WriteString("\n... (truncated)\n\n")
			} else {
				sb.WriteString(msg.Content)
				sb.WriteString("\n\n")
			}
		case schema.System:
			sb.WriteString("## System\n\n")
			sb.WriteString(msg.Content)
			sb.WriteString("\n\n")
		}
	}

	return sb.String(), nil
}

// GetMessages returns just the messages for an agent (lightweight variant).
func (a *AgentTranscriptAccess) GetMessages(agentID string) ([]*schema.Message, error) {
	if a.runner == nil {
		return nil, fmt.Errorf("agent_transcript: runner is nil")
	}
	snapshot, ok := a.runner.GetAgentSnapshot(agentID)
	if !ok {
		return nil, fmt.Errorf("agent_transcript: agent %q not found", agentID)
	}
	return snapshot.Messages, nil
}
