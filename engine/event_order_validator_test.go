package engine

import (
	"testing"
)

func TestEventOrderValidatorNormalTurn(t *testing.T) {
	v := NewEventOrderValidator()

	// Normal turn sequence
	events := []QueryEvent{
		{Type: EventStreamRequestStart},
		{Type: EventAssistant},
		{Type: EventToolResult},
		{Type: EventToolResult},
	}

	for _, evt := range events {
		if !v.Observe(evt) {
			t.Fatalf("expected valid event %s in normal sequence", evt.Type)
		}
	}

	if v.HasViolations() {
		t.Fatalf("expected no violations, got: %v", v.Violations())
	}

	if v.TurnCount() != 1 {
		t.Fatalf("expected turn count 1, got %d", v.TurnCount())
	}
}

func TestEventOrderValidatorMultiTurn(t *testing.T) {
	v := NewEventOrderValidator()

	// Turn 1
	v.Observe(QueryEvent{Type: EventStreamRequestStart})
	v.Observe(QueryEvent{Type: EventAssistant})
	v.Observe(QueryEvent{Type: EventToolResult})
	v.MarkTurnEnding()

	// Turn 2
	v.Observe(QueryEvent{Type: EventStreamRequestStart})
	v.Observe(QueryEvent{Type: EventAssistant})
	v.MarkTurnEnding()

	if v.TurnCount() != 2 {
		t.Fatalf("expected turn count 2, got %d", v.TurnCount())
	}
	if v.HasViolations() {
		t.Fatalf("expected no violations, got: %v", v.Violations())
	}
}

func TestEventOrderValidatorAssistantBeforeTurnStart(t *testing.T) {
	v := NewEventOrderValidator()

	// Assistant event without turn start is a violation
	valid := v.Observe(QueryEvent{Type: EventAssistant})
	if valid {
		t.Fatal("expected violation for assistant event in idle phase")
	}

	violations := v.Violations()
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(violations))
	}
	if violations[0].EventType != EventAssistant {
		t.Fatalf("expected violation event type assistant, got %s", violations[0].EventType)
	}
}

func TestEventOrderValidatorToolResultBeforeModel(t *testing.T) {
	v := NewEventOrderValidator()

	// Turn start without model call
	v.Observe(QueryEvent{Type: EventStreamRequestStart})
	valid := v.Observe(QueryEvent{Type: EventToolResult})
	if valid {
		t.Fatal("expected violation for tool_result before model call")
	}

	if !v.HasViolations() {
		t.Fatal("expected violations")
	}
}

func TestEventOrderValidatorAttachmentInIdle(t *testing.T) {
	v := NewEventOrderValidator()

	valid := v.Observe(QueryEvent{Type: EventAttachment})
	if valid {
		t.Fatal("expected violation for attachment in idle phase")
	}
}

func TestEventOrderValidatorAttachmentDuringTurn(t *testing.T) {
	v := NewEventOrderValidator()

	v.Observe(QueryEvent{Type: EventStreamRequestStart})
	v.Observe(QueryEvent{Type: EventAssistant})

	// Attachments during model_call phase are valid
	valid := v.Observe(QueryEvent{Type: EventAttachment})
	if !valid {
		t.Fatal("attachment during model call should be valid")
	}
}

func TestEventOrderValidatorMaxTurnsAfterTools(t *testing.T) {
	v := NewEventOrderValidator()

	v.Observe(QueryEvent{Type: EventStreamRequestStart})
	v.Observe(QueryEvent{Type: EventAssistant})
	v.Observe(QueryEvent{Type: EventToolResult})

	// Max turns after tool execution is valid
	valid := v.Observe(QueryEvent{Type: EventMaxTurnsReached})
	if !valid {
		t.Fatal("max_turns_reached after tool execution should be valid")
	}

	if v.Phase() != PhaseTurnEnding {
		t.Fatalf("expected phase turn_ending after max_turns, got %s", v.Phase())
	}
}

func TestEventOrderValidatorInterruptionMidTurn(t *testing.T) {
	v := NewEventOrderValidator()

	v.Observe(QueryEvent{Type: EventStreamRequestStart})
	v.Observe(QueryEvent{Type: EventAssistant})

	// Interruption mid-model-call is valid
	valid := v.Observe(QueryEvent{Type: EventUserInterruption})
	if !valid {
		t.Fatal("interruption during model call should be valid")
	}

	if v.Phase() != PhaseTurnEnding {
		t.Fatalf("expected phase turn_ending after interruption, got %s", v.Phase())
	}
}

func TestEventOrderValidatorInterruptionInIdle(t *testing.T) {
	v := NewEventOrderValidator()

	valid := v.Observe(QueryEvent{Type: EventUserInterruption})
	if valid {
		t.Fatal("interruption in idle phase should be a violation")
	}
}

func TestEventOrderValidatorCompactBoundaryNotInTerminal(t *testing.T) {
	v := NewEventOrderValidator()

	v.Observe(QueryEvent{Type: EventStreamRequestStart})
	v.Observe(QueryEvent{Type: EventAssistant})
	v.MarkTerminal()

	valid := v.Observe(QueryEvent{Type: EventCompactBoundary})
	if valid {
		t.Fatal("compact_boundary after terminal should be a violation")
	}
}

func TestEventOrderValidatorTerminalFromAnyPhase(t *testing.T) {
	// Terminal events can come from any non-idle phase (early termination)
	phases := []struct {
		name  string
		setup func(v *EventOrderValidator)
	}{
		{"idle", func(v *EventOrderValidator) {}},
		{"turn_started", func(v *EventOrderValidator) {
			v.Observe(QueryEvent{Type: EventStreamRequestStart})
		}},
		{"model_call", func(v *EventOrderValidator) {
			v.Observe(QueryEvent{Type: EventStreamRequestStart})
			v.Observe(QueryEvent{Type: EventAssistant})
		}},
		{"tool_execution", func(v *EventOrderValidator) {
			v.Observe(QueryEvent{Type: EventStreamRequestStart})
			v.Observe(QueryEvent{Type: EventAssistant})
			v.Observe(QueryEvent{Type: EventToolResult})
		}},
	}

	for _, tc := range phases {
		t.Run(tc.name, func(t *testing.T) {
			v := NewEventOrderValidator()
			tc.setup(v)

			// Terminal is always valid (error paths can terminate early)
			valid := v.Observe(QueryEvent{Type: EventTerminal})
			if !valid {
				t.Fatalf("terminal should be valid from phase %s", tc.name)
			}
			if v.Phase() != PhaseTerminal {
				t.Fatalf("expected terminal phase, got %s", v.Phase())
			}
		})
	}
}

func TestEventOrderValidatorReset(t *testing.T) {
	v := NewEventOrderValidator()

	v.Observe(QueryEvent{Type: EventStreamRequestStart})
	v.Observe(QueryEvent{Type: EventAssistant})
	v.Observe(QueryEvent{Type: EventAssistant}) // violation in idle would be, but here it's fine

	v.Reset()

	if v.Phase() != PhaseIdle {
		t.Fatalf("expected idle after reset, got %s", v.Phase())
	}
	if v.TurnCount() != 0 {
		t.Fatalf("expected turn count 0 after reset, got %d", v.TurnCount())
	}
	if v.HasViolations() {
		t.Fatal("expected no violations after reset")
	}
}

func TestEventOrderValidatorToolProgressDuringToolExecution(t *testing.T) {
	v := NewEventOrderValidator()

	v.Observe(QueryEvent{Type: EventStreamRequestStart})
	v.Observe(QueryEvent{Type: EventAssistant})
	v.Observe(QueryEvent{Type: EventToolResult})

	// Tool progress during tool execution is valid
	valid := v.Observe(QueryEvent{Type: EventToolProgress})
	if !valid {
		t.Fatal("tool_progress during tool execution should be valid")
	}
}

func TestEventOrderValidatorInformationalEventsAlwaysAllowed(t *testing.T) {
	v := NewEventOrderValidator()

	// Informational events like command_lifecycle should never cause violations
	informational := []QueryEventType{
		EventCommandLifecycle,
		EventTaskProgress,
		EventAgentLifecycle,
		EventTaskLifecycle,
		EventHookStatus,
	}

	for _, evtType := range informational {
		valid := v.Observe(QueryEvent{Type: evtType})
		if !valid {
			t.Fatalf("informational event %s should always be valid", evtType)
		}
	}

	if v.HasViolations() {
		t.Fatalf("expected no violations for informational events, got: %v", v.Violations())
	}
}

func TestEventOrderValidatorIntegrationWithQueryLoop(t *testing.T) {
	// Simulate a realistic 2-turn query loop event sequence
	v := NewEventOrderValidator()

	// Turn 1: model responds with tool call, tool executes
	events := []QueryEvent{
		{Type: EventStreamRequestStart},
		{Type: EventAssistant},  // model starts responding
		{Type: EventAssistant},  // streaming content
		{Type: EventToolResult}, // tool executed
		{Type: EventAttachment}, // attachment injected
	}
	for _, evt := range events {
		v.Observe(evt)
	}
	v.MarkTurnEnding()

	// Turn 2: model responds without tools (completes)
	events2 := []QueryEvent{
		{Type: EventStreamRequestStart},
		{Type: EventAssistant},
	}
	for _, evt := range events2 {
		v.Observe(evt)
	}
	v.MarkTerminal()

	if v.HasViolations() {
		t.Fatalf("expected no violations in realistic sequence, got: %v", v.Violations())
	}
	if v.TurnCount() != 2 {
		t.Fatalf("expected 2 turns, got %d", v.TurnCount())
	}
}
