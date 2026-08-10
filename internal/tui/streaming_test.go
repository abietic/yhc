package tui

import (
	"strings"
	"testing"
	"time"
)

func TestStreamingRenderer_BasicFlow(t *testing.T) {
	r := NewStreamingRenderer()

	// Initially not streaming
	if r.IsStreaming() {
		t.Fatal("should not be streaming initially")
	}

	// Start streaming
	r.StartStreaming()
	if !r.IsStreaming() {
		t.Fatal("should be streaming after Start")
	}

	// Process deltas
	r.OnDelta("Hello ")
	r.OnDelta("world!")

	if r.WordCount() != 2 {
		t.Fatalf("expected 2 words, got %d", r.WordCount())
	}
	if r.charCount != 12 {
		t.Fatalf("expected 12 chars, got %d", r.charCount)
	}
	if r.TokenEstimate() != 3 { // (12+3)/4 = 3
		t.Fatalf("expected 3 token estimate, got %d", r.TokenEstimate())
	}

	// Stop streaming
	r.StopStreaming()
	if r.IsStreaming() {
		t.Fatal("should not be streaming after Stop")
	}
}

func TestStreamingRenderer_CursorBlink(t *testing.T) {
	r := NewStreamingRenderer()
	r.StartStreaming()

	// Initially visible
	if !r.cursorVisible {
		t.Fatal("cursor should be visible initially")
	}

	// Tick 4 times — cursor should become invisible
	for i := 0; i < 4; i++ {
		r.OnTick()
	}
	if r.cursorVisible {
		t.Fatal("cursor should be invisible after 4 ticks")
	}

	// Tick 4 more times — cursor should become visible again
	for i := 0; i < 4; i++ {
		r.OnTick()
	}
	if !r.cursorVisible {
		t.Fatal("cursor should be visible again after 8 ticks")
	}
}

func TestStreamingRenderer_CodeBlockTracking(t *testing.T) {
	r := NewStreamingRenderer()
	r.StartStreaming()

	// Not in code block initially
	if r.InCodeBlock() {
		t.Fatal("should not be in code block initially")
	}

	// Open a code block
	r.OnDelta("```go\nfunc main() {\n")
	if !r.InCodeBlock() {
		t.Fatal("should be in code block after opening fence")
	}
	if r.CodeBlockFence() != "```" {
		t.Fatalf("expected fence '```', got '%s'", r.CodeBlockFence())
	}

	// Close the code block
	r.OnDelta("}\n```\n")
	if r.InCodeBlock() {
		t.Fatal("should not be in code block after closing fence")
	}
}

func TestStreamingRenderer_CursorRender(t *testing.T) {
	r := NewStreamingRenderer()
	styles := defaultStyles()

	// Not streaming — no cursor
	cursor := r.RenderCursor(styles)
	if cursor != "" {
		t.Fatalf("expected empty cursor when not streaming, got '%s'", cursor)
	}

	// Streaming — cursor should be non-empty
	r.StartStreaming()
	cursor = r.RenderCursor(styles)
	if cursor == "" {
		t.Fatal("expected non-empty cursor when streaming")
	}
}

func TestStreamingRenderer_Indicator(t *testing.T) {
	r := NewStreamingRenderer()
	styles := defaultStyles()

	// Not streaming — no indicator
	indicator := r.RenderStreamingIndicator(styles, 80)
	if indicator != "" {
		t.Fatal("expected empty indicator when not streaming")
	}

	// Start streaming and process content
	r.StartStreaming()
	r.OnDelta("Hello world this is a test of the streaming renderer component")

	// Wait a tiny bit so elapsed shows
	time.Sleep(10 * time.Millisecond)

	indicator = r.RenderStreamingIndicator(styles, 80)
	if indicator == "" {
		t.Fatal("expected non-empty indicator during streaming")
	}
	// Should contain word count
	if !strings.Contains(indicator, "words") {
		t.Fatalf("indicator should contain word count, got: '%s'", indicator)
	}
}

func TestStreamingMessage_Lifecycle(t *testing.T) {
	styles := defaultStyles()
	msg := NewStreamingMessage(styles)

	// Start
	msg.Start()
	if msg.Finished() {
		t.Fatal("should not be finished after Start")
	}

	// Append content
	msg.AppendContent("Hello ")
	msg.AppendContent("world!")
	if msg.content != "Hello world!" {
		t.Fatalf("expected 'Hello world!', got '%s'", msg.content)
	}

	// Render should include cursor
	rendered := msg.RenderLines(80, styles)
	if len(rendered) == 0 {
		t.Fatal("expected non-empty render")
	}

	// Finish
	msg.Finish()
	if !msg.Finished() {
		t.Fatal("should be finished after Finish")
	}
}

func TestStreamingMessage_PartialMarkdown(t *testing.T) {
	styles := defaultStyles()
	msg := NewStreamingMessage(styles)
	msg.Start()

	// Stream content with an unclosed code block
	msg.AppendContent("Here is some code:\n```go\nfunc main() {\n")

	// Render should not produce broken output
	rendered := msg.RenderLines(80, styles)
	if len(rendered) == 0 {
		t.Fatal("expected non-empty render with partial code block")
	}

	// The render should complete without panicking
	full := strings.Join(rendered, "\n")
	if full == "" {
		t.Fatal("render produced empty string")
	}
}

func TestPreparePartialMarkdown(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantFence bool
	}{
		{
			name:      "no code blocks",
			input:     "Hello world",
			wantFence: false,
		},
		{
			name:      "closed code block",
			input:     "```go\ncode\n```",
			wantFence: false,
		},
		{
			name:      "unclosed code block",
			input:     "text\n```go\ncode",
			wantFence: true,
		},
		{
			name:      "tilde fence unclosed",
			input:     "text\n~~~python\ncode",
			wantFence: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := PreparePartialMarkdown(tt.input)
			if tt.wantFence {
				// Result should be longer (fence appended)
				if len(result) <= len(tt.input) {
					t.Fatal("expected fence to be appended for unclosed block")
				}
			} else {
				// Result should be same
				if result != tt.input {
					t.Fatalf("expected no change, got: '%s'", result)
				}
			}
		})
	}
}

func TestExtractFence(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"```go", "```"},
		{"````", "````"},
		{"~~~python", "~~~"},
		{"``", ""}, // too short
		{"not a fence", ""},
		{"```", "```"},
	}

	for _, tt := range tests {
		got := extractFence(tt.input)
		if got != tt.want {
			t.Errorf("extractFence(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRenderAssistantWithCursor(t *testing.T) {
	styles := defaultStyles()
	r := NewStreamingRenderer()

	// Without streaming — lines unchanged
	lines := []string{"  Hello", "  World"}
	result := RenderAssistantWithCursor(lines, r, styles)
	if len(result) != 2 {
		t.Fatalf("expected 2 lines without streaming, got %d", len(result))
	}

	// With streaming — cursor appended + indicator line
	r.StartStreaming()
	r.OnDelta("Hello World some content")
	time.Sleep(10 * time.Millisecond)
	result = RenderAssistantWithCursor(lines, r, styles)
	if len(result) < 2 {
		t.Fatal("expected at least 2 lines with streaming")
	}
	// Last real content line should have cursor appended
	if result[0] == lines[0] && result[1] == lines[1] && len(result) == 2 {
		t.Fatal("expected cursor to be appended somewhere")
	}
}
