//go:build unix && !race

package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/internal/tui/terminalcap"
)

const (
	p203PlanEditorPTYHelperEnv = "YHC_P20_3_PLAN_EDITOR_PTY_HELPER"
	// Starting the external editor competes with every package when make test
	// runs with coverage. The ready marker remains the synchronization point;
	// this bound is process-start allowance, not a product latency budget.
	p203PlanEditorStartTimeout = 20 * time.Second
)

func TestP203PlanEditorRoundTripPTY(t *testing.T) {
	if os.Getenv(p203PlanEditorPTYHelperEnv) == "1" {
		runP203PlanEditorPTYHelper(t)
		return
	}

	editorScript := filepath.Join(t.TempDir(), "fake-vim")
	script := `#!/bin/sh
for target do :; done
printf '\033[?1049hFAKE_VIM_READY\n'
printf '\n- Edited by fake Vim\n' >> "$target"
IFS= read -r _
printf '\033[?1049lFAKE_VIM_DONE\n'
`
	if err := os.WriteFile(editorScript, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestP203PlanEditorRoundTripPTY$")
	command.Env = append(
		os.Environ(),
		p203PlanEditorPTYHelperEnv+"=1",
		"VISUAL="+editorScript+" --wait",
		"EDITOR="+filepath.Join(t.TempDir(), "ignored-editor"),
		"TERM=xterm-256color",
	)
	terminal, err := pty.StartWithSize(
		command,
		&pty.Winsize{Cols: 80, Rows: 24},
	)
	if err != nil {
		t.Fatalf("start Plan editor PTY: %v", err)
	}
	defer terminal.Close() //nolint:errcheck
	t.Cleanup(func() {
		if command.ProcessState != nil {
			return
		}
		_ = command.Process.Kill()
		_ = command.Wait()
	})

	output := newLockedPTYOutput(80, 24)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buffer := make([]byte, 8192)
		for {
			count, readErr := terminal.Read(buffer)
			if count > 0 {
				output.append(buffer[:count])
			}
			if readErr != nil {
				return
			}
		}
	}()

	waitPTYContains(t, command, output, "Ready to implement?")
	waitPTYContains(t, command, output, "Review · 4-")

	firstEditorMark := output.size()
	writePTY(t, terminal, "\x07")
	waitPTYContainsAfterWithin(
		t,
		command,
		output,
		firstEditorMark,
		"FAKE_VIM_READY",
		p203PlanEditorStartTimeout,
	)
	if err := pty.Setsize(
		terminal,
		&pty.Winsize{Cols: 100, Rows: 28},
	); err != nil {
		t.Fatalf("resize while fake Vim is active: %v", err)
	}
	output.setSize(100, 28)
	writePTY(t, terminal, "q\n")
	waitPTYContainsAfter(t, command, output, firstEditorMark, "Review · 4-16/83")
	waitP203RawContainsAfter(
		t,
		command,
		output,
		firstEditorMark,
		"\x1b[?1002h",
	)
	waitP203RawContainsAfter(
		t,
		command,
		output,
		firstEditorMark,
		"\x1b[?1004h",
	)
	waitP203RawContainsAfter(
		t,
		command,
		output,
		firstEditorMark,
		"\x1b[?1049h",
	)

	selectionMark := output.size()
	writePTY(t, terminal, "\t")
	waitPTYContainsAfter(t, command, output, selectionMark, "❯ 3.")
	writePTY(t, terminal, "\x1b[B")
	waitPTYContainsAfter(t, command, output, selectionMark, "❯ 4.")

	pageMark := output.size()
	writePTY(t, terminal, "\x1b[6~")
	waitPTYContainsAfter(t, command, output, pageMark, "Actions · 16-")
	writePTY(t, terminal, "\x1b[5~")
	waitPTYContainsAfter(t, command, output, pageMark, "Actions · 4-")

	mouseMark := output.size()
	writePTY(t, terminal, "\t")
	waitPTYContainsAfter(t, command, output, mouseMark, "Review · 4-")
	writePTY(t, terminal, "\x1b[<65;10;8M")
	waitPTYContainsAfter(t, command, output, mouseMark, "Review · 7-")

	secondEditorMark := output.size()
	writePTY(t, terminal, "\x07")
	waitPTYContainsAfterWithin(
		t,
		command,
		output,
		secondEditorMark,
		"FAKE_VIM_READY",
		p203PlanEditorStartTimeout,
	)
	writePTY(t, terminal, "q\n")
	waitPTYContainsAfter(t, command, output, secondEditorMark, "Review · 7-19/84")
	waitP203RawContainsAfter(
		t,
		command,
		output,
		secondEditorMark,
		"\x1b[?1002h",
	)
	waitP203RawContainsAfter(
		t,
		command,
		output,
		secondEditorMark,
		"\x1b[?1004h",
	)

	feedbackMark := output.size()
	writePTY(t, terminal, "\t")
	writePTY(t, terminal, "\r")
	waitPTYContainsAfter(t, command, output, feedbackMark, "keep rollback notes")
	feedbackRaw := output.raw()
	caretVisible, caretEvidence := p20R3FeedbackCaretVisible(
		feedbackRaw[feedbackMark:],
		100,
		28,
	)
	if !caretVisible {
		t.Fatalf(
			"Plan feedback caret is not visible after terminal reacquisition: %s\n%s",
			caretEvidence,
			output.raw(),
		)
	}
	actionsReturnMark := output.size()
	writePTY(t, terminal, "\x1b")
	waitPTYContainsAfter(t, command, output, actionsReturnMark, "Actions")

	confirmationMark := output.size()
	writePTY(t, terminal, "\x1b[A")
	writePTY(t, terminal, "\r")
	waitPTYContainsAfter(
		t,
		command,
		output,
		confirmationMark,
		"Bypass permissions disables all tool permission checks.",
	)
	waitPTYContainsAfter(
		t,
		command,
		output,
		confirmationMark,
		"❯ No, return to actions",
	)
	yesMark := output.size()
	writePTY(t, terminal, "\t")
	waitPTYContainsAfter(
		t,
		command,
		output,
		yesMark,
		"❯ Yes, bypass permissions",
	)
	writePTY(t, terminal, "\x1b")
	waitPTYContainsAfter(t, command, output, yesMark, "Actions")

	closeMark := output.size()
	writePTY(t, terminal, "\x1b")
	waitPTYContainsAfter(t, command, output, closeMark, "Ask anything")
	writePTY(t, terminal, "\x04")

	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	select {
	case waitErr := <-waitDone:
		if waitErr != nil {
			t.Fatalf("Plan editor PTY helper failed: %v\n%s", waitErr, output.raw())
		}
	case <-time.After(15 * time.Second):
		_ = command.Process.Kill()
		<-waitDone
		t.Fatalf("Plan editor PTY helper timed out\n%s", output.raw())
	}
	_ = terminal.Close()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("Plan editor PTY reader did not finish")
	}
	if !strings.Contains(output.raw(), "P20_3_PLAN_EDITOR_HELPER_RESTORED") {
		t.Fatalf("Plan editor helper restore marker missing\n%s", output.raw())
	}
}

func runP203PlanEditorPTYHelper(t *testing.T) {
	t.Helper()
	planPath := filepath.Join(t.TempDir(), "计划 with space.md")
	var plan strings.Builder
	plan.WriteString("# PTY external editor plan\n\n")
	for index := range 80 {
		plan.WriteString("- review step ")
		plan.WriteString(strings.Repeat("x", index%7))
		plan.WriteByte('\n')
	}
	planBytes := []byte(plan.String())
	if err := os.WriteFile(planPath, planBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	caps := terminalcap.Capabilities{
		Platform:       "linux",
		Terminal:       "wezterm",
		Interactive:    true,
		FocusReporting: true,
		Mouse:          true,
		BracketedPaste: true,
		Color:          terminalcap.ColorANSI16,
	}
	app := New(Config{
		Resumed:       true,
		Fullscreen:    true,
		MouseEnabled:  true,
		ReducedMotion: true,
		TerminalCaps:  &caps,
	})
	app.enqueueThreadAttention(threadAttentionRequest{
		ID: "p20-3-pty-attention", ThreadID: app.activeThreadViewID(),
		AgentID: "agent", Kind: threadAttentionPlan, Tool: "ExitPlanMode",
		SessionID: "session", Source: "callback",
		responseCh: make(chan PermissionResponse, 1),
		PlanApproval: &engine.PlanApprovalRequest{
			RequestID:         "p20-3-pty-request",
			PlanRevision:      9,
			PlanFileIdentity:  planPath,
			InitialPlanDigest: engine.PlanBytesDigest(planBytes),
			ReturnMode:        permission.ModeDefault,
		},
	})
	app.planDialog.Overlay("", 80, 24)
	app.planDialog.focus = planFocusReview
	app.planDialog.selectedIdx = 2
	app.planDialog.viewport.offset = 3
	app.planDialog.feedbackEditor.SetValue("keep rollback notes")
	setTextareaRuneCursor(&app.planDialog.feedbackEditor, 5)
	app.planDialog.feedbackUndo = []textEditorSnapshot{{
		Text: "previous rollback notes", CursorOffset: 3,
	}}

	program := tea.NewProgram(
		app,
	)
	app.SetProgram(program)
	if _, err := program.Run(); err != nil {
		t.Fatalf("run Plan editor PTY helper: %v", err)
	}
	_, _ = os.Stdout.WriteString("P20_3_PLAN_EDITOR_HELPER_RESTORED")
}

func p20R3FeedbackCaretVisible(
	raw string,
	width, height int,
) (bool, string) {
	emulator := vt.NewEmulator(width, height)
	if _, err := emulator.WriteString(raw); err != nil {
		return false, err.Error()
	}
	needle := []rune("keep rollback notes")
	for y := 0; y < height; y++ {
		for x := 0; x+len(needle) <= width; x++ {
			matched := true
			for index, expected := range needle {
				cell := emulator.CellAt(x+index, y)
				if cell == nil || cell.Content != string(expected) {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
			caret := emulator.CellAt(x+5, y)
			before := emulator.CellAt(x+4, y)
			if caret == nil || before == nil || caret.Content != "r" {
				return false, "feedback text matched without expected cursor cell"
			}
			caretForeground, caretBackground := caret.Style.Fg, caret.Style.Bg
			beforeForeground, beforeBackground := before.Style.Fg, before.Style.Bg
			if caret.Style.Attrs&uv.AttrReverse != 0 {
				caretForeground, caretBackground = caretBackground, caretForeground
			}
			if before.Style.Attrs&uv.AttrReverse != 0 {
				beforeForeground, beforeBackground = beforeBackground, beforeForeground
			}
			visible := caret.Style.Attrs&uv.AttrReverse != 0 ||
				(!p20R2SameColor(caretBackground, beforeBackground) &&
					!p20R2SameColor(caretForeground, beforeForeground))
			return visible, fmt.Sprintf(
				"before attrs=%v fg=%s bg=%s; caret attrs=%v fg=%s bg=%s",
				before.Style.Attrs,
				p20R2ColorString(beforeForeground),
				p20R2ColorString(beforeBackground),
				caret.Style.Attrs,
				p20R2ColorString(caretForeground),
				p20R2ColorString(caretBackground),
			)
		}
	}
	return false, "feedback text not found in final terminal cells"
}

func waitP203RawContainsAfter(
	t *testing.T,
	command *exec.Cmd,
	output *lockedPTYOutput,
	offset int,
	needle string,
) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		raw := output.raw()
		if offset >= 0 && offset <= len(raw) &&
			strings.Contains(raw[offset:], needle) {
			return
		}
		if command.ProcessState != nil && command.ProcessState.Exited() {
			t.Fatalf(
				"Plan editor helper exited before raw %q\n%s",
				needle,
				raw,
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf(
		"wait for Plan editor raw output %q timed out\n%s",
		needle,
		output.raw(),
	)
}
