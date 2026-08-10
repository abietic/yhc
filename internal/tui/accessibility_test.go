package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/internal/tui/terminalcap"
)

func TestNoColorFinalFrameContainsNoTerminalStyles(t *testing.T) {
	caps := interactiveTerminalCaps()
	caps.Color = terminalcap.ColorNone
	app := New(Config{Resumed: true, TerminalCaps: &caps})
	app.width, app.height = 80, 30
	app.setPermissionMode(permission.ModePlan)
	app.running = true
	app.spinnerState = SpinnerState{Mode: SpinnerToolUse, ToolName: "Bash", StartTime: time.Now()}
	app.chat.AppendSystem("\x1b]8;;https://example.com\x1b\\linked status\x1b]8;;\x1b\\")
	app.activeTools["tool"] = &inlineToolEntry{toolCallID: "tool", name: "Bash", startTime: time.Now()}
	app.activeToolsOrder = []string{"tool"}
	app.updateLayout()

	view := app.renderView()
	if strings.Contains(view, "\x1b") {
		t.Fatalf("NO_COLOR frame contains terminal escape sequences: %q", view)
	}
	for _, want := range []string{"running", "Bash", "[running]", "linked status"} {
		if !strings.Contains(view, want) {
			t.Fatalf("NO_COLOR frame missing textual state %q:\n%s", want, view)
		}
	}
}

func TestStatusMeaningDoesNotDependOnColor(t *testing.T) {
	app := New(Config{Resumed: true})
	app.width, app.height = 80, 30
	app.updateLayout()
	tests := []struct {
		name   string
		setup  func()
		status string
	}{
		{name: "default", setup: func() {
			app.running = false
			app.externalEditorActive = false
			app.setPermissionMode(permission.ModeDefault)
		}, status: "default"},
		{name: "plan", setup: func() {
			app.running = false
			app.externalEditorActive = false
			app.setPermissionMode(permission.ModePlan)
		}, status: "plan"},
		{name: "yolo", setup: func() {
			app.running = false
			app.externalEditorActive = false
			app.setConfirmedBypassMode()
		}, status: "yolo"},
		{name: "running", setup: func() { app.running = true; app.externalEditorActive = false }, status: "running"},
		{name: "external editor", setup: func() { app.running = false; app.externalEditorActive = true }, status: "external editor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()
			if got := xansi.Strip(app.renderStatus()); !strings.Contains(got, tt.status) {
				t.Fatalf("status %q missing from %q", tt.status, got)
			}
		})
	}
}

func TestReducedMotionKeepsPollingWithoutAdvancingAnimation(t *testing.T) {
	caps := interactiveTerminalCaps()
	app := New(Config{Resumed: true, ReducedMotion: true, TerminalCaps: &caps})
	if cmd := app.Init(); cmd != nil {
		t.Fatalf("reduced-motion Init should not issue terminal-control commands, got %T", cmd())
	}
	view := app.View()
	if view.WindowTitle != "yhc" || !view.ReportFocus {
		t.Fatalf(
			"reduced-motion View terminal contract = title:%q focus:%v",
			view.WindowTitle,
			view.ReportFocus,
		)
	}

	app.running = true
	app.spinnerCount = 7
	app.spinnerTickScheduled = true
	app.streamingCtx.BeginStreaming()
	app.thinkingInd.Start()
	streamCursor := app.streamingCtx.renderer.cursorVisible
	thinkingTick := app.thinkingInd.tick
	_, cmd := app.Update(spinnerTickMsg{})
	if cmd == nil || !app.spinnerTickScheduled {
		t.Fatal("reduced motion stopped required runtime polling")
	}
	if app.spinnerCount != 7 || app.streamingCtx.renderer.cursorVisible != streamCursor || app.thinkingInd.tick != thinkingTick {
		t.Fatalf("reduced motion advanced animation: spinner=%d cursor=%v thinking=%d", app.spinnerCount, app.streamingCtx.renderer.cursorVisible, app.thinkingInd.tick)
	}

	app.chat.scrollRemaining = 20
	app.Update(scrollAnimTickMsg{})
	if app.chat.scrollRemaining != 0 {
		t.Fatal("reduced motion did not snap smooth scroll to its destination")
	}
	app.mascotAnim.TriggerAnimation()
	if !app.mascotAnim.Active() {
		t.Fatal("test mascot animation did not start")
	}
	if _, mascotCmd := app.Update(mascotTickMsg{}); mascotCmd != nil || app.mascotAnim.Active() {
		t.Fatal("reduced motion continued mascot animation")
	}
}

func TestExpandViewRawProjectionIsReachableAndANSIIndependent(t *testing.T) {
	app := New(Config{Resumed: true})
	app.width, app.height = 80, 24
	app.chat.AppendUser("用户输入 e\u0301 👩‍💻")
	app.chat.AppendOrUpdateAssistant("full assistant answer")
	app.chat.FinishAssistant()
	app.chat.AppendToolStart("tool", "Bash", `{"command":"printf raw"}`)
	app.chat.UpdateToolResult("tool", "Bash", "raw tool output")
	app.updateLayout()
	app.enterExpandView()
	if app.state != StateExpand || app.expandRaw {
		t.Fatalf("expand view did not start expanded: state=%v raw=%v", app.state, app.expandRaw)
	}

	app.handleExpandKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'r'})})
	if !app.expandRaw {
		t.Fatal("r did not switch expand view to raw projection")
	}
	if xansi.Strip(app.expandContent) != app.expandContent {
		t.Fatalf("raw projection contains terminal styles: %q", app.expandContent)
	}
	for _, want := range []string{"用户输入 e\u0301 👩‍💻", "full assistant answer", "raw tool output"} {
		if !strings.Contains(app.expandContent, want) {
			t.Fatalf("raw projection missing %q:\n%s", want, app.expandContent)
		}
	}
	if status := xansi.Strip(app.renderExpandView()); !strings.Contains(status, "[RAW]") || !strings.Contains(status, "raw/expanded") {
		t.Fatalf("raw projection is not visible in status:\n%s", status)
	}

	app.handleExpandKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'r'})})
	if app.expandRaw {
		t.Fatal("second r did not restore expanded projection")
	}
}

func TestUnicodeAccessibilityMatrixStaysValidAndBounded(t *testing.T) {
	cwd := "/tmp/非常长的项目目录/组合-e\u0301/emoji-👩‍💻/" + strings.Repeat("路径", 30)
	query := engine.NewQueryEngine(engine.QueryEngineConfig{CWD: cwd, TranscriptDir: t.TempDir()})
	t.Cleanup(query.Close)
	caps := interactiveTerminalCaps()
	caps.Color = terminalcap.ColorNone
	app := New(Config{Engine: query, Resumed: true, TerminalCaps: &caps})
	content := "中文 CJK · e\u0301 combining · 👩‍💻 emoji · " + strings.Repeat("超长路径段", 80)
	app.chat.AppendUser(content)
	app.chat.AppendOrUpdateAssistant("回答: " + content)
	app.chat.FinishAssistant()

	for _, size := range []struct{ width, height int }{{40, 20}, {80, 30}, {120, 50}} {
		app.width, app.height = size.width, size.height
		app.updateLayout()
		view := app.renderView()
		if !utf8.ValidString(view) {
			t.Fatalf("%dx%d frame contains invalid UTF-8", size.width, size.height)
		}
		for lineNo, line := range strings.Split(view, "\n") {
			if got := xansi.StringWidth(line); got > size.width {
				t.Fatalf("%dx%d line %d width=%d: %q", size.width, size.height, lineNo, got, line)
			}
		}
	}
	raw := app.chat.RenderAllRaw(120)
	for _, want := range []string{"中文 CJK", "e\u0301 combining", "👩‍💻 emoji", strings.Repeat("超长路径段", 10)} {
		if !strings.Contains(raw, want) {
			t.Fatalf("raw history lost Unicode content %q", want)
		}
	}
}
