package engine

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/filehistory"
	"github.com/abietic/yhc/tools"
)

// workspaceCaptureBoundary runs before user-prompt/tool/stop hooks, not after
// the first rendered event. Legacy history never gains this positive proof.
func (e *QueryEngine) workspaceCaptureBoundary(ctx context.Context, turnID string) error {
	e.mu.Lock()
	identity := e.workspaceRevisionIdentityLocked()
	clock, hookExecutor, runner, manager := e.config.Clock, e.config.HookExecutor, e.agentRunner, e.mcpManager
	shellManager := e.shellManager
	entrypoint, custom := e.config.CommandEntrypoint, e.config.CanUseTool != nil
	c := &e.workspaceRevisions
	c.mu.Lock()
	e.mu.Unlock()
	defer c.mu.Unlock()
	if identity.child || identity.closed {
		return nil
	}
	journal := c.journalLocked(identity)
	if journal == nil {
		return nil
	}
	if _, found := c.turns[turnID]; found {
		return nil
	}
	reason := ""
	switch {
	case entrypoint != commands.EntrypointTUI:
		reason = "non_interactive_capture"
	case custom:
		reason = "custom_permission_callback"
	case hookExecutor.HasWorkspaceMutationHooks():
		reason = "hooks_unobserved"
	case runner != nil && runner.ActiveCount() > 0:
		reason = "child_writers_unobserved"
	case manager != nil && len(manager.InventorySnapshot().Servers) > 0:
		reason = "mcp_writers_unobserved"
	case shellManager != nil && len(shellManager.ListShells()) > 0:
		reason = "shell_lifecycle_unobserved"
	case c.externalWriters > 0:
		reason = "external_writer_active"
	case tools.BoundFileReadGuards(ctx) != nil && tools.BoundFileReadGuards(ctx) != c.restoreReadGuards:
		reason = "external_read_scope"
	}
	if reason == "" {
		for _, record := range journal.Snapshot().Records {
			if record.Path == "" {
				reason = "prior_writer_unobserved"
				break
			}
		}
	}
	at := clock().UTC()
	if err := journal.RecordTurnStartWithRestoreBoundary(turnID, identity.threadID, at, reason == "", reason); err != nil {
		c.warning = "Workspace history is incomplete; the capture boundary was not recorded."
		for _, prior := range journal.Snapshot().Turns {
			if prior.RestoreBoundaryKnown && prior.RestoreEligible {
				return fmt.Errorf("cannot persist a new capture boundary; reopen history or start a new session before continuing")
			}
		}
		return nil
	}
	if reason == "" {
		c.hadRestoreAuthority = true
	}
	c.turns[turnID] = filehistory.TurnRecord{SessionID: identity.sessionID, ThreadID: identity.threadID, TurnID: turnID, StartedAt: at, RestoreBoundaryKnown: true, RestoreEligible: reason == "", RestoreReason: reason, FileBoundarySequence: uint64(len(journal.Snapshot().Records))}
	return nil
}

func (c *workspaceRevisionController) markUnobservedWriter(turnID, reason string) {
	turn, ok := c.turns[turnID]
	if ok && turn.RestoreBoundaryKnown && turn.RestoreEligible {
		turn.RestoreEligible = false
		turn.RestoreReason = reason
		c.turns[turnID] = turn
	}
}

func supportedWorkspaceRead(action *PermissionActionDescriptor) bool {
	return action != nil && action.CapabilitiesDeclared && action.Origin == tools.ToolOriginBuiltin && (action.ReadOnly || action.InternalStateDefaultSafe) && !action.Network && !action.Child && !action.Dynamic
}

func supportedWorkspaceWrite(action *PermissionActionDescriptor, path string) bool {
	return action != nil && action.Registered && action.Enabled && action.SchemaValidated && action.CustomValidationComplete && action.CapabilitiesDeclared && action.Origin == tools.ToolOriginBuiltin && action.ActionKind == tools.ToolActionWrite && !action.Network && !action.Child && !action.Dynamic && action.PathWithinRoots && filepath.IsAbs(path)
}
