package engine

import (
	"fmt"
	"sync"
)

// EventOrderValidator enforces the event emission contract for the query loop.
// It validates that events within a single turn are emitted in the correct order:
//
//  1. Turn start (stream_request_start)
//  2. Model call phase (assistant messages, streaming content)
//  3. Tool execution phase (tool_result messages)
//  4. Turn end (state transition to next turn, max_turns, or terminal)
//
// The validator uses a state machine where each event type can only be emitted
// from certain states. Invalid transitions are recorded as violations but do
// NOT block emission — the validator is advisory, not blocking.
//
// This mirrors the implicit ordering guarantees in query.ts, making them
// explicit and testable.
type EventOrderValidator struct {
	mu         sync.Mutex
	state      TurnPhase
	violations []EventOrderViolation
	turnCount  int
}

// TurnPhase represents the current phase within a turn.
type TurnPhase string

const (
	// PhaseIdle is the initial state before any turn starts.
	PhaseIdle TurnPhase = "idle"

	// PhaseTurnStarted is after stream_request_start is emitted.
	PhaseTurnStarted TurnPhase = "turn_started"

	// PhaseModelCall is during model response streaming (assistant events).
	PhaseModelCall TurnPhase = "model_call"

	// PhaseToolExecution is during tool execution (tool_result events).
	PhaseToolExecution TurnPhase = "tool_execution"

	// PhaseTurnEnding is after tools complete, before the next turn or terminal.
	PhaseTurnEnding TurnPhase = "turn_ending"

	// PhaseTerminal is the final state after the loop ends.
	PhaseTerminal TurnPhase = "terminal"
)

// EventOrderViolation records a single ordering violation.
type EventOrderViolation struct {
	Turn      int
	Phase     TurnPhase
	EventType QueryEventType
	Message   string
}

// NewEventOrderValidator creates a validator in the idle state.
func NewEventOrderValidator() *EventOrderValidator {
	return &EventOrderValidator{
		state:     PhaseIdle,
		turnCount: 0,
	}
}

// Observe records an event and validates it against the expected ordering.
// Returns true if the event is valid for the current phase, false if it
// represents a violation (which is also recorded in Violations()).
func (v *EventOrderValidator) Observe(evt QueryEvent) bool {
	v.mu.Lock()
	defer v.mu.Unlock()

	valid := true
	switch evt.Type {
	case EventStreamRequestStart:
		// Valid from: idle, turn_ending (next turn starting)
		if v.state != PhaseIdle && v.state != PhaseTurnEnding {
			valid = false
			v.recordViolation(evt.Type, fmt.Sprintf(
				"stream_request_start in phase %s (expected idle or turn_ending)", v.state))
		}
		v.state = PhaseTurnStarted
		v.turnCount++

	case EventAssistant:
		// Valid from: turn_started, model_call (continued streaming)
		if v.state != PhaseTurnStarted && v.state != PhaseModelCall {
			valid = false
			v.recordViolation(evt.Type, fmt.Sprintf(
				"assistant event in phase %s (expected turn_started or model_call)", v.state))
		}
		v.state = PhaseModelCall

	case EventToolResult:
		// Valid from: model_call, tool_execution (multiple tools)
		if v.state != PhaseModelCall && v.state != PhaseToolExecution {
			valid = false
			v.recordViolation(evt.Type, fmt.Sprintf(
				"tool_result in phase %s (expected model_call or tool_execution)", v.state))
		}
		v.state = PhaseToolExecution

	case EventAttachment:
		// Attachments can appear in most phases (they carry metadata between steps).
		// Valid from: turn_started, model_call, tool_execution, turn_ending
		if v.state == PhaseIdle || v.state == PhaseTerminal {
			valid = false
			v.recordViolation(evt.Type, fmt.Sprintf(
				"attachment in phase %s (not expected in idle or terminal)", v.state))
		}

	case EventCompactBoundary:
		// Compact boundaries fire between turns (during turn preparation).
		// Valid from: turn_started (compaction happens during message preparation)
		// Also valid from turn_ending (between-turn compaction)
		if v.state == PhaseTerminal {
			valid = false
			v.recordViolation(evt.Type, fmt.Sprintf(
				"compact_boundary in phase %s (not expected after terminal)", v.state))
		}

	case EventMaxTurnsReached:
		// Valid from: tool_execution, turn_ending (fired after tools, before loop exit)
		if v.state != PhaseToolExecution && v.state != PhaseTurnEnding && v.state != PhaseModelCall {
			valid = false
			v.recordViolation(evt.Type, fmt.Sprintf(
				"max_turns_reached in phase %s (expected tool_execution or turn_ending)", v.state))
		}
		v.state = PhaseTurnEnding

	case EventUserInterruption:
		// Interruptions can happen at any non-idle phase.
		// After interruption, we transition to turn_ending for cleanup.
		if v.state == PhaseIdle {
			valid = false
			v.recordViolation(evt.Type, fmt.Sprintf(
				"user_interruption in phase %s (not expected before turn starts)", v.state))
		}
		v.state = PhaseTurnEnding

	case EventToolProgress:
		// Valid during tool execution
		if v.state != PhaseToolExecution && v.state != PhaseModelCall {
			valid = false
			v.recordViolation(evt.Type, fmt.Sprintf(
				"tool_progress in phase %s (expected model_call or tool_execution)", v.state))
		}

	case EventTerminal:
		// Terminal events can come from any phase (errors can terminate early).
		v.state = PhaseTerminal

	default:
		// Unknown or informational events (command_lifecycle, task_progress, etc.)
		// are always allowed — they don't affect turn ordering.
	}

	return valid
}

// MarkTurnEnding transitions to the turn_ending phase.
// Called after tool execution completes and before the next iteration begins.
func (v *EventOrderValidator) MarkTurnEnding() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.state = PhaseTurnEnding
}

// MarkTerminal transitions to the terminal state.
func (v *EventOrderValidator) MarkTerminal() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.state = PhaseTerminal
}

// Phase returns the current phase.
func (v *EventOrderValidator) Phase() TurnPhase {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.state
}

// TurnCount returns the number of turns observed (stream_request_start count).
func (v *EventOrderValidator) TurnCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.turnCount
}

// Violations returns all recorded violations.
func (v *EventOrderValidator) Violations() []EventOrderViolation {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]EventOrderViolation(nil), v.violations...)
}

// HasViolations returns true if any ordering violations were detected.
func (v *EventOrderValidator) HasViolations() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.violations) > 0
}

// Reset resets the validator to its initial state.
func (v *EventOrderValidator) Reset() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.state = PhaseIdle
	v.violations = nil
	v.turnCount = 0
}

// recordViolation adds a violation to the list (must be called with lock held).
func (v *EventOrderValidator) recordViolation(eventType QueryEventType, message string) {
	v.violations = append(v.violations, EventOrderViolation{
		Turn:      v.turnCount,
		Phase:     v.state,
		EventType: eventType,
		Message:   message,
	})
}
