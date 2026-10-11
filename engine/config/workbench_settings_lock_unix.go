//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package config

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func tryLockWorkbenchSettings(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, unix.EWOULDBLOCK), errors.Is(err, unix.EAGAIN):
		return false, nil
	default:
		return false, err
	}
}

func unlockWorkbenchSettings(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
