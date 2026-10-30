package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/keybindings"
	"github.com/charmbracelet/x/ansi"
)

func contextManagerKey(app *App, code rune, mod tea.KeyMod) {
	updateAppSilent(app, tea.KeyPressMsg{Code: code, Mod: mod})
}

func contextManagerFixture(app *App, labels, kinds, bodies []string) {
	text := "前言 e\u0301 "
	app.composerElements = nil
	for i, label := range labels {
		start := len([]rune(text))
		item := threadComposerElement{
			ID: fmt.Sprintf("context-%d", i), Kind: kinds[i], Label: label, Name: "sample",
			Value: "source", Data: bodies[i], MIMEType: "text/plain", Start: start, End: start + len([]rune(label)),
		}
		if item.Kind == composerElementKindPaste {
			item.Value, item.Data = item.Data, ""
		}
		app.composerElements = append(app.composerElements, item)
		text += label + " "
	}
	app.textarea.SetValue(text + "尾文")
	app.textarea.CursorEnd()
}

func TestComposerContextManagerDuplicateLabelsRemoveExactIdentity(t *testing.T) {
	for _, index := range []int{0, 1} {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			app := outputContextApp(t, 80)
			contextManagerFixture(app, []string{"@same", "@same", "[Output #1]"}, []string{"file", "file", "terminal_output"}, []string{"first file", "second file", "selected output"})
			before := app.captureComposerUndoEntry()
			contextManagerKey(app, 'c', tea.ModAlt)
			contextManagerKey(app, tea.KeyHome, 0)
			if index == 1 {
				contextManagerKey(app, tea.KeyDown, 0)
			}
			contextManagerKey(app, tea.KeyDelete, 0)
			if len(app.composerElements) != 2 || app.composerElements[0].ID != fmt.Sprintf("context-%d", 1-index) || app.composerElements[0].Data != []string{"second file", "first file"}[index] {
				t.Fatalf("removed the wrong identical reference: %#v", app.composerElements)
			}
			if app.captureComposerUndoEntry().CursorOffset != before.CursorOffset-len([]rune("@same ")) || app.textarea.Value() != "前言 e\u0301 @same [Output #1] 尾文" {
				t.Fatalf("unrelated Unicode draft/cursor changed: %q", app.textarea.Value())
			}
			_, prompt, err := app.composerSubmissionPrompt()
			if err != nil || !strings.Contains(prompt, "selected output") || strings.Contains(prompt, []string{"first file", "second file"}[index]) {
				t.Fatalf("removed payload survived: %q %v", prompt, err)
			}
			contextManagerKey(app, 'z', tea.ModCtrl)
			if after := app.captureComposerUndoEntry(); !reflect.DeepEqual(before, after) {
				t.Fatalf("undo did not restore rich draft: %#v", after)
			}
		})
	}
}

func TestComposerContextManagerPreviewsAllTextKindsAndImageMetadata(t *testing.T) {
	app := outputContextApp(t, 80)
	contextManagerFixture(app, []string{"@file", "[Output #1]", "@skill:check", "@mcp:srv/doc", "[Pasted Content 8 chars]"}, []string{"file", "terminal_output", "skill", "mcp_resource", "paste"}, []string{"file body", "output body", "skill body", "resource body", "paste body"})
	if err := app.addComposerImageAt("capture.png", "image/png", []byte("private-image-bytes"), len([]rune(app.textarea.Value()))); err != nil {
		t.Fatal(err)
	}
	imageID := app.composerElements[5].ID
	contextManagerKey(app, 'c', tea.ModAlt)
	contextManagerKey(app, tea.KeyHome, 0)
	for _, body := range []string{"file body", "output body", "skill body", "resource body", "paste body"} {
		view := ansi.Strip(app.View().Content)
		if !strings.Contains(view, body) || strings.Contains(view, "private-image-bytes") {
			t.Fatalf("selected context preview: %s", view)
		}
		contextManagerKey(app, tea.KeyDown, 0)
	}
	view := ansi.Strip(app.View().Content)
	for _, want := range []string{"capture.png", "image/png", "19 B", "metadata only"} {
		if !strings.Contains(view, want) {
			t.Errorf("image metadata missing %q: %s", want, view)
		}
	}
	contextManagerKey(app, tea.KeyDelete, 0)
	if len(app.composerElements) != 5 || app.draftMedia[imageID] == nil {
		t.Fatal("image removal lost undo media or failed to remove its reference")
	}
	contextManagerKey(app, 'z', tea.ModCtrl)
	if len(app.composerElements) != 6 || app.composerElements[5].ID != imageID {
		t.Fatal("image undo lost media identity")
	}
}

func TestComposerContextManagerLoadingCompletionAndSnapshot(t *testing.T) {
	app := fileRangeComposer(t, "main.go", "outside\nselected body\noutside\n", "inspect @ma#2")
	cmd := app.acceptMentionHint()
	contextManagerKey(app, 'c', tea.ModAlt)
	before := app.textarea.Value()
	contextManagerKey(app, tea.KeyDelete, 0)
	if len(app.composerElements) != 1 || app.textarea.Value() != before {
		t.Fatal("loading content was removed without a captured undo payload")
	}
	app.Update(cmd())
	if err := os.WriteFile(filepath.Join(app.engine.GetCWD(), "main.go"), []byte("changed disk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	view := ansi.Strip(app.View().Content)
	if !strings.Contains(view, "selected body") || strings.Contains(view, "changed disk") || strings.Contains(view, "Loading attached") {
		t.Fatalf("manager reread the source or lost payload completion: %s", view)
	}
	contextManagerKey(app, tea.KeyBackspace, 0)
	if len(app.composerElements) != 0 {
		t.Fatal("loaded context was not removed")
	}
	app.Update(composerMentionPayloadMsg{elementID: "mention-1", data: "late result"})
	if len(app.composerElements) != 0 {
		t.Fatal("late payload reattached removed context")
	}
	contextManagerKey(app, 'z', tea.ModCtrl)
	if len(app.composerElements) != 1 || app.composerElements[0].Data != "selected body\n" {
		t.Fatal("undo did not restore the captured file snapshot")
	}
}

func TestComposerContextManagerModalAndThreadOwnership(t *testing.T) {
	app := outputContextApp(t, 80)
	contextManagerFixture(app, []string{"[Output #1]"}, []string{"terminal_output"}, []string{"captured"})
	before := app.captureComposerUndoEntry()
	leader := app.activeThreadViewID()
	contextManagerKey(app, 'c', tea.ModAlt)
	app.Update(tea.PasteMsg{Content: "must not enter draft"})
	contextManagerKey(app, 'x', 0)
	if after := app.captureComposerUndoEntry(); !reflect.DeepEqual(before, after) {
		t.Fatal("modal input leaked to the composer")
	}
	app.pushDialog(StatePermission)
	if app.removeManagedComposerContext("context-0") {
		t.Fatal("hidden context manager bypassed permission input ownership")
	}
	app.popDialog(StatePermission)
	if err := app.switchThreadView("another-thread", engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	if app.hasDialog(StateContextManager) || app.removeManagedComposerContext("context-0") {
		t.Fatal("context manager retained authority over another thread")
	}
	if err := app.switchThreadView(leader, engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	if after := app.captureComposerUndoEntry(); !reflect.DeepEqual(before, after) {
		t.Fatal("switching threads changed the original rich draft")
	}
}

func TestComposerContextManagerRejectsIncompatibleInputOwners(t *testing.T) {
	for _, owner := range []string{"shell", "command", "search", "editor", "admission", "image", "replay", "dialog"} {
		t.Run(owner, func(t *testing.T) {
			app := outputContextApp(t, 80)
			switch owner {
			case "shell":
				app.inputMode = InputShell
			case "command":
				app.inputMode = InputCommand
			case "search":
				app.historySearch.Active = true
			case "editor":
				app.externalEditorActive = true
			case "admission":
				app.composerAdmissionPending = &composerAdmissionRequest{}
			case "image":
				app.composerImageLoadPending = &composerImageLoadRequest{}
			case "replay":
				app.threadViews.active().Mode = engine.ThreadModeReplayOnly
			case "dialog":
				app.pushDialog(StateHelp)
			}
			app.openComposerContextManager()
			if app.hasDialog(StateContextManager) {
				t.Fatal("manager stole input ownership")
			}
		})
	}
}

func TestComposerContextManagerPreviewBoundedGolden(t *testing.T) {
	var snapshots []string
	for _, width := range []int{40, 80, 120, 180} {
		app := outputContextApp(t, width)
		contextManagerFixture(app, []string{"@main.go#20-22", "[Output #1]"}, []string{"file", "terminal_output"}, []string{"captured first line\nsecond\nthird\nfourth", strings.Repeat("界", 360) + "\x1b[31m\nsecond\nthird\nfourth"})
		app.composerElements[0].StartLine, app.composerElements[0].EndLine, app.composerElements[0].TotalLines = 20, 22, 100
		contextManagerKey(app, 'c', tea.ModAlt)
		contextManagerKey(app, tea.KeyEnd, 0)
		view := ansi.Strip(app.composerContextManagerOverlay("", width, 24))
		if !strings.Contains(view, "Preview clipped;") || strings.Contains(view, "\x1b") {
			t.Fatalf("unsafe or missing clipped preview: %s", view)
		}
		for _, line := range strings.Split(view, "\n") {
			if app.renderEnvironment.profile.measure(line, 0) > width {
				t.Fatalf("preview exceeded width %d", width)
			}
		}
		rows := strings.Split(view, "\n")
		for i := range rows {
			rows[i] = strings.TrimRight(rows[i], " ")
		}
		snapshots = append(snapshots, fmt.Sprintf("== %d ==\n%s", width, strings.Trim(strings.Join(rows, "\n"), "\n")))
	}
	got := strings.Join(snapshots, "\n") + "\n"
	path := filepath.Join("testdata", "composer_context_manager.golden")
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
		t.Fatalf("context manager golden mismatch:\n%s", got)
	}
}

func TestComposerContextManagerEmptyLocateAndEsc(t *testing.T) {
	app := outputContextApp(t, 40)
	contextManagerKey(app, 'c', tea.ModAlt)
	if !strings.Contains(ansi.Strip(app.View().Content), "No attached context") {
		t.Fatal("missing empty state")
	}
	contextManagerKey(app, tea.KeyDelete, 0)
	contextManagerKey(app, tea.KeyEnter, 0)
	if app.running || app.hasDialog(StateContextManager) {
		t.Fatal("empty manager submitted a turn")
	}
	contextManagerFixture(app, []string{"@file"}, []string{"file"}, []string{"text"})
	contextManagerKey(app, 'c', tea.ModAlt)
	contextManagerKey(app, tea.KeyEnter, 0)
	if app.captureComposerUndoEntry().CursorOffset != app.composerElements[0].End || app.running {
		t.Fatal("locate did not return to the reference")
	}
	contextManagerKey(app, 'c', tea.ModAlt)
	contextManagerKey(app, 'c', tea.ModCtrl)
	if app.hasDialog(StateContextManager) || app.running {
		t.Fatal("Ctrl+C did not close the passive manager")
	}
}

func TestComposerContextManagerLiteralPreviewAndConfiguredActions(t *testing.T) {
	app := outputContextApp(t, 80)
	body := "first\tline\x1b[31m\nsecond\nthird\nfourth"
	contextManagerFixture(app, []string{"@file"}, []string{"file"}, []string{body})
	app.keybindResolver.SetBindings([]keybindings.Block{{Context: keybindings.ContextChat, Bindings: map[string]keybindings.Action{"ctrl+y": keybindings.ActionChatManageContext, "alt+u": keybindings.ActionChatUndo}}})
	contextManagerKey(app, 'y', tea.ModCtrl)
	view := ansi.Strip(app.composerContextManagerOverlay("", 80, 24))
	for _, want := range []string{`\u0009`, `\u001b`, "alt+u undo"} {
		if !strings.Contains(view, want) {
			t.Errorf("literal/configured preview missing %q: %s", want, view)
		}
	}
	if strings.Contains(view, "fourth") || app.composerElements[0].Data != body {
		t.Fatal("preview clipping changed or exposed the rest of the payload")
	}
	contextManagerKey(app, tea.KeyDelete, 0)
	contextManagerKey(app, 'u', tea.ModAlt)
	if len(app.composerElements) != 1 || app.composerElements[0].Data != body {
		t.Fatal("configured undo did not restore captured context")
	}
	prefix, clipped := composerContextPrefix(strings.Repeat("界", 400), 1024)
	if !clipped || !utf8.ValidString(prefix) || len(prefix) > 1024 {
		t.Fatal("preview prefix split UTF-8 or exceeded its byte bound")
	}
}

func TestComposerContextManagerViewportAndStaleReference(t *testing.T) {
	app := outputContextApp(t, 40)
	labels, kinds, bodies := make([]string, 32), make([]string, 32), make([]string, 32)
	for i := range labels {
		labels[i] = fmt.Sprintf("@file-%02d", i)
		kinds[i] = "file"
		bodies[i] = "body"
	}
	contextManagerFixture(app, labels, kinds, bodies)
	contextManagerKey(app, 'c', tea.ModAlt)
	contextManagerKey(app, tea.KeyEnd, 0)
	for _, height := range []int{6, 12, 18, 24} {
		view := ansi.Strip(app.composerContextManagerOverlay("", 40, height))
		if len(strings.Split(view, "\n")) > height {
			t.Fatal("context manager exceeded viewport height")
		}
		for _, line := range strings.Split(view, "\n") {
			if app.renderEnvironment.profile.measure(line, 0) > 40 {
				t.Fatal("context manager exceeded viewport width")
			}
		}
		if height >= 12 && (!strings.Contains(view, "@file-31") || strings.Contains(view, "@file-00")) {
			t.Fatal("context list did not scroll to the selected attachment")
		}
	}
	before := app.textarea.Value()
	app.composerElements[31].Label = "edited reference"
	contextManagerKey(app, tea.KeyDelete, 0)
	if app.hasDialog(StateContextManager) || app.textarea.Value() != before || len(app.composerElements) != 32 {
		t.Fatal("stale reference removed or changed a draft")
	}
}

func TestComposerContextManagerDoesNotLeakUndoChordAcrossOwners(t *testing.T) {
	for _, close := range []string{"escape", "interrupt", "permission", "thread"} {
		t.Run(close, func(t *testing.T) {
			app := outputContextApp(t, 80)
			contextManagerFixture(app, []string{"@file"}, []string{"file"}, []string{"body"})
			app.keybindResolver.SetBindings([]keybindings.Block{{Context: keybindings.ContextChat, Bindings: map[string]keybindings.Action{"ctrl+x ctrl+u": keybindings.ActionChatUndo}}})
			app.openComposerContextManager()
			contextManagerKey(app, 'x', tea.ModCtrl)
			switch close {
			case "escape":
				contextManagerKey(app, tea.KeyEscape, 0)
			case "interrupt":
				contextManagerKey(app, 'c', tea.ModCtrl)
			case "permission":
				app.pushDialog(StatePermission)
			case "thread":
				if err := app.switchThreadView("other", engine.ThreadModeLiveAttach); err != nil {
					t.Fatal(err)
				}
			}
			resolution := app.keybindResolver.ResolveEvent(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}, keybindings.ContextChat)
			if resolution.Kind == keybindings.ResolutionMatch && resolution.Action == keybindings.ActionChatUndo {
				t.Fatal("modal undo chord crossed an input-owner change")
			}
		})
	}
}

func TestComposerContextManagerOpensWithoutMovingOrSubmittingDraft(t *testing.T) {
	app := outputContextApp(t, 80)
	selectOutputRow(t, app, "selected failure")
	attachOutputShortcut(app)
	before := app.captureComposerUndoEntry()
	undo := len(app.composerUndo)
	contextManagerKey(app, 'c', tea.ModAlt)
	if _, active := app.activeDialogState(); !active {
		t.Fatal("Alt+C did not open the attached-context manager")
	}
	view := ansi.Strip(app.View().Content)
	for _, want := range []string{"Attached context", "[Output #1]", "selected failure"} {
		if !strings.Contains(view, want) {
			t.Errorf("manager missing %q: %s", want, view)
		}
	}
	contextManagerKey(app, tea.KeyEscape, 0)
	after := app.captureComposerUndoEntry()
	if before.Text != after.Text || before.CursorOffset != after.CursorOffset || len(app.composerUndo) != undo || app.running || len(app.engine.GetMessages()) != 0 {
		t.Fatal("opening/closing context management changed or submitted the draft")
	}
}
