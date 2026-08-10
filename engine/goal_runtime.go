package engine

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

// GoalLifecyclePhase identifies one ordered Goal read-model transition. These
// values describe engine-owned state changes; they are not commands or model
// authorities.
type GoalLifecyclePhase string

const (
	GoalLifecycleCreated                GoalLifecyclePhase = "created"
	GoalLifecycleObjectiveEdited        GoalLifecyclePhase = "objective_edited"
	GoalLifecyclePaused                 GoalLifecyclePhase = "paused"
	GoalLifecycleResumed                GoalLifecyclePhase = "resumed"
	GoalLifecycleBudgetUpdated          GoalLifecyclePhase = "budget_updated"
	GoalLifecycleCleared                GoalLifecyclePhase = "cleared"
	GoalLifecycleTurnStarted            GoalLifecyclePhase = "turn_started"
	GoalLifecyclePermissionWaiting      GoalLifecyclePhase = "permission_waiting"
	GoalLifecyclePermissionResumed      GoalLifecyclePhase = "permission_resumed"
	GoalLifecycleChildWaiting           GoalLifecyclePhase = "child_waiting"
	GoalLifecycleChildResumed           GoalLifecyclePhase = "child_resumed"
	GoalLifecycleCompletionRequested    GoalLifecyclePhase = "completion_requested"
	GoalLifecycleBlockerReported        GoalLifecyclePhase = "blocker_reported"
	GoalLifecycleBlocked                GoalLifecyclePhase = "blocked"
	GoalLifecycleTurnFinished           GoalLifecyclePhase = "turn_finished"
	GoalLifecycleUsageRecorded          GoalLifecyclePhase = "usage_recorded"
	GoalLifecycleUsageLimited           GoalLifecyclePhase = "usage_limited"
	GoalLifecycleBudgetLimited          GoalLifecyclePhase = "budget_limited"
	GoalLifecycleUsageAdmissionReleased GoalLifecyclePhase = "usage_admission_released"
)

// GoalUsageAdmissionSnapshot is a detached durable identity projection. It
// carries no provider handle or mutation authority.
type GoalUsageAdmissionSnapshot struct {
	Version                  uint16
	LedgerRevision           uint64
	GoalID                   string
	ObjectiveRevision        uint64
	RootSessionID            string
	RootThreadID             string
	RootAgentID              string
	ExecutingSessionID       string
	ExecutingThreadID        string
	ExecutingAgentID         string
	ExecutingAgentGeneration int64
	GoalTurnID               string
	LogicalRoundID           string
	ProviderCallID           string
	AdmittedAt               time.Time
}

// GoalSnapshot is a detached, presentation-free inspection result. It exposes
// durable facts only and grants no continuation, tool, provider, or mutation
// authority.
type GoalSnapshot struct {
	GoalID                           string
	Objective                        string
	ObjectiveRevision                uint64
	Status                           string
	StatusReasonCode                 string
	StatusReason                     string
	Revision                         uint64
	TokenBudget                      *uint64
	TokensUsed                       uint64
	UsageLedgerRevision              uint64
	UsageCoverage                    string
	PendingUsageAdmission            *GoalUsageAdmissionSnapshot
	RootActiveTimeMillis             int64
	ContinuationOrdinal              uint64
	LastGoalTurnID                   string
	LastTerminalSequence             uint64
	PendingCompleteTurnID            string
	PendingCompleteObjectiveRevision uint64
	BlockerKey                       string
	BlockerTurnIDs                   []string
	CreatedAt                        time.Time
	UpdatedAt                        time.Time
	Available                        bool
}

// GoalLifecycleEvent is the replayable reducer payload for one committed Goal
// snapshot. Root identity and ordering live in RuntimeEventEnvelope.
type GoalLifecycleEvent struct {
	Phase GoalLifecyclePhase
	Goal  GoalSnapshot
}

// goalExecutionIdentity is a process-local immutable attribution binding. A
// child receives a detached copy and can emit attributed events, but it never
// receives the root goalService or mutation authority.
type goalExecutionIdentity struct {
	GoalID            string
	ObjectiveRevision uint64
	RootSessionID     string
	RootThreadID      string
	RootAgentID       string
	GoalTurnID        string
}

// goalTurnRuntime is intentionally process-local. Durable elapsed time is
// checkpointed whenever root execution enters or leaves an excluded wait.
type goalTurnRuntime struct {
	identity    goalExecutionIdentity
	activeSince time.Time
	waiting     map[string]struct{}
}

type goalCompletionRequest struct {
	GoalID            string
	ObjectiveRevision uint64
	TurnID            string
}

type goalBlockerRequest struct {
	GoalID            string
	ObjectiveRevision uint64
	TurnID            string
	Reason            string
	BlockerKey        string
}

// GoalSnapshot returns a defensive copy of the current engine-owned Goal.
func (e *QueryEngine) GoalSnapshot() (*GoalSnapshot, bool) {
	if e == nil || e.goalService == nil {
		return nil, false
	}
	state := e.goalService.snapshot()
	if state == nil {
		return nil, false
	}
	snapshot := goalSnapshotFromState(state)
	return &snapshot, true
}

func goalSnapshotFromState(state *goalState) GoalSnapshot {
	if state == nil {
		return GoalSnapshot{}
	}
	return GoalSnapshot{
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
		UsageCoverage:                    goalUsageCoverage(state),
		PendingUsageAdmission:            goalUsageAdmissionSnapshot(state.PendingUsageAdmission),
		RootActiveTimeMillis:             state.RootActiveTimeMillis,
		ContinuationOrdinal:              state.ContinuationOrdinal,
		LastGoalTurnID:                   state.LastGoalTurnID,
		LastTerminalSequence:             state.LastTerminalSequence,
		PendingCompleteTurnID:            state.PendingCompleteTurnID,
		PendingCompleteObjectiveRevision: state.PendingCompleteObjectiveRevision,
		BlockerKey:                       state.BlockerKey,
		BlockerTurnIDs:                   append([]string(nil), state.BlockerTurnIDs...),
		CreatedAt:                        state.CreatedAt,
		UpdatedAt:                        state.UpdatedAt,
		Available:                        !state.unavailable,
	}
}

func cloneGoalSnapshot(snapshot GoalSnapshot) GoalSnapshot {
	cloned := snapshot
	cloned.TokenBudget = cloneUint64(snapshot.TokenBudget)
	cloned.BlockerTurnIDs = append([]string(nil), snapshot.BlockerTurnIDs...)
	if snapshot.PendingUsageAdmission != nil {
		pending := *snapshot.PendingUsageAdmission
		cloned.PendingUsageAdmission = &pending
	}
	return cloned
}

func cloneGoalExecutionIdentity(identity *goalExecutionIdentity) *goalExecutionIdentity {
	if identity == nil {
		return nil
	}
	cloned := *identity
	return &cloned
}

func (i goalExecutionIdentity) valid() bool {
	return validGoalBindingText(i.GoalID, true) &&
		i.ObjectiveRevision > 0 &&
		validGoalBindingText(i.RootSessionID, true) &&
		validGoalBindingText(i.RootThreadID, true) &&
		validGoalBindingText(i.RootAgentID, false) &&
		(i.GoalTurnID == "" || validateGoalTurnID(i.GoalTurnID) == nil)
}

func (i goalExecutionIdentity) decorate(envelope *RuntimeEventEnvelope) {
	if envelope == nil || !i.valid() {
		return
	}
	envelope.GoalID = i.GoalID
	envelope.GoalObjectiveRevision = i.ObjectiveRevision
	envelope.GoalRootSessionID = i.RootSessionID
	envelope.GoalRootThreadID = i.RootThreadID
	envelope.GoalRootAgentID = i.RootAgentID
	envelope.GoalTurnID = i.GoalTurnID
}

func goalLifecycleQueryEvent(
	phase GoalLifecyclePhase,
	state *goalState,
) QueryEvent {
	snapshot := goalSnapshotFromState(state)
	return QueryEvent{
		Type: EventGoalLifecycle,
		RuntimeEventEnvelope: RuntimeEventEnvelope{
			CausationID: fmt.Sprintf(
				"goal:%s:%d",
				snapshot.GoalID,
				snapshot.Revision,
			),
		},
		GoalLifecycle: &GoalLifecycleEvent{
			Phase: phase,
			Goal:  snapshot,
		},
	}
}

// projectGoalLifecycle applies an already-durable state change to the bounded
// runtime projection without publishing it through a transport. Turn-owned
// callers instead return the event to turnEventEmitter so channel order stays
// identical to reducer order.
func (e *QueryEngine) projectGoalLifecycle(
	phase GoalLifecyclePhase,
	state *goalState,
	turnID string,
	now time.Time,
) {
	if e == nil || state == nil || e.runtimeState == nil {
		return
	}
	e.mu.Lock()
	identity := RuntimeEventEnvelope{
		SessionID: e.config.SessionID,
		ThreadID:  e.config.ThreadID,
		AgentID:   e.config.AgentID,
	}
	e.mu.Unlock()
	goalIdentity := goalExecutionIdentity{
		GoalID:            state.GoalID,
		ObjectiveRevision: state.ObjectiveRevision,
		RootSessionID:     identity.SessionID,
		RootThreadID:      identity.ThreadID,
		RootAgentID:       identity.AgentID,
		GoalTurnID:        firstNonEmptyString(turnID, state.LastGoalTurnID),
	}
	goalIdentity.decorate(&identity)
	if strings.TrimSpace(turnID) == "" {
		turnID = fmt.Sprintf("external-goal:%s:%d", state.GoalID, state.Revision)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	event := goalLifecycleQueryEvent(phase, state)
	_, _ = e.decorateRuntimeEventWithIdentity(
		turnID,
		identity,
		func() time.Time { return now },
		event,
	)
}

func (e *QueryEngine) currentGoalExecutionIdentity() *goalExecutionIdentity {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	configBinding := cloneGoalExecutionIdentity(e.config.goalBinding)
	agentID := e.config.AgentID
	sessionID := e.config.SessionID
	threadID := e.config.ThreadID
	e.mu.Unlock()
	if configBinding != nil {
		return configBinding
	}
	if strings.TrimSpace(agentID) != "" {
		return nil
	}
	e.goalMu.Lock()
	defer e.goalMu.Unlock()
	if e.goalState == nil ||
		e.goalState.unavailable ||
		e.goalState.Status != goalStatusActive ||
		e.goalTurnRuntime == nil {
		return nil
	}
	identity := e.goalTurnRuntime.identity
	identity.RootSessionID = sessionID
	identity.RootThreadID = threadID
	return &identity
}

func (s *goalService) beginTurn(
	turnID string,
	userSteering bool,
	continuation *RuntimeItem,
	now time.Time,
) (QueryEvent, *goalExecutionIdentity, error) {
	if s == nil || s.engine == nil {
		return QueryEvent{}, nil, errGoalUnsupportedScope
	}
	if err := validateGoalTurnID(turnID); err != nil {
		return QueryEvent{}, nil, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	e := s.engine
	var coordinator *RuntimeInputCoordinator
	var scope RuntimeInputScope
	if continuation != nil || userSteering {
		var ownerErr error
		coordinator, scope, ownerErr = e.runtimeInputOwner()
		if ownerErr != nil {
			return QueryEvent{}, nil, ownerErr
		}
	}
	e.planMu.Lock()
	defer e.planMu.Unlock()
	e.goalMu.Lock()
	defer e.goalMu.Unlock()
	if e.planActiveTurnID != turnID {
		return QueryEvent{}, nil, fmt.Errorf(
			"goal turn %q does not own active boundary %q",
			turnID,
			e.planActiveTurnID,
		)
	}
	current := e.goalState
	if current == nil || current.unavailable || current.Status != goalStatusActive {
		return QueryEvent{}, nil, nil
	}
	if current.PendingUsageAdmission != nil {
		return QueryEvent{}, nil, errGoalUsageAdmissionPending
	}
	if err := e.validateGoalScopeLocked(); err != nil {
		return QueryEvent{}, nil, err
	}
	if e.goalTurnRuntime != nil {
		return QueryEvent{}, nil, fmt.Errorf(
			"goal turn %q is already active",
			e.goalTurnRuntime.identity.GoalTurnID,
		)
	}
	var next *goalState
	var err error
	if continuation != nil {
		next, err = s.beginContinuationTurn(
			turnID,
			*continuation,
			coordinator,
			scope,
			now,
		)
	} else {
		next, err = nextGoalRevision(current, now)
		if err == nil {
			next.LastGoalTurnID = turnID
			if userSteering {
				resetGoalTurnEvidence(next)
			}
			if next.Continuation != nil &&
				(next.Continuation.Disposition == goalContinuationDispositionPending ||
					next.Continuation.Disposition == goalContinuationDispositionAdmitting) {
				next.Continuation = cloneGoalContinuationCursor(next.Continuation)
				next.Continuation.Disposition = goalContinuationDispositionRejected
				next.Continuation.DispositionReason = "superseded-by-new-goal-turn"
				if userSteering {
					next.Continuation.DispositionReason = "superseded-by-user-steering"
				}
				next.Continuation.DispositionAt = now
			}
			err = e.persistSessionCheckpointMessagesLocked("", nil, next)
			if err != nil {
				err = fmt.Errorf("persist Goal turn admission: %w", err)
			}
		}
	}
	if err != nil {
		return QueryEvent{}, nil, err
	}
	if continuation == nil &&
		next.Continuation != nil &&
		next.Continuation.Disposition == goalContinuationDispositionRejected {
		if retireErr := retireGoalContinuationItem(
			coordinator,
			next.Continuation.ItemID,
			next.Continuation.DispositionReason,
			now,
		); retireErr != nil {
			e.goalState = cloneGoalState(next)
			return QueryEvent{}, nil, retireErr
		}
	}
	e.mu.Lock()
	identity := goalExecutionIdentity{
		GoalID:            next.GoalID,
		ObjectiveRevision: next.ObjectiveRevision,
		RootSessionID:     e.config.SessionID,
		RootThreadID:      e.config.ThreadID,
		RootAgentID:       e.config.AgentID,
		GoalTurnID:        turnID,
	}
	e.mu.Unlock()
	e.goalState = cloneGoalState(next)
	e.goalTurnRuntime = &goalTurnRuntime{
		identity:    identity,
		activeSince: now,
		waiting:     make(map[string]struct{}),
	}
	event := goalLifecycleQueryEvent(GoalLifecycleTurnStarted, next)
	return event, &identity, nil
}

func (s *goalService) requestCompletion(
	request goalCompletionRequest,
	now time.Time,
) (*goalState, error) {
	return s.commitTurnEvidence(
		GoalLifecycleCompletionRequested,
		request.GoalID,
		request.ObjectiveRevision,
		request.TurnID,
		now,
		func(next *goalState, _ time.Time) error {
			next.PendingCompleteTurnID = request.TurnID
			next.PendingCompleteObjectiveRevision = request.ObjectiveRevision
			return nil
		},
	)
}

func (s *goalService) reportBlocker(
	request goalBlockerRequest,
	now time.Time,
) (*goalState, error) {
	reason, err := normalizeGoalReason(request.Reason)
	if err != nil {
		return nil, err
	}
	key, err := normalizeGoalBlockerKey(request.BlockerKey)
	if err != nil {
		return nil, err
	}
	phase := GoalLifecycleBlockerReported
	state, err := s.commitTurnEvidence(
		phase,
		request.GoalID,
		request.ObjectiveRevision,
		request.TurnID,
		now,
		func(next *goalState, transitionAt time.Time) error {
			if next.BlockerKey != key {
				next.BlockerKey = key
				next.BlockerTurnIDs = nil
			}
			for _, existing := range next.BlockerTurnIDs {
				if existing == request.TurnID {
					return nil
				}
			}
			next.BlockerTurnIDs = append(next.BlockerTurnIDs, request.TurnID)
			if len(next.BlockerTurnIDs) < 3 {
				return nil
			}
			next.BlockerTurnIDs = append([]string(nil), next.BlockerTurnIDs[len(next.BlockerTurnIDs)-3:]...)
			next.Status = goalStatusBlocked
			next.StatusReasonCode = "model-blocker"
			next.StatusReason = reason
			s.stopActiveIntervalLocked(next, transitionAt)
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	return state, nil
}

func (s *goalService) commitTurnEvidence(
	phase GoalLifecyclePhase,
	goalID string,
	objectiveRevision uint64,
	turnID string,
	now time.Time,
	mutate func(*goalState, time.Time) error,
) (*goalState, error) {
	if s == nil || s.engine == nil {
		return nil, errGoalUnsupportedScope
	}
	if err := validateGoalTurnID(turnID); err != nil {
		return nil, err
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
	if err := mutableGoalStateError(current); err != nil {
		return nil, err
	}
	if current.Status != goalStatusActive ||
		current.GoalID != strings.TrimSpace(goalID) ||
		current.ObjectiveRevision != objectiveRevision ||
		current.LastGoalTurnID != turnID ||
		e.goalTurnRuntime == nil ||
		e.goalTurnRuntime.identity.GoalTurnID != turnID {
		return nil, fmt.Errorf("stale Goal turn evidence")
	}
	next, err := nextGoalRevision(current, now)
	if err != nil {
		return nil, err
	}
	beforeRevision := next.Revision
	if err := mutate(next, now); err != nil {
		return nil, err
	}
	// A duplicate same-turn blocker report is a strict no-op.
	if phase == GoalLifecycleBlockerReported &&
		next.BlockerKey == current.BlockerKey &&
		len(next.BlockerTurnIDs) == len(current.BlockerTurnIDs) {
		return cloneGoalState(current), nil
	}
	if next.Revision != beforeRevision {
		return nil, fmt.Errorf("goal evidence mutated revision directly")
	}
	if err := e.persistSessionCheckpointMessagesLocked("", nil, next); err != nil {
		return nil, fmt.Errorf("persist Goal turn evidence: %w", err)
	}
	e.goalState = cloneGoalState(next)
	projected := cloneGoalState(next)
	if phase == GoalLifecycleBlockerReported &&
		projected.Status == goalStatusBlocked {
		phase = GoalLifecycleBlocked
	}
	e.projectGoalLifecycle(phase, projected, turnID, now)
	return cloneGoalState(next), nil
}

func (s *goalService) stopActiveIntervalLocked(state *goalState, now time.Time) {
	if s == nil || s.engine == nil || state == nil {
		return
	}
	runtime := s.engine.goalTurnRuntime
	if runtime == nil || runtime.activeSince.IsZero() {
		return
	}
	if now.After(runtime.activeSince) {
		elapsed := now.Sub(runtime.activeSince).Milliseconds()
		if elapsed > 0 {
			if state.RootActiveTimeMillis > math.MaxInt64-elapsed {
				state.RootActiveTimeMillis = math.MaxInt64
			} else {
				state.RootActiveTimeMillis += elapsed
			}
		}
	}
	runtime.activeSince = time.Time{}
}

func (s *goalService) pauseTurnFor(
	reason string,
	phase GoalLifecyclePhase,
	now time.Time,
) (QueryEvent, bool, error) {
	if s == nil || s.engine == nil {
		return QueryEvent{}, false, nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return QueryEvent{}, false, fmt.Errorf("goal wait reason is required")
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
	runtime := e.goalTurnRuntime
	current := e.goalState
	if runtime == nil ||
		current == nil ||
		current.unavailable ||
		current.Status != goalStatusActive {
		return QueryEvent{}, false, nil
	}
	if _, duplicate := runtime.waiting[reason]; duplicate {
		return QueryEvent{}, false, nil
	}
	next, err := nextGoalRevision(current, now)
	if err != nil {
		return QueryEvent{}, false, err
	}
	previousActiveSince := runtime.activeSince
	if len(runtime.waiting) == 0 {
		s.stopActiveIntervalLocked(next, now)
	}
	runtime.waiting[reason] = struct{}{}
	if err := e.persistSessionCheckpointMessagesLocked("", nil, next); err != nil {
		delete(runtime.waiting, reason)
		runtime.activeSince = previousActiveSince
		return QueryEvent{}, false, fmt.Errorf("persist Goal wait boundary: %w", err)
	}
	e.goalState = cloneGoalState(next)
	return goalLifecycleQueryEvent(phase, next), true, nil
}

func (s *goalService) resumeTurnFor(
	reason string,
	phase GoalLifecyclePhase,
	now time.Time,
) (QueryEvent, bool, error) {
	if s == nil || s.engine == nil {
		return QueryEvent{}, false, nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return QueryEvent{}, false, fmt.Errorf("goal wait reason is required")
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
	runtime := e.goalTurnRuntime
	current := e.goalState
	if runtime == nil || current == nil {
		return QueryEvent{}, false, nil
	}
	if _, waiting := runtime.waiting[reason]; !waiting {
		return QueryEvent{}, false, nil
	}
	next, err := nextGoalRevision(current, now)
	if err != nil {
		return QueryEvent{}, false, err
	}
	previousActiveSince := runtime.activeSince
	delete(runtime.waiting, reason)
	if len(runtime.waiting) == 0 &&
		next.Status == goalStatusActive {
		runtime.activeSince = now
	}
	if err := e.persistSessionCheckpointMessagesLocked("", nil, next); err != nil {
		runtime.waiting[reason] = struct{}{}
		runtime.activeSince = previousActiveSince
		return QueryEvent{}, false, fmt.Errorf("persist Goal wait release: %w", err)
	}
	e.goalState = cloneGoalState(next)
	return goalLifecycleQueryEvent(phase, next), true, nil
}

// abandonTurn releases only process-local execution ownership after a
// fail-closed terminal path. It never advances or fabricates durable Goal
// state; a later user turn or cold restore must re-enter through normal
// admission.
func (s *goalService) abandonTurn(goalTurnID string) {
	if s == nil || s.engine == nil {
		return
	}
	e := s.engine
	e.planMu.Lock()
	defer e.planMu.Unlock()
	e.goalMu.Lock()
	defer e.goalMu.Unlock()
	if e.goalTurnRuntime != nil &&
		e.goalTurnRuntime.identity.GoalTurnID == strings.TrimSpace(goalTurnID) {
		e.goalTurnRuntime = nil
	}
}

// commitGoalTerminalEvents commits Goal aftercare and reduces the Goal
// turn-finished event immediately before the terminal event under one
// planMu -> goalMu -> runtimeMu boundary. Terminal therefore remains the final
// published event while durable LastTerminalSequence binds its exact sequence.
func (e *QueryEngine) commitGoalTerminalEvents(
	turnID string,
	identity RuntimeEventEnvelope,
	clock func() time.Time,
	terminal QueryEvent,
) (QueryEvent, QueryEvent, error) {
	if e == nil || terminal.TerminalInfo == nil {
		return QueryEvent{}, QueryEvent{}, fmt.Errorf("goal terminal event is incomplete")
	}
	if clock == nil {
		clock = time.Now
	}
	e.goalProviderBoundary.Lock()
	defer e.goalProviderBoundary.Unlock()
	completeEnabled := e.goalWorkflowEnabled()
	at := clock().UTC()
	coordinator, scope, coordinatorErr := e.runtimeInputOwner()
	e.planMu.Lock()
	defer e.planMu.Unlock()
	e.goalMu.Lock()
	defer e.goalMu.Unlock()
	e.runtimeMu.Lock()
	defer e.runtimeMu.Unlock()

	runtime := e.goalTurnRuntime
	current := e.goalState
	if runtime == nil ||
		current == nil ||
		runtime.identity.GoalTurnID != identity.GoalTurnID ||
		current.GoalID != identity.GoalID ||
		current.ObjectiveRevision != identity.GoalObjectiveRevision {
		return QueryEvent{}, QueryEvent{}, fmt.Errorf("goal terminal identity is stale")
	}
	sequence := e.runtimeSequences[identity.ThreadID]
	if e.runtimeState != nil {
		if shared := e.runtimeState.LastSequence(identity.ThreadID); shared > sequence {
			sequence = shared
		}
	}
	if sequence > math.MaxUint64-2 {
		return QueryEvent{}, QueryEvent{}, fmt.Errorf("runtime sequence exhausted")
	}
	terminalSequence := sequence + 2
	next, err := nextGoalRevision(current, at)
	if err != nil {
		return QueryEvent{}, QueryEvent{}, err
	}
	previousActiveSince := runtime.activeSince
	e.goalService.stopActiveIntervalLocked(next, at)
	next.LastTerminalSequence = terminalSequence
	if terminal.TerminalInfo.Reason != TerminalCompleted &&
		next.PendingCompleteTurnID == identity.GoalTurnID {
		next.PendingCompleteTurnID = ""
		next.PendingCompleteObjectiveRevision = 0
	}
	if completeEnabled &&
		terminal.TerminalInfo.Reason == TerminalCompleted &&
		next.PendingCompleteTurnID == identity.GoalTurnID &&
		next.PendingCompleteObjectiveRevision == identity.GoalObjectiveRevision &&
		next.PendingUsageAdmission == nil &&
		coordinatorErr == nil &&
		coordinator != nil &&
		!hasPendingGoalSteering(coordinator.Snapshot(scope)) &&
		len(runtime.waiting) == 0 {
		next.Status = goalStatusComplete
		next.StatusReasonCode = ""
		next.StatusReason = "matching completion request reached the safe terminal boundary"
		resetGoalTurnEvidence(next)
	}
	if goalContinuationEligible(next, runtime, *terminal.TerminalInfo) {
		if coordinatorErr != nil || coordinator == nil {
			if coordinatorErr == nil {
				coordinatorErr = fmt.Errorf(
					"runtime input coordinator is unavailable",
				)
			}
			runtime.activeSince = previousActiveSince
			return QueryEvent{}, QueryEvent{}, coordinatorErr
		}
		cursor, cursorErr := newGoalContinuationCursor(
			next,
			scope,
			*terminal.TerminalInfo,
			terminalSequence,
			at,
			coordinator.Revision(),
		)
		if cursorErr != nil {
			runtime.activeSince = previousActiveSince
			return QueryEvent{}, QueryEvent{}, cursorErr
		}
		next.ContinuationOrdinal = cursor.ContinuationOrdinal
		next.Continuation = cursor
	}
	if err := e.persistSessionCheckpointMessagesLocked(
		string(terminal.TerminalInfo.Reason),
		nil,
		next,
	); err != nil {
		runtime.activeSince = previousActiveSince
		return QueryEvent{}, QueryEvent{}, fmt.Errorf(
			"persist Goal terminal boundary: %w",
			err,
		)
	}
	e.goalState = cloneGoalState(next)
	e.goalTurnRuntime = nil
	if next.Continuation != nil &&
		next.Continuation.Disposition == goalContinuationDispositionPending {
		if _, enqueueErr := coordinator.enqueueDormantGoalContinuation(
			goalContinuationRuntimeItem(next.Continuation),
		); enqueueErr != nil && e.runtimeStateErr == nil {
			// The cursor is already durable. Keep the completed terminal and
			// let restart reconcile the deterministic item without fabricating
			// another cursor or publishing a transport wake.
			e.runtimeStateErr = fmt.Errorf(
				"persist Goal continuation item: %w",
				enqueueErr,
			)
		}
	}

	fixedClock := func() time.Time { return at }
	goalEvent, accepted := e.decorateRuntimeEventWithIdentityLocked(
		turnID,
		identity,
		fixedClock,
		goalLifecycleQueryEvent(GoalLifecycleTurnFinished, next),
	)
	if !accepted {
		return QueryEvent{}, QueryEvent{}, fmt.Errorf(
			"goal turn-finished projection was rejected",
		)
	}
	terminalEvent, accepted := e.decorateRuntimeEventWithIdentityLocked(
		turnID,
		identity,
		fixedClock,
		terminal,
	)
	if !accepted || terminalEvent.Sequence != terminalSequence {
		return QueryEvent{}, QueryEvent{}, fmt.Errorf(
			"goal terminal projection sequence mismatch",
		)
	}
	return goalEvent, terminalEvent, nil
}

func hasPendingGoalSteering(items []RuntimeItem) bool {
	for _, item := range items {
		if item.State != RuntimeItemPending &&
			item.State != RuntimeItemProcessing {
			continue
		}
		if item.Kind == RuntimeItemUserPrompt ||
			item.Kind == RuntimeItemSteering {
			return true
		}
	}
	return false
}

func (s *goalService) beginForegroundChildWait(
	agentID string,
	generation int64,
) func() {
	if s == nil || s.engine == nil ||
		strings.TrimSpace(agentID) == "" ||
		generation <= 0 {
		return func() {}
	}
	e := s.engine
	e.mu.Lock()
	clock := e.config.Clock
	e.mu.Unlock()
	if clock == nil {
		clock = time.Now
	}
	reason := fmt.Sprintf("child:%s:%d", strings.TrimSpace(agentID), generation)
	startedAt := clock().UTC()
	event, changed, err := s.pauseTurnFor(
		reason,
		GoalLifecycleChildWaiting,
		startedAt,
	)
	if err == nil && changed {
		e.projectGoalLifecycle(
			GoalLifecycleChildWaiting,
			goalStateFromLifecycleEvent(event.GoalLifecycle),
			event.GoalLifecycle.Goal.LastGoalTurnID,
			startedAt,
		)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			finishedAt := clock().UTC()
			resumed, resumedChanged, resumeErr := s.resumeTurnFor(
				reason,
				GoalLifecycleChildResumed,
				finishedAt,
			)
			if resumeErr == nil && resumedChanged {
				e.projectGoalLifecycle(
					GoalLifecycleChildResumed,
					goalStateFromLifecycleEvent(resumed.GoalLifecycle),
					resumed.GoalLifecycle.Goal.LastGoalTurnID,
					finishedAt,
				)
			}
		})
	}
}

func goalStateFromLifecycleEvent(event *GoalLifecycleEvent) *goalState {
	if event == nil {
		return nil
	}
	snapshot := event.Goal
	return &goalState{
		GoalID:                           snapshot.GoalID,
		Objective:                        snapshot.Objective,
		ObjectiveRevision:                snapshot.ObjectiveRevision,
		Status:                           goalStatus(snapshot.Status),
		StatusReasonCode:                 snapshot.StatusReasonCode,
		StatusReason:                     snapshot.StatusReason,
		Revision:                         snapshot.Revision,
		TokenBudget:                      cloneUint64(snapshot.TokenBudget),
		TokensUsed:                       snapshot.TokensUsed,
		UsageLedgerRevision:              snapshot.UsageLedgerRevision,
		PendingUsageAdmission:            goalUsageAdmissionFromSnapshot(snapshot.PendingUsageAdmission),
		RootActiveTimeMillis:             snapshot.RootActiveTimeMillis,
		ContinuationOrdinal:              snapshot.ContinuationOrdinal,
		LastGoalTurnID:                   snapshot.LastGoalTurnID,
		LastTerminalSequence:             snapshot.LastTerminalSequence,
		PendingCompleteTurnID:            snapshot.PendingCompleteTurnID,
		PendingCompleteObjectiveRevision: snapshot.PendingCompleteObjectiveRevision,
		BlockerKey:                       snapshot.BlockerKey,
		BlockerTurnIDs:                   append([]string(nil), snapshot.BlockerTurnIDs...),
		CreatedAt:                        snapshot.CreatedAt,
		UpdatedAt:                        snapshot.UpdatedAt,
		unavailable:                      !snapshot.Available,
	}
}
