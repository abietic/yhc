package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

func TestExecuteToolCallEmitsSupplementalAttachmentsAfterResult(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{Info: &schema.ToolInfo{
		Name:        "RichRead",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}})
	first := &schema.Message{Role: schema.User, Content: "first", Extra: map[string]any{"is_meta": true}}
	second := &schema.Message{Role: schema.User, Content: "second", Extra: map[string]any{"is_meta": true}}
	toolCall := &schema.ToolCall{ID: "tool-1", Function: schema.FunctionCall{Name: "RichRead", Arguments: `{}`}}
	outcome := executeToolCall(context.Background(), QueryParams{
		ToolRegistry: registry,
		ToolExecutor: func(ctx context.Context, _, _ string) (string, error) {
			if !tools.EmitAttachment(ctx, first) || !tools.EmitAttachment(ctx, second) {
				t.Fatal("tool attachment collector was unavailable")
			}
			return "read complete", nil
		},
	}, nil, nil, toolCall, nil)
	if outcome == nil || outcome.Result == nil || outcome.Result.Content != "read complete" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if len(outcome.AfterResults) != 2 || outcome.AfterResults[0] != first || outcome.AfterResults[1] != second {
		t.Fatalf("supplemental ordering = %#v", outcome.AfterResults)
	}
}

func TestExecuteToolCallDiscardsAttachmentsWhenToolFails(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{Info: &schema.ToolInfo{
		Name:        "FailingRichRead",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}})
	toolCall := &schema.ToolCall{ID: "tool-2", Function: schema.FunctionCall{Name: "FailingRichRead", Arguments: `{}`}}
	outcome := executeToolCall(context.Background(), QueryParams{
		ToolRegistry: registry,
		ToolExecutor: func(ctx context.Context, _, _ string) (string, error) {
			if !tools.EmitAttachment(ctx, &schema.Message{Role: schema.User, Content: "must not leak"}) {
				t.Fatal("tool attachment collector was unavailable")
			}
			return "", errors.New("read failed")
		},
	}, nil, nil, toolCall, nil)
	if outcome == nil || outcome.Result == nil || len(outcome.AfterResults) != 0 {
		t.Fatalf("failed outcome leaked attachments: %#v", outcome)
	}
	if isError, _ := outcome.Result.Extra["is_error"].(bool); !isError {
		t.Fatalf("failed tool result = %#v", outcome.Result)
	}
}

func TestExecuteToolCallIgnoresAttachmentsEmittedAfterReturn(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{Info: &schema.ToolInfo{
		Name:        "LateRichRead",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}})
	var retained context.Context
	toolCall := &schema.ToolCall{ID: "tool-late", Function: schema.FunctionCall{Name: "LateRichRead", Arguments: `{}`}}
	outcome := executeToolCall(context.Background(), QueryParams{
		ToolRegistry: registry,
		ToolExecutor: func(ctx context.Context, _, _ string) (string, error) {
			retained = ctx
			return "complete", nil
		},
	}, nil, nil, toolCall, nil)
	tools.EmitAttachment(retained, &schema.Message{Role: schema.User, Content: "late"})
	if outcome == nil || len(outcome.AfterResults) != 0 {
		t.Fatalf("late attachment changed outcome: %#v", outcome)
	}
}

func TestEmitStreamingToolResultOrdersSupplementalAttachments(t *testing.T) {
	before := &schema.Message{Role: schema.User, Content: "before"}
	result := &schema.Message{Role: schema.Tool, Content: "result", ToolCallID: "tool-stream"}
	after := &schema.Message{Role: schema.User, Content: "after"}
	completed := &execution.ToolResult{
		BeforeMessages: []*schema.Message{before},
		Message:        result,
		AfterMessages:  []*schema.Message{after},
		ToolCallID:     "tool-stream",
		ToolName:       "Read",
		Result:         "result",
	}
	var events []QueryEvent
	var messages []*schema.Message
	if err := emitStreamingToolResult(
		func(event QueryEvent) { events = append(events, event) },
		&messages,
		completed,
	); err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 ||
		events[0].Type != EventAttachment ||
		events[1].Type != EventCanonicalProjection ||
		events[2].Type != EventToolResult ||
		events[3].Type != EventAttachment {
		t.Fatalf("streaming event order = %#v", events)
	}
	if len(messages) != 3 || messages[0] != before || messages[1] != result || messages[2] != after {
		t.Fatalf("streaming message order = %#v", messages)
	}
}

func TestQueryEngineToolExecutorInjectsMediaCapability(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "CheckMedia", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{})},
		ExecuteCtx: func(ctx context.Context, _ string) (string, error) {
			if tools.MediaSupported(ctx) {
				return "supported", nil
			}
			return "unsupported", nil
		},
	})
	engine := &QueryEngine{config: QueryEngineConfig{Model: "gpt-4o"}, toolRegistry: registry}
	result, err := engine.toolExecutor(context.Background(), "CheckMedia", `{}`)
	if err != nil || result != "supported" {
		t.Fatalf("known media model result = %q err=%v", result, err)
	}
	engine.config.Model = "gpt-4"
	result, err = engine.toolExecutor(context.Background(), "CheckMedia", `{}`)
	if err != nil || result != "unsupported" {
		t.Fatalf("known text-only model result = %q err=%v", result, err)
	}
}
