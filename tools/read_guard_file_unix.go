//go:build darwin || linux

package tools

import (
	"os"

	"golang.org/x/sys/unix"
)

func readGuardSingleOwner(file *os.File) bool {
	var stat unix.Stat_t
	return unix.Fstat(int(file.Fd()), &stat) == nil && stat.Nlink == 1 && stat.Uid == uint32(os.Geteuid()) && stat.Gid == uint32(os.Getegid()) && stat.Mode&0o7000 == 0
}

func openReadGuardFile(root *os.Root, relative string) (*os.File, error) {
	// An adversarial replacement with a FIFO must not block validation. The
	// descriptor's regular-file/single-owner checks follow before reading.
	return root.OpenFile(relative, os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
}
