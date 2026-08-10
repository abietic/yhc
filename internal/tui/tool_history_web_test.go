package tui

import (
	"fmt"
	"strings"
	"testing"
)

func TestWebFetchHistoryRendererBoundedAndFullProjections(t *testing.T) {
	lines := make([]string, 0, 13)
	for i := 1; i <= 12; i++ {
		lines = append(lines, fmt.Sprintf("page-line-%02d", i))
	}
	lines = append(lines, "[Content truncated at 100KB]")
	output := strings.Join(lines, "\n")
	tool := &ToolMessage{
		name:   "WebFetch",
		input:  `{"url":"https://docs.example.com/guide?q=tui","prompt":"Extract renderer details","raw_mode":true}`,
		output: output, status: ToolSuccess, version: 1,
	}
	rendered := tool.Render(82, defaultStyles())
	plain := stripANSIForTest(rendered)
	for _, want := range []string{
		"Web", "fetched", "docs.example.com/guide?q=tui", "raw", "truncated",
		"page-line-01", "page-line-04", "+6 lines", "page-line-12", "Content truncated",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("WebFetch rich missing %q: %q", want, plain)
		}
	}
	if strings.Contains(plain, "page-line-06") {
		t.Fatalf("WebFetch rich was not bounded: %q", plain)
	}
	assertHistoryLinesFit(t, rendered, 82)

	expanded := stripANSIForTest(tool.RenderExpanded(HistoryRenderContext{Width: 64, Styles: defaultStyles()}))
	for _, want := range []string{"Input:", "Extract renderer details", "Result:", "page-line-06", "Content truncated"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("WebFetch expanded missing %q: %q", want, expanded)
		}
	}
	assertHistoryLinesFit(t, tool.RenderExpanded(HistoryRenderContext{Width: 64, Styles: defaultStyles()}), 64)
	transcript := tool.RenderTranscript(HistoryRenderContext{})
	if !strings.Contains(transcript, "docs.example.com") || !strings.Contains(transcript, "page-line-06") || !strings.Contains(transcript, "Status: completed") {
		t.Fatalf("WebFetch transcript = %q", transcript)
	}
}

func TestWebFetchHistoryRendererRedirectStructuredAndFallback(t *testing.T) {
	redirected := &ToolMessage{
		name: "WebFetch", input: `{"url":"https://short.example/a","prompt":"Read it"}`,
		output: `The URL redirected to a different host.

Original URL: https://short.example/a
Redirect URL: https://docs.example.com/final

Please make a new request with the redirect URL to fetch the content.`,
		status: ToolSuccess, version: 1,
	}
	redirectPlain := stripANSIForTest(redirected.Render(78, defaultStyles()))
	for _, want := range []string{"redirected", "short.example/a", "Redirect: docs.example.com/final", "Fetch the redirect URL explicitly"} {
		if !strings.Contains(redirectPlain, want) {
			t.Fatalf("redirect WebFetch missing %q: %q", want, redirectPlain)
		}
	}
	if strings.Contains(redirectPlain, "Please make a new request") {
		t.Fatalf("redirect boilerplate leaked: %q", redirectPlain)
	}

	structured := &ToolMessage{
		name: "WebFetch", input: `{"url":"https://api.example.com/data","prompt":"Summarize"}`,
		output: `{"bytes":2048,"code":200,"codeText":"OK","result":"summary line one\nsummary line two"}`,
		status: ToolSuccess, version: 1,
	}
	structuredPlain := stripANSIForTest(structured.Render(88, defaultStyles()))
	for _, want := range []string{"fetched", "api.example.com/data", "AI", "2.0 KB", "200 OK", "summary line one"} {
		if !strings.Contains(structuredPlain, want) {
			t.Fatalf("structured WebFetch missing %q: %q", want, structuredPlain)
		}
	}

	fallback := &ToolMessage{
		name: "WebFetch", input: `{"url":"https://page.example.com","prompt":"Analyze"}`,
		output: `Content fetched from: https://page.example.com

(AI processing failed: side model timeout)

--- Page Content ---

actual page content`,
		status: ToolSuccess, version: 1,
	}
	fallbackPlain := stripANSIForTest(fallback.Render(82, defaultStyles()))
	for _, want := range []string{"AI fallback", "AI processing failed: side model timeout", "actual page content"} {
		if !strings.Contains(fallbackPlain, want) {
			t.Fatalf("fallback WebFetch missing %q: %q", want, fallbackPlain)
		}
	}
	if strings.Contains(fallbackPlain, "Content fetched from:") || strings.Contains(fallbackPlain, "Page Content") {
		t.Fatalf("fallback WebFetch leaked wrapper: %q", fallbackPlain)
	}
}

func TestParseStructuredWebFetchHistoryResultBoundsStatusCode(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   int
	}{
		{name: "lower HTTP boundary", output: `{"code":100}`, want: 100},
		{name: "numeric success", output: `{"code":200}`, want: 200},
		{name: "string failure", output: `{"code":"404"}`, want: 404},
		{name: "upper HTTP boundary", output: `{"code":599}`, want: 599},
		{name: "below HTTP range", output: `{"code":99}`},
		{name: "above HTTP range", output: `{"code":600}`},
		{name: "above 32 bit", output: `{"code":2147483648}`},
		{name: "maximum int64", output: `{"code":9223372036854775807}`},
		{name: "negative", output: `{"code":-1}`},
		{name: "fractional", output: `{"code":200.5}`},
		{name: "invalid string", output: `{"code":"not-a-code"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseWebFetchHistoryResult(tt.output)
			if got.code != tt.want {
				t.Fatalf("code = %d, want %d", got.code, tt.want)
			}
		})
	}
}

func TestWebFetchHistoryRendererPreservesValidFailureStatus(t *testing.T) {
	tool := &ToolMessage{
		name: "WebFetch", input: `{"url":"https://api.example.com/missing"}`,
		output: `{"code":404,"codeText":"Not Found","result":"missing"}`,
		status: ToolSuccess, version: 1,
	}
	plain := stripANSIForTest(tool.Render(80, defaultStyles()))
	for _, want := range []string{"failed", "404 Not Found", "missing"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("structured failure missing %q: %q", want, plain)
		}
	}
}

func TestWebFetchHistoryRendererFailureSanitizesRemoteANSI(t *testing.T) {
	tool := &ToolMessage{
		name: "WebFetch", input: `{bad`,
		output: "\x1b[31mwebfetch: failed to fetch: HTTP 503\x1b[0m",
		status: ToolError, version: 1,
	}
	rendered := tool.Render(34, defaultStyles())
	plain := stripANSIForTest(rendered)
	if !strings.Contains(plain, "Web") || !strings.Contains(plain, "failed") ||
		!strings.Contains(plain, "webfetch: failed") || !strings.Contains(plain, "content clipped") {
		t.Fatalf("failed WebFetch = %q", plain)
	}
	assertHistoryLinesFit(t, rendered, 34)
	transcript := tool.RenderTranscript(HistoryRenderContext{})
	if strings.Contains(transcript, "\x1b[") || !strings.Contains(transcript, "HTTP 503") {
		t.Fatalf("WebFetch transcript ANSI handling = %q", transcript)
	}
}

func TestWebSearchHistoryRendererSourcesAndFilters(t *testing.T) {
	results := make([]string, 0, 7)
	for i := 1; i <= 7; i++ {
		results = append(results, fmt.Sprintf("%d. [Source %d](https://docs%d.example.com/page)\n   Snippet %d", i, i, i, i))
	}
	output := "Search results for: modern coding agent TUI\n\n" + strings.Join(results, "\n\n")
	tool := &ToolMessage{
		name:   "WebSearch",
		input:  `{"query":"modern coding agent TUI","allowed_domains":["example.com"],"blocked_domains":["ads.example.com"]}`,
		output: output, status: ToolSuccess, version: 1,
	}
	rendered := tool.Render(118, defaultStyles())
	plain := stripANSIForTest(rendered)
	for _, want := range []string{
		"Web", "searched", "modern coding agent TUI", "7 results", "only example.com", "blocked ads.example.com",
		"1. Source 1", "docs1.example.com", "5. Source 5", "+2 results",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("WebSearch rich missing %q: %q", want, plain)
		}
	}
	if strings.Contains(plain, "Source 6") || strings.Contains(plain, "Snippet") {
		t.Fatalf("WebSearch rich was not source-bounded: %q", plain)
	}
	assertHistoryLinesFit(t, rendered, 118)

	transcript := tool.RenderTranscript(HistoryRenderContext{})
	if !strings.Contains(transcript, "https://docs7.example.com/page") || !strings.Contains(transcript, "Snippet 7") {
		t.Fatalf("WebSearch transcript = %q", transcript)
	}
}

func TestWebSearchHistoryRendererStructuredAndNoResults(t *testing.T) {
	hits := make([]string, 0, 6)
	for i := 1; i <= 6; i++ {
		hits = append(hits, fmt.Sprintf(`{"title":"Result %d","url":"https://source%d.example.org/doc"}`, i, i))
	}
	output := `{"query":"semantic history","results":[{"tool_use_id":"search-1","content":[` + strings.Join(hits, ",") + `]},"search commentary"],"durationSeconds":0.25}`
	tool := &ToolMessage{name: "WebSearch", input: `{}`, output: output, status: ToolSuccess, version: 1}
	plain := stripANSIForTest(tool.Render(88, defaultStyles()))
	for _, want := range []string{"semantic history", "6 results", "250ms", "Result 1", "+1 results"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("structured WebSearch missing %q: %q", want, plain)
		}
	}
	if strings.Contains(plain, "Result 6") {
		t.Fatalf("structured WebSearch was not bounded: %q", plain)
	}
	expanded := stripANSIForTest(tool.RenderExpanded(HistoryRenderContext{Width: 64, Styles: defaultStyles()}))
	if !strings.Contains(expanded, "Result 6") || !strings.Contains(expanded, "source6.example.org") {
		t.Fatalf("structured WebSearch expanded = %q", expanded)
	}

	noResults := &ToolMessage{
		name: "WebSearch", input: `{"query":"impossible query","allowed_domains":["docs.example.com"]}`,
		output: "No search results found for query: impossible query", status: ToolSuccess, version: 1,
	}
	noResultsPlain := stripANSIForTest(noResults.Render(76, defaultStyles()))
	if !strings.Contains(noResultsPlain, "no results") || !strings.Contains(noResultsPlain, "No search results matched") {
		t.Fatalf("no-result WebSearch = %q", noResultsPlain)
	}
}

func TestWebHistoryRendererRegistrationAndFallback(t *testing.T) {
	for _, name := range []string{"WebFetch", "WebSearch"} {
		if _, ok := toolHistoryRendererFor(name).(webToolHistoryRenderer); !ok {
			t.Fatalf("%s renderer = %T", name, toolHistoryRendererFor(name))
		}
	}
	if _, ok := toolHistoryRendererFor("WebArchive").(genericToolHistoryRenderer); !ok {
		t.Fatalf("unknown web renderer = %T", toolHistoryRendererFor("WebArchive"))
	}
}
