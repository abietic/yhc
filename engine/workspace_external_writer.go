package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/abietic/yhc/engine/containment"
	"github.com/abietic/yhc/tools"
)

// BeginExternalWorkspaceWrite covers the TUI's direct shell execution, which
// intentionally does not traverse model tool dispatch. The durable gap is
// recorded before launch; a live writer excludes file recovery and session swap.
func (e *QueryEngine) BeginExternalWorkspaceWrite() (func(), error) {
	if e == nil {
		return nil, fmt.Errorf("terminal execution requires an engine")
	}
	e.planMu.Lock()
	defer e.planMu.Unlock()
	if strings.HasPrefix(e.planActiveTurnID, "workspace-restore-") || strings.HasPrefix(e.planActiveTurnID, "restore-preview-") {
		return nil, fmt.Errorf("wait for workspace recovery before terminal execution")
	}
	id, err := newWorkspaceRestoreID()
	if err != nil {
		return nil, err
	}
	identity, c := e.lockWorkspaceRevisions()
	if identity.child || !filepath.IsAbs(identity.cwd) {
		c.mu.Unlock()
		return nil, fmt.Errorf("terminal execution requires an absolute leader workspace")
	}
	if c.closed || identity.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("engine is closed")
	}
	if c.restoreCancel != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("wait for workspace restore before running a shell command")
	}
	journal := c.journalLocked(identity)
	if journal == nil && c.unavailable {
		c.mu.Unlock()
		return nil, fmt.Errorf("workspace history is unavailable; open a new session before terminal execution")
	}
	if journal != nil {
		operation, found, loadErr := journal.LoadRestoreOperation()
		if loadErr != nil || found && (operation.State == "applying" || operation.State == "partial") {
			c.mu.Unlock()
			return nil, fmt.Errorf("resolve interrupted workspace restore in /changes first")
		}
		if err := journal.RecordUncovered("shell-"+id, id, "Bash", "Direct terminal effects are not captured."); err != nil {
			c.warning = "Workspace history is incomplete; terminal execution was not started."
			c.mu.Unlock()
			return nil, fmt.Errorf("cannot persist the terminal capture boundary; terminal execution was not started")
		}
	}
	for turnID, turn := range c.turns {
		if turn.FinishedAt.IsZero() {
			c.markUnobservedWriter(turnID, "external_shell_unobserved")
		}
	}
	c.restorePreview = nil
	c.externalWriters++
	generation := c.generation
	c.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.generation == generation && c.externalWriters > 0 {
				c.externalWriters--
			}
		})
	}, nil
}

func (e *QueryEngine) hasExternalWorkspaceWriters() bool {
	c := &e.workspaceRevisions
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.externalWriters > 0
}

// ExternalWorkspaceShellContext binds an already reserved explicit terminal
// operation to this engine's working directory, containment and shell owner.
// The process-global compatibility shell is never a recovery-owned writer.
func (e *QueryEngine) ExternalWorkspaceShellContext(ctx context.Context) (context.Context, error) {
	if e == nil {
		return ctx, fmt.Errorf("terminal execution requires an engine")
	}
	e.mu.Lock()
	identity := e.workspaceRevisionIdentityLocked()
	manager, policy := e.shellManager, e.executionPolicy
	c := &e.workspaceRevisions
	c.mu.Lock()
	e.mu.Unlock()
	valid := !identity.closed && !identity.child && !c.closed && c.externalWriters > 0 && manager != nil
	c.mu.Unlock()
	if !valid {
		return ctx, fmt.Errorf("terminal writer lease is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = tools.WithExecutionCWD(ctx, identity.cwd)
	ctx = tools.WithSessionID(ctx, identity.sessionID)
	ctx = tools.WithThreadID(ctx, identity.threadID)
	ctx = tools.WithShellManager(ctx, manager)
	return containment.WithSnapshot(ctx, policy), nil
}
