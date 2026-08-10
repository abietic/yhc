package engine

import (
	"context"
	"fmt"

	"github.com/abietic/yhc/tools"
)

// PrepareRestoreSessionMCP binds one already-restored staging engine to a
// combined project/client MCP generation before any ACP replay or command
// delivery. Commit adopts this exact manager; abort closes it without
// persistence or stale registry rows.
func (e *QueryEngine) PrepareRestoreSessionMCP(
	ctx context.Context,
	servers []tools.SessionMCPServer,
) error {
	if e == nil || e.restoreStaging == nil {
		return fmt.Errorf(
			"%w: MCP setup requires a restore staging owner",
			ErrRestoreStagingTransition,
		)
	}
	if len(servers) == 0 {
		return nil
	}

	lifecycle := e.restoreStaging
	lifecycle.mu.RLock()
	defer lifecycle.mu.RUnlock()
	if lifecycle.state != restoreStagingStateStaged ||
		lifecycle.closeDecisionSet {
		return fmt.Errorf(
			"%w: restore staging owner is not available",
			ErrRestoreStagingTransition,
		)
	}

	e.mu.Lock()
	activation := e.pendingRestoreActivation
	if activation == nil {
		e.mu.Unlock()
		return fmt.Errorf(
			"%w: no restored session is ready for MCP setup",
			ErrRestoreStagingTransition,
		)
	}
	if activation.preparedMCP != nil {
		e.mu.Unlock()
		return fmt.Errorf(
			"%w: restored session MCP setup is already prepared",
			ErrRestoreStagingTransition,
		)
	}
	cwd := activation.restoreContext.cwd
	registry := e.toolRegistry
	e.mu.Unlock()

	manager, err := tools.PrepareSessionMCPManagerWithBinding(
		ctx,
		cwd,
		registry,
		servers,
		e.executionBindings.StdioMCP(),
	)
	if err != nil {
		return err
	}

	e.mu.Lock()
	if e.pendingRestoreActivation != activation ||
		activation.preparedMCP != nil {
		e.mu.Unlock()
		_ = manager.DisconnectAll()
		return fmt.Errorf(
			"%w: restored session changed during MCP setup",
			ErrRestoreStagingTransition,
		)
	}
	oldManager := e.mcpManager
	e.mcpManager = manager
	e.ownsMCPManager = true
	activation.preparedMCP = manager
	if e.subagentExecutor != nil {
		e.subagentExecutor.MCPManager = manager
	}
	e.mu.Unlock()

	if oldManager != nil && oldManager != manager {
		_ = oldManager.DisconnectAll()
	}
	return nil
}
