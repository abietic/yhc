package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const taskCreatePrompt = `Use this tool to create a structured task list for your current coding session. This helps you track progress, organize complex tasks, and demonstrate thoroughness to the user.
It also helps the user understand the progress of the task and overall progress of their requests.

## When to Use This Tool

Use this tool proactively in these scenarios:

- Complex multi-step tasks - When a task requires 3 or more distinct steps or actions
- Non-trivial and complex tasks - Tasks that require careful planning or multiple operations
- Plan mode - When using plan mode, create a task list to track the work
- User explicitly requests todo list - When the user directly asks you to use the todo list
- User provides multiple tasks - When users provide a list of things to be done (numbered or comma-separated)
- After receiving new instructions - Immediately capture user requirements as tasks
- When you start working on a task - Mark it as in_progress BEFORE beginning work
- After completing a task - Mark it as completed and add any new follow-up tasks discovered during implementation

## When NOT to Use This Tool

Skip using this tool when:
- There is only a single, straightforward task
- The task is trivial and tracking it provides no organizational benefit
- The task can be completed in less than 3 trivial steps
- The task is purely conversational or informational

NOTE that you should not use this tool if there is only one trivial task to do. In this case you are better off just doing the task directly.

## Task Fields

- **subject**: A brief, actionable title in imperative form (e.g., "Fix authentication bug in login flow")
- **description**: What needs to be done
- **activeForm** (optional): Present continuous form shown in the spinner when the task is in_progress (e.g., "Fixing authentication bug"). If omitted, the spinner shows the subject instead.

All tasks are created with status 'pending'.

## Tips

- Create tasks with clear, specific subjects that describe the outcome
- After creating tasks, use TaskUpdate to set up dependencies (blocks/blockedBy) if needed
- Check TaskList first to avoid creating duplicate tasks
`

// TaskCreateTool creates a new task in the task list.
// Mirrors src/tools/TaskCreateTool/TaskCreateTool.ts from the reference.
func TaskCreateTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "TaskCreate",
			Desc: taskCreatePrompt,
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"subject": {
					Type:     schema.String,
					Desc:     "A brief title for the task",
					Required: true,
				},
				"description": {
					Type:     schema.String,
					Desc:     "What needs to be done",
					Required: true,
				},
				"activeForm": {
					Type: schema.String,
					Desc: "Present continuous form shown in spinner when in_progress (e.g., \"Running tests\")",
				},
				"metadata": {
					Type: schema.Object,
					Desc: "Arbitrary metadata to attach to the task",
				},
			}),
		},
		Execute: executeTaskCreate,
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			manager, err := taskManagerForToolContext(ctx)
			if err != nil {
				return "", err
			}
			result, err := executeTaskCreateWithManager(input, manager)
			if err != nil {
				return "", err
			}
			observeWorkBoardTasks(ctx, manager)
			return result, nil
		},
	}
}

func executeTaskCreate(input string) (string, error) {
	return executeTaskCreateWithManager(input, nil)
}

func executeTaskCreateWithManager(
	input string,
	manager *TaskManager,
) (string, error) {
	if manager == nil {
		return "", ErrMissingToolOwner
	}
	var params struct {
		Subject     string         `json:"subject"`
		Description string         `json:"description"`
		ActiveForm  string         `json:"activeForm"`
		Metadata    map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("task create: invalid params: %w", err)
	}

	// Validate required parameters — mirrors reference validation.
	if strings.TrimSpace(params.Subject) == "" {
		return "", fmt.Errorf("task create: subject is required")
	}
	if strings.TrimSpace(params.Description) == "" {
		return "", fmt.Errorf("task create: description is required")
	}

	// Create the task with initial "pending" status via TaskManager.
	task, err := manager.CreateWithError(
		strings.TrimSpace(params.Subject),
		strings.TrimSpace(params.Description),
		strings.TrimSpace(params.ActiveForm),
		params.Metadata,
	)
	if err != nil {
		return "", err
	}

	// Format output matching reference's mapToolResultToToolResultBlockParam:
	// "Task #${task.id} created successfully: ${task.subject}"
	return fmt.Sprintf("Task #%s created successfully: %s", task.ID, task.Subject), nil
}
