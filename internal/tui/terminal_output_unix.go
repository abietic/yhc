//go:build unix

package tui

import (
	"errors"
	"os"
	"sync"
	"syscall"
	"time"
)

type unixTerminalOutputSink struct {
	file          *os.File
	originalFD    int
	originalFlags int

	closeOnce sync.Once
	closeErr  error
}

func newTerminalOutputSink(output *os.File) (terminalOutputSink, error) {
	originalFD := int(output.Fd())
	originalFlags, err := terminalOutputFcntl(
		uintptr(originalFD),
		syscall.F_GETFL,
		0,
	)
	if err != nil {
		return nil, err
	}

	duplicateFD, err := syscall.Dup(originalFD)
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(duplicateFD)

	if _, err := terminalOutputFcntl(
		uintptr(originalFD),
		syscall.F_SETFL,
		originalFlags|syscall.O_NONBLOCK,
	); err != nil {
		_ = syscall.Close(duplicateFD)
		return nil, err
	}

	duplicate := os.NewFile(uintptr(duplicateFD), output.Name()+"-tui-output")
	if duplicate == nil {
		_, _ = terminalOutputFcntl(
			uintptr(originalFD),
			syscall.F_SETFL,
			originalFlags,
		)
		_ = syscall.Close(duplicateFD)
		return nil, errors.New("create duplicate terminal output file")
	}

	return &unixTerminalOutputSink{
		file:          duplicate,
		originalFD:    originalFD,
		originalFlags: originalFlags,
	}, nil
}

func terminalOutputFcntl(fd uintptr, command, argument int) (int, error) {
	result, _, errno := syscall.Syscall(
		syscall.SYS_FCNTL,
		fd,
		uintptr(command),
		uintptr(argument),
	)
	if errno != 0 {
		return 0, errno
	}
	return int(result), nil
}

func (s *unixTerminalOutputSink) Write(p []byte) (int, error) {
	return s.file.Write(p)
}

func (s *unixTerminalOutputSink) SetWriteDeadline(deadline time.Time) error {
	return s.file.SetWriteDeadline(deadline)
}

func (s *unixTerminalOutputSink) Close() error {
	s.closeOnce.Do(func() {
		closeErr := s.file.Close()
		_, restoreErr := terminalOutputFcntl(
			uintptr(s.originalFD),
			syscall.F_SETFL,
			s.originalFlags,
		)
		s.closeErr = errors.Join(closeErr, restoreErr)
	})
	return s.closeErr
}

func (s *unixTerminalOutputSink) prepare() error { return nil }
func (s *unixTerminalOutputSink) finish()        {}
func (s *unixTerminalOutputSink) interrupt() error {
	return s.Close()
}
