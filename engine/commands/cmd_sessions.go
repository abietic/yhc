package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

const defaultSessionsLimit = 10

func executeSessions(_ context.Context, ctx *CommandContext) (*CommandResult, error) {
	args := append([]string(nil), ctx.Args...)
	if len(args) == 0 {
		return sessionsListResult("", defaultSessionsLimit), nil
	}

	switch strings.ToLower(args[0]) {
	case "list", "ls":
		limit, err := parseSessionsLimit(args[1:])
		if err != nil {
			return nil, err
		}
		return sessionsListResult("", limit), nil
	case "search", "find":
		if len(args) < 2 {
			return nil, fmt.Errorf("usage: /sessions search <query> [limit]")
		}
		queryArgs := args[1:]
		limit := defaultSessionsLimit
		if len(queryArgs) > 1 {
			if parsed, err := strconv.Atoi(queryArgs[len(queryArgs)-1]); err == nil {
				if parsed <= 0 {
					return nil, fmt.Errorf("session limit must be positive")
				}
				limit = parsed
				queryArgs = queryArgs[:len(queryArgs)-1]
			}
		}
		query := strings.TrimSpace(strings.Join(queryArgs, " "))
		if query == "" {
			return nil, fmt.Errorf("session search query is required")
		}
		return sessionsListResult(query, limit), nil
	case "resume":
		if len(args) != 2 || strings.TrimSpace(args[1]) == "" {
			return nil, fmt.Errorf("usage: /sessions resume <session-id>")
		}
		return &CommandResult{
			Output: fmt.Sprintf("Resuming session %s...", args[1]),
			Action: ActionResume,
			Data:   map[string]any{"session_id": args[1]},
		}, nil
	case "rename":
		if len(args) < 3 {
			return nil, fmt.Errorf("usage: /sessions rename <session-id> <name>")
		}
		sessionID, current := sessionTargetArgument(args[1])
		name := strings.TrimSpace(strings.Join(args[2:], " "))
		if (!current && sessionID == "") || name == "" {
			return nil, fmt.Errorf("session ID and name are required")
		}
		return &CommandResult{
			Action: ActionRename,
			Data: map[string]any{
				"session_id": sessionID,
				"name":       name,
			},
		}, nil
	case "export":
		if len(args) < 2 || len(args) > 3 {
			return nil, fmt.Errorf("usage: /sessions export <session-id> [filename]")
		}
		sessionID, current := sessionTargetArgument(args[1])
		if !current && sessionID == "" {
			return nil, fmt.Errorf("session ID is required")
		}
		filename := ""
		if len(args) == 3 {
			filename = strings.TrimSpace(args[2])
		}
		return &CommandResult{
			Action: ActionExport,
			Data: map[string]any{
				"session_id": sessionID,
				"filename":   filename,
			},
		}, nil
	default:
		return nil, fmt.Errorf(
			"unknown /sessions operation %q; use list, search, resume, rename, or export",
			args[0],
		)
	}
}

func sessionsListResult(search string, limit int) *CommandResult {
	return &CommandResult{
		Action: ActionSessions,
		Data: map[string]any{
			"operation": "list",
			"search":    search,
			"limit":     strconv.Itoa(limit),
		},
	}
}

func parseSessionsLimit(args []string) (int, error) {
	switch len(args) {
	case 0:
		return defaultSessionsLimit, nil
	case 1:
		limit, err := strconv.Atoi(args[0])
		if err != nil || limit <= 0 {
			return 0, fmt.Errorf("session limit must be a positive integer")
		}
		return limit, nil
	default:
		return 0, fmt.Errorf("usage: /sessions list [limit]")
	}
}

func sessionTargetArgument(value string) (string, bool) {
	if strings.EqualFold(strings.TrimSpace(value), "current") {
		return "", true
	}
	return strings.TrimSpace(value), false
}
