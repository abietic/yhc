package compact

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestGetMessagesAfterCompactBoundary(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "old message 1"},
		{Role: schema.Assistant, Content: "old response 1"},
		{Role: schema.System, Content: "", Extra: map[string]any{"subtype": "compact_boundary"}},
		{Role: schema.User, Content: "new message"},
		{Role: schema.Assistant, Content: "new response"},
	}

	result := GetMessagesAfterCompactBoundary(messages)
	if len(result) != 3 {
		t.Fatalf("expected boundary plus 2 messages, got %d", len(result))
	}
	if result[0].Extra == nil || result[0].Extra["subtype"] != "compact_boundary" {
		t.Fatalf("expected boundary at start of projected slice, got %#v", result[0])
		return
	}
	if result[1].Content != "new message" {
		t.Errorf("expected 'new message', got %q", result[1].Content)
	}
}

func TestGetMessagesAfterCompactBoundaryNoBoundary(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "message 1"},
		{Role: schema.Assistant, Content: "response 1"},
	}

	result := GetMessagesAfterCompactBoundary(messages)
	if len(result) != 2 {
		t.Fatalf("expected all messages when no boundary, got %d", len(result))
	}
}

func TestGetMessagesAfterCompactBoundaryBoundaryAtEnd(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "message 1"},
		{Role: schema.System, Content: "", Extra: map[string]any{"subtype": "compact_boundary"}},
	}

	result := GetMessagesAfterCompactBoundary(messages)
	if len(result) != 1 {
		t.Fatalf("expected boundary only when boundary is last, got %d", len(result))
	}
	if result[0].Extra == nil || result[0].Extra["subtype"] != "compact_boundary" {
		t.Fatalf("expected boundary when boundary is last, got %#v", result[0])
		return
	}
}
