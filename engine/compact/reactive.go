package compact

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// BasicReactiveResult holds the post-recovery message set after a reactive
// compact/strip retry. It is intentionally smaller than the reference runtime's
// full compaction result, but keeps the same visible shape: a compact boundary,
// a summary message, and a small preserved tail.
type BasicReactiveResult struct {
	Messages []*schema.Message
}

// TryReactiveCompact builds a reduced post-recovery context after a real API
// overflow/media failure. The Go port currently uses a deterministic summary +
// preserved tail in place of the reference implementation's LLM summarization.
func TryReactiveCompact(messages []*schema.Message, querySource, reason string) *BasicReactiveResult {
	if querySource == "compact" {
		return nil
	}
	if len(messages) == 0 {
		return nil
	}

	preserved := buildReactivePreservedTail(messages, reason)
	if len(preserved) == 0 {
		return nil
	}

	boundary := &schema.Message{
		Role:    schema.System,
		Content: "",
		Extra: map[string]any{
			"subtype": "compact_boundary",
			"trigger": "reactive_compact",
			"reason":  reason,
		},
	}
	summary := &schema.Message{
		Role:    schema.System,
		Content: buildReactiveSummary(messages, reason),
		Extra: map[string]any{
			"subtype": "compact_summary",
			"trigger": "reactive_compact",
			"reason":  reason,
		},
	}

	out := make([]*schema.Message, 0, len(preserved)+2)
	out = append(out, boundary, summary)
	out = append(out, preserved...)
	return &BasicReactiveResult{Messages: out}
}

func buildReactivePreservedTail(messages []*schema.Message, reason string) []*schema.Message {
	preserved := make([]*schema.Message, 0, 3)
	for i := len(messages) - 1; i >= 0 && len(preserved) < 2; i-- {
		msg := sanitizeReactiveMessage(messages[i], reason)
		if msg == nil {
			continue
		}
		preserved = append([]*schema.Message{msg}, preserved...)
	}
	return preserved
}

func sanitizeReactiveMessage(msg *schema.Message, reason string) *schema.Message {
	if msg == nil {
		return nil
	}

	clone := cloneMessage(msg)
	if clone == nil {
		return nil
	}
	if clone.Extra != nil {
		if subtype, _ := clone.Extra["subtype"].(string); subtype == "compact_boundary" || subtype == "compact_summary" || subtype == "collapse_staged" {
			return nil
		}
	}

	if reason != "media_size" {
		return clone
	}

	textParts := make([]string, 0, len(clone.UserInputMultiContent))
	for _, part := range clone.UserInputMultiContent {
		if strings.TrimSpace(part.Text) != "" {
			textParts = append(textParts, strings.TrimSpace(part.Text))
		}
	}
	if strings.TrimSpace(clone.Content) == "" {
		switch {
		case len(textParts) > 0:
			clone.Content = strings.Join(textParts, "\n")
		case clone.Role == schema.User:
			clone.Content = "[media omitted after recovery]"
		case clone.Role == schema.Assistant:
			clone.Content = "[assistant media omitted after recovery]"
		}
	}
	clone.MultiContent = nil
	clone.UserInputMultiContent = nil
	clone.AssistantGenMultiContent = nil
	clone.ResponseMeta = nil
	if clone.Extra == nil {
		clone.Extra = map[string]any{}
	}
	clone.Extra["media_stripped"] = true
	return clone
}

func buildReactiveSummary(messages []*schema.Message, reason string) string {
	intro := "Conversation was reactively compacted after a prompt-too-long error."
	if reason == "media_size" {
		intro = "Conversation was reactively compacted after a media-size error. Oversized media was stripped from preserved context."
	}

	lines := []string{intro, "Preserved recent context:"}
	recent := make([]string, 0, 4)
	for i := len(messages) - 1; i >= 0 && len(recent) < 4; i-- {
		msg := messages[i]
		if msg == nil {
			continue
		}
		if msg.Extra != nil {
			if subtype, _ := msg.Extra["subtype"].(string); subtype == "compact_boundary" || subtype == "compact_summary" || subtype == "collapse_staged" {
				continue
			}
		}
		preview := strings.TrimSpace(msg.Content)
		if preview == "" {
			if len(msg.UserInputMultiContent) > 0 {
				parts := make([]string, 0, len(msg.UserInputMultiContent))
				for _, part := range msg.UserInputMultiContent {
					if strings.TrimSpace(part.Text) != "" {
						parts = append(parts, strings.TrimSpace(part.Text))
					}
				}
				preview = strings.Join(parts, " ")
			}
		}
		if preview == "" && len(msg.ToolCalls) > 0 {
			preview = fmt.Sprintf("%d tool call(s)", len(msg.ToolCalls))
		}
		if preview == "" {
			continue
		}
		preview = strings.Join(strings.Fields(preview), " ")
		if len(preview) > 160 {
			preview = preview[:157] + "..."
		}
		recent = append([]string{fmt.Sprintf("- %s: %s", msg.Role, preview)}, recent...)
	}
	if len(recent) == 0 {
		recent = append(recent, "- recent context unavailable")
	}
	lines = append(lines, recent...)
	lines = append(lines, "Continue from the preserved tail without repeating stripped or compacted history.")
	return strings.Join(lines, "\n")
}

func cloneMessage(msg *schema.Message) *schema.Message {
	if msg == nil {
		return nil
	}
	clone := *msg
	if msg.Extra != nil {
		clone.Extra = make(map[string]any, len(msg.Extra))
		for k, v := range msg.Extra {
			clone.Extra[k] = v
		}
	}
	if len(msg.MultiContent) > 0 {
		clone.MultiContent = append([]schema.ChatMessagePart(nil), msg.MultiContent...) //nolint:staticcheck
	}
	if len(msg.UserInputMultiContent) > 0 {
		clone.UserInputMultiContent = append([]schema.MessageInputPart(nil), msg.UserInputMultiContent...)
	}
	if len(msg.AssistantGenMultiContent) > 0 {
		clone.AssistantGenMultiContent = append([]schema.MessageOutputPart(nil), msg.AssistantGenMultiContent...)
	}
	if len(msg.ToolCalls) > 0 {
		clone.ToolCalls = append([]schema.ToolCall(nil), msg.ToolCalls...)
	}
	clone.ResponseMeta = msg.ResponseMeta
	return &clone
}

// ---------------------------------------------------------------------------
// Reactive compact system: model-initiated compaction requests
// ---------------------------------------------------------------------------

// ReactiveCompactRequest describes a model-initiated or user-initiated
// compaction request with optional parameters controlling the behavior.
type ReactiveCompactRequest struct {
	// Trigger identifies who/what initiated the compaction.
	// Values: "user_command", "model_request", "api_error"
	Trigger string

	// TargetTokens is an optional token budget to compact to.
	// When zero, a reasonable default is chosen automatically.
	TargetTokens int

	// PreserveRecent is the number of recent messages to keep intact
	// (not included in the summarization).
	PreserveRecent int

	// CustomPrompt is an optional custom summarization prompt override.
	CustomPrompt string
}

// ReactiveCompactResult holds metadata about a completed reactive compaction.
type ReactiveCompactResult struct {
	// Success indicates whether compaction completed without error.
	Success bool
	// PreTokens is the estimated token count before compaction.
	PreTokens int
	// PostTokens is the estimated token count after compaction.
	PostTokens int
	// MessagesBefore is the message count before compaction.
	MessagesBefore int
	// MessagesAfter is the message count after compaction.
	MessagesAfter int
	// SummaryPreview is a short preview of the generated summary.
	SummaryPreview string
}

// ReactiveCompact performs reactive compaction on the given messages based on
// the request parameters. Uses LLM compaction when the model is available in
// opts; falls back to deterministic compaction when not.
// Returns the compaction metadata, the new message list, and any error.
func ReactiveCompact(ctx context.Context, messages []*schema.Message, req *ReactiveCompactRequest, opts LLMCompactOptions) (*ReactiveCompactResult, []*schema.Message, error) {
	if len(messages) == 0 {
		return &ReactiveCompactResult{Success: true}, nil, nil
	}
	if req == nil {
		req = &ReactiveCompactRequest{
			Trigger:        "model_request",
			PreserveRecent: 2,
		}
	}

	preserveCount := req.PreserveRecent
	if preserveCount < 0 {
		preserveCount = 0
	}

	preTokens := EstimateTokenCount(messages)
	messagesBefore := len(messages)

	// Split messages into those to compact and those to keep
	toCompact, toKeep := buildReactiveCompactPreserved(messages, preserveCount)

	if len(toCompact) == 0 {
		// Nothing to compact, just return original messages
		return &ReactiveCompactResult{
			Success:        true,
			PreTokens:      preTokens,
			PostTokens:     preTokens,
			MessagesBefore: messagesBefore,
			MessagesAfter:  messagesBefore,
		}, messages, nil
	}

	// Try LLM compaction when a model is available
	var summaryText string
	usedLLM := false

	if opts.ChatModel != nil {
		customInstr := opts.CustomInstructions
		if req.CustomPrompt != "" {
			customInstr = req.CustomPrompt
		}

		llmOpts := LLMCompactOptions{
			ChatModel:                 opts.ChatModel,
			ModelName:                 opts.ModelName,
			CustomInstructions:        customInstr,
			SuppressFollowUpQuestions: opts.SuppressFollowUpQuestions,
			TranscriptPath:            opts.TranscriptPath,
			IsAutoCompact:             false,
			ProviderUsage:             opts.ProviderUsage,
		}

		llmResult, err := RunLLMCompact(ctx, toCompact, llmOpts)
		if err == nil {
			summaryText = GetCompactUserSummaryMessage(llmResult.Summary, opts.SuppressFollowUpQuestions, opts.TranscriptPath, false)
			usedLLM = true
		}
		// On LLM failure, fall through to deterministic
	}

	if !usedLLM {
		// Deterministic fallback
		summaryText = buildReactiveDeterministicSummary(toCompact, req.Trigger)
	}

	// Build the output messages
	boundary := &schema.Message{
		Role:    schema.System,
		Content: "",
		Extra: map[string]any{
			"subtype": "compact_boundary",
			"trigger": "reactive_compact",
			"reason":  req.Trigger,
		},
	}
	summaryRole := schema.User
	if !usedLLM {
		summaryRole = schema.System
	}
	summaryMsg := &schema.Message{
		Role:    summaryRole,
		Content: summaryText,
		Extra: map[string]any{
			"subtype": "compact_summary",
			"trigger": "reactive_compact",
			"reason":  req.Trigger,
		},
	}

	newMessages := make([]*schema.Message, 0, 2+len(toKeep))
	newMessages = append(newMessages, boundary, summaryMsg)
	newMessages = append(newMessages, toKeep...)

	postTokens := EstimateTokenCount(newMessages)

	// Build summary preview (first 200 chars)
	preview := summaryText
	if len(preview) > 200 {
		preview = preview[:197] + "..."
	}

	result := &ReactiveCompactResult{
		Success:        true,
		PreTokens:      preTokens,
		PostTokens:     postTokens,
		MessagesBefore: messagesBefore,
		MessagesAfter:  len(newMessages),
		SummaryPreview: preview,
	}

	return result, newMessages, nil
}

// HandleCompactCommand is called when the user types /compact. It uses default
// preservation (last 2 messages) and returns a result suitable for display.
func HandleCompactCommand(ctx context.Context, messages []*schema.Message, opts LLMCompactOptions) (*ReactiveCompactResult, []*schema.Message, error) {
	req := &ReactiveCompactRequest{
		Trigger:        "user_command",
		PreserveRecent: 2,
	}
	return ReactiveCompact(ctx, messages, req, opts)
}

// HandleOverflowCompact is called on 413 API errors. It is more aggressive —
// preserves only the last message — and must succeed. Falls back to truncation
// if LLM compaction fails.
func HandleOverflowCompact(ctx context.Context, messages []*schema.Message, opts LLMCompactOptions) (*ReactiveCompactResult, []*schema.Message, error) {
	if len(messages) == 0 {
		return &ReactiveCompactResult{Success: true}, nil, nil
	}

	req := &ReactiveCompactRequest{
		Trigger:        "api_error",
		PreserveRecent: 1,
	}

	result, newMessages, err := ReactiveCompact(ctx, messages, req, opts)
	if err == nil && result.Success {
		return result, newMessages, nil
	}

	// Must succeed: fall back to aggressive truncation.
	// Keep only the last message and a truncation notice.
	preTokens := EstimateTokenCount(messages)
	messagesBefore := len(messages)

	_, toKeep := buildReactiveCompactPreserved(messages, 1)

	boundary := &schema.Message{
		Role:    schema.System,
		Content: "",
		Extra: map[string]any{
			"subtype": "compact_boundary",
			"trigger": "reactive_compact",
			"reason":  "api_error_truncation",
		},
	}
	truncNotice := &schema.Message{
		Role:    schema.System,
		Content: "Conversation was aggressively truncated after an API overflow error. Only the most recent message was preserved. Previous context has been lost.",
		Extra: map[string]any{
			"subtype": "compact_summary",
			"trigger": "reactive_compact",
			"reason":  "api_error_truncation",
		},
	}

	fallbackMessages := make([]*schema.Message, 0, 2+len(toKeep))
	fallbackMessages = append(fallbackMessages, boundary, truncNotice)
	fallbackMessages = append(fallbackMessages, toKeep...)

	postTokens := EstimateTokenCount(fallbackMessages)

	return &ReactiveCompactResult{
		Success:        true,
		PreTokens:      preTokens,
		PostTokens:     postTokens,
		MessagesBefore: messagesBefore,
		MessagesAfter:  len(fallbackMessages),
		SummaryPreview: truncNotice.Content,
	}, fallbackMessages, nil
}

// buildReactiveCompactPreserved splits messages into those to compact and
// those to keep. It ensures tool call/result pairs aren't split across the
// boundary — if a tool result is in the preserved set, its corresponding
// assistant message with the tool call is also preserved.
func buildReactiveCompactPreserved(messages []*schema.Message, preserveCount int) (toCompact, toKeep []*schema.Message) {
	if len(messages) == 0 {
		return nil, nil
	}
	if preserveCount <= 0 {
		return messages, nil
	}
	if preserveCount >= len(messages) {
		return nil, messages
	}

	// Start by taking the last preserveCount messages
	splitIdx := len(messages) - preserveCount

	// Adjust split point backward to avoid splitting tool call/result pairs.
	// A tool result message (Role == Tool) must have its corresponding assistant
	// message (with ToolCalls containing matching ID) in the same partition.
	for splitIdx > 0 {
		firstKept := messages[splitIdx]
		if firstKept == nil {
			break
		}

		// If the first kept message is a tool result, we need to pull back
		// to include the assistant message that issued the tool call.
		if firstKept.Role == schema.Tool && firstKept.ToolCallID != "" {
			// Look backward for the matching assistant tool call
			found := false
			for j := splitIdx - 1; j >= 0; j-- {
				msg := messages[j]
				if msg == nil {
					continue
				}
				if msg.Role == schema.Assistant && hasToolCallID(msg, firstKept.ToolCallID) {
					splitIdx = j
					found = true
					break
				}
			}
			if !found {
				// Can't find the matching tool call — just break here
				break
			}
			// After adjusting, check again in case the new first message
			// is also a tool result
			continue
		}
		break
	}

	toCompact = make([]*schema.Message, splitIdx)
	copy(toCompact, messages[:splitIdx])

	toKeep = make([]*schema.Message, len(messages)-splitIdx)
	for i, msg := range messages[splitIdx:] {
		toKeep[i] = cloneMessage(msg)
	}

	return toCompact, toKeep
}

// hasToolCallID checks whether an assistant message contains a tool call with
// the given ID.
func hasToolCallID(msg *schema.Message, toolCallID string) bool {
	for _, tc := range msg.ToolCalls {
		if tc.ID == toolCallID {
			return true
		}
	}
	return false
}

// buildReactiveDeterministicSummary creates a deterministic summary for when
// LLM compaction is not available.
func buildReactiveDeterministicSummary(messages []*schema.Message, trigger string) string {
	intro := "Conversation was reactively compacted"
	switch trigger {
	case "user_command":
		intro += " by user request."
	case "model_request":
		intro += " at model request."
	case "api_error":
		intro += " after an API overflow error."
	default:
		intro += "."
	}

	lines := []string{intro, "Preserved recent context:"}
	recent := make([]string, 0, 4)
	for i := len(messages) - 1; i >= 0 && len(recent) < 4; i-- {
		msg := messages[i]
		if msg == nil {
			continue
		}
		if msg.Extra != nil {
			if subtype, _ := msg.Extra["subtype"].(string); subtype == "compact_boundary" || subtype == "compact_summary" || subtype == "collapse_staged" {
				continue
			}
		}
		preview := strings.TrimSpace(msg.Content)
		if preview == "" && len(msg.UserInputMultiContent) > 0 {
			parts := make([]string, 0, len(msg.UserInputMultiContent))
			for _, part := range msg.UserInputMultiContent {
				if strings.TrimSpace(part.Text) != "" {
					parts = append(parts, strings.TrimSpace(part.Text))
				}
			}
			preview = strings.Join(parts, " ")
		}
		if preview == "" && len(msg.ToolCalls) > 0 {
			preview = fmt.Sprintf("%d tool call(s)", len(msg.ToolCalls))
		}
		if preview == "" {
			continue
		}
		preview = strings.Join(strings.Fields(preview), " ")
		if len(preview) > 160 {
			preview = preview[:157] + "..."
		}
		recent = append([]string{fmt.Sprintf("- %s: %s", msg.Role, preview)}, recent...)
	}
	if len(recent) == 0 {
		recent = append(recent, "- recent context unavailable")
	}
	lines = append(lines, recent...)
	lines = append(lines, "Continue from the preserved tail without repeating compacted history.")
	return strings.Join(lines, "\n")
}
