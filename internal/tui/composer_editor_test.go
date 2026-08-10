package tui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/internal/tui/attachments"
)

func TestPrepareComposerEditorRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	script := filepath.Join(t.TempDir(), "editor")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'edited prompt\\n\\n' > \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", script)
	command, path, err := prepareComposerEditor("seed prompt", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if command == nil || path == "" {
		t.Fatalf("prepared command=%#v path=%q", command, path)
	}
	seed, err := os.ReadFile(path)
	if err != nil || string(seed) != "seed prompt" {
		t.Fatalf("seed=%q err=%v", seed, err)
	}
	result := readComposerEditorResult("thread-a", path, command.Run())
	if result.err != nil || result.content != "edited prompt" || result.threadID != "thread-a" {
		t.Fatalf("editor result = %#v", result)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temporary editor file remains: %v", err)
	}
}

func TestExternalEditorExpandsPasteConservativelyAndUndoRestoresRichDraft(t *testing.T) {
	app := New(Config{Resumed: true})
	pasted := strings.Repeat("expanded content ", attachments.PasteThreshold)
	app.handleComposerPaste(pasteKey(pasted))
	display := app.textarea.Value()
	if len(app.composerElements) != 1 {
		t.Fatalf("paste fixture elements=%#v", app.composerElements)
	}
	app.composerUndo = nil
	app.running = true
	app.externalEditorActive = true

	app.applyComposerEditorResult(composerEditorFinishedMsg{
		threadID: app.activeThreadViewID(), content: expandComposerElements(display, app.composerElements) + "edited",
	})
	if app.running != true || app.externalEditorActive || !strings.Contains(app.textarea.Value(), "expanded content") || len(app.composerElements) != 0 {
		t.Fatalf("external apply running=%v active=%v text=%q elements=%#v", app.running, app.externalEditorActive, app.textarea.Value(), app.composerElements)
	}
	app.handleKey(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	if app.textarea.Value() != display || len(app.composerElements) != 1 || app.composerElements[0].Value != pasted {
		t.Fatalf("undo external edit text=%q elements=%#v", app.textarea.Value(), app.composerElements)
	}
}

func TestExternalEditorRejectsWrongThreadResult(t *testing.T) {
	app := New(Config{Resumed: true})
	app.textarea.SetValue("keep")
	app.externalEditorActive = true
	app.applyComposerEditorResult(composerEditorFinishedMsg{threadID: "other", content: "replace"})
	if app.textarea.Value() != "keep" || app.externalEditorActive {
		t.Fatalf("wrong-thread result text=%q active=%v", app.textarea.Value(), app.externalEditorActive)
	}
}
