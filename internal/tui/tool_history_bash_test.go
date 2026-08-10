package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestBashHistoryRendererForegroundProjections(t *testing.T) {
	var output strings.Builder
	output.WriteString("$ go test ./...\n")
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&output, "output-%02d\n", i)
	}
	output.WriteString("[exit code: 1]\n")

	tool := &ToolMessage{
		toolCallID: "bash-1",
		name:       "Bash",
		input:      `{"command":"go test ./..."}`,
		output:     output.String(),
		status:     ToolSuccess,
		version:    1,
	}
	rich := stripANSIForTest(tool.Render(60, defaultStyles()))
	if strings.Count(rich, "go test ./...") != 1 {
		t.Fatalf("command should render once: %q", rich)
	}
	for _, want := range []string{"output-01", "output-04", "+4 lines", "output-09", "output-12", "exit 1"} {
		if !strings.Contains(rich, want) {
			t.Fatalf("rich output missing %q: %q", want, rich)
		}
	}
	if strings.Contains(rich, "output-05") || strings.Contains(rich, "[exit code: 1]") {
		t.Fatalf("rich output did not collapse/normalize markers: %q", rich)
	}
	assertHistoryLinesFit(t, tool.Render(60, defaultStyles()), 60)

	expanded := stripANSIForTest(tool.RenderExpanded(HistoryRenderContext{Width: 60, Styles: defaultStyles()}))
	if !strings.Contains(expanded, "output-05") || strings.Contains(expanded, "expand for details") {
		t.Fatalf("expanded output = %q", expanded)
	}
	transcript := tool.RenderTranscript(HistoryRenderContext{Width: 60, Styles: defaultStyles()})
	for _, want := range []string{"$ go test ./...", "output-05", "[exit code: 1]"} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript missing %q: %q", want, transcript)
		}
	}
	if strings.Contains(transcript, "\x1b[") {
		t.Fatalf("transcript contains ANSI: %q", transcript)
	}
}

func TestBashHistoryRendererMultilineCommandIsNotDuplicated(t *testing.T) {
	tool := &ToolMessage{
		name:    "Bash",
		input:   `{"command":"printf one\nprintf two"}`,
		output:  "$ printf one\nprintf two\none\ntwo\n",
		status:  ToolSuccess,
		version: 1,
	}
	rich := stripANSIForTest(tool.Render(80, defaultStyles()))
	if strings.Count(rich, "printf one") != 1 || strings.Count(rich, "printf two") != 1 {
		t.Fatalf("multiline command duplicated: %q", rich)
	}
	if !strings.Contains(rich, "one") || !strings.Contains(rich, "two") {
		t.Fatalf("command output missing: %q", rich)
	}
}

func TestBashHistoryRendererBackgroundLifecycle(t *testing.T) {
	start := &ToolMessage{
		name:   "Bash",
		input:  `{"command":"make watch","description":"watch tests","run_in_background":true}`,
		output: "Command started in background.\nShell ID: bg-123\nDescription: watch tests\nUse BashOutput with this ID to check output later.\n",
		status: ToolSuccess, version: 1,
	}
	rich := stripANSIForTest(start.Render(80, defaultStyles()))
	for _, want := range []string{"Shell", "start", "bg-123", "watch tests", "background"} {
		if !strings.Contains(rich, want) {
			t.Fatalf("background start missing %q: %q", want, rich)
		}
	}
	if strings.Contains(rich, "Use BashOutput") || strings.Contains(rich, "Command started") {
		t.Fatalf("background boilerplate leaked: %q", rich)
	}
	transcript := start.RenderTranscript(HistoryRenderContext{Width: 80, Styles: defaultStyles()})
	if !strings.Contains(transcript, "$ make watch") || !strings.Contains(transcript, "[background shell: bg-123]") {
		t.Fatalf("background transcript = %q", transcript)
	}

	output := &ToolMessage{
		name:   "BashOutput",
		input:  `{"bash_id":"bg-123","filter":"FAIL"}`,
		output: "Background shell: bg-123\nOutput:\nPASS one\nFAIL two\n",
		status: ToolSuccess, version: 1,
	}
	outputRich := stripANSIForTest(output.Render(80, defaultStyles()))
	for _, want := range []string{"Shell", "output", "bg-123", "filter \"FAIL\"", "PASS one", "FAIL two"} {
		if !strings.Contains(outputRich, want) {
			t.Fatalf("background output missing %q: %q", want, outputRich)
		}
	}

	stop := &ToolMessage{
		name: "KillShell", input: `{"shell_id":"bg-123"}`,
		output: `Shell "bg-123" terminated successfully.`, status: ToolSuccess, version: 1,
	}
	stopRich := stripANSIForTest(stop.Render(80, defaultStyles()))
	for _, want := range []string{"Shell", "stop", "bg-123", "stopped"} {
		if !strings.Contains(stopRich, want) {
			t.Fatalf("background stop missing %q: %q", want, stopRich)
		}
	}
	if strings.Contains(stopRich, "terminated successfully") {
		t.Fatalf("stop boilerplate leaked: %q", stopRich)
	}
}

func TestBashHistoryRendererRunningAndNoOutputStates(t *testing.T) {
	running := &ToolMessage{
		name: "Bash", input: `{"command":"sleep 10"}`,
		output: "waiting", status: ToolRunning, version: 4, spinnerCount: 3,
	}
	rich := stripANSIForTest(running.Render(48, defaultStyles()))
	if !strings.Contains(rich, "running") || !strings.Contains(rich, "waiting") {
		t.Fatalf("running render = %q", rich)
	}
	if running.HistoryAnimationVersion(7) != running.Version()+7 {
		t.Fatalf("running animation version = %d", running.HistoryAnimationVersion(7))
	}

	noOutput := &ToolMessage{
		name: "BashOutput", input: `{"bash_id":"bg-empty"}`,
		output: "Background shell: bg-empty\n(no new output captured)\n", status: ToolSuccess, version: 1,
	}
	plain := stripANSIForTest(noOutput.Render(48, defaultStyles()))
	if !strings.Contains(plain, "no new output") || !strings.Contains(plain, "bg-empty") {
		t.Fatalf("no-output render = %q", plain)
	}
}

func TestBashHistoryRendererMalformedInputAndNarrowWidth(t *testing.T) {
	tool := &ToolMessage{
		name: "Bash", input: `{not-json`, output: "failure detail", status: ToolError, version: 1,
	}
	rendered := tool.Render(24, defaultStyles())
	plain := stripANSIForTest(rendered)
	if !strings.Contains(plain, "Bash") || !strings.Contains(plain, "failed") {
		t.Fatalf("malformed render = %q", plain)
	}
	assertHistoryLinesFit(t, rendered, 24)
}

func TestToolHistoryRendererKeepsGenericFallback(t *testing.T) {
	if _, ok := toolHistoryRendererFor("Bash").(bashToolHistoryRenderer); !ok {
		t.Fatalf("Bash renderer = %T", toolHistoryRendererFor("Bash"))
	}
	if _, ok := toolHistoryRendererFor("CustomTool").(genericToolHistoryRenderer); !ok {
		t.Fatalf("CustomTool renderer = %T", toolHistoryRendererFor("CustomTool"))
	}
	custom := &ToolMessage{name: "CustomTool", input: `{"query":"example"}`, output: "line", status: ToolSuccess, version: 1}
	if plain := stripANSIForTest(custom.Render(60, defaultStyles())); !strings.Contains(plain, "CustomTool") || !strings.Contains(plain, "line") {
		t.Fatalf("generic fallback = %q", plain)
	}
}

func assertHistoryLinesFit(t *testing.T, rendered string, width int) {
	t.Helper()
	for i, line := range strings.Split(rendered, "\n") {
		if got := ansi.StringWidth(line); got > width {
			t.Fatalf("line %d width = %d, want <= %d: %q", i, got, width, ansi.Strip(line))
		}
	}
}
