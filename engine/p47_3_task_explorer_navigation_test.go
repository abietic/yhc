package engine

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/tools"
)

func TestP473ApplySwitchReturnsExactTargetWithoutDispatch(t *testing.T) {
	release := make(chan struct{})
	eng, runner, _, item := p314EngineFixture(t)
	released := false
	defer func() {
		if !released {
			close(release)
		}
		eng.Close()
	}()
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
		p314LinkedOptions(eng, item, "p473-live"),
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-executor.executed:
	case <-time.After(time.Second):
		t.Fatal("exact navigation execution did not dispatch")
	}

	snapshot := eng.TaskExplorerSnapshot()
	row := p314ExecutionRow(t, snapshot, "p473-live", 1)
	if !taskExplorerActionAllowed(
		row.AllowedActions,
		TaskExplorerActionSwitch,
	) {
		t.Fatalf("exact live actions = %v", row.AllowedActions)
	}
	target, err := eng.ResolveTaskExplorerNavigationTarget(
		row.Key.AgentID,
		row.Key.Generation,
	)
	if err != nil {
		t.Fatalf("resolve production target: %v", err)
	}
	before, ok := runner.GetAgentSnapshot(row.Key.AgentID)
	if !ok {
		t.Fatal("live Agent snapshot missing before switch")
	}
	result := eng.ApplyTaskExplorerAction(TaskExplorerActionRequest{
		RequestID:       "p473-switch",
		BoardID:         snapshot.BoardID,
		BoardRevision:   snapshot.Revision.Board,
		RuntimeRevision: snapshot.Revision.Runtime,
		AgentID:         row.Key.AgentID,
		Generation:      row.Key.Generation,
		Action:          TaskExplorerActionSwitch,
	})
	if result.Outcome != "switched" || result.Conflict != "" ||
		result.NavigationTarget == nil ||
		*result.NavigationTarget != target ||
		result.SessionID != target.SessionID ||
		result.ThreadID != target.ThreadID {
		t.Fatalf("switch result = %+v, target = %+v", result, target)
	}
	after, ok := runner.GetAgentSnapshot(row.Key.AgentID)
	if !ok || before.Status != after.Status ||
		before.PendingMessageCount != after.PendingMessageCount ||
		before.ExecutionGeneration() != after.ExecutionGeneration() {
		t.Fatalf("switch mutated Agent: before=%+v after=%+v", before, after)
	}

	close(release)
	released = true
	p314WaitSettlement(t, runner, row.Key)
}

func TestP473NavigationTargetRequiresExactCurrentGeneration(t *testing.T) {
	selection := p473NavigationSelection()
	target, err := resolveTaskExplorerNavigationTarget(
		selection,
		"agent-a",
		1,
	)
	if err != nil {
		t.Fatalf("resolve exact target: %v", err)
	}
	want := TaskExplorerNavigationTarget{
		SessionID: "child-session", ThreadID: "shared-thread",
		AgentID: "agent-a", Generation: 1,
		Mode: ThreadModeLiveAttach,
	}
	if target != want {
		t.Fatalf("target = %#v, want %#v", target, want)
	}
	selection = p473NavigationSelection()
	longPath := "/tmp/" + strings.Repeat("x", maxTaskExplorerTextRunes+1)
	selection.snapshot.Executions[0].TranscriptPath = truncateTaskExplorerText(longPath)
	current := selection.runtime.Agents["agent-a"]
	current.TranscriptPath = longPath
	selection.runtime.Agents["agent-a"] = current
	selection.catalog.Threads[0].TranscriptPath = longPath
	if _, err := resolveTaskExplorerNavigationTarget(
		selection,
		"agent-a",
		1,
	); err != nil {
		t.Fatalf("truncated display path rejected exact runtime path: %v", err)
	}

	selection = p473NavigationSelection()
	current = selection.runtime.Agents["agent-a"]
	current.Generation = 2
	selection.runtime.Agents["agent-a"] = current
	_, err = resolveTaskExplorerNavigationTarget(selection, "agent-a", 1)
	if !errors.Is(err, ErrTaskExplorerNavigationStale) {
		t.Fatalf("superseded generation error = %v", err)
	}
}

func TestP473NavigationTargetFailsClosedOnCurrentFacts(t *testing.T) {
	tests := []struct {
		name string
		edit func(*taskExplorerSelection)
		want error
	}{
		{
			name: "missing exact row",
			edit: func(selection *taskExplorerSelection) {
				selection.snapshot.Executions = nil
			},
			want: ErrTaskExplorerNavigationStale,
		},
		{
			name: "runtime catalog revision raced",
			edit: func(selection *taskExplorerSelection) {
				selection.catalog.Revision++
			},
			want: ErrTaskExplorerNavigationStale,
		},
		{
			name: "current Agent missing",
			edit: func(selection *taskExplorerSelection) {
				delete(selection.runtime.Agents, "agent-a")
			},
			want: ErrTaskExplorerNavigationStale,
		},
		{
			name: "current Agent lineage changed",
			edit: func(selection *taskExplorerSelection) {
				agent := selection.runtime.Agents["agent-a"]
				agent.SessionID = "other-session"
				selection.runtime.Agents["agent-a"] = agent
			},
			want: ErrTaskExplorerNavigationStale,
		},
		{
			name: "predispatch row",
			edit: func(selection *taskExplorerSelection) {
				selection.snapshot.Executions[0].Predispatch = true
			},
			want: ErrTaskExplorerNavigationUnavailable,
		},
		{
			name: "replay-only row",
			edit: func(selection *taskExplorerSelection) {
				selection.snapshot.Executions[0].ReplayOnly = true
			},
			want: ErrTaskExplorerNavigationUnavailable,
		},
		{
			name: "missing transcript",
			edit: func(selection *taskExplorerSelection) {
				selection.snapshot.Executions[0].TranscriptPath = ""
			},
			want: ErrTaskExplorerNavigationUnavailable,
		},
		{
			name: "catalog row missing",
			edit: func(selection *taskExplorerSelection) {
				selection.catalog.Threads = nil
			},
			want: ErrTaskExplorerNavigationUnavailable,
		},
		{
			name: "catalog ThreadID duplicate",
			edit: func(selection *taskExplorerSelection) {
				selection.catalog.Threads = append(
					selection.catalog.Threads,
					selection.catalog.Threads[0],
				)
			},
			want: ErrTaskExplorerNavigationUnavailable,
		},
		{
			name: "catalog Session mismatched",
			edit: func(selection *taskExplorerSelection) {
				selection.catalog.Threads[0].SessionID = "other-session"
			},
			want: ErrTaskExplorerNavigationUnavailable,
		},
		{
			name: "catalog Agent mismatched",
			edit: func(selection *taskExplorerSelection) {
				selection.catalog.Threads[0].AgentID = "other-agent"
			},
			want: ErrTaskExplorerNavigationUnavailable,
		},
		{
			name: "catalog transcript mismatched",
			edit: func(selection *taskExplorerSelection) {
				selection.catalog.Threads[0].TranscriptPath = "/other"
			},
			want: ErrTaskExplorerNavigationUnavailable,
		},
		{
			name: "replay attachment unsupported",
			edit: func(selection *taskExplorerSelection) {
				selection.catalog.Threads[0].Mode = ThreadModeReplayOnly
			},
			want: ErrTaskExplorerNavigationUnavailable,
		},
		{
			name: "evicted attachment unsupported",
			edit: func(selection *taskExplorerSelection) {
				selection.catalog.Threads[0].Mode = ThreadModeEvictedTranscript
			},
			want: ErrTaskExplorerNavigationUnavailable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selection := p473NavigationSelection()
			test.edit(&selection)
			_, err := resolveTaskExplorerNavigationTarget(
				selection,
				"agent-a",
				1,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestP473SwitchDeclarationUsesExactNavigationResolver(t *testing.T) {
	selection := p473NavigationSelection()
	declareTaskExplorerActions(
		&selection.snapshot,
		selection.runtime,
		selection.catalog,
		tools.NewAgentRunner(1),
	)
	if !reflect.DeepEqual(
		selection.snapshot.Executions[0].AllowedActions,
		[]TaskExplorerAction{
			TaskExplorerActionInspect,
			TaskExplorerActionSwitch,
		},
	) {
		t.Fatalf(
			"exact actions = %v",
			selection.snapshot.Executions[0].AllowedActions,
		)
	}

	selection = p473NavigationSelection()
	current := selection.runtime.Agents["agent-a"]
	current.Generation = 2
	selection.runtime.Agents["agent-a"] = current
	declareTaskExplorerActions(
		&selection.snapshot,
		selection.runtime,
		selection.catalog,
		tools.NewAgentRunner(1),
	)
	if !reflect.DeepEqual(
		selection.snapshot.Executions[0].AllowedActions,
		[]TaskExplorerAction{TaskExplorerActionInspect},
	) {
		t.Fatalf(
			"superseded actions = %v",
			selection.snapshot.Executions[0].AllowedActions,
		)
	}
}

func p473NavigationSelection() taskExplorerSelection {
	row := TaskExplorerExecution{
		Key:       RuntimeExecutionKey{AgentID: "agent-a", Generation: 1},
		SessionID: "child-session", ThreadID: "shared-thread",
		TranscriptPath: "/tmp/p473-transcript.jsonl",
		Status:         "running", Phase: TaskExplorerExecutionRunning,
	}
	agent := RuntimeAgentSnapshot{
		AgentID: row.Key.AgentID, Generation: row.Key.Generation,
		SessionID: row.SessionID, ThreadID: row.ThreadID,
		TranscriptPath: row.TranscriptPath, Status: row.Status,
	}
	return taskExplorerSelection{
		snapshot: TaskExplorerSnapshot{
			Available: true, SessionID: "root-session", BoardID: "board",
			Revision:   TaskExplorerRevision{Board: 3, Runtime: 5},
			Executions: []TaskExplorerExecution{row},
		},
		runtime: RuntimeSnapshot{
			Revision: 5,
			Agents: map[string]RuntimeAgentSnapshot{
				row.Key.AgentID: agent,
			},
			Threads: map[string]RuntimeThreadSnapshot{},
			Tasks:   map[string]RuntimeTaskSnapshot{}, Worktrees: map[string]RuntimeWorktreeSnapshot{},
		},
		catalog: RuntimeThreadCatalogSnapshot{
			Revision: 5,
			Threads: []RuntimeThreadCatalogEntry{{
				SessionID: row.SessionID, ThreadID: row.ThreadID,
				AgentID: row.Key.AgentID, TranscriptPath: row.TranscriptPath,
				Status: RuntimeThreadRunning, Mode: ThreadModeLiveAttach,
			}},
		},
	}
}
