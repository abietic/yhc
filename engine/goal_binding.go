package engine

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/abietic/yhc/engine/session"
)

func persistedGoalBinding(
	identity *goalExecutionIdentity,
) *session.PersistedGoalBinding {
	if identity == nil ||
		!identity.valid() ||
		strings.TrimSpace(identity.GoalTurnID) == "" {
		return nil
	}
	return &session.PersistedGoalBinding{
		Version:           session.PersistedGoalBindingVersion,
		GoalID:            identity.GoalID,
		ObjectiveRevision: identity.ObjectiveRevision,
		RootSessionID:     identity.RootSessionID,
		RootThreadID:      identity.RootThreadID,
		RootAgentID:       identity.RootAgentID,
		GoalTurnID:        identity.GoalTurnID,
	}
}

func restoreGoalBinding(
	record *session.PersistedGoalBinding,
	agentID string,
) (*goalExecutionIdentity, []string) {
	if record == nil {
		return nil, nil
	}
	if strings.TrimSpace(agentID) == "" {
		return nil, []string{
			"ignored persisted Goal binding on a root Session",
		}
	}
	if record.Version != session.PersistedGoalBindingVersion {
		return nil, []string{fmt.Sprintf(
			"ignored unsupported persisted Goal binding version %d",
			record.Version,
		)}
	}
	identity := &goalExecutionIdentity{
		GoalID:            record.GoalID,
		ObjectiveRevision: record.ObjectiveRevision,
		RootSessionID:     record.RootSessionID,
		RootThreadID:      record.RootThreadID,
		RootAgentID:       record.RootAgentID,
		GoalTurnID:        record.GoalTurnID,
	}
	if !identity.valid() ||
		!validGoalBindingText(identity.GoalID, true) ||
		!validGoalBindingText(identity.RootSessionID, true) ||
		!validGoalBindingText(identity.RootThreadID, true) ||
		!validGoalBindingText(identity.RootAgentID, false) {
		return nil, []string{
			"ignored invalid persisted Goal binding on a child Session",
		}
	}
	if err := validateGoalTurnID(identity.GoalTurnID); err != nil {
		return nil, []string{
			"ignored invalid persisted Goal turn binding on a child Session",
		}
	}
	return identity, nil
}

func validGoalBindingText(value string, required bool) bool {
	if value == "" {
		return !required
	}
	return value == strings.TrimSpace(value) &&
		utf8.ValidString(value) &&
		!strings.ContainsRune(value, '\x00')
}

func clonePersistedGoalBinding(
	record *session.PersistedGoalBinding,
) *session.PersistedGoalBinding {
	if record == nil {
		return nil
	}
	cloned := *record
	return &cloned
}

func samePersistedGoalBinding(
	left *session.PersistedGoalBinding,
	right *session.PersistedGoalBinding,
) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Version == right.Version &&
		left.GoalID == right.GoalID &&
		left.ObjectiveRevision == right.ObjectiveRevision &&
		left.RootSessionID == right.RootSessionID &&
		left.RootThreadID == right.RootThreadID &&
		left.RootAgentID == right.RootAgentID &&
		left.GoalTurnID == right.GoalTurnID
}
