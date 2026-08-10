package engine

import (
	"context"
	"testing"
	"time"
)

func TestRecoveryManagerCanRetryWithinLimit(t *testing.T) {
	rm := NewRecoveryManager()

	// PTL has 3 attempts
	for i := 0; i < DefaultPTLMaxAttempts; i++ {
		if !rm.CanRetry(RecoveryCategoryPTL) {
			t.Fatalf("expected CanRetry=true at attempt %d", i)
		}
		rm.RecordAttempt(RecoveryCategoryPTL)
	}

	// After 3 attempts, should be exhausted
	if rm.CanRetry(RecoveryCategoryPTL) {
		t.Fatal("expected CanRetry=false after max attempts")
	}
}

func TestRecoveryManagerIndependentCategories(t *testing.T) {
	rm := NewRecoveryManager()

	// Exhaust PTL
	for i := 0; i < DefaultPTLMaxAttempts; i++ {
		rm.RecordAttempt(RecoveryCategoryPTL)
	}

	// Overloaded should still have budget
	if !rm.CanRetry(RecoveryCategoryOverloaded) {
		t.Fatal("overloaded should be independent of PTL")
	}
}

func TestRecoveryManagerResetCategory(t *testing.T) {
	rm := NewRecoveryManager()

	// Use up PTL budget
	for i := 0; i < DefaultPTLMaxAttempts; i++ {
		rm.RecordAttempt(RecoveryCategoryPTL)
	}
	if rm.CanRetry(RecoveryCategoryPTL) {
		t.Fatal("expected exhausted before reset")
	}

	// Reset PTL
	rm.ResetCategory(RecoveryCategoryPTL)
	if !rm.CanRetry(RecoveryCategoryPTL) {
		t.Fatal("expected CanRetry=true after reset")
	}
}

func TestRecoveryManagerTryRecoverPTL(t *testing.T) {
	rm := NewRecoveryManager()

	plan := rm.TryRecover(RecoveryCategoryPTL)
	if plan.Action != RecoveryActionCompactThenRetry {
		t.Fatalf("expected compact_then_retry, got %s", plan.Action)
	}
	if plan.Attempt != 0 {
		t.Fatalf("expected attempt 0, got %d", plan.Attempt)
	}
}

func TestRecoveryManagerTryRecoverOverloaded(t *testing.T) {
	rm := NewRecoveryManager()

	plan := rm.TryRecover(RecoveryCategoryOverloaded)
	if plan.Action != RecoveryActionBackoffThenRetry {
		t.Fatalf("expected backoff_then_retry, got %s", plan.Action)
	}
	if plan.Delay <= 0 {
		t.Fatal("expected positive delay for overloaded backoff")
	}
}

func TestRecoveryManagerTryRecoverRateLimit(t *testing.T) {
	rm := NewRecoveryManager()

	plan := rm.TryRecover(RecoveryCategoryRateLimit)
	if plan.Action != RecoveryActionBackoffThenRetry {
		t.Fatalf("expected backoff_then_retry, got %s", plan.Action)
	}
	if plan.Delay <= 0 {
		t.Fatal("expected positive delay for rate limit backoff")
	}
	// Rate limit delay should be >= overloaded delay for same attempt
	overloadedPlan := rm.TryRecover(RecoveryCategoryOverloaded)
	if plan.Delay < overloadedPlan.Delay {
		t.Fatalf("expected rate limit delay >= overloaded delay, got %v < %v",
			plan.Delay, overloadedPlan.Delay)
	}
}

func TestRecoveryManagerTryRecoverGenericExhaustsAfterOne(t *testing.T) {
	rm := NewRecoveryManager()

	// First attempt should allow retry
	plan := rm.TryRecover(RecoveryCategoryGeneric)
	if plan.Action != RecoveryActionRetryOnce {
		t.Fatalf("expected retry_once, got %s", plan.Action)
	}

	// Record the attempt
	rm.RecordAttempt(RecoveryCategoryGeneric)

	// Second attempt should surface
	plan = rm.TryRecover(RecoveryCategoryGeneric)
	if plan.Action != RecoveryActionSurface {
		t.Fatalf("expected surface after exhaustion, got %s", plan.Action)
	}
}

func TestRecoveryManagerTryRecoverMaxTokens(t *testing.T) {
	rm := NewRecoveryManager()

	plan := rm.TryRecover(RecoveryCategoryMaxTokens)
	if plan.Action != RecoveryActionContinueInNextTurn {
		t.Fatalf("expected continue_in_next_turn, got %s", plan.Action)
	}
}

func TestRecoveryManagerTryRecoverSurfacesAfterExhaustion(t *testing.T) {
	rm := NewRecoveryManager()

	// Exhaust overloaded attempts
	for i := 0; i < DefaultOverloadedMaxAttempts; i++ {
		rm.RecordAttempt(RecoveryCategoryOverloaded)
	}

	plan := rm.TryRecover(RecoveryCategoryOverloaded)
	if plan.Action != RecoveryActionSurface {
		t.Fatalf("expected surface, got %s", plan.Action)
	}
	if plan.Reason == "" {
		t.Fatal("expected non-empty reason for surface")
	}
}

func TestRecoveryManagerCustomLimits(t *testing.T) {
	rm := NewRecoveryManagerWithLimits(map[RecoveryCategory]int{
		RecoveryCategoryPTL:     1,
		RecoveryCategoryGeneric: 5,
	})

	// PTL should exhaust after 1
	rm.RecordAttempt(RecoveryCategoryPTL)
	if rm.CanRetry(RecoveryCategoryPTL) {
		t.Fatal("expected PTL exhausted after 1 with custom limit")
	}

	// Generic should allow 5
	for i := 0; i < 5; i++ {
		if !rm.CanRetry(RecoveryCategoryGeneric) {
			t.Fatalf("expected generic available at attempt %d", i)
		}
		rm.RecordAttempt(RecoveryCategoryGeneric)
	}
	if rm.CanRetry(RecoveryCategoryGeneric) {
		t.Fatal("expected generic exhausted after 5")
	}
}

func TestRecoveryManagerBackoffDelayIncreases(t *testing.T) {
	// Verify exponential backoff behavior
	d0 := backoffDelay(0)
	d1 := backoffDelay(1)
	d2 := backoffDelay(2)

	if d1 <= d0 {
		t.Fatalf("expected d1 > d0, got %v <= %v", d1, d0)
	}
	if d2 <= d1 {
		t.Fatalf("expected d2 > d1, got %v <= %v", d2, d1)
	}
}

func TestRecoveryManagerBackoffDelayCapped(t *testing.T) {
	// Very high attempt should still be bounded
	d := backoffDelay(100)
	if d > 120*time.Second {
		t.Fatalf("expected delay capped, got %v", d)
	}
}

func TestWaitForBackoffRespectsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := WaitForBackoff(ctx, 10*time.Second)
	if err == nil {
		t.Fatal("expected error from cancelled context")
		return
	}
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestWaitForBackoffZeroDelay(t *testing.T) {
	err := WaitForBackoff(context.Background(), 0)
	if err != nil {
		t.Fatalf("expected nil error for zero delay, got %v", err)
		return
	}
}

func TestRecoveryManagerResetAll(t *testing.T) {
	rm := NewRecoveryManager()
	rm.RecordAttempt(RecoveryCategoryPTL)
	rm.RecordAttempt(RecoveryCategoryOverloaded)
	rm.RecordAttempt(RecoveryCategoryGeneric)

	rm.ResetAll()

	if rm.AttemptCount(RecoveryCategoryPTL) != 0 {
		t.Fatal("expected PTL count 0 after ResetAll")
	}
	if rm.AttemptCount(RecoveryCategoryOverloaded) != 0 {
		t.Fatal("expected overloaded count 0 after ResetAll")
	}
	if rm.AttemptCount(RecoveryCategoryGeneric) != 0 {
		t.Fatal("expected generic count 0 after ResetAll")
	}
}
