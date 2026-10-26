//go:build linux

package filehistory

import "golang.org/x/sys/unix"

func restoreRenameNoReplace(fd int, from, to string) error {
	return unix.Renameat2(fd, from, fd, to, unix.RENAME_NOREPLACE)
}
