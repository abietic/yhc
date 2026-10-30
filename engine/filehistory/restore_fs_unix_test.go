//go:build darwin || linux

package filehistory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func restoreStateFixture(text string, mode uint32) RevisionFileState {
	data := []byte(text)
	digest := sha256.Sum256(data)
	return RevisionFileState{Captured: true, Exists: true, Mode: mode, Data: data, Digest: hex.EncodeToString(digest[:])}
}

func restorePathFixture(t *testing.T, root string) RestorePath {
	t.Helper()
	p := RestorePath{Path: "file.txt", BackupPath: ".yhc-restore-test-0.backup", TempPath: ".yhc-restore-test-0.tmp", Original: restoreStateFixture("agent after\r\n", 0o755), Target: restoreStateFixture("dirty user baseline\r\nlast", 0o640)}
	if err := os.WriteFile(filepath.Join(root, p.Path), p.Original.Data, os.FileMode(p.Original.Mode)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, p.Path), os.FileMode(p.Original.Mode)); err != nil {
		t.Fatal(err)
	}
	return p
}

func assertRestoreFile(t *testing.T, root, path string, state RevisionFileState) {
	t.Helper()
	if !state.Exists {
		if _, err := os.Lstat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatalf("expected absent %s: %v", path, err)
		}
		return
	}
	data, err := os.ReadFile(filepath.Join(root, path))
	if err != nil || string(data) != string(state.Data) {
		t.Fatalf("restored bytes %s = %q, err=%v", path, data, err)
	}
	info, err := os.Stat(filepath.Join(root, path))
	if err != nil || uint32(info.Mode().Perm()) != state.Mode {
		t.Fatalf("restored mode %s: %v, %v", path, info, err)
	}
}

func TestRestoreFilesystemPreservesDirtyBaselineAndCanRollback(t *testing.T) {
	j, root, _ := openJournal(t)
	defer j.Close()
	p := restorePathFixture(t, root)
	status, err := j.restorePathFilesystem(p, "restore")
	if err != nil || status != "applied" {
		t.Fatalf("restore=%s %v", status, err)
	}
	assertRestoreFile(t, root, p.Path, p.Target)
	assertRestoreFile(t, root, p.BackupPath, p.Original)
	status, err = j.restorePathFilesystem(p, "restore")
	if err != nil || status != "already_applied" {
		t.Fatalf("idempotent restore=%s %v", status, err)
	}
	status, err = j.restorePathFilesystem(p, "rollback")
	if err != nil || status != "applied" {
		t.Fatalf("rollback=%s %v", status, err)
	}
	assertRestoreFile(t, root, p.Path, p.Original)
	assertRestoreFile(t, root, p.BackupPath+".rollback", p.Target)
}

func TestRestoreFilesystemContinuesAfterBackupBeforeInstallation(t *testing.T) {
	j, root, _ := openJournal(t)
	defer j.Close()
	p := restorePathFixture(t, root)
	if err := os.Rename(filepath.Join(root, p.Path), filepath.Join(root, p.BackupPath)); err != nil {
		t.Fatal(err)
	}
	if status, err := j.restorePathFilesystem(p, "restore"); err != nil || status != "applied" {
		t.Fatalf("resume exact backed-up state=%s %v", status, err)
	}
	assertRestoreFile(t, root, p.Path, p.Target)
	assertRestoreFile(t, root, p.BackupPath, p.Original)
}

func TestRestoreFilesystemRemovesCreatedFileWithoutLosingOriginal(t *testing.T) {
	j, root, _ := openJournal(t)
	defer j.Close()
	p := restorePathFixture(t, root)
	p.Target = RevisionFileState{Captured: true}
	if status, err := j.restorePathFilesystem(p, "restore"); err != nil || status != "applied" {
		t.Fatalf("remove created file=%s %v", status, err)
	}
	assertRestoreFile(t, root, p.Path, p.Target)
	assertRestoreFile(t, root, p.BackupPath, p.Original)
	if status, err := j.restorePathFilesystem(p, "rollback"); err != nil || status != "applied" {
		t.Fatalf("rollback removal=%s %v", status, err)
	}
	assertRestoreFile(t, root, p.Path, p.Original)
}

func TestRestoreFilesystemPreservesConflictingAndOccupiedFiles(t *testing.T) {
	for _, kind := range []string{"external", "temp", "backup"} {
		t.Run(kind, func(t *testing.T) {
			j, root, _ := openJournal(t)
			defer j.Close()
			p := restorePathFixture(t, root)
			path := p.Path
			if kind == "temp" {
				path = p.TempPath
			}
			if kind == "backup" {
				path = p.BackupPath
			}
			conflict := restoreStateFixture("external data", 0o600)
			if err := os.WriteFile(filepath.Join(root, path), conflict.Data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(root, path), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := j.restorePathFilesystem(p, "restore"); err == nil {
				t.Fatal("accepted conflict")
			}
			assertRestoreFile(t, root, path, conflict)
			if path != p.Path {
				assertRestoreFile(t, root, p.Path, p.Original)
			}
		})
	}
}

func TestRestoreFilesystemNeverReplacesConcurrentDestination(t *testing.T) {
	j, root, _ := openJournal(t)
	defer j.Close()
	p := restorePathFixture(t, root)
	newFile := restoreStateFixture("external file created after backup", 0o600)
	j.restoreBeforeInstall = func() {
		if err := os.WriteFile(filepath.Join(root, p.Path), newFile.Data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := j.restorePathFilesystem(p, "restore"); err == nil {
		t.Fatal("overwrote concurrent destination")
	}
	assertRestoreFile(t, root, p.Path, newFile)
	assertRestoreFile(t, root, p.BackupPath, p.Original)
}

func TestRestoreFilesystemRejectsLinksAndChangedRoot(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "root"} {
		t.Run(kind, func(t *testing.T) {
			j, root, _ := openJournal(t)
			defer j.Close()
			p := restorePathFixture(t, root)
			switch kind {
			case "symlink":
				outside := filepath.Join(t.TempDir(), "outside")
				if err := os.WriteFile(outside, p.Original.Data, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(root, p.Path)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(root, p.Path)); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(filepath.Join(root, p.Path), filepath.Join(root, "other-link")); err != nil {
					t.Fatal(err)
				}
			case "root":
				moved := root + "-moved"
				if err := os.Rename(root, moved); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(moved) })
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := j.restorePathFilesystem(p, "restore"); err == nil {
				t.Fatal("accepted unsupported identity")
			}
		})
	}
}

func TestRestoreFilesystemRetriesUncertainTempSyncBeforeInstalling(t *testing.T) {
	j, root, _ := openJournal(t)
	defer j.Close()
	p := restorePathFixture(t, root)
	calls := 0
	j.restoreSyncFile = func(file *os.File) error {
		calls++
		if calls == 1 {
			return errors.New("injected data sync failure")
		}
		return file.Sync()
	}
	if _, err := j.restorePathFilesystem(p, "restore"); err == nil {
		t.Fatal("accepted failed temp data sync")
	}
	assertRestoreFile(t, root, p.Path, p.Original)
	if _, err := j.restorePathFilesystem(p, "restore"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("retry failed to resync existing temp data: calls=%d", calls)
	}
	assertRestoreFile(t, root, p.Path, p.Target)
}

func TestRestoreFilesystemRollbackResolvesInterruptedForwardGap(t *testing.T) {
	j, root, _ := openJournal(t)
	defer j.Close()
	p := restorePathFixture(t, root)
	if err := os.Rename(filepath.Join(root, p.Path), filepath.Join(root, p.BackupPath)); err != nil {
		t.Fatal(err)
	}
	if _, err := j.restorePathFilesystem(p, "rollback"); err != nil {
		t.Fatal(err)
	}
	assertRestoreFile(t, root, p.Path, p.Original)
}

func TestRestoreFilesystemAppliedStatusRejectsLaterExternalDeletion(t *testing.T) {
	j, root, _ := openJournal(t)
	defer j.Close()
	p := restorePathFixture(t, root)
	if _, err := j.restorePathFilesystem(p, "restore"); err != nil {
		t.Fatal(err)
	}
	p.Status = "applied"
	if err := os.Remove(filepath.Join(root, p.Path)); err != nil {
		t.Fatal(err)
	}
	if _, err := j.restorePathFilesystem(p, "rollback"); err == nil {
		t.Fatal("treated an external delete after completed progress as an interrupted rename")
	}
	assertRestoreFile(t, root, p.Path, RevisionFileState{Captured: true})
	assertRestoreFile(t, root, p.BackupPath, p.Original)
}
