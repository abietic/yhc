package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteSessionRemovesExactWorkspaceHistory(t *testing.T) {
	dir := t.TempDir()
	writeHistoryDeleteTranscript(t, dir, "target")
	writeHistoryDeleteTranscript(t, dir, "neighbor")
	writeWorkspaceHistory(t, dir, "target")
	neighborJournal := writeWorkspaceHistory(t, dir, "neighbor")

	result, err := DeleteSession(DeleteOptions{SessionID: "target", Dir: dir})
	if err != nil || !result.TranscriptRemoved || !result.CleanupCompleted {
		t.Fatalf("DeleteSession = %#v, %v", result, err)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "target.workspace-history")); err != nil || len(entries) != 0 {
		t.Fatalf("private history bytes remain: %v %v", entries, err)
	}
	if _, err := os.Lstat(neighborJournal); err != nil {
		t.Fatalf("neighbor history changed: %v", err)
	}
}

func TestDeleteSessionRejectsUnsafeWorkspaceHistoryBeforeTranscriptMutation(t *testing.T) {
	for _, setup := range []struct {
		name string
		add  func(t *testing.T, history string)
	}{
		{"unknown-file", func(t *testing.T, history string) {
			if err := os.WriteFile(filepath.Join(history, "other"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"subdirectory", func(t *testing.T, history string) {
			if err := os.Mkdir(filepath.Join(history, "nested"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"journal-link", func(t *testing.T, history string) {
			outside := filepath.Join(t.TempDir(), "private.txt")
			if err := os.WriteFile(outside, []byte("preserve"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(history, workspaceHistoryJournal)); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(history, workspaceHistoryJournal)); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(setup.name, func(t *testing.T) {
			dir := t.TempDir()
			writeHistoryDeleteTranscript(t, dir, "target")
			journal := writeWorkspaceHistory(t, dir, "target")
			history := filepath.Dir(journal)
			setup.add(t, history)
			if _, err := DeleteSession(DeleteOptions{SessionID: "target", Dir: dir}); err == nil {
				t.Fatal("DeleteSession accepted unsafe workspace history")
			}
			if _, err := os.Lstat(filepath.Join(dir, "target.jsonl")); err != nil {
				t.Fatalf("unsafe history removed transcript: %v", err)
			}
		})
	}
}

func TestDeleteSessionRejectsWorkspaceHistoryChangesBeforeMutation(t *testing.T) {
	t.Run("replaced", func(t *testing.T) {
		dir := t.TempDir()
		writeHistoryDeleteTranscript(t, dir, "target")
		writeWorkspaceHistory(t, dir, "target")
		history := filepath.Join(dir, "target.workspace-history")
		if _, err := DeleteSession(DeleteOptions{
			SessionID: "target", Dir: dir,
			beforeMutation: func() {
				if err := os.Remove(filepath.Join(history, workspaceHistoryJournal)); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(history); err != nil {
					t.Fatal(err)
				}
				writeWorkspaceHistory(t, dir, "target")
			},
		}); err == nil {
			t.Fatal("DeleteSession accepted replaced history")
		}
		if _, err := os.Lstat(filepath.Join(dir, "target.jsonl")); err != nil {
			t.Fatalf("transcript removed: %v", err)
		}
	})
	t.Run("appeared", func(t *testing.T) {
		dir := t.TempDir()
		writeHistoryDeleteTranscript(t, dir, "target")
		if _, err := DeleteSession(DeleteOptions{
			SessionID: "target", Dir: dir,
			beforeMutation: func() { writeWorkspaceHistory(t, dir, "target") },
		}); err == nil {
			t.Fatal("DeleteSession accepted newly appeared history")
		}
		if _, err := os.Lstat(filepath.Join(dir, "target.jsonl")); err != nil {
			t.Fatalf("transcript removed: %v", err)
		}
	})
}

func TestDeleteSessionWorkspaceHistoryCleanupRetry(t *testing.T) {
	dir := t.TempDir()
	writeHistoryDeleteTranscript(t, dir, "target")
	journal := writeWorkspaceHistory(t, dir, "target")
	result, err := DeleteSession(DeleteOptions{
		SessionID: "target", Dir: dir,
		removeHistory: func(string) error { return errors.New("injected history failure") },
	})
	if result == nil || !result.TranscriptRemoved || !errors.Is(err, ErrSessionCleanupPending) {
		t.Fatalf("first DeleteSession = %#v, %v", result, err)
	}
	if _, err := os.Lstat(journal); err != nil {
		t.Fatalf("failed cleanup removed history: %v", err)
	}
	retried, err := DeleteSession(DeleteOptions{SessionID: "target", Dir: dir})
	if err != nil || retried.TranscriptRemoved || !retried.CleanupCompleted {
		t.Fatalf("retry DeleteSession = %#v, %v", retried, err)
	}
	if entries, err := os.ReadDir(filepath.Dir(journal)); err != nil || len(entries) != 0 {
		t.Fatalf("private history bytes remain after retry: %v %v", entries, err)
	}
}

func writeHistoryDeleteTranscript(t *testing.T, dir, sessionID string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeWorkspaceHistory(t *testing.T, dir, sessionID string) string {
	t.Helper()
	history := filepath.Join(dir, sessionID+workspaceHistorySuffix)
	if err := os.Mkdir(history, 0o700); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(history, workspaceHistoryJournal)
	if err := os.WriteFile(journal, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return journal
}
