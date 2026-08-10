package engine

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

func TestP140DetachPreservesForegroundProjectGraphExecution(t *testing.T) {
	const (
		parentSession = "foreground-detach-parent-session"
		parentThread  = "foreground-detach-parent-thread"
		childSession  = "foreground-detach-child-session"
		childThread   = "foreground-detach-child-thread"
		childAgent    = "foreground-detach-child-agent"
		parentTool    = "foreground-detach-parent-tool"
	)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var toolCalls atomic.Int32
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "Read", Desc: "blocking Graph tool"},
		ExecuteCtx: func(ctx context.Context, _ string) (string, error) {
			toolCalls.Add(1)
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
				return "ok", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	})
	model := &blockingSubagentProgressModel{}
	runtimeState := NewRuntimeStateStore()
	executor := NewSubAgentExecutor(model, registry, t.TempDir())
	executor.RuntimeState = runtimeState
	runner := tools.NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	runner.SetExecutor(executor)
	executor.AgentRunner = runner

	parentCtx, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	type runOutcome struct {
		result *tools.AgentExecResult
		err    error
	}
	foreground := make(chan runOutcome, 1)
	go func() {
		result, err := tools.RunAgent(
			tools.WithAgentExecutor(
				tools.WithAgentRunner(parentCtx, runner),
				executor,
			),
			runner,
			tools.AgentExecOptions{
				Task:            "run one Graph tool then finish",
				Description:     "foreground ProjectGraph detach",
				AgentID:         childAgent,
				SessionID:       childSession,
				ThreadID:        childThread,
				ParentSessionID: parentSession,
				ParentThreadID:  parentThread,
				ToolUseID:       parentTool,
			},
		)
		foreground <- runOutcome{result: result, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("foreground ProjectGraph did not reach its blocking tool")
	}

	wrongOwner := NewQueryEngine(QueryEngineConfig{
		CWD:          t.TempDir(),
		SessionID:    "other-parent-session",
		RuntimeState: runtimeState,
		AgentRunner:  runner,
	})
	t.Cleanup(wrongOwner.Close)
	if _, err := wrongOwner.DetachAgent(childAgent, 1); err == nil ||
		!strings.Contains(err.Error(), "not owned") {
		t.Fatalf("wrong-owner detach error = %v", err)
	}

	owner := NewQueryEngine(QueryEngineConfig{
		CWD:          t.TempDir(),
		SessionID:    parentSession,
		RuntimeState: runtimeState,
		AgentRunner:  runner,
	})
	t.Cleanup(owner.Close)
	detached, err := owner.DetachAgent(childAgent, 1)
	if err != nil {
		t.Fatalf("detach foreground ProjectGraph: %v", err)
	}
	if detached.Outcome != tools.AgentExecOutcomeBackgrounded ||
		detached.AgentID != childAgent ||
		detached.SessionID != childSession ||
		detached.ThreadID != childThread ||
		detached.Generation != 1 {
		t.Fatalf("detach result = %#v", detached)
	}
	select {
	case got := <-foreground:
		if got.err != nil || got.result == nil ||
			got.result.Outcome != tools.AgentExecOutcomeBackgrounded ||
			got.result.AgentID != childAgent ||
			got.result.Generation != 1 {
			t.Fatalf("foreground detach outcome: result=%#v err=%v", got.result, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground Graph wait was not released")
	}

	agent, thread, _, ok := runtimeState.AgentThreadSnapshot(childAgent)
	if !ok {
		t.Fatal("detached Agent runtime projection was not found")
	}
	if agent.Status != "running" ||
		agent.Generation != 1 ||
		thread.Status != RuntimeThreadRunning ||
		thread.ActiveTurnID == "" ||
		thread.LastTerminal != nil ||
		!thread.CompletedAt.IsZero() {
		t.Fatalf("backgrounded runtime projection: agent=%#v thread=%#v", agent, thread)
	}
	var backgrounded int
	for _, event := range thread.Events {
		if event.Type == EventAgentLifecycle &&
			event.Summary == "backgrounded" {
			backgrounded++
			if event.AgentID != childAgent ||
				event.SessionID != childSession ||
				event.ThreadID != childThread ||
				event.TurnID != thread.ActiveTurnID ||
				event.Summary != "backgrounded" {
				t.Fatalf("backgrounded lifecycle identity = %#v", event)
			}
		}
	}
	if backgrounded != 1 {
		t.Fatalf("backgrounded lifecycle events = %d, want 1", backgrounded)
	}

	cancelParent()
	time.Sleep(25 * time.Millisecond)
	if snapshot, _ := runner.GetAgentSnapshot(childAgent); snapshot.Status != "running" {
		t.Fatalf("parent cancellation reached detached Graph child: %#v", snapshot)
	}
	close(release)
	completed := waitForAgentStatus(t, runner, childAgent, "completed", 2*time.Second)
	if completed.ExecutionGeneration() != 1 || completed.Result != "done" {
		t.Fatalf("detached Graph terminal snapshot = %#v", completed)
	}
	model.mu.Lock()
	modelCalls := model.calls
	model.mu.Unlock()
	if modelCalls != 2 || toolCalls.Load() != 1 {
		t.Fatalf(
			"detached Graph restarted: model calls=%d tool calls=%d",
			modelCalls,
			toolCalls.Load(),
		)
	}
}

func TestRuntimeStateStoreBackgroundedLifecyclePreservesActiveTurn(t *testing.T) {
	store := NewRuntimeStateStore()
	launch := runtimeTestEvent(
		1,
		"agent-launch:agent-1:1",
		EventAgentLifecycle,
		func(evt *QueryEvent) {
			evt.AgentLifecycle = &AgentLifecycleEvent{
				Phase:      "launched",
				Status:     "running",
				Generation: 1,
				StartedAt:  evt.Timestamp,
			}
		},
	)
	stream := runtimeTestEvent(2, "turn-1", EventStreamRequestStart, nil)
	backgrounded := runtimeTestEvent(
		3,
		"turn-1",
		EventAgentLifecycle,
		func(evt *QueryEvent) {
			evt.AgentLifecycle = &AgentLifecycleEvent{
				Phase:      "backgrounded",
				Status:     "running",
				Generation: 1,
			}
		},
	)
	if err := store.Replay([]QueryEvent{launch, stream, backgrounded}); err != nil {
		t.Fatalf("reduce backgrounded lifecycle: %v", err)
	}
	snapshot := store.Snapshot("thread-1")
	thread := snapshot.Threads["thread-1"]
	agent := snapshot.Agents["agent-1"]
	if thread.ActiveTurnID != "turn-1" ||
		thread.Status != RuntimeThreadRunning ||
		!thread.CompletedAt.IsZero() ||
		thread.LastTerminal != nil {
		t.Fatalf("backgrounded lifecycle seized active turn: %#v", thread)
	}
	if agent.Status != "running" ||
		agent.Generation != 1 ||
		!agent.CompletedAt.IsZero() {
		t.Fatalf("backgrounded Agent looked terminal: %#v", agent)
	}
	if event := thread.Events[2]; event.Summary != "backgrounded" {
		t.Fatalf("backgrounded lifecycle summary = %q", event.Summary)
	}
}
