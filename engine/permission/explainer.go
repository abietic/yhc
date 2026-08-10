package permission

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/abietic/yhc/engine/execution"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const (
	explainCommandToolName          = "explain_command"
	permissionExplainerQuerySource  = "permission_explainer"
	permissionExplainerSystemPrompt = "Analyze shell commands and explain what they do, why you're running them, and potential risks."
	defaultExplainerMaxTokens       = 1024
	recentAssistantContextChars     = 1000
	recentAssistantContextMessages  = 3
)

// RiskLevel is the structured risk bucket returned by the permission explainer.
type RiskLevel string

const (
	RiskLevelLow    RiskLevel = "LOW"
	RiskLevelMedium RiskLevel = "MEDIUM"
	RiskLevelHigh   RiskLevel = "HIGH"
)

// Explanation is the structured permission-explainer output.
type Explanation struct {
	RiskLevel   RiskLevel `json:"riskLevel"`
	Explanation string    `json:"explanation"`
	Reasoning   string    `json:"reasoning"`
	Risk        string    `json:"risk"`
}

// GenerateExplanationParams holds the narrow request shape for permission-explainer side queries.
type GenerateExplanationParams struct {
	ChatModel       model.BaseChatModel
	ProviderUsage   execution.ProviderUsageAdmitter
	Model           string
	ToolName        string
	ToolInput       any
	ToolDescription string
	Messages        []*schema.Message
	MaxOutputTokens *int
}

// GenerateExplanation runs the first migrated permission-explainer helper.
// This is a narrow Go port of the reference helper: it formats command context,
// forces a structured-output tool call, and parses the returned tool arguments.
func GenerateExplanation(ctx context.Context, params GenerateExplanationParams) (*Explanation, error) {
	if params.ChatModel == nil {
		return nil, fmt.Errorf("permission explainer: chat model is required")
	}
	if strings.TrimSpace(params.ToolName) == "" {
		return nil, fmt.Errorf("permission explainer: tool name is required")
	}

	maxTokens := defaultExplainerMaxTokens
	if params.MaxOutputTokens != nil && *params.MaxOutputTokens > 0 {
		maxTokens = *params.MaxOutputTokens
	}

	var logicalRoundID string
	if params.ProviderUsage != nil {
		logicalRoundID = params.ProviderUsage.NewLogicalRoundID()
	}
	assistantMessage, err := execution.SideQueryWithRetry(ctx, params.ChatModel, execution.SideQueryOptions{
		SystemPrompt:        permissionExplainerSystemPrompt,
		Messages:            []*schema.Message{{Role: schema.User, Content: buildExplanationPrompt(params)}},
		Tools:               []*schema.ToolInfo{explainCommandToolInfo()},
		Model:               params.Model,
		ForcedToolName:      explainCommandToolName,
		MaxOutputTokens:     &maxTokens,
		QuerySource:         permissionExplainerQuerySource,
		ProviderUsage:       params.ProviderUsage,
		UsageLogicalRoundID: logicalRoundID,
	}, nil)
	if err != nil {
		return nil, err
	}

	return parseExplanationMessage(assistantMessage)
}

func explainCommandToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: explainCommandToolName,
		Desc: "Provide an explanation of a shell command",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"explanation": {
				Type:     schema.String,
				Desc:     "What this command does (1-2 sentences)",
				Required: true,
			},
			"reasoning": {
				Type:     schema.String,
				Desc:     `Why YOU are running this command. Start with "I" - e.g. "I need to check the file contents"`,
				Required: true,
			},
			"risk": {
				Type:     schema.String,
				Desc:     "What could go wrong, under 15 words",
				Required: true,
			},
			"riskLevel": {
				Type:     schema.String,
				Enum:     []string{string(RiskLevelLow), string(RiskLevelMedium), string(RiskLevelHigh)},
				Desc:     "LOW (safe dev workflows), MEDIUM (recoverable changes), HIGH (dangerous/irreversible)",
				Required: true,
			},
		}),
	}
}

func buildExplanationPrompt(params GenerateExplanationParams) string {
	var builder strings.Builder
	builder.WriteString("Tool: ")
	builder.WriteString(strings.TrimSpace(params.ToolName))
	builder.WriteString("\n")
	if desc := strings.TrimSpace(params.ToolDescription); desc != "" {
		builder.WriteString("Description: ")
		builder.WriteString(desc)
		builder.WriteString("\n")
	}
	builder.WriteString("Input:\n")
	builder.WriteString(formatToolInput(params.ToolInput))
	if contextText := extractConversationContext(params.Messages, recentAssistantContextChars); contextText != "" {
		builder.WriteString("\nRecent conversation context:\n")
		builder.WriteString(contextText)
	}
	builder.WriteString("\n\nExplain this command in context.")
	return builder.String()
}

func formatToolInput(input any) string {
	if text, ok := input.(string); ok {
		return text
	}
	encoded, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return fmt.Sprint(input)
	}
	return string(encoded)
}

func extractConversationContext(messages []*schema.Message, maxChars int) string {
	if maxChars <= 0 || len(messages) == 0 {
		return ""
	}

	assistantMessages := make([]*schema.Message, 0, recentAssistantContextMessages)
	for i := len(messages) - 1; i >= 0 && len(assistantMessages) < recentAssistantContextMessages; i-- {
		if messages[i] == nil || messages[i].Role != schema.Assistant {
			continue
		}
		assistantMessages = append(assistantMessages, messages[i])
	}
	if len(assistantMessages) == 0 {
		return ""
	}

	parts := make([]string, 0, len(assistantMessages))
	totalChars := 0
	for i := len(assistantMessages) - 1; i >= 0; i-- {
		text := strings.TrimSpace(assistantContextText(assistantMessages[i]))
		if text == "" || totalChars >= maxChars {
			continue
		}
		remaining := maxChars - totalChars
		if len(text) > remaining {
			text = text[:remaining] + "..."
		}
		parts = append(parts, text)
		totalChars += len(text)
	}
	return strings.Join(parts, "\n\n")
}

func assistantContextText(msg *schema.Message) string {
	if msg == nil {
		return ""
	}
	if text := strings.TrimSpace(msg.Content); text != "" {
		return text
	}
	return strings.TrimSpace(msg.ReasoningContent)
}

func parseExplanationMessage(msg *schema.Message) (*Explanation, error) {
	if msg == nil {
		return nil, fmt.Errorf("permission explainer: missing assistant response")
	}
	for _, toolCall := range msg.ToolCalls {
		if strings.TrimSpace(toolCall.Function.Name) != explainCommandToolName {
			continue
		}
		return parseExplanationArguments(toolCall.Function.Arguments)
	}
	return nil, fmt.Errorf("permission explainer: side query returned no %s tool call", explainCommandToolName)
}

func parseExplanationArguments(raw string) (*Explanation, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	var explanation Explanation
	if err := json.Unmarshal([]byte(raw), &explanation); err != nil {
		return nil, fmt.Errorf("permission explainer: decode structured output: %w", err)
	}
	if explanation.RiskLevel != RiskLevelLow && explanation.RiskLevel != RiskLevelMedium && explanation.RiskLevel != RiskLevelHigh {
		return nil, fmt.Errorf("permission explainer: invalid risk level %q", explanation.RiskLevel)
	}
	if strings.TrimSpace(explanation.Explanation) == "" {
		return nil, fmt.Errorf("permission explainer: missing explanation")
	}
	if strings.TrimSpace(explanation.Reasoning) == "" {
		return nil, fmt.Errorf("permission explainer: missing reasoning")
	}
	if strings.TrimSpace(explanation.Risk) == "" {
		return nil, fmt.Errorf("permission explainer: missing risk")
	}
	return &explanation, nil
}
