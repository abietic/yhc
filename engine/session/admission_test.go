package session

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/internal/workboard"
	"github.com/abietic/yhc/engine/transcript"
)

func TestSessionResumeAdmissionReturnsTypedImportRequiredWithoutWrites(t *testing.T) {
	fixture := newSessionImportFixture(t, true)

	_, err := AdmitSessionResume(t.Context(), ResumeAdmissionRequest{
		SessionID:         fixture.sessionID,
		CWD:               fixture.project,
		CatalogPath:       fixture.canonicalCatalog,
		LegacyCatalogPath: fixture.legacyCatalog,
		UserRoots:         fixture.roots,
	})
	if !errors.Is(err, ErrLegacySessionImportRequired) {
		t.Fatalf("admission error = %v, want import required", err)
	}
	var required *LegacySessionImportRequiredError
	if !errors.As(err, &required) || required.SessionID != fixture.sessionID {
		t.Fatalf("typed admission error = %#v", required)
	}
	target, ok := LegacySessionImportTarget(err)
	if !ok || target.SessionID != fixture.sessionID ||
		target.CWD != fixture.project || target.TranscriptDir != fixture.legacyDir ||
		!target.ReadOnly || !target.NeedsImport {
		t.Fatalf("legacy admission target = %#v, ok=%v", target, ok)
	}
	fixture.assertLegacyUnchanged(t)
	for _, path := range []string{fixture.canonicalDir, fixture.canonicalCatalog} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("read-only admission created %q: %v", path, statErr)
		}
	}
}

func TestSessionResumeAdmissionReturnsExactCanonicalSource(t *testing.T) {
	fixture := newSessionImportFixture(t, false)
	if _, err := ImportSessionForResume(t.Context(), fixture.request(true)); err != nil {
		t.Fatal(err)
	}

	info, err := AdmitSessionResume(t.Context(), ResumeAdmissionRequest{
		SessionID:         fixture.sessionID,
		CWD:               fixture.project,
		CatalogPath:       fixture.canonicalCatalog,
		LegacyCatalogPath: fixture.legacyCatalog,
		UserRoots:         fixture.roots,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.SessionID != fixture.sessionID || info.CWD != fixture.project ||
		info.TranscriptDir != fixture.canonicalDir || info.ReadOnly || info.NeedsImport {
		t.Fatalf("canonical admission = %#v", info)
	}
}

func TestSessionResumeAdmissionAllowsCanonicalRefreshAndContinuationAfterImport(t *testing.T) {
	fixture := newSessionImportFixture(t, true)
	root, err := ImportSessionForResume(t.Context(), fixture.request(true))
	if err != nil {
		t.Fatal(err)
	}
	recorder := transcript.NewRecorder(fixture.sessionID, fixture.canonicalDir)
	if err := recorder.Record([]*schema.Message{{
		Role: schema.Assistant, Content: "canonical continuation",
	}}, false); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := workboard.NewStore(workboard.StoreConfig{
		Dir: fixture.canonicalDir, SessionID: fixture.sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	next := state.Record
	next.Board.Revision++
	if _, err := store.Commit(
		state.Record.BoardID,
		state.Record.Board.Revision,
		next,
	); err != nil {
		t.Fatal(err)
	}
	if err := RegisterSessionRoot(
		fixture.canonicalCatalog,
		fixture.project,
		fixture.canonicalDir,
		root.UpdatedAt.Add(time.Minute),
	); err != nil {
		t.Fatal(err)
	}

	info, err := AdmitSessionResume(t.Context(), ResumeAdmissionRequest{
		SessionID:         fixture.sessionID,
		CWD:               fixture.project,
		CatalogPath:       fixture.canonicalCatalog,
		LegacyCatalogPath: fixture.legacyCatalog,
		UserRoots:         fixture.roots,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.SessionID != fixture.sessionID || info.TranscriptDir != fixture.canonicalDir ||
		info.ReadOnly || info.NeedsImport {
		t.Fatalf("refreshed canonical admission = %#v", info)
	}
}

func TestSessionResumeAdmissionRejectsUnsafeCanonicalCatalog(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "non-private mode",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "multiple links",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Link(path, path+".hardlink"); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSessionImportFixture(t, false)
			if _, err := ImportSessionForResume(t.Context(), fixture.request(true)); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, fixture.canonicalCatalog)

			_, err := AdmitSessionResume(t.Context(), ResumeAdmissionRequest{
				SessionID:         fixture.sessionID,
				CWD:               fixture.project,
				CatalogPath:       fixture.canonicalCatalog,
				LegacyCatalogPath: fixture.legacyCatalog,
				UserRoots:         fixture.roots,
			})
			if !errors.Is(err, ErrSessionImportUnsafe) {
				t.Fatalf("unsafe catalog admission error = %v", err)
			}
		})
	}
}

func TestSessionResumeAdmissionRejectsTamperedCommittedBundle(t *testing.T) {
	fixture := newSessionImportFixture(t, false)
	if _, err := ImportSessionForResume(t.Context(), fixture.request(true)); err != nil {
		t.Fatal(err)
	}
	canonicalPath := filepath.Join(fixture.canonicalDir, fixture.sessionID+".jsonl")
	if err := os.Chmod(canonicalPath, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := AdmitSessionResume(t.Context(), ResumeAdmissionRequest{
		SessionID:         fixture.sessionID,
		CWD:               fixture.project,
		CatalogPath:       fixture.canonicalCatalog,
		LegacyCatalogPath: fixture.legacyCatalog,
		UserRoots:         fixture.roots,
	})
	if !errors.Is(err, ErrSessionImportUnsafe) {
		t.Fatalf("tampered committed bundle admission error = %v", err)
	}
	fixture.assertLegacyUnchanged(t)
}

func TestSessionResumeAdmissionAllowsOrdinaryCanonicalWithExplicitCatalog(t *testing.T) {
	fixture := newSessionImportFixture(t, false)
	legacyPath := filepath.Join(fixture.legacyDir, fixture.sessionID+".jsonl")
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fixture.canonicalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(fixture.canonicalDir, fixture.sessionID+".jsonl"),
		data,
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	info, err := AdmitSessionResume(t.Context(), ResumeAdmissionRequest{
		SessionID:   fixture.sessionID,
		CWD:         fixture.project,
		CatalogPath: filepath.Join(fixture.home, "custom-session-roots.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.TranscriptDir != fixture.canonicalDir || info.ReadOnly || info.NeedsImport {
		t.Fatalf("ordinary canonical admission = %#v", info)
	}
	fixture.assertLegacyUnchanged(t)
}

func TestSessionResumeAdmissionPreservesFallbackCandidatePhysicalRoot(t *testing.T) {
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	transcriptDir := filepath.Join(project, ".yhc", "transcripts")
	if err := os.MkdirAll(transcriptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transcriptDir, "fallback.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := AdmitSessionResume(t.Context(), ResumeAdmissionRequest{
		SessionID: "fallback",
		CWD:       project,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.SessionID != "fallback" || info.TranscriptDir != transcriptDir ||
		info.ReadOnly || info.NeedsImport {
		t.Fatalf("fallback admission = %#v", info)
	}
}

func TestSessionResumeAdmissionRejectsLegacyRootMisclassifiedAsCanonical(t *testing.T) {
	fixture := newSessionImportFixture(t, false)

	_, err := AdmitSessionResume(t.Context(), ResumeAdmissionRequest{
		SessionID:   fixture.sessionID,
		CWD:         fixture.project,
		CatalogPath: fixture.legacyCatalog,
		UserRoots:   fixture.roots,
	})
	if !errors.Is(err, ErrLegacySessionImportRequired) {
		t.Fatalf("misclassified legacy admission error = %v", err)
	}
	fixture.assertLegacyUnchanged(t)
	for _, path := range []string{fixture.canonicalDir, fixture.canonicalCatalog} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("misclassified legacy admission created %q: %v", path, statErr)
		}
	}
}

func TestSessionResumeAdmissionRejectsPartialBundleWithoutWrites(t *testing.T) {
	fixture := newSessionImportFixture(t, false)
	failed := false
	ctx := withSessionImportFailureHook(t.Context(), func(
		stage sessionImportFailureStage,
		_ string,
	) error {
		if stage == failureAfterFileSync && !failed {
			failed = true
			return errors.New("stop after first promoted file")
		}
		return nil
	})
	if _, err := ImportSessionForResume(ctx, fixture.request(true)); !errors.Is(err, ErrSessionImportUnsafe) {
		t.Fatalf("partial import error = %v", err)
	}
	if !failed {
		t.Fatal("partial import failure hook did not run")
	}
	before := snapshotAdmissionTrees(
		t,
		filepath.Join(fixture.project, ".yhc"),
		fixture.roots.Canonical,
	)

	_, err := AdmitSessionResume(t.Context(), ResumeAdmissionRequest{
		SessionID:         fixture.sessionID,
		CWD:               fixture.project,
		CatalogPath:       fixture.canonicalCatalog,
		LegacyCatalogPath: fixture.legacyCatalog,
		UserRoots:         fixture.roots,
	})
	if !errors.Is(err, ErrSessionImportUnsafe) {
		t.Fatalf("partial bundle admission error = %v", err)
	}
	after := snapshotAdmissionTrees(
		t,
		filepath.Join(fixture.project, ".yhc"),
		fixture.roots.Canonical,
	)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("read-only partial admission changed canonical state\nbefore=%#v\nafter=%#v", before, after)
	}
	fixture.assertLegacyUnchanged(t)
}

func snapshotAdmissionTrees(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	for index, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			value := info.Mode().String()
			if info.Mode().IsRegular() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				value += fmt.Sprintf(":%x", data)
			}
			snapshot[fmt.Sprintf("%d:%s", index, relative)] = value
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}
