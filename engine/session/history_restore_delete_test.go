package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/filehistory"
)

func createDeletionRestore(t *testing.T, dir string, completed bool) (string, filehistory.RestoreOperation) {
	t.Helper()
	if !filehistory.RestoreFilesystemSupported() {
		t.Skip("guarded restore unsupported")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "target.txt")
	if err := os.WriteFile(path, []byte("dirty before\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	journal, err := filehistory.OpenRevisionJournal(root, filepath.Join(dir, "target.workspace-history"), "target")
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	started := time.Now().UTC()
	if err := journal.RecordTurnStartWithRestoreBoundary("turn", "target", started, true, ""); err != nil {
		t.Fatal(err)
	}
	id, err := journal.Begin("turn", "write", "Write", "target.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("agent after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := journal.Finish(id, "succeeded"); err != nil {
		t.Fatal(err)
	}
	turn := journal.Snapshot().Turns[0]
	turn.FinishedAt = time.Now().UTC()
	turn.TerminalReason = "completed"
	if err := journal.RecordTurnTerminal(turn); err != nil {
		t.Fatal(err)
	}
	op, err := journal.PrepareRestoreOperation("turn", "")
	if err != nil {
		t.Fatal(err)
	}
	op.ConfirmationID = "confirmed"
	op.PermissionRevision = "permission"
	if completed {
		op, err = journal.ApplyRestoreOperation(context.Background(), op)
	} else {
		err = journal.SaveRestoreOperation(op)
	}
	if err != nil {
		t.Fatal(err)
	}
	return root, op
}

func TestDeleteSessionRestoreSidecarsAndPendingAuthority(t *testing.T) {
	t.Run("completed exact private set", func(t *testing.T) {
		dir := t.TempDir()
		writeHistoryDeleteTranscript(t, dir, "target")
		root, _ := createDeletionRestore(t, dir, true)
		if _, err := DeleteSession(DeleteOptions{SessionID: "target", Dir: dir}); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(filepath.Join(dir, "target.workspace-history"))
		if err != nil || len(entries) != 0 {
			t.Fatalf("private bytes remain: %v %v", entries, err)
		}
		actual, err := os.ReadFile(filepath.Join(root, "target.txt"))
		if err != nil || string(actual) != "dirty before\r\n" {
			t.Fatal("session deletion changed restored workspace file")
		}
	})
	t.Run("pending refuses before transcript mutation", func(t *testing.T) {
		dir := t.TempDir()
		writeHistoryDeleteTranscript(t, dir, "target")
		_, _ = createDeletionRestore(t, dir, false)
		if _, err := DeleteSession(DeleteOptions{SessionID: "target", Dir: dir}); err == nil {
			t.Fatal("pending restore authority was deleted")
		}
		if _, err := os.Stat(filepath.Join(dir, "target.jsonl")); err != nil {
			t.Fatal("pending rejection removed transcript")
		}
	})
	t.Run("sibling artifact keeps recovery authority", func(t *testing.T) {
		dir := t.TempDir()
		writeHistoryDeleteTranscript(t, dir, "target")
		root, op := createDeletionRestore(t, dir, true)
		sibling := filepath.Join(root, op.Paths[0].BackupPath)
		if err := os.WriteFile(sibling, []byte("external replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := DeleteSession(DeleteOptions{SessionID: "target", Dir: dir}); err == nil {
			t.Fatal("orphaned restore artifact lost authority")
		}
		actual, err := os.ReadFile(sibling)
		if err != nil || string(actual) != "external replacement" {
			t.Fatal("deletion changed unknown sibling")
		}
		if _, err := os.Stat(filepath.Join(dir, "target.jsonl")); err != nil {
			t.Fatal("artifact rejection removed transcript")
		}
	})
	t.Run("unpublished private temporary", func(t *testing.T) {
		dir := t.TempDir()
		writeHistoryDeleteTranscript(t, dir, "target")
		journal := writeWorkspaceHistory(t, dir, "target")
		if err := os.WriteFile(filepath.Join(filepath.Dir(journal), "restore.json.tmp"), []byte("interrupted unpublished private payload"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := DeleteSession(DeleteOptions{SessionID: "target", Dir: dir}); err != nil {
			t.Fatal(err)
		}
	})
}
