//go:build darwin || linux

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openRegularNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open checksum target without following links: %w", err)
	}
	return os.NewFile(uintptr(fd), path), nil
}
