package recovery

import "github.com/cloudwego/eino/schema"

// MaxTokensResult holds the result of max_output_tokens recovery.
type MaxTokensResult struct {
	Retry                   bool
	Continue                bool
	Terminal                bool
	Reason                  string
	MaxOutputTokensOverride *int
	RecoveryMessage         *schema.Message
}

const (
	maxOutputTokensRecoveryLimit = 3
	escalatedMaxTokens           = 64000
	maxTokensRecoveryPrompt      = "Output token limit hit. Resume directly — no apology, no recap of what you were doing. Pick up mid-thought if that is where the cut happened. Break remaining work into smaller pieces."
)

// TryMaxTokensRecovery attempts max_output_tokens recovery:
//  1. Escalate to 64k (one-shot)
//  2. Multi-turn recovery message (up to 3 retries)
//  3. Exhausted -> surface
//
// Mirrors query.ts:1185-1256.
func TryMaxTokensRecovery(
	recoveryCount int,
	maxOutputTokensOverride *int,
	capEnabled bool,
) *MaxTokensResult {
	// Stage 1: Escalate to 64k (one-shot)
	if capEnabled && recoveryCount == 0 && maxOutputTokensOverride == nil {
		escalated := escalatedMaxTokens
		return &MaxTokensResult{
			Retry:                   true,
			Continue:                true,
			Reason:                  "max_output_tokens_escalate",
			MaxOutputTokensOverride: &escalated,
		}
	}

	// Stage 2: Multi-turn recovery (up to limit)
	if recoveryCount < maxOutputTokensRecoveryLimit {
		return &MaxTokensResult{
			Retry:    true,
			Continue: true,
			Reason:   "max_output_tokens_recovery",
			RecoveryMessage: &schema.Message{
				Role:    schema.User,
				Content: maxTokensRecoveryPrompt,
				Extra:   map[string]any{"is_meta": true},
			},
		}
	}

	// Stage 3: Exhausted
	return &MaxTokensResult{Terminal: true, Reason: "model_error"}
}
