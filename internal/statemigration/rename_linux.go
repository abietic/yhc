//go:build linux

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
	return unix.Renameat2(
		int(sourceDirectory.Fd()),
		sourceName,
		int(targetDirectory.Fd()),
		targetName,
		unix.RENAME_NOREPLACE,
	)
}
