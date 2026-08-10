package session

import (
	"sync"
	"time"
)

// ActivityReason identifies what is keeping the session active.
type ActivityReason string

const (
	ActivityAPICall  ActivityReason = "api_call"
	ActivityToolExec ActivityReason = "tool_exec"
)

const activityIntervalDuration = 30 * time.Second

// ActivityTracker manages session activity tracking with refcount-based
// heartbeat timer. When work is in progress (refcount > 0), a periodic
// heartbeat fires to keep remote containers alive. When idle, an idle
// timer detects 30s of inactivity.
//
// Reference: src/utils/sessionActivity.ts (133 lines)
type ActivityTracker struct {
	mu              sync.Mutex
	refcount        int
	activeReasons   map[ActivityReason]int
	oldestStartedAt time.Time
	callback        func()
	heartbeatTicker *time.Ticker
	heartbeatDone   chan struct{}
	idleTimer       *time.Timer
	onIdle          func()
}

// NewActivityTracker creates a new session activity tracker.
func NewActivityTracker() *ActivityTracker {
	return &ActivityTracker{
		activeReasons: make(map[ActivityReason]int),
	}
}

// RegisterCallback sets the keep-alive callback that fires every 30s
// while work is in progress.
func (t *ActivityTracker) RegisterCallback(cb func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.callback = cb
	if t.refcount > 0 && t.heartbeatTicker == nil {
		t.startHeartbeat()
	}
}

// UnregisterCallback removes the keep-alive callback and stops timers.
func (t *ActivityTracker) UnregisterCallback() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.callback = nil
	t.stopHeartbeat()
	t.stopIdleTimer()
}

// OnIdle sets a callback that fires after 30s of inactivity.
func (t *ActivityTracker) OnIdle(cb func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onIdle = cb
}

// IsActive returns true if any activity is in progress.
func (t *ActivityTracker) IsActive() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.refcount > 0
}

// Refcount returns the current activity refcount.
func (t *ActivityTracker) Refcount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.refcount
}

// Start increments the activity refcount. When it transitions from 0→1
// and a callback is registered, starts a periodic heartbeat timer.
func (t *ActivityTracker) Start(reason ActivityReason) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.refcount++
	t.activeReasons[reason]++

	if t.refcount == 1 {
		t.oldestStartedAt = time.Now()
		t.stopIdleTimer()
		if t.callback != nil && t.heartbeatTicker == nil {
			t.startHeartbeat()
		}
	}
}

// Stop decrements the activity refcount. When it reaches 0, stops the
// heartbeat timer and starts an idle detection timer.
func (t *ActivityTracker) Stop(reason ActivityReason) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.refcount > 0 {
		t.refcount--
	}
	n := t.activeReasons[reason] - 1
	if n > 0 {
		t.activeReasons[reason] = n
	} else {
		delete(t.activeReasons, reason)
	}

	if t.refcount == 0 {
		t.stopHeartbeat()
		if t.callback != nil || t.onIdle != nil {
			t.startIdleTimer()
		}
	}
}

// Signal sends a one-shot keep-alive signal.
func (t *ActivityTracker) Signal() {
	t.mu.Lock()
	cb := t.callback
	t.mu.Unlock()
	if cb != nil {
		cb()
	}
}

// Close stops all timers and cleans up.
func (t *ActivityTracker) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopHeartbeat()
	t.stopIdleTimer()
	t.callback = nil
}

func (t *ActivityTracker) startHeartbeat() {
	t.stopIdleTimer()
	ticker := time.NewTicker(activityIntervalDuration)
	done := make(chan struct{})
	t.heartbeatTicker = ticker
	t.heartbeatDone = done
	cb := t.callback
	go func() {
		for {
			select {
			case <-ticker.C:
				if cb != nil {
					cb()
				}
			case <-done:
				return
			}
		}
	}()
}

func (t *ActivityTracker) stopHeartbeat() {
	if t.heartbeatTicker != nil {
		t.heartbeatTicker.Stop()
		close(t.heartbeatDone)
		t.heartbeatTicker = nil
		t.heartbeatDone = nil
	}
}

func (t *ActivityTracker) startIdleTimer() {
	t.stopIdleTimer()
	onIdle := t.onIdle
	t.idleTimer = time.AfterFunc(activityIntervalDuration, func() {
		if onIdle != nil {
			onIdle()
		}
	})
}

func (t *ActivityTracker) stopIdleTimer() {
	if t.idleTimer != nil {
		t.idleTimer.Stop()
		t.idleTimer = nil
	}
}
