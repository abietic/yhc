package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/abietic/yhc/engine"
)

func TestAgentToolTraceRendersBoundedParentSummaryAndLink(t *testing.T) {
	chat := NewChatView(defaultStyles())
	chat.SetSize(72, 12)
	chat.AppendOrUpdateTool("spawn-agent", "Agent", `{"description":"inspect runtime"}`)
	chat.UpdateToolResult("spawn-agent", "Agent", "raw full child output must stay out of the parent trace")
	trace := agentToolTrace{
		AgentID: "agent-1", ExecutionKey: engine.RuntimeExecutionKey{
			AgentID: "agent-1", Generation: 7,
		}, IdentityResolved: true,
		Status: "running", Summary: "Inspecting runtime state", LastToolName: "Bash",
		ToolUses: 3, TotalTokens: 1200, UnresolvedCount: 1,
		StartedAt: time.Now().Add(-2 * time.Second), UpdatedAt: time.Now(),
		RecentActivities: []agentToolTraceActivity{
			{ToolName: "Read", Description: "runtime_state.go"},
			{ToolName: "Grep", Description: "ParentToolUseID"},
			{ToolName: "Bash", Description: "go test ./engine"},
		},
	}
	if !chat.UpdateAgentToolTrace("spawn-agent", trace) {
		t.Fatal("Agent trace did not attach to spawning tool")
	}
	plain := stripANSIForTest(chat.Render(72, 12))
	for _, expected := range []string{"Agent", "running", "3 tools", "Inspecting runtime state", "Bash: go test ./engine", "1 request needs attention", "Open Agent details"} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("parent Agent trace missing %q: %q", expected, plain)
		}
	}
	if strings.Contains(plain, "raw full child output") {
		t.Fatalf("parent trace embedded full child output: %q", plain)
	}
	tool := chat.toolsByID["spawn-agent"]
	if tool == nil || tool.Finished() {
		t.Fatalf("running child should keep parent history item live: %#v", tool)
	}
	if key, ok := chat.LatestAgentTraceTarget(); !ok ||
		key != (engine.RuntimeExecutionKey{AgentID: "agent-1", Generation: 7}) {
		t.Fatalf("latest Agent target = %#v/%v", key, ok)
	}
	if key, ok := chat.AgentTraceTargetAtViewportRow(11); !ok ||
		key != (engine.RuntimeExecutionKey{AgentID: "agent-1", Generation: 7}) {
		t.Fatalf("link row Agent target = %#v/%v", key, ok)
	}

	trace.Status = "failed"
	trace.TerminalReason = "model_error"
	trace.Error = "upstream failed"
	trace.UnresolvedCount = 0
	trace.CompletedAt = time.Now()
	if !chat.UpdateAgentToolTrace("spawn-agent", trace) || !tool.Finished() {
		t.Fatalf("terminal trace did not freeze parent item: %#v", tool)
	}
	terminal := stripANSIForTest(chat.Render(72, 12))
	for _, expected := range []string{"failed", "model_error", "upstream failed"} {
		if !strings.Contains(terminal, expected) {
			t.Fatalf("terminal parent trace missing %q: %q", expected, terminal)
		}
	}
	chat.SetSize(28, 12)
	for i, line := range strings.Split(chat.Render(28, 12), "\n") {
		if width := xansi.StringWidth(line); width > 28 {
			t.Fatalf("narrow trace line %d width = %d: %q", i, width, line)
		}
	}
}

func TestAppSyncAgentTraceAndCtrlBOpensExistingDetail(t *testing.T) {
	app := New(Config{})
	app.chat.AppendOrUpdateTool("spawn-agent", "Agent", `{"description":"inspect runtime"}`)
	app.chat.UpdateToolResult("spawn-agent", "Agent", "background Agent started")
	now := time.Now()
	app.agentTraceProvider = func() []engine.AgentParentTraceSnapshot {
		return []engine.AgentParentTraceSnapshot{{
			AgentID: "agent-1", ParentToolUseID: "spawn-agent", Status: "running", StartedAt: now,
		}}
	}
	explorer := p313ExplorerSnapshot()
	execution := p313Execution(
		"agent-1",
		2,
		engine.TaskExplorerExecutionRunning,
	)
	execution.ParentToolUseID = "spawn-agent"
	explorer.Executions = []engine.TaskExplorerExecution{execution}
	installP313ExplorerSnapshot(app, &explorer)
	app.backgroundTasks.SetDetailProvider(func(agentID string) (engine.AgentDetailSnapshot, bool) {
		return engine.AgentDetailSnapshot{
			Revision: 1,
			Agent: engine.RuntimeAgentSnapshot{
				AgentID: "agent-1", Generation: 2,
				Description: "inspect runtime", Status: "running",
			},
			Thread: engine.RuntimeThreadSnapshot{Status: engine.RuntimeThreadRunning},
		}, agentID == "agent-1"
	})
	if !app.syncAgentToolTraces() {
		t.Fatal("running Agent trace was not reported active")
	}
	if first := app.ensureSpinnerTick(); first == nil {
		t.Fatal("active Agent did not schedule trace polling")
	}
	if duplicate := app.ensureSpinnerTick(); duplicate != nil {
		t.Fatal("active Agent scheduled a duplicate spinner loop")
	}
	_, nextTick := app.Update(spinnerTickMsg{})
	if nextTick == nil || !app.spinnerTickScheduled {
		t.Fatal("Agent trace polling did not continue after its scheduled tick")
	}
	app.enterBackgroundTasks()
	if app.state != StateBackgroundTasks || app.backgroundTasks.subView != bgTaskViewOutput || app.backgroundTasks.detailAgent != "agent-1" {
		t.Fatalf("Ctrl+B did not open linked Agent detail: state=%v panel=%#v", app.state, app.backgroundTasks)
	}

	app.popDialog(StateBackgroundTasks)
	app.backgroundTasks.Close()
	app.state = StateChat
	_, _ = app.Update(tea.WindowSizeMsg{Width: 72, Height: 20})
	_ = app.View()
	projection := app.chat.currentViewportProjection()
	linkRow := -1
	for row := range projection.rows {
		if key, ok := app.chat.AgentTraceTargetAtViewportRow(row); ok &&
			key == (engine.RuntimeExecutionKey{AgentID: "agent-1", Generation: 2}) {
			linkRow = row
			break
		}
	}
	if linkRow < 0 {
		t.Fatalf("published Agent trace link row not found: %#v", projection.rows)
	}
	linkY := app.layout.chatRect.Y + linkRow
	_, _ = app.Update(tuiMouseMsg{X: 8, Y: linkY, Button: tea.MouseLeft, Action: mouseActionPress})
	_, _ = app.Update(tuiMouseMsg{X: 8, Y: linkY, Button: tea.MouseLeft, Action: mouseActionRelease})
	if app.state != StateBackgroundTasks || app.backgroundTasks.detailAgent != "agent-1" {
		t.Fatalf("Agent trace link click did not open detail: state=%v panel=%#v", app.state, app.backgroundTasks)
	}
}
