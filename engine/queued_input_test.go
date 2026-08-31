package engine

import (
	"errors"
	"fmt"
	"testing"
)

func TestQueryEngineOwnsQueueAndClaimsPendingUserInput(t *testing.T) {
	engine := newP302bTestEngine(
		t,
		"queued-input-owner",
		t.TempDir(),
		t.TempDir(),
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)

	queued, err := engine.EnqueueUserInput(UserTurnInput{
		Display: "compact label", Prompt: "full prompt",
		Images: []UserImage{{
			Name: "a.png", Path: "/private/a.png",
			MIMEType: "image/png", Base64Data: testUserImagePNGBase64,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if queued.ID == "" || queued.State != RuntimeItemPending {
		t.Fatalf("queued input = %#v", queued)
	}
	if queued.Images[0].Name != "" || queued.Images[0].Path != "" {
		t.Fatalf("queued input retained image provenance: %#v", queued.Images[0])
	}

	snapshot := engine.QueuedUserInputs()
	if len(snapshot) != 1 || snapshot[0].Prompt != "full prompt" || len(snapshot[0].Images) != 1 {
		t.Fatalf("queue snapshot = %#v", snapshot)
	}
	snapshot[0].Images[0].Base64Data = "mutated"
	if engine.QueuedUserInputs()[0].Images[0].Base64Data != testUserImagePNGBase64 {
		t.Fatal("queue image snapshot aliases manager state")
	}

	claimed, ok := engine.ClaimNextQueuedUserInput()
	if !ok || claimed.ID != queued.ID || claimed.State != RuntimeItemProcessing {
		t.Fatalf("claimed input = %#v, %v", claimed, ok)
	}
	if len(engine.QueuedUserInputs()) != 0 {
		t.Fatal("claimed input remains pending")
	}
}

func TestQueryEngineQueueRejectsInvalidUserImages(t *testing.T) {
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
			name:  "missing MIME type",
			image: UserImage{Base64Data: "cG5n"},
			code:  "missing_mime_type",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := NewQueryEngine(QueryEngineConfig{
				CWD: t.TempDir(), TranscriptDir: t.TempDir(),
			})
			t.Cleanup(engine.Close)

			_, err := engine.EnqueueUserInput(UserTurnInput{
				Prompt: "inspect",
				Images: []UserImage{tc.image},
			})
			var admissionErr *PromptInputAdmissionError
			if !errors.As(err, &admissionErr) ||
				admissionErr.PartIndex != 1 ||
				admissionErr.PartKind != string(promptPartImage) ||
				admissionErr.ReasonCode != tc.code {
				t.Fatalf("error = %#v", err)
			}
			if items := engine.RuntimeItems(); len(items) != 0 {
				t.Fatalf("rejected image entered queue: %#v", items)
			}
		})
	}
}

func TestQueryEngineCancelsOnlyOwnUserQueueInput(t *testing.T) {
	engine := NewQueryEngine(QueryEngineConfig{
		CWD: t.TempDir(), TranscriptDir: t.TempDir(),
	})
	t.Cleanup(engine.Close)
	queued, err := engine.EnqueueUserInput(UserTurnInput{Prompt: "remove me"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.inputCoordinator.Enqueue(RuntimeItem{
		ID: "system", Kind: RuntimeItemAgentNotification, Priority: RuntimePriorityNext,
		Scope: engine.runtimeInputScope(), IsMeta: true, Origin: "system",
		AgentNotification: &RuntimeAgentNotification{
			AgentID: "agent", Status: "completed", Message: "keep",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !engine.CancelQueuedUserInput(queued.ID) {
		t.Fatal("pending user input was not cancelled")
	}
	if engine.CancelQueuedUserInput("system") {
		t.Fatal("user API cancelled a system queue command")
	}
	if remaining := engine.RuntimeItems(); len(remaining) != 1 || remaining[0].ID != "system" {
		t.Fatalf("remaining runtime inputs = %#v", remaining)
	}
}

func TestCommandToAttachmentMessagePreservesQueuedImages(t *testing.T) {
	message := runtimeItemToAttachmentMessage(RuntimeItem{
		ID: "queued-image", Kind: RuntimeItemUserPrompt, Priority: RuntimePriorityNext,
		UserPrompt: &RuntimeUserPrompt{Prompt: "inspect", Images: []UserImage{{
			Name: "screen.png", MIMEType: "image/png", Base64Data: testUserImagePNGBase64,
		}}},
	})
	if len(message.UserInputMultiContent) != 2 || message.UserInputMultiContent[1].Image == nil ||
		message.UserInputMultiContent[1].Image.Base64Data == nil ||
		*message.UserInputMultiContent[1].Image.Base64Data != testUserImagePNGBase64 {
		t.Fatalf("queued multimodal message = %#v", message)
	}
}

func TestQueryEngineBoundsPendingUserInputs(t *testing.T) {
	engine := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir(), TranscriptDir: t.TempDir()})
	t.Cleanup(engine.Close)
	for i := 0; i < maxQueuedUserInputs; i++ {
		if _, err := engine.EnqueueUserInput(UserTurnInput{Prompt: fmt.Sprintf("queued-%d", i)}); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	if _, err := engine.EnqueueUserInput(UserTurnInput{Prompt: "overflow"}); err == nil {
		t.Fatal("queue accepted input beyond the bounded user limit")
	}
}

func TestQueryEngineIDBoundQueueAdmissionIsIdempotentAndFailClosed(t *testing.T) {
	query := NewQueryEngine(QueryEngineConfig{
		CWD: t.TempDir(), TranscriptDir: t.TempDir(),
	})
	t.Cleanup(query.Close)

	const itemID = "11111111-1111-4111-8111-111111111111"
	first, err := query.EnqueueUserInputWithID(itemID, UserTurnInput{
		Display: "queued preview",
		Prompt:  "queued prompt",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstState, err := query.QueuedPromptState()
	if err != nil || firstState.Revision == 0 || len(firstState.Items) != 1 {
		t.Fatalf("first queue state = %#v err=%v", firstState, err)
	}
	repeated, err := query.EnqueueUserInputWithID(itemID, UserTurnInput{
		Display: "queued preview",
		Prompt:  "queued prompt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != itemID || repeated.ID != itemID ||
		first.EnqueuedAt != repeated.EnqueuedAt || first.State != repeated.State {
		t.Fatalf("idempotent admission changed identity: first=%#v repeated=%#v", first, repeated)
	}
	repeatedState, err := query.QueuedPromptState()
	if err != nil || repeatedState.Revision != firstState.Revision {
		t.Fatalf("idempotent admission advanced queue revision: first=%#v repeated=%#v err=%v", firstState, repeatedState, err)
	}

	_, err = query.EnqueueUserInputWithID(itemID, UserTurnInput{
		Display: "different preview",
		Prompt:  "different prompt",
	})
	var conflict *RuntimeInputConflictError
	if !errors.As(err, &conflict) || conflict.ID != itemID {
		t.Fatalf("conflicting retry error = %#v", err)
	}
	queued := query.QueuedUserInputs()
	if len(queued) != 1 || queued[0].ID != itemID || queued[0].Prompt != "queued prompt" {
		t.Fatalf("conflicting retry mutated queue: %#v", queued)
	}
}

func TestQueryEngineIDBoundQueueAdmissionReceiptSurvivesRemovalAndRestart(t *testing.T) {
	root := t.TempDir()
	const sessionID = "queue-admission-receipt-restart"
	config := QueryEngineConfig{
		SessionID:     sessionID,
		ThreadID:      sessionID,
		CWD:           root,
		TranscriptDir: root,
	}
	query := NewQueryEngine(config)

	const settledID = "22222222-2222-4222-8222-222222222222"
	settledInput := UserTurnInput{Display: "settled preview", Prompt: "settled prompt"}
	settled, err := query.EnqueueUserInputWithID(settledID, settledInput)
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := query.ClaimNextRuntimeItem()
	if err != nil || !ok || claimed.ID != settledID {
		t.Fatalf("claim settled admission: item=%#v ok=%v err=%v", claimed, ok, err)
	}
	if err := query.inputCoordinator.Settle(settledID); err != nil {
		t.Fatal(err)
	}
	settledState, err := query.QueuedPromptState()
	if err != nil || len(settledState.Items) != 0 {
		t.Fatalf("settled queue state = %#v err=%v", settledState, err)
	}
	if admitted, err := query.HasQueuedUserInputAdmission(settledID, settledInput); err != nil || !admitted {
		t.Fatalf("settled admission receipt: admitted=%v err=%v", admitted, err)
	}
	repeated, err := query.EnqueueUserInputWithID(settledID, settledInput)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.State != RuntimeItemProcessing || repeated.EnqueuedAt != settled.EnqueuedAt {
		t.Fatalf("historical settlement ACK = %#v, first=%#v", repeated, settled)
	}
	afterSettledRetry, err := query.QueuedPromptState()
	if err != nil || afterSettledRetry.Revision != settledState.Revision || len(afterSettledRetry.Items) != 0 {
		t.Fatalf("historical settlement retry mutated queue: before=%#v after=%#v err=%v", settledState, afterSettledRetry, err)
	}

	const cancelledID = "33333333-3333-4333-8333-333333333333"
	cancelledInput := UserTurnInput{Display: "cancelled preview", Prompt: "cancelled prompt"}
	cancelled, err := query.EnqueueUserInputWithID(cancelledID, cancelledInput)
	if err != nil {
		t.Fatal(err)
	}
	if !query.CancelQueuedUserInput(cancelledID) {
		t.Fatal("cancelled admission remained pending")
	}
	cancelledState, err := query.QueuedPromptState()
	if err != nil || len(cancelledState.Items) != 0 {
		t.Fatalf("cancelled queue state = %#v err=%v", cancelledState, err)
	}
	repeated, err = query.EnqueueUserInputWithID(cancelledID, cancelledInput)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.State != RuntimeItemProcessing || repeated.EnqueuedAt != cancelled.EnqueuedAt {
		t.Fatalf("historical cancellation ACK = %#v, first=%#v", repeated, cancelled)
	}
	afterCancelledRetry, err := query.QueuedPromptState()
	if err != nil || afterCancelledRetry.Revision != cancelledState.Revision || len(afterCancelledRetry.Items) != 0 {
		t.Fatalf("historical cancellation retry mutated queue: before=%#v after=%#v err=%v", cancelledState, afterCancelledRetry, err)
	}
	query.Close()

	reopened := NewQueryEngine(config)
	t.Cleanup(reopened.Close)
	for _, tc := range []struct {
		id       string
		input    UserTurnInput
		enqueued QueuedUserInput
	}{
		{id: settledID, input: settledInput, enqueued: settled},
		{id: cancelledID, input: cancelledInput, enqueued: cancelled},
	} {
		admitted, err := reopened.HasQueuedUserInputAdmission(tc.id, tc.input)
		if err != nil || !admitted {
			t.Fatalf("reopened receipt %s: admitted=%v err=%v", tc.id, admitted, err)
		}
		ack, err := reopened.EnqueueUserInputWithID(tc.id, tc.input)
		if err != nil {
			t.Fatalf("reopened historical ACK %s: %v", tc.id, err)
		}
		if ack.State != RuntimeItemProcessing || ack.EnqueuedAt != tc.enqueued.EnqueuedAt {
			t.Fatalf("reopened historical ACK %s = %#v, first=%#v", tc.id, ack, tc.enqueued)
		}
	}
	if state, err := reopened.QueuedPromptState(); err != nil || len(state.Items) != 0 {
		t.Fatalf("reopened historical receipts created work: state=%#v err=%v", state, err)
	}
	_, err = reopened.EnqueueUserInputWithID(settledID, UserTurnInput{
		Display: "different preview",
		Prompt:  "different prompt",
	})
	var conflict *RuntimeInputConflictError
	if !errors.As(err, &conflict) || conflict.ID != settledID {
		t.Fatalf("reopened receipt conflict = %#v", err)
	}
}
