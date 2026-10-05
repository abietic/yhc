package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
)

func finishArgumentCompletion(t *testing.T, app *App) {
	t.Helper()
	cmd := app.takeArgumentCompletionCmd()
	if cmd == nil {
		t.Fatal("missing completion request")
	}
	msg, ok := cmd().(argumentCompletionMsg)
	if !ok {
		t.Fatal("wrong completion response")
	}
	app.handleArgumentCompletion(msg)
}

func argumentCompletionApp(t *testing.T, input string) *App {
	t.Helper()
	app := newTestApp(80, 24)
	app.textarea.SetValue(input)
	app.textarea.CursorEnd()
	app.syncInputModeFromText()
	finishArgumentCompletion(t, app)
	return app
}

func TestArgumentCompletionPopupGhostAndAcceptOnly(t *testing.T) {
	app := argumentCompletionApp(t, "/diff st")
	before := app.captureComposerUndoEntry()
	if app.argumentGhost() != "aged" || !strings.Contains(app.renderArgumentHints(), "stat") {
		t.Fatal("missing matching popup and ghost")
	}
	app.moveAutocomplete(1)
	app.moveAutocomplete(1)
	if app.argumentGhost() != "at" {
		t.Fatal("selection did not change ghost")
	}
	if got := app.captureComposerUndoEntry(); got.Text != before.Text || got.CursorOffset != before.CursorOffset || len(app.composerUndo) != 0 {
		t.Fatal("selection mutated draft")
	}
	app.acceptAutocomplete(tea.KeyPressMsg{Code: tea.KeyEnter})
	if app.textarea.Value() != "/diff stat " || app.running {
		t.Fatal("Enter did not accept only")
	}
	if len(app.composerUndo) != 1 {
		t.Fatal("acceptance is not one undo edit")
	}
	app.undoComposerEdit()
	if app.textarea.Value() != before.Text {
		t.Fatal("undo did not restore input")
	}
}

func TestArgumentCompletionEscapeAndStaleResults(t *testing.T) {
	app := argumentCompletionApp(t, "/diff st")
	old := argumentCompletionMsg{Key: app.argumentCompletion.Key, Serial: app.argumentCompletion.Serial, Result: app.argumentCompletion.Result}
	app.dismissArgumentCompletion()
	app.updateCommandHints()
	if app.textarea.Value() != "/diff st" || len(app.argumentCandidates()) != 0 || app.commandArgumentGhostHint() != "" {
		t.Fatal("dismissal changed draft or reopened candidates")
	}
	app.textarea.SetValue("/theme da")
	app.textarea.CursorEnd()
	app.markComposerChanged()
	app.syncInputModeFromText()
	finishArgumentCompletion(t, app)
	app.handleArgumentCompletion(old)
	if app.argumentGhost() != "ybreak" {
		t.Fatal("stale response replaced new result")
	}
	app.historySetText = app.textarea.Value()
	app.syncInputModeFromText()
	if len(app.argumentCandidates()) != 0 {
		t.Fatal("history recall exposed completion")
	}
}

func TestArgumentCompletionEscapeClosesFreeformPromptBeforeCancellingDraft(t *testing.T) {
	app := argumentCompletionApp(t, "/compact reason")
	if app.argumentCompletion.Cancel != nil || !strings.Contains(app.renderArgumentHints(), "Ctrl+Space") {
		t.Fatal("local query did not finish with a visible freeform prompt")
	}
	app.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if app.textarea.Value() != "/compact reason" || app.renderArgumentHints() != "" {
		t.Fatal("first Escape did not dismiss the prompt and preserve the draft")
	}
	app.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if app.textarea.Value() != "" {
		t.Fatal("second Escape did not cancel the draft")
	}
}

func TestArgumentCompletionManualSuggestionBindingAndPendingCancellation(t *testing.T) {
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		CWD: t.TempDir(), TranscriptDir: t.TempDir(), CommandEntrypoint: commands.EntrypointTUI,
	})
	t.Cleanup(eng.Close)
	app := newTestApp(80, 24)
	app.engine = eng
	app.commandRegistry = eng.GetCommandRegistry()
	app.textarea.SetValue("/compact because")
	app.textarea.CursorEnd()
	app.syncInputModeFromText()
	finishArgumentCompletion(t, app)
	if app.argumentCompletion.Suggesting {
		t.Fatal("typing requested AI automatically")
	}
	app.handleEditorKey(tea.KeyPressMsg{Code: ' ', Mod: tea.ModCtrl})
	if !app.argumentCompletion.Suggesting || app.argumentCompletion.Pending == nil || !strings.Contains(app.renderArgumentHints(), "Suggesting argument") {
		t.Fatal("Ctrl+Space did not explicitly queue and show the suggestion request")
	}
	serial := app.argumentCompletion.Serial
	app.handleEditorKey(tea.KeyPressMsg{Code: ' ', Mod: tea.ModCtrl})
	if app.argumentCompletion.Serial != serial {
		t.Fatal("duplicate key started another request")
	}
	app.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if app.textarea.Value() != "/compact because" || app.argumentCompletion.Pending != nil || app.argumentCompletion.Cancel != nil || app.renderArgumentHints() != "" {
		t.Fatal("Escape did not cancel the pending request without changing the draft")
	}
}

func TestArgumentCompletionMiddleCursorKeepsSuffix(t *testing.T) {
	app := newTestApp(80, 24)
	if err := app.commandRegistry.Register(&commands.Command{Name: "inspect", Completion: []commands.ArgumentCompletion{{Choices: []string{"中文 folder"}}}, Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
		return &commands.CommandResult{}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	app.textarea.SetValue("/inspect 中 suffix")
	app.setTextareaRuneCursor(len([]rune("/inspect 中")))
	app.syncInputModeFromText()
	finishArgumentCompletion(t, app)
	if app.argumentGhost() != "" {
		t.Fatal("middle-cursor replacement showed append ghost")
	}
	app.acceptAutocomplete(tea.KeyPressMsg{Code: tea.KeyTab})
	if app.textarea.Value() != "/inspect \"中文 folder\" suffix" {
		t.Fatal(app.textarea.Value())
	}
	if got := app.argumentCompletionKey().Cursor; got != len([]rune("/inspect \"中文 folder\"")) {
		t.Fatalf("cursor = %d", got)
	}
}

func TestArgumentCompletionPreviewWidths(t *testing.T) {
	for _, width := range []int{40, 80, 120, 180} {
		app := argumentCompletionApp(t, "/theme ")
		app.width = width
		app.updateLayout()
		profile := app.renderEnvironment.normalized().profile
		for _, line := range strings.Split(app.renderArgumentHints(), "\n") {
			if profile.width(line) > app.hintContentWidth() {
				t.Fatalf("width %d overflow: %q", width, line)
			}
		}
	}
}
