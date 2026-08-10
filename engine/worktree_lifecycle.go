package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/abietic/yhc/engine/worktree"
	"github.com/abietic/yhc/tools"
)

// WorktreeLifecycleService returns the engine-owned durable lifecycle service.
// AgentRunner binding remains owned by P18.1.
func (e *QueryEngine) WorktreeLifecycleService() *worktree.Service {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.worktreeLifecycle
}

// WorktreeRecoveryWarnings returns bounded startup discovery diagnostics.
func (e *QueryEngine) WorktreeRecoveryWarnings() []string {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.worktreeRecoveryWarnings...)
}

func (e *QueryEngine) newWorktreeLifecycleService(
	projectRoot string,
) *worktree.Service {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	clock := e.config.Clock
	e.mu.Unlock()
	return worktree.NewService(worktree.ServiceConfig{
		ProjectRoot: projectRoot,
		Clock:       clock,
		TransitionSink: func(
			ctx context.Context,
			transition worktree.Transition,
		) error {
			return e.reduceWorktreeLifecycleTransition(ctx, transition)
		},
	})
}

func (e *QueryEngine) rebindWorktreeLifecycle(projectRoot string) {
	if e == nil {
		return
	}
	service := e.newWorktreeLifecycleService(projectRoot)
	e.mu.Lock()
	e.worktreeLifecycle = service
	subagentExecutor := e.subagentExecutor
	runner := e.agentRunner
	executorInstalled := e.subagentExecutorInstalled
	sessionID := e.config.SessionID
	e.mu.Unlock()
	if subagentExecutor == nil {
		return
	}
	if executorInstalled && runner != nil {
		runner.SetExecutor(nil)
	}
	subagentExecutor.bindAgentWorktreeRuntime(
		service,
		projectRoot,
		sessionID,
	)
	if executorInstalled && runner != nil {
		runner.SetExecutor(subagentExecutor)
	}
}

func (e *QueryEngine) restoreWorktreeRecoveryMetadata(
	ctx context.Context,
) []string {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	service := e.worktreeLifecycle
	runtimeState := e.runtimeState
	e.mu.Unlock()
	if service == nil || runtimeState == nil {
		return nil
	}
	discovery, err := service.Discover(ctx)
	if err != nil {
		return []string{"worktree recovery discovery is unavailable: " + err.Error()}
	}
	warnings := make([]string, 0, len(discovery.Diagnostics))
	for _, diagnostic := range discovery.Diagnostics {
		recordID := strings.TrimSpace(diagnostic.RecordID)
		if recordID == "" {
			recordID = "unknown"
		}
		warnings = append(
			warnings,
			fmt.Sprintf(
				"worktree record %s was rejected during recovery: %s",
				recordID,
				diagnostic.Message,
			),
		)
	}
	if err := runtimeState.RestoreWorktreeSnapshots(
		discovery.Records,
	); err != nil {
		warnings = append(
			warnings,
			"worktree runtime projection is unavailable: "+err.Error(),
		)
	}
	return warnings
}

// RetryAgentWorktreeCleanup resolves immutable ownership from the durable Agent
// snapshot instead of accepting caller-supplied owner fields. A fork has a new
// session identity and therefore cannot clean the source Agent's worktree.
func (e *QueryEngine) RetryAgentWorktreeCleanup(
	ctx context.Context,
	agentID string,
) (worktree.Record, error) {
	if e == nil {
		return worktree.Record{}, errors.New(
			"engine: worktree cleanup service is unavailable",
		)
	}
	e.mu.Lock()
	runner := e.agentRunner
	service := e.worktreeLifecycle
	sessionID := e.config.SessionID
	e.mu.Unlock()
	if runner == nil || service == nil {
		return worktree.Record{}, errors.New(
			"engine: worktree cleanup service is unavailable",
		)
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return worktree.Record{}, errors.New(
			"engine: Agent ID is required for worktree cleanup",
		)
	}
	running, found := runner.GetAgentSnapshot(agentID)
	if !found {
		var err error
		running, err = runner.LoadPersistedAgentSnapshot(agentID)
		if err != nil {
			return worktree.Record{}, fmt.Errorf(
				"engine: load Agent %q cleanup metadata: %w",
				agentID,
				err,
			)
		}
	}
	if strings.TrimSpace(running.ParentSessionID) == "" ||
		running.ParentSessionID != sessionID {
		return worktree.Record{}, fmt.Errorf(
			"engine: Agent %q worktree belongs to session %q, not %q",
			agentID,
			running.ParentSessionID,
			sessionID,
		)
	}
	if running.Worktree == nil ||
		strings.TrimSpace(running.Worktree.RecordID) == "" {
		return worktree.Record{}, fmt.Errorf(
			"engine: Agent %q has no durable worktree record",
			agentID,
		)
	}
	owner := worktreeOwnerFromRunningAgent(running)
	record, err := service.RetryCleanup(
		ctx,
		running.Worktree.RecordID,
		owner,
	)
	if err != nil {
		return record, fmt.Errorf(
			"engine: retry Agent %q worktree cleanup: %w",
			agentID,
			err,
		)
	}
	return record, nil
}

func worktreeOwnerFromRunningAgent(
	running tools.RunningAgent,
) worktree.Owner {
	return worktree.Owner{
		Kind:            worktree.OwnerAgent,
		ID:              running.ID,
		SessionID:       running.SessionID,
		ThreadID:        running.ThreadID,
		ParentSessionID: running.ParentSessionID,
		ParentThreadID:  running.ParentThreadID,
		ParentAgentID:   running.ParentAgentID,
		ParentToolUseID: running.ToolUseID,
	}
}

func (e *QueryEngine) reduceWorktreeLifecycleTransition(
	_ context.Context,
	transition worktree.Transition,
) error {
	if e == nil {
		return fmt.Errorf("engine: worktree lifecycle service has no engine owner")
	}
	record := transition.Record
	identity, clock := e.runtimeIdentitySnapshot()
	parentIdentity := identity
	identity.SessionID = firstNonEmptyString(record.Owner.SessionID, identity.SessionID)
	identity.ThreadID = firstNonEmptyString(record.Owner.ThreadID, identity.ThreadID)
	identity.AgentID = record.Owner.ID
	identity.ParentSessionID = firstNonEmptyString(
		record.Owner.ParentSessionID,
		parentIdentity.SessionID,
	)
	identity.ParentThreadID = firstNonEmptyString(
		record.Owner.ParentThreadID,
		parentIdentity.ThreadID,
	)
	identity.ParentAgentID = record.Owner.ParentAgentID
	identity.ParentToolUseID = record.Owner.ParentToolUseID
	event := QueryEvent{
		Type: EventWorktreeLifecycle,
		WorktreeLifecycle: &WorktreeLifecycleEvent{
			RecordID:           record.ID,
			OwnerKind:          record.Owner.Kind,
			OwnerID:            record.Owner.ID,
			FromState:          transition.From,
			State:              record.State,
			RecordRevision:     record.Revision,
			RepositoryIdentity: record.RepositoryIdentity,
			RepoRoot:           record.RepoRoot,
			Path:               record.Path,
			Branch:             record.Branch,
			BaseCommit:         record.BaseCommit,
			LastErrorCategory:  record.LastErrorCategory,
			LastError:          record.LastError,
		},
	}
	event.CausationID = record.ID
	_, accepted := e.decorateRuntimeEventWithIdentity(
		fmt.Sprintf("worktree-%s-%d", record.ID, record.Revision),
		identity,
		clock,
		event,
	)
	if !accepted {
		return fmt.Errorf("engine: worktree lifecycle reducer rejected record %q", record.ID)
	}
	return nil
}
