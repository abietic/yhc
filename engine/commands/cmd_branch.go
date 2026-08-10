package commands

import (
	"strings"
)

// ActionFork signals the TUI to perform a fork operation (branch + continue on new branch).
const ActionFork CommandAction = "fork"

// executeFork implements /fork — creates a branch from the current point and immediately
// switches to the new branch session. This is like /branch + /resume <new-branch>.
//
// Usage:
//
//	/fork             → fork and continue on the new branch
//	/fork <name>      → fork with a custom name
func executeFork(ctx *CommandContext, args string) (*CommandResult, error) {
	branchName := strings.TrimSpace(args)
	return &CommandResult{
		Action: ActionFork,
		Data: map[string]any{
			"branch_name": branchName,
		},
	}, nil
}
