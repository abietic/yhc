package compact

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"
)

func TestTrimLongThinkingStructuredHistory(t *testing.T) {
	for _, mirrored := range []bool{false, true} {
		t.Run(map[bool]string{false: "structured-only", true: "mirrored"}[mirrored], func(t *testing.T) {
			text := strings.Repeat("推理内容abc", 1000)
			parts := []schema.MessageOutputPart{
				{Type: schema.ChatMessagePartTypeReasoning, Reasoning: &schema.MessageOutputReasoning{Text: text[:6000]}, StreamingMeta: &schema.MessageStreamingMeta{Index: 0}},
				{Type: schema.ChatMessagePartTypeReasoning, Reasoning: &schema.MessageOutputReasoning{Text: text[6000:]}, StreamingMeta: &schema.MessageStreamingMeta{Index: 0}},
				{Type: schema.ChatMessagePartTypeText, Text: "answer", StreamingMeta: &schema.MessageStreamingMeta{Index: 1}},
			}
			msg := &schema.Message{Role: schema.Assistant, Content: "answer", AssistantGenMultiContent: parts}
			if mirrored {
				msg.ReasoningContent = text
			}
			got, freed := TrimLongThinking([]*schema.Message{msg}, 4000)
			wire := ""
			for _, p := range got[0].AssistantGenMultiContent {
				if p.Reasoning != nil {
					wire += p.Reasoning.Text
				}
			}
			if len(wire) >= 4000 || wire != got[0].ReasoningContent || !utf8.ValidString(wire) || freed <= 0 {
				t.Fatalf("wire bytes=%d flat=%d valid=%v freed=%d", len(wire), len(got[0].ReasoningContent), utf8.ValidString(wire), freed)
			}
			if got[0].Content != "answer" || got[0].AssistantGenMultiContent[len(got[0].AssistantGenMultiContent)-1].Text != "answer" {
				t.Fatal("answer changed")
			}
			if msg.AssistantGenMultiContent[0].Reasoning.Text+msg.AssistantGenMultiContent[1].Reasoning.Text != text {
				t.Fatal("input mutated")
			}
			again, more := TrimLongThinking(got, 4000)
			if more != 0 || !reflect.DeepEqual(got, again) {
				t.Fatal("not idempotent")
			}
		})
	}
}

func TestTrimLongThinkingPreservesSignedReasoning(t *testing.T) {
	text := strings.Repeat("signed reasoning ", 1000)
	msg := &schema.Message{Role: schema.Assistant, ReasoningContent: text, AssistantGenMultiContent: []schema.MessageOutputPart{{Type: schema.ChatMessagePartTypeReasoning, Reasoning: &schema.MessageOutputReasoning{Text: text, Signature: "opaque-signature"}}}}
	got, freed := TrimLongThinking([]*schema.Message{msg}, 4000)
	if freed != 0 || !reflect.DeepEqual(got[0], msg) {
		t.Fatal("signed reasoning must remain intact in both representations")
	}
}

func TestEstimateTokenCountDoesNotDependOnStreamChunksOrMirrors(t *testing.T) {
	text := strings.Repeat("reasoning ", 1000)
	plain := &schema.Message{Role: schema.Assistant, ReasoningContent: text, Content: "done"}
	chunked := &schema.Message{Role: schema.Assistant, Content: "done"}
	for _, c := range text {
		chunked.AssistantGenMultiContent = append(chunked.AssistantGenMultiContent, schema.MessageOutputPart{Type: schema.ChatMessagePartTypeReasoning, Reasoning: &schema.MessageOutputReasoning{Text: string(c)}, StreamingMeta: &schema.MessageStreamingMeta{Index: 0}})
	}
	want := EstimateTokenCount([]*schema.Message{plain})
	if got := EstimateTokenCount([]*schema.Message{chunked}); got != want {
		t.Fatalf("stream chunks change estimate: got %d want %d", got, want)
	}
	chunked.ReasoningContent = text
	if got := EstimateTokenCount([]*schema.Message{chunked}); got != want {
		t.Fatalf("mirror double counted: got %d want %d", got, want)
	}
}

func TestAutoCompactDoesNotSubtractSavingsFromAlreadyCompactedMessages(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "16000")
	messages := []*schema.Message{{Role: schema.Assistant, Content: strings.Repeat("x", 16000)}, {Role: schema.User, Content: "latest"}, {Role: schema.Assistant, Content: "answer"}}
	got, _, _ := AutoCompact(messages, "sdk", nil, 2000, "", nil)
	if got == nil {
		t.Fatal("already reduced context still exceeds 3000-token trigger; prior savings must not be subtracted twice")
	}
	if got.PreCompactTokenCount != EstimateTokenCount(messages) {
		t.Fatal("pre-compact estimate does not describe supplied context")
	}
}

func TestTrimLongThinkingPreservesWholeMixedSignedMessage(t *testing.T) {
	text := strings.Repeat("unsigned sibling ", 1000)
	msg := &schema.Message{Role: schema.Assistant, ReasoningContent: "signed" + text, AssistantGenMultiContent: []schema.MessageOutputPart{
		{Type: schema.ChatMessagePartTypeReasoning, Reasoning: &schema.MessageOutputReasoning{Text: "signed", Signature: "opaque-signature"}, StreamingMeta: &schema.MessageStreamingMeta{Index: 0}},
		{Type: schema.ChatMessagePartTypeReasoning, Reasoning: &schema.MessageOutputReasoning{Text: text}, StreamingMeta: &schema.MessageStreamingMeta{Index: 1}},
	}}
	got, freed := TrimLongThinking([]*schema.Message{msg}, 4000)
	if freed != 0 || !reflect.DeepEqual(got[0], msg) {
		t.Fatal("complete message binding must survive when signed state is present")
	}
}

func TestTrimLongThinkingPreservesUnsignedPartMetadata(t *testing.T) {
	text := strings.Repeat("reasoning ", 1000)
	part := schema.MessageOutputPart{Type: schema.ChatMessagePartTypeReasoning, Reasoning: &schema.MessageOutputReasoning{Text: text}, Extra: map[string]any{"provider": "fixture"}, StreamingMeta: &schema.MessageStreamingMeta{Index: 7}}
	msg := &schema.Message{Role: schema.Assistant, ReasoningContent: text, AssistantGenMultiContent: []schema.MessageOutputPart{part}}
	got, freed := TrimLongThinking([]*schema.Message{msg}, 4000)
	p := got[0].AssistantGenMultiContent[0]
	if freed <= 0 || p.Reasoning.Text == text || !reflect.DeepEqual(p.Extra, part.Extra) || !reflect.DeepEqual(p.StreamingMeta, part.StreamingMeta) {
		t.Fatal("unsigned reasoning not trimmed with metadata preserved")
	}
	if msg.AssistantGenMultiContent[0].Reasoning.Text != text {
		t.Fatal("input mutated")
	}
}
