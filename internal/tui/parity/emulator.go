//go:build parity

package parity

import (
	"sync"

	"github.com/charmbracelet/x/vt"
)

// ScreenEmulator wraps charmbracelet/x/vt.Emulator to provide thread-safe
// screen state access for PTY output interpretation.
type ScreenEmulator struct {
	mu  sync.Mutex
	emu *vt.Emulator
}

// NewScreenEmulator creates a virtual terminal emulator with the given dimensions.
func NewScreenEmulator(width, height int) *ScreenEmulator {
	return &ScreenEmulator{
		emu: vt.NewEmulator(width, height),
	}
}

// Write feeds raw PTY output bytes into the terminal emulator.
func (e *ScreenEmulator) Write(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.emu.Write(p)
}

// PlainText returns the current screen content as plain text.
func (e *ScreenEmulator) PlainText() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.emu.String()
}

// RawRender returns the current screen with ANSI escape sequences.
func (e *ScreenEmulator) RawRender() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.emu.Render()
}

// IsAltScreen returns whether the emulator is in alternate screen mode.
func (e *ScreenEmulator) IsAltScreen() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.emu.IsAltScreen()
}
