package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestP304OrderedPromptQueueSnapshotRestartAndEdit(t *testing.T) {
	transcriptDir := t.TempDir()
	cwd := t.TempDir()
	sessionID := "p304-ordered-queue"
	first := newP302bTestEngine(
		t,
		sessionID,
		transcriptDir,
		cwd,
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)
	queued, err := first.EnqueuePromptInput(
		context.Background(),
		"compare [Image #1] now",
		NewUntrustedPromptInput(
			NewPromptTextPart("compare "),
			NewPromptImagePart(
				testUserImagePNGBase64,
				"image/png",
				PromptImageDetailAuto,
			),
			NewPromptTextPart(" now"),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	assertP304SanitizedOrderedSnapshot(t, queued)
	rawSnapshot, err := json.Marshal(queued)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rawSnapshot, []byte(testUserImagePNGBase64)) ||
		bytes.Contains(rawSnapshot, []byte("media_id")) ||
		bytes.Contains(rawSnapshot, []byte("digest")) {
		t.Fatalf("queue snapshot exposed private media: %s", rawSnapshot)
	}
	first.Close()

	restarted := newP302bTestEngine(
		t,
		sessionID,
		transcriptDir,
		cwd,
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)
	snapshots, err := restarted.QueuedPromptInputs()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].ID != queued.ID {
		t.Fatalf("restarted snapshots = %#v", snapshots)
	}
	assertP304SanitizedOrderedSnapshot(t, snapshots[0])

	draft, edited, err := restarted.EditQueuedPrompt(queued.ID)
	if err != nil || !edited {
		t.Fatalf("edit result: edited=%v err=%v draft=%#v", edited, err, draft)
	}
	if len(draft.Parts) != 3 ||
		draft.Parts[0].Kind != QueuedPromptPartText ||
		draft.Parts[0].Text != "compare " ||
		draft.Parts[1].Kind != QueuedPromptPartImage ||
		draft.Parts[1].Image == nil ||
		draft.Parts[1].Image.MIMEType != "image/png" ||
		draft.Parts[2].Kind != QueuedPromptPartText ||
		draft.Parts[2].Text != " now" {
		t.Fatalf("edited draft = %#v", draft)
	}
	wantImage, err := base64.StdEncoding.DecodeString(testUserImagePNGBase64)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(draft.Parts[1].Image.Data, wantImage) {
		t.Fatal("queue edit did not materialize the exact image")
	}
	clearQueuedPromptDraft(&draft)
	if remaining, err := restarted.QueuedPromptInputs(); err != nil || len(remaining) != 0 {
		t.Fatalf("edited item remains pending: %#v, %v", remaining, err)
	}
}

func TestP304QueueEditPersistenceFailureChangesNothing(t *testing.T) {
	eng := newP302bTestEngine(
		t,
		"p304-edit-failure",
		t.TempDir(),
		t.TempDir(),
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)
	queued, err := eng.EnqueuePromptInput(
		context.Background(),
		"[Image #1]",
		NewUntrustedPromptInput(NewPromptImagePart(
			testUserImagePNGBase64,
			"image/png",
			PromptImageDetailAuto,
		)),
	)
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath := RuntimeInputPersistencePath(eng.transcript.Path())
	backupPath := ledgerPath + ".backup"
	if err := os.Rename(ledgerPath, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ledgerPath, 0o700); err != nil {
		t.Fatal(err)
	}

	draft, edited, err := eng.EditQueuedPrompt(queued.ID)
	if err == nil || edited || len(draft.Parts) != 0 {
		t.Fatalf("edit unexpectedly mutated: edited=%v err=%v draft=%#v", edited, err, draft)
	}
	items := eng.RuntimeItems()
	if len(items) != 1 ||
		items[0].ID != queued.ID ||
		items[0].State != RuntimeItemPending {
		t.Fatalf("failed edit mutated queue = %#v", items)
	}
}

func TestP304QueuedPromptPreservesOrderThroughExecutionAndTranscriptRestart(t *testing.T) {
	transcriptDir := t.TempDir()
	cwd := t.TempDir()
	sessionID := "p304-ordered-execution"
	first := newP302bTestEngine(
		t,
		sessionID,
		transcriptDir,
		cwd,
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)
	queued, err := first.EnqueuePromptInput(
		context.Background(),
		"alpha [Image #1] beta [Image #2] gamma",
		NewUntrustedPromptInput(
			NewPromptTextPart("alpha "),
			NewPromptImagePart(
				testUserImagePNGBase64,
				"image/png",
				PromptImageDetailLow,
			),
			NewPromptTextPart(" beta "),
			NewPromptImagePart(
				testUserImagePNGBase64,
				"image/png",
				PromptImageDetailHigh,
			),
			NewPromptTextPart(" gamma"),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()

	model := &captureInputModel{}
	restarted := newP302bTestEngine(
		t,
		sessionID,
		transcriptDir,
		cwd,
		model,
		DefaultPromptCapabilityResolver(),
	)
	claimed, ok, err := restarted.ClaimNextRuntimeItem()
	if err != nil || !ok || claimed.ID != queued.ID {
		t.Fatalf("claim: item=%#v ok=%v err=%v", claimed, ok, err)
	}
	events, _ := restarted.SubmitRuntimeItem(context.Background(), claimed)
	terminal, _ := collectPromptInputEvents(t, events)
	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %#v", terminal)
	}
	if len(model.inputs) != 1 {
		t.Fatalf("model calls = %d", len(model.inputs))
	}
	assertP304ExecutedOrder(t, model.inputs[0])
	restarted.Close()

	loaded := newP302bTestEngine(
		t,
		sessionID,
		transcriptDir,
		cwd,
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)
	assertP304ExecutedOrder(t, loaded.GetMessages())
	if pending, err := loaded.QueuedPromptInputs(); err != nil || len(pending) != 0 {
		t.Fatalf("settled queue = %#v err=%v", pending, err)
	}
}

func TestP304QueueClaimAndEditHaveOneWinner(t *testing.T) {
	eng := newP302bTestEngine(
		t,
		"p304-edit-claim-race",
		t.TempDir(),
		t.TempDir(),
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)
	queued, err := eng.EnqueuePromptInput(
		context.Background(),
		"[Image #1]",
		NewUntrustedPromptInput(NewPromptImagePart(
			testUserImagePNGBase64,
			"image/png",
			PromptImageDetailAuto,
		)),
	)
	if err != nil {
		t.Fatal(err)
	}

	var (
		wg       sync.WaitGroup
		claimOK  bool
		claimErr error
		editOK   bool
		editErr  error
		draft    QueuedPromptDraft
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, claimOK, claimErr = eng.ClaimNextRuntimeItem()
	}()
	go func() {
		defer wg.Done()
		draft, editOK, editErr = eng.EditQueuedPrompt(queued.ID)
	}()
	wg.Wait()
	defer clearQueuedPromptDraft(&draft)
	if claimErr != nil {
		t.Fatalf("claim error = %v", claimErr)
	}
	if editErr != nil {
		t.Fatalf("edit error = %v", editErr)
	}
	if claimOK == editOK {
		t.Fatalf("claim/edit winners: claim=%v edit=%v", claimOK, editOK)
	}
}

func assertP304SanitizedOrderedSnapshot(
	t *testing.T,
	snapshot QueuedPromptSnapshot,
) {
	t.Helper()
	if snapshot.ID == "" ||
		snapshot.Display != "compare [Image #1] now" ||
		snapshot.Unavailable ||
		len(snapshot.Parts) != 3 ||
		snapshot.Parts[0].Kind != QueuedPromptPartText ||
		snapshot.Parts[0].Text != "compare " ||
		snapshot.Parts[1].Kind != QueuedPromptPartImage ||
		snapshot.Parts[1].Image == nil ||
		snapshot.Parts[1].Image.MIMEType != "image/png" ||
		snapshot.Parts[1].Image.SizeBytes <= 0 ||
		snapshot.Parts[1].Image.Width != 1 ||
		snapshot.Parts[1].Image.Height != 1 ||
		snapshot.Parts[2].Kind != QueuedPromptPartText ||
		snapshot.Parts[2].Text != " now" {
		t.Fatalf("ordered snapshot = %#v", snapshot)
	}
	if filepath.IsAbs(snapshot.Display) {
		t.Fatalf("snapshot display unexpectedly became a path: %q", snapshot.Display)
	}
}

func assertP304ExecutedOrder(t *testing.T, messages []*schema.Message) {
	t.Helper()
	user := findPromptInputUserMessage(messages, "alpha  beta  gamma")
	if user == nil || len(user.UserInputMultiContent) != 5 {
		t.Fatalf("ordered user message = %#v", user)
	}
	assertPromptTextPart(t, user.UserInputMultiContent[0], "alpha ")
	assertPromptImagePart(t, user.UserInputMultiContent[1], schema.ImageURLDetailLow)
	assertPromptTextPart(t, user.UserInputMultiContent[2], " beta ")
	assertPromptImagePart(t, user.UserInputMultiContent[3], schema.ImageURLDetailHigh)
	assertPromptTextPart(t, user.UserInputMultiContent[4], " gamma")
}
