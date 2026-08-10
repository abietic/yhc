package compact

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestSnipCompactIfNeeded_BelowThreshold_ReturnsOriginal(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "hi"},
	}

	result := SnipCompactIfNeeded(messages)

	if result == nil {
		t.Fatalf("expected non-nil result")
	}
	if len(result.Messages) != len(messages) || &result.Messages[0] != &messages[0] {
		t.Errorf("expected the original message slice")
	}
	for i, msg := range result.Messages {
		if msg != messages[i] {
			t.Errorf("expected element %d to be the same pointer", i)
		}
	}
	if result.TokensFreed != 0 {
		t.Errorf("expected TokensFreed=0, got %d", result.TokensFreed)
	}
	if result.BoundaryMessage != nil {
		t.Errorf("expected BoundaryMessage=nil, got %+v", result.BoundaryMessage)
	}
}

func TestSnipCompactIfNeeded_AboveThreshold_TrimsLongToolResult(t *testing.T) {
	longToolContent := strings.Repeat("x", 10000)
	messages := make([]*schema.Message, 0, snipThreshold+1)
	for i := 0; i < snipThreshold; i++ {
		messages = append(messages, &schema.Message{
			Role:    schema.User,
			Content: "short user message",
		})
	}
	messages = append(messages, &schema.Message{
		Role:    schema.Tool,
		Content: longToolContent,
		Extra:   map[string]any{"tool_name": "Read"},
	})

	result := SnipCompactIfNeeded(messages)

	if result == nil {
		t.Fatalf("expected non-nil result")
	}
	if result.TokensFreed <= 0 {
		t.Errorf("expected positive TokensFreed, got %d", result.TokensFreed)
	}
	if result.BoundaryMessage == nil {
		t.Fatalf("expected BoundaryMessage, got nil")
	}
	if result.BoundaryMessage.Role != schema.System {
		t.Errorf("expected boundary role %v, got %v", schema.System, result.BoundaryMessage.Role)
	}
	if result.BoundaryMessage.Content != "Context history snipped" {
		t.Errorf("expected boundary content %q, got %q", "Context history snipped", result.BoundaryMessage.Content)
	}
	extra := result.BoundaryMessage.Extra
	if extra == nil {
		t.Fatalf("expected boundary Extra, got nil")
	}
	if extra["subtype"] != "snip_boundary" {
		t.Errorf("expected subtype %q, got %v", "snip_boundary", extra["subtype"])
	}
	if extra["trigger"] != "history_snip" {
		t.Errorf("expected trigger %q, got %v", "history_snip", extra["trigger"])
	}
	if extra["tokens_freed"] != result.TokensFreed {
		t.Errorf("expected tokens_freed %d, got %v", result.TokensFreed, extra["tokens_freed"])
	}

	lastMsg := result.Messages[len(result.Messages)-1]
	if lastMsg.Role != schema.Tool {
		t.Errorf("expected last message role %v, got %v", schema.Tool, lastMsg.Role)
	}
	if lastMsg.Content == longToolContent {
		t.Errorf("expected tool content to be truncated")
	}
	if !strings.Contains(lastMsg.Content, "...[truncated]...") {
		t.Errorf("expected truncated marker in tool content, got %q", lastMsg.Content)
	}
	if len(lastMsg.Content) >= len(longToolContent) {
		t.Errorf("expected truncated content to be shorter than original, got %d vs %d", len(lastMsg.Content), len(longToolContent))
	}
}

func TestSnipCompactIfNeeded_DoesNotMutateInput(t *testing.T) {
	longToolContent := strings.Repeat("y", 10000)
	unrelatedExtra := map[string]any{"keep": "this"}
	toolMsg := &schema.Message{
		Role:    schema.Tool,
		Content: longToolContent,
		Extra:   map[string]any{"tool_name": "Read"},
	}
	unrelatedMsg := &schema.Message{
		Role:    schema.Assistant,
		Content: "assistant reply",
		Extra:   unrelatedExtra,
	}

	messages := make([]*schema.Message, 0, snipThreshold+2)
	for i := 0; i < snipThreshold-1; i++ {
		messages = append(messages, &schema.Message{
			Role:    schema.User,
			Content: "short user message",
		})
	}
	messages = append(messages, unrelatedMsg, toolMsg)

	originalSlice := messages
	originalToolContent := toolMsg.Content
	originalToolExtra := toolMsg.Extra["tool_name"]
	originalUnrelatedContent := unrelatedMsg.Content
	originalUnrelatedExtraValue := unrelatedMsg.Extra["keep"]

	result := SnipCompactIfNeeded(messages)

	if result == nil {
		t.Fatalf("expected non-nil result")
	}
	if len(messages) != len(originalSlice) {
		t.Errorf("expected input slice length to be unchanged, got %d", len(messages))
	}
	for i, msg := range messages {
		if msg != originalSlice[i] {
			t.Errorf("expected input slice element %d to be unchanged", i)
		}
	}
	if len(result.Messages) != len(originalSlice) {
		t.Errorf("expected result.Messages length to match input, got %d", len(result.Messages))
	}
	if &result.Messages[0] == &originalSlice[0] {
		t.Errorf("expected result.Messages to have a different backing array than input")
	}
	if toolMsg.Content != originalToolContent {
		t.Errorf("expected tool message Content to be unmodified, got %q", toolMsg.Content)
	}
	if toolMsg.Extra["tool_name"] != originalToolExtra {
		t.Errorf("expected tool message Extra to be unmodified, got %v", toolMsg.Extra)
	}
	if unrelatedMsg.Content != originalUnrelatedContent {
		t.Errorf("expected unrelated message Content to be unmodified, got %q", unrelatedMsg.Content)
	}
	if unrelatedMsg.Extra["keep"] != originalUnrelatedExtraValue {
		t.Errorf("expected unrelated message Extra to be unmodified, got %v", unrelatedMsg.Extra)
	}
}

func TestSnipCompactIfNeeded_AboveThresholdNoSnippableContent_NoBoundary(t *testing.T) {
	messages := make([]*schema.Message, 0, snipThreshold+1)
	for i := 0; i < snipThreshold+1; i++ {
		messages = append(messages, &schema.Message{
			Role:    schema.User,
			Content: "short",
		})
	}

	result := SnipCompactIfNeeded(messages)

	if result == nil {
		t.Fatalf("expected non-nil result")
	}
	if result.TokensFreed != 0 {
		t.Errorf("expected TokensFreed=0 when nothing is snippable, got %d", result.TokensFreed)
	}
	if result.BoundaryMessage != nil {
		t.Errorf("expected no boundary when nothing is snippable, got %+v", result.BoundaryMessage)
	}
}
