//go:build !unix && !windows

package tui

import (
	"errors"
	"os"
)

func newTerminalOutputSink(*os.File) (terminalOutputSink, error) {
	return nil, errors.New("interruptible terminal output is unsupported on this platform")
}
