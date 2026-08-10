package workboard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreCutoverFailureInventoryAndRetry(t *testing.T) {
	fileStages := []FailureStage{
		FailureCreate,
		FailureChmod,
		FailureWrite,
		FailureSync,
		FailureClose,
		FailureRename,
		FailureDirSync,
	}
	for _, kind := range []ArtifactKind{
		ArtifactBackup,
		ArtifactAuthority,
		ArtifactMarker,
	} {
		for _, stage := range fileStages {
			t.Run(string(kind)+"/"+string(stage), func(t *testing.T) {
				dir := authorityPrivateTempDir(t)
				inject := true
				store, err := NewStore(StoreConfig{
					Dir:       dir,
					SessionID: "session",
					FileFailure: func(
						currentKind ArtifactKind,
						currentStage FailureStage,
					) error {
						if inject &&
							currentKind == kind &&
							currentStage == stage {
							inject = false
							return errors.New("stop")
						}
						return nil
					},
				})
				if err != nil {
					t.Fatalf("new store: %v", err)
				}
				record := validAuthorityRecordFixture()
				backup := backupFromRecord(record)
				if _, err := store.Cutover(record, backup); err == nil {
					t.Fatal("expected cutover failure")
				}
				state, err := store.Inspect()
				if err != nil {
					t.Fatalf("inspect pre-marker state: %v", err)
				}
				if state.Mode != AuthorityModeLegacy {
					t.Fatalf("failed cutover mode = %q", state.Mode)
				}
				retried, err := store.Cutover(record, backup)
				if err != nil {
					t.Fatalf("retry cutover: %v", err)
				}
				if retried.Mode != AuthorityModeWorkBoard {
					t.Fatalf("retried cutover mode = %q", retried.Mode)
				}
			})
		}
	}

	for _, stage := range []StoreStage{
		StoreStageBackupEncode,
		StoreStageAuthorityEncode,
		StoreStageMarkerEncode,
	} {
		t.Run(string(stage), func(t *testing.T) {
			dir := authorityPrivateTempDir(t)
			inject := true
			store, err := NewStore(StoreConfig{
				Dir:       dir,
				SessionID: "session",
				Failure: func(current StoreStage) error {
					if inject && current == stage {
						inject = false
						return errors.New("stop")
					}
					return nil
				},
			})
			if err != nil {
				t.Fatalf("new store: %v", err)
			}
			record := validAuthorityRecordFixture()
			if _, err := store.Cutover(
				record,
				backupFromRecord(record),
			); err == nil {
				t.Fatal("expected encode-stage failure")
			}
			state, err := store.Inspect()
			if err != nil {
				t.Fatalf("inspect failed cutover: %v", err)
			}
			if state.Mode != AuthorityModeLegacy {
				t.Fatalf("failed cutover mode = %q", state.Mode)
			}
			if _, err := store.Cutover(
				record,
				backupFromRecord(record),
			); err != nil {
				t.Fatalf("retry encode-stage cutover: %v", err)
			}
		})
	}
}

func TestStoreMarkerVisibleFailuresRemainAuthoritative(t *testing.T) {
	for _, stage := range []StoreStage{
		StoreStageMarkerReread,
		StoreStageInstall,
	} {
		t.Run(string(stage), func(t *testing.T) {
			dir := authorityPrivateTempDir(t)
			store, err := NewStore(StoreConfig{
				Dir:       dir,
				SessionID: "session",
				Failure: func(current StoreStage) error {
					if current == stage {
						return errors.New("stop")
					}
					return nil
				},
			})
			if err != nil {
				t.Fatalf("new store: %v", err)
			}
			record := validAuthorityRecordFixture()
			returned, err := store.Cutover(
				record,
				backupFromRecord(record),
			)
			if err == nil {
				t.Fatal("expected post-marker cutover failure")
			}
			if returned.Mode != AuthorityModeWorkBoard {
				t.Fatalf("returned marker-visible mode = %q", returned.Mode)
			}
			inspected, inspectErr := store.Inspect()
			if inspectErr != nil {
				t.Fatalf("inspect marker-visible state: %v", inspectErr)
			}
			if inspected.Mode != AuthorityModeWorkBoard ||
				inspected.Record.BoardID != record.BoardID {
				t.Fatalf("marker-visible authority = %+v", inspected)
			}
		})
	}
}

func TestStoreVisibleMarkerRequiresCompleteStrictSet(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, dir string)
	}{
		{
			name: "missing authority",
			mutate: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.Remove(filepath.Join(
					dir,
					"session"+AuthorityRecordSuffix,
				)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "corrupt backup",
			mutate: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.WriteFile(
					filepath.Join(dir, "session"+LegacyBackupSuffix),
					[]byte(`{"version":1}`),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unknown marker",
			mutate: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.WriteFile(
					filepath.Join(dir, "session"+AuthorityMarkerSuffix),
					[]byte(`{"version":99,"session_id":"session","minimum_reader":"workboard/v2"}`),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := authorityPrivateTempDir(t)
			store := mustAuthorityStore(t, dir, "session")
			record := validAuthorityRecordFixture()
			if _, err := store.Cutover(
				record,
				backupFromRecord(record),
			); err != nil {
				t.Fatalf("seed authority: %v", err)
			}
			test.mutate(t, dir)
			if _, err := store.Inspect(); err == nil {
				t.Fatal("expected marked state to fail closed")
			}
		})
	}
}

func TestStoreCutoverRejectsMismatchedBackupBeforeMarker(t *testing.T) {
	dir := authorityPrivateTempDir(t)
	store, err := NewStore(StoreConfig{
		Dir:       dir,
		SessionID: "session",
	})
	if err != nil {
		t.Fatal(err)
	}
	record := validAuthorityRecordFixture()
	backup := backupFromRecord(record)
	backup.Compatibility.Tasks[0].Subject = "different baseline"
	if _, err := store.Cutover(record, backup); err == nil {
		t.Fatal("mismatched backup unexpectedly cut over")
	}
	assertAuthorityArtifactsAbsent(t, dir, "session")
}

func TestStoreInspectAllowsBareLegacyDirectoryUntilFirstCutover(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod legacy directory: %v", err)
	}
	store := mustAuthorityStore(t, dir, "session")

	state, err := store.Inspect()
	if err != nil {
		t.Fatalf("inspect bare legacy directory: %v", err)
	}
	if state.Mode != AuthorityModeLegacy {
		t.Fatalf("bare legacy directory mode = %q", state.Mode)
	}

	record := validAuthorityRecordFixture()
	if _, err := store.Cutover(
		record,
		backupFromRecord(record),
	); err == nil || !strings.Contains(err.Error(), "mode is not 0700") {
		t.Fatalf("cutover error = %v", err)
	}
	assertAuthorityArtifactsAbsent(t, dir, "session")
}

func TestStoreInspectAllowsMissingLegacyDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "transcripts")
	store := mustAuthorityStore(t, dir, "session")

	state, err := store.Inspect()
	if err != nil {
		t.Fatalf("inspect missing legacy directory: %v", err)
	}
	if state.Mode != AuthorityModeLegacy {
		t.Fatalf("missing legacy directory mode = %q", state.Mode)
	}
}

func TestStoreInspectRejectsPreparedArtifactsInLegacyModeDirectory(t *testing.T) {
	for _, test := range []struct {
		name   string
		suffix string
	}{
		{name: "authority", suffix: AuthorityRecordSuffix},
		{name: "backup", suffix: LegacyBackupSuffix},
		{name: "marker", suffix: AuthorityMarkerSuffix},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o755); err != nil {
				t.Fatalf("chmod legacy directory: %v", err)
			}
			if err := os.WriteFile(
				filepath.Join(dir, "session"+test.suffix),
				[]byte("{}"),
				0o600,
			); err != nil {
				t.Fatalf("write prepared artifact: %v", err)
			}
			store := mustAuthorityStore(t, dir, "session")

			if _, err := store.Inspect(); err == nil ||
				!strings.Contains(err.Error(), "mode is not 0700") {
				t.Fatalf("inspect error = %v", err)
			}
		})
	}
}
