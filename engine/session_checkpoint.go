package engine

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/abietic/yhc/engine/auth"
	"github.com/abietic/yhc/engine/compact"
	"github.com/abietic/yhc/engine/session"
	"github.com/cloudwego/eino/schema"
)

const sessionGitBranchTimeout = 500 * time.Millisecond

// persistSessionCheckpoint appends one self-contained execution-context
// checkpoint after the transcript message rewrite. It persists references to
// pending interactions, never their payloads.
func (e *QueryEngine) persistSessionCheckpoint(status string) error {
	return e.persistSessionCheckpointMessages(status, nil)
}

func (e *QueryEngine) persistSessionCheckpointMessages(status string, checkpointMessages []*schema.Message) error {
	if e == nil {
		return nil
	}
	// Plan and Goal transitions use planMu -> goalMu -> mu. Keep checkpoint
	// sampling on the same lock order so their durable records and permission
	// mode are one coherent snapshot.
	e.planMu.Lock()
	defer e.planMu.Unlock()
	e.goalMu.Lock()
	defer e.goalMu.Unlock()
	err := e.persistSessionCheckpointMessagesLocked(
		status,
		checkpointMessages,
		e.goalState,
	)
	if err == nil && checkpointMessages != nil {
		e.clearContextModelDispatchBlock(checkpointMessages)
	}
	return err
}

// persistSessionCheckpointMessagesLocked requires planMu and goalMu. It may
// hold them across transcript flush so a Goal candidate is durably committed
// before the transition service publishes it as live state.
func (e *QueryEngine) persistSessionCheckpointMessagesLocked(
	status string,
	checkpointMessages []*schema.Message,
	goalState *goalState,
) error {
	return e.persistSessionCheckpointMessagesLockedWithModel(
		status,
		checkpointMessages,
		goalState,
		nil,
	)
}

type sessionCheckpointModelOverride struct {
	binding  *session.PersistedModelBinding
	model    string
	provider string
}

// persistModelControlCheckpointLocked commits one model-control candidate.
// The caller holds planMu; the test seam verifies ordering and failure
// classification without weakening the production recorder contract.
func (e *QueryEngine) persistModelControlCheckpointLocked(
	binding *session.PersistedModelBinding,
	model string,
	modelProvider string,
) error {
	if e.modelCheckpointWriter != nil {
		return e.modelCheckpointWriter(
			binding.Clone(),
			model,
			modelProvider,
		)
	}
	e.goalMu.Lock()
	defer e.goalMu.Unlock()
	return e.persistSessionCheckpointMessagesLockedWithModel(
		"",
		nil,
		e.goalState,
		&sessionCheckpointModelOverride{
			binding:  binding,
			model:    model,
			provider: modelProvider,
		},
	)
}

func (e *QueryEngine) persistSessionCheckpointMessagesLockedWithModel(
	status string,
	checkpointMessages []*schema.Message,
	goalState *goalState,
	modelOverride *sessionCheckpointModelOverride,
) error {
	planState := e.planState
	e.mu.Lock()
	if strings.TrimSpace(status) != "" {
		e.sessionStatus = strings.TrimSpace(status)
	}
	recorder := e.transcript
	messages := append([]*schema.Message(nil), checkpointMessages...)
	if checkpointMessages == nil {
		messages = append([]*schema.Message(nil), e.messages...)
	}
	model := e.config.Model
	modelProvider := auth.DetectProvider(model)
	binding := e.modelBinding.Clone()
	if binding != nil && binding.ValidateV1() == nil {
		model = binding.APIModel
		modelProvider = binding.Provider
	}
	if modelOverride != nil {
		binding = modelOverride.binding.Clone()
		model = modelOverride.model
		modelProvider = modelOverride.provider
	}
	meta := session.SessionMetadataFull{
		SessionID:          e.config.SessionID,
		ParentSessionID:    e.config.ParentSessionID,
		ParentThreadID:     e.config.ParentThreadID,
		ParentAgentID:      e.config.ParentAgentID,
		ParentToolUseID:    e.config.ParentToolUseID,
		Model:              model,
		Provider:           modelProvider,
		ThreadID:           e.config.ThreadID,
		AgentID:            e.config.AgentID,
		AgentGeneration:    e.config.AgentGeneration,
		AgentName:          e.config.AgentName,
		AgentRole:          e.config.AgentRole,
		ModelRole:          e.config.ModelRole,
		Status:             e.sessionStatus,
		PermissionMode:     string(e.config.PermissionMode),
		QueryKernelVersion: e.queryKernelSelection.version,
		QueryKernelStage: string(
			e.queryKernelSelection.stage,
		),
		QueryKernelIncompatibility: e.queryKernelSelection.
			incompatibility,
		PlanState:      persistedPlanState(planState),
		GoalState:      persistedGoalState(goalState),
		GoalBinding:    persistedGoalBinding(e.config.goalBinding),
		ModelBinding:   binding,
		WorktreePath:   e.config.WorktreePath,
		WorktreeBranch: e.config.WorktreeBranch,
		AdditionalDirs: append([]string(nil), e.config.AdditionalDirs...),
		CreatedAt:      e.sessionStartedAt,
		UpdatedAt:      e.config.Clock().UTC(),
		MessageCount:   len(messages),
		TokenUsage:     compact.EstimateTokenCount(messages),
		IsLeaf:         true,
		GitBranch:      e.sessionGitBranch,
		CWD:            e.config.CWD,
	}
	runtimeState := e.runtimeState
	graphCheckpoint := e.projectGraphCheckpoint
	kernelSelectionErr := e.queryKernelSelection.err
	e.mu.Unlock()

	if kernelSelectionErr != nil {
		return fmt.Errorf(
			"persist session with invalid query kernel selection: %w",
			kernelSelectionErr,
		)
	}
	if recorder == nil {
		return nil
	}
	if runtimeState != nil {
		snapshot := runtimeState.Snapshot(meta.ThreadID)
		meta.RuntimeRevision = snapshot.Revision
		meta.AgentIDs = relatedSessionAgentIDs(snapshot, meta.SessionID)
		meta.PendingRequestIDs = relatedSessionRequestIDs(snapshot, meta.SessionID, meta.AgentIDs)
	}
	if graphCheckpoint != nil {
		if active, ok := graphCheckpoint.ActiveInterrupt(); ok {
			meta.GraphInterrupt = persistedProjectGraphInterrupt(active)
			meta.PendingRequestIDs = appendUniqueSorted(
				meta.PendingRequestIDs,
				active.RequestID,
			)
		}
	}
	if err := session.WriteSessionMetadata(recorder, &meta); err != nil {
		return err
	}
	return recorder.Flush()
}

func persistedProjectGraphInterrupt(
	request projectGraphHITLRequest,
) *session.PersistedGraphInterrupt {
	return &session.PersistedGraphInterrupt{
		Version:          request.Version,
		RequestID:        request.RequestID,
		InterruptID:      request.InterruptID,
		InvocationDigest: request.InvocationDigest,
		PolicyRevision:   request.PolicyRevision,
		Kind:             request.Kind,
	}
}

func appendUniqueSorted(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	values = append(values, value)
	sort.Strings(values)
	return values
}

func persistedPlanState(state PlanState) *session.PersistedPlanState {
	return &session.PersistedPlanState{
		Version:               session.PersistedPlanStateVersion,
		Phase:                 string(state.Phase),
		PlanFileIdentity:      state.PlanFileIdentity,
		ReturnMode:            string(state.ReturnMode),
		ApprovalRequestID:     state.ApprovalRequestID,
		ApprovalInitialDigest: state.ApprovalInitialDigest,
		Revision:              state.Revision,
	}
}

func relatedSessionAgentIDs(snapshot RuntimeSnapshot, sessionID string) []string {
	selected := make(map[string]struct{})
	changed := true
	for changed {
		changed = false
		for id, agent := range snapshot.Agents {
			_, parentSelected := selected[agent.ParentAgentID]
			if agent.ParentSessionID != sessionID && !parentSelected {
				continue
			}
			if _, exists := selected[id]; !exists {
				selected[id] = struct{}{}
				changed = true
			}
		}
	}
	out := make([]string, 0, len(selected))
	for id := range selected {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func relatedSessionRequestIDs(snapshot RuntimeSnapshot, sessionID string, agentIDs []string) []string {
	agents := make(map[string]struct{}, len(agentIDs))
	for _, id := range agentIDs {
		agents[id] = struct{}{}
	}
	requests := make(map[string]struct{})
	for _, thread := range snapshot.Threads {
		_, relatedAgent := agents[thread.AgentID]
		if thread.SessionID != sessionID && thread.ParentSessionID != sessionID && !relatedAgent {
			continue
		}
		for id := range thread.PendingInteractions {
			requests[id] = struct{}{}
		}
	}
	out := make([]string, 0, len(requests))
	for id := range requests {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func detectSessionGitBranch(cwd string) string {
	if strings.TrimSpace(cwd) == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionGitBranchTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	command.Dir = cwd
	output, err := command.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}
