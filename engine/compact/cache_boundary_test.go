package compact

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

// --- FindCacheSafeSplitPoints tests ---

func TestFindCacheSafeSplitPointsBasic(t *testing.T) {
	// Two API rounds: [user, assistant(a1), user] and [assistant(a2)]
	// GroupMessagesByAPIRound boundary is at index 3.
	// User message at index 2 is also a valid split (user_turn_boundary).
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello world"},
		{Role: schema.Assistant, Content: "hi there", Extra: map[string]any{"message_id": "a1"}},
		{Role: schema.User, Content: "do something"},
		{Role: schema.Assistant, Content: "done", Extra: map[string]any{"message_id": "a2"}},
	}

	splits := FindCacheSafeSplitPoints(messages, 0.5)
	if len(splits) == 0 {
		t.Fatal("expected at least one split point")
	}

	// The best split should prefer the user_turn_boundary at index 2
	best := splits[0]
	if best.Reason != "user_turn_boundary" && best.Reason != "api_round_boundary" {
		t.Fatalf("expected valid reason, got %q", best.Reason)
	}
	if best.TokensBefore <= 0 {
		t.Fatalf("expected positive TokensBefore, got %d", best.TokensBefore)
	}
	if best.TokensAfter <= 0 {
		t.Fatalf("expected positive TokensAfter, got %d", best.TokensAfter)
	}
	if best.Index < 1 || best.Index >= len(messages) {
		t.Fatalf("expected valid split index, got %d", best.Index)
	}
}

func TestFindCacheSafeSplitPointsPrefsUserBoundary(t *testing.T) {
	// Three rounds with clear user turn starts at indices 2 and 4.
	// The algorithm should include user_turn_boundary splits in results.
	// When a user_turn_boundary is at the same distance as an api_round_boundary,
	// the user boundary wins due to the -0.1 preference bonus.
	messages := []*schema.Message{
		{Role: schema.User, Content: "first question"},
		{Role: schema.Assistant, Content: "first answer", Extra: map[string]any{"message_id": "a1"}},
		{Role: schema.User, Content: "second question"},
		{Role: schema.Assistant, Content: "second answer", Extra: map[string]any{"message_id": "a2"}},
		{Role: schema.User, Content: "third question"},
		{Role: schema.Assistant, Content: "third answer", Extra: map[string]any{"message_id": "a3"}},
	}

	splits := FindCacheSafeSplitPoints(messages, 0.5)
	if len(splits) == 0 {
		t.Fatal("expected split points")
	}

	// At least one of the returned splits should be a user_turn_boundary
	hasUserBoundary := false
	for _, sp := range splits {
		if sp.Reason == "user_turn_boundary" {
			hasUserBoundary = true
			break
		}
	}
	if !hasUserBoundary {
		t.Fatal("expected at least one user_turn_boundary split in results")
	}

	// All split points should be valid (never split mid-tool-call)
	for _, sp := range splits {
		valid, reason := ValidateSplitPoint(messages, sp.Index)
		if !valid {
			t.Fatalf("split at index %d is invalid: %s", sp.Index, reason)
		}
	}

	// When equal-distance, user_turn_boundary should be preferred over api_round_boundary.
	// Test with a ratio that hits the user boundary exactly.
	// Index 2 has tokensBefore ~= 23/70 ~= 0.33
	splitsAt33 := FindCacheSafeSplitPoints(messages, 0.33)
	if len(splitsAt33) == 0 {
		t.Fatal("expected split points at 33% ratio")
	}
	// The best split at 33% should be the user_turn_boundary at index 2
	if splitsAt33[0].Reason != "user_turn_boundary" {
		t.Fatalf("expected user_turn_boundary as best at 33%% ratio, got %q at index %d",
			splitsAt33[0].Reason, splitsAt33[0].Index)
	}
}

func TestFindCacheSafeSplitPointsNeverSplitsMidToolCall(t *testing.T) {
	// A tool call sequence that must stay together
	messages := []*schema.Message{
		{Role: schema.User, Content: "read file.go"},
		{Role: schema.Assistant, Content: "", Extra: map[string]any{"message_id": "a1"}, ToolCalls: []schema.ToolCall{
			{ID: "tc1", Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"/tmp/x"}`}},
		}},
		{Role: schema.Tool, Content: "file content here", ToolCallID: "tc1"},
		{Role: schema.User, Content: "now edit it"},
		{Role: schema.Assistant, Content: "edited", Extra: map[string]any{"message_id": "a2"}},
	}

	splits := FindCacheSafeSplitPoints(messages, 0.5)
	if len(splits) == 0 {
		t.Fatal("expected split points")
	}

	// The split should NOT be at index 2 (between assistant tool_call and tool result)
	for _, sp := range splits {
		if sp.Index == 2 {
			t.Fatalf("split at index 2 would separate tool call from result")
		}
	}
}

func TestFindCacheSafeSplitPointsTooFewMessages(t *testing.T) {
	// Single message — no split possible
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
	}
	splits := FindCacheSafeSplitPoints(messages, 0.5)
	if splits != nil {
		t.Fatalf("expected nil for single message, got %v", splits)
		return
	}
}

func TestFindCacheSafeSplitPointsEmptyHistory(t *testing.T) {
	splits := FindCacheSafeSplitPoints(nil, 0.5)
	if splits != nil {
		t.Fatalf("expected nil for nil messages, got %v", splits)
		return
	}
}

func TestFindCacheSafeSplitPointsSingleGroup(t *testing.T) {
	// All messages in one API round (same assistant ID)
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "part1", Extra: map[string]any{"message_id": "same"}},
		{Role: schema.Tool, Content: "result"},
		{Role: schema.Assistant, Content: "part2", Extra: map[string]any{"message_id": "same"}},
	}
	splits := FindCacheSafeSplitPoints(messages, 0.5)
	if splits != nil {
		t.Fatalf("expected nil for single group, got %v", splits)
		return
	}
}

func TestFindCacheSafeSplitPointsRatioClamping(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "q1"},
		{Role: schema.Assistant, Content: "a1", Extra: map[string]any{"message_id": "a1"}},
		{Role: schema.User, Content: "q2"},
		{Role: schema.Assistant, Content: "a2", Extra: map[string]any{"message_id": "a2"}},
	}

	// Extreme ratios should be clamped and still produce valid results
	splits1 := FindCacheSafeSplitPoints(messages, 0.0)
	if splits1 == nil {
		t.Fatal("expected split points even with ratio 0 (clamped to 0.2)")
		return
	}

	splits2 := FindCacheSafeSplitPoints(messages, 1.0)
	if splits2 == nil {
		t.Fatal("expected split points even with ratio 1.0 (clamped to 0.9)")
		return
	}
}

func TestFindCacheSafeSplitPointsAllToolCalls(t *testing.T) {
	// Conversation is entirely tool calls — should still find splits between rounds
	messages := []*schema.Message{
		{Role: schema.User, Content: "do tasks"},
		{Role: schema.Assistant, Content: "", Extra: map[string]any{"message_id": "a1"}, ToolCalls: []schema.ToolCall{
			{ID: "tc1", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"cmd":"ls"}`}},
		}},
		{Role: schema.Tool, Content: "file1.go\nfile2.go", ToolCallID: "tc1"},
		{Role: schema.Assistant, Content: "", Extra: map[string]any{"message_id": "a2"}, ToolCalls: []schema.ToolCall{
			{ID: "tc2", Function: schema.FunctionCall{Name: "Read", Arguments: `{"file":"file1.go"}`}},
		}},
		{Role: schema.Tool, Content: "package main", ToolCallID: "tc2"},
		{Role: schema.Assistant, Content: "done", Extra: map[string]any{"message_id": "a3"}},
	}

	splits := FindCacheSafeSplitPoints(messages, 0.5)
	if splits == nil {
		t.Fatal("expected split points for multi-round tool conversation")
		return
	}

	// Validate that all returned splits are actually safe
	for _, sp := range splits {
		valid, reason := ValidateSplitPoint(messages, sp.Index)
		if !valid {
			t.Fatalf("split at index %d is invalid: %s", sp.Index, reason)
		}
	}
}

// --- ValidateSplitPoint tests ---

func TestValidateSplitPointValid(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "hi"},
		{Role: schema.User, Content: "bye"},
	}

	valid, reason := ValidateSplitPoint(messages, 2)
	if !valid {
		t.Fatalf("expected valid split at index 2, got invalid: %s", reason)
	}
}

func TestValidateSplitPointInvalidMidToolCall(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "read a file"},
		{Role: schema.Assistant, Content: "", ToolCalls: []schema.ToolCall{
			{ID: "tc1", Function: schema.FunctionCall{Name: "Read", Arguments: `{}`}},
		}},
		{Role: schema.Tool, Content: "file content", ToolCallID: "tc1"},
		{Role: schema.User, Content: "thanks"},
	}

	// Splitting at index 2 (between assistant tool_call and tool result) should be invalid
	valid, reason := ValidateSplitPoint(messages, 2)
	if valid {
		t.Fatal("expected invalid split between tool call and tool result")
	}
	if reason == "" {
		t.Fatal("expected a reason for invalid split")
	}
}

func TestValidateSplitPointToolResultFromToolCallBefore(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.Assistant, Content: "", ToolCalls: []schema.ToolCall{
			{ID: "tc1", Function: schema.FunctionCall{Name: "Read", Arguments: `{}`}},
		}},
		{Role: schema.Tool, Content: "result", ToolCallID: "tc1"},
		{Role: schema.User, Content: "next"},
	}

	// Splitting at index 1 puts tool result separate from its call
	valid, reason := ValidateSplitPoint(messages, 1)
	if valid {
		t.Fatal("expected invalid split: tool result separated from tool call")
	}
	if reason == "" {
		t.Fatal("expected a reason")
	}
}

func TestValidateSplitPointOutOfRange(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
	}

	valid, _ := ValidateSplitPoint(messages, 0)
	if valid {
		t.Fatal("expected invalid for index 0")
	}

	valid, _ = ValidateSplitPoint(messages, 1)
	if valid {
		t.Fatal("expected invalid for index == len(messages)")
	}
}

func TestValidateSplitPointMidThinking(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "think about this"},
		{Role: schema.Assistant, Content: "part1", ReasoningContent: "thinking...", Extra: map[string]any{"message_id": "a1"}},
		{Role: schema.Assistant, Content: "part2", Extra: map[string]any{"message_id": "a1"}},
		{Role: schema.User, Content: "ok"},
	}

	// Splitting between two parts of the same thinking turn should be invalid
	valid, reason := ValidateSplitPoint(messages, 2)
	if valid {
		t.Fatal("expected invalid split inside thinking sequence")
	}
	if reason == "" {
		t.Fatal("expected a reason")
	}
}

// --- isMetaMessage tests ---

func TestIsMetaMessage(t *testing.T) {
	tests := []struct {
		name     string
		msg      *schema.Message
		expected bool
	}{
		{"nil message", nil, false},
		{"no extra", &schema.Message{Role: schema.User, Content: "hi"}, false},
		{"is_meta flag", &schema.Message{Role: schema.User, Extra: map[string]any{"is_meta": true}}, true},
		{"isMeta flag", &schema.Message{Role: schema.User, Extra: map[string]any{"isMeta": true}}, true},
		{"attachment subtype", &schema.Message{Role: schema.User, Extra: map[string]any{"subtype": "attachment"}}, true},
		{"compact_boundary", &schema.Message{Role: schema.System, Extra: map[string]any{"subtype": "compact_boundary"}}, true},
		{"compact_summary", &schema.Message{Role: schema.System, Extra: map[string]any{"subtype": "compact_summary"}}, true},
		{"regular user msg", &schema.Message{Role: schema.User, Content: "real message", Extra: map[string]any{"foo": "bar"}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isMetaMessage(tt.msg)
			if got != tt.expected {
				t.Fatalf("isMetaMessage() = %v, want %v", got, tt.expected)
			}
		})
	}
}
