package session

import (
	"sync/atomic"
	"testing"
)

func TestActivityTrackerRefcountReasonsAndTimers(t *testing.T) {
	tracker := NewActivityTracker()
	t.Cleanup(tracker.Close)

	var signals atomic.Int32
	tracker.RegisterCallback(func() {
		signals.Add(1)
	})

	if tracker.IsActive() || tracker.Refcount() != 0 {
		t.Fatalf("new tracker should be idle, active=%v refcount=%d", tracker.IsActive(), tracker.Refcount())
	}

	tracker.Start(ActivityAPICall)
	if !tracker.IsActive() || tracker.Refcount() != 1 {
		t.Fatalf("expected one active API call, active=%v refcount=%d", tracker.IsActive(), tracker.Refcount())
	}
	if tracker.activeReasons[ActivityAPICall] != 1 {
		t.Fatalf("expected api_call reason count 1, got %#v", tracker.activeReasons)
	}
	if tracker.heartbeatTicker == nil || tracker.heartbeatDone == nil {
		t.Fatal("expected heartbeat to start after first activity with callback")
		return
	}
	if !tracker.idleTimerStopped() {
		t.Fatal("idle timer should be stopped while active")
	}

	tracker.Start(ActivityToolExec)
	if tracker.Refcount() != 2 || tracker.activeReasons[ActivityToolExec] != 1 {
		t.Fatalf("expected two active reasons, refcount=%d reasons=%#v", tracker.Refcount(), tracker.activeReasons)
	}

	tracker.Signal()
	if signals.Load() != 1 {
		t.Fatalf("expected one explicit activity signal, got %d", signals.Load())
	}

	tracker.Stop(ActivityAPICall)
	if !tracker.IsActive() || tracker.Refcount() != 1 {
		t.Fatalf("expected one remaining activity, active=%v refcount=%d", tracker.IsActive(), tracker.Refcount())
	}
	if _, ok := tracker.activeReasons[ActivityAPICall]; ok {
		t.Fatalf("expected api_call reason removed, got %#v", tracker.activeReasons)
	}
	if tracker.heartbeatTicker == nil {
		t.Fatal("heartbeat should remain active while refcount > 0")
		return
	}

	tracker.Stop(ActivityToolExec)
	if tracker.IsActive() || tracker.Refcount() != 0 {
		t.Fatalf("expected idle tracker after stopping all work, active=%v refcount=%d", tracker.IsActive(), tracker.Refcount())
	}
	if len(tracker.activeReasons) != 0 {
		t.Fatalf("expected no active reasons, got %#v", tracker.activeReasons)
	}
	if tracker.heartbeatTicker != nil || tracker.heartbeatDone != nil {
		t.Fatal("heartbeat should stop when refcount reaches zero")
		return
	}
	if tracker.idleTimerStopped() {
		t.Fatal("expected idle timer to start after refcount reaches zero with callback")
	}

	tracker.Close()
	if tracker.heartbeatTicker != nil || tracker.idleTimer != nil || tracker.callback != nil {
		t.Fatalf("close should clear timers and callback: heartbeat=%v idle=%v callbackSet=%v", tracker.heartbeatTicker, tracker.idleTimer, tracker.callback != nil)
		return
	}
}

func TestActivityTrackerRegisterCallbackWhileActiveStartsHeartbeat(t *testing.T) {
	tracker := NewActivityTracker()
	t.Cleanup(tracker.Close)

	tracker.Start(ActivityToolExec)
	if tracker.heartbeatTicker != nil {
		t.Fatal("heartbeat should not start before callback is registered")
		return
	}

	tracker.RegisterCallback(func() {})
	if tracker.heartbeatTicker == nil || tracker.heartbeatDone == nil {
		t.Fatal("registering callback while active should start heartbeat")
		return
	}

	tracker.UnregisterCallback()
	if tracker.heartbeatTicker != nil || tracker.idleTimer != nil || tracker.callback != nil {
		t.Fatalf("unregister should clear timers and callback: heartbeat=%v idle=%v callbackSet=%v", tracker.heartbeatTicker, tracker.idleTimer, tracker.callback != nil)
		return
	}
	if !tracker.IsActive() || tracker.Refcount() != 1 {
		t.Fatalf("unregister should not change activity refcount, active=%v refcount=%d", tracker.IsActive(), tracker.Refcount())
	}
}

func TestActivityTrackerStopUnknownReasonDoesNotUnderflow(t *testing.T) {
	tracker := NewActivityTracker()
	t.Cleanup(tracker.Close)

	tracker.Stop(ActivityAPICall)
	if tracker.Refcount() != 0 || tracker.IsActive() {
		t.Fatalf("stop on idle tracker should not underflow, active=%v refcount=%d", tracker.IsActive(), tracker.Refcount())
	}
	if len(tracker.activeReasons) != 0 {
		t.Fatalf("expected no active reasons, got %#v", tracker.activeReasons)
	}
}

func (t *ActivityTracker) idleTimerStopped() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.idleTimer == nil
}
