package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
)

func TestReverseHistorySearchCyclesOlderAndCancelRestoresRichDraft(t *testing.T) {
	app := New(Config{Resumed: true})
	app.history = []string{"build release", "git status", "build debug"}
	app.historyIdx = len(app.history)
	app.textarea.SetValue("build")
	app.composerElements = []threadComposerElement{{
		ID: "draft", Kind: composerElementKindPaste, Label: "build", Value: "original", Start: 0, End: 5,
	}}

	app.handleKey(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if !app.historySearch.Active || app.textarea.Value() != "build debug" || len(app.historySearch.Matches) != 2 {
		t.Fatalf("opened search = %#v draft=%q", app.historySearch, app.textarea.Value())
	}
	app.handleKey(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if app.textarea.Value() != "build release" || app.historySearch.Selected != 1 {
		t.Fatalf("older match = %q search=%#v", app.textarea.Value(), app.historySearch)
	}
	app.handleKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if app.historySearch.Active || app.textarea.Value() != "build" || len(app.composerElements) != 1 || app.composerElements[0].Value != "original" {
		t.Fatalf("cancel did not restore rich draft: search=%#v text=%q elements=%#v", app.historySearch, app.textarea.Value(), app.composerElements)
	}
}

func TestReverseHistorySearchQueryAcceptAndNoMatch(t *testing.T) {
	app := New(Config{Resumed: true})
	app.width = 120
	app.history = []string{"deploy staging", "test package", "deploy prod"}
	app.historyIdx = len(app.history)

	app.startHistorySearch()
	for _, r := range "staging" {
		app.handleHistorySearchKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{r})})
	}
	if app.textarea.Value() != "deploy staging" || !strings.Contains(stripANSIForTest(app.renderHistorySearch()), "1/1") {
		t.Fatalf("query match text=%q hint=%q", app.textarea.Value(), app.renderHistorySearch())
	}
	app.handleHistorySearchKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if app.historySearch.Active || app.textarea.Value() != "deploy staging" || len(app.composerUndo) == 0 {
		t.Fatalf("accepted search active=%v text=%q undo=%#v", app.historySearch.Active, app.textarea.Value(), app.composerUndo)
	}

	app.startHistorySearch()
	for _, r := range " missing" {
		app.handleHistorySearchKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{r})})
	}
	if len(app.historySearch.Matches) != 0 || app.textarea.Value() != "deploy staging" {
		t.Fatalf("no-match search changed draft: %#v text=%q", app.historySearch, app.textarea.Value())
	}
}

func TestThreadSwitchCancelsHistorySearchAndKeepsUndoPerThread(t *testing.T) {
	app := New(Config{Resumed: true})
	app.history = []string{"leader history"}
	app.historyIdx = len(app.history)
	app.textarea.SetValue("leader draft")
	app.startHistorySearch()
	app.composerUndo = []composerUndoEntry{{Text: "leader before"}}
	leaderID := app.leaderThreadViewID()

	if err := app.switchThreadView("child-history", engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	if app.historySearch.Active || len(app.composerUndo) != 0 {
		t.Fatalf("child inherited transient search/undo: search=%#v undo=%#v", app.historySearch, app.composerUndo)
	}
	if err := app.switchThreadView(leaderID, engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	if app.textarea.Value() != "leader draft" || len(app.composerUndo) != 1 || app.composerUndo[0].Text != "leader before" {
		t.Fatalf("leader restore text=%q undo=%#v", app.textarea.Value(), app.composerUndo)
	}
}

func TestHistorySearchAndExternalEditorDoNotTakeOverCommandOrShellMode(t *testing.T) {
	for _, test := range []struct {
		name  string
		mode  InputMode
		value string
	}{
		{name: "command", mode: InputCommand, value: "/queue"},
		{name: "shell", mode: InputShell, value: "echo ready"},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := New(Config{Resumed: true})
			app.inputMode = test.mode
			app.textarea.SetValue(test.value)
			app.handleKey(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
			if app.historySearch.Active || app.textarea.Value() != test.value {
				t.Fatalf("history search captured %s mode: active=%v text=%q", test.name, app.historySearch.Active, app.textarea.Value())
			}
			app.handleKey(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
			if app.externalEditorActive || app.textarea.Value() != test.value {
				t.Fatalf("external editor captured %s mode: active=%v text=%q", test.name, app.externalEditorActive, app.textarea.Value())
			}
		})
	}
}
