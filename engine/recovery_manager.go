package engine

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// RecoveryManager provides bounded failure recovery cascades for the query loop.
// It tracks retry counts per error category and enforces maximum retry limits
// to prevent infinite loops.
//
// Recovery categories:
//   - PTL (Prompt Too Long): max 3 attempts via compact-then-retry
//   - Overloaded (529): max 5 attempts with exponential backoff
//   - RateLimit (429): max 5 attempts, respects retry-after headers
//   - Generic: max 1 retry before surfacing to the user
//
// The manager is designed to be composed with the existing query loop without
// modifying the CallModelWithRetry logic (which handles transient 429/529 retries
// internally). RecoveryManager handles the higher-level cascade decisions that
// operate between turns.
//
// Mirrors the recovery cascade in query.ts:1069-1256.
type RecoveryManager struct {
	mu     sync.Mutex
	counts map[RecoveryCategory]int
	limits map[RecoveryCategory]int
}

// RecoveryCategory classifies the type of failure for retry tracking.
type RecoveryCategory string

const (
	RecoveryCategoryPTL        RecoveryCategory = "prompt_too_long"
	RecoveryCategoryOverloaded RecoveryCategory = "overloaded"
	RecoveryCategoryRateLimit  RecoveryCategory = "rate_limit"
	RecoveryCategoryMaxTokens  RecoveryCategory = "max_output_tokens"
	RecoveryCategoryGeneric    RecoveryCategory = "generic"
)

// Default retry limits per category. These mirror the reference implementation's
// bounded retry behavior.
const (
	DefaultPTLMaxAttempts        = 3
	DefaultOverloadedMaxAttempts = 5
	DefaultRateLimitMaxAttempts  = 5
	DefaultMaxTokensMaxAttempts  = 3
	DefaultGenericMaxAttempts    = 1
)

// NewRecoveryManager creates a RecoveryManager with default limits.
func NewRecoveryManager() *RecoveryManager {
	return &RecoveryManager{
		counts: make(map[RecoveryCategory]int),
		limits: map[RecoveryCategory]int{
			RecoveryCategoryPTL:        DefaultPTLMaxAttempts,
			RecoveryCategoryOverloaded: DefaultOverloadedMaxAttempts,
			RecoveryCategoryRateLimit:  DefaultRateLimitMaxAttempts,
			RecoveryCategoryMaxTokens:  DefaultMaxTokensMaxAttempts,
			RecoveryCategoryGeneric:    DefaultGenericMaxAttempts,
		},
	}
}

// NewRecoveryManagerWithLimits creates a RecoveryManager with custom limits.
func NewRecoveryManagerWithLimits(limits map[RecoveryCategory]int) *RecoveryManager {
	rm := NewRecoveryManager()
	for cat, limit := range limits {
		rm.limits[cat] = limit
	}
	return rm
}

// CanRetry checks whether the given category has remaining retry budget.
func (rm *RecoveryManager) CanRetry(category RecoveryCategory) bool {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	limit, ok := rm.limits[category]
	if !ok {
		return false
	}
	return rm.counts[category] < limit
}

// RecordAttempt increments the retry count for a category.
// Returns the new count after increment.
func (rm *RecoveryManager) RecordAttempt(category RecoveryCategory) int {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.counts[category]++
	return rm.counts[category]
}

// AttemptCount returns the current retry count for a category.
func (rm *RecoveryManager) AttemptCount(category RecoveryCategory) int {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return rm.counts[category]
}

// ResetCategory resets the retry count for a specific category.
// Called when a successful response is received (e.g., PTL counter resets
// after a successful model call following compaction).
func (rm *RecoveryManager) ResetCategory(category RecoveryCategory) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.counts[category] = 0
}

// ResetAll resets all retry counters.
func (rm *RecoveryManager) ResetAll() {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	for k := range rm.counts {
		delete(rm.counts, k)
	}
}

// TryRecover evaluates the failure category and returns a RecoveryPlan.
// The plan tells the caller what action to take (retry, compact, surface).
// This does NOT automatically record an attempt — the caller should call
// RecordAttempt after confirming the recovery action will be taken.
func (rm *RecoveryManager) TryRecover(category RecoveryCategory) RecoveryPlan {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	limit, ok := rm.limits[category]
	if !ok {
		return RecoveryPlan{
			Action:  RecoveryActionSurface,
			Reason:  fmt.Sprintf("unknown recovery category: %s", category),
			Attempt: 0,
		}
	}

	count := rm.counts[category]
	if count >= limit {
		return RecoveryPlan{
			Action:  RecoveryActionSurface,
			Reason:  fmt.Sprintf("max retries exhausted for %s (%d/%d)", category, count, limit),
			Attempt: count,
		}
	}

	switch category {
	case RecoveryCategoryPTL:
		return RecoveryPlan{
			Action:  RecoveryActionCompactThenRetry,
			Reason:  fmt.Sprintf("prompt too long, attempting compaction (attempt %d/%d)", count+1, limit),
			Attempt: count,
		}
	case RecoveryCategoryOverloaded:
		return RecoveryPlan{
			Action:  RecoveryActionBackoffThenRetry,
			Reason:  fmt.Sprintf("server overloaded, backing off (attempt %d/%d)", count+1, limit),
			Delay:   backoffDelay(count),
			Attempt: count,
		}
	case RecoveryCategoryRateLimit:
		return RecoveryPlan{
			Action:  RecoveryActionBackoffThenRetry,
			Reason:  fmt.Sprintf("rate limited, backing off (attempt %d/%d)", count+1, limit),
			Delay:   rateLimitDelay(count),
			Attempt: count,
		}
	case RecoveryCategoryMaxTokens:
		return RecoveryPlan{
			Action:  RecoveryActionContinueInNextTurn,
			Reason:  fmt.Sprintf("max output tokens hit, continuing in next turn (attempt %d/%d)", count+1, limit),
			Attempt: count,
		}
	case RecoveryCategoryGeneric:
		return RecoveryPlan{
			Action:  RecoveryActionRetryOnce,
			Reason:  fmt.Sprintf("generic error, retrying once (attempt %d/%d)", count+1, limit),
			Attempt: count,
		}
	default:
		return RecoveryPlan{
			Action:  RecoveryActionSurface,
			Reason:  fmt.Sprintf("no recovery strategy for %s", category),
			Attempt: count,
		}
	}
}

// WaitForBackoff blocks until the delay expires or the context is cancelled.
// Returns nil on successful wait, or the context error if cancelled.
func WaitForBackoff(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// RecoveryAction describes the high-level action the loop should take.
type RecoveryAction string

const (
	// RecoveryActionCompactThenRetry means: compact the conversation, then retry
	// the model call. Used for PTL errors.
	RecoveryActionCompactThenRetry RecoveryAction = "compact_then_retry"

	// RecoveryActionBackoffThenRetry means: wait with exponential backoff, then
	// retry the model call. Used for overloaded/rate-limit errors.
	RecoveryActionBackoffThenRetry RecoveryAction = "backoff_then_retry"

	// RecoveryActionContinueInNextTurn means: the response was truncated at
	// max_tokens but is not a failure — continue processing in the next turn.
	RecoveryActionContinueInNextTurn RecoveryAction = "continue_in_next_turn"

	// RecoveryActionRetryOnce means: retry exactly once, then surface on failure.
	RecoveryActionRetryOnce RecoveryAction = "retry_once"

	// RecoveryActionSurface means: no recovery possible — surface the error to the user.
	RecoveryActionSurface RecoveryAction = "surface"
)

// RecoveryPlan describes what the caller should do in response to a failure.
type RecoveryPlan struct {
	// Action is the recovery strategy to apply.
	Action RecoveryAction

	// Reason is a human-readable explanation of the decision.
	Reason string

	// Delay is the backoff duration for BackoffThenRetry actions.
	// Zero for non-backoff actions.
	Delay time.Duration

	// Attempt is the current attempt number (0-based) before this recovery.
	Attempt int
}

// backoffDelay calculates exponential backoff with jitter for overloaded errors.
// Uses 2^attempt seconds as base, capped at 60 seconds, with ±25% jitter.
func backoffDelay(attempt int) time.Duration {
	baseMs := 2000.0 // 2 seconds base
	for i := 0; i < attempt; i++ {
		baseMs *= 2
	}
	if baseMs > 60000 {
		baseMs = 60000
	}
	// Deterministic jitter based on attempt number (avoids rand import).
	// This is good enough for delays — exact randomness is not critical.
	jitterFactor := 0.75 + float64(attempt%4)*0.125 // 0.75 to 1.125
	return time.Duration(baseMs*jitterFactor) * time.Millisecond
}

// rateLimitDelay calculates a longer delay for rate-limit errors.
// Rate limits often specify retry-after headers; this provides a sensible
// default when the header isn't available.
func rateLimitDelay(attempt int) time.Duration {
	baseMs := 5000.0 // 5 seconds base (rate limits need longer waits)
	for i := 0; i < attempt; i++ {
		baseMs *= 2
	}
	if baseMs > 120000 {
		baseMs = 120000 // 2 minute cap
	}
	return time.Duration(baseMs) * time.Millisecond
}
