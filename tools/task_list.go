package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const taskListPrompt = `Use this tool to list all tasks in the task list.

## When to Use This Tool

- To see what tasks are available to work on (status: 'pending', no owner, not blocked)
- To check overall progress on the project
- To find tasks that are blocked and need dependencies resolved
- After completing a task, to check for newly unblocked work or claim the next available task
- **Prefer working on tasks in ID order** (lowest ID first) when multiple tasks are available, as earlier tasks often set up context for later ones

## Output

Returns a summary of each task:
- **id**: Task identifier (use with TaskGet, TaskUpdate)
- **subject**: Brief description of the task
- **status**: 'pending', 'in_progress', or 'completed'
- **owner**: Agent ID if assigned, empty if available
- **blockedBy**: List of open task IDs that must be resolved first (tasks with blockedBy cannot be claimed until dependencies resolve)

Use TaskGet with a specific task ID to view full details including description and comments.
`

// TaskListTool lists all tasks with their status information.
// Mirrors src/tools/TaskListTool from the reference.
func TaskListTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "TaskList",
			Desc: taskListPrompt,
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"status": {
					Type: schema.String,
					Desc: "Filter by status: pending, in_progress, completed, failed, killed (optional)",
				},
			}),
		},
		Execute: executeTaskList,
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			manager, err := taskManagerForToolContext(ctx)
			if err != nil {
				return "", err
			}
			return executeTaskListWithManager(input, manager)
		},
	}
}

func executeTaskList(input string) (string, error) {
	return executeTaskListWithManager(input, nil)
}

func executeTaskListWithManager(
	input string,
	manager *TaskManager,
) (string, error) {
	if manager == nil {
		return "", ErrMissingToolOwner
	}
	var params struct {
		Status string `json:"status"`
	}
	// Allow empty input — list is valid with no parameters.
	if strings.TrimSpace(input) != "" && strings.TrimSpace(input) != "{}" {
		if err := json.Unmarshal([]byte(input), &params); err != nil {
			return "", fmt.Errorf("task list: invalid params: %w", err)
		}
	}

	tasks := manager.List()

	// Apply status filter if specified.
	statusFilter := strings.TrimSpace(strings.ToLower(params.Status))
	if statusFilter != "" {
		normalizedFilter := normalizeTaskStatus(TaskStatus(statusFilter))
		filtered := make([]*TaskRecord, 0)
		for _, task := range tasks {
			if task.Status == normalizedFilter {
				filtered = append(filtered, task)
			}
		}
		tasks = filtered
	}

	if len(tasks) == 0 {
		if statusFilter != "" {
			return fmt.Sprintf("No tasks found with status: %s", statusFilter), nil
		}
		return "No tasks found", nil
	}

	lines := make([]string, 0, len(tasks))
	for _, task := range tasks {
		line := formatTaskListEntry(task)
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), nil
}

// formatTaskListEntry formats a single task entry for the list output.
func formatTaskListEntry(task *TaskRecord) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "#%s [%s] %s", task.ID, task.Status, task.Subject)

	if task.Owner != "" {
		fmt.Fprintf(&sb, " (%s)", task.Owner)
	}
	if len(task.BlockedBy) > 0 {
		fmt.Fprintf(&sb, " [blocked by %s]", withTaskHashes(task.BlockedBy))
	}
	if task.ActiveForm != "" && task.Status == TaskStatusInProgress {
		fmt.Fprintf(&sb, " — %s", task.ActiveForm)
	}

	return sb.String()
}
