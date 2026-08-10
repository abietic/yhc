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

// Microcompact runs cache-safe tool result compaction BEFORE auto-compact.
// Mirrors query.ts:413-425 and microCompact.ts microcompactMessages.
// Priority: time-based MC (short-circuit) → standard micro-compact.
func Microcompact(
	messages []*schema.Message,
	querySource string,
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

	// Priority 2: Standard micro-compaction with a large target (trim everything possible).
	result := MicroCompact(messages, 1<<31-1)
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
