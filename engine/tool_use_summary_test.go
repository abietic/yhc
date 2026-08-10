package engine

import (
	"context"
	"testing"

	"github.com/abietic/yhc/engine/execution"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type summaryModel struct {
	called  int
	content string
}

func (m *summaryModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: m.content}, nil
}

func (m *summaryModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.called++
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: m.content}}), nil
}

func TestQueryEmitsToolUseSummaryBetweenTurns(t *testing.T) {
	ctx := context.Background()

	summaryMdl := &summaryModel{content: "Ran echo command"}
	toolCtx := &ToolUseContext{
		Options: &ToolUseOptions{
			MainLoopModel: "test-model",
			Tools:         []*schema.ToolInfo{{Name: "Bash"}},
		},
	}

	callCount := 0
	maxTurns := 3
	var events []QueryEvent

	terminal := Query(ctx, QueryParams{
		Messages:             []*schema.Message{{Role: schema.User, Content: "hello"}},
		SystemPrompt:         &schema.Message{Role: schema.System, Content: "sys"},
		ToolUseContext:       toolCtx,
		QuerySource:          QuerySourceSDK,
		ChatModel:            &callModelOptionsModel{},
		SummaryModel:         summaryMdl,
		ToolUseSummaryModel:  summaryMdl,
		EmitToolUseSummaries: true,
		MaxTurns:             &maxTurns,
		Deps: &QueryDeps{
			UUID: func() string { return "uuid-1" },
			CallModel: func(callCtx context.Context, chatModel model.BaseChatModel, messages []*schema.Message, systemPrompt *schema.Message, tools []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
				callCount++
				if callCount == 1 {
					return &execution.CallModelResult{
						StreamReader: schema.StreamReaderFromArray([]*schema.Message{{
							Role:    schema.Assistant,
							Content: "running bash",
							ToolCalls: []schema.ToolCall{{
								ID:       "tc1",
								Type:     "function",
								Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"echo hi"}`},
							}},
						}}),
						Model: opts.Model,
					}, nil
				}
				return &execution.CallModelResult{
					StreamReader: schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}),
					Model:        opts.Model,
				}, nil
			},
		},
	}, func(evt QueryEvent) {
		events = append(events, evt)
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected completed terminal, got %q", terminal.Reason)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 model calls, got %d", callCount)
	}
	if summaryMdl.called != 1 {
		t.Fatalf("expected summary model called once, got %d", summaryMdl.called)
	}

	// Find the tool use summary event
	var summaryEvt *ToolUseSummaryEvent
	for _, evt := range events {
		if evt.Type == EventToolUseSummary && evt.ToolUseSummary != nil {
			summaryEvt = evt.ToolUseSummary
		}
	}
	if summaryEvt == nil {
		t.Fatal("expected tool use summary event to be emitted")
		return
	}
	if summaryEvt.Summary != "Ran echo command" {
		t.Fatalf("expected summary text from model, got %q", summaryEvt.Summary)
	}
	if len(summaryEvt.PrecedingToolUseIDs) != 1 || summaryEvt.PrecedingToolUseIDs[0] != "tc1" {
		t.Fatalf("expected preceding tool use IDs to contain tc1, got %v", summaryEvt.PrecedingToolUseIDs)
	}
	if summaryEvt.UUID != "uuid-1" {
		t.Fatalf("expected UUID from deps, got %q", summaryEvt.UUID)
	}
}

func TestQuerySkipsSummaryWhenGateDisabled(t *testing.T) {
	ctx := context.Background()

	summaryMdl := &summaryModel{content: "should not be called"}
	toolCtx := &ToolUseContext{
		Options: &ToolUseOptions{
			MainLoopModel: "test-model",
			Tools:         []*schema.ToolInfo{{Name: "Bash"}},
		},
	}

	callCount := 0
	maxTurns := 3
	var events []QueryEvent

	Query(ctx, QueryParams{
		Messages:             []*schema.Message{{Role: schema.User, Content: "hello"}},
		SystemPrompt:         &schema.Message{Role: schema.System, Content: "sys"},
		ToolUseContext:       toolCtx,
		QuerySource:          QuerySourceSDK,
		ChatModel:            &callModelOptionsModel{},
		SummaryModel:         summaryMdl,
		ToolUseSummaryModel:  summaryMdl,
		EmitToolUseSummaries: false, // gate disabled
		MaxTurns:             &maxTurns,
		Deps: &QueryDeps{
			UUID: func() string { return "uuid-1" },
			CallModel: func(callCtx context.Context, chatModel model.BaseChatModel, messages []*schema.Message, systemPrompt *schema.Message, tools []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
				callCount++
				if callCount == 1 {
					return &execution.CallModelResult{
						StreamReader: schema.StreamReaderFromArray([]*schema.Message{{
							Role:    schema.Assistant,
							Content: "using tool",
							ToolCalls: []schema.ToolCall{{
								ID:       "tc1",
								Type:     "function",
								Function: schema.FunctionCall{Name: "Bash", Arguments: `{}`},
							}},
						}}),
						Model: opts.Model,
					}, nil
				}
				return &execution.CallModelResult{
					StreamReader: schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}),
					Model:        opts.Model,
				}, nil
			},
		},
	}, func(evt QueryEvent) {
		events = append(events, evt)
	})

	if summaryMdl.called != 0 {
		t.Fatalf("expected summary model NOT called when gate disabled, got %d", summaryMdl.called)
	}
	for _, evt := range events {
		if evt.Type == EventToolUseSummary {
			t.Fatal("expected no tool use summary event when gate disabled")
		}
	}
}

func TestQuerySkipsSummaryForSubagents(t *testing.T) {
	ctx := context.Background()

	summaryMdl := &summaryModel{content: "should not be called"}
	toolCtx := &ToolUseContext{
		AgentID: "sub-agent-1", // subagent — should skip
		Options: &ToolUseOptions{
			MainLoopModel: "test-model",
			Tools:         []*schema.ToolInfo{{Name: "Bash"}},
		},
	}

	callCount := 0
	maxTurns := 3

	Query(ctx, QueryParams{
		Messages:             []*schema.Message{{Role: schema.User, Content: "hello"}},
		SystemPrompt:         &schema.Message{Role: schema.System, Content: "sys"},
		ToolUseContext:       toolCtx,
		QuerySource:          QuerySourceSDK,
		ChatModel:            &callModelOptionsModel{},
		SummaryModel:         summaryMdl,
		ToolUseSummaryModel:  summaryMdl,
		EmitToolUseSummaries: true,
		MaxTurns:             &maxTurns,
		Deps: &QueryDeps{
			UUID: func() string { return "uuid-1" },
			CallModel: func(callCtx context.Context, chatModel model.BaseChatModel, messages []*schema.Message, systemPrompt *schema.Message, tools []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
				callCount++
				if callCount == 1 {
					return &execution.CallModelResult{
						StreamReader: schema.StreamReaderFromArray([]*schema.Message{{
							Role:    schema.Assistant,
							Content: "using tool",
							ToolCalls: []schema.ToolCall{{
								ID:       "tc1",
								Type:     "function",
								Function: schema.FunctionCall{Name: "Bash", Arguments: `{}`},
							}},
						}}),
						Model: opts.Model,
					}, nil
				}
				return &execution.CallModelResult{
					StreamReader: schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}),
					Model:        opts.Model,
				}, nil
			},
		},
	}, func(evt QueryEvent) {})

	if summaryMdl.called != 0 {
		t.Fatalf("expected summary model NOT called for subagent, got %d", summaryMdl.called)
	}
}
