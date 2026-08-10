package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const taskGetPrompt = `Use this tool to retrieve a task by its ID from the task list.

## When to Use This Tool

- When you need the full description and context before starting work on a task
- To understand task dependencies (what it blocks, what blocks it)
- After being assigned a task, to get complete requirements

## Output

Returns full task details:
- **subject**: Task title
- **description**: Detailed requirements and context
- **status**: 'pending', 'in_progress', or 'completed'
- **blocks**: Tasks waiting on this one to complete
- **blockedBy**: Tasks that must complete before this one can start

## Tips

- After fetching a task, verify its blockedBy list is empty before beginning work.
- Use TaskList to see all tasks in summary form.
`

func TaskGetTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "TaskGet",
			Desc: taskGetPrompt,
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"task_id": {Type: schema.String, Desc: "The ID of the task to retrieve", Required: true},
			}),
		},
		Execute: func(input string) (string, error) {
			return executeTaskGetWithManager(input, nil)
		},
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			manager, err := taskManagerForToolContext(ctx)
			if err != nil {
				return "", err
			}
			return executeTaskGetWithManager(input, manager)
		},
	}
}

func executeTaskGetWithManager(
	input string,
	manager *TaskManager,
) (string, error) {
	if manager == nil {
		return "", ErrMissingToolOwner
	}
	var params struct {
		TaskID string `json:"task_id"`
		TaskId string `json:"taskId"` //nolint:revive // backward compat JSON key
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("task get: invalid params: %w", err)
	}
	id := firstNonEmpty(params.TaskID, params.TaskId)
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("task get: task_id is required")
	}
	task, ok := manager.Get(id)
	if !ok {
		return "Task not found", nil
	}
	lines := []string{
		fmt.Sprintf("Task #%s: %s", task.ID, task.Subject),
		fmt.Sprintf("Status: %s", task.Status),
		fmt.Sprintf("Description: %s", task.Description),
	}
	if len(task.BlockedBy) > 0 {
		lines = append(lines, fmt.Sprintf("Blocked by: %s", withTaskHashes(task.BlockedBy)))
	}
	if len(task.Blocks) > 0 {
		lines = append(lines, fmt.Sprintf("Blocks: %s", withTaskHashes(task.Blocks)))
	}
	if task.Owner != "" {
		lines = append(lines, fmt.Sprintf("Owner: %s", task.Owner))
	}
	return strings.Join(lines, "\n"), nil
}
