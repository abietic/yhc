package engine

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// refreshTrackingModel issues tool calls for multiple turns to verify tool refresh.
type refreshTrackingModel struct {
	callCount  int
	totalCalls int
}

func (m *refreshTrackingModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *refreshTrackingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.callCount++
	if m.callCount <= m.totalCalls {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_refresh_" + string(rune('A'+m.callCount-1)),
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path":"/tmp/test.txt"}`,
				},
			}},
		}}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "done",
	}}), nil
}

func TestQueryToolRefreshBetweenTurns(t *testing.T) {
	ctx := context.Background()
	mdl := &refreshTrackingModel{totalCalls: 2}
	maxTurns := 5

	var refreshCount atomic.Int32
	refreshedTools := []*schema.ToolInfo{
		{Name: "Read", Desc: "read a file"},
		{Name: "NewTool", Desc: "a new tool added mid-session"},
	}

	_, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "read files"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		ToolUseContext: &ToolUseContext{
			Options: &ToolUseOptions{
				Tools: []*schema.ToolInfo{{Name: "Read", Desc: "read a file"}},
				RefreshTools: func() []*schema.ToolInfo {
					refreshCount.Add(1)
					return refreshedTools
				},
			},
		},
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "content", nil
		},
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected TerminalCompleted, got %q", terminal.Reason)
	}

	// RefreshTools should be called between turns (after each tool use turn)
	got := refreshCount.Load()
	if got < 1 {
		t.Fatalf("expected RefreshTools to be called at least once between turns, got %d calls", got)
	}
}

func TestQueryToolRefreshNilReturnKeepsExistingTools(t *testing.T) {
	ctx := context.Background()
	mdl := &refreshTrackingModel{totalCalls: 1}
	maxTurns := 3

	originalTools := []*schema.ToolInfo{{Name: "Read", Desc: "read a file"}}

	_, terminal := collectEvents(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "read"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		ToolUseContext: &ToolUseContext{
			Options: &ToolUseOptions{
				Tools: originalTools,
				RefreshTools: func() []*schema.ToolInfo {
					return nil // nil means "no changes"
				},
			},
		},
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "ok", nil
		},
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected TerminalCompleted, got %q", terminal.Reason)
	}
}

// contextModifierModel produces two tool calls — the first modifies context,
// the second runs with the modified context.
type contextModifierModel struct {
	callCount int
}

func (m *contextModifierModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *contextModifierModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.callCount++
	if m.callCount == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{
					ID:   "call_modifier",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "EnterPlanMode",
						Arguments: `{}`,
					},
				},
				{
					ID:   "call_after_modifier",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "Read",
						Arguments: `{"file_path":"/tmp/test"}`,
					},
				},
			},
		}}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "done",
	}}), nil
}

func TestQueryContextModifierPropagatesFromToolOutcome(t *testing.T) {
	ctx := context.Background()
	mdl := &contextModifierModel{}
	maxTurns := 4

	reg := tools.NewRegistry()
	// Register a stub tool whose execution returns a context modifier
	reg.Register(tools.ToolImpl{
		Info:             &schema.ToolInfo{Name: "EnterPlanMode", Desc: "enter plan mode"},
		IsReadOnly:       true,
		NeedsPermissions: false,
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			return "Plan mode entered.", nil
		},
	})
	reg.Register(tools.ToolImpl{
		Info:             &schema.ToolInfo{Name: "Read", Desc: "read a file"},
		IsReadOnly:       true,
		NeedsPermissions: false,
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			return "file content", nil
		},
	})

	var toolResultEvents []string
	terminal := Query(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "enter plan mode then read"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    mdl,
		ToolRegistry: reg,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			toolResultEvents = append(toolResultEvents, toolName)
			return "ok", nil
		},
	}, func(evt QueryEvent) {})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected TerminalCompleted, got %q", terminal.Reason)
	}

	// Both tools should have been executed
	if len(toolResultEvents) != 2 {
		t.Fatalf("expected 2 tool executions, got %d: %v", len(toolResultEvents), toolResultEvents)
	}
	if toolResultEvents[0] != "EnterPlanMode" || toolResultEvents[1] != "Read" {
		t.Errorf("unexpected tool execution order: %v", toolResultEvents)
	}
}

func TestQueryToolUseSummaryAsyncResolvedOnNextTurn(t *testing.T) {
	ctx := context.Background()
	// Use a model that does 2 tool-use turns
	mdl := &refreshTrackingModel{totalCalls: 2}
	maxTurns := 5

	var summaryEvents []ToolUseSummaryEvent

	// Create a simple summary model that returns a fixed summary
	summaryMdl := &fixedSummaryModel{summary: "Used Read tool to read files."}

	Query(ctx, QueryParams{
		Messages:             []*schema.Message{{Role: schema.User, Content: "read files"}},
		SystemPrompt:         &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:          QuerySourceSDK,
		MaxTurns:             &maxTurns,
		ChatModel:            mdl,
		SummaryModel:         summaryMdl,
		ToolUseSummaryModel:  summaryMdl,
		EmitToolUseSummaries: true,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "content", nil
		},
	}, func(evt QueryEvent) {
		if evt.Type == EventToolUseSummary && evt.ToolUseSummary != nil {
			summaryEvents = append(summaryEvents, *evt.ToolUseSummary)
		}
	})

	// After 2 tool-use turns, the second turn should yield the summary from the first.
	// (The first turn's summary is resolved at the start of the second turn.)
	if len(summaryEvents) < 1 {
		t.Fatalf("expected at least 1 tool_use_summary event, got %d", len(summaryEvents))
	}
	if summaryEvents[0].Summary == "" {
		t.Error("expected non-empty summary text")
	}
}

// fixedSummaryModel returns a fixed summary for tool use summary generation.
type fixedSummaryModel struct {
	summary string
}

func (m *fixedSummaryModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: m.summary}, nil
}

func (m *fixedSummaryModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: m.summary,
	}}), nil
}
