package promptrecord

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/internal/mediastore"
	"github.com/cloudwego/eino/schema"
)

const p305aPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func TestP305aVersion2RoundTripMaterializesTypedOrder(t *testing.T) {
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
	mimeText := "text/plain"
	record := Record{
		Version: Version2,
		TurnID:  "p30.5a-turn",
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
					MIMEType: &mimeText,
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
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), p305aPNGBase64) {
		t.Fatal("record contains inline media")
	}
	var decoded Record
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	message, err := decoded.Materialize(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if message.Role != schema.User ||
		len(message.UserInputMultiContent) != 7 {
		t.Fatalf("materialized message = %#v", message)
	}
	wantTypes := []schema.ChatMessagePartType{
		schema.ChatMessagePartTypeText,
		schema.ChatMessagePartTypeText,
		schema.ChatMessagePartTypeImageURL,
		schema.ChatMessagePartTypeText,
		schema.ChatMessagePartTypeText,
		schema.ChatMessagePartTypeImageURL,
		schema.ChatMessagePartTypeText,
	}
	for index, want := range wantTypes {
		if got := message.UserInputMultiContent[index].Type; got != want {
			t.Fatalf("part %d type = %q, want %q", index, got, want)
		}
	}
	if got := message.UserInputMultiContent[1].Text; !strings.Contains(
		got,
		`"type":"resource_link"`,
	) {
		t.Fatalf("resource descriptor = %q", got)
	}
	if got := message.UserInputMultiContent[3].Text; !strings.Contains(
		got,
		`"kind":"text"`,
	) || !strings.Contains(got, `"text":"embedded"`) {
		t.Fatalf("embedded text envelope = %q", got)
	}
	if got := message.UserInputMultiContent[4].Text; !strings.Contains(
		got,
		`"kind":"blob"`,
	) || strings.Contains(got, "media_id") {
		t.Fatalf("embedded blob envelope = %q", got)
	}
}

func TestP305aVersion1RemainsReadableAndVersion2FailsClosed(t *testing.T) {
	ref := mediastore.Ref{
		Version:   mediastore.RefVersion,
		MediaID:   strings.Repeat("a", 43),
		MIMEType:  "image/png",
		SizeBytes: 1,
		Width:     1,
		Height:    1,
	}
	version1 := Record{
		Version: Version1,
		TurnID:  "legacy-turn",
		Parts: []Part{{
			Kind:  PartImage,
			Image: &ImagePart{Ref: ref, Detail: "auto"},
		}},
	}
	if err := version1.Validate(); err != nil {
		t.Fatalf("version 1 rejected: %v", err)
	}

	overlap := Record{
		Version: Version2,
		TurnID:  "overlap-turn",
		Parts: []Part{{
			Kind: PartResourceLink,
			Text: &TextPart{Text: "must fail"},
			ResourceLink: &ResourceLinkPart{
				URI:  "file:///context",
				Name: "context",
			},
		}},
	}
	if err := overlap.Validate(); err == nil {
		t.Fatal("overlapping version-2 union accepted")
	}

	invalidAnnotations := Record{
		Version: Version2,
		TurnID:  "annotation-turn",
		Parts: []Part{{
			Kind: PartEmbeddedText,
			EmbeddedText: &EmbeddedTextPart{
				URI:         "file:///context",
				Text:        "context",
				Annotations: &Annotations{Audience: []string{"unknown"}},
			},
		}},
	}
	if err := invalidAnnotations.Validate(); err == nil {
		t.Fatal("invalid standard annotation accepted")
	}
}

func stringPointer(value string) *string {
	return &value
}
