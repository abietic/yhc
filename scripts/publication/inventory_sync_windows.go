package main

import (
	"errors"
	"os"
)

func syncInventoryDirectory(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("inventory directory sync requires a directory")
	}
	// Go opens directories without GENERIC_WRITE, which FlushFileBuffers requires.
	// Do not invoke that unsupported operation or suppress its access-error code.
	// The caller still synchronizes file data, promotes with rooted Rename,
	// revalidates directory identities, and checks handle-close errors. Windows
	// does not gain the Unix parent-directory crash-durability guarantee.
	return nil
}
