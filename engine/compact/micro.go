package compact

import "github.com/cloudwego/eino/schema"

// MicrocompactInfo holds deferred boundary metadata for cache-safe microcompact.
type MicrocompactInfo struct {
	Trigger                    string
	DeletedToolIDs             []string
	BaselineCacheDeletedTokens int
}

// MicrocompactResult holds the result of micro-compaction.
type MicrocompactResult struct {
	Messages       []*schema.Message
	CompactionInfo *MicrocompactInfo
}

// Microcompact runs bounded history trimming before auto-compaction. Idle-time
// clearing retains its own policy; standard trimming requires context pressure
// at the preparation-time model's existing auto-compact threshold.
func Microcompact(
	messages []*schema.Message,
	querySource string,
	modelName string,
) *MicrocompactResult {
	// Skip for compact queries to avoid double-compacting.
	if querySource == "compact" {
		return &MicrocompactResult{
			Messages:       messages,
			CompactionInfo: nil,
		}
	}

	// Priority 1: Time-based microcompact — clears old tool results after idle gap.
	if tbResult := TimeBasedMicrocompact(messages, querySource); tbResult != nil && tbResult.Applied {
		return &MicrocompactResult{
			Messages: tbResult.Messages,
			CompactionInfo: &MicrocompactInfo{
				Trigger: "time_based",
			},
		}
	}

	// Rewriting an already-sent tool result or reasoning block invalidates its
	// cached prefix and removes useful history. Do so only to relieve pressure,
	// using the same model capacity and user overrides as auto-compaction.
	targetTokensToFree := EstimateTokenCount(messages) - GetAutoCompactThreshold(modelName) + 1
	if targetTokensToFree <= 0 {
		return &MicrocompactResult{Messages: messages}
	}
	result := MicroCompact(messages, targetTokensToFree)
	if !result.Applied {
		return &MicrocompactResult{
			Messages:       messages,
			CompactionInfo: nil,
		}
	}

	// Build compaction info for tracking.
	info := &MicrocompactInfo{
		Trigger: "pre_auto_compact",
	}

	return &MicrocompactResult{
		Messages:       result.Messages,
		CompactionInfo: info,
	}
}
