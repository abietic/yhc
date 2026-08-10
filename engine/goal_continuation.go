package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/abietic/yhc/engine/session"
)

const (
	goalContinuationDispositionPending   = "pending"
	goalContinuationDispositionAdmitting = "admitting"
	goalContinuationDispositionRejected  = "rejected"
	goalContinuationDispositionDelivered = "delivered"
)

var errGoalContinuationPermanentlyRejected = fmt.Errorf(
	"goal continuation was permanently rejected",
)

func goalBudgetHasRemaining(tokenBudget *uint64, tokensUsed uint64) bool {
	return tokenBudget == nil || *tokenBudget > tokensUsed
}

func sameGoalTokenBudget(left, right *uint64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

type goalContinuationCursor struct {
	Version                     uint16
	ItemID                      string
	CheckpointID                string
	ContinuationTurnID          string
	GoalID                      string
	GoalSchemaVersion           uint16
	ObjectiveRevision           uint64
	GoalRevision                uint64
	GoalStatus                  string
	RootSessionID               string
	RootThreadID                string
	RootAgentID                 string
	PredecessorGoalTurnID       string
	PredecessorTerminalSequence uint64
	PredecessorTerminalReason   string
	PredecessorTerminalAt       time.Time
	TokenBudget                 *uint64
	TokensUsed                  uint64
	UsageLedgerRevision         uint64
	ContinuationOrdinal         uint64
	RuntimeRevision             uint64
	Disposition                 string
	DispositionReason           string
	DispositionAt               time.Time
}

type goalContinuationIdentity struct {
	Version                     uint16  `json:"version"`
	GoalID                      string  `json:"goal_id"`
	GoalSchemaVersion           uint16  `json:"goal_schema_version"`
	ObjectiveRevision           uint64  `json:"objective_revision"`
	GoalRevision                uint64  `json:"goal_revision"`
	GoalStatus                  string  `json:"goal_status"`
	RootSessionID               string  `json:"root_session_id"`
	RootThreadID                string  `json:"root_thread_id"`
	RootAgentID                 string  `json:"root_agent_id,omitempty"`
	PredecessorGoalTurnID       string  `json:"predecessor_goal_turn_id"`
	PredecessorTerminalSequence uint64  `json:"predecessor_terminal_sequence"`
	PredecessorTerminalReason   string  `json:"predecessor_terminal_reason"`
	PredecessorTerminalAt       string  `json:"predecessor_terminal_at"`
	TokenBudget                 *uint64 `json:"token_budget,omitempty"`
	TokensUsed                  uint64  `json:"tokens_used"`
	UsageLedgerRevision         uint64  `json:"usage_ledger_revision"`
	ContinuationOrdinal         uint64  `json:"continuation_ordinal"`
	RuntimeRevision             uint64  `json:"runtime_revision"`
}

func newGoalContinuationCursor(
	state *goalState,
	scope RuntimeInputScope,
	terminal Terminal,
	terminalSequence uint64,
	terminalAt time.Time,
	runtimeRevision uint64,
) (*goalContinuationCursor, error) {
	if state == nil {
		return nil, fmt.Errorf("goal continuation requires Goal state")
	}
	if state.ContinuationOrdinal == math.MaxUint64 {
		return nil, fmt.Errorf("goal continuation ordinal exhausted")
	}
	identity := goalContinuationIdentity{
		Version:                     session.PersistedGoalContinuationVersion,
		GoalID:                      state.GoalID,
		GoalSchemaVersion:           session.PersistedGoalStateVersion,
		ObjectiveRevision:           state.ObjectiveRevision,
		GoalRevision:                state.Revision,
		GoalStatus:                  string(state.Status),
		RootSessionID:               scope.SessionID,
		RootThreadID:                scope.ThreadID,
		RootAgentID:                 scope.AgentID,
		PredecessorGoalTurnID:       state.LastGoalTurnID,
		PredecessorTerminalSequence: terminalSequence,
		PredecessorTerminalReason:   string(terminal.Reason),
		PredecessorTerminalAt:       terminalAt.UTC().Format(time.RFC3339Nano),
		TokenBudget:                 cloneUint64(state.TokenBudget),
		TokensUsed:                  state.TokensUsed,
		UsageLedgerRevision:         state.UsageLedgerRevision,
		ContinuationOrdinal:         state.ContinuationOrdinal + 1,
		RuntimeRevision:             runtimeRevision,
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return nil, fmt.Errorf("encode Goal continuation identity: %w", err)
	}
	digest := sha256.Sum256(encoded)
	suffix := hex.EncodeToString(digest[:])
	cursor := &goalContinuationCursor{
		Version:                     identity.Version,
		ItemID:                      "goal-continuation:" + suffix,
		CheckpointID:                "goal-continuation-checkpoint:" + suffix,
		ContinuationTurnID:          "goal-continuation-turn:" + suffix,
		GoalID:                      identity.GoalID,
		GoalSchemaVersion:           identity.GoalSchemaVersion,
		ObjectiveRevision:           identity.ObjectiveRevision,
		GoalRevision:                identity.GoalRevision,
		GoalStatus:                  identity.GoalStatus,
		RootSessionID:               identity.RootSessionID,
		RootThreadID:                identity.RootThreadID,
		RootAgentID:                 identity.RootAgentID,
		PredecessorGoalTurnID:       identity.PredecessorGoalTurnID,
		PredecessorTerminalSequence: identity.PredecessorTerminalSequence,
		PredecessorTerminalReason:   identity.PredecessorTerminalReason,
		PredecessorTerminalAt:       terminalAt.UTC(),
		TokenBudget:                 cloneUint64(identity.TokenBudget),
		TokensUsed:                  identity.TokensUsed,
		UsageLedgerRevision:         identity.UsageLedgerRevision,
		ContinuationOrdinal:         identity.ContinuationOrdinal,
		RuntimeRevision:             identity.RuntimeRevision,
		Disposition:                 goalContinuationDispositionPending,
	}
	if err := validateGoalContinuationCursor(cursor); err != nil {
		return nil, err
	}
	return cursor, nil
}

func goalContinuationRuntimeItem(cursor *goalContinuationCursor) RuntimeItem {
	if cursor == nil {
		return RuntimeItem{}
	}
	payloadVersion := runtimeGoalContinuationVersion
	if cursor.Version == session.PersistedGoalContinuationLegacyVersion {
		payloadVersion = runtimeGoalContinuationLegacyVersion
	}
	return RuntimeItem{
		ID:       cursor.ItemID,
		Kind:     RuntimeItemGoalContinuation,
		Priority: RuntimePriorityLater,
		Scope: RuntimeInputScope{
			SessionID: cursor.RootSessionID,
			ThreadID:  cursor.RootThreadID,
			AgentID:   cursor.RootAgentID,
		},
		IsMeta:     true,
		Origin:     "goal",
		Provenance: "p24.3-durable-continuation",
		GoalContinuation: &RuntimeGoalContinuation{
			Version:                     payloadVersion,
			CheckpointID:                cursor.CheckpointID,
			ContinuationTurnID:          cursor.ContinuationTurnID,
			GoalID:                      cursor.GoalID,
			GoalSchemaVersion:           cursor.GoalSchemaVersion,
			ObjectiveRevision:           cursor.ObjectiveRevision,
			GoalRevision:                cursor.GoalRevision,
			GoalStatus:                  cursor.GoalStatus,
			RootSessionID:               cursor.RootSessionID,
			RootThreadID:                cursor.RootThreadID,
			RootAgentID:                 cursor.RootAgentID,
			PredecessorGoalTurnID:       cursor.PredecessorGoalTurnID,
			PredecessorTerminalSequence: cursor.PredecessorTerminalSequence,
			PredecessorTerminalReason:   cursor.PredecessorTerminalReason,
			PredecessorTerminalAt:       cursor.PredecessorTerminalAt,
			TokenBudget:                 cloneUint64(cursor.TokenBudget),
			TokensUsed:                  cursor.TokensUsed,
			UsageLedgerRevision:         cursor.UsageLedgerRevision,
			ContinuationOrdinal:         cursor.ContinuationOrdinal,
			RuntimeRevision:             cursor.RuntimeRevision,
		},
	}
}

func validateRuntimeGoalContinuation(
	itemID string,
	scope RuntimeInputScope,
	payload RuntimeGoalContinuation,
) error {
	var cursorVersion uint16
	switch payload.Version {
	case runtimeGoalContinuationLegacyVersion:
		cursorVersion = session.PersistedGoalContinuationLegacyVersion
	case runtimeGoalContinuationVersion:
		cursorVersion = session.PersistedGoalContinuationVersion
	default:
		return fmt.Errorf("unsupported payload version %d", payload.Version)
	}
	cursor := &goalContinuationCursor{
		Version:                     cursorVersion,
		ItemID:                      strings.TrimSpace(itemID),
		CheckpointID:                payload.CheckpointID,
		ContinuationTurnID:          payload.ContinuationTurnID,
		GoalID:                      payload.GoalID,
		GoalSchemaVersion:           payload.GoalSchemaVersion,
		ObjectiveRevision:           payload.ObjectiveRevision,
		GoalRevision:                payload.GoalRevision,
		GoalStatus:                  payload.GoalStatus,
		RootSessionID:               payload.RootSessionID,
		RootThreadID:                payload.RootThreadID,
		RootAgentID:                 payload.RootAgentID,
		PredecessorGoalTurnID:       payload.PredecessorGoalTurnID,
		PredecessorTerminalSequence: payload.PredecessorTerminalSequence,
		PredecessorTerminalReason:   payload.PredecessorTerminalReason,
		PredecessorTerminalAt:       payload.PredecessorTerminalAt,
		TokenBudget:                 cloneUint64(payload.TokenBudget),
		TokensUsed:                  payload.TokensUsed,
		UsageLedgerRevision:         payload.UsageLedgerRevision,
		ContinuationOrdinal:         payload.ContinuationOrdinal,
		RuntimeRevision:             payload.RuntimeRevision,
		Disposition:                 goalContinuationDispositionPending,
	}
	if !runtimeScopesEqual(scope, RuntimeInputScope{
		SessionID: cursor.RootSessionID,
		ThreadID:  cursor.RootThreadID,
		AgentID:   cursor.RootAgentID,
	}) {
		return fmt.Errorf("payload scope does not match item scope")
	}
	return validateGoalContinuationCursor(cursor)
}

func validateGoalContinuationCursor(cursor *goalContinuationCursor) error {
	if cursor == nil {
		return nil
	}
	switch cursor.Version {
	case session.PersistedGoalContinuationLegacyVersion:
		if cursor.GoalSchemaVersion !=
			session.PersistedGoalStateContinuationVersion ||
			cursor.TokenBudget == nil {
			return fmt.Errorf("invalid legacy Goal continuation schema or budget")
		}
	case session.PersistedGoalContinuationVersion:
		if cursor.GoalSchemaVersion != session.PersistedGoalStateVersion {
			return fmt.Errorf("invalid Goal continuation schema version")
		}
	default:
		return fmt.Errorf("unsupported Goal continuation version %d", cursor.Version)
	}
	if strings.TrimSpace(cursor.GoalID) == "" ||
		strings.TrimSpace(cursor.RootSessionID) == "" ||
		strings.TrimSpace(cursor.RootThreadID) == "" ||
		cursor.RootAgentID != "" {
		return fmt.Errorf("invalid Goal continuation scope")
	}
	if cursor.ObjectiveRevision == 0 ||
		cursor.GoalRevision == 0 ||
		cursor.GoalStatus != string(goalStatusActive) {
		return fmt.Errorf("invalid Goal continuation revision")
	}
	if err := validateGoalTurnID(cursor.PredecessorGoalTurnID); err != nil {
		return fmt.Errorf("invalid predecessor Goal turn")
	}
	if err := validateGoalTurnID(cursor.ContinuationTurnID); err != nil {
		return fmt.Errorf("invalid continuation Goal turn")
	}
	if cursor.PredecessorTerminalSequence == 0 ||
		cursor.PredecessorTerminalReason != string(TerminalCompleted) ||
		cursor.PredecessorTerminalAt.IsZero() {
		return fmt.Errorf("invalid predecessor terminal")
	}
	if !goalBudgetHasRemaining(cursor.TokenBudget, cursor.TokensUsed) ||
		cursor.ContinuationOrdinal == 0 {
		return fmt.Errorf("invalid Goal continuation budget or ordinal")
	}
	switch cursor.Disposition {
	case goalContinuationDispositionPending:
		if cursor.DispositionReason != "" || !cursor.DispositionAt.IsZero() {
			return fmt.Errorf("pending Goal continuation has a terminal disposition")
		}
	case goalContinuationDispositionAdmitting,
		goalContinuationDispositionRejected,
		goalContinuationDispositionDelivered:
		if strings.TrimSpace(cursor.DispositionReason) == "" ||
			cursor.DispositionAt.IsZero() {
			return fmt.Errorf("terminal Goal continuation disposition is incomplete")
		}
	default:
		return fmt.Errorf("unknown Goal continuation disposition %q", cursor.Disposition)
	}
	expected, err := expectedGoalContinuationIDs(cursor)
	if err != nil {
		return err
	}
	if cursor.ItemID != expected.ItemID ||
		cursor.CheckpointID != expected.CheckpointID ||
		cursor.ContinuationTurnID != expected.ContinuationTurnID {
		return fmt.Errorf("goal continuation deterministic identity mismatch")
	}
	return nil
}

func expectedGoalContinuationIDs(
	cursor *goalContinuationCursor,
) (*goalContinuationCursor, error) {
	identity := goalContinuationIdentity{
		Version:                     cursor.Version,
		GoalID:                      cursor.GoalID,
		GoalSchemaVersion:           cursor.GoalSchemaVersion,
		ObjectiveRevision:           cursor.ObjectiveRevision,
		GoalRevision:                cursor.GoalRevision,
		GoalStatus:                  cursor.GoalStatus,
		RootSessionID:               cursor.RootSessionID,
		RootThreadID:                cursor.RootThreadID,
		RootAgentID:                 cursor.RootAgentID,
		PredecessorGoalTurnID:       cursor.PredecessorGoalTurnID,
		PredecessorTerminalSequence: cursor.PredecessorTerminalSequence,
		PredecessorTerminalReason:   cursor.PredecessorTerminalReason,
		PredecessorTerminalAt:       cursor.PredecessorTerminalAt.UTC().Format(time.RFC3339Nano),
		TokenBudget:                 cloneUint64(cursor.TokenBudget),
		TokensUsed:                  cursor.TokensUsed,
		UsageLedgerRevision:         cursor.UsageLedgerRevision,
		ContinuationOrdinal:         cursor.ContinuationOrdinal,
		RuntimeRevision:             cursor.RuntimeRevision,
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return nil, fmt.Errorf("encode Goal continuation identity: %w", err)
	}
	digest := sha256.Sum256(encoded)
	suffix := hex.EncodeToString(digest[:])
	return &goalContinuationCursor{
		ItemID:             "goal-continuation:" + suffix,
		CheckpointID:       "goal-continuation-checkpoint:" + suffix,
		ContinuationTurnID: "goal-continuation-turn:" + suffix,
	}, nil
}

func cloneGoalContinuationCursor(
	cursor *goalContinuationCursor,
) *goalContinuationCursor {
	if cursor == nil {
		return nil
	}
	cloned := *cursor
	cloned.TokenBudget = cloneUint64(cursor.TokenBudget)
	return &cloned
}

func persistedGoalContinuation(
	cursor *goalContinuationCursor,
) *session.PersistedGoalContinuation {
	if cursor == nil {
		return nil
	}
	return &session.PersistedGoalContinuation{
		Version:                     cursor.Version,
		ItemID:                      cursor.ItemID,
		CheckpointID:                cursor.CheckpointID,
		ContinuationTurnID:          cursor.ContinuationTurnID,
		GoalID:                      cursor.GoalID,
		GoalSchemaVersion:           cursor.GoalSchemaVersion,
		ObjectiveRevision:           cursor.ObjectiveRevision,
		GoalRevision:                cursor.GoalRevision,
		GoalStatus:                  cursor.GoalStatus,
		RootSessionID:               cursor.RootSessionID,
		RootThreadID:                cursor.RootThreadID,
		RootAgentID:                 cursor.RootAgentID,
		PredecessorGoalTurnID:       cursor.PredecessorGoalTurnID,
		PredecessorTerminalSequence: cursor.PredecessorTerminalSequence,
		PredecessorTerminalReason:   cursor.PredecessorTerminalReason,
		PredecessorTerminalAt:       cursor.PredecessorTerminalAt,
		TokenBudget:                 cloneUint64(cursor.TokenBudget),
		TokensUsed:                  cursor.TokensUsed,
		UsageLedgerRevision:         cursor.UsageLedgerRevision,
		ContinuationOrdinal:         cursor.ContinuationOrdinal,
		RuntimeRevision:             cursor.RuntimeRevision,
		Disposition:                 cursor.Disposition,
		DispositionReason:           cursor.DispositionReason,
		DispositionAt:               cursor.DispositionAt,
	}
}

func goalContinuationFromPersisted(
	cursor *session.PersistedGoalContinuation,
) *goalContinuationCursor {
	if cursor == nil {
		return nil
	}
	return &goalContinuationCursor{
		Version:                     cursor.Version,
		ItemID:                      cursor.ItemID,
		CheckpointID:                cursor.CheckpointID,
		ContinuationTurnID:          cursor.ContinuationTurnID,
		GoalID:                      cursor.GoalID,
		GoalSchemaVersion:           cursor.GoalSchemaVersion,
		ObjectiveRevision:           cursor.ObjectiveRevision,
		GoalRevision:                cursor.GoalRevision,
		GoalStatus:                  cursor.GoalStatus,
		RootSessionID:               cursor.RootSessionID,
		RootThreadID:                cursor.RootThreadID,
		RootAgentID:                 cursor.RootAgentID,
		PredecessorGoalTurnID:       cursor.PredecessorGoalTurnID,
		PredecessorTerminalSequence: cursor.PredecessorTerminalSequence,
		PredecessorTerminalReason:   cursor.PredecessorTerminalReason,
		PredecessorTerminalAt:       cursor.PredecessorTerminalAt,
		TokenBudget:                 cloneUint64(cursor.TokenBudget),
		TokensUsed:                  cursor.TokensUsed,
		UsageLedgerRevision:         cursor.UsageLedgerRevision,
		ContinuationOrdinal:         cursor.ContinuationOrdinal,
		RuntimeRevision:             cursor.RuntimeRevision,
		Disposition:                 cursor.Disposition,
		DispositionReason:           cursor.DispositionReason,
		DispositionAt:               cursor.DispositionAt,
	}
}

func goalContinuationEligible(
	state *goalState,
	runtime *goalTurnRuntime,
	terminal Terminal,
) bool {
	return state != nil &&
		runtime != nil &&
		state.Status == goalStatusActive &&
		terminal.Reason == TerminalCompleted &&
		len(runtime.waiting) == 0 &&
		state.PendingCompleteTurnID == "" &&
		state.PendingCompleteObjectiveRevision == 0 &&
		state.PendingUsageAdmission == nil &&
		goalBudgetHasRemaining(state.TokenBudget, state.TokensUsed)
}

func runtimeGoalContinuationMatchesCursor(
	item RuntimeItem,
	cursor *goalContinuationCursor,
) bool {
	if cursor == nil ||
		item.ID != cursor.ItemID ||
		item.Kind != RuntimeItemGoalContinuation ||
		item.GoalContinuation == nil {
		return false
	}
	expected := goalContinuationRuntimeItem(cursor)
	left, leftErr := json.Marshal(item.GoalContinuation)
	right, rightErr := json.Marshal(expected.GoalContinuation)
	return leftErr == nil &&
		rightErr == nil &&
		bytes.Equal(left, right) &&
		runtimeScopesEqual(item.Scope, expected.Scope)
}

func (e *QueryEngine) claimGoalContinuation() (RuntimeItem, bool, error) {
	if e == nil {
		return RuntimeItem{}, false, nil
	}
	coordinator, scope, err := e.runtimeInputOwner()
	if err != nil {
		return RuntimeItem{}, false, err
	}
	e.planMu.Lock()
	defer e.planMu.Unlock()
	e.goalMu.Lock()
	defer e.goalMu.Unlock()
	if e.planActiveTurnID != "" ||
		planPhaseRequiresContainment(e.planState.Phase) {
		return RuntimeItem{}, false, nil
	}
	e.mu.Lock()
	checkpoint := e.projectGraphCheckpoint
	e.mu.Unlock()
	if checkpoint != nil {
		if _, waiting := checkpoint.ActiveInterrupt(); waiting {
			return RuntimeItem{}, false, nil
		}
	}
	state := e.goalState
	if state == nil ||
		state.unavailable ||
		state.Status != goalStatusActive ||
		state.Continuation == nil ||
		(state.Continuation.Disposition != goalContinuationDispositionPending &&
			state.Continuation.Disposition != goalContinuationDispositionAdmitting) {
		return RuntimeItem{}, false, nil
	}
	return coordinator.claimGoalContinuation(
		state.Continuation.ItemID,
		scope,
	)
}

func (e *QueryEngine) submitGoalContinuation(
	ctx context.Context,
	item RuntimeItem,
) (<-chan QueryEvent, Terminal) {
	if item.Kind != RuntimeItemGoalContinuation ||
		item.GoalContinuation == nil {
		return closedRuntimeItemError(
			fmt.Errorf("runtime item %q is not a Goal continuation", item.ID),
		)
	}
	extra := runtimeItemMetadata(item)
	extra["command_mode"] = "goal-continuation"
	extra["attachment_kind"] = "goal_continuation"
	extra["goal_continuation"] = true
	extra["goal_id"] = item.GoalContinuation.GoalID
	extra["goal_objective_revision"] = item.GoalContinuation.ObjectiveRevision
	extra["goal_continuation_ordinal"] = item.GoalContinuation.ContinuationOrdinal
	return e.submitMessageWithRuntimeItem(
		ctx,
		"Continue working toward the active Goal. Re-evaluate the remaining required work before acting.",
		extra,
		nil,
		&item,
		nil,
	)
}

func closedRuntimeItemError(err error) (<-chan QueryEvent, Terminal) {
	events := make(chan QueryEvent, 1)
	terminal := Terminal{Reason: TerminalModelError, Err: err}
	events <- QueryEvent{Type: EventTerminal, TerminalInfo: &terminal}
	close(events)
	return events, terminal
}

func (s *goalService) beginContinuationTurn(
	turnID string,
	item RuntimeItem,
	coordinator *RuntimeInputCoordinator,
	scope RuntimeInputScope,
	now time.Time,
) (*goalState, error) {
	if s == nil || s.engine == nil || coordinator == nil {
		return nil, errGoalUnsupportedScope
	}
	e := s.engine
	current := e.goalState
	if err := validateGoalContinuationAdmission(
		current,
		item,
		scope,
		turnID,
	); err != nil {
		return nil, err
	}
	if current.Continuation.Disposition == goalContinuationDispositionAdmitting {
		return cloneGoalState(current), nil
	}
	var next *goalState
	err := coordinator.withGoalContinuationAdmission(
		item.ID,
		scope,
		func(claimed RuntimeItem, runtimeRevision uint64) error {
			if !runtimeItemsEqualForGoalClaim(claimed, item) {
				return fmt.Errorf("goal continuation claim changed before admission")
			}
			if runtimeRevision <= current.Continuation.RuntimeRevision {
				return fmt.Errorf(
					"goal continuation runtime revision did not advance through enqueue and claim",
				)
			}
			var nextErr error
			next, nextErr = nextGoalRevision(current, now)
			if nextErr != nil {
				return nextErr
			}
			next.LastGoalTurnID = turnID
			next.Continuation = cloneGoalContinuationCursor(current.Continuation)
			next.Continuation.Disposition = goalContinuationDispositionAdmitting
			next.Continuation.DispositionReason = "continuation-turn-admitted"
			next.Continuation.DispositionAt = now
			if err := e.persistSessionCheckpointMessagesLocked(
				"",
				nil,
				next,
			); err != nil {
				return fmt.Errorf(
					"persist Goal continuation admission: %w",
					err,
				)
			}
			return nil
		},
	)
	if err != nil {
		if errors.Is(err, errGoalContinuationPermanentlyRejected) {
			return nil, s.rejectContinuationAdmissionLocked(
				current,
				coordinator,
				"superseded-by-explicit-user-input",
				now,
			)
		}
		return nil, err
	}
	return next, nil
}

func (s *goalService) rejectContinuationAdmissionLocked(
	current *goalState,
	coordinator *RuntimeInputCoordinator,
	reason string,
	now time.Time,
) error {
	if s == nil || s.engine == nil || current == nil ||
		current.Continuation == nil {
		return fmt.Errorf(
			"%w: no durable cursor",
			errGoalContinuationPermanentlyRejected,
		)
	}
	next, err := nextGoalRevision(current, now)
	if err != nil {
		return err
	}
	next.Continuation = cloneGoalContinuationCursor(current.Continuation)
	next.Continuation.Disposition = goalContinuationDispositionRejected
	next.Continuation.DispositionReason = strings.TrimSpace(reason)
	next.Continuation.DispositionAt = now
	if err := s.engine.persistSessionCheckpointMessagesLocked(
		"",
		nil,
		next,
	); err != nil {
		return fmt.Errorf(
			"persist permanent Goal continuation rejection: %w",
			err,
		)
	}
	s.engine.goalState = cloneGoalState(next)
	if err := retireGoalContinuationItem(
		coordinator,
		next.Continuation.ItemID,
		next.Continuation.DispositionReason,
		next.Continuation.DispositionAt,
	); err != nil {
		return fmt.Errorf(
			"%w: %w",
			errGoalContinuationPermanentlyRejected,
			err,
		)
	}
	return fmt.Errorf(
		"%w: %s",
		errGoalContinuationPermanentlyRejected,
		next.Continuation.DispositionReason,
	)
}

func validateGoalContinuationAdmission(
	state *goalState,
	item RuntimeItem,
	scope RuntimeInputScope,
	turnID string,
) error {
	if state == nil ||
		state.unavailable ||
		state.Status != goalStatusActive ||
		state.Continuation == nil {
		return fmt.Errorf("goal continuation has no active owner")
	}
	cursor := state.Continuation
	if cursor.Disposition != goalContinuationDispositionPending &&
		cursor.Disposition != goalContinuationDispositionAdmitting {
		return fmt.Errorf("goal continuation is already %s", cursor.Disposition)
	}
	if !runtimeGoalContinuationMatchesCursor(item, cursor) ||
		!runtimeScopesEqual(scope, item.Scope) ||
		turnID != cursor.ContinuationTurnID {
		return fmt.Errorf("goal continuation immutable identity is stale")
	}
	if state.GoalID != cursor.GoalID ||
		state.ObjectiveRevision != cursor.ObjectiveRevision ||
		state.LastTerminalSequence != cursor.PredecessorTerminalSequence ||
		!sameGoalTokenBudget(state.TokenBudget, cursor.TokenBudget) ||
		state.TokensUsed != cursor.TokensUsed ||
		state.UsageLedgerRevision != cursor.UsageLedgerRevision ||
		state.ContinuationOrdinal != cursor.ContinuationOrdinal ||
		state.PendingUsageAdmission != nil ||
		!goalBudgetHasRemaining(state.TokenBudget, state.TokensUsed) {
		return fmt.Errorf("goal continuation state or accounting identity is stale")
	}
	switch cursor.Disposition {
	case goalContinuationDispositionPending:
		if state.Revision != cursor.GoalRevision ||
			state.LastGoalTurnID != cursor.PredecessorGoalTurnID {
			return fmt.Errorf("goal continuation state revision is stale")
		}
	case goalContinuationDispositionAdmitting:
		if state.Revision <= cursor.GoalRevision ||
			state.LastGoalTurnID != cursor.ContinuationTurnID {
			return fmt.Errorf("goal continuation admitting state is stale")
		}
	}
	return nil
}

func runtimeItemsEqualForGoalClaim(left, right RuntimeItem) bool {
	left.State = RuntimeItemPending
	right.State = RuntimeItemPending
	return runtimeItemsEqual(left, right)
}

func (s *goalService) markContinuationDelivered(
	itemID string,
	turnID string,
	now time.Time,
) error {
	if s == nil || s.engine == nil {
		return errGoalUnsupportedScope
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	e := s.engine
	e.planMu.Lock()
	defer e.planMu.Unlock()
	e.goalMu.Lock()
	defer e.goalMu.Unlock()
	current := e.goalState
	if current == nil ||
		current.Continuation == nil ||
		current.Continuation.ItemID != strings.TrimSpace(itemID) ||
		current.Continuation.ContinuationTurnID != strings.TrimSpace(turnID) ||
		current.Continuation.Disposition != goalContinuationDispositionAdmitting {
		return fmt.Errorf("goal continuation delivery identity is stale")
	}
	next, err := nextGoalRevision(current, now)
	if err != nil {
		return err
	}
	next.Continuation = cloneGoalContinuationCursor(current.Continuation)
	next.Continuation.Disposition = goalContinuationDispositionDelivered
	next.Continuation.DispositionReason = "transcript-receipt-committed"
	next.Continuation.DispositionAt = now
	if err := e.persistSessionCheckpointMessagesLocked("", nil, next); err != nil {
		return fmt.Errorf("persist Goal continuation delivery: %w", err)
	}
	e.goalState = cloneGoalState(next)
	return nil
}

func retireGoalContinuationItem(
	coordinator *RuntimeInputCoordinator,
	itemID string,
	code string,
	at time.Time,
) error {
	if coordinator == nil || strings.TrimSpace(itemID) == "" {
		return nil
	}
	if err := coordinator.rejectGoalContinuation(itemID, code, at); err != nil {
		return fmt.Errorf("persist Goal continuation rejection: %w", err)
	}
	if err := coordinator.Settle(itemID); err != nil {
		return fmt.Errorf("settle rejected Goal continuation: %w", err)
	}
	return nil
}

func goalContinuationTransitionRejection(phase GoalLifecyclePhase) string {
	switch phase {
	case GoalLifecycleObjectiveEdited:
		return "superseded-by-objective-edit"
	case GoalLifecyclePaused:
		return "superseded-by-goal-pause"
	case GoalLifecycleBudgetUpdated:
		return "superseded-by-budget-change"
	case GoalLifecycleResumed:
		return "superseded-by-goal-resume"
	default:
		return "superseded-by-goal-control"
	}
}

func (s *goalService) pauseForCancellation(reason string, now time.Time) error {
	if s == nil || s.engine == nil {
		return nil
	}
	e := s.engine
	coordinator, _, coordinatorErr := e.runtimeInputOwner()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	_ = reason
	e.planMu.Lock()
	defer e.planMu.Unlock()
	e.goalMu.Lock()
	defer e.goalMu.Unlock()
	current := e.goalState
	if current == nil ||
		current.unavailable ||
		current.Status != goalStatusActive {
		return nil
	}
	next, err := nextGoalRevision(current, now)
	if err != nil {
		return err
	}
	next.Status = goalStatusPaused
	next.StatusReasonCode = goalReasonUserCancelled
	next.StatusReason = "automatic Goal continuation was cancelled by the user"
	resetGoalTurnEvidence(next)
	if e.goalTurnRuntime != nil {
		s.stopActiveIntervalLocked(next, now)
	}
	var retired *goalContinuationCursor
	if next.Continuation != nil &&
		(next.Continuation.Disposition == goalContinuationDispositionPending ||
			next.Continuation.Disposition == goalContinuationDispositionAdmitting) {
		if coordinatorErr != nil {
			return coordinatorErr
		}
		next.Continuation = cloneGoalContinuationCursor(next.Continuation)
		next.Continuation.Disposition = goalContinuationDispositionRejected
		next.Continuation.DispositionReason = "superseded-by-user-cancel"
		next.Continuation.DispositionAt = now
		retired = cloneGoalContinuationCursor(next.Continuation)
	}
	if err := e.persistSessionCheckpointMessagesLocked("", nil, next); err != nil {
		return fmt.Errorf("persist Goal cancellation: %w", err)
	}
	e.goalState = cloneGoalState(next)
	if retired != nil {
		if err := retireGoalContinuationItem(
			coordinator,
			retired.ItemID,
			retired.DispositionReason,
			retired.DispositionAt,
		); err != nil {
			e.recordRuntimeStateError(err)
		}
	}
	e.projectGoalLifecycle(GoalLifecyclePaused, next, next.LastGoalTurnID, now)
	return nil
}

func (e *QueryEngine) reconcileRestoredGoalContinuation(
	coordinator *RuntimeInputCoordinator,
) ([]string, error) {
	if e == nil || coordinator == nil {
		return nil, nil
	}
	e.goalMu.Lock()
	state := cloneGoalState(e.goalState)
	e.goalMu.Unlock()
	if state == nil || state.Continuation == nil {
		return nil, nil
	}
	cursor := state.Continuation
	covered, err := coordinator.ResolveDelivered([]string{cursor.ItemID})
	if err != nil {
		return nil, fmt.Errorf(
			"resolve Goal continuation transcript delivery: %w",
			err,
		)
	}
	if _, delivered := covered[cursor.ItemID]; delivered {
		if cursor.Disposition != goalContinuationDispositionDelivered {
			if err := e.pauseRestoredGoalContinuation(
				coordinator,
				cursor.ItemID,
				goalContinuationDispositionDelivered,
				"transcript-receipt-recovered",
			); err != nil {
				return nil, err
			}
		} else if err := coordinator.Settle(cursor.ItemID); err != nil {
			return nil, err
		}
		return []string{
			"paused Goal after recovering an already delivered continuation receipt",
		}, nil
	}
	if e.goalCapabilityConfigured() &&
		(!e.goalCapabilityEnabled() ||
			(e.goalWorkflowEnabled() &&
				planPhaseRequiresContainment(e.PlanState().Phase))) {
		disposition := goalContinuationDispositionRejected
		if cursor.Disposition == goalContinuationDispositionDelivered {
			disposition = goalContinuationDispositionDelivered
		}
		if err := e.pauseRestoredGoalContinuation(
			coordinator,
			cursor.ItemID,
			disposition,
			"goal-capability-disabled",
		); err != nil {
			return nil, err
		}
		return []string{
			"paused Goal because automatic continuation is disabled for this runtime",
		}, nil
	}
	switch cursor.Disposition {
	case goalContinuationDispositionRejected,
		goalContinuationDispositionDelivered:
		if err := retireGoalContinuationItem(
			coordinator,
			cursor.ItemID,
			firstNonEmptyString(cursor.DispositionReason, "restored-terminal-disposition"),
			firstNonZeroTime(cursor.DispositionAt, e.config.Clock().UTC()),
		); err != nil {
			return nil, err
		}
		return nil, nil
	case goalContinuationDispositionPending,
		goalContinuationDispositionAdmitting:
		if _, err := coordinator.enqueueDormantGoalContinuation(
			goalContinuationRuntimeItem(cursor),
		); err == nil {
			return nil, nil
		} else if pauseErr := e.pauseRestoredGoalContinuation(
			coordinator,
			cursor.ItemID,
			goalContinuationDispositionRejected,
			"recovery-item-conflict",
		); pauseErr != nil {
			return nil, fmt.Errorf(
				"reconcile Goal continuation item: %w; fail closed: %w",
				err,
				pauseErr,
			)
		}
		return []string{
			"paused Goal because its durable continuation item conflicted during recovery",
		}, nil
	default:
		return nil, fmt.Errorf(
			"unsupported restored Goal continuation disposition %q",
			cursor.Disposition,
		)
	}
}

func (e *QueryEngine) pauseRestoredGoalContinuation(
	coordinator *RuntimeInputCoordinator,
	itemID string,
	disposition string,
	reason string,
) error {
	now := e.config.Clock().UTC()
	e.planMu.Lock()
	defer e.planMu.Unlock()
	e.goalMu.Lock()
	defer e.goalMu.Unlock()
	current := e.goalState
	if current == nil ||
		current.Continuation == nil ||
		current.Continuation.ItemID != strings.TrimSpace(itemID) {
		return nil
	}
	next, err := nextGoalRevision(current, now)
	if err != nil {
		return err
	}
	next.Status = goalStatusPaused
	next.StatusReasonCode = goalReasonColdContinuationUnavailable
	next.StatusReason = "automatic continuation was paused during durable recovery"
	next.Continuation = cloneGoalContinuationCursor(current.Continuation)
	next.Continuation.Disposition = disposition
	next.Continuation.DispositionReason = reason
	next.Continuation.DispositionAt = now
	resetGoalTurnEvidence(next)
	if err := e.persistSessionCheckpointMessagesLocked("", nil, next); err != nil {
		return fmt.Errorf("persist recovered Goal continuation disposition: %w", err)
	}
	e.goalState = cloneGoalState(next)
	if disposition == goalContinuationDispositionRejected {
		return retireGoalContinuationItem(
			coordinator,
			itemID,
			reason,
			now,
		)
	}
	return coordinator.Settle(itemID)
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}
