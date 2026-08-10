package engine

import (
	"testing"
)

func TestTurnTrackerAdvanceAllowsWithinLimit(t *testing.T) {
	tracker := NewTurnTracker(5)

	if tracker.Current() != 1 {
		t.Fatalf("expected initial turn 1, got %d", tracker.Current())
	}

	// Advance through turns 2-5
	for i := 2; i <= 5; i++ {
		result := tracker.Advance()
		if !result.Allowed {
			t.Fatalf("expected turn %d to be allowed", i)
		}
		if tracker.Current() != i {
			t.Fatalf("expected current turn %d, got %d", i, tracker.Current())
		}
	}
}

func TestTurnTrackerAdvanceBlocksAtLimit(t *testing.T) {
	tracker := NewTurnTracker(3)

	// Advance to turn 2
	result := tracker.Advance()
	if !result.Allowed {
		t.Fatal("turn 2 should be allowed")
	}

	// Advance to turn 3
	result = tracker.Advance()
	if !result.Allowed {
		t.Fatal("turn 3 should be allowed")
	}

	// Turn 4 should exceed limit
	result = tracker.Advance()
	if result.Allowed {
		t.Fatal("turn 4 should NOT be allowed (limit is 3)")
	}
	if result.TurnCount != 4 {
		t.Fatalf("expected TurnCount=4, got %d", result.TurnCount)
	}
	if result.MaxTurns != 3 {
		t.Fatalf("expected MaxTurns=3, got %d", result.MaxTurns)
	}
	if result.Message == "" {
		t.Fatal("expected non-empty message for max turns reached")
	}
}

func TestTurnTrackerUnlimitedTurns(t *testing.T) {
	tracker := NewTurnTracker(0) // 0 means unlimited

	// Should allow many turns without blocking
	for i := 0; i < 200; i++ {
		result := tracker.Advance()
		if !result.Allowed {
			t.Fatalf("unlimited tracker blocked at turn %d", i+2)
		}
	}
	if tracker.Exhausted() {
		t.Fatal("unlimited tracker should never be exhausted")
	}
}

func TestTurnTrackerReset(t *testing.T) {
	tracker := NewTurnTracker(3)

	tracker.Advance()
	tracker.Advance()
	tracker.Advance() // should exhaust

	if !tracker.Exhausted() {
		t.Fatal("expected exhausted after 3 advances")
	}

	tracker.Reset()

	if tracker.Current() != 1 {
		t.Fatalf("expected turn 1 after reset, got %d", tracker.Current())
	}
	if tracker.Exhausted() {
		t.Fatal("expected not exhausted after reset")
	}

	// Should allow advances again
	result := tracker.Advance()
	if !result.Allowed {
		t.Fatal("expected allowed after reset")
	}
}

func TestTurnTrackerToMaxTurnsInfo(t *testing.T) {
	tracker := NewTurnTracker(2)

	// Advance until blocked
	tracker.Advance()           // turn 2 allowed
	result := tracker.Advance() // turn 3 blocked

	info := result.ToMaxTurnsInfo()
	if info == nil {
		t.Fatal("expected non-nil MaxTurnsInfo for blocked advance")
		return
	}
	if info.MaxTurns != 2 {
		t.Fatalf("expected MaxTurns=2, got %d", info.MaxTurns)
	}
	if info.TurnCount != 3 {
		t.Fatalf("expected TurnCount=3, got %d", info.TurnCount)
	}

	// Allowed result should return nil info
	tracker2 := NewTurnTracker(10)
	result2 := tracker2.Advance()
	if result2.ToMaxTurnsInfo() != nil {
		t.Fatal("expected nil MaxTurnsInfo for allowed advance")
		return
	}
}

func TestTurnTrackerSetMaxTurns(t *testing.T) {
	tracker := NewTurnTracker(100)
	tracker.SetMaxTurns(2)

	tracker.Advance()           // turn 2 allowed
	result := tracker.Advance() // turn 3 blocked

	if result.Allowed {
		t.Fatal("expected blocked after dynamic max turns change to 2")
	}
}
