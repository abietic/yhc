package filehistory

import "golang.org/x/sys/unix"

func coldExchange(fd int, from, to string) error {
	return unix.RenameatxNp(fd, from, fd, to, unix.RENAME_SWAP)
}
