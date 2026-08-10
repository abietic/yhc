package permission

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type explainerCaptureModel struct {
	streamMessages []*schema.Message
	streamOptions  []model.Option
	boundTools     []*schema.ToolInfo
	response       []*schema.Message
}

func (m *explainerCaptureModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *explainerCaptureModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.streamMessages = append([]*schema.Message(nil), input...)
	m.streamOptions = append([]model.Option(nil), opts...)
	return schema.StreamReaderFromArray(m.response), nil
}

func (m *explainerCaptureModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.boundTools = append([]*schema.ToolInfo(nil), tools...)
	return m, nil
}

func TestGenerateExplanationUsesForcedStructuredToolCall(t *testing.T) {
	chatModel := &explainerCaptureModel{response: []*schema.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_explain_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name: explainCommandToolName,
				},
			}},
		},
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_explain_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      explainCommandToolName,
					Arguments: `{"riskLevel":"LOW","explanation":"Prints the current working directory.","reasoning":"I need to verify where I am in the repo.","risk":"May expose filesystem paths"}`,
				},
			}},
		},
	}}

	explanation, err := GenerateExplanation(context.Background(), GenerateExplanationParams{
		ChatModel:       chatModel,
		Model:           "main-loop-model",
		ToolName:        "Bash",
		ToolDescription: "Executes a shell command.",
		ToolInput: map[string]any{
			"command": "pwd",
		},
		Messages: []*schema.Message{
			{Role: schema.Assistant, Content: "assistant-1 older"},
			{Role: schema.User, Content: "user turn"},
			{Role: schema.Assistant, Content: "assistant-2 recent"},
			{Role: schema.Assistant, Content: "assistant-3 newer"},
			{Role: schema.Assistant, Content: "assistant-4 newest"},
		},
	})
	if err != nil {
		t.Fatalf("GenerateExplanation returned error: %v", err)
		return
	}
	if explanation == nil {
		t.Fatal("expected explanation result")
		return
	}
	if explanation.RiskLevel != RiskLevelLow {
		t.Fatalf("expected LOW risk level, got %#v", explanation)
	}
	if explanation.Explanation != "Prints the current working directory." {
		t.Fatalf("unexpected explanation: %#v", explanation)
	}
	if explanation.Reasoning != "I need to verify where I am in the repo." {
		t.Fatalf("unexpected reasoning: %#v", explanation)
	}
	if explanation.Risk != "May expose filesystem paths" {
		t.Fatalf("unexpected risk: %#v", explanation)
	}

	if len(chatModel.boundTools) != 1 || chatModel.boundTools[0].Name != explainCommandToolName {
		t.Fatalf("expected explainer tool to be bound, got %#v", chatModel.boundTools)
	}
	if len(chatModel.streamMessages) != 2 {
		t.Fatalf("expected system prompt plus user prompt, got %#v", chatModel.streamMessages)
	}
	if chatModel.streamMessages[0].Role != schema.System || chatModel.streamMessages[0].Content != permissionExplainerSystemPrompt {
		t.Fatalf("unexpected system prompt: %#v", chatModel.streamMessages[0])
	}

	prompt := chatModel.streamMessages[1].Content
	for _, want := range []string{
		"Tool: Bash",
		"Description: Executes a shell command.",
		"Input:",
		`"command": "pwd"`,
		"Recent conversation context:",
		"assistant-2 recent",
		"assistant-3 newer",
		"assistant-4 newest",
		"Explain this command in context.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q, got %q", want, prompt)
		}
	}
	if strings.Contains(prompt, "assistant-1 older") {
		t.Fatalf("expected prompt to keep only the last three assistant messages, got %q", prompt)
	}

	common := model.GetCommonOptions(nil, chatModel.streamOptions...)
	if common.Model == nil || *common.Model != "main-loop-model" {
		t.Fatalf("expected helper model override, got %#v", common.Model)
		return
	}
	if common.MaxTokens == nil || *common.MaxTokens != defaultExplainerMaxTokens {
		t.Fatalf("expected default explainer max tokens, got %#v", common.MaxTokens)
		return
	}
	if common.ToolChoice == nil || *common.ToolChoice != schema.ToolChoiceForced {
		t.Fatalf("expected forced tool choice, got %#v", common.ToolChoice)
		return
	}
	if len(common.AllowedToolNames) != 1 || common.AllowedToolNames[0] != explainCommandToolName {
		t.Fatalf("expected named tool forcing for explainer tool, got %#v", common.AllowedToolNames)
	}
}

func TestGenerateExplanationRejectsInvalidStructuredOutput(t *testing.T) {
	chatModel := &explainerCaptureModel{response: []*schema.Message{{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:   "call_explain_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      explainCommandToolName,
				Arguments: `{"riskLevel":"SEVERE","explanation":"oops","reasoning":"I guessed","risk":"unknown"}`,
			},
		}},
	}}}

	explanation, err := GenerateExplanation(context.Background(), GenerateExplanationParams{
		ChatModel: chatModel,
		ToolName:  "Bash",
		ToolInput: map[string]any{"command": "pwd"},
	})
	if err == nil {
		t.Fatal("expected invalid structured output error")
		return
	}
	if explanation != nil {
		t.Fatalf("expected nil explanation on invalid output, got %#v", explanation)
		return
	}
	if !strings.Contains(err.Error(), "invalid risk level") {
		t.Fatalf("unexpected error: %v", err)
	}
}
