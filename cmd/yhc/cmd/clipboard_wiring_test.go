package cmd

import (
	"os"
	"strings"
	"testing"
)

func TestP273RunTUIInjectsExistingTerminalOutputIntoClipboardService(t *testing.T) {
	sourceBytes, err := os.ReadFile("root.go")
	if err != nil {
		t.Fatalf("read root.go: %v", err)
	}
	source := string(sourceBytes)
	outputIndex := strings.Index(
		source,
		"terminalOutput, err := tui.NewTerminalOutput(os.Stdout)",
	)
	injectionIndex := strings.Index(
		source,
		"app.SetClipboardService(tui.NewClipboardService(ctx, terminalOutput))",
	)
	programIndex := strings.Index(source, "p := tea.NewProgram(app, options...)")
	runIndex := strings.Index(source, "err = runTUIProgram(p, terminalOutput")
	if outputIndex < 0 || injectionIndex < 0 || programIndex < 0 || runIndex < 0 {
		t.Fatalf(
			"runTUI clipboard wiring is incomplete: output=%d injection=%d program=%d run=%d",
			outputIndex,
			injectionIndex,
			programIndex,
			runIndex,
		)
	}
	if outputIndex >= injectionIndex ||
		injectionIndex >= programIndex ||
		programIndex >= runIndex {
		t.Fatalf(
			"runTUI clipboard wiring order is unsafe: output=%d injection=%d program=%d run=%d",
			outputIndex,
			injectionIndex,
			programIndex,
			runIndex,
		)
	}
	if got := strings.Count(
		source,
		"tui.NewTerminalOutput(os.Stdout)",
	); got != 1 {
		t.Fatalf("production TerminalOutput constructors = %d, want 1", got)
	}
}
