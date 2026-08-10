package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/transcript"
	"github.com/cloudwego/eino/schema"
)

func TestP49CloneRuntimeGoalContinuationDetachesOptionalBudget(t *testing.T) {
	budget := uint64(100)
	item := RuntimeItem{
		GoalContinuation: &RuntimeGoalContinuation{TokenBudget: &budget},
	}
	cloned := cloneRuntimeItem(item)
	if cloned.GoalContinuation == nil ||
		cloned.GoalContinuation.TokenBudget == nil {
		t.Fatalf("cloned Goal continuation = %#v", cloned)
	}
	*cloned.GoalContinuation.TokenBudget = 200
	if *item.GoalContinuation.TokenBudget != 100 {
		t.Fatalf("Goal continuation budget aliases clone: %#v", item)
	}
}

func TestRuntimeInputCoordinatorOrdersByPriorityThenFIFOWithoutMetaOverride(t *testing.T) {
	coordinator := newTestRuntimeInputCoordinator(t, "priority-session", "")
	items := []RuntimeItem{
		runtimePromptTestItem("next-first", RuntimePriorityNext, false),
		runtimePromptTestItem("later-meta", RuntimePriorityLater, true),
		runtimePromptTestItem("now-regular", RuntimePriorityNow, false),
		runtimePromptTestItem("next-meta", RuntimePriorityNext, true),
	}
	for index := range items {
		items[index].Scope.SessionID = "priority-session"
		if _, err := coordinator.Enqueue(items[index]); err != nil {
			t.Fatal(err)
		}
	}

	claimed, err := coordinator.ClaimSafePoint(
		RuntimeInputScope{SessionID: "priority-session"},
		true,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	got := runtimeItemTestIDs(claimed)
	want := []string{"now-regular", "next-first", "next-meta", "later-meta"}
	if !equalRuntimeItemTestIDs(got, want) {
		t.Fatalf("claim order = %v, want %v", got, want)
	}
}

func TestRuntimeInputCoordinatorBoundedEnqueueIsAtomic(t *testing.T) {
	coordinator := newTestRuntimeInputCoordinator(t, "bounded-session", "")
	var accepted atomic.Int32
	var group sync.WaitGroup
	for index := 0; index < maxQueuedUserInputs*2; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			_, err := coordinator.EnqueueBounded(RuntimeItem{
				ID:       generateUUID(),
				Kind:     RuntimeItemUserPrompt,
				Priority: RuntimePriorityNext,
				Scope:    RuntimeInputScope{SessionID: "bounded-session"},
				UserPrompt: &RuntimeUserPrompt{
					Prompt: "queued",
				},
			}, maxQueuedUserInputs)
			if err == nil {
				accepted.Add(1)
			}
		}(index)
	}
	group.Wait()
	if accepted.Load() != maxQueuedUserInputs {
		t.Fatalf("accepted = %d, want %d", accepted.Load(), maxQueuedUserInputs)
	}
	if pending := coordinator.Snapshot(
		RuntimeInputScope{SessionID: "bounded-session"},
	); len(pending) != maxQueuedUserInputs {
		t.Fatalf("pending = %d, want %d", len(pending), maxQueuedUserInputs)
	}
}

func TestRuntimeInputCoordinatorRecoversProcessingAndTranscriptDelivery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-inputs.json")
	config := RuntimeInputCoordinatorConfig{
		SessionID: "recovery-session",
		Path:      path,
	}
	coordinator, err := NewRuntimeInputCoordinator(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"processing", "pending"} {
		item := runtimePromptTestItem(id, RuntimePriorityNext, false)
		item.Scope.SessionID = "recovery-session"
		if _, err := coordinator.Enqueue(item); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := coordinator.claimByID("processing"); err != nil || !ok {
		t.Fatalf("claim processing item: ok=%v err=%v", ok, err)
	}

	recovered, err := NewRuntimeInputCoordinator(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := recovered.Snapshot(RuntimeInputScope{SessionID: "recovery-session"})
	if got := runtimeItemTestIDs(snapshot); !equalRuntimeItemTestIDs(
		got,
		[]string{"processing", "pending"},
	) {
		t.Fatalf("recovered = %v", got)
	}
	for _, item := range snapshot {
		if item.State != RuntimeItemPending {
			t.Fatalf("recovered state = %#v", item)
		}
	}

	delivered, err := NewRuntimeInputCoordinator(
		config,
		map[string]struct{}{"processing": {}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := runtimeItemTestIDs(
		delivered.Snapshot(RuntimeInputScope{SessionID: "recovery-session"}),
	); !equalRuntimeItemTestIDs(got, []string{"pending"}) {
		t.Fatalf("transcript recovery = %v", got)
	}
}

func TestRuntimeItemDeliveryIDsRetainActiveTranscriptCoverageAfterReceiptEviction(
	t *testing.T,
) {
	loaded := &transcript.LoadResult{
		Messages: []*schema.Message{{
			Role:    schema.User,
			Content: "retained completion",
			Extra: map[string]any{
				"runtime_item_id": "completion-retained",
			},
		}},
		AgentCompletionReceipts: []transcript.AgentCompletionReceipt{{
			Version:      transcript.AgentCompletionReceiptVersion,
			CompletionID: "completion-newer",
		}},
	}
	ids := runtimeItemIDsFromLoadedTranscript(loaded)
	if _, ok := ids["completion-retained"]; !ok {
		t.Fatal("active transcript boundary lost evicted completion coverage")
	}
	if _, ok := ids["completion-newer"]; !ok {
		t.Fatal("retained completion receipt was not admitted")
	}
}

func TestRuntimeInputCoordinatorDropsStaleStopOnRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-inputs.json")
	config := RuntimeInputCoordinatorConfig{SessionID: "stop-session", Path: path}
	coordinator, err := NewRuntimeInputCoordinator(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = coordinator.Enqueue(RuntimeItem{
		ID: "stop", Kind: RuntimeItemStop, Priority: RuntimePriorityNow,
		Scope: RuntimeInputScope{SessionID: "stop-session"},
		Stop:  &RuntimeStop{Mode: RuntimeStopImmediate, Reason: "cancel"},
	})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := NewRuntimeInputCoordinator(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	if items := recovered.Snapshot(
		RuntimeInputScope{SessionID: "stop-session"},
	); len(items) != 0 {
		t.Fatalf("stale stop survived recovery: %#v", items)
	}
}

func TestRuntimeInputCoordinatorReleaseAndCancelRespectOwnership(t *testing.T) {
	coordinator := newTestRuntimeInputCoordinator(t, "ownership-session", "")
	item := runtimePromptTestItem("owned", RuntimePriorityNext, false)
	item.Scope.SessionID = "ownership-session"
	if _, err := coordinator.Enqueue(item); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := coordinator.claimByID("owned"); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if cancelled, err := coordinator.Cancel("owned"); err != nil || cancelled {
		t.Fatalf("processing cancellation: cancelled=%v err=%v", cancelled, err)
	}
	if released, err := coordinator.Release("owned"); err != nil || !released {
		t.Fatalf("release: released=%v err=%v", released, err)
	}
	if cancelled, err := coordinator.Cancel("owned"); err != nil || !cancelled {
		t.Fatalf("pending cancellation: cancelled=%v err=%v", cancelled, err)
	}
}

func TestRuntimeInputCoordinatorRoundTripsCheckpointSafeImagePayload(t *testing.T) {
	item := RuntimeItem{
		Version:  runtimeItemVersion,
		ID:       "image",
		Kind:     RuntimeItemUserPrompt,
		Priority: RuntimePriorityNext,
		Scope:    RuntimeInputScope{SessionID: "plain-session"},
		State:    RuntimeItemPending,
		UserPrompt: &RuntimeUserPrompt{
			Prompt: "inspect",
			Images: []UserImage{{
				Name: "screen.png", Path: "/tmp/screen.png",
				MIMEType: "image/png", Base64Data: testUserImagePNGBase64,
			}},
		},
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RuntimeItem
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !runtimeItemsEqual(item, decoded) {
		t.Fatalf("round trip = %#v", decoded)
	}
	message := runtimeItemToAttachmentMessage(decoded)
	if len(message.UserInputMultiContent) != 2 ||
		message.UserInputMultiContent[1].Image == nil ||
		message.UserInputMultiContent[1].Image.Base64Data == nil ||
		*message.UserInputMultiContent[1].Image.Base64Data != testUserImagePNGBase64 {
		t.Fatalf("image projection = %#v", message)
	}
}

func TestP306RuntimeInputCoordinatorInlineImagesAreDecodeOnly(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runtime-inputs.json")
	config := RuntimeInputCoordinatorConfig{SessionID: "image-session", Path: path}
	coordinator, err := NewRuntimeInputCoordinator(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	item := runtimePromptTestItem("image", RuntimePriorityNext, false)
	item.Scope.SessionID = "image-session"
	item.UserPrompt.Images = []UserImage{{
		Name: "a.png", Path: "/tmp/a.png",
		MIMEType: "image/png", Base64Data: testUserImagePNGBase64,
	}}
	for name, enqueue := range map[string]func() error{
		"bounded": func() error {
			_, enqueueErr := coordinator.EnqueueBounded(item, 1)
			return enqueueErr
		},
		"batch": func() error {
			_, enqueueErr := coordinator.EnqueueBatch([]RuntimeItem{item})
			return enqueueErr
		},
	} {
		t.Run(name, func(t *testing.T) {
			if enqueueErr := enqueue(); enqueueErr == nil ||
				enqueueErr.Error() != runtimeInlineImagesDecodeOnly {
				t.Fatalf("enqueue error = %v", enqueueErr)
			}
			if items := coordinator.Snapshot(
				RuntimeInputScope{SessionID: "image-session"},
			); len(items) != 0 {
				t.Fatalf("inline image entered queue: %#v", items)
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("rejected enqueue mutated ledger: %v", statErr)
			}
		})
	}
	if got := item.UserPrompt.Images[0]; got.Name != "a.png" ||
		got.Path != "/tmp/a.png" {
		t.Fatalf("enqueue mutated caller image provenance: %#v", got)
	}

	legacy := runtimeInputEnvelope{
		Version:      runtimeInputEnvelopeVersion,
		Revision:     1,
		NextSequence: 1,
		Items: []RuntimeItem{{
			Version:    runtimeItemVersion,
			ID:         "legacy-image",
			Kind:       RuntimeItemUserPrompt,
			Priority:   RuntimePriorityNext,
			Scope:      RuntimeInputScope{SessionID: "image-session"},
			Sequence:   1,
			EnqueuedAt: time.Unix(1, 0).UTC(),
			State:      RuntimeItemPending,
			UserPrompt: &RuntimeUserPrompt{
				Prompt: "inspect",
				Images: []UserImage{{
					Name: "legacy.png", Path: "/legacy/screen.png",
					MIMEType: "image/png", Base64Data: testUserImagePNGBase64,
				}},
			},
		}},
	}
	persisted, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, persisted, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewRuntimeInputCoordinator(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persisted, []byte("legacy.png")) ||
		bytes.Contains(persisted, []byte("/legacy/screen.png")) {
		t.Fatalf("recovery retained legacy image provenance: %s", persisted)
	}
	claimed, err := reopened.ClaimSafePoint(
		RuntimeInputScope{SessionID: "image-session"},
		true,
		true,
	)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	projected := runtimeItemToAttachmentMessage(claimed[0])
	if len(projected.UserInputMultiContent) != 2 ||
		projected.UserInputMultiContent[1].Image == nil ||
		projected.UserInputMultiContent[1].Image.Base64Data == nil ||
		*projected.UserInputMultiContent[1].Image.Base64Data != testUserImagePNGBase64 {
		t.Fatalf("projected=%#v", projected)
	}
	if got := claimed[0].UserPrompt.Images[0]; got.Name != "" || got.Path != "" {
		t.Fatalf("legacy image retained provenance: %#v", got)
	}
	for _, tc := range []struct {
		name  string
		image UserImage
		code  string
	}{
		{
			name:  "missing data",
			image: UserImage{MIMEType: "image/png"},
			code:  "missing_base64_data",
		},
		{
			name:  "missing MIME",
			image: UserImage{Base64Data: "cG5n"},
			code:  "missing_mime_type",
		},
	} {
		t.Run("legacy "+tc.name, func(t *testing.T) {
			corruptPath := filepath.Join(root, tc.name+".json")
			corrupt := legacy
			corrupt.Items = cloneRuntimeItems(legacy.Items)
			corrupt.Items[0].UserPrompt.Images = []UserImage{tc.image}
			data, marshalErr := json.MarshalIndent(corrupt, "", "  ")
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if writeErr := os.WriteFile(corruptPath, data, 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
			_, loadErr := NewRuntimeInputCoordinator(
				RuntimeInputCoordinatorConfig{
					SessionID: "image-session",
					Path:      corruptPath,
				},
				nil,
			)
			var imageErr *UserImageValidationError
			if !errors.As(loadErr, &imageErr) ||
				imageErr.ImageIndex != 0 ||
				imageErr.ReasonCode != tc.code {
				t.Fatalf("legacy recovery error = %v", loadErr)
			}
		})
	}
}

func TestP306RuntimeInputCoordinatorRejectsInlineImagesForDeliveredIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-inputs.json")
	coordinator, err := NewRuntimeInputCoordinator(
		RuntimeInputCoordinatorConfig{
			SessionID: "delivered-image-session",
			Path:      path,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	item := runtimePromptTestItem("delivered-image", RuntimePriorityNext, false)
	item.Scope.SessionID = "delivered-image-session"
	if _, err := coordinator.Enqueue(item); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Settle(item.ID); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	item.UserPrompt.Images = []UserImage{{
		MIMEType: "image/png", Base64Data: testUserImagePNGBase64,
	}}
	_, err = coordinator.Enqueue(item)
	if err == nil || err.Error() != runtimeInlineImagesDecodeOnly {
		t.Fatalf("delivered-ID enqueue err=%v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("delivered-ID rejection mutated durable ledger")
	}
}

func TestRuntimeInputCoordinatorFailsClosedOnCorruptLedger(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime-inputs.json")
	if err := os.WriteFile(
		path,
		[]byte(`{"version":999,"revision":1,"items":[]}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRuntimeInputCoordinator(
		RuntimeInputCoordinatorConfig{SessionID: "corrupt-session", Path: path},
		nil,
	); err == nil {
		t.Fatal("unsupported ledger version was accepted")
	}
}

func TestRuntimeInputCoordinatorRejectsMismatchedThreadOnEnqueueAndRecovery(t *testing.T) {
	config := RuntimeInputCoordinatorConfig{
		SessionID: "thread-session",
		ThreadID:  "thread-owner",
	}
	t.Run("enqueue", func(t *testing.T) {
		coordinator, err := NewRuntimeInputCoordinator(config, nil)
		if err != nil {
			t.Fatal(err)
		}
		item := runtimePromptTestItem("wrong-thread", RuntimePriorityNext, false)
		item.Scope = RuntimeInputScope{
			SessionID: "thread-session",
			ThreadID:  "thread-other",
		}
		if _, err := coordinator.Enqueue(item); err == nil {
			t.Fatal("mismatched thread scope was accepted")
		}
	})
	t.Run("recovery", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "runtime-inputs.json")
		item := runtimePromptTestItem("wrong-thread", RuntimePriorityNext, false)
		item.Version = runtimeItemVersion
		item.Sequence = 1
		item.State = RuntimeItemPending
		item.Scope = RuntimeInputScope{
			SessionID: "thread-session",
			ThreadID:  "thread-other",
		}
		data, err := json.Marshal(runtimeInputEnvelope{
			Version:      runtimeInputEnvelopeVersion,
			Revision:     1,
			NextSequence: 1,
			Items:        []RuntimeItem{item},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		recoveryConfig := config
		recoveryConfig.Path = path
		if _, err := NewRuntimeInputCoordinator(recoveryConfig, nil); err == nil {
			t.Fatal("recovery accepted a mismatched thread scope")
		}
	})
}

func TestQueryEngineFailsClosedWhenRuntimeInputLedgerIsCorrupt(t *testing.T) {
	root := t.TempDir()
	recorder := transcript.NewRecorder("corrupt-engine", root)
	path := RuntimeInputPersistencePath(recorder.Path())
	if err := os.WriteFile(
		path,
		[]byte(`{"version":999,"revision":1,"items":[]}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	queryEngine := NewQueryEngine(QueryEngineConfig{
		SessionID:     "corrupt-engine",
		TranscriptDir: root,
		CWD:           root,
		ChatModel:     &transcriptModel{},
	})
	t.Cleanup(queryEngine.Close)

	if _, err := queryEngine.EnqueueUserInput(UserTurnInput{Prompt: "queued"}); err == nil {
		t.Fatal("enqueue succeeded with a corrupt runtime input ledger")
	}
	events, terminal := queryEngine.SubmitMessage(t.Context(), "must not execute")
	if terminal.Reason != TerminalModelError || terminal.Err == nil {
		t.Fatalf("terminal = %#v", terminal)
	}
	for range events {
	}
}

func TestQueryEngineDoesNotFallbackToMemoryWhenRuntimeInputLedgerParentIsNotDirectory(
	t *testing.T,
) {
	root := t.TempDir()
	blockedDir := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blockedDir, []byte("block ledger path"), 0o600); err != nil {
		t.Fatal(err)
	}
	queryEngine := NewQueryEngine(QueryEngineConfig{
		SessionID:     "blocked-ledger",
		TranscriptDir: blockedDir,
		CWD:           root,
	})
	t.Cleanup(queryEngine.Close)

	if _, err := queryEngine.EnqueueUserInput(UserTurnInput{Prompt: "queued"}); err == nil {
		t.Fatal("enqueue succeeded with a non-directory ledger parent")
	}
	if items := queryEngine.RuntimeItems(); len(items) != 0 {
		t.Fatalf("failed durable enqueue mutated memory: %#v", items)
	}
}

func TestQueryEngineSettlesIdleClaimOnlyAfterTranscriptDelivery(t *testing.T) {
	root := t.TempDir()
	config := QueryEngineConfig{
		SessionID: "runtime-submit", TranscriptDir: root, CWD: root,
		ChatModel: &transcriptModel{},
	}
	queryEngine := NewQueryEngine(config)
	queued, err := queryEngine.EnqueueUserInput(UserTurnInput{
		Display: "queued", Prompt: "queued full prompt",
	})
	if err != nil {
		t.Fatal(err)
	}
	item, ok, err := queryEngine.ClaimNextRuntimeItem()
	if err != nil || !ok {
		t.Fatalf("claim runtime item: ok=%v err=%v", ok, err)
	}
	var lifecycle []CommandLifecyclePhase
	events, _ := queryEngine.SubmitRuntimeItem(t.Context(), item)
	for event := range events {
		if event.CommandLifecycle != nil &&
			event.CommandLifecycle.CommandUUID == queued.ID {
			lifecycle = append(lifecycle, event.CommandLifecycle.Phase)
		}
	}
	if len(lifecycle) != 2 ||
		lifecycle[0] != CommandLifecycleStarted ||
		lifecycle[1] != CommandLifecycleCompleted {
		t.Fatalf("submit lifecycle = %v", lifecycle)
	}
	if len(queryEngine.RuntimeItems()) != 0 {
		t.Fatalf("settled item remains: %#v", queryEngine.RuntimeItems())
	}
	loaded, err := queryEngine.GetTranscript().LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	delivered := runtimeItemIDsFromMessages(loaded.Messages)
	if _, ok := delivered[queued.ID]; !ok {
		t.Fatalf("transcript omitted runtime item %q", queued.ID)
	}
	queryEngine.Close()

	reopened := NewQueryEngine(config)
	t.Cleanup(reopened.Close)
	if len(reopened.RuntimeItems()) != 0 {
		t.Fatalf("delivered item replayed after restart: %#v", reopened.RuntimeItems())
	}
}

func runtimePromptTestItem(
	id string,
	priority RuntimeInputPriority,
	isMeta bool,
) RuntimeItem {
	return RuntimeItem{
		ID: id, Kind: RuntimeItemSteering, Priority: priority, IsMeta: isMeta,
		UserPrompt: &RuntimeUserPrompt{Prompt: id},
	}
}

func runtimeItemTestIDs(items []RuntimeItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func equalRuntimeItemTestIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
