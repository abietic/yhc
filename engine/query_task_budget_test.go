package engine

import (
	"context"
	"testing"

	"github.com/abietic/yhc/engine/execution"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type taskBudgetModel struct{}

func (m *taskBudgetModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *taskBudgetModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}), nil
}

func TestQueryFillsMissingCallModelDepFromDefaults(t *testing.T) {
	ctx := context.Background()
	chatModel := &taskBudgetModel{}

	terminal := Query(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "hello"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		ChatModel:    chatModel,
		Deps: &QueryDeps{
			UUID: func() string { return "chain-1" },
		},
	}, func(QueryEvent) {})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected completed terminal, got %q", terminal.Reason)
	}
}

func TestQueryPassesTaskBudgetWithoutCompaction(t *testing.T) {
	ctx := context.Background()
	chatModel := &taskBudgetModel{}
	captured := make([]execution.CallModelOptions, 0, 1)

	terminal := Query(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "hello"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		TaskBudget:   &TaskBudget{Total: 4096},
		ChatModel:    chatModel,
		Deps: &QueryDeps{
			UUID: func() string { return "chain-1" },
			CallModel: func(ctx context.Context, chatModel model.BaseChatModel, messages []*schema.Message, systemPrompt *schema.Message, tools []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
				captured = append(captured, opts)
				return execution.CallModel(ctx, chatModel, messages, systemPrompt, tools, opts)
			},
		},
	}, func(QueryEvent) {})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected completed terminal, got %q", terminal.Reason)
	}
	if len(captured) != 1 {
		t.Fatalf("expected 1 captured model call, got %d", len(captured))
	}
	if captured[0].TaskBudget == nil {
		t.Fatal("expected task budget to be passed to model call")
		return
	}
	if captured[0].TaskBudget.Total != 4096 {
		t.Fatalf("expected task budget total 4096, got %d", captured[0].TaskBudget.Total)
	}
	if captured[0].TaskBudget.Remaining != nil {
		t.Fatalf("expected nil remaining before compaction, got %d", *captured[0].TaskBudget.Remaining)
		return
	}
}

func TestQueryPassesReducedTaskBudgetAfterAutoCompact(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "2000")

	ctx := context.Background()
	chatModel := &taskBudgetModel{}
	captured := make([]execution.CallModelOptions, 0, 1)
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "done"},
		{Role: schema.User, Content: repeatedWords("older question", 220)},
		{Role: schema.Assistant, Content: repeatedWords("older answer", 200)},
		{Role: schema.User, Content: "latest question"},
		{Role: schema.Assistant, Content: "latest answer"},
	}

	terminal := Query(ctx, QueryParams{
		Messages:     messages,
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		TaskBudget:   &TaskBudget{Total: 5000},
		ChatModel:    chatModel,
		Deps: &QueryDeps{
			UUID: func() string { return "chain-1" },
			CallModel: func(ctx context.Context, chatModel model.BaseChatModel, messages []*schema.Message, systemPrompt *schema.Message, tools []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
				captured = append(captured, opts)
				return execution.CallModel(ctx, chatModel, messages, systemPrompt, tools, opts)
			},
		},
	}, func(QueryEvent) {})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected completed terminal, got %q", terminal.Reason)
	}
	if len(captured) != 1 {
		t.Fatalf("expected 1 captured model call, got %d", len(captured))
	}
	if captured[0].TaskBudget == nil {
		t.Fatal("expected task budget to be passed to model call")
		return
	}
	if captured[0].TaskBudget.Remaining == nil {
		t.Fatal("expected remaining budget after auto-compact")
		return
	}
	remaining := *captured[0].TaskBudget.Remaining
	if remaining < 0 || remaining >= 5000 {
		t.Fatalf("expected reduced remaining budget between 0 and total, got %d", remaining)
	}
}

func repeatedWords(word string, count int) string {
	out := ""
	for i := 0; i < count; i++ {
		if i > 0 {
			out += " "
		}
		out += word
	}
	return out
}
