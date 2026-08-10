package engine

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type streamToolOnlyModel struct {
	callCount int
}

func (m *streamToolOnlyModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *streamToolOnlyModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.callCount++
	if m.callCount == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_stream_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Bash",
					Arguments: `{"command":"echo hi"}`,
				},
			}},
		}}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}), nil
}

func TestQueryStreamingToolResultsPopulateTypedEventFields(t *testing.T) {
	ctx := context.Background()
	model := &streamToolOnlyModel{}
	maxTurns := 4
	var sawToolResult bool
	var toolMsg *schema.Message

	terminal := Query(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "run a command"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			return "ok", nil
		},
	}, func(evt QueryEvent) {
		if evt.Type == EventToolResult {
			sawToolResult = true
			toolMsg = evt.ToolResultMessage
		}
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected terminal completed, got %q", terminal.Reason)
	}
	if !sawToolResult {
		t.Fatal("expected a tool result event from streaming execution")
	}
	if toolMsg == nil {
		t.Fatal("expected typed ToolResultMessage to be populated")
		return
	}
	if toolMsg.ToolCallID != "call_stream_1" || toolMsg.ToolName != "Bash" || toolMsg.Content != "ok" {
		t.Fatalf("unexpected tool result message: %#v", toolMsg)
	}
}
