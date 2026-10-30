package compact

import "github.com/cloudwego/eino/schema"

// SnipResult holds the result of a history snip operation.
type SnipResult struct {
	Messages        []*schema.Message
	BoundaryMessage *schema.Message
	TokensFreed     int
}

// snipThreshold is the minimum message count for opportunistic snipping.
// Context pressure is also required.
const snipThreshold = 40

// SnipCompactIfNeeded snips long-running conversations only at the
// preparation-time model's auto-compact threshold. Empty models use the
// same conservative capacity fallback as auto-compaction.
func SnipCompactIfNeeded(messages []*schema.Message, modelName string) *SnipResult {
	if len(messages) < snipThreshold || EstimateTokenCount(messages) < GetAutoCompactThreshold(modelName) {
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
