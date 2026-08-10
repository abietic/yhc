package compact

import (
	"math"

	"github.com/cloudwego/eino/schema"
)

const (
	// MaxPTLRetries is the maximum number of prompt-too-long retry attempts.
	MaxPTLRetries = 3

	// ptlRetryMarker is prepended to truncated messages so the API sees a
	// user-first sequence after head groups are dropped.
	ptlRetryMarker = "[earlier conversation truncated for compaction retry]"
)

// GroupMessagesByAPIRound splits messages into groups at API-round boundaries.
// A new group starts when a new assistant message is seen (different from the
// previous assistant message, determined by Extra["message_id"]).
// Mirrors reference compact/grouping.ts groupMessagesByApiRound.
func GroupMessagesByAPIRound(messages []*schema.Message) [][]*schema.Message {
	var groups [][]*schema.Message
	var current []*schema.Message
	var lastAssistantID string
	seenAssistant := false

	for _, msg := range messages {
		if msg == nil {
			continue
		}

		// Detect new assistant turn boundary
		if msg.Role == schema.Assistant {
			msgID := getMessageID(msg)
			if seenAssistant && msgID != lastAssistantID && len(current) > 0 {
				groups = append(groups, current)
				current = []*schema.Message{msg}
			} else {
				current = append(current, msg)
			}
			lastAssistantID = msgID
			seenAssistant = true
		} else {
			current = append(current, msg)
		}
	}

	if len(current) > 0 {
		groups = append(groups, current)
	}
	return groups
}

// getMessageID extracts a message ID from Extra metadata.
// Falls back to empty string if not present.
func getMessageID(msg *schema.Message) string {
	if msg == nil || msg.Extra == nil {
		return ""
	}
	if id, ok := msg.Extra["message_id"].(string); ok {
		return id
	}
	// Also check "id" field
	if id, ok := msg.Extra["id"].(string); ok {
		return id
	}
	return ""
}

// TruncateHeadForPTLRetry drops the oldest API-round groups from messages
// until tokenGap tokens are freed. Returns nil when nothing can be dropped
// without leaving an empty summarize set.
// Mirrors reference compact/compact.ts truncateHeadForPTLRetry.
func TruncateHeadForPTLRetry(messages []*schema.Message, tokenGap int) []*schema.Message {
	// Strip our own synthetic marker from a previous retry before grouping
	input := messages
	if len(input) > 0 && input[0] != nil && input[0].Role == schema.User {
		if input[0].Content == ptlRetryMarker {
			input = input[1:]
		}
	}

	groups := GroupMessagesByAPIRound(input)
	if len(groups) < 2 {
		return nil
	}

	var dropCount int
	if tokenGap > 0 {
		acc := 0
		for _, g := range groups {
			acc += EstimateTokenCount(g)
			dropCount++
			if acc >= tokenGap {
				break
			}
		}
	} else {
		// Fallback: drop 20% of groups when gap is unparseable
		dropCount = int(math.Max(1, math.Floor(float64(len(groups))*0.2)))
	}

	// Keep at least one group
	if dropCount >= len(groups) {
		dropCount = len(groups) - 1
	}
	if dropCount < 1 {
		return nil
	}

	// Flatten remaining groups
	var sliced []*schema.Message
	for _, g := range groups[dropCount:] {
		sliced = append(sliced, g...)
	}

	// If first message is assistant, prepend a synthetic user marker
	if len(sliced) > 0 && sliced[0] != nil && sliced[0].Role == schema.Assistant {
		marker := &schema.Message{
			Role:    schema.User,
			Content: ptlRetryMarker,
		}
		sliced = append([]*schema.Message{marker}, sliced...)
	}
	return sliced
}
