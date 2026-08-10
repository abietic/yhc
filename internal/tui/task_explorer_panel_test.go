package tui

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/terminalcap"
)

func TestP313TaskExplorerResponsiveFixtureMatrix(t *testing.T) {
	t.Parallel()
	fixtures := []struct {
		name     string
		snapshot engine.TaskExplorerSnapshot
		want     string
	}{
		{name: "empty", snapshot: p313ExplorerSnapshot(), want: "No matching work"},
		{
			name: "plan-only",
			snapshot: p313ExplorerSnapshot(engine.TaskExplorerWorkItem{
				BoardID: "board", WorkItemID: "plan", Status: "pending",
				Title: "计划 CJK e\u0301 👩‍💻 " + strings.Repeat("long-token", 20),
			}),
			want: "pending",
		},
		{
			name: "blocked",
			snapshot: p313ExplorerSnapshot(engine.TaskExplorerWorkItem{
				BoardID: "board", WorkItemID: "blocked", Status: "blocked",
				Title: "blocked work", BlockedBy: []string{"dependency"},
				Attention: []string{"blocked_dependency"},
			}),
			want: "blocked",
		},
		{
			name: "attention",
			snapshot: func() engine.TaskExplorerSnapshot {
				snapshot := p313ExplorerSnapshot(engine.TaskExplorerWorkItem{
					BoardID: "board", WorkItemID: "attention", Status: "pending",
					Title: "needs attention", Attention: []string{"fact"},
				})
				snapshot.Attention = []engine.TaskExplorerAttention{{
					Category: "fact", WorkItemID: "attention",
				}}
				snapshot.Hidden.Attention = map[string]int{"overflow": 2}
				return snapshot
			}(),
			want: "attention",
		},
	}
	executionFixtures := []struct {
		name      string
		execution engine.TaskExplorerExecution
		want      string
	}{
		{
			name: "execution-only",
			execution: p313Execution(
				"agent-live",
				1,
				engine.TaskExplorerExecutionRunning,
			),
			want: "agent-live@g1",
		},
		{
			name: "failure",
			execution: p313Execution(
				"agent-failed",
				2,
				engine.TaskExplorerExecutionFailed,
			),
			want: "failed",
		},
		{
			name: "replay-only",
			execution: func() engine.TaskExplorerExecution {
				row := p313Execution(
					"agent-replay",
					1,
					engine.TaskExplorerExecutionReplayOnly,
				)
				row.ReplayOnly = true
				return row
			}(),
			want: "replay_only",
		},
	}
	sizes := []struct {
		width  int
		height int
	}{
		{width: 40, height: 20},
		{width: 80, height: 24},
		{width: 120, height: 30},
		{width: 180, height: 30},
	}

	for _, fixture := range fixtures {
		for _, size := range sizes {
			t.Run(fmt.Sprintf("%s/%dx%d", fixture.name, size.width, size.height), func(t *testing.T) {
				panel := NewTaskExplorerPanel(defaultStyles())
				panel.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
					return fixture.snapshot
				})
				panel.Show(taskExplorerLogical, false)
				assertP313ExplorerFrame(
					t,
					panel.Render(size.width, size.height),
					size.width,
					size.height,
					fixture.want,
				)
			})
		}
	}
	for _, fixture := range executionFixtures {
		for _, size := range sizes {
			t.Run(fmt.Sprintf("%s/%dx%d", fixture.name, size.width, size.height), func(t *testing.T) {
				snapshot := p313ExplorerSnapshot()
				snapshot.Executions = []engine.TaskExplorerExecution{
					fixture.execution,
				}
				panel := NewTaskExplorerPanel(defaultStyles())
				panel.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
					return snapshot
				})
				panel.Show(taskExplorerExecutions, false)
				assertP313ExplorerFrame(
					t,
					panel.Render(size.width, size.height),
					size.width,
					size.height,
					fixture.want,
				)
			})
		}
	}
}

func TestP313TaskExplorerRefreshSearchAndFocusPreserveExactIdentity(t *testing.T) {
	snapshot := p313ExplorerSnapshot(
		engine.TaskExplorerWorkItem{
			BoardID: "board", WorkItemID: "one", Title: "one", Status: "pending",
		},
		engine.TaskExplorerWorkItem{
			BoardID: "board", WorkItemID: "two", Title: "two", Status: "pending",
		},
	)
	panel := NewTaskExplorerPanel(defaultStyles())
	panel.SetSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	panel.Show(taskExplorerLogical, false)
	panel.move(1, 24)

	snapshot.WorkItems = []engine.TaskExplorerWorkItem{
		{BoardID: "board", WorkItemID: "zero", Title: "zero"},
		{BoardID: "board", WorkItemID: "two", Title: "two"},
	}
	panel.Refresh()
	if panel.cursor != 1 || panel.selection.workID != "two" {
		t.Fatalf(
			"surviving identity = cursor %d selection %#v",
			panel.cursor,
			panel.selection,
		)
	}

	snapshot.WorkItems = []engine.TaskExplorerWorkItem{{
		BoardID: "board", WorkItemID: "replacement", Title: "replacement",
	}}
	panel.Refresh()
	if panel.cursor != 0 || panel.selection.workID != "replacement" {
		t.Fatalf(
			"index fallback = cursor %d selection %#v",
			panel.cursor,
			panel.selection,
		)
	}

	panel.HandleKey(tea.KeyPressMsg{Code: '/', Text: "/"}, 24)
	panel.HandleKey(
		tea.KeyPressMsg{Code: tea.KeyExtended, Text: "missing"},
		24,
	)
	if len(panel.rows) != 0 || panel.selection.valid() {
		t.Fatalf("empty search retained selection: %#v", panel.selection)
	}
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyBackspace}, 24)
	panel.HandleKey(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}, 24)
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, 24)
	if panel.searchFocus || len(panel.rows) != 1 {
		t.Fatalf(
			"search focus=%v rows=%d query=%q",
			panel.searchFocus,
			len(panel.rows),
			panel.search,
		)
	}
}

func TestP315BackgroundAndTeamUseExactGenerationActions(t *testing.T) {
	explorer := p313ExplorerSnapshot()
	explorer.Executions = []engine.TaskExplorerExecution{
		p313Execution("agent", 1, engine.TaskExplorerExecutionReplayOnly),
		p313Execution("agent", 2, engine.TaskExplorerExecutionRunning),
	}
	explorer.Executions[0].ThreadID = "thread-g1"
	explorer.Executions[0].ReplayOnly = true
	explorer.Executions[0].AllowedActions = []engine.TaskExplorerAction{
		engine.TaskExplorerActionInspect,
		engine.TaskExplorerActionSwitch,
	}
	explorer.Executions[1].ThreadID = "thread-g2"
	explorer.Executions[1].AllowedActions = []engine.TaskExplorerAction{
		engine.TaskExplorerActionInspect,
		engine.TaskExplorerActionSwitch,
		engine.TaskExplorerActionCancel,
	}

	detailCalls := 0
	cancelledGenerations := make([]int64, 0, 1)
	background := NewBackgroundTasksPanel(defaultStyles())
	background.SetExplorerSnapshotProvider(
		func() engine.TaskExplorerSnapshot { return explorer },
	)
	background.SetDetailProvider(
		func(agentID string) (engine.AgentDetailSnapshot, bool) {
			detailCalls++
			return engine.AgentDetailSnapshot{
				Agent: engine.RuntimeAgentSnapshot{
					AgentID: agentID, Generation: 2,
				},
			}, true
		},
	)
	background.SetActionProvider(func(request engine.TaskExplorerActionRequest) engine.TaskExplorerActionResult {
		cancelledGenerations = append(
			cancelledGenerations,
			request.Generation,
		)
		return engine.TaskExplorerActionResult{RequestID: request.RequestID, BoardID: request.BoardID, BoardRevision: request.BoardRevision, RuntimeRevision: request.RuntimeRevision, AgentID: request.AgentID, Generation: request.Generation, Action: request.Action, Outcome: "accepted"}
	})
	background.Show()
	for index, item := range background.items {
		if item.execution.Key.Generation == 1 {
			background.cursor = index
		}
	}
	background.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, 24)
	if detailCalls != 1 ||
		background.detail.Agent.Generation == 2 ||
		background.detail.LoadError == "" {
		t.Fatalf(
			"retained detail crossed fence: calls=%d detail=%+v",
			detailCalls,
			background.detail,
		)
	}
	background.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc}, 24)
	background.stopSelectedTask()
	if len(cancelledGenerations) != 0 {
		t.Fatalf(
			"retained generation dispatched cancel: %v",
			cancelledGenerations,
		)
	}

	team := NewTeamsPanel(defaultStyles())
	team.SetExplorerSnapshotProvider(
		func() engine.TaskExplorerSnapshot { return explorer },
	)
	team.SetDetailProvider(
		func(string) (engine.AgentDetailSnapshot, bool) {
			detailCalls++
			return engine.AgentDetailSnapshot{}, true
		},
	)
	team.Show()
	for index, item := range team.items {
		if item.execution.Key.Generation == 1 {
			team.cursor = index
		}
	}
	team.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab}, 24)
	if detailCalls != 2 ||
		team.detail.Agent.Generation == 2 ||
		team.detail.LoadError == "" {
		t.Fatalf(
			"retained team detail crossed fence: calls=%d detail=%+v",
			detailCalls,
			team.detail,
		)
	}
	team.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc}, 24)
	team.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, 24)
	if thread := team.takeSwitchThread(); thread != "thread-g1" {
		t.Fatalf("retained exact generation navigation = %q", thread)
	}

	background.Show()
	for index, item := range background.items {
		if item.execution.Key.Generation == 2 {
			background.cursor = index
		}
	}
	background.stopSelectedTask()
	if !reflect.DeepEqual(cancelledGenerations, []int64{2}) {
		t.Fatalf(
			"current generation cancel calls = %v",
			cancelledGenerations,
		)
	}
	team.Show()
	for index, item := range team.items {
		if item.execution.Key.Generation == 2 {
			team.cursor = index
		}
	}
	team.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, 24)
	if thread := team.takeSwitchThread(); thread != "thread-g2" {
		t.Fatalf("current generation navigation=%q", thread)
	}
}

func TestP315TUIHasNoLegacyTaskOrAgentMutationProvider(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate P31.5 TUI source gate")
	}
	root := filepath.Dir(testFile)
	forbidden := []string{
		"TaskManager",
		"AgentRunner",
		"SendAgentMessage(",
		"CancelAgentQueuedInput(",
		".AbortAgent(",
		".PauseAgent(",
		".ResumeAgent(",
	}
	err := filepath.WalkDir(root, func(
		path string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() ||
			!strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, token := range forbidden {
			if strings.Contains(string(source), token) {
				t.Errorf(
					"TUI production source %s retains legacy owner/provider %q",
					path,
					token,
				)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestP313ActivityTreePreservesSelectorOrder(t *testing.T) {
	app := New(Config{})
	app.engine = &engine.QueryEngine{}
	snapshot := p313ExplorerSnapshot(
		engine.TaskExplorerWorkItem{
			BoardID: "board", WorkItemID: "z-first",
			Title: "selector first", Status: "in_progress",
		},
		engine.TaskExplorerWorkItem{
			BoardID: "board", WorkItemID: "a-second",
			Title: "selector second", Status: "in_progress",
		},
	)
	installP313ExplorerSnapshot(app, &snapshot)

	frame := stripANSIForTest(app.renderTaskTree())
	first := strings.Index(frame, "selector first")
	second := strings.Index(frame, "selector second")
	if first < 0 || second < 0 || first >= second {
		t.Fatalf("activity tree reordered selector rows:\n%s", frame)
	}
}

func TestP313AgentTraceNavigationRequiresExactGeneration(t *testing.T) {
	app := New(Config{})
	app.state = StateChat
	explorer := p313ExplorerSnapshot()
	explorer.Executions = []engine.TaskExplorerExecution{
		p313Execution("agent", 2, engine.TaskExplorerExecutionRunning),
	}
	explorer.Executions[0].ThreadID = "thread-g2"
	installP313ExplorerSnapshot(app, &explorer)
	detailCalls := 0
	app.backgroundTasks.SetDetailProvider(
		func(agentID string) (engine.AgentDetailSnapshot, bool) {
			detailCalls++
			return engine.AgentDetailSnapshot{
				Agent: engine.RuntimeAgentSnapshot{
					AgentID: agentID, Generation: 2, Status: "running",
				},
			}, true
		},
	)
	app.chat.AppendOrUpdateTool("spawn-agent", "Agent", `{}`)
	app.chat.UpdateAgentToolTrace("spawn-agent", agentToolTrace{
		AgentID: "agent", ExecutionKey: engine.RuntimeExecutionKey{
			AgentID: "agent", Generation: 1,
		}, IdentityResolved: true,
	})

	stale, ok := app.chat.LatestAgentTraceTarget()
	if !ok {
		t.Fatal("stale trace did not retain its exact identity")
	}
	app.openAgentDetail(stale)
	if detailCalls != 0 || app.state == StateBackgroundTasks {
		t.Fatalf(
			"stale trace crossed generation fence: calls=%d state=%v",
			detailCalls,
			app.state,
		)
	}

	app.chat.UpdateAgentToolTrace("spawn-agent", agentToolTrace{
		AgentID: "agent", ExecutionKey: engine.RuntimeExecutionKey{
			AgentID: "agent", Generation: 2,
		}, IdentityResolved: true,
	})
	key, ok := app.chat.LatestAgentTraceTarget()
	if !ok {
		t.Fatal("current trace identity unavailable")
	}
	app.openAgentDetail(key)
	if detailCalls != 1 || app.state != StateBackgroundTasks {
		t.Fatalf(
			"current trace did not open exact detail: calls=%d state=%v",
			detailCalls,
			app.state,
		)
	}
}

func TestP313AgentTraceSyncNeverUpgradesRetainedGeneration(t *testing.T) {
	app := New(Config{})
	app.state = StateChat
	app.chat.AppendOrUpdateTool("spawn-agent", "Agent", `{}`)
	app.agentTraceProvider = func() []engine.AgentParentTraceSnapshot {
		return []engine.AgentParentTraceSnapshot{{
			AgentID:         "agent",
			ParentToolUseID: "spawn-agent",
			Status:          "running",
		}}
	}
	explorer := p313ExplorerSnapshot()
	explorer.Executions = []engine.TaskExplorerExecution{
		p313Execution("agent", 1, engine.TaskExplorerExecutionRunning),
	}
	explorer.Executions[0].ParentToolUseID = "spawn-agent"
	installP313ExplorerSnapshot(app, &explorer)

	app.syncAgentToolTraces()
	first, ok := app.chat.LatestAgentTraceTarget()
	if !ok || first != (engine.RuntimeExecutionKey{
		AgentID: "agent", Generation: 1,
	}) {
		t.Fatalf("initial trace identity=%#v/%v", first, ok)
	}

	explorer.Executions[0] = p313Execution(
		"agent",
		2,
		engine.TaskExplorerExecutionRunning,
	)
	explorer.Executions[0].ParentToolUseID = "spawn-agent"
	app.syncAgentToolTraces()
	retained, ok := app.chat.LatestAgentTraceTarget()
	if !ok || retained != first {
		t.Fatalf(
			"retained trace identity upgraded: first=%#v after=%#v/%v",
			first,
			retained,
			ok,
		)
	}
	app.openAgentDetail(retained)
	if app.state == StateBackgroundTasks {
		t.Fatal("retained g1 trace opened current g2 detail")
	}
}

func TestP313UnresolvedAgentTraceNeverAcquiresLaterGeneration(t *testing.T) {
	app := New(Config{})
	app.chat.AppendOrUpdateTool("spawn-agent", "Agent", `{}`)
	app.agentTraceProvider = func() []engine.AgentParentTraceSnapshot {
		return []engine.AgentParentTraceSnapshot{{
			AgentID:         "agent",
			ParentToolUseID: "spawn-agent",
			Status:          "running",
		}}
	}
	explorer := p313ExplorerSnapshot()
	installP313ExplorerSnapshot(app, &explorer)

	app.syncAgentToolTraces()
	if key, ok := app.chat.LatestAgentTraceTarget(); ok {
		t.Fatalf("initially unresolved trace exposed %#v", key)
	}
	explorer.Executions = []engine.TaskExplorerExecution{
		p313Execution("agent", 2, engine.TaskExplorerExecutionRunning),
	}
	explorer.Executions[0].ParentToolUseID = "spawn-agent"
	app.syncAgentToolTraces()
	if key, ok := app.chat.LatestAgentTraceTarget(); ok {
		t.Fatalf("unresolved trace acquired later generation %#v", key)
	}
}

func TestP313ExplorerSteadyFrameIsViewportBounded(t *testing.T) {
	snapshot := p313ExplorerSnapshot()
	for index := 0; index < 100; index++ {
		snapshot.WorkItems = append(
			snapshot.WorkItems,
			engine.TaskExplorerWorkItem{
				BoardID: "board", WorkItemID: fmt.Sprintf("work-%03d", index),
				Title: fmt.Sprintf("bounded row %03d", index), Status: "pending",
			},
		)
	}
	providerCalls := 0
	panel := NewTaskExplorerPanel(defaultStyles())
	panel.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
		providerCalls++
		return snapshot
	})
	panel.Show(taskExplorerLogical, false)
	assertPerformanceP95(
		t,
		"P31.3 100-row explorer steady frame",
		20*time.Millisecond,
		80,
		func() {
			panel.Render(120, 24)
		},
	)
	if providerCalls != 1 {
		t.Fatalf("steady render re-read selector %d times", providerCalls)
	}
}

func TestP313ExplorerNoColorReducedMotionKeepsFactsAndRefresh(t *testing.T) {
	capabilities := interactiveTerminalCaps()
	capabilities.Color = terminalcap.ColorNone
	app := New(Config{
		Resumed: true, ReducedMotion: true, TerminalCaps: &capabilities,
	})
	app.width, app.height = 80, 24
	app.updateLayout()
	snapshot := p313ExplorerSnapshot(engine.TaskExplorerWorkItem{
		BoardID: "board", WorkItemID: "work", Title: "CJK 任务 e\u0301 👩‍💻",
		Status: "blocked", Attention: []string{"blocked_dependency"},
	})
	snapshot.Attention = []engine.TaskExplorerAttention{{
		Category: "blocked_dependency", WorkItemID: "work",
	}}
	snapshot.Hidden.WorkItems = map[string]int{"pending": 2}
	app.taskExplorer.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
		return snapshot
	})
	app.enterTaskPanel()

	frame := app.renderView()
	if strings.Contains(frame, "\x1b") {
		t.Fatalf("no-color explorer contains ANSI: %q", frame)
	}
	for _, want := range []string{
		"CJK 任务", "blocked", "attention 1", "hidden 2",
	} {
		if !strings.Contains(frame, want) {
			t.Fatalf("no-color explorer omitted %q:\n%s", want, frame)
		}
	}
	snapshot.WorkItems[0].Title = "refreshed fact"
	updated, _ := app.Update(taskExplorerTickMsg{})
	app = updated.(*App)
	if frame = app.renderView(); !strings.Contains(frame, "refreshed fact") {
		t.Fatalf("reduced-motion tick stopped refresh:\n%s", frame)
	}
}

func TestP314ExplorerActionsConfirmPayloadAndResultIdentity(t *testing.T) {
	snapshot := p313ExplorerSnapshot()
	snapshot.Revision = engine.TaskExplorerRevision{Board: 7, Runtime: 11}
	snapshot.Executions = []engine.TaskExplorerExecution{{
		Key:            engine.RuntimeExecutionKey{AgentID: "child", Generation: 3},
		SessionID:      "child-session",
		ThreadID:       "child-thread",
		TranscriptPath: "/tmp/child-transcript.jsonl",
		Phase:          engine.TaskExplorerExecutionRunning,
		AllowedActions: []engine.TaskExplorerAction{
			engine.TaskExplorerActionInspect,
			engine.TaskExplorerActionSwitch,
			engine.TaskExplorerActionSend,
			engine.TaskExplorerActionPause,
			engine.TaskExplorerActionCancel,
		},
	}}
	panel := NewTaskExplorerPanel(defaultStyles())
	panel.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
		return snapshot
	})
	var requests []engine.TaskExplorerActionRequest
	panel.SetActionProvider(func(
		request engine.TaskExplorerActionRequest,
	) engine.TaskExplorerActionResult {
		requests = append(requests, request)
		result := engine.TaskExplorerActionResult{
			RequestID:       request.RequestID,
			BoardID:         request.BoardID,
			BoardRevision:   request.BoardRevision,
			RuntimeRevision: request.RuntimeRevision + 1,
			AgentID:         request.AgentID,
			Generation:      request.Generation,
			Action:          request.Action,
			Outcome:         string(request.Action) + "_accepted",
			SessionID:       "child-session",
			ThreadID:        "child-thread",
		}
		if request.Action == engine.TaskExplorerActionSwitch {
			result.Outcome = "switched"
			result.NavigationTarget = &engine.TaskExplorerNavigationTarget{
				SessionID: "child-session", ThreadID: "child-thread",
				AgentID: request.AgentID, Generation: request.Generation,
				Mode: engine.ThreadModeLiveAttach,
			}
		}
		return result
	})
	panel.Show(taskExplorerExecutions, false)

	panel.HandleKey(tea.KeyPressMsg{Code: 'c', Text: "c"}, 24)
	frame := stripANSIForTest(panel.Render(80, 24))
	if !strings.Contains(frame, "Confirm cancel child@g3? y/N") ||
		len(requests) != 0 {
		t.Fatalf(
			"cancel was not held for confirmation: %q %+v",
			frame,
			requests,
		)
	}
	panel.HandleKey(tea.KeyPressMsg{Code: 'y', Text: "y"}, 24)
	if len(requests) != 1 ||
		requests[0].BoardID != "board" ||
		requests[0].BoardRevision != 7 ||
		requests[0].RuntimeRevision != 11 ||
		requests[0].AgentID != "child" ||
		requests[0].Generation != 3 ||
		requests[0].Action != engine.TaskExplorerActionCancel {
		t.Fatalf("cancel request = %+v", requests)
	}

	panel.HandleKey(tea.KeyPressMsg{Code: 's', Text: "s"}, 24)
	panel.HandleKey(
		tea.KeyPressMsg{Code: tea.KeyExtended, Text: "payload"},
		24,
	)
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, 24)
	if len(requests) != 2 ||
		requests[1].Action != engine.TaskExplorerActionSend ||
		requests[1].Payload != "payload" {
		t.Fatalf("send request = %+v", requests)
	}

	panel.HandleKey(tea.KeyPressMsg{Code: 'x', Text: "x"}, 24)
	if got, ok := panel.takeSwitchTarget(); !ok ||
		got.Target.ThreadID != "child-thread" ||
		got.Target.Generation != 3 {
		t.Fatalf("switch target = %+v, ok=%v", got, ok)
	}
	panel.SetActionProvider(func(
		request engine.TaskExplorerActionRequest,
	) engine.TaskExplorerActionResult {
		return engine.TaskExplorerActionResult{
			RequestID:     "late-other-request",
			BoardID:       request.BoardID,
			BoardRevision: request.BoardRevision,
			AgentID:       request.AgentID,
			Generation:    request.Generation,
			Action:        request.Action,
			Outcome:       "switched",
			SessionID:     "child-session",
			ThreadID:      "wrong-thread",
			NavigationTarget: &engine.TaskExplorerNavigationTarget{
				SessionID: "child-session", ThreadID: "wrong-thread",
				AgentID: request.AgentID, Generation: request.Generation,
				Mode: engine.ThreadModeLiveAttach,
			},
		}
	})
	panel.HandleKey(tea.KeyPressMsg{Code: 'x', Text: "x"}, 24)
	if got, ok := panel.takeSwitchTarget(); ok {
		t.Fatalf("late mismatched result retained target %+v", got)
	}
}

func TestP473PanelRejectsMalformedNavigationTarget(t *testing.T) {
	tests := []struct {
		name string
		edit func(*engine.TaskExplorerActionResult)
	}{
		{
			name: "missing target",
			edit: func(result *engine.TaskExplorerActionResult) {
				result.NavigationTarget = nil
			},
		},
		{
			name: "wrong Session",
			edit: func(result *engine.TaskExplorerActionResult) {
				result.NavigationTarget.SessionID = "other-session"
			},
		},
		{
			name: "wrong thread",
			edit: func(result *engine.TaskExplorerActionResult) {
				result.NavigationTarget.ThreadID = "other-thread"
			},
		},
		{
			name: "wrong Agent",
			edit: func(result *engine.TaskExplorerActionResult) {
				result.NavigationTarget.AgentID = "other-agent"
			},
		},
		{
			name: "wrong generation",
			edit: func(result *engine.TaskExplorerActionResult) {
				result.NavigationTarget.Generation++
			},
		},
		{
			name: "unsupported mode",
			edit: func(result *engine.TaskExplorerActionResult) {
				result.NavigationTarget.Mode = engine.ThreadModeReplayOnly
			},
		},
		{
			name: "legacy fields disagree",
			edit: func(result *engine.TaskExplorerActionResult) {
				result.ThreadID = "other-thread"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := p313ExplorerSnapshot()
			snapshot.Revision = engine.TaskExplorerRevision{
				Board: 7, Runtime: 11,
			}
			snapshot.Executions = []engine.TaskExplorerExecution{{
				Key: engine.RuntimeExecutionKey{
					AgentID: "agent-a", Generation: 3,
				},
				SessionID: "child-session", ThreadID: "child-thread",
				TranscriptPath: "/tmp/child-transcript.jsonl",
				Phase:          engine.TaskExplorerExecutionRunning,
				AllowedActions: []engine.TaskExplorerAction{
					engine.TaskExplorerActionInspect,
					engine.TaskExplorerActionSwitch,
				},
			}}
			panel := NewTaskExplorerPanel(defaultStyles())
			panel.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
				return snapshot
			})
			panel.SetActionProvider(func(
				request engine.TaskExplorerActionRequest,
			) engine.TaskExplorerActionResult {
				result := engine.TaskExplorerActionResult{
					RequestID: request.RequestID, BoardID: request.BoardID,
					BoardRevision:   request.BoardRevision,
					RuntimeRevision: request.RuntimeRevision,
					AgentID:         request.AgentID, Generation: request.Generation,
					Action: request.Action, Outcome: "switched",
					SessionID: "child-session", ThreadID: "child-thread",
					NavigationTarget: &engine.TaskExplorerNavigationTarget{
						SessionID: "child-session", ThreadID: "child-thread",
						AgentID: request.AgentID, Generation: request.Generation,
						Mode: engine.ThreadModeLiveAttach,
					},
				}
				test.edit(&result)
				return result
			})
			panel.Show(taskExplorerExecutions, false)
			panel.HandleKey(tea.KeyPressMsg{Code: 'x', Text: "x"}, 24)
			if target, ok := panel.takeSwitchTarget(); ok {
				t.Fatalf("malformed result retained target %+v", target)
			}
			if !strings.Contains(panel.notice, "Exact navigation target") {
				t.Fatalf("malformed result notice = %q", panel.notice)
			}
		})
	}
}

func TestP471PendingActionIntentCannotRetargetAcrossRefresh(t *testing.T) {
	actions := []struct {
		name    string
		action  engine.TaskExplorerAction
		start   tea.KeyPressMsg
		submit  tea.KeyPressMsg
		payload string
	}{
		{
			name: "send", action: engine.TaskExplorerActionSend,
			start:  tea.KeyPressMsg{Code: 's', Text: "s"},
			submit: tea.KeyPressMsg{Code: tea.KeyEnter}, payload: "send payload",
		},
		{
			name: "continue", action: engine.TaskExplorerActionContinue,
			start:  tea.KeyPressMsg{Code: 'n', Text: "n"},
			submit: tea.KeyPressMsg{Code: tea.KeyEnter}, payload: "continue payload",
		},
		{
			name: "cancel", action: engine.TaskExplorerActionCancel,
			start:  tea.KeyPressMsg{Code: 'c', Text: "c"},
			submit: tea.KeyPressMsg{Code: 'y', Text: "y"},
		},
	}
	refreshes := []struct {
		name    string
		updated func(
			engine.TaskExplorerExecution,
			engine.TaskExplorerExecution,
		) []engine.TaskExplorerExecution
	}{
		{
			name: "selected execution removed",
			updated: func(
				_ engine.TaskExplorerExecution,
				replacement engine.TaskExplorerExecution,
			) []engine.TaskExplorerExecution {
				return []engine.TaskExplorerExecution{replacement}
			},
		},
		{
			name: "selected execution retained after reorder",
			updated: func(
				original engine.TaskExplorerExecution,
				replacement engine.TaskExplorerExecution,
			) []engine.TaskExplorerExecution {
				return []engine.TaskExplorerExecution{replacement, original}
			},
		},
	}

	for _, actionCase := range actions {
		for _, refreshCase := range refreshes {
			t.Run(actionCase.name+"/"+refreshCase.name, func(t *testing.T) {
				original := p313Execution(
					"agent-a",
					1,
					engine.TaskExplorerExecutionRunning,
				)
				original.Name = "same label"
				original.AllowedActions = []engine.TaskExplorerAction{
					actionCase.action,
				}
				replacement := p313Execution(
					"agent-b",
					2,
					engine.TaskExplorerExecutionRunning,
				)
				replacement.Name = "same label"
				replacement.AllowedActions = []engine.TaskExplorerAction{
					actionCase.action,
				}

				snapshot := p313ExplorerSnapshot()
				snapshot.Revision = engine.TaskExplorerRevision{
					Board: 7, Runtime: 11,
				}
				snapshot.Executions = []engine.TaskExplorerExecution{
					original,
					replacement,
				}
				panel := NewTaskExplorerPanel(defaultStyles())
				snapshotCalls := 0
				panel.SetSnapshotProvider(
					func() engine.TaskExplorerSnapshot {
						snapshotCalls++
						return snapshot
					},
				)
				var requests []engine.TaskExplorerActionRequest
				panel.SetActionProvider(func(
					request engine.TaskExplorerActionRequest,
				) engine.TaskExplorerActionResult {
					requests = append(requests, request)
					messageID := request.MessageID
					if request.Action == engine.TaskExplorerActionSend {
						messageID = request.RequestID
					}
					return engine.TaskExplorerActionResult{
						RequestID:       request.RequestID,
						BoardID:         request.BoardID,
						BoardRevision:   request.BoardRevision,
						RuntimeRevision: request.RuntimeRevision + 1,
						AgentID:         request.AgentID,
						Generation:      request.Generation,
						MessageID:       messageID,
						Action:          request.Action,
						Outcome:         "accepted",
					}
				})
				panel.Show(taskExplorerExecutions, false)
				panel.HandleKey(actionCase.start, 24)

				snapshot.Revision = engine.TaskExplorerRevision{
					Board: 8, Runtime: 12,
				}
				snapshot.Executions = refreshCase.updated(
					original,
					replacement,
				)
				panel.Refresh()
				if actionCase.payload != "" {
					panel.HandleKey(tea.KeyPressMsg{
						Code: tea.KeyExtended,
						Text: actionCase.payload,
					}, 24)
				}
				panel.HandleKey(actionCase.submit, 24)

				if len(requests) != 1 {
					t.Fatalf("requests = %+v", requests)
				}
				if snapshotCalls != 3 {
					t.Fatalf(
						"accepted result did not refresh: calls=%d",
						snapshotCalls,
					)
				}
				request := requests[0]
				if request.RequestID == "" ||
					request.BoardID != "board" ||
					request.BoardRevision != 7 ||
					request.RuntimeRevision != 11 ||
					request.AgentID != "agent-a" ||
					request.Generation != 1 ||
					request.Action != actionCase.action ||
					request.Payload != actionCase.payload {
					t.Fatalf("pending action retargeted: %+v", request)
				}
			})
		}
	}
}

func TestP471ActionResultCannotClearNewerPendingIntent(t *testing.T) {
	original := p313Execution(
		"agent-a",
		1,
		engine.TaskExplorerExecutionRunning,
	)
	original.AllowedActions = []engine.TaskExplorerAction{
		engine.TaskExplorerActionSend,
	}
	replacement := p313Execution(
		"agent-b",
		2,
		engine.TaskExplorerExecutionRunning,
	)
	replacement.AllowedActions = []engine.TaskExplorerAction{
		engine.TaskExplorerActionContinue,
	}
	snapshot := p313ExplorerSnapshot()
	snapshot.Revision = engine.TaskExplorerRevision{Board: 7, Runtime: 11}
	snapshot.Executions = []engine.TaskExplorerExecution{original}

	panel := NewTaskExplorerPanel(defaultStyles())
	panel.SetSnapshotProvider(
		func() engine.TaskExplorerSnapshot { return snapshot },
	)
	var requests []engine.TaskExplorerActionRequest
	panel.SetActionProvider(func(
		request engine.TaskExplorerActionRequest,
	) engine.TaskExplorerActionResult {
		requests = append(requests, request)
		if len(requests) == 1 {
			panel.HandleKey(
				tea.KeyPressMsg{Code: 'n', Text: "n"},
				24,
			)
		}
		return engine.TaskExplorerActionResult{
			RequestID:       request.RequestID,
			BoardID:         request.BoardID,
			BoardRevision:   request.BoardRevision,
			RuntimeRevision: request.RuntimeRevision + 1,
			AgentID:         request.AgentID,
			Generation:      request.Generation,
			MessageID:       request.MessageID,
			Action:          request.Action,
			Outcome:         "accepted",
		}
	})
	panel.Show(taskExplorerExecutions, false)
	panel.HandleKey(tea.KeyPressMsg{Code: 's', Text: "s"}, 24)
	panel.HandleKey(tea.KeyPressMsg{
		Code: tea.KeyExtended,
		Text: "first payload",
	}, 24)

	snapshot.Revision = engine.TaskExplorerRevision{Board: 8, Runtime: 12}
	snapshot.Executions = []engine.TaskExplorerExecution{replacement}
	panel.Refresh()
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, 24)
	panel.HandleKey(tea.KeyPressMsg{
		Code: tea.KeyExtended,
		Text: "next payload",
	}, 24)
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, 24)

	if len(requests) != 2 {
		t.Fatalf("requests = %+v", requests)
	}
	want := []struct {
		boardRevision   uint64
		runtimeRevision uint64
		agentID         string
		generation      int64
		action          engine.TaskExplorerAction
		payload         string
	}{
		{
			boardRevision: 7, runtimeRevision: 11,
			agentID: "agent-a", generation: 1,
			action: engine.TaskExplorerActionSend, payload: "first payload",
		},
		{
			boardRevision: 8, runtimeRevision: 12,
			agentID: "agent-b", generation: 2,
			action: engine.TaskExplorerActionContinue, payload: "next payload",
		},
	}
	for index, expected := range want {
		request := requests[index]
		if request.BoardRevision != expected.boardRevision ||
			request.RuntimeRevision != expected.runtimeRevision ||
			request.AgentID != expected.agentID ||
			request.Generation != expected.generation ||
			request.Action != expected.action ||
			request.Payload != expected.payload {
			t.Fatalf(
				"request %d = %+v, want %+v",
				index,
				request,
				expected,
			)
		}
	}
}

func TestP471PendingActionRenderIsPure(t *testing.T) {
	snapshot := p313ExplorerSnapshot()
	execution := p313Execution(
		"agent-a",
		1,
		engine.TaskExplorerExecutionRunning,
	)
	execution.AllowedActions = []engine.TaskExplorerAction{
		engine.TaskExplorerActionSend,
	}
	snapshot.Executions = []engine.TaskExplorerExecution{execution}
	snapshotCalls := 0
	actionCalls := 0
	panel := NewTaskExplorerPanel(defaultStyles())
	panel.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
		snapshotCalls++
		return snapshot
	})
	panel.SetActionProvider(func(
		request engine.TaskExplorerActionRequest,
	) engine.TaskExplorerActionResult {
		actionCalls++
		return engine.TaskExplorerActionResult{RequestID: request.RequestID}
	})
	panel.Show(taskExplorerExecutions, false)
	panel.HandleKey(tea.KeyPressMsg{Code: 's', Text: "s"}, 24)

	first := stripANSIForTest(panel.Render(80, 24))
	second := stripANSIForTest(panel.Render(80, 24))
	if snapshotCalls != 1 || actionCalls != 0 {
		t.Fatalf(
			"render dispatched providers: snapshot=%d action=%d",
			snapshotCalls,
			actionCalls,
		)
	}
	for index, frame := range []string{first, second} {
		if !strings.Contains(frame, "send agent-a@g1>") {
			t.Fatalf("frame %d omitted frozen target:\n%s", index, frame)
		}
	}
}

func p313ExplorerSnapshot(
	workItems ...engine.TaskExplorerWorkItem,
) engine.TaskExplorerSnapshot {
	return engine.TaskExplorerSnapshot{
		Available: true,
		BoardID:   "board",
		WorkItems: workItems,
		Hidden: engine.TaskExplorerHiddenCounts{
			WorkItems:  map[string]int{},
			Executions: map[string]int{},
			Attention:  map[string]int{},
		},
	}
}

func p313Execution(
	agentID string,
	generation int64,
	phase engine.TaskExplorerExecutionPhase,
) engine.TaskExplorerExecution {
	return engine.TaskExplorerExecution{
		Key: engine.RuntimeExecutionKey{
			AgentID: agentID, Generation: generation,
		},
		Name:     agentID,
		Task:     "inspect " + agentID,
		Activity: "Read",
		Status:   string(phase),
		Phase:    phase,
	}
}

func assertP313ExplorerFrame(
	t *testing.T,
	frame string,
	width, height int,
	want string,
) {
	t.Helper()
	plain := stripANSIForTest(frame)
	if !strings.Contains(plain, want) {
		t.Fatalf("%dx%d omitted %q:\n%s", width, height, want, plain)
	}
	lines := strings.Split(frame, "\n")
	if len(lines) > height {
		t.Fatalf("%dx%d emitted %d rows", width, height, len(lines))
	}
	profile := DefaultDisplayCellProfile()
	for index, line := range lines {
		if got := profile.width(line); got > width {
			t.Fatalf(
				"%dx%d row %d width=%d: %q",
				width,
				height,
				index,
				got,
				line,
			)
		}
	}
}

func installP313ExplorerSnapshot(
	app *App,
	snapshot *engine.TaskExplorerSnapshot,
) {
	provider := func() engine.TaskExplorerSnapshot { return *snapshot }
	app.taskExplorer.SetSnapshotProvider(provider)
	app.backgroundTasks.SetExplorerSnapshotProvider(provider)
	app.teamsPanel.SetExplorerSnapshotProvider(provider)
}
