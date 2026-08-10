package provider

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func outputIndex(index int) *schema.MessageStreamingMeta {
	return &schema.MessageStreamingMeta{Index: index}
}

func TestMessagesToAgenticUsesEinoOutputPartConcatenation(t *testing.T) {
	msg := &schema.Message{
		Role:    schema.Assistant,
		Content: "Now let me read",
		AssistantGenMultiContent: []schema.MessageOutputPart{
			{
				Type:          schema.ChatMessagePartTypeText,
				Text:          "Now",
				Extra:         map[string]any{"provider": "test"},
				StreamingMeta: outputIndex(0),
			},
			{Type: schema.ChatMessagePartTypeText, Text: " let", StreamingMeta: outputIndex(0)},
			{Type: schema.ChatMessagePartTypeText, Text: " me", StreamingMeta: outputIndex(0)},
			{Type: schema.ChatMessagePartTypeText, Text: " read", StreamingMeta: outputIndex(0)},
		},
	}
	out, err := messagesToAgentic([]*schema.Message{msg})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 agentic message, got %d", len(out))
	}
	textBlocks := 0
	for _, block := range out[0].ContentBlocks {
		if block.Type != schema.ContentBlockTypeAssistantGenText {
			continue
		}
		textBlocks++
		if block.AssistantGenText.Text != "Now let me read" {
			t.Errorf("block text = %q", block.AssistantGenText.Text)
		}
		if block.StreamingMeta == nil || block.StreamingMeta.Index != 0 {
			t.Errorf("streaming metadata = %#v, want index 0", block.StreamingMeta)
		}
		if block.Extra["provider"] != "test" {
			t.Errorf("extra metadata lost: %#v", block.Extra)
		}
	}
	if textBlocks != 1 {
		t.Fatalf("expected 1 merged text block, got %d", textBlocks)
	}
}

func TestMessagesToAgenticPreservesDistinctStreamingIndices(t *testing.T) {
	msg := &schema.Message{
		Role: schema.Assistant,
		AssistantGenMultiContent: []schema.MessageOutputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "first", StreamingMeta: outputIndex(0)},
			{Type: schema.ChatMessagePartTypeText, Text: "second", StreamingMeta: outputIndex(1)},
			{
				Type:          schema.ChatMessagePartTypeReasoning,
				Reasoning:     &schema.MessageOutputReasoning{Text: "a", Signature: "sig-a"},
				StreamingMeta: outputIndex(2),
			},
			{
				Type:          schema.ChatMessagePartTypeReasoning,
				Reasoning:     &schema.MessageOutputReasoning{Text: "b", Signature: "sig-b"},
				StreamingMeta: outputIndex(3),
			},
		},
	}

	out, err := messagesToAgentic([]*schema.Message{msg})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(out[0].ContentBlocks) != 4 {
		t.Fatalf("distinct indexed parts collapsed: %#v", out)
	}
	for i, block := range out[0].ContentBlocks {
		if block.StreamingMeta == nil || block.StreamingMeta.Index != i {
			t.Fatalf("block[%d] metadata = %#v", i, block.StreamingMeta)
		}
	}
	if got := out[0].ContentBlocks[2].Reasoning.Signature; got != "sig-a" {
		t.Fatalf("first reasoning signature = %q", got)
	}
	if got := out[0].ContentBlocks[3].Reasoning.Signature; got != "sig-b" {
		t.Fatalf("second reasoning signature = %q", got)
	}
}
