package engine

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestP305aTypedPromptSurvivesCollectionAndRestart(t *testing.T) {
	transcriptDir := t.TempDir()
	cwd := t.TempDir()
	sessionID := "p305a-rich-restart"
	firstModel := &captureInputModel{}
	first := newP302aTestEngine(
		t,
		sessionID,
		transcriptDir,
		cwd,
		firstModel,
	)

	events, _ := first.SubmitPromptInput(
		context.Background(),
		p305aTypedPromptInput(),
	)
	terminal, _ := collectPromptInputEvents(t, events)
	if terminal.Reason != TerminalCompleted || len(firstModel.inputs) != 1 {
		t.Fatalf(
			"first rich turn: terminal=%#v model calls=%d",
			terminal,
			len(firstModel.inputs),
		)
	}
	assertP305aTypedModelOrder(t, firstModel.inputs[0])

	raw, err := os.ReadFile(first.transcript.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		testUserImagePNGBase64,
		`"digest"`,
		`"base64_data"`,
		`"_meta"`,
	} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("transcript leaked %q", forbidden)
		}
	}
	for _, required := range []string{
		`"version":2`,
		`"kind":"resource_link"`,
		`"kind":"embedded_text"`,
		`"kind":"embedded_blob"`,
	} {
		if !bytes.Contains(raw, []byte(required)) {
			t.Fatalf("transcript missing %q", required)
		}
	}

	collected, err := first.CollectSessionMedia(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if collected != (SessionMediaCollection{}) {
		t.Fatalf("collection removed reachable rich media: %#v", collected)
	}
	first.Close()

	restartedModel := &captureInputModel{}
	host := newP302aTestEngine(
		t,
		"p305a-rich-host",
		transcriptDir,
		cwd,
		restartedModel,
	)
	if _, err := host.ResumeSession(context.Background(), sessionID); err != nil {
		t.Fatalf("resume typed prompt: %v", err)
	}
	events, _ = host.SubmitMessage(context.Background(), "follow up")
	terminal, _ = collectPromptInputEvents(t, events)
	if terminal.Reason != TerminalCompleted || len(restartedModel.inputs) != 1 {
		t.Fatalf(
			"resumed turn: terminal=%#v model calls=%d",
			terminal,
			len(restartedModel.inputs),
		)
	}
	assertP305aTypedModelOrder(t, restartedModel.inputs[0])
}

func TestP305aTypedQueuedPromptRestartsEditsAndExecutes(t *testing.T) {
	transcriptDir := t.TempDir()
	cwd := t.TempDir()
	sessionID := "p305a-rich-queue"
	first := newP302aTestEngine(
		t,
		sessionID,
		transcriptDir,
		cwd,
		&captureInputModel{},
	)
	queued, err := first.EnqueuePromptInput(
		context.Background(),
		"typed rich prompt",
		p305aTypedPromptInput(),
	)
	if err != nil {
		t.Fatal(err)
	}
	assertP305aQueuedSnapshot(t, queued)
	first.Close()

	model := &captureInputModel{}
	restarted := newP302aTestEngine(
		t,
		sessionID,
		transcriptDir,
		cwd,
		model,
	)
	snapshots, err := restarted.QueuedPromptInputs()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].ID != queued.ID {
		t.Fatalf("restarted snapshots = %#v", snapshots)
	}
	assertP305aQueuedSnapshot(t, snapshots[0])
	claimed, ok, err := restarted.ClaimNextRuntimeItem()
	if err != nil || !ok || claimed.ID != queued.ID {
		t.Fatalf("claim: item=%#v ok=%v err=%v", claimed, ok, err)
	}
	events, _ := restarted.SubmitRuntimeItem(context.Background(), claimed)
	terminal, _ := collectPromptInputEvents(t, events)
	if terminal.Reason != TerminalCompleted || len(model.inputs) != 1 {
		t.Fatalf(
			"queued execution: terminal=%#v model calls=%d",
			terminal,
			len(model.inputs),
		)
	}
	assertP305aTypedModelOrder(t, model.inputs[0])

	editable, err := restarted.EnqueuePromptInput(
		context.Background(),
		"editable rich prompt",
		p305aTypedPromptInput(),
	)
	if err != nil {
		t.Fatal(err)
	}
	draft, edited, err := restarted.EditQueuedPrompt(editable.ID)
	if err != nil || !edited {
		t.Fatalf("edit: draft=%#v edited=%v err=%v", draft, edited, err)
	}
	defer clearQueuedPromptDraft(&draft)
	if len(draft.Parts) != 6 ||
		draft.Parts[1].ResourceLink == nil ||
		draft.Parts[1].ResourceLink.URI != "file:///workspace/schema.json" ||
		draft.Parts[2].Image == nil ||
		len(draft.Parts[2].Image.Data) == 0 ||
		draft.Parts[3].EmbeddedText == nil ||
		draft.Parts[3].EmbeddedText.Text != "embedded context" ||
		draft.Parts[4].EmbeddedBlob == nil ||
		draft.Parts[4].EmbeddedBlob.URI != "file:///workspace/pixel.png" ||
		len(draft.Parts[4].EmbeddedBlob.Data) == 0 {
		t.Fatalf("typed edit draft = %#v", draft)
	}
}

func p305aTypedPromptInput() UntrustedPromptInput {
	textMIME := "text/plain"
	resourceMIME := "application/json"
	lastModified := "2026-07-30T00:00:00Z"
	priority := 0.5
	return NewUntrustedPromptInput(
		NewPromptTextPart("/help"),
		NewPromptResourceLinkPart(PromptResourceLink{
			URI:      "file:///workspace/schema.json",
			Name:     "schema.json",
			MIMEType: &resourceMIME,
			Annotations: &PromptResourceAnnotations{
				Audience:     []string{"assistant"},
				LastModified: &lastModified,
				Priority:     &priority,
			},
		}),
		NewPromptImagePartWithAnnotations(
			testUserImagePNGBase64,
			"image/png",
			PromptImageDetailLow,
			&PromptResourceAnnotations{Audience: []string{"user"}},
		),
		NewPromptEmbeddedTextPart(PromptEmbeddedTextResource{
			URI:      "file:///workspace/context.txt",
			MIMEType: &textMIME,
			Text:     "embedded context",
			Annotations: &PromptResourceAnnotations{
				Audience: []string{"assistant"},
			},
		}),
		NewPromptEmbeddedBlobPart(PromptEmbeddedBlobResource{
			URI:        "file:///workspace/pixel.png",
			MIMEType:   "image/png",
			Base64Data: testUserImagePNGBase64,
			Detail:     PromptImageDetailHigh,
		}),
		NewPromptTextPart("tail"),
	)
}

func assertP305aTypedModelOrder(
	t *testing.T,
	messages []*schema.Message,
) {
	t.Helper()
	var user *schema.Message
	for _, message := range messages {
		if message != nil &&
			message.Role == schema.User &&
			len(message.UserInputMultiContent) == 7 {
			user = message
			break
		}
	}
	if user == nil {
		t.Fatalf("typed user prompt missing")
	}
	assertPromptTextPart(t, user.UserInputMultiContent[0], "/help")
	if part := user.UserInputMultiContent[1]; part.Type != schema.ChatMessagePartTypeText ||
		!strings.Contains(part.Text, `"type":"resource_link"`) {
		t.Fatalf("resource part = %#v", part)
	}
	assertPromptImagePart(
		t,
		user.UserInputMultiContent[2],
		schema.ImageURLDetailLow,
	)
	if part := user.UserInputMultiContent[3]; part.Type != schema.ChatMessagePartTypeText ||
		!strings.Contains(part.Text, `"kind":"text"`) ||
		!strings.Contains(part.Text, `"text":"embedded context"`) {
		t.Fatalf("embedded text part = %#v", part)
	}
	if part := user.UserInputMultiContent[4]; part.Type != schema.ChatMessagePartTypeText ||
		!strings.Contains(part.Text, `"kind":"blob"`) ||
		strings.Contains(part.Text, "media_id") {
		t.Fatalf("embedded blob envelope = %#v", part)
	}
	assertPromptImagePart(
		t,
		user.UserInputMultiContent[5],
		schema.ImageURLDetailHigh,
	)
	assertPromptTextPart(t, user.UserInputMultiContent[6], "tail")
}

func assertP305aQueuedSnapshot(
	t *testing.T,
	snapshot QueuedPromptSnapshot,
) {
	t.Helper()
	wantKinds := []QueuedPromptPartKind{
		QueuedPromptPartText,
		QueuedPromptPartResourceLink,
		QueuedPromptPartImage,
		QueuedPromptPartEmbeddedText,
		QueuedPromptPartEmbeddedBlob,
		QueuedPromptPartText,
	}
	if snapshot.ID == "" ||
		snapshot.Display != "typed rich prompt" ||
		snapshot.Unavailable ||
		len(snapshot.Parts) != len(wantKinds) {
		t.Fatalf("queued snapshot = %#v", snapshot)
	}
	for index, want := range wantKinds {
		if snapshot.Parts[index].Kind != want {
			t.Fatalf(
				"queued part %d kind = %q, want %q",
				index,
				snapshot.Parts[index].Kind,
				want,
			)
		}
	}
	if snapshot.Parts[1].MIMEType != "application/json" ||
		snapshot.Parts[3].MIMEType != "text/plain" ||
		snapshot.Parts[4].Image == nil ||
		snapshot.Parts[4].Image.MIMEType != "image/png" {
		t.Fatalf("queued descriptors = %#v", snapshot.Parts)
	}
}
