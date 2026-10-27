package filehistory

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PrepareRestoreOperation builds a read-only, guarded plan to restore the
// captured file set to the point immediately before turnID began.
func (j *RevisionJournal) PrepareRestoreOperation(turnID, onlyRevisionID string) (RestoreOperation, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.closed || j.sealed {
		return RestoreOperation{}, ErrJournalClosed
	}
	if err := j.coldRestoreMutationErrorLocked(); err != nil {
		return RestoreOperation{}, err
	}
	if err := j.verifyFreshRestoreJournalLocked(); err != nil {
		return RestoreOperation{}, err
	}
	if j.warning != "" {
		return RestoreOperation{}, fmt.Errorf("revision journal is incomplete")
	}
	if !restoreFilesystemSupported() {
		return RestoreOperation{}, fmt.Errorf("guarded workspace restore is unavailable on this platform")
	}
	if err := j.checkRootIdentity(); err != nil {
		return RestoreOperation{}, fmt.Errorf("workspace root identity changed")
	}
	if existing, found, err := j.loadRestoreOperationLocked(); err != nil {
		return RestoreOperation{}, fmt.Errorf("read restore operation")
	} else if found && (existing.State == "applying" || existing.State == "partial") {
		return RestoreOperation{}, fmt.Errorf("restore operation is already active")
	}

	target, targetIndex, ok := j.restoreTurnLocked(turnID)
	if !ok || target.FinishedAt.IsZero() || !target.RestoreBoundaryKnown || !target.RestoreEligible {
		return RestoreOperation{}, fmt.Errorf("restore target is not eligible")
	}
	for _, id := range j.turnOrder[targetIndex+1:] {
		turn := j.turns[id]
		if turn.FinishedAt.IsZero() || !turn.RestoreBoundaryKnown || !turn.RestoreEligible {
			return RestoreOperation{}, fmt.Errorf("later turn is not eligible for restore")
		}
	}

	for _, recordID := range j.order {
		if record := j.records[recordID]; record.Path == "" {
			return RestoreOperation{}, fmt.Errorf("an earlier unobserved writer prevents restore in this session")
		}
	}
	cutoff := target.FileBoundarySequence
	selectedPath := ""
	if onlyRevisionID != "" {
		record, found := j.records[onlyRevisionID]
		if !found || record.TurnID != turnID || !validRestoreRecord(record, j.sessionID) {
			return RestoreOperation{}, fmt.Errorf("requested revision is not restorable")
		}
		if record.Sequence <= target.FileBoundarySequence {
			return RestoreOperation{}, fmt.Errorf("requested revision is not restorable")
		}
		cutoff = record.Sequence - 1
		selectedPath = record.Path
	}

	if cutoff > uint64(len(j.order)) {
		return RestoreOperation{}, fmt.Errorf("restore boundary is invalid")
	}
	paths := map[string]*RestorePath{}
	for _, id := range j.order {
		record, found := j.records[id]
		if !found || record.Sequence == 0 || record.Sequence > uint64(len(j.order)) {
			return RestoreOperation{}, fmt.Errorf("revision journal is incomplete")
		}
		if record.Sequence <= target.FileBoundarySequence {
			continue
		}
		if !validRestoreRecord(record, j.sessionID) {
			return RestoreOperation{}, fmt.Errorf("revision journal contains an unrestoreable change")
		}
		if j.privateRestorePath(record.Path) {
			return RestoreOperation{}, fmt.Errorf("revision journal private path cannot be restored")
		}
		if rel, err := j.cleanPath(record.Path); err != nil || rel != record.Path {
			return RestoreOperation{}, fmt.Errorf("revision journal path is not restorable")
		}
		if record.Sequence <= cutoff {
			continue
		}
		if selectedPath != "" && record.Path != selectedPath {
			continue
		}
		path := paths[record.Path]
		if path == nil {
			path = &RestorePath{Path: record.Path, Original: cloneRestoreState(record.After), Target: cloneRestoreState(record.Before)}
			paths[record.Path] = path
			continue
		}
		if !sameState(path.Original, record.Before) {
			return RestoreOperation{}, fmt.Errorf("revision chain has an unobserved gap")
		}
		path.Original = cloneRestoreState(record.After)
	}

	operation := RestoreOperation{
		SessionID: j.sessionID, TurnID: turnID, Root: j.rootPath, RootIdentity: j.rootIdentity,
		JournalRevision: j.frameSequence, StartedAt: time.Now().UTC(), Direction: "restore", State: "applying",
	}
	var err error
	if operation.ID, err = randomID(); err != nil {
		return RestoreOperation{}, fmt.Errorf("generate restore operation ID: %w", err)
	}
	for path, restorePath := range paths {
		if sameState(restorePath.Original, restorePath.Target) {
			continue
		}
		current, captureErr := j.captureRestorePath(path)
		if captureErr != nil {
			return RestoreOperation{}, fmt.Errorf("restore preflight failed")
		}
		if sameState(current, restorePath.Target) {
			continue
		}
		if !sameState(current, restorePath.Original) {
			return RestoreOperation{}, fmt.Errorf("restore file state conflict")
		}
		parent := filepath.Dir(path)
		index := len(operation.Paths)
		restorePath.BackupPath = filepath.Join(parent, fmt.Sprintf(".yhc-restore-%s-%d.backup", operation.ID, index))
		restorePath.TempPath = filepath.Join(parent, fmt.Sprintf(".yhc-restore-%s-%d.tmp", operation.ID, index))
		restorePath.Status = "pending"
		operation.Paths = append(operation.Paths, *restorePath)
	}
	if len(operation.Paths) == 0 {
		return RestoreOperation{}, fmt.Errorf("restore plan has no changed files")
	}
	// Map iteration is intentionally normalized before allocating the final
	// sibling names, which makes both the plan and its digest deterministic.
	sortRestorePaths(operation.Paths)
	for index := range operation.Paths {
		parent := filepath.Dir(operation.Paths[index].Path)
		operation.Paths[index].BackupPath = filepath.Join(parent, fmt.Sprintf(".yhc-restore-%s-%d.backup", operation.ID, index))
		operation.Paths[index].TempPath = filepath.Join(parent, fmt.Sprintf(".yhc-restore-%s-%d.tmp", operation.ID, index))
		if err := j.preflightRestorePath(operation.Paths[index], "restore", true); err != nil {
			return RestoreOperation{}, fmt.Errorf("restore preflight failed")
		}
	}
	operation.PlanDigest = RestoreOperationPlanDigest(operation)
	if err := j.validateRestorePlanLocked(operation); err != nil {
		return RestoreOperation{}, err
	}
	return j.issueRestorePreviewLocked(operation)
}

func (j *RevisionJournal) restoreTurnLocked(turnID string) (TurnRecord, int, bool) {
	for index, id := range j.turnOrder {
		if id == turnID {
			return j.turns[id], index, true
		}
	}
	return TurnRecord{}, 0, false
}

func validRestoreRecord(record RevisionRecord, sessionID string) bool {
	if validRecord(record, sessionID) != nil || record.Path == "" || !record.ParentExisted || !validOutcome(record.Outcome) || !validStoredPath(record.Path) {
		return false
	}
	switch record.ToolName {
	case "Write", "Edit", "WorkspaceRestore", "WorkspaceRestoreRollback":
	default:
		return false
	}
	return validCapturedRestoreState(record.Before) && validCapturedRestoreState(record.After)
}

func (j *RevisionJournal) privateRestorePath(path string) bool {
	for _, parent := range []string{filepath.Dir(j.dir), resolvedRestoreParent(j.dir)} {
		privateParent, err := filepath.Rel(j.rootPath, parent)
		if err == nil && privateParent == "." {
			return true
		}
		if err == nil && privateParent != "." && privateParent != ".." && !strings.HasPrefix(privateParent, ".."+string(filepath.Separator)) && (path == privateParent || strings.HasPrefix(path, privateParent+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

func resolvedRestoreParent(dir string) string {
	parent, err := filepath.EvalSymlinks(filepath.Dir(dir))
	if err != nil {
		return ""
	}
	return parent
}

func (j *RevisionJournal) validateRestorePlanLocked(operation RestoreOperation) error {
	check := cloneRestoreOperation(operation)
	check.ConfirmationID = "prepare"
	check.PermissionRevision = "prepare"
	if err := j.validRestoreOperation(check); err != nil {
		return fmt.Errorf("invalid restore plan")
	}
	encoded, err := json.Marshal(restoreOperationEnvelope{Kind: restoreOperationKind, Version: restoreOperationVersion, Operation: check})
	if err != nil || len(encoded) > maxRestoreEncodedBytes {
		return fmt.Errorf("restore plan exceeds encoded size limit")
	}
	return nil
}

func cloneRestoreState(state RevisionFileState) RevisionFileState {
	state.Data = append([]byte(nil), state.Data...)
	return state
}

func sortRestorePaths(paths []RestorePath) {
	sort.Slice(paths, func(i, k int) bool { return paths[i].Path < paths[k].Path })
}
