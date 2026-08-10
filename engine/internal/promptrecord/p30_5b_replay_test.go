package promptrecord

import (
	"encoding/base64"
	"path/filepath"
	"testing"

	"github.com/abietic/yhc/engine/internal/mediastore"
)

func TestP305bReplayPartsUseExactRecordBinding(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString(p305aPNGBase64)
	if err != nil {
		t.Fatal(err)
	}
	store := mediastore.New(filepath.Join(t.TempDir(), "session.jsonl.media"))
	imageRef, err := store.Put(t.Context(), data, mediastore.Metadata{
		MIMEType: "image/png",
		Width:    1,
		Height:   1,
		Kind:     "prompt_image",
	})
	if err != nil {
		t.Fatal(err)
	}
	blobRef, err := store.Put(t.Context(), data, mediastore.Metadata{
		MIMEType: "image/png",
		Width:    1,
		Height:   1,
		Kind:     "prompt_image",
	})
	if err != nil {
		t.Fatal(err)
	}
	lastModified := "2026-07-30T00:00:00Z"
	priority := 0.5
	textMIME := "text/plain"
	record := Record{
		Version: Version2,
		TurnID:  "p30.5b-replay",
		Parts: []Part{
			{Kind: PartText, Text: &TextPart{Text: "before"}},
			{
				Kind: PartResourceLink,
				ResourceLink: &ResourceLinkPart{
					URI:      "file:///workspace/schema.json",
					Name:     "schema.json",
					MIMEType: stringPointer("application/json"),
					Annotations: &Annotations{
						Audience:     []string{"assistant"},
						LastModified: &lastModified,
						Priority:     &priority,
					},
				},
			},
			{
				Kind: PartImage,
				Image: &ImagePart{
					Ref:         imageRef,
					Detail:      "auto",
					Annotations: &Annotations{Audience: []string{"user"}},
				},
			},
			{
				Kind: PartEmbeddedText,
				EmbeddedText: &EmbeddedTextPart{
					URI:      "file:///workspace/context.txt",
					MIMEType: &textMIME,
					Text:     "embedded",
				},
			},
			{
				Kind: PartEmbeddedBlob,
				EmbeddedBlob: &EmbeddedBlobPart{
					URI:      "file:///workspace/pixel.png",
					MIMEType: "image/png",
					Ref:      blobRef,
					Detail:   "auto",
				},
			},
			{Kind: PartText, Text: &TextPart{Text: "after"}},
		},
	}
	message, err := record.Materialize(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}

	parts, err := record.ReplayParts(message)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 6 {
		t.Fatalf("replay parts = %d, want 6", len(parts))
	}
	wantKinds := []string{
		PartText,
		PartResourceLink,
		PartImage,
		PartEmbeddedText,
		PartEmbeddedBlob,
		PartText,
	}
	for index, want := range wantKinds {
		if parts[index].Kind != want {
			t.Fatalf("part %d kind = %q, want %q", index, parts[index].Kind, want)
		}
	}
	if parts[0].Text == nil || parts[0].Text.Text != "before" ||
		parts[1].ResourceLink == nil ||
		parts[1].ResourceLink.URI != "file:///workspace/schema.json" ||
		parts[1].ResourceLink.Annotations == nil ||
		parts[1].ResourceLink.Annotations.LastModified == nil ||
		*parts[1].ResourceLink.Annotations.LastModified != lastModified ||
		parts[2].Image == nil ||
		parts[2].Image.Data != p305aPNGBase64 ||
		parts[2].Image.MIMEType != "image/png" ||
		parts[3].EmbeddedText == nil ||
		parts[3].EmbeddedText.Text != "embedded" ||
		parts[4].EmbeddedBlob == nil ||
		parts[4].EmbeddedBlob.Data != p305aPNGBase64 ||
		parts[4].EmbeddedBlob.URI != "file:///workspace/pixel.png" ||
		parts[5].Text == nil ||
		parts[5].Text.Text != "after" {
		t.Fatalf("replay parts = %#v", parts)
	}
	if parts[4].EmbeddedBlob.Data == message.UserInputMultiContent[4].Text {
		t.Fatal("embedded blob replay used the provider metadata envelope")
	}

	parts[1].ResourceLink.Annotations.Audience[0] = "mutated"
	second, err := record.ReplayParts(message)
	if err != nil {
		t.Fatal(err)
	}
	if second[1].ResourceLink.Annotations.Audience[0] != "assistant" {
		t.Fatal("caller mutation escaped into a later replay projection")
	}

	message.UserInputMultiContent[1].Text = "provider shape drift"
	if projected, err := record.ReplayParts(message); err == nil || projected != nil {
		t.Fatalf("drifted projection = %#v, err = %v", projected, err)
	}
}

func TestP305bReplayPartsRetainVersion1TextAndImage(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString(p305aPNGBase64)
	if err != nil {
		t.Fatal(err)
	}
	store := mediastore.New(filepath.Join(t.TempDir(), "session.jsonl.media"))
	ref, err := store.Put(t.Context(), data, mediastore.Metadata{
		MIMEType: "image/png",
		Width:    1,
		Height:   1,
		Kind:     "prompt_image",
	})
	if err != nil {
		t.Fatal(err)
	}
	record := Record{
		Version: Version1,
		TurnID:  "p30.5b-version-1",
		Parts: []Part{
			{Kind: PartText, Text: &TextPart{Text: "before"}},
			{
				Kind: PartImage,
				Image: &ImagePart{
					Ref:    ref,
					Detail: "low",
				},
			},
			{Kind: PartText, Text: &TextPart{Text: "after"}},
		},
	}
	message, err := record.Materialize(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	parts, err := record.ReplayParts(message)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 ||
		parts[0].Kind != PartText ||
		parts[0].Text == nil ||
		parts[0].Text.Text != "before" ||
		parts[1].Kind != PartImage ||
		parts[1].Image == nil ||
		parts[1].Image.Data != p305aPNGBase64 ||
		parts[1].Image.MIMEType != "image/png" ||
		parts[2].Kind != PartText ||
		parts[2].Text == nil ||
		parts[2].Text.Text != "after" {
		t.Fatalf("version-1 replay parts = %#v", parts)
	}
}
