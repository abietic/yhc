package tui

import (
	"strings"
	"testing"

	"github.com/abietic/yhc/engine"
)

func TestAsyncHookStatusPreservesOtherRunningHooks(t *testing.T) {
	app := New(Config{Resumed: true})
	app.handleEngineEvent(asyncHookResponseEvent("hook-1", "Indexing", "running", "running", 0))
	app.handleEngineEvent(asyncHookResponseEvent("hook-2", "Checking policy", "running", "running", 0))
	if app.hookStatus != "Checking policy" {
		t.Fatalf("latest hook status = %q", app.hookStatus)
	}

	app.handleEngineEvent(asyncHookResponseEvent("hook-2", "Checking policy", "completed", "completed", 0))
	if app.hookStatus != "Indexing" {
		t.Fatalf("remaining hook status = %q, want Indexing", app.hookStatus)
	}
	app.handleEngineEvent(asyncHookResponseEvent("hook-1", "Indexing", "completed", "completed", 0))
	if app.hookStatus != "" || len(app.asyncHookStatuses) != 0 || len(app.asyncHookOrder) != 0 {
		t.Fatalf("completed hook state was retained: status=%q map=%#v order=%#v", app.hookStatus, app.asyncHookStatuses, app.asyncHookOrder)
	}
}

func TestAsyncHookFailureClearsStatusAndShowsBoundedWarning(t *testing.T) {
	app := New(Config{Resumed: true})
	app.handleEngineEvent(asyncHookResponseEvent("hook-fail", "Running checks", "running", "running", 0))
	app.handleEngineEvent(asyncHookResponseEvent("hook-fail", "Running checks", "completed", "timed_out", -1))
	if app.hookStatus != "" {
		t.Fatalf("hook status = %q after failure", app.hookStatus)
	}
	active := app.notifications.Active()
	if len(active) != 1 || active[0].Severity != NotifyWarning {
		t.Fatalf("notifications = %#v", active)
	}
	if !strings.Contains(active[0].Message, "Running checks timed out") || strings.Contains(active[0].Message, "secret-output") {
		t.Fatalf("warning message = %q", active[0].Message)
	}
}

func asyncHookResponseEvent(id, status, phase, outcome string, exitCode int) engine.QueryEvent {
	return engine.QueryEvent{Type: engine.EventHookResponse, HookResponse: &engine.HookResponseEvent{
		HookID: id, HookName: "test command", HookEvent: "PreToolUse",
		StatusMessage: status, Phase: phase, Outcome: outcome, ExitCode: exitCode,
	}}
}
