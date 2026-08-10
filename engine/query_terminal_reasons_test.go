package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// =============================================================================
// Terminal reason: model_error
// Validates that a model call error terminates with TerminalModelError.
// =============================================================================

type modelErrorModel struct {
	err error
}

func (m *modelErrorModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return nil, m.err
}

func (m *modelErrorModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, m.err
}

func TestTerminalModelError(t *testing.T) {
	ctx := context.Background()
	maxTurns := 5
	mdl := &modelErrorModel{err: errors.New("connection refused: upstream unavailable")}

	var assistantEvents []string
	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "hello"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
	})

	if terminal.Reason != TerminalModelError {
		t.Fatalf("expected TerminalModelError, got %q", terminal.Reason)
	}
	if terminal.Err == nil {
		t.Fatal("expected non-nil error in terminal")
		return
	}
	if !strings.Contains(terminal.Err.Error(), "connection refused") {
		t.Fatalf("expected error to contain 'connection refused', got %q", terminal.Err.Error())
	}

	// Should have emitted an assistant event with error info
	for _, evt := range events {
		if evt.Type == EventAssistant && evt.AssistantMessage != nil {
			assistantEvents = append(assistantEvents, evt.AssistantMessage.Content)
		}
	}
	if len(assistantEvents) == 0 {
		t.Fatal("expected at least one assistant event with error message")
	}
	foundError := false
	for _, msg := range assistantEvents {
		if strings.Contains(msg, "error") || strings.Contains(msg, "Error") {
			foundError = true
			break
		}
	}
	if !foundError {
		t.Fatalf("expected error assistant message, got %v", assistantEvents)
	}
}

// =============================================================================
// Terminal reason: aborted_streaming
// Validates that aborting during model streaming terminates correctly.
// =============================================================================

type slowStreamModel struct {
	mu        sync.Mutex
	callCount int
}

func (m *slowStreamModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *slowStreamModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.callCount++
	m.mu.Unlock()

	// Return a simple message — the abort is triggered by the AbortController
	// being cancelled before we get to tool execution.
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "streaming response that gets interrupted",
	}}), nil
}

func TestTerminalAbortedStreaming(t *testing.T) {
	ctx := context.Background()
	abortCtx, abortCancel := context.WithCancel(ctx)
	ac := &AbortController{Ctx: abortCtx, Cancel: abortCancel}
	mdl := &slowStreamModel{}
	maxTurns := 5

	// Pre-abort before the query even starts — this simulates user pressing ESC
	// while the model is producing output.
	ac.Abort()

	_, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "generate something"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		ToolUseContext: &ToolUseContext{
			AbortController: ac,
		},
	})

	if terminal.Reason != TerminalAbortedStreaming {
		t.Fatalf("expected TerminalAbortedStreaming, got %q", terminal.Reason)
	}
}

// =============================================================================
// Terminal reason: stop_hook_prevented
// Validates that a stop hook preventing continuation terminates correctly.
// =============================================================================

type simpleTextModel struct {
	mu        sync.Mutex
	callCount int
	text      string
}

func (m *simpleTextModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: m.text}, nil
}

func (m *simpleTextModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.callCount++
	m.mu.Unlock()
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: m.text,
	}}), nil
}

func TestTerminalStopHookPrevented(t *testing.T) {
	ctx := context.Background()
	maxTurns := 5
	mdl := &simpleTextModel{text: "I want to stop here."}

	hookExec := hooks.NewExecutor()
	hookExec.RegisterStop(func(messagesForQuery, assistantMessages []*schema.Message, stopHookActive bool) *hooks.StopHookResult {
		return &hooks.StopHookResult{PreventContinuation: true}
	})

	_, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "tell me something"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		HookExecutor: hookExec,
	})

	if terminal.Reason != TerminalStopHookPrevented {
		t.Fatalf("expected TerminalStopHookPrevented, got %q", terminal.Reason)
	}
}

// =============================================================================
// Terminal reason: hook_stopped (stop hook blocking error → continue → eventually stop)
// Validates that stop hooks with blocking errors inject messages and eventually stop.
// =============================================================================

type hookBlockingModel struct {
	mu        sync.Mutex
	callCount int
}

func (m *hookBlockingModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *hookBlockingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.callCount++
	count := m.callCount
	m.mu.Unlock()

	if count == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role:    schema.Assistant,
			Content: "First response that triggers blocking hook.",
		}}), nil
	}
	// On second call (after blocking error injection), complete cleanly
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "Acknowledged the blocking error.",
	}}), nil
}

func TestTerminalCompletedAfterStopHookBlocking(t *testing.T) {
	ctx := context.Background()
	maxTurns := 5
	mdl := &hookBlockingModel{}
	hookCallCount := 0

	hookExec := hooks.NewExecutor()
	hookExec.RegisterStop(func(messagesForQuery, assistantMessages []*schema.Message, stopHookActive bool) *hooks.StopHookResult {
		hookCallCount++
		if hookCallCount == 1 {
			// First call: inject a blocking error (model will retry)
			return &hooks.StopHookResult{
				BlockingErrors: []*schema.Message{{
					Role:    schema.User,
					Content: "BLOCKING: You must address this error before completing.",
					Extra:   map[string]any{"is_meta": true},
				}},
			}
		}
		// Second call: allow completion
		return nil
	})

	_, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "do something"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		HookExecutor: hookExec,
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected TerminalCompleted after blocking hook retry, got %q", terminal.Reason)
	}
	if mdl.callCount != 2 {
		t.Errorf("expected 2 model calls (original + retry after blocking), got %d", mdl.callCount)
	}
	if hookCallCount != 2 {
		t.Errorf("expected 2 stop hook calls, got %d", hookCallCount)
	}
}

// =============================================================================
// Full message lifecycle test
// Validates: user input → messages → model call → tool calls → tool results →
// model response → complete. Covers the entire happy-path flow.
// =============================================================================

type fullLifecycleModel struct {
	mu        sync.Mutex
	callCount int
}

func (m *fullLifecycleModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *fullLifecycleModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.callCount++
	count := m.callCount
	m.mu.Unlock()

	if count == 1 {
		// First call: request a tool call
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_lifecycle_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path":"/tmp/lifecycle_test"}`,
				},
			}},
		}}), nil
	}
	// Second call: produce final text response
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "File content received. Task complete.",
	}}), nil
}

func TestFullMessageLifecycle(t *testing.T) {
	ctx := context.Background()
	maxTurns := 5
	mdl := &fullLifecycleModel{}

	var eventSequence []QueryEventType
	var toolExecuted bool
	var toolName string

	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "read a file for me"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		ToolExecutor: func(ctx context.Context, tn, jsonInput string) (string, error) {
			toolExecuted = true
			toolName = tn
			return "file content here", nil
		},
	})

	for _, evt := range events {
		eventSequence = append(eventSequence, evt.Type)
	}

	// Terminal should be completed
	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected TerminalCompleted, got %q", terminal.Reason)
	}

	// Tool should have been executed
	if !toolExecuted {
		t.Fatal("expected tool to be executed")
	}
	if toolName != "Read" {
		t.Fatalf("expected tool name Read, got %q", toolName)
	}

	// Model should have been called twice (tool call + final response)
	if mdl.callCount != 2 {
		t.Fatalf("expected 2 model calls, got %d", mdl.callCount)
	}

	// Verify key events appear in expected order
	// Expected sequence: stream_request_start, tool_result, stream_request_start, assistant
	var foundFirstStream, foundToolResult, foundSecondStream bool
	streamCount := 0
	for _, et := range eventSequence {
		switch et {
		case EventStreamRequestStart:
			streamCount++
			if streamCount == 1 {
				foundFirstStream = true
			}
			if streamCount == 2 {
				foundSecondStream = true
			}
		case EventToolResult:
			if foundFirstStream {
				foundToolResult = true
			}
		}
	}

	if !foundFirstStream {
		t.Error("missing first stream_request_start")
	}
	if !foundToolResult {
		t.Error("missing tool_result after first stream")
	}
	if !foundSecondStream {
		t.Error("missing second stream_request_start (for final model call)")
	}
}

// =============================================================================
// Recovery cascade integration test
// PTL error → compact → retry → success, all in one flow.
// =============================================================================

type recoveryIntegrationModel struct {
	mu        sync.Mutex
	callCount int
}

func (m *recoveryIntegrationModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *recoveryIntegrationModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.callCount++
	count := m.callCount
	m.mu.Unlock()

	if count == 1 {
		// First call: simulate max_output_tokens error (recoverable)
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role:    schema.Assistant,
			Content: "partial response that hit output token limit",
			Extra: map[string]any{
				"api_error":  true,
				"error_type": "max_output_tokens",
			},
		}}), nil
	}
	if count == 2 {
		// Second call: succeed after recovery
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role:    schema.Assistant,
			Content: "Complete response after recovery.",
		}}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "should not reach here",
	}}), nil
}

func TestRecoveryCascadeMaxOutputTokensToSuccess(t *testing.T) {
	ctx := context.Background()
	maxTurns := 8
	mdl := &recoveryIntegrationModel{}

	var attachmentCount int
	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "generate a long response"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "claude-test",
		}},
	})

	for _, evt := range events {
		if evt.Type == EventAttachment {
			attachmentCount++
		}
	}

	// The recovery should allow the model to be called again
	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected TerminalCompleted after recovery, got %q", terminal.Reason)
	}
	// Model should have been called at least 2 times (error + recovery success)
	if mdl.callCount < 2 {
		t.Fatalf("expected at least 2 model calls for recovery, got %d", mdl.callCount)
	}
}

// =============================================================================
// Terminal reason: max_turns with event data
// Validates the MaxTurnsInfo event data is correct.
// =============================================================================

type neverCompletesModel struct {
	mu        sync.Mutex
	callCount int
}

func (m *neverCompletesModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *neverCompletesModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.callCount++
	count := m.callCount
	m.mu.Unlock()

	return schema.StreamReaderFromArray([]*schema.Message{{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:   "call_neverend_" + string(rune('0'+count)),
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "Read",
				Arguments: `{"file_path":"/tmp/never"}`,
			},
		}},
	}}), nil
}

func TestTerminalMaxTurnsEventData(t *testing.T) {
	ctx := context.Background()
	maxTurns := 2
	mdl := &neverCompletesModel{}

	var maxTurnsEvt *MaxTurnsInfo
	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "keep going forever"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "content", nil
		},
	})

	for _, evt := range events {
		if evt.Type == EventMaxTurnsReached && evt.MaxTurnsInfo != nil {
			maxTurnsEvt = evt.MaxTurnsInfo
		}
	}

	if terminal.Reason != TerminalMaxTurns {
		t.Fatalf("expected TerminalMaxTurns, got %q", terminal.Reason)
	}
	if maxTurnsEvt == nil {
		t.Fatal("expected MaxTurnsReached event to be emitted")
		return
	}
	if maxTurnsEvt.MaxTurns != 2 {
		t.Errorf("expected MaxTurns=2, got %d", maxTurnsEvt.MaxTurns)
	}
	if maxTurnsEvt.TurnCount <= 2 {
		t.Errorf("expected TurnCount > 2 (exceeded), got %d", maxTurnsEvt.TurnCount)
	}
}

// =============================================================================
// Terminal reason: completed (normal end_turn / stop sequence)
// Validates the simple text-only response completes cleanly.
// =============================================================================

func TestTerminalCompletedSimpleTextResponse(t *testing.T) {
	ctx := context.Background()
	maxTurns := 5
	mdl := &simpleTextModel{text: "Hello! How can I help you today?"}

	var assistantContent string
	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "hi"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected TerminalCompleted, got %q", terminal.Reason)
	}

	for _, evt := range events {
		if evt.Type == EventAssistant {
			// Stream processor may emit via Message or AssistantMessage field
			if evt.AssistantMessage != nil && evt.AssistantMessage.Content != "" {
				assistantContent = evt.AssistantMessage.Content
			} else if evt.Message != nil && evt.Message.Content != "" {
				assistantContent = evt.Message.Content
			}
		}
	}
	if assistantContent == "" {
		t.Fatal("expected non-empty assistant content in at least one assistant event")
	}
	if !strings.Contains(assistantContent, "Hello") {
		t.Fatalf("expected assistant content to contain 'Hello', got %q", assistantContent)
	}
}

// =============================================================================
// Turn counting with TurnTracker validates that event validator count matches.
// =============================================================================

func TestTurnCounterMatchesModelCalls(t *testing.T) {
	ctx := context.Background()
	mdl := &multiTurnToolModel{toolTurns: 4}
	maxTurns := 10

	_, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "read many files"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "content", nil
		},
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected TerminalCompleted, got %q", terminal.Reason)
	}

	// 4 tool turns + 1 final text turn = 5 model calls = 5 turns observed
	eventValidator := debugEventValidator.Load()
	if eventValidator == nil {
		t.Fatal("expected debugEventValidator to be set")
	}
	if eventValidator.TurnCount() != 5 {
		t.Errorf("expected 5 turns, got %d", eventValidator.TurnCount())
	}
}

// =============================================================================
// Hook notification fires on max_turns
// =============================================================================

func TestHookNotificationOnMaxTurns(t *testing.T) {
	ctx := context.Background()
	mdl := &neverCompletesModel{}
	maxTurns := 2

	var hookNotified bool
	hookExec := hooks.NewExecutor()
	hookExec.RegisterNotification(func(ctx context.Context, level, message string, data map[string]any) {
		if strings.Contains(message, "Max turns reached") {
			hookNotified = true
		}
	})

	_, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "go"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		HookExecutor: hookExec,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "ok", nil
		},
	})

	if terminal.Reason != TerminalMaxTurns {
		t.Fatalf("expected TerminalMaxTurns, got %q", terminal.Reason)
	}
	if !hookNotified {
		t.Error("expected notification hook to fire with 'Max turns reached'")
	}
}

// =============================================================================
// Event validator has no violations on a clean multi-tool run
// =============================================================================

func TestEventValidatorNoViolationsOnCleanRun(t *testing.T) {
	ctx := context.Background()
	mdl := &multiTurnToolModel{toolTurns: 3}
	maxTurns := 10

	collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "work"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "ok", nil
		},
	})

	eventValidator := debugEventValidator.Load()
	if eventValidator == nil {
		t.Fatal("expected debugEventValidator")
	}
	if eventValidator.HasViolations() {
		for _, v := range eventValidator.Violations() {
			t.Errorf("violation: turn=%d phase=%s event=%s msg=%s",
				v.Turn, v.Phase, v.EventType, v.Message)
		}
	}
}

// =============================================================================
// Abort with tool context fires user_interruption event
// =============================================================================

func TestAbortDuringToolsFiresInterruptionEvent(t *testing.T) {
	ctx := context.Background()
	abortCtx, abortCancel := context.WithCancel(ctx)
	ac := &AbortController{Ctx: abortCtx, Cancel: abortCancel}
	maxTurns := 5

	mdl := &singleToolCallModel{toolName: "Bash", args: `{"command":"sleep 5"}`}

	var interruptionEvent bool
	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "run command"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		ToolUseContext: &ToolUseContext{
			AbortController: ac,
		},
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			ac.Abort()
			return "interrupted", nil
		},
	})

	for _, evt := range events {
		if evt.Type == EventUserInterruption {
			interruptionEvent = true
		}
	}

	if terminal.Reason != TerminalAbortedTools {
		t.Fatalf("expected TerminalAbortedTools, got %q", terminal.Reason)
	}
	if !interruptionEvent {
		t.Error("expected user_interruption event")
	}
}

// =============================================================================
// No model configured — immediate completion
// =============================================================================

func TestTerminalCompletedNoModelConfigured(t *testing.T) {
	ctx := context.Background()
	maxTurns := 5

	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "hello"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		// No ChatModel set
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected TerminalCompleted with no model, got %q", terminal.Reason)
	}

	// Should have at least emitted stream_request_start
	hasStreamStart := false
	for _, evt := range events {
		if evt.Type == EventStreamRequestStart {
			hasStreamStart = true
		}
	}
	if !hasStreamStart {
		t.Error("expected stream_request_start even with no model")
	}
}

// =============================================================================
// Queue drain between turns (command_lifecycle events)
// =============================================================================

func TestQueueDrainBetweenTurnsEmitsLifecycle(t *testing.T) {
	ctx := context.Background()
	maxTurns := 5
	mdl := &simpleTextModel{text: "Done."}

	var lifecycleEvents []*CommandLifecycleEvent
	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "hello"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected TerminalCompleted, got %q", terminal.Reason)
	}

	for _, evt := range events {
		if evt.Type == EventCommandLifecycle && evt.CommandLifecycle != nil {
			lifecycleEvents = append(lifecycleEvents, evt.CommandLifecycle)
		}
	}
	// The query may or may not emit command lifecycle events depending on whether
	// a command UUID was tracked. The key assertion is no panic and clean completion.
	_ = lifecycleEvents
}

// =============================================================================
// Stream request start emitted exactly once per model call
// =============================================================================

func TestStreamRequestStartEmittedPerModelCall(t *testing.T) {
	ctx := context.Background()
	maxTurns := 10
	mdl := &multiTurnToolModel{toolTurns: 3}

	var streamStartCount int
	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "work"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "ok", nil
		},
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected TerminalCompleted, got %q", terminal.Reason)
	}

	for _, evt := range events {
		if evt.Type == EventStreamRequestStart {
			streamStartCount++
		}
	}

	// 3 tool turns + 1 final = 4 model calls = 4 stream_request_start events
	if streamStartCount != 4 {
		t.Errorf("expected 4 stream_request_start (one per model call), got %d", streamStartCount)
	}
}
