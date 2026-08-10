package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/internal/mediastore"
	"github.com/abietic/yhc/engine/internal/promptrecord"
	"github.com/abietic/yhc/engine/transcript"
)

func TestP305aRichBranchExportAndDeleteLifecycle(t *testing.T) {
	dir := t.TempDir()
	sourcePath, sourceRecord := createP305aRichSession(
		t,
		dir,
		"p305a-source",
	)
	branch, err := BranchSession(BranchOptions{
		SourceSessionID: "p305a-source",
		MessageIndex:    1,
		NewSessionID:    "p305a-child",
		OperationID:     "p305a-rich-branch",
		Dir:             dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if branch == nil || branch.MessagesCopied != 1 {
		t.Fatalf("branch = %#v", branch)
	}

	childProjection, err := transcript.NewRecorder(
		"p305a-child",
		dir,
	).LoadRefProjection()
	if err != nil {
		t.Fatal(err)
	}
	if len(childProjection.PromptRecords) != 1 {
		t.Fatalf("child prompt records = %#v", childProjection.PromptRecords)
	}
	childRecord := childProjection.PromptRecords[0].Record
	if childRecord.Version != promptrecord.Version2 ||
		len(childRecord.Parts) != len(sourceRecord.Parts) {
		t.Fatalf("child record = %#v", childRecord)
	}
	wantKinds := []string{
		promptrecord.PartText,
		promptrecord.PartResourceLink,
		promptrecord.PartImage,
		promptrecord.PartEmbeddedText,
		promptrecord.PartEmbeddedBlob,
		promptrecord.PartText,
	}
	for index, want := range wantKinds {
		if childRecord.Parts[index].Kind != want {
			t.Fatalf(
				"child part %d kind = %q, want %q",
				index,
				childRecord.Parts[index].Kind,
				want,
			)
		}
	}
	if childRecord.Parts[1].ResourceLink == nil ||
		childRecord.Parts[1].ResourceLink.URI !=
			sourceRecord.Parts[1].ResourceLink.URI ||
		childRecord.Parts[3].EmbeddedText == nil ||
		childRecord.Parts[3].EmbeddedText.Text !=
			sourceRecord.Parts[3].EmbeddedText.Text ||
		childRecord.Parts[4].EmbeddedBlob == nil ||
		childRecord.Parts[4].EmbeddedBlob.URI !=
			sourceRecord.Parts[4].EmbeddedBlob.URI {
		t.Fatalf("typed metadata changed during branch: %#v", childRecord)
	}
	sourceRefs, err := sourceRecord.MediaRefs()
	if err != nil {
		t.Fatal(err)
	}
	childRefs, err := childRecord.MediaRefs()
	if err != nil {
		t.Fatal(err)
	}
	if len(sourceRefs) != 2 || len(childRefs) != 2 {
		t.Fatalf("source refs=%#v child refs=%#v", sourceRefs, childRefs)
	}
	for index := range sourceRefs {
		if sourceRefs[index].MediaID == childRefs[index].MediaID {
			t.Fatalf("branch reused private media identity at %d", index)
		}
	}

	exported, err := ExportSession(ExportOptions{
		SessionID: "p305a-child",
		Dir:       dir,
		Format:    ExportJSON,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"file:///workspace/schema.json",
		"file:///workspace/context.txt",
		"file:///workspace/pixel.png",
		"embedded context",
		`"media_id"`,
		`"digest"`,
		`"base64_data"`,
	} {
		if strings.Contains(exported.Content, forbidden) {
			t.Fatalf("JSON export leaked %q", forbidden)
		}
	}
	var decoded ExportedSession
	if err := json.Unmarshal([]byte(exported.Content), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Messages) != 1 ||
		decoded.Messages[0].Content != "headtail" ||
		len(decoded.Messages[0].Parts) != len(wantKinds) {
		t.Fatalf("JSON export = %#v", decoded)
	}
	for index, want := range wantKinds {
		if decoded.Messages[0].Parts[index].Kind != want {
			t.Fatalf(
				"export part %d kind = %q, want %q",
				index,
				decoded.Messages[0].Parts[index].Kind,
				want,
			)
		}
	}

	markdown, err := ExportSession(ExportOptions{
		SessionID: "p305a-child",
		Dir:       dir,
		Format:    ExportMarkdown,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"[resource_link: application/json]",
		"[embedded_text: text/plain]",
		"[embedded_blob: image/png",
	} {
		if !strings.Contains(markdown.Content, required) {
			t.Fatalf("Markdown export missing %q: %s", required, markdown.Content)
		}
	}
	for _, forbidden := range []string{
		"file:///workspace/",
		"embedded context",
		"media_id",
		"digest",
		"base64_data",
	} {
		if strings.Contains(markdown.Content, forbidden) {
			t.Fatalf("Markdown export leaked %q", forbidden)
		}
	}

	if _, err := DeleteSession(DeleteOptions{
		SessionID: "p305a-source",
		Dir:       dir,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(sourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source transcript remains: %v", err)
	}
	childLoaded, err := transcript.NewRecorder(
		"p305a-child",
		dir,
	).LoadFull()
	if err != nil || len(childLoaded.Messages) != 1 ||
		len(childLoaded.Messages[0].UserInputMultiContent) != 7 {
		t.Fatalf("child after source delete = %#v, %v", childLoaded, err)
	}
	if _, err := DeleteSession(DeleteOptions{
		SessionID: "p305a-child",
		Dir:       dir,
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		branch.TranscriptPath,
		branch.TranscriptPath + mediaSidecarSuffix,
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("deleted child path remains: %s: %v", path, err)
		}
	}
}

func createP305aRichSession(
	t *testing.T,
	dir string,
	sessionID string,
) (string, promptrecord.Record) {
	t.Helper()
	recorder := transcript.NewRecorder(sessionID, dir)
	store := mediastore.New(recorder.Path() + mediaSidecarSuffix)
	data := p302aSessionPNG(t)
	imageRef, err := store.Put(context.Background(), data, mediastore.Metadata{
		MIMEType: "image/png",
		Width:    12,
		Height:   12,
		Kind:     "prompt_image",
	})
	if err != nil {
		t.Fatal(err)
	}
	blobRef, err := store.Put(context.Background(), data, mediastore.Metadata{
		MIMEType: "image/png",
		Width:    12,
		Height:   12,
		Kind:     "prompt_image",
	})
	if err != nil {
		t.Fatal(err)
	}
	resourceMIME := "application/json"
	textMIME := "text/plain"
	record := promptrecord.Record{
		Version: promptrecord.Version2,
		TurnID:  "turn-" + sessionID,
		Parts: []promptrecord.Part{
			{
				Kind: promptrecord.PartText,
				Text: &promptrecord.TextPart{Text: "head"},
			},
			{
				Kind: promptrecord.PartResourceLink,
				ResourceLink: &promptrecord.ResourceLinkPart{
					URI:      "file:///workspace/schema.json",
					Name:     "schema.json",
					MIMEType: &resourceMIME,
				},
			},
			{
				Kind: promptrecord.PartImage,
				Image: &promptrecord.ImagePart{
					Ref:    imageRef,
					Detail: "low",
				},
			},
			{
				Kind: promptrecord.PartEmbeddedText,
				EmbeddedText: &promptrecord.EmbeddedTextPart{
					URI:      "file:///workspace/context.txt",
					MIMEType: &textMIME,
					Text:     "embedded context",
				},
			},
			{
				Kind: promptrecord.PartEmbeddedBlob,
				EmbeddedBlob: &promptrecord.EmbeddedBlobPart{
					URI:      "file:///workspace/pixel.png",
					MIMEType: "image/png",
					Ref:      blobRef,
					Detail:   "high",
				},
			},
			{
				Kind: promptrecord.PartText,
				Text: &promptrecord.TextPart{Text: "tail"},
			},
		},
	}
	message, err := record.Materialize(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordUserPrompt(record, message); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMessages([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "answer",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	return recorder.Path(), record
}
