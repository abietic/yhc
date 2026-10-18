package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/permission"
)

func TestQuerySnipPreservesHealthyLongHistory(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "")
	t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "")
	t.Setenv("TIME_BASED_MC_ENABLED", "false")
	reasoning := strings.Repeat("fixture reasoning ", 1000)
	toolResult := strings.Repeat("fixture record\n", 1200)
	history := []*schema.Message{
		{Role: schema.User, Content: "Inspect the fixture."},
		{Role: schema.Assistant, Content: "Inspected.", ReasoningContent: reasoning, ToolCalls: []schema.ToolCall{{ID: "snip-read", Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: `{}`}}}},
		{Role: schema.Tool, ToolCallID: "snip-read", Content: toolResult},
		{Role: schema.Assistant, Content: "Result consumed."},
	}
	for len(history) < 40 {
		history = append(history, &schema.Message{Role: schema.User, Content: "Keep the fixture contract."})
	}
	model := &captureInputModel{}
	events, terminal := collectEvents(context.Background(), QueryParams{
		Messages: history, QuerySource: QuerySourceSDK, ChatModel: model,
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{MainLoopModel: "deepseek-flash"}},
	})
	if terminal.Reason != TerminalCompleted || len(model.inputs) != 1 {
		t.Fatalf("terminal=%s calls=%d", terminal.Reason, len(model.inputs))
	}
	var preserved, toolPreserved bool
	for _, msg := range model.inputs[0] {
		if msg.Role == schema.Tool && msg.ToolCallID == "snip-read" {
			toolPreserved = msg.Content == toolResult
		}
		if msg.Role == schema.Assistant && msg.Content == "Inspected." {
			preserved = msg.ReasoningContent == reasoning
			t.Logf("sent_reasoning_bytes=%d original_bytes=%d", len(msg.ReasoningContent), len(reasoning))
		}
	}
	if !preserved || !toolPreserved {
		t.Error("healthy history reasoning or tool output changed before dispatch")
	}
	for _, evt := range events {
		if evt.CompactBoundaryMessage != nil && evt.CompactBoundaryMessage.Extra["subtype"] == "snip_boundary" {
			t.Error("healthy history emitted a snip boundary")
		}
	}
	if history[1].ReasoningContent != reasoning || history[2].Content != toolResult {
		t.Fatal("caller history mutated")
	}
}

func TestQueryCollapseUsesPreparationModel(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "")
	t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "")
	t.Setenv("TIME_BASED_MC_ENABLED", "false")
	for _, tc := range []struct {
		name      string
		options   ToolUseOptions
		planModel string
		bytes     int
		collapsed bool
	}{
		{name: "flash-healthy-above-default-window", options: ToolUseOptions{MainLoopModel: "deepseek-flash"}, bytes: 640000},
		{name: "small-window-pressure", options: ToolUseOptions{MainLoopModel: "gpt-4o"}, bytes: 400000, collapsed: true},
		{name: "larger-plan-window", options: ToolUseOptions{MainLoopModel: "gpt-4o", ModelSetting: "opusplan", PermissionMode: permission.ModePlan}, planModel: "deepseek-flash", bytes: 640000},
		{name: "smaller-plan-window", options: ToolUseOptions{MainLoopModel: "deepseek-flash", ModelSetting: "haiku", PermissionMode: permission.ModePlan}, planModel: "gpt-4o", bytes: 400000, collapsed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ANTHROPIC_DEFAULT_OPUS_MODEL", tc.planModel)
			t.Setenv("ANTHROPIC_DEFAULT_SONNET_MODEL", tc.planModel)
			// Ordinary assistant text is not a microcompact target. This
			// isolates collapse below the selected model's auto threshold.
			history := []*schema.Message{
				{Role: schema.User, Content: "Inspect the fixture."},
				{Role: schema.Assistant, Content: strings.Repeat("x", tc.bytes)},
				{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "fixture-read", Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: `{}`}}}},
				{Role: schema.Tool, ToolCallID: "fixture-read", Content: "fixture result"},
				{Role: schema.Assistant, Content: "Result consumed."},
				{Role: schema.User, Content: "Continue."},
			}
			model := &captureInputModel{}
			terminal := Query(context.Background(), QueryParams{
				Messages: history, QuerySource: QuerySourceSDK, ChatModel: model,
				ToolUseContext: &ToolUseContext{Options: &tc.options},
			}, func(QueryEvent) {})
			if terminal.Reason != TerminalCompleted || len(model.inputs) != 1 {
				t.Fatalf("terminal=%s calls=%d", terminal.Reason, len(model.inputs))
			}
			var toolPresent bool
			for _, msg := range model.inputs[0] {
				if msg.Role == schema.Tool && msg.ToolCallID == "fixture-read" {
					toolPresent = true
				}
			}
			if toolPresent == tc.collapsed {
				t.Errorf("tool_present=%t want_collapsed=%t", toolPresent, tc.collapsed)
			}
			if hasMessageSubtype(model.inputs[0], "collapse_committed") != tc.collapsed || hasMessageSubtype(model.inputs[0], "compact_summary") {
				t.Error("expected only the model-aware collapse projection")
			}
			if len(history[2].ToolCalls) != 1 || history[3].Content != "fixture result" {
				t.Fatal("collapse mutated caller history")
			}
		})
	}
}

func TestQuerySnipUsesPreparationModel(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "")
	t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "")
	t.Setenv("TIME_BASED_MC_ENABLED", "false")
	for _, tc := range []struct {
		name, main, selected string
		pressured            bool
	}{
		{name: "larger-plan-window", main: "gpt-4o", selected: "deepseek-flash"},
		{name: "smaller-plan-window", main: "deepseek-flash", selected: "gpt-4o", pressured: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ANTHROPIC_DEFAULT_OPUS_MODEL", tc.selected)
			reasoning := strings.Repeat("fixture reasoning ", 1000)
			history := []*schema.Message{
				{Role: schema.User, Content: "Inspect the fixture."},
				{Role: schema.Assistant, Content: strings.Repeat("x", 444000), ReasoningContent: reasoning},
				{Role: schema.Assistant, Content: "Result consumed."},
			}
			for len(history) < 40 {
				history = append(history, &schema.Message{Role: schema.User, Content: "Continue."})
			}
			model := &captureInputModel{}
			events, terminal := collectEvents(context.Background(), QueryParams{
				Messages: history, QuerySource: QuerySourceSDK, ChatModel: model,
				ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{MainLoopModel: tc.main, ModelSetting: "opusplan", PermissionMode: permission.ModePlan}},
			})
			if terminal.Reason != TerminalCompleted || len(model.inputs) != 1 {
				t.Fatalf("terminal=%s calls=%d", terminal.Reason, len(model.inputs))
			}
			var snipped bool
			for _, evt := range events {
				if evt.CompactBoundaryMessage != nil && evt.CompactBoundaryMessage.Extra["subtype"] == "snip_boundary" {
					snipped = true
				}
			}
			if snipped != tc.pressured {
				t.Errorf("snipped=%t want=%t", snipped, tc.pressured)
			}
			if history[1].ReasoningContent != reasoning {
				t.Fatal("caller history mutated")
			}
		})
	}
}
