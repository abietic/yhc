package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/terminalcap"
)

func TestP477ExecutionTabsDispatchOnlyExactLazyCommands(t *testing.T) {
	snapshot := p477ExecutionSnapshot()
	snapshotCalls := 0
	actionCalls := 0
	transcriptRequests := make([]engine.AgentTranscriptPageRequest, 0, 1)
	detailRequests := make([]engine.AgentExecutionDetailRequest, 0, 2)
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
	panel.SetTranscriptProvider(func(
		request engine.AgentTranscriptPageRequest,
	) (engine.AgentTranscriptPage, bool, error) {
		transcriptRequests = append(transcriptRequests, request)
		return p477TranscriptPage(request, "transcript exact row"), true, nil
	})
	panel.SetExecutionDetailProvider(func(
		request engine.AgentExecutionDetailRequest,
	) (engine.AgentExecutionDetail, bool, error) {
		detailRequests = append(detailRequests, request)
		return p477ExecutionDetail(request, "bounded exact output"), true, nil
	})
	panel.Show(taskExplorerExecutions, false)

	if dismissed, cmd := panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyTab}, 30); dismissed || cmd != nil {
		t.Fatalf("opening cached overview = dismissed:%v cmd:%v", dismissed, cmd != nil)
	}
	for index := 0; index < 3; index++ {
		_ = panel.Render(180, 30)
	}
	if _, cmd := panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyRight}, 30); cmd != nil {
		t.Fatal("cached activity scheduled I/O")
	}
	if snapshotCalls != 1 || actionCalls != 0 || len(transcriptRequests) != 0 || len(detailRequests) != 0 {
		t.Fatalf(
			"cached tabs dispatched providers: snapshot=%d action=%d transcript=%d detail=%d",
			snapshotCalls,
			actionCalls,
			len(transcriptRequests),
			len(detailRequests),
		)
	}

	_, transcriptCmd := panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyRight}, 30)
	if transcriptCmd == nil || len(transcriptRequests) != 0 {
		t.Fatalf("transcript lazy command = %v, synchronous calls=%d", transcriptCmd != nil, len(transcriptRequests))
	}
	transcriptLoaded := transcriptCmd().(agentTranscriptPageLoadedMsg)
	if transcriptLoaded.request.surface != agentTranscriptSurfaceTaskExplorer ||
		transcriptLoaded.request.tab != taskExplorerDetailTranscript ||
		transcriptLoaded.request.selection.AgentID != "agent-a" ||
		transcriptLoaded.request.selection.Generation != 1 ||
		transcriptLoaded.request.selection.ThreadID != "shared-thread" ||
		transcriptLoaded.request.cursor != "" ||
		len(transcriptRequests) != 1 ||
		transcriptRequests[0] != (engine.AgentTranscriptPageRequest{
			AgentID: "agent-a", Generation: 1, Limit: agentTranscriptPageLimit,
		}) {
		t.Fatalf("exact transcript request = msg:%#v engine:%#v", transcriptLoaded.request, transcriptRequests)
	}
	if !panel.applyTranscriptPage(transcriptLoaded) {
		t.Fatal("exact transcript result was not applied")
	}
	assertP476Contains(
		t,
		panel.Render(180, 30),
		"Tabs: overview activity [transcript] output lineage",
		"transcript exact row",
	)

	_, outputCmd := panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyRight}, 30)
	if outputCmd == nil || len(detailRequests) != 0 {
		t.Fatalf("output lazy command = %v, synchronous calls=%d", outputCmd != nil, len(detailRequests))
	}
	outputLoaded := outputCmd().(taskExplorerExecutionDetailLoadedMsg)
	if outputLoaded.request.selection != (taskExplorerSelection{agentID: "agent-a", generation: 1}) ||
		outputLoaded.request.sessionID != "session-a" ||
		outputLoaded.request.threadID != "shared-thread" ||
		outputLoaded.request.tab != taskExplorerDetailOutput ||
		outputLoaded.request.cursor != "" ||
		len(detailRequests) != 1 ||
		!detailRequests[0].IncludeOutput {
		t.Fatalf("exact output request = msg:%#v engine:%#v", outputLoaded.request, detailRequests)
	}
	if !panel.applyExecutionDetail(outputLoaded) {
		t.Fatal("exact output result was not applied")
	}
	assertP476Contains(
		t,
		panel.Render(180, 30),
		"Tabs: overview activity transcript [output] lineage",
		"bounded exact output",
		"/tmp/agent-a-g1.out",
	)

	_, lineageCmd := panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyRight}, 30)
	if lineageCmd == nil || len(detailRequests) != 1 {
		t.Fatalf("lineage lazy command = %v, synchronous calls=%d", lineageCmd != nil, len(detailRequests))
	}
	lineageLoaded := lineageCmd().(taskExplorerExecutionDetailLoadedMsg)
	if lineageLoaded.request.tab != taskExplorerDetailLineage ||
		lineageLoaded.request.cursor != "" {
		t.Fatalf("lineage message identity = %#v", lineageLoaded.request)
	}
	if !panel.applyExecutionDetail(lineageLoaded) || len(detailRequests) != 2 || detailRequests[1].IncludeOutput {
		t.Fatalf("lineage result/capability = applied:%v requests:%#v", panel.applyExecutionDetail(lineageLoaded), detailRequests)
	}
	lineage := panel.Render(180, 30)
	assertP476Contains(
		t,
		lineage,
		"Tabs: overview activity transcript output [lineage]",
		"Session: session-a",
		"Parent Agent: parent-agent",
		"CWD: /workspace/exact",
		"Branch: codex/exact",
	)

	for _, size := range []struct{ width, height int }{{40, 20}, {80, 24}, {120, 30}, {180, 30}} {
		first := panel.Render(size.width, size.height)
		second := panel.Render(size.width, size.height)
		if first != second {
			t.Fatalf("%dx%d render mutated lazy state", size.width, size.height)
		}
		assertP313ExplorerFrame(t, first, size.width, size.height, "[lineage]")
	}
	if snapshotCalls != 1 || actionCalls != 0 || len(transcriptRequests) != 1 || len(detailRequests) != 2 {
		t.Fatalf(
			"render replay dispatched providers: snapshot=%d action=%d transcript=%d detail=%d",
			snapshotCalls,
			actionCalls,
			len(transcriptRequests),
			len(detailRequests),
		)
	}
}

func TestP477WorkItemsNeverGainExecutionReaderTabs(t *testing.T) {
	snapshot := p477ExecutionSnapshot()
	transcriptCalls := 0
	detailCalls := 0
	panel := NewTaskExplorerPanel(defaultStyles())
	panel.SetSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	panel.SetTranscriptProvider(func(
		engine.AgentTranscriptPageRequest,
	) (engine.AgentTranscriptPage, bool, error) {
		transcriptCalls++
		return engine.AgentTranscriptPage{}, false, nil
	})
	panel.SetExecutionDetailProvider(func(
		engine.AgentExecutionDetailRequest,
	) (engine.AgentExecutionDetail, bool, error) {
		detailCalls++
		return engine.AgentExecutionDetail{}, false, nil
	})
	panel.Show(taskExplorerLogical, false)
	_, _ = panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyTab}, 24)
	for index := 0; index < 8; index++ {
		if _, cmd := panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyRight}, 24); cmd != nil {
			t.Fatalf("WorkItem tab %d scheduled an execution reader", index)
		}
	}
	frame := panel.Render(80, 24)
	assertP476Contains(t, frame, "Detail · WorkItem", "Tabs: overview [activity]")
	assertP476Excludes(t, frame, "transcript", "output", "lineage")
	if transcriptCalls != 0 || detailCalls != 0 {
		t.Fatalf("WorkItem readers = transcript:%d detail:%d", transcriptCalls, detailCalls)
	}
}

func TestP477RejectsStaleDuplicateOutOfOrderAndClosedPanelResults(t *testing.T) {
	snapshot := p477ExecutionSnapshot()
	detailCalls := 0
	transcriptCalls := 0
	panel := NewTaskExplorerPanel(defaultStyles())
	panel.SetSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	panel.SetTranscriptProvider(func(
		request engine.AgentTranscriptPageRequest,
	) (engine.AgentTranscriptPage, bool, error) {
		transcriptCalls++
		return p477TranscriptPage(request, "stale transcript"), true, nil
	})
	panel.SetExecutionDetailProvider(func(
		request engine.AgentExecutionDetailRequest,
	) (engine.AgentExecutionDetail, bool, error) {
		detailCalls++
		output := "fresh output"
		if detailCalls > 1 {
			output = "stale output"
		}
		return p477ExecutionDetail(request, output), true, nil
	})
	panel.Show(taskExplorerExecutions, false)
	_, _ = panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyTab}, 30)
	_, _ = panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyRight}, 30)
	_, transcriptCmd := panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyRight}, 30)
	_, outputCmd := panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyRight}, 30)
	if transcriptCmd == nil || outputCmd == nil {
		t.Fatalf("initial commands = transcript:%v output:%v", transcriptCmd != nil, outputCmd != nil)
	}
	_, replacementCmd := panel.HandleKeyWithCmd(p476Key('r'), 30)
	if replacementCmd == nil {
		t.Fatal("explicit refresh did not supersede the in-flight output request")
	}

	replacement := replacementCmd().(taskExplorerExecutionDetailLoadedMsg)
	if !panel.applyExecutionDetail(replacement) {
		t.Fatal("newest output result was rejected")
	}
	if panel.applyExecutionDetail(replacement) {
		t.Fatal("duplicate output result was applied twice")
	}
	staleOutput := outputCmd().(taskExplorerExecutionDetailLoadedMsg)
	if panel.applyExecutionDetail(staleOutput) {
		t.Fatal("out-of-order superseded output result was applied")
	}
	staleTranscript := transcriptCmd().(agentTranscriptPageLoadedMsg)
	if panel.applyTranscriptPage(staleTranscript) {
		t.Fatal("result from the previous transcript tab was applied")
	}
	frame := panel.Render(120, 30)
	assertP476Contains(t, frame, "fresh output")
	assertP476Excludes(t, frame, "stale output", "stale transcript")

	_, inFlight := panel.HandleKeyWithCmd(p476Key('r'), 30)
	if inFlight == nil {
		t.Fatal("second exact output refresh did not start")
	}
	snapshot.Executions = []engine.TaskExplorerExecution{
		p477Execution("agent-a", 2, engine.TaskExplorerExecutionRunning),
	}
	panel.Refresh()
	replaced := inFlight().(taskExplorerExecutionDetailLoadedMsg)
	if panel.applyExecutionDetail(replaced) {
		t.Fatal("old generation result applied after selection replacement")
	}
	assertP476Excludes(t, panel.Render(120, 30), "fresh output", "stale output")

	app := New(Config{Resumed: true, ReducedMotion: true})
	app.taskExplorer.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
		return p477ExecutionSnapshot()
	})
	app.taskExplorer.SetExecutionDetailProvider(func(
		request engine.AgentExecutionDetailRequest,
	) (engine.AgentExecutionDetail, bool, error) {
		return p477ExecutionDetail(request, "closed panel output"), true, nil
	})
	app.taskExplorer.Show(taskExplorerExecutions, false)
	app.state = StateTaskPanel
	_ = app.handleTaskPanelKey(tea.KeyPressMsg{Code: tea.KeyTab})
	_ = app.handleTaskPanelKey(tea.KeyPressMsg{Code: tea.KeyRight})
	_ = app.handleTaskPanelKey(tea.KeyPressMsg{Code: tea.KeyRight})
	closedCmd := app.handleTaskPanelKey(tea.KeyPressMsg{Code: tea.KeyRight})
	if closedCmd == nil {
		t.Fatal("App did not return the lazy output command")
	}
	app.state = StateChat
	model, next := app.Update(closedCmd())
	if model != app || next != nil || strings.Contains(app.renderView(), "closed panel output") {
		t.Fatalf("closed panel result mutated UI: model=%T next=%v", model, next != nil)
	}
}

func TestP477RefilterReplacementInvalidatesLoadedAndInFlightDetail(t *testing.T) {
	snapshot := p477ExecutionSnapshot()
	snapshot.Executions = append(snapshot.Executions,
		p477Execution("agent-b", 1, engine.TaskExplorerExecutionRunning),
	)
	actionCalls := 0
	panel := NewTaskExplorerPanel(defaultStyles())
	panel.SetSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	panel.SetActionProvider(func(
		engine.TaskExplorerActionRequest,
	) engine.TaskExplorerActionResult {
		actionCalls++
		return engine.TaskExplorerActionResult{}
	})
	panel.SetExecutionDetailProvider(func(
		request engine.AgentExecutionDetailRequest,
	) (engine.AgentExecutionDetail, bool, error) {
		return p477ExecutionDetail(request, "detail for "+request.AgentID), true, nil
	})
	panel.Show(taskExplorerExecutions, false)
	_, _ = panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyTab}, 30)
	for index := 0; index < 3; index++ {
		_, _ = panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyRight}, 30)
	}
	_, firstCommand := panel.HandleKeyWithCmd(p476Key('r'), 30)
	if firstCommand == nil ||
		!panel.applyExecutionDetail(firstCommand().(taskExplorerExecutionDetailLoadedMsg)) {
		t.Fatal("agent-a output was not loaded")
	}
	assertP476Contains(t, panel.Render(120, 30), "detail for agent-a")

	_, inFlight := panel.HandleKeyWithCmd(p476Key('r'), 30)
	if inFlight == nil {
		t.Fatal("agent-a refresh did not start")
	}
	panel.search = "agent-b"
	panel.refilter()
	if panel.selection != (taskExplorerSelection{agentID: "agent-b", generation: 1}) {
		t.Fatalf("refilter selection = %#v", panel.selection)
	}
	if panel.detailTab != taskExplorerDetailOverview {
		t.Fatalf("refilter detail tab = %v, want overview", panel.detailTab)
	}
	if panel.applyExecutionDetail(inFlight().(taskExplorerExecutionDetailLoadedMsg)) {
		t.Fatal("agent-a in-flight detail applied after refilter replacement")
	}
	frame := panel.Render(120, 30)
	assertP476Contains(t, frame, "agent-b@g1", "Tabs: [overview] activity transcript output lineage")
	assertP476Excludes(t, frame, "detail for agent-a")
	if actionCalls != 0 || panel.switchTarget != nil {
		t.Fatalf("refilter detail changed action/navigation: actions=%d target=%#v", actionCalls, panel.switchTarget)
	}
}

func TestP477HistoricalDetailIsExplicitlyUnavailableAndNeverRebound(t *testing.T) {
	snapshot := p477ExecutionSnapshot()
	snapshot.Executions[0].Phase = engine.TaskExplorerExecutionReplayOnly
	snapshot.Executions[0].ReplayOnly = true
	panel := NewTaskExplorerPanel(defaultStyles())
	panel.SetSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	panel.SetTranscriptProvider(func(
		engine.AgentTranscriptPageRequest,
	) (engine.AgentTranscriptPage, bool, error) {
		return engine.AgentTranscriptPage{}, true, engine.ErrAgentTranscriptSelectionChanged
	})
	panel.SetExecutionDetailProvider(func(
		engine.AgentExecutionDetailRequest,
	) (engine.AgentExecutionDetail, bool, error) {
		return engine.AgentExecutionDetail{
			Agent: engine.RuntimeAgentSnapshot{
				AgentID: "agent-a", Generation: 2,
				SessionID: "newer-session", ThreadID: "newer-thread",
			},
			Output: "newer generation secret",
		}, true, engine.ErrAgentExecutionDetailSelectionChanged
	})
	panel.Show(taskExplorerExecutions, false)
	_, _ = panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyTab}, 30)
	_, _ = panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyRight}, 30)
	_, transcriptCmd := panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyRight}, 30)
	transcriptLoaded := transcriptCmd().(agentTranscriptPageLoadedMsg)
	if !panel.applyTranscriptPage(transcriptLoaded) {
		t.Fatal("historical transcript unavailability was not projected")
	}
	transcriptFrame := panel.Render(120, 30)
	assertP476Contains(t, transcriptFrame, "transcript is unavailable for this exact generation")

	_, outputCmd := panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyRight}, 30)
	outputLoaded := outputCmd().(taskExplorerExecutionDetailLoadedMsg)
	if !panel.applyExecutionDetail(outputLoaded) {
		t.Fatal("historical output unavailability was not projected")
	}
	outputFrame := panel.Render(120, 30)
	assertP476Contains(t, outputFrame, "output is unavailable for this exact generation")
	assertP476Excludes(t, outputFrame, "newer generation secret", "newer-session", "newer-thread")

	_, lineageCmd := panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyRight}, 30)
	lineageLoaded := lineageCmd().(taskExplorerExecutionDetailLoadedMsg)
	if !panel.applyExecutionDetail(lineageLoaded) {
		t.Fatal("historical lineage unavailability was not projected")
	}
	lineageFrame := panel.Render(120, 30)
	assertP476Contains(t, lineageFrame, "lineage is unavailable for this exact generation")
	assertP476Excludes(t, lineageFrame, "newer generation secret", "newer-session", "newer-thread")
}

func TestP477TerminalCurrentGenerationKeepsAllDeepTabsAvailable(t *testing.T) {
	snapshot := p477ExecutionSnapshot()
	snapshot.Executions[0].Phase = engine.TaskExplorerExecutionCompleted
	snapshot.Executions[0].Status = "completed"
	panel := NewTaskExplorerPanel(defaultStyles())
	panel.SetSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	panel.SetTranscriptProvider(func(
		request engine.AgentTranscriptPageRequest,
	) (engine.AgentTranscriptPage, bool, error) {
		page := p477TranscriptPage(request, "terminal transcript row")
		page.AttachMode = engine.ThreadModeReplayOnly
		page.Replay = true
		return page, true, nil
	})
	panel.SetExecutionDetailProvider(func(
		request engine.AgentExecutionDetailRequest,
	) (engine.AgentExecutionDetail, bool, error) {
		return p477ExecutionDetail(request, "terminal bounded output"), true, nil
	})
	panel.Show(taskExplorerExecutions, false)
	_, _ = panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyTab}, 30)
	_, _ = panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyRight}, 30)

	_, transcriptCommand := panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyRight}, 30)
	if transcriptCommand == nil ||
		!panel.applyTranscriptPage(transcriptCommand().(agentTranscriptPageLoadedMsg)) {
		t.Fatal("terminal current transcript was unavailable")
	}
	assertP476Contains(t, panel.Render(120, 30), "terminal transcript row")

	_, outputCommand := panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyRight}, 30)
	if outputCommand == nil ||
		!panel.applyExecutionDetail(outputCommand().(taskExplorerExecutionDetailLoadedMsg)) {
		t.Fatal("terminal current output was unavailable")
	}
	assertP476Contains(t, panel.Render(120, 30), "terminal bounded output")

	_, lineageCommand := panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyRight}, 30)
	if lineageCommand == nil ||
		!panel.applyExecutionDetail(lineageCommand().(taskExplorerExecutionDetailLoadedMsg)) {
		t.Fatal("terminal current lineage was unavailable")
	}
	assertP476Contains(t, panel.Render(120, 30), "Session: session-a", "Thread: shared-thread")
}

func TestP477TranscriptPagingCorrelatesOpaqueCursorAndRejectsDuplicate(t *testing.T) {
	snapshot := p477ExecutionSnapshot()
	requests := make([]engine.AgentTranscriptPageRequest, 0, 2)
	panel := NewTaskExplorerPanel(defaultStyles())
	panel.SetSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	panel.SetTranscriptProvider(func(
		request engine.AgentTranscriptPageRequest,
	) (engine.AgentTranscriptPage, bool, error) {
		requests = append(requests, request)
		page := p477TranscriptPage(request, "new transcript row")
		if request.Cursor == "" {
			page.HasMore = true
			page.NextCursor = "opaque-older"
		} else {
			page.Messages = []engine.AgentTranscriptMessage{
				p142cMessage("old-row", "old transcript row"),
			}
		}
		return page, true, nil
	})
	panel.Show(taskExplorerExecutions, false)
	_, _ = panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyTab}, 22)
	_, _ = panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyRight}, 22)
	_, firstCmd := panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyRight}, 22)
	first := firstCmd().(agentTranscriptPageLoadedMsg)
	if !panel.applyTranscriptPage(first) {
		t.Fatal("first transcript page was rejected")
	}
	_, olderCmd := panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyPgUp}, 22)
	if olderCmd == nil {
		t.Fatal("PgUp at transcript top did not request older cursor")
	}
	older := olderCmd().(agentTranscriptPageLoadedMsg)
	if older.request.cursor != "opaque-older" || len(requests) != 2 || requests[1].Cursor != "opaque-older" {
		t.Fatalf("older cursor = msg:%q requests:%#v", older.request.cursor, requests)
	}
	if !panel.applyTranscriptPage(older) || panel.applyTranscriptPage(older) {
		t.Fatal("older page was not applied exactly once")
	}
	frame := panel.Render(120, 30)
	assertP476Contains(t, frame, "old transcript row", "new transcript row")
}

func TestP477CachedReducerReplayRemainsReaderAndActionFree(t *testing.T) {
	capabilities := interactiveTerminalCaps()
	capabilities.Color = terminalcap.ColorNone
	app := New(Config{
		Resumed: true, ReducedMotion: true, TerminalCaps: &capabilities,
	})
	app.width, app.height = 80, 24
	app.updateLayout()
	snapshotCalls := 0
	actionCalls := 0
	transcriptCalls := 0
	detailCalls := 0
	app.taskExplorer.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
		snapshotCalls++
		return p477ExecutionSnapshot()
	})
	app.taskExplorer.SetActionProvider(func(
		engine.TaskExplorerActionRequest,
	) engine.TaskExplorerActionResult {
		actionCalls++
		return engine.TaskExplorerActionResult{}
	})
	app.taskExplorer.SetTranscriptProvider(func(
		engine.AgentTranscriptPageRequest,
	) (engine.AgentTranscriptPage, bool, error) {
		transcriptCalls++
		return engine.AgentTranscriptPage{}, false, nil
	})
	app.taskExplorer.SetExecutionDetailProvider(func(
		engine.AgentExecutionDetailRequest,
	) (engine.AgentExecutionDetail, bool, error) {
		detailCalls++
		return engine.AgentExecutionDetail{}, false, nil
	})
	app.taskExplorer.Show(taskExplorerExecutions, false)
	app.state = StateTaskPanel
	for _, msg := range []tea.Msg{
		tea.KeyPressMsg{Code: tea.KeyTab},
		tea.KeyPressMsg{Code: tea.KeyRight},
		tea.KeyPressMsg{Code: tea.KeyDown},
		tea.KeyPressMsg{Code: tea.KeyPgDown},
		tea.KeyPressMsg{Code: tea.KeyHome},
	} {
		model, cmd := app.Update(msg)
		if model != app || cmd != nil {
			t.Fatalf("cached reducer replay returned model=%T cmd=%v for %T", model, cmd != nil, msg)
		}
		first := app.renderView()
		second := app.renderView()
		if first != second || strings.Contains(first, "\x1b") {
			t.Fatalf("cached no-color render changed or emitted ANSI for %T", msg)
		}
	}
	if snapshotCalls != 1 || actionCalls != 0 || transcriptCalls != 0 || detailCalls != 0 {
		t.Fatalf(
			"cached replay providers: snapshot=%d action=%d transcript=%d detail=%d",
			snapshotCalls,
			actionCalls,
			transcriptCalls,
			detailCalls,
		)
	}
}

func p477ExecutionSnapshot() engine.TaskExplorerSnapshot {
	snapshot := p476DetailSnapshot()
	snapshot.WorkItems = snapshot.WorkItems[:1]
	snapshot.Executions = snapshot.Executions[:1]
	execution := &snapshot.Executions[0]
	execution.TranscriptPath = "/tmp/agent-a-g1.jsonl"
	execution.AllowedActions = []engine.TaskExplorerAction{
		engine.TaskExplorerActionInspect,
		engine.TaskExplorerActionSwitch,
		engine.TaskExplorerActionSend,
		engine.TaskExplorerActionPause,
		engine.TaskExplorerActionCancel,
		engine.TaskExplorerActionContinue,
	}
	return snapshot
}

func p477Execution(
	agentID string,
	generation int64,
	phase engine.TaskExplorerExecutionPhase,
) engine.TaskExplorerExecution {
	return engine.TaskExplorerExecution{
		Key:       engine.RuntimeExecutionKey{AgentID: agentID, Generation: generation},
		SessionID: "session-a", ThreadID: "shared-thread",
		TranscriptPath: "/tmp/" + agentID + ".jsonl",
		Status:         string(phase),
		Phase:          phase,
		AllowedActions: []engine.TaskExplorerAction{engine.TaskExplorerActionInspect},
	}
}

func p477TranscriptPage(
	request engine.AgentTranscriptPageRequest,
	content string,
) engine.AgentTranscriptPage {
	return engine.AgentTranscriptPage{
		Revision: 11, AgentID: request.AgentID,
		SessionID: "session-a", ThreadID: "shared-thread",
		Generation: request.Generation,
		AttachMode: engine.ThreadModeLiveAttach,
		Storage:    "durable",
		Messages: []engine.AgentTranscriptMessage{
			p142cMessage("current-row", content),
		},
	}
}

func p477ExecutionDetail(
	request engine.AgentExecutionDetailRequest,
	output string,
) engine.AgentExecutionDetail {
	return engine.AgentExecutionDetail{
		Revision: 11,
		Agent: engine.RuntimeAgentSnapshot{
			AgentID: request.AgentID, Generation: request.Generation,
			SessionID: request.SessionID, ThreadID: request.ThreadID,
			ParentSessionID: "parent-session", ParentThreadID: "parent-thread",
			ParentAgentID: "parent-agent", ParentToolUseID: "spawn-tool",
			CWD: "/workspace/exact", WorktreePath: "/workspace/exact/.worktree",
			WorktreeBranch: "codex/exact", TranscriptPath: "/tmp/agent-a-g1.jsonl",
			OutputFile: "/tmp/agent-a-g1.out",
		},
		Output: output,
	}
}

func TestP477ErrorSentinelsRemainComparable(t *testing.T) {
	if !errors.Is(engine.ErrAgentExecutionDetailSelectionChanged, engine.ErrAgentExecutionDetailSelectionChanged) {
		t.Fatal("exact detail stale sentinel is not errors.Is comparable")
	}
}
