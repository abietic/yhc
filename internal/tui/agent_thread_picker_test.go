package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/keybindings"
)

func TestAgentThreadPickerUsesStableSpawnOrderAndSearch(t *testing.T) {
	catalog := threadNavigationTestCatalog()
	picker := NewAgentThreadPicker(defaultStyles())
	picker.Show(catalog.Threads, "child-beta", "leader-thread")

	want := []string{"leader-thread", "child-alpha", "child-beta"}
	if len(picker.items) != len(want) {
		t.Fatalf("picker items = %#v", picker.items)
	}
	for i, threadID := range want {
		if picker.items[i].ThreadID != threadID {
			t.Fatalf("picker order[%d] = %q, want %q", i, picker.items[i].ThreadID, threadID)
		}
	}
	selected, ok := picker.selectedItem()
	if !ok || selected.ThreadID != "child-beta" {
		t.Fatalf("initial picker selection = %#v %v", selected, ok)
	}

	picker.input.SetValue("alpha scout")
	picker.refilter("")
	selected, ok = picker.selectedItem()
	if !ok || len(picker.filtered) != 1 || selected.ThreadID != "child-alpha" {
		t.Fatalf("filtered picker = %#v selected=%#v", picker.filtered, selected)
	}
	line := picker.renderItem(selected, 24)
	if xansi.StringWidth(line) > 24 {
		t.Fatalf("narrow picker row width = %d: %q", xansi.StringWidth(line), line)
	}
	overlay := picker.Overlay(strings.Repeat("\n", 12), 32, 14)
	if !strings.Contains(stripANSIForTest(overlay), "alpha scout") {
		t.Fatalf("narrow picker overlay omitted selected row:\n%s", overlay)
	}
	for _, line := range strings.Split(overlay, "\n") {
		if width := xansi.StringWidth(line); width > 32 {
			t.Fatalf("narrow picker overlay line width = %d: %q", width, line)
		}
	}

	picker.input.SetValue("main")
	picker.refilter("")
	selected, ok = picker.selectedItem()
	if !ok || len(picker.filtered) != 1 || selected.ThreadID != "leader-thread" {
		t.Fatalf("visible leader label was not searchable: filtered=%#v selected=%#v", picker.filtered, selected)
	}
}

func TestAgentThreadNavigationRestoresLeaderAndProjectsClosedChild(t *testing.T) {
	app, _, _ := newThreadNavigationTestApp(t)
	app.chat.AppendSystem("leader history")
	app.textarea.SetValue("leader draft")

	app.openAgentThreadPicker()
	if app.state != StateAgentPicker || !app.agentPicker.Visible() {
		t.Fatal("exact Agent picker did not open")
	}
	app.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	app.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if app.activeThreadViewID() != "child-alpha" || app.activeThreadViewMode() != engine.ThreadModeReplayOnly {
		t.Fatalf("active child = %q mode=%q", app.activeThreadViewID(), app.activeThreadViewMode())
	}
	childText := stripANSIForTest(app.chat.RenderAllExpanded(80))
	for _, want := range []string{"inspect runtime", "read main.go", "package main", "analysis complete"} {
		if !strings.Contains(childText, want) {
			t.Fatalf("projected child transcript missing %q:\n%s", want, childText)
		}
	}
	if got := stripANSIForTest(app.renderStatus()); !strings.Contains(got, "thread:@alpha scout") {
		t.Fatalf("active-thread status = %q", got)
	}

	app.textarea.SetValue("child draft")
	app.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModAlt})
	if app.activeThreadViewID() != "leader-thread" || app.textarea.Value() != "leader draft" || len(app.chat.Items()) != 1 {
		t.Fatalf("leader restore = thread:%q draft:%q items:%d", app.activeThreadViewID(), app.textarea.Value(), len(app.chat.Items()))
	}

	app.keybindResolver.SetBindings([]keybindings.Block{{
		Context: keybindings.ContextChat,
		Bindings: map[string]keybindings.Action{
			"alt+up": keybindings.ActionChatNextAgent,
		},
	}})
	app.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt})
	if app.activeThreadViewID() != "child-alpha" || app.textarea.Value() != "child draft" {
		t.Fatalf("custom next-Agent binding = thread:%q draft:%q", app.activeThreadViewID(), app.textarea.Value())
	}
}

func TestAgentThreadExactSlashCommandKeepsDefinitionSubcommands(t *testing.T) {
	app, _, _ := newThreadNavigationTestApp(t)
	app.running = true
	app.textarea.SetValue("/agent")
	app.inputMode = InputCommand
	app.sendMessage()
	if app.state != StateAgentPicker {
		t.Fatalf("/agent state = %v", app.state)
	}

	app.agentPicker.Close()
	app.state = StateChat
	app.running = false
	app.textarea.SetValue("/agent list")
	app.inputMode = InputCommand
	app.sendMessage()
	if app.state == StateAgentPicker {
		t.Fatal("/agent list was incorrectly captured by the runtime picker")
	}
}

func TestAgentThreadRefreshKeepsTerminalVisibleAndFailsOverWhenMissing(t *testing.T) {
	app, catalog, details := newThreadNavigationTestApp(t)
	if err := app.activateThreadByID("child-alpha"); err != nil {
		t.Fatal(err)
	}
	detail := details["agent-alpha"]
	for i := 0; i < 20; i++ {
		detail.Messages = append(detail.Messages, engine.AgentDetailMessage{
			ID: fmt.Sprintf("history-%d", i), Role: "assistant",
			Content: fmt.Sprintf("history row %d", i), Completed: true,
		})
	}
	detail.Revision = 2
	details["agent-alpha"] = detail
	app.refreshActiveThreadProjection()
	app.chat.Render(app.layout.chatRect.Width, app.layout.chatHeight)
	app.chat.ScrollToTop()
	if app.chat.Following() {
		t.Fatal("precondition: scrollable terminal child did not leave follow")
	}

	detail.Revision = 3
	detail.Messages = append(detail.Messages, engine.AgentDetailMessage{
		ID: "assistant-2", Role: "assistant", Content: "late terminal note", Completed: true,
	})
	details["agent-alpha"] = detail
	if running := app.refreshActiveThreadProjection(); running {
		t.Fatal("completed child reported running")
	}
	pill := app.chat.pillModel()
	if app.activeThreadViewID() != "child-alpha" || app.chat.Following() ||
		app.chat.followState.baselineValid || !pill.visible ||
		pill.label != "Jump to bottom" || pill.action != chatPillActionFollow {
		t.Fatalf("terminal child was not kept inspectable: thread=%q follow=%v state=%#v pill=%#v",
			app.activeThreadViewID(), app.chat.Following(), app.chat.followState, pill)
	}
	if text := stripANSIForTest(app.chat.RenderAllExpanded(80)); !strings.Contains(text, "late terminal note") {
		t.Fatalf("refreshed child transcript missing note:\n%s", text)
	}

	catalog.Threads = catalog.Threads[:1]
	app.refreshActiveThreadProjection()
	if app.activeThreadViewID() != "leader-thread" {
		t.Fatalf("missing visible child did not fail over: %q", app.activeThreadViewID())
	}
}

func TestAgentThreadComposerSendsToChildWithoutInterruptingLeader(t *testing.T) {
	app, _, _ := newThreadNavigationTestApp(t)
	var sentAgent, sentContent string
	app.taskExplorerActionProvider = func(request engine.TaskExplorerActionRequest) engine.TaskExplorerActionResult {
		sentAgent, sentContent = request.AgentID, request.Payload
		return engine.TaskExplorerActionResult{RequestID: request.RequestID, BoardID: request.BoardID, BoardRevision: request.BoardRevision, RuntimeRevision: request.RuntimeRevision, AgentID: request.AgentID, Generation: request.Generation, MessageID: request.RequestID, Action: request.Action, Outcome: "sent"}
	}
	if err := app.activateThreadByID("child-beta"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	app.running = true
	app.cancelFn = cancel
	app.textarea.SetValue("follow up on tests")
	app.sendMessage()

	if sentAgent != "agent-beta" || sentContent != "follow up on tests" {
		t.Fatalf("child send = agent:%q content:%q", sentAgent, sentContent)
	}
	select {
	case <-ctx.Done():
		t.Fatal("sending to child interrupted the running leader")
	default:
	}
	if !app.running || app.textarea.Value() != "" || len(app.queuedInputPreview) != 1 {
		t.Fatalf("child send state = running:%v draft:%q queue:%#v", app.running, app.textarea.Value(), app.queuedInputPreview)
	}
	view := app.threadViews.active()
	if view == nil || view.refreshTicks != 50 || !app.refreshActiveThreadProjection() || view.refreshTicks != 0 {
		t.Fatalf("resume refresh lease = %#v", view)
	}
}

func TestAgentThreadQueuedInputCanBeRestoredForEditing(t *testing.T) {
	app, _, _ := newThreadNavigationTestApp(t)
	if err := app.activateThreadByID("child-alpha"); err != nil {
		t.Fatal(err)
	}
	app.queuedInputPreview = []threadQueuedInput{{
		ID: "child-queued-1", AgentID: "agent-alpha", Generation: 1, BoardID: "thread-test", BoardRevision: 3, Content: "refine child work",
		Parts: []engine.QueuedPromptPart{{Kind: engine.QueuedPromptPartText, Text: "refine child work"}},
	}}
	var cancelledAgent, cancelledID string
	app.taskExplorerActionProvider = func(request engine.TaskExplorerActionRequest) engine.TaskExplorerActionResult {
		cancelledAgent, cancelledID = request.AgentID, request.MessageID
		return engine.TaskExplorerActionResult{RequestID: request.RequestID, BoardID: request.BoardID, BoardRevision: request.BoardRevision, RuntimeRevision: request.RuntimeRevision, AgentID: request.AgentID, Generation: request.Generation, MessageID: request.MessageID, Action: request.Action, Outcome: "input_cancelled"}
	}

	app.handleQueueSlashCommand("/queue edit last")
	if cancelledAgent != "agent-alpha" || cancelledID != "child-queued-1" {
		t.Fatalf("child cancel = %q %q", cancelledAgent, cancelledID)
	}
	if app.textarea.Value() != "refine child work" || len(app.queuedInputPreview) != 0 {
		t.Fatalf("child queue edit: draft=%q preview=%#v", app.textarea.Value(), app.queuedInputPreview)
	}
}

func newThreadNavigationTestApp(t *testing.T) (*App, *engine.RuntimeThreadCatalogSnapshot, map[string]engine.AgentDetailSnapshot) {
	t.Helper()
	app := New(Config{Resumed: true})
	app.rebindLeaderThreadView("leader-thread")
	app.width = 80
	app.height = 24
	app.updateLayout()
	catalog := threadNavigationTestCatalog()
	details := map[string]engine.AgentDetailSnapshot{
		"agent-alpha": {
			Revision: 1,
			Agent: engine.RuntimeAgentSnapshot{
				AgentID: "agent-alpha", ThreadID: "child-alpha", Name: "alpha scout", Status: "completed",
			},
			Thread: engine.RuntimeThreadSnapshot{ThreadID: "child-alpha", AgentID: "agent-alpha", Status: engine.RuntimeThreadCompleted},
			Messages: []engine.AgentDetailMessage{
				{ID: "user-1", Role: "user", Content: "inspect runtime", Completed: true},
				{ID: "assistant-1", Role: "assistant", Content: "read main.go", Completed: true, ToolCalls: []engine.RuntimeToolCallSnapshot{{ID: "read-1", Name: "Read", InputPreview: `{"file_path":"main.go"}`}}},
				{ID: "tool-1", Role: "tool", ToolCallID: "read-1", ToolName: "Read", Content: "package main", Completed: true},
				{ID: "assistant-final", Role: "assistant", Content: "analysis complete", Completed: true},
			},
		},
		"agent-beta": {
			Revision: 1,
			Agent: engine.RuntimeAgentSnapshot{
				AgentID: "agent-beta", ThreadID: "child-beta", Name: "beta builder", Status: "running",
			},
			Thread:   engine.RuntimeThreadSnapshot{ThreadID: "child-beta", AgentID: "agent-beta", Status: engine.RuntimeThreadRunning},
			Messages: []engine.AgentDetailMessage{{ID: "user-beta", Role: "user", Content: "build feature", Completed: true}},
		},
	}
	app.threadCatalogProvider = func() engine.RuntimeThreadCatalogSnapshot { return catalog }
	app.taskExplorerSnapshotSource = func() engine.TaskExplorerSnapshot {
		return engine.TaskExplorerSnapshot{Available: true, BoardID: "thread-test", Revision: engine.TaskExplorerRevision{Board: 3, Runtime: 3}, Executions: []engine.TaskExplorerExecution{
			{Key: engine.RuntimeExecutionKey{AgentID: "agent-alpha", Generation: 1}, ThreadID: "child-alpha", Name: "alpha scout", Status: "completed", Phase: engine.TaskExplorerExecutionCompleted, AllowedActions: []engine.TaskExplorerAction{engine.TaskExplorerActionInspect, engine.TaskExplorerActionSwitch}},
			{Key: engine.RuntimeExecutionKey{AgentID: "agent-beta", Generation: 1}, ThreadID: "child-beta", Name: "beta builder", Status: "running", Phase: engine.TaskExplorerExecutionRunning, AllowedActions: []engine.TaskExplorerAction{engine.TaskExplorerActionInspect, engine.TaskExplorerActionSwitch, engine.TaskExplorerActionSend, engine.TaskExplorerActionCancelInput}},
		}}
	}
	app.agentDetailProvider = func(agentID string) (engine.AgentDetailSnapshot, bool) {
		detail, ok := details[agentID]
		return detail, ok
	}
	return app, &catalog, details
}

func threadNavigationTestCatalog() engine.RuntimeThreadCatalogSnapshot {
	base := time.Date(2026, 7, 10, 18, 0, 0, 0, time.UTC)
	return engine.RuntimeThreadCatalogSnapshot{
		Revision: 3, ActiveThreadID: "leader-thread",
		Threads: []engine.RuntimeThreadCatalogEntry{
			{ThreadID: "child-beta", AgentID: "agent-beta", Name: "beta builder", Description: "implement UI", Status: engine.RuntimeThreadRunning, Mode: engine.ThreadModeLiveAttach, StartedAt: base.Add(2 * time.Second)},
			{ThreadID: "leader-thread", Status: engine.RuntimeThreadRunning, Mode: engine.ThreadModeLiveAttach, StartedAt: base},
			{ThreadID: "child-alpha", AgentID: "agent-alpha", Name: "alpha scout", Description: "inspect runtime", Status: engine.RuntimeThreadCompleted, Mode: engine.ThreadModeReplayOnly, StartedAt: base.Add(time.Second)},
		},
	}
}
