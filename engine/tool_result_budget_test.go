package engine

import (
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/transcript"
	"github.com/cloudwego/eino/schema"
)

func TestApplyToolResultBudgetUnderBudgetUnchanged(t *testing.T) {
	state := NewContentReplacementState()
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "I'll check", ToolCalls: []schema.ToolCall{{ID: "tc1", Function: schema.FunctionCall{Name: "Bash"}}}},
		{Role: schema.Tool, Content: "small result", ToolCallID: "tc1"},
	}

	result := ApplyToolResultBudget(messages, state, nil)

	if len(result.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result.Messages))
	}
	if result.Messages[2].Content != "small result" {
		t.Fatalf("expected content unchanged, got %q", result.Messages[2].Content)
	}
	// tc1 should be marked as seen
	if !state.SeenIDs["tc1"] {
		t.Fatal("expected tc1 to be marked as seen")
	}
	if _, replaced := state.Replacements["tc1"]; replaced {
		t.Fatal("expected tc1 to not be replaced (under budget)")
	}
	// No new replacements
	if len(result.NewReplacements) != 0 {
		t.Fatalf("expected no new replacements, got %d", len(result.NewReplacements))
	}
}

func TestApplyToolResultBudgetReplacesLargestFirst(t *testing.T) {
	state := NewContentReplacementState()

	// Create messages that exceed the budget
	smallResult := strings.Repeat("a", 50_000)  // 50K
	largeResult := strings.Repeat("b", 180_000) // 180K — over 200K total

	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "running", ToolCalls: []schema.ToolCall{
			{ID: "tc1", Function: schema.FunctionCall{Name: "Bash"}},
			{ID: "tc2", Function: schema.FunctionCall{Name: "Read"}},
		}},
		{Role: schema.Tool, Content: smallResult, ToolCallID: "tc1"},
		{Role: schema.Tool, Content: largeResult, ToolCallID: "tc2"},
	}

	result := ApplyToolResultBudget(messages, state, nil)

	// The large one (tc2) should be replaced, small one (tc1) kept
	if result.Messages[2].Content != smallResult {
		t.Fatal("expected small result to be unchanged")
	}
	if result.Messages[3].Content == largeResult {
		t.Fatal("expected large result to be replaced")
	}
	if !strings.Contains(result.Messages[3].Content, "<tool-result-budget-exceeded>") {
		t.Fatalf("expected budget-exceeded tag in replacement, got %q", result.Messages[3].Content[:100])
	}
	if !strings.Contains(result.Messages[3].Content, "Output too large") {
		t.Fatalf("expected size info in replacement, got %q", result.Messages[3].Content[:200])
	}

	// Both should be seen
	if !state.SeenIDs["tc1"] || !state.SeenIDs["tc2"] {
		t.Fatal("expected both IDs to be marked as seen")
	}
	// Only tc2 should have a replacement
	if _, ok := state.Replacements["tc1"]; ok {
		t.Fatal("expected tc1 to not have a replacement")
	}
	if _, ok := state.Replacements["tc2"]; !ok {
		t.Fatal("expected tc2 to have a replacement")
	}
	// NewReplacements should report tc2
	if len(result.NewReplacements) != 1 {
		t.Fatalf("expected 1 new replacement, got %d", len(result.NewReplacements))
	}
	if result.NewReplacements[0].ToolUseID != "tc2" {
		t.Fatalf("expected new replacement for tc2, got %q", result.NewReplacements[0].ToolUseID)
	}
}

func TestApplyToolResultBudgetSeenIDsStabilityAcrossCalls(t *testing.T) {
	state := NewContentReplacementState()

	// First call: small result under budget
	messages1 := []*schema.Message{
		{Role: schema.Tool, Content: strings.Repeat("x", 50_000), ToolCallID: "tc1"},
	}
	ApplyToolResultBudget(messages1, state, nil)

	// tc1 is now seen (frozen as unreplaced)
	if !state.SeenIDs["tc1"] {
		t.Fatal("expected tc1 to be seen after first call")
	}

	// Second call: same tc1 in a new context with a huge new result
	// tc1 should remain frozen (not replaced) even if budget is tight
	messages2 := []*schema.Message{
		{Role: schema.Tool, Content: strings.Repeat("x", 50_000), ToolCallID: "tc1"},
		{Role: schema.Tool, Content: strings.Repeat("y", 180_000), ToolCallID: "tc2"},
	}
	result := ApplyToolResultBudget(messages2, state, nil)

	// tc1 is frozen — should NOT be replaced even though budget is tight
	if result.Messages[0].Content != strings.Repeat("x", 50_000) {
		t.Fatal("expected frozen tc1 to remain unchanged")
	}
	// tc2 is fresh and large — should be replaced
	if !strings.Contains(result.Messages[1].Content, "<tool-result-budget-exceeded>") {
		t.Fatal("expected fresh tc2 to be replaced since it's over budget")
	}
}

func TestApplyToolResultBudgetReappliesPreviousReplacements(t *testing.T) {
	state := NewContentReplacementState()

	// First call: force a replacement
	largeContent := strings.Repeat("z", 210_000)
	messages1 := []*schema.Message{
		{Role: schema.Tool, Content: largeContent, ToolCallID: "tc1"},
	}
	result1 := ApplyToolResultBudget(messages1, state, nil)
	replacement := result1.Messages[0].Content

	if !strings.Contains(replacement, "<tool-result-budget-exceeded>") {
		t.Fatal("expected first call to produce a replacement")
	}
	// First call should report tc1 as newly replaced
	if len(result1.NewReplacements) != 1 || result1.NewReplacements[0].ToolUseID != "tc1" {
		t.Fatal("expected first call to report tc1 as newly replaced")
	}

	// Second call: same tc1 appears again — should get exact same replacement
	messages2 := []*schema.Message{
		{Role: schema.Tool, Content: largeContent, ToolCallID: "tc1"},
	}
	result2 := ApplyToolResultBudget(messages2, state, nil)

	if result2.Messages[0].Content != replacement {
		t.Fatal("expected re-application to produce identical replacement text")
	}
	// Re-application should NOT report new replacements (it's a re-apply, not new)
	if len(result2.NewReplacements) != 0 {
		t.Fatalf("expected no new replacements on re-apply, got %d", len(result2.NewReplacements))
	}
}

func TestApplyToolResultBudgetNilStateNoOp(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.Tool, Content: strings.Repeat("a", 300_000), ToolCallID: "tc1"},
	}

	result := ApplyToolResultBudget(messages, nil, nil)
	if result.Messages[0].Content != messages[0].Content {
		t.Fatal("expected nil state to be a no-op")
	}
	if len(result.NewReplacements) != 0 {
		t.Fatal("expected no new replacements with nil state")
	}
}

func TestBuildToolResultPreviewFormat(t *testing.T) {
	content := strings.Repeat("line\n", 1000) // 5000 bytes
	preview := buildToolResultPreview(content, len(content))

	if !strings.HasPrefix(preview, "<tool-result-budget-exceeded>") {
		t.Fatalf("expected opening tag, got %q", preview[:50])
	}
	if !strings.HasSuffix(preview, "</tool-result-budget-exceeded>") {
		t.Fatalf("expected closing tag, got %q", preview[len(preview)-50:])
	}
	if !strings.Contains(preview, "Output too large") {
		t.Fatal("expected size description in preview")
	}
	if !strings.Contains(preview, "...") {
		t.Fatal("expected truncation marker in preview")
	}
	// Preview should be much smaller than original
	if len(preview) > 3000 {
		t.Fatalf("expected preview to be compact, got %d bytes", len(preview))
	}
}

func TestReconstructContentReplacementState(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "running", ToolCalls: []schema.ToolCall{
			{ID: "tc1", Function: schema.FunctionCall{Name: "Bash"}},
			{ID: "tc2", Function: schema.FunctionCall{Name: "Read"}},
		}},
		{Role: schema.Tool, Content: "result1", ToolCallID: "tc1"},
		{Role: schema.Tool, Content: "result2", ToolCallID: "tc2"},
	}

	records := []transcript.Replacement{
		{ToolUseID: "tc2", Replacement: "<replaced-tc2>"},
		{ToolUseID: "tc99", Replacement: "<orphan>"}, // not in messages — should be ignored
	}

	state := ReconstructContentReplacementState(messages, records)

	// Both tc1 and tc2 should be seen (frozen)
	if !state.SeenIDs["tc1"] {
		t.Fatal("expected tc1 to be marked as seen")
	}
	if !state.SeenIDs["tc2"] {
		t.Fatal("expected tc2 to be marked as seen")
	}
	// tc99 should NOT be seen (not in messages)
	if state.SeenIDs["tc99"] {
		t.Fatal("expected tc99 to NOT be marked as seen (not in messages)")
	}
	// Only tc2 should have a replacement
	if _, ok := state.Replacements["tc1"]; ok {
		t.Fatal("expected tc1 to not have a replacement")
	}
	if state.Replacements["tc2"] != "<replaced-tc2>" {
		t.Fatalf("expected tc2 replacement, got %q", state.Replacements["tc2"])
	}
	// tc99's replacement should be ignored (not in messages)
	if _, ok := state.Replacements["tc99"]; ok {
		t.Fatal("expected tc99 replacement to be ignored")
	}
}
