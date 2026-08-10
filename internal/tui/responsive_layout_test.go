package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
)

func TestResponsiveLayoutSizeMatrix(t *testing.T) {
	for _, width := range []int{40, 80, 120, 180} {
		for _, height := range []int{20, 30, 50} {
			t.Run(fmt.Sprintf("%dx%d", width, height), func(t *testing.T) {
				layout := calculateLayout(layoutRequest{
					totalWidth: width, totalHeight: height, editorContentRows: 4,
					hintHeight: 12, taskTreeHeight: 6, contextHeight: 1,
					spinnerVisible: true, editorVisible: true, sidebarVisible: true,
				})
				wantMode := layoutModeStandard
				if width < compactWidth || height < compactHeight {
					wantMode = layoutModeCompact
				}
				if width >= wideWidth && height >= wideHeight {
					wantMode = layoutModeWide
				}
				if layout.mode != wantMode {
					t.Fatalf("mode = %q, want %q", layout.mode, wantMode)
				}
				if layout.statusRect.bottom() != height {
					t.Fatalf("main regions end at %d, want %d: %#v", layout.statusRect.bottom(), height, layout)
				}
				if layout.width+layout.sidebarRect.Width != width || layout.sidebarRect.X != layout.width {
					t.Fatalf("horizontal partition main=%d sidebar=%#v total=%d", layout.width, layout.sidebarRect, width)
				}
				if wantMode == layoutModeCompact && layout.hintRect.Height != compactHintMaxRows {
					t.Fatalf("compact hint band not bounded to %d rows: %#v", compactHintMaxRows, layout.hintRect)
				}
				if wantMode != layoutModeCompact && layout.hintRect.Height != 12 {
					t.Fatalf("non-compact mode clipped hint rows: %#v", layout.hintRect)
				}
				if wantMode == layoutModeWide && (layout.width < mainWideMin || layout.sidebarRect.Width < sidebarMin) {
					t.Fatalf("wide dimensions are unusable: %#v", layout)
				}
			})
		}
	}
}

func TestResponsiveAppMatrixBoundsAndWideSidebar(t *testing.T) {
	query := responsiveTestEngine(t)
	app := New(Config{Engine: query, Resumed: true, Model: "test-model"})
	explorer := p313ExplorerSnapshot()
	explorer.Executions = []engine.TaskExplorerExecution{
		p313Execution("agent-1", 1, engine.TaskExplorerExecutionRunning),
	}
	explorer.Executions[0].Task = "Review responsive layout"
	installP313ExplorerSnapshot(app, &explorer)
	app.chat.AppendUser(strings.Repeat("responsive history ", 30))
	app.textarea.SetValue(strings.Repeat("draft ", 40))

	for _, width := range []int{40, 80, 120, 180} {
		for _, height := range []int{20, 30, 50} {
			updateAppSilent(app, tea.WindowSizeMsg{Width: width, Height: height})
			view := app.renderView()
			assertViewBounds(t, view, width, height)
			wide := width >= wideWidth && height >= wideHeight
			if wide != (app.layout.mode == layoutModeWide) {
				t.Fatalf("%dx%d mode=%q", width, height, app.layout.mode)
			}
			if wide {
				plain := stripANSIForTest(view)
				if !strings.Contains(plain, "WORK") || !strings.Contains(plain, "RUN") || !strings.Contains(plain, "Review responsive") {
					t.Fatalf("%dx%d sidebar lacks canonical task facts:\n%s", width, height, plain)
				}
			}
		}
	}
}

func TestCompactModeKeepsPanelCommandsReachable(t *testing.T) {
	query := responsiveTestEngine(t)
	app := New(Config{Engine: query, Resumed: true})
	updateAppSilent(app, tea.WindowSizeMsg{Width: 40, Height: 20})
	if app.layout.mode != layoutModeCompact || app.layout.sidebarRect.Width != 0 {
		t.Fatalf("compact layout = %#v", app.layout)
	}

	app.handleKey(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	if !app.hasDialog(StateBackgroundTasks) {
		t.Fatal("Ctrl+B did not open task details without a sidebar")
	}
	app.popDialog(StateBackgroundTasks)
	app.handleKey(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	if !app.hasDialog(StateCommandPalette) {
		t.Fatal("Ctrl+K did not open the command palette in compact mode")
	}
}

func responsiveTestEngine(t testing.TB) *engine.QueryEngine {
	t.Helper()
	cwd := t.TempDir()
	query := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID: "responsive", ThreadID: "leader", CWD: cwd,
		TranscriptDir: filepath.Join(cwd, "transcripts"),
	})
	t.Cleanup(query.Close)
	return query
}
