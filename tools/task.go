package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

type taskManagerCtxKey struct{}

// WithTaskManager returns a context carrying the task store owned by the
// current root QueryEngine lineage.
func WithTaskManager(ctx context.Context, manager *TaskManager) context.Context {
	return context.WithValue(ctx, taskManagerCtxKey{}, manager)
}

// TaskManagerFromCtx returns only the explicitly bound compatibility facade.
func TaskManagerFromCtx(ctx context.Context) *TaskManager {
	if ctx != nil {
		if manager, ok := ctx.Value(taskManagerCtxKey{}).(*TaskManager); ok &&
			manager != nil {
			return manager
		}
	}
	return nil
}

func taskManagerForToolContext(ctx context.Context) (*TaskManager, error) {
	manager := TaskManagerFromCtx(ctx)
	if manager == nil {
		if err := durableLogicalWorkScopeError(ctx); err != nil {
			return nil, err
		}
		return nil, ErrMissingToolOwner
	}
	if strings.TrimSpace(SessionIDFromCtx(ctx)) == "" {
		return manager, nil
	}
	if manager != nil && manager.AuthorityBound() {
		return manager, nil
	}
	return nil, durableLogicalWorkScopeError(ctx)
}

func TaskTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "Task",
			Desc: "Manage background tasks and task-list records. Supports create, get, list, update, stop, and output actions.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"action":      {Type: schema.String, Desc: "Action: create, get, list, update, stop, output", Required: true},
				"task_id":     {Type: schema.String, Desc: "Task ID for get/update/stop/output"},
				"subject":     {Type: schema.String, Desc: "Short task title for create/update"},
				"description": {Type: schema.String, Desc: "Task description for create/update"},
				"active_form": {Type: schema.String, Desc: "Present-continuous status label"},
				"status":      {Type: schema.String, Desc: "Task status for update: pending, in_progress, completed, failed, killed"},
				"owner":       {Type: schema.String, Desc: "Task owner for update"},
				"output":      {Type: schema.String, Desc: "Append task output during update"},
			}),
		},
		Execute:    executeCombinedTaskTool,
		ExecuteCtx: executeCombinedTaskToolWithContext,
	}
}

func executeCombinedTaskTool(input string) (string, error) {
	return executeCombinedTaskToolWithContext(context.Background(), input)
}

func executeCombinedTaskToolWithContext(
	ctx context.Context,
	input string,
) (string, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(input), &raw); err != nil {
		return "", fmt.Errorf("task: invalid params: %w", err)
	}
	action, _ := raw["action"].(string)
	action = strings.TrimSpace(strings.ToLower(action))
	if action == "" {
		return "", fmt.Errorf("task: action is required")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return "", fmt.Errorf("task: marshal params: %w", err)
	}

	var tool ToolImpl
	switch action {
	case "create":
		tool = TaskCreateTool()
	case "get":
		tool = TaskGetTool()
	case "list":
		tool = TaskListTool()
	case "update":
		tool = TaskUpdateTool()
	case "stop", "cancel":
		tool = TaskStopTool()
	case "output", "monitor":
		tool = TaskOutputTool()
	default:
		return "", fmt.Errorf("task: unsupported action %q", action)
	}
	if tool.ExecuteCtx != nil {
		return tool.ExecuteCtx(ctx, string(encoded))
	}
	return tool.Execute(string(encoded))
}
