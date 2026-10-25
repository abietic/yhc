package execution

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/uuid"
)

func TestRunUsageCheckpointStoreRoundTripAndRepeatedWrites(t *testing.T) {
	requireRunUsageCheckpointStore(t)
	path := runUsageCheckpointPath(t)
	segmentID := uuid.NewString()
	metadata := runUsageCheckpointHash("a")
	store, err := newRunUsageCheckpointFileStore(path, segmentID, metadata)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck
	if err := store.WriteCheckpoint([]byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteCheckpoint([]byte("second")); err != nil {
		t.Fatal(err)
	}
	snapshot, gotMetadata, err := readRunUsageCheckpointFile(path, segmentID)
	if err != nil || string(snapshot) != "second" || gotMetadata != metadata {
		t.Fatalf("read snapshot=%q metadata=%q err=%v", snapshot, gotMetadata, err)
	}
}

func TestRunUsageCheckpointStoreRejectsInvalidSnapshotAndPoisonsWriter(t *testing.T) {
	requireRunUsageCheckpointStore(t)
	store, err := newRunUsageCheckpointFileStore(runUsageCheckpointPath(t), uuid.NewString(), runUsageCheckpointHash("a"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck
	if err := store.WriteCheckpoint(nil); !errors.Is(err, errRunUsageCheckpointStorage) {
		t.Fatalf("empty snapshot err=%v", err)
	}
	if err := store.WriteCheckpoint([]byte("later")); !errors.Is(err, errRunUsageCheckpointStorage) {
		t.Fatalf("poisoned writer err=%v", err)
	}
	other, err := newRunUsageCheckpointFileStore(runUsageCheckpointPath(t), uuid.NewString(), runUsageCheckpointHash("b"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close() //nolint:errcheck
	if err := other.WriteCheckpoint(make([]byte, maxRunUsageCheckpointBytes+1)); !errors.Is(err, errRunUsageCheckpointStorage) {
		t.Fatalf("oversized snapshot err=%v", err)
	}
}

func TestRunUsageCheckpointStoreExclusiveOwnerPreservesExistingBytes(t *testing.T) {
	requireRunUsageCheckpointStore(t)
	path := runUsageCheckpointPath(t)
	segmentID := uuid.NewString()
	metadata := runUsageCheckpointHash("b")
	store, err := newRunUsageCheckpointFileStore(path, segmentID, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteCheckpoint([]byte("known")); err != nil {
		t.Fatal(err)
	}
	if _, err := newRunUsageCheckpointFileStore(path, uuid.NewString(), runUsageCheckpointHash("c")); !errors.Is(err, errRunUsageCheckpointStorage) {
		t.Fatalf("second writer err=%v", err)
	}
	snapshot, gotMetadata, err := readRunUsageCheckpointFile(path, segmentID)
	if err != nil || string(snapshot) != "known" || gotMetadata != metadata {
		t.Fatalf("old checkpoint changed snapshot=%q metadata=%q err=%v", snapshot, gotMetadata, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := newRunUsageCheckpointFileStore(path, segmentID, metadata); !errors.Is(err, errRunUsageCheckpointStorage) {
		t.Fatalf("reopen writer err=%v", err)
	}
}

func TestRunUsageCheckpointStoreReaderRejectsIdentityAndMetadataMismatch(t *testing.T) {
	requireRunUsageCheckpointStore(t)
	path := runUsageCheckpointPath(t)
	segmentID := uuid.NewString()
	metadata := runUsageCheckpointHash("d")
	store, err := newRunUsageCheckpointFileStore(path, segmentID, metadata)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck
	if err := store.WriteCheckpoint([]byte("snapshot")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readRunUsageCheckpointFile(path, uuid.NewString()); !errors.Is(err, errRunUsageCheckpointStorage) {
		t.Fatalf("segment mismatch err=%v", err)
	}
	if _, err := newRunUsageCheckpointFileStore(runUsageCheckpointPath(t), segmentID, "ABC"); !errors.Is(err, errRunUsageCheckpointStorage) {
		t.Fatalf("invalid metadata writer err=%v", err)
	}
	owner := filepath.Join(path, "owner.json")
	if err := os.WriteFile(owner, []byte(`{"version":1,"segment_id":"`+segmentID+`","metadata_sha256":"`+runUsageCheckpointHash("e")+`","unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readRunUsageCheckpointFile(path, segmentID); !errors.Is(err, errRunUsageCheckpointStorage) {
		t.Fatalf("unknown owner field err=%v", err)
	}
	if err := store.WriteCheckpoint([]byte("must not replace altered owner")); !errors.Is(err, errRunUsageCheckpointStorage) {
		t.Fatalf("altered owner writer err=%v", err)
	}
}

func TestRunUsageCheckpointStoreRejectsUnsafeOrUncertainState(t *testing.T) {
	requireRunUsageCheckpointStore(t)
	t.Run("public parent", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "public")
		if err := os.Mkdir(parent, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := newRunUsageCheckpointFileStore(filepath.Join(parent, "run"), uuid.NewString(), runUsageCheckpointHash("f")); !errors.Is(err, errRunUsageCheckpointStorage) {
			t.Fatalf("public parent err=%v", err)
		}
	})
	t.Run("symlink namespace", func(t *testing.T) {
		root := runUsageCheckpointRoot(t)
		outside := filepath.Join(root, "outside")
		if err := os.Mkdir(outside, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "run")
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
		if _, err := newRunUsageCheckpointFileStore(path, uuid.NewString(), runUsageCheckpointHash("g")); !errors.Is(err, errRunUsageCheckpointStorage) {
			t.Fatalf("symlink err=%v", err)
		}
	})
	t.Run("extra staged file preserves state", func(t *testing.T) {
		path, segmentID := runUsageCheckpointPath(t), uuid.NewString()
		store, err := newRunUsageCheckpointFileStore(path, segmentID, runUsageCheckpointHash("a"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close() //nolint:errcheck
		if err := store.WriteCheckpoint([]byte("known")); err != nil {
			t.Fatal(err)
		}
		pending := filepath.Join(path, "pending-evidence.json")
		if err := os.WriteFile(pending, []byte("uncertain"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readRunUsageCheckpointFile(path, segmentID); !errors.Is(err, errRunUsageCheckpointStorage) {
			t.Fatalf("staged state err=%v", err)
		}
		if got, err := os.ReadFile(pending); err != nil || string(got) != "uncertain" {
			t.Fatalf("reader changed pending state=%q err=%v", got, err)
		}
	})
	t.Run("replaced directory poisons writer", func(t *testing.T) {
		path, segmentID := runUsageCheckpointPath(t), uuid.NewString()
		store, err := newRunUsageCheckpointFileStore(path, segmentID, runUsageCheckpointHash("b"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close() //nolint:errcheck
		if err := store.WriteCheckpoint([]byte("first")); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path, path+"-old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := store.WriteCheckpoint([]byte("second")); !errors.Is(err, errRunUsageCheckpointStorage) {
			t.Fatalf("replaced directory err=%v", err)
		}
		if err := store.WriteCheckpoint([]byte("third")); !errors.Is(err, errRunUsageCheckpointStorage) {
			t.Fatalf("poisoned writer err=%v", err)
		}
	})
	t.Run("replaced checkpoint target poisons writer", func(t *testing.T) {
		path, segmentID := runUsageCheckpointPath(t), uuid.NewString()
		store, err := newRunUsageCheckpointFileStore(path, segmentID, runUsageCheckpointHash("c"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close() //nolint:errcheck
		if err := store.WriteCheckpoint([]byte("first")); err != nil {
			t.Fatal(err)
		}
		checkpoint := filepath.Join(path, "checkpoint.json")
		if err := os.Rename(checkpoint, checkpoint+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(checkpoint+".old", checkpoint); err != nil {
			t.Fatal(err)
		}
		if err := store.WriteCheckpoint([]byte("second")); !errors.Is(err, errRunUsageCheckpointStorage) {
			t.Fatalf("replaced target err=%v", err)
		}
	})
}

func TestRunUsageCheckpointStoreRejectsHardlinkedCheckpoint(t *testing.T) {
	requireRunUsageCheckpointStore(t)
	path, segmentID := runUsageCheckpointPath(t), uuid.NewString()
	store, err := newRunUsageCheckpointFileStore(path, segmentID, runUsageCheckpointHash("d"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck
	if err := store.WriteCheckpoint([]byte("known")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(path, "checkpoint.json"), filepath.Join(filepath.Dir(path), "checkpoint-copy")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readRunUsageCheckpointFile(path, segmentID); !errors.Is(err, errRunUsageCheckpointStorage) {
		t.Fatalf("hardlinked checkpoint err=%v", err)
	}
}

func requireRunUsageCheckpointStore(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("checkpoint store is intentionally unsupported on this platform")
	}
}

func runUsageCheckpointPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(runUsageCheckpointRoot(t), "usage-checkpoint")
}

func runUsageCheckpointRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func runUsageCheckpointHash(seed string) string {
	return seed + "000000000000000000000000000000000000000000000000000000000000000"
}
