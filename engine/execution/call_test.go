package execution

import (
	"context"
	"encoding/json"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type captureCallOptionsModel struct {
	streamMessages []*schema.Message
	streamOptions  []model.Option
	boundTools     []*schema.ToolInfo
}

func (m *captureCallOptionsModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *captureCallOptionsModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.streamMessages = append([]*schema.Message(nil), input...)
	m.streamOptions = append([]model.Option(nil), opts...)
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}), nil
}

func (m *captureCallOptionsModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.boundTools = append([]*schema.ToolInfo(nil), tools...)
	return m, nil
}

func TestCallModelForwardsModelAndMaxTokensIntoStreamOptions(t *testing.T) {
	chatModel := &captureCallOptionsModel{}
	maxTokens := 2048
	messages := []*schema.Message{{Role: schema.User, Content: "hello"}}
	systemPrompt := &schema.Message{Role: schema.System, Content: "You are helpful."}
	tools := []*schema.ToolInfo{{Name: "Bash"}}

	result, err := CallModel(context.Background(), chatModel, messages, nil, tools, CallModelOptions{
		SystemPrompt:    systemPrompt,
		Model:           "fallback-model",
		MaxOutputTokens: &maxTokens,
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}
	if result == nil || result.StreamReader == nil {
		t.Fatal("expected stream reader result")
		return
	}
	if result.Model != "fallback-model" {
		t.Fatalf("expected result model to mirror call options, got %q", result.Model)
	}
	if len(chatModel.boundTools) != 1 || chatModel.boundTools[0].Name != "Bash" {
		t.Fatalf("expected tools to be bound before streaming, got %#v", chatModel.boundTools)
	}
	if len(chatModel.streamMessages) != 2 {
		t.Fatalf("expected system prompt plus user message, got %d messages", len(chatModel.streamMessages))
	}
	if chatModel.streamMessages[0].Role != schema.System || chatModel.streamMessages[0].Content != "You are helpful." {
		t.Fatalf("expected first streamed message to be the effective system prompt, got %#v", chatModel.streamMessages[0])
	}

	common := model.GetCommonOptions(nil, chatModel.streamOptions...)
	if common.Model == nil || *common.Model != "fallback-model" {
		t.Fatalf("expected model option to be forwarded, got %#v", common.Model)
		return
	}
	if common.MaxTokens == nil || *common.MaxTokens != 2048 {
		t.Fatalf("expected max-tokens option to be forwarded, got %#v", common.MaxTokens)
		return
	}
}

func TestCallModelOmitsEmptyModelAndUnsetMaxTokens(t *testing.T) {
	chatModel := &captureCallOptionsModel{}
	_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, nil, CallModelOptions{
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		Model:        "   ",
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}

	common := model.GetCommonOptions(nil, chatModel.streamOptions...)
	if common.Model != nil {
		t.Fatalf("expected empty model name to be omitted, got %#v", common.Model)
		return
	}
	if common.MaxTokens != nil {
		t.Fatalf("expected unset max tokens to be omitted, got %#v", common.MaxTokens)
		return
	}
}

func TestCallModelForwardsGenericToolChoiceIntoStreamOptions(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    schema.ToolChoice
		present bool
	}{
		{name: "forced", raw: "forced", want: schema.ToolChoiceForced, present: true},
		{name: "required alias", raw: "required", want: schema.ToolChoiceForced, present: true},
		{name: "allowed alias", raw: "auto", want: schema.ToolChoiceAllowed, present: true},
		{name: "forbidden alias", raw: "none", want: schema.ToolChoiceForbidden, present: true},
		{name: "unknown omitted", raw: "explain_command", present: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chatModel := &captureCallOptionsModel{}
			_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, []*schema.ToolInfo{{Name: "Bash"}}, CallModelOptions{
				SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
				ToolChoice:   tc.raw,
			})
			if err != nil {
				t.Fatalf("CallModel returned error: %v", err)
				return
			}

			common := model.GetCommonOptions(nil, chatModel.streamOptions...)
			if !tc.present {
				if common.ToolChoice != nil {
					t.Fatalf("expected tool choice to be omitted, got %#v", *common.ToolChoice)
					return
				}
				return
			}
			if common.ToolChoice == nil || *common.ToolChoice != tc.want {
				t.Fatalf("expected tool choice %q, got %#v", tc.want, common.ToolChoice)
				return
			}
		})
	}
}

func TestCallModelForwardsForcedToolNameIntoStreamOptions(t *testing.T) {
	chatModel := &captureCallOptionsModel{}
	_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, []*schema.ToolInfo{{Name: "Bash"}, {Name: "Read"}}, CallModelOptions{
		SystemPrompt:   &schema.Message{Role: schema.System, Content: "system"},
		ToolChoice:     "allowed",
		ForcedToolName: " Read ",
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}

	common := model.GetCommonOptions(nil, chatModel.streamOptions...)
	if common.ToolChoice == nil || *common.ToolChoice != schema.ToolChoiceForced {
		t.Fatalf("expected forced tool choice when a named tool is provided, got %#v", common.ToolChoice)
		return
	}
	if len(common.AllowedToolNames) != 1 || common.AllowedToolNames[0] != "Read" {
		t.Fatalf("expected named tool forcing to constrain allowed tools, got %#v", common.AllowedToolNames)
	}
}

func TestCallModelDoesNotSetAllowedToolNamesForGenericToolChoice(t *testing.T) {
	chatModel := &captureCallOptionsModel{}
	_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, []*schema.ToolInfo{{Name: "Bash"}}, CallModelOptions{
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		ToolChoice:   "forced",
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}

	common := model.GetCommonOptions(nil, chatModel.streamOptions...)
	if len(common.AllowedToolNames) != 0 {
		t.Fatalf("expected generic tool choice to omit allowed-tool-name constraints, got %#v", common.AllowedToolNames)
	}
}

func TestCallModelForwardsTaskBudgetIntoClaudeSpecificOptions(t *testing.T) {
	chatModel := &captureCallOptionsModel{}
	remaining := 1536

	_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, nil, CallModelOptions{
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		TaskBudget: &TaskBudget{
			Total:     4096,
			Remaining: &remaining,
		},
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}

	common := model.GetCommonOptions(nil, chatModel.streamOptions...)
	if common.Model != nil || common.MaxTokens != nil || common.ToolChoice != nil || common.AgenticToolChoice != nil {
		t.Fatalf("expected task-budget forwarding to use impl-specific provider options only, got common=%#v", common)
		return
	}

	var headers map[string]string
	var extraFields map[string]any
	for _, opt := range chatModel.streamOptions {
		name, gotHeaders, gotExtraFields, ok := extractClaudeImplSpecificPayload(opt)
		if !ok {
			continue
		}
		if strings.Contains(name, "WithCustomHeaders") {
			headers = gotHeaders
		}
		if strings.Contains(name, "WithExtraFields") {
			extraFields = gotExtraFields
		}
	}

	if headers == nil {
		t.Fatal("expected Claude custom-header option to be forwarded")
		return
	}
	if headers[claudeAnthropicBetaHeader] != claudeTaskBudgetsBetaHeader {
		t.Fatalf("expected task-budget beta header %q, got %#v", claudeTaskBudgetsBetaHeader, headers)
	}

	if extraFields == nil {
		t.Fatal("expected Claude extra-fields option to be forwarded")
		return
	}
	want := buildClaudeExtraFields(CallModelOptions{
		TaskBudget: &TaskBudget{Total: 4096, Remaining: &remaining},
	})
	if !reflect.DeepEqual(extraFields, want) {
		t.Fatalf("expected task-budget extra fields %#v, got %#v", want, extraFields)
	}
	if got := extraFields["output_config"]; !reflect.DeepEqual(got, want["output_config"]) {
		t.Fatalf("expected output_config %#v, got %#v", want["output_config"], got)
	}
}

func TestCallModelForwardsClaudeMetadataIntoExtraFields(t *testing.T) {
	t.Setenv("CLAUDE_CODE_EXTRA_METADATA", `{"device_id":"device-1","account_uuid":"acct-9"}`)

	chatModel := &captureCallOptionsModel{}
	_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, nil, CallModelOptions{
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		SessionID:    "session-123",
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}

	_, _, extraFields, ok := findClaudeExtraFields(chatModel.streamOptions)
	if !ok {
		t.Fatal("expected Claude extra-fields option to be forwarded")
	}

	metadata, ok := extraFields["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("expected metadata object in extra fields, got %#v", extraFields)
	}
	encoded, ok := metadata["user_id"].(string)
	if !ok {
		t.Fatalf("expected metadata.user_id string, got %#v", metadata)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(encoded), &payload); err != nil {
		t.Fatalf("expected metadata.user_id to be JSON, got error: %v", err)
		return
	}
	if payload["session_id"] != "session-123" {
		t.Fatalf("expected metadata session_id, got %#v", payload)
	}
	if payload["device_id"] != "device-1" || payload["account_uuid"] != "acct-9" {
		t.Fatalf("expected extra metadata values to be preserved, got %#v", payload)
	}
}

func TestCallModelMergesClaudeMetadataAndTaskBudgetExtraFields(t *testing.T) {
	t.Setenv("CLAUDE_CODE_EXTRA_METADATA", `{"device_id":"device-merged"}`)

	chatModel := &captureCallOptionsModel{}
	remaining := 1000
	_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, nil, CallModelOptions{
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		SessionID:    "session-merged",
		TaskBudget: &TaskBudget{
			Total:     4096,
			Remaining: &remaining,
		},
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}

	_, headers, extraFields, ok := findClaudeExtraFields(chatModel.streamOptions)
	if !ok {
		t.Fatal("expected merged Claude extra-fields option to be forwarded")
	}
	if headers[claudeAnthropicBetaHeader] != claudeTaskBudgetsBetaHeader {
		t.Fatalf("expected task-budget beta header %q, got %#v", claudeTaskBudgetsBetaHeader, headers)
	}
	if _, ok := extraFields["metadata"]; !ok {
		t.Fatalf("expected metadata in merged extra fields, got %#v", extraFields)
	}
	outputConfig, ok := extraFields["output_config"].(map[string]any)
	if !ok {
		t.Fatalf("expected output_config in merged extra fields, got %#v", extraFields)
	}
	taskBudget, ok := outputConfig["task_budget"].(map[string]any)
	if !ok {
		t.Fatalf("expected task_budget in merged extra fields, got %#v", outputConfig)
	}
	if taskBudget["total"] != 4096 || taskBudget["remaining"] != remaining {
		t.Fatalf("expected merged task budget values, got %#v", taskBudget)
	}
}

func TestBuildClaudeTaskBudgetExtraFieldsOmitsRemainingWhenUnset(t *testing.T) {
	got := buildClaudeTaskBudgetExtraFields(&TaskBudget{Total: 2048})
	want := map[string]any{
		"output_config": map[string]any{
			"task_budget": map[string]any{
				"type":  "tokens",
				"total": 2048,
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %#v, got %#v", want, got)
	}
}

func TestBuildClaudeMetadataOmitsInvalidExtraMetadataJSON(t *testing.T) {
	t.Setenv("CLAUDE_CODE_EXTRA_METADATA", `[]`)

	got := buildClaudeMetadata("session-invalid")
	metadata, ok := got["user_id"].(string)
	if !ok {
		t.Fatalf("expected metadata.user_id string, got %#v", got)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(metadata), &payload); err != nil {
		t.Fatalf("expected metadata.user_id to be JSON, got error: %v", err)
		return
	}
	if len(payload) != 1 || payload["session_id"] != "session-invalid" {
		t.Fatalf("expected only session_id in payload, got %#v", payload)
	}
}

func TestMergeExtraFieldsRecursivelyMergesNestedMaps(t *testing.T) {
	got := mergeExtraFields(
		map[string]any{"output_config": map[string]any{"task_budget": map[string]any{"total": 1}}},
		map[string]any{"output_config": map[string]any{"thinking": map[string]any{"type": "enabled"}}},
	)
	want := map[string]any{
		"output_config": map[string]any{
			"task_budget": map[string]any{"total": 1},
			"thinking":    map[string]any{"type": "enabled"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %#v, got %#v", want, got)
	}
}

func TestCallModelForwardsThinkingAdaptiveIntoExtraFields(t *testing.T) {
	chatModel := &captureCallOptionsModel{}
	_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, nil, CallModelOptions{
		SystemPrompt:   &schema.Message{Role: schema.System, Content: "system"},
		ThinkingConfig: &ThinkingConfig{Type: "adaptive"},
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}

	_, _, extraFields, ok := findClaudeExtraFields(chatModel.streamOptions)
	if !ok {
		t.Fatal("expected Claude extra-fields option to be forwarded")
	}
	thinking, ok := extraFields["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("expected thinking object in extra fields, got %#v", extraFields)
	}
	if thinking["type"] != "adaptive" {
		t.Fatalf("expected thinking type adaptive, got %#v", thinking)
	}
	if _, hasBudget := thinking["budget_tokens"]; hasBudget {
		t.Fatalf("expected no budget_tokens for adaptive mode, got %#v", thinking)
	}
}

func TestCallModelForwardsThinkingEnabledWithBudgetIntoExtraFields(t *testing.T) {
	chatModel := &captureCallOptionsModel{}
	budget := 8192
	_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, nil, CallModelOptions{
		SystemPrompt:   &schema.Message{Role: schema.System, Content: "system"},
		ThinkingConfig: &ThinkingConfig{Type: "enabled", BudgetTokens: &budget},
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}

	_, _, extraFields, ok := findClaudeExtraFields(chatModel.streamOptions)
	if !ok {
		t.Fatal("expected Claude extra-fields option to be forwarded")
	}
	thinking, ok := extraFields["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("expected thinking object in extra fields, got %#v", extraFields)
	}
	if thinking["type"] != "enabled" {
		t.Fatalf("expected thinking type enabled, got %#v", thinking)
	}
	if thinking["budget_tokens"] != 8192 {
		t.Fatalf("expected budget_tokens 8192, got %#v", thinking)
	}
}

func TestCallModelOmitsThinkingForDisabledMode(t *testing.T) {
	chatModel := &captureCallOptionsModel{}
	_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, nil, CallModelOptions{
		SystemPrompt:   &schema.Message{Role: schema.System, Content: "system"},
		ThinkingConfig: &ThinkingConfig{Type: "disabled"},
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}

	_, _, extraFields, ok := findClaudeExtraFields(chatModel.streamOptions)
	if ok && extraFields != nil {
		if _, hasThinking := extraFields["thinking"]; hasThinking {
			t.Fatalf("expected no thinking field for disabled mode, got %#v", extraFields)
			return
		}
	}
}

func TestCallModelOmitsThinkingWhenConfigNil(t *testing.T) {
	chatModel := &captureCallOptionsModel{}
	_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, nil, CallModelOptions{
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}

	_, _, extraFields, ok := findClaudeExtraFields(chatModel.streamOptions)
	if ok && extraFields != nil {
		if _, hasThinking := extraFields["thinking"]; hasThinking {
			t.Fatalf("expected no thinking field when config is nil, got %#v", extraFields)
			return
		}
	}
}

func TestCallModelForwardsEffortValueIntoExtraFields(t *testing.T) {
	chatModel := &captureCallOptionsModel{}
	_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, nil, CallModelOptions{
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		EffortValue:  "high",
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}

	_, _, extraFields, ok := findClaudeExtraFields(chatModel.streamOptions)
	if !ok {
		t.Fatal("expected Claude extra-fields option to be forwarded")
	}
	outputConfig, ok := extraFields["output_config"].(map[string]any)
	if !ok {
		t.Fatalf("expected output_config in extra fields, got %#v", extraFields)
	}
	if outputConfig["effort"] != "high" {
		t.Fatalf("expected effort=high in output_config, got %#v", outputConfig)
	}
}

func TestCallModelOmitsEffortWhenEmpty(t *testing.T) {
	chatModel := &captureCallOptionsModel{}
	_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, nil, CallModelOptions{
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		EffortValue:  "",
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}

	_, _, extraFields, ok := findClaudeExtraFields(chatModel.streamOptions)
	if ok && extraFields != nil {
		if outputConfig, hasOC := extraFields["output_config"]; hasOC {
			if oc, ok := outputConfig.(map[string]any); ok {
				if _, hasEffort := oc["effort"]; hasEffort {
					t.Fatalf("expected no effort field when value is empty, got %#v", extraFields)
				}
			}
		}
	}
}

func TestCallModelEffortCoexistsWithTaskBudget(t *testing.T) {
	chatModel := &captureCallOptionsModel{}
	total := 50000
	remaining := 40000
	_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, nil, CallModelOptions{
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		TaskBudget:   &TaskBudget{Total: total, Remaining: &remaining},
		EffortValue:  "low",
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}

	_, _, extraFields, ok := findClaudeExtraFields(chatModel.streamOptions)
	if !ok {
		t.Fatal("expected Claude extra-fields option to be forwarded")
	}
	outputConfig, ok := extraFields["output_config"].(map[string]any)
	if !ok {
		t.Fatalf("expected output_config in extra fields, got %#v", extraFields)
	}
	// Both effort and task_budget should coexist in output_config
	if outputConfig["effort"] != "low" {
		t.Fatalf("expected effort=low, got %#v", outputConfig)
	}
	if outputConfig["task_budget"] == nil {
		t.Fatalf("expected task_budget to coexist with effort, got %#v", outputConfig)
		return
	}
}

func TestCallModelLowersProviderReasoningEffortThroughTypedOptions(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		effort       string
		typeFragment string
		field        string
		want         string
	}{
		{
			name:         "openai xhigh",
			provider:     providerAgenticOpenAI,
			effort:       "xhigh",
			typeFragment: "agenticopenai.options",
			field:        "reasoning",
			want:         "xhigh",
		},
		{
			name:         "ark minimal",
			provider:     providerAgenticArk,
			effort:       "minimal",
			typeFragment: "agenticark.arkOptions",
			field:        "reasoning",
			want:         "minimal",
		},
		{
			name:         "gemini high",
			provider:     providerAgenticGemini,
			effort:       "high",
			typeFragment: "agenticgemini.options",
			field:        "ThinkingConfig",
			want:         "HIGH",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chatModel := &captureCallOptionsModel{}
			_, err := CallModel(
				context.Background(),
				chatModel,
				[]*schema.Message{{Role: schema.User, Content: "hello"}},
				nil,
				nil,
				CallModelOptions{
					Provider:    tc.provider,
					EffortValue: tc.effort,
				},
			)
			if err != nil {
				t.Fatalf("CallModel returned error: %v", err)
			}
			got, ok := extractReasoningOptionValue(
				chatModel.streamOptions,
				tc.typeFragment,
				tc.field,
			)
			if !ok {
				t.Fatalf(
					"expected typed %s option in %#v",
					tc.typeFragment,
					chatModel.streamOptions,
				)
			}
			if got != tc.want {
				t.Fatalf("expected typed effort %q, got %q", tc.want, got)
			}
			if _, _, _, ok := findClaudeExtraFields(chatModel.streamOptions); ok {
				t.Fatal("non-Claude effort must not use Claude extra fields")
			}
		})
	}
}

func TestCallModelRejectsUnsupportedEffortBeforeProviderUse(t *testing.T) {
	for _, tc := range []struct {
		provider string
		effort   string
	}{
		{provider: providerAgenticDeepSeek, effort: "high"},
		{provider: providerAgenticQwen, effort: "low"},
		{provider: providerAgenticGemini, effort: "medium"},
		{provider: providerAgenticArk, effort: "xhigh"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			chatModel := &captureCallOptionsModel{}
			usage := &countingProviderUsageAdmitter{}
			_, err := CallModel(
				context.Background(),
				chatModel,
				[]*schema.Message{{Role: schema.User, Content: "hello"}},
				nil,
				nil,
				CallModelOptions{
					Provider:            tc.provider,
					EffortValue:         tc.effort,
					ProviderUsage:       usage,
					UsageLogicalRoundID: "round-1",
				},
			)
			if err == nil {
				t.Fatal("expected unsupported effort error")
			}
			if usage.admissions != 0 {
				t.Fatalf("expected zero usage admissions, got %d", usage.admissions)
			}
			if len(chatModel.streamOptions) != 0 {
				t.Fatalf("expected zero provider calls, got %#v", chatModel.streamOptions)
			}
		})
	}
}

func TestCallModelAttributesFixedRoleAndProfileToProviderUsage(t *testing.T) {
	chatModel := &captureCallOptionsModel{}
	usage := &countingProviderUsageAdmitter{}
	_, err := CallModel(
		context.Background(),
		chatModel,
		[]*schema.Message{{Role: schema.User, Content: "hello"}},
		nil,
		nil,
		CallModelOptions{
			Model:               "secondary",
			Provider:            providerAgenticOpenAI,
			ModelRole:           "explore",
			ModelProfile:        "secondary",
			EffortValue:         "high",
			ProviderUsage:       usage,
			UsageLogicalRoundID: "round-1",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if usage.admissions != 1 ||
		usage.descriptor.LogicalRoundID != "round-1" ||
		usage.descriptor.ModelRole != "explore" ||
		usage.descriptor.ModelProfile != "secondary" ||
		usage.descriptor.ReasoningEffort != "high" {
		t.Fatalf("usage descriptor = %#v", usage.descriptor)
	}
}

type countingProviderUsageAdmitter struct {
	admissions int
	descriptor ProviderUsageDescriptor
}

func (a *countingProviderUsageAdmitter) NewLogicalRoundID() string {
	return "round"
}

func (a *countingProviderUsageAdmitter) AdmitProviderUsage(
	_ context.Context,
	descriptor ProviderUsageDescriptor,
) (ProviderUsageCall, error) {
	a.admissions++
	a.descriptor = descriptor
	return countingProviderUsageCall{}, nil
}

type countingProviderUsageCall struct{}

func (countingProviderUsageCall) ProviderCallID() string { return "counting-call" }
func (countingProviderUsageCall) CompleteProviderUsage(*schema.TokenUsage) error {
	return nil
}

func (countingProviderUsageCall) ReleaseProviderUsageBeforeDispatch() error {
	return nil
}

func (countingProviderUsageCall) MarkProviderUsageAmbiguous(error) error {
	return nil
}

func extractReasoningOptionValue(
	options []model.Option,
	typeFragment string,
	fieldName string,
) (string, bool) {
	for _, option := range options {
		payload, ok := extractImplSpecificPayload(option, typeFragment)
		if !ok {
			continue
		}
		field := payload.FieldByName(fieldName)
		if !field.IsValid() {
			continue
		}
		field = reflect.NewAt(
			field.Type(),
			unsafe.Pointer(field.UnsafeAddr()),
		).Elem()
		if field.IsNil() {
			continue
		}
		value := field.Elem()
		switch fieldName {
		case "ThinkingConfig":
			level := value.FieldByName("ThinkingLevel")
			return level.String(), true
		default:
			effort := value.FieldByName("Effort")
			if effort.Kind() == reflect.String {
				return effort.String(), true
			}
			if method := effort.MethodByName("String"); method.IsValid() {
				result := method.Call(nil)
				if len(result) == 1 {
					return result[0].String(), true
				}
			}
			if effort.CanInterface() {
				if text, ok := effort.Interface().(interface{ String() string }); ok {
					return text.String(), true
				}
			}
		}
	}
	return "", false
}

func extractImplSpecificPayload(
	option model.Option,
	typeFragment string,
) (reflect.Value, bool) {
	field := reflect.ValueOf(&option).Elem().FieldByName("implSpecificOptFn")
	if !field.IsValid() {
		return reflect.Value{}, false
	}
	field = reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
	if field.IsNil() {
		return reflect.Value{}, false
	}
	fnValue := reflect.ValueOf(field.Interface())
	fnType := fnValue.Type()
	if fnType.Kind() != reflect.Func ||
		fnType.NumIn() != 1 ||
		!strings.Contains(fnType.In(0).String(), typeFragment) {
		return reflect.Value{}, false
	}
	arg := reflect.New(fnType.In(0).Elem())
	fnValue.Call([]reflect.Value{arg})
	return arg.Elem(), true
}

func TestCallModelForwardsFastModeAsSpeedField(t *testing.T) {
	chatModel := &captureCallOptionsModel{}
	_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, nil, CallModelOptions{
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		FastMode:     true,
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}

	_, _, extraFields, ok := findClaudeExtraFields(chatModel.streamOptions)
	if !ok {
		t.Fatal("expected Claude extra-fields option to be forwarded")
	}
	if extraFields["speed"] != "fast" {
		t.Fatalf("expected speed=fast in extra fields, got %#v", extraFields)
	}
}

func TestCallModelOmitsSpeedWhenFastModeFalse(t *testing.T) {
	chatModel := &captureCallOptionsModel{}
	_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, nil, CallModelOptions{
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		FastMode:     false,
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}

	_, _, extraFields, ok := findClaudeExtraFields(chatModel.streamOptions)
	if ok && extraFields != nil {
		if _, hasSpeed := extraFields["speed"]; hasSpeed {
			t.Fatalf("expected no speed field when fast mode is false, got %#v", extraFields)
			return
		}
	}
}

func findClaudeExtraFields(opts []model.Option) (string, map[string]string, map[string]any, bool) {
	var headers map[string]string
	var extraFields map[string]any
	for _, opt := range opts {
		name, gotHeaders, gotExtraFields, ok := extractClaudeImplSpecificPayload(opt)
		if !ok {
			continue
		}
		if strings.Contains(name, "WithCustomHeaders") {
			headers = gotHeaders
		}
		if strings.Contains(name, "WithExtraFields") {
			extraFields = gotExtraFields
		}
	}
	if headers == nil && extraFields == nil {
		return "", nil, nil, false
	}
	return "", headers, extraFields, true
}

func extractClaudeImplSpecificPayload(opt model.Option) (string, map[string]string, map[string]any, bool) {
	field := reflect.ValueOf(&opt).Elem().FieldByName("implSpecificOptFn")
	if !field.IsValid() {
		return "", nil, nil, false
	}
	field = reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
	if field.IsNil() {
		return "", nil, nil, false
	}

	fn := field.Interface()
	fnValue := reflect.ValueOf(fn)
	fnType := fnValue.Type()
	if fnType.Kind() != reflect.Func || fnType.NumIn() != 1 || !strings.Contains(fnType.In(0).String(), "agenticclaude.claudeOptions") {
		return "", nil, nil, false
	}

	arg := reflect.New(fnType.In(0).Elem())
	fnValue.Call([]reflect.Value{arg})
	payload := arg.Elem()

	var headers map[string]string
	if headerField := payload.FieldByName("customHeaders"); headerField.IsValid() {
		headerField = reflect.NewAt(headerField.Type(), unsafe.Pointer(headerField.UnsafeAddr())).Elem()
		if !headerField.IsNil() {
			headers = headerField.Interface().(map[string]string)
		}
	}

	var extraFields map[string]any
	if extraField := payload.FieldByName("extraFields"); extraField.IsValid() {
		extraField = reflect.NewAt(extraField.Type(), unsafe.Pointer(extraField.UnsafeAddr())).Elem()
		if !extraField.IsNil() {
			extraFields = extraField.Interface().(map[string]any)
		}
	}

	name := ""
	if runtimeFn := runtime.FuncForPC(fnValue.Pointer()); runtimeFn != nil {
		name = runtimeFn.Name()
	}

	return name, headers, extraFields, true
}

func TestCallModelAlwaysSendsXAppHeader(t *testing.T) {
	chatModel := &captureCallOptionsModel{}
	_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, nil, CallModelOptions{
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}

	_, headers, _, ok := findClaudeExtraFields(chatModel.streamOptions)
	if !ok || headers == nil {
		t.Fatal("expected Claude custom-header option to be forwarded")
		return
	}
	if headers["x-app"] != "cli" {
		t.Fatalf("expected x-app=cli, got %q", headers["x-app"])
	}
}

func TestCallModelSendsSessionIDHeader(t *testing.T) {
	chatModel := &captureCallOptionsModel{}
	_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, nil, CallModelOptions{
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		SessionID:    "test-session-42",
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}

	_, headers, _, ok := findClaudeExtraFields(chatModel.streamOptions)
	if !ok || headers == nil {
		t.Fatal("expected Claude custom-header option to be forwarded")
		return
	}
	if headers["X-Claude-Code-Session-Id"] != "test-session-42" {
		t.Fatalf("expected X-Claude-Code-Session-Id=test-session-42, got %q", headers["X-Claude-Code-Session-Id"])
	}
}

func TestCallModelOmitsSessionIDHeaderWhenEmpty(t *testing.T) {
	chatModel := &captureCallOptionsModel{}
	_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, nil, CallModelOptions{
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		SessionID:    "",
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}

	_, headers, _, ok := findClaudeExtraFields(chatModel.streamOptions)
	if !ok || headers == nil {
		t.Fatal("expected Claude custom-header option to be forwarded")
		return
	}
	if _, has := headers["X-Claude-Code-Session-Id"]; has {
		t.Fatalf("expected no X-Claude-Code-Session-Id header when SessionID is empty, got %q", headers["X-Claude-Code-Session-Id"])
	}
}

func TestCallModelSendsClientRequestIDHeader(t *testing.T) {
	chatModel := &captureCallOptionsModel{}
	_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, nil, CallModelOptions{
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}

	_, headers, _, ok := findClaudeExtraFields(chatModel.streamOptions)
	if !ok || headers == nil {
		t.Fatal("expected Claude custom-header option to be forwarded")
		return
	}
	requestID := headers["x-client-request-id"]
	if requestID == "" {
		t.Fatal("expected x-client-request-id to be set")
	}
	// Validate it looks like a UUID (8-4-4-4-12 format)
	if len(requestID) != 36 || requestID[8] != '-' || requestID[13] != '-' {
		t.Fatalf("expected x-client-request-id to be a UUID, got %q", requestID)
	}
}

func TestCallModelGeneratesUniqueClientRequestIDs(t *testing.T) {
	chatModel := &captureCallOptionsModel{}

	_, _ = CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, nil, CallModelOptions{
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
	})
	_, headers1, _, _ := findClaudeExtraFields(chatModel.streamOptions)
	id1 := headers1["x-client-request-id"]

	chatModel2 := &captureCallOptionsModel{}
	_, _ = CallModel(context.Background(), chatModel2, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, nil, CallModelOptions{
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
	})
	_, headers2, _, _ := findClaudeExtraFields(chatModel2.streamOptions)
	id2 := headers2["x-client-request-id"]

	if id1 == id2 {
		t.Fatalf("expected unique x-client-request-id per call, both were %q", id1)
	}
}

func TestCallModelHeadersIncludeBetaWhenTaskBudgetSet(t *testing.T) {
	chatModel := &captureCallOptionsModel{}
	remaining := 1000
	_, err := CallModel(context.Background(), chatModel, []*schema.Message{{Role: schema.User, Content: "hello"}}, nil, nil, CallModelOptions{
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		SessionID:    "sess-1",
		TaskBudget:   &TaskBudget{Total: 2000, Remaining: &remaining},
	})
	if err != nil {
		t.Fatalf("CallModel returned error: %v", err)
		return
	}

	_, headers, _, ok := findClaudeExtraFields(chatModel.streamOptions)
	if !ok || headers == nil {
		t.Fatal("expected Claude custom-header option to be forwarded")
		return
	}
	// All attribution headers should be present alongside the beta header
	if headers["x-app"] != "cli" {
		t.Fatalf("expected x-app=cli, got %q", headers["x-app"])
	}
	if headers["X-Claude-Code-Session-Id"] != "sess-1" {
		t.Fatalf("expected session header, got %q", headers["X-Claude-Code-Session-Id"])
	}
	if headers["x-client-request-id"] == "" {
		t.Fatal("expected x-client-request-id")
	}
	if headers[claudeAnthropicBetaHeader] != claudeTaskBudgetsBetaHeader {
		t.Fatalf("expected beta header, got %q", headers[claudeAnthropicBetaHeader])
	}
}
