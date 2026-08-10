//go:build unix

package tui

import (
	"bytes"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestTerminalOutputUnixDuplicateDeadlineStopsAndRestoresFlags(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer reader.Close() //nolint:errcheck
	defer writer.Close() //nolint:errcheck

	before, err := terminalOutputFcntl(writer.Fd(), syscall.F_GETFL, 0)
	if err != nil {
		t.Fatalf("read original flags: %v", err)
	}
	output, err := NewTerminalOutputWithConfig(writer, TerminalOutputConfig{
		WriteTimeout:     20 * time.Millisecond,
		DrainTimeout:     time.Second,
		InterruptTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewTerminalOutputWithConfig: %v", err)
	}

	_, writeErr := output.Write(bytes.Repeat([]byte("x"), 1<<20))
	if !errors.Is(writeErr, ErrTerminalOutputWriteTimeout) {
		t.Fatalf("write error = %v, want terminal output timeout", writeErr)
	}
	if err := output.Close(); !errors.Is(err, ErrTerminalOutputWriteTimeout) {
		t.Fatalf("Close error = %v, want terminal output timeout", err)
	}
	if !output.Stopped() {
		t.Fatal("deadline did not stop the duplicate writer")
	}

	after, err := terminalOutputFcntl(writer.Fd(), syscall.F_GETFL, 0)
	if err != nil {
		t.Fatalf("read restored flags: %v", err)
	}
	if after&syscall.O_NONBLOCK != before&syscall.O_NONBLOCK {
		t.Fatalf(
			"original nonblocking flag = %#x after close, want %#x",
			after&syscall.O_NONBLOCK,
			before&syscall.O_NONBLOCK,
		)
	}
}
