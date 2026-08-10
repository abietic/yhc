package budget

import "testing"

func TestTokenBudgetContinue(t *testing.T) {
	tb := NewTokenBudget(100000)
	tb.RecordInput(10000)
	tb.RecordOutput(5000)
	d := tb.Check()
	if d.Action != "continue" {
		t.Errorf("expected 'continue', got %q", d.Action)
	}
}

func TestTokenBudgetNudge(t *testing.T) {
	tb := NewTokenBudget(100000)
	tb.RecordInput(60000)
	tb.RecordOutput(40000)
	d := tb.Check()
	if d.Action != "continue" {
		t.Errorf("expected 'continue', got %q", d.Action)
	}
	if d.NudgeMessage == "" {
		t.Error("expected nudge message")
	}
}

func TestTokenBudgetDiminishingReturns(t *testing.T) {
	tb := NewTokenBudget(100000)
	for i := 0; i < 4; i++ {
		tb.RecordInput(50000)
		tb.RecordOutput(50000)
		tb.Check()
	}
	tb.RecordInput(50000)
	tb.RecordOutput(50000)
	d := tb.Check()
	if d.Action != "complete" {
		t.Errorf("expected 'complete', got %q", d.Action)
	}
	if !d.DiminishingReturns {
		t.Error("expected diminishing returns")
	}
}

// --- ParseTokenBudget tests ---

func TestParseTokenBudgetShorthandStart(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"+500k", 500_000},
		{"+2M", 2_000_000},
		{"+1.5m", 1_500_000},
		{"  +100k fix this bug", 100_000},
		{"+1b", 1_000_000_000},
	}
	for _, tt := range tests {
		got := ParseTokenBudget(tt.input)
		if got != tt.expected {
			t.Errorf("ParseTokenBudget(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestParseTokenBudgetShorthandEnd(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"fix this bug +500k", 500_000},
		{"do the work +2m.", 2_000_000},
		{"implement feature +1.5M!", 1_500_000},
	}
	for _, tt := range tests {
		got := ParseTokenBudget(tt.input)
		if got != tt.expected {
			t.Errorf("ParseTokenBudget(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestParseTokenBudgetVerbose(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"use 2M tokens", 2_000_000},
		{"please use 500k tokens for this", 500_000},
		{"spend 1.5m token on this task", 1_500_000},
	}
	for _, tt := range tests {
		got := ParseTokenBudget(tt.input)
		if got != tt.expected {
			t.Errorf("ParseTokenBudget(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestParseTokenBudgetNoMatch(t *testing.T) {
	tests := []string{
		"hello world",
		"I have 500 tokens left",
		"the value is +5 (not a budget)",
		"k tokens",
		"",
	}
	for _, tt := range tests {
		got := ParseTokenBudget(tt)
		if got != 0 {
			t.Errorf("ParseTokenBudget(%q) = %d, want 0", tt, got)
		}
	}
}

func TestGetBudgetContinuationMessage(t *testing.T) {
	msg := GetBudgetContinuationMessage(45, 45000, 100000)
	if msg == "" {
		t.Fatal("expected non-empty message")
	}
	if got := msg; got != "Stopped at 45% of token target (45k / 100k). Keep working — do not summarize." {
		t.Errorf("unexpected message: %q", got)
	}
}

func TestGetBudgetContinuationMessageMillions(t *testing.T) {
	msg := GetBudgetContinuationMessage(50, 1000000, 2000000)
	if msg != "Stopped at 50% of token target (1.0M / 2.0M). Keep working — do not summarize." {
		t.Errorf("unexpected message: %q", msg)
	}
}
