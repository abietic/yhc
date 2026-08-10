package engine

import (
	"context"
	"testing"

	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type queryEngineToolCallModel struct {
	callCount int
}

func (m *queryEngineToolCallModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *queryEngineToolCallModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.callCount++
	if m.callCount == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "Bash",
						Arguments: "{}",
					},
				}},
			},
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "Bash",
						Arguments: `{"command":"echo hi"}`,
					},
				}},
			},
		}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "done",
	}}), nil
}

func TestQueryEngineStoresMergedAssistantHistory(t *testing.T) {
	reg := tools.NewRegistry()
	tools.RegisterDefaults(reg)

	eng := NewQueryEngine(QueryEngineConfig{
		CustomSystemPrompt: "You are helpful.",
		MaxTurns:           5,
		ChatModel:          &queryEngineToolCallModel{},
		ToolRegistry:       reg,
		Tools:              reg.List(),
		Model:              "test-model",
	})

	events, _ := eng.SubmitMessage(context.Background(), "run a command")
	for range events {
	}

	msgs := eng.GetMessages()
	if len(msgs) != 4 {
		t.Fatalf("expected 4 stored messages, got %d", len(msgs))
	}
	if msgs[0].Role != schema.User {
		t.Fatalf("expected first message to be user, got %q", msgs[0].Role)
	}
	if len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("expected merged assistant tool call, got %d tool calls", len(msgs[1].ToolCalls))
	}
	if msgs[1].ToolCalls[0].Function.Arguments != `{"command":"echo hi"}` {
		t.Fatalf("expected finalized tool args in history, got %q", msgs[1].ToolCalls[0].Function.Arguments)
	}
	if msgs[2].Role != schema.Tool {
		t.Fatalf("expected tool result in history, got %q", msgs[2].Role)
	}
	if msgs[2].ToolName != "Bash" {
		t.Fatalf("expected tool name Bash, got %q", msgs[2].ToolName)
	}
	if msgs[3].Role != schema.Assistant || msgs[3].Content != "done" {
		t.Fatalf("expected final assistant message 'done', got role=%q content=%q", msgs[3].Role, msgs[3].Content)
	}
}

type queryEngineReasoningModel struct {
	callCount int
}

func (m *queryEngineReasoningModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *queryEngineReasoningModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.callCount++
	if m.callCount == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{
			{
				Role:             schema.Assistant,
				ReasoningContent: "think",
				AssistantGenMultiContent: []schema.MessageOutputPart{{
					Type: schema.ChatMessagePartTypeReasoning,
					Reasoning: &schema.MessageOutputReasoning{
						Text:      "think",
						Signature: "sig_abc",
					},
				}},
				ToolCalls: []schema.ToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "Bash",
						Arguments: `{"command":"echo hi"}`,
					},
				}},
			},
		}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "done",
		AssistantGenMultiContent: []schema.MessageOutputPart{{
			Type: schema.ChatMessagePartTypeText,
			Text: "done",
		}},
	}}), nil
}

func TestQueryEngineStoresAssistantReasoningHistory(t *testing.T) {
	reg := tools.NewRegistry()
	tools.RegisterDefaults(reg)

	eng := NewQueryEngine(QueryEngineConfig{
		CustomSystemPrompt: "You are helpful.",
		MaxTurns:           5,
		ChatModel:          &queryEngineReasoningModel{},
		ToolRegistry:       reg,
		Tools:              reg.List(),
		Model:              "test-model",
	})

	events, _ := eng.SubmitMessage(context.Background(), "run a command")
	for range events {
	}

	msgs := eng.GetMessages()
	if len(msgs) != 4 {
		t.Fatalf("expected 4 stored messages, got %d", len(msgs))
	}
	assistant := msgs[1]
	if assistant.ReasoningContent != "think" {
		t.Fatalf("expected reasoning content in history, got %q", assistant.ReasoningContent)
	}
	if len(assistant.AssistantGenMultiContent) == 0 {
		t.Fatal("expected assistant multi-content in history")
	}
	if assistant.AssistantGenMultiContent[0].Reasoning == nil || assistant.AssistantGenMultiContent[0].Reasoning.Signature != "sig_abc" {
		t.Fatalf("expected reasoning signature in history, got %#v", assistant.AssistantGenMultiContent[0])
		return
	}
}

func TestConversationHistoryReplacesMessagesAtCompactBoundary(t *testing.T) {
	h := newConversationHistory([]*schema.Message{
		{Role: schema.User, Content: "old user"},
		{Role: schema.Assistant, Content: "old assistant"},
	})
	h.Observe(QueryEvent{
		Type: EventCompactBoundary,
		CompactBoundaryMessage: &schema.Message{
			Role:  schema.System,
			Extra: map[string]any{"subtype": "compact_boundary"},
		},
	})
	h.Observe(QueryEvent{
		Type: EventCompactBoundary,
		CompactBoundaryMessage: &schema.Message{
			Role:    schema.System,
			Content: "summary",
			Extra:   map[string]any{"subtype": "compact_summary"},
		},
	})
	h.Observe(QueryEvent{
		Type:                   EventCompactBoundary,
		CompactBoundaryMessage: &schema.Message{Role: schema.User, Content: "latest question"},
	})
	msgs := h.Messages()
	if len(msgs) != 3 {
		t.Fatalf("expected compacted history only, got %#v", msgs)
	}
	if msgs[0].Extra == nil || msgs[0].Extra["subtype"] != "compact_boundary" {
		t.Fatalf("expected compact boundary first, got %#v", msgs[0])
		return
	}
	if msgs[1].Extra == nil || msgs[1].Extra["subtype"] != "compact_summary" {
		t.Fatalf("expected compact summary second, got %#v", msgs[1])
		return
	}
	if msgs[2].Content != "latest question" {
		t.Fatalf("expected preserved message tail, got %#v", msgs[2])
	}
}
