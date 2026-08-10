package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/terminalcap"
)

func TestAgentMonitorRendersCanonicalExplorerStatesAcrossResponsiveWidths(t *testing.T) {
	snapshot := agentMonitorFixture()
	panel := NewTeamsPanel(defaultStyles())
	panel.SetExplorerSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	panel.Show()
	for _, width := range []int{40, 80, 140} {
		view := panel.Overlay(strings.Repeat("\n", 32), width, 32)
		plain := stripANSIForTest(view)
		for _, want := range []string{"foreground", "background", "backgrounded", "waiting-input", "paused", "completed", "failed", "aborted"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("width %d omitted %q:\n%s", width, want, plain)
			}
		}
		for _, line := range strings.Split(view, "\n") {
			if got := xansi.StringWidth(line); got > width {
				t.Fatalf("width %d rendered line width %d: %q", width, got, line)
			}
		}
	}
}

func TestAgentMonitorNoColorAndRefreshKeepExplorerFacts(t *testing.T) {
	caps := interactiveTerminalCaps()
	caps.Color = terminalcap.ColorNone
	app := New(Config{Resumed: true, ReducedMotion: true, TerminalCaps: &caps})
	app.width, app.height = 80, 30
	app.updateLayout()
	snapshot := agentMonitorFixture()
	app.teamsPanel.SetExplorerSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	app.enterTeams()
	view := app.renderView()
	if strings.Contains(view, "\x1b") {
		t.Fatalf("NO_COLOR monitor contains styles: %q", view)
	}
	for _, want := range []string{"foreground", "backgrounded", "waiting-input", "completed", "failed", "aborted"} {
		if !strings.Contains(view, want) {
			t.Fatalf("NO_COLOR monitor omitted %q:\n%s", want, view)
		}
	}
	for index := range app.teamsPanel.items {
		if app.teamsPanel.items[index].id == "bg" {
			app.teamsPanel.cursor = index
		}
	}
	snapshot.Executions[1].Phase = engine.TaskExplorerExecutionCompleted
	snapshot.Executions[1].Status, snapshot.Executions[1].Activity = "completed", "background work complete"
	snapshot.Revision.Runtime++
	app.teamsPanel.Refresh()
	if app.teamsPanel.items[app.teamsPanel.cursor].id != "bg" {
		t.Fatalf("refresh moved selection to %q", app.teamsPanel.items[app.teamsPanel.cursor].id)
	}
	if cmd := app.handleTeamsDialogKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: "r"}); cmd != nil {
		t.Fatal("read-only refresh unexpectedly scheduled runtime work")
	}
}

func TestAgentMonitorPeekRejectsStaleTranscriptPage(t *testing.T) {
	snapshot := p142cSnapshot()
	panel := NewTeamsPanel(defaultStyles())
	panel.SetExplorerSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	panel.SetTranscriptSelectionProvider(func(agentID string) (agentTranscriptSelection, bool) {
		for _, execution := range snapshot.Executions {
			if execution.Key.AgentID == agentID {
				return agentTranscriptSelection{AgentID: agentID, ThreadID: execution.ThreadID, Generation: execution.Key.Generation, Mode: engine.ThreadModeReplayOnly}, true
			}
		}
		return agentTranscriptSelection{}, false
	})
	panel.SetTranscriptProvider(func(request engine.AgentTranscriptPageRequest) (engine.AgentTranscriptPage, bool, error) {
		page := p142cPage(request, engine.ThreadModeReplayOnly)
		page.Messages = []engine.AgentTranscriptMessage{p142cMessage(request.AgentID+"-row", request.AgentID+" transcript")}
		return page, true, nil
	})
	panel.Show()
	_, alphaCmd := panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyTab}, 24)
	if alphaCmd == nil {
		t.Fatal("peek did not start alpha page")
	}
	alphaLoaded := alphaCmd().(agentTranscriptPageLoadedMsg)
	panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyEsc}, 24)
	panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyDown}, 24)
	_, betaCmd := panel.HandleKeyWithCmd(tea.KeyPressMsg{Code: tea.KeyTab}, 24)
	if betaCmd == nil {
		t.Fatal("peek did not start beta page")
	}
	if panel.applyTranscriptPage(alphaLoaded) {
		t.Fatal("stale alpha preview applied after beta selection")
	}
	if !panel.applyTranscriptPage(betaCmd().(agentTranscriptPageLoadedMsg)) {
		t.Fatal("current beta preview did not apply")
	}
	plain := stripANSIForTest(panel.renderDetailView(72, 24))
	if !strings.Contains(plain, "agent-b transcript") || strings.Contains(plain, "agent-a transcript") {
		t.Fatalf("peek crossed identity boundary:\n%s", plain)
	}
}

func TestAgentMonitorSwitchAndReturnPreserveLeaderDraftCursorAndFocus(t *testing.T) {
	app, catalog, details := newThreadNavigationTestApp(t)
	app.teamsPanel.SetExplorerSnapshotProvider(app.taskExplorerSnapshotSource)
	app.teamsPanel.SetDetailProvider(app.agentDetailProvider)

	app.textarea.SetValue("leader draft")
	app.textarea.SetCursorColumn(3)
	app.textarea.Focus()
	app.enterTeams()
	app.handleTeamsDialogKey(tea.KeyPressMsg{Code: tea.KeyTab})
	app.handleTeamsDialogKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	app.handleTeamsDialogKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if app.activeThreadViewID() != "leader-thread" ||
		app.textarea.Value() != "leader draft" ||
		!app.textarea.Focused() {
		t.Fatalf(
			"closing peek changed leader state: thread=%q draft=%q focused=%v",
			app.activeThreadViewID(),
			app.textarea.Value(),
			app.textarea.Focused(),
		)
	}

	app.enterTeams()
	app.handleTeamsDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if app.activeThreadViewID() != "child-beta" {
		t.Fatalf(
			"monitor switch selected %q, want child-beta; catalog=%#v details=%#v",
			app.activeThreadViewID(),
			catalog.Threads,
			details,
		)
	}
	app.textarea.SetValue("child draft")
	if err := app.activateThreadByID("leader-thread"); err != nil {
		t.Fatal(err)
	}
	line := app.textarea.LineInfo()
	if app.textarea.Value() != "leader draft" ||
		line.StartColumn+line.ColumnOffset != 3 ||
		!app.textarea.Focused() {
		t.Fatalf(
			"leader restore = draft:%q cursor:%d focused:%v",
			app.textarea.Value(),
			line.StartColumn+line.ColumnOffset,
			app.textarea.Focused(),
		)
	}
}

func agentMonitorFixture() engine.TaskExplorerSnapshot {
	base := time.Now().Add(-90 * time.Second)
	rows := []struct {
		id, status, mode, activity, err string
		phase                           engine.TaskExplorerExecutionPhase
	}{
		{"f", "running", "foreground", "Read", "", engine.TaskExplorerExecutionRunning}, {"bg", "running", "background", "Bash", "", engine.TaskExplorerExecutionRunning}, {"det", "running", "backgrounded", "Bash", "", engine.TaskExplorerExecutionRunning}, {"ask", "waiting-input", "foreground", "needs approval", "", engine.TaskExplorerExecutionWaitingInput}, {"pause", "paused", "background", "paused work", "", engine.TaskExplorerExecutionPaused}, {"done", "completed", "", "done", "", engine.TaskExplorerExecutionCompleted}, {"fail", "failed", "", "", "quota exhausted", engine.TaskExplorerExecutionFailed}, {"abort", "aborted", "", "", "user aborted", engine.TaskExplorerExecutionCancelled},
	}
	snapshot := p313ExplorerSnapshot()
	snapshot.Revision.Runtime = 7
	for i, row := range rows {
		snapshot.Executions = append(snapshot.Executions, engine.TaskExplorerExecution{Key: engine.RuntimeExecutionKey{AgentID: row.id, Generation: 1}, SessionID: "session-" + row.id, ThreadID: "thread-" + row.id, Name: row.id, Task: row.activity, Activity: row.activity, Status: row.status, DisplayMode: row.mode, Phase: row.phase, Attention: func() []string {
			if row.id == "ask" {
				return []string{"approval", "approval"}
			}
			return nil
		}(), AllowedActions: []engine.TaskExplorerAction{engine.TaskExplorerActionInspect, engine.TaskExplorerActionSwitch}})
		_ = base.Add(time.Duration(i) * time.Second)
	}
	return snapshot
}
