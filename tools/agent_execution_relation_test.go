package tools

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

type orderedAgentAdmissionExecutor struct {
	mu         sync.Mutex
	steps      []string
	linkErr    error
	executeErr error
	executed   chan int64
	release    <-chan struct{}
}

func (e *orderedAgentAdmissionExecutor) record(step string) {
	e.mu.Lock()
	e.steps = append(e.steps, step)
	e.mu.Unlock()
}

func (e *orderedAgentAdmissionExecutor) RecordAgentExecutionLinkAdmission(
	_ context.Context,
	opts AgentExecOptions,
) error {
	e.record("link")
	if opts.WorkItem == nil || opts.WorkItem.AdmittedAt.IsZero() {
		return errors.New("missing frozen WorkItem admission")
	}
	return e.linkErr
}

func (e *orderedAgentAdmissionExecutor) RecordAgentLaunch(
	_ context.Context,
	_ AgentLaunchSnapshot,
) error {
	e.record("launch")
	return nil
}

func (e *orderedAgentAdmissionExecutor) RecordAgentExecutionAdmission(
	_ context.Context,
	_ AgentExecOptions,
	_ []*schema.Message,
) error {
	e.record("child")
	return nil
}

func (e *orderedAgentAdmissionExecutor) ExecuteAgent(
	ctx context.Context,
	opts AgentExecOptions,
) (*AgentExecResult, error) {
	e.record("execute")
	if e.executed != nil {
		e.executed <- opts.Generation
	}
	if e.release != nil {
		select {
		case <-e.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if e.executeErr != nil {
		return nil, e.executeErr
	}
	return &AgentExecResult{Result: "done"}, nil
}

func (e *orderedAgentAdmissionExecutor) snapshotSteps() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.steps...)
}

func TestAgentRunnerLinkedLaunchOrdersAdmissionBeforeDispatch(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	executor := &orderedAgentAdmissionExecutor{
		executed: make(chan int64, 1),
	}
	runner.SetExecutor(executor)

	started, err := RunAgentBackground(
		context.Background(),
		runner,
		AgentExecOptions{
			AgentID:         "linked-agent",
			Task:            "linked task",
			ParentSessionID: "parent-session",
			ParentThreadID:  "parent-thread",
			ToolUseID:       "tool-use",
			WorkItem: &AgentWorkItemReference{
				BoardID:              "board",
				WorkItemID:           "task:1",
				ExpectedItemRevision: 1,
			},
		},
	)
	if err != nil {
		t.Fatalf("linked launch: %v", err)
	}
	if started.ID != "linked-agent" {
		t.Fatalf("Agent ID = %q", started.ID)
	}
	select {
	case generation := <-executor.executed:
		if generation != 1 {
			t.Fatalf("generation = %d", generation)
		}
	case <-time.After(time.Second):
		t.Fatal("executor was not entered")
	}
	waitForAgentStatus(t, runner, started.ID, "completed")
	want := []string{"link", "launch", "child", "execute"}
	got := executor.snapshotSteps()
	if len(got) != len(want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("steps = %v, want %v", got, want)
		}
	}
}

func TestAgentRunnerLinkedAdmissionFailureNeverDispatchesAndSettles(
	t *testing.T,
) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	executor := &orderedAgentAdmissionExecutor{
		linkErr:  errors.New("link commit uncertain"),
		executed: make(chan int64, 1),
	}
	runner.SetExecutor(executor)

	_, err := RunAgentBackground(
		context.Background(),
		runner,
		AgentExecOptions{
			AgentID:         "failed-link-agent",
			Task:            "linked task",
			ParentSessionID: "parent-session",
			ParentThreadID:  "parent-thread",
			ToolUseID:       "tool-use",
			WorkItem: &AgentWorkItemReference{
				BoardID:              "board",
				WorkItemID:           "task:1",
				ExpectedItemRevision: 1,
			},
		},
	)
	if err == nil {
		t.Fatal("expected linked admission failure")
	}
	select {
	case <-executor.executed:
		t.Fatal("executor entered after linked admission failure")
	default:
	}
	if got := executor.snapshotSteps(); len(got) != 1 || got[0] != "link" {
		t.Fatalf("steps = %v, want link only", got)
	}
	settlements := runner.AgentExecutionSettlements([]AgentExecutionKey{{
		AgentID: "failed-link-agent", Generation: 1,
	}})
	if len(settlements) != 1 ||
		!settlements[0].Settled ||
		settlements[0].State != "terminal_durable" {
		t.Fatalf("settlement = %+v", settlements)
	}
	durable, loadErr := runner.LoadPersistedAgentSnapshot("failed-link-agent")
	if loadErr != nil {
		t.Fatalf("load pre-dispatch failure: %v", loadErr)
	}
	if !durable.PredispatchFailure() {
		t.Fatal("durable failure lost pre-dispatch classification")
	}
}

func TestAgentRunnerExactGenerationControlsRejectStaleKey(t *testing.T) {
	release := make(chan struct{})
	executor := &orderedAgentAdmissionExecutor{release: release}
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	runner.SetExecutor(executor)
	started, err := RunAgentBackground(
		context.Background(),
		runner,
		AgentExecOptions{AgentID: "exact-agent", Task: "run"},
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := runner.QueueAgentMessageGeneration(
		started.ID,
		2,
		MessagePayload{Content: "stale"},
	); err == nil {
		t.Fatal("stale exact send succeeded")
	}
	if err := runner.PauseAgentGeneration(started.ID, 2); err == nil {
		t.Fatal("stale exact pause succeeded")
	}
	if err := runner.ResumeAgentGeneration(started.ID, 2); err == nil {
		t.Fatal("stale exact resume succeeded")
	}
	if err := runner.AbortAgentGeneration(started.ID, 2); err == nil {
		t.Fatal("stale exact cancel succeeded")
	}
	snapshot, ok := runner.GetAgentSnapshot(started.ID)
	if !ok || snapshot.Status != "running" ||
		snapshot.PendingMessageCount != 0 {
		t.Fatalf("stale controls mutated Agent: %+v", snapshot)
	}
	close(release)
	waitForAgentStatus(t, runner, started.ID, "completed")
}
