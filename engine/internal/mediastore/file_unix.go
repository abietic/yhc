//go:build !windows

package mediastore

import (
	"os"
)

func syncDirectoryFile(directory *os.File) error {
	return directory.Sync()
}
