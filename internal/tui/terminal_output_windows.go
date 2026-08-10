//go:build windows

package tui

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

var cancelSynchronousIO = windows.NewLazySystemDLL(
	"kernel32.dll",
).NewProc("CancelSynchronousIo")

type windowsTerminalOutputSink struct {
	file *os.File

	threadMu sync.Mutex
	thread   windows.Handle
	prepared bool

	closeOnce sync.Once
	closeErr  error
}

func newTerminalOutputSink(output *os.File) (terminalOutputSink, error) {
	process := windows.CurrentProcess()
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(
		process,
		windows.Handle(output.Fd()),
		process,
		&duplicate,
		0,
		false,
		windows.DUPLICATE_SAME_ACCESS,
	); err != nil {
		return nil, err
	}

	file := os.NewFile(uintptr(duplicate), output.Name()+"-tui-output")
	if file == nil {
		_ = windows.CloseHandle(duplicate)
		return nil, errors.New("create duplicate terminal output file")
	}
	return &windowsTerminalOutputSink{file: file}, nil
}

func (s *windowsTerminalOutputSink) prepare() error {
	runtime.LockOSThread()
	thread, err := windows.OpenThread(
		windows.THREAD_TERMINATE,
		false,
		windows.GetCurrentThreadId(),
	)
	if err != nil {
		runtime.UnlockOSThread()
		return err
	}

	s.threadMu.Lock()
	s.thread = thread
	s.prepared = true
	s.threadMu.Unlock()
	return nil
}

func (s *windowsTerminalOutputSink) finish() {
	s.threadMu.Lock()
	thread := s.thread
	s.thread = 0
	prepared := s.prepared
	s.prepared = false
	s.threadMu.Unlock()
	if thread != 0 {
		_ = windows.CloseHandle(thread)
	}
	if prepared {
		runtime.UnlockOSThread()
	}
}

func (s *windowsTerminalOutputSink) Write(p []byte) (int, error) {
	return s.file.Write(p)
}

func (s *windowsTerminalOutputSink) SetWriteDeadline(deadline time.Time) error {
	return s.file.SetWriteDeadline(deadline)
}

func (s *windowsTerminalOutputSink) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.file.Close()
	})
	return s.closeErr
}

func (s *windowsTerminalOutputSink) interrupt() error {
	s.threadMu.Lock()
	thread := s.thread
	var cancelErr error
	if thread != 0 {
		result, _, callErr := cancelSynchronousIO.Call(uintptr(thread))
		if result == 0 && !errors.Is(callErr, syscall.ERROR_NOT_FOUND) {
			cancelErr = callErr
		}
	}
	s.threadMu.Unlock()
	return errors.Join(cancelErr, s.Close())
}
