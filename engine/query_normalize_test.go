package engine

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

// --- Step 1: Filter nil messages ---

func TestNormalizeFilterNilMessages(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		nil,
		{Role: schema.Assistant, Content: "reply"},
		nil,
	}
	result := normalizeMessagesForAPI(msgs)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages after nil filtering, got %d", len(result))
	}
	if result[0].Content != "hello" {
		t.Fatalf("expected first message content 'hello', got %q", result[0].Content)
	}
	if result[1].Content != "reply" {
		t.Fatalf("expected second message content 'reply', got %q", result[1].Content)
	}
}

func TestNormalizeAllNilMessages(t *testing.T) {
	msgs := []*schema.Message{nil, nil, nil}
	result := normalizeMessagesForAPI(msgs)
	if len(result) != 0 {
		t.Fatalf("expected empty result for all-nil input, got %d", len(result))
	}
}

// --- Step 2: Filter virtual messages ---

func TestNormalizeFilterVirtualMessages(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "virtual reply", Extra: map[string]any{"virtual": true}},
		{Role: schema.Assistant, Content: "real reply"},
	}
	result := normalizeMessagesForAPI(msgs)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages after virtual filtering, got %d", len(result))
	}
	if result[1].Content != "real reply" {
		t.Fatalf("expected non-virtual assistant, got %q", result[1].Content)
	}
}

func TestNormalizeVirtualFalseNotFiltered(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "keep me", Extra: map[string]any{"virtual": false}},
	}
	result := normalizeMessagesForAPI(msgs)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages (virtual=false should not be filtered), got %d", len(result))
	}
}

func TestNormalizeVirtualNonBoolNotFiltered(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "keep me", Extra: map[string]any{"virtual": "yes"}},
	}
	result := normalizeMessagesForAPI(msgs)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages (virtual=string should not be filtered), got %d", len(result))
	}
}

// --- Step 3: Convert system messages to user ---

func TestNormalizeSystemToUser(t *testing.T) {
	// System followed by assistant (no merge with next user).
	msgs := []*schema.Message{
		{Role: schema.System, Content: "you are helpful"},
		{Role: schema.Assistant, Content: "hello"},
	}
	result := normalizeMessagesForAPI(msgs)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].Role != schema.User {
		t.Fatalf("expected system converted to user role, got %q", result[0].Role)
	}
	if result[0].Content != "[system] you are helpful" {
		t.Fatalf("expected prefixed content, got %q", result[0].Content)
	}
}

func TestNormalizeSystemFollowedByUserMerges(t *testing.T) {
	// System followed by user: both become user and merge.
	msgs := []*schema.Message{
		{Role: schema.System, Content: "you are helpful"},
		{Role: schema.User, Content: "hello"},
	}
	result := normalizeMessagesForAPI(msgs)
	if len(result) != 1 {
		t.Fatalf("expected 1 message (system+user merged), got %d", len(result))
	}
	expected := "[system] you are helpful\n\nhello"
	if result[0].Content != expected {
		t.Fatalf("expected %q, got %q", expected, result[0].Content)
	}
}

func TestNormalizeSystemMergesWithPrecedingUser(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.System, Content: "context info"},
	}
	result := normalizeMessagesForAPI(msgs)
	if len(result) != 1 {
		t.Fatalf("expected 1 message (system merged with user), got %d", len(result))
	}
	expected := "hello\n\n[system] context info"
	if result[0].Content != expected {
		t.Fatalf("expected %q, got %q", expected, result[0].Content)
	}
}

// --- Step 5: Merge consecutive user messages ---

func TestNormalizeMergeConsecutiveUsers(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "first"},
		{Role: schema.User, Content: "second"},
		{Role: schema.User, Content: "third"},
	}
	result := normalizeMessagesForAPI(msgs)
	if len(result) != 1 {
		t.Fatalf("expected 1 merged user message, got %d", len(result))
	}
	expected := "first\n\nsecond\n\nthird"
	if result[0].Content != expected {
		t.Fatalf("expected %q, got %q", expected, result[0].Content)
	}
}

func TestNormalizeMergeUserEmptyContent(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: ""},
		{Role: schema.User, Content: "second"},
	}
	result := normalizeMessagesForAPI(msgs)
	if len(result) != 1 {
		t.Fatalf("expected 1 merged user message, got %d", len(result))
	}
	if result[0].Content != "second" {
		t.Fatalf("expected 'second', got %q", result[0].Content)
	}
}

// --- Step 6: Merge consecutive assistant messages ---

func TestNormalizeMergeConsecutiveAssistants(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "part1"},
		{Role: schema.Assistant, Content: "part2"},
	}
	result := normalizeMessagesForAPI(msgs)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages after assistant merge, got %d", len(result))
	}
	expected := "part1\n\npart2"
	if result[1].Content != expected {
		t.Fatalf("expected %q, got %q", expected, result[1].Content)
	}
}

func TestNormalizeMergeAssistantToolCalls(t *testing.T) {
	tc1 := schema.ToolCall{ID: "1", Function: schema.FunctionCall{Name: "read"}}
	tc2 := schema.ToolCall{ID: "2", Function: schema.FunctionCall{Name: "write"}}
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "first", ToolCalls: []schema.ToolCall{tc1}},
		{Role: schema.Assistant, Content: "second", ToolCalls: []schema.ToolCall{tc2}},
		{Role: schema.Tool, Content: "file content", ToolCallID: "1", ToolName: "read"},
		{Role: schema.Tool, Content: "ok", ToolCallID: "2", ToolName: "write"},
	}
	result := normalizeMessagesForAPI(msgs)
	// After merge: [user, assistant(tc1+tc2), tool(1), tool(2)]
	if len(result) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(result))
	}
	merged := result[1]
	if len(merged.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls after merge, got %d", len(merged.ToolCalls))
	}
	if merged.ToolCalls[0].ID != "1" || merged.ToolCalls[1].ID != "2" {
		t.Fatalf("tool calls not properly merged: %+v", merged.ToolCalls)
	}
}

func TestNormalizeMergeAssistantReasoningContent(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "a", ReasoningContent: "think1"},
		{Role: schema.Assistant, Content: "b", ReasoningContent: "think2"},
	}
	result := normalizeMessagesForAPI(msgs)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	// Note: trailing thinking is stripped from last assistant (step 7),
	// so ReasoningContent will be empty.
	if result[1].ReasoningContent != "" {
		t.Fatalf("expected reasoning stripped from last assistant, got %q", result[1].ReasoningContent)
	}
	if result[1].Content != "a\n\nb" {
		t.Fatalf("expected merged content, got %q", result[1].Content)
	}
}

func TestNormalizeMergeAssistantReasoningNotLast(t *testing.T) {
	// When merged assistants are NOT the last message, reasoning is preserved.
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "a", ReasoningContent: "think1"},
		{Role: schema.Assistant, Content: "b", ReasoningContent: "think2"},
		{Role: schema.User, Content: "followup"},
	}
	result := normalizeMessagesForAPI(msgs)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	expected := "think1\n\nthink2"
	if result[1].ReasoningContent != expected {
		t.Fatalf("expected reasoning %q, got %q", expected, result[1].ReasoningContent)
	}
}

// --- Step 7: Strip trailing thinking from last assistant ---

func TestNormalizeStripTrailingThinkingNoContent(t *testing.T) {
	// Last assistant with reasoning but no content and no tool calls.
	// Step 4 now filters out such messages entirely because after
	// stripping reasoning they would be empty, causing the API to
	// reject with: "Invalid assistant message: content or tool_calls must be set".
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "", ReasoningContent: "deep thought"},
	}
	result := normalizeMessagesForAPI(msgs)
	if len(result) != 1 {
		t.Fatalf("expected 1 message (thinking-only assistant filtered), got %d", len(result))
	}
}

func TestNormalizeStripTrailingThinkingWithContent(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "reply", ReasoningContent: "thinking..."},
	}
	result := normalizeMessagesForAPI(msgs)
	if result[1].ReasoningContent != "" {
		t.Fatalf("expected trailing reasoning stripped, got %q", result[1].ReasoningContent)
	}
	if result[1].Content != "reply" {
		t.Fatalf("expected content preserved, got %q", result[1].Content)
	}
}

func TestNormalizeNoStripThinkingWhenNotLast(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "reply", ReasoningContent: "thinking..."},
		{Role: schema.User, Content: "next"},
	}
	result := normalizeMessagesForAPI(msgs)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	// Not the last message, so reasoning is preserved.
	if result[1].ReasoningContent != "thinking..." {
		t.Fatalf("expected reasoning preserved for non-last assistant, got %q", result[1].ReasoningContent)
	}
}

// --- Step 8: Ensure non-empty assistant content ---

func TestNormalizeEnsureNonEmptyAssistantContent(t *testing.T) {
	tc := schema.ToolCall{ID: "1", Function: schema.FunctionCall{Name: "bash"}}
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "", ToolCalls: []schema.ToolCall{tc}},
		{Role: schema.Tool, Content: "result", ToolCallID: "1"},
	}
	result := normalizeMessagesForAPI(msgs)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	if result[1].Content != " " {
		t.Fatalf("expected single space for empty assistant with tool calls, got %q", result[1].Content)
	}
}

func TestNormalizeAssistantWithContentNotModified(t *testing.T) {
	tc := schema.ToolCall{ID: "1", Function: schema.FunctionCall{Name: "bash"}}
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "I will run a command", ToolCalls: []schema.ToolCall{tc}},
	}
	result := normalizeMessagesForAPI(msgs)
	if result[1].Content != "I will run a command" {
		t.Fatalf("expected content preserved, got %q", result[1].Content)
	}
}

// --- Step 9: Sanitize error tool results ---

func TestNormalizeSanitizeErrorToolResult(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "running tool", ToolCalls: []schema.ToolCall{
			{ID: "1", Function: schema.FunctionCall{Name: "bash"}},
		}},
		{Role: schema.Tool, Content: "", ToolCallID: "1", Extra: map[string]any{"is_error": true}},
	}
	result := normalizeMessagesForAPI(msgs)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	if result[2].Content != "[Tool execution error]" {
		t.Fatalf("expected placeholder for empty error tool result, got %q", result[2].Content)
	}
}

func TestNormalizeSanitizeErrorToolResultWhitespaceOnly(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "running", ToolCalls: []schema.ToolCall{
			{ID: "1", Function: schema.FunctionCall{Name: "bash"}},
		}},
		{Role: schema.Tool, Content: "   \n\t  ", ToolCallID: "1", Extra: map[string]any{"is_error": true}},
	}
	result := normalizeMessagesForAPI(msgs)
	if result[2].Content != "[Tool execution error]" {
		t.Fatalf("expected placeholder for whitespace-only error tool result, got %q", result[2].Content)
	}
}

func TestNormalizeSanitizeErrorToolResultWithContent(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "running", ToolCalls: []schema.ToolCall{
			{ID: "1", Function: schema.FunctionCall{Name: "bash"}},
		}},
		{Role: schema.Tool, Content: "command not found", ToolCallID: "1", Extra: map[string]any{"is_error": true}},
	}
	result := normalizeMessagesForAPI(msgs)
	if result[2].Content != "command not found" {
		t.Fatalf("expected error content preserved when non-empty, got %q", result[2].Content)
	}
}

func TestNormalizeSanitizeNonErrorToolResultNotModified(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "running", ToolCalls: []schema.ToolCall{
			{ID: "1", Function: schema.FunctionCall{Name: "bash"}},
		}},
		{Role: schema.Tool, Content: "", ToolCallID: "1"},
	}
	result := normalizeMessagesForAPI(msgs)
	if result[2].Content != "" {
		t.Fatalf("expected empty content preserved for non-error tool result, got %q", result[2].Content)
	}
}

func TestNormalizeDropsDelayedToolResultFromLaterToolGroup(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.User, Content: "start"},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
			{ID: "call-0", Function: schema.FunctionCall{Name: "read"}},
			{ID: "call-1", Function: schema.FunctionCall{Name: "grep"}},
		}},
		{Role: schema.Tool, Content: "read result", ToolCallID: "call-0", ToolName: "read"},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
			{ID: "call-2", Function: schema.FunctionCall{Name: "bash"}},
		}},
		// This delayed result belongs to the previous assistant message. It must
		// not be attached to call-2's result group merely because it is adjacent.
		{Role: schema.Tool, Content: "grep result", ToolCallID: "call-1", ToolName: "grep"},
		{Role: schema.Tool, Content: "bash result", ToolCallID: "call-2", ToolName: "bash"},
	}

	result := normalizeMessagesForAPI(msgs)
	if len(result) != 6 {
		t.Fatalf("expected user plus two complete assistant/tool groups, got %d messages: %#v", len(result), result)
	}

	wantIDs := []string{"call-0", "call-1", "call-2"}
	gotIDs := []string{result[2].ToolCallID, result[3].ToolCallID, result[5].ToolCallID}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("tool result %d: expected ID %q, got %q", i, wantIDs[i], gotIDs[i])
		}
	}
	if result[3].Content != "[Tool result unavailable - execution was interrupted]" {
		t.Fatalf("expected missing call-1 result to be synthesized, got %q", result[3].Content)
	}
	if result[5].Content != "bash result" {
		t.Fatalf("expected call-2's real result to be preserved, got %q", result[5].Content)
	}
}

// --- Integration / ordering tests ---

func TestNormalizeFullPipeline(t *testing.T) {
	tc := schema.ToolCall{ID: "1", Function: schema.FunctionCall{Name: "bash"}}
	msgs := []*schema.Message{
		nil,
		{Role: schema.User, Content: "start"},
		{Role: schema.Assistant, Content: "virtual", Extra: map[string]any{"virtual": true}},
		{Role: schema.System, Content: "context"},
		{Role: schema.User, Content: "continue"},
		{Role: schema.Assistant, Content: "", ToolCalls: []schema.ToolCall{tc}},
		{Role: schema.Tool, Content: "", ToolCallID: "1", Extra: map[string]any{"is_error": true}},
	}
	result := normalizeMessagesForAPI(msgs)

	// Expected after normalization:
	// 1. nil filtered
	// 2. virtual assistant filtered
	// 3. system converted to "[system] context" and merged with preceding user "start"
	// 4. "continue" user merges with the preceding merged user
	// 5. assistant with empty content + tool calls gets " " content
	// 6. error tool result with empty content gets placeholder

	if len(result) != 3 {
		t.Fatalf("expected 3 messages in full pipeline, got %d", len(result))
	}

	// First: merged user message
	expectedUser := "start\n\n[system] context\n\ncontinue"
	if result[0].Role != schema.User || result[0].Content != expectedUser {
		t.Fatalf("expected merged user %q, got role=%q content=%q", expectedUser, result[0].Role, result[0].Content)
	}

	// Second: assistant with space content
	if result[1].Role != schema.Assistant || result[1].Content != " " {
		t.Fatalf("expected assistant with space content, got role=%q content=%q", result[1].Role, result[1].Content)
	}

	// Third: sanitized error tool result
	if result[2].Role != schema.Tool || result[2].Content != "[Tool execution error]" {
		t.Fatalf("expected sanitized tool result, got role=%q content=%q", result[2].Role, result[2].Content)
	}
}

func TestNormalizeEmptyInput(t *testing.T) {
	result := normalizeMessagesForAPI(nil)
	if result != nil {
		t.Fatalf("expected nil for nil input, got %v", result)
		return
	}

	result = normalizeMessagesForAPI([]*schema.Message{})
	if len(result) != 0 {
		t.Fatalf("expected empty for empty input, got %d", len(result))
	}
}
