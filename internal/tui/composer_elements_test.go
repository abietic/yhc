package tui

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/attachments"
)

func pasteKey(text string) tea.PasteMsg {
	return tea.PasteMsg{Content: text}
}

func handlePaste(app *App, text string) tea.Cmd {
	return app.handleComposerPaste(pasteKey(text))
}

func TestLargePasteUsesRangeElementAndExpandsForSubmission(t *testing.T) {
	app := New(Config{})
	pasted := strings.Repeat("内容", attachments.PasteThreshold/2+1)

	handlePaste(app, pasted)

	if len(app.composerElements) != 1 {
		t.Fatalf("composer elements = %#v", app.composerElements)
	}
	element := app.composerElements[0]
	if element.Kind != composerElementKindPaste || element.Value != pasted {
		t.Fatalf("paste element = %#v", element)
	}
	if got := string([]rune(app.textarea.Value())[element.Start:element.End]); got != element.Label {
		t.Fatalf("element range contains %q, want %q", got, element.Label)
	}
	display, expanded := app.composerSubmissionTexts()
	if display != element.Label {
		t.Fatalf("display = %q, want placeholder %q", display, element.Label)
	}
	if expanded != pasted {
		t.Fatalf("expanded submission length = %d, want %d", len(expanded), len(pasted))
	}
}

func TestComposerElementRebasesBeforeEditAndPrunesOverlap(t *testing.T) {
	app := New(Config{})
	pasted := strings.Repeat("x", attachments.PasteThreshold+1)
	handlePaste(app, pasted)
	original := app.composerElements[0]

	app.textarea.CursorStart()
	app.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("prefix "))})
	if len(app.composerElements) != 1 {
		t.Fatalf("element was lost after prefix edit: %#v", app.composerElements)
	}
	shifted := app.composerElements[0]
	if shifted.Start != original.Start+len([]rune("prefix ")) || shifted.End != original.End+len([]rune("prefix ")) {
		t.Fatalf("rebased range = %d..%d, want %d..%d", shifted.Start, shifted.End, original.Start+7, original.End+7)
	}

	app.textarea.CursorEnd()
	app.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if len(app.composerElements) != 0 {
		t.Fatalf("overlapping edit retained payload: %#v", app.composerElements)
	}
	if _, expanded := app.composerSubmissionTexts(); expanded == "prefix "+pasted {
		t.Fatal("pruned placeholder still expanded to hidden payload")
	}
}

func TestLargePasteSurvivesThreadSwitchWithoutPayloadTruncation(t *testing.T) {
	app := New(Config{})
	pasted := strings.Repeat("z", attachments.PasteThreshold+500)
	handlePaste(app, pasted)
	leaderID := app.activeThreadViewID()

	if err := app.switchThreadView("agent-thread", engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	if err := app.switchThreadView(leaderID, engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}

	if len(app.composerElements) != 1 || app.composerElements[0].Value != pasted {
		t.Fatalf("restored paste payload = %#v", app.composerElements)
	}
	if _, expanded := app.composerSubmissionTexts(); expanded != pasted {
		t.Fatalf("restored submission length = %d, want %d", len(expanded), len(pasted))
	}
}

func TestComposerHistoryPersistsOnlyExpandedSafeText(t *testing.T) {
	app := New(Config{})
	app.history = nil
	app.historyIdx = 0
	for i := 0; i <= maxRichComposerHistory; i++ {
		pasted := strings.Repeat(string(rune('a'+i%20)), attachments.PasteThreshold+1)
		app.textarea.Reset()
		app.composerElements = nil
		handlePaste(app, pasted)
		_, expanded := app.composerSubmissionTexts()
		app.recordComposerHistory(expanded)
	}

	if len(app.richHistoryElements) != 0 {
		t.Fatalf("history retained rich payloads: %#v", app.richHistoryElements)
	}
	if attachments.IsLargePaste(app.history[0]) == false || strings.Contains(app.history[0], "[Pasted Content") {
		t.Fatalf("history did not persist expanded safe text: %q", app.history[0])
	}

	latest := len(app.history) - 1
	app.historyIdx = len(app.history)
	app.textarea.Reset()
	app.composerElements = nil
	app.navigateHistory(-1)
	if app.historyIdx != latest || len(app.composerElements) != 0 ||
		!attachments.IsLargePaste(app.textarea.Value()) {
		t.Fatalf("safe text history was not restored: index=%d elements=%#v", app.historyIdx, app.composerElements)
	}
}

func TestPasteBypassesConfiguredSingleKeyAction(t *testing.T) {
	app := New(Config{})
	handlePaste(app, "?")
	if app.textarea.Value() != "?" || app.state == StateHelp {
		t.Fatalf("paste was dispatched as shortcut: value=%q state=%v", app.textarea.Value(), app.state)
	}
}

func TestPastedLocalImageBecomesStableMultimodalElement(t *testing.T) {
	app := New(Config{})
	path := filepath.Join(t.TempDir(), "screen.png")
	data := []byte("fake-png-data")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := handlePaste(app, path)
	if cmd == nil {
		t.Fatal("image path paste did not schedule attachment loading")
	}
	if _, updateCmd := app.Update(cmd()); updateCmd != nil {
		_ = updateCmd
	}

	if app.textarea.Value() != "[Image #1]" || len(app.composerElements) != 1 {
		t.Fatalf("image composer state: value=%q elements=%#v", app.textarea.Value(), app.composerElements)
	}
	element := app.composerElements[0]
	if element.Kind != composerElementKindImage || element.Name != "screen.png" ||
		element.Value != "" || element.Data != "" {
		t.Fatalf("image element = %#v", element)
	}
	image := app.draftMedia[element.ID]
	if image == nil || string(image.Data) != string(data) {
		t.Fatalf("draft media = %#v", image)
	}
	snapshot, err := app.captureComposerSubmission()
	if err != nil || !snapshot.HasImages || len(snapshot.Input.Parts) != 1 {
		t.Fatalf("submission snapshot = %#v err=%v", snapshot, err)
	}
	if persisted := expandComposerElementsForPersistence(app.textarea.Value(), app.composerElements); persisted != "[Image #1] (image content not restored)" {
		t.Fatalf("text-only image history = %q", persisted)
	}

	app.textarea.CursorEnd()
	app.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	afterDelete, err := app.captureComposerSubmission()
	if err != nil {
		t.Fatal(err)
	}
	if len(app.composerElements) != 0 || afterDelete.HasImages {
		t.Fatal("editing an image placeholder retained the hidden image payload")
	}
}

func TestP304ComposerCapturesOrderedImageBoundary(t *testing.T) {
	app := New(Config{})
	app.insertPastedComposerText("before ")
	if err := app.addComposerImage(
		"screen.png",
		"/tmp/screen.png",
		"image/png",
		base64.StdEncoding.EncodeToString([]byte("fake-png-data")),
	); err != nil {
		t.Fatal(err)
	}
	app.insertPastedComposerText(" after")

	snapshot, err := app.captureComposerSubmission()
	if err != nil {
		t.Fatal(err)
	}
	const want = "before [Image #1] after"
	if snapshot.Display != want || len(snapshot.Input.Parts) != 3 || !snapshot.HasImages {
		t.Fatalf("snapshot = %#v, want ordered text/image/text", snapshot)
	}
	if snapshot.SafeText != "before [Image #1] (image content not restored) after" {
		t.Fatalf("safe text = %q", snapshot.SafeText)
	}

	element := app.composerElements[0]
	if element.Start != len([]rune("before ")) ||
		element.End != len([]rune("before [Image #1]")) {
		t.Fatalf("image range = %d..%d", element.Start, element.End)
	}
}

func TestMissingPastedImagePathFallsBackToText(t *testing.T) {
	app := New(Config{})
	missing := filepath.Join(t.TempDir(), "missing.png")
	cmd := handlePaste(app, missing)
	if cmd == nil {
		t.Fatal("image-looking paste did not schedule resolution")
	}
	app.Update(cmd())
	if app.textarea.Value() != missing || len(app.composerElements) != 0 {
		t.Fatalf("fallback state: value=%q elements=%#v", app.textarea.Value(), app.composerElements)
	}
}

func TestUserMessageRewriteRestoresOnlySanitizedComposerLabel(t *testing.T) {
	app := New(Config{})
	pasted := strings.Repeat("history", attachments.PasteThreshold)
	handlePaste(app, pasted)
	display, _ := app.composerSubmissionTexts()
	app.chat.AppendUserWithComposer(display, app.composerDisplayElements())
	message := app.chat.Items()[0].(*UserMessage)

	app.textarea.Reset()
	app.composerElements = nil
	app.truncateAndLoadForRewrite(message.content, message.composerElements, 0)

	if app.textarea.Value() != display || len(app.composerElements) != 0 {
		t.Fatalf("rewrite draft: value=%q elements=%#v", app.textarea.Value(), app.composerElements)
	}
	if strings.Contains(app.textarea.Value(), pasted) {
		t.Fatal("rewritten user message restored hidden rich payload")
	}
}
