package tui

import (
	"fmt"
	"strings"
	"testing"
)

func TestEditHistoryRendererBoundedAndFullProjections(t *testing.T) {
	oldLines := make([]string, 40)
	newLines := make([]string, 40)
	for i := range oldLines {
		oldLines[i] = fmt.Sprintf("old-%02d", i+1)
		newLines[i] = oldLines[i]
	}
	for _, line := range []int{2, 12, 22, 32, 40} {
		newLines[line-1] = fmt.Sprintf("new-%02d", line)
	}
	input := editWriteHistoryInput{
		FilePath:  "/tmp/project/main.go",
		OldString: strings.Join(oldLines, "\n"),
		NewString: strings.Join(newLines, "\n"),
	}
	encoded := fmt.Sprintf(`{"file_path":%q,"old_string":%q,"new_string":%q}`, input.FilePath, input.OldString, input.NewString)
	tool := &ToolMessage{
		name: "Edit", input: encoded,
		output: "Replaced 1 occurrence in /tmp/project/main.go", status: ToolSuccess, version: 1,
	}
	rendered := tool.Render(80, defaultStyles())
	plain := stripANSIForTest(rendered)
	for _, want := range []string{"Edit", "+5 -5", "main.go", "diff lines", "new-40"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("edit rich missing %q: %q", want, plain)
		}
	}
	if got := len(strings.Split(rendered, "\n")); got > editWriteHistoryMaxRows+1 {
		t.Fatalf("edit rich rows = %d", got)
	}
	assertHistoryLinesFit(t, rendered, 80)

	expanded := stripANSIForTest(tool.RenderExpanded(HistoryRenderContext{Width: 80, Styles: defaultStyles()}))
	if strings.Contains(expanded, "diff lines") || !strings.Contains(expanded, "new-22") {
		t.Fatalf("edit expanded = %q", expanded)
	}
	transcript := tool.RenderTranscript(HistoryRenderContext{Width: 80, Styles: defaultStyles()})
	for _, want := range []string{"Edit /tmp/project/main.go", "new-22", "Result: Replaced", "[+5 -5]"} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("edit transcript missing %q: %q", want, transcript)
		}
	}
}

func TestWriteHistoryRendererShowsBoundedCreationDiff(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("content-%02d", i+1)
	}
	content := strings.Join(lines, "\n")
	input := fmt.Sprintf(`{"file_path":"/tmp/project/new.go","content":%q}`, content)
	tool := &ToolMessage{
		name: "Write", input: input,
		output: fmt.Sprintf("Wrote %d bytes to /tmp/project/new.go", len(content)), status: ToolSuccess, version: 1,
	}
	rich := stripANSIForTest(tool.Render(72, defaultStyles()))
	for _, want := range []string{"Write", "+20 -0", "new.go", "content-01", "content-20", "diff lines"} {
		if !strings.Contains(rich, want) {
			t.Fatalf("write rich missing %q: %q", want, rich)
		}
	}
	transcript := tool.RenderTranscript(HistoryRenderContext{Width: 72, Styles: defaultStyles()})
	if !strings.Contains(transcript, "content-10") || !strings.Contains(transcript, "[+20 -0]") {
		t.Fatalf("write transcript = %q", transcript)
	}
}

func TestEditWriteRendererMarksSemanticRejection(t *testing.T) {
	tool := &ToolMessage{
		name:   "Edit",
		input:  `{"file_path":"/tmp/a.go","old_string":"old","new_string":"new"}`,
		output: "File has not been read yet. Read it first before editing it.",
		status: ToolSuccess, version: 1,
	}
	rich := stripANSIForTest(tool.Render(72, defaultStyles()))
	for _, want := range []string{"Edit", "not applied", "a.go", "File has not been read", "- old", "+ new"} {
		if !strings.Contains(rich, want) {
			t.Fatalf("rejected edit missing %q: %q", want, rich)
		}
	}
	transcript := tool.RenderTranscript(HistoryRenderContext{Width: 72, Styles: defaultStyles()})
	if !strings.Contains(transcript, "[not applied]") || !strings.Contains(transcript, "Result: File has not been read") {
		t.Fatalf("rejected transcript = %q", transcript)
	}
}

func TestEditWriteRendererRunningMalformedAndNarrow(t *testing.T) {
	running := &ToolMessage{
		name: "Write", input: `{"file_path":"/tmp/a.go","content":"hello"}`,
		status: ToolRunning, version: 1,
	}
	runningPlain := stripANSIForTest(running.Render(40, defaultStyles()))
	if !strings.Contains(runningPlain, "running") || strings.Contains(runningPlain, "@@") {
		t.Fatalf("running write = %q", runningPlain)
	}

	malformed := &ToolMessage{name: "Edit", input: `{bad`, output: "parse failure", status: ToolError, version: 1}
	rendered := malformed.Render(24, defaultStyles())
	plain := stripANSIForTest(rendered)
	if !strings.Contains(plain, "Edit") || !strings.Contains(plain, "failed") || !strings.Contains(plain, "parse failure") {
		t.Fatalf("malformed edit = %q", plain)
	}
	assertHistoryLinesFit(t, rendered, 24)
	if _, ok := toolHistoryRendererFor("Edit").(editWriteToolHistoryRenderer); !ok {
		t.Fatalf("Edit renderer = %T", toolHistoryRendererFor("Edit"))
	}
}

func TestRenderStructuredDiffBoundedPreservesEnds(t *testing.T) {
	oldText := strings.Join([]string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}, "\n")
	newText := strings.Join([]string{"A", "b", "c", "D", "e", "f", "G", "h", "I"}, "\n")
	rendered := stripANSIForTest(renderStructuredDiffBounded(defaultStyles(), "", oldText, newText, 80, 7))
	if got := len(strings.Split(rendered, "\n")); got != 7 {
		t.Fatalf("bounded diff rows = %d: %q", got, rendered)
	}
	if !strings.Contains(rendered, "diff lines") || !strings.Contains(rendered, "A") || !strings.Contains(rendered, "I") {
		t.Fatalf("bounded diff = %q", rendered)
	}
}
