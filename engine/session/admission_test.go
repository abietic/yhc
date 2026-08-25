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

func TestImportDiscoveredLegacySessionRequiresAttestationBeforeWrites(t *testing.T) {
	fixture := newSessionImportFixture(t, false)
	t.Setenv("HOME", fixture.home)
	info := resolveLegacyImportFixture(t, fixture)

	_, err := ImportDiscoveredLegacySession(t.Context(), ImportDiscoveredLegacySessionRequest{
		Info:              info,
		CatalogPath:       fixture.canonicalCatalog,
		LegacyCatalogPath: fixture.legacyCatalog,
		UserRoots:         fixture.roots,
		Now:               time.Date(2026, 8, 10, 0, 1, 0, 0, time.UTC),
	})
	if !errors.Is(err, ErrSessionImportAttestationRequired) {
		t.Fatalf("unattested import error = %v", err)
	}
	fixture.assertLegacyUnchanged(t)
	for _, path := range []string{fixture.canonicalDir, fixture.canonicalCatalog} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("unattested facade created %q: %v", path, statErr)
		}
	}
}

func TestImportDiscoveredLegacySessionRequiresFreshProvenanceAndDefaultPair(t *testing.T) {
	fixture := newSessionImportFixture(t, false)
	t.Setenv("HOME", fixture.home)
	base := ImportDiscoveredLegacySessionRequest{
		Info:                 resolveLegacyImportFixture(t, fixture),
		CatalogPath:          fixture.canonicalCatalog,
		LegacyCatalogPath:    fixture.legacyCatalog,
		UserRoots:            fixture.roots,
		ConfirmLegacyStopped: true,
		Now:                  time.Date(2026, 8, 10, 0, 1, 0, 0, time.UTC),
	}

	t.Run("missing provenance", func(t *testing.T) {
		request := base
		request.Info.sourceCWD = ""
		if _, err := ImportDiscoveredLegacySession(t.Context(), request); !errors.Is(err, ErrSessionImportUnsafe) {
			t.Fatalf("missing provenance error = %v", err)
		}
	})
	t.Run("changed source", func(t *testing.T) {
		request := base
		request.Info.sourceCWD = t.TempDir()
		if _, err := ImportDiscoveredLegacySession(t.Context(), request); !errors.Is(err, ErrSessionImportUnsafe) {
			t.Fatalf("changed source error = %v", err)
		}
	})
	t.Run("changed legacy owner", func(t *testing.T) {
		request := base
		request.Info.TranscriptDir = t.TempDir()
		if _, err := ImportDiscoveredLegacySession(t.Context(), request); !errors.Is(err, ErrSessionImportUnsafe) {
			t.Fatalf("changed owner error = %v", err)
		}
	})
	t.Run("explicit catalog", func(t *testing.T) {
		request := base
		request.CatalogPath = filepath.Join(t.TempDir(), "session-roots.json")
		if _, err := ImportDiscoveredLegacySession(t.Context(), request); !errors.Is(err, ErrSessionImportUnsafe) {
			t.Fatalf("explicit catalog error = %v", err)
		}
	})
	fixture.assertLegacyUnchanged(t)
	for _, path := range []string{fixture.canonicalDir, fixture.canonicalCatalog} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("rejected facade created %q: %v", path, statErr)
		}
	}
}

func TestImportDiscoveredLegacySessionPromotesAndAdmitsIdempotently(t *testing.T) {
	fixture := newSessionImportFixture(t, true)
	t.Setenv("HOME", fixture.home)
	request := ImportDiscoveredLegacySessionRequest{
		Info:                 resolveLegacyImportFixture(t, fixture),
		CatalogPath:          fixture.canonicalCatalog,
		LegacyCatalogPath:    fixture.legacyCatalog,
		UserRoots:            fixture.roots,
		ConfirmLegacyStopped: true,
		Now:                  time.Date(2026, 8, 10, 0, 1, 0, 0, time.UTC),
	}
	// CWD is transcript metadata. The import target must be derived from the
	// non-wire resolved source instead of this caller-visible field.
	request.Info.CWD = t.TempDir()

	admitted, err := ImportDiscoveredLegacySession(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.SessionID != fixture.sessionID || admitted.ReadOnly || admitted.NeedsImport ||
		admitted.TranscriptDir != fixture.canonicalDir {
		t.Fatalf("admitted session = %#v", admitted)
	}
	fixture.assertLegacyUnchanged(t)

	// Re-use the original legacy discovery. The import transaction reports its
	// committed marker, and the facade must still return the canonical admission.
	admitted, err = ImportDiscoveredLegacySession(t.Context(), request)
	if err != nil {
		t.Fatalf("idempotent import: %v", err)
	}
	if admitted.SessionID != fixture.sessionID || admitted.ReadOnly || admitted.NeedsImport ||
		admitted.TranscriptDir != fixture.canonicalDir {
		t.Fatalf("idempotent admission = %#v", admitted)
	}
}

func resolveLegacyImportFixture(t *testing.T, fixture *sessionImportFixture) SessionInfo {
	t.Helper()
	info, err := ResolveSession(SessionQuery{
		Scope:             SessionScopeCWD,
		CWD:               fixture.project,
		CatalogPath:       fixture.canonicalCatalog,
		LegacyCatalogPath: fixture.legacyCatalog,
	}, fixture.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasResolvedSource() || !info.ReadOnly || !info.NeedsImport {
		t.Fatalf("legacy discovery = %#v", info)
	}
	return info
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
