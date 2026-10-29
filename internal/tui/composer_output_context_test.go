package tui

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/attachments"
	"github.com/abietic/yhc/internal/tui/keybindings"
	"github.com/charmbracelet/x/ansi"
)

func outputContextApp(t testing.TB, width int) *App {
	t.Helper()
	dir := t.TempDir()
	original := resolveHistoryLocations
	locations := historyLocations{projectRoot: dir, compatibilityConfigDir: t.TempDir()}
	resolveHistoryLocations = func() (historyLocations, error) { return locations, nil }
	t.Cleanup(func() { resolveHistoryLocations = original })
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{CWD: dir, TranscriptDir: t.TempDir()})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng, Resumed: true, KeybindingsConfigDir: t.TempDir()})
	app.Update(tea.WindowSizeMsg{Width: width, Height: 24})
	app.textarea.SetValue("diagnose ")
	app.textarea.CursorEnd()
	return app
}

func selectOutputRow(t *testing.T, app *App, text string) {
	t.Helper()
	app.chat.AppendOrUpdateTool("output-context", "Bash", `{"command":"make test"}`)
	app.chat.UpdateToolResult("output-context", "Bash", "outside-before\n"+text+"\noutside-after")
	app.chat.Render(app.layout.width, app.layout.chatHeight)
	for item, value := range app.chat.items {
		entry := app.chat.renderItem(value, app.chat.renderWidth())
		for line, row := range entry.selection {
			start, end, ok := selectionRowCellBounds(row)
			if ok && selectionRowText(app.renderEnvironment.profile, row, start, end) == text {
				app.selection.anchor = &selItemPoint{itemIdx: item, lineInItem: line, col: start}
				app.selection.focus = &selItemPoint{itemIdx: item, lineInItem: line, col: end}
				if !app.selection.ensureCompatible(app.chat) {
					t.Fatal("fixture selection did not bind its rendered source")
				}
				return
			}
		}
	}
	t.Fatal("fixture output row was not selectable")
}

func attachOutputShortcut(app *App) {
	updateAppSilent(app, tea.KeyPressMsg{Code: 'a', Mod: tea.ModAlt})
}

func TestComposerOutputContextCapturesOnlySelectedToolOutput(t *testing.T) {
	app := outputContextApp(t, 80)
	selectOutputRow(t, app, "selected error 中e\u0301")
	if selected, _, err := app.selectedOutputContext(5 * 1024 * 1024); err != nil || selected != "selected error 中e\u0301" {
		t.Fatalf("bound selection before shortcut: %q %v", selected, err)
	}
	attachOutputShortcut(app)
	if len(app.composerElements) != 1 {
		selected, _, err := app.selectedOutputContext(5 * 1024 * 1024)
		t.Fatalf("output attachment missing: state=%v mode=%v draft=%q elements=%#v selection=%q err=%v", app.state, app.inputMode, app.textarea.Value(), app.composerElements, selected, err)
	}
	element := app.composerElements[0]
	if element.Kind != "terminal_output" || element.Data != "selected error 中e\u0301" {
		t.Fatalf("output snapshot=%#v", element)
	}
	_, prompt, err := app.composerSubmissionPrompt()
	if err != nil || !strings.Contains(prompt, `"kind":"terminal_output"`) || !strings.Contains(prompt, "selected error 中e\u0301") {
		t.Fatalf("output input=%q err=%v", prompt, err)
	}
	for _, forbidden := range []string{"outside-before", "outside-after", "make test"} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("unselected output included %q", forbidden)
		}
	}
	if len(app.engine.GetMessages()) != 0 || app.running || app.selection.HasSelection() {
		t.Fatal("attaching selection submitted a turn or retained the old selection")
	}
}

func TestComposerOutputContextExpandSelectionReturnsToDraft(t *testing.T) {
	app := outputContextApp(t, 80)
	app.enterExpandContent("outside-before\nselected error\noutside-after", 0)
	app.expandConversation = true
	app.selection.expandAnchor = &expandSelPoint{row: 1, col: 0}
	app.selection.expandFocus = &expandSelPoint{row: 1, col: 14}
	attachOutputShortcut(app)
	if app.state != StateChat || len(app.composerElements) != 1 || app.composerElements[0].Data != "selected error" {
		t.Fatalf("expand selection: state=%v elements=%#v", app.state, app.composerElements)
	}
	if !strings.HasPrefix(app.textarea.Value(), "diagnose ") || app.selection.HasExpandSelection() {
		t.Fatal("expand attachment replaced the existing draft or retained selection")
	}
}

func TestComposerOutputContextRejectsStaleSelection(t *testing.T) {
	app := outputContextApp(t, 80)
	selectOutputRow(t, app, "old error")
	app.chat.UpdateToolResult("output-context", "Bash", "new error")
	app.chat.Render(app.layout.width, app.layout.chatHeight)
	attachOutputShortcut(app)
	if len(app.composerElements) != 0 || app.textarea.Value() != "diagnose " {
		t.Fatal("stale selection attached newer output or changed draft")
	}
}

func TestComposerOutputContextPreviewRemoveAndUndo(t *testing.T) {
	app := outputContextApp(t, 80)
	selectOutputRow(t, app, "selected error")
	attachOutputShortcut(app)
	preview := ansi.Strip(app.renderHintSection())
	for _, want := range []string{"Selected output", "14 B", "selected error", "Backspace"} {
		if !strings.Contains(preview, want) {
			t.Errorf("output preview missing %q: %q", want, preview)
		}
	}
	app.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if len(app.composerElements) != 0 || app.textarea.Value() != "diagnose " {
		t.Fatalf("output removal: draft=%q elements=%#v", app.textarea.Value(), app.composerElements)
	}
	app.undoComposerEdit()
	if len(app.composerElements) != 1 || app.composerElements[0].Data != "selected error" {
		t.Fatal("undo did not restore the captured output")
	}
}

func TestComposerOutputContextExtractionIsBounded(t *testing.T) {
	app := outputContextApp(t, 80)
	selectOutputRow(t, app, "selected error")
	text, _, err := app.selectedOutputContext(8)
	if !errors.Is(err, errSelectedOutputTooLarge) || text != "" {
		t.Fatalf("oversized chat selection returned partial text: %q %v", text, err)
	}
	app.enterExpandContent("first\nsecond", 0)
	app.expandConversation = true
	app.selection.expandAnchor = &expandSelPoint{row: 0, col: 0}
	app.selection.expandFocus = &expandSelPoint{row: 1, col: 6}
	text, _, err = app.selectedOutputContext(8)
	if !errors.Is(err, errSelectedOutputTooLarge) || text != "" {
		t.Fatalf("oversized expanded selection returned partial text: %q %v", text, err)
	}
	app.expandLines = []string{strings.Repeat("x", attachments.MaxAttachmentBytes+1)}
	app.selection.expandAnchor = &expandSelPoint{row: 0, col: 0}
	app.selection.expandFocus = &expandSelPoint{row: 0, col: 1}
	attachOutputShortcut(app)
	if len(app.composerElements) != 0 || app.textarea.Value() != "diagnose " {
		t.Fatal("an oversized rendered source line mutated the draft")
	}
}

func TestComposerOutputContextRejectsChangedExpandProjection(t *testing.T) {
	for _, change := range []string{"resize", "raw", "replacement"} {
		t.Run(change, func(t *testing.T) {
			app := outputContextApp(t, 80)
			app.enterExpandContent("selected error", 0)
			app.expandConversation = true
			app.selection.expandAnchor = &expandSelPoint{row: 0, col: 0}
			app.selection.expandFocus = &expandSelPoint{row: 0, col: 14}
			switch change {
			case "resize":
				app.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
			case "raw":
				app.toggleExpandRawProjection()
			case "replacement":
				app.enterExpandContent("other source", 0)
				app.expandConversation = true
			}
			attachOutputShortcut(app)
			if len(app.composerElements) != 0 || app.textarea.Value() != "diagnose " {
				t.Fatal("changed expanded projection reused stale selection coordinates")
			}
		})
	}
}

func TestComposerOutputContextKeepsSnapshotAcrossSourceAndThreadChanges(t *testing.T) {
	app := outputContextApp(t, 80)
	selectOutputRow(t, app, "selected error")
	attachOutputShortcut(app)
	app.chat.UpdateToolResult("output-context", "Bash", "replacement output")
	leader := app.activeThreadViewID()
	if err := app.switchThreadView("output-agent", engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	if err := app.switchThreadView(leader, engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	snapshot, err := app.captureComposerSubmission()
	if err != nil || !strings.Contains(snapshot.Text, "selected error") || strings.Contains(snapshot.Text, "replacement output") {
		t.Fatal("captured output followed later source mutations")
	}
	if strings.Contains(snapshot.SafeText, "selected error") || !strings.Contains(snapshot.SafeText, "output content not restored") {
		t.Fatalf("unsafe output history: %q", snapshot.SafeText)
	}
	app.recordComposerHistory(app.textarea.Value())
	app.textarea.Reset()
	app.composerElements = nil
	app.historyIdx = len(app.history)
	app.navigateHistory(-1)
	if len(app.composerElements) != 0 || strings.Contains(app.textarea.Value(), "selected error") {
		t.Fatal("history recall restored hidden output content")
	}
}

func TestComposerOutputContextRespectsInputOwners(t *testing.T) {
	for _, owner := range []string{"shell", "search", "editor", "admission", "replay", "preview"} {
		t.Run(owner, func(t *testing.T) {
			app := outputContextApp(t, 80)
			selectOutputRow(t, app, "selected error")
			switch owner {
			case "shell":
				app.inputMode = InputShell
			case "search":
				app.historySearch.Active = true
			case "editor":
				app.externalEditorActive = true
			case "admission":
				app.composerAdmissionPending = &composerAdmissionRequest{}
			case "replay":
				app.threadViews.active().Mode = engine.ThreadModeReplayOnly
			case "preview":
				app.enterExpandContent("selected error", StateResume)
				app.selection.expandAnchor = &expandSelPoint{row: 0, col: 0}
				app.selection.expandFocus = &expandSelPoint{row: 0, col: 14}
			}
			app.attachSelectedOutputContext()
			if len(app.composerElements) != 0 || app.textarea.Value() != "diagnose " {
				t.Fatal("non-editable input owner was bypassed")
			}
		})
	}
}

func TestComposerOutputContextShortcutCanBeRebound(t *testing.T) {
	app := outputContextApp(t, 80)
	selectOutputRow(t, app, "selected error")
	app.keybindResolver.SetBindings([]keybindings.Block{{
		Context:  keybindings.ContextScroll,
		Bindings: map[string]keybindings.Action{"ctrl+y": keybindings.ActionSelectionAttach},
	}})
	updateAppSilent(app, tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	if len(app.composerElements) != 1 || app.composerElements[0].Data != "selected error" {
		t.Fatal("configured selection attach shortcut was not dispatched")
	}
}

func TestComposerOutputContextThreadSwitchClosesExpandedSource(t *testing.T) {
	app := outputContextApp(t, 80)
	app.enterExpandContent("selected error", 0)
	app.expandConversation = true
	app.selection.expandAnchor = &expandSelPoint{row: 0, col: 0}
	app.selection.expandFocus = &expandSelPoint{row: 0, col: 14}
	if err := app.switchThreadView("other-output-thread", engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	if app.state != StateChat || app.expandConversation {
		t.Fatal("thread switch retained another thread's expanded source")
	}
	attachOutputShortcut(app)
	if len(app.composerElements) != 0 {
		t.Fatal("old expanded selection was attached to the new thread")
	}
}

func TestComposerOutputContextAttachmentLimitPreservesDraft(t *testing.T) {
	app := outputContextApp(t, 80)
	for range 32 {
		selectOutputRow(t, app, "selected error")
		attachOutputShortcut(app)
	}
	before := app.textarea.Value()
	selectOutputRow(t, app, "selected error")
	attachOutputShortcut(app)
	if len(app.composerElements) != 32 || app.textarea.Value() != before {
		t.Fatal("attachment count limit mutated the full draft")
	}
}

func TestComposerOutputContextDraftByteLimitPreservesExistingPayloads(t *testing.T) {
	app := outputContextApp(t, 80)
	selectOutputRow(t, app, "selected error")
	data := strings.Repeat("x", attachments.MaxAttachmentBytes)
	app.textarea.SetValue("@first.go @second.go ")
	app.textarea.CursorEnd()
	app.composerElements = []threadComposerElement{
		{ID: "first", Kind: composerElementKindFile, Label: "@first.go", Value: "first.go", Data: data, Start: 0, End: 9},
		{ID: "second", Kind: composerElementKindFile, Label: "@second.go", Value: "second.go", Data: data, Start: 10, End: 20},
	}
	before := app.textarea.Value()
	attachOutputShortcut(app)
	if len(app.composerElements) != 2 || app.textarea.Value() != before || app.composerElements[0].Data != data {
		t.Fatal("aggregate byte rejection damaged existing context")
	}
}

func TestComposerOutputContextPreviewWidthGolden(t *testing.T) {
	var got strings.Builder
	for _, width := range []int{40, 80, 120, 180} {
		app := outputContextApp(t, width)
		app.enterExpandContent(strings.Repeat("界", 70)+"\nsecond selected line\nthird selected line\nhidden selected fourth", 0)
		app.expandConversation = true
		app.selection.expandAnchor = &expandSelPoint{row: 0, col: 0}
		app.selection.expandFocus = &expandSelPoint{row: 3, col: 22}
		attachOutputShortcut(app)
		rendered := normalizeAppLayoutGolden(app.renderHintSection())
		for _, want := range []string{"Selected output", "Preview clipped", "Backspace"} {
			if !strings.Contains(rendered, want) {
				t.Fatalf("width %d lost %s: %s", width, want, rendered)
			}
		}
		for _, row := range strings.Split(rendered, "\n") {
			if app.renderEnvironment.profile.measure(row, 0) > width {
				t.Fatalf("output preview overflows width %d", width)
			}
		}
		_, prompt, err := app.composerSubmissionPrompt()
		if err != nil || !strings.Contains(prompt, "hidden selected fourth") || strings.Contains(rendered, "hidden selected fourth") {
			t.Fatal("output preview clipping changed the attached selection")
		}
		got.WriteString("== " + strconv.Itoa(width) + " ==\n" + rendered + "\n")
	}
	path := "testdata/composer_output_context.golden"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != string(want) {
		t.Fatalf("output context golden mismatch:\n%s", got.String())
	}
}

func TestComposerOutputContextRuneRangesPreserveUnicodeDraft(t *testing.T) {
	app := outputContextApp(t, 80)
	prefix := "诊断 👩🏽‍💻 "
	app.textarea.SetValue(prefix + "suffix")
	app.setTextareaRuneCursor(len([]rune(prefix)))
	selectOutputRow(t, app, "selected error")
	attachOutputShortcut(app)
	if len(app.composerElements) != 1 {
		t.Fatal("Unicode draft did not accept output context")
	}
	element := app.composerElements[0]
	runes := []rune(app.textarea.Value())
	if element.Start != len([]rune(prefix)) || string(runes[element.Start:element.End]) != element.Label || app.textarea.Value() != prefix+element.Label+" suffix" {
		t.Fatalf("output used display cells instead of source rune offsets: text=%q element=%#v", app.textarea.Value(), element)
	}
}

func TestComposerOutputContextCannotOverwriteExistingReference(t *testing.T) {
	app := outputContextApp(t, 80)
	selectOutputRow(t, app, "selected error")
	attachOutputShortcut(app)
	before := app.textarea.Value()
	app.setTextareaRuneCursor(app.composerElements[0].Start + 2)
	selectOutputRow(t, app, "replacement context")
	attachOutputShortcut(app)
	if app.textarea.Value() != before || len(app.composerElements) != 1 || app.composerElements[0].Data != "selected error" {
		t.Fatal("insertion inside a reference discarded the existing context")
	}
}
