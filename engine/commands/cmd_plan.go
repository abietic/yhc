package commands

import (
	"fmt"
	"strings"

	"github.com/abietic/yhc/engine/permission"
)

// ActionPlanMode is used to signal the caller to toggle plan mode.
const ActionPlanMode CommandAction = "plan_mode"

// executePlan implements the /plan command.
// - When not in plan mode: enables plan mode (returns ActionPlanMode).
// - When already in plan mode: shows plan mode status or disables it.
// - /plan <description>: enables plan mode and sends the description as a prompt.
// Mirrors src/commands/plan/ from the reference.
func executePlan(ctx *CommandContext, args string) (*CommandResult, error) {
	// Check current plan mode state from engine.
	inPlanMode := false
	if eng, ok := ctx.Engine.(interface{ PermissionMode() permission.Mode }); ok {
		inPlanMode = eng.PermissionMode() == permission.ModePlan
	}

	if !inPlanMode {
		// Enable plan mode.
		result := &CommandResult{
			Action: ActionPlanMode,
			Data:   map[string]any{"enable": true},
		}

		if args != "" && args != "off" {
			// User provided a description — enable plan mode and send as prompt.
			result.Output = fmt.Sprintf("Enabled plan mode.\n\nPlanning: %s", args)
			result.Data["description"] = args
		} else {
			result.Output = "Enabled plan mode. Tools are restricted to read-only operations.\nUse /plan off to exit plan mode."
		}
		return result, nil
	}

	// Already in plan mode.
	if args == "off" || args == "exit" {
		return &CommandResult{
			Output: "Exited plan mode. All tools are now available.",
			Action: ActionPlanMode,
			Data:   map[string]any{"enable": false},
		}, nil
	}

	// Show plan mode status.
	var sb strings.Builder
	sb.WriteString("Plan Mode: ACTIVE\n")
	sb.WriteString("Tools are restricted to read-only operations.\n")
	sb.WriteString("\nCommands:\n")
	sb.WriteString("  /plan off    — exit plan mode\n")
	sb.WriteString("  /plan <desc> — (re)start planning with a description\n")
	return &CommandResult{Output: sb.String()}, nil
}
