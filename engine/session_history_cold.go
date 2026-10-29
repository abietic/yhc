package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/abietic/yhc/engine/filehistory"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/tools"
)

// ImportWorkspaceHistory is an explicit provider-free administration operation
// for an externally admitted closed workspace. It does not start a query or
// supply restore confirmation, dispatch permission, or a spending allowance.
func (s *SessionService) ImportWorkspaceHistory(ctx context.Context, info session.SessionInfo, request filehistory.ColdHistoryImport) (filehistory.ColdHistoryReceipt, error) {
	if s == nil || s.engine == nil || !s.engine.administrationOnly || info.SessionID != request.Source.SessionID || filehistory.ValidateColdHistoryImport(request) != nil {
		return filehistory.ColdHistoryReceipt{}, fmt.Errorf("history import requires provider-free canonical Session administration")
	}
	if request.Source.Root != s.engine.GetCWD() {
		return filehistory.ColdHistoryReceipt{}, fmt.Errorf("cold Session import does not support logical root remapping")
	}
	if _, err := s.ResumeInfo(ctx, info); err != nil {
		return filehistory.ColdHistoryReceipt{}, err
	}
	e := s.engine
	receipt, err := filehistory.ImportColdState(ctx, e.GetCWD(), filepath.Join(e.GetTranscriptDir(), info.SessionID+".workspace-history"), info.SessionID, request)
	if err != nil {
		return filehistory.ColdHistoryReceipt{}, err
	}
	e.resetWorkspaceRevisions()
	return receipt, nil
}

// restoreColdHistoryGuards is called only after native/history/accounting
// binding. Reobserve history before discarding any saved read credit.
func (e *QueryEngine) restoreColdHistoryGuards(snapshot tools.ReadGuardSnapshot, expected filehistory.ColdHistoryProof) (*tools.FileReadGuards, error) {
	identity, c := e.lockWorkspaceRevisions()
	if c.restoreReadGuards != nil || c.restoreCancel != nil || c.externalWriters != 0 {
		c.mu.Unlock()
		return nil, ErrColdToolHandoff
	}
	j := c.journalLocked(identity)
	if j == nil {
		c.mu.Unlock()
		return nil, ErrColdToolHandoff
	}
	actual, err := j.ExportColdState()
	if err != nil || !reflect.DeepEqual(actual, expected) {
		c.mu.Unlock()
		return nil, ErrColdToolHandoff
	}
	var paths []string
	for _, record := range j.Snapshot().Records {
		if record.Outcome == "succeeded" && (record.ToolName == "WorkspaceRestore" || record.ToolName == "WorkspaceRestoreRollback") {
			paths = append(paths, filepath.ToSlash(record.Path))
		}
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	guards, err := tools.RestoreFileReadGuardsExcluding(snapshot, paths)
	if err != nil {
		c.mu.Unlock()
		return nil, ErrColdToolHandoff
	}
	c.restoreReadGuards = guards
	generation := c.generation
	c.mu.Unlock()
	if len(paths) > 0 {
		e.invalidateRestoredPaths(identity, generation, "cold:"+expected.SemanticSHA256, identity.cwd, paths)
	}
	return guards, nil
}
