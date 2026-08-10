//go:build windows

package mediastore

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func syncDirectoryFile(directory *os.File) error {
	if err := windows.FlushFileBuffers(windows.Handle(directory.Fd())); err != nil &&
		!errors.Is(err, windows.ERROR_INVALID_FUNCTION) {
		return err
	}
	return nil
}
