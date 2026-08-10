package tools

import (
	"context"
	"testing"
	"time"
)

func TestAgentRunnerAllocatesChildRuntimeIdentityBeforeExecution(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	executed := make(chan AgentExecOptions, 1)
	release := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		executed <- opts
		<-release
		return &AgentExecResult{Result: "done"}, nil
	}})

	running, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:            "inspect",
		Description:     "Inspect runtime",
		ParentSessionID: "leader-session",
		ParentThreadID:  "leader-thread",
		ParentAgentID:   "parent-agent",
		ToolUseID:       "spawn-call",
	})
	if err != nil {
		t.Fatal(err)
	}
	if running.ID == "" || running.SessionID == "" || running.ThreadID == "" {
		t.Fatalf("launch snapshot lacks allocated identity: %#v", running)
	}
	if running.ParentSessionID != "leader-session" || running.ParentThreadID != "leader-thread" ||
		running.ParentAgentID != "parent-agent" || running.ToolUseID != "spawn-call" {
		t.Fatalf("launch snapshot lineage = %#v", running)
	}

	var opts AgentExecOptions
	select {
	case opts = <-executed:
	case <-time.After(time.Second):
		t.Fatal("executor was not entered")
	}
	if opts.AgentID != running.ID || opts.SessionID != running.SessionID || opts.ThreadID != running.ThreadID {
		t.Fatalf("executor identity = %#v, launch snapshot = %#v", opts, running)
	}
	if opts.ParentSessionID != "leader-session" || opts.ParentThreadID != "leader-thread" ||
		opts.ParentAgentID != "parent-agent" || opts.ToolUseID != "spawn-call" {
		t.Fatalf("executor lineage = %#v", opts)
	}

	close(release)
	completed := waitForAgentStatus(t, runner, running.ID, "completed")
	if completed.SessionID != running.SessionID || completed.ThreadID != running.ThreadID {
		t.Fatalf("terminal lifecycle lost runtime identity: %#v", completed)
	}
}

func TestAgentToolCarriesRuntimeLineageFromExecutionContext(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	executed := make(chan AgentExecOptions, 1)
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		executed <- opts
		return &AgentExecResult{Result: "done"}, nil
	}})

	ctx := context.Background()
	ctx = WithAgentRunner(ctx, runner)
	ctx = WithSessionID(ctx, "leader-session")
	ctx = WithThreadID(ctx, "leader-thread")
	ctx = WithAgentID(ctx, "parent-agent")
	ctx = WithToolUseID(ctx, "spawn-call")
	if _, err := AgentTool().ExecuteCtx(ctx, `{"description":"inspect","prompt":"trace identity"}`); err != nil {
		t.Fatal(err)
	}

	select {
	case opts := <-executed:
		if opts.ParentSessionID != "leader-session" || opts.ParentThreadID != "leader-thread" ||
			opts.ParentAgentID != "parent-agent" || opts.ToolUseID != "spawn-call" {
			t.Fatalf("Agent tool lineage = %#v", opts)
		}
		if opts.AgentID == "" || opts.SessionID == "" || opts.ThreadID == "" {
			t.Fatalf("Agent tool child identity = %#v", opts)
		}
	case <-time.After(time.Second):
		t.Fatal("executor was not entered")
	}
}
