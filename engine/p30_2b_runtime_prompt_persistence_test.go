package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/internal/mediastore"
	"github.com/abietic/yhc/engine/internal/promptrecord"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/cloudwego/eino/schema"
)

func TestP302bQueuedImageRestartsAndTransfersSameRefs(t *testing.T) {
	transcriptDir := t.TempDir()
	cwd := t.TempDir()
	sessionID := "p302b-restart"
	imageBase64 := base64.StdEncoding.EncodeToString(largeP302aPNG(t))
	first := newP302bTestEngine(
		t,
		sessionID,
		transcriptDir,
		cwd,
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)
	queued, err := first.EnqueueUserInput(UserTurnInput{
		Display: "queued image",
		Prompt:  "inspect",
		Images: []UserImage{{
			Name:       "private.png",
			Path:       "/private/screen.png",
			MIMEType:   "image/png",
			Base64Data: imageBase64,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath := RuntimeInputPersistencePath(first.transcript.Path())
	record := readP302bLedgerRecord(t, ledgerPath)
	turnID := record.TurnID
	mediaID := record.Parts[1].Image.Ref.MediaID
	assertP302bRefOnlyLedger(t, ledgerPath)
	rawLedger, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rawLedger, []byte(imageBase64)) ||
		len(rawLedger) > 16*1024 {
		t.Fatalf("large queued image expanded ledger to %d bytes", len(rawLedger))
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
	snapshot := restarted.QueuedUserInputs()
	if len(snapshot) != 1 ||
		snapshot[0].ID != queued.ID ||
		snapshot[0].Prompt != "inspect" ||
		len(snapshot[0].Images) != 1 ||
		snapshot[0].Images[0].Base64Data != imageBase64 {
		t.Fatalf("restarted queue = %#v", snapshot)
	}
	claimed, ok, err := restarted.ClaimNextRuntimeItem()
	if err != nil || !ok {
		t.Fatalf("claim: item=%#v ok=%v err=%v", claimed, ok, err)
	}
	if claimed.ID != queued.ID ||
		claimed.UserPrompt == nil ||
		claimed.UserPrompt.durablePrompt == nil ||
		claimed.UserPrompt.durablePrompt.TurnID != turnID {
		t.Fatalf("claimed item = %#v", claimed)
	}
	assertP302bRefOnlyLedger(t, ledgerPath)

	events, _ := restarted.SubmitRuntimeItem(context.Background(), claimed)
	terminal, observed := collectPromptInputEvents(t, events)
	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %#v", terminal)
	}
	for _, event := range observed {
		if event.TurnID != turnID {
			t.Fatalf("event turn ID = %q, want %q", event.TurnID, turnID)
		}
	}
	if len(model.inputs) != 1 {
		t.Fatalf("model calls = %d", len(model.inputs))
	}
	message := findPromptInputUserMessage(model.inputs[0], "inspect")
	if message == nil ||
		len(message.UserInputMultiContent) != 2 ||
		message.UserInputMultiContent[1].Image == nil ||
		message.UserInputMultiContent[1].Image.Base64Data == nil ||
		message.UserInputMultiContent[1].Image.Detail !=
			schema.ImageURLDetail(PromptImageDetailAuto) ||
		*message.UserInputMultiContent[1].Image.Base64Data != imageBase64 {
		t.Fatalf("model prompt = %#v", model.inputs[0])
	}

	rawTranscript, err := os.ReadFile(restarted.transcript.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rawTranscript, []byte(`"kind":"user-prompt"`)) ||
		!bytes.Contains(rawTranscript, []byte(mediaID)) ||
		bytes.Contains(rawTranscript, []byte(imageBase64)) ||
		bytes.Contains(rawTranscript, []byte(`"digest"`)) {
		t.Fatalf("transcript did not transfer the same ref: %s", rawTranscript)
	}
	var envelope runtimeInputEnvelope
	rawLedger, err = os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawLedger, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Items) != 0 {
		t.Fatalf("settled ledger = %#v", envelope.Items)
	}
}

func TestP302bClaimFailureLeavesRefPromptPending(t *testing.T) {
	transcriptDir := t.TempDir()
	cwd := t.TempDir()
	sessionID := "p302b-missing"
	engine := newP302bTestEngine(
		t,
		sessionID,
		transcriptDir,
		cwd,
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)
	queued, err := engine.EnqueueUserInput(UserTurnInput{
		Prompt: "inspect",
		Images: []UserImage{{
			MIMEType:   "image/png",
			Base64Data: testUserImagePNGBase64,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	record := readP302bLedgerRecord(
		t,
		RuntimeInputPersistencePath(engine.transcript.Path()),
	)
	manifestPath := filepath.Join(
		engine.transcript.Path()+".media",
		"manifest.json",
	)
	var manifest mediastore.Manifest
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	delete(manifest.Entries, record.Parts[1].Image.Ref.MediaID)
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := engine.ClaimNextRuntimeItem(); err == nil || ok {
		t.Fatalf("claim accepted missing media: ok=%v err=%v", ok, err)
	}
	items := engine.RuntimeItems()
	if len(items) != 1 ||
		items[0].ID != queued.ID ||
		items[0].State != RuntimeItemPending ||
		items[0].UserPrompt == nil ||
		items[0].UserPrompt.durablePrompt == nil ||
		items[0].UserPrompt.materializedInput != nil {
		t.Fatalf("failed claim mutated queue = %#v", items)
	}
}

func TestP302bStoreFailurePublishesNoQueueRef(t *testing.T) {
	transcriptDir := t.TempDir()
	sessionID := "p302b-store-failure"
	transcriptPath := filepath.Join(transcriptDir, sessionID+".jsonl")
	outside := t.TempDir()
	if err := os.Symlink(outside, transcriptPath+".media"); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	engine := newP302bTestEngine(
		t,
		sessionID,
		transcriptDir,
		t.TempDir(),
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)
	_, err := engine.EnqueueUserInput(UserTurnInput{
		Prompt: "inspect",
		Images: []UserImage{{
			MIMEType:   "image/png",
			Base64Data: testUserImagePNGBase64,
		}},
	})
	if err == nil {
		t.Fatal("unsafe media store accepted queued image")
	}
	if strings.Contains(err.Error(), transcriptPath) ||
		strings.Contains(err.Error(), outside) ||
		strings.Contains(err.Error(), testUserImagePNGBase64) {
		t.Fatalf("store error leaked private data: %v", err)
	}
	if items := engine.RuntimeItems(); len(items) != 0 {
		t.Fatalf("failed store published queue item: %#v", items)
	}
	if _, err := os.Stat(RuntimeInputPersistencePath(transcriptPath)); !os.IsNotExist(err) {
		t.Fatalf("failed store published ledger: %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("unsafe store wrote outside: entries=%v err=%v", entries, err)
	}
}

func TestP302bLedgerFailureLeavesOnlyConservativeOrphan(t *testing.T) {
	engine := newP302bTestEngine(
		t,
		"p302b-ledger-failure",
		t.TempDir(),
		t.TempDir(),
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)
	ledgerPath := RuntimeInputPersistencePath(engine.transcript.Path())
	if err := os.Mkdir(ledgerPath, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := engine.EnqueueUserInput(UserTurnInput{
		Prompt: "inspect",
		Images: []UserImage{{
			MIMEType:   "image/png",
			Base64Data: testUserImagePNGBase64,
		}},
	})
	if err == nil {
		t.Fatal("ledger replacement failure accepted queue item")
	}
	if strings.Contains(err.Error(), ledgerPath) ||
		strings.Contains(err.Error(), testUserImagePNGBase64) {
		t.Fatalf("ledger error leaked private data: %v", err)
	}
	if items := engine.RuntimeItems(); len(items) != 0 {
		t.Fatalf("failed ledger commit mutated memory: %#v", items)
	}
	manifestPath := filepath.Join(
		engine.transcript.Path()+".media",
		"manifest.json",
	)
	data, readErr := os.ReadFile(manifestPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var manifest mediastore.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) != 1 {
		t.Fatalf("conservative orphan entries = %d, want 1", len(manifest.Entries))
	}
}

func TestP302bRouteDriftReleasesClaimWithoutModelEntry(t *testing.T) {
	var supported atomic.Bool
	supported.Store(true)
	capabilities := PromptCapabilityResolverFunc(func(
		provider.Provider,
		string,
	) PromptCapabilityDecision {
		status := PromptCapabilityUnsupported
		if supported.Load() {
			status = PromptCapabilitySupported
		}
		return PromptCapabilityDecision{
			Status: status,
			Source: "p302b-test",
		}
	})
	model := &captureInputModel{}
	engine := newP302bTestEngine(
		t,
		"p302b-route",
		t.TempDir(),
		t.TempDir(),
		model,
		capabilities,
	)
	queued, err := engine.EnqueueUserInput(UserTurnInput{
		Prompt: "inspect",
		Images: []UserImage{{
			MIMEType:   "image/png",
			Base64Data: testUserImagePNGBase64,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	supported.Store(false)

	if _, ok, err := engine.ClaimNextRuntimeItem(); err == nil || ok {
		t.Fatalf("route-incompatible claim: ok=%v err=%v", ok, err)
	}
	if len(model.inputs) != 0 {
		t.Fatalf("route-incompatible queue reached model %d times", len(model.inputs))
	}
	items := engine.RuntimeItems()
	if len(items) != 1 ||
		items[0].ID != queued.ID ||
		items[0].State != RuntimeItemPending {
		t.Fatalf("route failure did not release exact item: %#v", items)
	}
}

func TestP302bSafePointRouteDriftReleasesClaimWithoutProjection(t *testing.T) {
	var supported atomic.Bool
	supported.Store(true)
	capabilities := PromptCapabilityResolverFunc(func(
		provider.Provider,
		string,
	) PromptCapabilityDecision {
		status := PromptCapabilityUnsupported
		if supported.Load() {
			status = PromptCapabilitySupported
		}
		return PromptCapabilityDecision{
			Status: status,
			Source: "p302b-safe-point-test",
		}
	})
	engine := newP302bTestEngine(
		t,
		"p302b-safe-point-route",
		t.TempDir(),
		t.TempDir(),
		&captureInputModel{},
		capabilities,
	)
	queued, err := engine.EnqueueUserInput(UserTurnInput{
		Prompt: "inspect",
		Images: []UserImage{{
			MIMEType:   "image/png",
			Base64Data: testUserImagePNGBase64,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	supported.Store(false)

	var consumed []string
	messages, err := collectRuntimeItemsAtSafePoint(
		context.Background(),
		QueryParams{
			SessionID:        engine.config.SessionID,
			InputCoordinator: engine.inputCoordinator,
			AdmitRuntimeItem: engine.preflightClaimedRuntimeItem,
		},
		nil,
		true,
		true,
		nil,
		&consumed,
	)
	if err == nil {
		t.Fatalf("safe-point route drift returned messages: %#v", messages)
	}
	if len(messages) != 0 || len(consumed) != 0 {
		t.Fatalf(
			"safe-point route drift projected messages=%#v consumed=%#v",
			messages,
			consumed,
		)
	}
	items := engine.RuntimeItems()
	if len(items) != 1 ||
		items[0].ID != queued.ID ||
		items[0].State != RuntimeItemPending {
		t.Fatalf("safe-point route failure did not release item: %#v", items)
	}
}

func TestP302bSafePointTransfersRefsBeforeSettlement(t *testing.T) {
	engine := newP302bTestEngine(
		t,
		"p302b-safe-point",
		t.TempDir(),
		t.TempDir(),
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)
	queued, err := engine.EnqueueUserInput(UserTurnInput{
		Prompt: "inspect",
		Images: []UserImage{{
			MIMEType:   "image/png",
			Base64Data: testUserImagePNGBase64,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	record := readP302bLedgerRecord(
		t,
		RuntimeInputPersistencePath(engine.transcript.Path()),
	)
	items, err := engine.inputCoordinator.ClaimSafePoint(
		engine.runtimeInputScope(),
		true,
		true,
	)
	if err != nil || len(items) != 1 {
		t.Fatalf("safe-point claim = %#v, err=%v", items, err)
	}
	if err := engine.preflightClaimedRuntimeItem(
		context.Background(),
		items[0],
	); err != nil {
		t.Fatal(err)
	}
	message := runtimeItemToAttachmentMessage(items[0])
	if err := engine.recordTranscriptMessages(
		[]*schema.Message{message},
	); err != nil {
		t.Fatal(err)
	}
	if runtime := engine.RuntimeItems(); len(runtime) != 0 {
		t.Fatalf("safe-point prompt did not settle: %#v", runtime)
	}
	raw, err := os.ReadFile(engine.transcript.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(record.Parts[1].Image.Ref.MediaID)) ||
		bytes.Contains(raw, []byte(testUserImagePNGBase64)) {
		t.Fatalf("safe-point transcript lost ref ownership: %s", raw)
	}
	coverage, err := engine.transcript.RuntimeItemDeliveryCoverage(
		[]string{queued.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := coverage[queued.ID]; !ok {
		t.Fatal("safe-point transcript did not cover the runtime item")
	}
}

func TestP302bHookRejectionSettlesWithoutReplayingRejectedPrompt(t *testing.T) {
	model := &captureInputModel{}
	engine := newP302bTestEngine(
		t,
		"p302b-hook-rejection",
		t.TempDir(),
		t.TempDir(),
		model,
		DefaultPromptCapabilityResolver(),
	)
	engine.config.HookExecutor.RegisterUserPromptSubmit(
		func(context.Context, string) *hooks.UserPromptSubmitHookResult {
			return &hooks.UserPromptSubmitHookResult{
				Reject:       true,
				RejectReason: "blocked by policy",
			}
		},
	)
	queued, err := engine.EnqueueUserInput(UserTurnInput{
		Prompt: "private prompt",
		Images: []UserImage{{
			MIMEType:   "image/png",
			Base64Data: testUserImagePNGBase64,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := engine.ClaimNextRuntimeItem()
	if err != nil || !ok {
		t.Fatalf("claim: item=%#v ok=%v err=%v", claimed, ok, err)
	}
	events, _ := engine.SubmitRuntimeItem(context.Background(), claimed)
	terminal, _ := collectPromptInputEvents(t, events)
	if terminal.Reason != TerminalHookStopped {
		t.Fatalf("terminal = %#v", terminal)
	}
	if len(model.inputs) != 0 {
		t.Fatalf("rejected prompt reached model: %#v", model.inputs)
	}
	if items := engine.RuntimeItems(); len(items) != 0 {
		t.Fatalf("rejected prompt remained queued: %#v", items)
	}
	loaded, err := engine.transcript.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.HasMediaRefs ||
		len(loaded.Messages) != 1 ||
		loaded.Messages[0].Content != "blocked by policy" ||
		len(loaded.Messages[0].UserInputMultiContent) != 0 {
		t.Fatalf("rejected prompt replay = %#v", loaded)
	}
	coverage, err := engine.transcript.RuntimeItemDeliveryCoverage(
		[]string{queued.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := coverage[queued.ID]; !ok {
		t.Fatal("durable hook rejection did not cover settled queue item")
	}
	data, err := os.ReadFile(engine.transcript.Path())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"kind":"user-prompt"`)) ||
		bytes.Contains(data, []byte(testUserImagePNGBase64)) {
		t.Fatalf("hook rejection persisted rejected rich prompt: %s", data)
	}
}

func TestP302bTranscriptAppendFailureRecoversProcessingRefToPending(
	t *testing.T,
) {
	transcriptDir := t.TempDir()
	cwd := t.TempDir()
	sessionID := "p302b-transcript-failure"
	model := &captureInputModel{}
	engine := newP302bTestEngine(
		t,
		sessionID,
		transcriptDir,
		cwd,
		model,
		DefaultPromptCapabilityResolver(),
	)
	queued, err := engine.EnqueueUserInput(UserTurnInput{
		Prompt: "inspect",
		Images: []UserImage{{
			MIMEType:   "image/png",
			Base64Data: testUserImagePNGBase64,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := engine.ClaimNextRuntimeItem()
	if err != nil || !ok {
		t.Fatalf("claim: item=%#v ok=%v err=%v", claimed, ok, err)
	}

	transcriptPath := engine.transcript.Path()
	savedPath := transcriptPath + ".p302b-saved"
	if err := engine.transcript.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(transcriptPath, savedPath); err != nil {
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.WriteFile(savedPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(transcriptPath, 0o700); err != nil {
		t.Fatal(err)
	}
	events, _ := engine.SubmitRuntimeItem(context.Background(), claimed)
	terminal, _ := collectPromptInputEvents(t, events)
	if terminal.Reason != TerminalPersistenceError {
		t.Fatalf("terminal = %#v", terminal)
	}
	if len(model.inputs) != 0 {
		t.Fatalf("transcript failure reached model: %#v", model.inputs)
	}
	if strings.Contains(terminal.Err.Error(), transcriptPath) ||
		strings.Contains(terminal.Err.Error(), queued.ID) ||
		strings.Contains(terminal.Err.Error(), testUserImagePNGBase64) {
		t.Fatalf("transcript failure leaked private data: %v", terminal.Err)
	}
	if err := os.Remove(transcriptPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(savedPath, transcriptPath); err != nil {
		t.Fatal(err)
	}
	engine.Close()

	restarted := newP302bTestEngine(
		t,
		sessionID,
		transcriptDir,
		cwd,
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)
	items := restarted.RuntimeItems()
	if len(items) != 1 ||
		items[0].ID != queued.ID ||
		items[0].State != RuntimeItemPending {
		t.Fatalf("uncovered transcript failure recovery = %#v", items)
	}
	data, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"kind":"user-prompt"`)) {
		t.Fatalf("failed transcript append published prompt: %s", data)
	}
}

func TestP302bSettlementFailureRecoversFromTranscriptCoverage(t *testing.T) {
	transcriptDir := t.TempDir()
	cwd := t.TempDir()
	sessionID := "p302b-settlement-failure"
	model := &captureInputModel{}
	engine := newP302bTestEngine(
		t,
		sessionID,
		transcriptDir,
		cwd,
		model,
		DefaultPromptCapabilityResolver(),
	)
	queued, err := engine.EnqueueUserInput(UserTurnInput{
		Prompt: "inspect",
		Images: []UserImage{{
			MIMEType:   "image/png",
			Base64Data: testUserImagePNGBase64,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := engine.ClaimNextRuntimeItem()
	if err != nil || !ok {
		t.Fatalf("claim: item=%#v ok=%v err=%v", claimed, ok, err)
	}

	ledgerPath := RuntimeInputPersistencePath(engine.transcript.Path())
	processingLedger, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ledgerPath, 0o700); err != nil {
		t.Fatal(err)
	}
	events, _ := engine.SubmitRuntimeItem(context.Background(), claimed)
	terminal, _ := collectPromptInputEvents(t, events)
	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %#v", terminal)
	}
	if len(model.inputs) != 1 {
		t.Fatalf("settlement failure model calls = %d", len(model.inputs))
	}
	engine.inputCoordinator.mu.Lock()
	items := cloneRuntimeItems(engine.inputCoordinator.items)
	engine.inputCoordinator.mu.Unlock()
	if len(items) != 1 ||
		items[0].ID != queued.ID ||
		items[0].State != RuntimeItemProcessing {
		t.Fatalf("failed settlement memory state = %#v", items)
	}
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledgerPath, processingLedger, 0o600); err != nil {
		t.Fatal(err)
	}
	engine.Close()

	restarted := newP302bTestEngine(
		t,
		sessionID,
		transcriptDir,
		cwd,
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)
	if items := restarted.RuntimeItems(); len(items) != 0 {
		t.Fatalf("transcript-covered settlement failure survived: %#v", items)
	}
	coverage, err := restarted.transcript.RuntimeItemDeliveryCoverage(
		[]string{queued.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := coverage[queued.ID]; !ok {
		t.Fatal("settlement failure transcript lost delivery coverage")
	}
}

func TestP302bRestartSettlesTranscriptCoveredProcessingItem(t *testing.T) {
	transcriptDir := t.TempDir()
	cwd := t.TempDir()
	sessionID := "p302b-covered-processing"
	engine := newP302bTestEngine(
		t,
		sessionID,
		transcriptDir,
		cwd,
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)
	queued, err := engine.EnqueueUserInput(UserTurnInput{
		Prompt: "inspect",
		Images: []UserImage{{
			MIMEType:   "image/png",
			Base64Data: testUserImagePNGBase64,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := engine.inputCoordinator.ClaimNextIdle(
		engine.runtimeInputScope(),
	)
	if err != nil || !ok {
		t.Fatalf("claim: item=%#v ok=%v err=%v", claimed, ok, err)
	}
	record := runtimeItemDurablePrompt(&claimed)
	message := runtimeItemToAttachmentMessage(claimed)
	if err := engine.transcript.RecordRuntimeUserPrompt(
		record,
		message,
		queued.ID,
	); err != nil {
		t.Fatal(err)
	}
	if err := engine.transcript.Flush(); err != nil {
		t.Fatal(err)
	}
	engine.inputCoordinator.mu.Lock()
	items := cloneRuntimeItems(engine.inputCoordinator.items)
	engine.inputCoordinator.mu.Unlock()
	if len(items) != 1 || items[0].State != RuntimeItemProcessing {
		t.Fatalf("fixture did not retain processing item: %#v", items)
	}
	engine.Close()

	restarted := newP302bTestEngine(
		t,
		sessionID,
		transcriptDir,
		cwd,
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)
	if items := restarted.RuntimeItems(); len(items) != 0 {
		t.Fatalf(
			"transcript-covered item %q survived restart: %#v",
			queued.ID,
			items,
		)
	}
}

func TestP302bDeliveryCoverageRejectsSpuriousRuntimeIdentity(t *testing.T) {
	recorder := transcript.NewRecorder("p302b-spurious-coverage", t.TempDir())
	if err := os.WriteFile(
		recorder.Path(),
		[]byte(
			`{"kind":"metadata","runtime_item_id":"queued-prompt"}`+"\n",
		),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	covered, err := recorder.RuntimeItemDeliveryCoverage(
		[]string{"queued-prompt"},
	)
	if err == nil {
		t.Fatalf("spurious runtime identity covered item: %#v", covered)
	}
	if len(covered) != 0 {
		t.Fatalf("spurious runtime identity returned coverage: %#v", covered)
	}
}

func TestP302bConcurrentClaimReturnsOneRefPrompt(t *testing.T) {
	engine := newP302bTestEngine(
		t,
		"p302b-concurrent",
		t.TempDir(),
		t.TempDir(),
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)
	if _, err := engine.EnqueueUserInput(UserTurnInput{
		Prompt: "inspect",
		Images: []UserImage{{
			MIMEType:   "image/png",
			Base64Data: testUserImagePNGBase64,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	var claimed atomic.Int32
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, ok, err := engine.ClaimNextRuntimeItem(); err == nil && ok {
				claimed.Add(1)
			}
		}()
	}
	group.Wait()
	if claimed.Load() != 1 {
		t.Fatalf("successful claims = %d, want 1", claimed.Load())
	}
}

func TestP302bClaimedRefPromptHasOneSubmissionLease(t *testing.T) {
	engine := newP302bTestEngine(
		t,
		"p302b-submit-lease",
		t.TempDir(),
		t.TempDir(),
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)
	if _, err := engine.EnqueueUserInput(UserTurnInput{
		Prompt: "inspect",
		Images: []UserImage{{
			MIMEType:   "image/png",
			Base64Data: testUserImagePNGBase64,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := engine.inputCoordinator.ClaimNextIdle(
		engine.runtimeInputScope(),
	)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	var admitted atomic.Int32
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := engine.inputCoordinator.
				processingDurableRuntimePrompt(claimed); err == nil {
				admitted.Add(1)
			}
		}()
	}
	group.Wait()
	if admitted.Load() != 1 {
		t.Fatalf("submission leases = %d, want 1", admitted.Load())
	}
}

func TestP302bDurablePromptUnionAndWriterAreSealed(t *testing.T) {
	var prompt RuntimeUserPrompt
	err := json.Unmarshal([]byte(`{
		"prompt":"inline",
		"images":[{"MIMEType":"image/png","Base64Data":"cG5n"}],
		"prompt_envelope":{"version":1,"turn_id":"turn","parts":[]}
	}`), &prompt)
	if err == nil {
		t.Fatal("mixed inline/ref prompt was accepted")
	}

	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl.runtime-inputs.json")
	store := mediastore.New(filepath.Join(root, "session.jsonl.media"))
	coordinator, err := NewRuntimeInputCoordinator(
		RuntimeInputCoordinatorConfig{
			SessionID:  "sealed",
			Path:       path,
			mediaStore: store,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	record := promptrecord.Record{
		Version: promptrecord.Version1,
		TurnID:  "sealed-turn",
		Parts: []promptrecord.Part{{
			Kind: promptrecord.PartText,
			Text: &promptrecord.TextPart{Text: "inspect"},
		}},
	}
	_, err = coordinator.Enqueue(RuntimeItem{
		ID:       "caller-injected",
		Kind:     RuntimeItemUserPrompt,
		Priority: RuntimePriorityNext,
		Scope:    RuntimeInputScope{SessionID: "sealed"},
		UserPrompt: &RuntimeUserPrompt{
			durablePrompt: &record,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "unauthorized durable prompt") {
		t.Fatalf("caller-supplied ref error = %v", err)
	}
	if items := coordinator.Snapshot(
		RuntimeInputScope{SessionID: "sealed"},
	); len(items) != 0 {
		t.Fatalf("unauthorized prompt entered queue: %#v", items)
	}
}

func readP302bLedgerRecord(
	t *testing.T,
	path string,
) promptrecord.Record {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var envelope runtimeInputEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Items) != 1 ||
		envelope.Items[0].UserPrompt == nil ||
		envelope.Items[0].UserPrompt.durablePrompt == nil {
		t.Fatalf("ledger envelope = %#v", envelope)
	}
	return envelope.Items[0].UserPrompt.durablePrompt.Clone()
}

func assertP302bRefOnlyLedger(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		testUserImagePNGBase64,
		`"digest"`,
		`"Base64Data"`,
		`"Path"`,
		`"Name"`,
		"/private/screen.png",
		"private.png",
	} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatalf("ledger contains %q: %s", forbidden, data)
		}
	}
	if !bytes.Contains(data, []byte(`"prompt_envelope"`)) ||
		!bytes.Contains(data, []byte(`"media_id"`)) {
		t.Fatalf("ledger has no ref prompt envelope: %s", data)
	}
}

func newP302bTestEngine(
	t *testing.T,
	sessionID string,
	transcriptDir string,
	cwd string,
	model *captureInputModel,
	capabilities PromptCapabilityResolver,
) *QueryEngine {
	t.Helper()
	engine := NewQueryEngine(QueryEngineConfig{
		ChatModel:                model,
		CWD:                      cwd,
		TranscriptDir:            transcriptDir,
		SessionID:                sessionID,
		MaxTurns:                 2,
		Model:                    "gpt-4o",
		ModelResolver:            promptInputOpenAIResolver(),
		PromptCapabilityResolver: capabilities,
		HookExecutor:             hooks.NewExecutor(),
	})
	t.Cleanup(engine.Close)
	return engine
}
