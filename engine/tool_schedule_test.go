package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestP136bToolSchedulePreservesStableBatchesAndIdentity(t *testing.T) {
	calls := p136bToolCalls("SafeA", "SafeB", "Serial", "SafeC", "SafeD")
	schedule, err := newToolSchedule(calls, func(call *schema.ToolCall) bool {
		return strings.HasPrefix(call.Function.Name, "Safe")
	})
	if err != nil {
		t.Fatalf("new schedule: %v", err)
	}

	gotBatches := make([]int, len(schedule.Calls))
	for i, call := range schedule.Calls {
		gotBatches[i] = call.Batch
		if call.Ordinal != i {
			t.Fatalf("call %d ordinal = %d", i, call.Ordinal)
		}
		if call.ArgumentsDigest != toolArgumentsDigest(
			calls[i].Function.Arguments,
		) {
			t.Fatalf("call %d arguments digest changed", i)
		}
	}
	if got := fmt.Sprint(gotBatches); got != "[0 0 1 2 2]" {
		t.Fatalf("schedule batches = %s, want [0 0 1 2 2]", got)
	}
	if schedule.RoundID != toolScheduleRoundID(schedule.Calls) {
		t.Fatal("schedule round identity does not cover the frozen calls")
	}

	cloned := cloneToolSchedule(schedule)
	cloned.Calls[0].CallID = "mutated"
	if schedule.Calls[0].CallID == cloned.Calls[0].CallID {
		t.Fatal("cloned schedule aliases the original calls")
	}
}

func TestP136bToolScheduleValidationFailsClosed(t *testing.T) {
	if _, err := newToolSchedule(nil, nil); err == nil {
		t.Fatal("empty schedule should be rejected")
	}
	if _, err := newToolSchedule([]*schema.ToolCall{nil}, nil); err == nil {
		t.Fatal("nil call should be rejected")
	}

	missingID := p136bToolCalls("Read")[0]
	missingID.ID = ""
	if _, err := newToolSchedule([]*schema.ToolCall{missingID}, nil); err == nil {
		t.Fatal("missing call ID should be rejected")
	}
	nonCanonicalID := p136bToolCalls("Read")[0]
	nonCanonicalID.ID = " call-read "
	if _, err := newToolSchedule(
		[]*schema.ToolCall{nonCanonicalID},
		nil,
	); err == nil {
		t.Fatal("non-canonical call ID should be rejected")
	}
	missingName := p136bToolCalls("Read")[0]
	missingName.Function.Name = ""
	if _, err := newToolSchedule(
		[]*schema.ToolCall{missingName},
		nil,
	); err == nil {
		t.Fatal("missing tool name should be rejected")
	}
	nonCanonicalName := p136bToolCalls("Read")[0]
	nonCanonicalName.Function.Name = " Read "
	if _, err := newToolSchedule(
		[]*schema.ToolCall{nonCanonicalName},
		nil,
	); err == nil {
		t.Fatal("non-canonical tool name should be rejected")
	}
	duplicate := p136bToolCalls("Read", "Write")
	duplicate[1].ID = duplicate[0].ID
	if _, err := newToolSchedule(duplicate, nil); err == nil {
		t.Fatal("duplicate call ID should be rejected")
	}

	calls := p136bToolCalls("SafeA", "SafeB")
	schedule, err := newToolSchedule(calls, func(*schema.ToolCall) bool {
		panic("classifier bug")
	})
	if err != nil {
		t.Fatalf("panic classifier should fail closed: %v", err)
	}
	if schedule.Calls[0].ConcurrencySafe ||
		schedule.Calls[1].ConcurrencySafe ||
		schedule.Calls[0].Batch == schedule.Calls[1].Batch {
		t.Fatalf("panic classifier schedule = %#v, want serial calls", schedule.Calls)
	}

	invalidRoundID := cloneToolSchedule(schedule)
	invalidRoundID.RoundID = "not-a-digest"
	if err := validateToolSchedule(invalidRoundID); err == nil {
		t.Fatal("invalid round ID should be rejected")
	}
	mismatchedRoundID := cloneToolSchedule(schedule)
	mismatchedRoundID.Calls[0].ArgumentsDigest = toolArgumentsDigest("changed")
	if err := validateToolSchedule(mismatchedRoundID); err == nil {
		t.Fatal("schedule identity mismatch should be rejected")
	}
	invalidBatch := cloneToolSchedule(schedule)
	invalidBatch.Calls[1].Batch = invalidBatch.Calls[0].Batch
	invalidBatch.RoundID = toolScheduleRoundID(invalidBatch.Calls)
	if err := validateToolSchedule(invalidBatch); err == nil {
		t.Fatal("missing serial barrier should be rejected")
	}
}

func TestP136bAfterToolDecisionPreservesTypedControl(t *testing.T) {
	calls := p136bToolCalls("Read", "Write")
	schedule, err := newToolSchedule(calls, nil)
	if err != nil {
		t.Fatalf("new schedule: %v", err)
	}
	outcome := func(callID string, prevent bool) toolRoundOutcome {
		return toolRoundOutcome{
			CallID: callID,
			Outcome: &toolExecutionOutcome{
				Result: &schema.Message{
					Role:       schema.Tool,
					ToolCallID: callID,
					Content:    "result",
				},
				PreventContinuation: prevent,
			},
		}
	}
	normal := []toolRoundOutcome{
		outcome(calls[0].ID, false),
		outcome(calls[1].ID, false),
	}
	decision, err := decideAfterToolRound(schedule, normal)
	if err != nil || decision.Kind != afterToolContinue {
		t.Fatalf("continue decision = %#v, err = %v", decision, err)
	}

	returning := append([]toolRoundOutcome(nil), normal...)
	returning[0] = outcome(calls[0].ID, true)
	decision, err = decideAfterToolRound(schedule, returning)
	if err != nil ||
		decision.Kind != afterToolReturn ||
		decision.ReturnCallID != calls[0].ID ||
		decision.TerminalReason != TerminalHookStopped {
		t.Fatalf("return decision = %#v, err = %v", decision, err)
	}

	interrupted := append([]toolRoundOutcome(nil), returning...)
	interrupted[1] = toolRoundOutcome{
		CallID:      calls[1].ID,
		InterruptID: "approval-1",
	}
	decision, err = decideAfterToolRound(schedule, interrupted)
	if err != nil ||
		decision.Kind != afterToolInterrupt ||
		decision.InterruptID != "approval-1" {
		t.Fatalf("interrupt decision = %#v, err = %v", decision, err)
	}
}

func TestP136bAfterToolDecisionRejectsIncompleteOrMismatchedRound(t *testing.T) {
	calls := p136bToolCalls("Read", "Write")
	schedule, err := newToolSchedule(calls, nil)
	if err != nil {
		t.Fatalf("new schedule: %v", err)
	}
	outcome := func(callID string) toolRoundOutcome {
		return toolRoundOutcome{
			CallID: callID,
			Outcome: &toolExecutionOutcome{
				Result: &schema.Message{
					Role:       schema.Tool,
					ToolCallID: callID,
					Content:    "result",
				},
			},
		}
	}
	normal := []toolRoundOutcome{
		outcome(calls[0].ID),
		outcome(calls[1].ID),
	}
	invalidCases := [][]toolRoundOutcome{
		normal[:1],
		{normal[1], normal[0]},
		{
			normal[0],
			{CallID: calls[1].ID},
		},
		{
			normal[0],
			{
				CallID:      calls[1].ID,
				Outcome:     normal[1].Outcome,
				InterruptID: "both",
			},
		},
		{
			normal[0],
			{
				CallID:  calls[1].ID,
				Outcome: &toolExecutionOutcome{},
			},
		},
		{
			normal[0],
			{
				CallID: calls[1].ID,
				Outcome: &toolExecutionOutcome{
					Result: &schema.Message{
						Role:       schema.Tool,
						ToolCallID: calls[0].ID,
					},
				},
			},
		},
	}
	for i, invalid := range invalidCases {
		if _, err := decideAfterToolRound(schedule, invalid); err == nil {
			t.Fatalf("invalid decision case %d should fail", i)
		}
	}
}

func p136bToolCalls(names ...string) []*schema.ToolCall {
	calls := make([]*schema.ToolCall, 0, len(names))
	for _, name := range names {
		calls = append(calls, &schema.ToolCall{
			ID:   "call-" + strings.ToLower(name),
			Type: "function",
			Function: schema.FunctionCall{
				Name:      name,
				Arguments: `{"fixture":true}`,
			},
		})
	}
	return calls
}
