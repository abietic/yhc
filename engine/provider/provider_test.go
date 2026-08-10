package provider

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/cloudwego/eino-ext/components/model/agenticark"
	"github.com/cloudwego/eino-ext/components/model/agenticdeepseek"
	"github.com/cloudwego/eino-ext/components/model/agenticqwen"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	claudeschema "github.com/cloudwego/eino/schema/claude"
	geminischema "github.com/cloudwego/eino/schema/gemini"
	openaischema "github.com/cloudwego/eino/schema/openai"
)

// isolatedEnv sets HOME to a temp directory and clears all environment
// variables that NewChatModel / ResolveConfig consult for provider, model,
// API key, or base URL resolution. This keeps missing-key tests from passing
// because of ambient credentials on the developer machine.
func isolatedEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, k := range []string{
		"PROV",
		"PROV_MODEL",
		"PROV_API_KEY",
		"PROV_BASE_URL",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_BASE_URL",
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
		"GOOGLE_API_KEY",
		"GOOGLE_BASE_URL",
		"GEMINI_API_KEY",
		"DEEPSEEK_API_KEY",
		"DEEPSEEK_BASE_URL",
		"DASHSCOPE_API_KEY",
		"QWEN_API_KEY",
		"QWEN_BASE_URL",
		"ARK_API_KEY",
		"ARK_BASE_URL",
	} {
		t.Setenv(k, "")
	}
}

func TestNewChatModelUnknownProvider(t *testing.T) {
	ctx := context.Background()
	_, err := NewChatModel(ctx, Config{Provider: "unknown"})
	if err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestNewChatModelMissingAPIKey(t *testing.T) {
	// Clear env vars so we test the "no key" path, not an ambient PROV_API_KEY.
	isolatedEnv(t)
	ctx := context.Background()
	providers := []Provider{
		ProviderAgenticDeepSeek,
		ProviderAgenticClaude,
		ProviderAgenticGemini,
		ProviderAgenticOpenAI,
		ProviderAgenticArk,
		ProviderAgenticQwen,
	}
	for _, p := range providers {
		t.Run(string(p), func(t *testing.T) {
			_, err := NewChatModel(ctx, Config{Provider: p, APIKey: ""})
			if err == nil {
				t.Errorf("expected error for %q without API key, got nil", p)
			}
		})
	}
}

func TestNewChatModelDefaultProvider(t *testing.T) {
	isolatedEnv(t)
	ctx := context.Background()
	// Default is agenticdeepseek — should fail without API key
	_, err := NewChatModel(ctx, Config{Model: "test-model"})
	if err == nil {
		t.Error("expected error without API key")
	}
	t.Logf("expected error: %v", err)
}

func TestAgenticMessageConversion(t *testing.T) {
	// Round-trip: Message → AgenticMessage → Message
	original := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.System, Content: "be helpful"},
	}

	agentic, err := messagesToAgentic(original)
	if err != nil {
		t.Fatal(err)
	}
	if len(agentic) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(agentic))
	}
	if agentic[0].Role != schema.AgenticRoleTypeUser {
		t.Errorf("expected user role, got %q", agentic[0].Role)
	}
	if agentic[1].Role != schema.AgenticRoleTypeSystem {
		t.Errorf("expected system role, got %q", agentic[1].Role)
	}

	// Convert back
	result := agenticToMessage(agentic[0])
	if result.Role != schema.User {
		t.Errorf("expected user role, got %q", result.Role)
	}
}

func TestAgenticFinishReasonBridge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		message *schema.AgenticMessage
		want    string
	}{
		{
			name: "claude stop reason",
			message: &schema.AgenticMessage{ResponseMeta: &schema.AgenticResponseMeta{
				ClaudeExtension: &claudeschema.ResponseMetaExtension{StopReason: "tool_use"},
			}},
			want: "tool_use",
		},
		{
			name: "gemini finish reason",
			message: &schema.AgenticMessage{ResponseMeta: &schema.AgenticResponseMeta{
				GeminiExtension: &geminischema.ResponseMetaExtension{FinishReason: "MAX_TOKENS"},
			}},
			want: "MAX_TOKENS",
		},
		{
			name: "openai completed",
			message: &schema.AgenticMessage{ResponseMeta: &schema.AgenticResponseMeta{
				OpenAIExtension: &openaischema.ResponseMetaExtension{Status: openaischema.ResponseStatusCompleted},
			}},
			want: "stop",
		},
		{
			name: "openai incomplete max output",
			message: &schema.AgenticMessage{ResponseMeta: &schema.AgenticResponseMeta{
				OpenAIExtension: &openaischema.ResponseMetaExtension{
					Status:            openaischema.ResponseStatusIncomplete,
					IncompleteDetails: &openaischema.IncompleteDetails{Reason: "max_output_tokens"},
				},
			}},
			want: "max_output_tokens",
		},
		{
			name: "ark incomplete",
			message: &schema.AgenticMessage{ResponseMeta: &schema.AgenticResponseMeta{
				Extension: &agenticark.ResponseMetaExtension{
					Status:            agenticark.ResponseStatusIncomplete,
					IncompleteDetails: &agenticark.IncompleteDetails{Reason: "max_output_tokens"},
				},
			}},
			want: "max_output_tokens",
		},
		{
			name:    "deepseek finish reason",
			message: &schema.AgenticMessage{Extra: map[string]any{"provider_meta": &agenticdeepseek.ResponseMetaExtension{FinishReason: "length"}}},
			want:    "length",
		},
		{
			name:    "qwen finish reason",
			message: &schema.AgenticMessage{Extra: map[string]any{"provider_meta": &agenticqwen.ResponseMetaExtension{FinishReason: "stop"}}},
			want:    "stop",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := agenticFinishReason(tt.message); got != tt.want {
				t.Fatalf("finish reason = %q, want %q", got, tt.want)
			}
			legacy := legacyResponseMeta(tt.message)
			if legacy == nil || legacy.FinishReason != tt.want {
				t.Fatalf("legacy response meta = %#v, want finish reason %q", legacy, tt.want)
			}
		})
	}
}

type metadataOnlyAgenticModel struct {
	message *schema.AgenticMessage
}

func (m *metadataOnlyAgenticModel) Generate(context.Context, []*schema.AgenticMessage, ...model.Option) (*schema.AgenticMessage, error) {
	return m.message, nil
}

func (m *metadataOnlyAgenticModel) Stream(context.Context, []*schema.AgenticMessage, ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	return schema.StreamReaderFromArray([]*schema.AgenticMessage{m.message}), nil
}

func TestAgenticChatModelPreservesMetadataOnlyTerminalChunk(t *testing.T) {
	t.Parallel()

	wrapped := wrapAgenticModel(&metadataOnlyAgenticModel{message: &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ResponseMeta: &schema.AgenticResponseMeta{OpenAIExtension: &openaischema.ResponseMetaExtension{
			Status:            openaischema.ResponseStatusIncomplete,
			IncompleteDetails: &openaischema.IncompleteDetails{Reason: "max_output_tokens"},
		}},
	}})
	stream, err := wrapped.Stream(context.Background(), nil)
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	message, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv returned error: %v", err)
	}
	if message == nil || message.ResponseMeta == nil || message.ResponseMeta.FinishReason != "max_output_tokens" {
		t.Fatalf("message = %#v, want metadata-only truncation terminal", message)
	}
	_, err = stream.Recv()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("second Recv error = %v, want EOF", err)
	}
}

func TestToolCallAccumulatorAssemblesDeltas(t *testing.T) {
	acc := newToolCallAccumulator()

	// Chunk 1: tool call announced with name and ID, empty arguments
	chunk1 := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			{
				Type: schema.ContentBlockTypeFunctionToolCall,
				FunctionToolCall: &schema.FunctionToolCall{
					CallID:    "call_123",
					Name:      "Read",
					Arguments: "",
				},
				StreamingMeta: &schema.StreamingMeta{Index: 0},
			},
		},
	}
	msg1 := acc.convertChunk(chunk1)
	if len(msg1.ToolCalls) != 1 {
		t.Fatalf("chunk1: expected 1 tool call, got %d", len(msg1.ToolCalls))
	}
	if msg1.ToolCalls[0].Function.Name != "Read" {
		t.Errorf("chunk1: expected name Read, got %q", msg1.ToolCalls[0].Function.Name)
	}
	if msg1.ToolCalls[0].Function.Arguments != "{}" {
		t.Errorf("chunk1: expected args {}, got %q", msg1.ToolCalls[0].Function.Arguments)
	}

	// Chunk 2: argument delta only
	chunk2 := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			{
				Type: schema.ContentBlockTypeFunctionToolCall,
				FunctionToolCall: &schema.FunctionToolCall{
					Arguments: `{"file_`,
				},
				StreamingMeta: &schema.StreamingMeta{Index: 0},
			},
		},
	}
	msg2 := acc.convertChunk(chunk2)
	if len(msg2.ToolCalls) != 1 {
		t.Fatalf("chunk2: expected 1 tool call, got %d", len(msg2.ToolCalls))
	}
	if msg2.ToolCalls[0].Function.Arguments != `{"file_` {
		t.Errorf("chunk2: expected accumulated args, got %q", msg2.ToolCalls[0].Function.Arguments)
	}
	if msg2.ToolCalls[0].Function.Name != "Read" {
		t.Errorf("chunk2: expected name Read carried forward, got %q", msg2.ToolCalls[0].Function.Name)
	}
	if msg2.ToolCalls[0].ID != "call_123" {
		t.Errorf("chunk2: expected ID call_123 carried forward, got %q", msg2.ToolCalls[0].ID)
	}

	// Chunk 3: final argument delta
	chunk3 := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			{
				Type: schema.ContentBlockTypeFunctionToolCall,
				FunctionToolCall: &schema.FunctionToolCall{
					Arguments: `path": "/tmp/foo"}`,
				},
				StreamingMeta: &schema.StreamingMeta{Index: 0},
			},
		},
	}
	msg3 := acc.convertChunk(chunk3)
	if len(msg3.ToolCalls) != 1 {
		t.Fatalf("chunk3: expected 1 tool call, got %d", len(msg3.ToolCalls))
	}
	if msg3.ToolCalls[0].Function.Arguments != `{"file_path": "/tmp/foo"}` {
		t.Errorf("chunk3: expected full accumulated args, got %q", msg3.ToolCalls[0].Function.Arguments)
	}
}

func TestToolCallAccumulatorMultipleTools(t *testing.T) {
	acc := newToolCallAccumulator()

	// Two tool calls at different indices in the same chunk
	chunk := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			{
				Type: schema.ContentBlockTypeFunctionToolCall,
				FunctionToolCall: &schema.FunctionToolCall{
					CallID: "call_1", Name: "Read", Arguments: "",
				},
				StreamingMeta: &schema.StreamingMeta{Index: 0},
			},
			{
				Type: schema.ContentBlockTypeFunctionToolCall,
				FunctionToolCall: &schema.FunctionToolCall{
					CallID: "call_2", Name: "Bash", Arguments: "",
				},
				StreamingMeta: &schema.StreamingMeta{Index: 1},
			},
		},
	}
	msg := acc.convertChunk(chunk)
	if len(msg.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(msg.ToolCalls))
	}

	// Delta for tool at index 1 only
	delta := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			{
				Type: schema.ContentBlockTypeFunctionToolCall,
				FunctionToolCall: &schema.FunctionToolCall{
					Arguments: `{"command": "ls"}`,
				},
				StreamingMeta: &schema.StreamingMeta{Index: 1},
			},
		},
	}
	msg2 := acc.convertChunk(delta)
	if len(msg2.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call in delta chunk, got %d", len(msg2.ToolCalls))
	}
	if msg2.ToolCalls[0].Function.Name != "Bash" {
		t.Errorf("expected Bash, got %q", msg2.ToolCalls[0].Function.Name)
	}
	if msg2.ToolCalls[0].Function.Arguments != `{"command": "ls"}` {
		t.Errorf("expected accumulated args, got %q", msg2.ToolCalls[0].Function.Arguments)
	}
}

func TestToolCallAccumulatorPreservesTextAndReasoning(t *testing.T) {
	acc := newToolCallAccumulator()

	chunk := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			{
				Type:             schema.ContentBlockTypeAssistantGenText,
				AssistantGenText: &schema.AssistantGenText{Text: "hello"},
			},
			{
				Type:      schema.ContentBlockTypeReasoning,
				Reasoning: &schema.Reasoning{Text: "thinking"},
			},
		},
	}
	msg := acc.convertChunk(chunk)
	if msg.Content != "hello" {
		t.Errorf("expected content hello, got %q", msg.Content)
	}
	if msg.ReasoningContent != "thinking" {
		t.Errorf("expected reasoning thinking, got %q", msg.ReasoningContent)
	}
}

func TestStreamConvertsAccumulatedToolCalls(t *testing.T) {
	acc := newToolCallAccumulator()

	chunks := []*schema.AgenticMessage{
		{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				{
					Type:             schema.ContentBlockTypeAssistantGenText,
					AssistantGenText: &schema.AssistantGenText{Text: "I'll read that file."},
				},
			},
		},
		{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				{
					Type: schema.ContentBlockTypeFunctionToolCall,
					FunctionToolCall: &schema.FunctionToolCall{
						CallID: "call_abc", Name: "Read", Arguments: "",
					},
					StreamingMeta: &schema.StreamingMeta{Index: 0},
				},
			},
		},
		{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				{
					Type: schema.ContentBlockTypeFunctionToolCall,
					FunctionToolCall: &schema.FunctionToolCall{
						Arguments: `{"file_path":`,
					},
					StreamingMeta: &schema.StreamingMeta{Index: 0},
				},
			},
		},
		{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				{
					Type: schema.ContentBlockTypeFunctionToolCall,
					FunctionToolCall: &schema.FunctionToolCall{
						Arguments: ` "/etc/hosts"}`,
					},
					StreamingMeta: &schema.StreamingMeta{Index: 0},
				},
			},
		},
	}

	var lastToolArgs string
	for _, chunk := range chunks {
		msg := acc.convertChunk(chunk)
		if len(msg.ToolCalls) > 0 {
			lastToolArgs = msg.ToolCalls[0].Function.Arguments
		}
	}

	expected := `{"file_path": "/etc/hosts"}`
	if lastToolArgs != expected {
		t.Errorf("expected final accumulated args %q, got %q", expected, lastToolArgs)
	}
}

func TestAssistantReasoningRoundTripPreservesMultiContent(t *testing.T) {
	original := []*schema.Message{{
		Role:    schema.Assistant,
		Content: "Final answer",
		AssistantGenMultiContent: []schema.MessageOutputPart{
			{
				Type: schema.ChatMessagePartTypeReasoning,
				Reasoning: &schema.MessageOutputReasoning{
					Text:      "think",
					Signature: "sig_123",
				},
				StreamingMeta: &schema.MessageStreamingMeta{Index: 0},
			},
			{
				Type:          schema.ChatMessagePartTypeText,
				Text:          "Final answer",
				StreamingMeta: &schema.MessageStreamingMeta{Index: 1},
			},
		},
		ToolCalls: []schema.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "Bash",
				Arguments: `{"command":"pwd"}`,
			},
		}},
	}}

	agentic, err := messagesToAgentic(original)
	if err != nil {
		t.Fatal(err)
	}
	if len(agentic) != 1 {
		t.Fatalf("expected 1 agentic message, got %d", len(agentic))
	}

	roundTrip := agenticToMessage(agentic[0])
	if roundTrip.ReasoningContent != "think" {
		t.Fatalf("expected reasoning content to survive round trip, got %q", roundTrip.ReasoningContent)
	}
	if len(roundTrip.AssistantGenMultiContent) != 2 {
		t.Fatalf("expected 2 assistant output parts, got %d", len(roundTrip.AssistantGenMultiContent))
	}
	if roundTrip.AssistantGenMultiContent[0].Type != schema.ChatMessagePartTypeReasoning {
		t.Fatalf("expected first output part to be reasoning, got %q", roundTrip.AssistantGenMultiContent[0].Type)
	}
	if roundTrip.AssistantGenMultiContent[0].Reasoning == nil || roundTrip.AssistantGenMultiContent[0].Reasoning.Signature != "sig_123" {
		t.Fatalf("expected reasoning signature to survive round trip, got %#v", roundTrip.AssistantGenMultiContent[0].Reasoning)
		return
	}
	if roundTrip.AssistantGenMultiContent[1].Type != schema.ChatMessagePartTypeText || roundTrip.AssistantGenMultiContent[1].Text != "Final answer" {
		t.Fatalf("expected text output part to survive round trip, got %#v", roundTrip.AssistantGenMultiContent[1])
	}
	if len(roundTrip.ToolCalls) != 1 || roundTrip.ToolCalls[0].Function.Arguments != `{"command":"pwd"}` {
		t.Fatalf("expected tool call to survive round trip, got %#v", roundTrip.ToolCalls)
	}
}

func TestMessagesToAgenticDoesNotTrustPersistedAssistantExtra(t *testing.T) {
	original := &schema.Message{
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
		Extra: map[string]any{
			"openai-generated": true,
			"gemini-generated": true,
			"provider-private": "untrusted",
		},
	}

	converted, err := messagesToAgentic([]*schema.Message{original})
	if err != nil {
		t.Fatal(err)
	}
	if len(converted) != 1 {
		t.Fatalf("expected one assistant message, got %d", len(converted))
	}
	if converted[0].Extra != nil {
		t.Fatalf("persisted assistant Extra reached adapter input: %#v", converted[0].Extra)
	}
	if len(converted[0].ContentBlocks) != 2 ||
		converted[0].ContentBlocks[0].Type != schema.ContentBlockTypeReasoning ||
		converted[0].ContentBlocks[0].Reasoning == nil ||
		converted[0].ContentBlocks[0].Reasoning.Signature != "private-signature" ||
		converted[0].ContentBlocks[1].Type !=
			schema.ContentBlockTypeAssistantGenText ||
		converted[0].ContentBlocks[1].AssistantGenText.Text != "public answer" {
		t.Fatalf("assistant content changed before adapter policy: %#v", converted[0])
	}
	if original.Extra["openai-generated"] != true ||
		original.Extra["provider-private"] != "untrusted" {
		t.Fatalf("conversion mutated caller metadata: %#v", original.Extra)
	}
}

func TestNormalizeAgenticOptionsConvertsGenericToolChoice(t *testing.T) {
	opts := normalizeAgenticOptions([]model.Option{model.WithToolChoice(schema.ToolChoiceForced)})
	common := model.GetCommonOptions(nil, opts...)
	if common.ToolChoice != nil {
		t.Fatalf("expected generic tool-choice option to be replaced for agentic providers, got %#v", common.ToolChoice)
		return
	}
	if common.AgenticToolChoice == nil || common.AgenticToolChoice.Type != schema.ToolChoiceForced {
		t.Fatalf("expected forced agentic tool choice, got %#v", common.AgenticToolChoice)
		return
	}
	if len(common.AllowedToolNames) != 0 {
		t.Fatalf("expected allowed tool names to be cleared after conversion, got %#v", common.AllowedToolNames)
	}
}

func TestNormalizeAgenticOptionsConvertsNamedToolChoiceConstraint(t *testing.T) {
	opts := normalizeAgenticOptions([]model.Option{model.WithToolChoice(schema.ToolChoiceForced, "explain_command")})
	common := model.GetCommonOptions(nil, opts...)
	if common.AgenticToolChoice == nil || common.AgenticToolChoice.Type != schema.ToolChoiceForced {
		t.Fatalf("expected forced agentic tool choice, got %#v", common.AgenticToolChoice)
		return
	}
	if common.AgenticToolChoice.Forced == nil || len(common.AgenticToolChoice.Forced.Tools) != 1 {
		t.Fatalf("expected single forced allowed-tool entry, got %#v", common.AgenticToolChoice)
		return
	}
	if common.AgenticToolChoice.Forced.Tools[0].FunctionName != "explain_command" {
		t.Fatalf("expected forced tool explain_command, got %#v", common.AgenticToolChoice.Forced.Tools[0])
	}
	if common.ToolChoice != nil || len(common.AllowedToolNames) != 0 {
		t.Fatalf("expected chat-model tool-choice options to be stripped after conversion, got toolChoice=%#v allowed=%#v", common.ToolChoice, common.AllowedToolNames)
		return
	}
}

func TestNormalizeAgenticOptionsPreservesExistingAgenticToolChoice(t *testing.T) {
	want := &schema.AgenticToolChoice{Type: schema.ToolChoiceAllowed}
	opts := normalizeAgenticOptions([]model.Option{
		model.WithToolChoice(schema.ToolChoiceForced, "ignored"),
		model.WithAgenticToolChoice(want),
	})
	common := model.GetCommonOptions(nil, opts...)
	if common.AgenticToolChoice != want {
		t.Fatalf("expected existing agentic tool choice to win, got %#v", common.AgenticToolChoice)
	}
	if common.ToolChoice == nil || *common.ToolChoice != schema.ToolChoiceForced {
		t.Fatalf("expected original chat-model tool choice to remain when agentic choice already exists, got %#v", common.ToolChoice)
		return
	}
}
