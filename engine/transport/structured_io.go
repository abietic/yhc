// Package transport provides structured I/O for SDK-facing communication.
// It allows programmatic interaction with the agent via JSON messages over
// stdin/stdout, used by the SDK and remote integrations.
package transport

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/abietic/yhc/engine"
)

// InputMessage represents a structured input from the SDK/client.
type InputMessage struct {
	Type    string         `json:"type"` // "user_message", "tool_result", "control"
	Content string         `json:"content,omitempty"`
	Meta    map[string]any `json:"meta,omitempty"`
}

// OutputMessage represents a structured output to the SDK/client.
type OutputMessage struct {
	Type      string         `json:"type"` // "assistant", "tool_use", "error", "status", "result"
	Content   string         `json:"content,omitempty"`
	ToolName  string         `json:"tool_name,omitempty"`
	ToolInput map[string]any `json:"tool_input,omitempty"`
	ToolID    string         `json:"tool_id,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
}

// ControlType constants for control messages.
const (
	ControlInterrupt = "interrupt"
	ControlResume    = "resume"
	ControlCompact   = "compact"
	ControlStatus    = "status"
)

// IsControl returns true if the input message is a control message.
func (m *InputMessage) IsControl() bool {
	return m.Type == "control"
}

// StructuredIO handles reading/writing structured JSON messages over
// a reader/writer pair (typically os.Stdin and os.Stdout for CLI usage).
type StructuredIO struct {
	reader  *bufio.Reader
	writer  *bufio.Writer
	encoder *json.Encoder
	decoder *json.Decoder
	mu      sync.Mutex
}

// NewStructuredIO creates a new structured I/O transport over the given
// reader and writer (typically os.Stdin and os.Stdout for CLI usage).
func NewStructuredIO(r io.Reader, w io.Writer) *StructuredIO {
	br := bufio.NewReader(r)
	bw := bufio.NewWriter(w)
	return &StructuredIO{
		reader:  br,
		writer:  bw,
		encoder: json.NewEncoder(bw),
		decoder: json.NewDecoder(br),
	}
}

// ReadMessage reads and decodes the next input message.
// Returns io.EOF when the input stream is closed.
func (s *StructuredIO) ReadMessage() (*InputMessage, error) {
	var msg InputMessage
	if err := s.decoder.Decode(&msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// WriteMessage sends an output message to the client.
func (s *StructuredIO) WriteMessage(msg *OutputMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.encoder.Encode(msg); err != nil {
		return err
	}
	return s.writer.Flush()
}

// WriteAssistant sends an assistant text response.
func (s *StructuredIO) WriteAssistant(content string) error {
	return s.WriteMessage(&OutputMessage{
		Type:    "assistant",
		Content: content,
	})
}

// WriteToolUse sends a tool use request.
func (s *StructuredIO) WriteToolUse(toolName, toolID string, input map[string]any) error {
	return s.WriteMessage(&OutputMessage{
		Type:      "tool_use",
		ToolName:  toolName,
		ToolID:    toolID,
		ToolInput: input,
	})
}

// WriteError sends an error message.
func (s *StructuredIO) WriteError(err error) error {
	return s.WriteMessage(&OutputMessage{
		Type:    "error",
		Content: err.Error(),
	})
}

// WriteStatus sends a status update.
func (s *StructuredIO) WriteStatus(status string, meta map[string]any) error {
	return s.WriteMessage(&OutputMessage{
		Type:    "status",
		Content: status,
		Meta:    meta,
	})
}

// StreamAdapter adapts engine QueryEvents to structured output messages.
type StreamAdapter struct {
	sio *StructuredIO
}

// NewStreamAdapter creates an adapter that writes engine events as structured messages.
func NewStreamAdapter(sio *StructuredIO) *StreamAdapter {
	return &StreamAdapter{sio: sio}
}

// HandleEvent converts and writes a single engine event.
func (a *StreamAdapter) HandleEvent(event any) error {
	qe, ok := event.(*engine.QueryEvent)
	if !ok {
		return fmt.Errorf("transport: unsupported event type %T", event)
	}

	switch qe.Type {
	case engine.EventAssistant:
		content := ""
		if qe.AssistantMessage != nil {
			content = qe.AssistantMessage.Content
		}
		return a.sio.WriteAssistant(content)

	case engine.EventStream:
		content := ""
		if qe.StreamEvent != nil {
			content = qe.StreamEvent.Content
		}
		return a.sio.WriteMessage(&OutputMessage{
			Type:    "assistant",
			Content: content,
			Meta:    map[string]any{"streaming": true},
		})

	case engine.EventToolResult:
		content := ""
		if qe.ToolResultMessage != nil {
			content = qe.ToolResultMessage.Content
		}
		return a.sio.WriteMessage(&OutputMessage{
			Type:    "result",
			Content: content,
		})

	case engine.EventUserInterruption:
		return a.sio.WriteStatus("interrupted", map[string]any{
			"tool_use": qe.InterruptionToolUse,
		})

	case engine.EventMaxTurnsReached:
		meta := map[string]any{}
		if qe.MaxTurnsInfo != nil {
			meta["max_turns"] = qe.MaxTurnsInfo.MaxTurns
			meta["turn_count"] = qe.MaxTurnsInfo.TurnCount
		}
		return a.sio.WriteStatus("max_turns_reached", meta)

	case engine.EventTerminal:
		meta := map[string]any{}
		if qe.TerminalInfo != nil {
			meta["reason"] = string(qe.TerminalInfo.Reason)
			meta["turn_count"] = qe.TerminalInfo.TurnCount
			if qe.TerminalInfo.Err != nil {
				meta["error"] = qe.TerminalInfo.Err.Error()
			}
		}
		return a.sio.WriteStatus("terminal", meta)

	case engine.EventCompactBoundary:
		return a.sio.WriteStatus("compact_boundary", nil)

	case engine.EventToolUseSummary:
		meta := map[string]any{}
		if qe.ToolUseSummary != nil {
			meta["summary"] = qe.ToolUseSummary.Summary
			meta["tool_use_ids"] = qe.ToolUseSummary.PrecedingToolUseIDs
		}
		return a.sio.WriteStatus("tool_use_summary", meta)

	case engine.EventCommandLifecycle:
		meta := map[string]any{}
		if qe.CommandLifecycle != nil {
			meta["command_uuid"] = qe.CommandLifecycle.CommandUUID
			meta["phase"] = string(qe.CommandLifecycle.Phase)
		}
		return a.sio.WriteStatus("command_lifecycle", meta)

	default:
		return a.sio.WriteStatus(string(qe.Type), nil)
	}
}
