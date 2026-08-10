package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/abietic/yhc/engine/internal/mediastore"
	"github.com/abietic/yhc/engine/internal/promptrecord"
)

func TestP302cCollectSessionMediaUsesCompleteOwnerReachability(t *testing.T) {
	eng := newP302bTestEngine(
		t,
		"p302c-collect",
		t.TempDir(),
		t.TempDir(),
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)
	ctx := context.Background()
	store := eng.inputCoordinator.mediaStore

	transcriptPNG := p302cLifecyclePNG(t, color.NRGBA{
		R: 0x11,
		G: 0x42,
		B: 0x73,
		A: 0xff,
	})
	transcriptRef, err := store.Put(ctx, transcriptPNG, mediastore.Metadata{
		MIMEType: "image/png",
		Width:    2,
		Height:   2,
		Kind:     "prompt_image",
	})
	if err != nil {
		t.Fatal(err)
	}
	transcriptRecord := promptrecord.Record{
		Version: promptrecord.Version1,
		TurnID:  "transcript-turn",
		Parts: []promptrecord.Part{{
			Kind: promptrecord.PartImage,
			Image: &promptrecord.ImagePart{
				Ref:    transcriptRef,
				Detail: "auto",
			},
		}},
	}
	transcriptMessage, err := transcriptRecord.Materialize(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.transcript.RecordUserPrompt(
		transcriptRecord,
		transcriptMessage,
	); err != nil {
		t.Fatal(err)
	}
	if err := eng.transcript.Flush(); err != nil {
		t.Fatal(err)
	}

	queuePNG := p302cLifecyclePNG(t, color.NRGBA{
		R: 0xa1,
		G: 0xb2,
		B: 0xc3,
		A: 0xff,
	})
	queued, err := eng.EnqueueUserInput(UserTurnInput{
		Prompt: "queued media",
		Images: []UserImage{{
			MIMEType:   "image/png",
			Base64Data: base64.StdEncoding.EncodeToString(queuePNG),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	queueRef := p302cRuntimeMediaRef(t, eng, queued.ID)

	orphanRef, err := store.Put(ctx, transcriptPNG, mediastore.Metadata{
		MIMEType: "image/png",
		Width:    2,
		Height:   2,
		Kind:     "prompt_image",
	})
	if err != nil {
		t.Fatal(err)
	}
	collected, err := eng.CollectSessionMedia(ctx)
	if err != nil {
		t.Fatalf("collect pending reachability: %v", err)
	}
	if collected.ManifestEntriesRemoved != 1 ||
		collected.BlobsRemoved != 0 {
		t.Fatalf("shared-digest collection = %#v", collected)
	}
	if _, err := store.Resolve(ctx, orphanRef); err == nil {
		t.Fatal("orphan manifest entry survived collection")
	}
	for name, ref := range map[string]mediastore.Ref{
		"transcript": transcriptRef,
		"pending":    queueRef,
	} {
		if _, err := store.Resolve(ctx, ref); err != nil {
			t.Fatalf("%s ref was collected: %v", name, err)
		}
	}

	claimed, ok, err := eng.ClaimNextRuntimeItem()
	if err != nil || !ok || claimed.ID != queued.ID {
		t.Fatalf("claim = %#v, %v, %v", claimed, ok, err)
	}
	collected, err = eng.CollectSessionMedia(ctx)
	if err != nil {
		t.Fatalf("collect processing reachability: %v", err)
	}
	if collected != (SessionMediaCollection{}) {
		t.Fatalf("processing collection = %#v", collected)
	}
	if _, err := store.Resolve(ctx, queueRef); err != nil {
		t.Fatalf("processing ref was collected: %v", err)
	}

	if err := eng.inputCoordinator.Settle(claimed.ID); err != nil {
		t.Fatal(err)
	}
	collected, err = eng.CollectSessionMedia(ctx)
	if err != nil {
		t.Fatalf("collect settled reachability: %v", err)
	}
	if collected.ManifestEntriesRemoved != 1 ||
		collected.BlobsRemoved != 1 ||
		collected.BytesRemoved != int64(len(queuePNG)) {
		t.Fatalf("settled collection = %#v", collected)
	}
	if _, err := store.Resolve(ctx, queueRef); err == nil {
		t.Fatal("settled queue ref survived collection")
	}
	if _, err := store.Resolve(ctx, transcriptRef); err != nil {
		t.Fatalf("transcript ref was collected: %v", err)
	}

	eng.Close()
	if _, err := eng.CollectSessionMedia(ctx); err == nil {
		t.Fatal("closed owner accepted collection")
	}
}

func TestP302cPrivateMediaInspectionIncludesRuntimeQueue(t *testing.T) {
	eng := newP302bTestEngine(
		t,
		"p302c-inspect",
		t.TempDir(),
		t.TempDir(),
		&captureInputModel{},
		DefaultPromptCapabilityResolver(),
	)
	private, err := eng.HasPrivateSessionMedia()
	if err != nil || private {
		t.Fatalf("empty private media = %v, %v", private, err)
	}
	queued, err := eng.EnqueueUserInput(UserTurnInput{
		Prompt: "queued media",
		Images: []UserImage{{
			MIMEType:   "image/png",
			Base64Data: testUserImagePNGBase64,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	private, err = eng.HasPrivateSessionMedia()
	if err != nil || !private {
		t.Fatalf("queued private media = %v, %v", private, err)
	}
	if !eng.CancelQueuedUserInput(queued.ID) {
		t.Fatal("cancel queued media")
	}
	private, err = eng.HasPrivateSessionMedia()
	if err != nil || private {
		t.Fatalf("unreachable private media = %v, %v", private, err)
	}
}

func p302cRuntimeMediaRef(
	t *testing.T,
	eng *QueryEngine,
	itemID string,
) mediastore.Ref {
	t.Helper()
	eng.inputCoordinator.mu.Lock()
	defer eng.inputCoordinator.mu.Unlock()
	for _, item := range eng.inputCoordinator.items {
		if item.ID != itemID ||
			item.UserPrompt == nil ||
			item.UserPrompt.durablePrompt == nil {
			continue
		}
		refs, err := item.UserPrompt.durablePrompt.MediaRefs()
		if err != nil {
			t.Fatal(err)
		}
		if len(refs) != 1 {
			t.Fatalf("runtime refs = %#v", refs)
		}
		return refs[0]
	}
	t.Fatalf("runtime media item %q not found", itemID)
	return mediastore.Ref{}
}

func p302cLifecyclePNG(t *testing.T, fill color.NRGBA) []byte {
	t.Helper()
	source := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for y := range 2 {
		for x := range 2 {
			source.SetNRGBA(x, y, fill)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}
