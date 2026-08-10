package tui

import (
	"os"
	"strings"
	"testing"
)

func TestStreamingMarkdownCurrentOutputGolden(t *testing.T) {
	t.Parallel()

	chunks := []struct {
		name  string
		delta string
	}{
		{name: "heading", delta: "# Streaming title\n"},
		{name: "paragraph-tail", delta: "\nParagraph with **bold"},
		{name: "paragraph-complete", delta: "** text and [link](https://example.com).\n\n"},
		{name: "list-first", delta: "- first item\n"},
		{name: "list-complete", delta: "- second item\n\n"},
		{name: "table-header", delta: "| Name | State |\n"},
		{name: "table-shape", delta: "| --- | --- |\n"},
		{name: "table-row", delta: "| alpha | ready |\n"},
		{name: "fence-open", delta: "\n```go\n"},
		{name: "fence-body", delta: "fmt.Println(\"ok\")\n"},
		{name: "fence-close", delta: "```\n"},
		{name: "final-tail", delta: "\nDone."},
	}

	var source strings.Builder
	var golden strings.Builder
	stream := &StreamingMarkdown{}
	for _, chunk := range chunks {
		source.WriteString(chunk.delta)
		golden.WriteString("== " + chunk.name + " ==\n")
		golden.WriteString(normalizeStreamingGolden(stream.Render(source.String(), 52, ThemePolarNight)))
		golden.WriteString("\n")
	}

	want, err := os.ReadFile("testdata/streaming_markdown.golden")
	if err != nil {
		t.Fatalf("read streaming markdown golden: %v", err)
	}
	got := strings.TrimSpace(golden.String())
	if got != strings.TrimSpace(string(want)) {
		t.Fatalf("streaming markdown golden mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func normalizeStreamingGolden(rendered string) string {
	lines := strings.Split(stripANSIForTest(rendered), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}
