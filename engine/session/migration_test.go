package session

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/internal/workboard"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/internal/statepath"
)

func TestImportSessionBundleCommitsTranscriptWorkBoardAndCatalogTogether(t *testing.T) {
	fixture := newSessionImportFixture(t, true)

	root, err := ImportSessionForResume(context.Background(), fixture.request(true))
	if err != nil {
		t.Fatalf("import session bundle: %v", err)
	}
	if root.CWD != fixture.project || root.TranscriptDir != fixture.canonicalDir {
		t.Fatalf("imported root = %#v", root)
	}
	fixture.assertLegacyUnchanged(t)
	fixture.assertCanonicalBundle(t)

	roots, err := LoadSessionRoots(fixture.canonicalCatalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0].CWD != fixture.project || roots[0].TranscriptDir != fixture.canonicalDir {
		t.Fatalf("canonical catalog roots = %#v", roots)
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
	if state.Mode != workboard.AuthorityModeWorkBoard ||
		state.Record.BoardID != "board-import" || state.Record.Board.Revision != 7 {
		t.Fatalf("canonical WorkBoard state = %#v", state)
	}
}

func TestResumeLegacyCatalogIsReadOnlyUntilBundlePromotion(t *testing.T) {
	fixture := newSessionImportFixture(t, false)
	query := SessionQuery{
		Scope:             SessionScopeCWD,
		CWD:               fixture.project,
		TranscriptDir:     fixture.canonicalDir,
		CatalogPath:       fixture.canonicalCatalog,
		LegacyCatalogPath: fixture.legacyCatalog,
		Limit:             10,
	}

	before, err := ResolveSession(query, fixture.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ReadOnly || !before.NeedsImport || before.TranscriptDir != fixture.legacyDir {
		t.Fatalf("legacy target before import = %#v", before)
	}

	if _, err := ImportSessionForResume(context.Background(), fixture.request(true)); err != nil {
		t.Fatal(err)
	}
	after, err := ResolveSession(query, fixture.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ReadOnly || after.NeedsImport || after.TranscriptDir != fixture.canonicalDir {
		t.Fatalf("canonical target after import = %#v", after)
	}
}

func TestSessionImportRequiresExplicitAttestationAndStableSnapshot(t *testing.T) {
	t.Run("attestation", func(t *testing.T) {
		fixture := newSessionImportFixture(t, false)
		_, err := ImportSessionForResume(context.Background(), fixture.request(false))
		if !errors.Is(err, ErrSessionImportAttestationRequired) {
			t.Fatalf("import error = %v, want attestation required", err)
		}
		fixture.assertLegacyUnchanged(t)
		for _, path := range []string{fixture.canonicalDir, fixture.roots.Canonical} {
			if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("unattested import created %q: %v", path, statErr)
			}
		}
	})

	t.Run("stable pinned snapshots", func(t *testing.T) {
		fixture := newSessionImportFixture(t, false)
		mutated := false
		ctx := withSessionImportFailureHook(context.Background(), func(stage sessionImportFailureStage, _ string) error {
			if stage != failureAfterFirstSourceSnapshot || mutated {
				return nil
			}
			mutated = true
			file, err := os.OpenFile(
				filepath.Join(fixture.legacyDir, fixture.sessionID+".jsonl"),
				os.O_APPEND|os.O_WRONLY,
				0,
			)
			if err != nil {
				return err
			}
			defer file.Close() //nolint:errcheck
			_, err = file.WriteString("{\"partial\":")
			return err
		})
		_, err := ImportSessionForResume(ctx, fixture.request(true))
		if !errors.Is(err, ErrSessionImportUnsafe) {
			t.Fatalf("unstable import error = %v, want unsafe", err)
		}
		if !mutated {
			t.Fatal("source instability hook did not run")
		}
		if _, statErr := os.Lstat(fixture.canonicalCatalog); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("unstable import committed catalog: %v", statErr)
		}
	})
}

func TestSessionImportRejectsSymlinkReplacementCollisionAndUnsafeMode(t *testing.T) {
	t.Run("symlink collision", func(t *testing.T) {
		fixture := newSessionImportFixture(t, false)
		if err := os.MkdirAll(fixture.canonicalDir, 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.jsonl")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(fixture.canonicalDir, fixture.sessionID+".jsonl")); err != nil {
			t.Fatal(err)
		}
		_, err := ImportSessionForResume(context.Background(), fixture.request(true))
		if !errors.Is(err, ErrSessionImportCollision) && !errors.Is(err, ErrSessionImportUnsafe) {
			t.Fatalf("collision error = %v", err)
		}
		data, readErr := os.ReadFile(outside)
		if readErr != nil || string(data) != "outside" {
			t.Fatalf("outside target changed: %q, %v", data, readErr)
		}
		fixture.assertLegacyUnchanged(t)
	})

	t.Run("unsafe legacy mode", func(t *testing.T) {
		fixture := newSessionImportFixture(t, false)
		path := filepath.Join(fixture.legacyDir, fixture.sessionID+".jsonl")
		if err := os.Chmod(path, 0o666); err != nil {
			t.Fatal(err)
		}
		_, err := ImportSessionForResume(context.Background(), fixture.request(true))
		if !errors.Is(err, ErrSessionImportUnsafe) {
			t.Fatalf("unsafe mode error = %v", err)
		}
		if _, statErr := os.Lstat(fixture.canonicalCatalog); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("unsafe import committed catalog: %v", statErr)
		}
	})
}

func TestSessionImportConcurrentSingleWinner(t *testing.T) {
	fixture := newSessionImportFixture(t, true)
	request := fixture.request(true)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := ImportSessionForResume(context.Background(), request)
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	successes := 0
	alreadyCommitted := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrSessionImportAlreadyCommitted):
			alreadyCommitted++
		default:
			t.Fatalf("concurrent import error = %v", err)
		}
	}
	if successes != 1 || alreadyCommitted != 1 {
		t.Fatalf("concurrent outcomes: success=%d already=%d", successes, alreadyCommitted)
	}
	fixture.assertLegacyUnchanged(t)
	fixture.assertCanonicalBundle(t)
}

func TestSessionImportConcurrentCatalogUpdatesDoNotLoseRoots(t *testing.T) {
	first := newSessionImportFixture(t, false)
	second := newSessionImportFixture(t, false)
	second.roots = first.roots
	second.canonicalCatalog = first.canonicalCatalog

	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, fixture := range []*sessionImportFixture{first, second} {
		wait.Add(1)
		go func(current *sessionImportFixture) {
			defer wait.Done()
			<-start
			_, err := ImportSessionForResume(context.Background(), current.request(true))
			results <- err
		}(fixture)
	}
	close(start)
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent catalog import: %v", err)
		}
	}
	roots, err := LoadSessionRoots(first.canonicalCatalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 {
		t.Fatalf("concurrent catalog roots = %#v", roots)
	}
}

func TestSessionImportPreservesTruncatedTailRecovery(t *testing.T) {
	fixture := newSessionImportFixture(t, false)
	legacyPath := filepath.Join(fixture.legacyDir, fixture.sessionID+".jsonl")
	file, err := os.OpenFile(legacyPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{\"timestamp\":\"truncated"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.captureLegacy(t)

	legacyLoaded, err := transcript.NewRecorder(fixture.sessionID, fixture.legacyDir).LoadFullContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(legacyLoaded.Corruptions) == 0 {
		t.Fatal("legacy fixture did not expose truncated-tail corruption")
	}
	if _, err := ImportSessionForResume(context.Background(), fixture.request(true)); err != nil {
		t.Fatal(err)
	}
	canonicalLoaded, err := transcript.NewRecorder(fixture.sessionID, fixture.canonicalDir).LoadFullContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(canonicalLoaded.Messages) != len(legacyLoaded.Messages) ||
		len(canonicalLoaded.Corruptions) != len(legacyLoaded.Corruptions) {
		t.Fatalf("canonical recovery = (%d messages, %d corruptions), legacy = (%d, %d)",
			len(canonicalLoaded.Messages), len(canonicalLoaded.Corruptions),
			len(legacyLoaded.Messages), len(legacyLoaded.Corruptions))
	}
	fixture.assertLegacyUnchanged(t)
}

func TestSessionImportPreservesWorkBoardAuthorityAndRevision(t *testing.T) {
	fixture := newSessionImportFixture(t, true)
	legacyStore, err := workboard.NewStore(workboard.StoreConfig{
		Dir: fixture.legacyDir, SessionID: fixture.sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := legacyStore.Inspect()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ImportSessionForResume(context.Background(), fixture.request(true)); err != nil {
		t.Fatal(err)
	}
	canonicalStore, err := workboard.NewStore(workboard.StoreConfig{
		Dir: fixture.canonicalDir, SessionID: fixture.sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := canonicalStore.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode != after.Mode || before.Record.BoardID != after.Record.BoardID ||
		before.Record.Board.Revision != after.Record.Board.Revision ||
		before.Backup.BoardID != after.Backup.BoardID ||
		before.Backup.Board.Revision != after.Backup.Board.Revision {
		t.Fatalf("WorkBoard authority changed: before=%#v after=%#v", before, after)
	}
}

func TestSessionImportLeavesLegacyBundleUnchanged(t *testing.T) {
	fixture := newSessionImportFixture(t, true)
	if _, err := ImportSessionForResume(context.Background(), fixture.request(true)); err != nil {
		t.Fatal(err)
	}
	fixture.assertLegacyUnchanged(t)
}

type sessionImportFixture struct {
	project          string
	home             string
	legacyDir        string
	canonicalDir     string
	legacyCatalog    string
	canonicalCatalog string
	sessionID        string
	roots            statepath.Roots
	legacy           map[string]sessionImportFileState
}

type sessionImportFileState struct {
	data    []byte
	mode    os.FileMode
	modTime time.Time
}

func newSessionImportFixture(t *testing.T, withWorkBoard bool) *sessionImportFixture {
	t.Helper()
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	roots, err := statepath.UserRoots(home)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &sessionImportFixture{
		project:          project,
		home:             home,
		legacyDir:        filepath.Join(project, ".eino-agent", "transcripts"),
		canonicalDir:     filepath.Join(project, ".yhc", "transcripts"),
		legacyCatalog:    filepath.Join(roots.Legacy, "session-roots.json"),
		canonicalCatalog: filepath.Join(roots.Canonical, "session-roots.json"),
		sessionID:        "session-import",
		roots:            roots,
	}
	recorder := transcript.NewRecorder(fixture.sessionID, fixture.legacyDir)
	if err := recorder.Record([]*schema.Message{{Role: schema.User, Content: "preserve this session"}}, false); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	if withWorkBoard {
		store, err := workboard.NewStore(workboard.StoreConfig{
			Dir: fixture.legacyDir, SessionID: fixture.sessionID,
		})
		if err != nil {
			t.Fatal(err)
		}
		record := workboard.AuthorityRecord{
			Version:   workboard.AuthorityRecordVersion,
			SessionID: fixture.sessionID,
			BoardID:   "board-import",
			Board: workboard.Board{
				Revision: 7, NextTodoID: 1, Items: []workboard.WorkItem{},
			},
			Compatibility: workboard.CompatibilityPayload{
				NextTaskID: 1,
				Tasks:      []workboard.TaskCompatibility{},
				TodoScopes: []workboard.TodoScopeCompatibility{},
			},
		}
		backup := workboard.LegacyBackup{
			Version:       workboard.LegacyBackupVersion,
			SessionID:     fixture.sessionID,
			BoardID:       record.BoardID,
			Board:         record.Board,
			Compatibility: record.Compatibility,
		}
		if _, err := store.Cutover(record, backup); err != nil {
			t.Fatal(err)
		}
	}
	if err := RegisterSessionRoot(
		fixture.legacyCatalog,
		fixture.project,
		fixture.legacyDir,
		time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
	); err != nil {
		t.Fatal(err)
	}
	fixture.captureLegacy(t)
	return fixture
}

func (fixture *sessionImportFixture) request(confirm bool) ImportRequest {
	return ImportRequest{
		Target: LegacySessionTarget{
			SessionID:     fixture.sessionID,
			CWD:           fixture.project,
			TranscriptDir: fixture.legacyDir,
			ReadOnly:      true,
			NeedsImport:   true,
		},
		UserRoots:            fixture.roots,
		ConfirmLegacyStopped: confirm,
		Now:                  time.Date(2026, 8, 10, 0, 1, 0, 0, time.UTC),
	}
}

func (fixture *sessionImportFixture) captureLegacy(t *testing.T) {
	t.Helper()
	fixture.legacy = make(map[string]sessionImportFileState)
	for _, name := range sessionBundleNames(fixture.sessionID) {
		path := filepath.Join(fixture.legacyDir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fixture.legacy[name] = sessionImportFileState{
			data: append([]byte(nil), data...), mode: info.Mode(), modTime: info.ModTime(),
		}
	}
}

func (fixture *sessionImportFixture) assertLegacyUnchanged(t *testing.T) {
	t.Helper()
	for name, expected := range fixture.legacy {
		path := filepath.Join(fixture.legacyDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read legacy %s: %v", name, err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("stat legacy %s: %v", name, err)
		}
		if !bytes.Equal(data, expected.data) || info.Mode() != expected.mode ||
			!info.ModTime().Equal(expected.modTime) {
			t.Fatalf("legacy %s changed", name)
		}
	}
}

func (fixture *sessionImportFixture) assertCanonicalBundle(t *testing.T) {
	t.Helper()
	for name, expected := range fixture.legacy {
		path := filepath.Join(fixture.canonicalDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read canonical %s: %v", name, err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("stat canonical %s: %v", name, err)
		}
		if !bytes.Equal(data, expected.data) || info.Mode().Perm() != 0o600 {
			t.Fatalf("canonical %s content/mode mismatch: mode=%04o", name, info.Mode().Perm())
		}
	}
}
