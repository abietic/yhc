package transport

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/abietic/yhc/engine"
)

// LifecycleSchemaVersion is the stable schema version for the headless JSONL
// lifecycle stream. The version covers both event and final result records.
const LifecycleSchemaVersion = 1

// LifecycleRecordType selects the one payload carried by a lifecycle record.
type LifecycleRecordType string

const (
	LifecycleRecordEvent  LifecycleRecordType = "event"
	LifecycleRecordResult LifecycleRecordType = "result"
)

// LifecycleRecord is one newline-delimited JSON record. Exactly one of Event
// and Result is populated according to Type.
type LifecycleRecord struct {
	SchemaVersion int                 `json:"schema_version"`
	Type          LifecycleRecordType `json:"type"`
	Event         *LifecycleEvent     `json:"event,omitempty"`
	Result        *LifecycleResult    `json:"result,omitempty"`
}

// LifecycleIdentity is the transport-safe subset of RuntimeEventEnvelope used
// to correlate and order one headless stream. It intentionally omits parent,
// Goal, path, prompt, and provider data.
type LifecycleIdentity struct {
	SessionID   string `json:"session_id,omitempty"`
	ThreadID    string `json:"thread_id,omitempty"`
	TurnID      string `json:"turn_id,omitempty"`
	Sequence    uint64 `json:"sequence,omitempty"`
	Timestamp   string `json:"timestamp,omitempty"`
	CausationID string `json:"causation_id,omitempty"`
}

// LifecycleIdentityFromEnvelope returns the bounded outward identity for one
// engine event without retaining a mutable engine value.
func LifecycleIdentityFromEnvelope(envelope engine.RuntimeEventEnvelope) LifecycleIdentity {
	identity := LifecycleIdentity{
		SessionID:   envelope.SessionID,
		ThreadID:    envelope.ThreadID,
		TurnID:      envelope.TurnID,
		Sequence:    envelope.Sequence,
		CausationID: envelope.CausationID,
	}
	if !envelope.Timestamp.IsZero() {
		identity.Timestamp = envelope.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	return identity
}

// LifecycleEvent is the closed version-1 outward event union. Canonical
// assistant/tool facts are already committed and redacted by the engine. The
// remaining event types carry only renderer-neutral bounded fields.
type LifecycleEvent struct {
	LifecycleIdentity

	Kind         string                    `json:"kind"`
	Family       engine.RuntimeEventFamily `json:"family"`
	Assistant    *LifecycleAssistant       `json:"assistant,omitempty"`
	Tool         *LifecycleTool            `json:"tool,omitempty"`
	Command      *LifecycleCommand         `json:"command,omitempty"`
	MaxTurns     *LifecycleMaxTurns        `json:"max_turns,omitempty"`
	Interruption *LifecycleInterruption    `json:"interruption,omitempty"`
}

// LifecycleAssistant is the bounded payload for one committed assistant delta.
type LifecycleAssistant struct {
	MessageID string `json:"message_id"`
	Delta     string `json:"delta"`
}

// LifecycleTool is the canonical, engine-redacted payload for one tool event.
type LifecycleTool struct {
	ToolCallID     string                      `json:"tool_call_id"`
	ToolName       string                      `json:"tool_name,omitempty"`
	EffectiveInput json.RawMessage             `json:"effective_input,omitempty"`
	Content        string                      `json:"content,omitempty"`
	Outcome        engine.CanonicalToolOutcome `json:"outcome,omitempty"`
	RawOutput      json.RawMessage             `json:"raw_output,omitempty"`
}

// LifecycleCommand is the renderer-neutral result of one slash command.
type LifecycleCommand struct {
	Command string                     `json:"command"`
	Status  engine.CommandResultStatus `json:"status"`
	Output  string                     `json:"output,omitempty"`
}

// LifecycleMaxTurns records the configured and observed turn boundary.
type LifecycleMaxTurns struct {
	MaxTurns  int `json:"max_turns"`
	TurnCount int `json:"turn_count"`
}

// LifecycleInterruption records whether user interruption occurred in tool use.
type LifecycleInterruption struct {
	ToolUse bool `json:"tool_use"`
}

// LifecycleResult is the exactly-once closing record written after the engine
// event channel drains and the headless process classifies its exit status.
// Pre-turn failures carry only identity that exists; pre-engine failures carry none.
type LifecycleResult struct {
	LifecycleIdentity

	Status         string          `json:"status"`
	Output         string          `json:"output,omitempty"`
	TerminalReason string          `json:"terminal_reason,omitempty"`
	ExitCode       int             `json:"exit_code"`
	Error          *LifecycleError `json:"error,omitempty"`
}

// LifecycleError is the sanitized, classified error carried by a result.
type LifecycleError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

var (
	errLifecycleWriterNil         = errors.New("lifecycle writer is nil")
	errLifecycleCanonicalEventNil = errors.New("lifecycle canonical projection is missing")
	errLifecycleInvalidUTF8       = errors.New("lifecycle projection contains invalid UTF-8")
)

// LifecycleWriter serializes complete records without interleaving concurrent
// writers. json.Encoder emits the newline delimiter for every record.
type LifecycleWriter struct {
	mu      sync.Mutex
	encoder *json.Encoder
}

// NewLifecycleWriter creates a newline-delimited, non-HTML-escaping encoder.
func NewLifecycleWriter(writer io.Writer) *LifecycleWriter {
	if writer == nil {
		return &LifecycleWriter{}
	}
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return &LifecycleWriter{encoder: encoder}
}

// WriteEvent writes one supported engine event. Unsupported or duplicate
// legacy projection events are skipped deliberately and return written=false.
func (writer *LifecycleWriter) WriteEvent(event engine.QueryEvent) (written bool, err error) {
	projected, ok, err := ProjectLifecycleEvent(event)
	if err != nil || !ok {
		return false, err
	}
	if err := writer.write(LifecycleRecord{
		SchemaVersion: LifecycleSchemaVersion,
		Type:          LifecycleRecordEvent,
		Event:         projected,
	}); err != nil {
		return false, err
	}
	return true, nil
}

// WriteResult writes the single final process result record.
func (writer *LifecycleWriter) WriteResult(result LifecycleResult) error {
	return writer.write(LifecycleRecord{
		SchemaVersion: LifecycleSchemaVersion,
		Type:          LifecycleRecordResult,
		Result:        &result,
	})
}

func (writer *LifecycleWriter) write(record LifecycleRecord) error {
	if writer == nil || writer.encoder == nil {
		return errLifecycleWriterNil
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.encoder.Encode(record)
}

// ProjectLifecycleEvent selects the safe, non-duplicated version-1 outward
// events. EventTerminal is intentionally skipped because the headless adapter
// owns one final classified LifecycleResult record.
func ProjectLifecycleEvent(event engine.QueryEvent) (*LifecycleEvent, bool, error) {
	identity := LifecycleIdentityFromEnvelope(event.RuntimeEventEnvelope)
	projected := &LifecycleEvent{
		LifecycleIdentity: identity,
		Family:            event.Family(),
	}

	switch event.Type {
	case engine.EventCanonicalProjection:
		if event.CanonicalProjection == nil {
			return nil, false, errLifecycleCanonicalEventNil
		}
		if err := event.CanonicalProjection.Validate(); err != nil {
			return nil, false, err
		}
		canonical := event.CanonicalProjection.Clone()
		if !validLifecycleCanonicalUTF8(canonical) {
			return nil, false, errLifecycleInvalidUTF8
		}
		projected.Kind = string(canonical.Kind)
		if canonical.Assistant != nil {
			projected.Assistant = &LifecycleAssistant{
				MessageID: canonical.Assistant.MessageID,
				Delta:     string(canonical.Assistant.Delta),
			}
		}
		if canonical.Tool != nil {
			projected.Tool = &LifecycleTool{
				ToolCallID:     canonical.Tool.ToolCallID,
				ToolName:       canonical.Tool.ToolName,
				EffectiveInput: bytes.Clone(canonical.Tool.EffectiveInput),
				Content:        canonical.Tool.Content,
				Outcome:        canonical.Tool.Outcome,
				RawOutput:      bytes.Clone(canonical.Tool.RawOutput),
			}
		}
		return projected, true, nil
	case engine.EventCommandResult:
		if event.CommandResult == nil {
			return nil, false, nil
		}
		if !utf8.ValidString(event.CommandResult.Command) ||
			!utf8.ValidString(string(event.CommandResult.Status)) ||
			!utf8.ValidString(event.CommandResult.Output) {
			return nil, false, errLifecycleInvalidUTF8
		}
		projected.Kind = string(engine.EventCommandResult)
		projected.Command = &LifecycleCommand{
			Command: event.CommandResult.Command,
			Status:  event.CommandResult.Status,
			Output:  event.CommandResult.Output,
		}
		return projected, true, nil
	case engine.EventCompactBoundary:
		projected.Kind = string(engine.EventCompactBoundary)
		return projected, true, nil
	case engine.EventMaxTurnsReached:
		projected.Kind = string(engine.EventMaxTurnsReached)
		if event.MaxTurnsInfo != nil {
			projected.MaxTurns = &LifecycleMaxTurns{
				MaxTurns:  event.MaxTurnsInfo.MaxTurns,
				TurnCount: event.MaxTurnsInfo.TurnCount,
			}
		}
		return projected, true, nil
	case engine.EventUserInterruption:
		projected.Kind = string(engine.EventUserInterruption)
		projected.Interruption = &LifecycleInterruption{
			ToolUse: event.InterruptionToolUse,
		}
		return projected, true, nil
	default:
		return nil, false, nil
	}
}

func validLifecycleCanonicalUTF8(canonical *engine.CanonicalProjectionEvent) bool {
	if canonical == nil || !utf8.ValidString(string(canonical.Kind)) {
		return false
	}
	if canonical.Assistant != nil {
		return utf8.ValidString(canonical.Assistant.MessageID) &&
			utf8.Valid(canonical.Assistant.Delta)
	}
	if canonical.Tool == nil {
		return false
	}
	return utf8.ValidString(canonical.Tool.ToolCallID) &&
		utf8.ValidString(canonical.Tool.ToolName) &&
		utf8.Valid(canonical.Tool.EffectiveInput) &&
		utf8.ValidString(canonical.Tool.Content) &&
		utf8.ValidString(string(canonical.Tool.Outcome)) &&
		utf8.Valid(canonical.Tool.RawOutput)
}
