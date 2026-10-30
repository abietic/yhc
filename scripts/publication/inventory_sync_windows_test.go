package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsDirectorySyncOnlySkipsUnsupportedDirectoryFlush(t *testing.T) {
	dir := t.TempDir()
	file, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := syncInventoryDirectory(file); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := syncInventoryDirectory(file); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed directory error was suppressed: %v", err)
	}
	name := filepath.Join(dir, "file")
	if err := os.WriteFile(name, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	regular, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := regular.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := syncInventoryDirectory(regular); err == nil {
		t.Fatal("Windows directory capability was applied to a regular file")
	}
}

func TestWindowsInventoryRenameFailureRemainsFatal(t *testing.T) {
	t.Chdir(t.TempDir())
	name := filepath.Join("build", "publication", "output.json")
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("old complete output"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The Windows read-only attribute must still reject replacement. This is a
	// real permission failure, unlike the unsupported directory flush operation.
	if err := os.Chmod(name, 0o400); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chmod(name, 0o600); err != nil {
			t.Error(err)
		}
	}()
	if err := writeInventory(name, []byte("new complete output")); !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "promote inventory output") {
		t.Fatalf("rename failure was suppressed: %v", err)
	}
	actual, err := os.ReadFile(name)
	if err != nil || string(actual) != "old complete output" {
		t.Fatalf("failed replacement changed old data: %q %v", actual, err)
	}
}
