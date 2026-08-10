package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func prepareViewSizedApp(app *App) *App {
	app.width = 100
	app.height = 30
	app.updateLayout()
	return app
}

func TestWelcomeStateRetainsPreSubmitEditingNavigationAndControl(t *testing.T) {
	t.Run("rune editing stays on welcome", func(t *testing.T) {
		app := New(Config{})

		app.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("h"))})
		if app.state != StateWelcome {
			t.Fatalf("state after typing = %v, want welcome", app.state)
		}
		if got := app.textarea.Value(); got != "h" {
			t.Fatalf("textarea after typing = %q", got)
		}

		app.handleKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
		if app.state != StateWelcome {
			t.Fatalf("state after backspace = %v, want welcome", app.state)
		}
		if got := app.textarea.Value(); got != "" {
			t.Fatalf("textarea after backspace = %q", got)
		}
	})

	t.Run("navigation stays on welcome", func(t *testing.T) {
		app := New(Config{})
		app.textarea.SetValue("hello")

		app.handleKey(tea.KeyPressMsg{Code: tea.KeyLeft})
		if app.state != StateWelcome {
			t.Fatalf("state after left = %v, want welcome", app.state)
		}
		if got := app.textarea.Value(); got != "hello" {
			t.Fatalf("textarea after left = %q", got)
		}
	})

	t.Run("command and shell mode triggers stay on welcome", func(t *testing.T) {
		commandApp := New(Config{})
		commandApp.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("/"))})
		if commandApp.state != StateWelcome {
			t.Fatalf("state after slash trigger = %v, want welcome", commandApp.state)
		}
		if commandApp.inputMode != InputCommand {
			t.Fatalf("input mode after slash trigger = %v", commandApp.inputMode)
		}
		if got := commandApp.textarea.Value(); got != "/" {
			t.Fatalf("textarea after slash trigger = %q", got)
		}

		shellApp := New(Config{})
		shellApp.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("!"))})
		if shellApp.state != StateWelcome {
			t.Fatalf("state after shell trigger = %v, want welcome", shellApp.state)
		}
		if shellApp.inputMode != InputShell {
			t.Fatalf("input mode after shell trigger = %v", shellApp.inputMode)
		}
	})

	t.Run("first ctrl+c stays on welcome", func(t *testing.T) {
		app := New(Config{})

		app.handleKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		if app.state != StateWelcome {
			t.Fatalf("state after first ctrl+c = %v, want welcome", app.state)
		}
		if app.quitting {
			t.Fatal("first ctrl+c should not quit")
		}
		if app.lastCtrlC.IsZero() {
			t.Fatal("first ctrl+c did not record quit warning timestamp")
		}

		app.lastCtrlC = time.Now()
		app.handleKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		if !app.quitting {
			t.Fatal("second ctrl+c should still quit from welcome")
		}
	})
}

func TestWelcomeEmptySubmitDoesNotDismiss(t *testing.T) {
	app := New(Config{})

	cmd := app.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("empty enter returned unexpected command")
		return
	}
	if app.state != StateWelcome {
		t.Fatalf("state after empty enter = %v, want welcome", app.state)
	}
	if len(app.chat.items) != 0 {
		t.Fatalf("chat items after empty enter = %d", len(app.chat.items))
	}
}

func TestWelcomeFirstNormalSubmitTransitionsToChat(t *testing.T) {
	app := newTextSubmissionApp(t, false)
	app.textarea.SetValue("hello welcome")

	cmd := app.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	settleComposerCommand(t, app, cmd)
	if app.state != StateChat {
		t.Fatalf("state after normal submit = %v, want chat", app.state)
	}
	if len(app.chat.items) == 0 {
		t.Fatal("normal submit did not append chat items")
	}
	user, ok := app.chat.items[0].(*UserMessage)
	if !ok || user.content != "hello welcome" {
		t.Fatalf("first chat item = %#v", app.chat.items[0])
	}
}

func TestWelcomeFirstSlashSubmitTransitionsToChat(t *testing.T) {
	app := New(Config{})
	app.inputMode = InputCommand
	app.textarea.SetValue("/unknown")

	cmd := app.sendMessage()
	if cmd != nil {
		t.Fatal("slash submit should not return a command without an engine")
		return
	}
	if app.state != StateChat {
		t.Fatalf("state after slash submit = %v, want chat", app.state)
	}
	if len(app.chat.items) == 0 {
		t.Fatal("slash submit did not append chat items")
	}
	user, ok := app.chat.items[0].(*UserMessage)
	if !ok || user.content != "/unknown" {
		t.Fatalf("first chat item = %#v", app.chat.items[0])
	}
}

func TestWelcomeFirstShellSubmitTransitionsToChat(t *testing.T) {
	app := New(Config{})
	app.inputMode = InputShell
	app.textarea.SetValue("pwd")

	cmd := app.sendMessage()
	if cmd == nil {
		t.Fatal("shell submit did not return shell command")
		return
	}
	if app.state != StateChat {
		t.Fatalf("state after shell submit = %v, want chat", app.state)
	}
	if len(app.chat.items) == 0 {
		t.Fatal("shell submit did not append chat items")
	}
	user, ok := app.chat.items[0].(*UserMessage)
	if !ok || user.content != "!pwd" {
		t.Fatalf("first chat item = %#v", app.chat.items[0])
	}
}

func TestWelcomeHintsRenderNavigateAndStayOnWelcome(t *testing.T) {
	app := prepareViewSizedApp(New(Config{}))

	app.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("/"))})
	if app.state != StateWelcome {
		t.Fatalf("state after slash trigger = %v, want welcome", app.state)
	}
	if len(app.commandHints) == 0 {
		t.Fatal("expected slash trigger to populate command hints")
	}

	view := stripANSIForTest(app.renderView())
	if !strings.Contains(view, "/new") ||
		!strings.Contains(view, "Start a fresh session") {
		t.Fatalf("expected visible command hints in welcome view, got %q", view)
	}

	app.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if app.state != StateWelcome {
		t.Fatalf("state after hint navigation = %v, want welcome", app.state)
	}
	if app.commandHintIdx != 0 {
		t.Fatalf("command hint index after down = %d, want 0", app.commandHintIdx)
	}

	view = stripANSIForTest(app.renderView())
	if !strings.Contains(view, "/new") {
		t.Fatalf("expected command hints to remain visible after navigation, got %q", view)
	}

	selected := app.commandHints[app.commandHintIdx].Name
	app.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if app.state != StateWelcome {
		t.Fatalf("state after accepting hint = %v, want welcome", app.state)
	}
	if got := app.textarea.Value(); got != "/"+selected+" " {
		t.Fatalf("textarea after accepting hint = %q, want %q", got, "/"+selected+" ")
	}
	if len(app.chat.items) != 0 {
		t.Fatalf("chat items after accepting hint = %d, want 0", len(app.chat.items))
	}
}

func TestWelcomeFileHintsRenderAfterCommandSelection(t *testing.T) {
	app := prepareViewSizedApp(New(Config{}))
	commands := app.commandRegistry.List()
	if len(commands) == 0 {
		t.Fatal("expected command registry entries")
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
		return
	}
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "alpha.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
		return
	}
	defer func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore cwd: %v", err)
			return
		}
	}()

	app.inputMode = InputCommand
	app.textarea.SetValue("/" + commands[0].Name + " a")
	app.updateCommandHints()

	if app.state != StateWelcome {
		t.Fatalf("state before rendering file hints = %v, want welcome", app.state)
	}
	if len(app.fileHints) == 0 {
		t.Fatal("expected file hints after valid command plus space")
	}

	view := stripANSIForTest(app.renderView())
	if !strings.Contains(view, "alpha.txt") {
		t.Fatalf("expected visible file hints in welcome view, got %q", view)
	}
}

func TestChatCommandHintsStillRenderOutsideWelcome(t *testing.T) {
	app := prepareViewSizedApp(New(Config{Resumed: true}))
	app.inputMode = InputCommand
	app.textarea.SetValue("/h")
	app.updateCommandHints()

	if app.state != StateChat {
		t.Fatalf("state before chat hint render = %v, want chat", app.state)
	}

	view := stripANSIForTest(app.renderView())
	if !strings.Contains(view, "/help") || !strings.Contains(view, "List available commands") {
		t.Fatalf("expected visible command hints in chat view, got %q", view)
	}
}

func TestCompactChatCommandHintsRemainVisibleAfterWelcome(t *testing.T) {
	app := newTextSubmissionApp(t, false)
	updateAppSilent(app, tea.WindowSizeMsg{Width: 72, Height: 24})

	app.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("/"))})
	if view := stripANSIForTest(app.renderView()); !strings.Contains(view, "/new") {
		t.Fatalf("welcome slash hints are not visible: %q", view)
	}

	app.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	app.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("hello"))})
	settleComposerCommand(t, app, app.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter}))
	if app.state != StateChat {
		t.Fatalf("state after first submission = %v, want chat", app.state)
	}

	app.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("/"))})
	if len(app.commandHints) == 0 {
		t.Fatal("second slash did not populate command hint state")
	}
	if view := stripANSIForTest(app.renderView()); !strings.Contains(view, "/new") {
		t.Fatalf("compact chat hid second slash hints: %q", view)
	}
}

func TestCompactChatKeepsSelectedCommandHintAboveQueuedPreviews(t *testing.T) {
	app := New(Config{Resumed: true})
	updateAppSilent(app, tea.WindowSizeMsg{Width: 72, Height: 24})
	app.queuedInputPreview = []threadQueuedInput{
		{ID: "queue-1", Content: "first queued prompt"},
		{ID: "queue-2", Content: "second queued prompt"},
		{ID: "queue-3", Content: "third queued prompt"},
	}

	app.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("/"))})
	for range 6 {
		app.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if app.commandHintIdx < 0 || app.commandHintIdx >= len(app.commandHints) {
		t.Fatalf("selected command index = %d for %d hints", app.commandHintIdx, len(app.commandHints))
	}
	selected := "/" + app.commandHints[app.commandHintIdx].Name
	if view := stripANSIForTest(app.renderView()); !strings.Contains(view, selected) {
		t.Fatalf("compact queued previews hid selected command %q: %q", selected, view)
	}
}

func TestResumedConfigStartsInChat(t *testing.T) {
	app := New(Config{Resumed: true})

	if app.state != StateChat {
		t.Fatalf("resumed state = %v, want chat", app.state)
	}

	app.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("h"))})
	if app.state != StateChat {
		t.Fatalf("state after resumed typing = %v, want chat", app.state)
	}
	if got := app.textarea.Value(); got != "h" {
		t.Fatalf("textarea after resumed typing = %q", got)
	}
}
