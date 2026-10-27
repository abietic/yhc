package engine

import (
	"context"
	"fmt"
	"path/filepath"

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

// bindColdHistoryGuards is called only after native/history/accounting proofs
// match the actual retained stores and before consuming the launch claim.
func (e *QueryEngine) bindColdHistoryGuards(guards *tools.FileReadGuards) error {
	identity, c := e.lockWorkspaceRevisions()
	if guards == nil || c.restoreReadGuards != nil || c.restoreCancel != nil || c.externalWriters != 0 {
		c.mu.Unlock()
		return ErrColdToolHandoff
	}
	j := c.journalLocked(identity)
	if j == nil {
		c.mu.Unlock()
		return ErrColdToolHandoff
	}
	op, found, err := j.LoadRestoreOperation()
	if err != nil || found && op.State != "completed" && op.State != "rolled_back" {
		c.mu.Unlock()
		return ErrColdToolHandoff
	}
	c.restoreReadGuards = guards
	generation := c.generation
	c.mu.Unlock()
	if found {
		e.invalidateRestoredWorkspace(identity, generation, op)
	}
	return nil
}
