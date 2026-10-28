package filehistory

import (
	"context"
	"fmt"
	"reflect"
	"time"
)

type restorePreviewAdmission struct {
	token, id, direction, digest string
	revision                     uint64
	at                           time.Time
	current                      []RevisionFileState
}

func (j *RevisionJournal) issueRestorePreviewLocked(op RestoreOperation) (RestoreOperation, error) {
	token, err := randomID()
	if err != nil {
		return RestoreOperation{}, err
	}
	states := make([]RevisionFileState, len(op.Paths))
	for index, path := range op.Paths {
		if err := j.preflightRestorePath(path, op.Direction, false); err != nil {
			return RestoreOperation{}, err
		}
		current, err := j.captureRestorePath(path.Path)
		if err != nil {
			return RestoreOperation{}, err
		}
		if current.Exists && !sameState(current, path.Original) && !sameState(current, path.Target) {
			return RestoreOperation{}, fmt.Errorf("restore preview changed")
		}
		states[index] = current
	}
	j.restorePreview = &restorePreviewAdmission{token: token, id: op.ID, direction: op.Direction, digest: op.PlanDigest, revision: j.frameSequence, at: time.Now(), current: states}
	op.previewToken = token
	return cloneRestoreOperation(op), nil
}

func (j *RevisionJournal) consumeRestorePreviewLocked(op RestoreOperation) ([]RevisionFileState, error) {
	admission := j.restorePreview
	if admission == nil || op.previewToken == "" || admission.token != op.previewToken || admission.id != op.ID || admission.direction != op.Direction || admission.digest != op.PlanDigest || admission.revision != j.frameSequence || time.Since(admission.at) > 2*time.Minute {
		return nil, fmt.Errorf("a fresh process-local restore preview is required")
	}
	j.restorePreview = nil
	return admission.current, nil
}

// RestoreFilesystemSupported reports whether anchored no-replace installation
// and directory locking are implemented on this host.
func RestoreFilesystemSupported() bool { return restoreFilesystemSupported() }

// lockStoreLocked holds a stable private directory, never a replaceable lock
// file. The caller owns j.mu. No lock is held while awaiting user permission.
func (j *RevisionJournal) lockStoreLocked() (func(), error) {
	if j.restoreLeaseHeld {
		return func() {}, nil
	}
	if err := j.ensureDirForAppend(); err != nil {
		return nil, err
	}
	if err := j.checkRestoreStoreDirectory(j.dirRoot); err != nil {
		return nil, err
	}
	file, err := j.dirRoot.Open(".")
	if err != nil {
		return nil, err
	}
	unlock, err := lockRestoreDirectory(file)
	if err != nil {
		file.Close()
		return nil, err
	}
	if err := j.checkRestoreStoreDirectory(j.dirRoot); err != nil {
		unlock()
		file.Close()
		return nil, err
	}
	return func() { unlock(); _ = file.Close() }, nil
}

// verifyFreshRestoreJournalLocked rejects a stale process-local replay. It
// never repairs, creates or truncates history.
func (j *RevisionJournal) verifyFreshRestoreJournalLocked() error {
	fresh, err := OpenRevisionJournal(j.rootPath, j.dir, j.sessionID)
	if err != nil {
		return fmt.Errorf("workspace history cannot be revalidated")
	}
	defer fresh.Close()
	if fresh.frameSequence != j.frameSequence || fresh.bytes != j.bytes || !reflect.DeepEqual(fresh.records, j.records) || !reflect.DeepEqual(fresh.turns, j.turns) || !reflect.DeepEqual(fresh.order, j.order) || !reflect.DeepEqual(fresh.turnOrder, j.turnOrder) {
		return fmt.Errorf("workspace history changed; reopen the session before restoring")
	}
	return nil
}

// PrepareRestoreRecovery previews a durable interrupted operation. Stored
// confirmation/permission fields confer no authority to resume it.
func (j *RevisionJournal) PrepareRestoreRecovery(direction string) (RestoreOperation, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !restoreFilesystemSupported() {
		return RestoreOperation{}, fmt.Errorf("guarded workspace restore is unavailable on this platform")
	}
	if err := j.verifyFreshRestoreJournalLocked(); err != nil {
		return RestoreOperation{}, err
	}
	op, found, err := j.loadRestoreOperationLocked()
	if err != nil || !found {
		return RestoreOperation{}, fmt.Errorf("no recoverable restore operation")
	}
	if terminalRestoreState(op.State) {
		return RestoreOperation{}, fmt.Errorf("restore operation is already finished")
	}
	if !validRestoreDirection(direction) || op.Direction == "rollback" && direction != "rollback" {
		return RestoreOperation{}, fmt.Errorf("rollback must finish before another restore")
	}
	op.Direction = direction
	for _, path := range op.Paths {
		if err := j.preflightRestorePath(path, direction, false); err != nil {
			return RestoreOperation{}, fmt.Errorf("restore recovery has a file conflict")
		}
	}
	op.ConfirmationID, op.PermissionRevision = "", ""

	return j.issueRestorePreviewLocked(op)
}

// ApplyRestoreOperation is a bounded low-level transition. The engine must
// supply a newly confirmed plan and a fresh allow-once decision immediately
// before calling it. Durable fields are receipts, never permission grants.
func (j *RevisionJournal) ApplyRestoreOperation(ctx context.Context, op RestoreOperation) (RestoreOperation, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if j.closed || j.sealed {
		return op, ErrJournalClosed
	}
	if !restoreFilesystemSupported() {
		return op, fmt.Errorf("guarded workspace restore is unavailable on this platform")
	}
	if err := j.validRestoreOperation(op); err != nil {
		return op, err
	}
	previewStates, err := j.consumeRestorePreviewLocked(op)
	if err != nil {
		return op, err
	}
	unlock, err := j.lockStoreLocked()
	if err != nil {
		return op, err
	}
	defer unlock()
	j.restoreLeaseHeld = true
	defer func() { j.restoreLeaseHeld = false }()
	if err := j.verifyFreshRestoreJournalLocked(); err != nil {
		return op, err
	}
	stored, found, err := j.loadRestoreOperationLocked()
	if err != nil {
		return op, err
	}
	recovery := found && stored.ID == op.ID
	if found && !recovery && !terminalRestoreState(stored.State) {
		return op, fmt.Errorf("another restore is pending")
	}
	if recovery {
		if terminalRestoreState(stored.State) || !sameRestoreImmutable(stored, op) || stored.Direction == "rollback" && op.Direction != "rollback" {
			return op, fmt.Errorf("stale restore recovery")
		}
		// The store, not a preview caller, owns the last durable per-path status.
		for index := range op.Paths {
			op.Paths[index].Status = stored.Paths[index].Status
		}
	} else if op.JournalRevision != j.frameSequence || op.Direction != "restore" {
		return op, fmt.Errorf("stale restore preview")
	}
	for index, path := range op.Paths {
		current, err := j.captureRestorePath(path.Path)
		if err != nil || index >= len(previewStates) || !sameState(current, previewStates[index]) {
			return op, fmt.Errorf("restore files changed since preview; prepare again")
		}
		if err := j.preflightRestorePath(path, op.Direction, !recovery); err != nil {
			return op, err
		}
		if !recovery {
			current, err := j.captureRestorePath(path.Path)
			if err != nil || !sameState(current, path.Original) {
				return op, fmt.Errorf("restore preview changed; prepare it again")
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return op, err
	}
	op.State = "applying"
	if err := j.admitRestoreEvolutionLocked(op); err != nil {
		return op, err
	}
	if err := j.saveRestoreOperationLocked(op); err != nil {
		return op, err
	}
	for index := range op.Paths {
		if err := ctx.Err(); err != nil {
			return j.partialRestoreLocked(op, err)
		}
		if err := j.verifyFreshRestoreJournalLocked(); err != nil {
			return j.partialRestoreLocked(op, err)
		}
		status, err := j.restorePathFilesystem(op.Paths[index], op.Direction)
		if err != nil {
			op.Paths[index].Status = "failed"
			return j.partialRestoreLocked(op, err)
		}
		if j.restoreAfterPath != nil {
			if err := j.restoreAfterPath(index); err != nil {
				return op, err
			} // crash seam: stable intent remains applying
		}
		if op.Direction == "rollback" && status == "applied" {
			status = "rolled_back"
		}
		op.Paths[index].Status = status
		if err := j.saveRestoreOperationLocked(op); err != nil {
			return op, err
		}
	}
	if op.Direction == "rollback" {
		op.State = "rolled_back"
	} else {
		op.State = "completed"
	}
	if err := j.saveRestoreOperationLocked(op); err != nil {
		return op, err
	}
	if j.restoreAfterTerminalSave != nil {
		if err := j.restoreAfterTerminalSave(); err != nil {
			return op, err
		}
	}
	if err := j.reconcileRestoreReceiptsLocked(op); err != nil {
		return op, fmt.Errorf("files restored; history receipts are incomplete: %w", err)
	}
	if err := j.cleanupRestoreSiblings(op); err != nil {
		j.warning = "Restore finished; a sibling backup could not be safely removed."
		return cloneRestoreOperation(op), fmt.Errorf("files restored; owned backup cleanup is incomplete")
	}
	return cloneRestoreOperation(op), nil
}

func (j *RevisionJournal) partialRestoreLocked(op RestoreOperation, cause error) (RestoreOperation, error) {
	op.State = "partial"
	if err := j.saveRestoreOperationLocked(op); err != nil {
		return op, fmt.Errorf("restore interrupted; progress persistence is uncertain: %w", err)
	}
	return cloneRestoreOperation(op), fmt.Errorf("restore interrupted; preview retry or rollback: %w", cause)
}

// ReconcileRestoreReceipts records completed local transitions before accepting
// a new model turn. Existing bindings and missing observed results must verify;
// remaining owned siblings block admission. It never changes workspace files
// or resumes an operation.
func (j *RevisionJournal) ReconcileRestoreReceipts() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	op, found, err := j.loadRestoreOperationLocked()
	if err != nil {
		return err
	}
	if !found || !terminalRestoreState(op.State) {
		return nil
	}
	unlock, err := j.lockStoreLocked()
	if err != nil {
		return err
	}
	defer unlock()
	j.restoreLeaseHeld = true
	defer func() { j.restoreLeaseHeld = false }()
	current, stillFound, err := j.loadRestoreOperationLocked()
	if err != nil || !stillFound || !reflect.DeepEqual(current, op) || !terminalRestoreState(current.State) {
		return fmt.Errorf("restore operation changed before reconciliation")
	}
	if err := j.restoreSiblingsAbsent(current); err != nil {
		return err
	}
	return j.reconcileRestoreReceiptsLocked(current)
}

func (j *RevisionJournal) reconcileRestoreReceiptsLocked(op RestoreOperation) error {
	if err := j.verifyFreshRestoreJournalLocked(); err != nil {
		return err
	}
	plan := restoreOperationPlan(op)
	var pending []RevisionRecord
	for index, path := range op.Paths {
		want, err := j.restoreReceiptRequirement(plan, op.Direction, index)
		if err != nil {
			return err
		}
		if _, ok := j.records[want.id]; ok || want.before == want.after {
			continue
		}
		before, after := path.Original, path.Target
		if op.Direction == "rollback" {
			before, after = path.Target, path.Original
		}
		actual, err := j.captureRestorePath(path.Path)
		if err != nil || !sameState(actual, after) {
			return fmt.Errorf("restore receipt target is unverified")
		}
		pending = append(pending, RevisionRecord{ID: want.id, SessionID: j.sessionID, TurnID: op.ID, ToolUseID: op.ID, ToolName: want.tool, Path: path.Path, Outcome: "succeeded", ParentExisted: true, Before: cloneRestoreState(before), After: cloneRestoreState(after)})
	}
	if len(j.order)+len(pending) > maxLogicalRecords {
		return ErrJournalCapacity
	}
	bytesNeeded := j.bytes
	stamp := time.Now().UTC()
	for index := range pending {
		pending[index].Sequence, pending[index].Timestamp = uint64(len(j.order)+index+1), stamp
		frame := journalFrame{Version: 2, Type: "restore_result", Root: j.rootPath, SessionID: j.sessionID, Record: pending[index]}
		encoded, err := j.encodeFrame(frame, j.frameSequence+uint64(index)+1)
		if err != nil {
			return err
		}
		bytesNeeded += int64(len(encoded) + 1)
		if bytesNeeded > maxJournalBytes {
			return ErrJournalCapacity
		}
	}
	for _, record := range pending {
		// All known conflicts/capacity were checked before publishing any fact.
		// Repeat the bounded observation immediately before its append.
		actual, err := j.captureRestorePath(record.Path)
		if err != nil || !sameState(actual, record.After) {
			return fmt.Errorf("restore receipt target changed")
		}
		if err := j.append("restore_result", record); err != nil {
			return err
		}
		j.records[record.ID] = cloneRecord(record)
		j.order = append(j.order, record.ID)
	}
	return nil
}

// RestorePreviewStates returns the actual states observed for this exact live
// preview. It never presents the immutable original state as the current disk.
func (j *RevisionJournal) RestorePreviewStates(op RestoreOperation) ([]RevisionFileState, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	admission := j.restorePreview
	if admission == nil || op.previewToken == "" || admission.token != op.previewToken || admission.id != op.ID || admission.direction != op.Direction || admission.digest != op.PlanDigest {
		return nil, fmt.Errorf("restore preview is no longer current")
	}
	states := make([]RevisionFileState, len(admission.current))
	for index, state := range admission.current {
		states[index] = cloneRestoreState(state)
	}
	return states, nil
}
