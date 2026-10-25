package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/abietic/yhc/internal/tui/keybindings"
	"github.com/charmbracelet/x/ansi"
)

func contextManagerSearch(app *App, query string) {
	contextManagerKey(app, '/', 0)
	contextManagerKey(app, 'u', tea.ModCtrl)
	updateAppSilent(app, tea.PasteMsg{Content: query})
	contextManagerKey(app, tea.KeyEnter, 0)
}

func TestComposerContextManagerSearchAndTypeIntersection(t *testing.T) {
	app := outputContextApp(t, 80)
	contextManagerFixture(app, []string{"@共享.go", "[Output #1]", "@other.go"}, []string{"file", "terminal_output", "file"}, []string{"file body", "output body", "body-only-needle"})
	app.composerElements[0].Value = "src/共享.go"
	app.composerElements[1].Name = "共享 check"
	before := app.captureComposerUndoEntry()
	app.openComposerContextManager()
	contextManagerSearch(app, "共享")
	view := ansi.Strip(app.composerContextManagerOverlay("", 80, 24))
	if !strings.Contains(view, "@共享.go") || !strings.Contains(view, "[Output #1]") || strings.Contains(view, "@other.go") {
		t.Fatalf("metadata search did not narrow attachments: %s", view)
	}
	contextManagerKey(app, tea.KeyTab, 0)
	view = ansi.Strip(app.composerContextManagerOverlay("", 80, 24))
	if !strings.Contains(view, "@共享.go") || strings.Contains(view, "[Output #1]") {
		t.Fatalf("file filter did not intersect search: %s", view)
	}
	contextManagerSearch(app, "body-only-needle")
	if view = ansi.Strip(app.composerContextManagerOverlay("", 80, 24)); !strings.Contains(view, "No matching context") {
		t.Fatalf("search inspected payloads or lacked empty state: %s", view)
	}
	if after := app.captureComposerUndoEntry(); !reflect.DeepEqual(before, after) || app.running {
		t.Fatal("search changed or submitted the rich draft")
	}
}

func TestComposerContextManagerMarkedBatchIsOneUndo(t *testing.T) {
	app := outputContextApp(t, 80)
	contextManagerFixture(app, []string{"@same", "@same", "[Output #1]", "@last"}, []string{"file", "file", "terminal_output", "file"}, []string{"first", "second", "keep output", "last"})
	before := app.captureComposerUndoEntry()
	undoCount := len(app.composerUndo)
	app.openComposerContextManager()
	contextManagerKey(app, tea.KeyHome, 0)
	contextManagerKey(app, tea.KeySpace, 0)
	contextManagerKey(app, tea.KeyEnd, 0)
	contextManagerKey(app, tea.KeySpace, 0)
	contextManagerKey(app, tea.KeyDelete, 0)
	if app.textarea.Value() != "前言 e\u0301 @same [Output #1] 尾文" || len(app.composerElements) != 2 || app.composerElements[0].ID != "context-1" || app.composerElements[1].ID != "context-2" {
		t.Fatalf("batch removed the wrong identities/ranges: %q %#v", app.textarea.Value(), app.composerElements)
	}
	if len(app.composerUndo) != undoCount+1 {
		t.Fatal("batch did not produce exactly one undo entry")
	}
	contextManagerKey(app, 'z', tea.ModCtrl)
	if after := app.captureComposerUndoEntry(); !reflect.DeepEqual(before, after) {
		t.Fatalf("batch undo did not restore exact rich draft: %#v", after)
	}
}

func TestComposerContextManagerSearchMetadataAndTypes(t *testing.T) {
	app := outputContextApp(t, 80)
	contextManagerFixture(app,
		[]string{"@range", "@whole", "[Output #1]", "@skill:check", "@mcp:srv/doc", "[Pasted Content 6 chars]"},
		[]string{"file", "file", "terminal_output", "skill", "mcp_resource", "paste"},
		[]string{"body-secret", "body-secret", "body-secret", "body-secret", "body-secret", "paste-secret"})
	app.composerElements[0].StartLine = 20
	app.composerElements[0].Value = "src/Only-PATH.go"
	if err := app.addComposerImageAt("Capture.PNG", "image/png", []byte("image-secret"), len([]rune(app.textarea.Value()))); err != nil {
		t.Fatal(err)
	}
	app.openComposerContextManager()
	items, valid := app.composerContextItems()
	if !valid {
		t.Fatal("invalid fixture")
	}
	for _, tt := range []struct{ query, id string }{
		{"only-path", "context-0"}, {"FILE LINES", "context-0"}, {"skill:CHECK", "context-3"}, {"mcp:srv/DOC", "context-4"}, {"capture.png", app.composerElements[6].ID},
	} {
		contextManagerSearch(app, tt.query)
		matches := app.contextManager.filtered(items)
		if len(matches) != 1 || matches[0].ID != tt.id {
			t.Errorf("metadata search %q returned %#v", tt.query, matches)
		}
	}
	for _, query := range []string{"body-secret", "paste-secret", "image-secret"} {
		contextManagerSearch(app, query)
		if got := app.contextManager.filtered(items); len(got) != 0 {
			t.Errorf("search read payload for %q", query)
		}
	}
	contextManagerSearch(app, "")
	for i, count := range []int{7, 2, 1, 1, 1, 1, 1, 0, 7} {
		if matches := app.contextManager.filtered(items); len(matches) != count {
			t.Fatalf("type %d matched %d items, want %d", i, len(matches), count)
		}
		contextManagerKey(app, tea.KeyTab, 0)
	}
	contextManagerKey(app, tea.KeyTab, tea.ModShift)
	if app.contextManager.filter != 0 {
		t.Fatal("reverse type navigation did not return to All")
	}
}

func TestComposerContextManagerSelectAllMatchesAndClearOnFilter(t *testing.T) {
	app := outputContextApp(t, 80)
	labels, kinds, bodies := make([]string, 20), make([]string, 20), make([]string, 20)
	for i := range labels {
		labels[i], kinds[i], bodies[i] = "@same", "file", "captured"
	}
	kinds[9] = "terminal_output"
	contextManagerFixture(app, labels, kinds, bodies)
	app.openComposerContextManager()
	contextManagerKey(app, 'a', tea.ModCtrl)
	if len(app.contextManager.marked) != 20 {
		t.Fatal("select all omitted offscreen matches")
	}
	contextManagerKey(app, 'a', tea.ModCtrl)
	if len(app.contextManager.marked) != 0 {
		t.Fatal("select all did not toggle off")
	}
	contextManagerKey(app, tea.KeySpace, 0)
	contextManagerKey(app, tea.KeyTab, 0)
	if len(app.contextManager.marked) != 0 {
		t.Fatal("type change retained hidden marks")
	}
	contextManagerKey(app, 'a', tea.ModCtrl)
	contextManagerSearch(app, "not-found")
	if len(app.contextManager.marked) != 0 {
		t.Fatal("query change retained marks")
	}
	contextManagerKey(app, 'a', tea.ModCtrl)
	if len(app.contextManager.marked) != 0 {
		t.Fatal("empty result marked hidden attachments")
	}
	contextManagerSearch(app, "")
	contextManagerKey(app, 'a', tea.ModCtrl)
	contextManagerKey(app, tea.KeyBackspace, 0)
	if len(app.composerElements) != 1 || app.composerElements[0].ID != "context-9" {
		t.Fatal("batch removed an item excluded by type")
	}
	_, prompt, err := app.composerSubmissionPrompt()
	if err != nil || strings.Count(prompt, `"kind":"terminal_output"`) != 1 || strings.Contains(prompt, `"kind":"file"`) {
		t.Fatalf("submission did not reflect filtered removal: %q %v", prompt, err)
	}
}

func TestComposerContextManagerBatchPreflightIsAtomic(t *testing.T) {
	for _, failure := range []string{"loading", "missing", "duplicate", "hidden", "stale-reference", "modal", "thread"} {
		t.Run(failure, func(t *testing.T) {
			app := outputContextApp(t, 80)
			contextManagerFixture(app, []string{"@same", "@same", "[Output #1]"}, []string{"file", "file", "terminal_output"}, []string{"first", "second", "output"})
			app.openComposerContextManager()
			ids := []string{"context-0", "context-1"}
			switch failure {
			case "loading":
				app.composerElements[1].Data = ""
			case "missing":
				ids[1] = "removed-by-async-failure"
			case "duplicate":
				ids[1] = ids[0]
			case "hidden":
				app.contextManager.filter = 2
			case "stale-reference":
				app.composerElements[1].Label = "stale"
			case "modal":
				app.pushDialog(StatePermission)
			case "thread":
				app.contextManager.threadID = "other"
			}
			before := app.captureComposerUndoEntry()
			undoCount := len(app.composerUndo)
			if app.removeManagedComposerContexts(ids) {
				t.Fatal("invalid batch succeeded")
			}
			if after := app.captureComposerUndoEntry(); !reflect.DeepEqual(before, after) || len(app.composerUndo) != undoCount {
				t.Fatal("failed batch partially mutated the draft/undo")
			}
		})
	}
	app := outputContextApp(t, 80)
	contextManagerFixture(app, []string{"@safe"}, []string{"file"}, []string{"keep"})
	app.openComposerContextManager()
	app.contextManager.marked = map[string]bool{"stale": true}
	contextManagerKey(app, tea.KeyDelete, 0)
	if len(app.composerElements) != 1 || len(app.contextManager.marked) != 0 {
		t.Fatal("stale marks fell back to deleting the highlighted attachment")
	}
}

func TestComposerContextManagerSearchOwnsInputAndBounds(t *testing.T) {
	app := outputContextApp(t, 80)
	contextManagerFixture(app, []string{"@file"}, []string{"file"}, []string{"body"})
	app.keybindResolver.SetBindings([]keybindings.Block{{Context: keybindings.ContextChat, Bindings: map[string]keybindings.Action{"ctrl+x ctrl+u": keybindings.ActionChatUndo}}})
	before := app.captureComposerUndoEntry()
	app.openComposerContextManager()
	contextManagerKey(app, 'x', tea.ModCtrl)
	contextManagerKey(app, '/', 0)
	contextManagerKey(app, 'u', tea.ModCtrl)
	updateAppSilent(app, tea.KeyPressMsg{Code: tea.KeyExtended, Text: "文件"})
	updateAppSilent(app, tea.PasteMsg{Content: "\n\t\x1b\u202e" + strings.Repeat("界", 2000)})
	query := app.contextManager.query.Value()
	if !strings.HasPrefix(query, "文件  ") || len([]rune(query)) != 128 || !utf8.ValidString(query) || strings.ContainsAny(query, "\n\t\x1b\u202e") {
		t.Fatalf("unsafe/unbounded search: %q", query)
	}
	contextManagerKey(app, tea.KeyEscape, 0)
	if !app.hasDialog(StateContextManager) || app.contextManager.searching || app.contextManager.query.Value() != query {
		t.Fatal("search Esc did not retain the query in the manager")
	}
	if resolution := app.keybindResolver.ResolveEvent(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}, keybindings.ContextChat); resolution.Kind == keybindings.ResolutionMatch {
		t.Fatal("search ownership leaked the pending undo chord")
	}
	contextManagerKey(app, '/', 0)
	app.pushDialog(StatePermission)
	updateAppSilent(app, tea.PasteMsg{Content: "hidden-search-edit"})
	if app.contextManager.query.Value() != query {
		t.Fatal("hidden search stole permission paste")
	}
	app.popDialog(StatePermission)
	contextManagerKey(app, 'c', tea.ModCtrl)
	if app.hasDialog(StateContextManager) {
		t.Fatal("Ctrl+C did not close the focused search")
	}
	if after := app.captureComposerUndoEntry(); !reflect.DeepEqual(before, after) || app.running || len(app.engine.GetMessages()) != 0 {
		t.Fatal("search mutated or submitted the draft")
	}
	app.openComposerContextManager()
	if app.contextManager.query.Value() != "" || app.contextManager.searching || app.contextManager.filter != 0 || len(app.contextManager.marked) != 0 {
		t.Fatal("reopened manager retained transient state")
	}
}

func TestComposerContextManagerBatchRebasesRuneCursor(t *testing.T) {
	// Two adjacent duplicate labels and a later output are removed around a
	// retained label whose leading space belongs to its reference.
	for _, cursor := range []int{0, 6, 7, 10, 14, 17} {
		t.Run(string(rune('a'+cursor)), func(t *testing.T) {
			app := outputContextApp(t, 80)
			app.textarea.SetValue("前 e\u0301 @同@同 保留 [O] 尾")
			app.composerElements = []threadComposerElement{
				{ID: "a", Kind: "file", Label: "@同", Data: "first", Start: 5, End: 7},
				{ID: "b", Kind: "file", Label: "@同", Data: "second", Start: 7, End: 9},
				{ID: "keep", Kind: "paste", Label: " 保留", Value: "retained paste", Start: 9, End: 12},
				{ID: "out", Kind: "terminal_output", Label: "[O]", Data: "output", Start: 13, End: 16},
			}
			app.setTextareaRuneCursor(cursor)
			before := app.captureComposerUndoEntry()
			app.openComposerContextManager()
			if !app.removeManagedComposerContexts([]string{"out", "b", "a"}) {
				t.Fatal("valid adjacent batch rejected")
			}
			if app.textarea.Value() != "前 e\u0301  保留 尾" || len(app.composerElements) != 1 || app.composerElements[0].Start != 5 || app.composerElements[0].End != 8 {
				t.Fatalf("adjacent ranges or owned space corrupted: %q %#v", app.textarea.Value(), app.composerElements)
			}
			wantCursor := cursor
			switch {
			case cursor >= 17:
				wantCursor -= 8
			case cursor >= 13:
				wantCursor = 9
			case cursor >= 9:
				wantCursor -= 4
			case cursor > 5:
				wantCursor = 5
			}
			if app.captureComposerUndoEntry().CursorOffset != wantCursor {
				t.Fatalf("cursor at %d did not rebase to %d", cursor, wantCursor)
			}
			contextManagerKey(app, 'z', tea.ModCtrl)
			if after := app.captureComposerUndoEntry(); !reflect.DeepEqual(before, after) {
				t.Fatal("adjacent batch undo lost original identities/cursor")
			}
		})
	}
}

func TestComposerContextManagerLoadedBatchRestoresImageAndSnapshot(t *testing.T) {
	app := outputContextApp(t, 80)
	contextManagerFixture(app, []string{"@file", "[Output #1]"}, []string{"file", "terminal_output"}, []string{"", "output"})
	if err := app.addComposerImageAt("pic.png", "image/png", []byte("image bytes"), len([]rune(app.textarea.Value()))); err != nil {
		t.Fatal(err)
	}
	imageID := app.composerElements[2].ID
	app.openComposerContextManager()
	contextManagerKey(app, 'a', tea.ModCtrl)
	contextManagerKey(app, tea.KeyDelete, 0)
	if len(app.composerElements) != 3 || len(app.contextManager.marked) != 3 {
		t.Fatal("loading preflight mutated the batch or lost selection")
	}
	app.Update(composerMentionPayloadMsg{elementID: "context-0", data: "captured file", mimeType: "text/plain"})
	before := app.captureComposerUndoEntry()
	contextManagerKey(app, tea.KeyDelete, 0)
	if len(app.composerElements) != 0 || app.draftMedia[imageID] == nil {
		t.Fatal("loaded batch failed or discarded undo-owned media")
	}
	app.Update(composerMentionPayloadMsg{elementID: "context-0", data: "late body"})
	contextManagerKey(app, 'z', tea.ModCtrl)
	if after := app.captureComposerUndoEntry(); !reflect.DeepEqual(before, after) || app.draftMedia[imageID] == nil || len(app.contextManager.marked) != 0 {
		t.Fatal("batch undo lost captured snapshot, media or selection reset")
	}
}

func TestComposerContextManagerSearchFocusSelectsMatchingAttachments(t *testing.T) {
	app := outputContextApp(t, 80)
	contextManagerFixture(app, []string{"@match", "@match", "@other"}, []string{"file", "file", "file"}, []string{"first", "second", "keep"})
	app.openComposerContextManager()
	contextManagerKey(app, '/', 0)
	updateAppSilent(app, tea.PasteMsg{Content: "match"})
	contextManagerKey(app, 'a', tea.ModCtrl)
	if len(app.contextManager.marked) != 2 || !app.contextManager.searching || app.contextManager.query.Value() != "match" {
		t.Fatal("focused search swallowed Ctrl+A instead of selecting matches")
	}
	contextManagerKey(app, tea.KeyEnter, 0)
	contextManagerKey(app, tea.KeyDelete, 0)
	if len(app.composerElements) != 1 || app.composerElements[0].ID != "context-2" {
		t.Fatal("search selection removed a nonmatching item")
	}
}

func TestComposerContextManagerFilteredGolden(t *testing.T) {
	var snapshots []string
	for _, width := range []int{40, 80, 120, 180} {
		app := outputContextApp(t, width)
		contextManagerFixture(app, []string{"@共享.go", "@共享_test.go", "[Output #1]", "@other"}, []string{"file", "file", "terminal_output", "file"}, []string{"first line\nsecond", "test body", "output", "excluded"})
		app.openComposerContextManager()
		contextManagerKey(app, '/', 0)
		updateAppSilent(app, tea.PasteMsg{Content: "共享"})
		contextManagerKey(app, tea.KeyTab, 0)
		contextManagerKey(app, 'a', tea.ModCtrl)
		for _, state := range []string{"searching", "marked", "empty"} {
			switch state {
			case "marked":
				contextManagerKey(app, tea.KeyEnter, 0)
			case "empty":
				contextManagerSearch(app, "no matches")
			}
			view := ansi.Strip(app.composerContextManagerOverlay("", width, 24))
			rows := strings.Split(view, "\n")
			if len(rows) > 24 {
				t.Fatal("filtered panel exceeded viewport height")
			}
			for i, row := range rows {
				if app.renderEnvironment.profile.measure(row, 0) > width {
					t.Fatal("filtered panel exceeded viewport width")
				}
				rows[i] = strings.TrimRight(row, " ")
			}
			snapshots = append(snapshots, fmt.Sprintf("== %d %s ==\n%s", width, state, strings.Trim(strings.Join(rows, "\n"), "\n")))
		}
	}
	got := strings.Join(snapshots, "\n") + "\n"
	path := filepath.Join("testdata", "composer_context_filter.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Fatalf("filtered context golden mismatch:\n%s", got)
	}
}
