//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package statemigration

import (
	"syscall"
	"testing"
)

func createSpecialMigrationFile(t *testing.T, path string) bool {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Logf("mkfifo unavailable: %v", err)
		return false
	}
	return true
}
