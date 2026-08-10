package recovery

import "github.com/cloudwego/eino/schema"

// Reason constants (local to avoid circular import)
type RecoveryReason string

const (
	ReasonCollapseDrainRetry   RecoveryReason = "collapse_drain_retry"
	ReasonReactiveCompactRetry RecoveryReason = "reactive_compact_retry"
)

// PTLResult holds the result of prompt-too-long recovery.
type PTLResult struct {
	Retry    bool
	Continue bool
	Terminal bool
	Reason   string
	Messages []*schema.Message
}

// DrainResultT is (local type mirroring compact.DrainResult)
type DrainResultT struct {
	Committed int
	Messages  []*schema.Message
}

// TryPTLRecovery runs the 3-stage PTL recovery cascade:
//  1. Collapse drain (cheap, keeps granular context)
//  2. Reactive compact (full summarization)
//  3. Surface (nothing left to try)
//
// Mirrors query.ts:1069-1183.
func TryPTLRecovery(
	lastAssistant *schema.Message,
	messages []*schema.Message,
	querySource string,
	hasAttemptedReactiveCompact bool,
	previousTransition RecoveryReason,
	tryCollapseDrain func(messages []*schema.Message, querySource string) *DrainResultT,
	tryReactiveCompact func(messages []*schema.Message, querySource string) []*schema.Message,
) *PTLResult {
	// Stage 1: Collapse drain (if not already attempted)
	if previousTransition != ReasonCollapseDrainRetry {
		if tryCollapseDrain != nil {
			drained := tryCollapseDrain(messages, querySource)
			if drained != nil && drained.Committed > 0 && len(drained.Messages) > 0 {
				return &PTLResult{
					Retry:    true,
					Continue: true,
					Reason:   "collapse_drain_retry",
					Messages: drained.Messages,
				}
			}
		}
	}

	// Stage 2: Reactive compact (if not already attempted)
	if !hasAttemptedReactiveCompact {
		if tryReactiveCompact != nil {
			compacted := tryReactiveCompact(messages, querySource)
			if len(compacted) > 0 {
				return &PTLResult{
					Retry:    true,
					Continue: true,
					Reason:   "reactive_compact_retry",
					Messages: compacted,
				}
			}
		}
	}

	// Stage 3: Surface
	return &PTLResult{Terminal: true, Reason: "prompt_too_long"}
}
