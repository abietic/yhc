package compact

import "github.com/cloudwego/eino/schema"

// SnipResult holds the result of a history snip operation.
type SnipResult struct {
	Messages        []*schema.Message
	BoundaryMessage *schema.Message
	TokensFreed     int
}

// snipThreshold is the message count above which snipping is triggered.
// Conversations shorter than this are left untouched.
const snipThreshold = 40

// SnipCompactIfNeeded snips old messages from long-running conversations.
// Mirrors query.ts:401-409.
// Delegates to the real Snip() implementation when the conversation is long enough.
func SnipCompactIfNeeded(messages []*schema.Message) *SnipResult {
	// Only attempt snipping on long conversations.
	if len(messages) < snipThreshold {
		return &SnipResult{
			Messages:    messages,
			TokensFreed: 0,
		}
	}

	result, err := Snip(messages)
	if err != nil || !result.Applied {
		return &SnipResult{
			Messages:    messages,
			TokensFreed: 0,
		}
	}

	return &SnipResult{
		Messages: result.Messages,
		BoundaryMessage: &schema.Message{
			Role:    schema.System,
			Content: "Context history snipped",
			Extra: map[string]any{
				"subtype":      "snip_boundary",
				"trigger":      "history_snip",
				"tokens_freed": result.TokensFreed,
			},
		},
		TokensFreed: result.TokensFreed,
	}
}
