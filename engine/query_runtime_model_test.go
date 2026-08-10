package engine

import (
	"testing"

	"github.com/abietic/yhc/engine/permission"
	"github.com/cloudwego/eino/schema"
)

func TestGetRuntimeMainLoopModelNilOpts(t *testing.T) {
	got := getRuntimeMainLoopModel(nil, nil)
	if got != "" {
		t.Fatalf("expected empty string for nil opts, got %q", got)
	}
}

func TestGetRuntimeMainLoopModelNoSetting(t *testing.T) {
	opts := &ToolUseOptions{MainLoopModel: "claude-sonnet-4-20250514"}
	got := getRuntimeMainLoopModel(opts, nil)
	if got != "claude-sonnet-4-20250514" {
		t.Fatalf("expected mainLoopModel passthrough, got %q", got)
	}
}

func TestGetRuntimeMainLoopModelOpusPlanInPlanMode(t *testing.T) {
	t.Setenv("ANTHROPIC_DEFAULT_OPUS_MODEL", "claude-3-opus-20240229")
	opts := &ToolUseOptions{
		MainLoopModel:  "claude-sonnet-4-20250514",
		ModelSetting:   "opusplan",
		PermissionMode: permission.ModePlan,
	}
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "hi there"},
	}
	got := getRuntimeMainLoopModel(opts, messages)
	if got != "claude-3-opus-20240229" {
		t.Fatalf("expected opus model in plan mode with opusplan, got %q", got)
	}
}

func TestGetRuntimeMainLoopModelOpusPlanNotPlanMode(t *testing.T) {
	opts := &ToolUseOptions{
		MainLoopModel:  "claude-sonnet-4-20250514",
		ModelSetting:   "opusplan",
		PermissionMode: permission.ModeDefault,
	}
	got := getRuntimeMainLoopModel(opts, nil)
	if got != "claude-sonnet-4-20250514" {
		t.Fatalf("expected mainLoopModel when not in plan mode, got %q", got)
	}
}

func TestGetRuntimeMainLoopModelHaikuInPlanMode(t *testing.T) {
	t.Setenv("ANTHROPIC_DEFAULT_SONNET_MODEL", "claude-sonnet-4-20250514")
	opts := &ToolUseOptions{
		MainLoopModel:  "claude-3-5-haiku-20241022",
		ModelSetting:   "haiku",
		PermissionMode: permission.ModePlan,
	}
	got := getRuntimeMainLoopModel(opts, nil)
	if got != "claude-sonnet-4-20250514" {
		t.Fatalf("expected sonnet model for haiku in plan mode, got %q", got)
	}
}

func TestGetRuntimeMainLoopModelHaikuNotPlanMode(t *testing.T) {
	opts := &ToolUseOptions{
		MainLoopModel:  "claude-3-5-haiku-20241022",
		ModelSetting:   "haiku",
		PermissionMode: permission.ModeDefault,
	}
	got := getRuntimeMainLoopModel(opts, nil)
	if got != "claude-3-5-haiku-20241022" {
		t.Fatalf("expected mainLoopModel when haiku not in plan mode, got %q", got)
	}
}

func TestDoesRecentAssistantExceed200k(t *testing.T) {
	// Small message should not exceed
	small := []*schema.Message{
		{Role: schema.Assistant, Content: "small reply"},
	}
	if doesMostRecentAssistantMessageExceed200k(small) {
		t.Fatal("small message should not exceed 200k")
	}

	// No assistant message
	noAsst := []*schema.Message{
		{Role: schema.User, Content: "hello"},
	}
	if doesMostRecentAssistantMessageExceed200k(noAsst) {
		t.Fatal("no assistant message should not exceed 200k")
	}

	// Empty messages
	if doesMostRecentAssistantMessageExceed200k(nil) {
		t.Fatal("nil messages should not exceed 200k")
	}
}

func TestStripSignatureBlocksRemovesReasoningContent(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{
			Role:             schema.Assistant,
			Content:          "reply",
			ReasoningContent: "thinking about this...",
			AssistantGenMultiContent: []schema.MessageOutputPart{
				{
					Type: schema.ChatMessagePartTypeReasoning,
					Reasoning: &schema.MessageOutputReasoning{
						Text:      "thinking about this...",
						Signature: "private-signature",
					},
				},
				{Type: schema.ChatMessagePartTypeText, Text: "reply"},
			},
			ToolCalls: []schema.ToolCall{{
				ID:   "call-1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "lookup",
					Arguments: `{"id":"public"}`,
				},
			}},
			Extra: map[string]any{"openai-generated": true},
		},
		{Role: schema.Assistant, Content: "another reply"},
	}
	stripped := stripSignatureBlocks(msgs)

	if stripped[0] != msgs[0] {
		t.Fatal("non-assistant messages should be unchanged reference")
	}
	if stripped[1].ReasoningContent != "" {
		t.Fatalf("expected reasoning content stripped, got %q", stripped[1].ReasoningContent)
	}
	if stripped[1].Content != "reply" {
		t.Fatalf("expected text content preserved, got %q", stripped[1].Content)
	}
	if len(stripped[1].AssistantGenMultiContent) != 1 ||
		stripped[1].AssistantGenMultiContent[0].Type !=
			schema.ChatMessagePartTypeText ||
		stripped[1].AssistantGenMultiContent[0].Text != "reply" {
		t.Fatalf(
			"expected only public text output part, got %#v",
			stripped[1].AssistantGenMultiContent,
		)
	}
	if stripped[1].Extra != nil {
		t.Fatalf("expected provider metadata stripped, got %#v", stripped[1].Extra)
	}
	if len(stripped[1].ToolCalls) != 1 ||
		stripped[1].ToolCalls[0].ID != "call-1" ||
		stripped[1].ToolCalls[0].Function.Arguments != `{"id":"public"}` {
		t.Fatalf("expected public tool call preserved, got %#v", stripped[1].ToolCalls)
	}
	if stripped[2] != msgs[2] {
		t.Fatal("assistant without reasoning should be unchanged reference")
	}
	if msgs[1].ReasoningContent != "thinking about this..." ||
		len(msgs[1].AssistantGenMultiContent) != 2 ||
		msgs[1].Extra["openai-generated"] != true {
		t.Fatalf("strip mutated canonical history: %#v", msgs[1])
	}
}

func TestStripSignatureBlocksRemovesStructuredReasoningWithoutLegacyText(t *testing.T) {
	msgs := []*schema.Message{{
		Role:    schema.Assistant,
		Content: "public answer",
		AssistantGenMultiContent: []schema.MessageOutputPart{
			{
				Type: schema.ChatMessagePartTypeReasoning,
				Reasoning: &schema.MessageOutputReasoning{
					Text:      "private reasoning",
					Signature: "private-signature",
				},
			},
			{Type: schema.ChatMessagePartTypeText, Text: "public answer"},
		},
	}}

	stripped := stripSignatureBlocks(msgs)
	if len(stripped) != 1 || len(stripped[0].AssistantGenMultiContent) != 1 {
		t.Fatalf("structured reasoning survived strip: %#v", stripped)
	}
	if stripped[0].AssistantGenMultiContent[0].Text != "public answer" {
		t.Fatalf("public output changed: %#v", stripped[0])
	}
	if len(msgs[0].AssistantGenMultiContent) != 2 {
		t.Fatalf("strip mutated caller history: %#v", msgs[0])
	}
}

func TestStripSignatureBlocksNoOpWhenNoReasoning(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "reply"},
	}
	stripped := stripSignatureBlocks(msgs)
	// Should return same slice (structural sharing)
	if &stripped[0] != &msgs[0] {
		t.Fatal("expected same slice returned when no changes needed")
	}
}

func TestNormalizeMessagesForAPIMergesConsecutiveUserMessages(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "first"},
		{Role: schema.User, Content: "second"},
		{Role: schema.Assistant, Content: "reply"},
	}
	result := normalizeMessagesForAPI(msgs)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages after merge, got %d", len(result))
	}
	if result[0].Content != "first\n\nsecond" {
		t.Fatalf("expected merged content, got %q", result[0].Content)
	}
	if result[1].Content != "reply" {
		t.Fatalf("expected assistant unchanged, got %q", result[1].Content)
	}
}

func TestNormalizeMessagesForAPIFiltersEmptyAssistant(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: ""},
		{Role: schema.Assistant, Content: "real reply"},
	}
	result := normalizeMessagesForAPI(msgs)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages after filtering empty assistant, got %d", len(result))
	}
	if result[1].Content != "real reply" {
		t.Fatalf("expected non-empty assistant, got %q", result[1].Content)
	}
}

func TestNormalizeMessagesForAPIStripsTrailingThinking(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "reply", ReasoningContent: "thinking..."},
	}
	result := normalizeMessagesForAPI(msgs)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[1].ReasoningContent != "" {
		t.Fatalf("expected trailing reasoning stripped, got %q", result[1].ReasoningContent)
	}
	if result[1].Content != "reply" {
		t.Fatalf("expected content preserved, got %q", result[1].Content)
	}
}
