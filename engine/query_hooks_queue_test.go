package engine

import (
	"context"
	"testing"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// hookPreventModel does one tool call, then returns text (trigger stop hooks).
type hookPreventModel struct {
	callCount int
}

func (m *hookPreventModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *hookPreventModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.callCount++
	if m.callCount == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_hook_prev",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path":"/tmp/hook_test"}`,
				},
			}},
		}}), nil
	}
	// Second call: no tool use, so stop hooks are checked
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "I'm done.",
	}}), nil
}

func TestQueryHookStopPreventsContinuation(t *testing.T) {
	ctx := context.Background()
	mdl := &hookPreventModel{}
	maxTurns := 5

	hookExec := hooks.NewExecutor()
	hookExec.RegisterStop(func(messagesForQuery, assistantMessages []*schema.Message, stopHookActive bool) *hooks.StopHookResult {
		return &hooks.StopHookResult{PreventContinuation: true}
	})

	_, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "do something"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		HookExecutor: hookExec,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "content", nil
		},
	})

	if terminal.Reason != TerminalStopHookPrevented {
		t.Fatalf("expected TerminalStopHookPrevented, got %q", terminal.Reason)
	}
}

func TestQueryHookStoppedFromToolOutcomePreventContinuation(t *testing.T) {
	ctx := context.Background()
	maxTurns := 5

	// Model always returns a tool call
	mdl := &singleToolCallModel{toolName: "Bash", args: `{"command":"echo"}`}

	hookExec := hooks.NewExecutor()
	hookExec.RegisterPostTool(func(ctx context.Context, toolName, toolUseID string, input map[string]any, result string) *hooks.PostToolHookResult {
		return &hooks.PostToolHookResult{PreventContinuation: true}
	})

	_, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "run"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		HookExecutor: hookExec,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "ok", nil
		},
	})

	if terminal.Reason != TerminalHookStopped {
		t.Fatalf("expected TerminalHookStopped, got %q", terminal.Reason)
	}
}

func TestQueryQueueDrainInjectsCommandsBetweenTurns(t *testing.T) {
	ctx := context.Background()
	mdl := &refreshTrackingModel{totalCalls: 1}
	maxTurns := 5

	// Set up a queue with a command ready
	coordinator := newTestRuntimeInputCoordinator(t, "hook-queue", "")
	mustEnqueueRuntimePrompt(
		t, coordinator, "cmd-queue-1", RuntimePriorityNext, "", "follow up instruction",
	)

	var attachmentEvents []*schema.Message
	var lifecycleEvents []CommandLifecycleEvent

	Query(ctx, QueryParams{
		Messages:         []*schema.Message{{Role: schema.User, Content: "read"}},
		SystemPrompt:     &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:      QuerySourceSDK,
		MaxTurns:         &maxTurns,
		ChatModel:        mdl,
		SessionID:        "hook-queue",
		InputCoordinator: coordinator,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "ok", nil
		},
	}, func(evt QueryEvent) {
		if evt.Type == EventAttachment && evt.AttachmentMessage != nil {
			attachmentEvents = append(attachmentEvents, evt.AttachmentMessage)
		}
		if evt.Type == EventCommandLifecycle && evt.CommandLifecycle != nil {
			lifecycleEvents = append(lifecycleEvents, *evt.CommandLifecycle)
		}
	})

	// Should have received at least one attachment from the queued command
	if len(attachmentEvents) == 0 {
		t.Fatal("expected queue-drained command to appear as attachment event")
	}

	// Should have lifecycle started event
	hasStarted := false
	for _, lc := range lifecycleEvents {
		if lc.CommandUUID == "cmd-queue-1" && lc.Phase == CommandLifecycleStarted {
			hasStarted = true
		}
	}
	if !hasStarted {
		t.Error("expected CommandLifecycle started event for queued command")
	}
}

func TestQueryQueueDrainMainThreadOnly(t *testing.T) {
	ctx := context.Background()
	mdl := &refreshTrackingModel{totalCalls: 1}
	maxTurns := 5

	coordinator := newTestRuntimeInputCoordinator(t, "hook-agent", "")
	mustEnqueueRuntimePrompt(
		t, coordinator, "cmd-agent-skip", RuntimePriorityNext, "", "should not drain for agent",
	)

	var attachmentCount int
	// Run as a sub-agent (AgentID set) — should NOT drain the queue
	Query(ctx, QueryParams{
		Messages:         []*schema.Message{{Role: schema.User, Content: "read"}},
		SystemPrompt:     &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:      QuerySourceAgent,
		MaxTurns:         &maxTurns,
		ChatModel:        mdl,
		SessionID:        "hook-agent",
		InputCoordinator: coordinator,
		ToolUseContext: &ToolUseContext{
			AgentID: "sub-agent-1",
		},
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "ok", nil
		},
	}, func(evt QueryEvent) {
		if evt.Type == EventAttachment {
			attachmentCount++
		}
	})

	// Sub-agent should NOT drain queue — command should still be there
	if attachmentCount > 0 {
		t.Errorf("expected no main-scope runtime input in sub-agent context, got %d attachments", attachmentCount)
	}
	remaining := coordinator.Snapshot(RuntimeInputScope{SessionID: "hook-agent"})
	if len(remaining) == 0 {
		t.Error("expected command to remain in queue (not drained by sub-agent)")
	}
}

// twoToolCallModel returns 2 sequential tool-call turns, then completes.
type twoToolCallModel struct {
	callCount int
}

func (m *twoToolCallModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *twoToolCallModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.callCount++
	if m.callCount <= 2 {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_turn_" + string(rune('0'+m.callCount)),
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path":"/tmp/test"}`,
				},
			}},
		}}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "done",
	}}), nil
}

func TestQueryTurnCountIncrementsProperly(t *testing.T) {
	ctx := context.Background()
	mdl := &twoToolCallModel{}
	maxTurns := 2 // should hit max turns after 2 tool-use turns

	var maxTurnsEvent *MaxTurnsInfo
	_, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "read files"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "content", nil
		},
	})

	// After 2 tool-use turns, max_turns should fire
	if terminal.Reason != TerminalMaxTurns {
		t.Fatalf("expected TerminalMaxTurns, got %q", terminal.Reason)
	}
	if terminal.TurnCount != 3 {
		t.Errorf("expected TurnCount=3 (turn exceeded maxTurns=2), got %d", terminal.TurnCount)
	}
	_ = maxTurnsEvent
}

func TestQueryStreamRequestStartEmittedEveryTurn(t *testing.T) {
	ctx := context.Background()
	mdl := &twoToolCallModel{}
	maxTurns := 4

	var startEvents int
	Query(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "read"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "ok", nil
		},
	}, func(evt QueryEvent) {
		if evt.Type == EventStreamRequestStart {
			startEvents++
		}
	})

	// Model does 2 tool-use turns + 1 final text turn = 3 model calls = 3 stream_request_start
	if startEvents != 3 {
		t.Errorf("expected 3 stream_request_start events (one per model call), got %d", startEvents)
	}
}
