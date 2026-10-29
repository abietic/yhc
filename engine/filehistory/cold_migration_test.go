package filehistory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func coldJournal(t *testing.T) (*RevisionJournal, string, string) {
	t.Helper()
	if !RestoreFilesystemSupported() {
		t.Skip("atomic directory exchange unsupported")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".yhc", "transcripts", "cold-session.workspace-history")
	j, err := OpenRevisionJournal(root, dir, "cold-session")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	coldWrite(t, j, "turn-1", "after")
	return j, root, dir
}

func coldWrite(t *testing.T, j *RevisionJournal, turnID, value string) {
	t.Helper()
	if err := j.RecordTurnStartWithRestoreBoundary(turnID, j.sessionID, time.Unix(10, 0).UTC(), true, ""); err != nil {
		t.Fatal(err)
	}
	id, err := j.Begin(turnID, "write-"+turnID, "Write", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(j.rootPath, "a.txt"), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := j.Finish(id, "succeeded"); err != nil {
		t.Fatal(err)
	}
	turn := j.turns[turnID]
	turn.FinishedAt, turn.TerminalReason = time.Unix(11, 0).UTC(), "completed"
	turn.KnownToolRecords, turn.KnownToolsSucceeded, turn.ToolCountsBounded = 1, 1, true
	if err := j.RecordTurnTerminal(turn); err != nil {
		t.Fatal(err)
	}
}

func coldCopy(t *testing.T, source string) (string, string) {
	t.Helper()
	target, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		to := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.Mkdir(to, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(to, data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
	return target, filepath.Join(target, ".yhc", "transcripts", "cold-session.workspace-history")
}

func coldRequest(proof ColdHistoryProof) ColdHistoryImport {
	return ColdHistoryImport{
		Version: 1, Source: proof, SourceSHA256: proof.SHA256,
		MigrationID: uuid.NewString(), BindingSHA256: strings.Repeat("a", 64),
	}
}

func TestColdHistoryRoundTripPreservesFactsAndOriginalBytes(t *testing.T) {
	j, source, dir := coldJournal(t)
	proof, err := j.ExportColdState()
	if err != nil {
		t.Fatal(err)
	}
	before := j.Snapshot()
	raw, err := os.ReadFile(filepath.Join(dir, journalFile))
	if err != nil {
		t.Fatal(err)
	}
	target, targetDir := coldCopy(t, source)
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	req := coldRequest(proof)
	receipt, err := ImportColdState(t.Context(), target, targetDir, "cold-session", req)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := OpenRevisionJournal(target, targetDir, "cold-session")
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if !reflect.DeepEqual(before, loaded.Snapshot()) {
		t.Fatal("history facts changed during migration")
	}
	actual, err := loaded.ExportColdState()
	if err != nil || actual.SemanticSHA256 != proof.SemanticSHA256 || actual.Root != target {
		t.Fatalf("target proof=%+v err=%v", actual, err)
	}
	archived, err := os.ReadFile(filepath.Join(filepath.Dir(targetDir), receipt.ArchiveDirectory, journalFile))
	if err != nil || !bytes.Equal(raw, archived) {
		t.Fatal("original history bytes not preserved", err)
	}
	again, err := ImportColdState(t.Context(), target, targetDir, "cold-session", req)
	if err != nil || !reflect.DeepEqual(again, receipt) {
		t.Fatal("exact retry did not read the same receipt", err)
	}
	stale := req
	stale.MigrationID = uuid.NewString()
	if _, err := ImportColdState(t.Context(), target, targetDir, "cold-session", stale); err == nil {
		t.Fatal("different request reset imported history")
	}
}

func TestColdHistoryRejectsIncompleteOrChangedSource(t *testing.T) {
	for _, scenario := range []string{"open_turn", "changed_file", "partial", "unknown_file", "restore_temp"} {
		t.Run(scenario, func(t *testing.T) {
			j, root, dir := coldJournal(t)
			switch scenario {
			case "open_turn":
				if err := j.RecordTurnStart("pending", j.sessionID, time.Unix(12, 0).UTC()); err != nil {
					t.Fatal(err)
				}
			case "changed_file":
				if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("external"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "partial":
				op := restoreOperationFor(j)
				op.State, op.Paths[0].Status = "partial", "failed"
				if err := j.SaveRestoreOperation(op); err != nil {
					t.Fatal(err)
				}
			case "unknown_file", "restore_temp":
				name := "unknown"
				if scenario == "restore_temp" {
					name = restoreOperationTemp
				}
				if err := os.WriteFile(filepath.Join(dir, name), []byte("unverified"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(filepath.Join(dir, journalFile))
			if _, err := j.ExportColdState(); err == nil {
				t.Fatal("unverified source admitted")
			}
			after, _ := os.ReadFile(filepath.Join(dir, journalFile))
			if !bytes.Equal(before, after) {
				t.Fatal("inspection repaired or changed history")
			}
		})
	}
}

func TestColdHistoryTerminalRestoreAndPlanRebinding(t *testing.T) {
	j, root, _ := coldJournal(t)
	op, err := j.PrepareRestoreOperation("turn-1", "")
	if err != nil {
		t.Fatal(err)
	}
	op.ConfirmationID, op.PermissionRevision = "source-allow-once", "source-policy"
	op, err = j.ApplyRestoreOperation(t.Context(), op)
	if err != nil || op.State != "completed" {
		t.Fatal("source restore", err)
	}
	p, err := j.ExportColdState()
	if err != nil {
		t.Fatal(err)
	}
	target, targetDir := coldCopy(t, root)
	if _, err := ImportColdState(t.Context(), target, targetDir, j.sessionID, coldRequest(p)); err != nil {
		t.Fatal(err)
	}
	got, err := OpenRevisionJournal(target, targetDir, j.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	restored, found, err := got.LoadRestoreOperation()
	if err != nil || !found || restored.State != "completed" || restored.ID != op.ID || restored.PlanDigest == op.PlanDigest || restored.PlanDigest != RestoreOperationPlanDigest(restored) || !reflect.DeepEqual(restored.Paths, op.Paths) {
		t.Fatalf("terminal operation=%+v err=%v", restored, err)
	}
	q, err := got.ExportColdState()
	if err != nil || q.SemanticSHA256 != p.SemanticSHA256 {
		t.Fatal("physical rebinding changed semantic state", err)
	}
	if _, err := got.ApplyRestoreOperation(t.Context(), restored); err == nil {
		t.Fatal("stored source confirmation became new authority")
	}
}

func TestColdHistoryInterruptedExchangeStaysBlockedAndConverges(t *testing.T) {
	for _, stage := range []string{"marker_published", "before_exchange", "after_exchange", "parent_synced", "before_marker_remove"} {
		t.Run(stage, func(t *testing.T) {
			j, source, _ := coldJournal(t)
			p, err := j.ExportColdState()
			if err != nil {
				t.Fatal(err)
			}
			target, dir := coldCopy(t, source)
			req := coldRequest(p)
			injected := errors.New("owned crash seam")
			_, err = importColdState(t.Context(), target, dir, j.sessionID, req, func(at string) error {
				if at == stage {
					return injected
				}
				return nil
			})
			if err == nil {
				t.Fatal("fault did not stop import")
			}
			if view, err := OpenRevisionJournal(target, dir, j.sessionID); err == nil {
				_ = view.Close()
				t.Fatal("incomplete migration opened")
			}
			if _, err := ImportColdState(context.Background(), target, dir, j.sessionID, req); err != nil {
				t.Fatal("exact recovery failed", err)
			}
			view, err := OpenRevisionJournal(target, dir, j.sessionID)
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
		})
	}
}

func TestColdHistorySecondMigrationPreservesLineageAndCannotResetProgress(t *testing.T) {
	j, source, _ := coldJournal(t)
	p, err := j.ExportColdState()
	if err != nil {
		t.Fatal(err)
	}
	target, dir := coldCopy(t, source)
	old := coldRequest(p)
	if _, err := ImportColdState(t.Context(), target, dir, j.sessionID, old); err != nil {
		t.Fatal(err)
	}
	view, err := OpenRevisionJournal(target, dir, j.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	coldWrite(t, view, "turn-2", "after second turn")
	if _, err := ImportColdState(t.Context(), target, dir, j.sessionID, old); err == nil {
		t.Fatal("old import accepted after new progress")
	}
	q, err := view.ExportColdState()
	if err != nil {
		t.Fatal(err)
	}
	if err := view.Close(); err != nil {
		t.Fatal(err)
	}
	next, nextDir := coldCopy(t, target)
	if _, err := ImportColdState(t.Context(), next, nextDir, j.sessionID, coldRequest(q)); err != nil {
		t.Fatal("second migration", err)
	}
	last, err := OpenRevisionJournal(next, nextDir, j.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer last.Close()
	final, err := last.ExportColdState()
	if err != nil || final.SemanticSHA256 != q.SemanticSHA256 || len(last.Snapshot().Records) != 2 {
		t.Fatal("lineage or progress lost", err)
	}
}

func TestColdHistorySamePhysicalIdentityStillRequiresImportReceipt(t *testing.T) {
	j, root, dir := coldJournal(t)
	p, err := j.ExportColdState()
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	req := coldRequest(p)
	r, err := ImportColdState(t.Context(), root, dir, "cold-session", req)
	if err != nil || r.RootIdentity != p.RootIdentity || r.MigrationID != req.MigrationID {
		t.Fatal("same-identity input did not receive explicit provenance", err)
	}
	current, err := OpenRevisionJournal(root, dir, "cold-session")
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	actual, err := current.ExportColdState()
	if err != nil || actual.OriginSHA256 == "" || actual.SemanticSHA256 != p.SemanticSHA256 {
		t.Fatal("identity equality substituted for migration", err)
	}
}

func TestColdHistoryProofCrossLanguageDigest(t *testing.T) {
	p := ColdHistoryProof{Version: 1, Scope: "workspace-history-state-v1", SessionID: "session-1", Root: "/workspace/资料<&>\u2028", RootIdentity: "unix:1:2", JournalSHA256: strings.Repeat("a", 64), JournalBytes: 3, FrameSequence: 2, RestoreState: "none", SemanticSHA256: strings.Repeat("b", 64)}
	if got := coldProofDigest(p); got != "8fee5a1c5690219f3c8ec5bef40fe7e185268741bd1b82fd7d2a826cd3bf0c45" {
		t.Fatal("canonical proof changed", got)
	}
}

func TestColdHistoryRolledBackTransactionMigratesWithoutReexecution(t *testing.T) {
	j, root, _ := coldJournal(t)
	op, err := j.PrepareRestoreOperation("turn-1", "")
	if err != nil {
		t.Fatal(err)
	}
	op.ConfirmationID, op.PermissionRevision = "forward-confirm", "policy"
	j.restoreAfterPath = func(int) error { return errors.New("owned crash after filesystem transition") }
	if _, err := j.ApplyRestoreOperation(t.Context(), op); err == nil {
		t.Fatal("crash not reached")
	}
	j.restoreAfterPath = nil
	rollback, err := j.PrepareRestoreRecovery("rollback")
	if err != nil {
		t.Fatal(err)
	}
	rollback.ConfirmationID, rollback.PermissionRevision = "fresh-rollback-confirm", "new-policy"
	rollback, err = j.ApplyRestoreOperation(t.Context(), rollback)
	if err != nil || rollback.State != "rolled_back" {
		t.Fatal("rollback", err)
	}
	p, err := j.ExportColdState()
	if err != nil {
		t.Fatal(err)
	}
	target, dir := coldCopy(t, root)
	if _, err := ImportColdState(t.Context(), target, dir, j.sessionID, coldRequest(p)); err != nil {
		t.Fatal(err)
	}
	view, err := OpenRevisionJournal(target, dir, j.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	got, found, err := view.LoadRestoreOperation()
	if err != nil || !found || got.State != "rolled_back" || got.ID != rollback.ID {
		t.Fatal("rollback transaction lost", err)
	}
	data, err := os.ReadFile(filepath.Join(target, "a.txt"))
	if err != nil || string(data) != "after" {
		t.Fatal("migration replayed restore effects", err)
	}
}

func TestColdHistoryPartialStagingRemainsBlocked(t *testing.T) {
	j, source, _ := coldJournal(t)
	p, err := j.ExportColdState()
	if err != nil {
		t.Fatal(err)
	}
	target, dir := coldCopy(t, source)
	req := coldRequest(p)
	_, err = importColdState(t.Context(), target, dir, j.sessionID, req, func(stage string) error {
		if stage == "marker_published" {
			return errors.New("owned crash")
		}
		return nil
	})
	if err == nil {
		t.Fatal("fault not reached")
	}
	stage := filepath.Join(filepath.Dir(dir), filepath.Base(dir)+".cold-source-"+req.MigrationID)
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, journalFile), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, journalFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportColdState(t.Context(), target, dir, j.sessionID, req); err == nil {
		t.Fatal("unknown stage repaired implicitly")
	}
	after, err := os.ReadFile(filepath.Join(dir, journalFile))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("original progress changed", err)
	}
	if view, err := OpenRevisionJournal(target, dir, j.sessionID); err == nil {
		view.Close()
		t.Fatal("unverified stage opened")
	}
}

func TestColdHistoryOrdinaryOwnerRejectsChangedProvenance(t *testing.T) {
	for _, fault := range []string{"prefix", "missing_origin", "changed_origin", "archive"} {
		t.Run(fault, func(t *testing.T) {
			j, root, _ := coldJournal(t)
			p, err := j.ExportColdState()
			if err != nil {
				t.Fatal(err)
			}
			target, dir := coldCopy(t, root)
			r, err := ImportColdState(t.Context(), target, dir, j.sessionID, coldRequest(p))
			if err != nil {
				t.Fatal(err)
			}
			cached, err := OpenRevisionJournal(target, dir, j.sessionID)
			if err != nil {
				t.Fatal(err)
			}
			defer cached.Close()
			name := filepath.Join(dir, journalFile)
			if fault == "missing_origin" {
				err = os.Remove(filepath.Join(dir, coldOriginFile))
			} else {
				if fault == "changed_origin" {
					name = filepath.Join(dir, coldOriginFile)
				}
				if fault == "archive" {
					name = filepath.Join(filepath.Dir(dir), r.ArchiveDirectory, journalFile)
				}
				raw, readErr := os.ReadFile(name)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if fault == "changed_origin" {
					raw = bytes.ReplaceAll(raw, []byte(r.BindingSHA256), []byte(strings.Repeat("f", 64)))
				} else {
					// Same-size, individually valid frames; replay alone accepts them.
					raw = bytes.ReplaceAll(raw, []byte(`"ToolName":"Write"`), []byte(`"ToolName":"Edits"`))
				}
				err = os.WriteFile(name, raw, 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(dir, journalFile))
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := cached.LoadRestoreOperation(); err == nil {
				t.Fatal("cached owner accepted changed provenance")
			}
			if err := cached.RecordTurnStart("blocked", j.sessionID, time.Now().UTC()); err == nil {
				t.Fatal("append accepted changed provenance")
			}
			after, err := os.ReadFile(filepath.Join(dir, journalFile))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejected append changed history", err)
			}
			if fault != "changed_origin" {
				if reopened, err := OpenRevisionJournal(target, dir, j.sessionID); err == nil {
					reopened.Close()
					t.Fatal("fresh owner accepted missing provenance or changed prefix")
				}
			}
		})
	}
}

func TestColdHistoryFreshRestorePreviewDoesNotPublishAuthorityAndLiveTurnSupported(t *testing.T) {
	j, root, _ := coldJournal(t)
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
	defer current.Close()
	if _, err := current.PrepareRestoreOperation("turn-1", ""); err != nil {
		t.Fatal("fresh imported preview", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, restoreOperationFile)); !os.IsNotExist(err) {
		t.Fatal("preview published a restore transaction")
	}
	current.restorePreview = nil // Owned cancellation of this unused preview.
	if err := current.ReconcileRestoreReceipts(); err != nil {
		t.Fatal("receipt reconciliation", err)
	}
	if _, err := current.ExportColdState(); err != nil {
		t.Fatal("closed imported history cannot be observed", err)
	}
	if err := current.RecordTurnStart("live", j.sessionID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := current.LoadRestoreOperation(); err != nil {
		t.Fatal("normal live turn mistaken for incomplete migration", err)
	}
	if _, err := current.ExportColdState(); err == nil {
		t.Fatal("live turn was given a cold proof")
	}
}

func TestColdHistoryChangedTerminalTransactionRejectedByOrdinaryOwner(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback=%t", rollback), func(t *testing.T) {
			j, root, _ := coldJournal(t)
			op, err := j.PrepareRestoreOperation("turn-1", "")
			if err != nil {
				t.Fatal(err)
			}
			op.ConfirmationID, op.PermissionRevision = "confirmation", "policy"
			if rollback {
				j.restoreAfterPath = func(int) error { return errors.New("owned crash") }
				if _, err := j.ApplyRestoreOperation(t.Context(), op); err == nil {
					t.Fatal("crash not reached")
				}
				j.restoreAfterPath = nil
				op, err = j.PrepareRestoreRecovery("rollback")
				if err != nil {
					t.Fatal(err)
				}
				op.ConfirmationID, op.PermissionRevision = "confirmation", "policy"
			}
			if _, err := j.ApplyRestoreOperation(t.Context(), op); err != nil {
				t.Fatal(err)
			}
			p, err := j.ExportColdState()
			if err != nil {
				t.Fatal(err)
			}
			target, dir := coldCopy(t, root)
			if _, err := ImportColdState(t.Context(), target, dir, j.sessionID, coldRequest(p)); err != nil {
				t.Fatal(err)
			}
			cached, err := OpenRevisionJournal(target, dir, j.sessionID)
			if err != nil {
				t.Fatal(err)
			}
			defer cached.Close()
			name := filepath.Join(dir, restoreOperationFile)
			raw, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			var changed restoreOperationEnvelope
			if err := json.Unmarshal(raw, &changed); err != nil {
				t.Fatal(err)
			}
			changed.Operation.ConfirmationID = strings.Repeat("x", len(changed.Operation.ConfirmationID))
			if err := cached.validRestoreOperation(changed.Operation); err != nil {
				t.Fatal("negative fixture must remain independently valid", err)
			}
			data, err := json.Marshal(changed)
			if err != nil || len(data) != len(raw) || bytes.Equal(data, raw) {
				t.Fatal("negative fixture must change same-size terminal bytes", err)
			}
			if err := os.WriteFile(name, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := cached.LoadRestoreOperation(); err == nil {
				t.Fatal("cached owner accepted a substituted terminal operation")
			}
			if err := cached.RecordTurnStart("blocked", j.sessionID, time.Now().UTC()); err == nil {
				t.Fatal("append accepted substituted transaction")
			}
			if fresh, err := OpenRevisionJournal(target, dir, j.sessionID); err == nil {
				fresh.Close()
				t.Fatal("fresh owner accepted substituted transaction")
			}
			if got, err := os.ReadFile(name); err != nil || !bytes.Equal(got, data) {
				t.Fatal("rejection rewrote transaction evidence", err)
			}
		})
	}
}

func TestColdHistoryCanceledBeforeImportPreservesSource(t *testing.T) {
	j, root, _ := coldJournal(t)
	p, err := j.ExportColdState()
	if err != nil {
		t.Fatal(err)
	}
	target, dir := coldCopy(t, root)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ImportColdState(ctx, target, dir, j.sessionID, coldRequest(p)); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation not preserved", err)
	}
	if err := CheckColdHistoryGate(dir); err != nil {
		t.Fatal("pre-canceled request published a marker", err)
	}
}

func TestColdHistoryConcurrentImportOneWinnerAndExplicitRetry(t *testing.T) {
	j, root, _ := coldJournal(t)
	p, err := j.ExportColdState()
	if err != nil {
		t.Fatal(err)
	}
	target, dir := coldCopy(t, root)
	request := coldRequest(p)
	var results [2]ColdHistoryReceipt
	var failures [2]error
	start := make(chan struct{})
	var group sync.WaitGroup
	for i := range results {
		group.Go(func() {
			<-start
			results[i], failures[i] = ImportColdState(t.Context(), target, dir, j.sessionID, request)
		})
	}
	close(start)
	group.Wait()
	winner := -1
	for i, err := range failures {
		if err == nil {
			if winner >= 0 && results[i] != results[winner] {
				t.Fatal("duplicate import returned different provenance")
			}
			winner = i
		} else if !strings.Contains(err.Error(), "in use by another operation") {
			t.Fatal("unexpected concurrent failure", err)
		}
	}
	if winner < 0 {
		t.Fatal("neither import acquired admission", failures)
	}
	repeated, err := ImportColdState(t.Context(), target, dir, j.sessionID, request)
	if err != nil || repeated != results[winner] {
		t.Fatal("explicit exact retry did not converge", err)
	}
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(dir), j.sessionID+".workspace-history.cold-source-*"))
	if err != nil || len(paths) != 1 || CheckColdHistoryGate(dir) != nil {
		t.Fatal("duplicate transaction or pending marker", paths, err)
	}
}

func TestColdHistoryNoMarkerCanExistUnderRegularFile(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "transcripts")
	if err := os.WriteFile(parent, []byte("transient transcript blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckColdHistoryGate(filepath.Join(parent, "new.workspace-history")); err != nil {
		t.Fatal("absent history bypassed the ordinary transcript repair owner", err)
	}
}

func TestColdHistoryRejectsSystemRootBeforePublishingUnverifiableOrigin(t *testing.T) {
	if !RestoreFilesystemSupported() {
		t.Skip("atomic directory exchange unsupported")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".yhc", "transcripts", "cold-session.workspace-history")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	j, err := OpenRevisionJournal(root, dir, "cold-session")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	p, err := j.ExportColdState()
	if err != nil {
		t.Fatal(err)
	}
	_, target := coldCopy(t, root)
	// Every path that could be written still belongs to this test's temporary
	// directory. The system root is only the rejected logical root argument.
	if _, err := ImportColdState(t.Context(), string(filepath.Separator), target, "cold-session", coldRequest(p)); err == nil {
		t.Fatal("published an origin whose root the proof schema cannot accept")
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 || CheckColdHistoryGate(target) != nil {
		t.Fatal("invalid root changed retained history or left a marker", err)
	}
}

func TestColdHistoryFreshProcessCrashAndRecovery(t *testing.T) {
	for _, stage := range []string{"marker_published", "before_exchange", "after_exchange", "parent_synced", "before_marker_remove"} {
		t.Run(stage, func(t *testing.T) {
			j, source, _ := coldJournal(t)
			op, err := j.PrepareRestoreOperation("turn-1", "")
			if err != nil {
				t.Fatal(err)
			}
			op.ConfirmationID, op.PermissionRevision = "source-confirm", "policy"
			if _, err := j.ApplyRestoreOperation(t.Context(), op); err != nil {
				t.Fatal(err)
			}
			p, err := j.ExportColdState()
			if err != nil {
				t.Fatal(err)
			}
			target, dir := coldCopy(t, source)
			req := coldRequest(p)
			control := t.TempDir()
			input := filepath.Join(control, "request.json")
			output := filepath.Join(control, "receipt.json")
			raw, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(input, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			run := func(mode string) error {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestColdHistoryProcessHelper$")
				cmd.Env = append(os.Environ(), "YHC_TEST_COLD_MODE="+mode, "YHC_TEST_COLD_ROOT="+target, "YHC_TEST_COLD_REQUEST="+input, "YHC_TEST_COLD_OUTPUT="+output, "YHC_TEST_COLD_PHASE="+stage)
				data, err := cmd.CombinedOutput()
				if err != nil {
					var status *exec.ExitError
					if mode == "crash" && errors.As(err, &status) && status.ExitCode() == 77 {
						return err
					}
					t.Fatalf("helper mode=%s err=%v output=%s", mode, err, data)
				}
				return err
			}
			if err := run("crash"); err == nil {
				t.Fatal("actual process did not crash")
			}
			if view, err := OpenRevisionJournal(target, dir, j.sessionID); err == nil {
				view.Close()
				t.Fatal("crashed import activated")
			}
			if err := run("recover"); err != nil {
				t.Fatal(err)
			}
			view, err := OpenRevisionJournal(target, dir, j.sessionID)
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			q, err := view.ExportColdState()
			if err != nil || q.SemanticSHA256 != p.SemanticSHA256 {
				t.Fatal("fresh process recovery changed state", err)
			}
		})
	}
}

func TestColdHistoryProcessHelper(t *testing.T) {
	mode := os.Getenv("YHC_TEST_COLD_MODE")
	if mode == "" {
		t.Skip("owned subprocess helper")
	}
	root, input := os.Getenv("YHC_TEST_COLD_ROOT"), os.Getenv("YHC_TEST_COLD_REQUEST")
	raw, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	var request ColdHistoryImport
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".yhc", "transcripts", request.Source.SessionID+".workspace-history")
	receipt, err := importColdState(context.Background(), root, dir, request.Source.SessionID, request, func(phase string) error {
		if mode == "crash" && phase == os.Getenv("YHC_TEST_COLD_PHASE") {
			os.Exit(77)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("YHC_TEST_COLD_OUTPUT"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
