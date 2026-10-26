//go:build darwin || linux

package tools

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestColdReadGuardOpensFIFOWithoutBlockingAndRejectsIt(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "pipe")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	handle, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	// Exercise the replacement-safe opener directly, independently from its
	// preliminary Lstat check. A FIFO has no writer, so blocking open is wrong.
	file, err := openReadGuardFile(handle, "pipe")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Mode().IsRegular() {
		t.Fatalf("FIFO descriptor: %v %v", info, err)
	}
	if _, err := readGuardFile(root, path); err == nil {
		t.Fatal("special file received read credit")
	}
}
