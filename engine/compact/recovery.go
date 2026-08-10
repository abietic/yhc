package compact

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// RecoveryStrategy defines how compaction failures are handled.
type RecoveryStrategy int

const (
	// RecoveryRetrySmaller retries compaction with a smaller input (truncates
	// the oldest messages to reduce size).
	RecoveryRetrySmaller RecoveryStrategy = iota

	// RecoveryDeterministic falls back to deterministic (non-LLM) compaction.
	RecoveryDeterministic

	// RecoveryPreserveOriginal abandons compaction and keeps the original messages.
	RecoveryPreserveOriginal
)

// CompactRecoveryConfig controls the failure recovery behavior.
type CompactRecoveryConfig struct {
	// MaxRetries is the maximum number of retry attempts with smaller input.
	// Default: 2 (matching reference MAX_PTL_RETRIES - 1 since first attempt
	// is the initial call).
	MaxRetries int

	// ShrinkFactor controls how much of the input is dropped on each retry.
	// 0.2 means drop 20% of the oldest groups per retry. Default: 0.2.
	ShrinkFactor float64

	// FallbackToDeterministic controls whether to use deterministic compaction
	// as a last resort before giving up entirely. Default: true.
	FallbackToDeterministic bool

	// PreserveFacts are key facts that must appear in the pivot message
	// regardless of which recovery path is taken.
	PreserveFacts []string
}

// CompactWithRecoveryResult holds the outcome of a recovery-wrapped compaction.
type CompactWithRecoveryResult struct {
	// Success indicates whether compaction (or recovery) succeeded.
	Success bool

	// Messages is the post-compaction message list. If compaction failed
	// entirely (Success=false), this is nil — the caller should keep the
	// original messages.
	Messages []*schema.Message

	// Strategy indicates which recovery strategy was ultimately used.
	// When the initial compaction succeeds, this is -1 (no recovery needed).
	Strategy RecoveryStrategy

	// Attempts is the total number of compaction attempts made.
	Attempts int

	// Error is non-nil when Success is false, describing why all recovery
	// strategies failed.
	Error error

	// PreCompactTokens is the estimated token count before compaction.
	PreCompactTokens int

	// PostCompactTokens is the estimated token count after compaction.
	PostCompactTokens int
}

// CompactWithRecovery wraps the LLM compaction path with structured failure
// recovery. The recovery cascade is:
//
//  1. Attempt LLM compaction on the full input.
//  2. On failure (model error, empty response, PTL): retry with progressively
//     smaller input (dropping oldest API-round groups).
//  3. If all retries fail: fall back to deterministic compaction (no LLM needed).
//  4. If deterministic also fails: preserve original messages (no message loss).
//
// This function NEVER loses messages silently. On total failure, it returns
// Success=false and the caller keeps the original messages unchanged.
//
// Mirrors the recovery behavior from reference compact/compact.ts
// (PTL retry loop, streaming retry, fallback to deterministic summary).
func CompactWithRecovery(ctx context.Context, messages []*schema.Message, opts LLMCompactOptions, config *CompactRecoveryConfig) *CompactWithRecoveryResult {
	if config == nil {
		config = &CompactRecoveryConfig{
			MaxRetries:              2,
			ShrinkFactor:            0.2,
			FallbackToDeterministic: true,
		}
	}
	if config.MaxRetries <= 0 {
		config.MaxRetries = 2
	}
	if config.ShrinkFactor <= 0 || config.ShrinkFactor >= 1.0 {
		config.ShrinkFactor = 0.2
	}

	preTokens := EstimateTokenCount(messages)
	result := &CompactWithRecoveryResult{
		PreCompactTokens: preTokens,
		Strategy:         RecoveryStrategy(-1), // no recovery needed initially
	}

	// Guard: need messages and a model
	if len(messages) == 0 {
		result.Success = false
		result.Error = errors.New("no messages to compact")
		return result
	}

	// Attempt 1: Full LLM compaction
	if opts.ChatModel != nil {
		llmResult, err := RunLLMCompact(ctx, messages, opts)
		result.Attempts++

		if err == nil && llmResult != nil && strings.TrimSpace(llmResult.Summary) != "" {
			// Success on first attempt
			postMessages := buildRecoveryPostMessages(llmResult.Summary, messages, opts, config)
			result.Success = true
			result.Messages = postMessages
			result.PostCompactTokens = EstimateTokenCount(postMessages)
			return result
		}

		// First attempt failed — try with progressively smaller input
		currentMessages := messages
		for attempt := 0; attempt < config.MaxRetries; attempt++ {
			// Shrink the input by dropping oldest groups
			shrunk := shrinkForRetry(currentMessages, config.ShrinkFactor)
			if len(shrunk) == 0 {
				break // Can't shrink further
			}
			currentMessages = shrunk

			llmResult, err = RunLLMCompact(ctx, currentMessages, opts)
			result.Attempts++

			if err == nil && llmResult != nil && strings.TrimSpace(llmResult.Summary) != "" {
				// Succeeded with smaller input
				result.Strategy = RecoveryRetrySmaller
				postMessages := buildRecoveryPostMessages(llmResult.Summary, messages, opts, config)
				result.Success = true
				result.Messages = postMessages
				result.PostCompactTokens = EstimateTokenCount(postMessages)
				return result
			}
		}
	}

	// All LLM attempts failed — try deterministic fallback
	if config.FallbackToDeterministic {
		detResult := buildDeterministicRecovery(messages, config)
		if detResult != nil {
			result.Strategy = RecoveryDeterministic
			result.Attempts++
			result.Success = true
			result.Messages = detResult
			result.PostCompactTokens = EstimateTokenCount(detResult)
			return result
		}
	}

	// Total failure — preserve original (no message loss guarantee)
	result.Strategy = RecoveryPreserveOriginal
	result.Success = false
	result.Error = fmt.Errorf("compaction failed after %d attempts; original messages preserved", result.Attempts)
	return result
}

// shrinkForRetry drops the oldest API-round groups to reduce input size.
// Returns nil if the input cannot be meaningfully shrunk.
func shrinkForRetry(messages []*schema.Message, shrinkFactor float64) []*schema.Message {
	groups := GroupMessagesByAPIRound(messages)
	if len(groups) < 2 {
		return nil
	}

	dropCount := int(float64(len(groups)) * shrinkFactor)
	if dropCount < 1 {
		dropCount = 1
	}
	if dropCount >= len(groups) {
		return nil
	}

	// Flatten remaining groups
	var result []*schema.Message
	for _, group := range groups[dropCount:] {
		result = append(result, group...)
	}

	// Ensure result starts with a user message (API requirement)
	if len(result) > 0 && result[0] != nil && result[0].Role != schema.User && result[0].Role != schema.System {
		marker := &schema.Message{
			Role:    schema.User,
			Content: "[earlier conversation truncated for compaction retry]",
			Extra: map[string]any{
				"isMeta": true,
			},
		}
		result = append([]*schema.Message{marker}, result...)
	}

	return result
}

// buildRecoveryPostMessages constructs the post-compaction message list using
// the pivot message system for proper continuation semantics.
func buildRecoveryPostMessages(summary string, originalMessages []*schema.Message, opts LLMCompactOptions, config *CompactRecoveryConfig) []*schema.Message {
	// Determine the trigger name
	trigger := "auto"
	if !opts.IsAutoCompact {
		trigger = "manual"
	}

	// Build pivot message with proper continuation semantics
	pivotConfig := PivotConfig{
		Trigger:                 trigger,
		PreCompactTokenCount:    EstimateTokenCount(originalMessages),
		SuppressFollowUp:        opts.SuppressFollowUpQuestions,
		TranscriptPath:          opts.TranscriptPath,
		RecentMessagesPreserved: true,
		PreservedFacts:          config.PreserveFacts,
		IncludeContinuation:     opts.IsAutoCompact,
	}

	pivot := CreatePivotMessage(summary, pivotConfig)

	// Preserve the tail of the conversation
	preserved := buildAutoCompactPreservedTail(originalMessages)

	// Assemble: pivot messages + preserved tail
	pivotMsgs := pivot.Messages()
	result := make([]*schema.Message, 0, len(pivotMsgs)+len(preserved))
	result = append(result, pivotMsgs...)
	result = append(result, preserved...)
	return result
}

// buildDeterministicRecovery creates a compacted message set using only
// deterministic logic (no LLM call). This is the last-resort fallback that
// still produces a usable result without losing messages entirely.
func buildDeterministicRecovery(messages []*schema.Message, config *CompactRecoveryConfig) []*schema.Message {
	if len(messages) == 0 {
		return nil
	}

	// Build a deterministic summary of what was compacted
	summaryLines := []string{
		"Conversation was compacted using deterministic fallback (LLM summarization unavailable).",
		"",
		"Recent activity:",
	}

	// Capture the last few messages as context
	previewCount := 6
	start := len(messages) - previewCount
	if start < 0 {
		start = 0
	}
	for i := start; i < len(messages); i++ {
		msg := messages[i]
		if msg == nil {
			continue
		}
		if msg.Extra != nil {
			if subtype, _ := msg.Extra["subtype"].(string); subtype == "compact_boundary" || subtype == "compact_summary" {
				continue
			}
		}
		preview := strings.TrimSpace(msg.Content)
		if preview == "" && len(msg.ToolCalls) > 0 {
			preview = fmt.Sprintf("[%d tool call(s)]", len(msg.ToolCalls))
		}
		if preview == "" {
			continue
		}
		preview = strings.Join(strings.Fields(preview), " ")
		if len(preview) > 200 {
			preview = preview[:197] + "..."
		}
		summaryLines = append(summaryLines, fmt.Sprintf("- %s: %s", msg.Role, preview))
	}

	// Add preserved facts if available
	if len(config.PreserveFacts) > 0 {
		summaryLines = append(summaryLines, "", "Key facts:")
		for _, fact := range config.PreserveFacts {
			summaryLines = append(summaryLines, "- "+fact)
		}
	}

	summaryLines = append(summaryLines, "", "Continue from the preserved context without repeating compacted history.")
	summaryText := strings.Join(summaryLines, "\n")

	// Build pivot using the deterministic summary
	pivot := CreatePivotMessage(summaryText, PivotConfig{
		Trigger:                 "deterministic_fallback",
		PreCompactTokenCount:    EstimateTokenCount(messages),
		RecentMessagesPreserved: true,
		PreservedFacts:          config.PreserveFacts,
		IncludeContinuation:     true,
	})

	// Preserve the tail
	preserved := buildAutoCompactPreservedTail(messages)

	pivotMsgs := pivot.Messages()
	result := make([]*schema.Message, 0, len(pivotMsgs)+len(preserved))
	result = append(result, pivotMsgs...)
	result = append(result, preserved...)
	return result
}
