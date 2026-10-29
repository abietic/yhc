//go:build darwin || linux

package filehistory

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func lockRestoreDirectory(file *os.File) (func(), error) {
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, fmt.Errorf("workspace history is in use by another operation")
	}
	return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }, nil
}

func restoreFileSingleLink(info os.FileInfo) bool {
	native, ok := info.Sys().(*syscall.Stat_t)
	return ok && native.Nlink == 1
}
