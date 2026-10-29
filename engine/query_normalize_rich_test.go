package engine

import (
	"reflect"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/compact"
	"github.com/cloudwego/eino/schema"
)

func TestNormalizeCompactedRequestsPreservesRichParts(t *testing.T) {
	imageURL := "https://example.invalid/fixture.png"
	image := schema.MessageInputPart{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &imageURL}}}
	text := func(value string) schema.MessageInputPart {
		return schema.MessageInputPart{Type: schema.ChatMessagePartTypeText, Text: value}
	}
	for _, richFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "rich-second", true: "rich-first"}[richFirst], func(t *testing.T) {
			first := &schema.Message{Role: schema.User, Content: "first request"}
			second := &schema.Message{Role: schema.User, Content: "second request"}
			rich := second
			if richFirst {
				rich = first
			}
			rich.UserInputMultiContent = []schema.MessageInputPart{text(rich.Content), image}
			messages := []*schema.Message{
				first,
				{Role: schema.Assistant, Content: strings.Repeat("old investigation ", 400)},
				second,
				{Role: schema.Assistant, Content: strings.Repeat("more investigation ", 400)},
				{Role: schema.Assistant, Content: "recent action"},
				{Role: schema.Assistant, Content: "recent result"},
			}
			compacted := compact.TryReactiveCompact(messages, "sdk", "prompt_too_long")
			if compacted == nil {
				t.Fatal("fixture did not compact")
			}
			out := normalizeMessagesForAPI(compacted.Messages)
			var merged *schema.Message
			for _, msg := range out {
				if msg.Role == schema.User && msg.Content == "first request\n\nsecond request" {
					merged = msg
				}
			}
			if merged == nil {
				t.Fatal("missing normalized requests")
			}
			want := []schema.MessageInputPart{text(first.Content), text("\n\n"), text(second.Content), image}
			if richFirst {
				want = []schema.MessageInputPart{text(first.Content), image, text("\n\n"), text(second.Content)}
			}
			if !reflect.DeepEqual(merged.UserInputMultiContent, want) {
				t.Fatal("rich parts or text order changed")
			}
			merged.UserInputMultiContent[0].Text = "changed"
			if len(rich.UserInputMultiContent) != 2 || rich.UserInputMultiContent[0].Text != rich.Content {
				t.Fatal("normalization mutated history")
			}
		})
	}
}
