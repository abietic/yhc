package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/abietic/yhc/tools"
)

// AgentMessageResult describes whether a detail-view message was queued into a
// running Agent or resumed a retained/evicted terminal Agent.
type AgentMessageResult struct {
	AgentID     string
	Disposition string
	MessageID   string
}

// SendAgentMessage routes detail-view input through AgentRunner's existing
// queue/resume boundary. command_uuid is the optimistic/replay dedupe identity.
func (e *QueryEngine) SendAgentMessage(agentID, content string) (AgentMessageResult, error) {
	if e == nil {
		return AgentMessageResult{}, fmt.Errorf("engine: cannot message Agent without a query engine")
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return AgentMessageResult{}, fmt.Errorf("engine: Agent ID is required")
	}
	if strings.TrimSpace(content) == "" {
		return AgentMessageResult{}, fmt.Errorf("engine: Agent message cannot be empty")
	}
	e.mu.Lock()
	runner := e.agentRunner
	from := firstNonEmptyString(e.config.AgentID, "team-lead")
	clock := e.config.Clock
	e.mu.Unlock()
	if runner == nil {
		return AgentMessageResult{}, fmt.Errorf("engine: Agent runtime is unavailable")
	}
	messageID := generateUUID()
	timestamp := time.Now().UTC()
	if clock != nil {
		timestamp = clock().UTC()
	}
	routedID, disposition, err := runner.SendOrResumeAgentMessage(agentID, tools.MessagePayload{
		From:      from,
		To:        agentID,
		Content:   content,
		Timestamp: timestamp,
		Metadata: map[string]any{
			"command_uuid": messageID,
		},
	})
	if err != nil {
		return AgentMessageResult{}, err
	}
	return AgentMessageResult{AgentID: routedID, Disposition: disposition, MessageID: messageID}, nil
}

// CancelAgentQueuedInput removes a still-pending Agent message by its stable
// command UUID. Messages already drained into a child turn cannot be recalled.
func (e *QueryEngine) CancelAgentQueuedInput(agentID, messageID string) (bool, error) {
	if e == nil {
		return false, fmt.Errorf("engine: cannot cancel Agent input without a query engine")
	}
	e.mu.Lock()
	runner := e.agentRunner
	e.mu.Unlock()
	if runner == nil {
		return false, fmt.Errorf("engine: Agent runtime is unavailable")
	}
	return runner.CancelAgentMessage(agentID, messageID)
}

// AbortAgent cancels a running Agent through the engine-scoped runner. The
// child query emits the canonical terminal event as cancellation settles.
func (e *QueryEngine) AbortAgent(agentID string) error {
	if e == nil {
		return fmt.Errorf("engine: cannot abort Agent without a query engine")
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return fmt.Errorf("engine: Agent ID is required")
	}
	e.mu.Lock()
	runner := e.agentRunner
	e.mu.Unlock()
	if runner == nil {
		return fmt.Errorf("engine: Agent runtime is unavailable")
	}
	return runner.AbortAgent(agentID)
}

// DetachAgent releases the exact foreground wait owned by this Session. The
// child keeps its Agent identity, generation, executor, and cancellation owner.
func (e *QueryEngine) DetachAgent(
	agentID string,
	generation int64,
) (tools.AgentDetachResult, error) {
	runner, normalizedID, err := e.agentControlRunner(agentID, "detach")
	if err != nil {
		return tools.AgentDetachResult{}, err
	}
	e.mu.Lock()
	parentSessionID := e.config.SessionID
	e.mu.Unlock()
	return runner.DetachAgent(tools.AgentDetachRequest{
		AgentID:         normalizedID,
		Generation:      generation,
		ParentSessionID: parentSessionID,
	})
}

// PauseAgent requests a pause at the next safe query/tool-round boundary.
func (e *QueryEngine) PauseAgent(agentID string) error {
	runner, normalizedID, err := e.agentControlRunner(agentID, "pause")
	if err != nil {
		return err
	}
	return runner.PauseAgent(normalizedID)
}

// ResumeAgent releases a query loop currently held at a safe pause boundary.
// Terminal Agents are resumed by SendAgentMessage with new input instead.
func (e *QueryEngine) ResumeAgent(agentID string) error {
	runner, normalizedID, err := e.agentControlRunner(agentID, "resume")
	if err != nil {
		return err
	}
	return runner.ResumeAgent(normalizedID)
}

func (e *QueryEngine) agentControlRunner(agentID, action string) (*tools.AgentRunner, string, error) {
	if e == nil {
		return nil, "", fmt.Errorf("engine: cannot %s Agent without a query engine", action)
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, "", fmt.Errorf("engine: Agent ID is required")
	}
	e.mu.Lock()
	runner := e.agentRunner
	e.mu.Unlock()
	if runner == nil {
		return nil, "", fmt.Errorf("engine: Agent runtime is unavailable")
	}
	return runner, agentID, nil
}

func (e *QueryEngine) waitAtAgentPauseCheckpoint(
	ctx context.Context,
	agentID string,
	emit func(AgentLifecycleEvent),
) (bool, error) {
	if e == nil {
		return false, nil
	}
	e.mu.Lock()
	runner := e.agentRunner
	e.mu.Unlock()
	if runner == nil || !runner.IsAgentPaused(agentID) {
		return false, nil
	}
	if emit != nil {
		emit(AgentLifecycleEvent{Phase: "paused", Status: "paused"})
	}
	_, err := runner.WaitIfAgentPaused(ctx, agentID)
	if err != nil {
		return true, err
	}
	if emit != nil {
		emit(AgentLifecycleEvent{Phase: "resumed_control", Status: "running"})
	}
	return true, nil
}
