package compact

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
)

const (
	// CompactMaxOutputTokens limits the summarization response.
	CompactMaxOutputTokens = 8192

	// PromptTooLongErrorPrefix is the prefix returned by the API when
	// the input exceeds the model's context window.
	PromptTooLongErrorPrefix = "prompt is too long"
)

// LLMCompactOptions configures the LLM-driven compaction call.
type LLMCompactOptions struct {
	// ChatModel is the model to use for summarization.
	ChatModel model.BaseChatModel
	// ModelName identifies the model for routing purposes.
	ModelName string
	// CustomInstructions are user-supplied summarization directives.
	CustomInstructions string
	// SuppressFollowUpQuestions tells the post-compact summary to instruct
	// the model to continue without asking questions.
	SuppressFollowUpQuestions bool
	// TranscriptPath is included in the summary for reference.
	TranscriptPath string
	// IsAutoCompact distinguishes auto from manual compaction.
	IsAutoCompact bool
	// Direction controls which portion of the conversation to compact.
	// Defaults to CompactDirectionFull if empty.
	Direction CompactDirection
	// ProviderUsage is present only for an active root Goal or one exact bound
	// descendant. Non-Goal compaction retains its existing behavior.
	ProviderUsage execution.ProviderUsageAdmitter
}

// LLMCompactResult is the output of an LLM-driven compaction.
type LLMCompactResult struct {
	// Summary is the formatted summary text (analysis stripped).
	Summary string
	// RawSummary is the unprocessed model output.
	RawSummary string
	// Usage is exact provider metadata aggregated across successful PTL
	// compaction attempts. It is nil when the provider omitted usage.
	Usage *schema.TokenUsage
}

// RunLLMCompact performs LLM-driven conversation compaction.
// It sends the conversation history to a model with the compact prompt,
// sanitizes binary media first, and handles PTL retries.
// Mirrors reference compact/compact.ts compactConversation (the summarization part).
func RunLLMCompact(ctx context.Context, messages []*schema.Message, opts LLMCompactOptions) (*LLMCompactResult, error) {
	if len(messages) == 0 {
		return nil, errors.New("not enough messages to compact")
	}
	if opts.ChatModel == nil {
		return nil, errors.New("chat model is required for LLM compaction")
	}

	// Get messages after the last compact boundary (don't re-summarize old summaries)
	messagesToSummarize := GetMessagesAfterCompactBoundary(messages)

	// Replace binary media with bounded modality placeholders before summarization.
	stripped := SanitizeMediaForCompaction(messagesToSummarize)

	// Build the compact prompt with direction awareness
	direction := opts.Direction
	if direction == "" {
		direction = CompactDirectionFull
	}
	compactPrompt := GetCompactPromptWithDirection(direction, opts.CustomInstructions)

	// PTL retry loop
	var rawSummary string
	var compactUsage *schema.TokenUsage
	var ptlAttempts int
	var usageLogicalRoundID string
	if opts.ProviderUsage != nil {
		usageLogicalRoundID = opts.ProviderUsage.NewLogicalRoundID()
	}

	for {
		summary, usage, err := callCompactModel(
			ctx,
			opts.ChatModel,
			stripped,
			compactPrompt,
			opts.ModelName,
			opts.ProviderUsage,
			usageLogicalRoundID,
		)
		if err != nil {
			return nil, fmt.Errorf("compact summarization failed: %w", err)
		}
		compactUsage = addTokenUsage(compactUsage, usage)

		rawSummary = summary

		// Check if the response indicates prompt-too-long
		if !strings.HasPrefix(strings.ToLower(summary), PromptTooLongErrorPrefix) {
			break
		}

		// PTL retry: truncate oldest groups and retry
		ptlAttempts++
		if ptlAttempts > MaxPTLRetries {
			return nil, errors.New("conversation too long - compaction request itself exceeds model context window after retries")
		}

		// Estimate how much to drop (use 20% fallback since we don't parse the gap from error)
		truncated := TruncateHeadForPTLRetry(stripped, 0)
		if truncated == nil {
			return nil, errors.New("conversation too long - cannot truncate further for compaction")
		}
		stripped = truncated
	}

	if strings.TrimSpace(rawSummary) == "" {
		return nil, errors.New("compact summarization returned empty response")
	}

	return &LLMCompactResult{
		Summary:    FormatCompactSummary(rawSummary),
		RawSummary: rawSummary,
		Usage:      compactUsage,
	}, nil
}

// callCompactModel performs the actual model call for summarization.
func callCompactModel(
	ctx context.Context,
	chatModel model.BaseChatModel,
	messages []*schema.Message,
	compactPrompt string,
	modelName string,
	providerUsage execution.ProviderUsageAdmitter,
	usageLogicalRoundID string,
) (string, *schema.TokenUsage, error) {
	// Build the messages to send: conversation + compact prompt as last user message
	apiMessages := make([]*schema.Message, 0, len(messages)+1)
	apiMessages = append(apiMessages, messages...)
	apiMessages = append(apiMessages, &schema.Message{
		Role:    schema.User,
		Content: compactPrompt,
	})

	// Ensure every assistant tool_call has a matching tool result.
	// Raw conversation messages may have orphaned tool_calls from interruptions.
	apiMessages = ensureToolCallResultPairing(apiMessages)

	maxTokens := CompactMaxOutputTokens
	result, err := execution.SideQueryWithRetry(ctx, chatModel, execution.SideQueryOptions{
		SystemPrompt:        "You are a helpful AI assistant tasked with summarizing conversations.",
		Messages:            apiMessages,
		MaxOutputTokens:     &maxTokens,
		QuerySource:         "compact",
		Model:               modelName,
		ProviderUsage:       providerUsage,
		UsageLogicalRoundID: usageLogicalRoundID,
	}, nil)
	if err != nil {
		return "", nil, err
	}
	var usage *schema.TokenUsage
	if result.ResponseMeta != nil && result.ResponseMeta.Usage != nil {
		copied := *result.ResponseMeta.Usage
		usage = &copied
	}
	return strings.TrimSpace(result.Content), usage, nil
}

func addTokenUsage(total, next *schema.TokenUsage) *schema.TokenUsage {
	if next == nil {
		return total
	}
	if total == nil {
		copied := *next
		if copied.TotalTokens == 0 &&
			(copied.PromptTokens != 0 || copied.CompletionTokens != 0) {
			copied.TotalTokens = copied.PromptTokens + copied.CompletionTokens
		}
		return &copied
	}
	total.PromptTokens += next.PromptTokens
	total.CompletionTokens += next.CompletionTokens
	nextTotal := next.TotalTokens
	if nextTotal == 0 && (next.PromptTokens != 0 || next.CompletionTokens != 0) {
		nextTotal = next.PromptTokens + next.CompletionTokens
	}
	total.TotalTokens += nextTotal
	return total
}

// BuildLLMAutoCompact constructs the full AutoCompactResult using LLM summarization.
// This replaces buildDeterministicAutoCompact when a ChatModel is available.
func BuildLLMAutoCompact(ctx context.Context, messages []*schema.Message, preCompactTokens int, opts LLMCompactOptions) (*AutoCompactResult, error) {
	llmResult, err := RunLLMCompact(ctx, messages, opts)
	if err != nil {
		return nil, err
	}

	preserved := buildAutoCompactPreservedTail(messages)

	// Build the summary user message (mirrors reference getCompactUserSummaryMessage)
	summaryText := GetCompactUserSummaryMessage(llmResult.Summary, opts.SuppressFollowUpQuestions, opts.TranscriptPath, false)

	boundary := &schema.Message{
		Role:    schema.System,
		Content: "",
		Extra: map[string]any{
			"subtype":        "compact_boundary",
			"trigger":        "auto_compact",
			"usage_expected": true,
		},
		ResponseMeta: &schema.ResponseMeta{Usage: llmResult.Usage},
	}
	summaryMsg := &schema.Message{
		Role:    schema.User,
		Content: summaryText,
		Extra: map[string]any{
			"subtype": "compact_summary",
			"trigger": "auto_compact",
		},
	}

	postMessages := make([]*schema.Message, 0, 2+len(preserved))
	postMessages = append(postMessages, boundary, summaryMsg)
	postMessages = append(postMessages, preserved...)
	postCompactTokens := EstimateTokenCount(postMessages)

	return &AutoCompactResult{
		PreCompactTokenCount:      preCompactTokens,
		PostCompactTokenCount:     postCompactTokens,
		TruePostCompactTokenCount: postCompactTokens,
		CompactionUsage:           llmResult.Usage,
		BoundaryMarker:            boundary,
		SummaryMessages:           []*schema.Message{summaryMsg},
		MessagesToKeep:            preserved,
		Attachments:               nil,
		HookResults:               nil,
		Summary:                   llmResult.Summary,
	}, nil
}

// PartialCompact compacts a subset of messages around a pivot index.
// Direction controls which side gets compacted:
//   - CompactDirectionUpTo: compact messages[0:pivot], keep messages[pivot:] verbatim
//   - CompactDirectionFrom: keep messages[0:pivot], compact messages[pivot:]
//
// Mirrors the reference's partial compaction in compact/compact.ts.
func PartialCompact(ctx context.Context, messages []*schema.Message, pivot int, opts LLMCompactOptions) (*AutoCompactResult, error) {
	if pivot <= 0 || pivot >= len(messages) {
		// Invalid pivot — fall back to full compact
		opts.Direction = CompactDirectionFull
		return BuildLLMAutoCompact(ctx, messages, EstimateTokenCount(messages), opts)
	}

	preCompactTokens := EstimateTokenCount(messages)

	switch opts.Direction {
	case CompactDirectionUpTo:
		return partialCompactUpTo(ctx, messages, pivot, preCompactTokens, opts)
	case CompactDirectionFrom:
		return partialCompactFrom(ctx, messages, pivot, preCompactTokens, opts)
	default:
		opts.Direction = CompactDirectionFull
		return BuildLLMAutoCompact(ctx, messages, preCompactTokens, opts)
	}
}

// partialCompactUpTo compacts messages before the pivot; messages after pivot are kept verbatim.
func partialCompactUpTo(ctx context.Context, messages []*schema.Message, pivot, preCompactTokens int, opts LLMCompactOptions) (*AutoCompactResult, error) {
	toCompact := messages[:pivot]
	toKeep := messages[pivot:]

	opts.Direction = CompactDirectionUpTo
	llmResult, err := RunLLMCompact(ctx, toCompact, opts)
	if err != nil {
		return nil, err
	}

	summaryText := GetCompactUserSummaryMessage(llmResult.Summary, opts.SuppressFollowUpQuestions, opts.TranscriptPath, true)
	boundary := &schema.Message{
		Role:    schema.System,
		Content: "",
		Extra: map[string]any{
			"subtype":        "compact_boundary",
			"trigger":        "partial_compact_up_to",
			"usage_expected": true,
		},
		ResponseMeta: &schema.ResponseMeta{Usage: llmResult.Usage},
	}
	summaryMsg := &schema.Message{
		Role:    schema.User,
		Content: summaryText,
		Extra: map[string]any{
			"subtype": "compact_summary",
			"trigger": "partial_compact_up_to",
		},
	}

	postMessages := make([]*schema.Message, 0, 2+len(toKeep))
	postMessages = append(postMessages, boundary, summaryMsg)
	postMessages = append(postMessages, toKeep...)
	postCompactTokens := EstimateTokenCount(postMessages)

	return &AutoCompactResult{
		PreCompactTokenCount:      preCompactTokens,
		PostCompactTokenCount:     postCompactTokens,
		TruePostCompactTokenCount: postCompactTokens,
		CompactionUsage:           llmResult.Usage,
		BoundaryMarker:            boundary,
		SummaryMessages:           []*schema.Message{summaryMsg},
		MessagesToKeep:            toKeep,
		Summary:                   llmResult.Summary,
	}, nil
}

// partialCompactFrom compacts messages from the pivot onward; messages before pivot are kept.
func partialCompactFrom(ctx context.Context, messages []*schema.Message, pivot, preCompactTokens int, opts LLMCompactOptions) (*AutoCompactResult, error) {
	toKeep := messages[:pivot]
	toCompact := messages[pivot:]

	opts.Direction = CompactDirectionFrom
	llmResult, err := RunLLMCompact(ctx, toCompact, opts)
	if err != nil {
		return nil, err
	}

	summaryText := GetCompactUserSummaryMessage(llmResult.Summary, opts.SuppressFollowUpQuestions, opts.TranscriptPath, false)
	boundary := &schema.Message{
		Role:    schema.System,
		Content: "",
		Extra: map[string]any{
			"subtype":        "compact_boundary",
			"trigger":        "partial_compact_from",
			"usage_expected": true,
		},
		ResponseMeta: &schema.ResponseMeta{Usage: llmResult.Usage},
	}
	summaryMsg := &schema.Message{
		Role:    schema.User,
		Content: summaryText,
		Extra: map[string]any{
			"subtype": "compact_summary",
			"trigger": "partial_compact_from",
		},
	}

	postMessages := make([]*schema.Message, 0, len(toKeep)+2)
	postMessages = append(postMessages, toKeep...)
	postMessages = append(postMessages, boundary, summaryMsg)
	postCompactTokens := EstimateTokenCount(postMessages)

	return &AutoCompactResult{
		PreCompactTokenCount:      preCompactTokens,
		PostCompactTokenCount:     postCompactTokens,
		TruePostCompactTokenCount: postCompactTokens,
		CompactionUsage:           llmResult.Usage,
		BoundaryMarker:            boundary,
		SummaryMessages:           []*schema.Message{summaryMsg},
		MessagesToKeep:            toKeep,
		Summary:                   llmResult.Summary,
	}, nil
}

// FindPartialCompactPivot finds a good pivot point for partial compaction.
// It looks for the midpoint of the conversation, snapping to a message boundary
// between user turns (preferring to split between a tool result and the next user message).
// Returns -1 if no good pivot is found.
func FindPartialCompactPivot(messages []*schema.Message) int {
	if len(messages) < 6 {
		return -1 // too few messages for partial compact to be worthwhile
	}

	mid := len(messages) / 2

	// Search around the midpoint for a good split (user message start).
	bestPivot := -1
	bestDist := len(messages)
	for i := 1; i < len(messages)-1; i++ {
		if messages[i].Role == schema.User {
			// Skip meta/attachment messages
			if messages[i].Extra != nil {
				if _, ok := messages[i].Extra["is_meta"]; ok {
					continue
				}
			}
			dist := mid - i
			if dist < 0 {
				dist = -dist
			}
			if dist < bestDist {
				bestDist = dist
				bestPivot = i
			}
		}
	}

	return bestPivot
}

// ensureToolCallResultPairing checks that every tool call in assistant messages
// has a corresponding tool result message IMMEDIATELY following it (before any
// non-tool message). If any are missing or misplaced, synthetic placeholder
// results are inserted immediately after the assistant message's tool result group.
func ensureToolCallResultPairing(messages []*schema.Message) []*schema.Message {
	hasToolCalls := false
	for _, msg := range messages {
		if msg != nil && msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
			hasToolCalls = true
			break
		}
	}
	if !hasToolCalls {
		return messages
	}

	out := make([]*schema.Message, 0, len(messages)+4)
	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		out = append(out, msg)

		if msg.Role != schema.Assistant || len(msg.ToolCalls) == 0 {
			continue
		}

		needed := make(map[string]string, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			if tc.ID != "" {
				needed[tc.ID] = tc.Function.Name
			}
		}

		for i+1 < len(messages) && messages[i+1] != nil && messages[i+1].Role == schema.Tool {
			i++
			out = append(out, messages[i])
			delete(needed, messages[i].ToolCallID)
		}

		for _, tc := range msg.ToolCalls {
			if tc.ID == "" {
				continue
			}
			if _, still := needed[tc.ID]; !still {
				continue
			}
			out = append(out, &schema.Message{
				Role:       schema.Tool,
				Content:    "[Tool result unavailable - execution was interrupted]",
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
			})
			delete(needed, tc.ID)
		}
	}
	return out
}
