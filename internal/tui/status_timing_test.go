package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine"
)

func TestStatusAndSpinnerUseEngineCumulativeThreadTiming(t *testing.T) {
	const threadID = "timing-thread"
	store := engine.NewRuntimeStateStore()
	base := time.Now().UTC().Add(-time.Minute)
	apply := func(
		sequence uint64,
		turnID string,
		offset time.Duration,
		eventType engine.QueryEventType,
		mutate func(*engine.QueryEvent),
	) {
		t.Helper()
		evt := engine.QueryEvent{
			RuntimeEventEnvelope: engine.RuntimeEventEnvelope{
				SessionID: threadID,
				ThreadID:  threadID,
				TurnID:    turnID,
				Sequence:  sequence,
				Timestamp: base.Add(offset),
			},
			Type: eventType,
		}
		if mutate != nil {
			mutate(&evt)
		}
		if err := store.Apply(evt); err != nil {
			t.Fatalf("apply %q: %v", eventType, err)
		}
	}
	apply(1, "turn-1", 0, engine.EventStreamRequestStart, nil)
	apply(2, "turn-1", 2*time.Second, engine.EventPermissionRequest, func(evt *engine.QueryEvent) {
		evt.PermissionRequest = &engine.PermissionRequestEvent{
			ToolName: "Bash", ToolUseID: "permission-1",
		}
	})

	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:    threadID,
		ThreadID:     threadID,
		RuntimeState: store,
		CWD:          t.TempDir(),
	})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng, Model: "test-model", ReducedMotion: true})
	app.width = 120
	app.height = 30
	app.state = StateChat
	app.running = true
	app.spinnerState = SpinnerState{
		Mode:      SpinnerToolUse,
		ToolName:  "Bash",
		StartTime: time.Now(),
	}
	app.updateLayout()

	elapsed, ok := app.activeThreadElapsedAt(base.Add(time.Hour))
	if !ok || elapsed != 2*time.Second {
		t.Fatalf("waiting active elapsed = %s, ok=%v, want 2s", elapsed, ok)
	}
	status := stripANSIForTest(app.renderStatus())
	if !strings.Contains(status, "waiting") || !strings.Contains(status, "active 2s") {
		t.Fatalf("waiting status does not expose frozen active time:\n%s", status)
	}
	if spinner := stripANSIForTest(app.renderSpinner()); !strings.Contains(spinner, "2.0s") {
		t.Fatalf("spinner does not share frozen runtime time:\n%s", spinner)
	}

	app.updateLayout()
	_ = app.renderStatus()
	if afterUI, _ := app.activeThreadElapsedAt(base.Add(2 * time.Hour)); afterUI != 2*time.Second {
		t.Fatalf("UI-only work advanced active time to %s", afterUI)
	}

	apply(3, "turn-1", 30*time.Second, engine.EventPermissionResolved, func(evt *engine.QueryEvent) {
		evt.PermissionResolved = &engine.PermissionResolvedEvent{
			ToolUseID: "permission-1", Decision: "allow",
		}
	})
	if resumed, _ := app.activeThreadElapsedAt(base.Add(34 * time.Second)); resumed != 6*time.Second {
		t.Fatalf("resumed active elapsed = %s, want 6s", resumed)
	}
	app.running = false
	if resumedStatus := stripANSIForTest(app.renderStatus()); !strings.Contains(resumedStatus, "running") {
		t.Fatalf("runtime-running thread was presented as idle:\n%s", resumedStatus)
	}

	apply(4, "turn-1", 35*time.Second, engine.EventTerminal, func(evt *engine.QueryEvent) {
		evt.TerminalInfo = &engine.Terminal{Reason: engine.TerminalCompleted}
	})
	app.width = 220
	app.updateLayout()
	if completed, _ := app.activeThreadElapsedAt(base.Add(time.Hour)); completed != 7*time.Second {
		t.Fatalf("completed active elapsed = %s, want frozen 7s", completed)
	}
	if completedStatus := stripANSIForTest(app.renderStatus()); !strings.Contains(completedStatus, "active 7s") {
		t.Fatalf("completed status lost frozen active time:\n%s", completedStatus)
	}

	apply(5, "turn-2", 40*time.Second, engine.EventStreamRequestStart, nil)
	apply(6, "turn-2", 45*time.Second, engine.EventTerminal, func(evt *engine.QueryEvent) {
		evt.TerminalInfo = &engine.Terminal{Reason: engine.TerminalCompleted}
	})
	if cumulative, _ := app.activeThreadElapsedAt(base.Add(time.Hour)); cumulative != 12*time.Second {
		t.Fatalf("second turn reset cumulative active elapsed to %s, want 12s", cumulative)
	}
	if cumulativeStatus := stripANSIForTest(app.renderStatus()); !strings.Contains(cumulativeStatus, "active 12s") {
		t.Fatalf("status does not expose cumulative active time:\n%s", cumulativeStatus)
	}
}

func TestFormatActiveDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{duration: -time.Second, want: "0s"},
		{duration: 59*time.Second + 900*time.Millisecond, want: "59s"},
		{duration: time.Minute + 2*time.Second, want: "1m02s"},
		{duration: time.Hour + 3*time.Minute, want: "1h03m"},
	}
	for _, test := range tests {
		if got := formatActiveDuration(test.duration); got != test.want {
			t.Errorf("formatActiveDuration(%s) = %q, want %q", test.duration, got, test.want)
		}
	}
}
