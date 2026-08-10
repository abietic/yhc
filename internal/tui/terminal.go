package tui

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// TerminalState holds the saved terminal state for restoration on panic/crash.
type TerminalState struct {
	fd        int
	state     *term.State
	output    io.Writer
	restoreFn func(int, *term.State) error
}

const terminalCleanupSequences = "\x1b[?1004l" + // focus reporting
	"\x1b[?2004l" + // bracketed paste
	"\x1b[?1003l\x1b[?1002l\x1b[?1000l\x1b[?1006l" + // mouse modes
	"\x1b[<u\x1b[>4;0m" + // Kitty keyboard and modifyOtherKeys
	"\x1b[?25h\x1b[0m\x1b[?1049l" // cursor, attributes, alternate screen

// SaveTerminalState captures the current terminal state so it can be restored
// if the program panics or exits abnormally while in raw/alternate-screen mode.
func SaveTerminalState() *TerminalState {
	fd := int(os.Stdin.Fd())
	state, _ := term.GetState(fd)
	return &TerminalState{fd: fd, state: state, output: os.Stdout, restoreFn: term.Restore}
}

// Restore returns the terminal to the saved state. It also writes escape
// sequences to exit alternate screen and show the cursor, in case those
// were left active by a crash.
func (ts *TerminalState) Restore() {
	if ts == nil {
		return
	}
	output := ts.output
	if output == nil {
		output = os.Stdout
	}
	_, _ = fmt.Fprint(output, terminalCleanupSequences)
	if ts.state != nil {
		restoreFn := ts.restoreFn
		if restoreFn == nil {
			restoreFn = term.Restore
		}
		_ = restoreFn(ts.fd, ts.state)
	}
}

// PanicRecovery is intended to be used as a deferred call in the TUI entry
// point. It recovers from panics, restores the terminal, then re-panics so
// the stack trace is still printed.
func PanicRecovery(ts *TerminalState) {
	if r := recover(); r != nil {
		ts.Restore()
		// Re-panic to show the stack trace
		panic(r)
	}
}
