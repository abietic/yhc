package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/internal/tui/attachments"
)

func TestProductStatesCurrentOutputGolden(t *testing.T) {
	var got strings.Builder
	appendProductGoldenSection(&got, "tools", renderProductToolGolden())
	appendProductGoldenSection(&got, "agent", renderProductAgentGolden(t))
	appendProductGoldenSection(&got, "permission", renderProductPermissionGolden())
	appendProductGoldenSection(&got, "composer", renderProductComposerGolden())
	appendProductGoldenSection(&got, "responsive", renderProductResponsiveGolden(t))
	appendProductGoldenSection(&got, "baseline_leader", renderProductSurfaceMatrix(productLeaderBaseline))
	appendProductGoldenSection(&got, "baseline_permission", renderProductSurfaceMatrix(productPermissionBaseline))
	appendProductGoldenSection(&got, "baseline_resume", renderProductSurfaceMatrix(productResumeBaseline))
	appendProductGoldenSection(&got, "baseline_background", renderProductSurfaceMatrix(productBackgroundBaseline))
	appendProductGoldenSection(&got, "baseline_team", renderProductSurfaceMatrix(productTeamBaseline))

	path := "testdata/product_states.golden"
	actual := strings.TrimSpace(got.String()) + "\n"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(actual), 0o600); err != nil {
			t.Fatalf("update product states golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read product states golden: %v", err)
	}
	if actual != string(want) {
		t.Fatalf("product states golden mismatch:\n--- got ---\n%s\n--- want ---\n%s", actual, want)
	}
}

func appendProductGoldenSection(output *strings.Builder, name, content string) {
	output.WriteString("== " + name + " ==\n")
	output.WriteString(normalizeAppLayoutGolden(content))
	output.WriteString("\n")
}

func renderProductToolGolden() string {
	read := &ToolMessage{
		name: "Read", input: `{"file_path":"internal/tui/app.go","offset":120,"limit":4}`,
		output: "120 package tui\n121\n122 func (a *App) View() string {\n123     return view\n", status: ToolSuccess, version: 1,
	}
	bash := &ToolMessage{
		name: "Bash", input: `{"command":"make test"}`,
		output: "$ make test\nok engine\nok internal/tui\n[exit code: 0]\n", status: ToolSuccess, version: 1,
	}
	custom := &ToolMessage{
		name: "CustomIndex", input: `{"query":"thread ownership"}`,
		output: "3 canonical matches", status: ToolSuccess, version: 1,
	}
	agent := &ToolMessage{
		name: "Agent", input: `{"description":"inspect TUI state"}`,
		status: ToolSuccess, version: 1,
		agentTrace: &agentToolTrace{
			AgentID: "agent-7", Status: "completed", Summary: "Mapped thread and modal ownership",
			LastToolName: "Grep", ToolUses: 4, TotalTokens: 1800, TerminalReason: "completed",
			StartedAt:        time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC),
			CompletedAt:      time.Date(2026, 7, 11, 9, 0, 2, 0, time.UTC),
			RecentActivities: []agentToolTraceActivity{{ToolName: "Grep", Description: "dialog ownership"}},
		},
	}
	return strings.Join([]string{
		read.Render(72, defaultStyles()),
		bash.Render(72, defaultStyles()),
		custom.Render(72, defaultStyles()),
		agent.Render(72, defaultStyles()),
	}, "\n")
}

func renderProductAgentGolden(t *testing.T) string {
	t.Helper()
	app, _, _ := newThreadNavigationTestApp(t)
	if err := app.activateThreadByID("child-alpha"); err != nil {
		t.Fatal(err)
	}
	app.statusLineHook = func(_, _ string) (string, string) {
		return "  default · thread:@alpha scout", "test-model"
	}
	app.updateLayout()
	return app.renderView()
}

func renderProductPermissionGolden() string {
	app := productGoldenApp(72, 24, nil)
	app.chat.AppendUser("Run the complete verification gates.")
	app.dialog.Show("Bash", `{"command":"make fmt && make lint && make test"}`, "project tests", make(chan PermissionResponse, 1))
	app.pushDialog(StatePermission)
	return app.renderView()
}

func renderProductComposerGolden() string {
	app := productGoldenApp(80, 24, nil)
	app.chat.AppendSystem("Ready for the next change.")
	pasted := strings.Repeat("fixture line\n", attachments.PasteThreshold/len("fixture line\n")+2)
	handlePaste(app, pasted)
	app.textarea.InsertString(" verify after paste")
	app.queuedInputPreview = []threadQueuedInput{{
		ID: "queued-1", Content: "Run focused tests after the current turn",
	}}
	app.updateLayout()
	return app.renderView()
}

func renderProductResponsiveGolden(t *testing.T) string {
	t.Helper()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{SessionID: "responsive-session", CWD: t.TempDir()})
	t.Cleanup(eng.Close)
	app := productGoldenApp(40, 20, eng)
	explorer := p313ExplorerSnapshot(engine.TaskExplorerWorkItem{
		BoardID: "board", WorkItemID: "task-golden", Title: "Golden capture",
		ActiveForm: "Rendering goldens", Status: "in_progress",
	})
	explorer.Executions = []engine.TaskExplorerExecution{
		p313Execution(
			"agent-layout",
			1,
			engine.TaskExplorerExecutionRunning,
		),
	}
	explorer.Executions[0].Task = "Layout scout"
	installP313ExplorerSnapshot(app, &explorer)
	app.chat.AppendUser("Inspect the modern TUI.")
	app.chat.AppendOrUpdateAssistant("Thread state, tools, and composer remain readable at every supported width.")
	app.chat.FinishAssistant()
	app.textarea.SetValue("follow up")

	var output strings.Builder
	for _, viewport := range []struct {
		width  int
		height int
	}{{40, 20}, {80, 30}, {120, 30}, {180, 30}} {
		app.width = viewport.width
		app.height = viewport.height
		app.updateLayout()
		output.WriteString("-- ")
		output.WriteString(productViewportLabel(viewport.width, viewport.height, app.layout.mode))
		output.WriteString(" --\n")
		output.WriteString(app.renderView())
		output.WriteString("\n")
	}
	return output.String()
}

func productGoldenApp(width, height int, eng *engine.QueryEngine) *App {
	app := New(Config{Engine: eng, Resumed: true, Model: "test-model", ReducedMotion: true})
	app.width = width
	app.height = height
	app.state = StateChat
	app.sessionStart = time.Time{}
	app.statusLineHook = fixedProductStatus
	app.updateLayout()
	return app
}

func fixedProductStatus(_, _ string) (string, string) {
	return "  default · thread:main", "test-model"
}

func productViewportLabel(width, height int, mode responsiveLayoutMode) string {
	return strings.Join([]string{
		"width=" + productInt(width),
		"height=" + productInt(height),
		"mode=" + string(mode),
	}, " ")
}

func productInt(value int) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var reversed [20]byte
	index := len(reversed)
	for value > 0 {
		index--
		reversed[index] = digits[value%10]
		value /= 10
	}
	return string(reversed[index:])
}

func renderProductSurfaceMatrix(build func(int, int) *App) string {
	var output strings.Builder
	for _, width := range []int{40, 80, 120, 180} {
		height := 30
		app := build(width, height)
		output.WriteString("-- width=")
		output.WriteString(productInt(width))
		output.WriteString(" height=")
		output.WriteString(productInt(height))
		output.WriteString(" --\n")
		output.WriteString(app.renderView())
		output.WriteString("\n")
	}
	return output.String()
}

func productLeaderBaseline(width, height int) *App {
	app := productGoldenApp(width, height, nil)
	app.chat.AppendUser("Inspect the leader baseline.")
	app.chat.AppendOrUpdateAssistant("Leader chat keeps the transcript and composer reachable.")
	app.chat.FinishAssistant()
	app.textarea.SetValue("next leader prompt")
	app.updateLayout()
	return app
}

func productPermissionBaseline(width, height int) *App {
	app := productLeaderBaseline(width, height)
	app.dialog.Show("Bash", `{"command":"make test"}`, "project tests", make(chan PermissionResponse, 1))
	app.pushDialog(StatePermission)
	return app
}

func productResumeBaseline(width, height int) *App {
	app := productGoldenApp(width, height, nil)
	now := time.Now()
	app.resume.Show("session-current")
	app.resume.SetSessions([]session.SessionInfo{
		{SessionID: "session-recent", Summary: "Continue TUI performance work", LastModified: now.Add(-10 * time.Minute), CWD: "/repo", Model: "test-model"},
		{SessionID: "session-older", Summary: "Review permission ownership", LastModified: now.Add(-2 * time.Hour), CWD: "/repo", Model: "test-model"},
	}, nil)
	app.pushDialog(StateResume)
	return app
}

func productBackgroundBaseline(width, height int) *App {
	app := productGoldenApp(width, height, nil)
	snapshot := productTaskAgentBaselineSnapshot()
	app.backgroundTasks.SetExplorerSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	app.enterBackgroundTasks()
	return app
}

func productTeamBaseline(width, height int) *App {
	app := productGoldenApp(width, height, nil)
	snapshot := productTaskAgentBaselineSnapshot()
	snapshot.Executions[0].ThreadID = "child-alpha"
	app.teamsPanel.SetExplorerSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	app.enterTeams()
	return app
}

func productTaskAgentBaselineSnapshot() engine.TaskExplorerSnapshot {
	return engine.TaskExplorerSnapshot{Available: true, BoardID: "product", Revision: engine.TaskExplorerRevision{Board: 1, Runtime: 1}, WorkItems: []engine.TaskExplorerWorkItem{{BoardID: "product", WorkItemID: "docs", Title: "Update architecture docs", ActiveForm: "Writing contracts", Status: "in_progress", Owner: "leader"}}, Executions: []engine.TaskExplorerExecution{{Key: engine.RuntimeExecutionKey{AgentID: "alpha", Generation: 1}, Name: "alpha scout", Description: "Inspect runtime ownership", Activity: "Reading thread state", Status: "running", DisplayMode: "foreground", LastToolName: "Grep", ToolUseCount: 3, TokenCount: 900, Phase: engine.TaskExplorerExecutionRunning, AllowedActions: []engine.TaskExplorerAction{engine.TaskExplorerActionInspect, engine.TaskExplorerActionSwitch}}}}
}
