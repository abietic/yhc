package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/keybindings"
)

func newTextSubmissionEngine(t *testing.T) *engine.QueryEngine {
	t.Helper()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		ChatModel:     &compactCommandModel{},
		CWD:           t.TempDir(),
		TranscriptDir: t.TempDir(),
	})
	t.Cleanup(eng.Close)
	return eng
}

func newTextSubmissionApp(t *testing.T, resumed bool) *App {
	t.Helper()
	return New(Config{Engine: newTextSubmissionEngine(t), Resumed: resumed})
}

func updateApp(t *testing.T, app *App, msg tea.Msg) {
	t.Helper()
	model, cmd := app.Update(msg)
	if model != app {
		t.Fatalf("Update returned model %T, want original app", model)
	}
	settleComposerCommand(t, app, cmd)
}

func settleComposerCommand(t *testing.T, app *App, cmd tea.Cmd) {
	t.Helper()
	if app.composerAdmissionPending == nil || cmd == nil {
		return
	}
	message := cmd()
	switch typed := message.(type) {
	case composerAdmissionSettledMsg:
		settleTextSubmission(t, app, typed)
	case tea.BatchMsg:
		for _, child := range typed {
			if child == nil {
				continue
			}
			if settled, ok := child().(composerAdmissionSettledMsg); ok {
				settleTextSubmission(t, app, settled)
				return
			}
		}
	}
}

func settleTextSubmission(t *testing.T, app *App, settled composerAdmissionSettledMsg) {
	t.Helper()
	app.Update(settled)
	for event := range settled.Events {
		app.handleEngineEvent(event)
	}
}

func submittedUserContent(t *testing.T, app *App) string {
	t.Helper()
	if len(app.chat.items) == 0 {
		t.Fatal("submission did not append a chat item")
	}
	user, ok := app.chat.items[0].(*UserMessage)
	if !ok {
		t.Fatalf("first chat item = %T, want *UserMessage", app.chat.items[0])
	}
	return user.content
}

func TestAppUpdateSubmitKeys(t *testing.T) {
	tests := []struct {
		name  string
		input string
		key   rune
	}{
		{name: "single line enter", input: "hello", key: tea.KeyEnter},
		{name: "multiline enter", input: "first line\nsecond line", key: tea.KeyEnter},
		{name: "terminal ctrl m variant", input: "from ctrl-m", key: tea.KeyReturn},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newTextSubmissionApp(t, false)
			app.textarea.SetValue(tt.input)

			updateApp(t, app, tea.KeyPressMsg{Code: tt.key})

			if got := submittedUserContent(t, app); got != tt.input {
				t.Fatalf("submitted payload = %q, want %q", got, tt.input)
			}
			if got := app.textarea.Value(); got != "" {
				t.Fatalf("textarea after submit = %q, want empty", got)
			}
		})
	}
}

func TestAppUpdateCtrlJThenEnterSubmitsExactMultilinePayload(t *testing.T) {
	app := newTextSubmissionApp(t, false)
	updateApp(t, app, tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("first line"))})
	updateApp(t, app, tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	updateApp(t, app, tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("second line"))})

	const want = "first line\nsecond line"
	if got := app.textarea.Value(); got != want {
		t.Fatalf("textarea before submit = %q, want %q", got, want)
	}

	updateApp(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := submittedUserContent(t, app); got != want {
		t.Fatalf("submitted payload = %q, want %q", got, want)
	}
	if got := app.textarea.Value(); got != "" {
		t.Fatalf("textarea after submit = %q, want empty", got)
	}
}

func TestAppUpdateRuneInputDoesNotSubmit(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.Msg
		want string
	}{
		{
			name: "pasted multiline runes",
			msg:  tea.PasteMsg{Content: "pasted\ntext"},
			want: "pasted\ntext",
		},
		{
			name: "ime multi-rune input",
			msg:  tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("你好"))},
			want: "你好",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := New(Config{})
			updateApp(t, app, tt.msg)

			if got := app.textarea.Value(); got != tt.want {
				t.Fatalf("textarea = %q, want %q", got, tt.want)
			}
			if len(app.chat.items) != 0 {
				t.Fatalf("chat items = %d, want no submission", len(app.chat.items))
			}
		})
	}
}

func TestAppUpdateWhitespaceEnterRemainsNoOp(t *testing.T) {
	app := New(Config{})
	app.textarea.SetValue(" \t\n ")
	want := app.textarea.Value()

	updateApp(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})

	if got := app.textarea.Value(); got != want {
		t.Fatalf("textarea = %q, want whitespace input preserved", got)
	}
	if len(app.chat.items) != 0 {
		t.Fatalf("chat items = %d, want no submission", len(app.chat.items))
	}
}

func TestDefaultResolverRetainsDistinguishableShiftEnterCompatibility(t *testing.T) {
	resolver := keybindings.NewResolver()
	for _, keyName := range resolver.GetKeysForAction(keybindings.ContextChat, keybindings.ActionChatNewline) {
		if keyName == "shift+enter" {
			return
		}
	}
	t.Fatal("newline binding does not retain shift+enter compatibility")
}

func TestViewDoesNotMutateEditorState(t *testing.T) {
	app := prepareViewSizedApp(New(Config{}))
	app.state = StateChat
	app.textarea.SetValue("first\nsecond")
	beforeValue := app.textarea.Value()
	beforeLayout := app.layout

	view := app.renderView()

	if !strings.Contains(view, "[2 lines]") {
		t.Fatalf("view does not contain multiline indicator: %q", view)
	}
	if got := app.textarea.Value(); got != beforeValue {
		t.Fatalf("View changed textarea from %q to %q", beforeValue, got)
	}
	if app.layout != beforeLayout {
		t.Fatalf("View changed layout from %#v to %#v", beforeLayout, app.layout)
	}
}

func TestMultilineStatusAdvertisesSupportedKeys(t *testing.T) {
	app := prepareViewSizedApp(New(Config{}))
	app.textarea.SetValue("first\nsecond")

	status := app.renderStatus()
	if !strings.Contains(status, "enter send") || !strings.Contains(status, "ctrl+j newline") {
		t.Fatalf("multiline status = %q", status)
	}
	if strings.Contains(status, "ctrl+enter") {
		t.Fatalf("multiline status advertises unsupported ctrl+enter: %q", status)
	}
}
