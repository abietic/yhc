package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/internal/statemigration"
	"github.com/abietic/yhc/internal/statepath"
	"github.com/abietic/yhc/internal/tui/keybindings"
)

func TestConfigurableSubmitActionUsesActiveComposer(t *testing.T) {
	app := prepareViewSizedApp(newTextSubmissionApp(t, true))
	app.keybindResolver.SetBindings([]keybindings.Block{{
		Context: keybindings.ContextChat,
		Bindings: map[string]keybindings.Action{
			"alt+s": keybindings.ActionChatSubmit,
		},
	}})
	app.textarea.SetValue("custom submit")

	cmd := app.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'s'}), Mod: tea.ModAlt})
	settleComposerCommand(t, app, cmd)
	if got := submittedUserContent(t, app); got != "custom submit" {
		t.Fatalf("submitted content = %q", got)
	}
}

func TestAutocompleteContextPrecedesChatHistory(t *testing.T) {
	app := New(Config{Resumed: true})
	app.keybindResolver.SetBindings([]keybindings.Block{
		{Context: keybindings.ContextChat, Bindings: map[string]keybindings.Action{
			"up": keybindings.ActionHistoryPrevious,
		}},
		{Context: keybindings.ContextAutocomplete, Bindings: map[string]keybindings.Action{
			"up": keybindings.ActionAutocompletePrev,
		}},
	})
	app.inputMode = InputCommand
	app.commandHints = []*commands.Command{{Name: "one"}, {Name: "two"}}
	app.commandHintIdx = 1
	app.history = []string{"history"}
	app.historyIdx = len(app.history)

	app.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if app.commandHintIdx != 0 {
		t.Fatalf("command hint index = %d", app.commandHintIdx)
	}
	if app.textarea.Value() == "history" {
		t.Fatal("chat history handled a key owned by autocomplete")
	}
}

func TestTUILoadsCanonicalKeybindingsAfterImport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YHC_CONFIG_DIR", "")
	t.Setenv("EINO_AGENT_CONFIG_DIR", "")
	roots, err := statepath.UserRoots(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(roots.Legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"bindings":[{"context":"Chat","bindings":{"alt+up":"chat:nextAgent"}}]}`)
	if err := os.WriteFile(filepath.Join(roots.Legacy, "keybindings.json"), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := (statemigration.Importer{}).Import(
		t.Context(),
		roots,
		keybindings.UserMigrationSpec(),
	)
	if err != nil || result.Status != statemigration.StatusImported {
		t.Fatalf("Import() = %#v, %v", result, err)
	}

	app := New(Config{Resumed: true})
	action, ok := app.keybindResolver.Resolve(
		tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt},
		keybindings.ContextChat,
	)
	if !ok || action != keybindings.ActionChatNextAgent {
		t.Fatalf("canonical custom action = %q ok=%v", action, ok)
	}
}

func TestGlobalConfiguredActionIsReachableFromComposer(t *testing.T) {
	app := New(Config{Resumed: true})
	app.keybindResolver.SetBindings([]keybindings.Block{{
		Context: keybindings.ContextGlobal,
		Bindings: map[string]keybindings.Action{
			"alt+t": keybindings.ActionAppToggleTodos,
		},
	}})

	app.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'t'}), Mod: tea.ModAlt})
	if app.state != StateTaskPanel {
		t.Fatalf("state = %v, want task panel", app.state)
	}
}

func TestHelpAndStatusProjectConfiguredBindings(t *testing.T) {
	app := prepareViewSizedApp(New(Config{Resumed: true}))
	app.keybindResolver.SetBindings([]keybindings.Block{{
		Context: keybindings.ContextChat,
		Bindings: map[string]keybindings.Action{
			"alt+s": keybindings.ActionChatSubmit,
			"alt+n": keybindings.ActionChatNewline,
			"alt+m": keybindings.ActionChatCycleMode,
		},
	}})
	app.help.Show(nil)
	helpText := stripANSIForTest(strings.Join(app.help.lines, "\n"))
	if !strings.Contains(helpText, "alt+s") || !strings.Contains(helpText, "Send message") {
		t.Fatalf("help does not project custom submit binding:\n%s", helpText)
	}

	status := stripANSIForTest(app.renderStatus())
	for _, want := range []string{"alt+n newline", "alt+m mode"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status missing %q: %s", want, status)
		}
	}
}

func TestVimEditingPrecedesConfigurablePlainKey(t *testing.T) {
	app := New(Config{Resumed: true})
	app.keybindResolver.SetBindings([]keybindings.Block{{
		Context: keybindings.ContextChat,
		Bindings: map[string]keybindings.Action{
			"h": keybindings.ActionChatSubmit,
		},
	}})
	app.textarea.SetValue("abc")
	app.vimModel.Enable()
	app.vimModel.SetValue("abc")
	app.vimModel.SetCursor(3)

	app.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'h'})})
	if len(app.chat.Items()) != 0 {
		t.Fatal("resolver stole a Vim normal-mode editing key")
	}
	if app.textarea.Value() != "abc" || app.vimModel.Cursor() != 2 {
		t.Fatalf("vim edit state value=%q cursor=%d", app.textarea.Value(), app.vimModel.Cursor())
	}
}

func TestKeybindingChordIsConsumedUntilActionCompletes(t *testing.T) {
	app := New(Config{Resumed: true})
	app.keybindResolver.SetBindings([]keybindings.Block{{
		Context: keybindings.ContextChat,
		Bindings: map[string]keybindings.Action{
			"ctrl+x ctrl+t": keybindings.ActionChatNextAgent,
		},
	}})

	app.handleEditorKey(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if app.textarea.Value() != "" {
		t.Fatalf("chord prefix leaked to editor: %q", app.textarea.Value())
	}
	app.handleEditorKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if app.textarea.Value() != "" {
		t.Fatalf("chord completion leaked to editor: %q", app.textarea.Value())
	}
}

func TestConfiguredContextActionsReachActiveSurfaces(t *testing.T) {
	t.Run("help", func(t *testing.T) {
		app := New(Config{Resumed: true})
		app.keybindResolver.SetBindings([]keybindings.Block{{
			Context:  keybindings.ContextHelp,
			Bindings: map[string]keybindings.Action{"alt+h": keybindings.ActionHelpDismiss},
		}})
		app.openHelpOverlay()
		app.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'h'}), Mod: tea.ModAlt})
		if app.hasDialog(StateHelp) {
			t.Fatal("custom Help dismissal did not close the dialog")
		}
	})

	t.Run("transcript", func(t *testing.T) {
		app := New(Config{Resumed: true})
		app.keybindResolver.SetBindings([]keybindings.Block{{
			Context:  keybindings.ContextTranscript,
			Bindings: map[string]keybindings.Action{"alt+x": keybindings.ActionTranscriptExit},
		}})
		app.state = StateExpand
		app.expandLines = []string{"one", "two"}
		app.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'x'}), Mod: tea.ModAlt})
		if app.state != StateChat {
			t.Fatalf("transcript state = %v", app.state)
		}
	})

	t.Run("task", func(t *testing.T) {
		app := New(Config{Resumed: true})
		app.keybindResolver.SetBindings([]keybindings.Block{{
			Context:  keybindings.ContextTask,
			Bindings: map[string]keybindings.Action{"alt+x": keybindings.ActionTaskClose},
		}})
		app.state = StateTaskPanel
		app.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'x'}), Mod: tea.ModAlt})
		if app.state != StateChat {
			t.Fatalf("task state = %v", app.state)
		}
	})

	t.Run("message selector", func(t *testing.T) {
		app := New(Config{Resumed: true})
		app.chat.AppendUser("first")
		app.chat.AppendUser("second")
		app.openMessageSelector()
		app.keybindResolver.SetBindings([]keybindings.Block{{
			Context:  keybindings.ContextMessageSelector,
			Bindings: map[string]keybindings.Action{"alt+u": keybindings.ActionMessageSelectorUp},
		}})
		app.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'u'}), Mod: tea.ModAlt})
		if app.msgSelector.selectedPos != 0 {
			t.Fatalf("selector position = %d", app.msgSelector.selectedPos)
		}
	})

	t.Run("scroll", func(t *testing.T) {
		app := New(Config{Resumed: true})
		app.height = 10
		app.state = StateExpand
		app.expandLines = make([]string, 40)
		app.keybindResolver.SetBindings([]keybindings.Block{{
			Context:  keybindings.ContextScroll,
			Bindings: map[string]keybindings.Action{"alt+d": keybindings.ActionScrollPageDown},
		}})
		app.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'d'}), Mod: tea.ModAlt})
		if app.expandOffset == 0 {
			t.Fatal("custom scroll binding did not move the transcript")
		}
	})
}
