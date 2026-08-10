package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/terminalcap"
)

func TestP142cThreadDetailLoadsBoundedPagesWithStablePhysicalIdentity(t *testing.T) {
	app, _, requests := newP142cThreadApp(t)
	detailCalls := 0
	app.agentDetailProvider = func(string) (engine.AgentDetailSnapshot, bool) {
		detailCalls++
		return engine.AgentDetailSnapshot{}, false
	}
	app.agentTranscriptProvider = func(request engine.AgentTranscriptPageRequest) (engine.AgentTranscriptPage, bool, error) {
		*requests = append(*requests, request)
		page := p142cPage(request, engine.ThreadModeReplayOnly)
		if request.Cursor == "" {
			page.Messages = []engine.AgentTranscriptMessage{
				p142cMessage("new-1", "same text"),
				p142cMessage("new-2", "same text"),
			}
			page.HasMore = true
			page.NextCursor = "older"
		} else {
			page.Messages = []engine.AgentTranscriptMessage{p142cMessage("old-1", "older text")}
		}
		return page, true, nil
	}

	cmd, err := app.activateThreadByIDWithCmd("child-a")
	if err != nil || cmd == nil {
		t.Fatalf("first activation = cmd:%v err:%v", cmd != nil, err)
	}
	if len(*requests) != 0 || detailCalls != 0 {
		t.Fatalf("activation performed synchronous reads: pages=%d details=%d", len(*requests), detailCalls)
	}
	applyP142cCmd(t, app, cmd)
	if len(*requests) != 1 || (*requests)[0].Limit != agentTranscriptPageLimit || (*requests)[0].Cursor != "" {
		t.Fatalf("first page request = %#v", *requests)
	}
	view := app.threadViews.active()
	if got, want := view.projectedIDs, []string{"new-1", "new-2"}; !reflectStringSlice(got, want) {
		t.Fatalf("first physical identities = %#v, want %#v", got, want)
	}
	if text := stripANSIForTest(app.chat.RenderAllExpanded(80)); strings.Count(text, "same text") != 2 {
		t.Fatalf("equal-content physical rows collapsed:\n%s", text)
	}

	app.chat.ScrollToTop()
	olderCmd := app.loadOlderActiveAgentTranscript()
	if olderCmd == nil {
		t.Fatal("top-of-view did not request the next cursor")
	}
	applyP142cCmd(t, app, olderCmd)
	if got, want := view.projectedIDs, []string{"old-1", "new-1", "new-2"}; !reflectStringSlice(got, want) {
		t.Fatalf("paged physical identities = %#v, want %#v", got, want)
	}
	if len(*requests) != 2 || (*requests)[1].Cursor != "older" {
		t.Fatalf("continuation requests = %#v", *requests)
	}
	if detailCalls != 0 {
		t.Fatalf("lazy paging called legacy detail reader %d time(s)", detailCalls)
	}
}

func TestP142cRapidThreadSwitchDiscardsStaleAsyncPage(t *testing.T) {
	app, _, requests := newP142cThreadApp(t)
	app.agentTranscriptProvider = func(request engine.AgentTranscriptPageRequest) (engine.AgentTranscriptPage, bool, error) {
		*requests = append(*requests, request)
		page := p142cPage(request, engine.ThreadModeReplayOnly)
		page.Messages = []engine.AgentTranscriptMessage{p142cMessage(request.AgentID+"-row", request.AgentID+" content")}
		return page, true, nil
	}

	aCmd, err := app.activateThreadByIDWithCmd("child-a")
	if err != nil {
		t.Fatal(err)
	}
	bCmd, err := app.activateThreadByIDWithCmd("child-b")
	if err != nil {
		t.Fatal(err)
	}
	aLoaded := aCmd().(agentTranscriptPageLoadedMsg)
	if app.applyAgentTranscriptPage(aLoaded) {
		t.Fatal("stale child-a page applied after child-b selection")
	}
	if app.activeThreadViewID() != "child-b" || strings.Contains(stripANSIForTest(app.chat.RenderAllExpanded(80)), "agent-a content") {
		t.Fatalf("stale result crossed thread boundary: thread=%q chat=%q", app.activeThreadViewID(), app.chat.RenderAllExpanded(80))
	}
	applyP142cCmd(t, app, bCmd)
	if text := stripANSIForTest(app.chat.RenderAllExpanded(80)); !strings.Contains(text, "agent-b content") || strings.Contains(text, "agent-a content") {
		t.Fatalf("selected child projection = %q", text)
	}

	returnCmd, err := app.activateThreadByIDWithCmd("child-a")
	if err != nil || returnCmd == nil {
		t.Fatalf("discarded in-flight page was not re-requested: cmd=%v err=%v", returnCmd != nil, err)
	}
}

func TestP142cGenerationChangeRejectsAlreadyLoadedOldPage(t *testing.T) {
	app, _, _ := newP142cThreadApp(t)
	snapshot := p142cSnapshot()
	app.taskExplorerSnapshotSource = func() engine.TaskExplorerSnapshot { return snapshot }
	app.agentTranscriptProvider = func(request engine.AgentTranscriptPageRequest) (engine.AgentTranscriptPage, bool, error) {
		page := p142cPage(request, engine.ThreadModeReplayOnly)
		page.Messages = []engine.AgentTranscriptMessage{p142cMessage("old-generation", "stale generation")}
		return page, true, nil
	}
	cmd, err := app.activateThreadByIDWithCmd("child-a")
	if err != nil {
		t.Fatal(err)
	}
	loaded := cmd().(agentTranscriptPageLoadedMsg)
	if !app.applyAgentTranscriptPage(loaded) {
		t.Fatal("initial generation page did not apply")
	}
	if text := stripANSIForTest(app.chat.RenderAllExpanded(80)); !strings.Contains(text, "stale generation") {
		t.Fatalf("initial generation was not projected: %q", text)
	}
	snapshot.Executions[0].Key.Generation = 9
	snapshot.Revision.Runtime++
	_, nextCmd := app.refreshActiveThreadProjectionWithCmd()
	if nextCmd == nil {
		t.Fatal("new generation did not start a replacement page")
	}
	if text := stripANSIForTest(app.chat.RenderAllExpanded(80)); strings.Contains(text, "stale generation") {
		t.Fatalf("old generation remained visible while replacement loaded: %q", text)
	}
	if app.applyAgentTranscriptPage(loaded) {
		t.Fatal("old generation page applied after current selection advanced")
	}
	view := app.threadViews.active()
	if view.transcript.selection.Generation != 9 || len(view.transcript.messages) != 0 {
		t.Fatalf("generation rebind = selection:%#v messages:%#v", view.transcript.selection, view.transcript.messages)
	}
	applyP142cCmd(t, app, nextCmd)
}

func TestP142cThreadViewRestoresPresentationAndReplayControlsAreInert(t *testing.T) {
	app, _, _ := newP142cThreadApp(t)
	app.agentTranscriptProvider = func(request engine.AgentTranscriptPageRequest) (engine.AgentTranscriptPage, bool, error) {
		page := p142cPage(request, engine.ThreadModeReplayOnly)
		page.Messages = []engine.AgentTranscriptMessage{
			p142cMessage("row-1", strings.Repeat("line one ", 20)),
			p142cMessage("row-2", strings.Repeat("line two ", 20)),
		}
		return page, true, nil
	}
	cmd, err := app.activateThreadByIDWithCmd("child-a")
	if err != nil {
		t.Fatal(err)
	}
	applyP142cCmd(t, app, cmd)
	app.textarea.SetValue("preserved draft")
	app.threadDetailTab = agentDetailTranscript
	app.chat.ScrollToTop()
	follow, offset := app.chat.Following(), app.chat.offsetIdx
	if err := app.activateThreadByID("leader"); err != nil {
		t.Fatal(err)
	}
	returnCmd, err := app.activateThreadByIDWithCmd("child-a")
	if err != nil || returnCmd != nil {
		t.Fatalf("restored loaded view unexpectedly re-read: cmd=%v err=%v", returnCmd != nil, err)
	}
	if app.textarea.Value() != "preserved draft" || app.threadDetailTab != agentDetailTranscript || app.chat.Following() != follow || app.chat.offsetIdx != offset {
		t.Fatalf("presentation restore = draft:%q tab:%v follow:%v offset:%d", app.textarea.Value(), app.threadDetailTab, app.chat.Following(), app.chat.offsetIdx)
	}

	sends := 0
	app.taskExplorerActionProvider = func(engine.TaskExplorerActionRequest) engine.TaskExplorerActionResult {
		sends++
		return engine.TaskExplorerActionResult{}
	}
	app.textarea.SetValue("must not send")
	app.sendMessage()
	if sends != 0 || app.textarea.Value() != "must not send" {
		t.Fatalf("replay composer mutated runtime: sends=%d draft=%q", sends, app.textarea.Value())
	}
}

func TestP142cPanelsShareLazyTranscriptProjectionAndReadonlyControls(t *testing.T) {
	snapshot := p142cSnapshot()
	selection := agentTranscriptSelection{AgentID: "agent-a", ThreadID: "child-a", Generation: 7, Mode: engine.ThreadModeEvictedTranscript}
	pageProvider := func(request engine.AgentTranscriptPageRequest) (engine.AgentTranscriptPage, bool, error) {
		page := p142cPage(request, engine.ThreadModeEvictedTranscript)
		page.Messages = []engine.AgentTranscriptMessage{p142cMessage("panel-row", "panel transcript")}
		return page, true, nil
	}
	selectionProvider := func(string) (agentTranscriptSelection, bool) { return selection, true }
	controls := 0

	background := NewBackgroundTasksPanel(defaultStyles())
	background.SetExplorerSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	background.SetTranscriptProvider(pageProvider)
	background.SetTranscriptSelectionProvider(selectionProvider)
	background.SetActionProvider(func(request engine.TaskExplorerActionRequest) engine.TaskExplorerActionResult {
		controls++
		return engine.TaskExplorerActionResult{RequestID: request.RequestID, BoardID: request.BoardID, BoardRevision: request.BoardRevision, RuntimeRevision: request.RuntimeRevision, AgentID: request.AgentID, Generation: request.Generation, Action: request.Action, Outcome: "accepted"}
	})
	found, cmd := background.ShowAgent("agent-a")
	if !found || cmd == nil {
		t.Fatalf("background detail = found:%v cmd:%v", found, cmd != nil)
	}
	background.applyTranscriptPage(cmd().(agentTranscriptPageLoadedMsg))
	background.detailTab = agentDetailTranscript
	background.rebuildAgentDetailLines()
	background.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("i"))}, 20)
	background.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("x"))}, 20)
	if controls != 0 {
		t.Fatalf("evicted background controls invoked runtime %d time(s)", controls)
	}
	assertP142cPanelBounds(t, background.Overlay(strings.Repeat("\n", 20), 40, 20), 40, "panel transcript")

	teams := NewTeamsPanel(defaultStyles())
	teams.SetExplorerSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	teams.SetTranscriptProvider(pageProvider)
	teams.SetTranscriptSelectionProvider(selectionProvider)
	teams.Show()
	_, teamsCmd := teams.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyTab}, 20)
	if teamsCmd == nil {
		t.Fatal("teams detail did not issue first lazy page")
	}
	teams.applyTranscriptPage(teamsCmd().(agentTranscriptPageLoadedMsg))
	teams.rebuildAgentDetailLines()
	for _, key := range []string{"i", "x", "p", "s"} {
		teams.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune(key))}, 20)
	}
	if controls != 0 {
		t.Fatalf("evicted teams controls invoked runtime %d time(s)", controls)
	}
	assertP142cPanelBounds(t, teams.Overlay(strings.Repeat("\n", 20), 40, 20), 40, "panel transcript")
}

func TestP142cNoColorThreadProjectionContainsNoTerminalStyles(t *testing.T) {
	caps := interactiveTerminalCaps()
	caps.Color = terminalcap.ColorNone
	app := New(Config{Resumed: true, TerminalCaps: &caps})
	app.rebindLeaderThreadView("leader")
	app.width, app.height = 40, 20
	app.updateLayout()
	app.threadCatalogProvider = func() engine.RuntimeThreadCatalogSnapshot {
		return p142cCatalog()
	}
	app.taskExplorerSnapshotSource = func() engine.TaskExplorerSnapshot { return p142cSnapshot() }
	app.agentTranscriptProvider = func(request engine.AgentTranscriptPageRequest) (engine.AgentTranscriptPage, bool, error) {
		page := p142cPage(request, engine.ThreadModeReplayOnly)
		page.Messages = []engine.AgentTranscriptMessage{p142cMessage("row", "readable without color")}
		return page, true, nil
	}
	cmd, err := app.activateThreadByIDWithCmd("child-a")
	if err != nil {
		t.Fatal(err)
	}
	applyP142cCmd(t, app, cmd)
	frame := app.renderView()
	if strings.Contains(frame, "\x1b") || !strings.Contains(frame, "readable without color") {
		t.Fatalf("NO_COLOR child frame = %q", frame)
	}
}

func TestP473TaskExplorerSwitchDoesNotRebindSupersededGeneration(
	t *testing.T,
) {
	app, snapshot, catalog, requests := newP473TaskExplorerApp(t)
	app.taskExplorer.SetActionProvider(func(
		request engine.TaskExplorerActionRequest,
	) engine.TaskExplorerActionResult {
		snapshot.Executions[0].Key.Generation = 2
		snapshot.Revision.Runtime++
		catalog.Revision++
		return engine.TaskExplorerActionResult{
			RequestID:       request.RequestID,
			BoardID:         request.BoardID,
			BoardRevision:   request.BoardRevision,
			RuntimeRevision: request.RuntimeRevision,
			AgentID:         request.AgentID,
			Generation:      request.Generation,
			Action:          request.Action,
			Outcome:         "switched",
			SessionID:       "child-session",
			ThreadID:        "shared-thread",
			NavigationTarget: &engine.TaskExplorerNavigationTarget{
				SessionID: "child-session", ThreadID: "shared-thread",
				AgentID: request.AgentID, Generation: request.Generation,
				Mode: engine.ThreadModeLiveAttach,
			},
		}
	})

	cmd := app.handleTaskPanelKey(
		tea.KeyPressMsg{Code: 'x', Text: "x"},
	)
	if cmd != nil {
		t.Fatal("superseded target scheduled a command")
	}
	if got := app.activeThreadViewID(); got != "leader-thread" {
		t.Fatalf("superseded generation rebound to %q", got)
	}
	if app.state != StateTaskPanel {
		t.Fatalf("superseded target closed Ctrl+T: state=%v", app.state)
	}
	if len(*requests) != 0 {
		t.Fatalf("superseded target read transcript: %#v", *requests)
	}
	active := app.notifications.Active()
	if len(active) != 1 || active[0].Severity != NotifyError {
		t.Fatalf("superseded target feedback = %#v", active)
	}
}

func TestP473TaskExplorerSwitchRequestsSelectedGenerationOnce(t *testing.T) {
	app, _, _, requests := newP473TaskExplorerApp(t)
	app.taskExplorer.SetActionProvider(func(
		request engine.TaskExplorerActionRequest,
	) engine.TaskExplorerActionResult {
		return engine.TaskExplorerActionResult{
			RequestID:       request.RequestID,
			BoardID:         request.BoardID,
			BoardRevision:   request.BoardRevision,
			RuntimeRevision: request.RuntimeRevision,
			AgentID:         request.AgentID,
			Generation:      request.Generation,
			Action:          request.Action,
			Outcome:         "switched",
			SessionID:       "child-session",
			ThreadID:        "shared-thread",
			NavigationTarget: &engine.TaskExplorerNavigationTarget{
				SessionID: "child-session", ThreadID: "shared-thread",
				AgentID: request.AgentID, Generation: request.Generation,
				Mode: engine.ThreadModeLiveAttach,
			},
		}
	})

	cmd := app.handleTaskPanelKey(
		tea.KeyPressMsg{Code: 'x', Text: "x"},
	)
	if cmd == nil {
		t.Fatal("exact target did not schedule a bounded transcript request")
	}
	if len(*requests) != 0 {
		t.Fatalf("activation performed a synchronous read: %#v", *requests)
	}
	msg := cmd()
	if loaded, ok := msg.(agentTranscriptPageLoadedMsg); ok {
		app.applyAgentTranscriptPage(loaded)
	}
	if len(*requests) != 1 ||
		(*requests)[0] != (engine.AgentTranscriptPageRequest{
			AgentID: "agent-a", Generation: 1,
			Limit: agentTranscriptPageLimit,
		}) {
		t.Fatalf("exact target requests = %#v", *requests)
	}
	if got := app.activeThreadViewID(); got != "shared-thread" {
		t.Fatalf("exact target activated %q", got)
	}
}

func TestP473TaskExplorerSwitchRejectsPageAfterGenerationIsSuperseded(
	t *testing.T,
) {
	app, snapshot, catalog, requests := newP473TaskExplorerApp(t)
	app.taskExplorer.SetActionProvider(func(
		request engine.TaskExplorerActionRequest,
	) engine.TaskExplorerActionResult {
		return engine.TaskExplorerActionResult{
			RequestID:       request.RequestID,
			BoardID:         request.BoardID,
			BoardRevision:   request.BoardRevision,
			RuntimeRevision: request.RuntimeRevision,
			AgentID:         request.AgentID,
			Generation:      request.Generation,
			Action:          request.Action,
			Outcome:         "switched",
			SessionID:       "child-session",
			ThreadID:        "shared-thread",
			NavigationTarget: &engine.TaskExplorerNavigationTarget{
				SessionID: "child-session", ThreadID: "shared-thread",
				AgentID: request.AgentID, Generation: request.Generation,
				Mode: engine.ThreadModeLiveAttach,
			},
		}
	})
	app.agentTranscriptProvider = func(
		request engine.AgentTranscriptPageRequest,
	) (engine.AgentTranscriptPage, bool, error) {
		*requests = append(*requests, request)
		return engine.AgentTranscriptPage{
			Revision: uint64(request.Generation), AgentID: request.AgentID,
			SessionID: "child-session", ThreadID: "shared-thread",
			Generation: request.Generation,
			AttachMode: engine.ThreadModeLiveAttach,
			Messages: []engine.AgentTranscriptMessage{
				p142cMessage("stale-exact-page", "stale exact generation"),
			},
		}, true, nil
	}

	cmd := app.handleTaskPanelKey(
		tea.KeyPressMsg{Code: 'x', Text: "x"},
	)
	if cmd == nil {
		t.Fatal("exact target did not schedule a transcript request")
	}

	snapshot.Revision.Runtime++
	catalog.Revision = snapshot.Revision.Runtime
	snapshot.Executions[0].Attention = []string{"waiting_input"}
	snapshot.Executions[0].AllowedActions = []engine.TaskExplorerAction{
		engine.TaskExplorerActionInspect,
	}
	snapshot.Executions = append(snapshot.Executions, engine.TaskExplorerExecution{
		Key: engine.RuntimeExecutionKey{
			AgentID: "agent-a", Generation: 2,
		},
		SessionID:      "child-session",
		ThreadID:       "shared-thread",
		TranscriptPath: "/tmp/p473-transcript.jsonl",
		Status:         "running",
		Phase:          engine.TaskExplorerExecutionRunning,
		AllowedActions: []engine.TaskExplorerAction{
			engine.TaskExplorerActionInspect,
			engine.TaskExplorerActionSwitch,
		},
	})

	loaded := cmd().(agentTranscriptPageLoadedMsg)
	if app.applyAgentTranscriptPage(loaded) {
		t.Fatal("superseded exact generation page was applied")
	}
	if len(*requests) != 1 {
		t.Fatalf("superseded page dispatched %d transcript requests", len(*requests))
	}
	if text := stripANSIForTest(app.chat.RenderAllExpanded(80)); strings.Contains(text, "stale exact generation") ||
		!strings.Contains(text, "changed before activation") {
		t.Fatalf("superseded page projection = %q", text)
	}
	active := app.notifications.Active()
	if len(active) != 1 || active[0].Severity != NotifyError {
		t.Fatalf("superseded page feedback = %#v", active)
	}
}

func TestP473TaskExplorerSwitchRejectsOlderRequestAfterGenerationIsSuperseded(
	t *testing.T,
) {
	app, snapshot, catalog, requests := newP473TaskExplorerApp(t)
	app.taskExplorer.SetActionProvider(func(
		request engine.TaskExplorerActionRequest,
	) engine.TaskExplorerActionResult {
		return engine.TaskExplorerActionResult{
			RequestID:       request.RequestID,
			BoardID:         request.BoardID,
			BoardRevision:   request.BoardRevision,
			RuntimeRevision: request.RuntimeRevision,
			AgentID:         request.AgentID,
			Generation:      request.Generation,
			Action:          request.Action,
			Outcome:         "switched",
			SessionID:       "child-session",
			ThreadID:        "shared-thread",
			NavigationTarget: &engine.TaskExplorerNavigationTarget{
				SessionID: "child-session", ThreadID: "shared-thread",
				AgentID: request.AgentID, Generation: request.Generation,
				Mode: engine.ThreadModeLiveAttach,
			},
		}
	})
	app.agentTranscriptProvider = func(
		request engine.AgentTranscriptPageRequest,
	) (engine.AgentTranscriptPage, bool, error) {
		*requests = append(*requests, request)
		page := engine.AgentTranscriptPage{
			Revision: uint64(request.Generation), AgentID: request.AgentID,
			SessionID: "child-session", ThreadID: "shared-thread",
			Generation: request.Generation,
			AttachMode: engine.ThreadModeLiveAttach,
		}
		if request.Cursor == "" {
			page.Messages = []engine.AgentTranscriptMessage{
				p142cMessage("current-exact-page", "current exact generation"),
			}
			page.HasMore = true
			page.NextCursor = "older"
		} else {
			page.Messages = []engine.AgentTranscriptMessage{
				p142cMessage("stale-older-page", "stale older generation"),
			}
		}
		return page, true, nil
	}

	cmd := app.handleTaskPanelKey(
		tea.KeyPressMsg{Code: 'x', Text: "x"},
	)
	if cmd == nil {
		t.Fatal("exact target did not schedule a transcript request")
	}
	applyP142cCmd(t, app, cmd)
	if len(*requests) != 1 {
		t.Fatalf("initial exact page requests = %#v", *requests)
	}

	snapshot.Revision.Runtime++
	catalog.Revision = snapshot.Revision.Runtime
	snapshot.Executions[0].Attention = []string{"waiting_input"}
	snapshot.Executions[0].AllowedActions = []engine.TaskExplorerAction{
		engine.TaskExplorerActionInspect,
	}
	snapshot.Executions = append(snapshot.Executions, engine.TaskExplorerExecution{
		Key: engine.RuntimeExecutionKey{
			AgentID: "agent-a", Generation: 2,
		},
		SessionID:      "child-session",
		ThreadID:       "shared-thread",
		TranscriptPath: "/tmp/p473-transcript.jsonl",
		Status:         "running",
		Phase:          engine.TaskExplorerExecutionRunning,
		AllowedActions: []engine.TaskExplorerAction{
			engine.TaskExplorerActionInspect,
			engine.TaskExplorerActionSwitch,
		},
	})

	app.chat.ScrollToTop()
	if olderCmd := app.loadOlderActiveAgentTranscript(); olderCmd != nil {
		t.Fatal("superseded exact target scheduled an older transcript request")
	}
	if len(*requests) != 1 {
		t.Fatalf("superseded exact target dispatched requests = %#v", *requests)
	}
	if text := stripANSIForTest(app.chat.RenderAllExpanded(80)); strings.Contains(text, "stale older generation") ||
		!strings.Contains(text, "changed before activation") {
		t.Fatalf("superseded older-page projection = %q", text)
	}
	active := app.notifications.Active()
	if len(active) != 1 || active[0].Severity != NotifyError {
		t.Fatalf("superseded older-page feedback = %#v", active)
	}
}

func TestP473TaskExplorerExactRefreshPreservesViewOnReusedCatalogEntry(
	t *testing.T,
) {
	app, snapshot, catalog, requests := newP473TaskExplorerApp(t)
	app.taskExplorer.SetActionProvider(func(
		request engine.TaskExplorerActionRequest,
	) engine.TaskExplorerActionResult {
		return engine.TaskExplorerActionResult{
			RequestID:       request.RequestID,
			BoardID:         request.BoardID,
			BoardRevision:   request.BoardRevision,
			RuntimeRevision: request.RuntimeRevision,
			AgentID:         request.AgentID,
			Generation:      request.Generation,
			Action:          request.Action,
			Outcome:         "switched",
			SessionID:       "child-session",
			ThreadID:        "shared-thread",
			NavigationTarget: &engine.TaskExplorerNavigationTarget{
				SessionID: "child-session", ThreadID: "shared-thread",
				AgentID: request.AgentID, Generation: request.Generation,
				Mode: engine.ThreadModeLiveAttach,
			},
		}
	})

	cmd := app.handleTaskPanelKey(
		tea.KeyPressMsg{Code: 'x', Text: "x"},
	)
	if cmd == nil {
		t.Fatal("exact target did not schedule a transcript request")
	}
	applyP142cCmd(t, app, cmd)
	view := app.threadViews.active()
	wantMode, wantLabel := view.Mode, view.displayLabel

	snapshot.Revision.Runtime++
	catalog.Revision = snapshot.Revision.Runtime
	catalog.Threads[0].AgentID = "other-agent"
	catalog.Threads[0].Name = "wrong-generation"
	catalog.Threads[0].Mode = engine.ThreadModeReplayOnly

	running, refreshCmd := app.refreshActiveThreadProjectionWithCmd()
	if running || refreshCmd != nil {
		t.Fatalf("reused catalog refresh = running:%v cmd:%v", running, refreshCmd != nil)
	}
	if view.Mode != wantMode || view.displayLabel != wantLabel {
		t.Fatalf(
			"failed exact refresh rewrote view = mode:%q label:%q, want mode:%q label:%q",
			view.Mode,
			view.displayLabel,
			wantMode,
			wantLabel,
		)
	}
	if got := app.activeThreadViewID(); got != "shared-thread" {
		t.Fatalf("failed exact refresh changed active view to %q", got)
	}
	if len(*requests) != 1 {
		t.Fatalf("failed exact refresh dispatched requests = %#v", *requests)
	}
	active := app.notifications.Active()
	if len(active) != 1 || active[0].Severity != NotifyError {
		t.Fatalf("failed exact refresh feedback = %#v", active)
	}
}

func TestP473TaskExplorerExactActivationFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		edit func(
			*App,
			*engine.TaskExplorerSnapshot,
			*engine.RuntimeThreadCatalogSnapshot,
			*taskExplorerNavigationIntent,
		)
	}{
		{
			name: "runtime revision changed",
			edit: func(
				_ *App,
				snapshot *engine.TaskExplorerSnapshot,
				_ *engine.RuntimeThreadCatalogSnapshot,
				_ *taskExplorerNavigationIntent,
			) {
				snapshot.Revision.Runtime++
			},
		},
		{
			name: "catalog revision changed",
			edit: func(
				_ *App,
				_ *engine.TaskExplorerSnapshot,
				catalog *engine.RuntimeThreadCatalogSnapshot,
				_ *taskExplorerNavigationIntent,
			) {
				catalog.Revision++
			},
		},
		{
			name: "execution removed",
			edit: func(
				_ *App,
				snapshot *engine.TaskExplorerSnapshot,
				_ *engine.RuntimeThreadCatalogSnapshot,
				_ *taskExplorerNavigationIntent,
			) {
				snapshot.Executions = nil
			},
		},
		{
			name: "switch action revoked",
			edit: func(
				_ *App,
				snapshot *engine.TaskExplorerSnapshot,
				_ *engine.RuntimeThreadCatalogSnapshot,
				_ *taskExplorerNavigationIntent,
			) {
				snapshot.Executions[0].AllowedActions = []engine.TaskExplorerAction{
					engine.TaskExplorerActionInspect,
				}
			},
		},
		{
			name: "catalog removed",
			edit: func(
				_ *App,
				_ *engine.TaskExplorerSnapshot,
				catalog *engine.RuntimeThreadCatalogSnapshot,
				_ *taskExplorerNavigationIntent,
			) {
				catalog.Threads = nil
			},
		},
		{
			name: "catalog duplicated",
			edit: func(
				_ *App,
				_ *engine.TaskExplorerSnapshot,
				catalog *engine.RuntimeThreadCatalogSnapshot,
				_ *taskExplorerNavigationIntent,
			) {
				catalog.Threads = append(catalog.Threads, catalog.Threads[0])
			},
		},
		{
			name: "catalog Session changed",
			edit: func(
				_ *App,
				_ *engine.TaskExplorerSnapshot,
				catalog *engine.RuntimeThreadCatalogSnapshot,
				_ *taskExplorerNavigationIntent,
			) {
				catalog.Threads[0].SessionID = "other-session"
			},
		},
		{
			name: "catalog Agent changed",
			edit: func(
				_ *App,
				_ *engine.TaskExplorerSnapshot,
				catalog *engine.RuntimeThreadCatalogSnapshot,
				_ *taskExplorerNavigationIntent,
			) {
				catalog.Threads[0].AgentID = "other-agent"
			},
		},
		{
			name: "catalog mode changed",
			edit: func(
				_ *App,
				_ *engine.TaskExplorerSnapshot,
				catalog *engine.RuntimeThreadCatalogSnapshot,
				_ *taskExplorerNavigationIntent,
			) {
				catalog.Threads[0].Mode = engine.ThreadModeReplayOnly
			},
		},
		{
			name: "transcript provider missing",
			edit: func(
				app *App,
				_ *engine.TaskExplorerSnapshot,
				_ *engine.RuntimeThreadCatalogSnapshot,
				_ *taskExplorerNavigationIntent,
			) {
				app.agentTranscriptProvider = nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, snapshot, catalog, requests := newP473TaskExplorerApp(t)
			intent := taskExplorerNavigationIntent{
				Target: engine.TaskExplorerNavigationTarget{
					SessionID: "child-session", ThreadID: "shared-thread",
					AgentID: "agent-a", Generation: 1,
					Mode: engine.ThreadModeLiveAttach,
				},
				RuntimeRevision: 7,
			}
			test.edit(app, snapshot, catalog, &intent)
			cmd, err := app.activateTaskExplorerNavigationTarget(intent)
			if err == nil || cmd != nil {
				t.Fatalf("failed-closed activation = cmd:%v err:%v", cmd != nil, err)
			}
			if got := app.activeThreadViewID(); got != "leader-thread" {
				t.Fatalf("failed target changed active view to %q", got)
			}
			if len(*requests) != 0 {
				t.Fatalf("failed target read transcript: %#v", *requests)
			}
		})
	}
}

func newP473TaskExplorerApp(
	t *testing.T,
) (*App, *engine.TaskExplorerSnapshot, *engine.RuntimeThreadCatalogSnapshot,
	*[]engine.AgentTranscriptPageRequest,
) {
	t.Helper()
	app := New(Config{Resumed: true})
	app.rebindLeaderThreadView("leader-thread")
	app.width, app.height = 80, 24
	app.updateLayout()
	snapshot := &engine.TaskExplorerSnapshot{
		Available: true,
		SessionID: "root-session",
		BoardID:   "board-p473",
		Revision:  engine.TaskExplorerRevision{Board: 4, Runtime: 7},
		Executions: []engine.TaskExplorerExecution{{
			Key: engine.RuntimeExecutionKey{
				AgentID: "agent-a", Generation: 1,
			},
			SessionID:      "child-session",
			ThreadID:       "shared-thread",
			TranscriptPath: "/tmp/p473-transcript.jsonl",
			Status:         "running",
			Phase:          engine.TaskExplorerExecutionRunning,
			AllowedActions: []engine.TaskExplorerAction{
				engine.TaskExplorerActionInspect,
				engine.TaskExplorerActionSwitch,
			},
		}},
		Hidden: engine.TaskExplorerHiddenCounts{
			WorkItems: map[string]int{}, Executions: map[string]int{},
			Attention: map[string]int{},
		},
	}
	catalog := &engine.RuntimeThreadCatalogSnapshot{
		Revision:       snapshot.Revision.Runtime,
		ActiveThreadID: "leader-thread",
		Threads: []engine.RuntimeThreadCatalogEntry{{
			ThreadID: "shared-thread", SessionID: "child-session",
			AgentID: "agent-a", TranscriptPath: "/tmp/p473-transcript.jsonl",
			Status: engine.RuntimeThreadRunning, Mode: engine.ThreadModeLiveAttach,
		}},
	}
	requests := &[]engine.AgentTranscriptPageRequest{}
	app.taskExplorerSnapshotSource = func() engine.TaskExplorerSnapshot {
		return *snapshot
	}
	app.taskExplorer.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
		copy := *snapshot
		copy.Executions = append(
			[]engine.TaskExplorerExecution(nil),
			snapshot.Executions...,
		)
		return copy
	})
	app.threadCatalogProvider = func() engine.RuntimeThreadCatalogSnapshot {
		return *catalog
	}
	app.agentTranscriptProvider = func(
		request engine.AgentTranscriptPageRequest,
	) (engine.AgentTranscriptPage, bool, error) {
		*requests = append(*requests, request)
		return engine.AgentTranscriptPage{
			Revision: uint64(request.Generation), AgentID: request.AgentID,
			SessionID: "child-session", ThreadID: "shared-thread",
			Generation: request.Generation,
			AttachMode: engine.ThreadModeLiveAttach,
		}, true, nil
	}
	app.taskExplorer.Show(taskExplorerExecutions, false)
	app.state = StateTaskPanel
	return app, snapshot, catalog, requests
}

func newP142cThreadApp(t *testing.T) (*App, *engine.RuntimeThreadCatalogSnapshot, *[]engine.AgentTranscriptPageRequest) {
	t.Helper()
	app := New(Config{Resumed: true})
	app.rebindLeaderThreadView("leader")
	app.width, app.height = 80, 24
	app.updateLayout()
	catalog := p142cCatalog()
	app.threadCatalogProvider = func() engine.RuntimeThreadCatalogSnapshot { return catalog }
	app.taskExplorerSnapshotSource = func() engine.TaskExplorerSnapshot { return p142cSnapshot() }
	requests := make([]engine.AgentTranscriptPageRequest, 0, 4)
	return app, &catalog, &requests
}

func p142cCatalog() engine.RuntimeThreadCatalogSnapshot {
	return engine.RuntimeThreadCatalogSnapshot{Revision: 10, ActiveThreadID: "leader", Threads: []engine.RuntimeThreadCatalogEntry{
		{ThreadID: "leader", Mode: engine.ThreadModeLiveAttach, Status: engine.RuntimeThreadRunning},
		{ThreadID: "child-a", AgentID: "agent-a", Mode: engine.ThreadModeReplayOnly, Status: engine.RuntimeThreadCompleted},
		{ThreadID: "child-b", AgentID: "agent-b", Mode: engine.ThreadModeReplayOnly, Status: engine.RuntimeThreadCompleted},
	}}
}

func p142cSnapshot() engine.TaskExplorerSnapshot {
	return engine.TaskExplorerSnapshot{Available: true, BoardID: "p142c", Revision: engine.TaskExplorerRevision{Board: 10, Runtime: 10}, Executions: []engine.TaskExplorerExecution{
		{Key: engine.RuntimeExecutionKey{AgentID: "agent-a", Generation: 7}, SessionID: "session-a", ThreadID: "child-a", Name: "alpha", Task: "inspect", Description: "inspect", Status: "completed", Phase: engine.TaskExplorerExecutionCompleted, AllowedActions: []engine.TaskExplorerAction{engine.TaskExplorerActionInspect, engine.TaskExplorerActionSwitch}},
		{Key: engine.RuntimeExecutionKey{AgentID: "agent-b", Generation: 8}, SessionID: "session-b", ThreadID: "child-b", Name: "beta", Task: "review", Description: "review", Status: "completed", Phase: engine.TaskExplorerExecutionCompleted, AllowedActions: []engine.TaskExplorerAction{engine.TaskExplorerActionInspect, engine.TaskExplorerActionSwitch}},
	}}
}

func p142cPage(request engine.AgentTranscriptPageRequest, mode engine.ThreadAttachmentMode) engine.AgentTranscriptPage {
	snapshot := p142cSnapshot()
	threadID := ""
	sessionID := ""
	for _, execution := range snapshot.Executions {
		if execution.Key.AgentID == request.AgentID {
			threadID = execution.ThreadID
			sessionID = execution.SessionID
			break
		}
	}
	return engine.AgentTranscriptPage{
		Revision: 10, AgentID: request.AgentID, SessionID: sessionID, ThreadID: threadID,
		Generation: request.Generation, AttachMode: mode, Replay: mode != engine.ThreadModeLiveAttach, Storage: "durable",
	}
}

func p142cMessage(identity, content string) engine.AgentTranscriptMessage {
	return engine.AgentTranscriptMessage{
		ID: identity, TranscriptEntryID: identity, Role: "assistant", Content: content, Completed: true,
	}
}

func applyP142cCmd(t *testing.T, app *App, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("nil Agent transcript command")
	}
	msg, ok := cmd().(agentTranscriptPageLoadedMsg)
	if !ok {
		t.Fatalf("Agent transcript command returned %T", cmd())
	}
	if !app.applyAgentTranscriptPage(msg) {
		t.Fatalf("Agent transcript page was not applied: %#v", msg.request)
	}
}

func reflectStringSlice(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func assertP142cPanelBounds(t *testing.T, view string, width int, want string) {
	t.Helper()
	plain := stripANSIForTest(view)
	if !strings.Contains(plain, want) || !strings.Contains(strings.ToLower(plain), "read-only") {
		t.Fatalf("panel omitted lazy/read-only state:\n%s", plain)
	}
	for _, line := range strings.Split(view, "\n") {
		if got := xansi.StringWidth(line); got > width {
			t.Fatalf("panel width = %d, want <= %d: %q", got, width, line)
		}
	}
}
