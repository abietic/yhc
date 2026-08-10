package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// TodoItem represents a single item in a structured todo list.
// Mirrors src/utils/todo/types.ts: { content, status, activeForm }.
type TodoItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"`     // "pending", "in_progress", "completed"
	ActiveForm string `json:"activeForm"` // present continuous form for spinner display
}

// FormatTodoList renders the todo list as readable text.
func FormatTodoList(items []TodoItem) string {
	if len(items) == 0 {
		return "Todo list is empty."
	}

	var inProgress, pending, completed []TodoItem
	for _, item := range items {
		switch item.Status {
		case "in_progress":
			inProgress = append(inProgress, item)
		case "completed":
			completed = append(completed, item)
		default:
			pending = append(pending, item)
		}
	}

	var sb strings.Builder
	sb.WriteString("Todo List:\n")

	if len(inProgress) > 0 {
		sb.WriteString("\n## In Progress\n")
		for _, item := range inProgress {
			fmt.Fprintf(&sb, "  [~] %s\n", item.Content)
		}
	}

	if len(pending) > 0 {
		sb.WriteString("\n## Pending\n")
		for _, item := range pending {
			fmt.Fprintf(&sb, "  [ ] %s\n", item.Content)
		}
	}

	if len(completed) > 0 {
		sb.WriteString("\n## Completed\n")
		for _, item := range completed {
			fmt.Fprintf(&sb, "  [x] %s\n", item.Content)
		}
	}

	total := len(items)
	doneCount := len(completed)
	fmt.Fprintf(&sb, "\nProgress: %d/%d completed", doneCount, total)

	return sb.String()
}

const todoWriteToolPrompt = `Use this tool to create and manage a structured task list for your current coding session. This helps you track progress, organize complex tasks, and demonstrate thoroughness to the user.
It also helps the user understand the progress of the task and overall progress of their requests.

## When to Use This Tool
Use this tool proactively in these scenarios:

1. Complex multi-step tasks - When a task requires 3 or more distinct steps or actions
2. Non-trivial and complex tasks - Tasks that require careful planning or multiple operations
3. User explicitly requests todo list - When the user directly asks you to use the todo list
4. User provides multiple tasks - When users provide a list of things to be done (numbered or comma-separated)
5. After receiving new instructions - Immediately capture user requirements as todos
6. When you start working on a task - Mark it as in_progress BEFORE beginning work. Ideally you should only have one todo as in_progress at a time
7. After completing a task - Mark it as completed and add any new follow-up tasks discovered during implementation

## When NOT to Use This Tool

Skip using this tool when:
1. There is only a single, straightforward task
2. The task is trivial and tracking it provides no organizational benefit
3. The task can be completed in less than 3 trivial steps
4. The task is purely conversational or informational

NOTE that you should not use this tool if there is only one trivial task to do. In this case you are better off just doing the task directly.

## Task States and Management

1. **Task States**: Use these states to track progress:
   - pending: Task not yet started
   - in_progress: Currently working on (limit to ONE task at a time)
   - completed: Task finished successfully

   **IMPORTANT**: Task descriptions must have two forms:
   - content: The imperative form describing what needs to be done (e.g., "Run tests", "Build the project")
   - activeForm: The present continuous form shown during execution (e.g., "Running tests", "Building the project")

2. **Task Management**:
   - Update task status in real-time as you work
   - Mark tasks complete IMMEDIATELY after finishing (don't batch completions)
   - Exactly ONE task must be in_progress at any time (not less, not more)
   - Complete current tasks before starting new ones
   - Remove tasks that are no longer relevant from the list entirely

3. **Task Completion Requirements**:
   - ONLY mark a task as completed when you have FULLY accomplished it
   - If you encounter errors, blockers, or cannot finish, keep the task as in_progress
   - When blocked, create a new task describing what needs to be resolved
   - Never mark a task as completed if:
     - Tests are failing
     - Implementation is partial
     - You encountered unresolved errors
     - You couldn't find necessary files or dependencies

4. **Task Breakdown**:
   - Create specific, actionable items
   - Break complex tasks into smaller, manageable steps
   - Use clear, descriptive task names
   - Always provide both forms:
     - content: "Fix authentication bug"
     - activeForm: "Fixing authentication bug"

When in doubt, use this tool. Being proactive with task management demonstrates attentiveness and ensures you complete all requirements successfully.`

// TodoWriteTool creates a tool for writing and managing a structured todo list.
// Mirrors src/tools/TodoWriteTool/TodoWriteTool.ts from the reference.
func TodoWriteTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "TodoWrite",
			Desc: todoWriteToolPrompt,
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"todos": {
					Type:     schema.Array,
					Desc:     "The complete todo list replacing the current one",
					Required: true,
					ElemInfo: &schema.ParameterInfo{
						Type: schema.Object,
						Desc: "A todo item with content, status, and activeForm",
						SubParams: map[string]*schema.ParameterInfo{
							"content": {
								Type:     schema.String,
								Desc:     "The todo item text in imperative form (e.g., 'Run tests')",
								Required: true,
							},
							"status": {
								Type:     schema.String,
								Desc:     "The status of the todo item",
								Required: true,
							},
							"activeForm": {
								Type:     schema.String,
								Desc:     "Present continuous form for spinner display (e.g., 'Running tests')",
								Required: true,
							},
						},
					},
				},
			}),
		},
		ExecuteCtx: executeTodoWrite,
	}
}

func executeTodoWrite(ctx context.Context, input string) (string, error) {
	var params struct {
		Todos []TodoItem `json:"todos"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("todo write: invalid params: %w", err)
	}

	if params.Todos == nil {
		return "", fmt.Errorf("todo write: todos is required")
	}

	validStatuses := map[string]bool{
		"pending":     true,
		"in_progress": true,
		"completed":   true,
	}

	for i, item := range params.Todos {
		if strings.TrimSpace(item.Content) == "" {
			return "", fmt.Errorf("todo write: item %d missing content", i)
		}
		if strings.TrimSpace(item.ActiveForm) == "" {
			return "", fmt.Errorf("todo write: item %d missing activeForm", i)
		}
		if !validStatuses[item.Status] {
			return "", fmt.Errorf("todo write: item %d invalid status %q, must be pending|in_progress|completed", i, item.Status)
		}
	}

	scope := todoScopeFromCtx(ctx)
	if authority := logicalWorkAuthorityFromCtx(ctx); authority != nil {
		if err := authority.ReplaceTodos(scope, params.Todos); err != nil {
			return "", err
		}
	} else {
		if err := durableLogicalWorkScopeError(ctx); err != nil {
			return "", err
		}
		return "", ErrMissingToolOwner
	}

	return "Todos have been modified successfully. Ensure that you continue to use the todo list to track your progress. Please proceed with the current tasks if applicable", nil
}
