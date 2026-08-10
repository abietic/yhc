package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

// TaskOutputMaxLength is the maximum output length before truncation.
// Mirrors TASK_MAX_OUTPUT_DEFAULT from the reference (32KB default).
const TaskOutputMaxLength = 32_000

// TaskOutputTool retrieves output from a running or completed task.
// Mirrors src/tools/TaskOutputTool/TaskOutputTool.tsx from the reference.
//
// Key behavioral contracts:
//   - accepts task_id (required)
//   - supports block (default true) and timeout (default 30000ms, max 600000ms)
//   - non-blocking (block=false): returns current state immediately
//   - blocking (block=true): polls until task completes or timeout
//   - returns structured XML-like output matching reference format
//   - handles running, completed, failed, and killed states
func TaskOutputTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "TaskOutput",
			Desc: "Retrieve output from a running or completed task",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"task_id": {
					Type:     schema.String,
					Desc:     "The task ID to get output from",
					Required: true,
				},
				"block": {
					Type: schema.Boolean,
					Desc: "Whether to wait for completion (default: true)",
				},
				"timeout": {
					Type: schema.Number,
					Desc: "Max wait time in ms when block=true (default: 30000, max: 600000)",
				},
			}),
		},
		Execute: executeTaskOutput,
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			manager, err := taskManagerForToolContext(ctx)
			if err != nil {
				return "", err
			}
			return executeTaskOutputWithRuntime(
				input,
				AgentRunnerFromCtx(ctx),
				manager,
			)
		},
	}
}

func executeTaskOutput(input string) (string, error) {
	return executeTaskOutputWithRuntime(input, nil, nil)
}

func executeTaskOutputWithRuntime(
	input string,
	runner *AgentRunner,
	manager *TaskManager,
) (string, error) {
	if manager == nil {
		return "", ErrMissingToolOwner
	}
	var params struct {
		TaskID  string   `json:"task_id"`
		TaskId  string   `json:"taskId"` //nolint:revive // JSON compat alias
		Block   *bool    `json:"block"`
		Timeout *float64 `json:"timeout"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("task output: invalid params: %w", err)
	}

	id := firstNonEmpty(params.TaskID, params.TaskId)
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("task output: task_id is required")
	}
	id = strings.TrimSpace(id)

	// Resolve default parameters — mirrors reference defaults.
	block := true
	if params.Block != nil {
		block = *params.Block
	}
	timeoutMs := 30000.0
	if params.Timeout != nil {
		timeoutMs = *params.Timeout
	}
	// Clamp timeout to 0-600000ms range.
	if timeoutMs < 0 {
		timeoutMs = 0
	}
	if timeoutMs > 600000 {
		timeoutMs = 600000
	}

	// First try local task manager.
	task, ok := manager.Get(id)
	if ok {
		return resolveLocalTaskOutput(task, manager, id, block, timeoutMs)
	}

	// Try the agent runner for background agents.
	if runner != nil {
		snapshot, agentFound := runner.GetAgentSnapshot(id)
		if agentFound {
			return resolveAgentTaskOutput(&snapshot, runner, block, timeoutMs)
		}
	}

	return "", fmt.Errorf("task output: no task found with ID: %s", id)
}

// resolveLocalTaskOutput handles output retrieval for local tasks from the TaskManager.
func resolveLocalTaskOutput(task *TaskRecord, manager *TaskManager, id string, block bool, timeoutMs float64) (string, error) {
	if !block {
		// Non-blocking: return current state immediately.
		return formatLocalTaskOutput(task), nil
	}

	// If already terminal, return immediately.
	if isTerminalLocalTaskStatus(task.Status) {
		return formatLocalTaskOutput(task), nil
	}

	// Blocking: poll until terminal or timeout.
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	pollInterval := 100 * time.Millisecond

	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		task, _ = manager.Get(id)
		if task == nil {
			return "", fmt.Errorf("task output: task %s disappeared during polling", id)
		}
		if isTerminalLocalTaskStatus(task.Status) {
			return formatLocalTaskOutput(task), nil
		}
	}

	// Timeout — return current state with timeout status.
	task, _ = manager.Get(id)
	if task == nil {
		return formatTimeoutOutput(id), nil
	}
	return formatLocalTaskOutputWithStatus(task, "timeout"), nil
}

// resolveAgentTaskOutput handles output retrieval for background agent tasks.
func resolveAgentTaskOutput(snapshot *RunningAgent, runner *AgentRunner, block bool, timeoutMs float64) (string, error) {
	id := snapshot.ID

	if !block {
		return formatAgentOutput(snapshot), nil
	}

	// If already terminal, return immediately.
	if isTerminalAgentStatus(snapshot.Status) {
		return formatAgentOutput(snapshot), nil
	}

	// Blocking: poll until terminal or timeout.
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	pollInterval := 100 * time.Millisecond

	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		if runner == nil {
			break
		}
		updated, found := runner.GetAgentSnapshot(id)
		if !found {
			break
		}
		snapshot = &updated
		if isTerminalAgentStatus(snapshot.Status) {
			return formatAgentOutput(snapshot), nil
		}
	}

	// Timeout — return current state.
	return formatAgentOutputWithStatus(snapshot, "timeout"), nil
}

// formatLocalTaskOutput formats task output matching the reference's structured
// XML-like mapToolResultToToolResultBlockParam format.
func formatLocalTaskOutput(task *TaskRecord) string {
	return formatLocalTaskOutputWithStatus(task, "success")
}

func formatLocalTaskOutputWithStatus(task *TaskRecord, retrievalStatus string) string {
	if task == nil {
		return "<retrieval_status>not_ready</retrieval_status>"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "<retrieval_status>%s</retrieval_status>\n\n", retrievalStatus)
	fmt.Fprintf(&sb, "<task_id>%s</task_id>\n\n", task.ID)
	sb.WriteString("<task_type>local_task</task_type>\n\n")
	fmt.Fprintf(&sb, "<status>%s</status>", task.Status)

	output := strings.TrimSpace(task.Output)
	if output != "" {
		formatted := truncateTaskOutput(output, task.ID)
		fmt.Fprintf(&sb, "\n\n<output>\n%s\n</output>", formatted)
	}

	return sb.String()
}

// formatAgentOutput formats agent task output matching the reference structure.
func formatAgentOutput(agent *RunningAgent) string {
	return formatAgentOutputWithStatus(agent, "success")
}

func formatAgentOutputWithStatus(agent *RunningAgent, retrievalStatus string) string {
	if agent == nil {
		return "<retrieval_status>not_ready</retrieval_status>"
	}

	// Determine actual retrieval status based on agent state.
	status := retrievalStatus
	if !isTerminalAgentStatus(agent.Status) && retrievalStatus == "success" {
		status = "not_ready"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "<retrieval_status>%s</retrieval_status>\n\n", status)
	fmt.Fprintf(&sb, "<task_id>%s</task_id>\n\n", agent.ID)
	sb.WriteString("<task_type>local_agent</task_type>\n\n")
	fmt.Fprintf(&sb, "<status>%s</status>", agent.Status)

	// For agents, prefer the clean result over raw output.
	output := strings.TrimSpace(agent.Result)
	if output != "" {
		formatted := truncateTaskOutput(output, agent.ID)
		fmt.Fprintf(&sb, "\n\n<output>\n%s\n</output>", formatted)
	}

	if agent.Error != nil {
		fmt.Fprintf(&sb, "\n\n<error>%s</error>", agent.Error.Error())
	}
	if details := formatAgentWorktreeDetails(agent.Worktree); details != "" {
		fmt.Fprintf(
			&sb,
			"\n\n<worktree_handoff>\n%s\n</worktree_handoff>",
			details,
		)
	}

	return sb.String()
}

func formatTimeoutOutput(taskID string) string {
	var sb strings.Builder
	sb.WriteString("<retrieval_status>timeout</retrieval_status>\n\n")
	fmt.Fprintf(&sb, "<task_id>%s</task_id>", taskID)
	return sb.String()
}

// truncateTaskOutput truncates output that exceeds the maximum length,
// keeping the tail (most recent output). Mirrors the reference's formatTaskOutput.
func truncateTaskOutput(output, _ string) string {
	if len(output) <= TaskOutputMaxLength {
		return output
	}
	header := fmt.Sprintf("[Truncated — showing last %d chars of %d total]\n\n", TaskOutputMaxLength-100, len(output))
	available := TaskOutputMaxLength - len(header)
	if available <= 0 {
		return output[len(output)-TaskOutputMaxLength:]
	}
	return header + output[len(output)-available:]
}
