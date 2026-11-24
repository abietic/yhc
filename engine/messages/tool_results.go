package messages

import (
	"maps"

	"github.com/cloudwego/eino/schema"
)

const toolRoundAttachmentCallIDKey = "_yhc_tool_round_attachment_call_id"

// MarkToolRoundAttachment records which tool emitted a supplemental user
// message. Copy the metadata before publishing the attachment or saving it.
func MarkToolRoundAttachment(message *schema.Message, callID string) *schema.Message {
	if message.Role != schema.User || callID == "" {
		return message
	}
	clone := *message
	clone.Extra = maps.Clone(message.Extra)
	if clone.Extra == nil {
		clone.Extra = make(map[string]any, 2)
	}
	clone.Extra["is_meta"] = true
	clone.Extra[toolRoundAttachmentCallIDKey] = callID
	return &clone
}

// PairToolCallResults groups provider-visible results by their assistant's
// call IDs. The transcript may interleave results with supplemental messages
// from the same tool round. Providers receive those supplements after the
// result group, in their original relative order.
func PairToolCallResults(history []*schema.Message) []*schema.Message {
	needsProjection := false
	for _, message := range history {
		if message != nil && (message.Role == schema.Assistant && len(message.ToolCalls) > 0 ||
			message.Extra[toolRoundAttachmentCallIDKey] != nil) {
			needsProjection = true
			break
		}
	}
	if !needsProjection {
		return history
	}

	result := make([]*schema.Message, 0, len(history))
	for i := 0; i < len(history); i++ {
		message := history[i]
		if message == nil || message.Role == schema.Tool {
			continue
		}
		result = append(result, withoutToolRoundAttachmentMarker(message))
		if message.Role != schema.Assistant || len(message.ToolCalls) == 0 {
			continue
		}

		pending := make(map[string]string, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			if call.ID != "" {
				pending[call.ID] = call.Function.Name
			}
		}
		var supplements []*schema.Message
		for len(pending) > 0 && i+1 < len(history) {
			next := history[i+1]
			if next == nil {
				break
			}
			if next.Role != schema.Tool {
				if !isToolRoundSupplement(next, message.ToolCalls) {
					break
				}
				supplements = append(supplements, withoutToolRoundAttachmentMarker(next))
				i++
				continue
			}
			i++
			name, ok := pending[next.ToolCallID]
			if !ok {
				continue
			}
			if next.ToolName == "" && name != "" {
				clone := *next
				clone.ToolName = name
				next = &clone
			}
			result = append(result, next)
			delete(pending, next.ToolCallID)
		}
		for _, call := range message.ToolCalls {
			name, missing := pending[call.ID]
			if !missing {
				continue
			}
			result = append(result, &schema.Message{
				Role:       schema.Tool,
				Content:    "[Tool result unavailable - execution was interrupted]",
				ToolCallID: call.ID,
				ToolName:   name,
			})
			delete(pending, call.ID)
		}
		result = append(result, supplements...)
	}
	return result
}

func isToolRoundSupplement(message *schema.Message, calls []schema.ToolCall) bool {
	meta, _ := message.Extra["is_meta"].(bool)
	if message.Role != schema.User || !meta {
		return false
	}
	if marker, marked := message.Extra[toolRoundAttachmentCallIDKey]; marked {
		callID, ok := marker.(string)
		if !ok || callID == "" {
			return false
		}
		for _, call := range calls {
			if call.ID == callID {
				return true
			}
		}
		return false
	}
	// Older transcripts provide only the attachment kind. Queued input and
	// other lifecycle messages still delimit the result group.
	kind, _ := message.Extra["attachment_kind"].(string)
	switch kind {
	case "image", "pdf_pages", "hook", "shell_hook_output", "hook_system_message",
		"hook_non_blocking_error", "hook_stopped_continuation", "permission_denied_retry":
		return true
	default:
		return false
	}
}

func withoutToolRoundAttachmentMarker(message *schema.Message) *schema.Message {
	if _, marked := message.Extra[toolRoundAttachmentCallIDKey]; !marked {
		return message
	}
	clone := *message
	clone.Extra = maps.Clone(message.Extra)
	delete(clone.Extra, toolRoundAttachmentCallIDKey)
	return &clone
}
