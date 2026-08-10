package compact

import "github.com/cloudwego/eino/schema"

// toolResultBudgetMax is the maximum character length for a single tool result.
// Results exceeding this are replaced with a truncated version.
const toolResultBudgetMax = 30000

// toolResultKeepHead is how many characters to keep from the start when truncating.
const toolResultKeepHead = 10000

// toolResultKeepTail is how many characters to keep from the end when truncating.
const toolResultKeepTail = 10000

// ApplyToolResultBudget enforces per-message budget on aggregate tool result size.
// Mirrors query.ts:376-394. Tool results exceeding toolResultBudgetMax are truncated.
// replacementState tracks which tool_use_ids have been processed (interface{} to avoid
// circular import with engine package — expects *ContentReplacementState or nil).
// unlimitedTools is a set of tool names exempt from budget enforcement.
func ApplyToolResultBudget(
	messages []*schema.Message,
	replacementState interface{},
	persistFn interface{},
	unlimitedTools map[string]struct{},
) []*schema.Message {
	if len(messages) == 0 {
		return messages
	}

	// Type-assert the replacement state if provided.
	type replacementTracker interface {
		HasSeen(id string) bool
		MarkSeen(id string)
		SetReplacement(id, replacement string)
	}
	var tracker replacementTracker
	if replacementState != nil {
		if t, ok := replacementState.(replacementTracker); ok {
			tracker = t
		}
	}

	result := make([]*schema.Message, len(messages))
	modified := false

	for i, msg := range messages {
		if msg == nil || msg.Role != schema.Tool {
			result[i] = msg
			continue
		}

		// Check if this tool is unlimited (exempt from budget).
		if unlimitedTools != nil {
			toolName := ""
			if msg.Extra != nil {
				if name, ok := msg.Extra["tool_name"].(string); ok {
					toolName = name
				}
			}
			if _, exempt := unlimitedTools[toolName]; exempt {
				result[i] = msg
				continue
			}
		}

		// Check if already processed via replacement tracker.
		toolUseID := ""
		if msg.Extra != nil {
			if id, ok := msg.Extra["tool_use_id"].(string); ok {
				toolUseID = id
			}
		}
		if tracker != nil && toolUseID != "" && tracker.HasSeen(toolUseID) {
			result[i] = msg
			continue
		}

		// Mark as seen.
		if tracker != nil && toolUseID != "" {
			tracker.MarkSeen(toolUseID)
		}

		content := msg.Content
		if len(content) <= toolResultBudgetMax {
			result[i] = msg
			continue
		}

		// Truncate: keep head + separator + tail.
		truncated := content[:toolResultKeepHead] +
			"\n\n...[output truncated due to length]...\n\n" +
			content[len(content)-toolResultKeepTail:]

		clone := *msg
		clone.Content = truncated
		if msg.Extra != nil {
			clone.Extra = make(map[string]any, len(msg.Extra)+1)
			for k, v := range msg.Extra {
				clone.Extra[k] = v
			}
			clone.Extra["truncated"] = true
		}
		result[i] = &clone
		modified = true

		// Record replacement if tracker available.
		if tracker != nil && toolUseID != "" {
			tracker.SetReplacement(toolUseID, truncated)
		}
	}

	if !modified {
		return messages
	}
	return result
}
