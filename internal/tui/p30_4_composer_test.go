package tui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/attachments"
)

func TestP304ClipboardImageLoadIsFencedAndUsesCapturedAnchor(t *testing.T) {
	var lastRead []byte
	reader := func(context.Context) attachments.ImagePasteResult {
		lastRead = []byte("clipboard-png")
		return attachments.ImagePasteResult{
			HasImage: true,
			Data:     lastRead,
			Format:   "png",
		}
	}

	t.Run("captured anchor", func(t *testing.T) {
		app := New(Config{ClipboardImageReader: reader})
		app.textarea.SetValue("left right")
		setTextareaRuneCursor(&app.textarea, len([]rune("left")))
		cmd := app.pasteClipboardImage()
		if cmd == nil {
			t.Fatal("clipboard image load was not started")
		}
		setTextareaRuneCursor(&app.textarea, len([]rune(app.textarea.Value())))
		app.Update(cmd())
		if got := app.textarea.Value(); got != "left[Image #1] right" {
			t.Fatalf("anchored draft = %q", got)
		}
		if len(app.composerElements) != 1 {
			t.Fatalf("elements = %#v", app.composerElements)
		}
		element := app.composerElements[0]
		if element.Value != "" || element.Data != "" ||
			string(app.draftMedia[element.ID].Data) != "clipboard-png" {
			t.Fatalf("image ownership = element:%#v media:%#v", element, app.draftMedia[element.ID])
		}
	})

	t.Run("stale revision", func(t *testing.T) {
		app := New(Config{ClipboardImageReader: reader})
		app.textarea.SetValue("keep")
		app.textarea.CursorEnd()
		cmd := app.pasteClipboardImage()
		if second := app.pasteClipboardImage(); second != nil {
			t.Fatal("a second image load started for the same draft")
		}
		app.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: "!"})
		app.Update(cmd())
		if app.textarea.Value() != "keep!" || len(app.composerElements) != 0 ||
			len(app.draftMedia) != 0 || app.composerImageLoadPending != nil {
			t.Fatalf(
				"stale result mutated draft: value=%q elements=%#v media=%#v pending=%#v",
				app.textarea.Value(),
				app.composerElements,
				app.draftMedia,
				app.composerImageLoadPending,
			)
		}
		for index, value := range lastRead {
			if value != 0 {
				t.Fatalf("stale image byte %d was not cleared", index)
			}
		}
	})

	t.Run("failure redacts backend paths", func(t *testing.T) {
		const sensitivePath = "/private/tmp/eino-agent-clipboard-secret.png"
		app := New(Config{ClipboardImageReader: func(context.Context) attachments.ImagePasteResult {
			return attachments.ImagePasteResult{
				HasImage: true,
				Error:    errors.New("read " + sensitivePath + ": denied"),
			}
		}})
		cmd := app.pasteClipboardImage()
		if cmd == nil {
			t.Fatal("clipboard image load was not started")
		}
		app.Update(cmd())
		active := app.notifications.Active()
		if len(active) != 1 {
			t.Fatalf("notifications = %#v", active)
		}
		if strings.Contains(active[0].Message, sensitivePath) ||
			active[0].Message != "Unable to paste image from clipboard" {
			t.Fatalf("clipboard failure was not redacted: %q", active[0].Message)
		}
	})
}

func TestP304ComposerSnapshotRejectsInvalidElementsAndKeepsLiteralLookalikes(t *testing.T) {
	t.Run("literal image label", func(t *testing.T) {
		app := New(Config{})
		app.textarea.SetValue("before [Image #1] after")
		snapshot, err := app.captureComposerSubmission()
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.HasImages || len(snapshot.Input.Parts) != 1 {
			t.Fatalf("literal snapshot = %#v", snapshot)
		}
	})

	t.Run("missing media", func(t *testing.T) {
		app := New(Config{})
		app.textarea.SetValue("[Image #1]")
		app.composerElements = []threadComposerElement{{
			ID: "missing", Kind: composerElementKindImage, Label: "[Image #1]",
			MIMEType: "image/png", Start: 0, End: len([]rune("[Image #1]")),
		}}
		if _, err := app.captureComposerSubmission(); err == nil ||
			!strings.Contains(err.Error(), "no image content") {
			t.Fatalf("missing-media error = %v", err)
		}
	})

	t.Run("overlap", func(t *testing.T) {
		app := New(Config{})
		app.textarea.SetValue("[one]")
		app.composerElements = []threadComposerElement{
			{ID: "one", Kind: composerElementKindPaste, Label: "[one]", Value: "first", Start: 0, End: 5},
			{ID: "two", Kind: composerElementKindPaste, Label: "[one]", Value: "second", Start: 0, End: 5},
		}
		if _, err := app.captureComposerSubmission(); err == nil ||
			!strings.Contains(err.Error(), "invalid range") {
			t.Fatalf("overlap error = %v", err)
		}
	})
}

func TestP304IdleAdmissionSettlesBeforeDraftAndChatMutation(t *testing.T) {
	app := newTextSubmissionApp(t, false)
	app.textarea.SetValue("accepted input")
	cmd := app.sendMessage()
	if cmd == nil || app.composerAdmissionPending == nil {
		t.Fatal("idle admission was not started")
	}
	if app.textarea.Value() != "accepted input" || len(app.chat.Items()) != 0 || app.running {
		t.Fatalf(
			"state changed before acceptance: draft=%q chat=%#v running=%v",
			app.textarea.Value(),
			app.chat.Items(),
			app.running,
		)
	}
	if second := app.sendMessage(); second != nil {
		t.Fatal("second Enter started another admission")
	}
	message := cmd()
	settled, ok := message.(composerAdmissionSettledMsg)
	if !ok {
		t.Fatalf("settlement = %T", message)
	}
	defer func() {
		for range settled.Events {
		}
	}()
	app.Update(settled)
	if app.textarea.Value() != "" || len(app.chat.Items()) == 0 || !app.running ||
		app.composerAdmissionPending != nil {
		t.Fatalf(
			"accepted state: draft=%q chat=%#v running=%v pending=%#v",
			app.textarea.Value(),
			app.chat.Items(),
			app.running,
			app.composerAdmissionPending,
		)
	}
}

func TestP304AdmissionRejectionAndCancellationRetainDraft(t *testing.T) {
	t.Run("engine rejection", func(t *testing.T) {
		app, eng := newQueueTestApp(t)
		if err := app.addComposerImage(
			"screen.png",
			"/private/screen.png",
			"image/png",
			base64.StdEncoding.EncodeToString([]byte("private-image")),
		); err != nil {
			t.Fatal(err)
		}
		cmd := app.sendMessage()
		if cmd == nil {
			t.Fatal("rejected admission command is nil")
		}
		app.Update(cmd())
		if app.textarea.Value() != "[Image #1]" || len(app.composerElements) != 1 ||
			len(app.draftMedia) != 1 || len(app.chat.Items()) != 0 ||
			app.composerAdmissionPending != nil {
			t.Fatalf(
				"rejection changed draft: value=%q elements=%#v media=%#v chat=%#v pending=%#v",
				app.textarea.Value(),
				app.composerElements,
				app.draftMedia,
				app.chat.Items(),
				app.composerAdmissionPending,
			)
		}
		if pending, err := eng.QueuedPromptInputs(); err != nil || len(pending) != 0 {
			t.Fatalf("rejection mutated queue = %#v err=%v", pending, err)
		}
	})

	t.Run("ctrl c", func(t *testing.T) {
		app := newTextSubmissionApp(t, false)
		app.textarea.SetValue("retain on cancel")
		cmd := app.sendMessage()
		app.handleInterrupt()
		if app.composerAdmissionPending == nil || !app.composerAdmissionPending.Cancelled ||
			app.textarea.Value() != "retain on cancel" {
			t.Fatalf("cancel state = %#v draft=%q", app.composerAdmissionPending, app.textarea.Value())
		}
		app.Update(cmd())
		if app.composerAdmissionPending != nil || app.textarea.Value() != "retain on cancel" ||
			len(app.chat.Items()) != 0 {
			t.Fatalf(
				"cancel settlement: pending=%#v draft=%q chat=%#v",
				app.composerAdmissionPending,
				app.textarea.Value(),
				app.chat.Items(),
			)
		}
	})
}

func TestP304AdmissionErrorsExposeOnlyBoundedCategories(t *testing.T) {
	const sensitivePath = "/private/session/runtime-input.json"
	if got := composerAdmissionFailureMessage(errors.New("write "+sensitivePath+": denied"), true); got != "Queued input could not be accepted" {
		t.Fatalf("persistence failure = %q", got)
	}
	safe := &engine.PromptInputAdmissionError{
		PartIndex: 1, PartKind: "image", ReasonCode: "capability_unknown",
	}
	if got := composerAdmissionFailureMessage(safe, false); got != safe.Error() {
		t.Fatalf("admission failure = %q, want %q", got, safe.Error())
	}
}

func TestP304DraftMediaIsReleasedAfterUndoReachabilityExpires(t *testing.T) {
	app := New(Config{})
	if err := app.addComposerImage(
		"draft.png",
		"",
		"image/jpeg",
		queueTestPNGBase64,
	); err != nil {
		t.Fatal(err)
	}
	image := app.draftMedia[app.composerElements[0].ID]
	app.textarea.CursorEnd()
	app.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if len(app.draftMedia) != 1 {
		t.Fatal("undo-reachable image was released too early")
	}
	for range maxComposerUndoEntries + 1 {
		app.insertPastedComposerText("x")
	}
	if len(app.draftMedia) != 0 {
		t.Fatalf("orphaned draft media remains: %#v", app.draftMedia)
	}
	for index, value := range image.Data {
		if value != 0 {
			t.Fatalf("released image byte %d was not cleared", index)
		}
	}
}

func TestP304SessionViewResetClearsDetachedDraftMedia(t *testing.T) {
	app, _ := newQueueTestApp(t)
	raw := []byte("private-image-bytes")
	app.draftMedia["image-1"] = &composerDraftImage{
		MIMEType: "image/png",
		Data:     raw,
	}
	app.composerElements = []threadComposerElement{{
		ID: "image-1", Kind: composerElementKindImage,
		Label: "[Image #1]", MIMEType: "image/png", Start: 0, End: 10,
	}}
	app.textarea.SetValue("[Image #1]")

	if err := app.resetAndRestoreSessionViews(); err != nil {
		t.Fatalf("reset session views: %v", err)
	}
	if len(app.draftMedia) != 0 {
		t.Fatalf("draft media survived session reset: %#v", app.draftMedia)
	}
	for index, value := range raw {
		if value != 0 {
			t.Fatalf("detached byte %d was not cleared: %d", index, value)
		}
	}
}

func TestP304RichChatRewriteRestoresOnlyVisibleImageNotice(t *testing.T) {
	app := New(Config{})
	if err := app.addComposerImage(
		"screen.png",
		"/private/screen.png",
		"image/png",
		queueTestPNGBase64,
	); err != nil {
		t.Fatal(err)
	}
	app.chat.AppendUserWithComposer(
		app.textarea.Value(),
		app.composerDisplayElements(),
	)
	message := app.chat.Items()[0].(*UserMessage)
	if len(message.composerElements) != 1 ||
		message.composerElements[0].ID != "" ||
		message.composerElements[0].Data != "" ||
		message.composerElements[0].Value != "" {
		t.Fatalf("chat metadata = %#v", message.composerElements)
	}
	app.truncateAndLoadForRewrite(message.content, message.composerElements, 0)
	if app.textarea.Value() != "[Image #1] (image content not restored)" ||
		len(app.composerElements) != 0 ||
		len(app.draftMedia) != 0 {
		t.Fatalf(
			"rewrite state: value=%q elements=%#v media=%#v",
			app.textarea.Value(),
			app.composerElements,
			app.draftMedia,
		)
	}
}

func TestP304TUIQueueEditRestoresExactOrderedMediaWithoutPreviewSecrets(t *testing.T) {
	app, eng := newQueueTestApp(t)
	app.running = true
	imageBytes, err := base64.StdEncoding.DecodeString(queueTestPNGBase64)
	if err != nil {
		t.Fatal(err)
	}
	app.insertPastedComposerText("before ")
	if err := app.addComposerImage(
		"screen.png",
		"/private/screen.png",
		"image/png",
		queueTestPNGBase64,
	); err != nil {
		t.Fatal(err)
	}
	app.insertPastedComposerText(" after")
	settleComposerAdmission(t, app, app.sendMessage())
	if len(app.queuedInputPreview) != 1 {
		t.Fatalf("preview = %#v notification=%q draft=%q", app.queuedInputPreview, app.activeToast(), app.textarea.Value())
	}
	serialized, err := json.Marshal(app.queuedInputPreview)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{queueTestPNGBase64, "/private/screen.png", "media_id"} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("preview leaked %q: %s", forbidden, serialized)
		}
	}

	app.running = false
	app.handleQueueSlashCommand("/queue edit last")
	if app.textarea.Value() != "before [Image #1] after" ||
		len(app.composerElements) != 1 ||
		string(app.draftMedia[app.composerElements[0].ID].Data) != string(imageBytes) ||
		len(app.queuedInputPreview) != 0 {
		t.Fatalf(
			"restored draft: value=%q elements=%#v media=%#v preview=%#v",
			app.textarea.Value(),
			app.composerElements,
			app.draftMedia,
			app.queuedInputPreview,
		)
	}
	snapshots, err := eng.QueuedPromptInputs()
	if err != nil || len(snapshots) != 0 {
		t.Fatalf("engine queue after edit = %#v err=%v", snapshots, err)
	}
}
