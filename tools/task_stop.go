package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const taskStopPrompt = `Stops a running background task (shell or agent) by its ID.

- For shell tasks: terminates the running process.
- For agent tasks: sends a shutdown signal to the agent.
- Takes a task_id parameter identifying the task to stop.
- Returns a success or failure status with the task kind.
- Use this tool when you need to terminate a long-running background shell or agent.
- Has no effect on tasks that have already finished.`

// TaskStopTool stops a running background task by ID.
// Mirrors src/tools/TaskStopTool/TaskStopTool.ts from the reference.
func TaskStopTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "TaskStop",
			Desc: taskStopPrompt,
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"task_id": {
					Type:     schema.String,
					Desc:     "The ID of the background task to stop",
					Required: true,
				},
				"shell_id": {
					Type: schema.String,
					Desc: "Deprecated: use task_id instead",
				},
			}),
		},
		Execute: executeTaskStop,
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			manager, err := taskManagerForToolContext(ctx)
			if err != nil {
				return "", err
			}
			result, mutated, err := executeTaskStopWithRuntimeAndMutation(
				input,
				AgentRunnerFromCtx(ctx),
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

func executeTaskStop(input string) (string, error) {
	return executeTaskStopWithRuntime(input, nil, nil)
}

func executeTaskStopWithRuntime(
	input string,
	runner *AgentRunner,
	manager *TaskManager,
) (string, error) {
	result, _, err := executeTaskStopWithRuntimeAndMutation(input, runner, manager)
	return result, err
}

func executeTaskStopWithRuntimeAndMutation(
	input string,
	runner *AgentRunner,
	manager *TaskManager,
) (string, bool, error) {
	if manager == nil {
		return "", false, ErrMissingToolOwner
	}
	var params struct {
		TaskID  string `json:"task_id"`
		TaskId  string `json:"taskId"` //nolint:revive // backward compat JSON key
		ShellID string `json:"shell_id"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", false, fmt.Errorf("task stop: invalid params: %w", err)
	}

	// Support both task_id and shell_id (deprecated KillShell compat).
	id := firstNonEmpty(params.TaskID, params.TaskId, params.ShellID)
	if strings.TrimSpace(id) == "" {
		return "", false, fmt.Errorf("task stop: missing required parameter: task_id")
	}
	id = strings.TrimSpace(id)

	// Look up the task first to validate it exists.
	task, ok := manager.Get(id)
	if !ok {
		// Also try the AgentRunner for background agent tasks.
		if runner != nil {
			if _, agentFound := runner.GetAgent(id); agentFound {
				if err := runner.AbortAgent(id); err != nil {
					return "", false, fmt.Errorf("task stop: %s", err.Error())
				}
				return formatStopResult(id, "local_agent", ""), false, nil
			}
		}
		return "", false, fmt.Errorf("task stop: no task found with ID: %s", id)
	}

	// Check if task is already in a terminal state — handle gracefully.
	if isTerminalLocalTaskStatus(task.Status) {
		return formatStopResult(task.ID, "local_task", task.Subject), false, nil
	}

	// Validate the task is in a stoppable state.
	if task.Status != TaskStatusInProgress && task.Status != TaskStatusRunning && task.Status != TaskStatusPending {
		return "", false, fmt.Errorf("task stop: task %s is not running (status: %s)", id, task.Status)
	}

	// Perform the stop via TaskManager — transitions to "killed" status.
	stoppedTask, err := manager.Stop(id)
	if err != nil {
		return "", false, fmt.Errorf("task stop: %s", err.Error())
	}

	return formatStopResult(stoppedTask.ID, "local_task", stoppedTask.Subject), true, nil
}

// formatStopResult builds the structured stop result matching the reference's
// output format: { message, task_id, task_type, command }.
func formatStopResult(taskID, taskType, command string) string {
	description := command
	if description == "" {
		description = taskID
	}
	return fmt.Sprintf(
		"Successfully stopped task: %s (%s)\ntask_id: %s\ntask_type: %s",
		taskID, description, taskID, taskType,
	)
}

// isTerminalLocalTaskStatus returns true if the local task status represents
// a completed lifecycle, matching the reference check.
func isTerminalLocalTaskStatus(status TaskStatus) bool {
	switch status {
	case TaskStatusCompleted, TaskStatusFailed, TaskStatusKilled:
		return true
	default:
		return false
	}
}
