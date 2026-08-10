package tui

import (
	"fmt"
	"strings"
	"testing"
)

func TestReadHistoryRendererProjections(t *testing.T) {
	var output strings.Builder
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&output, "%6d→line-%02d\n", i, i)
	}
	tool := &ToolMessage{
		toolCallID: "read-1",
		name:       "Read",
		input:      `{"file_path":"/tmp/project/main.go","offset":10,"limit":12}`,
		output:     output.String(),
		status:     ToolSuccess,
		version:    1,
	}
	richRendered := tool.Render(72, defaultStyles())
	rich := stripANSIForTest(richRendered)
	for _, want := range []string{"Read", "12 lines", "main.go", "offset 10", "limit 12", "line-01", "+6 lines", "line-12"} {
		if !strings.Contains(rich, want) {
			t.Fatalf("read rich missing %q: %q", want, rich)
		}
	}
	if strings.Contains(rich, "line-04") {
		t.Fatalf("read rich did not collapse middle: %q", rich)
	}
	assertHistoryLinesFit(t, richRendered, 72)

	expanded := stripANSIForTest(tool.RenderExpanded(HistoryRenderContext{Width: 72, Styles: defaultStyles()}))
	if !strings.Contains(expanded, "line-04") || strings.Contains(expanded, "expand for details") {
		t.Fatalf("read expanded = %q", expanded)
	}
	transcript := tool.RenderTranscript(HistoryRenderContext{Width: 72, Styles: defaultStyles()})
	for _, want := range []string{"Read /tmp/project/main.go", "line-04", "[12 lines]"} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("read transcript missing %q: %q", want, transcript)
		}
	}
}

func TestGrepHistoryRendererCountsAndHeadTail(t *testing.T) {
	lines := make([]string, 12)
	for i := range lines {
		lines[i] = fmt.Sprintf("pkg/file%d.go:%d:TODO item", i+1, i+10)
	}
	tool := &ToolMessage{
		name:   "Grep",
		input:  `{"pattern":"TODO","path":"/tmp/project","glob":"*.go","output_mode":"content"}`,
		output: strings.Join(lines, "\n") + "\n\n[Showing results with pagination = limit: 12]",
		status: ToolSuccess, version: 1,
	}
	rich := stripANSIForTest(tool.Render(76, defaultStyles()))
	for _, want := range []string{"Grep", "12 matches+", `"TODO"`, "project", "*.go", "file1.go", "+6 lines", "file12.go"} {
		if !strings.Contains(rich, want) {
			t.Fatalf("grep rich missing %q: %q", want, rich)
		}
	}
	if strings.Contains(rich, "file5.go") {
		t.Fatalf("grep rich did not collapse middle: %q", rich)
	}
	transcript := tool.RenderTranscript(HistoryRenderContext{Width: 76, Styles: defaultStyles()})
	if !strings.Contains(transcript, `Grep "TODO" in /tmp/project`) || !strings.Contains(transcript, "file5.go") {
		t.Fatalf("grep transcript = %q", transcript)
	}
}

func TestGlobHistoryRendererCountsAndNoResults(t *testing.T) {
	paths := make([]string, 12)
	for i := range paths {
		paths[i] = fmt.Sprintf("/tmp/project/pkg/file%02d.go", i+1)
	}
	tool := &ToolMessage{
		name: "Glob", input: `{"pattern":"**/*.go","path":"/tmp/project"}`,
		output: strings.Join(paths, "\n") + "\n(Results are truncated. Consider using a more specific path or pattern.)",
		status: ToolSuccess, version: 1,
	}
	rich := stripANSIForTest(tool.Render(72, defaultStyles()))
	for _, want := range []string{"Glob", "12 files+", `"**/*.go"`, "file01.go", "file12.go"} {
		if !strings.Contains(rich, want) {
			t.Fatalf("glob rich missing %q: %q", want, rich)
		}
	}

	empty := &ToolMessage{
		name: "Grep", input: `{"pattern":"missing","output_mode":"content"}`,
		output: "No matches found", status: ToolSuccess, version: 1,
	}
	emptyRich := stripANSIForTest(empty.Render(48, defaultStyles()))
	if !strings.Contains(emptyRich, "0 matches") || !strings.Contains(emptyRich, "No matches found") {
		t.Fatalf("empty grep = %q", emptyRich)
	}
}

func TestReadSearchToolGroupSummaryAndNestedIdentity(t *testing.T) {
	chat := NewChatView(defaultStyles())
	chat.AppendToolStart("read-group", "Read", `{"file_path":"/tmp/a.go"}`)
	chat.UpdateToolResult("read-group", "Read", "     1→package a\n")
	chat.AppendToolStart("grep-group", "Grep", `{"pattern":"TODO","path":"/tmp"}`)
	chat.UpdateToolResult("grep-group", "Grep", "Found 1 file\n/tmp/a.go")

	items := chat.Items()
	if len(items) != 1 {
		t.Fatalf("group item count = %d", len(items))
	}
	group, ok := items[0].(*ToolGroupMessage)
	if !ok {
		t.Fatalf("group item = %T", items[0])
	}
	plain := stripANSIForTest(group.Render(80, defaultStyles()))
	for _, want := range []string{"Explore", "2 operations", "1 read", "1 search"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("group summary missing %q: %q", want, plain)
		}
	}
	children := group.NestedHistoryItems()
	if len(children) != 2 || children[0].ID() == children[1].ID() {
		t.Fatalf("group children = %#v", children)
	}
	chat.ToggleExpand()
	expanded := stripANSIForTest(chat.RenderAllExpanded(80))
	if !strings.Contains(expanded, "package a") || !strings.Contains(expanded, "/tmp/a.go") {
		t.Fatalf("expanded group = %q", expanded)
	}
}

func TestReadSearchRendererMalformedAndNarrow(t *testing.T) {
	tool := &ToolMessage{name: "Grep", input: `{bad`, output: "failure", status: ToolError, version: 1}
	rendered := tool.Render(24, defaultStyles())
	plain := stripANSIForTest(rendered)
	if !strings.Contains(plain, "Grep") || !strings.Contains(plain, "failed") {
		t.Fatalf("malformed search = %q", plain)
	}
	assertHistoryLinesFit(t, rendered, 24)
	if _, ok := toolHistoryRendererFor("Read").(readSearchToolHistoryRenderer); !ok {
		t.Fatalf("Read renderer = %T", toolHistoryRendererFor("Read"))
	}
}
