package engine

import (
	"fmt"
	"sync"
)

// TurnTracker manages turn counting and max-turn enforcement for the query loop.
// It provides a composable helper that validates turn limits without modifying
// the existing query loop structure.
//
// The tracker enforces:
//   - Turn counting starts at 1 and increments after each complete turn
//   - Max-turn limit is validated before allowing a new turn
//   - Notification event is emitted when the limit is reached
//   - The tracker is safe for concurrent reads but designed for sequential use
//
// Mirrors the turn counting logic in query.ts:1705-1711.
type TurnTracker struct {
	mu        sync.Mutex
	current   int
	maxTurns  int
	exhausted bool
}

// NewTurnTracker creates a TurnTracker with the given max turns limit.
// A maxTurns value of 0 means unlimited turns. Callers reject negative values.
func NewTurnTracker(maxTurns int) *TurnTracker {
	return &TurnTracker{
		current:  1,
		maxTurns: maxTurns,
	}
}

// Current returns the current turn number (1-based).
func (t *TurnTracker) Current() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.current
}

// MaxTurns returns the configured max turns limit (0 means unlimited).
func (t *TurnTracker) MaxTurns() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.maxTurns
}

// Exhausted returns true if the max turn limit has been reached.
func (t *TurnTracker) Exhausted() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exhausted
}

// Advance increments the turn counter and checks against the max-turn limit.
// Returns a TurnAdvanceResult indicating whether the next turn is allowed or
// whether the limit has been reached.
//
// This should be called after all tool results are processed and before
// the loop continues to the next iteration.
func (t *TurnTracker) Advance() TurnAdvanceResult {
	t.mu.Lock()
	defer t.mu.Unlock()

	nextTurn := t.current + 1

	if t.maxTurns > 0 && nextTurn > t.maxTurns {
		t.exhausted = true
		return TurnAdvanceResult{
			Allowed:   false,
			TurnCount: nextTurn,
			MaxTurns:  t.maxTurns,
			Message:   fmt.Sprintf("Reached maximum number of turns (%d)", t.maxTurns),
		}
	}

	t.current = nextTurn
	return TurnAdvanceResult{
		Allowed:   true,
		TurnCount: nextTurn,
		MaxTurns:  t.maxTurns,
	}
}

// Reset resets the tracker to turn 1. Useful for testing or re-query scenarios.
func (t *TurnTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.current = 1
	t.exhausted = false
}

// SetMaxTurns updates the max turns limit dynamically.
func (t *TurnTracker) SetMaxTurns(maxTurns int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.maxTurns = maxTurns
}

// TurnAdvanceResult is returned by Advance() to communicate whether the next
// turn is allowed and provide information for the max-turns-reached event.
type TurnAdvanceResult struct {
	// Allowed indicates whether the loop may continue to the next turn.
	Allowed bool

	// TurnCount is the turn number that would be entered (current+1).
	TurnCount int

	// MaxTurns is the configured limit (0 means unlimited).
	MaxTurns int

	// Message is a human-readable notification when the limit is reached.
	// Empty when Allowed is true.
	Message string
}

// ToMaxTurnsInfo converts the result to a MaxTurnsInfo event payload.
// Returns nil if the turn was allowed (not exhausted).
func (r TurnAdvanceResult) ToMaxTurnsInfo() *MaxTurnsInfo {
	if r.Allowed {
		return nil
	}
	return &MaxTurnsInfo{
		MaxTurns:  r.MaxTurns,
		TurnCount: r.TurnCount,
	}
}
