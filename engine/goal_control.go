package engine

import (
	"fmt"
	"strings"
)

// GoalControlOperation is one user-owned Goal lifecycle transition exposed to
// typed entrypoint adapters. It does not grant model or continuation authority.
type GoalControlOperation string

const (
	GoalControlCreate GoalControlOperation = "create"
	GoalControlEdit   GoalControlOperation = "edit"
	GoalControlPause  GoalControlOperation = "pause"
	GoalControlResume GoalControlOperation = "resume"
	GoalControlBudget GoalControlOperation = "budget"
	GoalControlClear  GoalControlOperation = "clear"
)

// GoalControlRequest binds one typed transition to the exact detached Goal
// revision observed by the caller. Create uses revision zero and no Goal ID.
type GoalControlRequest struct {
	Operation        GoalControlOperation
	ExpectedGoalID   string
	ExpectedRevision uint64
	Objective        string
	TokenBudget      *uint64
}

// GoalControlResult reports the durable state after one transition.
type GoalControlResult struct {
	Phase          GoalLifecyclePhase
	Goal           *GoalSnapshot
	Cleared        bool
	RequiresPrompt bool
}

// GoalControlConflictError reports optimistic identity drift without granting
// permission to retry a transition against a different revision.
type GoalControlConflictError struct {
	Reason  string
	Current *GoalSnapshot
}

func (e *GoalControlConflictError) Error() string {
	if e == nil || strings.TrimSpace(e.Reason) == "" {
		return "Goal control identity conflict"
	}
	return "Goal control identity conflict: " + e.Reason
}

// ApplyGoalControl maps a typed entrypoint request into the existing
// QueryEngine-owned transition service. The control mutex also serializes the
// TUI/Plain command adapter, so optimistic revision validation and mutation
// form one process-local boundary.
func (e *QueryEngine) ApplyGoalControl(
	request GoalControlRequest,
) (GoalControlResult, error) {
	if e == nil {
		return GoalControlResult{}, fmt.Errorf("goal requires an engine runtime")
	}
	e.goalControlMu.Lock()
	defer e.goalControlMu.Unlock()

	if available, reason := e.GoalCommandAvailability(); !available {
		return GoalControlResult{}, fmt.Errorf("%s", reason)
	}
	current, exists := e.GoalSnapshot()
	if err := validateGoalControlExpectation(request, current, exists); err != nil {
		return GoalControlResult{}, err
	}

	var (
		state *goalState
		phase GoalLifecyclePhase
		err   error
	)
	switch request.Operation {
	case GoalControlCreate:
		state, err = e.goalService.create(goalCreateRequest{
			Objective:   request.Objective,
			TokenBudget: cloneUint64(request.TokenBudget),
		})
		phase = GoalLifecycleCreated
	case GoalControlEdit:
		state, err = e.goalService.edit(request.Objective)
		phase = GoalLifecycleObjectiveEdited
	case GoalControlPause:
		state, err = e.goalService.pause()
		phase = GoalLifecyclePaused
	case GoalControlResume:
		state, err = e.goalService.resume()
		phase = GoalLifecycleResumed
	case GoalControlBudget:
		if request.TokenBudget == nil || *request.TokenBudget == 0 {
			return GoalControlResult{}, fmt.Errorf(
				"goal token budget must be a positive integer",
			)
		}
		state, err = e.goalService.setBudget(*request.TokenBudget)
		phase = GoalLifecycleBudgetUpdated
	case GoalControlClear:
		err = e.goalService.clear()
		phase = GoalLifecycleCleared
	default:
		return GoalControlResult{}, fmt.Errorf(
			"unsupported Goal operation %q",
			request.Operation,
		)
	}
	if err != nil {
		return GoalControlResult{}, err
	}
	if request.Operation == GoalControlClear {
		return GoalControlResult{
			Phase:   phase,
			Cleared: true,
		}, nil
	}
	snapshot := goalSnapshotFromState(state)
	if current != nil && snapshot.Revision == current.Revision {
		return GoalControlResult{}, &GoalControlConflictError{
			Reason:  "operation would not change Goal state",
			Current: current,
		}
	}
	return GoalControlResult{
		Phase: phase,
		Goal:  &snapshot,
		RequiresPrompt: (request.Operation == GoalControlCreate ||
			request.Operation == GoalControlResume) &&
			snapshot.Status == string(goalStatusActive),
	}, nil
}

func validateGoalControlExpectation(
	request GoalControlRequest,
	current *GoalSnapshot,
	exists bool,
) error {
	conflict := func(reason string) error {
		var snapshot *GoalSnapshot
		if current != nil {
			cloned := cloneGoalSnapshot(*current)
			snapshot = &cloned
		}
		return &GoalControlConflictError{
			Reason:  reason,
			Current: snapshot,
		}
	}
	if request.Operation == GoalControlCreate {
		if request.ExpectedRevision != 0 ||
			strings.TrimSpace(request.ExpectedGoalID) != "" {
			return conflict("create requires revision zero and no Goal ID")
		}
		if exists {
			return conflict("Goal state already exists")
		}
		return nil
	}
	if request.ExpectedRevision == 0 ||
		strings.TrimSpace(request.ExpectedGoalID) == "" {
		return conflict("an exact Goal ID and positive revision are required")
	}
	if !exists || current == nil {
		return conflict("Goal state no longer exists")
	}
	if request.ExpectedGoalID != current.GoalID ||
		request.ExpectedRevision != current.Revision {
		return conflict("Goal ID or revision changed")
	}
	return nil
}
