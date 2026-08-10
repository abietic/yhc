package statemigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareFileSetCapture(t *testing.T) {
	t.Run("required optional and exact allowlist", func(t *testing.T) {
		directory := fileSetDirectory(t, 0o700)
		writeFileSetFile(t, directory, "required", "one", 0o600)
		writeFileSetFile(t, directory, "unlisted", "ignored", 0o600)
		prepared := prepareFileSet(t, FileSetSpec{
			Owner: "yhc", Scope: "user", SourceDir: directory, LegacyMode: LegacyPrivate,
			Files: []ExactFileSpec{{Name: "required", Required: true, MaxBytes: 8}, {Name: "optional", MaxBytes: 8}},
			Validate: func(_ context.Context, snapshot Snapshot) error {
				return snapshot.Walk(func(relative string, _ os.DirEntry) error { return nil })
			},
		})
		defer prepared.Close() //nolint:errcheck
		var names []string
		if err := prepared.Snapshot().Walk(func(relative string, _ os.DirEntry) error {
			names = append(names, relative)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if len(names) != 1 || names[0] != "required" {
			t.Fatalf("captured %v, want only required", names)
		}
	})

	t.Run("missing required is absent", func(t *testing.T) {
		_, status, err := PrepareFileSet(context.Background(), FileSetSpec{
			Owner: "yhc", Scope: "user", SourceDir: fileSetDirectory(t, 0o700), LegacyMode: LegacyPrivate,
			Files: []ExactFileSpec{{Name: "required", Required: true, MaxBytes: 8}}, Validate: acceptFileSet,
		})
		if err != nil || status != StatusAbsent {
			t.Fatalf("PrepareFileSet() status=%q err=%v", status, err)
		}
	})

	t.Run("owner controlled accepts 0644", func(t *testing.T) {
		directory := fileSetDirectory(t, 0o755)
		writeFileSetFile(t, directory, "state", "ok", 0o644)
		prepared := prepareFileSet(t, FileSetSpec{
			Owner: "yhc", Scope: "user", SourceDir: directory, LegacyMode: LegacyOwnerControlled,
			Files: []ExactFileSpec{{Name: "state", Required: true, MaxBytes: 8}}, Validate: acceptFileSet,
		})
		defer prepared.Close() //nolint:errcheck
	})

	t.Run("private rejects 0644", func(t *testing.T) {
		directory := fileSetDirectory(t, 0o700)
		writeFileSetFile(t, directory, "state", "ok", 0o644)
		_, status, err := PrepareFileSet(context.Background(), FileSetSpec{
			Owner: "yhc", Scope: "user", SourceDir: directory, LegacyMode: LegacyPrivate,
			Files: []ExactFileSpec{{Name: "state", Required: true, MaxBytes: 8}}, Validate: acceptFileSet,
		})
		if !errors.Is(err, errMigrationUnsafe) || status != StatusUnsafe {
			t.Fatalf("PrepareFileSet() status=%q err=%v", status, err)
		}
	})
}

func TestPrepareFileSetRejectsUnsafeFile(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(t *testing.T, directory string)
	}{
		{"symlink", func(t *testing.T, directory string) {
			writeFileSetFile(t, directory, "outside", "ok", 0o600)
			if err := os.Symlink(filepath.Join(directory, "outside"), filepath.Join(directory, "state")); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
		}},
		{"hardlink", func(t *testing.T, directory string) {
			writeFileSetFile(t, directory, "outside", "ok", 0o600)
			if err := os.Link(filepath.Join(directory, "outside"), filepath.Join(directory, "state")); err != nil {
				t.Skipf("hardlink unavailable: %v", err)
			}
		}},
		{"replacement", func(t *testing.T, directory string) { writeFileSetFile(t, directory, "state", "old", 0o600) }},
		{"two snapshot instability", func(t *testing.T, directory string) { writeFileSetFile(t, directory, "state", "old", 0o600) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := fileSetDirectory(t, 0o700)
			test.setup(t, directory)
			calls := 0
			_, status, err := PrepareFileSet(context.Background(), FileSetSpec{
				Owner: "yhc", Scope: "user", SourceDir: directory, LegacyMode: LegacyPrivate,
				Files: []ExactFileSpec{{Name: "state", Required: true, MaxBytes: 8}},
				Validate: func(_ context.Context, _ Snapshot) error {
					calls++
					if test.name == "replacement" && calls == 1 {
						if err := os.Remove(filepath.Join(directory, "state")); err != nil {
							return err
						}
						writeFileSetFile(t, directory, "state", "new", 0o600)
					}
					if test.name == "two snapshot instability" && calls == 1 {
						writeFileSetFile(t, directory, "state", "changed", 0o600)
					}
					return nil
				},
			})
			if !errors.Is(err, errMigrationUnsafe) || status != StatusUnsafe {
				t.Fatalf("PrepareFileSet() status=%q err=%v", status, err)
			}
		})
	}
}

func TestPreparedFileSetRevalidateRejectsSourceMutation(t *testing.T) {
	directory := fileSetDirectory(t, 0o700)
	writeFileSetFile(t, directory, "state", "old", 0o600)
	prepared := prepareFileSet(t, fileSetSpec(directory))
	defer prepared.Close() //nolint:errcheck
	writeFileSetFile(t, directory, "state", "new", 0o600)
	if err := prepared.Revalidate(context.Background()); !errors.Is(err, errMigrationUnsafe) {
		t.Fatalf("Revalidate() err=%v, want unsafe", err)
	}
}

func TestCanonicalStoreLock(t *testing.T) {
	store := canonicalFileSetStore(t)
	defer store.Close() //nolint:errcheck
	release, err := store.Lock(context.Background(), "migration.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Lock(ctx, "migration.lock"); !errors.Is(err, errMigrationUnsafe) {
		t.Fatalf("contending cancelled Lock() err=%v, want unsafe", err)
	}
}

func TestCanonicalStorePromoteRegularFromNoReplace(t *testing.T) {
	t.Run("collision preserves target", func(t *testing.T) {
		source, target := canonicalPromotionStores(t)
		defer source.Close() //nolint:errcheck
		defer target.Close() //nolint:errcheck
		expected := writeCanonicalRegular(t, source, "from", "source")
		writeCanonicalRegular(t, target, "to", "target")
		collision, err := target.PromoteRegularFromNoReplace(source, "from", "to", expected)
		if err != nil || !collision {
			t.Fatalf("Promote collision=%t err=%v", collision, err)
		}
		if err := source.ValidateRegular("from", expected); err != nil {
			t.Fatalf("source changed: %v", err)
		}
	})

	t.Run("promotes expected file", func(t *testing.T) {
		source, target := canonicalPromotionStores(t)
		defer source.Close() //nolint:errcheck
		defer target.Close() //nolint:errcheck
		expected := writeCanonicalRegular(t, source, "from", "source")
		collision, err := target.PromoteRegularFromNoReplace(source, "from", "to", expected)
		if err != nil || collision {
			t.Fatalf("Promote collision=%t err=%v", collision, err)
		}
		if err := target.ValidateRegular("to", expected); err != nil {
			t.Fatalf("target invalid: %v", err)
		}
		if _, _, exists, err := source.OpenRegular("from", os.O_RDONLY, false); err != nil || exists {
			t.Fatalf("source exists=%t err=%v, want removed", exists, err)
		}
	})
}

func TestCanonicalStorePromoteRegularFromNoReplaceSameStore(t *testing.T) {
	t.Run("promotes within one pinned directory", func(t *testing.T) {
		store := canonicalFileSetStore(t)
		defer store.Close() //nolint:errcheck
		expected := writeCanonicalRegular(t, store, "from", "source")
		collision, err := store.PromoteRegularFromNoReplace(store, "from", "to", expected)
		if err != nil || collision {
			t.Fatalf("Promote collision=%t err=%v", collision, err)
		}
		if err := store.ValidateRegular("to", expected); err != nil {
			t.Fatalf("target invalid: %v", err)
		}
	})

	t.Run("same-directory collision preserves both files", func(t *testing.T) {
		store := canonicalFileSetStore(t)
		defer store.Close() //nolint:errcheck
		expected := writeCanonicalRegular(t, store, "from", "source")
		target := writeCanonicalRegular(t, store, "to", "target")
		collision, err := store.PromoteRegularFromNoReplace(store, "from", "to", expected)
		if err != nil || !collision {
			t.Fatalf("Promote collision=%t err=%v", collision, err)
		}
		if err := store.ValidateRegular("from", expected); err != nil {
			t.Fatalf("source changed: %v", err)
		}
		if err := store.ValidateRegular("to", target); err != nil {
			t.Fatalf("target changed: %v", err)
		}
	})
}

func acceptFileSet(context.Context, Snapshot) error { return nil }

func fileSetSpec(directory string) FileSetSpec {
	return FileSetSpec{
		Owner: "yhc", Scope: "user", SourceDir: directory, LegacyMode: LegacyPrivate,
		Files: []ExactFileSpec{{Name: "state", Required: true, MaxBytes: 32}}, Validate: acceptFileSet,
	}
}

func prepareFileSet(t *testing.T, spec FileSetSpec) *PreparedFileSet {
	t.Helper()
	prepared, status, err := PrepareFileSet(context.Background(), spec)
	if err != nil || status != StatusReady {
		t.Fatalf("PrepareFileSet() status=%q err=%v", status, err)
	}
	return prepared
}

func fileSetDirectory(t *testing.T, mode os.FileMode) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "legacy")
	if err := os.Mkdir(directory, mode); err != nil {
		t.Fatal(err)
	}
	return directory
}

func writeFileSetFile(t *testing.T, directory, name, data string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(directory, name), mode); err != nil {
		t.Fatal(err)
	}
}

func canonicalFileSetStore(t *testing.T) *CanonicalStore {
	t.Helper()
	store, exists, err := OpenCanonicalStore(filepath.Join(t.TempDir(), ".yhc"), ".", true)
	if err != nil || !exists {
		t.Fatalf("OpenCanonicalStore() exists=%t err=%v", exists, err)
	}
	return store
}

func canonicalPromotionStores(t *testing.T) (*CanonicalStore, *CanonicalStore) {
	t.Helper()
	return canonicalFileSetStore(t), canonicalFileSetStore(t)
}

func writeCanonicalRegular(t *testing.T, store *CanonicalStore, name, data string) os.FileInfo {
	t.Helper()
	file, info, exists, err := store.OpenRegular(name, os.O_WRONLY, true)
	if err != nil || !exists {
		t.Fatalf("OpenRegular() exists=%t err=%v", exists, err)
	}
	if _, err := file.WriteString(data); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return info
}
