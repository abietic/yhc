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

func TestP476WorkItemDetailSeparatesOverviewFromExactCachedActivity(t *testing.T) {
	snapshot := p476DetailSnapshot()
	panel := p476OpenPanel(t, &snapshot, taskExplorerMixed, 180, 30)

	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab}, 30)
	overview := stripANSIForTest(panel.Render(180, 30))
	assertP476Contains(
		t,
		overview,
		"Detail · WorkItem",
		"Tabs: [overview] activity",
		"work-a",
		"overview description A",
	)
	assertP476Excludes(t, overview, "exact work diagnostic", "other-board-link")

	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}, 30)
	activity := stripANSIForTest(panel.Render(180, 30))
	assertP476Contains(
		t,
		activity,
		"Detail · WorkItem",
		"Tabs: overview [activity]",
		"agent-a@g1",
		"agent-a@g2",
		"exact-link-reason",
		"exact work attention",
		"exact work diagnostic",
	)
	assertP476Excludes(
		t,
		activity,
		"overview description A",
		"other-board-link",
		"other work attention",
		"global work attention",
		"other work diagnostic",
		"global work diagnostic",
	)

	panel.HandleKey(p476Key('h'), 30)
	if frame := stripANSIForTest(panel.Render(180, 30)); !strings.Contains(frame, "Tabs: [overview] activity") {
		t.Fatalf("h did not restore overview tab:\n%s", frame)
	}
	panel.HandleKey(p476Key('l'), 30)
	if frame := stripANSIForTest(panel.Render(180, 30)); !strings.Contains(frame, "Tabs: overview [activity]") {
		t.Fatalf("l did not restore activity tab:\n%s", frame)
	}
}

func TestP476BoardlessWorkItemFactsFailClosedWhenIDIsAmbiguous(t *testing.T) {
	snapshot := p313ExplorerSnapshot(
		engine.TaskExplorerWorkItem{
			BoardID: "board-a", WorkItemID: "duplicate", Title: "board A row",
		},
		engine.TaskExplorerWorkItem{
			BoardID: "board-b", WorkItemID: "duplicate", Title: "board B row",
		},
	)
	snapshot.Links = []engine.TaskExplorerLink{{
		WorkExecutionLink: engine.WorkExecutionLink{
			BoardID: "board-a", WorkItemID: "duplicate",
			AgentID: "exact-agent", Generation: 1,
		},
		State: engine.TaskExplorerLinkValid,
	}}
	snapshot.Attention = []engine.TaskExplorerAttention{{
		Category: "ambiguous", WorkItemID: "duplicate",
		Reason: "ambiguous boardless attention",
	}}
	snapshot.Diagnostics = []engine.TaskExplorerDiagnostic{{
		Kind: "ambiguous", ItemID: "duplicate",
		Message: "ambiguous boardless diagnostic",
	}}
	panel := p476OpenPanel(t, &snapshot, taskExplorerLogical, 180, 30)
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab}, 30)
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}, 30)

	activity := stripANSIForTest(panel.Render(180, 30))
	assertP476Contains(t, activity, "exact-agent@g1")
	assertP476Excludes(
		t,
		activity,
		"ambiguous boardless attention",
		"ambiguous boardless diagnostic",
	)
}

func TestP476ExecutionDetailUsesExactGenerationActivity(t *testing.T) {
	snapshot := p476DetailSnapshot()
	panel := p476OpenPanel(t, &snapshot, taskExplorerExecutions, 180, 30)

	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab}, 30)
	overview := stripANSIForTest(panel.Render(180, 30))
	assertP476Contains(
		t,
		overview,
		"Detail · Execution",
		"Tabs: [overview] activity",
		"agent-a@g1",
		"execution task one",
		"shared-thread",
	)
	assertP476Excludes(
		t,
		overview,
		"other generation activity",
		"/must/not/read/transcript.jsonl",
	)

	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}, 30)
	activity := stripANSIForTest(panel.Render(180, 30))
	assertP476Contains(
		t,
		activity,
		"Tabs: overview [activity]",
		"exact generation activity",
		"ExactTool",
		"exact execution attention",
		"exact execution snapshot attention",
		"exact-link-reason",
	)
	assertP476Excludes(
		t,
		activity,
		"other generation activity",
		"other generation attention",
		"other generation snapshot attention",
		"global execution attention",
		"other-generation-link",
	)
}

func TestP476RowKindsStateTruthfulUnavailableActivity(t *testing.T) {
	workSnapshot := p313ExplorerSnapshot(engine.TaskExplorerWorkItem{
		BoardID: "board", WorkItemID: "empty-work", Title: "empty work",
	})
	workPanel := p476OpenPanel(t, &workSnapshot, taskExplorerLogical, 80, 24)
	workPanel.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab}, 24)
	workPanel.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}, 24)
	workFrame := stripANSIForTest(workPanel.Render(80, 24))
	assertP476Contains(
		t,
		workFrame,
		"Detail · WorkItem",
		"Tabs: overview [activity]",
		"No cached execution activity for this WorkItem",
	)

	executionSnapshot := p313ExplorerSnapshot()
	executionSnapshot.Executions = []engine.TaskExplorerExecution{{
		Key: engine.RuntimeExecutionKey{AgentID: "empty-agent", Generation: 1},
	}}
	executionPanel := p476OpenPanel(
		t,
		&executionSnapshot,
		taskExplorerExecutions,
		80,
		24,
	)
	executionPanel.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab}, 24)
	executionPanel.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}, 24)
	executionFrame := stripANSIForTest(executionPanel.Render(80, 24))
	assertP476Contains(
		t,
		executionFrame,
		"Detail · Execution",
		"Tabs: overview [activity]",
		"No cached activity for this exact execution",
	)
}

func TestP476RefreshDefensivelyCopiesEveryConsumedBackingStore(t *testing.T) {
	source := p476DetailSnapshot()
	wantCached := p476DetailSnapshot()
	panel := p476OpenPanel(t, &source, taskExplorerMixed, 120, 30)

	source.WorkItems[0].WorkItemID = "mutated-work"
	source.WorkItems[0].Blocks[0] = "mutated-block"
	source.WorkItems[0].BlockedBy[0] = "mutated-blocker"
	source.WorkItems[0].Attention[0] = "mutated-row-attention"
	source.WorkItems[0].ExecutionKeys[0].AgentID = "mutated-execution-key"
	source.WorkItems[0].AllowedActions[0] = engine.TaskExplorerActionCancel
	source.Executions[0].Key.AgentID = "mutated-agent"
	source.Executions[0].Attention[0] = "mutated-execution-attention"
	source.Executions[0].AllowedActions[0] = engine.TaskExplorerActionCancel
	source.Links[0].WorkItemID = "mutated-link-work"
	source.Links[0].AllowedActions[0] = engine.TaskExplorerActionCancel
	source.Attention[0].Reason = "mutated-snapshot-attention"
	source.Diagnostics[0].Message = "mutated-diagnostic"
	source.Hidden.WorkItems["pending"] = 99
	source.Hidden.Executions["running"] = 98
	source.Hidden.Attention["blocked"] = 97

	if !reflect.DeepEqual(panel.snapshot, wantCached) {
		t.Fatalf(
			"provider mutation changed cached snapshot before Refresh:\n got: %#v\nwant: %#v",
			panel.snapshot,
			wantCached,
		)
	}
	if panel.selection != (taskExplorerSelection{boardID: "board-a", workID: "work-a"}) {
		t.Fatalf("provider mutation changed cached selection: %#v", panel.selection)
	}

	panel.Refresh()
	if !reflect.DeepEqual(panel.snapshot, source) {
		t.Fatalf(
			"explicit Refresh did not observe latest provider values:\n got: %#v\nwant: %#v",
			panel.snapshot,
			source,
		)
	}
}

func TestP476RenderReplayAndDetailInputNeverDispatchProviders(t *testing.T) {
	snapshot := p476LongActivitySnapshot()
	snapshotCalls := 0
	actionCalls := 0
	newPanel := func() *TaskExplorerPanel {
		panel := NewTaskExplorerPanel(defaultStyles())
		panel.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
			snapshotCalls++
			return snapshot
		})
		panel.SetActionProvider(func(
			engine.TaskExplorerActionRequest,
		) engine.TaskExplorerActionResult {
			actionCalls++
			return engine.TaskExplorerActionResult{}
		})
		panel.Show(taskExplorerMixed, false)
		return panel
	}
	panel := newPanel()

	keys := []tea.KeyPressMsg{
		{Code: tea.KeyTab},
		{Code: tea.KeyRight},
		{Code: tea.KeyDown},
		{Code: tea.KeyPgDown},
		{Code: tea.KeyEnd},
		{Code: tea.KeyHome},
		{Code: tea.KeyLeft},
		{Code: tea.KeyRight},
	}
	for _, key := range keys {
		if dismissed := panel.HandleKey(key, 20); dismissed {
			t.Fatalf("detail replay dismissed panel for %q", key.String())
		}
		_ = panel.Render(64, 20)
	}
	firstReplay := panel.Render(64, 20)
	replayed := newPanel()
	for _, key := range keys {
		if dismissed := replayed.HandleKey(key, 20); dismissed {
			t.Fatalf("second detail replay dismissed panel for %q", key.String())
		}
		_ = replayed.Render(64, 20)
	}
	if secondReplay := replayed.Render(64, 20); secondReplay != firstReplay {
		t.Fatalf(
			"identical detail replay diverged:\nfirst:\n%s\nsecond:\n%s",
			firstReplay,
			secondReplay,
		)
	}
	detail := panel.geometry.detail
	if detail.Width <= 0 || detail.Height <= 0 {
		t.Fatalf("detail render did not publish geometry: %#v", detail)
	}
	selection, cursor, listOffset := panel.selection, panel.cursor, panel.offset
	detailOffset := panel.detailOffset
	panel.HandleMouse(tuiMouseMsg{
		X: detail.X, Y: detail.Y,
		Button: tea.MouseWheelDown, Action: mouseActionPress,
	})
	if panel.detailOffset <= detailOffset || panel.selection != selection ||
		panel.cursor != cursor || panel.offset != listOffset {
		t.Fatalf(
			"detail wheel changed the wrong state: detail=%d selection=%#v cursor=%d list=%d",
			panel.detailOffset,
			panel.selection,
			panel.cursor,
			panel.offset,
		)
	}
	panel.HandleMouse(tuiMouseMsg{
		X: detail.X, Y: detail.Y,
		Button: tea.MouseWheelUp, Action: mouseActionPress,
	})
	for _, size := range []struct{ width, height int }{
		{40, 20}, {64, 22}, {120, 30}, {180, 30},
	} {
		_ = panel.Render(size.width, size.height)
	}
	if snapshotCalls != 2 || actionCalls != 0 {
		t.Fatalf(
			"cached detail dispatched providers: snapshot=%d action=%d",
			snapshotCalls,
			actionCalls,
		)
	}
}

func TestP476AppReducerDetailReplayReturnsNoIOCommand(t *testing.T) {
	snapshot := p476LongActivitySnapshot()
	snapshotCalls := 0
	actionCalls := 0
	app := New(Config{Resumed: true, ReducedMotion: true})
	app.width, app.height = 80, 24
	app.updateLayout()
	app.taskExplorer.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
		snapshotCalls++
		return snapshot
	})
	app.taskExplorer.SetActionProvider(func(
		engine.TaskExplorerActionRequest,
	) engine.TaskExplorerActionResult {
		actionCalls++
		return engine.TaskExplorerActionResult{}
	})
	app.enterTaskPanel()
	_ = app.renderView()

	for _, msg := range []tea.Msg{
		tea.KeyPressMsg{Code: tea.KeyTab},
		tea.KeyPressMsg{Code: tea.KeyRight},
		tea.KeyPressMsg{Code: tea.KeyDown},
		tea.KeyPressMsg{Code: tea.KeyPgDown},
		tea.KeyPressMsg{Code: tea.KeyHome},
	} {
		model, cmd := app.Update(msg)
		if model != app || cmd != nil {
			t.Fatalf("detail replay returned model=%T cmd=%v for %T", model, cmd, msg)
		}
		_ = app.renderView()
	}
	detail := app.taskExplorer.geometry.detail
	model, cmd := app.Update(tuiMouseMsg{
		X:      app.layout.overlayRect.X + detail.X,
		Y:      app.layout.overlayRect.Y + detail.Y,
		Button: tea.MouseWheelDown,
		Action: mouseActionPress,
	})
	if model != app || cmd != nil {
		t.Fatalf("detail wheel returned model=%T cmd=%v", model, cmd)
	}
	if snapshotCalls != 1 || actionCalls != 0 {
		t.Fatalf(
			"App detail replay dispatched providers: snapshot=%d action=%d",
			snapshotCalls,
			actionCalls,
		)
	}
}

func TestP476DetailScrollIsIndependentBoundedAndResizeStable(t *testing.T) {
	capabilities := interactiveTerminalCaps()
	capabilities.Color = terminalcap.ColorNone
	app := New(Config{
		Resumed: true, ReducedMotion: true, TerminalCaps: &capabilities,
	})
	app.width, app.height = 64, 22
	app.updateLayout()
	snapshot := p476LongActivitySnapshot()
	app.taskExplorer.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
		return snapshot
	})
	app.enterTaskPanel()
	panel := app.taskExplorer
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab}, 22)
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}, 22)

	initial := app.renderView()
	if strings.Contains(initial, "\x1b") {
		t.Fatalf("no-color detail contains ANSI: %q", initial)
	}
	assertP313ExplorerFrame(t, initial, 64, 22, "Tabs: overview [activity]")
	selection, cursor, listOffset := panel.selection, panel.cursor, panel.offset

	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd}, 22)
	bottom := app.renderView()
	assertP476Contains(t, bottom, "exact long diagnostic 19", " of ")
	if bottom == initial {
		t.Fatal("End did not move the detail viewport")
	}
	if panel.selection != selection || panel.cursor != cursor || panel.offset != listOffset {
		t.Fatalf(
			"detail End changed list state: selection=%#v cursor=%d offset=%d",
			panel.selection,
			panel.cursor,
			panel.offset,
		)
	}

	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyHome}, 22)
	home := app.renderView()
	if home != initial {
		t.Fatalf("Home did not restore deterministic top frame:\ninitial:\n%s\nhome:\n%s", initial, home)
	}
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgDown}, 22)
	pageDown := app.renderView()
	if pageDown == initial || pageDown == bottom {
		t.Fatalf("PageDown did not produce an intermediate detail frame:\n%s", pageDown)
	}
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgUp}, 22)
	if frame := app.renderView(); frame != initial {
		t.Fatalf("PageUp did not restore the top frame:\n%s", frame)
	}

	for _, size := range []struct{ width, height int }{
		{40, 20}, {80, 24}, {120, 30}, {180, 30},
	} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			app.width, app.height = size.width, size.height
			app.updateLayout()
			beforeRenderOffset := panel.detailOffset
			first := app.renderView()
			second := app.renderView()
			if first != second {
				t.Fatalf("steady detail render changed state:\nfirst:\n%s\nsecond:\n%s", first, second)
			}
			assertP313ExplorerFrame(t, first, size.width, size.height, "Detail · WorkItem")
			if panel.detailOffset != beforeRenderOffset {
				t.Fatalf(
					"render mutated detail offset: got %d want %d",
					panel.detailOffset,
					beforeRenderOffset,
				)
			}
			if panel.selection != selection || panel.cursor != cursor || panel.offset != listOffset {
				t.Fatalf(
					"resize changed list identity: selection=%#v cursor=%d offset=%d",
					panel.selection,
					panel.cursor,
					panel.offset,
				)
			}
		})
	}
}

func TestP476SelectionChangeResetsDetailWhileSameRefreshPreservesIt(t *testing.T) {
	snapshot := p476LongActivitySnapshot()
	panel := p476OpenPanel(t, &snapshot, taskExplorerMixed, 64, 22)
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab}, 22)
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}, 22)
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd}, 22)
	beforeRefresh := stripANSIForTest(panel.Render(64, 22))
	assertP476Contains(t, beforeRefresh, "Tabs: overview [activity]", "exact long diagnostic 19")

	panel.Refresh()
	afterRefresh := stripANSIForTest(panel.Render(64, 22))
	if afterRefresh != beforeRefresh {
		t.Fatalf(
			"same exact selection Refresh did not preserve detail state:\nbefore:\n%s\nafter:\n%s",
			beforeRefresh,
			afterRefresh,
		)
	}

	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc}, 22)
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown}, 22)
	panel.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab}, 22)
	afterSelection := stripANSIForTest(panel.Render(64, 22))
	assertP476Contains(t, afterSelection, "Detail · WorkItem", "Tabs: [overview] activity")
	assertP476Excludes(t, afterSelection, "exact long diagnostic 19")
}

func p476DetailSnapshot() engine.TaskExplorerSnapshot {
	return engine.TaskExplorerSnapshot{
		Available: true,
		SessionID: "session",
		BoardID:   "board-a",
		Revision:  engine.TaskExplorerRevision{Board: 7, Runtime: 11},
		WorkItems: []engine.TaskExplorerWorkItem{
			{
				BoardID: "board-a", WorkItemID: "work-a", Revision: 7, Order: 2,
				Status: "in_progress", Title: "same label", Description: "overview description A",
				ActiveForm: "active form A", Owner: "owner-a", ResultSummary: "result A",
				Blocks: []string{"work-b"}, BlockedBy: []string{"work-prerequisite"},
				LinkedLive: true, Attention: []string{"row work attention"},
				ExecutionKeys: []engine.RuntimeExecutionKey{
					{AgentID: "agent-a", Generation: 1},
					{AgentID: "agent-a", Generation: 2},
				},
				AllowedActions: []engine.TaskExplorerAction{engine.TaskExplorerActionInspect},
			},
			{
				BoardID: "board-a", WorkItemID: "work-b", Status: "pending",
				Title: "same label", Description: "overview description B",
			},
		},
		Executions: []engine.TaskExplorerExecution{
			{
				Key:       engine.RuntimeExecutionKey{AgentID: "agent-a", Generation: 1},
				SessionID: "session-a", ThreadID: "shared-thread", ParentSessionID: "parent-session",
				ParentThreadID: "parent-thread", ParentAgentID: "parent-agent", ParentToolUseID: "tool-use",
				TranscriptPath: "/must/not/read/transcript.jsonl", Name: "execution one",
				Task: "execution task one", Description: "execution description one",
				Activity: "exact generation activity", Status: "running", DisplayMode: "live",
				LastToolName: "ExactTool", ToolUseCount: 3, TokenCount: 42,
				Phase: engine.TaskExplorerExecutionRunning, ObservationOrdinal: 9, OrdinalPresent: true,
				Attention:      []string{"exact execution attention"},
				AllowedActions: []engine.TaskExplorerAction{engine.TaskExplorerActionInspect},
			},
			{
				Key:       engine.RuntimeExecutionKey{AgentID: "agent-a", Generation: 2},
				SessionID: "session-a", ThreadID: "shared-thread", Name: "execution two",
				Task: "execution task two", Activity: "other generation activity", Status: "running",
				Phase:     engine.TaskExplorerExecutionRunning,
				Attention: []string{"other generation attention"},
			},
		},
		Links: []engine.TaskExplorerLink{
			{
				WorkExecutionLink: engine.WorkExecutionLink{
					BoardID: "board-a", WorkItemID: "work-a", AgentID: "agent-a", Generation: 1,
				},
				State: engine.TaskExplorerLinkValid, UnavailableReason: "exact-link-reason",
				AllowedActions: []engine.TaskExplorerAction{engine.TaskExplorerActionInspect},
			},
			{
				WorkExecutionLink: engine.WorkExecutionLink{
					BoardID: "board-b", WorkItemID: "work-a", AgentID: "agent-b", Generation: 1,
				},
				State: engine.TaskExplorerLinkStale, UnavailableReason: "other-board-link",
			},
			{
				WorkExecutionLink: engine.WorkExecutionLink{
					BoardID: "board-a", WorkItemID: "work-b", AgentID: "agent-a", Generation: 1,
				},
				State: engine.TaskExplorerLinkStale, UnavailableReason: "other-work-link",
			},
			{
				WorkExecutionLink: engine.WorkExecutionLink{
					BoardID: "board-a", WorkItemID: "work-a", AgentID: "agent-a", Generation: 2,
				},
				State: engine.TaskExplorerLinkStale, UnavailableReason: "other-generation-link",
			},
		},
		Attention: []engine.TaskExplorerAttention{
			{Category: "work-exact", WorkItemID: "work-a", Reason: "exact work attention"},
			{Category: "work-other", WorkItemID: "work-b", Reason: "other work attention"},
			{Category: "work-global", Reason: "global work attention"},
			{Category: "exec-exact", AgentID: "agent-a", Generation: 1, Reason: "exact execution snapshot attention"},
			{Category: "exec-other", AgentID: "agent-a", Generation: 2, Reason: "other generation snapshot attention"},
			{Category: "exec-global", AgentID: "agent-a", Reason: "global execution attention"},
		},
		Diagnostics: []engine.TaskExplorerDiagnostic{
			{Kind: "work-exact", ItemID: "work-a", Message: "exact work diagnostic"},
			{Kind: "work-other", ItemID: "work-b", Message: "other work diagnostic"},
			{Kind: "work-global", Message: "global work diagnostic"},
		},
		Hidden: engine.TaskExplorerHiddenCounts{
			WorkItems:  map[string]int{"pending": 1},
			Executions: map[string]int{"running": 2},
			Attention:  map[string]int{"blocked": 3},
		},
	}
}

func p476LongActivitySnapshot() engine.TaskExplorerSnapshot {
	snapshot := p476DetailSnapshot()
	snapshot.Attention = snapshot.Attention[:0]
	snapshot.Diagnostics = snapshot.Diagnostics[:0]
	for index := 0; index < 20; index++ {
		snapshot.Attention = append(snapshot.Attention, engine.TaskExplorerAttention{
			Category:   "long-attention",
			WorkItemID: "work-a",
			Reason:     fmt.Sprintf("exact long attention %02d", index),
		})
		snapshot.Diagnostics = append(snapshot.Diagnostics, engine.TaskExplorerDiagnostic{
			Kind:    "long-diagnostic",
			ItemID:  "work-a",
			Message: fmt.Sprintf("exact long diagnostic %02d", index),
		})
	}
	return snapshot
}

func p476OpenPanel(
	t *testing.T,
	snapshot *engine.TaskExplorerSnapshot,
	section taskExplorerSection,
	width, height int,
) *TaskExplorerPanel {
	t.Helper()
	app := New(Config{Resumed: true, ReducedMotion: true})
	app.width, app.height = width, height
	app.updateLayout()
	app.taskExplorer.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
		return *snapshot
	})
	app.taskExplorer.Show(section, false)
	return app.taskExplorer
}

func p476Key(key rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: key, Text: string(key)}
}

func assertP476Contains(t *testing.T, frame string, wants ...string) {
	t.Helper()
	plain := stripANSIForTest(frame)
	for _, want := range wants {
		if !strings.Contains(plain, want) {
			t.Fatalf("frame omitted %q:\n%s", want, plain)
		}
	}
}

func assertP476Excludes(t *testing.T, frame string, values ...string) {
	t.Helper()
	plain := stripANSIForTest(frame)
	for _, value := range values {
		if strings.Contains(plain, value) {
			t.Fatalf("frame leaked %q:\n%s", value, plain)
		}
	}
}
