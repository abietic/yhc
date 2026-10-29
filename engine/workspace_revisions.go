package engine

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/abietic/yhc/tools"

	"github.com/abietic/yhc/engine/filehistory"
)

// WorkspaceChangeSnapshot lists captured file facts, never inferred Git state
// or a claim that arbitrary tool/external effects can be undone.
type WorkspaceChangeSnapshot struct {
	Restore             *WorkspaceRestoreStatus
	Turns               []filehistory.TurnRecord
	OwnerGeneration     uint64
	SessionID, ThreadID string
	Revision            uint64
	Records             []filehistory.RevisionHeader
	Warning             string
}

type workspaceRevisionController struct {
	hadRestoreAuthority          bool
	restorePreview               *workspaceRestorePending
	restoreCancel                context.CancelFunc
	restoreNoticePaths           []string
	externalWriters              int
	restoreReadGuards            *tools.FileReadGuards
	lastRestoreGuardID           string
	mu                           sync.Mutex
	journal                      *filehistory.RevisionJournal
	sessionID, cwd, dir, warning string
	closed                       bool
	generation                   uint64
	unavailable                  bool
	turns                        map[string]filehistory.TurnRecord
}

type workspaceRevisionIdentity struct {
	sessionID, threadID, cwd, dir string
	child, closed                 bool
}

func (e *QueryEngine) workspaceRevisionIdentity() workspaceRevisionIdentity {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.workspaceRevisionIdentityLocked()
}

func (e *QueryEngine) workspaceRevisionIdentityLocked() workspaceRevisionIdentity {
	return workspaceRevisionIdentity{sessionID: e.config.SessionID, threadID: e.config.ThreadID, cwd: e.config.CWD, dir: e.config.TranscriptDir, child: e.config.AgentID != "", closed: e.promptRouteClosed}
}

// WorkspaceHistoryAvailability is metadata-only and never opens a journal.
func (e *QueryEngine) WorkspaceHistoryAvailability() (bool, string) {
	if e == nil {
		return false, "Workspace history requires an interactive engine."
	}
	identity := e.workspaceRevisionIdentity()
	if identity.closed {
		return false, "The engine is closed."
	}
	if identity.child {
		return false, "Workspace history uses the leader session."
	}
	if !filepath.IsAbs(identity.cwd) {
		return false, "Workspace history requires an absolute working directory."
	}
	return true, ""
}

func (c *workspaceRevisionController) journalLocked(identity workspaceRevisionIdentity) *filehistory.RevisionJournal {
	if c.closed || identity.closed || identity.child || !filepath.IsAbs(identity.cwd) {
		return nil
	}
	if c.sessionID != "" && (c.sessionID != identity.sessionID || c.cwd != identity.cwd || c.dir != identity.dir) {
		if c.journal != nil {
			_ = c.journal.Close()
		}
		c.journal = nil
		c.warning = ""
		c.unavailable = false
		c.turns = nil
	}
	if c.unavailable {
		return nil
	}
	if c.journal == nil {
		c.sessionID, c.cwd, c.dir = identity.sessionID, identity.cwd, identity.dir
		journal, err := filehistory.OpenRevisionJournal(identity.cwd, filepath.Join(identity.dir, identity.sessionID+".workspace-history"), identity.sessionID)
		if err != nil {
			c.unavailable = true
			c.warning = "Workspace history is unavailable; the existing journal was preserved."
			return nil
		}
		c.journal = journal
		c.turns = make(map[string]filehistory.TurnRecord)
		for _, turn := range journal.Snapshot().Turns {
			if turn.RestoreBoundaryKnown && turn.RestoreEligible {
				c.hadRestoreAuthority = true
			}
			c.turns[turn.TurnID] = turn
		}
	}
	return c.journal
}

// lockWorkspaceRevisions binds the owner while respecting session activation's
// e.mu -> controller order. Never take e.mu while holding the returned lock.
func (e *QueryEngine) lockWorkspaceRevisions() (workspaceRevisionIdentity, *workspaceRevisionController) {
	e.mu.Lock()
	identity := e.workspaceRevisionIdentityLocked()
	c := &e.workspaceRevisions
	c.mu.Lock()
	e.mu.Unlock()
	return identity, c
}

func (e *QueryEngine) WorkspaceHistoryIdentity() (string, string, uint64) {
	identity, c := e.lockWorkspaceRevisions()
	defer c.mu.Unlock()
	return identity.sessionID, identity.threadID, c.generation
}

func (e *QueryEngine) WorkspaceChanges() WorkspaceChangeSnapshot {
	if e == nil {
		return WorkspaceChangeSnapshot{Warning: "Workspace history requires an engine."}
	}
	identity, c := e.lockWorkspaceRevisions()
	defer c.mu.Unlock()
	snapshot := WorkspaceChangeSnapshot{SessionID: identity.sessionID, ThreadID: identity.threadID, OwnerGeneration: c.generation}
	if !filepath.IsAbs(identity.cwd) {
		snapshot.Warning = "Workspace history requires an absolute working directory."
		return snapshot
	}
	if identity.child || identity.closed {
		snapshot.Warning = "Workspace history uses the open leader session."
		return snapshot
	}
	if journal := c.journalLocked(identity); journal != nil {
		captured := journal.Snapshot()
		snapshot.Records, snapshot.Revision, snapshot.Warning = captured.Records, captured.Revision, captured.Warning
		snapshot.Turns = captured.Turns
		if op, found, err := journal.LoadRestoreOperation(); err != nil {
			snapshot.Warning = "Restore recovery state is unavailable; existing files were preserved."
		} else if found {
			status := workspaceRestoreStatus(op)
			snapshot.Restore = &status
		}
		for index, turn := range snapshot.Turns {
			if live, ok := c.turns[turn.TurnID]; ok && turn.FinishedAt.IsZero() {
				snapshot.Turns[index].ModelRequested = live.ModelRequested
				snapshot.Turns[index].RestoreBoundaryKnown = live.RestoreBoundaryKnown
				snapshot.Turns[index].RestoreEligible = live.RestoreEligible
				snapshot.Turns[index].RestoreReason = live.RestoreReason
			}
		}
	}
	if c.warning != "" {
		snapshot.Warning = c.warning
	}
	return snapshot
}

func (e *QueryEngine) WorkspaceChange(id string) (filehistory.RevisionRecord, bool) {
	if e == nil {
		return filehistory.RevisionRecord{}, false
	}
	identity, c := e.lockWorkspaceRevisions()
	defer c.mu.Unlock()
	journal := c.journalLocked(identity)
	if journal == nil {
		return filehistory.RevisionRecord{}, false
	}
	return journal.Read(id)
}

// Capturing runs only after actual permission/hook/input admission. It never
// changes a tool result or blocks ordinary execution when history is unavailable.
func (e *QueryEngine) workspaceRecorderForTurn(turnID string) func(string, string, map[string]any, *PermissionActionDescriptor) func(string) {
	e.mu.Lock()
	identity := e.workspaceRevisionIdentityLocked()
	c := &e.workspaceRevisions
	c.mu.Lock()
	generation := c.generation
	c.mu.Unlock()
	e.mu.Unlock()
	if identity.child || identity.closed {
		return nil
	}
	return func(name, toolUseID string, input map[string]any, action *PermissionActionDescriptor) func(string) {
		if name != "Write" && name != "Edit" && supportedWorkspaceRead(action) {
			return nil
		}

		c.mu.Lock()
		if c.generation != generation {
			c.mu.Unlock()
			return nil
		}
		journal := c.journalLocked(identity)
		if journal == nil {
			c.mu.Unlock()
			return nil
		}
		if name != "Write" && name != "Edit" {
			c.markUnobservedWriter(turnID, "unsupported_tool_effects")
			if err := journal.RecordUncovered(turnID, toolUseID, name, "Non-file tool effects are not captured."); err != nil {
				c.warning = "Workspace history is incomplete; some tool effects were not recorded."
			}
			c.mu.Unlock()
			return nil
		}
		path, _ := input["file_path"].(string)
		if !supportedWorkspaceWrite(action, path) {
			c.markUnobservedWriter(turnID, "unsupported_file_dispatch")
		}
		rel, ok := workspaceRevisionRelativePath(identity.cwd, path)
		var id string
		var err error
		if ok {
			id, err = journal.Begin(turnID, toolUseID, name, rel)
		}
		if !ok || err != nil {
			c.markUnobservedWriter(turnID, "file_capture_unavailable")
			c.warning = "Workspace history is incomplete; a file snapshot could not be captured."
			_ = journal.RecordUncovered(turnID, toolUseID, name, "File snapshot unavailable.")
			c.mu.Unlock()
			return nil
		}
		c.mu.Unlock()
		return func(outcome string) {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.closed || c.generation != generation || c.journal != journal {
				return
			}
			if err := journal.Finish(id, outcome); err != nil {
				c.markUnobservedWriter(turnID, "file_capture_unavailable")
				c.warning = "Workspace history is incomplete; a file result could not be captured."
			}
		}
	}
}

func workspaceRevisionRelativePath(cwd, path string) (string, bool) {
	if path == "" {
		return "", false
	}
	if !filepath.IsAbs(path) {
		return path, true
	}
	roots := []string{cwd}
	if canonical, err := filepath.EvalSymlinks(cwd); err == nil {
		roots = append(roots, canonical)
	}
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return rel, true
		}
	}
	return "", false
}

func (e *QueryEngine) closeWorkspaceRevisions() {
	c := &e.workspaceRevisions
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.restoreCancel != nil {
		c.restoreCancel()
		c.restoreCancel = nil
	}
	c.restorePreview = nil
	c.closed = true
	c.generation++
	c.restorePreview = nil
	c.restoreReadGuards = nil
	c.hadRestoreAuthority = false
	c.restoreNoticePaths = nil
	c.lastRestoreGuardID = ""
	if c.journal != nil {
		_ = c.journal.Close()
		c.journal = nil
	}
}

func (e *QueryEngine) resetWorkspaceRevisions() {
	c := &e.workspaceRevisions
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	c.restorePreview = nil
	c.restoreReadGuards = nil
	c.hadRestoreAuthority = false
	c.restoreNoticePaths = nil
	c.lastRestoreGuardID = ""
	if c.journal != nil {
		_ = c.journal.Close()
		c.journal = nil
	}
	c.warning = ""
	c.unavailable = false
	c.turns = nil
}
