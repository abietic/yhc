package budget

import "testing"

func TestParseTokenBudgetFromText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		// Shorthand with suffix
		{"+500k", "+500k", 500_000},
		{"+500K uppercase", "+500K", 500_000},
		{"+2M", "+2M", 2_000_000},
		{"+2m lowercase", "+2m", 2_000_000},
		{"+1.5M decimal", "+1.5M", 1_500_000},
		{"+1.5m decimal lowercase", "+1.5m", 1_500_000},
		{"+0.5k", "+0.5k", 500},
		{"+10k", "+10k", 10_000},

		// Verbose: "use N tokens"
		{"use 500k tokens", "use 500k tokens", 500_000},
		{"use 2M tokens", "use 2M tokens", 2_000_000},
		{"use 100k tokens", "use 100k tokens", 100_000},
		{"please use 1.5m tokens for this", "please use 1.5m tokens for this", 1_500_000},
		{"spend 500k tokens", "spend 500k tokens", 500_000},
		{"use 50000 tokens plain", "use 50000 tokens", 50_000},

		// "N more tokens"
		{"500k more tokens", "500k more tokens", 500_000},
		{"2M more tokens", "2M more tokens", 2_000_000},
		{"1.5m more tokens", "1.5m more tokens", 1_500_000},

		// Plain number with +
		{"+50000 plain", "+50000", 50_000},
		{"+100000", "+100000", 100_000},
		{"+1000 tokens", "+1000 tokens", 1_000},

		// Embedded in text
		{"embedded shorthand", "fix this bug +500k", 500_000},
		{"embedded verbose", "please use 2M tokens for this task", 2_000_000},
		{"leading text with +", "continue working +100k", 100_000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseTokenBudgetFromText(tt.input)
			if got != tt.expected {
				t.Errorf("ParseTokenBudgetFromText(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseTokenBudgetFromTextNoMatch(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"plain hello", "hello"},
		{"use the API", "let's use the API"},
		{"500 items no suffix", "500 items"},
		{"empty string", ""},
		{"just plus", "+"},
		{"just k", "k"},
		{"0k zero", "0k"},
		{"negative number", "-500k"},
		{"no plus no context", "some random text here"},
		{"word tokens without number", "we need more tokens"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseTokenBudgetFromText(tt.input)
			if got != 0 {
				t.Errorf("ParseTokenBudgetFromText(%q) = %d, want 0", tt.input, got)
			}
		})
	}
}

func TestIsTokenBudgetContinuation(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		// Pure budget patterns → true
		{"+500k", "+500k", true},
		{"+2M", "+2M", true},
		{"+1.5m", "+1.5m", true},
		{"+50000", "+50000", true},
		{"500k", "500k", true},
		{"2M tokens", "2M tokens", true},
		{"500k more tokens", "500k more tokens", true},
		{"+100k tokens", "+100k tokens", true},
		{"with whitespace", "  +500k  ", true},
		{"with trailing period", "+500k.", true},
		{"with trailing bang", "+2M!", true},

		// Mixed content → false
		{"with instruction", "+500k fix this bug", false},
		{"verbose in sentence", "please use 500k tokens and fix the bug", false},
		{"long text", "I need you to refactor the entire module", false},
		{"empty", "", false},
		{"just whitespace", "   ", false},
		{"plain text", "hello world", false},
		{"no budget keyword", "continue working", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsTokenBudgetContinuation(tt.input)
			if got != tt.expected {
				t.Errorf("IsTokenBudgetContinuation(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}
