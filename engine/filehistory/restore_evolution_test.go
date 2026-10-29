package filehistory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestColdHistoryNewRestoreSurvivesSecondImport(t *testing.T) {
	for _, priorRestore := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_prior_operation", true: "terminal_baseline"}[priorRestore], func(t *testing.T) {
			j, source, _ := coldJournal(t)
			if priorRestore {
				coldApply(t, j, "turn-1")
			}
			proof, err := j.ExportColdState()
			if err != nil {
				t.Fatal(err)
			}
			target, dir := coldCopy(t, source)
			if _, err := ImportColdState(t.Context(), target, dir, j.sessionID, coldRequest(proof)); err != nil {
				t.Fatal(err)
			}
			current, err := OpenRevisionJournal(target, dir, j.sessionID)
			if err != nil {
				t.Fatal(err)
			}
			defer current.Close()
			coldWrite(t, current, "new-turn", "new live change")
			coldApply(t, current, "new-turn")
			next, err := current.ExportColdState()
			if err != nil {
				t.Fatal("new terminal transaction not exportable", err)
			}
			if next.SemanticSHA256 == proof.SemanticSHA256 {
				t.Fatal("new transaction missing from semantic proof")
			}
			third, thirdDir := coldCopy(t, target)
			if _, err := ImportColdState(t.Context(), third, thirdDir, j.sessionID, coldRequest(next)); err != nil {
				t.Fatal("second import", err)
			}
			last, err := OpenRevisionJournal(third, thirdDir, j.sessionID)
			if err != nil {
				t.Fatal(err)
			}
			defer last.Close()
			actual, err := last.ExportColdState()
			if err != nil || actual.SemanticSHA256 != next.SemanticSHA256 {
				t.Fatal("evolution lost on migration", err)
			}
			want := "after"
			if priorRestore {
				want = "before"
			}
			data, err := os.ReadFile(filepath.Join(third, "a.txt"))
			if err != nil || string(data) != want {
				t.Fatalf("restored file=%q err=%v", data, err)
			}
			coldWrite(t, last, "third-turn", "third change")
			coldApply(t, last, "third-turn")
			if _, err := last.ExportColdState(); err != nil {
				t.Fatal("new transaction after second import", err)
			}
		})
	}
}

func coldImportedRestoreJournal(t *testing.T, terminal bool) (*RevisionJournal, string, string) {
	t.Helper()
	j, root, _ := coldJournal(t)
	if terminal {
		coldApply(t, j, "turn-1")
	}
	p, err := j.ExportColdState()
	if err != nil {
		t.Fatal(err)
	}
	target, dir := coldCopy(t, root)
	if _, err := ImportColdState(t.Context(), target, dir, j.sessionID, coldRequest(p)); err != nil {
		t.Fatal(err)
	}
	current, err := OpenRevisionJournal(target, dir, j.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Close() })
	return current, target, dir
}

func TestColdHistoryInterruptedNewRestoreRequiresFreshAuthority(t *testing.T) {
	for _, direction := range []string{"restore", "rollback"} {
		t.Run(direction, func(t *testing.T) {
			j, root, dir := coldImportedRestoreJournal(t, true)
			coldWrite(t, j, "new-turn", "live change")
			op, err := j.PrepareRestoreOperation("new-turn", "")
			if err != nil {
				t.Fatal(err)
			}
			op.ConfirmationID, op.PermissionRevision = "old-confirmation", "old-policy"
			j.restoreAfterPath = func(int) error { return errors.New("owned crash after path effect") }
			if _, err := j.ApplyRestoreOperation(t.Context(), op); err == nil {
				t.Fatal("crash did not stop apply")
			}
			if _, err := j.ExportColdState(); err == nil {
				t.Fatal("unfinished progress got cold proof")
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			fresh, err := OpenRevisionJournal(root, dir, j.sessionID)
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			stored, found, err := fresh.LoadRestoreOperation()
			if err != nil || !found || stored.State != "applying" {
				t.Fatal("durable progress missing", err)
			}
			before, _ := os.ReadFile(filepath.Join(dir, restoreOperationFile))
			if _, err := fresh.ApplyRestoreOperation(t.Context(), stored); err == nil {
				t.Fatal("stored authority replayed")
			}
			after, _ := os.ReadFile(filepath.Join(dir, restoreOperationFile))
			if !bytes.Equal(before, after) {
				t.Fatal("authority rejection changed state")
			}
			preview, err := fresh.PrepareRestoreRecovery(direction)
			if err != nil || preview.ConfirmationID != "" || preview.PermissionRevision != "" {
				t.Fatal("fresh recovery preview", err)
			}
			if _, err := fresh.ApplyRestoreOperation(t.Context(), preview); err == nil {
				t.Fatal("unconfirmed preview applied")
			}
			preview, err = fresh.PrepareRestoreRecovery(direction)
			if err != nil {
				t.Fatal(err)
			}
			preview.ConfirmationID, preview.PermissionRevision = "fresh-confirmation", "fresh-policy"
			finished, err := fresh.ApplyRestoreOperation(t.Context(), preview)
			if err != nil || !terminalRestoreState(finished.State) {
				t.Fatal("manual recovery", err)
			}
			if _, err := fresh.ExportColdState(); err != nil {
				t.Fatal("verified recovery not exportable", err)
			}
			want := "before"
			if direction == "rollback" {
				want = "live change"
			}
			data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
			if string(data) != want {
				t.Fatalf("recovered bytes=%q", data)
			}
		})
	}
}

func TestColdHistoryPartialNewRestoreCanRecoverWithoutResettingProgress(t *testing.T) {
	j, root, dir := coldImportedRestoreJournal(t, true)
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("b original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordTurnStartWithRestoreBoundary("two-files", j.sessionID, time.Unix(20, 0).UTC(), true, ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		id, err := j.Begin("two-files", "write-"+name, "Write", name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte("new "+name), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := j.Finish(id, "succeeded"); err != nil {
			t.Fatal(err)
		}
	}
	turn := j.turns["two-files"]
	turn.FinishedAt, turn.TerminalReason, turn.ToolCountsBounded = time.Unix(21, 0).UTC(), "completed", true
	turn.KnownToolRecords, turn.KnownToolsSucceeded = 2, 2
	if err := j.RecordTurnTerminal(turn); err != nil {
		t.Fatal(err)
	}
	op, err := j.PrepareRestoreOperation("two-files", "")
	if err != nil {
		t.Fatal(err)
	}
	op.ConfirmationID, op.PermissionRevision = "fresh", "policy"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	j.restoreAfterPath = func(int) error { cancel(); return nil }
	partial, err := j.ApplyRestoreOperation(ctx, op)
	if !errors.Is(err, context.Canceled) || partial.State != "partial" || partial.Paths[0].Status != "applied" || partial.Paths[1].Status != "pending" {
		t.Fatal("partial progress not persisted", err)
	}
	if _, err := j.ExportColdState(); err == nil {
		t.Fatal("partial operation exported")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	fresh, err := OpenRevisionJournal(root, dir, j.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	preview, err := fresh.PrepareRestoreRecovery("restore")
	if err != nil {
		t.Fatal(err)
	}
	preview.ConfirmationID, preview.PermissionRevision = "new-confirmation", "new-policy"
	finished, err := fresh.ApplyRestoreOperation(t.Context(), preview)
	if err != nil || finished.State != "completed" || finished.Paths[0].Status != "already_applied" {
		t.Fatal("partial recovery", err)
	}
	if _, err := fresh.ExportColdState(); err != nil {
		t.Fatal("partial recovery proof", err)
	}
}

func TestColdHistoryEvolutionRejectsAlteredBaselineAndProgress(t *testing.T) {
	for _, fault := range []string{"base", "drop", "previous", "head", "version", "revision", "old_prefix", "terminal_diagnostics", "receipt", "sibling"} {
		t.Run(fault, func(t *testing.T) {
			j, root, dir := coldImportedRestoreJournal(t, true)
			coldWrite(t, j, "new-turn", "live change")
			coldApply(t, j, "new-turn")
			path := filepath.Join(dir, restoreOperationFile)
			raw, _ := os.ReadFile(path)
			var e restoreOperationEnvelope
			if json.Unmarshal(raw, &e) != nil || e.Evolution == nil {
				t.Fatal("no evolution envelope")
			}
			switch fault {
			case "base":
				e.Evolution.Base.ConfirmationID = "substituted"
			case "drop":
				e.Evolution = nil
				e.Version = 1
			case "previous":
				e.Evolution.Steps[0].PreviousSHA256 = coldSHA([]byte("other"))
			case "head":
				e.Operation.ConfirmationID = "substituted"
			case "version":
				e.Version = 3
			case "revision":
				e.Evolution.Steps[0].JournalRevision = j.frameSequence + 1
			case "terminal_diagnostics":
				s := restoreSummary(e.Operation)
				s.ConfirmationID = "reconfirmed"
				e.Evolution.Steps = append(e.Evolution.Steps, restoreEvolutionStep{PreviousSHA256: restoreSummarySHA(&e.Evolution.Steps[len(e.Evolution.Steps)-1].After), JournalRevision: j.frameSequence, After: s})
				e.Operation.ConfirmationID = s.ConfirmationID
			case "old_prefix":
				receipt, err := j.ColdOrigin(coldSHA(mustReadCold(t, filepath.Join(dir, coldOriginFile))))
				if err != nil {
					t.Fatal(err)
				}
				archive := filepath.Join(filepath.Dir(dir), receipt.ArchiveDirectory, restoreOperationFile)
				if err := os.WriteFile(archive, []byte("changed baseline"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "receipt":
				for id, record := range j.records {
					if record.ToolName == "WorkspaceRestore" && record.TurnID == e.Operation.ID {
						delete(j.records, id)
						break
					}
				}
			case "sibling":
				if err := os.WriteFile(filepath.Join(root, e.Operation.Paths[0].BackupPath), []byte("unverified"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if fault != "old_prefix" && fault != "receipt" && fault != "sibling" {
				changed, _ := json.Marshal(e)
				if err := os.WriteFile(path, changed, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := mustReadCold(t, path)
			if _, err := j.ExportColdState(); err == nil {
				t.Fatal("unverified evolution admitted")
			}
			if !bytes.Equal(before, mustReadCold(t, path)) {
				t.Fatal("inspection repaired rejected evolution")
			}
		})
	}
}

func mustReadCold(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestColdHistoryEvolutionTerminalNoopAndProgressReset(t *testing.T) {
	j, _, dir := coldImportedRestoreJournal(t, false)
	op := coldApply(t, j, "turn-1")
	before := mustReadCold(t, filepath.Join(dir, restoreOperationFile))
	if err := j.SaveRestoreOperation(op); err != nil {
		t.Fatal("terminal no-op", err)
	}
	if !bytes.Equal(before, mustReadCold(t, filepath.Join(dir, restoreOperationFile))) {
		t.Fatal("no-op changed provenance")
	}
	op.ConfirmationID = "different"
	if err := j.SaveRestoreOperation(op); err == nil {
		t.Fatal("terminal diagnostics rewritten")
	}
	var e restoreOperationEnvelope
	if json.Unmarshal(before, &e) != nil {
		t.Fatal("envelope")
	}
	state := e.Evolution.Steps[1].After
	for _, fault := range []string{"pending_reset", "rollback_reverse", "partial_status_reset"} {
		previous := state
		previous.Statuses = append([]string(nil), state.Statuses...)
		next := previous
		next.Statuses = append([]string(nil), previous.Statuses...)
		switch fault {
		case "pending_reset":
			next.Statuses[0] = "pending"
		case "rollback_reverse":
			previous.Direction = "rollback"
		case "partial_status_reset":
			previous.State = "partial"
			next.Statuses[0] = "pending"
		}
		if validRestoreEvolutionTransition(&previous, next, j.frameSequence) {
			t.Fatal("invalid transition", fault)
		}
	}
}

func TestColdHistoryEvolutionBoundsStopBeforeFileEffects(t *testing.T) {
	j, root, dir := coldImportedRestoreJournal(t, false)
	op, err := j.PrepareRestoreOperation("turn-1", "")
	if err != nil {
		t.Fatal(err)
	}
	op.ConfirmationID, op.PermissionRevision = "confirmed", "policy"
	if err := j.SaveRestoreOperation(op); err != nil {
		t.Fatal(err)
	}
	var e restoreOperationEnvelope
	if json.Unmarshal(mustReadCold(t, filepath.Join(dir, restoreOperationFile)), &e) != nil {
		t.Fatal("envelope")
	}
	for len(e.Evolution.Steps) < maxRestoreEvolutionSteps-1 {
		previous := e.Evolution.Steps[len(e.Evolution.Steps)-1].After
		next := previous
		next.PermissionRevision = fmt.Sprintf("policy-%d", len(e.Evolution.Steps))
		e.Evolution.Steps = append(e.Evolution.Steps, restoreEvolutionStep{PreviousSHA256: restoreSummarySHA(&previous), JournalRevision: j.frameSequence, After: next})
	}
	e.Operation.PermissionRevision = e.Evolution.Steps[len(e.Evolution.Steps)-1].After.PermissionRevision
	raw, _ := json.Marshal(e)
	if err := os.WriteFile(filepath.Join(dir, restoreOperationFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := j.PrepareRestoreRecovery("restore")
	if err != nil {
		t.Fatal(err)
	}
	preview.ConfirmationID, preview.PermissionRevision = "new-confirmation", "new-policy"
	if _, err := j.ApplyRestoreOperation(t.Context(), preview); err == nil {
		t.Fatal("capacity exhaustion not denied")
	}
	if !bytes.Equal(raw, mustReadCold(t, filepath.Join(dir, restoreOperationFile))) || string(mustReadCold(t, filepath.Join(root, "a.txt"))) != "after" {
		t.Fatal("capacity rejection changed progress or user bytes")
	}
	if _, err := os.Lstat(filepath.Join(root, op.Paths[0].BackupPath)); !os.IsNotExist(err) {
		t.Fatal("capacity rejection created a backup")
	}
}

func TestColdHistoryEvolutionUnpublishedIntentIsPreservedAndBlocked(t *testing.T) {
	j, root, dir := coldImportedRestoreJournal(t, true)
	coldWrite(t, j, "new-turn", "live change")
	before := mustReadCold(t, filepath.Join(dir, restoreOperationFile))
	op, err := j.PrepareRestoreOperation("new-turn", "")
	if err != nil {
		t.Fatal(err)
	}
	op.ConfirmationID, op.PermissionRevision = "fresh", "fresh-policy"
	j.restoreBeforeStorePublish = func() { panic("owned process crash before rename") }
	func() {
		defer func() {
			if recover() == nil {
				t.Error("crash seam not reached")
			}
		}()
		_, _ = j.ApplyRestoreOperation(t.Context(), op)
	}()
	staged := mustReadCold(t, filepath.Join(dir, restoreOperationTemp))
	if _, err := j.ExportColdState(); err == nil {
		t.Fatal("unpublished intent admitted")
	}
	if fresh, err := OpenRevisionJournal(root, dir, j.sessionID); err == nil {
		fresh.Close()
		t.Fatal("unpublished intent reopened")
	}
	if !bytes.Equal(staged, mustReadCold(t, filepath.Join(dir, restoreOperationTemp))) || !bytes.Equal(before, mustReadCold(t, filepath.Join(dir, restoreOperationFile))) || string(mustReadCold(t, filepath.Join(root, "a.txt"))) != "live change" {
		t.Fatal("uncertain state repaired or user bytes changed")
	}
}

func coldApply(t *testing.T, j *RevisionJournal, turn string) RestoreOperation {
	t.Helper()
	op, err := j.PrepareRestoreOperation(turn, "")
	if err != nil {
		t.Fatal("prepare fresh authorized restore", err)
	}
	op.ConfirmationID, op.PermissionRevision = "fresh-confirmation", "fresh-policy"
	op, err = j.ApplyRestoreOperation(t.Context(), op)
	if err != nil || op.State != "completed" {
		t.Fatal("apply restore", err)
	}
	return op
}
