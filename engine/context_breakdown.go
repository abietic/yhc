package engine

import (
	"fmt"
	"math"
	"strings"

	"github.com/abietic/yhc/engine/compact"
	modelcaps "github.com/abietic/yhc/engine/model"
	"github.com/cloudwego/eino/schema"
)

// ContextCategory represents a category of context usage with its token estimate.
type ContextCategory struct {
	// Name is the human-readable category name.
	Name string
	// Tokens is the estimated token count for this category.
	Tokens int
	// Percentage is the percentage of total context window used by this category.
	Percentage float64
}

// ContextBreakdown holds the per-category token breakdown of the context window.
type ContextBreakdown struct {
	// Model is the currently active model name.
	Model string
	// MaxContextTokens is the model's maximum context window size.
	MaxContextTokens int
	// TotalTokens is the estimated total tokens currently used.
	TotalTokens int
	// UsagePercent is the overall context usage percentage (0-100).
	UsagePercent int
	// AvailableTokens is the remaining context capacity.
	AvailableTokens int
	// Categories is the per-category breakdown, ordered for display.
	Categories []ContextCategory
}

// GetContextBreakdown returns a per-category breakdown of context window usage.
// Categories are determined by message role:
//   - "System/Instructions": system messages (system prompts, AGENTS.md, project instructions)
//   - "User messages": user-role messages (excluding tool results)
//   - "Assistant messages": assistant-role messages (excluding tool calls)
//   - "Tool calls": tool call content from assistant messages
//   - "Tool results": tool-role messages (results returned from tool execution)
//   - "Other": anything that doesn't fit the above (e.g., compaction summaries)
//
// Token estimation uses the same heuristic as compact.EstimateTokenCount (chars/4).
func (e *QueryEngine) GetContextBreakdown() *ContextBreakdown {
	e.mu.Lock()
	msgs := make([]*schema.Message, len(e.messages))
	copy(msgs, e.messages)
	model := e.config.Model
	e.mu.Unlock()

	// Get model context window
	maxContext := modelcaps.ContextWindow(model)
	if maxContext <= 0 {
		maxContext = 200000 // sensible fallback
	}

	// Category accumulators
	var systemTokens, userTokens, assistantTokens, toolCallTokens, toolResultTokens, otherTokens int

	for _, msg := range msgs {
		if msg == nil {
			continue
		}
		switch msg.Role {
		case schema.System:
			systemTokens += estimateSingleMessageTokens(msg)
		case schema.User:
			userTokens += estimateSingleMessageTokens(msg)
		case schema.Assistant:
			// Separate tool call tokens from assistant text tokens
			textTokens, callTokens := estimateAssistantMessageTokens(msg)
			assistantTokens += textTokens
			toolCallTokens += callTokens
		case schema.Tool:
			toolResultTokens += estimateSingleMessageTokens(msg)
		default:
			otherTokens += estimateSingleMessageTokens(msg)
		}
	}

	// Add a baseline system prompt estimate (the system prompt itself is not in messages
	// but is always present). Use a conservative estimate based on typical system prompt size.
	// This accounts for the system prompt, AGENTS.md, and other injected instructions.
	systemPromptEstimate := e.estimateSystemPromptTokens()
	systemTokens += systemPromptEstimate

	totalTokens := systemTokens + userTokens + assistantTokens + toolCallTokens + toolResultTokens + otherTokens
	availableTokens := maxContext - totalTokens
	if availableTokens < 0 {
		availableTokens = 0
	}

	usagePercent := 0
	if maxContext > 0 {
		usagePercent = totalTokens * 100 / maxContext
		if usagePercent > 100 {
			usagePercent = 100
		}
	}

	// Build categories (only include non-zero categories)
	var categories []ContextCategory
	addCategory := func(name string, tokens int) {
		if tokens > 0 {
			pct := float64(tokens) * 100.0 / float64(maxContext)
			categories = append(categories, ContextCategory{
				Name:       name,
				Tokens:     tokens,
				Percentage: pct,
			})
		}
	}

	addCategory("System/Instructions", systemTokens)
	addCategory("User messages", userTokens)
	addCategory("Assistant messages", assistantTokens)
	addCategory("Tool calls", toolCallTokens)
	addCategory("Tool results", toolResultTokens)
	addCategory("Other", otherTokens)

	return &ContextBreakdown{
		Model:            model,
		MaxContextTokens: maxContext,
		TotalTokens:      totalTokens,
		UsagePercent:     usagePercent,
		AvailableTokens:  availableTokens,
		Categories:       categories,
	}
}

// estimateSystemPromptTokens returns a rough estimate of the system prompt token overhead.
// The system prompt is not stored in messages but is always sent with every API call.
func (e *QueryEngine) estimateSystemPromptTokens() int {
	e.mu.Lock()
	customSP := e.config.CustomSystemPrompt
	appendSP := e.config.AppendSystemPrompt
	e.mu.Unlock()

	// Combine custom and append prompts for estimation
	combined := customSP + appendSP
	if combined == "" {
		// Default system prompt is typically ~800-1200 tokens.
		// Use a conservative estimate.
		return 1000
	}
	// Base prompt overhead + custom content
	return 1000 + roughTokenEstimate(combined)
}

// estimateSingleMessageTokens estimates tokens for a single message using the same
// heuristic as compact.estimateMessageTokens.
func estimateSingleMessageTokens(msg *schema.Message) int {
	return compact.EstimateTokenCount([]*schema.Message{msg})
}

// estimateAssistantMessageTokens splits an assistant message into text tokens
// and tool call tokens.
func estimateAssistantMessageTokens(msg *schema.Message) (textTokens, toolCallTokens int) {
	if msg == nil {
		return 0, 0
	}

	// Base overhead per message
	textTokens = 8

	// Text content
	textTokens += roughTokenEstimate(msg.Content)
	textTokens += roughTokenEstimate(msg.ReasoningContent)

	// Multi-content
	if len(msg.MultiContent) > 0 {
		textTokens += len(msg.MultiContent) * 32
	}
	if len(msg.AssistantGenMultiContent) > 0 {
		textTokens += len(msg.AssistantGenMultiContent) * 32
	}

	// Tool calls
	for _, tc := range msg.ToolCalls {
		toolCallTokens += 12 // per-call overhead
		toolCallTokens += roughTokenEstimate(tc.ID)
		toolCallTokens += roughTokenEstimate(tc.Type)
		toolCallTokens += roughTokenEstimate(tc.Function.Name)
		toolCallTokens += roughTokenEstimate(tc.Function.Arguments)
	}

	return textTokens, toolCallTokens
}

// roughTokenEstimate estimates token count from a string using chars/4 heuristic.
func roughTokenEstimate(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	return int(math.Ceil(float64(len(text)) / 4.0))
}

// GetContextBreakdownFormatted returns a pre-formatted string showing the
// per-category context window usage breakdown. This is the interface-friendly
// method used by the /context command to avoid circular package imports.
func (e *QueryEngine) GetContextBreakdownFormatted() string {
	return FormatContextBreakdown(e.GetContextBreakdown())
}

// FormatContextBreakdown formats a ContextBreakdown as a human-readable string
// suitable for display in the TUI as a system message.
func FormatContextBreakdown(b *ContextBreakdown) string {
	if b == nil {
		return "No context breakdown available."
	}

	var sb strings.Builder
	border := strings.Repeat("\u2500", 48)

	fmt.Fprintf(&sb, "\u2500\u2500\u2500 Context Usage %s\n", border[:33])
	sb.WriteString("\n")

	// Model and summary line
	fmt.Fprintf(&sb, "  Model: %s\n", b.Model)
	fmt.Fprintf(&sb, "  Context: %s / %s tokens (%d%%)\n",
		formatTokenCount(b.TotalTokens),
		formatTokenCount(b.MaxContextTokens),
		b.UsagePercent)
	sb.WriteString("\n")

	// Category header
	fmt.Fprintf(&sb, "  %-22s %8s %5s  %s\n", "Category", "Tokens", "%", "Usage")
	fmt.Fprintf(&sb, "  %s\n", strings.Repeat("\u2500", 55))

	// Category rows
	for _, cat := range b.Categories {
		bar := renderBar(cat.Percentage, 10)
		fmt.Fprintf(&sb, "  %-22s %8s %4.0f%%  %s\n",
			cat.Name,
			formatTokenCount(cat.Tokens),
			cat.Percentage,
			bar)
	}

	fmt.Fprintf(&sb, "  %s\n", strings.Repeat("\u2500", 55))

	// Total and available
	fmt.Fprintf(&sb, "  %-22s %8s / %s (%d%%)\n",
		"Total",
		formatTokenCount(b.TotalTokens),
		formatTokenCount(b.MaxContextTokens),
		b.UsagePercent)
	fmt.Fprintf(&sb, "  %-22s %8s tokens\n",
		"Available",
		formatTokenCount(b.AvailableTokens))

	sb.WriteString("\n")
	fmt.Fprintf(&sb, "%s\n", border)

	return sb.String()
}

// renderBar creates a simple bar visualization using Unicode block characters.
// percentage is 0-100, width is the number of character positions for the bar.
func renderBar(percentage float64, width int) string {
	if width <= 0 {
		width = 10
	}
	filled := int(math.Round(percentage / 100.0 * float64(width)))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	empty := width - filled
	return strings.Repeat("\u2588", filled) + strings.Repeat("\u2591", empty)
}

// formatTokenCount formats a token count with comma separators for readability.
func formatTokenCount(n int) string {
	if n < 0 {
		return "-" + formatTokenCount(-n)
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}

	// Insert commas from the right
	var result strings.Builder
	remainder := len(s) % 3
	if remainder > 0 {
		result.WriteString(s[:remainder])
		if len(s) > remainder {
			result.WriteString(",")
		}
	}
	for i := remainder; i < len(s); i += 3 {
		if i > remainder {
			result.WriteString(",")
		}
		result.WriteString(s[i : i+3])
	}
	return result.String()
}
