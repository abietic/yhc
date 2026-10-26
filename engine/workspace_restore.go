package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/containment"
	"github.com/abietic/yhc/engine/filehistory"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
)

// WorkspaceRestorePreview is a bounded, inert presentation of an engine-owned
// plan. Editing this value cannot alter the transition retained by the engine.
type WorkspaceRestorePreview struct {
	ID, SessionID, ThreadID, TurnID, Direction string
	OwnerGeneration                            uint64
	ExpiresAt                                  time.Time
	Files                                      []WorkspaceRestoreFile
	Recovery                                   bool
}

type WorkspaceRestoreFile struct {
	Path, Status    string
	Current, Target filehistory.RevisionFileState
}

type WorkspaceRestoreStatus struct {
	ID, State, Direction string
	Paths, Finished      int
}

type workspaceRestorePending struct {
	preview               WorkspaceRestorePreview
	operation             filehistory.RestoreOperation
	policyID, executionID string
}

func newWorkspaceRestoreID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

// workspaceRestoreBoundary is evaluated with the plan turn reserved, while no
// e.mu/controller lock is held. It cannot be replaced by a permission-mode grant.
func (e *QueryEngine) workspaceRestoreBoundary() error {
	e.mu.Lock()
	identity := e.workspaceRevisionIdentityLocked()
	entrypoint, prompt, custom := e.config.CommandEntrypoint, e.config.PermissionPrompt, e.config.CanUseTool != nil
	mode, runner, coordinator := e.config.PermissionMode, e.agentRunner, e.inputCoordinator
	shells := e.shellManager
	hookExecutor, mcpManager := e.config.HookExecutor, e.mcpManager
	policy := e.executionPolicy
	e.mu.Unlock()
	if identity.closed || identity.child || !filepath.IsAbs(identity.cwd) || entrypoint != commands.EntrypointTUI {
		return fmt.Errorf("restore requires the open interactive leader session")
	}
	if !filehistory.RestoreFilesystemSupported() {
		return fmt.Errorf("guarded restore is available on macOS and Linux")
	}
	if prompt == nil || custom {
		return fmt.Errorf("restore requires the native fresh permission prompt")
	}
	if mode == permission.ModeDontAsk || mode == permission.ModePlan || e.PlanState().Phase != PlanPhaseInactive {
		return fmt.Errorf("leave Plan or dontAsk mode before restoring")
	}
	if policy != nil {
		spec := policy.Spec()
		if spec.Profile == containment.ProfileReadOnly || spec.State == containment.StateUnavailable {
			return fmt.Errorf("execution policy does not permit workspace restore")
		}
	}
	if hookExecutor.HasWorkspaceMutationHooks() || mcpManager != nil && len(mcpManager.InventorySnapshot().Servers) > 0 {
		return fmt.Errorf("configured hooks or MCP processes prevent guarded restore")
	}
	if shells != nil && len(shells.ListShells()) > 0 {
		return fmt.Errorf("close existing shell sessions before restoring; earlier unobserved writers require a fresh Session")
	}
	if runner != nil && runner.ActiveCount() > 0 {
		return fmt.Errorf("wait for child agents before restoring")
	}
	if goal, ok := e.GoalSnapshot(); ok && (goal.Status == string(goalStatusActive) || goal.PendingUsageAdmission != nil) {
		return fmt.Errorf("pause the active Goal before restoring")
	}
	if e.RuntimeSnapshot().UnresolvedCount > 0 {
		return fmt.Errorf("resolve pending interactions before restoring")
	}
	if coordinator != nil {
		for _, item := range coordinator.Snapshot(RuntimeInputScope{SessionID: identity.sessionID, ThreadID: identity.threadID}) {
			if item.State == RuntimeItemPending || item.State == RuntimeItemProcessing {
				return fmt.Errorf("withdraw queued work before restoring")
			}
		}
	}
	return nil
}

func (e *QueryEngine) restorePolicyAllows(operation filehistory.RestoreOperation) error {
	rules := e.permissionRulesSnapshot()
	for _, path := range operation.Paths {
		absolute := filepath.Join(operation.Root, path.Path)
		input := map[string]any{"file_path": absolute}
		for _, tool := range []string{"Write", "Edit", "WorkspaceRestore"} {
			if action, _ := rules.EvaluateMatch(tool, input); action == permission.ActionDeny {
				return fmt.Errorf("a permission deny rule blocks the restore")
			}
		}
		if e.executionPolicy != nil {
			spec := e.executionPolicy.Spec()
			if spec.Profile != containment.ProfileDangerFullAccess && !pathWithinAny(absolute, spec.WriteRoots) {
				return fmt.Errorf("restore path is outside execution write roots")
			}
			if pathWithinAny(absolute, spec.DeniedRoots) {
				return fmt.Errorf("restore path is denied by execution policy")
			}
		}
	}
	return nil
}

// PrepareWorkspaceRestore never writes a workspace file or dispatches a model.
// Empty direction selects a fresh turn/file plan; restore/rollback previews an
// interrupted operation and always requires a new confirmation and permission.
func (e *QueryEngine) PrepareWorkspaceRestore(turnID, revisionID, direction string) (WorkspaceRestorePreview, error) {
	if e == nil {
		return WorkspaceRestorePreview{}, fmt.Errorf("restore requires an engine")
	}
	id, err := newWorkspaceRestoreID()
	if err != nil {
		return WorkspaceRestorePreview{}, err
	}
	lease := "restore-preview-" + id
	if _, err := e.beginPlanTurn(lease); err != nil {
		return WorkspaceRestorePreview{}, err
	}
	defer e.endPlanTurn(lease)
	if err := e.workspaceRestoreBoundary(); err != nil {
		return WorkspaceRestorePreview{}, err
	}
	policyID := e.effectivePolicySnapshot(nil).ID()
	executionID := ""
	if e.executionPolicy != nil {
		executionID = e.executionPolicy.Digest()
	}
	identity, c := e.lockWorkspaceRevisions()
	if c.externalWriters > 0 || c.restoreCancel != nil {
		c.mu.Unlock()
		return WorkspaceRestorePreview{}, fmt.Errorf("wait for the workspace writer to finish")
	}
	journal := c.journalLocked(identity)
	if journal == nil {
		c.mu.Unlock()
		return WorkspaceRestorePreview{}, fmt.Errorf("workspace history is unavailable")
	}
	// Publish missing completed receipts only as part of explicit admission,
	// never as a side effect of opening a passive history view.
	if err := journal.ReconcileRestoreReceipts(); err != nil {
		c.mu.Unlock()
		return WorkspaceRestorePreview{}, err
	}
	var operation filehistory.RestoreOperation
	if direction == "" {
		operation, err = journal.PrepareRestoreOperation(turnID, revisionID)
	} else {
		operation, err = journal.PrepareRestoreRecovery(direction)
	}
	if err != nil {
		c.mu.Unlock()
		return WorkspaceRestorePreview{}, err
	}
	preview := WorkspaceRestorePreview{ID: id, SessionID: identity.sessionID, ThreadID: identity.threadID, TurnID: operation.TurnID, Direction: operation.Direction, OwnerGeneration: c.generation, ExpiresAt: time.Now().Add(2 * time.Minute), Recovery: direction != "", Files: make([]WorkspaceRestoreFile, 0, len(operation.Paths))}
	observedStates, stateErr := journal.RestorePreviewStates(operation)
	if stateErr != nil {
		c.mu.Unlock()
		return WorkspaceRestorePreview{}, stateErr
	}
	for index, path := range operation.Paths {
		current, target := observedStates[index], path.Target
		if operation.Direction == "rollback" {
			target = path.Original
		}
		target.Data = append([]byte(nil), target.Data...)
		status := path.Status
		if current.Captured && current.Exists == target.Exists && current.Mode == target.Mode && current.Digest == target.Digest {
			status = "already target"
		}
		preview.Files = append(preview.Files, WorkspaceRestoreFile{Path: path.Path, Status: status, Current: current, Target: target})
	}

	c.restorePreview = &workspaceRestorePending{preview: preview, operation: operation, policyID: policyID, executionID: executionID}
	c.mu.Unlock()
	if err := e.restorePolicyAllows(operation); err != nil {
		e.DiscardWorkspaceRestorePreview(id)
		return WorkspaceRestorePreview{}, err
	}
	return preview, nil
}

// DiscardWorkspaceRestorePreview removes ephemeral authority only. It never
// deletes recovery intent, backups, captured history, or conversation messages.
func (e *QueryEngine) DiscardWorkspaceRestorePreview(id string) {
	if e == nil {
		return
	}
	c := &e.workspaceRevisions
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.restorePreview != nil && c.restorePreview.preview.ID == id {
		c.restorePreview = nil
	}
}

// ConfirmWorkspaceRestore is an explicit user action after reviewing a plan.
// It asks the permission coordinator for an independent fresh allow-once even
// in bypass/acceptEdits/Auto mode; persisted receipts never satisfy that prompt.
func (e *QueryEngine) ConfirmWorkspaceRestore(ctx context.Context, id string, confirmed bool) (WorkspaceRestoreStatus, error) {
	if e == nil || !confirmed {
		return WorkspaceRestoreStatus{}, fmt.Errorf("review and confirm the restore preview first")
	}
	lease := "workspace-restore-" + id
	if _, err := e.beginPlanTurn(lease); err != nil {
		return WorkspaceRestoreStatus{}, err
	}
	defer e.endPlanTurn(lease)
	if err := e.workspaceRestoreBoundary(); err != nil {
		return WorkspaceRestoreStatus{}, err
	}
	policyID := e.effectivePolicySnapshot(nil).ID()
	identity, c := e.lockWorkspaceRevisions()
	pending := c.restorePreview
	if pending == nil || pending.preview.ID != id || pending.preview.SessionID != identity.sessionID || pending.preview.ThreadID != identity.threadID || pending.preview.OwnerGeneration != c.generation || time.Now().After(pending.preview.ExpiresAt) || pending.policyID != policyID || c.externalWriters > 0 || c.restoreCancel != nil {
		c.mu.Unlock()
		return WorkspaceRestoreStatus{}, fmt.Errorf("restore preview expired or its owner/policy changed; prepare again")
	}
	if e.executionPolicy != nil && pending.executionID != e.executionPolicy.Digest() {
		c.mu.Unlock()
		return WorkspaceRestoreStatus{}, fmt.Errorf("execution policy changed")
	}
	journal := c.journalLocked(identity)
	if journal == nil {
		c.mu.Unlock()
		return WorkspaceRestoreStatus{}, fmt.Errorf("workspace history is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	c.restoreCancel = cancel
	c.restorePreview = nil
	operation := pending.operation
	generation := c.generation
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		if c.generation == generation {
			c.restoreCancel = nil
		}
		c.mu.Unlock()
	}()
	if err := e.restorePolicyAllows(operation); err != nil {
		return WorkspaceRestoreStatus{}, err
	}
	requestID := "restore-permission-" + id
	paths := make([]string, 0, len(operation.Paths))
	for _, path := range operation.Paths {
		paths = append(paths, path.Path)
	}
	request := PermissionPromptRequest{Kind: PermissionInteractionKindPermission, Source: "workspace_restore", ToolName: "WorkspaceRestore", CanonicalToolName: "WorkspaceRestore", ToolUseID: requestID, Input: map[string]any{"direction": operation.Direction, "paths": paths}, Message: "Apply only the reviewed captured file transitions. Conversation history stays intact. Re-read restored files before editing.", SessionScope: "this exact restore preview, once", ProjectIdentity: e.permissionProjectIdentity, RootSessionID: e.permissionRootSessionID, SessionID: identity.sessionID, ThreadID: identity.threadID, DecisionConstraint: PermissionAllowOnceOnly}
	result := e.permissionCoordinator.request(ctx, e.permissionEngineID, request, e.config.PermissionPrompt, nil, func(answer PermissionInteractionResult) PermissionInteractionResult {
		if answer.Decision != PermissionAllowOnce || len(answer.UpdatedInput) > 0 || answer.PlanApproval != nil {
			return PermissionInteractionResult{Decision: PermissionDeny, Message: "restore requires a fresh unchanged allow-once decision"}
		}
		return answer
	}, nil)
	if result.Decision != PermissionAllowOnce {
		return WorkspaceRestoreStatus{}, fmt.Errorf("workspace restore was not authorized")
	}
	if err := ctx.Err(); err != nil {
		return WorkspaceRestoreStatus{}, err
	}
	if time.Now().After(pending.preview.ExpiresAt) || e.effectivePolicySnapshot(nil).ID() != policyID {
		return WorkspaceRestoreStatus{}, fmt.Errorf("restore preview or policy changed while awaiting permission")
	}
	if err := e.workspaceRestoreBoundary(); err != nil {
		return WorkspaceRestoreStatus{}, err
	}
	if err := e.restorePolicyAllows(operation); err != nil {
		return WorkspaceRestoreStatus{}, err
	}
	current, c := e.lockWorkspaceRevisions()
	valid := !c.closed && c.generation == generation && current.sessionID == identity.sessionID && current.threadID == identity.threadID && c.externalWriters == 0 && c.journal == journal
	c.mu.Unlock()
	if !valid {
		return WorkspaceRestoreStatus{}, fmt.Errorf("restore owner changed")
	}
	operation.ConfirmationID = id
	operation.PermissionRevision = policyID
	completed, applyErr := journal.ApplyRestoreOperation(ctx, operation)
	stored, found, loadErr := journal.LoadRestoreOperation()
	if loadErr == nil && found && stored.ID == operation.ID {
		completed = stored
	} else if loadErr == nil {
		completed.State = ""
		completed.Paths = nil
	}

	// Clear read authority after any possibly started transition, including a
	// crash-uncertain step. This never clears another engine's compatibility map.
	e.invalidateRestoredWorkspace(identity, generation, completed)
	return workspaceRestoreStatus(completed), applyErr
}

func workspaceRestoreStatus(operation filehistory.RestoreOperation) WorkspaceRestoreStatus {
	status := WorkspaceRestoreStatus{ID: operation.ID, State: operation.State, Direction: operation.Direction, Paths: len(operation.Paths)}
	for _, path := range operation.Paths {
		if path.Status == "applied" || path.Status == "already_applied" || path.Status == "rolled_back" {
			status.Finished++
		}
	}
	return status
}

// CancelWorkspaceRestore cancels the current permission wait or stops between
// file transitions. It does not disguise an interrupted operation as complete.
func (e *QueryEngine) CancelWorkspaceRestore() {
	if e == nil {
		return
	}
	c := &e.workspaceRevisions
	c.mu.Lock()
	cancel := c.restoreCancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (e *QueryEngine) invalidateRestoredWorkspace(identity workspaceRevisionIdentity, generation uint64, op filehistory.RestoreOperation) {
	if op.ID == "" || len(op.Paths) == 0 {
		return
	}
	e.mu.Lock()
	cache := e.fileStateCache
	c := &e.workspaceRevisions
	c.mu.Lock()
	e.mu.Unlock()
	defer c.mu.Unlock()
	if c.generation != generation || c.closed {
		return
	}
	roots := []string{identity.cwd, op.Root}
	if c.restoreReadGuards == nil {
		c.restoreReadGuards = tools.ForkFileReadGuards(context.Background(), roots)
	}
	var paths []string
	for _, path := range op.Paths {
		for _, root := range roots {
			paths = append(paths, filepath.Join(root, path.Path))
		}
	}
	c.restoreReadGuards.Invalidate(paths...)
	c.lastRestoreGuardID = op.ID + ":" + op.Direction + ":" + op.State
	c.restoreNoticePaths = append([]string(nil), paths...)
	if cache != nil {
		cache.mu.Lock()
		for _, path := range paths {
			delete(cache.ReadFiles, path)
			delete(cache.EditFiles, path)
			delete(cache.WriteFiles, path)
		}
		cache.mu.Unlock()
	}
}

func (e *QueryEngine) workspaceRestoreQueryContext(ctx context.Context) (context.Context, string, error) {
	identity, c := e.lockWorkspaceRevisions()
	if c.restoreCancel != nil || c.externalWriters > 0 && c.restorePreview != nil {
		c.mu.Unlock()
		return ctx, "", fmt.Errorf("wait for workspace restore or shell work to finish")
	}
	journal := c.journalLocked(identity)
	if journal == nil {
		if c.unavailable {
			_, statErr := os.Lstat(filepath.Join(identity.dir, identity.sessionID+".workspace-history"))
			if statErr == nil || c.hadRestoreAuthority {
				c.mu.Unlock()
				return ctx, "", fmt.Errorf("existing workspace history cannot be revalidated; reopen it or start a new session before continuing")
			}
		}
		guards := c.restoreReadGuards
		if bound := tools.BoundFileReadGuards(ctx); guards != nil && bound != nil && bound != guards {
			c.mu.Unlock()
			return ctx, "", fmt.Errorf("restored workspace requires the current session read scope; use a fresh session for an external recovery scope")
		}
		c.mu.Unlock()
		return tools.WithDefaultFileReadGuards(ctx, guards), "", nil
	}
	op, found, err := journal.LoadRestoreOperation()
	if err != nil {
		c.mu.Unlock()
		return ctx, "", fmt.Errorf("restore recovery state is unavailable; inspect /changes before continuing")
	}
	if found && (op.State == "applying" || op.State == "partial") {
		c.mu.Unlock()
		return ctx, "", fmt.Errorf("workspace restore is interrupted; use /changes to retry or roll back, or start a new session")
	}
	generation := c.generation
	needsGuard := found && c.lastRestoreGuardID != op.ID+":"+op.Direction+":"+op.State
	c.mu.Unlock()
	if found {
		if err := journal.ReconcileRestoreReceipts(); err != nil {
			return ctx, "", fmt.Errorf("completed restore receipts are incomplete; inspect /changes")
		}
		if needsGuard {
			e.invalidateRestoredWorkspace(identity, generation, op)
		}
	}
	_, c = e.lockWorkspaceRevisions()
	defer c.mu.Unlock()
	guards := c.restoreReadGuards
	if bound := tools.BoundFileReadGuards(ctx); guards != nil && bound != nil && bound != guards {
		return ctx, "", fmt.Errorf("restored workspace requires the current session read scope; use a fresh session for an external recovery scope")
	}
	notice := ""
	if len(c.restoreNoticePaths) > 0 {
		var names []string
		for _, path := range c.restoreNoticePaths {
			if rel, err := filepath.Rel(identity.cwd, path); err == nil {
				names = append(names, fmt.Sprintf("%q", rel))
			}
		}
		notice = "Local workspace recovery changed captured files: " + strings.Join(names, ", ") + ". Conversation messages were retained and may describe older bytes. Re-read each affected file with Read before editing or writing it."
		c.restoreNoticePaths = nil
	}
	return tools.WithDefaultFileReadGuards(ctx, guards), notice, nil
}

// workspaceRestorePendingActionError protects command dispatch as well as the
// ordinary model entrypoint. Prompt expansion and compaction cannot run ahead
// of an interrupted filesystem operation.
func (e *QueryEngine) workspaceRestorePendingActionError() error {
	identity, c := e.lockWorkspaceRevisions()
	defer c.mu.Unlock()
	if c.restoreCancel != nil {
		return fmt.Errorf("workspace restore is in progress")
	}
	journal := c.journalLocked(identity)
	if journal == nil {
		if c.hadRestoreAuthority {
			return fmt.Errorf("workspace recovery history is unavailable")
		}
		if c.unavailable {
			if _, err := os.Lstat(filepath.Join(identity.dir, identity.sessionID+".workspace-history")); err == nil {
				return fmt.Errorf("existing workspace history cannot be revalidated")
			}
		}
		return nil
	}
	operation, found, err := journal.LoadRestoreOperation()
	if err != nil {
		return fmt.Errorf("workspace recovery state cannot be revalidated")
	}
	if found && (operation.State == "applying" || operation.State == "partial") {
		return fmt.Errorf("resolve interrupted workspace restore in /changes before model or workspace commands")
	}
	return nil
}
