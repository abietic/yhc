package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine/commands"
)

func historyHintTestRegistry() *commands.Registry {
	registry := commands.NewRegistry()
	for _, name := range []string{"theme", "resume", "help"} {
		_ = registry.Register(&commands.Command{
			Name:        name,
			Description: name + " cmd",
			Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
				return &commands.CommandResult{Output: "ok"}, nil
			},
		})
	}
	return registry
}

func TestHistoryRecallSuppressesCommandHints(t *testing.T) {
	app := newTestApp(80, 24)
	app.commandRegistry = historyHintTestRegistry()
	app.history = []string{"/theme daybreak", "/resume", "plain note"}
	app.richHistoryElements = map[int][]threadComposerElement{}
	app.historyIdx = len(app.history)

	app.navigateHistory(-1)
	if !app.suppressingHistoryHints() {
		t.Fatal("untouched history recall must suppress autocomplete")
	}
	if len(app.commandHints) > 0 || len(app.mentionHints) > 0 {
		t.Fatal("hints opened during history traversal")
	}

	app.navigateHistory(-1)
	if !app.suppressingHistoryHints() {
		t.Fatal("untouched /command recall must suppress hints")
	}
	if len(app.commandHints) > 0 {
		t.Fatalf("command hints opened during history traversal: %d", len(app.commandHints))
	}

	app.navigateHistory(-1)
	if got := app.textarea.Value(); got != "/theme daybreak" {
		t.Fatalf("up traversal blocked, value = %q", got)
	}
	app.navigateHistory(1)
	if got := app.textarea.Value(); got != "/resume" {
		t.Fatalf("down traversal blocked, value = %q", got)
	}
	if app.autocompleteOwnsKey(tea.KeyPressMsg{Code: tea.KeyUp}) {
		t.Fatal("up key claimed by autocomplete during history traversal")
	}

	app.textarea.SetValue("/res")
	app.syncInputModeFromText()
	if app.suppressingHistoryHints() {
		t.Fatal("edited recall must lift hint suppression")
	}
	if len(app.commandHints) == 0 {
		t.Fatal("hints did not re-enable after edit")
	}
	app.textarea.SetValue("/resume")
	app.syncInputModeFromText()
	if app.suppressingHistoryHints() {
		t.Fatal("recreating recalled text after an edit must not re-enter suppression")
	}

	app.navigateHistory(1)
	app.navigateHistory(1)
	if app.suppressingHistoryHints() {
		t.Fatal("draft must not be suppressed")
	}
}

func TestHistoryRecallPlainTextExitsCommandMode(t *testing.T) {
	app := newTestApp(80, 24)
	app.commandRegistry = historyHintTestRegistry()
	app.history = []string{"/theme daybreak", "please spawn subagents"}
	app.richHistoryElements = map[int][]threadComposerElement{}
	app.historyIdx = len(app.history)

	app.navigateHistory(-1)
	app.navigateHistory(-1)
	if app.inputMode != InputCommand {
		t.Fatalf("/command recall should enter InputCommand, got %v", app.inputMode)
	}
	app.navigateHistory(1)
	if app.inputMode == InputCommand {
		t.Fatal("plain-text recall must exit InputCommand")
	}
	if app.inputMode != InputNormal {
		t.Fatalf("expected InputNormal, got %v", app.inputMode)
	}

	app.inputMode = InputCommand
	app.textarea.SetValue("theme")
	app.syncInputModeFromText()
	if app.inputMode == InputCommand {
		t.Fatal("deleting the slash must exit InputCommand")
	}
}
