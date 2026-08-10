//go:build unix

package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/creack/pty"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/internal/tui/terminalcap"
)

const p244GoalPTYHelperEnv = "YHC_P244_GOAL_PTY_HELPER"

func TestP244GoalWorkflowPTY(t *testing.T) {
	if os.Getenv(p244GoalPTYHelperEnv) == "1" {
		runP244GoalPTYHelper(t)
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestP244GoalWorkflowPTY$")
	command.Env = append(os.Environ(), p244GoalPTYHelperEnv+"=1")
	terminal, err := pty.StartWithSize(
		command,
		&pty.Winsize{Cols: 100, Rows: 28},
	)
	if err != nil {
		t.Fatalf("start Goal PTY: %v", err)
	}
	defer terminal.Close() //nolint:errcheck

	output := newLockedPTYOutput(100, 28)
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

	waitPTYContains(t, command, output, "goal active 0 active:0s")
	writePTY(t, terminal, "/goal pause\r")
	waitPTYContains(t, command, output, "goal paused 0 active:")
	writePTY(t, terminal, "/goal budget 24000\r")
	waitPTYContains(t, command, output, "goal paused 0/24.0k")
	writePTY(t, terminal, "\x04")

	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	select {
	case waitErr := <-waitDone:
		if waitErr != nil {
			t.Fatalf("Goal PTY helper failed: %v\n%s", waitErr, output.raw())
		}
	case <-time.After(15 * time.Second):
		_ = command.Process.Kill()
		<-waitDone
		t.Fatalf("Goal PTY helper timed out\n%s", output.raw())
	}
	_ = terminal.Close()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("Goal PTY reader did not finish")
	}

	raw := output.raw()
	for _, sequence := range []string{
		"\x1b[?1049h",
		"\x1b[?1049l",
		"\x1b[?2004h",
		"\x1b[?2004l",
		"\x1b[?25h",
	} {
		if !strings.Contains(raw, sequence) {
			t.Fatalf("Goal terminal output missing cleanup %q\n%q", sequence, raw)
		}
	}
	cleanupIndex := strings.LastIndex(raw, "\x1b[?1049l")
	restoredIndex := strings.LastIndex(raw, "P244_GOAL_PTY_RESTORED")
	if cleanupIndex < 0 || restoredIndex <= cleanupIndex {
		t.Fatalf(
			"Goal restore marker did not follow terminal cleanup: cleanup=%d marker=%d\n%q",
			cleanupIndex,
			restoredIndex,
			raw,
		)
	}
}

func runP244GoalPTYHelper(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:         "p24-4-goal-pty",
		ThreadID:          "p24-4-goal-pty",
		CWD:               dir,
		TranscriptDir:     filepath.Join(dir, "transcripts"),
		CommandEntrypoint: commands.EntrypointTUI,
		GoalCapability: &engine.GoalCapabilityConfig{
			Enabled: true,
		},
	})
	defer eng.Close()
	events, _ := eng.SubmitMessage(
		context.Background(),
		"/goal finish every accepted migration slice",
	)
	for range events {
	}

	caps := terminalcap.Capabilities{
		Platform:       "linux",
		Terminal:       "wezterm",
		Interactive:    true,
		FocusReporting: true,
		BracketedPaste: true,
	}
	app := New(Config{
		Engine:         eng,
		Model:          "test-model",
		Resumed:        true,
		Fullscreen:     true,
		ReducedMotion:  true,
		TerminalCaps:   &caps,
		StatusLineHook: func(left, _ string) (string, string) { return left, "p24.4" },
	})
	program := tea.NewProgram(app)
	app.SetProgram(program)
	if _, err := program.Run(); err != nil {
		t.Fatalf("run Goal TUI: %v", err)
	}
	fmt.Fprint(os.Stdout, "P244_GOAL_PTY_RESTORED")
}
