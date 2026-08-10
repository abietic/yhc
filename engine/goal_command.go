package engine

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/abietic/yhc/engine/commands"
)

const goalInitialPrompt = "Begin or continue working toward the active Goal. Re-evaluate the remaining required work before acting."

func (e *QueryEngine) applyGoalCommand(
	result *commands.CommandResult,
	turnID string,
) (*QueryEvent, string, error) {
	e.goalControlMu.Lock()
	defer e.goalControlMu.Unlock()
	if available, reason := e.GoalCommandAvailability(); !available {
		return nil, "", fmt.Errorf("%s", reason)
	}
	operation, err := result.RequiredString("operation")
	if err != nil {
		return nil, "", err
	}
	var state *goalState
	switch operation {
	case "view":
		snapshot, ok := e.GoalSnapshot()
		if !ok {
			result.Output = "No Goal is recorded for this session."
			return nil, "", nil
		}
		result.Output = formatGoalSnapshot(*snapshot)
		return nil, "", nil
	case "create":
		objective, objectiveErr := result.RequiredString("objective")
		if objectiveErr != nil {
			return nil, "", objectiveErr
		}
		var transitionErr error
		state, transitionErr = e.goalService.create(goalCreateRequest{
			Objective:   objective,
			TokenBudget: e.goalDefaultTokenBudget(),
		}, turnID)
		err = transitionErr
	case "edit":
		objective, requiredErr := result.RequiredString("objective")
		if requiredErr != nil {
			return nil, "", requiredErr
		}
		state, err = e.goalService.edit(objective, turnID)
	case "pause":
		state, err = e.goalService.pause(turnID)
	case "resume":
		state, err = e.goalService.resume(turnID)
	case "budget":
		rawBudget, requiredErr := result.RequiredString("token_budget")
		if requiredErr != nil {
			return nil, "", requiredErr
		}
		budget, parseErr := strconv.ParseUint(rawBudget, 10, 64)
		if parseErr != nil || budget == 0 {
			return nil, "", fmt.Errorf("goal token budget must be a positive integer")
		}
		state, err = e.goalService.setBudget(budget, turnID)
	case "clear":
		err = e.goalService.clearForTurn(turnID)
	default:
		return nil, "", fmt.Errorf("unsupported Goal operation %q", operation)
	}
	if err != nil {
		return nil, "", err
	}
	if operation == "clear" {
		result.Output = "Goal cleared. Persisted Goal state and pending continuation were retired."
		return nil, "", nil
	}
	snapshot := goalSnapshotFromState(state)
	result.Output = formatGoalSnapshot(snapshot)
	if (operation == "create" || operation == "resume") &&
		snapshot.Status == string(goalStatusActive) {
		return nil, goalInitialPrompt, nil
	}
	return nil, "", nil
}

func formatGoalSnapshot(snapshot GoalSnapshot) string {
	var budget string
	if snapshot.TokenBudget == nil {
		budget = "unbounded"
	} else {
		budget = strconv.FormatUint(*snapshot.TokenBudget, 10)
	}
	var remaining string
	if snapshot.TokenBudget == nil {
		remaining = "unbounded"
	} else if *snapshot.TokenBudget <= snapshot.TokensUsed {
		remaining = "0"
	} else {
		remaining = strconv.FormatUint(*snapshot.TokenBudget-snapshot.TokensUsed, 10)
	}
	lines := []string{
		"Goal: " + snapshot.Objective,
		"Status: " + snapshot.Status,
		fmt.Sprintf(
			"Tokens: %d used / %s budget (%s remaining; coverage %s)",
			snapshot.TokensUsed,
			budget,
			remaining,
			firstNonEmptyString(snapshot.UsageCoverage, "unknown"),
		),
		fmt.Sprintf(
			"Progress: objective revision %d, Goal revision %d, continuation %d",
			snapshot.ObjectiveRevision,
			snapshot.Revision,
			snapshot.ContinuationOrdinal,
		),
	}
	if strings.TrimSpace(snapshot.StatusReason) != "" {
		lines = append(lines, "Reason: "+snapshot.StatusReason)
	}
	if strings.TrimSpace(snapshot.BlockerKey) != "" {
		lines = append(
			lines,
			fmt.Sprintf(
				"Blocker: %s (%d distinct Goal turn reports)",
				snapshot.BlockerKey,
				len(snapshot.BlockerTurnIDs),
			),
		)
	}
	return strings.Join(lines, "\n")
}
