package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/internal/workboard"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
)

// --- Deletion Tests ---

func TestDeleteSession_Success(t *testing.T) {
	dir := t.TempDir()

	// Create a session.
	rec := transcript.NewRecorder("delete-me", dir)
	if err := rec.Record([]*schema.Message{{Role: schema.User, Content: "hello"}}, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// Verify it exists.
	if _, err := os.Stat(filepath.Join(dir, "delete-me.jsonl")); err != nil {
		t.Fatalf("session file should exist: %v", err)
		return
	}

	// Delete it.
	result, err := DeleteSession(DeleteOptions{SessionID: "delete-me", Dir: dir})
	if err != nil {
		t.Fatalf("delete failed: %v", err)
		return
	}
	if !result.TranscriptRemoved {
		t.Fatal("expected transcript to be removed")
	}
	if result.BytesFreed <= 0 {
		t.Fatalf("expected positive bytes freed, got %d", result.BytesFreed)
	}

	// Verify it's gone.
	if _, err := os.Stat(filepath.Join(dir, "delete-me.jsonl")); !os.IsNotExist(err) {
		t.Fatal("session file should not exist after deletion")
	}
}

func TestDeleteSession_NonExistent(t *testing.T) {
	dir := t.TempDir()
	_, err := DeleteSession(DeleteOptions{SessionID: "ghost", Dir: dir})
	if err == nil {
		t.Fatal("expected error for non-existent session")
		return
	}
}

func TestDeleteSession_EmptyID(t *testing.T) {
	dir := t.TempDir()
	_, err := DeleteSession(DeleteOptions{Dir: dir})
	if err == nil {
		t.Fatal("expected error for empty session ID")
		return
	}
}

func TestDeleteSession_RejectsUnsafeIDsWithoutMutation(t *testing.T) {
	for _, id := range []string{"../victim", "/absolute", "nested/session", `nested\session`} {
		t.Run(id, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "transcripts")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			unsafeTarget := filepath.Join(dir, id+".jsonl")
			if err := os.MkdirAll(filepath.Dir(unsafeTarget), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(unsafeTarget, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}

			if _, err := DeleteSession(DeleteOptions{SessionID: id, Dir: dir}); err == nil {
				t.Fatal("expected unsafe ID to be rejected")
			}
			if data, err := os.ReadFile(unsafeTarget); err != nil {
				t.Fatalf("rejection removed unsafe target: %v", err)
			} else if string(data) != "keep" {
				t.Fatalf("rejection changed unsafe target to %q", data)
			}
		})
	}
}

func TestDeleteSession_MissingTranscriptWrapsNotExist(t *testing.T) {
	_, err := DeleteSession(DeleteOptions{SessionID: "missing", Dir: t.TempDir()})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}

func TestDeleteSession_WorkBoardPartialCleanupRetry(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const sessionID = "workboard-cleanup-retry"
	recorder := transcript.NewRecorder(sessionID, dir)
	if err := recorder.Record(
		[]*schema.Message{{Role: schema.User, Content: "hello"}},
		false,
	); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	manager := tools.NewTaskManager()
	if _, err := workboard.BindLogicalWorkAdapter(
		workboard.AdapterConfig{
			SessionID:   sessionID,
			Dir:         dir,
			LeaderScope: tools.TodoScope{SessionID: sessionID},
		},
		manager,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CreateWithError(
		"task",
		"description",
		"",
		nil,
	); err != nil {
		t.Fatal(err)
	}

	removals := 0
	result, err := DeleteSession(DeleteOptions{
		SessionID: sessionID,
		Dir:       dir,
		removeWorkBoard: func(path string) error {
			if _, statErr := os.Lstat(
				filepath.Join(dir, sessionID+".jsonl"),
			); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf(
					"WorkBoard cleanup ran before transcript removal: %v",
					statErr,
				)
			}
			removals++
			if removals == 2 {
				return errors.New("injected cleanup failure")
			}
			return os.Remove(path)
		},
	})
	if result == nil || !result.TranscriptRemoved {
		t.Fatalf("partial delete result = %#v", result)
	}
	if !errors.Is(err, ErrSessionCleanupPending) {
		t.Fatalf("partial delete error = %v", err)
	}

	retried, err := DeleteSession(DeleteOptions{
		SessionID: sessionID,
		Dir:       dir,
	})
	if err != nil {
		t.Fatalf("retry cleanup: %v", err)
	}
	if retried.TranscriptRemoved ||
		!retried.WorkBoardAuthorityRemoved ||
		!retried.CleanupCompleted {
		t.Fatalf("retry cleanup result = %#v", retried)
	}
	for _, suffix := range []string{
		workboard.AuthorityMarkerSuffix,
		workboard.AuthorityRecordSuffix,
		workboard.LegacyBackupSuffix,
	} {
		if _, statErr := os.Lstat(
			filepath.Join(dir, sessionID+suffix),
		); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("WorkBoard artifact %s remains: %v", suffix, statErr)
		}
	}
}

func TestDeleteSession_PreflightRejectsUnsafeOwnedTargetsWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, suffix := range append([]string{""}, ".tmp", runtimeInputSidecarSuffix, projectGraphSidecarSuffix) {
		t.Run(suffix, func(t *testing.T) {
			id := "unsafe" + strings.ReplaceAll(suffix, ".", "-")
			transcriptPath := filepath.Join(dir, id+".jsonl")
			if err := os.WriteFile(transcriptPath, []byte("transcript"), 0o600); err != nil {
				t.Fatal(err)
			}
			unsafePath := transcriptPath + suffix
			if suffix == "" {
				if err := os.Remove(transcriptPath); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(outside, unsafePath); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			if _, err := DeleteSession(DeleteOptions{SessionID: id, Dir: dir}); err == nil {
				t.Fatal("expected unsafe target rejection")
			}
			if suffix != "" {
				if _, err := os.Stat(transcriptPath); err != nil {
					t.Fatalf("preflight rejection removed transcript: %v", err)
				}
			}
			if info, err := os.Lstat(unsafePath); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("preflight rejection mutated unsafe target: %v, %v", info, err)
			}
			if data, err := os.ReadFile(outside); err != nil || string(data) != "outside" {
				t.Fatalf("preflight rejection mutated outside file: %q, %v", data, err)
			}
		})
	}
}

func TestDeleteSession_RejectsNonRegularTranscriptWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "directory.jsonl")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := DeleteSession(DeleteOptions{SessionID: "directory", Dir: dir}); err == nil {
		t.Fatal("expected non-regular transcript rejection")
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("rejection mutated directory: %v, %v", info, err)
	}
}

func TestDeleteSession_ResolvesRootSymlinkAndPreservesNeighbor(t *testing.T) {
	realDir := t.TempDir()
	rootLink := filepath.Join(t.TempDir(), "transcripts")
	if err := os.Symlink(realDir, rootLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	for _, suffix := range []string{"", ".tmp", runtimeInputSidecarSuffix, projectGraphSidecarSuffix} {
		if err := os.WriteFile(filepath.Join(realDir, "session.jsonl")+suffix, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	neighbor := filepath.Join(realDir, "session.jsonl.unrelated")
	if err := os.WriteFile(neighbor, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DeleteSession(DeleteOptions{SessionID: "session", Dir: rootLink}); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", ".tmp", runtimeInputSidecarSuffix, projectGraphSidecarSuffix} {
		if _, err := os.Lstat(filepath.Join(realDir, "session.jsonl") + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owned target %q remains: %v", suffix, err)
		}
	}
	if data, err := os.ReadFile(neighbor); err != nil || string(data) != "keep" {
		t.Fatalf("unrelated neighbor changed: %q, %v", data, err)
	}
}

func TestDeleteSession_UpdatesParentLineage(t *testing.T) {
	dir := t.TempDir()

	// Create parent session.
	parentRec := transcript.NewRecorder("parent-session", dir)
	if err := parentRec.Record([]*schema.Message{
		{Role: schema.User, Content: "parent msg"},
	}, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := parentRec.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	// Create a branch from parent.
	_, err := parentRec.Branch("child-session", 1)
	if err != nil {
		t.Fatal(err)
		return
	}

	// Verify parent has a child.
	branches, err := ListBranches("parent-session", dir)
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(branches) != 1 {
		t.Fatalf("expected 1 branch, got %d", len(branches))
	}

	// Delete the child.
	result, err := DeleteSession(DeleteOptions{SessionID: "child-session", Dir: dir})
	if err != nil {
		t.Fatalf("delete child: %v", err)
		return
	}
	if !result.ParentUpdated {
		t.Fatal("expected parent to be updated (became leaf)")
	}

	// Verify parent now has no children.
	branches, err = ListBranches("parent-session", dir)
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(branches) != 0 {
		t.Fatalf("expected 0 branches after delete, got %d", len(branches))
	}
}

func TestDeleteSession_RemovesTmpFile(t *testing.T) {
	dir := t.TempDir()

	// Create session with a leftover .tmp file.
	rec := transcript.NewRecorder("with-tmp", dir)
	if err := rec.Record([]*schema.Message{{Role: schema.User, Content: "msg"}}, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
		return
	}
	// Create a fake .tmp file.
	tmpPath := filepath.Join(dir, "with-tmp.jsonl.tmp")
	if err := os.WriteFile(tmpPath, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
		return
	}

	// Delete session.
	_, err := DeleteSession(DeleteOptions{SessionID: "with-tmp", Dir: dir})
	if err != nil {
		t.Fatal(err)
		return
	}

	// Verify .tmp is also gone.
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatal(".tmp file should be removed")
	}
}

func TestDeleteSession_RemovesRuntimeAndProjectGraphSidecars(t *testing.T) {
	dir := t.TempDir()
	sessionID := "with-sidecars"
	rec := transcript.NewRecorder(sessionID, dir)
	if err := rec.Record(
		[]*schema.Message{{Role: schema.User, Content: "hello"}},
		false,
	); err != nil {
		t.Fatal(err)
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{
		runtimeInputSidecarSuffix,
		projectGraphSidecarSuffix,
	} {
		if err := os.WriteFile(
			rec.Path()+suffix,
			[]byte(`{"version":1}`),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := DeleteSession(DeleteOptions{
		SessionID: sessionID,
		Dir:       dir,
	}); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{
		runtimeInputSidecarSuffix,
		projectGraphSidecarSuffix,
	} {
		if _, err := os.Stat(rec.Path() + suffix); !os.IsNotExist(err) {
			t.Fatalf("sidecar %q still exists: %v", suffix, err)
		}
	}
}

func TestDeleteSessionRemovesExactWorkBoardShadowSidecar(t *testing.T) {
	dir := t.TempDir()
	const sessionID = "workboard-shadow-delete"
	rec := transcript.NewRecorder(sessionID, dir)
	if err := rec.Record(
		[]*schema.Message{{Role: schema.User, Content: "hello"}},
		false,
	); err != nil {
		t.Fatal(err)
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
	}
	shadowPath, err := workboard.SidecarPath(dir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadowPath, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := DeleteSession(DeleteOptions{SessionID: sessionID, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !result.TranscriptRemoved || !result.WorkBoardShadowRemoved {
		t.Fatalf("delete result = %#v", result)
	}
	if _, err := os.Lstat(shadowPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("WorkBoard shadow still exists: %v", err)
	}
}

func TestDeleteSessionRejectsUnsafeWorkBoardShadowWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	const sessionID = "workboard-shadow-unsafe"
	rec := transcript.NewRecorder(sessionID, dir)
	if err := rec.Record(
		[]*schema.Message{{Role: schema.User, Content: "hello"}},
		false,
	); err != nil {
		t.Fatal(err)
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
	}
	shadowPath, err := workboard.SidecarPath(dir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, shadowPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if _, err := DeleteSession(DeleteOptions{
		SessionID: sessionID,
		Dir:       dir,
	}); err == nil {
		t.Fatal("expected unsafe WorkBoard shadow rejection")
	}
	if _, err := os.Stat(rec.Path()); err != nil {
		t.Fatalf("preflight rejection removed transcript: %v", err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "outside" {
		t.Fatalf("outside target changed: data=%q err=%v", data, err)
	}
}

func TestDeleteSessionRejectsWorkBoardShadowAppearingBeforeMutation(t *testing.T) {
	dir := t.TempDir()
	const sessionID = "workboard-shadow-race"
	rec := transcript.NewRecorder(sessionID, dir)
	if err := rec.Record(
		[]*schema.Message{{Role: schema.User, Content: "hello"}},
		false,
	); err != nil {
		t.Fatal(err)
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
	}
	shadowPath, err := workboard.SidecarPath(dir, sessionID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := DeleteSession(DeleteOptions{
		SessionID: sessionID,
		Dir:       dir,
		beforeMutation: func() {
			if writeErr := os.WriteFile(shadowPath, []byte("new"), 0o600); writeErr != nil {
				t.Errorf("create replacement sidecar: %v", writeErr)
			}
		},
	}); err == nil || !strings.Contains(err.Error(), "appeared before deletion") {
		t.Fatalf("delete error = %v", err)
	}
	if _, err := os.Stat(rec.Path()); err != nil {
		t.Fatalf("race rejection removed transcript: %v", err)
	}
}

// --- Bulk Deletion Tests ---

func TestBulkDeleteSessions_ByAge(t *testing.T) {
	dir := t.TempDir()

	// Create sessions with different mtimes.
	for _, id := range []string{"old-session", "new-session"} {
		rec := transcript.NewRecorder(id, dir)
		if err := rec.Record([]*schema.Message{{Role: schema.User, Content: "msg from " + id}}, false); err != nil {
			t.Fatal(err)
			return
		}
		if err := rec.Flush(); err != nil {
			t.Fatal(err)
			return
		}
	}

	// Make "old-session" have an old mtime.
	oldPath := filepath.Join(dir, "old-session.jsonl")
	oldTime := time.Now().Add(-30 * 24 * time.Hour) // 30 days ago
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
		return
	}

	// Bulk delete sessions older than 7 days.
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	result, err := BulkDeleteSessions(BulkDeleteOptions{
		Dir:       dir,
		OlderThan: cutoff,
	})
	if err != nil {
		t.Fatalf("bulk delete: %v", err)
		return
	}

	if len(result.Deleted) != 1 || result.Deleted[0] != "old-session" {
		t.Fatalf("expected only old-session deleted, got %v", result.Deleted)
	}

	// Verify old session is gone.
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatal("old-session should be deleted")
	}

	// Verify new session still exists.
	if _, err := os.Stat(filepath.Join(dir, "new-session.jsonl")); err != nil {
		t.Fatal("new-session should still exist")
		return
	}
}

func TestBulkDeleteSessions_ByGitBranch(t *testing.T) {
	dir := t.TempDir()

	// Create sessions with different git branches.
	createSessionWithGitBranch(t, dir, "feature-session", "feature/foo")
	createSessionWithGitBranch(t, dir, "main-session", "main")

	result, err := BulkDeleteSessions(BulkDeleteOptions{
		Dir:       dir,
		GitBranch: "feature/foo",
	})
	if err != nil {
		t.Fatalf("bulk delete: %v", err)
		return
	}

	if len(result.Deleted) != 1 || result.Deleted[0] != "feature-session" {
		t.Fatalf("expected feature-session deleted, got %v", result.Deleted)
	}
}

func TestBulkDeleteSessions_RequiresFilter(t *testing.T) {
	dir := t.TempDir()
	_, err := BulkDeleteSessions(BulkDeleteOptions{Dir: dir})
	if err == nil {
		t.Fatal("expected error when no filter criteria specified")
		return
	}
}

// Helper to create a session with a specific git branch in metadata.
func createSessionWithGitBranch(t *testing.T, dir, sessionID, gitBranch string) {
	t.Helper()
	rec := transcript.NewRecorder(sessionID, dir)
	if err := rec.Record([]*schema.Message{{Role: schema.User, Content: "msg"}}, false); err != nil {
		t.Fatal(err)
		return
	}
	if err := rec.RecordMetadata("git_branch", gitBranch); err != nil {
		t.Fatal(err)
		return
	}

	// Also write the git branch in a format ReadSessionLite can find via the
	// extractLastJSONStringField heuristic.
	entry := map[string]interface{}{
		"type":      "metadata",
		"gitBranch": gitBranch,
	}
	data, _ := json.Marshal(entry)
	// Append it manually to the file.
	f, err := os.OpenFile(filepath.Join(dir, sessionID+".jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
		return
	}
	_, _ = f.Write(append(data, '\n'))
	_ = f.Close()
}
