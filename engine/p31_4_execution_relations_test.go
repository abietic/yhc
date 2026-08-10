package engine

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/internal/workboard"
	"github.com/abietic/yhc/tools"
)

type p314RelationExecutor struct {
	sub       *SubAgentExecutor
	executed  chan int64
	release   <-chan struct{}
	launchErr error
}

func (e *p314RelationExecutor) RecordAgentExecutionLinkAdmission(
	ctx context.Context,
	opts tools.AgentExecOptions,
) error {
	return e.sub.RecordAgentExecutionLinkAdmission(ctx, opts)
}

func (e *p314RelationExecutor) RecordAgentLaunch(
	ctx context.Context,
	launch tools.AgentLaunchSnapshot,
) error {
	if e.launchErr != nil {
		return e.launchErr
	}
	if launch.Generation > 1 {
		return nil
	}
	return e.sub.RecordAgentLaunch(ctx, launch)
}

func (e *p314RelationExecutor) RecordAgentExecutionAdmission(
	context.Context,
	tools.AgentExecOptions,
	[]*schema.Message,
) error {
	return nil
}

func (e *p314RelationExecutor) ExecuteAgent(
	ctx context.Context,
	opts tools.AgentExecOptions,
) (*tools.AgentExecResult, error) {
	if e.executed != nil {
		e.executed <- opts.Generation
	}
	if e.release != nil {
		<-e.release
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &tools.AgentExecResult{Result: "done"}, nil
}

func TestP314PredispatchFailureIsInspectableWithoutMutationActions(
	t *testing.T,
) {
	eng, runner, _, item := p314EngineFixture(t)
	defer eng.Close()
	executor := &p314RelationExecutor{
		sub: &SubAgentExecutor{
			RuntimeState:       eng.runtimeState,
			logicalWorkAdapter: eng.logicalWorkAdapter,
		},
		executed:  make(chan int64, 1),
		launchErr: errors.New("launch event write failed"),
	}
	runner.SetExecutor(executor)
	if _, err := tools.RunAgentBackground(
		context.Background(),
		runner,
		p314LinkedOptions(eng, item, "p314-predispatch"),
	); err == nil {
		t.Fatal("expected pre-dispatch launch failure")
	}
	select {
	case <-executor.executed:
		t.Fatal("executor entered after pre-dispatch failure")
	default:
	}

	snapshot := eng.TaskExplorerSnapshot()
	row := p314ExecutionRow(t, snapshot, "p314-predispatch", 1)
	if !row.Predispatch || row.Phase != TaskExplorerExecutionFailed {
		t.Fatalf("pre-dispatch row = %+v", row)
	}
	for _, action := range row.AllowedActions {
		switch action {
		case TaskExplorerActionInspect, TaskExplorerActionSwitch:
		default:
			t.Fatalf("pre-dispatch mutation action exposed: %v", row.AllowedActions)
		}
	}
	if len(snapshot.Links) != 1 ||
		snapshot.Links[0].State != TaskExplorerLinkValid {
		t.Fatalf("pre-dispatch link = %+v", snapshot.Links)
	}
}

func TestP314ExplorerDispatcherFencesExactGenerationAndSettlement(
	t *testing.T,
) {
	release := make(chan struct{})
	eng, runner, manager, item := p314EngineFixture(t)
	defer eng.Close()
	executor := &p314RelationExecutor{
		sub: &SubAgentExecutor{
			RuntimeState:       eng.runtimeState,
			logicalWorkAdapter: eng.logicalWorkAdapter,
		},
		executed: make(chan int64, 1),
		release:  release,
	}
	runner.SetExecutor(executor)
	if _, err := tools.RunAgentBackground(
		context.Background(),
		runner,
		p314LinkedOptions(eng, item, "p314-live"),
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-executor.executed:
	case <-time.After(time.Second):
		t.Fatal("linked execution did not dispatch")
	}
	snapshot := eng.TaskExplorerSnapshot()
	row := p314ExecutionRow(t, snapshot, "p314-live", 1)
	if !taskExplorerActionAllowed(
		row.AllowedActions,
		TaskExplorerActionSend,
	) || !taskExplorerActionAllowed(
		row.AllowedActions,
		TaskExplorerActionCancel,
	) {
		t.Fatalf("live actions = %v", row.AllowedActions)
	}
	stale := eng.ApplyTaskExplorerAction(TaskExplorerActionRequest{
		RequestID:       "stale",
		BoardID:         snapshot.BoardID,
		BoardRevision:   snapshot.Revision.Board - 1,
		RuntimeRevision: snapshot.Revision.Runtime,
		AgentID:         row.Key.AgentID,
		Generation:      row.Key.Generation,
		Action:          TaskExplorerActionSend,
		Payload:         "must not queue",
	})
	if stale.Conflict != "stale_board" {
		t.Fatalf("stale result = %+v", stale)
	}
	live, _ := runner.GetAgentSnapshot(row.Key.AgentID)
	if live.PendingMessageCount != 0 {
		t.Fatalf("stale send queued input: %+v", live)
	}
	sent := eng.ApplyTaskExplorerAction(TaskExplorerActionRequest{
		RequestID:       "send",
		BoardID:         snapshot.BoardID,
		BoardRevision:   snapshot.Revision.Board,
		RuntimeRevision: snapshot.Revision.Runtime,
		AgentID:         row.Key.AgentID,
		Generation:      row.Key.Generation,
		Action:          TaskExplorerActionSend,
		Payload:         "queued",
	})
	if sent.Outcome != "sent" {
		t.Fatalf("send result = %+v", sent)
	}
	cancelled := eng.ApplyTaskExplorerAction(TaskExplorerActionRequest{
		RequestID:       "cancel",
		BoardID:         snapshot.BoardID,
		BoardRevision:   snapshot.Revision.Board,
		RuntimeRevision: snapshot.Revision.Runtime,
		AgentID:         row.Key.AgentID,
		Generation:      row.Key.Generation,
		Action:          TaskExplorerActionCancel,
	})
	if cancelled.Outcome != "cancel_requested" {
		t.Fatalf("cancel result = %+v", cancelled)
	}
	if _, err := manager.Stop(item.Source.LegacyID); err == nil {
		t.Fatal("WorkItem terminalized while cancellation was pending")
	}
	close(release)
	p314WaitSettlement(t, runner, row.Key)
	if _, err := manager.Stop(item.Source.LegacyID); err != nil {
		t.Fatalf("settled WorkItem stop: %v", err)
	}
}

func TestP472UnlinkedLiveExecutionDoesNotBlockWorkItemSettlement(
	t *testing.T,
) {
	eng, runner, manager, item := p314EngineFixture(t)
	defer eng.Close()
	settledExecutor := &p314RelationExecutor{
		sub: &SubAgentExecutor{
			RuntimeState:       eng.runtimeState,
			logicalWorkAdapter: eng.logicalWorkAdapter,
		},
	}
	runner.SetExecutor(settledExecutor)
	if _, err := tools.RunAgentBackground(
		context.Background(),
		runner,
		p314LinkedOptions(eng, item, "p472-linked"),
	); err != nil {
		t.Fatalf("run linked execution: %v", err)
	}
	p314WaitSettlement(t, runner, RuntimeExecutionKey{
		AgentID: "p472-linked", Generation: 1,
	})

	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	liveExecutor := &p314RelationExecutor{
		sub: &SubAgentExecutor{
			RuntimeState:       eng.runtimeState,
			logicalWorkAdapter: eng.logicalWorkAdapter,
		},
		executed: make(chan int64, 1),
		release:  release,
	}
	runner.SetExecutor(liveExecutor)
	if _, err := tools.RunAgentBackground(
		context.Background(),
		runner,
		tools.AgentExecOptions{
			AgentID: "p472-unlinked",
			Task:    "unlinked live execution",
		},
	); err != nil {
		t.Fatalf("run unlinked execution: %v", err)
	}
	select {
	case <-liveExecutor.executed:
	case <-time.After(time.Second):
		t.Fatal("unlinked execution did not become live")
	}
	unlinked := runner.AgentExecutionSettlements([]tools.AgentExecutionKey{{
		AgentID: "p472-unlinked", Generation: 1,
	}})
	if len(unlinked) != 1 || unlinked[0].State != "live" {
		t.Fatalf("unlinked settlement = %+v", unlinked)
	}
	snapshot := eng.TaskExplorerSnapshot()
	if len(snapshot.Links) != 1 ||
		snapshot.Links[0].AgentID != "p472-linked" {
		t.Fatalf("execution links = %+v", snapshot.Links)
	}
	if _, err := manager.Stop(item.Source.LegacyID); err != nil {
		t.Fatalf("unlinked live execution blocked WorkItem: %v", err)
	}

	close(release)
	released = true
	p314WaitSettlement(t, runner, RuntimeExecutionKey{
		AgentID: "p472-unlinked", Generation: 1,
	})
}

func TestP315ExplorerMessageIdentityRetryAndCancellation(t *testing.T) {
	release := make(chan struct{})
	eng, runner, _, item := p314EngineFixture(t)
	defer eng.Close()
	executor := &p314RelationExecutor{
		sub: &SubAgentExecutor{
			RuntimeState:       eng.runtimeState,
			logicalWorkAdapter: eng.logicalWorkAdapter,
		},
		executed: make(chan int64, 1),
		release:  release,
	}
	runner.SetExecutor(executor)
	if _, err := tools.RunAgentBackground(
		context.Background(),
		runner,
		p314LinkedOptions(eng, item, "p315-message"),
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-executor.executed:
	case <-time.After(time.Second):
		t.Fatal("linked execution did not dispatch")
	}
	snapshot := eng.TaskExplorerSnapshot()
	row := p314ExecutionRow(t, snapshot, "p315-message", 1)
	request := TaskExplorerActionRequest{
		RequestID:       "message-command-1",
		BoardID:         snapshot.BoardID,
		BoardRevision:   snapshot.Revision.Board,
		RuntimeRevision: snapshot.Revision.Runtime,
		AgentID:         row.Key.AgentID,
		Generation:      row.Key.Generation,
		Action:          TaskExplorerActionSend,
		Payload:         "retry-safe input",
	}
	for range 2 {
		result := eng.ApplyTaskExplorerAction(request)
		if result.Outcome != "sent" ||
			result.RequestID != request.RequestID ||
			result.MessageID != request.RequestID ||
			result.BoardID != request.BoardID ||
			result.BoardRevision != request.BoardRevision ||
			result.AgentID != request.AgentID ||
			result.Generation != request.Generation {
			t.Fatalf("exact send result = %+v", result)
		}
	}
	running, ok := runner.GetAgentSnapshot(row.Key.AgentID)
	if !ok || running.PendingMessageCount != 1 {
		t.Fatalf("idempotent pending input = %#v", running)
	}
	current := eng.TaskExplorerSnapshot()
	currentRow := p314ExecutionRow(t, current, row.Key.AgentID, row.Key.Generation)
	if !taskExplorerActionAllowed(
		currentRow.AllowedActions,
		TaskExplorerActionCancelInput,
	) {
		t.Fatalf("pending input actions = %v", currentRow.AllowedActions)
	}
	cancel := TaskExplorerActionRequest{
		RequestID:       "cancel-command-1",
		BoardID:         current.BoardID,
		BoardRevision:   current.Revision.Board,
		RuntimeRevision: current.Revision.Runtime,
		AgentID:         row.Key.AgentID,
		Generation:      row.Key.Generation,
		MessageID:       request.RequestID,
		Action:          TaskExplorerActionCancelInput,
	}
	result := eng.ApplyTaskExplorerAction(cancel)
	if result.Outcome != "input_cancelled" ||
		result.RequestID != cancel.RequestID ||
		result.MessageID != cancel.MessageID ||
		result.AgentID != cancel.AgentID ||
		result.Generation != cancel.Generation {
		t.Fatalf("exact cancel result = %+v", result)
	}
	if retry := eng.ApplyTaskExplorerAction(cancel); retry.Outcome !=
		"input_not_pending" {
		t.Fatalf("cancel retry = %+v", retry)
	}
	cancel.RequestID = "cancel-stale"
	cancel.Generation++
	if stale := eng.ApplyTaskExplorerAction(cancel); stale.Outcome !=
		"stale_generation" {
		t.Fatalf("stale cancel = %+v", stale)
	}
	close(release)
	p314WaitSettlement(t, runner, row.Key)
}

func TestP314LinkedContinuationAppendsGenerationWithoutRewriting(t *testing.T) {
	eng, runner, _, item := p314EngineFixture(t)
	defer eng.Close()
	executor := &p314RelationExecutor{
		sub: &SubAgentExecutor{
			RuntimeState:       eng.runtimeState,
			logicalWorkAdapter: eng.logicalWorkAdapter,
		},
		executed: make(chan int64, 2),
	}
	runner.SetExecutor(executor)
	if _, err := tools.RunAgentBackground(
		context.Background(),
		runner,
		p314LinkedOptions(eng, item, "p314-continue"),
	); err != nil {
		t.Fatal(err)
	}
	p314WaitSettlement(t, runner, RuntimeExecutionKey{
		AgentID: "p314-continue", Generation: 1,
	})
	snapshot := eng.TaskExplorerSnapshot()
	row := p314ExecutionRow(t, snapshot, "p314-continue", 1)
	if !taskExplorerActionAllowed(
		row.AllowedActions,
		TaskExplorerActionContinue,
	) {
		t.Fatalf("terminal actions = %v", row.AllowedActions)
	}
	result := eng.ApplyTaskExplorerAction(TaskExplorerActionRequest{
		RequestID:       "continue",
		BoardID:         snapshot.BoardID,
		BoardRevision:   snapshot.Revision.Board,
		RuntimeRevision: snapshot.Revision.Runtime,
		AgentID:         row.Key.AgentID,
		Generation:      row.Key.Generation,
		Action:          TaskExplorerActionContinue,
		Payload:         "next generation",
	})
	if result.Outcome != "continued" || result.NewGeneration != 2 {
		t.Fatalf("continue result = %+v", result)
	}
	p314WaitSettlement(t, runner, RuntimeExecutionKey{
		AgentID: "p314-continue", Generation: 2,
	})
	authority := eng.logicalWorkAdapter.Snapshot().Record
	if len(authority.ExecutionLinks) != 2 ||
		authority.ExecutionLinks[0].Generation != 1 ||
		authority.ExecutionLinks[1].Generation != 2 ||
		authority.ExecutionLinks[0].WorkItemID !=
			authority.ExecutionLinks[1].WorkItemID {
		t.Fatalf("continuation links = %+v", authority.ExecutionLinks)
	}
}

func TestP314ReplayOnlyTerminalRejectsContinuation(t *testing.T) {
	eng, runner, _, item := p314EngineFixture(t)
	defer eng.Close()
	executor := &p314RelationExecutor{
		sub: &SubAgentExecutor{
			RuntimeState:       eng.runtimeState,
			logicalWorkAdapter: eng.logicalWorkAdapter,
		},
	}
	runner.SetExecutor(executor)
	if _, err := tools.RunAgentBackground(
		context.Background(),
		runner,
		p314LinkedOptions(eng, item, "p314-replay"),
	); err != nil {
		t.Fatal(err)
	}
	p314WaitSettlement(t, runner, RuntimeExecutionKey{
		AgentID: "p314-replay", Generation: 1,
	})
	durable, err := runner.LoadPersistedAgentSnapshot("p314-replay")
	if err != nil {
		t.Fatal(err)
	}
	eng.runtimeState = NewRuntimeStateStore()
	if err := eng.runtimeState.RestoreAgentSnapshot(
		runtimeAgentSnapshotFromRunner(durable),
		nil,
		false,
	); err != nil {
		t.Fatal(err)
	}

	snapshot := eng.TaskExplorerSnapshot()
	row := p314ExecutionRow(t, snapshot, "p314-replay", 1)
	if !row.ReplayOnly ||
		taskExplorerActionAllowed(
			row.AllowedActions,
			TaskExplorerActionContinue,
		) {
		t.Fatalf("replay-only actions = %v", row.AllowedActions)
	}
	result := eng.ApplyTaskExplorerAction(TaskExplorerActionRequest{
		RequestID:       "replay-continue",
		BoardID:         snapshot.BoardID,
		BoardRevision:   snapshot.Revision.Board,
		RuntimeRevision: snapshot.Revision.Runtime,
		AgentID:         row.Key.AgentID,
		Generation:      row.Key.Generation,
		Action:          TaskExplorerActionContinue,
		Payload:         "must not continue",
	})
	if result.Conflict != "unsupported_action" {
		t.Fatalf("replay-only continue result = %+v", result)
	}
	authority := eng.logicalWorkAdapter.Snapshot().Record
	if len(authority.ExecutionLinks) != 1 {
		t.Fatalf("replay-only continue added link: %+v", authority.ExecutionLinks)
	}
	current, err := runner.LoadPersistedAgentSnapshot("p314-replay")
	if err != nil {
		t.Fatal(err)
	}
	if current.ExecutionGeneration() != 1 {
		t.Fatalf(
			"replay-only continue created generation %d",
			current.ExecutionGeneration(),
		)
	}
}

func TestP314ActiveDeleteRejectsLiveAndCancellationPendingLinks(t *testing.T) {
	release := make(chan struct{})
	eng, runner, _, item := p314EngineFixture(t)
	executor := &p314RelationExecutor{
		sub: &SubAgentExecutor{
			RuntimeState:       eng.runtimeState,
			logicalWorkAdapter: eng.logicalWorkAdapter,
		},
		executed: make(chan int64, 1),
		release:  release,
	}
	runner.SetExecutor(executor)
	if _, err := tools.RunAgentBackground(
		context.Background(),
		runner,
		p314LinkedOptions(eng, item, "p314-delete"),
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-executor.executed:
	case <-time.After(time.Second):
		t.Fatal("linked execution did not dispatch")
	}
	p311bWriteTranscript(t, eng.GetTranscript(), eng.config.SessionID)
	if _, err := eng.SessionService().Delete(
		context.Background(),
		eng.config.SessionID,
	); err == nil {
		t.Fatal("active delete accepted live linked generation")
	}
	if !eng.sessionDeletionGate.open() {
		t.Fatal("rejected deletion did not reopen admission")
	}
	if err := runner.AbortAgentGeneration("p314-delete", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SessionService().Delete(
		context.Background(),
		eng.config.SessionID,
	); err == nil {
		t.Fatal("active delete accepted cancellation-pending generation")
	}
	close(release)
	p314WaitSettlement(t, runner, RuntimeExecutionKey{
		AgentID: "p314-delete", Generation: 1,
	})
	result, err := eng.SessionService().Delete(
		context.Background(),
		eng.config.SessionID,
	)
	if err != nil {
		t.Fatalf("delete settled linked Session: %v", err)
	}
	if !result.TranscriptRemoved || !result.WorkBoardAuthorityRemoved {
		t.Fatalf("delete result = %+v", result)
	}
	if eng.sessionDeletionGate.open() {
		t.Fatal("successful deletion reopened runtime admission")
	}
	if _, err := tools.RunAgentBackground(
		context.Background(),
		runner,
		tools.AgentExecOptions{
			AgentID: "after-delete", Task: "must not launch",
		},
	); err == nil {
		t.Fatal("Agent launched after Session deletion admission")
	}
	eng.Close()
}

func p314EngineFixture(
	t *testing.T,
) (*QueryEngine, *tools.AgentRunner, *tools.TaskManager, workboard.WorkItem) {
	t.Helper()
	runner := tools.NewAgentRunner(4)
	runner.SetOutputDir(t.TempDir())
	manager := tools.NewTaskManager()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "p314-session",
		ThreadID:      "p314-thread",
		CWD:           dir,
		TranscriptDir: dir,
		AgentRunner:   runner,
		TaskManager:   manager,
		Clock: func() time.Time {
			return time.Date(2026, 7, 31, 2, 0, 0, 0, time.UTC)
		},
	})
	created, err := manager.CreateWithError("linked work", "", "", nil)
	if err != nil {
		t.Fatalf("create WorkItem compatibility task: %v", err)
	}
	authority := eng.logicalWorkAdapter.Snapshot().Record
	for _, item := range authority.Board.Items {
		if item.Source.LegacyID == created.ID {
			return eng, runner, manager, item
		}
	}
	t.Fatal("WorkItem not found")
	return nil, nil, nil, workboard.WorkItem{}
}

func p314LinkedOptions(
	eng *QueryEngine,
	item workboard.WorkItem,
	agentID string,
) tools.AgentExecOptions {
	authority := eng.logicalWorkAdapter.Snapshot()
	return tools.AgentExecOptions{
		AgentID:         agentID,
		Task:            "linked execution",
		ParentSessionID: eng.config.SessionID,
		ParentThreadID:  eng.config.ThreadID,
		ToolUseID:       "tool-" + agentID,
		WorkItem: &tools.AgentWorkItemReference{
			BoardID:              authority.BoardID,
			WorkItemID:           item.ID,
			ExpectedItemRevision: item.Revision,
		},
	}
}

func p314ExecutionRow(
	t *testing.T,
	snapshot TaskExplorerSnapshot,
	agentID string,
	generation int64,
) TaskExplorerExecution {
	t.Helper()
	for _, row := range snapshot.Executions {
		if row.Key.AgentID == agentID &&
			row.Key.Generation == generation {
			return row
		}
	}
	t.Fatalf("execution %s/%d not found: %+v", agentID, generation, snapshot)
	return TaskExplorerExecution{}
}

func p314WaitSettlement(
	t *testing.T,
	runner *tools.AgentRunner,
	key RuntimeExecutionKey,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		settlements := runner.AgentExecutionSettlements(
			[]tools.AgentExecutionKey{{
				AgentID: key.AgentID, Generation: key.Generation,
			}},
		)
		if len(settlements) == 1 && settlements[0].Settled {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("execution %s/%d did not settle", key.AgentID, key.Generation)
}
