package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/abietic/yhc/tools"
)

type goalToolSnapshot struct {
	Exists               bool    `json:"exists"`
	Available            bool    `json:"available,omitempty"`
	GoalID               string  `json:"goal_id,omitempty"`
	Objective            string  `json:"objective,omitempty"`
	ObjectiveRevision    uint64  `json:"objective_revision,omitempty"`
	Status               string  `json:"status,omitempty"`
	StatusReasonCode     string  `json:"status_reason_code,omitempty"`
	StatusReason         string  `json:"status_reason,omitempty"`
	Revision             uint64  `json:"revision,omitempty"`
	TokenBudget          *uint64 `json:"token_budget,omitempty"`
	TokensUsed           uint64  `json:"tokens_used,omitempty"`
	TokensRemaining      *uint64 `json:"tokens_remaining,omitempty"`
	UsageCoverage        string  `json:"usage_coverage,omitempty"`
	ContinuationOrdinal  uint64  `json:"continuation_ordinal,omitempty"`
	BlockerKey           string  `json:"blocker_key,omitempty"`
	BlockerDistinctTurns int     `json:"blocker_distinct_turns,omitempty"`
}

func (e *QueryEngine) executeGoalTool(name, input string) (string, error) {
	if !e.goalModelToolsVisible() {
		return "", fmt.Errorf(
			"%s is unavailable outside an enabled saved root TUI, Plain, dedicated headless Goal, or negotiated ACP runtime",
			strings.TrimSpace(name),
		)
	}
	switch strings.TrimSpace(name) {
	case tools.GetGoalToolName:
		if strings.TrimSpace(input) != "" {
			var params map[string]any
			if err := json.Unmarshal([]byte(input), &params); err != nil {
				return "", fmt.Errorf("parse get_goal input: %w", err)
			}
			if len(params) != 0 {
				return "", fmt.Errorf("get_goal does not accept arguments")
			}
		}
		return e.goalToolSnapshotJSON()
	case tools.UpdateGoalToolName:
		var params struct {
			Status     string `json:"status"`
			Reason     string `json:"reason"`
			BlockerKey string `json:"blocker_key"`
		}
		if err := json.Unmarshal([]byte(input), &params); err != nil {
			return "", fmt.Errorf("parse update_goal input: %w", err)
		}
		identity := e.currentGoalExecutionIdentity()
		if identity == nil || strings.TrimSpace(identity.GoalTurnID) == "" {
			return "", fmt.Errorf("update_goal requires an active root Goal turn")
		}
		now := time.Now().UTC()
		e.mu.Lock()
		if e.config.Clock != nil {
			now = e.config.Clock().UTC()
		}
		e.mu.Unlock()
		switch strings.ToLower(strings.TrimSpace(params.Status)) {
		case "complete":
			if _, err := e.goalService.requestCompletion(
				goalCompletionRequest{
					GoalID:            identity.GoalID,
					ObjectiveRevision: identity.ObjectiveRevision,
					TurnID:            identity.GoalTurnID,
				},
				now,
			); err != nil {
				return "", err
			}
		case "blocked":
			if strings.TrimSpace(params.Reason) == "" ||
				strings.TrimSpace(params.BlockerKey) == "" {
				return "", fmt.Errorf(
					"update_goal blocked requires reason and blocker_key",
				)
			}
			if _, err := e.goalService.reportBlocker(
				goalBlockerRequest{
					GoalID:            identity.GoalID,
					ObjectiveRevision: identity.ObjectiveRevision,
					TurnID:            identity.GoalTurnID,
					Reason:            params.Reason,
					BlockerKey:        params.BlockerKey,
				},
				now,
			); err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf(
				"update_goal status must be complete or blocked",
			)
		}
		return e.goalToolSnapshotJSON()
	default:
		return "", fmt.Errorf("unsupported Goal tool %q", name)
	}
}

func (e *QueryEngine) goalToolSnapshotJSON() (string, error) {
	snapshot, ok := e.GoalSnapshot()
	if !ok {
		encoded, err := json.Marshal(goalToolSnapshot{Exists: false})
		return string(encoded), err
	}
	projected := goalToolSnapshot{
		Exists:               true,
		Available:            snapshot.Available,
		GoalID:               snapshot.GoalID,
		Objective:            snapshot.Objective,
		ObjectiveRevision:    snapshot.ObjectiveRevision,
		Status:               snapshot.Status,
		StatusReasonCode:     snapshot.StatusReasonCode,
		StatusReason:         snapshot.StatusReason,
		Revision:             snapshot.Revision,
		TokenBudget:          cloneUint64(snapshot.TokenBudget),
		TokensUsed:           snapshot.TokensUsed,
		UsageCoverage:        snapshot.UsageCoverage,
		ContinuationOrdinal:  snapshot.ContinuationOrdinal,
		BlockerKey:           snapshot.BlockerKey,
		BlockerDistinctTurns: len(snapshot.BlockerTurnIDs),
	}
	if snapshot.TokenBudget != nil {
		remaining := uint64(0)
		if *snapshot.TokenBudget > snapshot.TokensUsed {
			remaining = *snapshot.TokenBudget - snapshot.TokensUsed
		}
		projected.TokensRemaining = &remaining
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		return "", fmt.Errorf("encode Goal snapshot: %w", err)
	}
	return string(encoded), nil
}
