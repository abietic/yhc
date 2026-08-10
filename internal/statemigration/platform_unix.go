//go:build darwin || linux

package statemigration

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func tryLockMigration(file *os.File) (bool, error) {
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

func unlockMigration(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}

func validateSingleLink(file *os.File) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil || stat.Nlink != 1 {
		return errMigrationUnsafe
	}
	return nil
}

func syncDirectoryFile(directory *os.File) error {
	return directory.Sync()
}
