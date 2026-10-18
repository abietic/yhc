package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/permission"
)

func TestQueryMicrocompactPreservesHealthyHistory(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "")
	t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "")
	t.Setenv("TIME_BASED_MC_ENABLED", "false")
	assertQueryMicrocompactPressure(t, false, &ToolUseOptions{MainLoopModel: "deepseek-flash"}, 1000)
}

func TestQueryMicrocompactStillRelievesPressure(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "16000")
	t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "")
	t.Setenv("TIME_BASED_MC_ENABLED", "false")
	assertQueryMicrocompactPressure(t, true, &ToolUseOptions{MainLoopModel: "deepseek-flash"}, 1000)
}

func TestQueryMicrocompactUsesPlanModel(t *testing.T) {
	t.Setenv("TIME_BASED_MC_ENABLED", "false")
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "")
	t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "")
	for _, setting := range []string{"opusplan", "haiku"} {
		for _, tc := range []struct {
			name      string
			main      string
			selected  string
			pressured bool
		}{
			{name: "larger-plan-window", main: "gpt-4o", selected: "deepseek-flash"},
			{name: "smaller-plan-window", main: "deepseek-flash", selected: "gpt-4o", pressured: true},
		} {
			t.Run(setting+"/"+tc.name, func(t *testing.T) {
				t.Setenv("ANTHROPIC_DEFAULT_OPUS_MODEL", tc.selected)
				t.Setenv("ANTHROPIC_DEFAULT_SONNET_MODEL", tc.selected)
				// Between the 128k and 1M pressure thresholds, below the
				// separate collapse threshold so this isolates model selection.
				assertQueryMicrocompactPressure(t, tc.pressured, &ToolUseOptions{
					MainLoopModel: tc.main, ModelSetting: setting, PermissionMode: permission.ModePlan,
				}, 32000)
			})
		}
	}
}

func TestQueryMicrocompactRetainsDynamicPlanSelection(t *testing.T) {
	t.Setenv("TIME_BASED_MC_ENABLED", "false")
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "")
	t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "")
	t.Setenv("ANTHROPIC_DEFAULT_OPUS_MODEL", "deepseek-flash")
	options := &ToolUseOptions{MainLoopModel: "gpt-4o", ModelSetting: "opusplan", PermissionMode: permission.ModePlan}
	reasoning := strings.Repeat("step ", 170000)
	history := []*schema.Message{
		{Role: schema.User, Content: "Inspect the fixture."},
		{Role: schema.Assistant, Content: "Inspected.", ReasoningContent: reasoning},
		{Role: schema.User, Content: "Continue."},
	}
	if got := getRuntimeMainLoopModel(options, history); got != "gpt-4o" {
		t.Fatalf("fixture must start on the over-200k context route, got %s", got)
	}
	var selected string
	var sentReasoningBytes int
	terminal := Query(context.Background(), QueryParams{
		Messages: history, QuerySource: QuerySourceSDK,
		ChatModel: &captureInputModel{}, ToolUseContext: &ToolUseContext{Options: options},
		Deps: &QueryDeps{CallModel: func(_ context.Context, _ model.BaseChatModel, messages []*schema.Message, _ *schema.Message, _ []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
			selected = opts.Model
			for _, msg := range messages {
				sentReasoningBytes += len(msg.ReasoningContent)
			}
			return &execution.CallModelResult{Model: opts.Model, StreamReader: schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}})}, nil
		}},
	}, func(QueryEvent) {})
	if terminal.Reason != TerminalCompleted || selected != "deepseek-flash" || sentReasoningBytes >= len(reasoning) {
		t.Fatalf("terminal=%s selected=%s reasoning_bytes=%d", terminal.Reason, selected, sentReasoningBytes)
	}
	if history[1].ReasoningContent != reasoning || options.MainLoopModel != "gpt-4o" {
		t.Fatal("dynamic selection mutated the caller history or configured route")
	}
}

func assertQueryMicrocompactPressure(t *testing.T, pressured bool, options *ToolUseOptions, reasoningRepeats int) {
	t.Helper()
	text := strings.Repeat("fixture record\n", 1200)
	reasoning := strings.Repeat("reasoning step\n", reasoningRepeats)
	history := []*schema.Message{
		{Role: schema.User, Content: "Inspect the fixture and preserve its contract."},
		{
			Role: schema.Assistant, ReasoningContent: reasoning,
			AssistantGenMultiContent: []schema.MessageOutputPart{{Type: schema.ChatMessagePartTypeReasoning, Reasoning: &schema.MessageOutputReasoning{Text: reasoning}}},
			ToolCalls:                []schema.ToolCall{{ID: "read-old", Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: `{}`}}},
		},
		{Role: schema.Tool, ToolCallID: "read-old", Content: text},
		{Role: schema.Assistant, Content: "The read result has been consumed."},
		{Role: schema.User, Content: "Continue."},
	}
	model := &captureInputModel{}
	terminal := Query(context.Background(), QueryParams{
		Messages:       history,
		SystemPrompt:   &schema.Message{Role: schema.System, Content: "Fixture system prompt."},
		QuerySource:    QuerySourceSDK,
		ChatModel:      model,
		ToolUseContext: &ToolUseContext{Options: options},
	}, func(QueryEvent) {})
	if terminal.Reason != TerminalCompleted || len(model.inputs) != 1 {
		t.Fatalf("terminal=%s provider calls=%d", terminal.Reason, len(model.inputs))
	}
	var tool *schema.Message
	var prior *schema.Message
	for _, msg := range model.inputs[0] {
		if msg.Role == schema.Tool && msg.ToolCallID == "read-old" {
			tool = msg
		}
		if msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 && msg.ToolCalls[0].ID == "read-old" {
			prior = msg
		}
	}
	if tool == nil || prior == nil {
		for _, msg := range model.inputs[0] {
			t.Logf("role=%s calls=%d tool_id=%s content_bytes=%d reasoning_bytes=%d", msg.Role, len(msg.ToolCalls), msg.ToolCallID, len(msg.Content), len(msg.ReasoningContent))
		}
		t.Fatal("tool round was lost")
	}
	if pressured {
		if len(tool.Content) >= len(text) || len(prior.ReasoningContent) >= len(reasoning) {
			t.Fatal("context pressure did not retain the bounded trimming path")
		}
	} else {
		if tool.Content != text {
			t.Errorf("healthy history tool result changed: bytes=%d want=%d", len(tool.Content), len(text))
		}
		if prior.ReasoningContent != reasoning || len(prior.AssistantGenMultiContent) != 1 || prior.AssistantGenMultiContent[0].Reasoning.Text != reasoning {
			t.Error("healthy history reasoning changed before provider dispatch")
		}
	}
	if history[2].Content != text || history[1].ReasoningContent != reasoning || history[1].AssistantGenMultiContent[0].Reasoning.Text != reasoning {
		t.Fatal("caller history mutated")
	}
}
