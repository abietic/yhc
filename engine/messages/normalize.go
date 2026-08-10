package messages

import (
	"strings"

	"github.com/cloudwego/eino/schema"
)

// internalExtraKeys lists Extra map keys that are internal-only metadata and
// should not be forwarded to the model API.
var internalExtraKeys = []string{
	"subtype",
	"trigger",
	"committed",
	"is_meta",
	"attachment_kind",
	"level",
	"virtual",
	"command_uuid",
	"command_mode",
	"message_id",
}

// CreateUserMessage constructs a standard user message.
func CreateUserMessage(content string) *schema.Message {
	return &schema.Message{
		Role:    schema.User,
		Content: content,
	}
}

// CreateAssistantMessage constructs a standard assistant message.
func CreateAssistantMessage(content string) *schema.Message {
	return &schema.Message{
		Role:    schema.Assistant,
		Content: content,
	}
}

// CreateToolResultMessage constructs a tool-role message representing the result
// of a tool call. When isError is true, the Extra["is_error"] flag is set.
func CreateToolResultMessage(toolCallID, content string, isError bool) *schema.Message {
	msg := &schema.Message{
		Role:       schema.Tool,
		Content:    content,
		ToolCallID: toolCallID,
	}
	if isError {
		msg.Extra = map[string]any{"is_error": true}
	}
	return msg
}

// NormalizeMessagesForAPI prepares a message list for API submission:
//   - Removes nil and empty messages
//   - Strips internal-only Extra fields
//   - Ensures proper role alternation by merging consecutive same-role messages
//   - Ensures tool results follow tool calls
//   - Removes empty messages post-merge
func NormalizeMessagesForAPI(messages []*schema.Message) []*schema.Message {
	if len(messages) == 0 {
		return messages
	}

	// Step 1: Filter nil and empty messages, strip internal metadata.
	filtered := make([]*schema.Message, 0, len(messages))
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		if isEmptyMessage(msg) {
			continue
		}
		filtered = append(filtered, StripInternalMetadata(msg))
	}

	if len(filtered) == 0 {
		return nil
	}

	// Step 2: Merge consecutive same-role messages (enforces alternation).
	merged := MergeConsecutiveMessages(filtered)

	// Step 3: Ensure tool results follow tool calls.
	merged = ensureToolResultOrder(merged)

	// Step 4: Final pass - remove any empty messages produced by merging.
	result := make([]*schema.Message, 0, len(merged))
	for _, msg := range merged {
		if !isEmptyMessage(msg) {
			result = append(result, msg)
		}
	}

	return result
}

// StripInternalMetadata returns a cloned message with internal-only Extra fields
// removed. API-relevant fields (Role, Content, ToolCalls, ToolCallID, ToolName,
// ReasoningContent, ResponseMeta) are preserved.
func StripInternalMetadata(msg *schema.Message) *schema.Message {
	if msg == nil {
		return nil
	}
	clone := cloneMessage(msg)

	if len(clone.Extra) == 0 {
		return clone
	}

	for _, key := range internalExtraKeys {
		delete(clone.Extra, key)
	}

	// If Extra is now empty, nil it out for cleanliness.
	if len(clone.Extra) == 0 {
		clone.Extra = nil
	}

	return clone
}

// ValidateMessageSequence checks a message list for structural issues:
//   - Role alternation violations (consecutive same roles that aren't tool)
//   - Tool call/result pairing mismatches
//
// Returns a list of warning strings; empty means valid.
func ValidateMessageSequence(messages []*schema.Message) []string {
	var warnings []string

	if len(messages) == 0 {
		return warnings
	}

	// Track tool call IDs that need results.
	pendingToolCalls := make(map[string]bool)

	for i, msg := range messages {
		if msg == nil {
			warnings = append(warnings, "nil message at index")
			continue
		}

		// Check role alternation (user/assistant should alternate; tool follows assistant).
		if i > 0 && messages[i-1] != nil {
			prev := messages[i-1]
			if msg.Role == prev.Role && msg.Role != schema.Tool {
				warnings = append(warnings, "consecutive "+string(msg.Role)+" messages at index "+itoa(i-1)+"-"+itoa(i))
			}
			if msg.Role == schema.Tool && prev.Role != schema.Assistant && prev.Role != schema.Tool {
				warnings = append(warnings, "tool message at index "+itoa(i)+" not preceded by assistant or tool message")
			}
		}

		// Track tool calls from assistant messages.
		if msg.Role == schema.Assistant {
			for _, tc := range msg.ToolCalls {
				if tc.ID != "" {
					pendingToolCalls[tc.ID] = true
				}
			}
		}

		// Match tool results.
		if msg.Role == schema.Tool && msg.ToolCallID != "" {
			if !pendingToolCalls[msg.ToolCallID] {
				warnings = append(warnings, "tool result at index "+itoa(i)+" references unknown tool call ID: "+msg.ToolCallID)
			} else {
				delete(pendingToolCalls, msg.ToolCallID)
			}
		}
	}

	// Report unmatched tool calls.
	for id := range pendingToolCalls {
		warnings = append(warnings, "tool call ID "+id+" has no matching tool result")
	}

	return warnings
}

// MergeConsecutiveMessages merges consecutive messages with the same role.
// Content is combined with a newline separator. Tool calls are preserved
// individually (assistant messages with ToolCalls are not merged into the
// preceding assistant message).
func MergeConsecutiveMessages(messages []*schema.Message) []*schema.Message {
	if len(messages) == 0 {
		return nil
	}

	result := make([]*schema.Message, 0, len(messages))
	result = append(result, cloneMessage(messages[0]))

	for i := 1; i < len(messages); i++ {
		msg := messages[i]
		if msg == nil {
			continue
		}

		last := result[len(result)-1]

		// Don't merge tool messages (they're keyed by ToolCallID).
		if msg.Role == schema.Tool {
			result = append(result, cloneMessage(msg))
			continue
		}

		// Don't merge assistant messages that contain tool calls — these need
		// to remain distinct so tool results can pair with them.
		if msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
			result = append(result, cloneMessage(msg))
			continue
		}

		// Don't merge into an assistant message that has tool calls.
		if last.Role == schema.Assistant && len(last.ToolCalls) > 0 {
			result = append(result, cloneMessage(msg))
			continue
		}

		// Merge if same role.
		if msg.Role == last.Role {
			if last.Content != "" && msg.Content != "" {
				last.Content = last.Content + "\n" + msg.Content
			} else if msg.Content != "" {
				last.Content = msg.Content
			}
			mergeExtra(last, msg)
			// Merge reasoning content.
			if msg.ReasoningContent != "" {
				if last.ReasoningContent != "" {
					last.ReasoningContent = last.ReasoningContent + "\n" + msg.ReasoningContent
				} else {
					last.ReasoningContent = msg.ReasoningContent
				}
			}
		} else {
			result = append(result, cloneMessage(msg))
		}
	}

	return result
}

func mergeExtra(dst, src *schema.Message) {
	if dst == nil || src == nil || len(src.Extra) == 0 {
		return
	}
	if dst.Extra == nil {
		dst.Extra = make(map[string]any, len(src.Extra))
	}
	for k, v := range src.Extra {
		dst.Extra[k] = v
	}
}

// TruncateMessageContent returns a new message with Content truncated to
// maxChars characters. If truncation occurs, "[truncated]" is appended.
// The original message is not modified.
func TruncateMessageContent(msg *schema.Message, maxChars int) *schema.Message {
	if msg == nil {
		return nil
	}
	clone := cloneMessage(msg)
	if maxChars <= 0 {
		return clone
	}
	if len(clone.Content) > maxChars {
		clone.Content = clone.Content[:maxChars] + "[truncated]"
	}
	return clone
}

// ExtractToolCalls collects all tool calls from assistant messages in the
// given message slice, returning them as a flat list in encounter order.
func ExtractToolCalls(messages []*schema.Message) []schema.ToolCall {
	var calls []schema.ToolCall
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		if msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
			calls = append(calls, msg.ToolCalls...)
		}
	}
	return calls
}

// FindToolResult locates the tool result message for a given tool call ID.
// Returns nil if not found.
func FindToolResult(messages []*schema.Message, toolCallID string) *schema.Message {
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		if msg.Role == schema.Tool && msg.ToolCallID == toolCallID {
			return msg
		}
	}
	return nil
}

// --- internal helpers ---

// cloneMessage performs a shallow clone of a Message, copying slices and maps
// at one level deep to prevent accidental mutation of the original.
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
	if len(msg.MultiContent) > 0 { //nolint:staticcheck
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
	return &clone
}

// isEmptyMessage returns true if the message has no meaningful content.
func isEmptyMessage(msg *schema.Message) bool {
	if msg == nil {
		return true
	}
	if strings.TrimSpace(msg.Content) != "" {
		return false
	}
	if msg.ReasoningContent != "" {
		return false
	}
	if len(msg.ToolCalls) > 0 {
		return false
	}
	if msg.ToolCallID != "" {
		return false
	}
	if len(msg.MultiContent) > 0 {
		return false
	}
	if len(msg.UserInputMultiContent) > 0 {
		return false
	}
	if len(msg.AssistantGenMultiContent) > 0 {
		return false
	}
	return true
}

// ensureToolResultOrder reorders messages so that tool result messages (role=tool)
// appear immediately after their corresponding assistant message containing
// the tool call. Messages that are already in the correct position are not moved.
func ensureToolResultOrder(messages []*schema.Message) []*schema.Message {
	if len(messages) == 0 {
		return messages
	}

	// Build a map of toolCallID -> tool result message.
	toolResults := make(map[string]*schema.Message)
	for _, msg := range messages {
		if msg != nil && msg.Role == schema.Tool && msg.ToolCallID != "" {
			toolResults[msg.ToolCallID] = msg
		}
	}

	// If no tool results at all, nothing to reorder.
	if len(toolResults) == 0 {
		return messages
	}

	// Rebuild: insert tool results right after the assistant message that
	// contains their corresponding tool call.
	emittedToolResults := make(map[string]bool)
	result := make([]*schema.Message, 0, len(messages))

	for _, msg := range messages {
		if msg == nil {
			continue
		}

		// Skip tool result messages in their original position; they'll be
		// re-inserted after their tool call.
		if msg.Role == schema.Tool && msg.ToolCallID != "" && toolResults[msg.ToolCallID] != nil {
			continue
		}

		result = append(result, msg)

		// After an assistant message with tool calls, emit any matching tool results.
		if msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				if tr, ok := toolResults[tc.ID]; ok && !emittedToolResults[tc.ID] {
					result = append(result, tr)
					emittedToolResults[tc.ID] = true
				}
			}
		}
	}

	// Append any orphaned tool results that couldn't be matched.
	for id, tr := range toolResults {
		if !emittedToolResults[id] {
			result = append(result, tr)
		}
	}

	return result
}

// itoa is a minimal int-to-string helper to avoid importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := false
	if n < 0 {
		negative = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ConcatAssistantOutputParts delegates streamed assistant output
// reconstruction to Eino. Its concat contract is aware of output-part type,
// StreamingMeta.Index, Extra metadata, and reasoning signatures; treating
// adjacent parts as plain strings would collapse distinct provider blocks.
func ConcatAssistantOutputParts(parts []schema.MessageOutputPart) ([]schema.MessageOutputPart, error) {
	if len(parts) < 2 {
		return parts, nil
	}
	merged, err := schema.ConcatMessages([]*schema.Message{{
		Role:                     schema.Assistant,
		AssistantGenMultiContent: parts,
	}})
	if err != nil {
		return nil, err
	}
	return merged.AssistantGenMultiContent, nil
}
