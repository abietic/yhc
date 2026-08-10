package worktree

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/internal/identity"
)

func TestInspectLegacyWorktreesIsReadOnly(t *testing.T) {
	project := t.TempDir()
	git := newFakeGit(t, project)
	service := newLegacyFixtureService(project, git)
	record := newLegacyFixtureRecord(t, service, git, "legacy-ready", StateReady)
	writeLegacyFixtureRecord(t, service, git, record)
	marker := filepath.Join(record.Path, "marker.txt")
	if err := os.WriteFile(marker, []byte("legacy-bytes"), 0o640); err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(service.StoreRoot(), record.ID+".json")
	recordBefore := snapshotLegacyFixture(t, recordPath)
	markerBefore := snapshotLegacyFixture(t, marker)

	inspection, err := inspectLegacyWithGit(t.Context(), project, git)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Status != LegacyInspectionReady ||
		len(inspection.Records) != 1 ||
		inspection.Records[0] != (LegacyRecordInspection{
			RecordID: record.ID,
			Status:   LegacyRecordActive,
		}) {
		t.Fatalf("inspection = %#v", inspection)
	}
	assertLegacyFixtureUnchanged(t, recordPath, recordBefore)
	assertLegacyFixtureUnchanged(t, marker, markerBefore)
	assertNoLegacyGitMutation(t, git)
}

func TestWorktreeMigrationRefusesEveryLiveOrUnverifiableRecord(t *testing.T) {
	tests := []struct {
		name      string
		state     State
		configure func(*fakeGit, Record)
		want      LegacyRecordStatus
	}{
		{name: "creating", state: StateCreating, want: LegacyRecordActive},
		{name: "ready", state: StateReady, want: LegacyRecordActive},
		{name: "retained", state: StateRetained, want: LegacyRecordActive},
		{name: "removing", state: StateRemoving, want: LegacyRecordActive},
		{name: "cleanup-failed", state: StateCleanupFailed, want: LegacyRecordActive},
		{
			name:  "dirty",
			state: StateReady,
			configure: func(git *fakeGit, record Record) {
				git.status[record.Path] = "?? legacy-change.txt"
			},
			want: LegacyRecordDirty,
		},
		{
			name:  "new-commit",
			state: StateReady,
			configure: func(git *fakeGit, record Record) {
				git.commits[record.Path] = 1
			},
			want: LegacyRecordDirty,
		},
		{
			name:  "missing-tree",
			state: StateReady,
			configure: func(_ *fakeGit, record Record) {
				if err := os.RemoveAll(record.Path); err != nil {
					panic(err)
				}
			},
			want: LegacyRecordUnavailable,
		},
		{
			name:  "missing-branch",
			state: StateReady,
			configure: func(git *fakeGit, record Record) {
				delete(git.branches, record.Branch)
			},
			want: LegacyRecordUnavailable,
		},
		{
			name:  "git-error",
			state: StateReady,
			configure: func(git *fakeGit, _ Record) {
				git.repositoryErr = errors.New("unavailable")
			},
			want: LegacyRecordUnavailable,
		},
		{name: "removed", state: StateRemoved, want: LegacyRecordTerminal},
		{name: "failed-without-tree", state: StateFailed, want: LegacyRecordUnavailable},
		{
			name:  "failed-with-live-tree",
			state: StateFailed,
			configure: func(git *fakeGit, record Record) {
				if err := os.MkdirAll(record.Path, 0o700); err != nil {
					panic(err)
				}
				git.worktrees[record.Path] = Inspection{
					Repository: Repository{Root: record.Path, CommonDir: git.commonDir},
					Head:       record.BaseCommit,
					Branch:     record.Branch,
				}
				git.branches[record.Branch] = record.BaseCommit
			},
			want: LegacyRecordUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project := t.TempDir()
			git := newFakeGit(t, project)
			service := newLegacyFixtureService(project, git)
			record := newLegacyFixtureRecord(t, service, git, "legacy-"+strings.ReplaceAll(test.name, "-", "_"), test.state)
			writeLegacyFixtureRecord(t, service, git, record)
			if test.configure != nil {
				test.configure(git, record)
			}

			inspection, err := inspectLegacyWithGit(t.Context(), project, git)
			if err != nil {
				t.Fatal(err)
			}
			if inspection.Status != LegacyInspectionReady ||
				len(inspection.Records) != 1 ||
				inspection.Records[0].RecordID != record.ID ||
				inspection.Records[0].Status != test.want {
				t.Fatalf("inspection = %#v, want status %q", inspection, test.want)
			}
			if inspection.Records[0].Status == LegacyRecordStatus("quiescent") {
				t.Fatal("legacy worktree was presented as quiescent")
			}
			assertNoLegacyGitMutation(t, git)
		})
	}
}

func TestLegacyInspectionRejectsSymlinkAndRepositoryIdentityMismatch(t *testing.T) {
	t.Run("record-symlink", func(t *testing.T) {
		project := t.TempDir()
		git := newFakeGit(t, project)
		service := newLegacyFixtureService(project, git)
		if err := os.MkdirAll(service.StoreRoot(), 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(service.StoreRoot(), "target.txt")
		if err := os.WriteFile(target, []byte("not-a-record"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(service.StoreRoot(), "linked.json")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		inspection, err := inspectLegacyWithGit(t.Context(), project, git)
		if err != nil {
			t.Fatal(err)
		}
		if inspection.Status != LegacyInspectionReady ||
			len(inspection.Records) != 1 ||
			inspection.Records[0] != (LegacyRecordInspection{
				RecordID: "linked",
				Status:   LegacyRecordUnavailable,
			}) {
			t.Fatalf("inspection = %#v", inspection)
		}
		assertNoLegacyGitMutation(t, git)
	})

	t.Run("records-ancestor-symlink", func(t *testing.T) {
		project := t.TempDir()
		git := newFakeGit(t, project)
		external := t.TempDir()
		legacyRoot := filepath.Join(project, identity.LegacyDirName)
		if err := os.Symlink(external, legacyRoot); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		inspection, err := inspectLegacyWithGit(t.Context(), project, git)
		if err != nil {
			t.Fatal(err)
		}
		if inspection.Status != LegacyInspectionUnsafe || len(inspection.Records) != 0 {
			t.Fatalf("inspection = %#v", inspection)
		}
		assertNoLegacyGitMutation(t, git)
	})

	t.Run("repository-identity-mismatch", func(t *testing.T) {
		project := t.TempDir()
		git := newFakeGit(t, project)
		service := newLegacyFixtureService(project, git)
		record := newLegacyFixtureRecord(t, service, git, "identity-mismatch", StateReady)
		record.RepositoryIdentity = filepath.Join(project, "foreign.git")
		writeLegacyFixtureRecord(t, service, git, record)

		inspection, err := inspectLegacyWithGit(t.Context(), project, git)
		if err != nil {
			t.Fatal(err)
		}
		if inspection.Status != LegacyInspectionReady ||
			len(inspection.Records) != 1 ||
			inspection.Records[0].Status != LegacyRecordUnavailable {
			t.Fatalf("inspection = %#v", inspection)
		}
		assertNoLegacyGitMutation(t, git)
	})
}

func newLegacyFixtureService(project string, git Git) *Service {
	canonicalProject, err := canonicalExistingPath(project)
	if err != nil {
		panic(err)
	}
	root := filepath.Join(canonicalProject, identity.LegacyDirName, "worktrees", "v1")
	return &Service{
		projectRoot: canonicalProject,
		managedRoot: filepath.Join(root, "trees"),
		store:       NewStore(filepath.Join(root, "records")),
		git:         git,
	}
}

func newLegacyFixtureRecord(
	t *testing.T,
	service *Service,
	git *fakeGit,
	id string,
	state State,
) Record {
	t.Helper()
	record := recoveryRecord(t, service, git, id, testOwner(id), state)
	record.Branch = "eino-agent/worktree/" + id
	return record
}

func writeLegacyFixtureRecord(
	t *testing.T,
	service *Service,
	git *fakeGit,
	record Record,
) {
	t.Helper()
	if record.State != StateRemoved && record.State != StateFailed {
		if err := os.MkdirAll(record.Path, 0o700); err != nil {
			t.Fatal(err)
		}
		git.worktrees[record.Path] = Inspection{
			Repository: Repository{Root: record.Path, CommonDir: git.commonDir},
			Head:       record.BaseCommit,
			Branch:     record.Branch,
		}
		git.branches[record.Branch] = record.BaseCommit
	}
	if _, err := service.store.Create(t.Context(), record); err != nil {
		t.Fatal(err)
	}
}

type legacyFixtureSnapshot struct {
	data    []byte
	mode    os.FileMode
	modTime time.Time
}

func snapshotLegacyFixture(t *testing.T, path string) legacyFixtureSnapshot {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return legacyFixtureSnapshot{data: data, mode: info.Mode(), modTime: info.ModTime()}
}

func assertLegacyFixtureUnchanged(
	t *testing.T,
	path string,
	want legacyFixtureSnapshot,
) {
	t.Helper()
	got := snapshotLegacyFixture(t, path)
	if !bytes.Equal(got.data, want.data) ||
		got.mode != want.mode ||
		!got.modTime.Equal(want.modTime) {
		t.Fatalf("legacy fixture changed: %s", path)
	}
}

func assertNoLegacyGitMutation(t *testing.T, git *fakeGit) {
	t.Helper()
	git.mu.Lock()
	defer git.mu.Unlock()
	if git.addCalls != 0 || git.removeCalls != 0 ||
		git.restoreCalls != 0 || git.deleteCalls != 0 {
		t.Fatalf(
			"legacy inspection mutated Git: add=%d remove=%d restore=%d delete=%d",
			git.addCalls,
			git.removeCalls,
			git.restoreCalls,
			git.deleteCalls,
		)
	}
}
