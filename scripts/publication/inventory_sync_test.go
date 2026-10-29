package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type failingInventoryFile struct {
	writeErr, syncErr, closeErr error
	short                       bool
	syncCalls, closeCalls       int
}

func (file *failingInventoryFile) Write(data []byte) (int, error) {
	if file.short {
		return len(data) - 1, nil
	}
	return len(data), file.writeErr
}

func (file *failingInventoryFile) Sync() error {
	file.syncCalls++
	return file.syncErr
}

func (file *failingInventoryFile) Close() error {
	file.closeCalls++
	return file.closeErr
}

func TestInventoryFileWriteSyncAndCloseFailuresRemainFatal(t *testing.T) {
	writeErr := errors.New("write failure")
	syncErr := errors.New("data sync failure")
	closeErr := errors.New("close failure")
	for _, tc := range []struct {
		name      string
		file      failingInventoryFile
		want      []error
		syncCalls int
	}{
		{name: "success", syncCalls: 1},
		{name: "short write", file: failingInventoryFile{short: true}, want: []error{io.ErrShortWrite}},
		{name: "write", file: failingInventoryFile{writeErr: writeErr}, want: []error{writeErr}},
		{name: "sync", file: failingInventoryFile{syncErr: syncErr}, want: []error{syncErr}, syncCalls: 1},
		{name: "close", file: failingInventoryFile{closeErr: closeErr}, want: []error{closeErr}, syncCalls: 1},
		{name: "write and close", file: failingInventoryFile{writeErr: writeErr, closeErr: closeErr}, want: []error{writeErr, closeErr}},
		{name: "sync and close", file: failingInventoryFile{syncErr: syncErr, closeErr: closeErr}, want: []error{syncErr, closeErr}, syncCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := writeSyncedInventoryFile(&tc.file, []byte("complete output"))
			if len(tc.want) == 0 && err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !errors.Is(err, want) {
					t.Fatalf("lost failure %v: %v", want, err)
				}
			}
			if tc.file.syncCalls != tc.syncCalls || tc.file.closeCalls != 1 {
				t.Fatalf("sync=%d close=%d", tc.file.syncCalls, tc.file.closeCalls)
			}
		})
	}
}

func TestInventoryOutputRetainsCompleteReplacement(t *testing.T) {
	t.Chdir(t.TempDir())
	name := filepath.Join("build", "publication", "output.json")
	for _, data := range []string{"old complete output", "new complete output"} {
		if err := writeInventory(name, []byte(data)); err != nil {
			t.Fatal(err)
		}
		actual, err := os.ReadFile(name)
		if err != nil || string(actual) != data {
			t.Fatalf("incomplete replacement: %q %v", actual, err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(name))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary output leaked: %v %v", entries, err)
	}
}

func TestInventoryUnexpectedDirectorySyncErrorsRemainFatal(t *testing.T) {
	for _, failure := range []error{os.ErrPermission, errors.New("directory I/O failure")} {
		t.Run(failure.Error(), func(t *testing.T) {
			t.Chdir(t.TempDir())
			called := false
			err := writeInventoryWithDirectorySync(filepath.Join("build", "publication", "output.json"), []byte("complete"), func(file *os.File) error {
				called = true
				info, err := file.Stat()
				if err != nil || !info.IsDir() {
					t.Fatalf("not a verified directory: %v", err)
				}
				return failure
			})
			if !called || !errors.Is(err, failure) {
				t.Fatalf("directory failure was suppressed: %v", err)
			}
		})
	}
}

func TestInventoryDirectoryCloseErrorsRemainFatal(t *testing.T) {
	for _, syncErr := range []error{nil, errors.New("directory sync failure")} {
		t.Run(fmt.Sprint(syncErr), func(t *testing.T) {
			t.Chdir(t.TempDir())
			err := writeInventoryWithDirectorySync(filepath.Join("build", "publication", "output.json"), []byte("complete"), func(file *os.File) error {
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
				return syncErr
			})
			if !errors.Is(err, os.ErrClosed) || (syncErr != nil && !errors.Is(err, syncErr)) {
				t.Fatalf("directory close or sync failure was lost: %v", err)
			}
		})
	}
}
