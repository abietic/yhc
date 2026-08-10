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
