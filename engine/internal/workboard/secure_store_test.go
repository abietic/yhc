package workboard

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/abietic/yhc/tools"
)

func TestArtifactStoreExactPathsAndAtomicReplacement(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	store, err := NewArtifactStore(dir, "session", nil)
	if err != nil {
		t.Fatalf("new artifact store: %v", err)
	}
	paths := store.Paths()
	if filepath.Base(paths.Authority) != "session"+AuthorityRecordSuffix ||
		filepath.Base(paths.Marker) != "session"+AuthorityMarkerSuffix ||
		filepath.Base(paths.Backup) != "session"+LegacyBackupSuffix {
		t.Fatalf("unexpected artifact paths: %+v", paths)
	}
	if err := store.Write(ArtifactAuthority, []byte("old\n")); err != nil {
		t.Fatalf("write initial authority: %v", err)
	}
	if err := store.Write(ArtifactAuthority, []byte("new\n")); err != nil {
		t.Fatalf("replace authority: %v", err)
	}
	data, err := store.Read(ArtifactAuthority)
	if err != nil {
		t.Fatalf("read authority: %v", err)
	}
	if string(data) != "new\n" {
		t.Fatalf("authority content = %q", data)
	}
	info, err := os.Lstat(paths.Authority)
	if err != nil {
		t.Fatalf("stat authority: %v", err)
	}
	if info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		t.Fatalf("authority mode = %v", info.Mode())
	}
	assertNoArtifactTemps(t, dir)
}

func TestArtifactStoreFailureStagesLeaveOnlyCompleteState(t *testing.T) {
	stages := []FailureStage{
		FailureCreate,
		FailureChmod,
		FailureWrite,
		FailureSync,
		FailureClose,
		FailureRename,
		FailureDirSync,
	}
	for _, stage := range stages {
		t.Run(string(stage), func(t *testing.T) {
			dir := authorityPrivateTempDir(t)
			store, err := NewArtifactStore(dir, "session", nil)
			if err != nil {
				t.Fatalf("new initial store: %v", err)
			}
			if err := store.Write(ArtifactAuthority, []byte("old\n")); err != nil {
				t.Fatalf("write initial authority: %v", err)
			}
			store, err = NewArtifactStore(
				dir,
				"session",
				func(kind ArtifactKind, current FailureStage) error {
					if kind == ArtifactAuthority && current == stage {
						return errors.New("stop")
					}
					return nil
				},
			)
			if err != nil {
				t.Fatalf("new injected store: %v", err)
			}
			if err := store.Write(
				ArtifactAuthority,
				[]byte("new\n"),
			); err == nil {
				t.Fatal("expected injected write failure")
			}
			data, readErr := os.ReadFile(store.Paths().Authority)
			if readErr != nil {
				t.Fatalf("read complete artifact after failure: %v", readErr)
			}
			if string(data) != "old\n" && string(data) != "new\n" {
				t.Fatalf("partial artifact after %s: %q", stage, data)
			}
			if string(data) != "old\n" {
				t.Fatalf("pre-rename failure %s replaced target: %q", stage, data)
			}
			assertNoArtifactTemps(t, dir)
		})
	}
}

func TestArtifactStoreRejectsUnsafeDirectoryAndTargets(t *testing.T) {
	t.Run("directory mode", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatalf("chmod directory: %v", err)
		}
		if _, err := NewArtifactStore(dir, "session", nil); err == nil {
			t.Fatal("expected unsafe directory mode rejection")
		}
	})

	t.Run("symlink directory", func(t *testing.T) {
		realDir := authorityPrivateTempDir(t)
		link := filepath.Join(t.TempDir(), "transcripts")
		if err := os.Symlink(realDir, link); err != nil {
			t.Fatalf("symlink directory: %v", err)
		}
		if _, err := NewArtifactStore(link, "session", nil); err == nil {
			t.Fatal("expected symlink directory rejection")
		}
	})

	for _, kind := range []string{"symlink", "directory", "mode"} {
		t.Run(kind+" target", func(t *testing.T) {
			dir := authorityPrivateTempDir(t)
			path := filepath.Join(dir, "session"+AuthorityRecordSuffix)
			switch kind {
			case "symlink":
				external := filepath.Join(t.TempDir(), "external")
				if err := os.WriteFile(external, []byte("external"), 0o600); err != nil {
					t.Fatalf("write external: %v", err)
				}
				if err := os.Symlink(external, path); err != nil {
					t.Fatalf("symlink target: %v", err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatalf("mkdir target: %v", err)
				}
			case "mode":
				if err := os.WriteFile(path, []byte("unsafe"), 0o644); err != nil {
					t.Fatalf("write target: %v", err)
				}
			}
			store, err := NewArtifactStore(dir, "session", nil)
			if err != nil {
				t.Fatalf("new store: %v", err)
			}
			if err := store.Write(
				ArtifactAuthority,
				[]byte("new\n"),
			); err == nil {
				t.Fatal("expected unsafe target rejection")
			}
		})
	}
}

func TestArtifactStoreRejectsDeterministicReplacementRace(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	var path string
	store, err := NewArtifactStore(
		dir,
		"session",
		func(kind ArtifactKind, stage FailureStage) error {
			if kind == ArtifactAuthority && stage == FailureRename {
				if err := os.WriteFile(path, []byte("racer\n"), 0o600); err != nil {
					t.Fatalf("install racing target: %v", err)
				}
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	path = store.Paths().Authority
	err = store.Write(ArtifactAuthority, []byte("new\n"))
	if err == nil || !strings.Contains(err.Error(), "appeared") {
		t.Fatalf("replacement race error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read racing target: %v", err)
	}
	if string(data) != "racer\n" {
		t.Fatalf("racing target overwritten: %q", data)
	}
	assertNoArtifactTemps(t, dir)
}

func TestArtifactStoreObservesParentDirSyncStage(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	observed := false
	store, err := NewArtifactStore(
		dir,
		"session",
		func(kind ArtifactKind, stage FailureStage) error {
			if kind == ArtifactMarker && stage == FailureDirSync {
				observed = true
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := store.Write(ArtifactMarker, []byte("marker\n")); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if !observed {
		t.Fatal("parent directory sync stage was not observed")
	}
}

func TestArtifactStorePostRenameDirSyncFailureRestoresAbsentTarget(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	store, err := NewArtifactStore(
		dir,
		"session",
		func(kind ArtifactKind, stage FailureStage) error {
			if kind == ArtifactMarker && stage == FailureDirSync {
				return errors.New("stop after rename")
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := store.Write(ArtifactMarker, []byte("marker\n")); err == nil {
		t.Fatal("expected post-rename directory sync failure")
	}
	if _, err := os.Lstat(store.Paths().Marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker survived compensated write: %v", err)
	}
	assertNoArtifactTemps(t, dir)
}

func TestArtifactStoreReportsUncertainDurabilityWhenRollbackFails(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	store, err := NewArtifactStore(dir, "session", nil)
	if err != nil {
		t.Fatalf("new initial store: %v", err)
	}
	if err := store.Write(ArtifactAuthority, []byte("old\n")); err != nil {
		t.Fatalf("write initial authority: %v", err)
	}
	store, err = NewArtifactStore(
		dir,
		"session",
		func(kind ArtifactKind, stage FailureStage) error {
			if kind != ArtifactAuthority {
				return nil
			}
			if stage == FailureDirSync || stage == FailureRollback {
				return errors.New("stop")
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("new injected store: %v", err)
	}
	err = store.Write(ArtifactAuthority, []byte("new\n"))
	if err == nil || !IsDurabilityUncertain(err) {
		t.Fatalf("write error = %v, want uncertain durability", err)
	}
	var uncertain *DurabilityUncertainError
	if !errors.As(err, &uncertain) || !uncertain.Quarantined {
		t.Fatalf("uncertain write did not persist quarantine: %v", err)
	}
	data, readErr := os.ReadFile(store.Paths().Authority)
	if readErr != nil {
		t.Fatalf("read uncertain authority: %v", readErr)
	}
	if string(data) != "new\n" {
		t.Fatalf("visible uncertain authority = %q", data)
	}
	info, statErr := os.Lstat(store.Paths().Authority)
	if statErr != nil {
		t.Fatalf("stat quarantined authority: %v", statErr)
	}
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("quarantined authority mode = %o", info.Mode().Perm())
	}
	if _, readErr := store.Read(ArtifactAuthority); readErr == nil {
		t.Fatal("quarantined authority remained readable through ArtifactStore")
	}
	authorityStore, storeErr := NewStore(StoreConfig{
		Dir:       dir,
		SessionID: "session",
	})
	if storeErr != nil {
		t.Fatalf("new authority store: %v", storeErr)
	}
	if _, inspectErr := authorityStore.Inspect(); inspectErr == nil ||
		!strings.Contains(inspectErr.Error(), "unsafe prepared authority") {
		t.Fatalf("restart inspection did not reject quarantine: %v", inspectErr)
	}
	if _, adapterErr := NewLogicalWorkAdapter(
		AdapterConfig{
			SessionID:   "session",
			Dir:         dir,
			LeaderScope: tools.TodoScope{SessionID: "session"},
		},
		tools.TaskManagerSnapshot{NextID: 1},
	); adapterErr == nil ||
		!strings.Contains(adapterErr.Error(), "unsafe prepared authority") {
		t.Fatalf("restart adapter did not reject quarantine: %v", adapterErr)
	}
	assertNoArtifactTemps(t, dir)
}

func TestArtifactStoreRejectsPathLikeSessionID(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	for _, sessionID := range []string{"", ".", "..", "../escape", "a/b", `a\b`} {
		if _, err := NewArtifactStore(dir, sessionID, nil); err == nil {
			t.Fatalf("accepted path-like SessionID %q", sessionID)
		}
	}
}

func TestPreparePrivateTranscriptDirectoryRejectsUnsafeObjects(t *testing.T) {
	t.Run("regular file", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "transcripts")
		if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := preparePrivateTranscriptDirectory(path); err == nil ||
			!strings.Contains(err.Error(), "is not a directory") {
			t.Fatalf("regular-file error = %v", err)
		}
		assertAuthorityArtifactsAbsent(t, root, "session")
	})

	t.Run("final symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation requires platform-specific privileges")
		}
		root := t.TempDir()
		target := filepath.Join(root, "target")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "transcripts")
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if err := preparePrivateTranscriptDirectory(path); err == nil ||
			!strings.Contains(err.Error(), "is not a directory") {
			t.Fatalf("symlink error = %v", err)
		}
		info, err := os.Lstat(target)
		if err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("symlink target mode = %#v, %v", info, err)
		}
		assertAuthorityArtifactsAbsent(t, target, "session")
	})
}

func TestPreparePrivateTranscriptDirectoryRejectsReplacement(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	moved := dir + "-moved"
	err := preparePrivateTranscriptDirectoryWithHook(dir, func() {
		if err := os.Rename(dir, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "changed while securing") {
		t.Fatalf("replacement error = %v", err)
	}
	assertAuthorityArtifactsAbsent(t, dir, "session")
	assertAuthorityArtifactsAbsent(t, moved, "session")
}

func authorityPrivateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod private temp directory: %v", err)
	}
	return dir
}

func assertNoArtifactTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read artifact directory: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temporary artifact leaked: %s", entry.Name())
		}
	}
}
