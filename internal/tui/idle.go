package tui

import (
	"fmt"
	"time"
)

// Default idle threshold after which a "welcome back" toast is shown.
const defaultIdleThreshold = 5 * time.Minute

// idleReturnDuration is how long the "welcome back" toast stays visible.
// Shorter than normal toasts since it's purely informational.

// idleTracker manages idle-state detection and the return notification.
// It records the last time the user pressed a key and, when input resumes
// after an extended gap, triggers a brief non-blocking toast.
type idleTracker struct {
	lastInputTime  time.Time     // last observed keypress timestamp
	idleThreshold  time.Duration // how long before we consider the user "idle"
	notifiedReturn bool          // prevents repeated toasts within the same idle window
}

// newIdleTracker creates an idleTracker with the default threshold.
func newIdleTracker() *idleTracker {
	return &idleTracker{
		lastInputTime: time.Now(),
		idleThreshold: defaultIdleThreshold,
	}
}

// recordInput checks whether the user was idle and returns the idle duration
// if the threshold was exceeded. It always updates lastInputTime.
// Returns (idleDuration, wasIdle).
func (t *idleTracker) recordInput() (time.Duration, bool) {
	now := time.Now()
	elapsed := now.Sub(t.lastInputTime)
	wasIdle := elapsed >= t.idleThreshold && !t.notifiedReturn

	if wasIdle {
		t.notifiedReturn = true
	}

	// Reset notification flag once the user has been active again
	// (i.e., if the elapsed time is below threshold, the window is over).
	if elapsed < t.idleThreshold {
		t.notifiedReturn = false
	}

	t.lastInputTime = now
	return elapsed, wasIdle
}

// formatIdleDuration formats a duration into a human-readable string.
// Mirrors the reference formatIdleDuration from IdleReturnDialog.tsx.
func formatIdleDuration(d time.Duration) string {
	minutes := int(d.Minutes())
	if minutes < 1 {
		return "< 1 minute"
	}
	if minutes < 60 {
		if minutes == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", minutes)
	}
	hours := minutes / 60
	remaining := minutes % 60
	if remaining == 0 {
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	}
	if hours == 1 {
		return fmt.Sprintf("1 hour %d minutes", remaining)
	}
	return fmt.Sprintf("%d hours %d minutes", hours, remaining)
}

// idleReturnMessage builds the toast text shown when the user returns.
func idleReturnMessage(d time.Duration) string {
	return fmt.Sprintf("Welcome back! You were away for %s", formatIdleDuration(d))
}
