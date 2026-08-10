package tui

import (
	"strings"
	"testing"
)

func BenchmarkStreamingMarkdownRender(b *testing.B) {
	content := strings.Repeat("Here is some **bold** text and `code` and a [link](http://example.com).\n\n", 50)
	sm := &StreamingMarkdown{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sm.Render(content, 80, ThemePolarNight)
	}
}

func BenchmarkStreamingMarkdownIncremental(b *testing.B) {
	base := "Here is a paragraph with **markdown** formatting.\n\n```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```\n\n"
	content := strings.Repeat(base, 10)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sm := &StreamingMarkdown{}
		// Simulate incremental streaming by growing content
		for j := 1; j <= len(content); j += 50 {
			end := j
			if end > len(content) {
				end = len(content)
			}
			sm.Render(content[:end], 80, ThemePolarNight)
		}
	}
}

func BenchmarkStreamingMarkdownPlainText(b *testing.B) {
	content := strings.Repeat("This is plain text without any markdown syntax at all. Just regular sentences.\n", 100)
	sm := &StreamingMarkdown{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sm.Render(content, 80, ThemePolarNight)
	}
}

func BenchmarkStreamingMarkdownStablePrefixActiveTable(b *testing.B) {
	stable := strings.Repeat(
		"Stable paragraph with **markdown** and a [link](https://example.com).\n\n",
		100,
	)
	tails := []string{
		"| Key | Value |\n| --- | --- |\n| active | one |\n",
		"| Key | Value |\n| --- | --- |\n| active | two |\n",
	}
	stream := &StreamingMarkdown{}
	stream.Render(stable+tails[0], 80, ThemePolarNight)

	b.ReportMetric(float64(len(stable)), "stable-bytes")
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		stream.Render(stable+tails[index%len(tails)], 80, ThemePolarNight)
	}
}
