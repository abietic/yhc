package execution

import (
	"strings"

	"github.com/cloudwego/eino/schema"
)

// YieldMissingToolResults yields synthetic tool_result blocks for orphaned tool_use blocks.
// Mirrors query.ts:123-149.
func YieldMissingToolResults(
	assistantMessages []*schema.Message,
	errMsg string,
	yield func(QueryEvent),
) {
	for _, msg := range assistantMessages {
		for _, tc := range msg.ToolCalls {
			toolCallID := strings.TrimSpace(tc.ID)
			if toolCallID == "" {
				toolCallID = strings.TrimSpace(tc.Function.Name)
			}
			if toolCallID == "" {
				// A tool result without an ID is rejected by model providers and
				// cannot be paired safely. Ignore a completely unidentified call.
				continue
			}
			yield(QueryEvent{
				Type: QueryEventType("tool_result"),
				Message: &schema.Message{
					Role:       schema.Tool,
					Content:    errMsg,
					ToolCallID: toolCallID,
					ToolName:   tc.Function.Name,
					Extra:      map[string]any{"is_error": true},
				},
			})
		}
	}
}
