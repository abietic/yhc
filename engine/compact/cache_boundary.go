package compact

import (
	"sort"

	"github.com/cloudwego/eino/schema"
)

// CacheSplitPoint describes an optimal position in the message history where
// compaction can safely split without breaking API-round invariants (e.g.,
// mid-tool-call sequences). Each split point is provider-neutral: it relies
// only on message role sequencing, not on provider-specific cache headers.
//
// Mirrors the boundary detection logic in the reference
// compact/grouping.ts + compact/compact.ts truncateHeadForPTLRetry.
type CacheSplitPoint struct {
	// Index is the message index where the split occurs. Messages before this
	// index form the "to compact" set; messages from this index onward form
	// the "to keep" set.
	Index int

	// TokensBefore estimates the token count of messages before the split.
	TokensBefore int

	// TokensAfter estimates the token count of messages from the split onward.
	TokensAfter int

	// Reason describes why this is a valid split point.
	Reason string
}

// FindCacheSafeSplitPoints identifies positions in the message history where
// the conversation can be safely split for compaction. Each split point
// satisfies:
//   - Never splits in the middle of a tool-call/tool-result pair
//   - Never splits inside a "thinking" sequence (assistant reasoning content)
//   - Prefers boundaries between user turns (natural conversation boundaries)
//   - Respects API-round boundaries (from GroupMessagesByAPIRound)
//
// The returned split points are sorted by preference: the first element is
// the "best" split (closest to the target ratio), and subsequent elements
// are alternatives. Returns nil if no valid split points exist.
//
// targetRatio controls the desired balance: 0.5 means split in the middle,
// 0.7 means compact 70% and keep 30%, etc. Clamped to [0.2, 0.9].
//
// This is the provider-neutral cache boundary detection that the reference
// runtime uses to decide where in the conversation to draw the compaction line.
func FindCacheSafeSplitPoints(messages []*schema.Message, targetRatio float64) []CacheSplitPoint {
	if len(messages) < 2 {
		return nil
	}

	// Clamp target ratio
	if targetRatio < 0.2 {
		targetRatio = 0.2
	}
	if targetRatio > 0.9 {
		targetRatio = 0.9
	}

	// Compute total tokens for ratio calculations.
	totalTokens := EstimateTokenCount(messages)
	if totalTokens == 0 {
		return nil
	}

	// Step 1: Find all candidate split points. We consider two sources:
	//
	// (a) API-round boundaries from GroupMessagesByAPIRound — these are always
	//     safe because tool-call/result pairs are kept together within a group.
	//
	// (b) User-message boundaries — positions where a real user message starts.
	//     These provide more natural conversation splits (matching the reference's
	//     FindPartialCompactPivot behavior). Each is validated to ensure it
	//     doesn't break a tool-call/result pair.
	type scoredSplit struct {
		index        int
		tokensBefore int
		score        float64 // lower is better
		reason       string
	}

	// Compute cumulative tokens at each message for fast range lookups.
	cumTokens := make([]int, len(messages)+1)
	for i, msg := range messages {
		cumTokens[i+1] = cumTokens[i] + estimateMessageTokens(msg)
	}

	targetTokens := int(float64(totalTokens) * targetRatio)

	// Track seen indices to avoid duplicates.
	seen := make(map[int]bool)
	var scored []scoredSplit

	// Source (a): API-round group boundaries.
	groups := GroupMessagesByAPIRound(messages)
	if len(groups) >= 2 {
		msgOffset := 0
		for gi := 0; gi < len(groups)-1; gi++ { // Skip last boundary
			msgOffset += len(groups[gi])
			if seen[msgOffset] {
				continue
			}
			seen[msgOffset] = true

			tokensBefore := cumTokens[msgOffset]
			dist := float64(tokensBefore-targetTokens) / float64(totalTokens)
			if dist < 0 {
				dist = -dist
			}
			scored = append(scored, scoredSplit{
				index:        msgOffset,
				tokensBefore: tokensBefore,
				score:        dist,
				reason:       "api_round_boundary",
			})
		}
	}

	// Source (b): User-message boundaries (validated).
	for i := 1; i < len(messages)-1; i++ {
		msg := messages[i]
		if msg == nil || msg.Role != schema.User {
			continue
		}
		if isMetaMessage(msg) {
			continue
		}
		if seen[i] {
			// Already found as a group boundary — upgrade its reason
			for j := range scored {
				if scored[j].index == i {
					scored[j].reason = "user_turn_boundary"
					scored[j].score -= 0.1 // preference bonus
					break
				}
			}
			continue
		}

		// Validate this is a safe split point (not mid-tool-call)
		if valid, _ := ValidateSplitPoint(messages, i); !valid {
			continue
		}

		seen[i] = true
		tokensBefore := cumTokens[i]
		dist := float64(tokensBefore-targetTokens) / float64(totalTokens)
		if dist < 0 {
			dist = -dist
		}
		scored = append(scored, scoredSplit{
			index:        i,
			tokensBefore: tokensBefore,
			score:        dist - 0.1, // preference bonus for user turn boundary
			reason:       "user_turn_boundary",
		})
	}

	if len(scored) == 0 {
		return nil
	}

	// Sort by score (lower = better)
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score < scored[j].score
	})

	// Return top 5 results.
	limit := len(scored)
	if limit > 5 {
		limit = 5
	}
	results := make([]CacheSplitPoint, 0, limit)
	for i := 0; i < limit; i++ {
		sb := scored[i]
		tokensAfter := totalTokens - sb.tokensBefore
		results = append(results, CacheSplitPoint{
			Index:        sb.index,
			TokensBefore: sb.tokensBefore,
			TokensAfter:  tokensAfter,
			Reason:       sb.reason,
		})
	}

	return results
}

// ValidateSplitPoint checks whether a given index is a safe split point in the
// message history. A safe split point must not:
//   - Fall inside a tool-call/tool-result pair (an assistant message with ToolCalls
//     must have all its corresponding tool result messages in the same partition)
//   - Fall inside an assistant reasoning sequence (message with ReasoningContent)
//
// Returns true if the index is a valid split point, false otherwise with a reason.
func ValidateSplitPoint(messages []*schema.Message, index int) (bool, string) {
	if index <= 0 || index >= len(messages) {
		return false, "index out of valid range (must be 1..len-1)"
	}

	// Check if we're splitting inside a tool-call/result pair.
	// Look at the message immediately before the split: if it's an assistant
	// with tool calls, check whether all its tool results are also before the split.
	prevMsg := messages[index-1]
	if prevMsg != nil && prevMsg.Role == schema.Assistant && len(prevMsg.ToolCalls) > 0 {
		// Collect the tool call IDs that need results
		needed := make(map[string]bool)
		for _, tc := range prevMsg.ToolCalls {
			if tc.ID != "" {
				needed[tc.ID] = true
			}
		}

		// Check tool results between prevMsg and the split point
		for j := index; j < len(messages); j++ {
			msg := messages[j]
			if msg == nil {
				continue
			}
			if msg.Role == schema.Tool && msg.ToolCallID != "" {
				if needed[msg.ToolCallID] {
					// A tool result for this call exists AFTER the split — invalid
					return false, "splits inside tool-call/result pair"
				}
			}
			// Stop scanning once we hit a non-tool message after the split
			if msg.Role != schema.Tool {
				break
			}
		}
	}

	// Check if the message AT the split index is a tool result. If so, its
	// corresponding tool call must also be after the split (or the split is invalid).
	atSplit := messages[index]
	if atSplit != nil && atSplit.Role == schema.Tool && atSplit.ToolCallID != "" {
		// Find the corresponding tool call — if it's before the split, this is invalid
		for j := index - 1; j >= 0; j-- {
			msg := messages[j]
			if msg == nil {
				continue
			}
			if msg.Role == schema.Assistant {
				for _, tc := range msg.ToolCalls {
					if tc.ID == atSplit.ToolCallID {
						return false, "splits tool result from its tool call"
					}
				}
			}
		}
	}

	// Check if we're splitting inside thinking/reasoning content.
	// If the previous message has reasoning content and the next message is
	// from the same assistant turn (same message_id), splitting here would
	// break the reasoning sequence.
	if prevMsg != nil && prevMsg.Role == schema.Assistant && prevMsg.ReasoningContent != "" {
		if index < len(messages) {
			nextMsg := messages[index]
			if nextMsg != nil && nextMsg.Role == schema.Assistant {
				prevID := getMessageID(prevMsg)
				nextID := getMessageID(nextMsg)
				if prevID != "" && prevID == nextID {
					return false, "splits inside thinking/reasoning sequence"
				}
			}
		}
	}

	return true, ""
}

// isMetaMessage checks if a message is a meta/attachment message rather than
// a real user input.
func isMetaMessage(msg *schema.Message) bool {
	if msg == nil || msg.Extra == nil {
		return false
	}
	if _, ok := msg.Extra["is_meta"]; ok {
		return true
	}
	if _, ok := msg.Extra["isMeta"]; ok {
		return true
	}
	subtype, _ := msg.Extra["subtype"].(string)
	return subtype == "attachment" || subtype == "compact_boundary" || subtype == "compact_summary"
}
