package recovery

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/internal/mediastore"
	"github.com/abietic/yhc/engine/internal/promptrecord"
)

func TestP303MediaRecoveryOmitsOnlyExactHistoricalPartsInPosition(
	t *testing.T,
) {
	historical := p303PromptMessage(
		t,
		"before",
		"after",
		PromptImageFixture{MIME: "image/png", Detail: "high"},
	)
	current := p303PromptMessage(
		t,
		"inspect",
		"now",
		PromptImageFixture{MIME: "image/png", Detail: "low"},
	)
	currentBinding, err := BindCurrentTurn("turn-current", current)
	if err != nil {
		t.Fatal(err)
	}
	record := p303PromptRecord(
		"turn-historical",
		"before",
		"after",
		"image/png",
		"high",
	)
	originalHistorical := *historical.UserInputMultiContent[1].Image.Base64Data
	originalCurrent := *current.UserInputMultiContent[1].Image.Base64Data

	candidate, err := BuildMediaCandidate(
		context.Background(),
		[]*schema.Message{
			historical,
			{Role: schema.Assistant, Content: "answer"},
			current,
		},
		currentBinding,
		[]BoundPromptRecord{{Message: historical, Record: record}},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ClearProviderMessages(candidate.ProviderMessages)
	if candidate.OmittedImageCount != 1 ||
		candidate.OmittedTurnCount != 1 ||
		candidate.CanonicalMessages[0] == historical ||
		candidate.CanonicalMessages[2] != current {
		t.Fatalf("candidate = %#v", candidate)
	}
	parts := candidate.CanonicalMessages[0].UserInputMultiContent
	if len(parts) != 3 ||
		parts[0].Text != "before" ||
		parts[1].Type != schema.ChatMessagePartTypeText ||
		parts[1].Text !=
			"[historical image omitted during media-size recovery: mime=image/png detail=high]" ||
		parts[2].Text != "after" {
		t.Fatalf("historical projection = %#v", parts)
	}
	if candidate.ProviderMessages[2] == current ||
		candidate.ProviderMessages[2].UserInputMultiContent[1].Image ==
			current.UserInputMultiContent[1].Image {
		t.Fatal("provider attempt aliases the canonical current message")
	}
	if *historical.UserInputMultiContent[1].Image.Base64Data != originalHistorical ||
		*current.UserInputMultiContent[1].Image.Base64Data != originalCurrent {
		t.Fatal("recovery mutated source messages")
	}
}

func TestP305aMediaRecoveryPreservesEmbeddedMetadataAndOmitsBlobImage(
	t *testing.T,
) {
	blobRef := mediastore.Ref{
		Version:   mediastore.RefVersion,
		MediaID:   strings.Repeat("B", 43),
		MIMEType:  "image/png",
		SizeBytes: 1024,
		Width:     64,
		Height:    64,
	}
	resource := promptrecord.ResourceLinkPart{
		URI:  "file:///reference.txt",
		Name: "reference.txt",
	}
	embeddedText := promptrecord.EmbeddedTextPart{
		URI:  "file:///notes.txt",
		Text: "embedded notes",
	}
	embeddedBlob := promptrecord.EmbeddedBlobPart{
		URI:      "file:///diagram.png",
		MIMEType: "image/png",
		Ref:      blobRef,
		Detail:   "auto",
	}
	resourceText, err := promptrecord.RenderResourceLink(resource)
	if err != nil {
		t.Fatal(err)
	}
	embeddedTextEnvelope, err := promptrecord.RenderEmbeddedText(embeddedText)
	if err != nil {
		t.Fatal(err)
	}
	embeddedBlobEnvelope, err := promptrecord.RenderEmbeddedBlob(embeddedBlob)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(p303PNG(t))
	historical := &schema.Message{
		Role: schema.User,
		Content: resourceText +
			embeddedTextEnvelope +
			embeddedBlobEnvelope,
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: resourceText},
			{Type: schema.ChatMessagePartTypeText, Text: embeddedTextEnvelope},
			{Type: schema.ChatMessagePartTypeText, Text: embeddedBlobEnvelope},
			{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{
						Base64Data: &encoded,
						MIMEType:   "image/png",
					},
					Detail: schema.ImageURLDetail("auto"),
				},
			},
		},
	}
	record := promptrecord.Record{
		Version: promptrecord.Version2,
		TurnID:  "turn-historical",
		Parts: []promptrecord.Part{
			{
				Kind:         promptrecord.PartResourceLink,
				ResourceLink: &resource,
			},
			{
				Kind:         promptrecord.PartEmbeddedText,
				EmbeddedText: &embeddedText,
			},
			{
				Kind:         promptrecord.PartEmbeddedBlob,
				EmbeddedBlob: &embeddedBlob,
			},
		},
	}
	current := p303PromptMessage(
		t,
		"inspect",
		"now",
		PromptImageFixture{MIME: "image/png", Detail: "low"},
	)
	currentBinding, err := BindCurrentTurn("turn-current", current)
	if err != nil {
		t.Fatal(err)
	}

	candidate, err := BuildMediaCandidate(
		context.Background(),
		[]*schema.Message{historical, current},
		currentBinding,
		[]BoundPromptRecord{{Message: historical, Record: record}},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ClearProviderMessages(candidate.ProviderMessages)
	if candidate.OmittedImageCount != 1 ||
		candidate.OmittedTurnCount != 1 {
		t.Fatalf("candidate counts = %#v", candidate)
	}
	parts := candidate.CanonicalMessages[0].UserInputMultiContent
	if len(parts) != 4 ||
		parts[0].Text != resourceText ||
		parts[1].Text != embeddedTextEnvelope ||
		parts[2].Text != embeddedBlobEnvelope ||
		parts[3].Text !=
			"[historical image omitted during media-size recovery: mime=image/png detail=auto]" {
		t.Fatalf("historical projection = %#v", parts)
	}
	if historical.UserInputMultiContent[3].Type !=
		schema.ChatMessagePartTypeImageURL ||
		*historical.UserInputMultiContent[3].Image.Base64Data != encoded {
		t.Fatal("recovery mutated the canonical embedded blob")
	}
}

func TestP303MediaRecoveryFailsClosedForUnprovedAndReorderedHistory(
	t *testing.T,
) {
	first := p303PromptMessage(
		t,
		"a",
		"b",
		PromptImageFixture{MIME: "image/png", Detail: "auto"},
	)
	second := p303PromptMessage(
		t,
		"c",
		"d",
		PromptImageFixture{MIME: "image/png", Detail: "auto"},
	)
	current := p303PromptMessage(
		t,
		"e",
		"f",
		PromptImageFixture{MIME: "image/gif", Detail: "auto"},
	)
	current.UserInputMultiContent[1].Image.MIMEType = "image/gif"
	gif := "R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw=="
	current.UserInputMultiContent[1].Image.Base64Data = &gif
	binding, err := BindCurrentTurn("turn-current", current)
	if err != nil {
		t.Fatal(err)
	}
	messages := []*schema.Message{first, second, current}
	records := []BoundPromptRecord{
		{
			Message: second,
			Record: p303PromptRecord(
				"turn-second",
				"c",
				"d",
				"image/png",
				"auto",
			),
		},
		{
			Message: first,
			Record: p303PromptRecord(
				"turn-first",
				"a",
				"b",
				"image/png",
				"auto",
			),
		},
	}
	candidate, err := BuildMediaCandidate(
		context.Background(),
		messages,
		binding,
		records,
	)
	if err == nil || candidate != nil {
		t.Fatalf("reordered unproved history recovered: %#v, %v", candidate, err)
	}
	for index, message := range messages[:2] {
		if message.UserInputMultiContent[1].Type !=
			schema.ChatMessagePartTypeImageURL {
			t.Fatalf("history %d was mutated", index)
		}
	}
}

func TestP303MediaRecoveryRejectsDuplicateTurnAndCurrentMismatch(t *testing.T) {
	historicalOne := p303PromptMessage(
		t,
		"a",
		"b",
		PromptImageFixture{MIME: "image/png", Detail: "auto"},
	)
	historicalTwo := p303PromptMessage(
		t,
		"c",
		"d",
		PromptImageFixture{MIME: "image/png", Detail: "auto"},
	)
	current := p303PromptMessage(
		t,
		"e",
		"f",
		PromptImageFixture{MIME: "image/png", Detail: "auto"},
	)
	binding, err := BindCurrentTurn("turn-current", current)
	if err != nil {
		t.Fatal(err)
	}
	records := []BoundPromptRecord{
		{
			Message: historicalOne,
			Record: p303PromptRecord(
				"turn-duplicate",
				"a",
				"b",
				"image/png",
				"auto",
			),
		},
		{
			Message: historicalTwo,
			Record: p303PromptRecord(
				"turn-duplicate",
				"c",
				"d",
				"image/png",
				"auto",
			),
		},
		{
			Message: current,
			Record: p303PromptRecord(
				"wrong-current",
				"e",
				"f",
				"image/png",
				"auto",
			),
		},
	}
	candidate, err := BuildMediaCandidate(
		context.Background(),
		[]*schema.Message{historicalOne, historicalTwo, current},
		binding,
		records,
	)
	if err == nil || candidate != nil ||
		!strings.Contains(err.Error(), "current_turn_mismatch") {
		t.Fatalf("mismatched current record = %#v, %v", candidate, err)
	}
}

func TestP303MediaRecoveryLeavesLegacyInlineHistoryUnchanged(t *testing.T) {
	proved := p303PromptMessage(
		t,
		"proved-before",
		"proved-after",
		PromptImageFixture{MIME: "image/png", Detail: "auto"},
	)
	legacy := p303PromptMessage(
		t,
		"legacy-before",
		"legacy-after",
		PromptImageFixture{MIME: "image/png", Detail: "auto"},
	)
	current := p303PromptMessage(
		t,
		"current-before",
		"current-after",
		PromptImageFixture{MIME: "image/png", Detail: "auto"},
	)
	binding, err := BindCurrentTurn("turn-current", current)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := BuildMediaCandidate(
		context.Background(),
		[]*schema.Message{proved, legacy, current},
		binding,
		[]BoundPromptRecord{{
			Message: proved,
			Record: p303PromptRecord(
				"turn-proved",
				"proved-before",
				"proved-after",
				"image/png",
				"auto",
			),
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ClearProviderMessages(candidate.ProviderMessages)
	if candidate.OmittedImageCount != 1 ||
		candidate.CanonicalMessages[0] == proved ||
		candidate.CanonicalMessages[1] != legacy ||
		candidate.CanonicalMessages[1].UserInputMultiContent[1].Type !=
			schema.ChatMessagePartTypeImageURL {
		t.Fatalf("mixed legacy projection = %#v", candidate)
	}
}

func TestP303MediaRecoveryBoundaryAndTerminalAreRedacted(t *testing.T) {
	boundary := BoundaryMessage(2, 1)
	attachment := AttachmentMessage(MediaStageSelected, 2, 1, false)
	terminal := TerminalMessage(MediaStageFallback)
	serialized := strings.Join(
		[]string{
			boundary.Content,
			attachment.Content,
			terminal.Content,
			terminal.Extra["stage"].(string),
		},
		" ",
	)
	for _, forbidden := range []string{
		"turn-",
		"media_id",
		"digest",
		"base64",
		"/tmp/",
		"provider body",
	} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("redacted projection contains %q: %s", forbidden, serialized)
		}
	}
	if boundary.Extra["omitted_image_count"] != 2 ||
		boundary.Extra["omitted_turn_count"] != 1 ||
		attachment.Extra["attachment_kind"] != "media_recovery" ||
		terminal.Extra["error_type"] != "media_size" {
		t.Fatalf(
			"boundary=%#v attachment=%#v terminal=%#v",
			boundary,
			attachment,
			terminal,
		)
	}
}

type PromptImageFixture struct {
	MIME   string
	Detail string
}

func p303PromptMessage(
	t *testing.T,
	before string,
	after string,
	imagePart PromptImageFixture,
) *schema.Message {
	t.Helper()
	data := p303PNG(t)
	encoded := base64.StdEncoding.EncodeToString(data)
	return &schema.Message{
		Role:    schema.User,
		Content: before + after,
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: before},
			{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{
						Base64Data: &encoded,
						MIMEType:   imagePart.MIME,
					},
					Detail: schema.ImageURLDetail(imagePart.Detail),
				},
			},
			{Type: schema.ChatMessagePartTypeText, Text: after},
		},
	}
}

func p303PromptRecord(
	turnID string,
	before string,
	after string,
	mimeType string,
	detail string,
) promptrecord.Record {
	return promptrecord.Record{
		Version: promptrecord.Version1,
		TurnID:  turnID,
		Parts: []promptrecord.Part{
			{
				Kind: promptrecord.PartText,
				Text: &promptrecord.TextPart{Text: before},
			},
			{
				Kind: promptrecord.PartImage,
				Image: &promptrecord.ImagePart{
					Ref: mediastore.Ref{
						Version:   mediastore.RefVersion,
						MediaID:   strings.Repeat("A", 43),
						MIMEType:  mimeType,
						SizeBytes: 1024,
						Width:     64,
						Height:    64,
					},
					Detail: detail,
				},
			},
			{
				Kind: promptrecord.PartText,
				Text: &promptrecord.TextPart{Text: after},
			},
		},
	}
}

func p303PNG(t *testing.T) []byte {
	t.Helper()
	source := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			source.SetNRGBA(x, y, color.NRGBA{
				R: uint8(x * 3),
				G: uint8(y * 3),
				B: uint8(x + y),
				A: 0xff,
			})
		}
	}
	var encoded bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.NoCompression}
	if err := encoder.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}
