package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/filehistory"

	"github.com/abietic/yhc/engine/commands"
)

func TestWorkspaceHistoryDispatchPreservesDraftWithoutModelWork(t *testing.T) {
	app := newG27EngineApp(t)
	app.textarea.SetValue("keep this rich draft")
	before := app.captureComposerUndoEntry()
	ctx := &commands.CommandContext{Engine: app.engine, Extra: map[string]any{"leader_view": true}}
	if cmd := app.dispatchHistoryView("/changes", ctx, nil, true); cmd != nil {
		t.Fatal("/changes returned async/model command")
	}
	if !app.hasDialog(StateChangeReview) || app.textarea.Value() != before.Text || app.running {
		t.Fatalf("changes state dialog=%v draft=%q running=%v", app.hasDialog(StateChangeReview), app.textarea.Value(), app.running)
	}
	app.dismissChangeReview()
	if cmd := app.dispatchHistoryView("/recap", ctx, nil, true); cmd != nil {
		t.Fatal("/recap returned async/model command")
	}
	if app.state != StateExpand || app.textarea.Value() != before.Text || app.running {
		t.Fatalf("recap altered draft or started work: state=%v draft=%q running=%v", app.state, app.textarea.Value(), app.running)
	}
}

func TestWorkspaceHistoryCapturedContextIsRawUndoableAndDraftGated(t *testing.T) {
	app := newG27EngineApp(t)
	app.textarea.SetValue("draft")
	raw := "--- before ---\r\nold\r\n--- after ---\r\nnew"
	if reason := app.appendCapturedReviewContext(raw, "note.txt"); reason != "" {
		t.Fatal(reason)
	}
	if len(app.composerElements) != 1 || app.composerElements[0].Value != raw || !strings.Contains(app.textarea.Value(), "[") {
		t.Fatalf("captured context was not retained as literal draft element: %#v %q", app.composerElements, app.textarea.Value())
	}
	if len(app.engine.GetMessages()) != 0 {
		t.Fatal("adding review context submitted a model message")
	}
	app.undoComposerEdit()
	if app.textarea.Value() != "draft" || len(app.composerElements) != 0 {
		t.Fatalf("undo did not restore draft: %q %#v", app.textarea.Value(), app.composerElements)
	}
	for _, mode := range []InputMode{InputCommand, InputShell} {
		app.inputMode = mode
		app.textarea.SetValue("not normal")
		if reason := app.appendCapturedReviewContext(raw, "note.txt"); reason == "" {
			t.Fatalf("mode %v accepted captured context", mode)
		}
	}
}

func newCapturedHistoryApp(t *testing.T) *App {
	t.Helper()
	root, dir := t.TempDir(), t.TempDir()
	journal, err := filehistory.OpenRevisionJournal(root, filepath.Join(dir, "history-ui.workspace-history"), "history-ui")
	if err != nil {
		t.Fatal(err)
	}
	if err = journal.RecordTurnStart("turn-ui", "history-ui", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "note.txt")
	if err = os.WriteFile(path, []byte("old\r\nlast"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := journal.Begin("turn-ui", "tool-ui", "Edit", "note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("new\r\nLAST"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = journal.Finish(id, "succeeded"); err != nil {
		t.Fatal(err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{SessionID: "history-ui", CWD: root, TranscriptDir: dir, CommandEntrypoint: commands.EntrypointTUI})
	t.Cleanup(eng.Close)
	return New(Config{Engine: eng})
}

func TestWorkspaceHistorySelectionRevalidatesSourceAndAddsOnlyDraft(t *testing.T) {
	app := newCapturedHistoryApp(t)
	app.textarea.SetValue("keep original")
	if reason := app.appendCapturedReviewContext("existing raw context", "first.txt"); reason != "" {
		t.Fatal(reason)
	}
	before := app.captureComposerUndoEntry()
	app.openChangeReview()
	app.handleChangeReviewKey(tea.KeyPressMsg{Code: tea.KeyDown})
	app.handleChangeReviewKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if app.changeReviewPanel.preview == nil {
		t.Fatal("record not selectable")
	}
	correctTurn := app.changeReviewPanel.preview.TurnID
	app.changeReviewPanel.preview.TurnID = "unrelated-turn"
	app.handleChangeReviewKey(tea.KeyPressMsg{Code: 'a'})
	if app.textarea.Value() != before.Text || len(app.composerElements) != 1 || app.changeReviewPanel.feedback == "" {
		t.Fatal("mismatched record mutated draft")
	}
	app.changeReviewPanel.preview.TurnID = correctTurn
	app.handleChangeReviewKey(tea.KeyPressMsg{Code: 'a'})
	if app.hasDialog(StateChangeReview) || len(app.composerElements) != 2 {
		t.Fatal("valid capture not added")
	}
	body := app.composerElements[1].Value
	for _, want := range []string{"Captured change context", "Session: history-ui", "Turn: turn-ui", "old\r\nlast", "new\r\nLAST"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing source or raw captured bytes %q in %q", want, body)
		}
	}
	if len(app.engine.GetMessages()) != 0 || app.running {
		t.Fatal("adding capture submitted main work")
	}
	app.undoComposerEdit()
	if app.textarea.Value() != before.Text || len(app.composerElements) != 1 || app.composerElements[0].Value != "existing raw context" {
		t.Fatal("undo lost original rich draft")
	}
	app.openChangeReview()
	replacement := newG27EngineApp(t).engine
	app.engine = replacement
	app.handleChangeReviewKey(tea.KeyPressMsg{Code: 'a'})
	if app.hasDialog(StateChangeReview) || app.textarea.Value() != before.Text || len(app.composerElements) != 1 {
		t.Fatal("stale engine panel changed draft")
	}
}

func TestWorkspaceHistorySameEngineResumeClosesOldPanelOnResult(t *testing.T) {
	app := newCapturedHistoryApp(t)
	app.openChangeReview()
	if !app.hasDialog(StateChangeReview) {
		t.Fatal("fixture panel missing")
	}
	dir := app.engine.GetTranscriptDir()
	writePickerSession(t, dir, "next-history-session", "a saved session", time.Now())
	if _, err := app.engine.ResumeSession(context.Background(), "next-history-session"); err != nil {
		t.Fatal(err)
	}
	app.Update(resumeSessionActionFinishedMsg{resumedID: "next-history-session"})
	if app.hasDialog(StateChangeReview) || app.changeReviewPanel.IsVisible() || app.changeReviewPanel.load != nil || app.changeReviewTarget.engine != nil {
		t.Fatal("resumed owner retained old private review projection")
	}
}
