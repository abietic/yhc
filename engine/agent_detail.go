package engine

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

const (
	maxAgentDetailMessages    = 256
	maxAgentDetailOutputBytes = 64 * 1024
)

// ErrAgentExecutionDetailSelectionChanged reports that an exact execution
// selection is no longer current or no longer has the requested lineage.
var ErrAgentExecutionDetailSelectionChanged = errors.New("agent execution detail selection changed")

// AgentExecutionDetailRequest selects one current Agent execution generation.
type AgentExecutionDetailRequest struct {
	AgentID       string
	Generation    int64
	SessionID     string
	ThreadID      string
	IncludeOutput bool
}

// AgentExecutionDetail contains the bounded metadata and optional output for
// one exact current Agent execution generation.
type AgentExecutionDetail struct {
	Revision        uint64
	Agent           RuntimeAgentSnapshot
	Output          string
	OutputTruncated bool
	LoadError       string
}

// AgentDetailMessage is the bounded, source-merged message shape used by TUI
// Agent detail views. It deliberately excludes raw tool output beyond the
// runtime transcript bounds.
type AgentDetailMessage struct {
	ID               string
	Role             string
	Content          string
	ReasoningContent string
	ToolCallID       string
	ToolName         string
	ToolCalls        []RuntimeToolCallSnapshot
	Completed        bool
	Timestamp        time.Time
	Source           string
	explicitID       bool
}

// AgentDetailSnapshot combines canonical live state with retained or evicted
// transcript/output data. Runtime state always owns status, progress, lineage,
// attention, and terminal facts.
type AgentDetailSnapshot struct {
	Revision            uint64
	Agent               RuntimeAgentSnapshot
	Thread              RuntimeThreadSnapshot
	Messages            []AgentDetailMessage
	Output              string
	OutputTruncated     bool
	PendingMessageCount int
	UnresolvedCount     int
	SteeringState       string
	TotalPausedMS       int64
	Storage             string
	LoadError           string
}

// AgentExecutionDetail reads one exact current execution generation from
// runtime state. It never falls back to a newer generation or an AgentRunner.
func (e *QueryEngine) AgentExecutionDetail(
	request AgentExecutionDetailRequest,
) (AgentExecutionDetail, bool, error) {
	return e.agentExecutionDetail(request, readAgentDetailOutput)
}

func (e *QueryEngine) agentExecutionDetail(
	request AgentExecutionDetailRequest,
	readOutput func(string) (string, bool, error),
) (AgentExecutionDetail, bool, error) {
	if strings.TrimSpace(request.AgentID) == "" {
		return AgentExecutionDetail{}, false, fmt.Errorf("agent execution detail: AgentID is required")
	}
	if request.Generation <= 0 {
		return AgentExecutionDetail{}, false, fmt.Errorf("agent execution detail: Generation must be positive")
	}
	if strings.TrimSpace(request.SessionID) == "" {
		return AgentExecutionDetail{}, false, fmt.Errorf("agent execution detail: SessionID is required")
	}
	if strings.TrimSpace(request.ThreadID) == "" {
		return AgentExecutionDetail{}, false, fmt.Errorf("agent execution detail: ThreadID is required")
	}
	if e == nil || e.runtimeState == nil {
		return AgentExecutionDetail{}, false, nil
	}

	agent, thread, revision, found := e.runtimeState.AgentThreadSnapshot(request.AgentID)
	if !found {
		return AgentExecutionDetail{}, false, nil
	}
	if !agentExecutionDetailMatches(request, agent, thread) {
		return AgentExecutionDetail{}, false, fmt.Errorf(
			"agent execution detail: %w", ErrAgentExecutionDetailSelectionChanged,
		)
	}

	detail := AgentExecutionDetail{Revision: revision, Agent: agent}
	if !request.IncludeOutput || strings.TrimSpace(agent.OutputFile) == "" ||
		!isRuntimeTerminalAgentStatus(agent.Status) {
		return detail, true, nil
	}
	if readOutput == nil {
		readOutput = readAgentDetailOutput
	}
	output, truncated, err := readOutput(agent.OutputFile)
	currentAgent, currentThread, _, currentFound := e.runtimeState.AgentThreadSnapshot(request.AgentID)
	if !currentFound ||
		!agentExecutionDetailMatches(request, currentAgent, currentThread) ||
		currentAgent.OutputFile != agent.OutputFile {
		return AgentExecutionDetail{}, false, fmt.Errorf(
			"agent execution detail: %w", ErrAgentExecutionDetailSelectionChanged,
		)
	}
	if err != nil {
		detail.LoadError = joinAgentDetailError("", fmt.Sprintf("output: %v", err))
	} else {
		detail.Output = output
		detail.OutputTruncated = truncated
	}
	return detail, true, nil
}

func agentExecutionDetailMatches(
	request AgentExecutionDetailRequest,
	agent RuntimeAgentSnapshot,
	thread RuntimeThreadSnapshot,
) bool {
	return agent.AgentID == request.AgentID &&
		agent.Generation == request.Generation &&
		agent.SessionID == request.SessionID &&
		agent.ThreadID == request.ThreadID &&
		(thread.SessionID == "" || thread.SessionID == request.SessionID) &&
		(thread.ThreadID == "" || thread.ThreadID == request.ThreadID) &&
		(thread.AgentID == "" || thread.AgentID == request.AgentID) &&
		(thread.AgentGeneration <= 0 || thread.AgentGeneration == request.Generation)
}

// AgentDetailSnapshot returns one bounded read-only detail model for a
// canonical Agent. Retained runner messages are preferred over disk reads;
// evicted Agents bootstrap from the same persisted launch/terminal files.
func (e *QueryEngine) AgentDetailSnapshot(agentID string) (AgentDetailSnapshot, bool) {
	if e == nil || e.runtimeState == nil || strings.TrimSpace(agentID) == "" {
		return AgentDetailSnapshot{}, false
	}
	agent, thread, revision, ok := e.runtimeState.AgentThreadSnapshot(agentID)
	if !ok {
		return AgentDetailSnapshot{}, false
	}
	detail := AgentDetailSnapshot{
		Revision: revision,
		Agent:    agent,
		Thread:   thread,
		Storage:  "runtime-only",
	}
	detail.UnresolvedCount = len(thread.PendingInteractions)

	e.mu.Lock()
	runner := e.agentRunner
	e.mu.Unlock()

	var persistedMessages []*schema.Message
	var pendingMessages []AgentDetailMessage
	if runner != nil {
		if steering, exists := runner.AgentSteeringInfo(agentID); exists {
			detail.SteeringState = string(steering.State)
			detail.TotalPausedMS = steering.TotalPausedMs
		}
		if retained, exists := runner.GetAgentSnapshot(agentID); exists {
			detail.Storage = "retained"
			detail.PendingMessageCount = retained.PendingMessageCount
			persistedMessages = retained.Messages
			pendingMessages = agentDetailMessagesFromPayloads(retained.PendingMessages)
		} else if persisted, err := runner.LoadPersistedAgentSnapshot(agentID); err == nil {
			detail.Storage = "evicted"
			persistedMessages = persisted.Messages
		} else {
			detail.LoadError = truncateRuntimeText(err.Error(), maxRuntimeInteractionRunes)
		}
	}

	diskMessages := agentDetailMessagesFromSchema(persistedMessages)
	if len(diskMessages) == 0 && strings.TrimSpace(agent.Task) != "" {
		diskMessages = []AgentDetailMessage{{
			Role:      string(schema.User),
			Content:   truncateRuntimeText(agent.Task, maxRuntimeMessageRunes),
			Completed: true,
			Source:    "metadata",
		}}
	}
	runtimeMessages := agentDetailMessagesFromRuntime(detail.Thread)
	runtimeMessages = append(runtimeMessages, pendingMessages...)
	detail.Messages = mergeAgentDetailMessages(diskMessages, runtimeMessages, agent.ThreadID)

	if strings.TrimSpace(agent.OutputFile) != "" {
		output, truncated, err := readAgentDetailOutput(agent.OutputFile)
		switch {
		case err == nil:
			detail.Output = output
			detail.OutputTruncated = truncated
		case errors.Is(err, os.ErrNotExist) && agent.Status == "running":
		default:
			detail.LoadError = joinAgentDetailError(detail.LoadError, fmt.Sprintf("output: %v", err))
		}
	}
	return detail, true
}

func agentDetailMessagesFromSchema(messages []*schema.Message) []AgentDetailMessage {
	if len(messages) > maxAgentDetailMessages {
		messages = messages[len(messages)-maxAgentDetailMessages:]
	}
	out := make([]AgentDetailMessage, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		detail := AgentDetailMessage{
			ID:               explicitAgentDetailMessageID(message),
			Role:             string(message.Role),
			Content:          truncateRuntimeText(message.Content, maxRuntimeMessageRunes),
			ReasoningContent: truncateRuntimeText(message.ReasoningContent, maxRuntimeReasoningRunes),
			ToolCallID:       message.ToolCallID,
			ToolName:         message.ToolName,
			Completed:        true,
			Source:           "retained",
		}
		detail.explicitID = detail.ID != ""
		for i, call := range message.ToolCalls {
			if i >= maxRuntimeMessageToolCalls {
				break
			}
			detail.ToolCalls = append(detail.ToolCalls, RuntimeToolCallSnapshot{
				ID:           call.ID,
				Name:         call.Function.Name,
				InputPreview: truncateRuntimeText(call.Function.Arguments, maxRuntimeToolPreviewRunes),
			})
		}
		out = append(out, detail)
	}
	return out
}

func agentDetailMessagesFromRuntime(thread RuntimeThreadSnapshot) []AgentDetailMessage {
	messages := append([]RuntimeMessageSnapshot(nil), thread.Messages...)
	if thread.LiveMessage != nil {
		messages = append(messages, *thread.LiveMessage)
	}
	if len(messages) > maxAgentDetailMessages {
		messages = messages[len(messages)-maxAgentDetailMessages:]
	}
	out := make([]AgentDetailMessage, 0, len(messages))
	for _, message := range messages {
		detail := AgentDetailMessage{
			ID:               message.ID,
			Role:             message.Role,
			Content:          message.Content,
			ReasoningContent: message.ReasoningContent,
			ToolCallID:       message.ToolCallID,
			ToolName:         message.ToolName,
			ToolCalls:        append([]RuntimeToolCallSnapshot(nil), message.ToolCalls...),
			Completed:        message.Completed,
			Timestamp:        message.Timestamp,
			Source:           "runtime",
		}
		detail.explicitID = detail.ID != "" && !strings.HasPrefix(detail.ID, thread.ThreadID+":")
		out = append(out, detail)
	}
	return out
}

func mergeAgentDetailMessages(persisted, runtimeMessages []AgentDetailMessage, threadID string) []AgentDetailMessage {
	merged := make([]AgentDetailMessage, 0, min(len(persisted)+len(runtimeMessages), maxAgentDetailMessages))
	positions := make(map[string]int, len(persisted)+len(runtimeMessages))
	persistedKeys := agentDetailMergeKeys(persisted)
	for i, message := range persisted {
		message.ToolCalls = append([]RuntimeToolCallSnapshot(nil), message.ToolCalls...)
		merged = append(merged, message)
		positions[persistedKeys[i]] = len(merged) - 1
	}
	runtimeKeys := agentDetailMergeKeys(runtimeMessages)
	for i, message := range runtimeMessages {
		key := runtimeKeys[i]
		if position, exists := positions[key]; exists {
			merged[position] = mergeAgentDetailMessage(merged[position], message)
			continue
		}
		message.ToolCalls = append([]RuntimeToolCallSnapshot(nil), message.ToolCalls...)
		merged = append(merged, message)
		positions[key] = len(merged) - 1
	}
	if len(merged) > maxAgentDetailMessages {
		merged = append([]AgentDetailMessage(nil), merged[len(merged)-maxAgentDetailMessages:]...)
	}
	fingerprints := make(map[string]int, len(merged))
	ids := make(map[string]int, len(merged))
	for i := range merged {
		if merged[i].ID == "" {
			fingerprint := agentDetailMessageFingerprint(merged[i])
			fingerprints[fingerprint]++
			merged[i].ID = fmt.Sprintf("%s:message:%s:%d", threadID, fingerprint[:16], fingerprints[fingerprint])
		}
		baseID := merged[i].ID
		ids[baseID]++
		if ids[baseID] > 1 {
			merged[i].ID = fmt.Sprintf("%s:duplicate:%d", baseID, ids[baseID])
		}
	}
	return merged
}

func agentDetailMergeKeys(messages []AgentDetailMessage) []string {
	counts := make(map[string]int, len(messages))
	keys := make([]string, len(messages))
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		base := "content:" + agentDetailMessageFingerprint(message)
		if message.explicitID {
			base = "id:" + message.ID
		}
		counts[base]++
		keys[i] = fmt.Sprintf("%s:%d", base, counts[base])
	}
	return keys
}

func mergeAgentDetailMessage(persisted, live AgentDetailMessage) AgentDetailMessage {
	if live.ID != "" {
		persisted.ID = live.ID
	}
	if live.Role != "" {
		persisted.Role = live.Role
	}
	if live.Content != "" || persisted.Content == "" {
		persisted.Content = live.Content
	}
	if live.ReasoningContent != "" || persisted.ReasoningContent == "" {
		persisted.ReasoningContent = live.ReasoningContent
	}
	if live.ToolCallID != "" {
		persisted.ToolCallID = live.ToolCallID
	}
	if live.ToolName != "" {
		persisted.ToolName = live.ToolName
	}
	if len(live.ToolCalls) > 0 {
		persisted.ToolCalls = append([]RuntimeToolCallSnapshot(nil), live.ToolCalls...)
	}
	persisted.Completed = live.Completed
	if !live.Timestamp.IsZero() {
		persisted.Timestamp = live.Timestamp
	}
	persisted.Source = "retained+runtime"
	persisted.explicitID = persisted.explicitID || live.explicitID
	return persisted
}

func agentDetailMessageFingerprint(message AgentDetailMessage) string {
	hash := sha256.New()
	write := func(value string) {
		_, _ = io.WriteString(hash, value)
		_, _ = hash.Write([]byte{0})
	}
	write(message.Role)
	write(message.Content)
	write(message.ReasoningContent)
	write(message.ToolCallID)
	write(message.ToolName)
	for _, call := range message.ToolCalls {
		write(call.ID)
		write(call.Name)
		write(call.InputPreview)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func explicitAgentDetailMessageID(message *schema.Message) string {
	if message == nil || message.Extra == nil {
		return ""
	}
	for _, key := range []string{"uuid", "message_id", "id", "command_uuid"} {
		if id, ok := message.Extra[key].(string); ok && strings.TrimSpace(id) != "" {
			return id
		}
	}
	return ""
}

func agentDetailMessagesFromPayloads(messages []tools.MessagePayload) []AgentDetailMessage {
	if len(messages) > maxAgentDetailMessages {
		messages = messages[len(messages)-maxAgentDetailMessages:]
	}
	out := make([]AgentDetailMessage, 0, len(messages))
	for _, message := range messages {
		commandUUID, _ := message.Metadata["command_uuid"].(string)
		out = append(out, AgentDetailMessage{
			ID:         commandUUID,
			Role:       string(schema.User),
			Content:    truncateRuntimeText(message.Content, maxRuntimeMessageRunes),
			Completed:  false,
			Timestamp:  message.Timestamp,
			Source:     "pending",
			explicitID: strings.TrimSpace(commandUUID) != "",
		})
	}
	return out
}

func readAgentDetailOutput(path string) (string, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer file.Close() //nolint:errcheck
	info, err := file.Stat()
	if err != nil {
		return "", false, err
	}
	truncated := info.Size() > maxAgentDetailOutputBytes
	if truncated {
		if _, err := file.Seek(info.Size()-maxAgentDetailOutputBytes, io.SeekStart); err != nil {
			return "", false, err
		}
	}
	data, err := io.ReadAll(io.LimitReader(file, maxAgentDetailOutputBytes+1))
	if err != nil {
		return "", false, err
	}
	return strings.ToValidUTF8(string(data), ""), truncated, nil
}

func joinAgentDetailError(current, next string) string {
	current = strings.TrimSpace(current)
	next = strings.TrimSpace(next)
	if current == "" {
		return truncateRuntimeText(next, maxRuntimeInteractionRunes)
	}
	if next == "" {
		return current
	}
	return truncateRuntimeText(current+"; "+next, maxRuntimeInteractionRunes)
}
