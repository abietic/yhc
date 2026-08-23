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

type reasoningEffortControl interface {
	ReasoningEffortCapability(context.Context) (supported bool, reason string, err error)
	ReasoningEffort() string
}

type reasoningEffortOptionSource interface {
	ReasoningEffortOptions(context.Context) ([]string, error)
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
	levels, err := availableEffortLevels(ctx)
	if err != nil {
		return nil, err
	}
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
				strings.Join(levels, ", "),
			),
		}, nil
	}

	level := strings.ToLower(strings.TrimSpace(args))
	if !validEffortLevel(level, levels) {
		return nil, fmt.Errorf(
			"unsupported reasoning effort %q (valid: %s)",
			level,
			strings.Join(levels, ", "),
		)
	}

	return &CommandResult{
		Output: "Applying reasoning effort...",
		Action: ActionSetEffort,
		Data:   map[string]any{"level": level},
	}, nil
}

func availableEffortLevels(cmdCtx *CommandContext) ([]string, error) {
	if cmdCtx == nil || cmdCtx.Engine == nil {
		return nil, fmt.Errorf("reasoning effort requires an active provider runtime")
	}
	control, ok := cmdCtx.Engine.(reasoningEffortOptionSource)
	if !ok {
		return nil, fmt.Errorf("the active runtime does not expose reasoning effort options")
	}
	ctx := cmdCtx.Context
	if ctx == nil {
		ctx = context.Background()
	}
	options, err := control.ReasoningEffortOptions(ctx)
	if err != nil {
		return nil, err
	}
	levels := make([]string, 0, len(options)+1)
	levels = append(levels, "default")
	levels = append(levels, options...)
	return levels, nil
}

func validEffortLevel(level string, levels []string) bool {
	for _, candidate := range levels {
		if level == candidate {
			return true
		}
	}
	return false
}
