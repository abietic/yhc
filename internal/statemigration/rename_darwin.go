//go:build darwin

package statemigration

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameNoReplace(
	sourceDirectory *os.File,
	sourceName string,
	targetDirectory *os.File,
	targetName string,
) error {
	return unix.RenameatxNp(
		int(sourceDirectory.Fd()),
		sourceName,
		int(targetDirectory.Fd()),
		targetName,
		unix.RENAME_EXCL,
	)
}
