package provider

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
)

type captureAgenticInputModel struct {
	generateCalls int
	streamCalls   int
	generated     []*schema.AgenticMessage
	streamed      []*schema.AgenticMessage
}

func (m *captureAgenticInputModel) Generate(_ context.Context, input []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
	m.generateCalls++
	m.generated = input
	return &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant}, nil
}

func (m *captureAgenticInputModel) Stream(_ context.Context, input []*schema.AgenticMessage, _ ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	m.streamCalls++
	m.streamed = input
	return schema.StreamReaderFromArray([]*schema.AgenticMessage{{Role: schema.AgenticRoleTypeAssistant}}), nil
}

func TestMessagesToAgenticUserInputParts(t *testing.T) {
	url := "https://example.invalid/image?q=secret"
	mediaURL := "https://example.invalid/media"
	data := "cGF5bG9hZA=="
	input := []*schema.Message{
		{Role: schema.User, Content: "ignored", Extra: map[string]any{"message-secret": "x"}, UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "first", Extra: map[string]any{"part-secret": "x"}},
			{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &url, Extra: map[string]any{"media-secret": "x"}}, Detail: schema.ImageURLDetailHigh}},
			{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: "image/png"}}},
			{Type: schema.ChatMessagePartTypeAudioURL, Audio: &schema.MessageInputAudio{MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: "audio/wav"}}},
			{Type: schema.ChatMessagePartTypeAudioURL, Audio: &schema.MessageInputAudio{MessagePartCommon: schema.MessagePartCommon{URL: &mediaURL}}},
			{Type: schema.ChatMessagePartTypeVideoURL, Video: &schema.MessageInputVideo{MessagePartCommon: schema.MessagePartCommon{URL: &mediaURL}}},
			{Type: schema.ChatMessagePartTypeVideoURL, Video: &schema.MessageInputVideo{MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: "video/mp4"}}},
			{Type: schema.ChatMessagePartTypeFileURL, File: &schema.MessageInputFile{MessagePartCommon: schema.MessagePartCommon{URL: &mediaURL}, Name: "url.txt"}},
			{Type: schema.ChatMessagePartTypeFileURL, File: &schema.MessageInputFile{MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: "application/pdf"}, Name: "a.pdf"}},
		}},
		{Role: schema.User, Content: "second"},
	}
	inner := &captureAgenticInputModel{}
	wrapped := wrapAgenticModel(inner)
	if _, err := wrapped.Generate(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Stream(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	out := inner.generated
	if inner.generateCalls != 1 || inner.streamCalls != 1 ||
		len(out) != 2 || len(out[0].ContentBlocks) != 9 ||
		!reflect.DeepEqual(out, inner.streamed) {
		t.Fatalf("output = %#v", out)
	}
	blocks := out[0].ContentBlocks
	if out[0].Extra != nil ||
		blocks[0].Type != schema.ContentBlockTypeUserInputText ||
		blocks[0].UserInputText.Text != "first" ||
		blocks[0].Extra != nil {
		t.Fatalf("text = %#v", blocks[0])
	}
	if blocks[1].Type != schema.ContentBlockTypeUserInputImage || blocks[1].UserInputImage.URL != url || blocks[1].UserInputImage.Detail != schema.ImageURLDetailHigh || blocks[1].Extra != nil {
		t.Fatalf("image = %#v", blocks[1])
	}
	if blocks[2].Type != schema.ContentBlockTypeUserInputImage ||
		blocks[2].UserInputImage.Base64Data != data ||
		blocks[2].UserInputImage.MIMEType != "image/png" {
		t.Fatalf("base64 image = %#v", blocks[2])
	}
	if blocks[3].Type != schema.ContentBlockTypeUserInputAudio ||
		blocks[3].UserInputAudio.Base64Data != data ||
		blocks[3].UserInputAudio.MIMEType != "audio/wav" {
		t.Fatalf("audio = %#v", blocks[3])
	}
	if blocks[4].UserInputAudio.URL != mediaURL ||
		blocks[5].UserInputVideo.URL != mediaURL ||
		blocks[6].UserInputVideo.Base64Data != data ||
		blocks[7].UserInputFile.URL != mediaURL {
		t.Fatalf("url/base64 media = %#v", blocks)
	}
	if blocks[8].Type != schema.ContentBlockTypeUserInputFile ||
		blocks[8].UserInputFile.Base64Data != data ||
		blocks[8].UserInputFile.MIMEType != "application/pdf" ||
		blocks[8].UserInputFile.Name != "a.pdf" {
		t.Fatalf("file = %#v", blocks[8])
	}
	if out[1].ContentBlocks[0].UserInputText == nil || out[1].ContentBlocks[0].UserInputText.Text != "second" {
		t.Fatalf("plain = %#v", out[1])
	}
}

func TestMessagesToAgenticSkipsHistoricalUnsupportedAssistantOutput(t *testing.T) {
	message := &schema.Message{Role: schema.Assistant, AssistantGenMultiContent: []schema.MessageOutputPart{{Type: schema.ChatMessagePartTypeImageURL}}}
	out, err := messagesToAgentic([]*schema.Message{message})
	if err != nil || len(out) != 1 || len(out[0].ContentBlocks) != 0 {
		t.Fatalf("out=%#v err=%v", out, err)
	}
}

func TestMessagesToAgenticPreservesEmptyMultipartText(t *testing.T) {
	out, err := messagesToAgentic([]*schema.Message{{
		Role:    schema.User,
		Content: "must not be appended",
		UserInputMultiContent: []schema.MessageInputPart{{
			Type: schema.ChatMessagePartTypeText,
			Text: "",
		}},
	}})
	if err != nil ||
		len(out) != 1 ||
		len(out[0].ContentBlocks) != 1 ||
		out[0].ContentBlocks[0].Type != schema.ContentBlockTypeUserInputText ||
		out[0].ContentBlocks[0].UserInputText == nil ||
		out[0].ContentBlocks[0].UserInputText.Text != "" {
		t.Fatalf("out=%#v err=%v", out, err)
	}
}

func TestMessagesToAgenticPreservesUnknownRoleTextFallback(t *testing.T) {
	message := &schema.Message{
		Role:    schema.RoleType("legacy-role"),
		Content: "legacy text",
		UserInputMultiContent: []schema.MessageInputPart{{
			Type: schema.ChatMessagePartType("legacy-part"),
		}},
	}
	out, err := messagesToAgentic([]*schema.Message{message})
	if err != nil ||
		len(out) != 1 ||
		len(out[0].ContentBlocks) != 1 ||
		out[0].ContentBlocks[0].UserInputText == nil ||
		out[0].ContentBlocks[0].UserInputText.Text != "legacy text" {
		t.Fatalf("out=%#v err=%v", out, err)
	}
}

func TestMessagesToAgenticRejectsInvalidUserInput(t *testing.T) {
	data, empty := "abc", ""
	secretURL := "https://example.invalid/file?token=url-secret#fragment-secret"
	secretData := "base64-secret"
	cases := []struct {
		name, code    string
		messages      []*schema.Message
		message, part int
	}{
		{"nil message", "nil_message", []*schema.Message{nil}, 0, -1},
		{"nil image", "nil_part_payload", []*schema.Message{{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeImageURL}}}}, 0, 0},
		{"nil audio", "nil_part_payload", []*schema.Message{{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeAudioURL}}}}, 0, 0},
		{"nil video", "nil_part_payload", []*schema.Message{{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeVideoURL}}}}, 0, 0},
		{"nil file", "nil_part_payload", []*schema.Message{{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeFileURL}}}}, 0, 0},
		{"text with media", "mismatched_part_payload", []*schema.Message{{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeText, Text: "text-secret", Image: &schema.MessageInputImage{}}}}}, 0, 0},
		{"media with text", "mismatched_part_payload", []*schema.Message{{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeImageURL, Text: "text-secret", Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: "image/png"}}}}}}, 0, 0},
		{"cross media", "mismatched_part_payload", []*schema.Message{{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: "image/png"}}, Audio: &schema.MessageInputAudio{}}}}}, 0, 0},
		{"ambiguous", "media_source_ambiguous", []*schema.Message{{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &empty, Base64Data: &data, MIMEType: "image/png"}}}}}}, 0, 0},
		{"redacted ambiguous file", "media_source_ambiguous", []*schema.Message{{Role: schema.User, Extra: map[string]any{"message-secret": "x"}, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeFileURL, File: &schema.MessageInputFile{MessagePartCommon: schema.MessagePartCommon{URL: &secretURL, Base64Data: &secretData, MIMEType: "application/pdf", Extra: map[string]any{"media-secret": "x"}}, Name: "name-secret"}, Extra: map[string]any{"part-secret": "x"}}}}}, 0, 0},
		{"missing", "media_source_missing", []*schema.Message{{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeVideoURL, Video: &schema.MessageInputVideo{}}}}}, 0, 0},
		{"empty", "media_source_missing", []*schema.Message{{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeFileURL, File: &schema.MessageInputFile{MessagePartCommon: schema.MessagePartCommon{URL: &empty}}}}}}, 0, 0},
		{"mime", "media_mime_type_missing", []*schema.Message{{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeAudioURL, Audio: &schema.MessageInputAudio{MessagePartCommon: schema.MessagePartCommon{Base64Data: &data}}}}}}, 0, 0},
		{"blank mime", "media_mime_type_missing", []*schema.Message{{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeVideoURL, Video: &schema.MessageInputVideo{MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: " \t "}}}}}}, 0, 0},
		{"reasoning", "unsupported_part_type", []*schema.Message{{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeReasoning}}}}, 0, 0},
		{"tool search", "unsupported_part_type", []*schema.Message{{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeToolSearchResult}}}}, 0, 0},
		{"unknown", "unknown_part_type", []*schema.Message{{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartType("type-secret")}}}}, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := &captureAgenticInputModel{}
			wrapped := wrapAgenticModel(inner)
			_, err := wrapped.Generate(context.Background(), tc.messages)
			assertAgenticInputConversionError(t, err, tc)
			if inner.generateCalls != 0 {
				t.Fatalf("generate calls = %d", inner.generateCalls)
			}
			_, streamErr := wrapped.Stream(context.Background(), tc.messages)
			assertAgenticInputConversionError(t, streamErr, tc)
			if inner.streamCalls != 0 {
				t.Fatalf("stream err=%v calls=%d", streamErr, inner.streamCalls)
			}
			if strings.Contains(err.Error(), "abc") ||
				strings.Contains(err.Error(), "secret") {
				t.Fatalf("error leaks payload: %v", err)
			}
		})
	}
}

func assertAgenticInputConversionError(
	t *testing.T,
	err error,
	tc struct {
		name, code    string
		messages      []*schema.Message
		message, part int
	},
) {
	t.Helper()
	var converted *AgenticInputConversionError
	if !errors.As(err, &converted) ||
		converted.ReasonCode != tc.code ||
		converted.MessageIndex != tc.message ||
		converted.PartIndex != tc.part {
		t.Fatalf("err = %#v", err)
	}
	var wantRole schema.RoleType
	var wantPartType schema.ChatMessagePartType
	if tc.messages[tc.message] != nil {
		wantRole = tc.messages[tc.message].Role
		if tc.part >= 0 {
			wantPartType = tc.messages[tc.message].UserInputMultiContent[tc.part].Type
		}
	}
	if converted.Role != wantRole || converted.PartType != wantPartType {
		t.Fatalf(
			"error identity = role %q part type %q, want %q/%q",
			converted.Role,
			converted.PartType,
			wantRole,
			wantPartType,
		)
	}
}

func TestAgenticInputConversionErrorDoesNotRetryOrFallback(t *testing.T) {
	t.Setenv("CLAUDE_CODE_UNATTENDED_RETRY", "")
	data := "base64-secret"
	input := []*schema.Message{{
		Role: schema.User,
		UserInputMultiContent: []schema.MessageInputPart{{
			Type:  schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &data}},
		}},
	}}
	inner := &captureAgenticInputModel{}
	wrapped := wrapAgenticModel(inner)
	attempts := 0
	_, err := execution.CallModelWithRetry(
		context.Background(),
		execution.RetryConfig{
			MaxRetries: 3,
		},
		func(ctx context.Context, _ int) (*execution.CallModelResult, error) {
			attempts++
			_, callErr := wrapped.Stream(ctx, input)
			return nil, callErr
		},
		nil,
	)
	var conversionErr *AgenticInputConversionError
	if !errors.As(err, &conversionErr) ||
		attempts != 1 ||
		inner.streamCalls != 0 {
		t.Fatalf(
			"err=%v attempts=%d inner calls=%d",
			err,
			attempts,
			inner.streamCalls,
		)
	}
}
