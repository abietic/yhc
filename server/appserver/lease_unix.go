//go:build !windows

package appserver

import (
	"fmt"
	"os"
	"syscall"
)

func lockSessionFile(file *os.File) error {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("lock file: %w", err)
	}
	return nil
}

func unlockSessionFile(file *os.File) error {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		return fmt.Errorf("unlock file: %w", err)
	}
	return nil
}
