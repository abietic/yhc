package tui

import (
	"strings"
	"testing"
)

func TestStreamingMarkdownCodeBlockHighlightWithLanguage(t *testing.T) {
	// Code blocks with language tags should produce syntax-highlighted output
	// with distinct colors for keywords, function names, and string literals.
	content := "Here is some Go code:\n\n```go\npackage main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello world\")\n}\n```\n\nDone."

	sm := &StreamingMarkdown{}
	result := sm.Render(content, 80, ThemePolarNight)

	// Glamour's Chroma path downsamples the Revontuli semantic colors to
	// terminal256: violet keywords, teal functions, and green strings.
	hasKeywordColor := strings.Contains(result, "\x1b[38;5;141m")
	hasFuncColor := strings.Contains(result, "\x1b[38;5;79m")
	hasStringColor := strings.Contains(result, "\x1b[38;5;114m")

	if !hasKeywordColor {
		t.Error("Missing semantic keyword highlighting color (38;5;141) in rendered code block with language tag")
	}
	if !hasFuncColor {
		t.Error("Missing semantic function highlighting color (38;5;79) in rendered code block with language tag")
	}
	if !hasStringColor {
		t.Error("Missing semantic string highlighting color (38;5;114) in rendered code block with language tag")
	}
}

func TestStreamingMarkdownCodeBlockNoLanguagePlain(t *testing.T) {
	// Code blocks WITHOUT language tags should NOT have syntax highlighting.
	// They should render as plain monospace text with the base code color only.
	content := "Plain code:\n\n```\nvar x = function() {\n  return 42;\n}\n```\n"

	sm := &StreamingMarkdown{}
	result := sm.Render(content, 80, ThemePolarNight)

	// Should not have the semantic Chroma colors when auto-detection is off.
	hasKeywordColor := strings.Contains(result, "\x1b[38;5;141m")
	hasFuncColor := strings.Contains(result, "\x1b[38;5;79m")

	if hasKeywordColor {
		t.Error("Code block without language tag should not have keyword highlighting color")
	}
	if hasFuncColor {
		t.Error("Code block without language tag should not have function highlighting color")
	}

	// Content should still be present (rendered as plain text)
	stripped := stripANSIForTest(result)
	if !strings.Contains(stripped, "var x") {
		t.Error("Code block content 'var x' missing from rendered output")
	}
}

func TestStreamingMarkdownCodeBlockStreamingHighlight(t *testing.T) {
	// Simulate streaming of content that builds up to a complete code block.
	// After streaming completes, the final render should have syntax highlighting.
	content := "Here is code:\n\n```python\ndef hello():\n    print(\"world\")\n```\n\nEnd."

	sm := &StreamingMarkdown{}

	// Stream character by character (worst case)
	var result string
	for i := 1; i <= len(content); i++ {
		result = sm.Render(content[:i], 80, ThemePolarNight)
	}

	// After streaming completes, the final render should have syntax highlighting
	stripped := stripANSIForTest(result)
	if !strings.Contains(stripped, "def") {
		t.Error("Final streamed render missing 'def' keyword")
	}
	if !strings.Contains(stripped, "hello") {
		t.Error("Final streamed render missing 'hello' function name")
	}

	// Should have keyword/function colors from chroma
	hasViolet := strings.Contains(result, "\x1b[38;5;141m") // keyword
	hasTeal := strings.Contains(result, "\x1b[38;5;79m")    // function
	if !hasViolet && !hasTeal {
		t.Error("Streamed code block missing syntax highlighting colors after completion")
	}
}

func TestStreamingMarkdownCodeBlockTildeFence(t *testing.T) {
	// Tilde fences (~~~) should also receive syntax highlighting with language tags
	// and plain rendering without.
	contentWithLang := "~~~go\nfunc main() {}\n~~~\n"
	contentNoLang := "~~~\nfunc main() {}\n~~~\n"

	sm1 := &StreamingMarkdown{}
	result1 := sm1.Render(contentWithLang, 80, ThemePolarNight)

	sm2 := &StreamingMarkdown{}
	result2 := sm2.Render(contentNoLang, 80, ThemePolarNight)

	// With language: should have keyword color
	if !strings.Contains(result1, "\x1b[38;5;141m") {
		t.Error("Tilde fence with language tag missing keyword highlighting")
	}
	// Without language: should NOT have keyword color
	if strings.Contains(result2, "\x1b[38;5;141m") {
		t.Error("Tilde fence without language tag should not have keyword highlighting")
	}
}

func TestLabelUnlabeledCodeBlocks(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "backtick fence without language gets text label",
			input:    "```\ncode here\n```",
			expected: "```text\ncode here\n```",
		},
		{
			name:     "backtick fence with language unchanged",
			input:    "```go\ncode here\n```",
			expected: "```go\ncode here\n```",
		},
		{
			name:     "tilde fence without language gets text label",
			input:    "~~~\ncode here\n~~~",
			expected: "~~~text\ncode here\n~~~",
		},
		{
			name:     "tilde fence with language unchanged",
			input:    "~~~python\ncode here\n~~~",
			expected: "~~~python\ncode here\n~~~",
		},
		{
			name:     "no fences unchanged",
			input:    "hello world\nno code here",
			expected: "hello world\nno code here",
		},
		{
			name:     "indented fence preserved",
			input:    "  ```\n  code\n  ```",
			expected: "  ```text\n  code\n  ```",
		},
		{
			name:     "multiple blocks mixed",
			input:    "```go\nfunc a() {}\n```\n\n```\nplain\n```",
			expected: "```go\nfunc a() {}\n```\n\n```text\nplain\n```",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := labelUnlabeledCodeBlocks(tt.input)
			if got != tt.expected {
				t.Errorf("labelUnlabeledCodeBlocks(%q)\n  got:  %q\n  want: %q", tt.input, got, tt.expected)
			}
		})
	}
}
