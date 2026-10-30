package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/keybindings"
	"github.com/charmbracelet/x/ansi"
)

func contextManagerNavigationIDs(app *App, count int) []string {
	contextManagerKey(app, tea.KeyHome, 0)
	var ids []string
	for i := 0; i < count; i++ {
		ids = append(ids, app.contextManager.selectedID)
		contextManagerKey(app, tea.KeyDown, 0)
	}
	return ids
}

func TestComposerContextManagerTokenSortIsStableAndPassive(t *testing.T) {
	app := outputContextApp(t, 80)
	contextManagerFixture(app, []string{"[large]", "[same]", "[same]", "@loading", "@unknown"},
		[]string{"paste", "paste", "paste", "file", "unsupported"},
		[]string{strings.Repeat("x", 800), strings.Repeat("y", 80), strings.Repeat("z", 80), "", "body"})
	if err := app.addComposerImageAt("pic.png", "image/png", []byte("fixture"), len([]rune(app.textarea.Value()))); err != nil {
		t.Fatal(err)
	}
	imageID := app.composerElements[5].ID
	before := app.captureComposerUndoEntry()
	undoCount := len(app.composerUndo)
	app.openComposerContextManager()
	contextManagerKey(app, tea.KeyHome, 0)
	contextManagerKey(app, tea.KeySpace, 0)
	for _, tt := range []struct {
		label string
		ids   []string
	}{
		{"Tokens high", []string{"context-0", imageID, "context-1", "context-2", "context-3", "context-4"}},
		{"Tokens low", []string{"context-1", "context-2", imageID, "context-0", "context-3", "context-4"}},
		{"Draft", []string{"context-0", "context-1", "context-2", "context-3", "context-4", imageID}},
	} {
		selected := app.contextManager.selectedID
		contextManagerKey(app, 's', 0)
		view := ansi.Strip(app.composerContextManagerOverlay("", 80, 24))
		if !strings.Contains(view, "Sort: "+tt.label) || app.contextManager.selectedID != selected || !app.contextManager.marked["context-0"] {
			t.Fatalf("sort lost its label, identity or marks: %s", view)
		}
		if got := contextManagerNavigationIDs(app, 6); !reflect.DeepEqual(got, tt.ids) {
			t.Fatalf("%s navigation order: %v, want %v", tt.label, got, tt.ids)
		}
	}
	if !reflect.DeepEqual(before, app.captureComposerUndoEntry()) || len(app.composerUndo) != undoCount || app.running || len(app.engine.GetMessages()) != 0 {
		t.Fatal("sorting changed the rich draft, undo history or submission")
	}
	contextManagerKey(app, tea.KeyEsc, 0)
	app.openComposerContextManager()
	if view := ansi.Strip(app.composerContextManagerOverlay("", 80, 24)); !strings.Contains(view, "Sort: Draft") || len(app.contextManager.marked) != 0 {
		t.Fatal("sort/marks did not reset on reopen")
	}
}

func TestComposerContextManagerSortedFilterLoadDeleteUndoAndLocate(t *testing.T) {
	app := outputContextApp(t, 80)
	contextManagerFixture(app, []string{"@same", "@same", "[Output #1]"}, []string{"file", "file", "terminal_output"}, []string{"small", "", "output"})
	app.openComposerContextManager()
	contextManagerKey(app, 's', 0)
	contextManagerKey(app, tea.KeyTab, 0)
	if got := contextManagerNavigationIDs(app, 2); !reflect.DeepEqual(got, []string{"context-0", "context-1"}) {
		t.Fatalf("pending item not last in sorted file filter: %v", got)
	}
	contextManagerKey(app, tea.KeySpace, 0)
	app.Update(composerMentionPayloadMsg{elementID: "context-1", data: strings.Repeat("loaded", 300), mimeType: "text/plain"})
	before := app.captureComposerUndoEntry()
	view := ansi.Strip(app.composerContextManagerOverlay("", 80, 24))
	if app.contextManager.selectedID != "context-1" || app.contextManager.cursor != 0 || !app.contextManager.marked["context-1"] || strings.Contains(view, "pending") {
		t.Fatalf("load refresh did not reorder by stable identity: %s", view)
	}
	contextManagerKey(app, tea.KeyDelete, 0)
	if len(app.composerElements) != 2 || app.composerElements[0].ID != "context-0" || app.composerElements[1].ID != "context-2" {
		t.Fatal("sorted removal deleted an index instead of the marked identity")
	}
	contextManagerKey(app, 'z', tea.ModCtrl)
	if !reflect.DeepEqual(before, app.captureComposerUndoEntry()) {
		t.Fatal("undo did not restore original draft order/payloads")
	}
	contextManagerKey(app, tea.KeyHome, 0)
	wantCursor := app.composerElements[1].End
	contextManagerKey(app, tea.KeyEnter, 0)
	if app.captureComposerUndoEntry().CursorOffset != wantCursor || app.hasDialog(StateContextManager) {
		t.Fatal("sorted locate did not return to the original rune range")
	}
}

func TestComposerContextManagerSortKeyBelongsToSearchWhileFocused(t *testing.T) {
	app := outputContextApp(t, 80)
	contextManagerFixture(app, []string{"@small", "@large"}, []string{"file", "file"}, []string{"small", strings.Repeat("large", 100)})
	app.openComposerContextManager()
	contextManagerKey(app, '/', 0)
	updateAppSilent(app, tea.KeyPressMsg{Code: 's', Text: "s"})
	if app.contextManager.query.Value() != "s" || !app.contextManager.searching {
		t.Fatal("sort shortcut intercepted search text")
	}
	contextManagerKey(app, tea.KeyEnter, 0)
	if view := ansi.Strip(app.composerContextManagerOverlay("", 80, 24)); !strings.Contains(view, "Sort: Draft") {
		t.Fatal("typing s changed sort mode")
	}
}

func TestComposerContextManagerSortRespectsConfiguredUndo(t *testing.T) {
	for _, binding := range []string{"s", "s u"} {
		t.Run(binding, func(t *testing.T) {
			app := outputContextApp(t, 80)
			contextManagerFixture(app, []string{"@file"}, []string{"file"}, []string{"captured"})
			app.keybindResolver.SetBindings([]keybindings.Block{{Context: keybindings.ContextChat, Bindings: map[string]keybindings.Action{binding: keybindings.ActionChatUndo}}})
			before := app.captureComposerUndoEntry()
			app.openComposerContextManager()
			contextManagerKey(app, tea.KeyDelete, 0)
			contextManagerKey(app, 's', 0)
			if binding == "s u" {
				contextManagerKey(app, 'u', 0)
			}
			if !reflect.DeepEqual(before, app.captureComposerUndoEntry()) {
				t.Fatal("sort shortcut swallowed a configured undo key/chord")
			}
		})
	}
}

func TestComposerContextBudgetWarningBoundariesAndOwnership(t *testing.T) {
	for _, tt := range []struct {
		name, model string
		tokens      int
		pending     bool
		want        string
	}{
		{"below", "gpt-4o", 102399, false, ""},
		{"near", "gpt-4o", 102400, false, "Warn: attached est. >=80% window"},
		{"near-upper", "gpt-4o", 127999, false, "Warn: attached est. >=80% window"},
		{"at", "gpt-4o", 128000, false, "Alert: attached est. >=100% window"},
		{"over", "gpt-4o", 128001, false, "Alert: attached est. >=100% window"},
		{"partial", "gpt-4o", 102400, true, "Warn: partial est. >=80% window"},
		{"partial-below", "gpt-4o", 102399, true, ""},
		{"partial-over", "gpt-4o", 128001, true, "Alert: partial est. >=100% window"},
		{"unknown", "unknown-fixture", 128001, false, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app := outputContextApp(t, 80)
			app.SetEngine(contextBudgetEngine(t, tt.model, true))
			contextManagerFixture(app, []string{"[large]", "@pending"}, []string{"paste", "file"}, []string{strings.Repeat("x", tt.tokens*4), "small"})
			if tt.pending {
				app.composerElements[1].Data = ""
			} else {
				// Keep the threshold oracle exact, with only the paste's tokens.
				app.composerElements = app.composerElements[:1]
			}
			before := app.captureComposerUndoEntry()
			app.openComposerContextManager()
			for _, height := range []int{12, 18, 24} {
				view := ansi.Strip(app.composerContextManagerOverlay("", 40, height))
				if tt.want != "" && !strings.Contains(view, tt.want) || tt.want == "" && (strings.Contains(view, "Warn:") || strings.Contains(view, "Alert:")) {
					t.Fatalf("warning at height %d: want %q: %s", height, tt.want, view)
				}
				if !strings.Contains(view, "Sort:") || !strings.Contains(view, "Esc close") {
					t.Fatalf("warning displaced compact controls: %s", view)
				}
			}
			contextManagerSearch(app, "nothing-matches")
			view := ansi.Strip(app.composerContextManagerOverlay("", 80, 24))
			if tt.want != "" && !strings.Contains(view, tt.want) || !strings.Contains(view, "No matching context") {
				t.Fatal("filter hid the draft-wide warning")
			}
			if !reflect.DeepEqual(before, app.captureComposerUndoEntry()) || app.running || len(app.engine.GetMessages()) != 1 {
				t.Fatal("warning changed/submitted the draft or provider facts")
			}
		})
	}
}

func TestComposerContextBudgetWarningRemoveUndo(t *testing.T) {
	app := outputContextApp(t, 80)
	app.SetEngine(contextBudgetEngine(t, "gpt-4o", false))
	contextManagerFixture(app, []string{"[large]", "[small]"}, []string{"paste", "paste"}, []string{strings.Repeat("x", 128001*4), "small"})
	app.openComposerContextManager()
	contextManagerKey(app, 's', 0)
	contextManagerKey(app, tea.KeyHome, 0)
	contextManagerKey(app, tea.KeyDelete, 0)
	if view := ansi.Strip(app.composerContextManagerOverlay("", 80, 24)); strings.Contains(view, "Alert:") || strings.Contains(view, "Warn:") {
		t.Fatal("removed attachment left a stale warning")
	}
	contextManagerKey(app, 'z', tea.ModCtrl)
	if view := ansi.Strip(app.composerContextManagerOverlay("", 80, 24)); !strings.Contains(view, "Alert: attached est. >=100% window") {
		t.Fatal("undo did not restore the warning")
	}
}

func TestComposerContextBudgetWarningLoadAndChildIsolation(t *testing.T) {
	app := outputContextApp(t, 80)
	app.SetEngine(contextBudgetEngine(t, "gpt-4o", false))
	contextManagerFixture(app, []string{"@loading"}, []string{"file"}, []string{""})
	app.openComposerContextManager()
	if view := ansi.Strip(app.composerContextManagerOverlay("", 80, 24)); strings.Contains(view, "Warn:") || strings.Contains(view, "Alert:") {
		t.Fatal("pending payload invented a budget warning")
	}
	app.Update(composerMentionPayloadMsg{elementID: "context-0", data: strings.Repeat("x", 128000*4), mimeType: "text/plain"})
	if view := ansi.Strip(app.composerContextManagerOverlay("", 80, 24)); !strings.Contains(view, "Alert: attached est. >=100% window") {
		t.Fatal("completed payload did not refresh the warning")
	}
	if err := app.switchThreadView("child-sort-warning", engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	contextManagerFixture(app, []string{"[large]"}, []string{"paste"}, []string{strings.Repeat("x", 128001*4)})
	app.openComposerContextManager()
	view := ansi.Strip(app.composerContextManagerOverlay("", 80, 24))
	if !strings.Contains(view, "Window: unavailable") || strings.Contains(view, "Warn:") || strings.Contains(view, "Alert:") {
		t.Fatal("child warning reused the leader's capacity")
	}
}

func TestComposerContextBudgetSortedWarningGolden(t *testing.T) {
	var snapshots []string
	for _, size := range [][2]int{{40, 12}, {40, 24}, {80, 24}, {120, 24}, {180, 24}} {
		width, height := size[0], size[1]
		for _, state := range []string{"partial-near", "over"} {
			app := outputContextApp(t, width)
			app.SetEngine(contextBudgetEngine(t, "gpt-4o", true))
			tokens, pending := 110000, ""
			if state == "over" {
				tokens, pending = 128001, "ready"
			}
			contextManagerFixture(app, []string{"@pending", "[large]", "[Output #1]"}, []string{"file", "paste", "terminal_output"}, []string{pending, strings.Repeat("x", tokens*4), "small output"})
			if err := app.addComposerImageAt("capture.png", "image/png", []byte("fixture-image"), len([]rune(app.textarea.Value()))); err != nil {
				t.Fatal(err)
			}
			app.openComposerContextManager()
			contextManagerKey(app, 's', 0)
			contextManagerKey(app, tea.KeyHome, 0)
			contextManagerKey(app, tea.KeySpace, 0)
			view := ansi.Strip(app.composerContextManagerOverlay("", width, height))
			rows := strings.Split(view, "\n")
			if len(rows) > height || !strings.Contains(view, "Sort: Tokens high") || !strings.Contains(view, "[x] 1. [large]") || !strings.Contains(view, "Esc close") {
				t.Fatalf("sorted warning displaced rows or controls: %s", view)
			}
			for i, row := range rows {
				if app.renderEnvironment.profile.measure(row, 0) > width {
					t.Fatalf("sorted warning exceeded width %d", width)
				}
				rows[i] = strings.TrimRight(row, " ")
			}
			snapshots = append(snapshots, fmt.Sprintf("== %dx%d %s ==\n%s", width, height, state, strings.Trim(strings.Join(rows, "\n"), "\n")))
		}
	}
	got := strings.Join(snapshots, "\n") + "\n"
	path := filepath.Join("testdata", "composer_context_sort_warning.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("sorted warning golden mismatch:\n%s", got)
	}
}
