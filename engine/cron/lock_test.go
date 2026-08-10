package cron

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalSchedulerLockIsPrivateAndOwnedRelease(t *testing.T) {
	project := t.TempDir()
	acquired, err := TryAcquireSchedulerLock(project)
	if err != nil || !acquired {
		t.Fatalf("acquire=%t err=%v", acquired, err)
	}
	lockPath := filepath.Join(project, ".yhc", "scheduler.lock")
	if info, err := os.Stat(lockPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode=%v err=%v", info, err)
	}
	if _, err := os.Lstat(filepath.Join(project, ".eino-agent", "scheduler.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canonical lock touched legacy path: %v", err)
	}
	acquired, err = TryAcquireSchedulerLock(project)
	if err != nil || !acquired {
		t.Fatalf("same-process reacquire=%t err=%v", acquired, err)
	}

	if err := os.WriteFile(lockPath, []byte("999999 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	ReleaseSchedulerLock(project)
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("release removed a replacement owner lock: %v", err)
	}
}
