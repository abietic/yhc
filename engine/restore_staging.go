package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/internal/workboard"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/tools"
)

// ErrRestoreStagingTransition reports a lifecycle operation that would weaken
// ordinary persistence or reuse an already aborted staging owner.
var ErrRestoreStagingTransition = errors.New("restore staging transition rejected")

type restoreStagingState uint8

const (
	restoreStagingStateStaged restoreStagingState = iota
	restoreStagingStateCommitting
	restoreStagingStateCommitted
	restoreStagingStateAborted
)

type restoreStagingLifecycle struct {
	mu               sync.RWMutex
	state            restoreStagingState
	closeDecisionSet bool
	closePersistence bool
}

type restoreStagingActivation struct {
	result                   *session.ResumedSession
	sessionID                string
	threadID                 string
	metadata                 session.SessionMetadata
	restoreContext           resumedExecutionContext
	restoredShellHooks       *hooks.ShellHookConfig
	restoredGraphInterrupt   *projectGraphHITLRequest
	restoredPlanState        PlanState
	restoredPermissionMode   permission.Mode
	preserveLivePlanApproval bool
	restoredInputCoordinator *RuntimeInputCoordinator
	initializeAgentMemory    bool
	cancelPlanApprovals      bool
	persistGoalCheckpoint    bool
	preparedMCP              *tools.MCPToolManager
	logicalWorkAdapter       *workboard.LogicalWorkAdapter
}

func newRestoreStagingLifecycle(enabled bool) *restoreStagingLifecycle {
	if !enabled {
		return nil
	}
	return &restoreStagingLifecycle{state: restoreStagingStateStaged}
}

func cloneRestoreSessionMetadata(metadata session.SessionMetadata) session.SessionMetadata {
	cloned := metadata
	cloned.AdditionalDirs = append([]string(nil), metadata.AdditionalDirs...)
	cloned.AgentIDs = append([]string(nil), metadata.AgentIDs...)
	cloned.PendingRequestIDs = append([]string(nil), metadata.PendingRequestIDs...)
	cloned.GoalState = clonePersistedGoalState(metadata.GoalState)
	cloned.GoalBinding = clonePersistedGoalBinding(metadata.GoalBinding)
	cloned.ModelBinding = metadata.ModelBinding.Clone()
	return cloned
}

func (e *QueryEngine) beginRestoreLifecycle() (bool, func(), error) {
	if e == nil || e.restoreStaging == nil {
		return false, func() {}, nil
	}
	lifecycle := e.restoreStaging
	lifecycle.mu.RLock()
	switch lifecycle.state {
	case restoreStagingStateStaged:
		e.mu.Lock()
		alreadyRestored := e.pendingRestoreActivation != nil
		e.mu.Unlock()
		if alreadyRestored {
			lifecycle.mu.RUnlock()
			return false, func() {}, fmt.Errorf(
				"%w: staging owner already restored a session",
				ErrRestoreStagingTransition,
			)
		}
		return true, lifecycle.mu.RUnlock, nil
	case restoreStagingStateCommitted:
		return false, lifecycle.mu.RUnlock, nil
	case restoreStagingStateCommitting:
		lifecycle.mu.RUnlock()
		return false, func() {}, fmt.Errorf(
			"%w: staging commit is incomplete and must be retried",
			ErrRestoreStagingTransition,
		)
	default:
		lifecycle.mu.RUnlock()
		return false, func() {}, fmt.Errorf(
			"%w: staging owner is aborted",
			ErrRestoreStagingTransition,
		)
	}
}

func (e *QueryEngine) closePersistenceForLifecycle() bool {
	if e == nil || e.restoreStaging == nil {
		return true
	}
	lifecycle := e.restoreStaging
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if !lifecycle.closeDecisionSet {
		lifecycle.closeDecisionSet = true
		switch lifecycle.state {
		case restoreStagingStateStaged:
			lifecycle.state = restoreStagingStateAborted
			lifecycle.closePersistence = false
		case restoreStagingStateCommitting:
			// A commit attempt may have durably advanced one of several
			// monotonic recovery owners. Close performs no additional write;
			// the next process reconciles the remaining owner.
			lifecycle.state = restoreStagingStateAborted
			lifecycle.closePersistence = false
		case restoreStagingStateAborted:
			lifecycle.closePersistence = false
		case restoreStagingStateCommitted:
			lifecycle.closePersistence = true
		}
	}
	return lifecycle.closePersistence
}

// AbortRestoreStaging releases a pre-commit staging owner without invoking
// checkpoint persistence or transcript sync. Repeated aborts are harmless.
func (e *QueryEngine) AbortRestoreStaging() error {
	if e == nil || e.restoreStaging == nil {
		return fmt.Errorf(
			"%w: ordinary engine cannot abort persistence",
			ErrRestoreStagingTransition,
		)
	}
	lifecycle := e.restoreStaging
	lifecycle.mu.Lock()
	switch lifecycle.state {
	case restoreStagingStateCommitted:
		lifecycle.mu.Unlock()
		return fmt.Errorf(
			"%w: committed engine cannot abort persistence",
			ErrRestoreStagingTransition,
		)
	case restoreStagingStateAborted:
		lifecycle.mu.Unlock()
		e.closeWithPersistence(false)
		return nil
	case restoreStagingStateCommitting:
		lifecycle.mu.Unlock()
		return fmt.Errorf(
			"%w: staging commit has started; retry commit or close",
			ErrRestoreStagingTransition,
		)
	case restoreStagingStateStaged:
		lifecycle.state = restoreStagingStateAborted
		if !lifecycle.closeDecisionSet {
			lifecycle.closeDecisionSet = true
			lifecycle.closePersistence = false
		}
		e.mu.Lock()
		e.pendingRestoreActivation = nil
		e.mu.Unlock()
		lifecycle.mu.Unlock()
		e.closeWithPersistence(false)
		return nil
	default:
		lifecycle.mu.Unlock()
		return fmt.Errorf(
			"%w: unknown staging state",
			ErrRestoreStagingTransition,
		)
	}
}

// CommitRestoreStaging durably commits every prepared recovery owner, activates
// the already-restored runtime, and restores the ordinary QueryEngine.Close
// persistence contract. Once a prepared commit starts, failures are retry-only
// because separate monotonic durable owners cannot be truthfully rolled back
// as one write-free operation.
func (e *QueryEngine) CommitRestoreStaging() error {
	if e == nil || e.restoreStaging == nil {
		return fmt.Errorf(
			"%w: ordinary engine has no staging lifecycle",
			ErrRestoreStagingTransition,
		)
	}
	lifecycle := e.restoreStaging
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	switch lifecycle.state {
	case restoreStagingStateCommitted:
		return nil
	case restoreStagingStateAborted:
		return fmt.Errorf(
			"%w: aborted staging owner cannot commit",
			ErrRestoreStagingTransition,
		)
	case restoreStagingStateStaged:
	case restoreStagingStateCommitting:
		// Retry the idempotent owner commits below.
	default:
		return fmt.Errorf(
			"%w: unknown staging state",
			ErrRestoreStagingTransition,
		)
	}
	if lifecycle.closeDecisionSet {
		return fmt.Errorf(
			"%w: staging owner is closing",
			ErrRestoreStagingTransition,
		)
	}
	e.mu.Lock()
	activation := e.pendingRestoreActivation
	e.mu.Unlock()
	if activation == nil {
		return fmt.Errorf(
			"%w: no restored session is ready to commit",
			ErrRestoreStagingTransition,
		)
	}
	if lifecycle.state == restoreStagingStateStaged {
		// Commit spans the transcript checkpoint and runtime-input recovery
		// ledger. They are separate monotonic durable owners, so a failed
		// attempt is retry-only rather than falsely abortable as write-free.
		lifecycle.state = restoreStagingStateCommitting
	}
	if err := activation.restoredInputCoordinator.commitDeferredRecovery(); err != nil {
		return fmt.Errorf(
			"%w: commit runtime input recovery: %w",
			ErrRestoreStagingTransition,
			err,
		)
	}
	if activation.persistGoalCheckpoint {
		if err := e.persistSessionCheckpoint(""); err != nil {
			return fmt.Errorf(
				"%w: persist normalized Goal checkpoint: %w",
				ErrRestoreStagingTransition,
				err,
			)
		}
	}
	continuationWarnings, err := e.reconcileRestoredGoalContinuation(
		activation.restoredInputCoordinator,
	)
	if err != nil {
		return fmt.Errorf(
			"%w: reconcile Goal continuation: %w",
			ErrRestoreStagingTransition,
			err,
		)
	}
	if activation.result != nil {
		activation.result.Warnings = append(
			activation.result.Warnings,
			continuationWarnings...,
		)
	}
	if err := e.commitPreparedLogicalWork(activation.logicalWorkAdapter); err != nil {
		return fmt.Errorf(
			"%w: commit WorkBoard activation: %w",
			ErrRestoreStagingTransition,
			err,
		)
	}

	e.planMu.Lock()
	e.activateResumedRuntime(context.Background(), activation)
	e.planMu.Unlock()
	e.mu.Lock()
	e.pendingRestoreActivation = nil
	e.mu.Unlock()
	lifecycle.state = restoreStagingStateCommitted
	return nil
}

func (e *QueryEngine) commitPreparedLogicalWork(
	prepared *workboard.LogicalWorkAdapter,
) error {
	if prepared == nil || e.administrationOnly {
		return nil
	}
	e.mu.Lock()
	current := e.logicalWorkAdapter
	e.mu.Unlock()
	switch current {
	case nil:
		if err := e.taskManager.BindAuthority(func(
			tools.TaskManagerSnapshot,
		) (tools.TaskAuthority, error) {
			return prepared, nil
		}); err != nil {
			return err
		}
		e.mu.Lock()
		e.logicalWorkAdapter = prepared
		e.logicalWorkErr = nil
		e.mu.Unlock()
	default:
		release, err := current.BeginActivation(prepared)
		if err != nil {
			return err
		}
		release()
		e.mu.Lock()
		e.logicalWorkErr = nil
		e.mu.Unlock()
	}
	if e.subagentExecutor != nil {
		e.subagentExecutor.TaskManager = e.taskManager
		e.subagentExecutor.logicalWorkAdapter = current
		if current == nil {
			e.subagentExecutor.logicalWorkAdapter = prepared
		}
	}
	return nil
}

func (e *QueryEngine) ensureRestoreStagingCommitted() error {
	if e == nil || e.restoreStaging == nil {
		return nil
	}
	lifecycle := e.restoreStaging
	lifecycle.mu.RLock()
	defer lifecycle.mu.RUnlock()
	switch lifecycle.state {
	case restoreStagingStateCommitted:
		return nil
	case restoreStagingStateStaged:
		return fmt.Errorf(
			"%w: staging owner is not committed",
			ErrRestoreStagingTransition,
		)
	case restoreStagingStateCommitting:
		return fmt.Errorf(
			"%w: staging commit is incomplete and must be retried",
			ErrRestoreStagingTransition,
		)
	default:
		return fmt.Errorf(
			"%w: staging owner is aborted",
			ErrRestoreStagingTransition,
		)
	}
}

func (e *QueryEngine) activateResumedRuntime(
	ctx context.Context,
	activation *restoreStagingActivation,
) {
	if e == nil || activation == nil {
		return
	}
	result := activation.result
	appendWarning := func(warning string) {
		if result != nil && warning != "" {
			result.Warnings = append(result.Warnings, warning)
		}
	}
	if activation.cancelPlanApprovals {
		if cancelled := e.permissionCoordinator.cancelPlanApprovals(
			e.permissionEngineID,
			activation.sessionID,
			activation.threadID,
		); cancelled > 0 {
			appendWarning(fmt.Sprintf(
				"cancelled %d live Plan approval(s) that could not survive session restore",
				cancelled,
			))
		}
	}
	e.mu.Lock()
	e.inputCoordinator = activation.restoredInputCoordinator
	e.inputCoordinatorErr = nil
	e.mu.Unlock()
	e.rebindPermissionRuntime(
		activation.restoreContext.cwd,
		activation.sessionID,
	)
	if !activation.preserveLivePlanApproval {
		if err := e.runtimeState.RestorePlanSnapshot(
			activation.sessionID,
			activation.threadID,
			activation.metadata.AgentID,
			activation.restoredPlanState,
			string(activation.restoredPermissionMode),
			activation.metadata.LastActiveAt,
		); err != nil {
			appendWarning(
				"could not rebuild the runtime Plan projection: " + err.Error(),
			)
		}
	}
	e.goalMu.Lock()
	restoredGoal := cloneGoalState(e.goalState)
	e.goalMu.Unlock()
	if err := e.runtimeState.RestoreGoalSnapshot(
		activation.sessionID,
		activation.threadID,
		activation.metadata.AgentID,
		restoredGoal,
		activation.metadata.LastActiveAt,
	); err != nil {
		appendWarning(
			"could not rebuild the runtime Goal projection: " + err.Error(),
		)
	}
	if e.administrationOnly {
		return
	}
	e.rebindLongSessionServices()
	if activation.restoredGraphInterrupt != nil {
		e.reprojectProjectGraphInterrupt(*activation.restoredGraphInterrupt)
	}
	e.reloadResumedExecutionContext(
		ctx,
		activation.restoreContext.cwd,
		activation.restoredShellHooks,
		activation.preparedMCP,
	)
	if activation.initializeAgentMemory && e.subagentExecutor != nil {
		_ = e.subagentExecutor.InitializeAgentMemorySnapshots()
	}
	e.rebindWorktreeLifecycle(activation.restoreContext.cwd)
	worktreeWarnings := e.restoreWorktreeRecoveryMetadata(ctx)
	e.mu.Lock()
	e.worktreeRecoveryWarnings = append(
		[]string(nil),
		worktreeWarnings...,
	)
	e.mu.Unlock()
	if result != nil {
		result.Warnings = append(result.Warnings, worktreeWarnings...)
	}
	restoredAgents, agentWarnings := e.restoreSessionAgents(
		activation.sessionID,
		activation.threadID,
		activation.metadata,
	)
	if result != nil {
		result.RestoredAgents = restoredAgents
		result.Warnings = append(result.Warnings, agentWarnings...)
	}
}
