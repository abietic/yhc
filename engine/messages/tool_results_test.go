package messages

import (
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestPairToolCallResultsUsesPersistedAttachmentOwner(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  string
		want string
	}{
		{name: "matching owner", ref: "read-a", want: "b is available"},
		{name: "another batch", ref: "earlier-call", want: "[Tool result unavailable - execution was interrupted]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assistant := &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
				{ID: "read-a", Function: schema.FunctionCall{Name: "Read"}},
				{ID: "read-b", Function: schema.FunctionCall{Name: "Read"}},
			}}
			first := &schema.Message{Role: schema.Tool, ToolCallID: "read-a", Content: "a is available"}
			second := &schema.Message{Role: schema.Tool, ToolCallID: "read-b", Content: "b is available"}
			inputAttachment := &schema.Message{
				Role: schema.User, Content: "tool context",
				Extra: map[string]any{"attachment_kind": "image"},
			}
			attachment := MarkToolRoundAttachment(inputAttachment, tc.ref)

			saved, err := json.Marshal([]*schema.Message{assistant, first, attachment, second})
			if err != nil {
				t.Fatal(err)
			}
			var reopened []*schema.Message
			if err := json.Unmarshal(saved, &reopened); err != nil {
				t.Fatal(err)
			}
			got := PairToolCallResults(reopened)
			if len(got) != 4 || got[2].ToolCallID != "read-b" || got[2].Content != tc.want ||
				got[3].Content != inputAttachment.Content {
				t.Fatalf("paired messages = %v, want second result %q before attachment", got, tc.want)
			}
			if _, present := got[3].Extra[toolRoundAttachmentCallIDKey]; present {
				t.Fatal("provider projection leaked the tool-round marker")
			}
			if reopened[2].Extra[toolRoundAttachmentCallIDKey] != tc.ref {
				t.Fatal("projection modified the reopened history")
			}
		})
	}
}
