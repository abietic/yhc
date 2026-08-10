package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestComposerUndoRestoresTypingAndBubblesWordDelete(t *testing.T) {
	app := New(Config{Resumed: true})
	for _, r := range "hello world" {
		app.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{r})})
	}
	app.handleEditorKey(tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
	if app.textarea.Value() != "hello " {
		t.Fatalf("Bubbles ctrl+w result = %q", app.textarea.Value())
	}
	app.handleEditorKey(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	if app.textarea.Value() != "hello world" {
		t.Fatalf("undo word delete = %q", app.textarea.Value())
	}
	app.handleEditorKey(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	if app.textarea.Value() != "hello worl" {
		t.Fatalf("undo last typed rune = %q", app.textarea.Value())
	}
}

func TestComposerUndoIsBoundedAndClearsAfterSubmit(t *testing.T) {
	app := New(Config{Resumed: true})
	for i := 0; i < maxComposerUndoEntries+20; i++ {
		app.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'x'})})
	}
	if len(app.composerUndo) != maxComposerUndoEntries {
		t.Fatalf("undo size = %d", len(app.composerUndo))
	}
	app.clearInputAfterSubmit(app.textarea.Value())
	if len(app.composerUndo) != 0 {
		t.Fatalf("submit retained undo history: %#v", app.composerUndo)
	}
}
