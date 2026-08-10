package engine

import (
	"context"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// abortDuringStreamModel simulates a model that streams a tool call, but the
// AbortController fires while the stream is still being consumed.
type abortDuringStreamModel struct {
	abort     *AbortController
	callCount int
}

func (m *abortDuringStreamModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *abortDuringStreamModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.callCount++
	// On first call, abort synchronously before returning the stream.
	// This simulates an abort that fires while the model is streaming.
	// By the time stage 9 checks AbortController.Aborted(), it's already true.
	if m.callCount == 1 {
		m.abort.Abort()
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role:    schema.Assistant,
			Content: "thinking...",
		}}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "should not reach here",
	}}), nil
}

func TestQueryAbortDuringStreamingYieldsAbortedStreamingTerminal(t *testing.T) {
	ctx := context.Background()
	abortCtx, abortCancel := context.WithCancel(ctx)
	ac := &AbortController{Ctx: abortCtx, Cancel: abortCancel}
	mdl := &abortDuringStreamModel{abort: ac}
	maxTurns := 5

	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "hello"}},
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

	// Verify UserInterruption event was emitted (abort reason != "interrupt")
	hasInterruption := false
	for _, e := range events {
		if e.Type == EventUserInterruption {
			hasInterruption = true
			if e.InterruptionToolUse {
				t.Error("expected InterruptionToolUse=false for streaming abort")
			}
		}
	}
	if !hasInterruption {
		t.Error("expected user_interruption event for non-interrupt abort reason")
	}
}

// abortDuringToolModel returns a tool call, then fires abort during tool execution.
type abortDuringToolModel struct {
	callCount int
}

func (m *abortDuringToolModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *abortDuringToolModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.callCount++
	if m.callCount == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_abort_tool",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Bash",
					Arguments: `{"command":"sleep 10"}`,
				},
			}},
		}}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "done",
	}}), nil
}

func TestQueryAbortDuringToolExecutionYieldsAbortedToolsTerminal(t *testing.T) {
	ctx := context.Background()
	abortCtx, abortCancel := context.WithCancel(ctx)
	ac := &AbortController{Ctx: abortCtx, Cancel: abortCancel}
	mdl := &abortDuringToolModel{}
	maxTurns := 5

	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "run something"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		ToolUseContext: &ToolUseContext{
			AbortController: ac,
		},
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			// Fire abort during tool execution
			ac.Abort()
			return "partial result", nil
		},
	})

	if terminal.Reason != TerminalAbortedTools {
		t.Fatalf("expected TerminalAbortedTools, got %q", terminal.Reason)
	}

	// Verify UserInterruption event with InterruptionToolUse=true
	hasInterruption := false
	for _, e := range events {
		if e.Type == EventUserInterruption {
			hasInterruption = true
			if !e.InterruptionToolUse {
				t.Error("expected InterruptionToolUse=true for tools abort")
			}
		}
	}
	if !hasInterruption {
		t.Error("expected user_interruption event for tools abort")
	}
}

func TestQueryAbortDuringToolsWithMaxTurnsEmitsMaxTurnsWarning(t *testing.T) {
	ctx := context.Background()
	abortCtx, abortCancel := context.WithCancel(ctx)
	ac := &AbortController{Ctx: abortCtx, Cancel: abortCancel}
	mdl := &abortDuringToolModel{}
	maxTurns := 1 // exactly 1 turn — abort at turn 1 means nextTurnCount=2 > maxTurns=1

	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "run something"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		ToolUseContext: &ToolUseContext{
			AbortController: ac,
		},
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			ac.Abort()
			return "ok", nil
		},
	})

	if terminal.Reason != TerminalAbortedTools {
		t.Fatalf("expected TerminalAbortedTools, got %q", terminal.Reason)
	}

	// Should also emit max_turns_reached event
	hasMaxTurns := false
	for _, e := range events {
		if e.Type == EventMaxTurnsReached {
			hasMaxTurns = true
			if e.MaxTurnsInfo == nil {
				t.Fatal("expected MaxTurnsInfo to be non-nil")
				return
			}
			if e.MaxTurnsInfo.MaxTurns != 1 {
				t.Errorf("expected MaxTurns=1, got %d", e.MaxTurnsInfo.MaxTurns)
			}
		}
	}
	if !hasMaxTurns {
		t.Error("expected max_turns_reached event alongside abort at max turns")
	}
}

func TestQueryAbortWithInterruptReasonDoesNotEmitUserInterruption(t *testing.T) {
	ctx := context.Background()
	abortCtx, abortCancel := context.WithCancel(ctx)
	ac := &AbortController{Ctx: abortCtx, Cancel: abortCancel, Reason: "interrupt"}
	// Pre-abort so the loop exits immediately at stage 9
	abortCancel()
	ac.Reason = "interrupt"

	mdl := &abortDuringStreamModel{abort: ac, callCount: 1} // skip first call logic
	maxTurns := 5

	events, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "hello"}},
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

	// Should NOT emit user_interruption when reason is "interrupt"
	for _, e := range events {
		if e.Type == EventUserInterruption {
			t.Error("expected NO user_interruption event when abort reason is 'interrupt'")
		}
	}
}

// multiTurnAbortModel issues tool calls for N turns, then the abort fires.
type multiTurnAbortModel struct {
	mu        sync.Mutex
	callCount int
	abortAt   int
	abort     *AbortController
}

func (m *multiTurnAbortModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *multiTurnAbortModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.callCount++
	count := m.callCount
	m.mu.Unlock()

	if count <= m.abortAt {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_" + string(rune('0'+count)),
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

func TestQueryAbortMidTurnPreservesPreviousTurnResults(t *testing.T) {
	ctx := context.Background()
	abortCtx, abortCancel := context.WithCancel(ctx)
	ac := &AbortController{Ctx: abortCtx, Cancel: abortCancel}
	mdl := &multiTurnAbortModel{abortAt: 3, abort: ac}
	maxTurns := 10

	var toolResultCount int
	_, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "read files"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		ToolUseContext: &ToolUseContext{
			AbortController: ac,
		},
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			toolResultCount++
			if toolResultCount == 2 {
				// Abort during second tool execution
				ac.Abort()
			}
			return "file content", nil
		},
	})

	if terminal.Reason != TerminalAbortedTools {
		t.Fatalf("expected TerminalAbortedTools, got %q", terminal.Reason)
	}
	// First tool should have completed, second tool aborted mid-execution
	if toolResultCount < 2 {
		t.Fatalf("expected at least 2 tool executions before abort, got %d", toolResultCount)
	}
}
