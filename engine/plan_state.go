package engine

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/tools"
)

// PlanPhase is the QueryEngine-owned Plan lifecycle phase.
type PlanPhase string

const (
	PlanPhaseInactive         PlanPhase = "inactive"
	PlanPhaseActive           PlanPhase = "active"
	PlanPhaseAwaitingApproval PlanPhase = "awaiting_approval"
)

// PlanState is the authoritative in-memory Plan snapshot for one QueryEngine.
// ApprovalInitialDigest binds a live AwaitingApproval request to the exact
// initial bytes that entered review; adapters separately report the bytes they
// actually rendered.
type PlanState struct {
	Phase                 PlanPhase
	PlanFileIdentity      string
	ReturnMode            permission.Mode
	ApprovalRequestID     string
	ApprovalInitialDigest string
	Revision              uint64
}

// ErrPlanTransitionInFlight reports an external mode change that lost the
// serialized safe-boundary race to an active turn.
var ErrPlanTransitionInFlight = errors.New(
	"cannot change Plan state while a turn is active",
)

type planTransitionSource string

const (
	planTransitionTool            planTransitionSource = "tool"
	planTransitionExternal        planTransitionSource = "external"
	planTransitionUserConfirmed   planTransitionSource = "user_confirmed"
	planTransitionCommand         planTransitionSource = "command"
	planTransitionApprovalRequest planTransitionSource = "approval_request"
	planTransitionApprovalResult  planTransitionSource = "approval_result"
)

type planTurnSnapshot struct {
	State PlanState
	Mode  permission.Mode
}

type planTransitionRequest struct {
	Source    planTransitionSource
	TurnID    string
	RequestID string
	Mode      permission.Mode
}

func initialPlanState(config QueryEngineConfig) PlanState {
	state := PlanState{
		Phase: PlanPhaseInactive,
		PlanFileIdentity: tools.GetPlanFilePath(
			config.SessionID,
			config.AgentID,
		),
		ReturnMode: permission.ModeDefault,
	}
	if config.PermissionMode == permission.ModePlan {
		state.Phase = PlanPhaseActive
		state.Revision = 1
	}
	return state
}

// PlanState returns a defensive snapshot of the engine-owned Plan state.
func (e *QueryEngine) PlanState() PlanState {
	if e == nil {
		return PlanState{Phase: PlanPhaseInactive}
	}
	e.planMu.Lock()
	defer e.planMu.Unlock()
	return e.planState
}

func (e *QueryEngine) beginPlanTurn(turnID string) (planTurnSnapshot, error) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return planTurnSnapshot{}, fmt.Errorf("plan turn requires an identity")
	}
	e.planMu.Lock()
	defer e.planMu.Unlock()
	if e.planActiveTurnID != "" {
		return planTurnSnapshot{}, fmt.Errorf(
			"%w: turn %s owns the boundary",
			ErrPlanTransitionInFlight,
			e.planActiveTurnID,
		)
	}
	e.planActiveTurnID = turnID
	e.mu.Lock()
	mode := e.config.PermissionMode
	e.mu.Unlock()
	if mode == "" {
		mode = permission.ModeDefault
	}
	if planPhaseRequiresContainment(e.planState.Phase) {
		mode = permission.ModePlan
	}
	return planTurnSnapshot{State: e.planState, Mode: mode}, nil
}

func (e *QueryEngine) endPlanTurn(turnID string) {
	if e == nil {
		return
	}
	e.planMu.Lock()
	if e.planActiveTurnID == strings.TrimSpace(turnID) {
		e.planActiveTurnID = ""
	}
	e.planMu.Unlock()
}

func (e *QueryEngine) applyPlanTransition(
	request planTransitionRequest,
) (*PlanStateTransitionEvent, bool, error) {
	if e == nil {
		return nil, false, fmt.Errorf("nil QueryEngine")
	}
	request.TurnID = strings.TrimSpace(request.TurnID)
	request.RequestID = strings.TrimSpace(request.RequestID)
	if request.RequestID == "" {
		return nil, false, fmt.Errorf(
			"plan transition requires a request identity",
		)
	}
	if !knownPermissionMode(request.Mode) {
		return nil, false, fmt.Errorf(
			"unknown permission mode %q",
			request.Mode,
		)
	}

	e.planMu.Lock()
	defer e.planMu.Unlock()
	switch request.Source {
	case planTransitionTool:
		if request.TurnID == "" ||
			request.TurnID != e.planActiveTurnID {
			return nil, false, fmt.Errorf(
				"%w: tool turn %q does not own boundary %q",
				ErrPlanTransitionInFlight,
				request.TurnID,
				e.planActiveTurnID,
			)
		}
	case planTransitionCommand:
		if request.TurnID == "" ||
			request.TurnID != e.planActiveTurnID {
			return nil, false, fmt.Errorf(
				"%w: command turn %q does not own boundary %q",
				ErrPlanTransitionInFlight,
				request.TurnID,
				e.planActiveTurnID,
			)
		}
	case planTransitionExternal, planTransitionUserConfirmed:
		if e.planActiveTurnID != "" {
			return nil, false, fmt.Errorf(
				"%w: turn %s owns the boundary",
				ErrPlanTransitionInFlight,
				e.planActiveTurnID,
			)
		}
		if e.planState.Phase == PlanPhaseAwaitingApproval {
			return nil, false, fmt.Errorf(
				"%w: Plan approval %s owns the boundary",
				ErrPlanTransitionInFlight,
				e.planState.ApprovalRequestID,
			)
		}
		if e.sessionState != nil &&
			e.sessionState.GetState() != session.StateIdle {
			return nil, false, fmt.Errorf(
				"%w: session is %s",
				ErrPlanTransitionInFlight,
				e.sessionState.GetState(),
			)
		}
	default:
		return nil, false, fmt.Errorf(
			"unknown Plan transition source %q",
			request.Source,
		)
	}
	if request.Source == planTransitionUserConfirmed &&
		request.Mode != permission.ModeBypassPermissions {
		return nil, false, fmt.Errorf(
			"user-confirmed Plan transition cannot target permission mode %q",
			request.Mode,
		)
	}
	if request.Mode == permission.ModePlan &&
		e.planState.Phase != PlanPhaseActive {
		e.goalMu.Lock()
		blockedByGoal := goalBlocksPlan(e.goalState)
		e.goalMu.Unlock()
		if blockedByGoal {
			return nil, false, errGoalPlanConflict
		}
	}

	e.mu.Lock()
	currentMode := e.config.PermissionMode
	if currentMode == "" {
		currentMode = permission.ModeDefault
	}
	fromPhase := e.planState.Phase
	if request.Source == planTransitionExternal &&
		fromPhase == PlanPhaseActive &&
		request.Mode != permission.ModePlan {
		// An idle user abandon restores the mode that existed before Plan. It
		// cannot smuggle a new implementation permission target around typed
		// ExitPlanMode approval.
		request.Mode = e.planState.ReturnMode
	}
	targetPhase := PlanPhaseInactive
	if request.Mode == permission.ModePlan {
		targetPhase = PlanPhaseActive
	}

	if request.Source == planTransitionTool {
		if request.Mode == permission.ModePlan {
			if fromPhase != PlanPhaseInactive {
				e.mu.Unlock()
				return nil, false, fmt.Errorf(
					"EnterPlanMode requires inactive phase, got %s",
					fromPhase,
				)
			}
		} else {
			if !knownPlanExitTargetMode(request.Mode) {
				e.mu.Unlock()
				return nil, false, fmt.Errorf(
					"tool Plan exit cannot target permission mode %q",
					request.Mode,
				)
			}
			if fromPhase != PlanPhaseActive {
				e.mu.Unlock()
				return nil, false, fmt.Errorf(
					"ExitPlanMode requires active phase, got %s",
					fromPhase,
				)
			}
		}
	} else if currentMode == request.Mode && fromPhase == targetPhase {
		e.mu.Unlock()
		return nil, false, nil
	}

	next := e.planState
	if targetPhase == PlanPhaseActive && fromPhase != PlanPhaseActive {
		if currentMode != permission.ModePlan {
			next.ReturnMode = currentMode
		}
	}
	next.Phase = targetPhase
	next.ApprovalRequestID = ""
	next.ApprovalInitialDigest = ""
	next.Revision++
	e.planState = next
	e.config.PermissionMode = request.Mode
	if fromPhase != targetPhase {
		e.promptRouteGeneration++
	}
	if e.subagentExecutor != nil {
		e.subagentExecutor.PermissionMode = request.Mode
	}
	e.mu.Unlock()

	event := &PlanStateTransitionEvent{
		FromPhase:         fromPhase,
		Phase:             next.Phase,
		PermissionMode:    request.Mode,
		PlanFileIdentity:  next.PlanFileIdentity,
		ReturnMode:        next.ReturnMode,
		ApprovalRequestID: next.ApprovalRequestID,
		RequestID:         request.RequestID,
		Revision:          next.Revision,
		Source:            string(request.Source),
	}
	return event, true, nil
}

func (e *QueryEngine) beginPlanApproval(
	requestID string,
	emit func(QueryEvent),
) (*PlanApprovalRequest, error) {
	if e == nil {
		return nil, fmt.Errorf("nil QueryEngine")
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, fmt.Errorf("plan approval requires a request identity")
	}

	e.planMu.Lock()
	if e.planActiveTurnID == "" {
		e.planMu.Unlock()
		return nil, fmt.Errorf(
			"%w: Plan approval has no active turn owner",
			ErrPlanTransitionInFlight,
		)
	}
	if e.planState.Phase != PlanPhaseActive {
		phase := e.planState.Phase
		e.planMu.Unlock()
		return nil, fmt.Errorf(
			"ExitPlanMode approval requires active phase, got %s",
			phase,
		)
	}
	_, initialDigest, err := ReadPlanReviewSnapshot(
		e.planState.PlanFileIdentity,
	)
	if err != nil {
		e.planMu.Unlock()
		return nil, fmt.Errorf("plan approval snapshot unavailable: %w", err)
	}
	fromPhase := e.planState.Phase
	next := e.planState
	next.Phase = PlanPhaseAwaitingApproval
	next.ApprovalRequestID = requestID
	next.ApprovalInitialDigest = initialDigest
	next.Revision++
	e.planState = next
	request := &PlanApprovalRequest{
		RequestID:         requestID,
		PlanRevision:      next.Revision,
		PlanFileIdentity:  next.PlanFileIdentity,
		InitialPlanDigest: next.ApprovalInitialDigest,
		ReturnMode:        next.ReturnMode,
	}
	event := e.planTransitionEvent(
		fromPhase,
		next,
		permission.ModePlan,
		requestID,
		planTransitionApprovalRequest,
	)
	e.planMu.Unlock()

	emitPlanTransition(emit, event)
	return request, nil
}

func (e *QueryEngine) settlePlanApproval(
	request *PlanApprovalRequest,
	result PermissionInteractionResult,
	emit func(QueryEvent),
) PermissionInteractionResult {
	if e == nil || request == nil {
		return PermissionInteractionResult{
			Decision: PermissionDeny,
			Message:  "invalid Plan approval request",
		}
	}

	e.planMu.Lock()
	if e.planState.Phase != PlanPhaseAwaitingApproval ||
		e.planState.ApprovalRequestID != request.RequestID ||
		e.planState.Revision != request.PlanRevision ||
		e.planState.ApprovalInitialDigest != request.InitialPlanDigest {
		e.planMu.Unlock()
		return PermissionInteractionResult{
			Decision: PermissionDeny,
			Message:  "stale Plan approval decision ignored",
		}
	}

	result = normalizePlanApprovalDecision(request, result)
	fromPhase := e.planState.Phase
	next := e.planState
	next.Phase = PlanPhaseActive
	next.ApprovalRequestID = ""
	next.ApprovalInitialDigest = ""
	next.Revision++
	e.planState = next
	event := e.planTransitionEvent(
		fromPhase,
		next,
		permission.ModePlan,
		request.RequestID,
		planTransitionApprovalResult,
	)
	e.planMu.Unlock()

	emitPlanTransition(emit, event)
	return result
}

func (e *QueryEngine) ensurePlanApprovalSettled(
	request *PlanApprovalRequest,
	result PermissionInteractionResult,
	emit func(QueryEvent),
) PermissionInteractionResult {
	if e == nil || request == nil {
		return result
	}
	state := e.PlanState()
	if state.Phase != PlanPhaseAwaitingApproval ||
		state.ApprovalRequestID != request.RequestID ||
		state.Revision != request.PlanRevision {
		return result
	}
	return e.settlePlanApproval(request, result, emit)
}

func (e *QueryEngine) planTransitionEvent(
	fromPhase PlanPhase,
	next PlanState,
	mode permission.Mode,
	requestID string,
	source planTransitionSource,
) *PlanStateTransitionEvent {
	return &PlanStateTransitionEvent{
		FromPhase:         fromPhase,
		Phase:             next.Phase,
		PermissionMode:    mode,
		PlanFileIdentity:  next.PlanFileIdentity,
		ReturnMode:        next.ReturnMode,
		ApprovalRequestID: next.ApprovalRequestID,
		RequestID:         requestID,
		Revision:          next.Revision,
		Source:            string(source),
	}
}

func emitPlanTransition(
	emit func(QueryEvent),
	transition *PlanStateTransitionEvent,
) {
	if emit == nil || transition == nil {
		return
	}
	event := QueryEvent{
		Type:                EventPlanStateTransition,
		PlanStateTransition: transition,
	}
	event.CausationID = transition.RequestID
	emit(event)
}

func normalizePlanApprovalDecision(
	request *PlanApprovalRequest,
	result PermissionInteractionResult,
) PermissionInteractionResult {
	if request != nil && request.InitialPlanDigest == "" {
		if _, digest, err := ReadPlanReviewSnapshot(
			request.PlanFileIdentity,
		); err == nil {
			cloned := *request
			cloned.InitialPlanDigest = digest
			request = &cloned
		}
	}
	decision := clonePlanApprovalDecision(result.PlanApproval)
	if decision == nil {
		decision = &PlanApprovalDecision{
			RequestID:    request.RequestID,
			PlanRevision: request.PlanRevision,
			Outcome:      PlanApprovalCancel,
			TargetMode:   permission.ModePlan,
		}
		result.PlanApproval = decision
		if result.Allowed() {
			result.Decision = PermissionDeny
			result.Message = "structured Plan approval decision required"
		} else if strings.TrimSpace(result.Message) == "" {
			result.Message = "User rejected the plan."
		}
		return result
	}
	decision.settled = false
	decision.Feedback = strings.TrimSpace(decision.Feedback)
	if decision.RequestID != request.RequestID ||
		decision.PlanRevision != request.PlanRevision {
		return PermissionInteractionResult{
			Decision: PermissionDeny,
			Message:  "mismatched Plan approval identity",
			PlanApproval: &PlanApprovalDecision{
				RequestID:    request.RequestID,
				PlanRevision: request.PlanRevision,
				Outcome:      PlanApprovalCancel,
				TargetMode:   permission.ModePlan,
			},
		}
	}

	legacy := decision.Outcome == ""
	if legacy {
		switch {
		case decision.Approved && result.Allowed():
			decision.Outcome = PlanApprovalApprove
			if decision.ReviewedPlanDigest == "" {
				decision.ReviewedPlanDigest = request.InitialPlanDigest
			}
		case decision.Feedback != "":
			decision.Outcome = PlanApprovalRevise
		default:
			decision.Outcome = PlanApprovalCancel
		}
	}
	// Approved is accepted only as a compatibility input. Outcome is the sole
	// runtime authority and every normalized result must stop propagating the
	// deprecated boolean.
	decision.Approved = false

	switch decision.Outcome {
	case PlanApprovalRevise:
		if decision.Feedback == "" {
			decision.Outcome = PlanApprovalCancel
			return normalizePlanApprovalCancel(
				result,
				decision,
				"Plan approval cancelled.",
			)
		}
		decision.Approved = false
		decision.Confirmed = false
		decision.TargetMode = permission.ModePlan
		decision.ReviewedPlanDigest = ""
		result.PlanApproval = decision
		result.Decision = PermissionDeny
		result.Message = "User requested further planning: " + decision.Feedback
		return result
	case PlanApprovalCancel:
		return normalizePlanApprovalCancel(
			result,
			decision,
			"Plan approval cancelled.",
		)
	case PlanApprovalApprove:
		if !result.Allowed() {
			decision.Outcome = PlanApprovalCancel
			return normalizePlanApprovalCancel(
				result,
				decision,
				"Plan approval was not authorized.",
			)
		}
	default:
		return PermissionInteractionResult{
			Decision: PermissionDeny,
			Message:  "invalid Plan approval outcome",
			PlanApproval: &PlanApprovalDecision{
				RequestID:    request.RequestID,
				PlanRevision: request.PlanRevision,
				Outcome:      PlanApprovalCancel,
				TargetMode:   permission.ModePlan,
			},
		}
	}

	if !knownPlanExitTargetMode(decision.TargetMode) {
		decision.Outcome = PlanApprovalCancel
		return normalizePlanApprovalCancel(
			result,
			decision,
			"invalid Plan approval target mode",
		)
	}
	if decision.TargetMode == permission.ModeBypassPermissions &&
		!decision.Confirmed {
		decision.Outcome = PlanApprovalCancel
		return normalizePlanApprovalCancel(
			result,
			decision,
			"explicit confirmation is required for Plan bypass",
		)
	}
	if !validPlanDigest(decision.ReviewedPlanDigest) {
		decision.Outcome = PlanApprovalCancel
		return normalizePlanApprovalCancel(
			result,
			decision,
			"Plan approval did not identify reviewed bytes",
		)
	}
	_, currentDigest, err := ReadPlanReviewSnapshot(request.PlanFileIdentity)
	if err != nil {
		decision.Outcome = PlanApprovalCancel
		return normalizePlanApprovalCancel(
			result,
			decision,
			"Plan review is stale: "+err.Error(),
		)
	}
	if currentDigest != decision.ReviewedPlanDigest {
		decision.Outcome = PlanApprovalCancel
		return normalizePlanApprovalCancel(
			result,
			decision,
			"Plan review is stale: the Plan file changed after review",
		)
	}
	decision.Feedback = ""
	decision.settled = true
	result.Decision = PermissionAllowOnce
	result.Message = ""
	result.PlanApproval = decision
	return result
}

func normalizePlanApprovalCancel(
	result PermissionInteractionResult,
	decision *PlanApprovalDecision,
	message string,
) PermissionInteractionResult {
	decision.settled = false
	decision.Approved = false
	decision.Confirmed = false
	decision.TargetMode = permission.ModePlan
	decision.ReviewedPlanDigest = ""
	decision.Feedback = ""
	result.PlanApproval = decision
	if result.Allowed() {
		result.Decision = PermissionDeny
	}
	if strings.TrimSpace(result.Message) == "" {
		result.Message = message
	}
	return result
}

func (e *QueryEngine) transitionPermissionModeForTurn(
	turnID string,
	emit func(QueryEvent) bool,
	toolCtx *ToolUseContext,
	mode permission.Mode,
	requestID string,
) (*ToolUseContext, func(), error) {
	transition, changed, err := e.applyPlanTransition(
		planTransitionRequest{
			Source:    planTransitionTool,
			TurnID:    turnID,
			RequestID: requestID,
			Mode:      mode,
		},
	)
	if err != nil {
		return toolCtx, nil, err
	}
	applyPermissionModeToToolContext(toolCtx, mode)
	if !changed || transition == nil {
		return toolCtx, nil, nil
	}

	event := QueryEvent{
		Type:                EventPlanStateTransition,
		PlanStateTransition: transition,
	}
	event.CausationID = transition.RequestID
	var once sync.Once
	publish := func() {
		once.Do(func() {
			if emit != nil {
				emit(event)
			}
			if e.sessionState != nil {
				e.sessionState.NotifyPermissionModeChanged(
					string(transition.PermissionMode),
				)
			}
			_ = e.persistSessionCheckpoint("")
		})
	}
	return toolCtx, publish, nil
}

func (e *QueryEngine) transitionPermissionModeForCommandTurn(
	turnID string,
	emit func(QueryEvent) bool,
	mode permission.Mode,
	requestID string,
) error {
	transition, changed, err := e.applyPlanTransition(
		planTransitionRequest{
			Source:    planTransitionCommand,
			TurnID:    turnID,
			RequestID: requestID,
			Mode:      mode,
		},
	)
	if err != nil || !changed || transition == nil {
		return err
	}
	event := QueryEvent{
		Type:                EventPlanStateTransition,
		PlanStateTransition: transition,
	}
	event.CausationID = transition.RequestID
	if emit != nil {
		emit(event)
	}
	if e.sessionState != nil {
		e.sessionState.NotifyPermissionModeChanged(
			string(transition.PermissionMode),
		)
	}
	_ = e.persistSessionCheckpoint("")
	return nil
}

// SetPermissionMode serializes external mode changes with Plan tool
// transitions. An active turn wins the boundary and external callers must
// retry after it settles. Bypass requires SetPermissionModeConfirmed.
func (e *QueryEngine) SetPermissionMode(mode permission.Mode) error {
	return e.SetPermissionModeConfirmed(mode, false)
}

func (e *QueryEngine) setPermissionMode(
	mode permission.Mode,
	source planTransitionSource,
) error {
	requestID := generateUUID()
	transition, changed, err := e.applyPlanTransition(
		planTransitionRequest{
			Source:    source,
			RequestID: requestID,
			Mode:      mode,
		},
	)
	if err != nil || !changed || transition == nil {
		return err
	}
	event := QueryEvent{
		Type:                EventPlanStateTransition,
		PlanStateTransition: transition,
	}
	event.CausationID = transition.RequestID
	e.decorateRuntimeEvent("external-plan-"+requestID, event)
	if e.sessionState != nil {
		e.sessionState.NotifyPermissionModeChanged(
			string(transition.PermissionMode),
		)
	}
	_ = e.persistSessionCheckpoint("")
	return nil
}

func knownPermissionMode(mode permission.Mode) bool {
	switch mode {
	case permission.ModeDefault,
		permission.ModePlan,
		permission.ModeAcceptEdits,
		permission.ModeBypassPermissions,
		permission.ModeDontAsk,
		permission.ModeAuto,
		permission.ModeBubble:
		return true
	default:
		return false
	}
}

func knownPlanExitTargetMode(mode permission.Mode) bool {
	return knownPlanReturnMode(mode)
}

func knownPlanReturnMode(mode permission.Mode) bool {
	return mode != permission.ModePlan && knownPermissionMode(mode)
}

func planPhaseRequiresContainment(phase PlanPhase) bool {
	return phase == PlanPhaseActive ||
		phase == PlanPhaseAwaitingApproval
}
