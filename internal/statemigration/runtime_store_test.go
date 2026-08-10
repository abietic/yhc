package statemigration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalStoreRejectsSymlinkRootAndIntermediate(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		parent := t.TempDir()
		outside := t.TempDir()
		rootPath := filepath.Join(parent, ".yhc")
		if err := os.Symlink(outside, rootPath); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if store, _, err := OpenCanonicalStore(rootPath, ".", true); err == nil {
			_ = store.Close()
			t.Fatal("canonical store accepted a symlink root")
		}
	})

	t.Run("intermediate", func(t *testing.T) {
		parent := t.TempDir()
		outside := t.TempDir()
		rootPath := filepath.Join(parent, ".yhc")
		if err := os.Mkdir(rootPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(rootPath, "permission-review-audit")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if store, _, err := OpenCanonicalStore(rootPath, "permission-review-audit/v1", true); err == nil {
			_ = store.Close()
			t.Fatal("canonical store accepted an intermediate symlink")
		}
		if _, err := os.Lstat(filepath.Join(outside, "v1")); !os.IsNotExist(err) {
			t.Fatalf("canonical store created outside its root: %v", err)
		}
	})
}

func TestCanonicalStoreRootReplacementCannotRedirectRootedWrites(t *testing.T) {
	parent := t.TempDir()
	rootPath := filepath.Join(parent, ".yhc")
	store, exists, err := OpenCanonicalStore(rootPath, ".", true)
	if err != nil || !exists {
		t.Fatalf("OpenCanonicalStore() exists=%t err=%v", exists, err)
	}
	defer store.Close() //nolint:errcheck
	detached := rootPath + "-detached"
	outside := t.TempDir()
	if err := os.Rename(rootPath, detached); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, rootPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	file, err := store.Root().OpenFile("history", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("detached-only\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Revalidate(); err == nil {
		t.Fatal("canonical store missed root replacement")
	}
	if _, err := os.Lstat(filepath.Join(outside, "history")); !os.IsNotExist(err) {
		t.Fatalf("root replacement redirected a write outside canonical state: %v", err)
	}
	if _, err := os.Stat(filepath.Join(detached, "history")); err != nil {
		t.Fatalf("pinned root did not retain the detached write: %v", err)
	}
}
