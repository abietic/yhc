package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

type goalCommandCapability interface {
	GoalCommandAvailability() (bool, string)
}

func goalCommand() *Command {
	return &Command{
		Name:        "goal",
		Description: "Create, inspect, or control the durable Goal workflow",
		Usage:       "/goal [<objective>|edit <objective>|pause|resume|clear|budget <tokens>]",
		DetailedHelp: `Goal is an opt-in saved-session workflow. Creating without a configured
default budget records a paused draft; set a positive budget before resuming.

Examples:
  /goal
  /goal Finish every accepted migration slice
  /goal edit Finish the remaining release gates
  /goal budget 200000
  /goal resume
  /goal pause
  /goal clear`,
		ResolveAvailability: resolveGoalAvailability,
		Execute:             executeGoal,
	}
}

func resolveGoalAvailability(
	_ context.Context,
	ctx *CommandContext,
) (AvailabilityState, string) {
	if ctx == nil {
		return AvailabilityUnavailable, "Goal requires an engine runtime"
	}
	capability, ok := ctx.Engine.(goalCommandCapability)
	if !ok {
		return AvailabilityUnavailable, "Goal requires an engine runtime"
	}
	available, reason := capability.GoalCommandAvailability()
	if !available {
		return AvailabilityDisabled, reason
	}
	return AvailabilitySupported, ""
}

func executeGoal(
	_ context.Context,
	ctx *CommandContext,
) (*CommandResult, error) {
	args := []string(nil)
	if ctx != nil {
		args = append(args, ctx.Args...)
		if _, parsed := ParseCommandInput(ctx.RawInput); len(parsed) > 0 {
			args = append(args[:0], parsed...)
		}
	}
	operation := "view"
	data := map[string]any{}
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "edit":
			operation = "edit"
			objective := strings.TrimSpace(strings.Join(args[1:], " "))
			if objective == "" {
				return nil, fmt.Errorf("usage: /goal edit <objective>")
			}
			data["objective"] = objective
		case "pause", "resume", "clear":
			if len(args) != 1 {
				return nil, fmt.Errorf("usage: /goal %s", strings.ToLower(args[0]))
			}
			operation = strings.ToLower(args[0])
		case "budget":
			if len(args) != 2 {
				return nil, fmt.Errorf("usage: /goal budget <positive-tokens>")
			}
			budget, err := strconv.ParseUint(args[1], 10, 64)
			if err != nil || budget == 0 {
				return nil, fmt.Errorf("goal token budget must be a positive integer")
			}
			operation = "budget"
			data["token_budget"] = strconv.FormatUint(budget, 10)
		default:
			operation = "create"
			data["objective"] = strings.TrimSpace(strings.Join(args, " "))
		}
	}
	data["operation"] = operation
	return &CommandResult{
		Action: ActionGoal,
		Data:   data,
	}, nil
}
