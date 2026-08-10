//go:build unix

package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/abietic/yhc/internal/tui/terminalcap"
)

const (
	p20R2FeedbackCursorPTYHelperEnv = "YHC_P20_R2_CURSOR_PTY_HELPER"
	p20R2FeedbackCursorPTYBegin     = "P20_R2_CURSOR_BEGIN"
	p20R2FeedbackCursorPTYEnd       = "P20_R2_CURSOR_END"
)

func TestP20R2FeedbackCursorPTY(t *testing.T) {
	if mode := os.Getenv(p20R2FeedbackCursorPTYHelperEnv); mode != "" {
		runP20R2FeedbackCursorPTYHelper(t, mode)
		return
	}

	for _, mode := range []string{"color", "no-color"} {
		t.Run(mode, func(t *testing.T) {
			command := exec.Command(
				os.Args[0],
				"-test.run=^TestP20R2FeedbackCursorPTY$",
			)
			command.Env = append(
				os.Environ(),
				p20R2FeedbackCursorPTYHelperEnv+"="+mode,
				"TERM=xterm-256color",
			)
			terminal, err := pty.StartWithSize(
				command,
				&pty.Winsize{Cols: 80, Rows: 24},
			)
			if err != nil {
				t.Fatalf("start Plan feedback cursor PTY: %v", err)
			}
			defer terminal.Close() //nolint:errcheck

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

			waitDone := make(chan error, 1)
			go func() { waitDone <- command.Wait() }()
			select {
			case waitErr := <-waitDone:
				if waitErr != nil {
					t.Fatalf(
						"Plan feedback cursor PTY helper failed: %v\n%s",
						waitErr,
						output.raw(),
					)
				}
			case <-time.After(15 * time.Second):
				_ = command.Process.Kill()
				<-waitDone
				t.Fatalf(
					"Plan feedback cursor PTY helper timed out\n%s",
					output.raw(),
				)
			}
			_ = terminal.Close()
			select {
			case <-readDone:
			case <-time.After(time.Second):
				t.Fatal("Plan feedback cursor PTY reader did not finish")
			}

			frame := p20R2FeedbackCursorPTYFrame(t, output.raw())
			switch mode {
			case "color":
				if !strings.Contains(frame, "\x1b[") {
					t.Fatalf("color PTY frame has no ANSI styling:\n%s", frame)
				}
				if strings.Contains(frame, planFeedbackNoColorCaret) {
					t.Fatalf(
						"color PTY frame used no-color caret projection:\n%s",
						frame,
					)
				}
			case "no-color":
				if strings.Contains(frame, "\x1b[") {
					t.Fatalf(
						"no-color PTY frame retained ANSI styling: %q",
						frame,
					)
				}
				if strings.Count(frame, planFeedbackNoColorCaret) != 1 ||
					!strings.Contains(
						frame,
						planFeedbackNoColorCaret+"Q current",
					) {
					t.Fatalf(
						"no-color PTY frame lacks one visible caret before current text:\n%s",
						frame,
					)
				}
			default:
				t.Fatalf("unexpected PTY mode %q", mode)
			}
		})
	}
}

func runP20R2FeedbackCursorPTYHelper(t *testing.T, mode string) {
	t.Helper()
	caps := terminalcap.Capabilities{
		Platform:    "linux",
		Terminal:    "xterm",
		Interactive: true,
		Color:       terminalcap.ColorANSI16,
	}
	switch mode {
	case "color":
	case "no-color":
		// Keep semantic styling enabled before App.finalizeView. ColorNone must
		// both select the literal caret and strip the complete final frame.
		caps.Color = terminalcap.ColorNone
	default:
		t.Fatalf("unsupported PTY helper mode %q", mode)
	}

	app := New(Config{
		Resumed:       true,
		ReducedMotion: true,
		TerminalCaps:  &caps,
	})
	app.width = 80
	app.height = 24
	app.updateLayout()
	app.planDialog.Show(
		"main",
		"p20-r2-pty",
		"",
		nil,
		make(chan PermissionResponse, 1),
	)
	app.planDialog.focus = planFocusFeedback
	app.planDialog.feedbackEditor.SetValue("Q current")
	setTextareaRuneCursor(&app.planDialog.feedbackEditor, 0)
	app.planDialog.feedbackEditor.Focus()
	p20R2SetStaticCursor(app.planDialog)
	app.pushDialog(StatePlanApproval)

	fmt.Printf(
		"%s\n%s\n%s\n",
		p20R2FeedbackCursorPTYBegin,
		app.renderView(),
		p20R2FeedbackCursorPTYEnd,
	)
}

func p20R2FeedbackCursorPTYFrame(t *testing.T, raw string) string {
	t.Helper()
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	start := strings.Index(normalized, p20R2FeedbackCursorPTYBegin+"\n")
	if start < 0 {
		t.Fatalf("PTY begin marker missing:\n%s", normalized)
	}
	start += len(p20R2FeedbackCursorPTYBegin) + 1
	end := strings.Index(normalized[start:], "\n"+p20R2FeedbackCursorPTYEnd)
	if end < 0 {
		t.Fatalf("PTY end marker missing:\n%s", normalized)
	}
	return normalized[start : start+end]
}
