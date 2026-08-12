//go:build windows

package appserver

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func lockSessionFile(file *os.File) error {
	overlapped := new(windows.Overlapped)
	if err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		overlapped,
	); err != nil {
		return fmt.Errorf("lock file: %w", err)
	}
	return nil
}

func unlockSessionFile(file *os.File) error {
	overlapped := new(windows.Overlapped)
	if err := windows.UnlockFileEx(
		windows.Handle(file.Fd()),
		0,
		1,
		0,
		overlapped,
	); err != nil {
		return fmt.Errorf("unlock file: %w", err)
	}
	return nil
}
