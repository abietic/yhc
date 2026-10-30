//go:build !windows

package main

import "os"

func syncInventoryDirectory(file *os.File) error {
	return file.Sync()
}
