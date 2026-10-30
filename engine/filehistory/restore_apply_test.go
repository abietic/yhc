package filehistory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func requireRestoreFilesystem(t *testing.T) {
	t.Helper()
	if !RestoreFilesystemSupported() {
		t.Skip("guarded restore filesystem is unsupported on this platform")
	}
}

func authorizeRestore(op RestoreOperation) RestoreOperation {
	op.ConfirmationID = "fresh-confirmation"
	op.PermissionRevision = "fresh-permission"
	return op
}

func writeRestoreFixture(t *testing.T, root, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, path), []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, path), mode); err != nil {
		t.Fatal(err)
	}
}

func assertRestoreFixture(t *testing.T, root, path, body string, mode os.FileMode) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, path))
	if err != nil || !bytes.Equal(got, []byte(body)) {
		t.Fatalf("%s bytes = %q, err=%v", path, got, err)
	}
	info, err := os.Stat(filepath.Join(root, path))
	if err != nil || info.Mode().Perm() != mode.Perm() {
		t.Fatalf("%s mode = %v, err=%v", path, info, err)
	}
}

func restoreApplyPlan(t *testing.T, j *RevisionJournal, root string) RestoreOperation {
	t.Helper()
	writeRestoreFixture(t, root, "a.txt", "dirty a\r\nlast", 0o640)
	writeRestoreFixture(t, root, "b.txt", "dirty b\r\nlast", 0o750)
	restorePlanTurn(t, j, "restore-a", "a.txt", "agent a\n")
	restorePlanTurn(t, j, "restore-b", "b.txt", "agent b\n")
	op, err := j.PrepareRestoreOperation("restore-a", "")
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func TestPrepareRestoreOperationDoesNotWriteAndApplyRequiresFreshAuthorization(t *testing.T) {
	requireRestoreFilesystem(t)
	j, root, dir := openJournal(t)
	defer j.Close()
	writeRestoreFixture(t, root, "a.txt", "dirty\r\nbytes", 0o640)
	restorePlanTurn(t, j, "turn", "a.txt", "agent\n")
	beforeFile, err := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	op, err := j.PrepareRestoreOperation("turn", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "a.txt")); err != nil || !bytes.Equal(got, beforeFile) {
		t.Fatalf("prepare wrote workspace: %q, %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, restoreOperationFile)); !os.IsNotExist(err) {
		t.Fatalf("prepare wrote durable intent: %v", err)
	}
	for _, mutate := range []func(*RestoreOperation){
		func(p *RestoreOperation) { p.ConfirmationID = "" },
		func(p *RestoreOperation) { p.PermissionRevision = "" },
	} {
		candidate := authorizeRestore(op)
		mutate(&candidate)
		if _, err := j.ApplyRestoreOperation(context.Background(), candidate); err == nil {
			t.Fatal("accepted missing fresh authorization")
		}
		assertRestoreFixture(t, root, "a.txt", "agent\n", 0o640)
	}
}

func TestApplyRestoreRecoversCrashAfterFirstPathAndReplaysReceipts(t *testing.T) {
	requireRestoreFilesystem(t)
	j, root, dir := openJournal(t)
	op := restoreApplyPlan(t, j, root)
	j.restoreAfterPath = func(index int) error {
		if index == 0 {
			return errors.New("injected crash after first filesystem transition")
		}
		return nil
	}
	if _, err := j.ApplyRestoreOperation(context.Background(), authorizeRestore(op)); err == nil {
		t.Fatal("apply ignored crash seam")
	}
	assertRestoreFixture(t, root, "a.txt", "dirty a\r\nlast", 0o640)
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenRevisionJournal(root, dir, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovery, err := reopened.PrepareRestoreRecovery("restore")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := reopened.ApplyRestoreOperation(context.Background(), authorizeRestore(recovery))
	if err != nil || completed.State != "completed" {
		t.Fatalf("recovery = %#v, %v", completed, err)
	}
	assertRestoreFixture(t, root, "a.txt", "dirty a\r\nlast", 0o640)
	assertRestoreFixture(t, root, "b.txt", "dirty b\r\nlast", 0o750)
	var receipts int
	for _, record := range reopened.Snapshot().Records {
		if record.ToolName == "WorkspaceRestore" {
			receipts++
		}
	}
	if receipts != 2 {
		t.Fatalf("restore receipts = %d, want 2", receipts)
	}
	restorePlanTurn(t, reopened, "later", "a.txt", "later agent\n")
	if _, err := reopened.PrepareRestoreOperation("later", ""); err != nil {
		t.Fatalf("receipt replay broke later plan: %v", err)
	}
}

func TestApplyRestoreRejectsStalePreviewBeforeAnyWriteAndCanceledContext(t *testing.T) {
	requireRestoreFilesystem(t)
	for _, tc := range []struct {
		name string
		run  func(context.Context, *RevisionJournal, RestoreOperation) error
	}{
		{"external-edit", func(ctx context.Context, j *RevisionJournal, op RestoreOperation) error {
			return nil
		}},
		{"canceled", func(ctx context.Context, j *RevisionJournal, op RestoreOperation) error {
			_, err := j.ApplyRestoreOperation(ctx, authorizeRestore(op))
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, root, _ := openJournal(t)
			defer j.Close()
			op := restoreApplyPlan(t, j, root)
			ctx := context.Background()
			if tc.name == "external-edit" {
				writeRestoreFixture(t, root, "b.txt", "external edit", 0o600)
				_, err := j.ApplyRestoreOperation(ctx, authorizeRestore(op))
				if err == nil {
					t.Fatal("accepted stale preview")
				}
			} else {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				if err := tc.run(cancelled, j, op); !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled apply = %v", err)
				}
			}
			assertRestoreFixture(t, root, "a.txt", "agent a\n", 0o640)
			if tc.name == "external-edit" {
				assertRestoreFixture(t, root, "b.txt", "external edit", 0o600)
			} else {
				assertRestoreFixture(t, root, "b.txt", "agent b\n", 0o750)
			}
		})
	}
}

func TestApplyRestorePartialConflictCanRollbackOriginalBytesAndModes(t *testing.T) {
	requireRestoreFilesystem(t)
	j, root, _ := openJournal(t)
	defer j.Close()
	op := restoreApplyPlan(t, j, root)
	j.restoreAfterPath = func(index int) error {
		if index == 0 {
			writeRestoreFixture(t, root, "b.txt", "external conflict", 0o600)
		}
		return nil
	}
	partial, err := j.ApplyRestoreOperation(context.Background(), authorizeRestore(op))
	if err == nil || partial.State != "partial" {
		t.Fatalf("partial apply = %#v, %v", partial, err)
	}
	assertRestoreFixture(t, root, "a.txt", "dirty a\r\nlast", 0o640)
	assertRestoreFixture(t, root, "b.txt", "external conflict", 0o600)
	_, err = j.PrepareRestoreRecovery("rollback")
	if err == nil {
		// The conflict must be preserved rather than rolled back over; restore it
		// to the original state only after proving the conflict blocked recovery.
		t.Fatal("rollback accepted external conflict")
	}
	writeRestoreFixture(t, root, "b.txt", "agent b\n", 0o750)
	j.restoreAfterPath = nil
	rollback, err := j.PrepareRestoreRecovery("rollback")
	if err != nil {
		t.Fatal(err)
	}
	rolledBack, err := j.ApplyRestoreOperation(context.Background(), authorizeRestore(rollback))
	if err != nil || rolledBack.State != "rolled_back" {
		t.Fatalf("rollback = %#v, %v", rolledBack, err)
	}
	assertRestoreFixture(t, root, "a.txt", "agent a\n", 0o640)
	assertRestoreFixture(t, root, "b.txt", "agent b\n", 0o750)
}

func TestApplyRestoreDirectoryLeaseAndTerminalOperationCannotReapply(t *testing.T) {
	requireRestoreFilesystem(t)
	j, root, dir := openJournal(t)
	defer j.Close()
	op := restoreApplyPlan(t, j, root)
	peer, err := OpenRevisionJournal(root, dir, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	var peerErr error
	j.restoreAfterPath = func(index int) error {
		if index == 0 {
			candidate, err := peer.PrepareRestoreRecovery("restore")
			if err != nil {
				peerErr = err
			} else {
				_, peerErr = peer.ApplyRestoreOperation(context.Background(), authorizeRestore(candidate))
			}
			return errors.New("stop after lease assertion")
		}
		return nil
	}
	if _, err := j.ApplyRestoreOperation(context.Background(), authorizeRestore(op)); err == nil {
		t.Fatal("primary apply ignored seam")
	}
	if peerErr == nil {
		t.Fatal("concurrent journal acquired restore lease")
	}
	j.restoreAfterPath = nil
	recovery, err := j.PrepareRestoreRecovery("restore")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := j.ApplyRestoreOperation(context.Background(), authorizeRestore(recovery))
	if err != nil || completed.State != "completed" {
		t.Fatalf("completion = %#v, %v", completed, err)
	}
	if _, err := j.ApplyRestoreOperation(context.Background(), authorizeRestore(completed)); err == nil {
		t.Fatal("reapplied terminal restore operation")
	}
}

func TestRestorePersistedConfirmationCannotAuthorizeReplay(t *testing.T) {
	requireRestoreFilesystem(t)
	j, root, dir := openJournal(t)
	operation := restoreApplyPlan(t, j, root)
	j.restoreAfterPath = func(int) error { return errors.New("crash") }
	if _, err := j.ApplyRestoreOperation(context.Background(), authorizeRestore(operation)); err == nil {
		t.Fatal("missing injected crash")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenRevisionJournal(root, dir, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	saved, found, err := reopened.LoadRestoreOperation()
	if err != nil || !found {
		t.Fatal("missing durable intent")
	}
	if _, err := reopened.ApplyRestoreOperation(context.Background(), saved); err == nil {
		t.Fatal("persisted confirmation authorized replay")
	}
	if saved.ConfirmationID == "" || saved.PermissionRevision == "" {
		t.Fatal("fixture did not retain stale fields")
	}
	assertRestoreFixture(t, root, "b.txt", "agent b\n", 0o750)
}

func TestRestoreLeaseExcludesPeerJournalAppend(t *testing.T) {
	requireRestoreFilesystem(t)
	j, root, dir := openJournal(t)
	defer j.Close()
	operation := restoreApplyPlan(t, j, root)
	peer, err := OpenRevisionJournal(root, dir, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	var appendErr error
	j.restoreAfterPath = func(index int) error {
		if index == len(operation.Paths)-1 {
			_, appendErr = peer.Begin("external", "peer", "Write", "new.txt")
		}
		return nil
	}
	if _, err := j.ApplyRestoreOperation(context.Background(), authorizeRestore(operation)); err != nil {
		t.Fatal(err)
	}
	if appendErr == nil {
		t.Fatal("peer appended during restore lease")
	}
	if len(peer.Snapshot().Records) != 2 {
		t.Fatal("failed peer append changed its replay")
	}
	// This peer now has a stale pre-restore replay. It must not append old
	// sequence numbers; an explicit reopen recovers the completed receipts.
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	fresh, err := OpenRevisionJournal(root, dir, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if _, err := fresh.Begin("after-reopen", "next", "Write", "new.txt"); err != nil {
		t.Fatalf("fresh peer cannot record after lease release: %v", err)
	}
}

func TestRecoveryPreviewShowsActualStatesAndRejectsLaterKnownStateSwitch(t *testing.T) {
	requireRestoreFilesystem(t)
	j, root, _ := openJournal(t)
	defer j.Close()
	op := restoreApplyPlan(t, j, root)
	j.restoreAfterPath = func(index int) error {
		if index == 0 {
			return errors.New("crash")
		}
		return nil
	}
	if _, err := j.ApplyRestoreOperation(context.Background(), authorizeRestore(op)); err == nil {
		t.Fatal("missing crash")
	}
	recovery, err := j.PrepareRestoreRecovery("restore")
	if err != nil {
		t.Fatal(err)
	}
	states, err := j.RestorePreviewStates(recovery)
	if err != nil {
		t.Fatal(err)
	}
	if string(states[0].Data) != "dirty a\r\nlast" || string(states[1].Data) != "agent b\n" {
		t.Fatal("preview presented immutable originals as current disk")
	}
	// This is a known before/after state, yet it changed after preview and must
	// not be silently folded into a previously reviewed transition.
	writeRestoreFixture(t, root, "a.txt", "agent a\n", 0o640)
	j.restoreAfterPath = nil
	if _, err := j.ApplyRestoreOperation(context.Background(), authorizeRestore(recovery)); err == nil {
		t.Fatal("stale recovery preview accepted a known-state switch")
	}
	assertRestoreFixture(t, root, "b.txt", "agent b\n", 0o750)
}
