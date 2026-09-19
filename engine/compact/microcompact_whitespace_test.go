package compact

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestSnipCompactIfNeeded_PreservesWhitespaceSensitiveShortMessages(t *testing.T) {
	userContent := "Follow these exact instructions:\n\n\n    keep four leading spaces\nend"
	readContent := "{\n  \"path\": \"example.py\",\n  \"content\": \"def run():\\n    if ready:\\n        return 'three   spaces'\\n\"\n}"
	assistantContent := "Result:\n\n\n    preserve this block"
	reasoningContent := "Reasoning:\n\n\n    nested detail with   three spaces"
	longToolContent := strings.Repeat("x", 10000)

	messages := []*schema.Message{
		{Role: schema.User, Content: userContent},
		{Role: schema.Tool, Content: readContent, Extra: map[string]any{"tool_name": "Read"}},
		{Role: schema.Assistant, Content: assistantContent, ReasoningContent: reasoningContent},
	}
	for len(messages) < snipThreshold {
		messages = append(messages, &schema.Message{Role: schema.User, Content: "short"})
	}
	messages = append(messages, &schema.Message{Role: schema.Tool, Content: longToolContent})

	result := SnipCompactIfNeeded(messages)

	if result.BoundaryMessage == nil || result.TokensFreed <= 0 {
		t.Fatalf("expected long tool result to be trimmed and accounted for, got boundary=%v tokens=%d", result.BoundaryMessage, result.TokensFreed)
	}
	if got := result.Messages[0].Content; got != userContent {
		t.Errorf("user content changed:\nwant %q\n got %q", userContent, got)
	}
	if got := result.Messages[1].Content; got != readContent {
		t.Errorf("Read tool content changed:\nwant %q\n got %q", readContent, got)
	}
	if got := result.Messages[2].Content; got != assistantContent {
		t.Errorf("assistant content changed:\nwant %q\n got %q", assistantContent, got)
	}
	if got := result.Messages[2].ReasoningContent; got != reasoningContent {
		t.Errorf("assistant reasoning changed:\nwant %q\n got %q", reasoningContent, got)
	}
	if got := result.Messages[len(result.Messages)-1].Content; got == longToolContent || !strings.Contains(got, "...[truncated]...") {
		t.Errorf("expected long tool result to be truncated, got %q", got)
	}

	if messages[0].Content != userContent || messages[1].Content != readContent || messages[2].Content != assistantContent || messages[2].ReasoningContent != reasoningContent {
		t.Error("SnipCompactIfNeeded mutated the input messages")
	}
}
