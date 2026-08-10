package engine

import (
	"testing"
	"time"
)

func TestRuntimeThreadTimingExcludesHumanWaitAndPause(t *testing.T) {
	store := NewRuntimeStateStore()
	base := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	event := func(
		sequence uint64,
		offset time.Duration,
		eventType QueryEventType,
		mutate func(*QueryEvent),
	) QueryEvent {
		evt := runtimeTestEvent(sequence, "turn-timing", eventType, mutate)
		evt.Timestamp = base.Add(offset)
		return evt
	}
	apply := func(evt QueryEvent) {
		t.Helper()
		if err := store.Apply(evt); err != nil {
			t.Fatalf("apply %q: %v", evt.Type, err)
		}
	}
	timing := func(now time.Time) RuntimeThreadTimingSnapshot {
		t.Helper()
		snapshot, ok := store.ThreadTimingSnapshot("thread-1")
		if !ok {
			t.Fatal("thread timing snapshot is unavailable")
		}
		if snapshot.Elapsed(now) < 0 {
			t.Fatalf("negative active elapsed: %#v", snapshot)
		}
		return snapshot
	}

	apply(event(1, 0, EventStreamRequestStart, nil))
	apply(event(2, 2*time.Second, EventPermissionRequest, func(evt *QueryEvent) {
		evt.PermissionRequest = &PermissionRequestEvent{
			ToolName: "Bash", ToolUseID: "permission-a",
		}
	}))
	if got := timing(base.Add(time.Hour)).Elapsed(base.Add(time.Hour)); got != 2*time.Second {
		t.Fatalf("active elapsed while waiting = %s, want 2s", got)
	}

	apply(event(3, 4*time.Second, EventPermissionRequest, func(evt *QueryEvent) {
		evt.PermissionRequest = &PermissionRequestEvent{
			ToolName: "Write", ToolUseID: "permission-b",
		}
	}))
	apply(event(4, 10*time.Second, EventPermissionResolved, func(evt *QueryEvent) {
		evt.PermissionResolved = &PermissionResolvedEvent{
			ToolUseID: "permission-a", Decision: "allow",
		}
	}))
	waiting := timing(base.Add(time.Hour))
	if waiting.Status != RuntimeThreadWaitingInput ||
		waiting.Elapsed(base.Add(time.Hour)) != 2*time.Second {
		t.Fatalf("partial resolution resumed timing: %#v", waiting)
	}

	apply(event(5, 12*time.Second, EventPermissionResolved, func(evt *QueryEvent) {
		evt.PermissionResolved = &PermissionResolvedEvent{
			ToolUseID: "permission-b", Decision: "allow",
		}
	}))
	if got := timing(base.Add(14 * time.Second)).Elapsed(base.Add(14 * time.Second)); got != 4*time.Second {
		t.Fatalf("active elapsed after final resolution = %s, want 4s", got)
	}

	apply(event(6, 15*time.Second, EventAgentLifecycle, func(evt *QueryEvent) {
		evt.AgentLifecycle = &AgentLifecycleEvent{Phase: "paused", Status: "paused"}
	}))
	paused := timing(base.Add(time.Hour))
	if paused.Status != RuntimeThreadPaused ||
		paused.Elapsed(base.Add(time.Hour)) != 5*time.Second {
		t.Fatalf("paused timing = %#v, want frozen 5s", paused)
	}

	apply(event(7, 20*time.Second, EventAgentLifecycle, func(evt *QueryEvent) {
		evt.AgentLifecycle = &AgentLifecycleEvent{
			Phase: "resumed_control", Status: "running",
		}
	}))
	apply(event(8, 24*time.Second, EventTerminal, func(evt *QueryEvent) {
		evt.TerminalInfo = &Terminal{Reason: TerminalCompleted}
	}))
	completed := timing(base.Add(time.Hour))
	if completed.Status != RuntimeThreadCompleted ||
		completed.Elapsed(base.Add(time.Hour)) != 9*time.Second {
		t.Fatalf("completed timing = %#v, want frozen 9s", completed)
	}
}

func TestRuntimeThreadTimingAccumulatesAcrossTurnsAndNeverGoesNegative(t *testing.T) {
	store := NewRuntimeStateStore()
	base := time.Date(2026, 7, 24, 11, 0, 0, 0, time.UTC)
	first := runtimeTestEvent(1, "turn-1", EventStreamRequestStart, nil)
	first.Timestamp = base
	firstTerminal := runtimeTestEvent(2, "turn-1", EventTerminal, func(evt *QueryEvent) {
		evt.TerminalInfo = &Terminal{Reason: TerminalCompleted}
	})
	firstTerminal.Timestamp = base.Add(5 * time.Second)
	second := runtimeTestEvent(3, "turn-2", EventStreamRequestStart, nil)
	second.Timestamp = base.Add(10 * time.Second)
	backwardPause := runtimeTestEvent(4, "turn-2", EventPermissionRequest, func(evt *QueryEvent) {
		evt.PermissionRequest = &PermissionRequestEvent{
			ToolName: "Bash", ToolUseID: "permission-backward",
		}
	})
	backwardPause.Timestamp = base.Add(9 * time.Second)
	resume := runtimeTestEvent(5, "turn-2", EventPermissionResolved, func(evt *QueryEvent) {
		evt.PermissionResolved = &PermissionResolvedEvent{
			ToolUseID: "permission-backward", Decision: "allow",
		}
	})
	resume.Timestamp = base.Add(20 * time.Second)
	secondTerminal := runtimeTestEvent(6, "turn-2", EventTerminal, func(evt *QueryEvent) {
		evt.TerminalInfo = &Terminal{Reason: TerminalCompleted}
	})
	secondTerminal.Timestamp = base.Add(23 * time.Second)

	for _, evt := range []QueryEvent{
		first,
		firstTerminal,
		second,
		backwardPause,
		resume,
		secondTerminal,
	} {
		if err := store.Apply(evt); err != nil {
			t.Fatalf("apply %q: %v", evt.Type, err)
		}
	}
	timing, ok := store.ThreadTimingSnapshot("thread-1")
	if !ok {
		t.Fatal("cumulative thread timing snapshot is unavailable")
	}
	if timing.TurnID != "turn-2" ||
		timing.ActiveDuration != 8*time.Second ||
		timing.Elapsed(base.Add(time.Hour)) != 8*time.Second {
		t.Fatalf("cumulative thread timing = %#v, want frozen 8s across turns", timing)
	}
}

func TestRuntimeThreadTimingIgnoresAgentLaunchWithoutTurn(t *testing.T) {
	store := NewRuntimeStateStore()
	launch := runtimeTestEvent(
		1,
		"agent-launch:agent-1:1",
		EventAgentLifecycle,
		func(evt *QueryEvent) {
			evt.AgentLifecycle = &AgentLifecycleEvent{
				Phase: "launched", Status: "running", Generation: 1,
				StartedAt: evt.Timestamp,
			}
		},
	)
	if err := store.Apply(launch); err != nil {
		t.Fatal(err)
	}
	if timing, ok := store.ThreadTimingSnapshot("thread-1"); ok {
		t.Fatalf("out-of-band launch started thread timing: %#v", timing)
	}
}

func TestRuntimeThreadTimingRestoreAndEvictionBoundaries(t *testing.T) {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	store := NewRuntimeStateStore(RuntimeStoreLimits{Threads: 1})
	if err := store.RestoreAgentSnapshot(RuntimeAgentSnapshot{
		AgentID:         "agent-1",
		SessionID:       "session-1",
		ThreadID:        "thread-1",
		ParentSessionID: "parent-session",
		ParentThreadID:  "parent-thread",
		ParentAgentID:   "parent-agent",
		ParentToolUseID: "spawn-call",
		Status:          string(RuntimeThreadRunning),
		StartedAt:       base,
		UpdatedAt:       base,
	}, nil, true); err != nil {
		t.Fatalf("restore live Agent: %v", err)
	}
	if timing, ok := store.ThreadTimingSnapshot("thread-1"); ok {
		t.Fatalf("restore invented active timing: %#v", timing)
	}

	firstLiveEvent := runtimeTestEvent(1, "turn-restored", EventStreamRequestStart, nil)
	firstLiveEvent.Timestamp = base.Add(5 * time.Second)
	if err := store.Apply(firstLiveEvent); err != nil {
		t.Fatalf("apply first live event: %v", err)
	}
	timing, ok := store.ThreadTimingSnapshot("thread-1")
	if !ok || timing.Elapsed(base.Add(8*time.Second)) != 3*time.Second {
		t.Fatalf("restored live timing = %#v, want 3s after first new event", timing)
	}

	terminal := runtimeTestEvent(2, "turn-restored", EventTerminal, func(evt *QueryEvent) {
		evt.TerminalInfo = &Terminal{Reason: TerminalCompleted}
	})
	terminal.Timestamp = base.Add(10 * time.Second)
	if err := store.Apply(terminal); err != nil {
		t.Fatalf("complete restored turn: %v", err)
	}

	nextThread := runtimeTestEvent(1, "turn-2", EventStreamRequestStart, nil)
	nextThread.SessionID = "session-2"
	nextThread.ThreadID = "thread-2"
	nextThread.AgentID = "agent-2"
	nextThread.Timestamp = base.Add(20 * time.Second)
	if err := store.Apply(nextThread); err != nil {
		t.Fatalf("start next thread after eviction: %v", err)
	}
	if timing, ok := store.ThreadTimingSnapshot("thread-1"); ok {
		t.Fatalf("evicted thread retained timing: %#v", timing)
	}
	nextTiming, ok := store.ThreadTimingSnapshot("thread-2")
	if !ok || nextTiming.TurnID != "turn-2" ||
		nextTiming.Elapsed(base.Add(22*time.Second)) != 2*time.Second {
		t.Fatalf("next thread timing = %#v, want independent 2s", nextTiming)
	}
}
