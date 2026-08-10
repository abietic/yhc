package messages

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestNormalizeMessagesForAPIStripsInternalMetadataAndReordersToolResults(t *testing.T) {
	call := schema.ToolCall{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: `{}`}}
	input := []*schema.Message{
		nil,
		CreateUserMessage("hello"),
		{Role: schema.User, Content: "again", Extra: map[string]any{"is_meta": true, "keep": "yes"}},
		{Role: schema.Tool, ToolCallID: "call-1", Content: "file content"},
		{Role: schema.Assistant, Content: "reading", ToolCalls: []schema.ToolCall{call}, Extra: map[string]any{"subtype": "internal", "message_id": "logical-1", "api": "ok"}},
		{Role: schema.Assistant, Content: ""},
	}

	got := NormalizeMessagesForAPI(input)
	if len(got) != 3 {
		t.Fatalf("expected 3 normalized messages, got %d: %#v", len(got), got)
	}
	if got[0].Role != schema.User || got[0].Content != "hello\nagain" {
		t.Fatalf("expected merged user message, got %#v", got[0])
	}
	if got[0].Extra["is_meta"] != nil || got[0].Extra["keep"] != "yes" {
		t.Fatalf("expected internal metadata stripped and API metadata preserved, got %#v", got[0].Extra)
		return
	}
	if got[1].Role != schema.Assistant || len(got[1].ToolCalls) != 1 {
		t.Fatalf("expected assistant tool call second, got %#v", got[1])
	}
	if got[1].Extra["subtype"] != nil ||
		got[1].Extra["message_id"] != nil ||
		got[1].Extra["api"] != "ok" {
		t.Fatalf("expected assistant metadata stripped/preserved, got %#v", got[1].Extra)
		return
	}
	if got[2].Role != schema.Tool || got[2].ToolCallID != "call-1" {
		t.Fatalf("expected matching tool result after assistant call, got %#v", got[2])
	}

	if input[1].Content != "hello" || input[2].Extra["is_meta"] != true {
		t.Fatal("NormalizeMessagesForAPI mutated original messages")
	}
}

func TestValidateMessageSequenceWarnings(t *testing.T) {
	warnings := ValidateMessageSequence([]*schema.Message{
		CreateUserMessage("one"),
		CreateUserMessage("two"),
		{Role: schema.Tool, ToolCallID: "orphan", Content: "orphan result"},
		{Role: schema.Assistant, Content: "call", ToolCalls: []schema.ToolCall{{ID: "missing", Type: "function"}}},
	})

	joined := strings.Join(warnings, "\n")
	for _, want := range []string{
		"consecutive user messages",
		"not preceded by assistant or tool message",
		"references unknown tool call ID: orphan",
		"tool call ID missing has no matching tool result",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected warning containing %q, got:\n%s", want, joined)
		}
	}
}

func TestMergeConsecutiveMessagesPreservesToolCallBoundaries(t *testing.T) {
	call := schema.ToolCall{ID: "call-1", Type: "function"}
	got := MergeConsecutiveMessages([]*schema.Message{
		{Role: schema.Assistant, Content: "first"},
		{Role: schema.Assistant, Content: "second"},
		{Role: schema.Assistant, Content: "call", ToolCalls: []schema.ToolCall{call}},
		{Role: schema.Assistant, Content: "after call"},
	})

	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %#v", got)
	}
	if got[0].Content != "first\nsecond" {
		t.Fatalf("expected first two assistant messages merged, got %#v", got[0])
	}
	if len(got[1].ToolCalls) != 1 || got[2].Content != "after call" {
		t.Fatalf("expected tool-call boundary preserved, got %#v", got)
	}
}

func TestExtractToolCallsFindToolResultAndTruncate(t *testing.T) {
	call1 := schema.ToolCall{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "Read"}}
	call2 := schema.ToolCall{ID: "call-2", Type: "function", Function: schema.FunctionCall{Name: "Grep"}}
	messages := []*schema.Message{
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{call1}},
		{Role: schema.Tool, ToolCallID: "call-1", ToolName: "Read", Content: "read result"},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{call2}},
	}

	calls := ExtractToolCalls(messages)
	if len(calls) != 2 || calls[0].ID != "call-1" || calls[1].ID != "call-2" {
		t.Fatalf("unexpected extracted tool calls: %#v", calls)
	}
	result := FindToolResult(messages, "call-1")
	if result == nil || result.Content != "read result" {
		t.Fatalf("unexpected tool result: %#v", result)
		return
	}
	if got := FindToolResult(messages, "missing"); got != nil {
		t.Fatalf("expected nil for missing tool result, got %#v", got)
		return
	}

	original := CreateAssistantMessage("abcdef")
	truncated := TruncateMessageContent(original, 3)
	if truncated.Content != "abc[truncated]" {
		t.Fatalf("unexpected truncated content: %q", truncated.Content)
	}
	if original.Content != "abcdef" {
		t.Fatal("truncate mutated original message")
	}
}
