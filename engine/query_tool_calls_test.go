package engine

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type partialToolCallModel struct {
	callCount int
}

type truncatedToolCallModel struct {
	callCount int
}

type withheldTruncatedToolCallModel struct {
	callCount int
}

func (m *withheldTruncatedToolCallModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *withheldTruncatedToolCallModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.callCount++
	if m.callCount == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{
			{
				Role:      schema.Assistant,
				ToolCalls: []schema.ToolCall{*makeQueryToolCall("call_withheld", "Write", `{"file_path":"/tmp/a","content":"a"}`)},
			},
			{
				Role:    schema.Assistant,
				Content: "output limit",
				Extra: map[string]any{
					"api_error":  true,
					"error_type": "max_output_tokens",
				},
			},
		}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "recovered"}}), nil
}

func (m *truncatedToolCallModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *truncatedToolCallModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.callCount++
	if m.callCount == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{
			{
				Role:      schema.Assistant,
				ToolCalls: []schema.ToolCall{*makeQueryToolCall("call_truncated", "Write", `{"file_path":"/tmp/a","content":"a"}`)},
			},
			{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{FinishReason: "max_tokens"}},
		}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:         schema.Assistant,
		Content:      "recovered",
		ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"},
	}}), nil
}

func TestQueryRejectsTruncatedStreamToolCallWithoutExecution(t *testing.T) {
	t.Parallel()

	model := &truncatedToolCallModel{}
	var executions atomic.Int32
	maxTurns := 5
	var rejectedResult *schema.Message
	terminal := Query(context.Background(), QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "write a file"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		ToolExecutor: func(context.Context, string, string) (string, error) {
			executions.Add(1)
			return "unexpected", nil
		},
	}, func(event QueryEvent) {
		if event.Type == EventToolResult && event.ToolResultMessage != nil && event.ToolResultMessage.ToolCallID == "call_truncated" {
			rejectedResult = event.ToolResultMessage
		}
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal reason = %q, want %q", terminal.Reason, TerminalCompleted)
	}
	if got := executions.Load(); got != 0 {
		t.Fatalf("tool executions = %d, want 0", got)
	}
	if rejectedResult == nil || rejectedResult.Extra["is_error"] != true || !strings.Contains(rejectedResult.Content, "truncated") {
		t.Fatalf("rejected result = %#v, want truncation error", rejectedResult)
	}
	if model.callCount != 2 {
		t.Fatalf("model calls = %d, want recovery follow-up", model.callCount)
	}
}

func TestQueryWithheldTruncationRejectsToolBeforeRecovery(t *testing.T) {
	t.Parallel()

	model := &withheldTruncatedToolCallModel{}
	var executions atomic.Int32
	maxTurns := 5
	var rejectedResult *schema.Message
	terminal := Query(context.Background(), QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "write a file"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		ToolExecutor: func(context.Context, string, string) (string, error) {
			executions.Add(1)
			return "unexpected", nil
		},
	}, func(event QueryEvent) {
		if event.Type == EventToolResult && event.ToolResultMessage != nil && event.ToolResultMessage.ToolCallID == "call_withheld" {
			rejectedResult = event.ToolResultMessage
		}
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal reason = %q, want %q", terminal.Reason, TerminalCompleted)
	}
	if got := executions.Load(); got != 0 {
		t.Fatalf("tool executions = %d, want 0", got)
	}
	if rejectedResult == nil || rejectedResult.Extra["is_error"] != true || !strings.Contains(rejectedResult.Content, "truncated") {
		t.Fatalf("rejected result = %#v, want truncation error", rejectedResult)
	}
	if model.callCount != 2 {
		t.Fatalf("model calls = %d, want one max-output recovery retry", model.callCount)
	}
}

func makeQueryToolCall(id, name, arguments string) *schema.ToolCall {
	return &schema.ToolCall{
		ID:   id,
		Type: "function",
		Function: schema.FunctionCall{
			Name:      name,
			Arguments: arguments,
		},
	}
}

func (m *partialToolCallModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *partialToolCallModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
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

func TestQueryExecutesMergedStreamToolCallOnce(t *testing.T) {
	ctx := context.Background()
	model := &partialToolCallModel{}
	var calls []string

	maxTurns := 5
	terminal := Query(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "run a command"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			calls = append(calls, toolName+":"+jsonInput)
			return "ok", nil
		},
	}, func(QueryEvent) {})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected terminal completed, got %q", terminal.Reason)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool execution, got %d (%v)", len(calls), calls)
	}
	if calls[0] != `Bash:{"command":"echo hi"}` {
		t.Fatalf("expected finalized tool args, got %q", calls[0])
	}
}
