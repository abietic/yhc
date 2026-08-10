package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const taskUpdatePrompt = `Use this tool to update a task in the task list.

## When to Use This Tool

**Mark tasks as resolved:**
- When you have completed the work described in a task
- When a task is no longer needed or has been superseded
- IMPORTANT: Always mark your assigned tasks as resolved when you finish them
- After resolving, call TaskList to find your next task

- ONLY mark a task as completed when you have FULLY accomplished it
- If you encounter errors, blockers, or cannot finish, keep the task as in_progress
- When blocked, create a new task describing what needs to be resolved
- Never mark a task as completed if:
  - Tests are failing
  - Implementation is partial
  - You encountered unresolved errors
  - You couldn't find necessary files or dependencies

**Delete tasks:**
- When a task is no longer relevant or was created in error
- Setting status to 'deleted' permanently removes the task

**Update task details:**
- When requirements change or become clearer
- When establishing dependencies between tasks

## Fields You Can Update

- **status**: The task status (see Status Workflow below)
- **subject**: Change the task title (imperative form, e.g., "Run tests")
- **description**: Change the task description
- **activeForm**: Present continuous form shown in spinner when in_progress (e.g., "Running tests")
- **owner**: Change the task owner (agent name)
- **metadata**: Merge metadata keys into the task (set a key to null to delete it)
- **addBlocks**: Mark tasks that cannot start until this one completes
- **addBlockedBy**: Mark tasks that must complete before this one can start

## Status Workflow

Status progresses: 'pending' -> 'in_progress' -> 'completed'

Use 'deleted' to permanently remove a task.

## Staleness

Make sure to read a task's latest state using TaskGet before updating it.
`

func TaskUpdateTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "TaskUpdate",
			Desc: taskUpdatePrompt,
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"task_id":        {Type: schema.String, Desc: "The ID of the task to update", Required: true},
				"subject":        {Type: schema.String, Desc: "New subject for the task"},
				"description":    {Type: schema.String, Desc: "New description for the task"},
				"active_form":    {Type: schema.String, Desc: "New present-continuous label"},
				"status":         {Type: schema.String, Desc: "New task status"},
				"owner":          {Type: schema.String, Desc: "New owner"},
				"add_blocks":     {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}, Desc: "Task IDs this task blocks"},
				"add_blocked_by": {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}, Desc: "Task IDs that block this task"},
				"output":         {Type: schema.String, Desc: "Append task output"},
			}),
		},
		Execute: func(input string) (string, error) {
			return executeTaskUpdateWithManager(input, nil)
		},
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			manager, err := taskManagerForToolContext(ctx)
			if err != nil {
				return "", err
			}
			result, mutated, err := executeTaskUpdateWithManagerAndMutation(
				input,
				manager,
			)
			if err != nil {
				return "", err
			}
			if mutated {
				observeWorkBoardTasks(ctx, manager)
			}
			return result, nil
		},
	}
}

func executeTaskUpdateWithManager(
	input string,
	manager *TaskManager,
) (string, error) {
	result, _, err := executeTaskUpdateWithManagerAndMutation(input, manager)
	return result, err
}

func executeTaskUpdateWithManagerAndMutation(
	input string,
	manager *TaskManager,
) (string, bool, error) {
	if manager == nil {
		return "", false, ErrMissingToolOwner
	}
	var params struct {
		TaskID       string         `json:"task_id"`
		TaskId       string         `json:"taskId"` //nolint:revive // backward compat JSON key
		Subject      *string        `json:"subject"`
		Description  *string        `json:"description"`
		ActiveForm   *string        `json:"active_form"`
		Status       *string        `json:"status"`
		Owner        *string        `json:"owner"`
		AddBlocks    []string       `json:"add_blocks"`
		AddBlockedBy []string       `json:"add_blocked_by"`
		Metadata     map[string]any `json:"metadata"`
		Output       *string        `json:"output"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", false, fmt.Errorf("task update: invalid params: %w", err)
	}
	id := firstNonEmpty(params.TaskID, params.TaskId)
	if strings.TrimSpace(id) == "" {
		return "", false, fmt.Errorf("task update: task_id is required")
	}
	update := TaskUpdate{
		TaskID:       id,
		Subject:      params.Subject,
		Description:  params.Description,
		ActiveForm:   params.ActiveForm,
		Owner:        params.Owner,
		AddBlocks:    params.AddBlocks,
		AddBlockedBy: params.AddBlockedBy,
		Metadata:     params.Metadata,
		Output:       params.Output,
	}
	if params.Status != nil {
		status := normalizeTaskStatus(TaskStatus(strings.ToLower(strings.TrimSpace(*params.Status))))
		update.Status = &status
	}
	task, fields, err := manager.Update(update)
	if err != nil {
		return "", false, err
	}
	if len(fields) == 0 {
		return fmt.Sprintf("Task #%s unchanged", task.ID), false, nil
	}
	return fmt.Sprintf(
		"Task #%s updated: %s",
		task.ID,
		strings.Join(fields, ", "),
	), true, nil
}
