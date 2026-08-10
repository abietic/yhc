package permission

import (
	"testing"
	"time"
)

func TestDenialTrackingRecordDenial(t *testing.T) {
	state := NewDenialTrackingState()
	if state.ConsecutiveDenials != 0 || state.TotalDenials != 0 {
		t.Fatal("expected fresh state to be zero")
	}

	state.RecordDenial()
	if state.ConsecutiveDenials != 1 {
		t.Fatalf("expected consecutive=1, got %d", state.ConsecutiveDenials)
	}
	if state.TotalDenials != 1 {
		t.Fatalf("expected total=1, got %d", state.TotalDenials)
	}

	state.RecordDenial()
	if state.ConsecutiveDenials != 2 || state.TotalDenials != 2 {
		t.Fatalf("expected consecutive=2 total=2, got %d/%d", state.ConsecutiveDenials, state.TotalDenials)
	}
}

func TestDenialTrackingRecordSuccessResetsConsecutive(t *testing.T) {
	state := NewDenialTrackingState()
	state.RecordDenial()
	state.RecordDenial()
	state.RecordSuccess()

	if state.ConsecutiveDenials != 0 {
		t.Fatalf("expected consecutive=0 after success, got %d", state.ConsecutiveDenials)
	}
	if state.TotalDenials != 2 {
		t.Fatalf("expected total=2 preserved after success, got %d", state.TotalDenials)
	}
}

func TestShouldFallbackToPromptingConsecutive(t *testing.T) {
	state := NewDenialTrackingState()

	// Below threshold
	for i := 0; i < DenialLimits.MaxConsecutive-1; i++ {
		state.RecordDenial()
	}
	if state.ShouldFallbackToPrompting() {
		t.Fatal("should not fallback below consecutive threshold")
	}

	// At threshold
	state.RecordDenial()
	if !state.ShouldFallbackToPrompting() {
		t.Fatal("should fallback at consecutive threshold")
	}
}

func TestShouldFallbackToPromptingTotal(t *testing.T) {
	state := NewDenialTrackingState()

	// Accumulate total denials with resets to avoid consecutive trigger
	for i := 0; i < DenialLimits.MaxTotal-1; i++ {
		state.RecordDenial()
		state.RecordSuccess()
	}
	// At this point total = MaxTotal-1, consecutive = 0
	if state.ShouldFallbackToPrompting() {
		t.Fatal("should not fallback below total threshold")
	}

	state.RecordDenial()
	// Now total = MaxTotal, consecutive = 1
	if !state.ShouldFallbackToPrompting() {
		t.Fatal("should fallback at total threshold")
	}
}

func TestDenialLimitsValues(t *testing.T) {
	if DenialLimits.MaxConsecutive != 3 {
		t.Fatalf("expected MaxConsecutive=3, got %d", DenialLimits.MaxConsecutive)
	}
	if DenialLimits.MaxTotal != 20 {
		t.Fatalf("expected MaxTotal=20, got %d", DenialLimits.MaxTotal)
	}
}

func TestRecordDenialWithDetailsHistory(t *testing.T) {
	state := NewDenialTrackingState()

	state.RecordDenialWithDetails("Bash", map[string]any{"command": "rm -rf /"}, ReasonRule, "denied by rule")
	state.RecordDenialWithDetails("Write", map[string]any{"file_path": "/etc/passwd"}, ReasonSafetyCheck, "sensitive file")

	if state.ConsecutiveDenials != 2 {
		t.Fatalf("expected consecutive=2, got %d", state.ConsecutiveDenials)
	}
	if state.TotalDenials != 2 {
		t.Fatalf("expected total=2, got %d", state.TotalDenials)
	}

	history := state.GetHistory()
	if len(history) != 2 {
		t.Fatalf("expected 2 history records, got %d", len(history))
	}
	if history[0].ToolName != "Bash" {
		t.Fatalf("expected first record to be Bash, got %q", history[0].ToolName)
	}
	if history[1].ToolName != "Write" {
		t.Fatalf("expected second record to be Write, got %q", history[1].ToolName)
	}
	if history[0].Reason != ReasonRule {
		t.Fatalf("expected reason=rule, got %q", history[0].Reason)
	}
	if history[0].Message != "denied by rule" {
		t.Fatalf("expected message 'denied by rule', got %q", history[0].Message)
	}
	// Timestamp should be recent.
	if time.Since(history[0].Timestamp) > time.Second {
		t.Fatal("timestamp should be within last second")
	}
}

func TestHistoryMaxSize(t *testing.T) {
	state := NewDenialTrackingState()

	// Record more than MaxHistorySize denials.
	for i := 0; i < MaxHistorySize+50; i++ {
		state.RecordDenialWithDetails("Bash", map[string]any{"command": "test"}, ReasonOther, "test")
	}

	history := state.GetHistory()
	if len(history) != MaxHistorySize {
		t.Fatalf("expected history capped at %d, got %d", MaxHistorySize, len(history))
	}
}

func TestRateLimitingSameOperation(t *testing.T) {
	state := NewDenialTrackingState()

	input := map[string]any{"command": "rm -rf /"}

	// First check — not rate-limited.
	if state.IsRateLimited("Bash", input) {
		t.Fatal("should not be rate-limited before any denial")
	}

	// Record a denial.
	state.RecordDenialWithDetails("Bash", input, ReasonRule, "denied")

	// Now should be rate-limited.
	if !state.IsRateLimited("Bash", input) {
		t.Fatal("should be rate-limited after denial")
	}

	// Different input should NOT be rate-limited.
	differentInput := map[string]any{"command": "ls"}
	if state.IsRateLimited("Bash", differentInput) {
		t.Fatal("different input should not be rate-limited")
	}

	// Different tool should NOT be rate-limited.
	if state.IsRateLimited("Read", input) {
		t.Fatal("different tool should not be rate-limited")
	}
}

func TestRateLimitingClear(t *testing.T) {
	state := NewDenialTrackingState()

	input := map[string]any{"command": "rm -rf /"}
	state.RecordDenialWithDetails("Bash", input, ReasonRule, "denied")

	// Should be rate-limited.
	if !state.IsRateLimited("Bash", input) {
		t.Fatal("should be rate-limited")
	}

	// Clear specific rate limit.
	state.ClearRateLimit("Bash", input)

	// Should no longer be rate-limited.
	if state.IsRateLimited("Bash", input) {
		t.Fatal("should not be rate-limited after clear")
	}
}

func TestRateLimitingClearAll(t *testing.T) {
	state := NewDenialTrackingState()

	state.RecordDenialWithDetails("Bash", map[string]any{"command": "rm"}, ReasonRule, "denied")
	state.RecordDenialWithDetails("Write", map[string]any{"file_path": "/etc/passwd"}, ReasonRule, "denied")

	state.ClearAllRateLimits()

	if state.IsRateLimited("Bash", map[string]any{"command": "rm"}) {
		t.Fatal("should not be rate-limited after ClearAll")
	}
	if state.IsRateLimited("Write", map[string]any{"file_path": "/etc/passwd"}) {
		t.Fatal("should not be rate-limited after ClearAll")
	}
}

func TestReset(t *testing.T) {
	state := NewDenialTrackingState()

	state.RecordDenialWithDetails("Bash", map[string]any{"command": "rm"}, ReasonRule, "denied")
	state.RecordDenialWithDetails("Bash", map[string]any{"command": "rm"}, ReasonRule, "denied")

	state.Reset()

	if state.ConsecutiveDenials != 0 {
		t.Fatalf("expected consecutive=0 after reset, got %d", state.ConsecutiveDenials)
	}
	if state.TotalDenials != 0 {
		t.Fatalf("expected total=0 after reset, got %d", state.TotalDenials)
	}
	if len(state.GetHistory()) != 0 {
		t.Fatal("expected empty history after reset")
	}
	if state.IsRateLimited("Bash", map[string]any{"command": "rm"}) {
		t.Fatal("should not be rate-limited after reset")
	}
}

func TestGetHistoryReturnsSnapshot(t *testing.T) {
	state := NewDenialTrackingState()
	state.RecordDenialWithDetails("Bash", map[string]any{"command": "ls"}, ReasonOther, "test")

	history := state.GetHistory()
	// Modifying the snapshot should not affect internal state.
	history[0].ToolName = "MODIFIED"

	fresh := state.GetHistory()
	if fresh[0].ToolName == "MODIFIED" {
		t.Fatal("GetHistory should return a snapshot, not a reference")
	}
}

func TestDenialRateLimitKeyFilePath(t *testing.T) {
	// Verify that file tools use file_path as the key differentiator.
	state := NewDenialTrackingState()

	state.RecordDenialWithDetails("Write", map[string]any{"file_path": "/etc/shadow"}, ReasonSafetyCheck, "denied")

	if !state.IsRateLimited("Write", map[string]any{"file_path": "/etc/shadow"}) {
		t.Fatal("same file_path should be rate-limited")
	}
	if state.IsRateLimited("Write", map[string]any{"file_path": "/tmp/safe.txt"}) {
		t.Fatal("different file_path should not be rate-limited")
	}
}
