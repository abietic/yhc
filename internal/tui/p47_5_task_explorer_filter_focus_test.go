package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/terminalcap"
)

func TestP475FilterTruthAndSearchComposeWithoutMutatingSnapshot(t *testing.T) {
	snapshot := p475FilterSnapshot()
	wantSnapshot := p475FilterSnapshot()
	panel := p475OpenPanel(t, &snapshot, 80, 24)

	assertP475RowKeys(t, panel, p475AllRowKeys())
	panel.HandleKey(p475Key('f'), 24)
	assertP475RowKeys(t, panel, []string{
		"work:work-active",
		"exec:exec-running@g1",
		"exec:exec-waiting@g1",
		"exec:exec-paused@g1",
		"exec:exec-attention@g1",
	})
	if frame := stripANSIForTest(panel.Render(80, 24)); !strings.Contains(frame, "[active]") ||
		!strings.Contains(frame, "local hidden 11") {
		t.Fatalf("active filter state missing:\n%s", frame)
	}

	panel.HandleKey(p475Key('f'), 24)
	assertP475RowKeys(t, panel, []string{
		"work:work-attention",
		"exec:exec-attention@g1",
	})
	panel.HandleKey(p475Key('f'), 24)
	assertP475RowKeys(t, panel, []string{
		"work:work-completed",
		"work:work-failed",
		"work:work-cancelled",
		"exec:exec-completed@g1",
		"exec:exec-failed@g1",
		"exec:exec-cancelled@g1",
	})
	panel.HandleKey(p475Key('f'), 24)
	assertP475RowKeys(t, panel, p475AllRowKeys())

	panel.HandleKey(p475Key('f'), 24)
	panel.HandleKey(p475Key('/'), 24)
	panel.HandleKey(
		tea.KeyPressMsg{Code: tea.KeyExtended, Text: "waiting marker"},
		24,
	)
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, 24)
	assertP475RowKeys(t, panel, []string{"exec:exec-waiting@g1"})
	frame := stripANSIForTest(panel.Render(80, 24))
	for _, want := range []string{
		"Focus: list", "[active]", "Search: /waiting marker",
		"local hidden 15",
	} {
		if !strings.Contains(frame, want) {
			t.Fatalf("composed filter/search omitted %q:\n%s", want, frame)
		}
	}
	if !reflect.DeepEqual(snapshot, wantSnapshot) {
		t.Fatalf("local filter mutated source snapshot:\n got: %#v\nwant: %#v", snapshot, wantSnapshot)
	}
}

func TestP475FilterPreservesExactIdentityAndEmptyReset(t *testing.T) {
	snapshot := p475FilterSnapshot()
	panel := p475OpenPanel(t, &snapshot, 80, 24)
	p475SelectRow(t, panel, "exec:exec-waiting@g1")
	panel.HandleKey(p475Key('f'), 24)
	if panel.selection.agentID != "exec-waiting" || panel.selection.generation != 1 {
		t.Fatalf("active filter retargeted surviving execution: %#v", panel.selection)
	}

	snapshot.Executions = []engine.TaskExplorerExecution{
		p475Execution("exec-paused", engine.TaskExplorerExecutionPaused, nil),
		p475Execution("exec-waiting", engine.TaskExplorerExecutionWaitingInput, nil),
	}
	snapshot.WorkItems = []engine.TaskExplorerWorkItem{
		{BoardID: "board", WorkItemID: "work-active", Status: "in_progress", Title: "active work"},
	}
	panel.Refresh()
	if panel.selection.agentID != "exec-waiting" || panel.selection.generation != 1 {
		t.Fatalf("refresh retargeted exact execution: %#v", panel.selection)
	}
	if frame := stripANSIForTest(panel.Render(80, 24)); !strings.Contains(frame, "[active]") {
		t.Fatalf("refresh lost local filter:\n%s", frame)
	}

	panel.detail = true
	panel.offset = 2
	snapshot.WorkItems = []engine.TaskExplorerWorkItem{{
		BoardID: "board", WorkItemID: "pending", Status: "pending", Title: "pending only",
	}}
	snapshot.Executions = []engine.TaskExplorerExecution{
		p475Execution("replay", engine.TaskExplorerExecutionReplayOnly, nil),
	}
	panel.Refresh()
	if panel.cursor != -1 || panel.selection.valid() || panel.detail || panel.offset != 0 {
		t.Fatalf(
			"empty active filter retained state: cursor=%d selection=%#v detail=%v offset=%d",
			panel.cursor,
			panel.selection,
			panel.detail,
			panel.offset,
		)
	}
	frame := stripANSIForTest(panel.Render(80, 24))
	if !strings.Contains(frame, "No rows match active filter") ||
		!strings.Contains(frame, "local hidden 2") {
		t.Fatalf("empty filter facts missing:\n%s", frame)
	}
}

func TestP475FocusCycleSearchAndPromptPriority(t *testing.T) {
	snapshot := p475FocusSnapshot()
	panel := p475OpenPanel(t, &snapshot, 80, 24)
	assertP475FrameContains(t, panel, "Focus: list", "Filter: [all]", "f filter", "/ search", "Tab focus")

	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab}, 24)
	if !panel.detail {
		t.Fatal("Tab from list did not open current detail")
	}
	assertP475FrameContains(t, panel, "Focus: detail")

	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, 24)
	if panel.detail {
		t.Fatal("Shift+Tab from detail did not return to list")
	}
	assertP475FrameContains(t, panel, "Focus: list")

	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, 24)
	assertP475FrameContains(t, panel, "Focus: controls")
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab}, 24)
	assertP475FrameContains(t, panel, "Focus: list")

	panel.HandleKey(p475Key('/'), 24)
	assertP475FrameContains(t, panel, "Focus: controls", "Search: editing")
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: "work"}, 24)
	assertP475FrameContains(t, panel, "Search input: /work")
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, 24)
	assertP475FrameContains(t, panel, "Focus: list", "Search: /work")

	panel.search = ""
	panel.refilter()
	p475SelectRow(t, panel, "exec:exec-action@g1")
	panel.HandleKey(p475Key('s'), 24)
	before := stripANSIForTest(panel.Render(80, 24))
	selectionBeforeMouse := panel.selection
	panel.HandleMouse(tuiMouseMsg{
		X: 47, Y: 2, Button: tea.MouseLeft, Action: mouseActionPress,
	})
	if panel.filter != taskExplorerFilterAll ||
		panel.selection != selectionBeforeMouse ||
		panel.actionPrompt != taskExplorerActionPromptInput {
		t.Fatalf(
			"action prompt lost mouse ownership: filter=%s selection=%#v prompt=%d",
			panel.filter,
			panel.selection,
			panel.actionPrompt,
		)
	}
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab}, 24)
	after := stripANSIForTest(panel.Render(80, 24))
	if !strings.Contains(before, "send exec-action@g1>") ||
		!strings.Contains(after, "send exec-action@g1>") ||
		!strings.Contains(after, "Focus: list") {
		t.Fatalf("action prompt did not retain keyboard ownership:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc}, 24)

	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, 24)
	if dismissed := panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc}, 24); dismissed {
		t.Fatal("Esc from controls dismissed panel instead of returning to list")
	}
	assertP475FrameContains(t, panel, "Focus: list")
	if dismissed := panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc}, 24); !dismissed {
		t.Fatal("Esc from list did not dismiss panel")
	}
}

func TestP475HintsStateDisabledActionsWithoutDispatch(t *testing.T) {
	snapshot := p475FocusSnapshot()
	panel := p475OpenPanel(t, &snapshot, 80, 24)
	requests := make([]engine.TaskExplorerActionRequest, 0, 1)
	panel.SetActionProvider(func(request engine.TaskExplorerActionRequest) engine.TaskExplorerActionResult {
		requests = append(requests, request)
		return engine.TaskExplorerActionResult{
			RequestID: request.RequestID, BoardID: request.BoardID,
			BoardRevision: request.BoardRevision, RuntimeRevision: request.RuntimeRevision,
			AgentID: request.AgentID, Generation: request.Generation,
			Action: request.Action, Conflict: "test_unavailable",
		}
	})

	assertP475FrameContains(
		t,
		panel,
		"x switch disabled",
		"s send disabled",
		"p pause/resume disabled",
		"c cancel disabled",
		"n continue disabled",
	)
	for _, key := range []rune{'x', 's', 'p', 'c', 'n'} {
		panel.HandleKey(p475Key(key), 24)
	}
	if len(requests) != 0 {
		t.Fatalf("disabled WorkItem actions dispatched: %+v", requests)
	}

	p475SelectRow(t, panel, "exec:exec-action@g1")
	assertP475FrameContains(t, panel, "x switch", "s send", "c cancel disabled")
	panel.HandleKey(p475Key('x'), 24)
	if len(requests) != 1 || requests[0].AgentID != "exec-action" ||
		requests[0].Generation != 1 ||
		requests[0].Action != engine.TaskExplorerActionSwitch {
		t.Fatalf("exact enabled request = %+v", requests)
	}
}

func TestP475ResponsiveNoColorShowsFocusFilterAndHints(t *testing.T) {
	capabilities := interactiveTerminalCaps()
	capabilities.Color = terminalcap.ColorNone
	app := New(Config{
		Resumed: true, ReducedMotion: true, TerminalCaps: &capabilities,
	})
	snapshot := p475FocusSnapshot()
	app.taskExplorer.SetSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	app.enterTaskPanel()

	for _, size := range []struct{ width, height int }{
		{40, 20}, {80, 24}, {120, 30}, {180, 30},
	} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			app.width, app.height = size.width, size.height
			app.updateLayout()
			frame := app.renderView()
			if strings.Contains(frame, "\x1b") {
				t.Fatalf("no-color frame contains ANSI: %q", frame)
			}
			for _, want := range []string{"Focus", "[all]", "f filter", "/ search", "Tab focus"} {
				if !strings.Contains(frame, want) {
					t.Fatalf("%dx%d omitted %q:\n%s", size.width, size.height, want, frame)
				}
			}
			assertP313ExplorerFrame(t, frame, size.width, size.height, "WorkItem")
		})
	}
}

func TestP475TaskPanelMouseUsesPanelSelectionAndNeverLeaksToChat(t *testing.T) {
	snapshot := p475FocusSnapshot()
	app := p475OpenApp(t, &snapshot, 80, 24)
	actionRequests := 0
	app.taskExplorer.SetActionProvider(func(
		engine.TaskExplorerActionRequest,
	) engine.TaskExplorerActionResult {
		actionRequests++
		return engine.TaskExplorerActionResult{}
	})
	for index := 0; index < 30; index++ {
		app.chat.AppendSystem(fmt.Sprintf("chat row %d", index))
	}
	app.chat.Render(app.layout.chatRect.Width, app.layout.chatRect.Height)
	app.chat.ScrollUp(5)
	chatIndex, chatLine := app.chat.offsetIdx, app.chat.offsetLine
	_ = app.renderView()

	updateAppSilent(app, tuiMouseMsg{
		X:      app.layout.overlayRect.X + 2,
		Y:      app.layout.overlayRect.Y + 5,
		Button: tea.MouseLeft,
		Action: mouseActionPress,
	})
	if panel := app.taskExplorer; panel.selection.agentID != "exec-action" || panel.selection.generation != 1 {
		t.Fatalf("list click selection = %#v", panel.selection)
	}
	if app.chat.offsetIdx != chatIndex || app.chat.offsetLine != chatLine || app.selection.HasSelection() {
		t.Fatalf(
			"TaskPanel click leaked to chat: offset=(%d,%d) selection=%v",
			app.chat.offsetIdx,
			app.chat.offsetLine,
			app.selection.HasSelection(),
		)
	}
	_ = app.renderView()
	detail := app.taskExplorer.geometry.detail
	updateAppSilent(app, tuiMouseMsg{
		X:      app.layout.overlayRect.X + detail.X,
		Y:      app.layout.overlayRect.Y + detail.Y,
		Button: tea.MouseLeft,
		Action: mouseActionPress,
	})
	if app.taskExplorer.focus != taskExplorerFocusDetail ||
		actionRequests != 0 {
		t.Fatalf(
			"detail click focus=%s action requests=%d",
			app.taskExplorer.focus,
			actionRequests,
		)
	}
}

func TestP475TaskPanelMouseFilterSearchAndWheelMatchKeyboard(t *testing.T) {
	snapshot := p475FilterSnapshot()
	app := p475OpenApp(t, &snapshot, 80, 24)
	_ = app.renderView()

	updateAppSilent(app, tuiMouseMsg{
		X:      app.layout.overlayRect.X + 47,
		Y:      app.layout.overlayRect.Y + 2,
		Button: tea.MouseLeft,
		Action: mouseActionPress,
	})
	assertP475RowKeys(t, app.taskExplorer, []string{
		"work:work-completed",
		"work:work-failed",
		"work:work-cancelled",
		"exec:exec-completed@g1",
		"exec:exec-failed@g1",
		"exec:exec-cancelled@g1",
	})
	assertP475FrameContains(t, app.taskExplorer, "Focus: controls", "[terminal]")
	_ = app.renderView()

	updateAppSilent(app, tuiMouseMsg{
		X:      app.layout.overlayRect.X + 65,
		Y:      app.layout.overlayRect.Y + 2,
		Button: tea.MouseLeft,
		Action: mouseActionPress,
	})
	app.handleTaskPanelKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: "failed marker"})
	app.handleTaskPanelKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	assertP475RowKeys(t, app.taskExplorer, []string{"exec:exec-failed@g1"})

	longSnapshot := p313ExplorerSnapshot()
	for index := 0; index < 20; index++ {
		longSnapshot.WorkItems = append(longSnapshot.WorkItems, engine.TaskExplorerWorkItem{
			BoardID: "board", WorkItemID: fmt.Sprintf("work-%02d", index),
			Status: "pending", Title: fmt.Sprintf("work %02d", index),
		})
	}
	wheelApp := p475OpenApp(t, &longSnapshot, 80, 14)
	_ = wheelApp.renderView()
	updateAppSilent(wheelApp, tuiMouseMsg{
		X:      wheelApp.layout.overlayRect.X + 2,
		Y:      wheelApp.layout.overlayRect.Y + 4,
		Button: tea.MouseWheelDown,
		Action: mouseActionPress,
	})

	keyboardApp := p475OpenApp(t, &longSnapshot, 80, 14)
	for range 3 {
		keyboardApp.handleTaskPanelKey(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if wheelApp.taskExplorer.selection != keyboardApp.taskExplorer.selection ||
		wheelApp.taskExplorer.cursor != keyboardApp.taskExplorer.cursor {
		t.Fatalf(
			"wheel selection cursor=%d %#v, keyboard cursor=%d %#v",
			wheelApp.taskExplorer.cursor,
			wheelApp.taskExplorer.selection,
			keyboardApp.taskExplorer.cursor,
			keyboardApp.taskExplorer.selection,
		)
	}
}

func TestP475UnavailableAndStaleGeometryRejectPointerMutation(t *testing.T) {
	snapshot := p475FocusSnapshot()
	app := p475OpenApp(t, &snapshot, 80, 24)
	clickTerminal := func() {
		updateAppSilent(app, tuiMouseMsg{
			X:      app.layout.overlayRect.X + 47,
			Y:      app.layout.overlayRect.Y + 2,
			Button: tea.MouseLeft,
			Action: mouseActionPress,
		})
	}

	clickTerminal()
	if app.taskExplorer.filter != taskExplorerFilterAll {
		t.Fatalf("pre-render geometry changed filter to %s", app.taskExplorer.filter)
	}

	_ = app.renderView()
	snapshot = engine.TaskExplorerSnapshot{UnavailableReason: "test_unavailable"}
	app.taskExplorer.Refresh()
	clickTerminal()
	if app.taskExplorer.filter != taskExplorerFilterAll {
		t.Fatalf("stale geometry changed filter to %s", app.taskExplorer.filter)
	}

	_ = app.renderView()
	clickTerminal()
	if app.taskExplorer.filter != taskExplorerFilterAll ||
		app.taskExplorer.searchFocus {
		t.Fatalf(
			"unavailable geometry changed controls: filter=%s search=%v",
			app.taskExplorer.filter,
			app.taskExplorer.searchFocus,
		)
	}
}

func p475FilterSnapshot() engine.TaskExplorerSnapshot {
	snapshot := p313ExplorerSnapshot(
		engine.TaskExplorerWorkItem{BoardID: "board", WorkItemID: "work-active", Status: "in_progress", Title: "active work"},
		engine.TaskExplorerWorkItem{BoardID: "board", WorkItemID: "work-pending", Status: "pending", Title: "pending work"},
		engine.TaskExplorerWorkItem{BoardID: "board", WorkItemID: "work-attention", Status: "pending", Title: "attention work", Attention: []string{"blocked"}},
		engine.TaskExplorerWorkItem{BoardID: "board", WorkItemID: "work-completed", Status: "completed", Title: "completed work"},
		engine.TaskExplorerWorkItem{BoardID: "board", WorkItemID: "work-failed", Status: "failed", Title: "failed work"},
		engine.TaskExplorerWorkItem{BoardID: "board", WorkItemID: "work-cancelled", Status: "cancelled", Title: "cancelled work"},
		engine.TaskExplorerWorkItem{BoardID: "board", WorkItemID: "work-unknown", Status: "unknown", Title: "unknown work"},
	)
	snapshot.Hidden.WorkItems["overflow"] = 2
	snapshot.Hidden.Executions["overflow"] = 3
	snapshot.Executions = []engine.TaskExplorerExecution{
		p475Execution("exec-running", engine.TaskExplorerExecutionRunning, nil),
		p475Execution("exec-waiting", engine.TaskExplorerExecutionWaitingInput, nil),
		p475Execution("exec-paused", engine.TaskExplorerExecutionPaused, nil),
		p475Execution("exec-attention", engine.TaskExplorerExecutionRunning, []string{"needs_input"}),
		p475Execution("exec-replay", engine.TaskExplorerExecutionReplayOnly, nil),
		p475Execution("exec-completed", engine.TaskExplorerExecutionCompleted, nil),
		p475Execution("exec-failed", engine.TaskExplorerExecutionFailed, nil),
		p475Execution("exec-cancelled", engine.TaskExplorerExecutionCancelled, nil),
		p475Execution("exec-unknown", engine.TaskExplorerExecutionUnknown, nil),
	}
	snapshot.Executions[1].Name = "waiting marker"
	snapshot.Executions[6].Name = "failed marker"
	return snapshot
}

func p475FocusSnapshot() engine.TaskExplorerSnapshot {
	snapshot := p313ExplorerSnapshot(engine.TaskExplorerWorkItem{
		BoardID: "board", WorkItemID: "work", Status: "in_progress", Title: "focus work",
		AllowedActions: []engine.TaskExplorerAction{engine.TaskExplorerActionInspect},
	})
	execution := p475Execution("exec-action", engine.TaskExplorerExecutionRunning, nil)
	execution.AllowedActions = []engine.TaskExplorerAction{
		engine.TaskExplorerActionInspect,
		engine.TaskExplorerActionSwitch,
		engine.TaskExplorerActionSend,
	}
	snapshot.Executions = []engine.TaskExplorerExecution{execution}
	return snapshot
}

func p475Execution(
	agentID string,
	phase engine.TaskExplorerExecutionPhase,
	attention []string,
) engine.TaskExplorerExecution {
	execution := p313Execution(agentID, 1, phase)
	execution.Name = agentID + " marker"
	execution.Attention = attention
	return execution
}

func p475OpenApp(
	t *testing.T,
	snapshot *engine.TaskExplorerSnapshot,
	width, height int,
) *App {
	t.Helper()
	app := New(Config{Resumed: true})
	app.width, app.height = width, height
	app.updateLayout()
	app.taskExplorer.SetSnapshotProvider(func() engine.TaskExplorerSnapshot { return *snapshot })
	app.enterTaskPanel()
	return app
}

func p475OpenPanel(
	t *testing.T,
	snapshot *engine.TaskExplorerSnapshot,
	width, height int,
) *TaskExplorerPanel {
	t.Helper()
	return p475OpenApp(t, snapshot, width, height).taskExplorer
}

func p475Key(key rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: key, Text: string(key)}
}

func p475AllRowKeys() []string {
	return []string{
		"work:work-active",
		"work:work-pending",
		"work:work-attention",
		"work:work-completed",
		"work:work-failed",
		"work:work-cancelled",
		"work:work-unknown",
		"exec:exec-running@g1",
		"exec:exec-waiting@g1",
		"exec:exec-paused@g1",
		"exec:exec-attention@g1",
		"exec:exec-replay@g1",
		"exec:exec-completed@g1",
		"exec:exec-failed@g1",
		"exec:exec-cancelled@g1",
		"exec:exec-unknown@g1",
	}
}

func assertP475RowKeys(t *testing.T, panel *TaskExplorerPanel, want []string) {
	t.Helper()
	got := make([]string, 0, len(panel.rows))
	for _, row := range panel.rows {
		switch {
		case row.work != nil:
			got = append(got, "work:"+row.work.WorkItemID)
		case row.execution != nil:
			got = append(got, fmt.Sprintf(
				"exec:%s@g%d",
				row.execution.Key.AgentID,
				row.execution.Key.Generation,
			))
		default:
			got = append(got, "invalid")
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("row keys = %v, want %v", got, want)
	}
}

func p475SelectRow(t *testing.T, panel *TaskExplorerPanel, key string) {
	t.Helper()
	for index, row := range panel.rows {
		candidate := ""
		if row.work != nil {
			candidate = "work:" + row.work.WorkItemID
		} else if row.execution != nil {
			candidate = fmt.Sprintf(
				"exec:%s@g%d",
				row.execution.Key.AgentID,
				row.execution.Key.Generation,
			)
		}
		if candidate == key {
			panel.cursor = index
			panel.selection = row.selection
			return
		}
	}
	t.Fatalf("row %q not found", key)
}

func assertP475FrameContains(
	t *testing.T,
	panel *TaskExplorerPanel,
	wants ...string,
) {
	t.Helper()
	frame := stripANSIForTest(panel.Render(80, 24))
	for _, want := range wants {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame omitted %q:\n%s", want, frame)
		}
	}
}
