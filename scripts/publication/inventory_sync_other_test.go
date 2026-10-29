//go:build !windows

package main

import (
	"errors"
	"os"
	"testing"
)

func TestNonWindowsDirectorySyncFailureIsNotIgnored(t *testing.T) {
	file, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	err = syncInventoryDirectory(file)
	var pathError *os.PathError
	if !errors.Is(err, os.ErrClosed) || !errors.As(err, &pathError) || pathError.Op != "sync" {
		t.Fatalf("non-Windows native directory Sync error was not preserved: %v", err)
	}
}
