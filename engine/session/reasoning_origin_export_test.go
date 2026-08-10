package session

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/internal/providerorigin"
	"github.com/abietic/yhc/engine/transcript"
)

func TestSessionExportExcludesPrivateReasoningOrigin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	message := &schema.Message{
		Role:             schema.Assistant,
		Content:          "public answer",
		ReasoningContent: "private reasoning sentinel",
		Extra: map[string]any{
			"message_id":       "export-assistant",
			"openai-generated": true,
		},
		AssistantGenMultiContent: []schema.MessageOutputPart{{
			Type: schema.ChatMessagePartTypeReasoning,
			Reasoning: &schema.MessageOutputReasoning{
				Text:      "private summary sentinel",
				Signature: "private signature sentinel",
			},
		}},
	}
	recorder := transcript.NewRecorder("private-export", dir)
	if err := recorder.StageAssistantOrigin(message, providerorigin.Origin{
		Version:             providerorigin.OriginVersion,
		Provider:            "agenticopenai",
		AccountID:           "work-openai",
		APIFamily:           providerorigin.OpenAIResponsesV1,
		APIModel:            "gpt-5.4",
		RouteIdentityDigest: strings.Repeat("c", 64),
		CredentialOriginID:  "private-origin-sentinel/r1",
		RoutePublication:    3,
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMessages([]*schema.Message{message}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}

	for _, format := range []ExportFormat{ExportJSON, ExportMarkdown} {
		exported, err := ExportSession(ExportOptions{
			SessionID:        "private-export",
			Dir:              dir,
			Format:           format,
			IncludeToolCalls: true,
			IncludeMetadata:  true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(exported.Content, "public answer") {
			t.Fatalf("format %v omitted public answer: %s", format, exported.Content)
		}
		for _, forbidden := range []string{
			"assistant_origins",
			"assistant-origin-binding",
			"private-origin-sentinel",
			"openai-generated",
			"private reasoning sentinel",
			"private summary sentinel",
			"private signature sentinel",
		} {
			if strings.Contains(exported.Content, forbidden) {
				t.Fatalf("format %v leaked %q: %s", format, forbidden, exported.Content)
			}
		}
	}
}
