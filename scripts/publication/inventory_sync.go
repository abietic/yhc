package main

import (
	"errors"
	"fmt"
	"io"
)

type inventorySyncFile interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

func writeSyncedInventoryFile(file inventorySyncFile, contents []byte) (resultErr error) {
	defer func() {
		if err := file.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close inventory output: %w", err))
		}
	}()
	written, err := file.Write(contents)
	if err != nil {
		return fmt.Errorf("write inventory output: %w", err)
	}
	if written != len(contents) {
		return fmt.Errorf("write inventory output: %w", io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync inventory output: %w", err)
	}
	return nil
}
