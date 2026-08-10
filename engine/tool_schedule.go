package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

type toolScheduleCall struct {
	Ordinal         int    `json:"ordinal"`
	CallID          string `json:"call_id"`
	ToolName        string `json:"tool_name"`
	ArgumentsDigest string `json:"arguments_digest"`
	Batch           int    `json:"batch"`
	ConcurrencySafe bool   `json:"concurrency_safe"`
}

// toolSchedule freezes the model-ordered identity and stable execution batches
// for one committed tool-call round. It is invocation-local project state, not
// a durable checkpoint or framework adapter.
type toolSchedule struct {
	RoundID string
	Calls   []toolScheduleCall
}

func newToolSchedule(
	toolCalls []*schema.ToolCall,
	classifier func(*schema.ToolCall) bool,
) (toolSchedule, error) {
	if len(toolCalls) == 0 {
		return toolSchedule{}, errors.New("engine: tool schedule has no calls")
	}

	schedule := toolSchedule{
		Calls: make([]toolScheduleCall, 0, len(toolCalls)),
	}
	seen := make(map[string]struct{}, len(toolCalls))
	batch := -1
	previousSafe := false
	for ordinal, call := range toolCalls {
		if call == nil {
			return toolSchedule{}, fmt.Errorf(
				"engine: tool schedule call %d is nil",
				ordinal,
			)
		}
		callID := strings.TrimSpace(call.ID)
		if callID == "" {
			return toolSchedule{}, fmt.Errorf(
				"engine: tool schedule call %d has no ID",
				ordinal,
			)
		}
		if call.ID != callID {
			return toolSchedule{}, fmt.Errorf(
				"engine: tool schedule call %d has a non-canonical ID",
				ordinal,
			)
		}
		if _, ok := seen[callID]; ok {
			return toolSchedule{}, fmt.Errorf(
				"engine: duplicate tool schedule call ID %q",
				callID,
			)
		}
		seen[callID] = struct{}{}

		toolName := strings.TrimSpace(call.Function.Name)
		if toolName == "" {
			return toolSchedule{}, fmt.Errorf(
				"engine: tool schedule call %q has no tool name",
				callID,
			)
		}
		if call.Function.Name != toolName {
			return toolSchedule{}, fmt.Errorf(
				"engine: tool schedule call %q has a non-canonical tool name",
				callID,
			)
		}

		safe := classifyToolScheduleCall(classifier, call)
		if ordinal == 0 || !safe || !previousSafe {
			batch++
		}
		schedule.Calls = append(schedule.Calls, toolScheduleCall{
			Ordinal:         ordinal,
			CallID:          callID,
			ToolName:        toolName,
			ArgumentsDigest: toolArgumentsDigest(call.Function.Arguments),
			Batch:           batch,
			ConcurrencySafe: safe,
		})
		previousSafe = safe
	}
	schedule.RoundID = toolScheduleRoundID(schedule.Calls)
	if err := validateToolSchedule(schedule); err != nil {
		return toolSchedule{}, err
	}
	return cloneToolSchedule(schedule), nil
}

func classifyToolScheduleCall(
	classifier func(*schema.ToolCall) bool,
	call *schema.ToolCall,
) (safe bool) {
	if classifier == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			safe = false
		}
	}()
	return classifier(call)
}

func toolArgumentsDigest(arguments string) string {
	digest := sha256.Sum256([]byte(arguments))
	return hex.EncodeToString(digest[:])
}

func toolScheduleRoundID(calls []toolScheduleCall) string {
	encoded, _ := json.Marshal(calls)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func validateToolSchedule(schedule toolSchedule) error {
	if len(schedule.Calls) == 0 {
		return errors.New("engine: tool schedule has no calls")
	}
	if len(schedule.RoundID) != sha256.Size*2 {
		return errors.New("engine: tool schedule round ID is invalid")
	}
	if _, err := hex.DecodeString(schedule.RoundID); err != nil {
		return errors.New("engine: tool schedule round ID is invalid")
	}

	seenCalls := make(map[string]struct{}, len(schedule.Calls))
	for i, call := range schedule.Calls {
		if call.Ordinal != i {
			return fmt.Errorf(
				"engine: tool schedule ordinal %d is out of order",
				call.Ordinal,
			)
		}
		if strings.TrimSpace(call.CallID) == "" ||
			strings.TrimSpace(call.ToolName) == "" {
			return fmt.Errorf("engine: tool schedule call %d is incomplete", i)
		}
		if call.CallID != strings.TrimSpace(call.CallID) ||
			call.ToolName != strings.TrimSpace(call.ToolName) {
			return fmt.Errorf(
				"engine: tool schedule call %d has non-canonical identity",
				i,
			)
		}
		if _, ok := seenCalls[call.CallID]; ok {
			return fmt.Errorf(
				"engine: duplicate tool schedule call ID %q",
				call.CallID,
			)
		}
		seenCalls[call.CallID] = struct{}{}
		if len(call.ArgumentsDigest) != sha256.Size*2 {
			return fmt.Errorf(
				"engine: tool schedule call %q has an invalid arguments digest",
				call.CallID,
			)
		}
		if _, err := hex.DecodeString(call.ArgumentsDigest); err != nil {
			return fmt.Errorf(
				"engine: tool schedule call %q has an invalid arguments digest",
				call.CallID,
			)
		}

		if i == 0 {
			if call.Batch != 0 {
				return errors.New("engine: tool schedule must start at batch 0")
			}
			continue
		}
		previous := schedule.Calls[i-1]
		if call.ConcurrencySafe && previous.ConcurrencySafe {
			if call.Batch != previous.Batch {
				return fmt.Errorf(
					"engine: adjacent safe tool schedule call %q starts a new batch",
					call.CallID,
				)
			}
		} else if call.Batch != previous.Batch+1 {
			return fmt.Errorf(
				"engine: tool schedule call %q does not start a serial barrier",
				call.CallID,
			)
		}
	}
	if expected := toolScheduleRoundID(schedule.Calls); !strings.EqualFold(
		expected,
		schedule.RoundID,
	) {
		return errors.New("engine: tool schedule round ID mismatch")
	}
	return nil
}

func cloneToolSchedule(schedule toolSchedule) toolSchedule {
	schedule.Calls = append([]toolScheduleCall(nil), schedule.Calls...)
	return schedule
}

type afterToolDecisionKind string

const (
	afterToolContinue  afterToolDecisionKind = "continue"
	afterToolReturn    afterToolDecisionKind = "return"
	afterToolInterrupt afterToolDecisionKind = "interrupt"
)

type afterToolDecision struct {
	Kind           afterToolDecisionKind
	ReturnCallID   string
	InterruptID    string
	TerminalReason TerminalReason
}

type toolRoundOutcome struct {
	CallID      string
	Outcome     *toolExecutionOutcome
	InterruptID string
}

// decideAfterToolRound validates one complete model-ordered result set before
// selecting the next project Graph branch. Errors represent invalid runtime
// state; successful return and interrupt remain typed control decisions.
func decideAfterToolRound(
	schedule toolSchedule,
	outcomes []toolRoundOutcome,
) (afterToolDecision, error) {
	if err := validateToolSchedule(schedule); err != nil {
		return afterToolDecision{}, err
	}
	if len(outcomes) != len(schedule.Calls) {
		return afterToolDecision{}, errors.New(
			"engine: tool round outcomes are incomplete",
		)
	}
	seen := make(map[string]struct{}, len(outcomes))
	for i, planned := range schedule.Calls {
		outcome := outcomes[i]
		if outcome.CallID != planned.CallID {
			return afterToolDecision{}, fmt.Errorf(
				"engine: tool round outcome %d is out of model order",
				i,
			)
		}
		if _, ok := seen[outcome.CallID]; ok {
			return afterToolDecision{}, fmt.Errorf(
				"engine: duplicate tool round outcome %q",
				outcome.CallID,
			)
		}
		seen[outcome.CallID] = struct{}{}
		hasInterrupt := strings.TrimSpace(outcome.InterruptID) != ""
		if hasInterrupt == (outcome.Outcome != nil) {
			return afterToolDecision{}, fmt.Errorf(
				"engine: tool round outcome %q must contain exactly one result or interrupt",
				outcome.CallID,
			)
		}
		if outcome.Outcome != nil && outcome.Outcome.Result == nil {
			return afterToolDecision{}, fmt.Errorf(
				"engine: tool round outcome %q has no primary result",
				outcome.CallID,
			)
		}
		if outcome.Outcome != nil &&
			outcome.Outcome.Result.ToolCallID != outcome.CallID {
			return afterToolDecision{}, fmt.Errorf(
				"engine: tool round outcome %q has a mismatched primary result",
				outcome.CallID,
			)
		}
	}
	for _, outcome := range outcomes {
		if interruptID := strings.TrimSpace(outcome.InterruptID); interruptID != "" {
			return afterToolDecision{
				Kind:        afterToolInterrupt,
				InterruptID: interruptID,
			}, nil
		}
	}
	for _, outcome := range outcomes {
		if outcome.Outcome.PreventContinuation {
			return afterToolDecision{
				Kind:           afterToolReturn,
				ReturnCallID:   outcome.CallID,
				TerminalReason: TerminalHookStopped,
			}, nil
		}
	}
	return afterToolDecision{Kind: afterToolContinue}, nil
}
