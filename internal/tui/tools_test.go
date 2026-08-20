package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/permission"
)

func TestRenderToolHeaderFormatsFunctionCall(t *testing.T) {
	header := renderToolHeader(defaultStyles(), "Read", ToolSuccess, `{"file_path":"/tmp/main.go","limit":80,"offset":1072}`, 0)
	plain := stripANSIForTest(header)
	if !strings.Contains(plain, "● Read") {
		t.Fatalf("unexpected header: %q", plain)
	}
	if !strings.Contains(plain, "main.go") {
		t.Fatalf("expected path in header: %q", plain)
	}
}

func TestFileHyperlinkEscapesDestinationControls(t *testing.T) {
	path := "/tmp/name with \x1b]8;;https://example.test\x1b\\ controls.go"
	link := fileHyperlink(path, "visible.go")
	end := strings.Index(link, "\x1b\\")
	if end < 0 {
		t.Fatalf("hyperlink opener has no terminator: %q", link)
	}
	destination := strings.TrimPrefix(link[:end], "\x1b]8;;")
	if strings.ContainsAny(destination, "\x1b\a") {
		t.Fatalf("hyperlink destination retained a terminal control: %q", destination)
	}
	if !strings.Contains(destination, "%1B") ||
		!strings.Contains(destination, "%20") {
		t.Fatalf("hyperlink destination was not URL-escaped: %q", destination)
	}
	label := strings.TrimSuffix(link[end+len("\x1b\\"):], "\x1b]8;;\x1b\\")
	if label != "visible.go" {
		t.Fatalf("hyperlink label = %q, want visible.go", label)
	}
}

func TestRenderToolBodyCollapsedPreview(t *testing.T) {
	output := strings.Join([]string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}, "\n")
	body := renderToolBody(defaultStyles(), "Read", output, false, ToolSuccess, 80)
	plain := stripANSIForTest(body)
	if !strings.Contains(plain, "⎿") {
		t.Fatalf("expected result gutter, got %q", plain)
	}
	if !strings.Contains(plain, "Read 9 lines") {
		t.Fatalf("expected read summary, got %q", plain)
	}
}

func TestRenderToolBodyExpandedPreview(t *testing.T) {
	output := strings.Join([]string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}, "\n")
	body := renderToolBody(defaultStyles(), "Read", output, true, ToolSuccess, 80)
	plain := stripANSIForTest(body)
	if strings.Contains(plain, "expand for details") {
		t.Fatalf("expanded output should not include expand hint: %q", plain)
	}
	if !strings.Contains(plain, "nine") {
		t.Fatalf("expanded output should include all lines: %q", plain)
	}
}

func TestAlignStatusLineRightAlignsModel(t *testing.T) {
	line := alignStatusLine(DefaultDisplayCellProfile(), "left", "model", 20)
	if len(line) != 20 {
		t.Fatalf("expected width 20, got %d: %q", len(line), line)
	}
	if !strings.HasSuffix(line, "model") {
		t.Fatalf("expected right suffix, got %q", line)
	}
}

func TestThinkingMessageCollapsesAfterFinish(t *testing.T) {
	chat := NewChatView(defaultStyles())
	chat.SetSize(100, 20)
	chat.StreamThinkingDelta("Looking at the code and deciding what to do next.")
	live := stripANSIForTest(chat.Render(100, 20))
	if !strings.Contains(live, "Thinking...") || !strings.Contains(live, "Looking at the code") {
		t.Fatalf("expected expanded live thinking, got %q", live)
	}
	chat.FinishThinking()
	collapsed := stripANSIForTest(chat.Render(100, 20))
	if !strings.Contains(collapsed, "Thinking (") || strings.Contains(collapsed, "Looking at the code") {
		t.Fatalf("expected collapsed thinking summary, got %q", collapsed)
	}
	chat.ToggleExpand()
	expanded := stripANSIForTest(chat.Render(100, 20))
	if !strings.Contains(expanded, "Looking at the code") || !strings.Contains(expanded, "expand for details") {
		t.Fatalf("expected expanded finished thinking, got %q", expanded)
	}
}

func TestCommandModeShowsHintsInEditor(t *testing.T) {
	app := New(Config{Model: "test-model", Resumed: true})
	app.width = 100
	app.height = 30
	app.updateLayout()
	app.inputMode = InputCommand
	app.textarea.SetValue("/h")
	app.updateCommandHints()
	view := stripANSIForTest(app.renderView())
	if !strings.Contains(view, "/help") || !strings.Contains(view, "List available commands") {
		t.Fatalf("expected command hint popup in view, got %q", view)
	}
}

func TestTogglePlanModeUpdatesEngine(t *testing.T) {
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{CWD: t.TempDir()})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng, Model: "test-model"})
	app.state = StateChat
	app.togglePlanMode() // default → plan
	if app.permMode != permission.ModePlan || eng.PermissionMode() != permission.ModePlan {
		t.Fatalf("expected plan mode on, app=%v engine=%q", app.permMode, eng.PermissionMode())
	}
	app.togglePlanMode() // plan → confirmation, Plan remains active
	if !app.hasDialog(StateBypassConfirm) ||
		app.permissionMode() != permission.ModePlan ||
		eng.PermissionMode() != permission.ModePlan {
		t.Fatalf("expected Plan-preserving bypass confirmation, dialogs=%v app=%v engine=%q", app.dialogs, app.permissionMode(), eng.PermissionMode())
	}
	app.handleBypassConfirmKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if app.hasDialog(StateBypassConfirm) ||
		app.permissionMode() != permission.ModePlan ||
		eng.PermissionMode() != permission.ModePlan {
		t.Fatalf("cancel changed Plan mode, dialogs=%v app=%v engine=%q", app.dialogs, app.permissionMode(), eng.PermissionMode())
	}

	app.togglePlanMode()
	app.bypassConfirmIdx = 1
	app.resolveBypassConfirm()
	if app.permissionMode() != permission.ModeBypassPermissions ||
		eng.PermissionMode() != permission.ModeBypassPermissions ||
		eng.PlanState().Phase != engine.PlanPhaseInactive {
		t.Fatalf("confirmed bypass did not leave Plan, app=%v engine=%q state=%#v", app.permissionMode(), eng.PermissionMode(), eng.PlanState())
	}
	app.togglePlanMode() // bypassPermissions → default
	if app.permissionMode() != permission.ModeDefault ||
		eng.PermissionMode() != permission.ModeDefault {
		t.Fatalf("expected default mode, app=%v engine=%q", app.permissionMode(), eng.PermissionMode())
	}
}

func TestEnginePlanModeInitializesAndDrivesHeader(t *testing.T) {
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{CWD: t.TempDir(), PermissionMode: permission.ModePlan})
	app := New(Config{Engine: eng, Model: "test-model", Resumed: true})
	if app.permMode != permission.ModePlan {
		t.Fatalf("initial TUI mode = %q, want plan", app.permMode)
	}
	if header := stripANSIForTest(app.renderHeader()); !strings.Contains(header, "[PLAN]") {
		t.Fatalf("plan mode missing from header: %q", header)
	}

	eng.SetPermissionMode(permission.ModeDefault)
	if header := stripANSIForTest(app.renderHeader()); strings.Contains(header, "[PLAN]") {
		t.Fatalf("header did not derive mode from engine: %q", header)
	}
}

func TestEngineAutoModeInitializesAndDrivesVisibleModeLabels(t *testing.T) {
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		CWD:            t.TempDir(),
		PermissionMode: permission.ModeAuto,
	})
	app := New(Config{Engine: eng, Model: "test-model", Resumed: true})
	if app.permissionMode() != permission.ModeAuto {
		t.Fatalf("initial TUI mode = %q, want auto", app.permissionMode())
	}
	if got := app.welcomeMode(); got != "auto permissions" {
		t.Fatalf("welcome mode = %q, want auto permissions", got)
	}
	if status := stripANSIForTest(app.renderStatus()); !strings.Contains(status, "● auto") {
		t.Fatalf("auto mode missing from status: %q", status)
	}
}

func TestP170PlanStateEventProjectsWithoutSecondEngineMutation(t *testing.T) {
	app := New(Config{Model: "test-model", Resumed: true})
	app.permMode = permission.ModeDefault
	app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventPlanStateTransition,
		PlanStateTransition: &engine.PlanStateTransitionEvent{
			FromPhase:      engine.PlanPhaseInactive,
			Phase:          engine.PlanPhaseActive,
			PermissionMode: permission.ModePlan,
			RequestID:      "enter",
			Revision:       1,
		},
	})
	if app.permMode != permission.ModePlan {
		t.Fatalf("Plan event projection mode = %q", app.permMode)
	}
}

func TestShiftTabIgnoredWhileQueryRunning(t *testing.T) {
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{CWD: t.TempDir(), PermissionMode: permission.ModeDefault})
	app := New(Config{Engine: eng, Model: "test-model", Resumed: true})
	app.running = true

	app.handleKey(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})

	if app.permissionMode() != permission.ModeDefault || eng.PermissionMode() != permission.ModeDefault {
		t.Fatalf("Shift-Tab changed mode while running: app=%q engine=%q", app.permissionMode(), eng.PermissionMode())
	}
}

func TestShellResultUpdatesBashTool(t *testing.T) {
	app := New(Config{Model: "test-model"})
	app.chat.SetSize(100, 20)
	app.chat.AppendToolStart("shell_1", "Bash", `{"command":"echo hi"}`)
	_, _ = app.Update(shellResultMsg{toolID: "shell_1", result: "hi"})
	view := stripANSIForTest(app.chat.Render(100, 20))
	if !strings.Contains(view, "Bash") || !strings.Contains(view, "echo hi") || !strings.Contains(view, "⎿") {
		t.Fatalf("expected shell tool result, got %q", view)
	}
}

func TestBareSlashCommandModeDoesNotPanic(t *testing.T) {
	app := New(Config{Model: "test-model"})
	app.inputMode = InputCommand
	app.textarea.SetValue("/")
	app.updateCommandHints()
	if len(app.commandHints) == 0 {
		t.Fatal("expected bare slash to show command hints")
	}
}

func TestTaskProgressEventUpdatesTaskTree(t *testing.T) {
	app := New(Config{Model: "test-model", Resumed: true})
	app.width = 100
	app.height = 30
	app.running = true
	app.updateLayout()

	err := app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventTaskProgress,
		TaskProgress: &engine.TaskProgressEvent{
			Type:         "system",
			Subtype:      "task_progress",
			TaskID:       "agent-1234567890",
			ToolUseID:    "toolu-1",
			Description:  "Investigate flaky tests",
			LastToolName: "Grep",
			Usage:        engine.TaskProgressUsage{ToolUses: 2, TotalTokens: 512, DurationMS: 1500},
		},
	})
	if err != nil {
		t.Fatal(err)
		return
	}

	plain := stripANSIForTest(app.renderView())
	if !strings.Contains(plain, "Investigate flaky tests") || !strings.Contains(plain, "Grep") {
		t.Fatalf("expected visible task progress with description and last tool, got %q", plain)
	}
}

func TestTaskProgressTreeFallsBackToUsageWhenLastToolMissing(t *testing.T) {
	app := New(Config{Model: "test-model"})
	app.width = 100
	app.height = 30
	app.running = true
	app.updateLayout()

	err := app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventTaskProgress,
		TaskProgress: &engine.TaskProgressEvent{
			Type:        "system",
			Subtype:     "task_progress",
			TaskID:      "agent-usage",
			Description: "Summarize logs",
			Usage:       engine.TaskProgressUsage{ToolUses: 3, TotalTokens: 1200, DurationMS: 2000},
		},
	})
	if err != nil {
		t.Fatal(err)
		return
	}

	plain := stripANSIForTest(app.renderTaskTree())
	if !strings.Contains(plain, "Summarize logs") || !strings.Contains(plain, "(3 tools)") {
		t.Fatalf("expected task tree usage fallback, got %q", plain)
	}
}

func TestTaskProgressClearsOnTerminalInterruptionAndMaxTurns(t *testing.T) {
	for _, tc := range []struct {
		name string
		evt  engine.QueryEvent
	}{
		{name: "terminal", evt: engine.QueryEvent{Type: engine.EventTerminal}},
		{name: "interruption", evt: engine.QueryEvent{Type: engine.EventUserInterruption}},
		{name: "max turns", evt: engine.QueryEvent{Type: engine.EventMaxTurnsReached, MaxTurnsInfo: &engine.MaxTurnsInfo{TurnCount: 3, MaxTurns: 3}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := New(Config{Model: "test-model"})
			app.activeTasks["agent-stale"] = &taskEntry{taskID: "agent-stale", description: "stale agent"}
			app.running = true

			if err := app.handleEngineEvent(tc.evt); err != nil {
				t.Fatal(err)
				return
			}
			if len(app.activeTasks) != 0 {
				t.Fatalf("expected active tasks cleared after %s, got %#v", tc.name, app.activeTasks)
			}
		})
	}
}

func TestTaskLifecycleEventRendersLocalTaskListEntry(t *testing.T) {
	app := New(Config{Model: "test-model"})
	app.width = 100
	app.height = 30
	app.running = true
	app.updateLayout()

	if err := app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventTaskLifecycle,
		TaskLifecycle: &engine.TaskLifecycleEvent{
			Phase:      "created",
			TaskID:     "1",
			Subject:    "Add focused tests",
			ActiveForm: "Writing focused tests",
			Status:     "pending",
		},
	}); err != nil {
		t.Fatal(err)
		return
	}
	plain := stripANSIForTest(app.renderTaskTree())
	if !strings.Contains(plain, "Add focused tests") || !strings.Contains(plain, "[pending]") {
		t.Fatalf("expected pending local task entry, got %q", plain)
	}

	if err := app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventTaskLifecycle,
		TaskLifecycle: &engine.TaskLifecycleEvent{
			Phase:      "updated",
			TaskID:     "1",
			Subject:    "Add focused tests",
			ActiveForm: "Writing focused tests",
			Status:     "in_progress",
		},
	}); err != nil {
		t.Fatal(err)
		return
	}
	plain = stripANSIForTest(app.renderTaskTree())
	if !strings.Contains(plain, "Writing focused tests") || !strings.Contains(plain, "[in_progress]") {
		t.Fatalf("expected active-form in-progress local task entry, got %q", plain)
	}

	if err := app.handleEngineEvent(engine.QueryEvent{Type: engine.EventTerminal}); err != nil {
		t.Fatal(err)
		return
	}
	if len(app.activeTasks) != 0 {
		t.Fatalf("expected terminal cleanup of local task entries, got %#v", app.activeTasks)
	}
}

func TestTaskPanelCtrlTRendersTaskExplorerSnapshot(t *testing.T) {
	app, _ := newTaskPanelTestApp(t, 80, 24)
	app.taskExplorer.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
		return engine.TaskExplorerSnapshot{
			Available: true,
			WorkItems: []engine.TaskExplorerWorkItem{{
				BoardID:    "board",
				WorkItemID: "work",
				Title:      "Inspect logical work",
				Status:     "pending",
			}},
		}
	})

	_, _ = app.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})

	if app.state != StateTaskPanel {
		t.Fatalf("expected ctrl+t to open task panel, state=%v", app.state)
	}
	plain := stripANSIForTest(app.renderTaskPanel())
	if !strings.Contains(plain, "Inspect logical work") {
		t.Fatalf("expected task panel to render explorer work, got %q", plain)
	}
}

func TestTaskPanelPreservesCanonicalSelectorOrderWithinBudget(t *testing.T) {
	app, _ := newTaskPanelTestApp(t, 80, 10)
	snapshot := p313ExplorerSnapshot(
		engine.TaskExplorerWorkItem{BoardID: "board", WorkItemID: "1", Title: "active task", Status: "in_progress"},
		engine.TaskExplorerWorkItem{BoardID: "board", WorkItemID: "2", Title: "pending task", Status: "pending"},
		engine.TaskExplorerWorkItem{BoardID: "board", WorkItemID: "3", Title: "recent completed", Status: "completed"},
		engine.TaskExplorerWorkItem{BoardID: "board", WorkItemID: "4", Title: "older completed", Status: "completed"},
	)
	app.taskExplorer.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
		return snapshot
	})

	plain := stripANSIForTest(app.buildTaskPanelViewForTest())
	assertContainsInOrder(t, plain, "active task", "pending task", "recent completed")
	if strings.Contains(plain, "older completed") {
		t.Fatalf("expected older completed task hidden when visible budget is exceeded, got %q", plain)
	}
}

func TestTaskPanelHiddenSummaryWhenTasksExceedVisibleBudget(t *testing.T) {
	app, _ := newTaskPanelTestApp(t, 80, 17)
	snapshot := p313ExplorerSnapshot(
		engine.TaskExplorerWorkItem{BoardID: "board", WorkItemID: "1", Title: "active task", Status: "in_progress"},
		engine.TaskExplorerWorkItem{BoardID: "board", WorkItemID: "2", Title: "pending task", Status: "pending"},
		engine.TaskExplorerWorkItem{BoardID: "board", WorkItemID: "3", Title: "visible completed", Status: "completed"},
	)
	snapshot.Hidden.WorkItems = map[string]int{
		"pending": 1, "completed": 1,
	}
	app.taskExplorer.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
		return snapshot
	})

	plain := stripANSIForTest(app.buildTaskPanelViewForTest())
	if !strings.Contains(plain, "hidden 2") {
		t.Fatalf("expected hidden summary with hidden task counts, got %q", plain)
	}
}

func TestTaskPanelScrollAndCloseKeysRemainStable(t *testing.T) {
	app, _ := newTaskPanelTestApp(t, 80, 12)
	snapshot := p313ExplorerSnapshot()
	for i := 1; i <= 8; i++ {
		id := string(rune('0' + i))
		snapshot.WorkItems = append(snapshot.WorkItems, engine.TaskExplorerWorkItem{
			BoardID: "board", WorkItemID: id, Title: "task " + id, Status: "pending",
		})
	}
	app.taskExplorer.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
		return snapshot
	})
	app.enterTaskPanel()

	app.handleTaskPanelKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if app.taskExplorer.cursor != 1 {
		t.Fatalf("expected down to increment explorer cursor, got %d", app.taskExplorer.cursor)
	}
	app.handleTaskPanelKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if app.taskExplorer.cursor != 0 {
		t.Fatalf("expected up to decrement explorer cursor, got %d", app.taskExplorer.cursor)
	}
	app.handleTaskPanelKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if app.state != StateChat {
		t.Fatalf("expected ctrl+t to close task panel, state=%v", app.state)
	}
}

func TestTaskPanelLongNarrowRenderingTruncatesSafely(t *testing.T) {
	app, _ := newTaskPanelTestApp(t, 12, 12)
	long := strings.Repeat("very-long-subject-", 8) + "终端"
	snapshot := p313ExplorerSnapshot(engine.TaskExplorerWorkItem{
		BoardID: "board", WorkItemID: "1", Title: long, Status: "in_progress",
	})
	app.taskExplorer.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
		return snapshot
	})

	plain := stripANSIForTest(app.buildTaskPanelViewForTest())
	if !strings.Contains(plain, "...") {
		t.Fatalf("expected narrow task panel to truncate long content safely, got %q", plain)
	}
}

func TestTaskAgentViewsConvergeOnCanonicalRuntimeSelector(t *testing.T) {
	runtimeState := engine.NewRuntimeStateStore()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	progress := engine.QueryEvent{
		RuntimeEventEnvelope: engine.RuntimeEventEnvelope{
			SessionID:       "agent-session",
			ThreadID:        "agent-thread",
			TurnID:          "agent-turn",
			AgentID:         "agent-selector",
			ParentSessionID: "leader-session",
			ParentThreadID:  "leader-thread",
			ParentToolUseID: "spawn-selector",
			Sequence:        1,
			Timestamp:       now,
		},
		Type: engine.EventTaskProgress,
		TaskProgress: &engine.TaskProgressEvent{
			Type:             "system",
			Subtype:          "task_progress",
			TaskID:           "agent-selector",
			ToolUseID:        "spawn-selector",
			Description:      "Inspect selector convergence",
			Usage:            engine.TaskProgressUsage{ToolUses: 2, TotalTokens: 512},
			LastToolName:     "Read",
			Summary:          "Reading selector.go",
			RecentActivities: []engine.TaskProgressActivity{{ToolName: "Read", Description: "Reading selector.go", IsRead: true}},
		},
	}
	if err := runtimeState.Apply(progress); err != nil {
		t.Fatalf("apply progress: %v", err)
	}
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:    "leader-session",
		ThreadID:     "leader-thread",
		CWD:          t.TempDir(),
		RuntimeState: runtimeState,
	})
	defer eng.Close()
	app := New(Config{Engine: eng, Model: "test-model", Resumed: true})
	app.width = 100
	app.height = 30
	app.updateLayout()
	app.activeTasks["stale-local-row"] = &taskEntry{taskID: "stale-local-row", description: "must not render"}
	explorer := p313ExplorerSnapshot(engine.TaskExplorerWorkItem{
		BoardID: "board", WorkItemID: "selector-work", Status: "in_progress",
		Title: "Inspect selector convergence", ActiveForm: "Reading selector.go",
	})
	explorer.Executions = []engine.TaskExplorerExecution{
		p313Execution(
			"agent-selector",
			1,
			engine.TaskExplorerExecutionRunning,
		),
	}
	explorer.Executions[0].Activity = "Reading selector.go"
	app.taskExplorer.SetSnapshotProvider(
		func() engine.TaskExplorerSnapshot { return explorer },
	)
	app.backgroundTasks.SetExplorerSnapshotProvider(
		func() engine.TaskExplorerSnapshot { return explorer },
	)
	app.teamsPanel.SetExplorerSnapshotProvider(
		func() engine.TaskExplorerSnapshot { return explorer },
	)

	app.backgroundTasks.Refresh()
	if len(app.backgroundTasks.items) != 2 {
		t.Fatalf("Ctrl+B rows = %#v", app.backgroundTasks.items)
	}
	var background bgTaskItem
	for _, item := range app.backgroundTasks.items {
		if item.kind == "agent" {
			background = item
			break
		}
	}
	if background.id != "agent-selector" ||
		background.status != "running" ||
		background.summary != "Reading selector.go" {
		t.Fatalf("Ctrl+B did not use canonical row: %#v", background)
	}
	app.teamsPanel.Refresh()
	if len(app.teamsPanel.items) != 1 ||
		app.teamsPanel.items[0].status != "running" ||
		app.teamsPanel.items[0].summary != "Reading selector.go" {
		t.Fatalf("/team did not use canonical row: %#v", app.teamsPanel.items)
	}
	app.enterTaskPanel()
	taskPanel := stripANSIForTest(app.renderTaskPanel())
	if !strings.Contains(taskPanel, "Inspect selector convergence") ||
		!strings.Contains(taskPanel, "Reading selector.go") {
		t.Fatalf("Ctrl+T did not use canonical row: %q", taskPanel)
	}
	inline := stripANSIForTest(app.renderTaskTree())
	if !strings.Contains(inline, "Reading selector.go") ||
		!strings.Contains(inline, "inspect agent-selector") ||
		strings.Contains(inline, "must not render") {
		t.Fatalf("inline status did not use canonical row: %q", inline)
	}
	if app.backgroundTaskCount() != 1 {
		t.Fatalf("canonical background count = %d, want 1", app.backgroundTaskCount())
	}

	terminal := progress
	terminal.Type = engine.EventTerminal
	terminal.Sequence = 2
	terminal.Timestamp = now.Add(time.Second)
	terminal.TaskProgress = nil
	terminal.TerminalInfo = &engine.Terminal{Reason: engine.TerminalCompleted}
	if err := runtimeState.Apply(terminal); err != nil {
		t.Fatalf("apply terminal: %v", err)
	}
	explorer.WorkItems[0].Status = "completed"
	explorer.Executions[0].Status = "completed"
	explorer.Executions[0].Phase = engine.TaskExplorerExecutionCompleted
	app.backgroundTasks.Refresh()
	app.teamsPanel.Refresh()
	if app.backgroundTasks.items[0].status != "completed" || app.teamsPanel.items[0].status != "completed" {
		t.Fatalf("terminal rows diverged: background=%#v team=%#v", app.backgroundTasks.items[0], app.teamsPanel.items[0])
	}
	if inline := stripANSIForTest(app.renderTaskTree()); inline != "" {
		t.Fatalf("terminal canonical row remained inline: %q", inline)
	}
	if app.backgroundTaskCount() != 0 {
		t.Fatalf("terminal canonical background count = %d, want 0", app.backgroundTaskCount())
	}
}

func newTaskPanelTestApp(t *testing.T, width, height int) (*App, *engine.QueryEngine) {
	t.Helper()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{CWD: t.TempDir()})
	app := New(Config{Engine: eng, Model: "test-model", Resumed: true})
	app.width = width
	app.height = height
	app.updateLayout()
	return app, eng
}

func (a *App) buildTaskPanelViewForTest() string {
	a.enterTaskPanel()
	return a.renderTaskPanel()
}

func assertContainsInOrder(t *testing.T, s string, parts ...string) {
	t.Helper()
	pos := 0
	for _, part := range parts {
		idx := strings.Index(s[pos:], part)
		if idx < 0 {
			t.Fatalf("expected %q after offset %d in %q", part, pos, s)
		}
		pos += idx + len(part)
	}
}
