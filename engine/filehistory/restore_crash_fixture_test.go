package filehistory

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This entrypoint is compiled only into an owned test binary. It exercises a
// real terminal-save/process-exit boundary inside the network-isolated fixture.
func TestTerminalRestoreProcessFixture(t *testing.T) {
	if os.Getenv("YHC_OWNED_TERMINAL_RESTORE_FIXTURE") != "1" {
		t.Skip("owned container fixture only")
	}
	root, err := os.Getwd()
	requestedSession := os.Getenv("YHC_TERMINAL_CRASH_SESSION")
	session := filepath.Base(requestedSession)
	direction := os.Getenv("YHC_TERMINAL_CRASH_DIRECTION")
	if err != nil || root != "/workspace" || session != requestedSession || !validColdSession(session) || !validRestoreDirection(direction) {
		t.Fatal("invalid owned fixture scope")
	}
	dir := filepath.Join(root, ".yhc", "transcripts", session+".workspace-history")
	j, err := OpenRevisionJournal(root, dir, session)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	writeRestoreFixture(t, root, "continued.txt", "operator before\n", 0o600)
	if err := j.RecordTurnStartWithRestoreBoundary("terminal-fixture-turn", session, time.Unix(30, 0).UTC(), true, ""); err != nil {
		t.Fatal(err)
	}
	id, err := j.Begin("terminal-fixture-turn", "terminal-fixture-write", "Write", "continued.txt")
	if err != nil {
		t.Fatal(err)
	}
	writeRestoreFixture(t, root, "continued.txt", "operator changed\n", 0o600)
	if err := j.Finish(id, "succeeded"); err != nil {
		t.Fatal(err)
	}
	turn := j.turns["terminal-fixture-turn"]
	turn.FinishedAt, turn.TerminalReason, turn.ToolCountsBounded = time.Unix(31, 0).UTC(), "completed", true
	turn.KnownToolRecords, turn.KnownToolsSucceeded = 1, 1
	if err := j.RecordTurnTerminal(turn); err != nil {
		t.Fatal(err)
	}
	op, err := j.PrepareRestoreOperation("terminal-fixture-turn", "")
	if err != nil {
		t.Fatal(err)
	}
	if direction == "rollback" {
		j.restoreAfterPath = func(int) error { return errors.New("owned pre-rollback crash") }
		if _, err := j.ApplyRestoreOperation(t.Context(), authorizeRestore(op)); err == nil {
			t.Fatal("first crash not reached")
		}
		j.restoreAfterPath = nil
		op, err = j.PrepareRestoreRecovery("rollback")
		if err != nil {
			t.Fatal(err)
		}
	}
	j.restoreAfterTerminalSave = func() error { os.Exit(73); return nil }
	_, _ = j.ApplyRestoreOperation(t.Context(), authorizeRestore(op))
	t.Fatal("terminal process-exit seam not reached")
}
