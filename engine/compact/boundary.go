package compact

import "github.com/cloudwego/eino/schema"

// GetMessagesAfterCompactBoundary returns messages from the last compact boundary onward.
// Mirrors the reference's getMessagesAfterCompactBoundary in query.ts.
func GetMessagesAfterCompactBoundary(messages []*schema.Message) []*schema.Message {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role == schema.System && msg.Extra != nil {
			if subtype, ok := msg.Extra["subtype"]; ok && subtype == "compact_boundary" {
				return messages[i:]
			}
		}
	}
	return messages
}
