package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/execution"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type queryInputShapeModel struct{}

func (m *queryInputShapeModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *queryInputShapeModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}), nil
}

func TestQueryShapesSystemAndUserContextForModelCall(t *testing.T) {
	t.Setenv("NODE_ENV", "production")

	var capturedMessages []*schema.Message
	var capturedSystemPrompt *schema.Message
	terminal := Query(context.Background(), QueryParams{
		Messages:       []*schema.Message{{Role: schema.User, Content: "hello"}},
		SystemPrompt:   &schema.Message{Role: schema.System, Content: "Base prompt."},
		UserContext:    map[string]string{"cwd": "/tmp/project", "platform": "linux/amd64"},
		SystemContext:  map[string]string{"gitStatus": "clean"},
		QuerySource:    QuerySourceSDK,
		ChatModel:      &queryInputShapeModel{},
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{MainLoopModel: "main-model"}},
		Deps: &QueryDeps{
			UUID: func() string { return "chain-1" },
			CallModel: func(callCtx context.Context, chatModel model.BaseChatModel, messages []*schema.Message, systemPrompt *schema.Message, tools []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
				capturedMessages = append([]*schema.Message(nil), messages...)
				capturedSystemPrompt = systemPrompt
				return &execution.CallModelResult{
					StreamReader: schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}),
					Model:        opts.Model,
				}, nil
			},
		},
	}, func(QueryEvent) {})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected completed terminal, got %q", terminal.Reason)
	}
	if capturedSystemPrompt == nil {
		t.Fatal("expected shaped system prompt to be passed to call model")
		return
	}
	for _, want := range []string{"Base prompt.", "gitStatus: clean"} {
		if !strings.Contains(capturedSystemPrompt.Content, want) {
			t.Fatalf("expected shaped system prompt to contain %q, got %q", want, capturedSystemPrompt.Content)
		}
	}
	if len(capturedMessages) != 2 {
		t.Fatalf("expected prepended user context reminder plus original user message, got %#v", capturedMessages)
	}
	if capturedMessages[0].Role != schema.User {
		t.Fatalf("expected prepended message to be user-role, got %#v", capturedMessages[0])
	}
	if capturedMessages[0].Extra == nil || capturedMessages[0].Extra["is_meta"] != true {
		t.Fatalf("expected prepended message to be meta, got %#v", capturedMessages[0].Extra)
		return
	}
	for _, want := range []string{"<system-reminder>", "# cwd", "/tmp/project"} {
		if !strings.Contains(capturedMessages[0].Content, want) {
			t.Fatalf("expected prepended user reminder to contain %q, got %q", want, capturedMessages[0].Content)
		}
	}
	if capturedMessages[1].Role != schema.User || capturedMessages[1].Content != "hello" {
		t.Fatalf("expected original user message after reminder, got %#v", capturedMessages[1])
	}
}
