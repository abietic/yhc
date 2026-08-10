package compact

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestRecoverFromOverflowStagesCollapsedSummary(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "old question"},
		{Role: schema.Assistant, Content: "old answer"},
		{Role: schema.User, Content: "latest question"},
		{Role: schema.Assistant, Content: "latest answer"},
	}

	got := RecoverFromOverflow(messages, "sdk")
	if got.Committed != 2 {
		t.Fatalf("expected 2 committed messages, got %d", got.Committed)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("expected collapse summary plus preserved tail, got %d messages", len(got.Messages))
	}
	if got.Messages[0].Extra == nil || got.Messages[0].Extra["subtype"] != "collapse_staged" {
		t.Fatalf("expected first message to be staged collapse summary, got %#v", got.Messages[0])
		return
	}
	if got.Messages[1].Content != "latest question" || got.Messages[2].Content != "latest answer" {
		t.Fatalf("expected preserved tail after summary, got %#v", got.Messages)
	}
}

func TestRecoverFromOverflowSkipsAlreadyCollapsedState(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.System, Content: "", Extra: map[string]any{"subtype": "compact_boundary"}},
		{Role: schema.System, Content: "summary", Extra: map[string]any{"subtype": "compact_summary"}},
		{Role: schema.User, Content: "latest question"},
		{Role: schema.Assistant, Content: "latest answer"},
	}

	got := RecoverFromOverflow(messages, "sdk")
	if got.Committed != 0 {
		t.Fatalf("expected no drain when already compacted, got %d", got.Committed)
	}
	if len(got.Messages) != len(messages) {
		t.Fatalf("expected original messages to be preserved, got %#v", got.Messages)
	}
}

func TestRecoverFromOverflowSkipsCompactQuerySource(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "old question"},
		{Role: schema.Assistant, Content: "old answer"},
		{Role: schema.User, Content: "latest question"},
	}

	got := RecoverFromOverflow(messages, "compact")
	if got.Committed != 0 {
		t.Fatalf("expected no drain during compact query source, got %d", got.Committed)
	}
}
