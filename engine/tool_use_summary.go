package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/abietic/yhc/engine/execution"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// toolUseSummarySystemPrompt is the system prompt for summary generation.
// Mirrors reference toolUseSummaryGenerator.ts.
const toolUseSummarySystemPrompt = `Write a short summary label describing what these tool calls accomplished. It appears as a single-line row in a mobile app and truncates around 30 characters, so think git-commit-subject, not sentence.

Keep the verb in past tense and the most distinctive noun. Drop articles, connectors, and long location context first.

Examples:
- Searched in auth/
- Fixed NPE in UserService
- Created signup endpoint
- Read config.json
- Ran failing tests`

// generateToolUseSummaryAsync fires an async side-query to generate a tool use summary.
// Returns a promise that will be resolved when the summary is ready.
// Mirrors reference query.ts:1468-1481 and toolUseSummaryGenerator.ts.
func generateToolUseSummaryAsync(
	ctx context.Context,
	summaryModel model.BaseChatModel,
	summaryCall *modelCallIdentity,
	toolUseBlocks []*schema.ToolCall,
	toolResults []*schema.Message,
	assistantMessages []*schema.Message,
	deps *QueryDeps,
) *ToolUseSummaryPromise {
	// Collect tool use IDs
	toolUseIDs := make([]string, 0, len(toolUseBlocks))
	for _, tc := range toolUseBlocks {
		if tc != nil {
			toolUseIDs = append(toolUseIDs, tc.ID)
		}
	}

	promise := NewToolUseSummaryPromise(toolUseIDs)
	var providerUsage execution.ProviderUsageAdmitter
	if deps != nil {
		providerUsage = deps.ProviderUsage
	}
	var logicalRoundID string
	if providerUsage != nil {
		logicalRoundID = providerUsage.NewLogicalRoundID()
	}

	go func() {
		summary := generateToolUseSummaryWithCall(
			ctx,
			summaryModel,
			summaryCall,
			toolUseBlocks,
			toolResults,
			assistantMessages,
			providerUsage,
			logicalRoundID,
		)
		promise.Resolve(summary)
	}()

	return promise
}

// generateToolUseSummary performs the synchronous model call to generate a summary.
func generateToolUseSummary(
	ctx context.Context,
	summaryModel model.BaseChatModel,
	toolUseBlocks []*schema.ToolCall,
	toolResults []*schema.Message,
	assistantMessages []*schema.Message,
	providerUsage execution.ProviderUsageAdmitter,
) string {
	var logicalRoundID string
	if providerUsage != nil {
		logicalRoundID = providerUsage.NewLogicalRoundID()
	}
	return generateToolUseSummaryWithCall(
		ctx,
		summaryModel,
		nil,
		toolUseBlocks,
		toolResults,
		assistantMessages,
		providerUsage,
		logicalRoundID,
	)
}

func generateToolUseSummaryWithCall(
	ctx context.Context,
	summaryModel model.BaseChatModel,
	summaryCall *modelCallIdentity,
	toolUseBlocks []*schema.ToolCall,
	toolResults []*schema.Message,
	assistantMessages []*schema.Message,
	providerUsage execution.ProviderUsageAdmitter,
	logicalRoundID string,
) string {
	if summaryModel == nil {
		return ""
	}

	// Build result lookup
	resultByID := make(map[string]string, len(toolResults))
	for _, tr := range toolResults {
		if tr != nil && tr.ToolCallID != "" {
			resultByID[tr.ToolCallID] = truncateString(tr.Content, 300)
		}
	}

	// Extract last assistant text for context
	lastAssistantText := ""
	for i := len(assistantMessages) - 1; i >= 0; i-- {
		if assistantMessages[i] != nil && assistantMessages[i].Content != "" {
			lastAssistantText = truncateString(assistantMessages[i].Content, 200)
			break
		}
	}

	// Build user prompt
	var sb strings.Builder
	if lastAssistantText != "" {
		sb.WriteString("User's intent (from assistant's last message): ")
		sb.WriteString(lastAssistantText)
		sb.WriteString("\n\n")
	}
	sb.WriteString("Tools completed:\n\n")
	for _, tc := range toolUseBlocks {
		if tc == nil {
			continue
		}
		fmt.Fprintf(&sb, "Tool: %s\n", tc.Function.Name)
		fmt.Fprintf(&sb, "Input: %s\n", truncateString(tc.Function.Arguments, 300))
		output := resultByID[tc.ID]
		if output == "" {
			output = "(no output)"
		}
		fmt.Fprintf(&sb, "Output: %s\n\n", output)
	}
	sb.WriteString("Label:")

	// Use a short timeout for the summary call
	summaryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	sideQuery := execution.SideQueryOptions{
		SystemPrompt: toolUseSummarySystemPrompt,
		Messages: []*schema.Message{
			{Role: schema.User, Content: sb.String()},
		},
		QuerySource:         "tool_use_summary_generation",
		ProviderUsage:       providerUsage,
		UsageLogicalRoundID: logicalRoundID,
	}
	if summaryCall != nil {
		sideQuery.Model = summaryCall.Selector
		sideQuery.Provider = summaryCall.Provider
		sideQuery.ModelRole = summaryCall.Role
		sideQuery.ModelProfile = summaryCall.Profile
		sideQuery.EffortValue = summaryCall.Reasoning
	}
	result, err := execution.SideQueryWithRetry(
		summaryCtx,
		summaryModel,
		sideQuery,
		nil,
	)
	if err != nil {
		return ""
	}
	if result == nil {
		return ""
	}
	return strings.TrimSpace(result.Content)
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
