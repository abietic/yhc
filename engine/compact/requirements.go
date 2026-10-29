package compact

import "github.com/cloudwego/eino/schema"

// splitDeterministicContext retains real user requests in their original role
// and order. A preview of recent activity cannot substitute for instructions.
// It also retains the recent tail, expanding it to include tool-call owners.
// Only the active history is considered; older compacted history stays retired.
func splitDeterministicContext(messages []*schema.Message, recentCount int) (discarded, kept []*schema.Message) {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg != nil && msg.Role == schema.System && msg.Extra["subtype"] == "compact_boundary" {
			messages = messages[i+1:]
			break
		}
	}
	active := make([]*schema.Message, 0, len(messages))
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		switch msg.Extra["subtype"] {
		case "compact_boundary", "compact_summary", "collapse_staged":
			continue
		}
		active = append(active, msg)
	}
	start := max(0, len(active)-max(0, recentCount))
	for i := len(active) - 1; i >= start; i-- {
		msg := active[i]
		if msg.Role != schema.Tool || msg.ToolCallID == "" {
			continue
		}
		for j := i - 1; j >= 0; j-- {
			if active[j].Role == schema.Assistant && hasToolCallID(active[j], msg.ToolCallID) {
				start = min(start, j)
				break
			}
		}
	}
	for i, msg := range active {
		if i >= start || (msg.Role == schema.User && !isMetaMessage(msg)) {
			kept = append(kept, cloneMessage(msg))
		} else {
			discarded = append(discarded, msg)
		}
	}
	return discarded, kept
}
