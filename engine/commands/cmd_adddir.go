package commands

import "strings"

// ActionAddDir asks the engine owner to validate and atomically expand the
// active workspace roots. The command handler itself performs no filesystem or
// permission mutation.
const ActionAddDir CommandAction = "add_dir"

func executeAddDir(_ *CommandContext, args string) (*CommandResult, error) {
	path := strings.TrimSpace(args)
	if path == "" {
		return &CommandResult{
			Output: "Usage: /add-dir <path>\nAdds one canonical directory to the current session's active workspace roots.",
		}, nil
	}
	return &CommandResult{
		Action: ActionAddDir,
		Data:   map[string]any{"path": path},
	}, nil
}
