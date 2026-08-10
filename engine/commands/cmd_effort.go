package commands

import (
	"context"
	"fmt"
	"strings"
)

// ActionSetEffort signals the engine to update provider reasoning effort for
// subsequent model calls. It is deliberately separate from continuation token
// budgeting.
const ActionSetEffort CommandAction = "set_effort"

var effortLevels = []string{"default", "low", "medium", "high", "max"}

type reasoningEffortControl interface {
	ReasoningEffortCapability(context.Context) (supported bool, reason string, err error)
	ReasoningEffort() string
}

func resolveEffortAvailability(
	ctx context.Context,
	cmdCtx *CommandContext,
) (AvailabilityState, string) {
	if cmdCtx == nil || cmdCtx.Engine == nil {
		return AvailabilityUnavailable, "reasoning effort requires an active provider runtime"
	}
	control, ok := cmdCtx.Engine.(reasoningEffortControl)
	if !ok {
		return AvailabilityUnavailable, "the active runtime does not expose reasoning effort control"
	}
	supported, reason, err := control.ReasoningEffortCapability(ctx)
	if err != nil {
		return AvailabilityUnavailable, err.Error()
	}
	if !supported {
		if strings.TrimSpace(reason) == "" {
			reason = "the selected provider/model does not support compatible reasoning effort"
		}
		return AvailabilityUnavailable, reason
	}
	return AvailabilitySupported, ""
}

// executeEffort shows or changes the provider reasoning effort used by later
// model calls. Capability admission is resolved by the registry before this
// handler runs.
func executeEffort(ctx *CommandContext, args string) (*CommandResult, error) {
	if strings.TrimSpace(args) == "" {
		current := "default"
		if control, ok := ctx.Engine.(reasoningEffortControl); ok {
			if effective := strings.TrimSpace(control.ReasoningEffort()); effective != "" {
				current = effective
			}
		}
		return &CommandResult{
			Output: fmt.Sprintf(
				"Current reasoning effort: %s\nAvailable levels: %s\nUsage: /effort <level>",
				current,
				strings.Join(effortLevels, ", "),
			),
		}, nil
	}

	level := strings.ToLower(strings.TrimSpace(args))
	if !validEffortLevel(level) {
		return nil, fmt.Errorf(
			"unsupported reasoning effort %q (valid: %s)",
			level,
			strings.Join(effortLevels, ", "),
		)
	}

	return &CommandResult{
		Output: "Applying reasoning effort...",
		Action: ActionSetEffort,
		Data:   map[string]any{"level": level},
	}, nil
}

func validEffortLevel(level string) bool {
	for _, candidate := range effortLevels {
		if level == candidate {
			return true
		}
	}
	return false
}
