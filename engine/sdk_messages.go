package engine

import (
	"encoding/json"
	"time"

	"github.com/cloudwego/eino/schema"
)

// SDKMessageType identifies the kind of SDK message.
type SDKMessageType string

const (
	SDKMessageAssistant      SDKMessageType = "assistant"
	SDKMessageUser           SDKMessageType = "user"
	SDKMessageSystem         SDKMessageType = "system"
	SDKMessageToolUse        SDKMessageType = "tool_use"
	SDKMessageToolResult     SDKMessageType = "tool_result"
	SDKMessageResult         SDKMessageType = "result"
	SDKMessageCompactSummary SDKMessageType = "compact_summary"
	SDKMessageSystemInit     SDKMessageType = "system_init"
)

// SDKMessage is a normalized message format for SDK consumers (ACP, headless).
// Mirrors the reference SDK message types from QueryEngine.ts.
type SDKMessage struct {
	Type      SDKMessageType  `json:"type"`
	Message   *schema.Message `json:"message,omitempty"`
	Timestamp time.Time       `json:"timestamp"`

	// For tool_use messages
	ToolName  string `json:"tool_name,omitempty"`
	ToolInput string `json:"tool_input,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`

	// For result messages
	ResultType string `json:"result_type,omitempty"` // "success", "error", "max_turns"
	TurnCount  int    `json:"turn_count,omitempty"`

	// For system_init
	Model     string   `json:"model,omitempty"`
	Tools     []string `json:"tools,omitempty"`
	SessionID string   `json:"session_id,omitempty"`

	// Raw JSON for extension
	Extra json.RawMessage `json:"extra,omitempty"`
}

// QueryEventToSDKMessage converts an internal QueryEvent to an SDK-compatible message.
func QueryEventToSDKMessage(evt QueryEvent) *SDKMessage {
	now := time.Now()
	if !evt.Timestamp.IsZero() {
		now = evt.Timestamp
	}

	switch evt.Type {
	case EventAssistant:
		msg := evt.AssistantMessage
		if msg == nil {
			msg = evt.Message
		}
		return &SDKMessage{
			Type:      SDKMessageAssistant,
			Message:   msg,
			Timestamp: now,
		}

	case EventToolResult:
		toolName := ""
		toolUseID := ""
		if evt.ToolResultMessage != nil {
			toolName = evt.ToolResultMessage.ToolName
			toolUseID = evt.ToolResultMessage.ToolCallID
		}
		return &SDKMessage{
			Type:      SDKMessageToolResult,
			Message:   evt.ToolResultMessage,
			ToolName:  toolName,
			ToolUseID: toolUseID,
			Timestamp: now,
		}

	case EventCompactBoundary:
		return &SDKMessage{
			Type:      SDKMessageCompactSummary,
			Message:   evt.Message,
			Timestamp: now,
		}

	case EventTerminal:
		resultType := "success"
		turnCount := 0
		if evt.TerminalInfo != nil {
			switch evt.TerminalInfo.Reason {
			case TerminalCompleted:
				resultType = "success"
			case TerminalMaxTurns:
				resultType = "max_turns"
			case TerminalAbortedStreaming, TerminalAbortedTools:
				resultType = "aborted"
			default:
				resultType = "error"
			}
			turnCount = evt.TerminalInfo.TurnCount
		}
		return &SDKMessage{
			Type:       SDKMessageResult,
			Message:    evt.Message,
			ResultType: resultType,
			TurnCount:  turnCount,
			Timestamp:  now,
		}

	case EventAttachment:
		return &SDKMessage{
			Type:      SDKMessageUser,
			Message:   evt.AttachmentMessage,
			Timestamp: now,
		}

	case EventToolUseSummary:
		toolName := ""
		toolInput := ""
		toolID := ""
		if evt.ToolUseSummary != nil {
			toolName = "ToolUseSummary"
			toolID = evt.ToolUseSummary.UUID
		}
		return &SDKMessage{
			Type:      SDKMessageToolUse,
			ToolName:  toolName,
			ToolInput: toolInput,
			ToolUseID: toolID,
			Timestamp: now,
		}

	case EventToolProgress:
		return nil // Progress events are streaming-only, not surfaced to SDK

	default:
		return &SDKMessage{
			Type:      SDKMessageSystem,
			Message:   evt.Message,
			Timestamp: now,
		}
	}
}

// SDKEventStream wraps a QueryEvent channel and emits SDK messages.
func SDKEventStream(events <-chan QueryEvent) <-chan *SDKMessage {
	out := make(chan *SDKMessage, 32)
	go func() {
		defer close(out)
		for evt := range events {
			if msg := QueryEventToSDKMessage(evt); msg != nil {
				out <- msg
			}
		}
	}()
	return out
}
