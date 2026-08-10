package engine

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

type goalStatus string

const (
	goalStatusActive        goalStatus = "active"
	goalStatusPaused        goalStatus = "paused"
	goalStatusBlocked       goalStatus = "blocked"
	goalStatusUsageLimited  goalStatus = "usage_limited"
	goalStatusBudgetLimited goalStatus = "budget_limited"
	goalStatusComplete      goalStatus = "complete"
)

const (
	goalReasonBudgetRequired              = "budget-required"
	goalReasonUserPaused                  = "user-paused"
	goalReasonUserCancelled               = "user-cancelled"
	goalReasonColdContinuationUnavailable = "cold-continuation-unavailable"
	goalReasonUnsupportedVersion          = "unsupported-version"
	goalReasonCorruptState                = "corrupt-state"
	goalReasonUsageCoverageIncomplete     = "usage-coverage-incomplete"
	goalReasonBudgetExhausted             = "budget-exhausted"
)

var (
	errGoalNotFound                   = errors.New("goal does not exist")
	errGoalAlreadyExists              = errors.New("an unfinished goal already exists")
	errGoalUnavailable                = errors.New("goal state is unavailable")
	errGoalUnsupportedScope           = errors.New("goal requires a saved root session")
	errGoalPlanConflict               = errors.New("goal and Plan cannot be active together")
	errGoalTerminal                   = errors.New("completed goal cannot transition")
	errGoalBudget                     = errors.New("goal token budget has no remaining capacity")
	errGoalUsageUnavailable           = errors.New("goal usage availability has not been revalidated")
	errGoalTransitionInFlight         = errors.New("cannot change Goal state while a Goal turn is active")
	errGoalUsageCapabilityUnavailable = errors.New("goal usage reporting capability is unavailable")
	errGoalUsageAdmissionPending      = errors.New("a Goal provider admission is still unaccounted")
	errGoalUsageCoverageIncomplete    = errors.New("goal provider usage coverage is incomplete")
)

// goalState is the QueryEngine-owned lifecycle snapshot. It is deliberately
// presentation-free and cannot carry callbacks, provider handles, permission
// authority, or runtime-input ownership.
type goalState struct {
	GoalID                             string
	Objective                          string
	ObjectiveRevision                  uint64
	Status                             goalStatus
	StatusReasonCode                   string
	StatusReason                       string
	Revision                           uint64
	TokenBudget                        *uint64
	TokensUsed                         uint64
	UsageLedgerRevision                uint64
	RootActiveTimeMillis               int64
	ContinuationOrdinal                uint64
	Continuation                       *goalContinuationCursor
	LastGoalTurnID                     string
	LastTerminalSequence               uint64
	PendingCompleteTurnID              string
	PendingCompleteObjectiveRevision   uint64
	PendingUsageAdmission              *goalUsageAdmission
	BlockerKey                         string
	BlockerTurnIDs                     []string
	CreatedAt                          time.Time
	UpdatedAt                          time.Time
	unavailable                        bool
	unavailablePersistedRepresentation *session.PersistedGoalState
}

type goalCreateRequest struct {
	Objective   string
	TokenBudget *uint64
}

// goalService is the sole transition owner for one QueryEngine Goal. Callers
// must not mutate QueryEngine.goalState directly.
type goalService struct {
	engine            *QueryEngine
	usageGateOnce     sync.Once
	usageGate         chan struct{}
	usageUncertain    atomic.Bool
	recordGoalUsageFn func(transcript.GoalUsageRecord) error
}

func (s *goalService) snapshot() *goalState {
	if s == nil || s.engine == nil {
		return nil
	}
	s.engine.goalMu.Lock()
	defer s.engine.goalMu.Unlock()
	return cloneGoalState(s.engine.goalState)
}

func (s *goalService) create(
	request goalCreateRequest,
	ownerTurnID ...string,
) (*goalState, error) {
	objective, err := normalizeGoalObjective(request.Objective)
	if err != nil {
		return nil, err
	}
	if request.TokenBudget != nil && *request.TokenBudget == 0 {
		return nil, errGoalBudget
	}
	return s.transition(GoalLifecycleCreated, firstGoalOwnerTurnID(ownerTurnID), func(
		current *goalState,
		planState PlanState,
		now time.Time,
	) (*goalState, error) {
		if planPhaseRequiresContainment(planState.Phase) {
			return nil, errGoalPlanConflict
		}
		if current != nil && current.Status != goalStatusComplete {
			return nil, errGoalAlreadyExists
		}
		return &goalState{
			GoalID:            generateUUID(),
			Objective:         objective,
			ObjectiveRevision: 1,
			Status:            goalStatusActive,
			Revision:          1,
			TokenBudget:       cloneUint64(request.TokenBudget),
			CreatedAt:         now,
			UpdatedAt:         now,
		}, nil
	})
}

func (s *goalService) edit(
	objective string,
	ownerTurnID ...string,
) (*goalState, error) {
	normalized, err := normalizeGoalObjective(objective)
	if err != nil {
		return nil, err
	}
	return s.transition(
		GoalLifecycleObjectiveEdited,
		firstGoalOwnerTurnID(ownerTurnID),
		func(
			current *goalState,
			_ PlanState,
			now time.Time,
		) (*goalState, error) {
			if err := mutableGoalStateError(current); err != nil {
				return nil, err
			}
			if current.Status == goalStatusComplete {
				return nil, errGoalTerminal
			}
			next := cloneGoalState(current)
			if next.ObjectiveRevision == math.MaxUint64 || next.Revision == math.MaxUint64 {
				return nil, fmt.Errorf("goal revision exhausted")
			}
			next.Objective = normalized
			next.ObjectiveRevision++
			next.Revision++
			next.UpdatedAt = now
			resetGoalTurnEvidence(next)
			return next, nil
		},
	)
}

func (s *goalService) pause(ownerTurnID ...string) (*goalState, error) {
	return s.transition(GoalLifecyclePaused, firstGoalOwnerTurnID(ownerTurnID), func(
		current *goalState,
		_ PlanState,
		now time.Time,
	) (*goalState, error) {
		if err := mutableGoalStateError(current); err != nil {
			return nil, err
		}
		if current.Status == goalStatusComplete {
			return nil, errGoalTerminal
		}
		if current.Status == goalStatusPaused &&
			current.StatusReasonCode == goalReasonUserPaused {
			return cloneGoalState(current), nil
		}
		next, err := nextGoalRevision(current, now)
		if err != nil {
			return nil, err
		}
		next.Status = goalStatusPaused
		next.StatusReasonCode = goalReasonUserPaused
		next.StatusReason = "paused by user"
		resetGoalTurnEvidence(next)
		return next, nil
	})
}

func (s *goalService) resume(ownerTurnID ...string) (*goalState, error) {
	return s.transition(GoalLifecycleResumed, firstGoalOwnerTurnID(ownerTurnID), func(
		current *goalState,
		planState PlanState,
		now time.Time,
	) (*goalState, error) {
		if err := mutableGoalStateError(current); err != nil {
			return nil, err
		}
		if planPhaseRequiresContainment(planState.Phase) {
			return nil, errGoalPlanConflict
		}
		if current.Status == goalStatusComplete {
			return nil, errGoalTerminal
		}
		if current.Status == goalStatusActive {
			return cloneGoalState(current), nil
		}
		if current.Status == goalStatusUsageLimited {
			return nil, errGoalUsageUnavailable
		}
		if current.TokenBudget != nil &&
			*current.TokenBudget <= current.TokensUsed {
			return nil, errGoalBudget
		}
		next, err := nextGoalRevision(current, now)
		if err != nil {
			return nil, err
		}
		next.Status = goalStatusActive
		next.StatusReasonCode = ""
		next.StatusReason = ""
		resetGoalTurnEvidence(next)
		return next, nil
	})
}

func (s *goalService) setBudget(
	tokenBudget uint64,
	ownerTurnID ...string,
) (*goalState, error) {
	if tokenBudget == 0 {
		return nil, errGoalBudget
	}
	return s.transition(
		GoalLifecycleBudgetUpdated,
		firstGoalOwnerTurnID(ownerTurnID),
		func(
			current *goalState,
			_ PlanState,
			now time.Time,
		) (*goalState, error) {
			if err := mutableGoalStateError(current); err != nil {
				return nil, err
			}
			if current.Status == goalStatusComplete {
				return nil, errGoalTerminal
			}
			next, err := nextGoalRevision(current, now)
			if err != nil {
				return nil, err
			}
			next.TokenBudget = &tokenBudget
			if next.Status == goalStatusActive && tokenBudget <= next.TokensUsed {
				next.Status = goalStatusBudgetLimited
				next.StatusReasonCode = "budget-exhausted"
				next.StatusReason = "the Goal token budget has been reached"
				resetGoalTurnEvidence(next)
			}
			return next, nil
		},
	)
}

func (s *goalService) clear() error {
	return s.clearForTurn("")
}

func (s *goalService) clearForTurn(ownerTurnID string) error {
	if s == nil || s.engine == nil {
		return errGoalNotFound
	}
	e := s.engine
	coordinator, _, coordinatorErr := e.runtimeInputOwner()
	e.planMu.Lock()
	defer e.planMu.Unlock()
	e.goalMu.Lock()
	defer e.goalMu.Unlock()
	owner := strings.TrimSpace(ownerTurnID)
	if e.planActiveTurnID != "" && e.planActiveTurnID != owner {
		return fmt.Errorf(
			"%w: turn %s owns the boundary",
			errGoalTransitionInFlight,
			e.planActiveTurnID,
		)
	}
	if e.goalState == nil {
		return nil
	}
	if e.goalState.PendingUsageAdmission != nil {
		return errGoalUsageAdmissionPending
	}
	if err := e.validateGoalScopeLocked(); err != nil {
		return err
	}
	if e.goalState.Continuation != nil &&
		(e.goalState.Continuation.Disposition == goalContinuationDispositionPending ||
			e.goalState.Continuation.Disposition == goalContinuationDispositionAdmitting) {
		if coordinatorErr != nil {
			return coordinatorErr
		}
		e.mu.Lock()
		clock := e.config.Clock
		e.mu.Unlock()
		if clock == nil {
			clock = time.Now
		}
		rejected, err := nextGoalRevision(e.goalState, clock().UTC())
		if err != nil {
			return err
		}
		rejected.Continuation = cloneGoalContinuationCursor(
			e.goalState.Continuation,
		)
		rejected.Continuation.Disposition = goalContinuationDispositionRejected
		rejected.Continuation.DispositionReason = "superseded-by-goal-clear"
		rejected.Continuation.DispositionAt = rejected.UpdatedAt
		if err := e.persistSessionCheckpointMessagesLocked(
			"",
			nil,
			rejected,
		); err != nil {
			return fmt.Errorf("reject Goal continuation before clear: %w", err)
		}
		e.goalState = cloneGoalState(rejected)
		if err := retireGoalContinuationItem(
			coordinator,
			rejected.Continuation.ItemID,
			rejected.Continuation.DispositionReason,
			rejected.Continuation.DispositionAt,
		); err != nil {
			return err
		}
	}
	if err := e.persistSessionCheckpointMessagesLocked("", nil, nil); err != nil {
		return fmt.Errorf("clear goal checkpoint: %w", err)
	}
	cleared := cloneGoalState(e.goalState)
	e.goalState = nil
	e.goalTurnRuntime = nil
	e.mu.Lock()
	clock := e.config.Clock
	sessionID := e.config.SessionID
	threadID := e.config.ThreadID
	agentID := e.config.AgentID
	e.mu.Unlock()
	if clock == nil {
		clock = time.Now
	}
	if cleared != nil &&
		!cleared.unavailable &&
		strings.TrimSpace(cleared.GoalID) != "" {
		e.projectGoalLifecycle(
			GoalLifecycleCleared,
			cleared,
			"",
			clock().UTC(),
		)
	} else if e.runtimeState != nil {
		_ = e.runtimeState.RestoreGoalSnapshot(
			sessionID,
			threadID,
			agentID,
			nil,
			clock().UTC(),
		)
	}
	return nil
}

func (s *goalService) transition(
	phase GoalLifecyclePhase,
	ownerTurnID string,
	build func(*goalState, PlanState, time.Time) (*goalState, error),
) (*goalState, error) {
	if s == nil || s.engine == nil {
		return nil, errGoalUnsupportedScope
	}
	e := s.engine
	coordinator, _, coordinatorErr := e.runtimeInputOwner()
	e.goalProviderBoundary.Lock()
	defer e.goalProviderBoundary.Unlock()
	e.planMu.Lock()
	defer e.planMu.Unlock()
	e.goalMu.Lock()
	defer e.goalMu.Unlock()
	if err := e.validateGoalScopeLocked(); err != nil {
		return nil, err
	}
	if (e.planActiveTurnID != "" && e.planActiveTurnID != ownerTurnID) ||
		e.goalTurnRuntime != nil {
		owner := e.planActiveTurnID
		if owner == "" && e.goalTurnRuntime != nil {
			owner = e.goalTurnRuntime.identity.GoalTurnID
		}
		return nil, fmt.Errorf(
			"%w: turn %s owns the boundary",
			errGoalTransitionInFlight,
			owner,
		)
	}
	e.mu.Lock()
	clock := e.config.Clock
	e.mu.Unlock()
	if clock == nil {
		clock = time.Now
	}
	next, err := build(cloneGoalState(e.goalState), e.planState, clock().UTC())
	if err != nil {
		return nil, err
	}
	if next == nil {
		return nil, fmt.Errorf("goal transition produced no state")
	}
	if e.goalState != nil &&
		next.GoalID == e.goalState.GoalID &&
		next.Revision == e.goalState.Revision {
		return cloneGoalState(e.goalState), nil
	}
	var retiredContinuation *goalContinuationCursor
	if e.goalState != nil &&
		e.goalState.Continuation != nil &&
		(e.goalState.Continuation.Disposition == goalContinuationDispositionPending ||
			e.goalState.Continuation.Disposition == goalContinuationDispositionAdmitting) {
		if coordinatorErr != nil {
			return nil, coordinatorErr
		}
		next.Continuation = cloneGoalContinuationCursor(e.goalState.Continuation)
		next.Continuation.Disposition = goalContinuationDispositionRejected
		next.Continuation.DispositionReason = goalContinuationTransitionRejection(phase)
		next.Continuation.DispositionAt = next.UpdatedAt
		retiredContinuation = cloneGoalContinuationCursor(next.Continuation)
	}
	if err := e.persistSessionCheckpointMessagesLocked("", nil, next); err != nil {
		return nil, fmt.Errorf("persist goal transition: %w", err)
	}
	e.goalState = cloneGoalState(next)
	if retiredContinuation != nil {
		if err := retireGoalContinuationItem(
			coordinator,
			retiredContinuation.ItemID,
			retiredContinuation.DispositionReason,
			retiredContinuation.DispositionAt,
		); err != nil {
			e.recordRuntimeStateError(err)
		}
	}
	if e.goalState != nil && e.goalState.Status != goalStatusActive {
		e.goalTurnRuntime = nil
	}
	e.projectGoalLifecycle(phase, next, "", next.UpdatedAt)
	return cloneGoalState(next), nil
}

func firstGoalOwnerTurnID(ownerTurnIDs []string) string {
	if len(ownerTurnIDs) == 0 {
		return ""
	}
	return strings.TrimSpace(ownerTurnIDs[0])
}

// validateGoalScopeLocked requires planMu and goalMu. It reads engine identity
// under engine.mu, preserving the global planMu -> goalMu -> engine.mu order.
func (e *QueryEngine) validateGoalScopeLocked() error {
	if e == nil || e.administrationOnly {
		return errGoalUnsupportedScope
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if strings.TrimSpace(e.config.SessionID) == "" ||
		strings.TrimSpace(e.config.ThreadID) == "" ||
		strings.TrimSpace(e.config.AgentID) != "" ||
		e.transcript == nil ||
		strings.TrimSpace(e.transcript.Path()) == "" {
		return errGoalUnsupportedScope
	}
	return nil
}

func mutableGoalStateError(state *goalState) error {
	switch {
	case state == nil:
		return errGoalNotFound
	case state.unavailable:
		return errGoalUnavailable
	case state.PendingUsageAdmission != nil:
		return errGoalUsageAdmissionPending
	default:
		return nil
	}
}

func nextGoalRevision(state *goalState, now time.Time) (*goalState, error) {
	if state == nil {
		return nil, errGoalNotFound
	}
	if state.Revision == math.MaxUint64 {
		return nil, fmt.Errorf("goal revision exhausted")
	}
	next := cloneGoalState(state)
	next.Revision++
	next.UpdatedAt = now
	return next, nil
}

func resetGoalTurnEvidence(state *goalState) {
	if state == nil {
		return
	}
	state.PendingCompleteTurnID = ""
	state.PendingCompleteObjectiveRevision = 0
	state.BlockerKey = ""
	state.BlockerTurnIDs = nil
}

func goalBlocksPlan(state *goalState) bool {
	return state != nil && !state.unavailable && state.Status == goalStatusActive
}

func cloneGoalState(state *goalState) *goalState {
	if state == nil {
		return nil
	}
	cloned := *state
	cloned.TokenBudget = cloneUint64(state.TokenBudget)
	cloned.Continuation = cloneGoalContinuationCursor(state.Continuation)
	cloned.BlockerTurnIDs = append([]string(nil), state.BlockerTurnIDs...)
	cloned.PendingUsageAdmission = cloneGoalUsageAdmission(
		state.PendingUsageAdmission,
	)
	return &cloned
}

func cloneUint64(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
