package appserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/abietic/yhc/engine"
)

// Optional: embedded ordinary sessions need not expose Goal.
type sessionGoalController interface {
	GoalSnapshot() (*engine.GoalSnapshot, bool)
	GoalCommandAvailability() (bool, string)
	GoalDefaultTokenBudget() *uint64
	ApplyGoalControl(engine.GoalControlRequest) (engine.GoalControlResult, error)
	SubscribeGoalContinuations() <-chan struct{}
	ClaimNextGoalContinuation() (engine.RuntimeItem, bool, error)
	SubmitGoalContinuation(context.Context, engine.RuntimeItem) (<-chan engine.QueryEvent, engine.Terminal)
}

var (
	_                         sessionGoalController = (*engine.QueryEngine)(nil)
	errGoalAdapterUnavailable                       = errors.New("goal adapter unavailable")
)

type goalView struct {
	GoalID               string  `json:"goal_id"`
	Objective            string  `json:"objective"`
	Status               string  `json:"status"`
	Revision             string  `json:"revision"`
	TokenBudget          *string `json:"token_budget"`
	TokensUsed           string  `json:"tokens_used"`
	RootActiveTimeMillis string  `json:"root_active_time_ms"`
	UsageCoverage        string  `json:"usage_coverage"`
	StatusReasonCode     string  `json:"status_reason_code"`
	StatusReason         string  `json:"status_reason"`
}

type goalResponse struct {
	SessionID          string    `json:"session_id"`
	Available          bool      `json:"available"`
	Diagnostic         string    `json:"diagnostic"`
	DefaultTokenBudget *string   `json:"default_token_budget"`
	Goal               *goalView `json:"goal"`
}

type goalControlResponse struct {
	State      goalResponse       `json:"state"`
	Saved      bool               `json:"saved"`
	Started    bool               `json:"started"`
	Turn       *StartTurnResponse `json:"turn,omitempty"`
	StartError *APIError          `json:"start_error,omitempty"`
}

type goalControlInput struct {
	Operation        engine.GoalControlOperation `json:"operation"`
	ExpectedGoalID   *string                     `json:"expected_goal_id"`
	ExpectedRevision *string                     `json:"expected_revision"`
	Objective        *string                     `json:"objective,omitempty"`
	TokenBudget      *string                     `json:"token_budget,omitempty"`
}

func decimalGoalNumber(s string) (uint64, bool) {
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil && strconv.FormatUint(n, 10) == s
}

// Reject encoding/json's lossy replacement of invalid Unicode before decode.
// Legitimate literal U+FFFD and correctly paired surrogate escapes remain valid.
func validGoalJSONUnicode(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		n, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

func decodeGoalControl(w http.ResponseWriter, r *http.Request) (engine.GoalControlRequest, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32768))
	if err != nil || !validGoalJSONUnicode(raw) {
		return engine.GoalControlRequest{}, false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var input goalControlInput
	if decodeJSON(r, &input) != nil || input.ExpectedGoalID == nil || input.ExpectedRevision == nil {
		return engine.GoalControlRequest{}, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return engine.GoalControlRequest{}, false
	}
	revision, valid := decimalGoalNumber(*input.ExpectedRevision)
	if !valid || len(*input.ExpectedGoalID) > 256 || strings.TrimSpace(*input.ExpectedGoalID) != *input.ExpectedGoalID {
		return engine.GoalControlRequest{}, false
	}
	request := engine.GoalControlRequest{Operation: input.Operation, ExpectedGoalID: *input.ExpectedGoalID, ExpectedRevision: revision}
	wantsObjective := input.Operation == engine.GoalControlCreate || input.Operation == engine.GoalControlEdit
	_, hasObjective := fields["objective"]
	if hasObjective != wantsObjective || (wantsObjective && (input.Objective == nil || strings.TrimSpace(*input.Objective) == "" || strings.ContainsRune(*input.Objective, 0) || utf8.RuneCountInString(*input.Objective) > 4000)) {
		return engine.GoalControlRequest{}, false
	}
	if wantsObjective {
		request.Objective = *input.Objective
	}
	_, hasBudget := fields["token_budget"]
	if hasBudget {
		if input.TokenBudget == nil || (input.Operation != engine.GoalControlCreate && input.Operation != engine.GoalControlBudget) {
			return engine.GoalControlRequest{}, false
		}
		budget, valid := decimalGoalNumber(*input.TokenBudget)
		if !valid || budget == 0 {
			return engine.GoalControlRequest{}, false
		}
		request.TokenBudget = &budget
	}
	if input.Operation == engine.GoalControlBudget && !hasBudget {
		return engine.GoalControlRequest{}, false
	}
	switch input.Operation {
	case engine.GoalControlCreate, engine.GoalControlEdit, engine.GoalControlPause, engine.GoalControlResume, engine.GoalControlBudget, engine.GoalControlClear, engine.GoalControlContinue:
		return request, true
	default:
		return engine.GoalControlRequest{}, false
	}
}

func goalBudgetString(budget *uint64) *string {
	if budget == nil {
		return nil
	}
	s := strconv.FormatUint(*budget, 10)
	return &s
}

func projectGoalSnapshot(goal *engine.GoalSnapshot) *goalView {
	if goal == nil {
		return nil
	}
	return &goalView{GoalID: truncateSnapshotText(goal.GoalID, 256), Objective: truncateSnapshotText(goal.Objective, 4000), Status: mcpInventoryChoice(goal.Status, "active", "paused", "blocked", "usage_limited", "budget_limited", "complete"), Revision: strconv.FormatUint(goal.Revision, 10), TokenBudget: goalBudgetString(goal.TokenBudget), TokensUsed: strconv.FormatUint(goal.TokensUsed, 10), RootActiveTimeMillis: strconv.FormatInt(goal.RootActiveTimeMillis, 10), UsageCoverage: mcpInventoryChoice(goal.UsageCoverage, "complete", "incomplete", "pending", "unavailable"), StatusReasonCode: truncateSnapshotText(goal.StatusReasonCode, 128), StatusReason: truncateSnapshotText(goal.StatusReason, 1024)}
}

func (s *session) goalState(controller sessionGoalController) goalResponse {
	available, _ := controller.GoalCommandAvailability()
	goal, _ := controller.GoalSnapshot()
	result := goalResponse{SessionID: s.id, Available: available && (goal == nil || goal.Available), DefaultTokenBudget: goalBudgetString(controller.GoalDefaultTokenBudget()), Goal: projectGoalSnapshot(goal)}
	if !result.Available {
		result.Diagnostic = "Goal is unavailable: check saved root capability, Plan mode, and durable state."
	}
	return result
}

func (s *Server) handleGetGoal(w http.ResponseWriter, r *http.Request)     { s.handleGoal(w, r, false) }
func (s *Server) handleControlGoal(w http.ResponseWriter, r *http.Request) { s.handleGoal(w, r, true) }

func (s *Server) handleGoal(w http.ResponseWriter, r *http.Request, control bool) {
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request", "Goal does not accept query parameters")
		return
	}
	owned, ok := s.getSession(r.PathValue("session_id"))
	if !ok {
		writeError(w, 404, "session_not_found", "open a live session first")
		return
	}
	var input engine.GoalControlRequest
	if control {
		var valid bool
		input, valid = decodeGoalControl(w, r)
		if !valid {
			writeError(w, 400, "goal_invalid", "invalid bounded Goal control request")
			return
		}
	}
	result, err := s.goalResult(r.Context(), owned, control, input)
	var conflict *engine.GoalControlConflictError
	switch {
	case err == nil:
		writeJSON(w, 200, result)
	case errors.Is(err, errSessionClosed):
		writeError(w, 404, "session_not_found", "open a live session first")
	case errors.Is(err, errSessionBusy):
		writeError(w, 409, "goal_active_turn", "stop execution before changing Goal")
	case errors.As(err, &conflict):
		writeError(w, 409, "goal_conflict", "Goal changed; reload before retrying")
	case errors.Is(err, errGoalAdapterUnavailable):
		writeError(w, 501, "goal_unavailable", "Goal is unavailable for this runtime")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(w, 408, "goal_cancelled", "Goal request cancelled before saving")
	default:
		writeError(w, 422, "goal_unavailable", "Goal transition could not be applied safely; reload and check state")
	}
}

// Lease exact ownership through persistence and admission, never response I/O.
func (s *Server) goalResult(ctx context.Context, owned *session, control bool, input engine.GoalControlRequest) (any, error) {
	owned.turnAdmissionMu.Lock()
	defer owned.turnAdmissionMu.Unlock()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closing || s.sessions[owned.id] != owned {
		return nil, errSessionClosed
	}
	owned.mu.Lock()
	closed, busy := owned.closed, owned.activeTurnID != "" || owned.status == "waiting"
	owned.mu.Unlock()
	if closed {
		return nil, errSessionClosed
	}
	controller, ok := owned.engine.(sessionGoalController)
	if !ok {
		return nil, errGoalAdapterUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !control {
		return owned.goalState(controller), nil
	}
	if busy {
		return nil, errSessionBusy
	}
	if input.Operation == engine.GoalControlCreate && input.TokenBudget == nil {
		input.TokenBudget = controller.GoalDefaultTokenBudget()
	}
	transition, err := controller.ApplyGoalControl(input)
	if err != nil {
		return nil, err
	}
	response := goalControlResponse{State: owned.goalState(controller), Saved: input.Operation != engine.GoalControlContinue}
	owned.publishSynthetic("goal.updated", "", response.State)
	if transition.RequiresPrompt {
		turn, err := owned.startTurnLocked(StartTurnRequest{Prompt: engine.GoalInitialPrompt})
		if err != nil {
			response.StartError = &APIError{Code: "goal_start_failed", Message: "Goal is saved but execution did not start; reload before explicitly continuing."}
		} else {
			response.Started = true
			response.Turn = &turn
		}
	}
	return response, nil
}
