package filehistory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRestoreReceiptReconciliationRejectsUnobservedTargetBeforeAnyAppend(t *testing.T) {
	requireRestoreFilesystem(t)
	j, root, dir := openJournal(t)
	defer j.Close()
	op := authorizeRestore(restoreApplyPlan(t, j, root))
	op.State = "completed"
	for i := range op.Paths {
		op.Paths[i].Status = "applied"
	}
	if err := j.SaveRestoreOperation(op); err != nil {
		t.Fatal(err)
	}
	// One file matches the asserted terminal result, the other does not. A
	// metadata assertion alone must never manufacture successful file facts.
	first := op.Paths[0]
	writeRestoreFixture(t, root, first.Path, string(first.Target.Data), os.FileMode(first.Target.Mode))
	before := mustReadCold(t, filepath.Join(dir, journalFile))
	if err := j.ReconcileRestoreReceipts(); err == nil {
		t.Fatal("unobserved terminal target was accepted")
	}
	if !bytes.Equal(before, mustReadCold(t, filepath.Join(dir, journalFile))) {
		t.Fatal("failed preflight published a partial receipt set")
	}
	assertRestoreFixture(t, root, "b.txt", "agent b\n", 0o750)
}

func terminalReceiptFixture(t *testing.T) (*RevisionJournal, string, string, RestoreOperation) {
	t.Helper()
	requireRestoreFilesystem(t)
	j, root, dir := openJournal(t)
	t.Cleanup(func() { _ = j.Close() })
	op := authorizeRestore(restoreApplyPlan(t, j, root))
	op.State = "completed"
	for i := range op.Paths {
		op.Paths[i].Status = "applied"
		writeRestoreFixture(t, root, op.Paths[i].Path, string(op.Paths[i].Target.Data), os.FileMode(op.Paths[i].Target.Mode))
	}
	if err := j.SaveRestoreOperation(op); err != nil {
		t.Fatal(err)
	}
	return j, root, dir, op
}

func TestRestoreReceiptReconciliationRejectsUnobservedModeBeforeAnyAppend(t *testing.T) {
	j, root, dir, op := terminalReceiptFixture(t)
	path := op.Paths[len(op.Paths)-1]
	mode := os.FileMode(path.Target.Mode) ^ 0o100
	if err := os.Chmod(filepath.Join(root, path.Path), mode); err != nil {
		t.Fatal(err)
	}
	before := mustReadCold(t, filepath.Join(dir, journalFile))
	if err := j.ReconcileRestoreReceipts(); err == nil {
		t.Fatal("unobserved target mode was accepted")
	}
	if !bytes.Equal(before, mustReadCold(t, filepath.Join(dir, journalFile))) {
		t.Fatal("mode rejection published a partial receipt set")
	}
	assertRestoreFixture(t, root, path.Path, string(path.Target.Data), mode)
}

func TestRestoreReceiptReconciliationValidatesExistingIDsBeforeAppend(t *testing.T) {
	for _, fault := range []string{"turn", "tool_use", "tool", "path", "before", "after"} {
		t.Run(fault, func(t *testing.T) {
			j, _, dir, op := terminalReceiptFixture(t)
			p := op.Paths[0]
			r := RevisionRecord{ID: fmt.Sprintf("restore-%s-0-restore", op.ID), SessionID: j.sessionID, TurnID: op.ID, ToolUseID: op.ID, ToolName: "WorkspaceRestore", Path: p.Path, Outcome: "succeeded", ParentExisted: true, Sequence: uint64(len(j.order) + 1), Timestamp: time.Now().UTC(), Before: p.Original, After: p.Target}
			switch fault {
			case "turn":
				r.TurnID = "another-operation"
			case "tool_use":
				r.ToolUseID = "another-operation"
			case "tool":
				r.ToolName = "WorkspaceRestoreRollback"
			case "path":
				r.Path = op.Paths[1].Path
			case "before":
				r.Before = restoreState("unobserved before")
			case "after":
				r.After = restoreState("unobserved after")
			}
			if err := j.append("restore_result", r); err != nil {
				t.Fatal(err)
			}
			j.records[r.ID], j.order = r, append(j.order, r.ID)
			before := mustReadCold(t, filepath.Join(dir, journalFile))
			if err := j.ReconcileRestoreReceipts(); err == nil {
				t.Fatal("matching ID bypassed immutable receipt binding")
			}
			if !bytes.Equal(before, mustReadCold(t, filepath.Join(dir, journalFile))) {
				t.Fatal("rejection appended another receipt")
			}
		})
	}
}

func TestRestoreReceiptRepairIsIdempotentAndAllowsLaterEditing(t *testing.T) {
	j, root, dir, _ := terminalReceiptFixture(t)
	if err := j.ReconcileRestoreReceipts(); err != nil {
		t.Fatal(err)
	}
	before := mustReadCold(t, filepath.Join(dir, journalFile))
	if err := j.ReconcileRestoreReceipts(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, mustReadCold(t, filepath.Join(dir, journalFile))) {
		t.Fatal("repair is not idempotent")
	}
	restorePlanTurn(t, j, "later", "a.txt", "ordinary later edit")
	if err := j.ReconcileRestoreReceipts(); err != nil {
		t.Fatal("completed restore froze later file bytes", err)
	}
	assertRestoreFixture(t, root, "a.txt", "ordinary later edit", 0o640)
}

func TestRestoreReceiptNoopIDStillRequiresExactBinding(t *testing.T) {
	requireRestoreFilesystem(t)
	j, root, dir := openJournal(t)
	defer j.Close()
	op := authorizeRestore(restoreApplyPlan(t, j, root))
	op.State, op.Direction = "rolled_back", "rollback"
	for i := range op.Paths {
		op.Paths[i].Status = "already_applied"
	}
	if err := j.SaveRestoreOperation(op); err != nil {
		t.Fatal(err)
	}
	p := op.Paths[0]
	r := RevisionRecord{ID: fmt.Sprintf("restore-%s-0-rollback", op.ID), SessionID: j.sessionID, TurnID: op.ID, ToolUseID: "another-operation", ToolName: "WorkspaceRestoreRollback", Path: p.Path, Outcome: "succeeded", Sequence: uint64(len(j.order) + 1), ParentExisted: true, Timestamp: time.Now().UTC(), Before: p.Original, After: p.Original}
	if err := j.append("restore_result", r); err != nil {
		t.Fatal(err)
	}
	j.records[r.ID], j.order = r, append(j.order, r.ID)
	before := mustReadCold(t, filepath.Join(dir, journalFile))
	if err := j.ReconcileRestoreReceipts(); err == nil {
		t.Fatal("no-op ID bypassed receipt binding")
	}
	if err := j.verifyRestoreReceiptPlan(restoreOperationPlan(op), op.Direction); err == nil {
		t.Fatal("cold verifier accepted unbound no-op")
	}
	if !bytes.Equal(before, mustReadCold(t, filepath.Join(dir, journalFile))) {
		t.Fatal("no-op rejection rewrote history")
	}
}

func TestRestoreRollbackReceiptIgnoresLaterEditsAcrossColdImport(t *testing.T) {
	for _, imported := range []bool{false, true} {
		t.Run(fmt.Sprintf("imported=%t", imported), func(t *testing.T) {
			var j *RevisionJournal
			var root string
			if imported {
				j, root, _ = coldImportedRestoreJournal(t, false)
			} else {
				j, root, _ = coldJournal(t)
			}
			op, err := j.PrepareRestoreOperation("turn-1", "")
			if err != nil {
				t.Fatal(err)
			}
			j.restoreAfterPath = func(int) error { return errors.New("owned filesystem crash") }
			if _, err := j.ApplyRestoreOperation(t.Context(), authorizeRestore(op)); err == nil {
				t.Fatal("crash not reached")
			}
			j.restoreAfterPath = nil
			recovery, err := j.PrepareRestoreRecovery("rollback")
			if err != nil {
				t.Fatal(err)
			}
			finished, err := j.ApplyRestoreOperation(t.Context(), authorizeRestore(recovery))
			if err != nil || finished.State != "rolled_back" {
				t.Fatal("rollback", err)
			}
			coldWrite(t, j, "later", "legitimate later edit")
			before := mustReadCold(t, filepath.Join(j.dir, journalFile))
			if err := j.ReconcileRestoreReceipts(); err != nil {
				t.Fatal("later edit altered old rollback", err)
			}
			if !bytes.Equal(before, mustReadCold(t, filepath.Join(j.dir, journalFile))) {
				t.Fatal("later edit manufactured rollback history")
			}
			p, err := j.ExportColdState()
			if err != nil {
				t.Fatal("cold export after later edit", err)
			}
			target, dir := coldCopy(t, root)
			if _, err := ImportColdState(t.Context(), target, dir, j.sessionID, coldRequest(p)); err != nil {
				t.Fatal("import", err)
			}
			fresh, err := OpenRevisionJournal(target, dir, j.sessionID)
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			q, err := fresh.ExportColdState()
			if err != nil || q.SemanticSHA256 != p.SemanticSHA256 {
				t.Fatal("imported semantics", err)
			}
		})
	}
}

func TestRestoreReceiptReconciliationPreflightsEncodedByteCapacity(t *testing.T) {
	requireRestoreFilesystem(t)
	j, root, dir := openJournal(t)
	defer j.Close()
	restoreApplyPlan(t, j, root)
	// Add enough bounded frames to hold a large but valid whitespace-padded log.
	restorePlanTurn(t, j, "unrelated", "a.txt", "another observed write")
	op, err := j.PrepareRestoreOperation("restore-a", "")
	if err != nil {
		t.Fatal(err)
	}
	op = authorizeRestore(op)
	op.State = "completed"
	for i := range op.Paths {
		op.Paths[i].Status = "applied"
		p := op.Paths[i]
		writeRestoreFixture(t, root, p.Path, string(p.Target.Data), os.FileMode(p.Target.Mode))
	}
	if err := j.SaveRestoreOperation(op); err != nil {
		t.Fatal(err)
	}
	first := op.Paths[0]
	prototype := journalFrame{Version: 2, Type: "restore_result", Root: j.rootPath, RootIdentity: j.rootIdentity, SessionID: j.sessionID, FrameSequence: j.frameSequence + 1, Record: RevisionRecord{ID: fmt.Sprintf("restore-%s-0-restore", op.ID), SessionID: j.sessionID, TurnID: op.ID, ToolUseID: op.ID, ToolName: "WorkspaceRestore", Path: first.Path, Outcome: "succeeded", Sequence: uint64(len(j.order) + 1), ParentExisted: true, Timestamp: time.Date(2026, 10, 28, 12, 34, 56, 999999999, time.UTC), Before: first.Original, After: first.Target}}
	encoded, err := json.Marshal(prototype)
	if err != nil {
		t.Fatal(err)
	}
	remaining := len(encoded) + 17
	raw := mustReadCold(t, filepath.Join(dir, journalFile))
	extra := maxJournalBytes - remaining - len(raw)
	var padded bytes.Buffer
	for _, line := range bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'}) {
		n := min(extra, maxFrameBytes-1-len(line))
		extra -= n
		padded.Write(line)
		padded.Write(bytes.Repeat([]byte{' '}, n))
		padded.WriteByte('\n')
	}
	if extra != 0 {
		t.Fatal("insufficient fixture frames")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, journalFile), padded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh, err := OpenRevisionJournal(root, dir, j.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if err := fresh.ReconcileRestoreReceipts(); !errors.Is(err, ErrJournalCapacity) {
		t.Fatal("whole receipt group not denied by byte capacity", err)
	}
	if !bytes.Equal(padded.Bytes(), mustReadCold(t, filepath.Join(dir, journalFile))) {
		t.Fatal("byte-capacity failure published a partial receipt group")
	}
}

func TestRestoreReceiptReconciliationAllowsSafelyRemovedParent(t *testing.T) {
	requireRestoreFilesystem(t)
	j, root, _ := openJournal(t)
	defer j.Close()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeRestoreFixture(t, root, "sub/a.txt", "before", 0o600)
	restorePlanTurn(t, j, "nested", "sub/a.txt", "after")
	op, err := j.PrepareRestoreOperation("nested", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.ApplyRestoreOperation(t.Context(), authorizeRestore(op)); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	if err := j.ReconcileRestoreReceipts(); err != nil {
		t.Fatal("ordinary removal of parent was mistaken for a leftover artifact", err)
	}
}
