package tui

import (
	"testing"
	"time"
)

func TestIdleTracker_NotIdleOnFirstInput(t *testing.T) {
	tracker := newIdleTracker()
	// First input should not report idle (just initialized)
	_, wasIdle := tracker.recordInput()
	if wasIdle {
		t.Error("expected first input not to be idle")
	}
}

func TestIdleTracker_DetectsIdle(t *testing.T) {
	tracker := newIdleTracker()
	tracker.idleThreshold = 100 * time.Millisecond

	// Simulate time passing by backdating lastInputTime
	tracker.lastInputTime = time.Now().Add(-200 * time.Millisecond)

	dur, wasIdle := tracker.recordInput()
	if !wasIdle {
		t.Error("expected idle detection after threshold exceeded")
	}
	if dur < 200*time.Millisecond {
		t.Errorf("expected duration >= 200ms, got %v", dur)
	}
}

func TestIdleTracker_NoDoubleNotification(t *testing.T) {
	tracker := newIdleTracker()
	tracker.idleThreshold = 100 * time.Millisecond

	// First return from idle
	tracker.lastInputTime = time.Now().Add(-200 * time.Millisecond)
	_, wasIdle := tracker.recordInput()
	if !wasIdle {
		t.Fatal("expected first idle detection")
	}

	// Immediate second input should NOT trigger again
	_, wasIdle = tracker.recordInput()
	if wasIdle {
		t.Error("expected no duplicate idle notification")
	}
}

func TestIdleTracker_ResetsAfterActivity(t *testing.T) {
	tracker := newIdleTracker()
	tracker.idleThreshold = 100 * time.Millisecond

	// First idle
	tracker.lastInputTime = time.Now().Add(-200 * time.Millisecond)
	tracker.recordInput()

	// Some rapid activity to reset the notified flag
	tracker.recordInput()

	// Go idle again
	tracker.lastInputTime = time.Now().Add(-200 * time.Millisecond)
	_, wasIdle := tracker.recordInput()
	if !wasIdle {
		t.Error("expected idle detection after reset")
	}
}

func TestFormatIdleDuration(t *testing.T) {
	tests := []struct {
		dur    time.Duration
		expect string
	}{
		{30 * time.Second, "< 1 minute"},
		{1 * time.Minute, "1 minute"},
		{5 * time.Minute, "5 minutes"},
		{12 * time.Minute, "12 minutes"},
		{60 * time.Minute, "1 hour"},
		{120 * time.Minute, "2 hours"},
		{90 * time.Minute, "1 hour 30 minutes"},
		{150 * time.Minute, "2 hours 30 minutes"},
	}

	for _, tc := range tests {
		got := formatIdleDuration(tc.dur)
		if got != tc.expect {
			t.Errorf("formatIdleDuration(%v) = %q, want %q", tc.dur, got, tc.expect)
		}
	}
}

func TestIdleReturnMessage(t *testing.T) {
	msg := idleReturnMessage(12 * time.Minute)
	expected := "Welcome back! You were away for 12 minutes"
	if msg != expected {
		t.Errorf("idleReturnMessage(12m) = %q, want %q", msg, expected)
	}
}
