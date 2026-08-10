package engine

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

func persistedGoalState(state *goalState) *session.PersistedGoalState {
	if state == nil {
		return nil
	}
	if state.unavailable {
		return clonePersistedGoalState(state.unavailablePersistedRepresentation)
	}
	return &session.PersistedGoalState{
		Version:                          session.PersistedGoalStateVersion,
		GoalID:                           state.GoalID,
		Objective:                        state.Objective,
		ObjectiveRevision:                state.ObjectiveRevision,
		Status:                           string(state.Status),
		StatusReasonCode:                 state.StatusReasonCode,
		StatusReason:                     state.StatusReason,
		Revision:                         state.Revision,
		TokenBudget:                      cloneUint64(state.TokenBudget),
		TokensUsed:                       state.TokensUsed,
		UsageLedgerRevision:              state.UsageLedgerRevision,
		RootActiveTimeMillis:             state.RootActiveTimeMillis,
		ContinuationOrdinal:              state.ContinuationOrdinal,
		Continuation:                     persistedGoalContinuation(state.Continuation),
		LastGoalTurnID:                   state.LastGoalTurnID,
		LastTerminalSequence:             state.LastTerminalSequence,
		PendingCompleteTurnID:            state.PendingCompleteTurnID,
		PendingCompleteObjectiveRevision: state.PendingCompleteObjectiveRevision,
		PendingUsageAdmission:            persistedGoalUsageAdmission(state.PendingUsageAdmission),
		BlockerKey:                       state.BlockerKey,
		BlockerTurnIDs:                   append([]string(nil), state.BlockerTurnIDs...),
		CreatedAt:                        state.CreatedAt,
		UpdatedAt:                        state.UpdatedAt,
	}
}

func restorePersistedGoalState(
	record *session.PersistedGoalState,
	agentID string,
	now time.Time,
) (*goalState, []string, bool) {
	return restorePersistedGoalStateWithUsage(record, agentID, now, nil, false)
}

func restorePersistedGoalStateWithUsage(
	record *session.PersistedGoalState,
	agentID string,
	now time.Time,
	loaded *transcript.LoadResult,
	reconcileUsage bool,
) (*goalState, []string, bool) {
	if record == nil {
		return nil, nil, false
	}
	if strings.TrimSpace(agentID) != "" {
		return nil, []string{
			"ignored persisted Goal state on a child or review Agent session",
		}, true
	}
	if record.HasInvalidEncoding() {
		reason := "persisted Goal state has invalid encoding; Goal is unavailable"
		return unavailableGoalState(
			record,
			goalReasonCorruptState,
			reason,
		), []string{reason}, false
	}
	if record.PendingUsageAdmission != nil &&
		record.PendingUsageAdmission.Version !=
			session.PersistedGoalUsageAdmissionVersion {
		reason := fmt.Sprintf(
			"unsupported persisted Goal usage admission version %d; Goal is unavailable",
			record.PendingUsageAdmission.Version,
		)
		return unavailableGoalState(
			record,
			goalReasonUnsupportedVersion,
			reason,
		), []string{reason}, false
	}
	sourceVersion := record.Version
	legacyVersion := sourceVersion == session.PersistedGoalStateLegacyVersion
	accountingVersion := sourceVersion == session.PersistedGoalStateAccountingVersion
	continuationVersion := sourceVersion ==
		session.PersistedGoalStateContinuationVersion
	if record.Version != session.PersistedGoalStateVersion &&
		!legacyVersion &&
		!accountingVersion &&
		!continuationVersion {
		reason := fmt.Sprintf(
			"unsupported persisted Goal state version %d; Goal is unavailable",
			record.Version,
		)
		return unavailableGoalState(
			record,
			goalReasonUnsupportedVersion,
			reason,
		), []string{reason}, false
	}
	working := clonePersistedGoalState(record)
	if legacyVersion {
		if working.PendingUsageAdmission != nil {
			reason := "legacy persisted Goal state contains unsupported usage admission; Goal is unavailable"
			return unavailableGoalState(
				record,
				goalReasonUnsupportedVersion,
				reason,
			), []string{reason}, false
		}
		working.Version = session.PersistedGoalStateAccountingVersion
	}
	if legacyVersion || accountingVersion {
		if working.Continuation != nil {
			reason := "older persisted Goal state contains unsupported continuation; Goal is unavailable"
			return unavailableGoalState(
				record,
				goalReasonUnsupportedVersion,
				reason,
			), []string{reason}, false
		}
	}
	if sourceVersion != session.PersistedGoalStateVersion {
		working.Version = session.PersistedGoalStateVersion
	}
	if err := validatePersistedGoalStateFromVersion(
		working,
		sourceVersion,
	); err != nil {
		reason := "persisted Goal state is invalid; Goal is unavailable"
		return unavailableGoalState(
			record,
			goalReasonCorruptState,
			reason,
		), []string{reason + ": " + err.Error()}, false
	}

	state := goalStateFromPersisted(working)
	warnings := make([]string, 0, 2)
	checkpointRequired := sourceVersion != session.PersistedGoalStateVersion
	if checkpointRequired {
		warnings = append(
			warnings,
			fmt.Sprintf(
				"migrated persisted Goal state from version %d to version %d",
				record.Version,
				session.PersistedGoalStateVersion,
			),
		)
	}
	if reconcileUsage {
		reconciled, changed, usageErr := reconcileGoalUsageState(
			state,
			loaded,
			now,
		)
		if changed {
			state = reconciled
			checkpointRequired = true
		}
		if usageErr != nil {
			warnings = append(
				warnings,
				"Goal provider usage recovery failed closed: "+usageErr.Error(),
			)
		}
	}
	if state.Status == goalStatusActive &&
		(state.Continuation == nil ||
			(state.Continuation.Disposition != goalContinuationDispositionPending &&
				state.Continuation.Disposition != goalContinuationDispositionAdmitting)) {
		if state.Revision == math.MaxUint64 {
			reason := "persisted active Goal revision is exhausted; Goal is unavailable"
			return unavailableGoalState(
				record,
				goalReasonCorruptState,
				reason,
			), []string{reason}, false
		}
		state.Status = goalStatusPaused
		state.StatusReasonCode = goalReasonColdContinuationUnavailable
		state.StatusReason = "automatic continuation is unavailable without an eligible durable cursor"
		state.Revision++
		state.UpdatedAt = now
		resetGoalTurnEvidence(state)
		warnings = append(
			warnings,
			"normalized persisted active Goal to paused because no eligible durable continuation cursor exists",
		)
		return state, warnings, true
	}
	return state, warnings, checkpointRequired
}

func validatePersistedGoalState(record *session.PersistedGoalState) error {
	if record == nil {
		return nil
	}
	return validatePersistedGoalStateFromVersion(record, record.Version)
}

func validatePersistedGoalStateFromVersion(
	record *session.PersistedGoalState,
	sourceVersion uint16,
) error {
	if record == nil {
		return nil
	}
	if strings.TrimSpace(record.GoalID) == "" ||
		record.GoalID != strings.TrimSpace(record.GoalID) ||
		!utf8.ValidString(record.GoalID) ||
		strings.ContainsRune(record.GoalID, '\x00') {
		return fmt.Errorf("invalid Goal identity")
	}
	objective, err := normalizeGoalObjective(record.Objective)
	if err != nil || objective != record.Objective {
		return fmt.Errorf("invalid objective")
	}
	if record.ObjectiveRevision == 0 || record.Revision == 0 {
		return fmt.Errorf("zero revision")
	}
	if record.ObjectiveRevision > record.Revision {
		return fmt.Errorf("objective revision exceeds Goal revision")
	}
	status := goalStatus(strings.TrimSpace(record.Status))
	if status != goalStatus(record.Status) || !knownGoalStatus(status) {
		return fmt.Errorf("unknown status")
	}
	if record.TokenBudget != nil && *record.TokenBudget == 0 {
		return fmt.Errorf("zero token budget")
	}
	if status == goalStatusActive &&
		((sourceVersion < session.PersistedGoalStateVersion &&
			record.TokenBudget == nil) ||
			!goalBudgetHasRemaining(record.TokenBudget, record.TokensUsed)) {
		return fmt.Errorf("active Goal has no remaining token budget")
	}
	if record.StatusReason != "" {
		reason, reasonErr := normalizeGoalReason(record.StatusReason)
		if reasonErr != nil || reason != record.StatusReason {
			return fmt.Errorf("invalid status reason")
		}
	}
	if record.StatusReasonCode != "" {
		code, codeErr := normalizeGoalBlockerKey(record.StatusReasonCode)
		if codeErr != nil || code != record.StatusReasonCode {
			return fmt.Errorf("invalid status reason code")
		}
	}
	if record.BlockerKey != "" {
		key, keyErr := normalizeGoalBlockerKey(record.BlockerKey)
		if keyErr != nil || key != record.BlockerKey {
			return fmt.Errorf("invalid blocker key")
		}
	}
	if len(record.BlockerTurnIDs) > 3 {
		return fmt.Errorf("too many blocker turns")
	}
	if (record.BlockerKey == "") != (len(record.BlockerTurnIDs) == 0) {
		return fmt.Errorf("incomplete blocker evidence")
	}
	seenTurns := make(map[string]struct{}, len(record.BlockerTurnIDs))
	for _, turnID := range record.BlockerTurnIDs {
		if strings.TrimSpace(turnID) == "" ||
			turnID != strings.TrimSpace(turnID) ||
			!utf8.ValidString(turnID) ||
			strings.ContainsRune(turnID, '\x00') {
			return fmt.Errorf("invalid blocker turn identity")
		}
		if _, duplicate := seenTurns[turnID]; duplicate {
			return fmt.Errorf("duplicate blocker turn identity")
		}
		seenTurns[turnID] = struct{}{}
	}
	if record.RootActiveTimeMillis < 0 {
		return fmt.Errorf("negative active time")
	}
	if record.CreatedAt.IsZero() ||
		record.UpdatedAt.IsZero() ||
		record.UpdatedAt.Before(record.CreatedAt) {
		return fmt.Errorf("invalid timestamps")
	}
	if (record.PendingCompleteTurnID == "") !=
		(record.PendingCompleteObjectiveRevision == 0) {
		return fmt.Errorf("incomplete pending-completion identity")
	}
	if record.PendingCompleteTurnID != "" {
		if err := validateGoalTurnID(record.PendingCompleteTurnID); err != nil {
			return fmt.Errorf("invalid pending-completion turn identity")
		}
		if record.PendingCompleteObjectiveRevision != record.ObjectiveRevision {
			return fmt.Errorf("stale pending-completion objective revision")
		}
	}
	if record.LastGoalTurnID != "" {
		if err := validateGoalTurnID(record.LastGoalTurnID); err != nil {
			return fmt.Errorf("invalid last Goal turn identity")
		}
	}
	if record.Continuation != nil {
		cursor := goalContinuationFromPersisted(record.Continuation)
		if err := validateGoalContinuationCursor(cursor); err != nil {
			return fmt.Errorf("invalid Goal continuation: %w", err)
		}
		if cursor.GoalID != record.GoalID ||
			cursor.ObjectiveRevision != record.ObjectiveRevision ||
			cursor.ContinuationOrdinal != record.ContinuationOrdinal ||
			cursor.PredecessorTerminalSequence != record.LastTerminalSequence ||
			!sameGoalTokenBudget(cursor.TokenBudget, record.TokenBudget) ||
			cursor.TokensUsed != record.TokensUsed ||
			cursor.UsageLedgerRevision != record.UsageLedgerRevision {
			return fmt.Errorf("stale Goal continuation")
		}
		if cursor.Disposition == goalContinuationDispositionPending {
			if record.Status != string(goalStatusActive) ||
				cursor.PredecessorGoalTurnID != record.LastGoalTurnID ||
				cursor.GoalRevision != record.Revision {
				return fmt.Errorf("pending Goal continuation does not bind current state")
			}
		} else if cursor.Disposition == goalContinuationDispositionAdmitting {
			if record.Status != string(goalStatusActive) ||
				record.LastGoalTurnID != cursor.ContinuationTurnID ||
				cursor.GoalRevision >= record.Revision {
				return fmt.Errorf("admitting Goal continuation does not bind current turn")
			}
		} else if cursor.GoalRevision > record.Revision {
			return fmt.Errorf("goal continuation revision exceeds current state")
		}
	}
	if record.PendingUsageAdmission != nil {
		admission := goalUsageAdmissionFromPersisted(
			record.PendingUsageAdmission,
		)
		if err := validateGoalUsageAdmission(admission); err != nil {
			return fmt.Errorf("invalid pending usage admission: %w", err)
		}
		if admission.GoalID != record.GoalID ||
			admission.ObjectiveRevision != record.ObjectiveRevision ||
			admission.LedgerRevision != record.UsageLedgerRevision+1 ||
			admission.GoalTurnID != record.LastGoalTurnID {
			return fmt.Errorf("stale pending usage admission")
		}
		if admission.AdmittedAt.Before(record.CreatedAt) ||
			admission.AdmittedAt.After(record.UpdatedAt) {
			return fmt.Errorf("invalid pending usage admission timestamp")
		}
	}
	return nil
}

func validateGoalTurnID(turnID string) error {
	if strings.TrimSpace(turnID) == "" ||
		turnID != strings.TrimSpace(turnID) ||
		!utf8.ValidString(turnID) ||
		strings.ContainsRune(turnID, '\x00') {
		return fmt.Errorf("invalid turn identity")
	}
	return nil
}

func knownGoalStatus(status goalStatus) bool {
	switch status {
	case goalStatusActive,
		goalStatusPaused,
		goalStatusBlocked,
		goalStatusUsageLimited,
		goalStatusBudgetLimited,
		goalStatusComplete:
		return true
	default:
		return false
	}
}

func goalStateFromPersisted(record *session.PersistedGoalState) *goalState {
	if record == nil {
		return nil
	}
	return &goalState{
		GoalID:                           record.GoalID,
		Objective:                        record.Objective,
		ObjectiveRevision:                record.ObjectiveRevision,
		Status:                           goalStatus(record.Status),
		StatusReasonCode:                 record.StatusReasonCode,
		StatusReason:                     record.StatusReason,
		Revision:                         record.Revision,
		TokenBudget:                      cloneUint64(record.TokenBudget),
		TokensUsed:                       record.TokensUsed,
		UsageLedgerRevision:              record.UsageLedgerRevision,
		RootActiveTimeMillis:             record.RootActiveTimeMillis,
		ContinuationOrdinal:              record.ContinuationOrdinal,
		Continuation:                     goalContinuationFromPersisted(record.Continuation),
		LastGoalTurnID:                   record.LastGoalTurnID,
		LastTerminalSequence:             record.LastTerminalSequence,
		PendingCompleteTurnID:            record.PendingCompleteTurnID,
		PendingCompleteObjectiveRevision: record.PendingCompleteObjectiveRevision,
		PendingUsageAdmission:            goalUsageAdmissionFromPersisted(record.PendingUsageAdmission),
		BlockerKey:                       record.BlockerKey,
		BlockerTurnIDs:                   append([]string(nil), record.BlockerTurnIDs...),
		CreatedAt:                        record.CreatedAt,
		UpdatedAt:                        record.UpdatedAt,
	}
}

func unavailableGoalState(
	record *session.PersistedGoalState,
	reasonCode string,
	reason string,
) *goalState {
	state := goalStateFromPersisted(record)
	if state == nil {
		state = &goalState{}
	}
	state.Status = goalStatusPaused
	state.StatusReasonCode = reasonCode
	state.StatusReason = reason
	state.unavailable = true
	state.unavailablePersistedRepresentation = clonePersistedGoalState(record)
	resetGoalTurnEvidence(state)
	return state
}

func clonePersistedGoalState(
	record *session.PersistedGoalState,
) *session.PersistedGoalState {
	if record == nil {
		return nil
	}
	cloned := *record
	cloned.TokenBudget = cloneUint64(record.TokenBudget)
	if record.Continuation != nil {
		continuation := *record.Continuation
		continuation.TokenBudget = cloneUint64(record.Continuation.TokenBudget)
		cloned.Continuation = &continuation
	}
	cloned.BlockerTurnIDs = append([]string(nil), record.BlockerTurnIDs...)
	if record.PendingUsageAdmission != nil {
		pending := *record.PendingUsageAdmission
		cloned.PendingUsageAdmission = &pending
	}
	return &cloned
}
