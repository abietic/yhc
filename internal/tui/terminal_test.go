package tui

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/term"
)

func TestTerminalStateRestoreResetsEveryOwnedMode(t *testing.T) {
	var output bytes.Buffer
	restored := false
	state := &TerminalState{
		fd:     42,
		state:  &term.State{},
		output: &output,
		restoreFn: func(fd int, state *term.State) error {
			restored = fd == 42 && state != nil
			return nil
		},
	}
	state.Restore()
	if !restored {
		t.Fatal("terminal termios state was not restored")
	}
	for _, sequence := range []string{
		"\x1b[?1004l", "\x1b[?2004l", "\x1b[?1003l", "\x1b[?1002l",
		"\x1b[?1000l", "\x1b[?1006l", "\x1b[<u", "\x1b[>4;0m",
		"\x1b[?25h", "\x1b[0m", "\x1b[?1049l",
	} {
		if !strings.Contains(output.String(), sequence) {
			t.Fatalf("cleanup output missing %q: %q", sequence, output.String())
		}
	}
}

func TestPanicRecoveryRestoresAndRepanics(t *testing.T) {
	var output bytes.Buffer
	state := &TerminalState{output: &output}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		func() {
			defer PanicRecovery(state)
			panic("terminal panic")
		}()
	}()
	if recovered != "terminal panic" {
		t.Fatalf("PanicRecovery recovered %v, want original panic", recovered)
	}
	if output.String() != terminalCleanupSequences {
		t.Fatalf("panic cleanup = %q, want %q", output.String(), terminalCleanupSequences)
	}
}
