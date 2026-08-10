package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
)

// MessagePayload represents a message sent between agents or tasks.
type MessagePayload struct {
	From      string         `json:"from"`
	To        string         `json:"to"`
	Content   string         `json:"content"`
	Summary   string         `json:"summary,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	Color     string         `json:"color,omitempty"`
}

// StructuredMessageType identifies the kind of structured message.
type StructuredMessageType string

const (
	StructuredShutdownRequest      StructuredMessageType = "shutdown_request"
	StructuredShutdownResponse     StructuredMessageType = "shutdown_response"
	StructuredPlanApprovalResponse StructuredMessageType = "plan_approval_response"
)

// StructuredMessage represents a typed control message between agents.
type StructuredMessage struct {
	Type      StructuredMessageType `json:"type"`
	RequestID string                `json:"request_id,omitempty"`
	Approve   *bool                 `json:"approve,omitempty"`
	Reason    string                `json:"reason,omitempty"`
	Feedback  string                `json:"feedback,omitempty"`
}

// MessageRouting carries sender/receiver context for UI rendering.
type MessageRouting struct {
	Sender      string `json:"sender"`
	SenderColor string `json:"senderColor,omitempty"`
	Target      string `json:"target"`
	TargetColor string `json:"targetColor,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Content     string `json:"content,omitempty"`
}

// MessageOutput is the result of a direct message send.
type MessageOutput struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Routing *MessageRouting `json:"routing,omitempty"`
}

// BroadcastOutput is the result of a broadcast to all teammates.
type BroadcastOutput struct {
	Success    bool            `json:"success"`
	Message    string          `json:"message"`
	Recipients []string        `json:"recipients"`
	Routing    *MessageRouting `json:"routing,omitempty"`
}

// SendMessageInput matches the reference input schema.
// Supports both new (to/message) and legacy (recipient/content) field names.
type SendMessageInput struct {
	To        string          `json:"to"`
	Summary   string          `json:"summary,omitempty"`
	Message   json.RawMessage `json:"message"`
	Recipient string          `json:"recipient,omitempty"`
	Content   string          `json:"content,omitempty"`
	Metadata  map[string]any  `json:"metadata,omitempty"`
}

const teamLeadName = "team-lead"

// Message queue infrastructure for inter-agent communication.
var (
	messageQueues = make(map[string][]MessagePayload)
	mqMu          sync.RWMutex
)

// EnqueueMessage appends a message to the recipient's queue.
func EnqueueMessage(recipient string, msg MessagePayload) {
	mqMu.Lock()
	defer mqMu.Unlock()
	messageQueues[recipient] = append(messageQueues[recipient], msg)
}

// DequeueMessages removes and returns all messages for the given recipient.
func DequeueMessages(recipient string) []MessagePayload {
	mqMu.Lock()
	defer mqMu.Unlock()
	msgs := messageQueues[recipient]
	if len(msgs) == 0 {
		return nil
	}
	delete(messageQueues, recipient)
	return msgs
}

// PeekMessages returns all messages for the given recipient without removing them.
func PeekMessages(recipient string) []MessagePayload {
	mqMu.RLock()
	defer mqMu.RUnlock()
	msgs := messageQueues[recipient]
	if len(msgs) == 0 {
		return nil
	}
	out := make([]MessagePayload, len(msgs))
	copy(out, msgs)
	return out
}

// SendMessageTeamContext provides team info for message routing.
// Uses the existing TeamMember type from team.go.
type SendMessageTeamContext struct {
	TeamName string
	Members  []SendMessageTeamMember
}

// SendMessageTeamMember represents a teammate for message routing.
type SendMessageTeamMember struct {
	Name    string `json:"name"`
	AgentID string `json:"agentId,omitempty"`
	Color   string `json:"color,omitempty"`
}

// SendMessageConfig configures the SendMessage tool.
type SendMessageConfig struct {
	AgentName string
	AgentID   string
	Color     string
	GetTeam   func() *SendMessageTeamContext
}

var defaultSendConfig = &SendMessageConfig{
	AgentName: "assistant",
}

// SetSendMessageConfig sets the global send message configuration.
func SetSendMessageConfig(cfg *SendMessageConfig) {
	defaultSendConfig = cfg
}

// SendMessageTool returns a tool that sends messages to other agents or tasks
// for inter-agent communication in multi-agent workflows.
//
// Reference: src/tools/SendMessageTool/SendMessageTool.ts (917 lines)
func SendMessageTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "SendMessage",
			Desc: `Send a message to a previously spawned background agent to continue its work with additional context or instructions. The agent resumes with full context from its prior run.

Note: This only works for agents launched with run_in_background=true. Foreground agents are automatically shut down after returning their result and cannot receive further messages.`,
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"to": {
					Type:     "string",
					Desc:     `Recipient: agent name or ID, "*" for broadcast, "uds:<socket-path>" for a local peer, or "bridge:<session-id>" for a Remote Control peer`,
					Required: true,
				},
				"message": {
					Type:     "string",
					Desc:     "The message content to send. Can be plain text or a structured message.",
					Required: true,
				},
				"summary": {
					Type: "string",
					Desc: "A 5-10 word summary shown as a preview in the UI (required when message is a string)",
				},
			}),
		},
		ValidateInput: validateSendMessageInput,
		Execute:       executeSendMessage,
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			manager, err := taskManagerForToolContext(ctx)
			if err != nil {
				return "", err
			}
			runner := AgentRunnerFromCtx(ctx)
			if runner == nil {
				return "", ErrMissingToolOwner
			}
			return executeSendMessageWithRuntime(
				input,
				runner,
				manager,
			)
		},
	}
}

// validateSendMessageInput validates the input before execution.
// Reference: SendMessageTool.ts validateInput (lines 604-718)
func validateSendMessageInput(input map[string]any) error {
	to, _ := input["to"].(string)
	to = strings.TrimSpace(to)
	if to == "" {
		return fmt.Errorf("'to' must not be empty")
	}
	if strings.Contains(to, "@") {
		return fmt.Errorf("to must be a bare teammate name or star — there is only one team per session")
	}

	if strings.HasPrefix(to, "uds:") && strings.TrimSpace(to[4:]) == "" {
		return fmt.Errorf("address target must not be empty")
	}
	if strings.HasPrefix(to, "bridge:") && strings.TrimSpace(to[7:]) == "" {
		return fmt.Errorf("address target must not be empty")
	}

	msg := input["message"]
	switch v := msg.(type) {
	case string:
		if strings.HasPrefix(to, "bridge:") || strings.HasPrefix(to, "uds:") {
			return nil
		}
		summary, _ := input["summary"].(string)
		if to != "*" && strings.TrimSpace(summary) == "" {
			return fmt.Errorf("'summary' is required when message is a string")
		}
	case map[string]any:
		if to == "*" {
			return fmt.Errorf("structured messages cannot be broadcast to all")
		}
		if strings.HasPrefix(to, "bridge:") || strings.HasPrefix(to, "uds:") {
			return fmt.Errorf("structured messages cannot be sent cross-session — only plain text")
		}
		msgType, _ := v["type"].(string)
		if msgType == "shutdown_response" && to != teamLeadName {
			return fmt.Errorf("shutdown_response must be sent to %q", teamLeadName)
		}
		if msgType == "shutdown_response" {
			approve, _ := v["approve"].(bool)
			if !approve {
				reason, _ := v["reason"].(string)
				if strings.TrimSpace(reason) == "" {
					return fmt.Errorf("'reason' is required when rejecting a shutdown request")
				}
			}
		}
	case nil:
		return fmt.Errorf("'message' is required")
	default:
		_ = v
	}
	return nil
}

func executeSendMessage(input string) (string, error) {
	return executeSendMessageWithRuntime(input, nil, nil)
}

func executeSendMessageWithRuntime(
	input string,
	runner *AgentRunner,
	taskManager *TaskManager,
) (string, error) {
	if runner == nil || taskManager == nil {
		return "", ErrMissingToolOwner
	}
	var params SendMessageInput
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("send_message: invalid params: %w", err)
	}

	// Support legacy field names (recipient/content) for backward compatibility
	to := strings.TrimSpace(params.To)
	if to == "" {
		to = strings.TrimSpace(params.Recipient)
	}
	if to == "" {
		return "", fmt.Errorf("send_message: 'to' is required")
	}

	var messageText string
	if len(params.Message) > 0 {
		if err := json.Unmarshal(params.Message, &messageText); err != nil {
			messageText = string(params.Message)
		}
	}
	messageText = strings.TrimSpace(messageText)
	if messageText == "" {
		messageText = strings.TrimSpace(params.Content)
	}
	if messageText == "" {
		return "", fmt.Errorf("send_message: 'message' is required")
	}

	var structured *StructuredMessage
	if strings.HasPrefix(strings.TrimSpace(string(params.Message)), "{") {
		var sm StructuredMessage
		if err := json.Unmarshal(params.Message, &sm); err == nil && sm.Type != "" {
			structured = &sm
		}
	}

	if to == "*" {
		return handleBroadcast(messageText, params.Summary)
	}

	// UDS peer routing: "uds:<socket-path>"
	if strings.HasPrefix(to, "uds:") {
		return handleUDSMessage(to[4:], messageText, params.Summary)
	}

	// Bridge routing: "bridge:<session-id>"
	if strings.HasPrefix(to, "bridge:") {
		return handleBridgeMessage(to[7:], messageText, params.Summary)
	}

	if structured != nil {
		return handleStructuredMessageWithRunner(to, structured, messageText, params.Summary, runner)
	}

	return handleDirectMessageWithRuntime(
		to,
		messageText,
		params.Summary,
		params.Metadata,
		runner,
		taskManager,
	)
}

func handleDirectMessageWithRunner(
	recipient,
	content,
	summary string,
	metadata map[string]any,
	runner *AgentRunner,
) (string, error) {
	return "", ErrMissingToolOwner
}

func handleDirectMessageWithRuntime(
	recipient,
	content,
	summary string,
	metadata map[string]any,
	runner *AgentRunner,
	taskManager *TaskManager,
) (string, error) {
	cfg := defaultSendConfig
	senderName := cfg.AgentName

	msg := MessagePayload{
		From:      senderName,
		To:        recipient,
		Content:   content,
		Summary:   summary,
		Color:     cfg.Color,
		Metadata:  metadata,
		Timestamp: time.Now().UTC(),
	}

	if runner != nil {
		if agentID, disposition, err := runner.SendOrResumeAgentMessage(recipient, msg); err == nil {
			result := MessageOutput{
				Success: true,
				Message: fmt.Sprintf("Message delivered to %q.", agentID),
				Routing: &MessageRouting{
					Sender:  senderName,
					Target:  fmt.Sprintf("@%s", recipient),
					Summary: summary,
					Content: content,
				},
			}
			if disposition == "resumed" {
				result.Message = fmt.Sprintf("Agent %q was stopped; resumed in the background with your message.", agentID)
			}
			data, _ := json.Marshal(result)
			return string(data), nil
		} else if !isAgentNotFoundError(err) {
			return "", fmt.Errorf("send_message: %w", err)
		}
	}

	_, found := taskManager.Get(recipient)
	if !found {
		EnqueueMessage(recipient, msg)
		result := MessageOutput{
			Success: true,
			Message: fmt.Sprintf("Message queued for %q (agent/task not currently running).", recipient),
			Routing: &MessageRouting{
				Sender:  senderName,
				Target:  fmt.Sprintf("@%s", recipient),
				Summary: summary,
				Content: content,
			},
		}
		data, _ := json.Marshal(result)
		return string(data), nil
	}

	EnqueueMessage(recipient, msg)
	result := MessageOutput{
		Success: true,
		Message: fmt.Sprintf("Message delivered to %q.", recipient),
		Routing: &MessageRouting{
			Sender:  senderName,
			Target:  fmt.Sprintf("@%s", recipient),
			Summary: summary,
			Content: content,
		},
	}
	data, _ := json.Marshal(result)
	return string(data), nil
}

func handleBroadcast(content, summary string) (string, error) {
	cfg := defaultSendConfig
	senderName := cfg.AgentName

	var getTeam func() *SendMessageTeamContext
	if cfg.GetTeam != nil {
		getTeam = cfg.GetTeam
	}

	if getTeam == nil {
		return "", fmt.Errorf("send_message: not in a team context, create a team first")
	}

	team := getTeam()
	if team == nil {
		return "", fmt.Errorf("send_message: team context unavailable")
	}

	var recipients []string
	for _, member := range team.Members {
		if strings.EqualFold(member.Name, senderName) {
			continue
		}
		recipients = append(recipients, member.Name)
	}

	if len(recipients) == 0 {
		result := BroadcastOutput{
			Success:    true,
			Message:    "No teammates to broadcast to (you are the only team member)",
			Recipients: []string{},
		}
		data, _ := json.Marshal(result)
		return string(data), nil
	}

	for _, recipientName := range recipients {
		msg := MessagePayload{
			From:      senderName,
			To:        recipientName,
			Content:   content,
			Summary:   summary,
			Color:     cfg.Color,
			Timestamp: time.Now().UTC(),
		}
		EnqueueMessage(recipientName, msg)
	}

	result := BroadcastOutput{
		Success:    true,
		Message:    fmt.Sprintf("Message broadcast to %d teammate(s): %s", len(recipients), strings.Join(recipients, ", ")),
		Recipients: recipients,
		Routing: &MessageRouting{
			Sender:  senderName,
			Target:  "@team",
			Summary: summary,
			Content: content,
		},
	}
	data, _ := json.Marshal(result)
	return string(data), nil
}

func handleStructuredMessageWithRunner(to string, msg *StructuredMessage, content, summary string, runner *AgentRunner) (string, error) {
	cfg := defaultSendConfig
	senderName := cfg.AgentName

	switch msg.Type {
	case StructuredShutdownRequest:
		payload := MessagePayload{
			From:    senderName,
			To:      to,
			Content: content,
			Summary: "Shutdown request",
			Metadata: map[string]any{
				"type":   string(StructuredShutdownRequest),
				"reason": msg.Reason,
			},
			Timestamp: time.Now().UTC(),
		}
		EnqueueMessage(to, payload)
		result := map[string]any{
			"success": true,
			"message": fmt.Sprintf("Shutdown request sent to %s.", to),
			"target":  to,
		}
		data, _ := json.Marshal(result)
		return string(data), nil

	case StructuredShutdownResponse:
		approve := msg.Approve != nil && *msg.Approve
		payload := MessagePayload{
			From:    senderName,
			To:      teamLeadName,
			Content: content,
			Summary: fmt.Sprintf("Shutdown %s", map[bool]string{true: "approved", false: "rejected"}[approve]),
			Metadata: map[string]any{
				"type":       string(StructuredShutdownResponse),
				"request_id": msg.RequestID,
				"approve":    approve,
				"reason":     msg.Reason,
			},
			Timestamp: time.Now().UTC(),
		}
		EnqueueMessage(teamLeadName, payload)
		action := "rejected"
		if approve {
			action = "approved"
		}
		result := map[string]any{
			"success":    true,
			"message":    fmt.Sprintf("Shutdown %s response sent.", action),
			"request_id": msg.RequestID,
		}
		data, _ := json.Marshal(result)
		return string(data), nil

	case StructuredPlanApprovalResponse:
		approve := msg.Approve != nil && *msg.Approve
		payload := MessagePayload{
			From:    senderName,
			To:      to,
			Content: content,
			Summary: fmt.Sprintf("Plan %s", map[bool]string{true: "approved", false: "rejected"}[approve]),
			Metadata: map[string]any{
				"type":       string(StructuredPlanApprovalResponse),
				"request_id": msg.RequestID,
				"approve":    approve,
				"feedback":   msg.Feedback,
			},
			Timestamp: time.Now().UTC(),
		}
		EnqueueMessage(to, payload)
		action := "rejected"
		if approve {
			action = "approved"
		}
		result := map[string]any{
			"success":    true,
			"message":    fmt.Sprintf("Plan approval %s sent to %s.", action, to),
			"request_id": msg.RequestID,
		}
		data, _ := json.Marshal(result)
		return string(data), nil

	default:
		return handleDirectMessageWithRunner(to, content, summary, nil, runner)
	}
}

func isAgentNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	errText := err.Error()
	return strings.HasPrefix(errText, "agent_runner: agent ") && strings.HasSuffix(errText, " not found")
}

// UDS message handler — sends via Unix Domain Socket to a peer process.
// Reference: SendMessageTool.ts routes "uds:<socket-path>" to local peers.
var UDSSender func(socketPath, message string) error

func handleUDSMessage(socketPath, content, summary string) (string, error) {
	if UDSSender == nil {
		return "", fmt.Errorf("send_message: UDS transport not configured")
	}
	payload := map[string]string{
		"from":    defaultSendConfig.AgentName,
		"content": content,
		"summary": summary,
	}
	data, _ := json.Marshal(payload)
	if err := UDSSender(socketPath, string(data)); err != nil {
		return "", fmt.Errorf("send_message: UDS send failed: %w", err)
	}
	result := MessageOutput{
		Success: true,
		Message: fmt.Sprintf("Message sent via UDS to %s", socketPath),
		Routing: &MessageRouting{
			Sender:  defaultSendConfig.AgentName,
			Target:  "uds:" + socketPath,
			Summary: summary,
			Content: content,
		},
	}
	out, _ := json.Marshal(result)
	return string(out), nil
}

// Bridge message handler — sends via bridge transport to a remote session.
// Reference: SendMessageTool.ts routes "bridge:<session-id>" to Remote Control peers.
var BridgeSender func(sessionID, message string) error

func handleBridgeMessage(sessionID, content, summary string) (string, error) {
	if BridgeSender == nil {
		return "", fmt.Errorf("send_message: bridge transport not configured")
	}
	payload := map[string]string{
		"from":    defaultSendConfig.AgentName,
		"content": content,
		"summary": summary,
	}
	data, _ := json.Marshal(payload)
	if err := BridgeSender(sessionID, string(data)); err != nil {
		return "", fmt.Errorf("send_message: bridge send failed: %w", err)
	}
	result := MessageOutput{
		Success: true,
		Message: fmt.Sprintf("Message sent via bridge to session %s", sessionID),
		Routing: &MessageRouting{
			Sender:  defaultSendConfig.AgentName,
			Target:  "bridge:" + sessionID,
			Summary: summary,
			Content: content,
		},
	}
	out, _ := json.Marshal(result)
	return string(out), nil
}
