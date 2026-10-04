package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine/commands"
)

func TestCommandArgumentGhostHintIsRenderOnly(t *testing.T) {
	app := newTestApp(80, 24)
	cmd := commandWithArgumentHint(t, app)
	app.inputMode = InputCommand
	app.textarea.SetValue("/" + cmd.Name + " ")
	app.textarea.CursorEnd()
	app.updateCommandHints()

	beforeValue := app.textarea.Value()
	beforeUndo := len(app.composerUndo)
	rendered := stripANSIForTest(app.renderEditor())
	if !strings.Contains(rendered, cmd.ArgumentHint()) {
		t.Fatalf("rendered editor missing argument hint %q: %q", cmd.ArgumentHint(), rendered)
	}
	if got := app.textarea.Value(); got != beforeValue {
		t.Fatalf("render mutated textarea = %q, want %q", got, beforeValue)
	}
	if len(app.composerUndo) != beforeUndo {
		t.Fatal("render mutated composer undo")
	}

	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyTab}, {Code: tea.KeyRight}} {
		updateAppSilent(app, key)
		if got := app.textarea.Value(); got != beforeValue {
			t.Fatalf("%v inserted argument hint: %q", key, got)
		}
	}
}

func TestCommandArgumentGhostHintEligibility(t *testing.T) {
	app := newTestApp(80, 24)
	cmd := commandWithArgumentHint(t, app)
	for _, test := range []struct {
		name   string
		value  string
		mutate func(*App)
	}{
		{name: "real argument", value: "/" + cmd.Name + " value"},
		{name: "middle cursor", value: "/" + cmd.Name + " ", mutate: func(a *App) { a.textarea.CursorStart() }},
		{name: "soft wrap", value: "/" + cmd.Name + " " + strings.Repeat("x", 100)},
		{name: "history recall", value: "/" + cmd.Name + " ", mutate: func(a *App) { a.historySetText = a.textarea.Value() }},
		{name: "welcome is allowed", value: "/" + cmd.Name + " ", mutate: func(a *App) { a.state = StateWelcome }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := newTestApp(80, 24)
			candidate.inputMode = InputCommand
			candidate.textarea.SetValue(test.value)
			candidate.textarea.CursorEnd()
			if test.mutate != nil {
				test.mutate(candidate)
			}
			got := candidate.commandArgumentGhostHint()
			if test.name == "welcome is allowed" {
				if got == "" {
					t.Fatal("welcome command argument hint is hidden")
				}
				return
			}
			if got != "" {
				t.Fatalf("ghost hint = %q, want hidden", got)
			}
		})
	}
}

func TestCommandHintTransitionsAndAliasCompletion(t *testing.T) {
	app := newTestApp(80, 24)
	cmd := commandWithAlias(t, app)
	app.inputMode = InputCommand
	app.textarea.SetValue("/" + cmd.Aliases[0])
	app.updateCommandHints()
	if len(app.commandHints) == 0 {
		t.Fatalf("alias %q did not match a command hint", cmd.Aliases[0])
	}
	firstName := app.commandHints[0].Name
	app.commandHintIdx = -1
	updateAppSilent(app, tea.KeyPressMsg{Code: tea.KeyTab})
	if got, want := app.textarea.Value(), "/"+firstName+" "; got != want {
		t.Fatalf("Tab value = %q, want %q", got, want)
	}

	for _, value := range []string{"/" + cmd.Name + " ", "/unknown argument"} {
		app.textarea.SetValue(value)
		app.inputMode = InputCommand
		app.updateCommandHints()
		if len(app.commandHints) != 0 {
			t.Fatalf("command hints for %q = %d, want closed", value, len(app.commandHints))
		}
	}
}

func commandWithAlias(t *testing.T, app *App) *commands.Command {
	t.Helper()
	for _, cmd := range app.commandRegistry.List() {
		if len(cmd.Aliases) > 0 {
			return cmd
		}
	}
	t.Fatal("test requires an available command with an alias")
	return nil
}

func commandWithArgumentHint(t *testing.T, app *App) *commands.Command {
	t.Helper()
	for _, cmd := range app.commandRegistry.List() {
		if cmd.ArgumentHint() != "" {
			return cmd
		}
	}
	t.Fatal("test requires an available command with an argument hint")
	return nil
}

func TestCommandArgumentGhostAcrossWidthsAndPlainRendering(t *testing.T) {
	for _, width := range []int{40, 80, 120, 180} {
		app := newTestApp(width, 24)
		if err := app.commandRegistry.Register(&commands.Command{Name: "inspect", Usage: "/inspect <目标👩‍💻> [说明]", Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
			return &commands.CommandResult{}, nil
		}}); err != nil {
			t.Fatal(err)
		}
		app.textarea.SetValue("/inspect ")
		app.syncInputModeFromText()
		before := app.captureComposerUndoEntry()
		view := app.renderEditor()
		profile := app.renderEnvironment.normalized().profile
		if !strings.Contains(stripANSIForTest(view), "<目标👩‍💻>") {
			t.Fatalf("width %d missing hint: %q", width, view)
		}
		for _, line := range strings.Split(view, "\n") {
			if profile.width(line) > width {
				t.Fatalf("width %d overflow: %q", width, line)
			}
		}
		after := app.captureComposerUndoEntry()
		if before.Text != after.Text || before.CursorOffset != after.CursorOffset {
			t.Fatal("render changed cursor or text")
		}
		plain := "❯ /inspect  " + strings.Repeat(" ", width-12)
		projected := renderCommandArgumentGhost(profile, plain, width, 12, "<目标👩‍💻> [说明]")
		if !strings.Contains(projected, "<目标👩‍💻>") || profile.width(projected) > width {
			t.Fatalf("plain ghost = %q", projected)
		}
	}
}

func TestCommandArgumentFileHintsAreScopedToPaths(t *testing.T) {
	app := newTestApp(80, 24)
	for _, value := range []string{"/model ", "/model example", "/help ", "/unknown argument"} {
		app.textarea.SetValue(value)
		app.syncInputModeFromText()
		if len(app.fileHints) != 0 || len(app.commandHints) != 0 {
			t.Fatalf("unexpected popup for %q", value)
		}
	}
	app.textarea.SetValue(" /model ")
	app.syncInputModeFromText()
	if len(app.commandHints) != 0 {
		t.Fatal("leading whitespace regressed command parsing")
	}
}
