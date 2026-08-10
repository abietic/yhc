//go:build darwin || linux

package tools

import (
	"os"

	"golang.org/x/sys/unix"
)

func configFileHasSingleLink(file *os.File) bool {
	var stat unix.Stat_t
	return file != nil && unix.Fstat(int(file.Fd()), &stat) == nil && stat.Nlink == 1
}
