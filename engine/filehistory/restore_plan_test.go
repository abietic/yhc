package filehistory

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPrepareRestoreOperationRestoresTurnBoundary(t *testing.T) {
	j, root, dir := openJournal(t)
	defer j.Close()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := restorePlanTurn(t, j, "first", "a.txt", "one")
	second := restorePlanTurn(t, j, "second", "a.txt", "two")
	op, err := j.PrepareRestoreOperation("first", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(op.Paths) != 1 || string(op.Paths[0].Target.Data) != "dirty" || string(op.Paths[0].Original.Data) != "two" {
		t.Fatalf("plan = %#v", op)
	}
	if op.ConfirmationID != "" || op.PermissionRevision != "" || op.State != "applying" {
		t.Fatalf("unexpected auth/progress: %#v", op)
	}
	if _, err := os.Lstat(filepath.Join(dir, restoreOperationFile)); !os.IsNotExist(err) {
		t.Fatalf("preview published restore storage: %v", err)
	}
	selected, err := j.PrepareRestoreOperation("second", second)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected.Paths) != 1 || string(selected.Paths[0].Target.Data) != "one" || string(selected.Paths[0].Original.Data) != "two" {
		t.Fatalf("selected plan = %#v", selected)
	}
	if first == "" {
		t.Fatal("empty first revision")
	}
}

func TestPrepareRestoreOperationRejectsIncompleteAndConflictingHistory(t *testing.T) {
	t.Run("pending later turn", func(t *testing.T) {
		j, root, _ := openJournal(t)
		defer j.Close()
		if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("zero"), 0o600); err != nil {
			t.Fatal(err)
		}
		restorePlanTurn(t, j, "target", "a.txt", "one")
		if err := j.RecordTurnStartWithRestoreBoundary("later", "thread", time.Now().UTC(), true, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := j.PrepareRestoreOperation("target", ""); err == nil {
			t.Fatal("accepted pending later turn")
		}
	})
	t.Run("external conflict", func(t *testing.T) {
		j, root, _ := openJournal(t)
		defer j.Close()
		if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("zero"), 0o600); err != nil {
			t.Fatal(err)
		}
		restorePlanTurn(t, j, "target", "a.txt", "one")
		if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := j.PrepareRestoreOperation("target", ""); err == nil {
			t.Fatal("accepted external conflict")
		}
	})
	t.Run("uncovered gap", func(t *testing.T) {
		j, root, _ := openJournal(t)
		defer j.Close()
		if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("zero"), 0o600); err != nil {
			t.Fatal(err)
		}
		restorePlanTurn(t, j, "target", "a.txt", "one")
		if err := j.RecordUncovered("target", "tool", "Bash", "external"); err != nil {
			t.Fatal(err)
		}
		if _, err := j.PrepareRestoreOperation("target", ""); err == nil {
			t.Fatal("accepted gap")
		}
	})
	t.Run("missing parent proof", func(t *testing.T) {
		j, root, _ := openJournal(t)
		defer j.Close()
		started := time.Now().UTC()
		if err := j.RecordTurnStartWithRestoreBoundary("target", "thread", started, true, ""); err != nil {
			t.Fatal(err)
		}
		id, err := j.Begin("target", "tool", "Write", "new/a.txt")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, "new"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "new/a.txt"), []byte("one"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := j.Finish(id, "succeeded"); err != nil {
			t.Fatal(err)
		}
		finishRestorePlanTurn(t, j, "target")
		if _, err := j.PrepareRestoreOperation("target", ""); err == nil {
			t.Fatal("accepted missing parent proof")
		}
	})
	t.Run("legacy boundary and chain mismatch", func(t *testing.T) {
		j, root, _ := openJournal(t)
		defer j.Close()
		if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("zero"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := j.RecordTurnStart("legacy", "thread", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		finishRestorePlanTurn(t, j, "legacy")
		if _, err := j.PrepareRestoreOperation("legacy", ""); err == nil {
			t.Fatal("accepted legacy boundary")
		}
		restorePlanTurn(t, j, "first", "a.txt", "one")
		second := restorePlanTurn(t, j, "second", "a.txt", "two")
		record := j.records[second]
		record.Before = restoreState("mismatch")
		j.records[second] = record
		if _, err := j.PrepareRestoreOperation("first", ""); err == nil {
			t.Fatal("accepted mismatched revision chain")
		}
	})
}

func restorePlanTurn(t *testing.T, j *RevisionJournal, turnID, path, after string) string {
	t.Helper()
	started := time.Now().UTC()
	if err := j.RecordTurnStartWithRestoreBoundary(turnID, "thread", started, true, ""); err != nil {
		t.Fatal(err)
	}
	id, err := j.Begin(turnID, "tool", "Edit", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(j.rootPath, path), []byte(after), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := j.Finish(id, "succeeded"); err != nil {
		t.Fatal(err)
	}
	finishRestorePlanTurn(t, j, turnID)
	return id
}

func finishRestorePlanTurn(t *testing.T, j *RevisionJournal, turnID string) {
	t.Helper()
	turn := j.turns[turnID]
	turn.FinishedAt, turn.TerminalReason = time.Now().UTC(), "completed"
	if err := j.RecordTurnTerminal(turn); err != nil && !errors.Is(err, ErrJournalClosed) {
		t.Fatal(err)
	}
}
