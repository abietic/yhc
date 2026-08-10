package tui

import (
	"os"
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestStreamingMarkdownRenderCorrectness(t *testing.T) {
	content, err := os.ReadFile("testdata/render_test.md")
	if err != nil {
		t.Fatalf("failed to read test file: %v", err)
		return
	}
	src := string(content)

	sm := &StreamingMarkdown{}
	width := 100

	// Single full render
	result := sm.Render(src, width, ThemePolarNight)
	if result == "" {
		t.Fatal("render returned empty string")
	}

	// Strip ANSI for content checks
	stripped := stripANSIForTest(result)

	// Check all heading levels appear
	checks := []struct {
		label string
		want  string
	}{
		{"H1", "Heading Level 1"},
		{"H2", "Heading Level 2"},
		{"H3", "Heading Level 3"},
		{"H4", "Heading Level 4"},
		{"H5", "Heading Level 5"},
		{"H6", "Heading Level 6"},
		{"HR", "━━━━━━━━"},
		{"Bold", "bold text"},
		{"Inline code", "inline code"},
		{"Block quote", "▎"},
		{"Unordered list", "•"},
		{"Task list done", "☑"},
		{"Table header", "Feature"},
		{"Code block content", "StreamingMarkdown"},
		{"Link text", "link to Go documentation"},
	}

	for _, c := range checks {
		if !strings.Contains(stripped, c.want) {
			t.Errorf("%s: expected %q in output, not found", c.label, c.want)
		}
	}
}

func TestStreamingMarkdownStreamingSimulation(t *testing.T) {
	content := "# Hello World\n\nFirst paragraph.\n\n## Second Heading\n\nSecond paragraph.\n\n---\n\nAfter rule.\n\n### Third Heading\n\nFinal text."

	sm := &StreamingMarkdown{}
	width := 80

	// Simulate streaming: content grows in chunks
	var lastResult string
	for i := 10; i <= len(content); i += 15 {
		end := i
		if end > len(content) {
			end = len(content)
		}
		_ = sm.Render(content[:end], width, ThemePolarNight)
	}
	// Final render with full content
	lastResult = sm.Render(content, width, ThemePolarNight)

	stripped := stripANSIForTest(lastResult)

	// All content should be present in final render
	for _, want := range []string{"Hello World", "Second Heading", "Third Heading", "After rule", "━━━━"} {
		if !strings.Contains(stripped, want) {
			t.Errorf("streaming final render missing %q", want)
		}
	}
}

func TestStreamingMarkdownCacheConsistency(t *testing.T) {
	content := "# Title\n\nParagraph one.\n\n## Subtitle\n\nParagraph two.\n\n---\n\nEnd."

	sm := &StreamingMarkdown{}
	width := 80

	// First render
	r1 := sm.Render(content, width, ThemePolarNight)
	// Second render (should hit cache)
	r2 := sm.Render(content, width, ThemePolarNight)

	if r1 != r2 {
		t.Error("cached render differs from initial render")
	}

	// Different width should produce different result
	r3 := sm.Render(content, 40, ThemePolarNight)
	if r3 == r1 {
		t.Error("different width produced same result")
	}
}

func BenchmarkStreamingMarkdownFullRender(b *testing.B) {
	content, err := os.ReadFile("testdata/render_test.md")
	if err != nil {
		b.Fatalf("failed to read test file: %v", err)
	}
	src := string(content)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sm := &StreamingMarkdown{}
		sm.Render(src, 100, ThemePolarNight)
	}
}

func BenchmarkStreamingMarkdownCachedRender(b *testing.B) {
	content, err := os.ReadFile("testdata/render_test.md")
	if err != nil {
		b.Fatalf("failed to read test file: %v", err)
	}
	src := string(content)

	sm := &StreamingMarkdown{}
	sm.Render(src, 100, ThemePolarNight) // prime the cache

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sm.Render(src, 100, ThemePolarNight)
	}
}

func BenchmarkStreamingMarkdownStreaming(b *testing.B) {
	content, err := os.ReadFile("testdata/render_test.md")
	if err != nil {
		b.Fatalf("failed to read test file: %v", err)
	}
	src := string(content)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sm := &StreamingMarkdown{}
		// Simulate streaming in 50-char chunks
		for j := 50; j <= len(src); j += 50 {
			sm.Render(src[:j], 100, ThemePolarNight)
		}
		sm.Render(src, 100, ThemePolarNight)
	}
}

func TestCompletedSemanticTableAlignmentWithEmoji(t *testing.T) {
	// Flag emoji originally exposed the split between Glamour measurement and
	// terminal cells; semantic layout must now keep one WidthProfile owner.
	content := "| Country | Flag |\n|---------|------|\n| US | 🇺🇸 |\n| JP | 🇯🇵 |\n| Plain | text |\n"

	sm := &StreamingMarkdown{}
	sm.Finalize(content)
	result := sm.Render(content, 80, ThemePolarNight)

	assertTableAlignment(t, result)
}

func TestCompletedSemanticTableAlignmentWithZWJ(t *testing.T) {
	// ZWJ sequences originally exposed rune-by-rune overestimation; semantic
	// layout must preserve each grapheme while keeping equal borders.
	content := "| Name | Icon | Desc |\n|------|------|------|\n| Family | 👨‍👩‍👧‍👦 | ZWJ |\n| Coder | 👩‍💻 | prof |\n| Plain | X | text |\n"

	sm := &StreamingMarkdown{}
	sm.Finalize(content)
	result := sm.Render(content, 60, ThemePolarNight)

	assertTableAlignment(t, result)
}

func TestCompletedSemanticTableAlignmentBareEmoji(t *testing.T) {
	// The semantic renderer and WidthProfile own bare-emoji geometry without
	// a post-render Glamour repair pass.
	content := "| Task | Status |\n|------|--------|\n| Build | 🏷 |\n| Test | ✅ |\n| Plain | ok |\n"

	sm := &StreamingMarkdown{}
	sm.Finalize(content)
	result := sm.Render(content, 60, ThemePolarNight)

	assertTableAlignment(t, result)
}

func assertTableAlignment(t *testing.T, result string) {
	t.Helper()
	lines := strings.Split(result, "\n")

	// Find table lines (contain │ or ┼ — glamour uses ┼ for separator)
	var tableLines []string
	for _, line := range lines {
		if strings.Contains(line, "│") || strings.Contains(line, "┼") {
			tableLines = append(tableLines, line)
		}
	}

	if len(tableLines) < 3 {
		t.Fatalf("expected at least 3 table lines, got %d\nfull output:\n%s", len(tableLines), result)
	}

	// All table lines must agree under the exact WidthProfile used by semantic
	// table allocation and padding.
	profile := DefaultDisplayCellProfile()
	var refWidth int
	for _, line := range tableLines {
		if strings.ContainsAny(line, "─━┼") {
			refWidth = profile.width(line)
			break
		}
	}

	if refWidth == 0 {
		t.Fatal("no separator line found in table output")
	}

	for i, line := range tableLines {
		w := profile.width(line)
		if w != refWidth {
			t.Errorf("table line %d width=%d, want %d\n  line: %q", i, w, refWidth, xansi.Strip(line))
		}
	}
}

// stripANSIForTest removes ANSI escape codes for content verification.
func stripANSIForTest(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			// Skip until 'm'
			i += 2
			for i < len(s) && s[i] != 'm' {
				i++
			}
			if i < len(s) {
				i++ // skip 'm'
			}
		} else {
			result.WriteByte(s[i])
			i++
		}
	}
	return result.String()
}
