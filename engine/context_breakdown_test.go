package engine

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestGetContextBreakdown(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		Model: "claude-sonnet-4-20250514",
	})

	// Empty conversation
	b := eng.GetContextBreakdown()
	if b == nil {
		t.Fatal("GetContextBreakdown returned nil")
		return
	}
	if b.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", b.Model, "claude-sonnet-4-20250514")
	}
	if b.MaxContextTokens <= 0 {
		t.Errorf("MaxContextTokens = %d, want > 0", b.MaxContextTokens)
	}
	// Should have at least System/Instructions (from base prompt estimate)
	if len(b.Categories) == 0 {
		t.Error("expected at least one category for system prompt estimate")
	}
	foundSystem := false
	for _, cat := range b.Categories {
		if cat.Name == "System/Instructions" {
			foundSystem = true
			if cat.Tokens <= 0 {
				t.Error("System/Instructions tokens should be > 0 even with no messages")
			}
		}
	}
	if !foundSystem {
		t.Error("expected System/Instructions category")
	}

	// Add some messages and verify breakdown
	eng.mu.Lock()
	eng.messages = []*schema.Message{
		{Role: schema.User, Content: "Hello, please help me with a task."},
		{Role: schema.Assistant, Content: "Sure, I'd be happy to help you with that task."},
		{Role: schema.Assistant, Content: "", ToolCalls: []schema.ToolCall{
			{ID: "tc_1", Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: `{"path": "/tmp/test.go"}`}},
		}},
		{Role: schema.Tool, Content: "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}", ToolCallID: "tc_1"},
	}
	eng.mu.Unlock()

	b = eng.GetContextBreakdown()
	if b.TotalTokens <= 0 {
		t.Errorf("TotalTokens = %d, want > 0", b.TotalTokens)
	}
	if b.UsagePercent < 0 || b.UsagePercent > 100 {
		t.Errorf("UsagePercent = %d, want 0-100", b.UsagePercent)
	}
	if b.AvailableTokens <= 0 {
		t.Errorf("AvailableTokens = %d, want > 0", b.AvailableTokens)
	}

	// Check that categories are populated
	categoryMap := make(map[string]int)
	for _, cat := range b.Categories {
		categoryMap[cat.Name] = cat.Tokens
	}

	if categoryMap["User messages"] <= 0 {
		t.Error("expected non-zero User messages tokens")
	}
	if categoryMap["Assistant messages"] <= 0 {
		t.Error("expected non-zero Assistant messages tokens")
	}
	if categoryMap["Tool calls"] <= 0 {
		t.Error("expected non-zero Tool calls tokens")
	}
	if categoryMap["Tool results"] <= 0 {
		t.Error("expected non-zero Tool results tokens")
	}
}

func TestFormatContextBreakdown(t *testing.T) {
	b := &ContextBreakdown{
		Model:            "claude-sonnet-4-20250514",
		MaxContextTokens: 200000,
		TotalTokens:      45000,
		UsagePercent:     22,
		AvailableTokens:  155000,
		Categories: []ContextCategory{
			{Name: "System/Instructions", Tokens: 4200, Percentage: 2.1},
			{Name: "User messages", Tokens: 8100, Percentage: 4.05},
			{Name: "Assistant messages", Tokens: 22300, Percentage: 11.15},
			{Name: "Tool results", Tokens: 8400, Percentage: 4.2},
			{Name: "Other", Tokens: 2000, Percentage: 1.0},
		},
	}

	output := FormatContextBreakdown(b)

	// Check key elements are present
	checks := []string{
		"Context Usage",
		"claude-sonnet-4-20250514",
		"45,000",
		"200,000",
		"22%",
		"System/Instructions",
		"User messages",
		"Assistant messages",
		"Tool results",
		"Other",
		"Total",
		"Available",
		"155,000",
		"\u2588", // filled block
		"\u2591", // empty block
	}

	for _, check := range checks {
		if !strings.Contains(output, check) {
			t.Errorf("output missing %q\nGot:\n%s", check, output)
		}
	}
}

func TestFormatContextBreakdownNil(t *testing.T) {
	output := FormatContextBreakdown(nil)
	if output != "No context breakdown available." {
		t.Errorf("unexpected output for nil: %q", output)
	}
}

func TestFormatTokenCount(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1,000"},
		{45000, "45,000"},
		{200000, "200,000"},
		{1234567, "1,234,567"},
	}

	for _, tt := range tests {
		got := formatTokenCount(tt.input)
		if got != tt.want {
			t.Errorf("formatTokenCount(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRenderBar(t *testing.T) {
	tests := []struct {
		pct   float64
		width int
		want  string
	}{
		{0, 10, "\u2591\u2591\u2591\u2591\u2591\u2591\u2591\u2591\u2591\u2591"},
		{100, 10, "\u2588\u2588\u2588\u2588\u2588\u2588\u2588\u2588\u2588\u2588"},
		{50, 10, "\u2588\u2588\u2588\u2588\u2588\u2591\u2591\u2591\u2591\u2591"},
		{10, 10, "\u2588\u2591\u2591\u2591\u2591\u2591\u2591\u2591\u2591\u2591"},
	}

	for _, tt := range tests {
		got := renderBar(tt.pct, tt.width)
		if got != tt.want {
			t.Errorf("renderBar(%v, %d) = %q, want %q", tt.pct, tt.width, got, tt.want)
		}
	}
}

func TestGetContextBreakdownFormatted(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		Model: "claude-sonnet-4-20250514",
	})

	eng.mu.Lock()
	eng.messages = []*schema.Message{
		{Role: schema.User, Content: "Hello"},
		{Role: schema.Assistant, Content: "Hi there!"},
	}
	eng.mu.Unlock()

	output := eng.GetContextBreakdownFormatted()
	if output == "" {
		t.Fatal("GetContextBreakdownFormatted returned empty string")
	}
	if !strings.Contains(output, "Context Usage") {
		t.Errorf("output missing header, got:\n%s", output)
	}
	if !strings.Contains(output, "claude-sonnet-4-20250514") {
		t.Errorf("output missing model name, got:\n%s", output)
	}
}
