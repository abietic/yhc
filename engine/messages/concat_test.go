package messages

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestConcatAssistantOutputPartsFollowsEinoStreamingIndices(t *testing.T) {
	index := func(value int) *schema.MessageStreamingMeta {
		return &schema.MessageStreamingMeta{Index: value}
	}
	parts := []schema.MessageOutputPart{
		{Type: schema.ChatMessagePartTypeText, Text: "hel", StreamingMeta: index(0)},
		{Type: schema.ChatMessagePartTypeText, Text: "lo", StreamingMeta: index(0)},
		{Type: schema.ChatMessagePartTypeText, Text: "world", StreamingMeta: index(1)},
	}

	got, err := ConcatAssistantOutputParts(parts)
	if err != nil {
		t.Fatalf("ConcatAssistantOutputParts returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("parts = %#v, want two streaming blocks", got)
	}
	if got[0].Text != "hello" || got[1].Text != "world" {
		t.Fatalf("parts = %#v", got)
	}
	if got[0].StreamingMeta == nil || got[0].StreamingMeta.Index != 0 ||
		got[1].StreamingMeta == nil || got[1].StreamingMeta.Index != 1 {
		t.Fatalf("streaming indices lost: %#v", got)
	}
}

func TestConcatAssistantOutputPartsPreservesReasoningSignatureAndExtra(t *testing.T) {
	index := &schema.MessageStreamingMeta{Index: 4}
	parts := []schema.MessageOutputPart{
		{
			Type:          schema.ChatMessagePartTypeReasoning,
			Reasoning:     &schema.MessageOutputReasoning{Text: "think"},
			Extra:         map[string]any{"provider": "test"},
			StreamingMeta: index,
		},
		{
			Type:          schema.ChatMessagePartTypeReasoning,
			Reasoning:     &schema.MessageOutputReasoning{Text: "ing", Signature: "signed"},
			StreamingMeta: &schema.MessageStreamingMeta{Index: 4},
		},
	}

	got, err := ConcatAssistantOutputParts(parts)
	if err != nil {
		t.Fatalf("ConcatAssistantOutputParts returned error: %v", err)
	}
	if len(got) != 1 || got[0].Reasoning == nil {
		t.Fatalf("parts = %#v, want one reasoning block", got)
	}
	if got[0].Reasoning.Text != "thinking" || got[0].Reasoning.Signature != "signed" {
		t.Fatalf("reasoning = %#v", got[0].Reasoning)
	}
	if got[0].Extra["provider"] != "test" {
		t.Fatalf("extra metadata lost: %#v", got[0].Extra)
	}
}
