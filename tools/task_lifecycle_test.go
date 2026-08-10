package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// TaskCreate Tests
// =============================================================================

func TestTaskCreate_HappyPath(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	result, err := TaskCreateTool().ExecuteCtx(ctx, `{
		"subject": "Implement feature X",
		"description": "Port the authentication module from reference"
	}`)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
		return
	}
	if !strings.Contains(result, "Task #1 created successfully: Implement feature X") {
		t.Fatalf("unexpected output: %q", result)
	}

	// Verify the task was actually stored.
	task, ok := manager.Get("1")
	if !ok {
		t.Fatal("task was not stored in manager")
	}
	if task.Subject != "Implement feature X" {
		t.Fatalf("unexpected subject: %q", task.Subject)
	}
	if task.Description != "Port the authentication module from reference" {
		t.Fatalf("unexpected description: %q", task.Description)
	}
	if task.Status != TaskStatusPending {
		t.Fatalf("expected pending status, got: %s", task.Status)
	}
}

func TestTaskCreate_WithActiveFormAndMetadata(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	result, err := TaskCreateTool().ExecuteCtx(ctx, `{
		"subject": "Run tests",
		"description": "Execute full test suite",
		"activeForm": "Running tests",
		"metadata": {"priority": "high", "module": "auth"}
	}`)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
		return
	}
	if !strings.Contains(result, "Task #1 created successfully") {
		t.Fatalf("unexpected output: %q", result)
	}

	task, ok := manager.Get("1")
	if !ok {
		t.Fatal("task not found")
	}
	if task.ActiveForm != "Running tests" {
		t.Fatalf("expected activeForm 'Running tests', got: %q", task.ActiveForm)
	}
	if task.Metadata["priority"] != "high" {
		t.Fatalf("expected metadata priority 'high', got: %v", task.Metadata)
	}
}

func TestTaskCreate_MissingSubject(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	_, err := TaskCreateTool().ExecuteCtx(ctx, `{"description": "some description"}`)
	if err == nil {
		t.Fatal("expected error for missing subject")
		return
	}
	if !strings.Contains(err.Error(), "subject is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTaskCreate_EmptySubject(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	_, err := TaskCreateTool().ExecuteCtx(ctx, `{"subject": "   ", "description": "desc"}`)
	if err == nil {
		t.Fatal("expected error for whitespace-only subject")
		return
	}
	if !strings.Contains(err.Error(), "subject is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTaskCreate_MissingDescription(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	_, err := TaskCreateTool().ExecuteCtx(ctx, `{"subject": "Fix bug"}`)
	if err == nil {
		t.Fatal("expected error for missing description")
		return
	}
	if !strings.Contains(err.Error(), "description is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTaskCreate_InvalidJSON(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	_, err := TaskCreateTool().ExecuteCtx(ctx, `not valid json`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
		return
	}
	if !strings.Contains(err.Error(), "invalid params") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTaskCreate_IncrementalIDs(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	for i := 1; i <= 3; i++ {
		result, err := TaskCreateTool().ExecuteCtx(ctx, `{
			"subject": "Task `+string(rune('A'+i-1))+`",
			"description": "desc"
		}`)
		if err != nil {
			t.Fatalf("task %d: unexpected error: %v", i, err)
			return
		}
		expected := "Task #" + string(rune('0'+i)) + " created successfully"
		if !strings.Contains(result, expected) {
			t.Fatalf("task %d: expected %q in output, got: %q", i, expected, result)
		}
	}
}

// =============================================================================
// TaskStop Tests
// =============================================================================

func TestTaskStop_HappyPath(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	// Create a task and set it to in_progress.
	task := manager.Create("Running task", "A task that is running", "", nil)
	status := TaskStatusInProgress
	_, _, _ = manager.Update(TaskUpdate{TaskID: task.ID, Status: &status})

	result, err := TaskStopTool().ExecuteCtx(ctx, `{"task_id": "`+task.ID+`"}`)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
		return
	}
	if !strings.Contains(result, "Successfully stopped task") {
		t.Fatalf("unexpected output: %q", result)
	}
	if !strings.Contains(result, task.ID) {
		t.Fatalf("expected task ID in output: %q", result)
	}

	// Verify the task was actually killed.
	stopped, _ := manager.Get(task.ID)
	if stopped.Status != TaskStatusKilled {
		t.Fatalf("expected killed status, got: %s", stopped.Status)
	}
}

func TestTaskStop_DeprecatedShellID(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	task := manager.Create("Shell task", "desc", "", nil)
	status := TaskStatusInProgress
	_, _, _ = manager.Update(TaskUpdate{TaskID: task.ID, Status: &status})

	result, err := TaskStopTool().ExecuteCtx(ctx, `{"shell_id": "`+task.ID+`"}`)
	if err != nil {
		t.Fatalf("expected no error with shell_id, got: %v", err)
		return
	}
	if !strings.Contains(result, "Successfully stopped task") {
		t.Fatalf("unexpected output: %q", result)
	}
}

func TestTaskStop_MissingTaskID(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	_, err := TaskStopTool().ExecuteCtx(ctx, `{}`)
	if err == nil {
		t.Fatal("expected error for missing task_id")
		return
	}
	if !strings.Contains(err.Error(), "task_id") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTaskStop_TaskNotFound(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	_, err := TaskStopTool().ExecuteCtx(ctx, `{"task_id": "nonexistent"}`)
	if err == nil {
		t.Fatal("expected error for nonexistent task")
		return
	}
	if !strings.Contains(err.Error(), "no task found with ID") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTaskStop_AlreadyCompleted(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	task := manager.Create("Done task", "desc", "", nil)
	status := TaskStatusCompleted
	_, _, _ = manager.Update(TaskUpdate{TaskID: task.ID, Status: &status})

	// Should handle gracefully — not error, just acknowledge.
	result, err := TaskStopTool().ExecuteCtx(ctx, `{"task_id": "`+task.ID+`"}`)
	if err != nil {
		t.Fatalf("expected graceful handling of already-completed task, got error: %v", err)
		return
	}
	if !strings.Contains(result, "Successfully stopped task") {
		t.Fatalf("unexpected output: %q", result)
	}
}

func TestTaskStop_PendingTask(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	task := manager.Create("Pending task", "desc", "", nil)

	// Pending tasks should also be stoppable.
	result, err := TaskStopTool().ExecuteCtx(ctx, `{"task_id": "`+task.ID+`"}`)
	if err != nil {
		t.Fatalf("expected no error stopping pending task, got: %v", err)
		return
	}
	if !strings.Contains(result, "Successfully stopped task") {
		t.Fatalf("unexpected output: %q", result)
	}
}

func TestTaskStop_InvalidJSON(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	_, err := TaskStopTool().ExecuteCtx(ctx, `invalid`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
		return
	}
	if !strings.Contains(err.Error(), "invalid params") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// =============================================================================
// TaskOutput Tests
// =============================================================================

func TestTaskOutput_HappyPath(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	task := manager.Create("Test task", "desc", "", nil)
	output := "build succeeded\nall 42 tests passed"
	_, _, _ = manager.Update(TaskUpdate{TaskID: task.ID, Output: &output})

	result, err := TaskOutputTool().ExecuteCtx(ctx, `{"task_id": "`+task.ID+`", "block": false}`)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
		return
	}
	if !strings.Contains(result, "<retrieval_status>") {
		t.Fatalf("expected structured output, got: %q", result)
	}
	if !strings.Contains(result, task.ID) {
		t.Fatalf("expected task ID in output: %q", result)
	}
	if !strings.Contains(result, "build succeeded") {
		t.Fatalf("expected task output content: %q", result)
	}
}

func TestTaskOutput_CompletedTask(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	task := manager.Create("Completed task", "desc", "", nil)
	output := "final result"
	status := TaskStatusCompleted
	_, _, _ = manager.Update(TaskUpdate{TaskID: task.ID, Output: &output, Status: &status})

	result, err := TaskOutputTool().ExecuteCtx(ctx, `{"task_id": "`+task.ID+`", "block": true}`)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
		return
	}
	if !strings.Contains(result, "<retrieval_status>success</retrieval_status>") {
		t.Fatalf("expected success status: %q", result)
	}
	if !strings.Contains(result, "<status>completed</status>") {
		t.Fatalf("expected completed status: %q", result)
	}
	if !strings.Contains(result, "final result") {
		t.Fatalf("expected output content: %q", result)
	}
}

func TestTaskOutput_NoOutputYet(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	task := manager.Create("Empty task", "desc", "", nil)

	result, err := TaskOutputTool().ExecuteCtx(ctx, `{"task_id": "`+task.ID+`", "block": false}`)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
		return
	}
	if !strings.Contains(result, "<retrieval_status>") {
		t.Fatalf("expected structured output: %q", result)
	}
	// Should not contain <output> tag when empty.
	if strings.Contains(result, "<output>") {
		t.Fatalf("expected no output tag for empty task: %q", result)
	}
}

func TestTaskOutput_MissingTaskID(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	_, err := TaskOutputTool().ExecuteCtx(ctx, `{}`)
	if err == nil {
		t.Fatal("expected error for missing task_id")
		return
	}
	if !strings.Contains(err.Error(), "task_id is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTaskOutput_TaskNotFound(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	_, err := TaskOutputTool().ExecuteCtx(ctx, `{"task_id": "nonexistent"}`)
	if err == nil {
		t.Fatal("expected error for nonexistent task")
		return
	}
	if !strings.Contains(err.Error(), "no task found with ID") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTaskOutput_NonBlockingRunningTask(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	task := manager.Create("Running task", "desc", "", nil)
	status := TaskStatusInProgress
	output := "partial output..."
	_, _, _ = manager.Update(TaskUpdate{TaskID: task.ID, Status: &status, Output: &output})

	result, err := TaskOutputTool().ExecuteCtx(ctx, `{"task_id": "`+task.ID+`", "block": false}`)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
		return
	}
	if !strings.Contains(result, "<status>in_progress</status>") {
		t.Fatalf("expected in_progress status: %q", result)
	}
	if !strings.Contains(result, "partial output...") {
		t.Fatalf("expected partial output: %q", result)
	}
}

func TestTaskOutput_BlockingWithQuickCompletion(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	task := manager.Create("Quick task", "desc", "", nil)
	status := TaskStatusInProgress
	_, _, _ = manager.Update(TaskUpdate{TaskID: task.ID, Status: &status})

	// Complete the task asynchronously after a short delay.
	go func() {
		time.Sleep(50 * time.Millisecond)
		completedStatus := TaskStatusCompleted
		finalOutput := "done!"
		_, _, _ = manager.Update(TaskUpdate{TaskID: task.ID, Status: &completedStatus, Output: &finalOutput})
	}()

	result, err := TaskOutputTool().ExecuteCtx(ctx, `{"task_id": "`+task.ID+`", "block": true, "timeout": 5000}`)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
		return
	}
	if !strings.Contains(result, "<retrieval_status>success</retrieval_status>") {
		t.Fatalf("expected success after blocking: %q", result)
	}
	if !strings.Contains(result, "done!") {
		t.Fatalf("expected final output: %q", result)
	}
}

func TestTaskOutput_BlockingTimeout(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	task := manager.Create("Slow task", "desc", "", nil)
	status := TaskStatusInProgress
	_, _, _ = manager.Update(TaskUpdate{TaskID: task.ID, Status: &status})

	// Use a very short timeout to force timeout behavior.
	result, err := TaskOutputTool().ExecuteCtx(ctx, `{"task_id": "`+task.ID+`", "block": true, "timeout": 200}`)
	if err != nil {
		t.Fatalf("expected no error on timeout, got: %v", err)
		return
	}
	if !strings.Contains(result, "<retrieval_status>timeout</retrieval_status>") {
		t.Fatalf("expected timeout retrieval status: %q", result)
	}
}

func TestTaskOutput_DefaultBlockIsTrue(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	task := manager.Create("Default block task", "desc", "", nil)
	status := TaskStatusCompleted
	output := "complete"
	_, _, _ = manager.Update(TaskUpdate{TaskID: task.ID, Status: &status, Output: &output})

	// No block parameter — should default to true and return immediately
	// because task is already completed.
	result, err := TaskOutputTool().ExecuteCtx(ctx, `{"task_id": "`+task.ID+`"}`)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
		return
	}
	if !strings.Contains(result, "<retrieval_status>success</retrieval_status>") {
		t.Fatalf("expected success: %q", result)
	}
}

func TestTaskOutput_InvalidJSON(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	_, err := TaskOutputTool().ExecuteCtx(ctx, `not json`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
		return
	}
	if !strings.Contains(err.Error(), "invalid params") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTaskOutput_OutputTruncation(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	task := manager.Create("Big output task", "desc", "", nil)
	// Generate output larger than TaskOutputMaxLength.
	bigOutput := strings.Repeat("x", TaskOutputMaxLength+1000)
	_, _, _ = manager.Update(TaskUpdate{TaskID: task.ID, Output: &bigOutput})

	result, err := TaskOutputTool().ExecuteCtx(ctx, `{"task_id": "`+task.ID+`", "block": false}`)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
		return
	}
	if !strings.Contains(result, "Truncated") {
		t.Fatalf("expected truncation marker in output: %q", result[:200])
	}
}

// =============================================================================
// TaskList Tests
// =============================================================================

func TestTaskList_EmptyList(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	result, err := TaskListTool().ExecuteCtx(ctx, `{}`)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
		return
	}
	if result != "No tasks found" {
		t.Fatalf("unexpected output: %q", result)
	}
}

func TestTaskList_MultipleTasks(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)
	manager.Create("First task", "desc1", "", nil)
	manager.Create("Second task", "desc2", "Working on it", nil)

	result, err := TaskListTool().ExecuteCtx(ctx, `{}`)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
		return
	}
	if !strings.Contains(result, "#1 [pending] First task") {
		t.Fatalf("expected first task in output: %q", result)
	}
	if !strings.Contains(result, "#2 [pending] Second task") {
		t.Fatalf("expected second task in output: %q", result)
	}
}

func TestTaskList_FilterByStatus(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)
	manager.Create("Pending task", "desc", "", nil)
	task2 := manager.Create("Running task", "desc", "Running", nil)
	status := TaskStatusInProgress
	_, _, _ = manager.Update(TaskUpdate{TaskID: task2.ID, Status: &status})

	result, err := TaskListTool().ExecuteCtx(ctx, `{"status": "in_progress"}`)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
		return
	}
	if !strings.Contains(result, "Running task") {
		t.Fatalf("expected running task in filtered output: %q", result)
	}
	if strings.Contains(result, "Pending task") {
		t.Fatalf("should not contain pending task in filtered output: %q", result)
	}
}

func TestTaskList_FilterNoMatch(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)
	manager.Create("Pending task", "desc", "", nil)

	result, err := TaskListTool().ExecuteCtx(ctx, `{"status": "completed"}`)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
		return
	}
	if !strings.Contains(result, "No tasks found with status: completed") {
		t.Fatalf("unexpected output: %q", result)
	}
}

func TestTaskList_ShowsOwner(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)
	task := manager.Create("Owned task", "desc", "", nil)
	owner := "agent-1"
	_, _, _ = manager.Update(TaskUpdate{TaskID: task.ID, Owner: &owner})

	result, err := TaskListTool().ExecuteCtx(ctx, `{}`)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
		return
	}
	if !strings.Contains(result, "(agent-1)") {
		t.Fatalf("expected owner in output: %q", result)
	}
}

func TestTaskList_ShowsBlockedBy(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)
	manager.Create("Blocker", "desc", "", nil)
	task2 := manager.Create("Blocked task", "desc", "", nil)
	_, _, _ = manager.Update(TaskUpdate{TaskID: task2.ID, AddBlockedBy: []string{"1"}})

	result, err := TaskListTool().ExecuteCtx(ctx, `{}`)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
		return
	}
	if !strings.Contains(result, "[blocked by") {
		t.Fatalf("expected blocked-by info in output: %q", result)
	}
}

func TestTaskList_ShowsActiveFormForInProgress(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)
	task := manager.Create("Running task", "desc", "Executing tests", nil)
	status := TaskStatusInProgress
	_, _, _ = manager.Update(TaskUpdate{TaskID: task.ID, Status: &status})

	result, err := TaskListTool().ExecuteCtx(ctx, `{}`)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
		return
	}
	if !strings.Contains(result, "Executing tests") {
		t.Fatalf("expected activeForm in output for in_progress task: %q", result)
	}
}

func TestTaskList_EmptyInput(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)
	manager.Create("A task", "desc", "", nil)

	// Even with empty string input, should work.
	result, err := TaskListTool().ExecuteCtx(ctx, ``)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
		return
	}
	if !strings.Contains(result, "A task") {
		t.Fatalf("unexpected output: %q", result)
	}
}

// =============================================================================
// Integration Tests — Full Lifecycle
// =============================================================================

func TestFullTaskLifecycle_CreateUpdateOutputStop(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	// 1. Create
	createResult, err := TaskCreateTool().ExecuteCtx(ctx, `{
		"subject": "Build project",
		"description": "Run go build ./...",
		"activeForm": "Building project"
	}`)
	if err != nil {
		t.Fatalf("create: %v", err)
		return
	}
	if !strings.Contains(createResult, "Task #1 created successfully") {
		t.Fatalf("create unexpected: %q", createResult)
	}

	// 2. Update to in_progress
	_, err = TaskUpdateTool().ExecuteCtx(ctx, `{"task_id": "1", "status": "in_progress"}`)
	if err != nil {
		t.Fatalf("update to in_progress: %v", err)
		return
	}

	// 3. Add output
	_, err = TaskUpdateTool().ExecuteCtx(ctx, `{"task_id": "1", "output": "compiling main.go...\ncompiling utils.go..."}`)
	if err != nil {
		t.Fatalf("add output: %v", err)
		return
	}

	// 4. Get output
	outputResult, err := TaskOutputTool().ExecuteCtx(ctx, `{"task_id": "1", "block": false}`)
	if err != nil {
		t.Fatalf("output: %v", err)
		return
	}
	if !strings.Contains(outputResult, "compiling main.go") {
		t.Fatalf("output expected compilation text: %q", outputResult)
	}

	// 5. List shows in_progress
	listResult, err := TaskListTool().ExecuteCtx(ctx, `{}`)
	if err != nil {
		t.Fatalf("list: %v", err)
		return
	}
	if !strings.Contains(listResult, "[in_progress]") {
		t.Fatalf("list expected in_progress status: %q", listResult)
	}
	if !strings.Contains(listResult, "Building project") {
		t.Fatalf("list expected activeForm: %q", listResult)
	}

	// 6. Stop
	stopResult, err := TaskStopTool().ExecuteCtx(ctx, `{"task_id": "1"}`)
	if err != nil {
		t.Fatalf("stop: %v", err)
		return
	}
	if !strings.Contains(stopResult, "Successfully stopped task") {
		t.Fatalf("stop unexpected: %q", stopResult)
	}

	// 7. Verify final state
	task, _ := manager.Get("1")
	if task.Status != TaskStatusKilled {
		t.Fatalf("expected killed status, got: %s", task.Status)
	}
}

func TestCombinedTaskTool_AllActions(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	// Create via combined tool.
	result, err := TaskTool().ExecuteCtx(ctx, `{"action":"create","subject":"Test","description":"A test task"}`)
	if err != nil {
		t.Fatalf("combined create: %v", err)
		return
	}
	if !strings.Contains(result, "Task #1 created successfully") {
		t.Fatalf("combined create unexpected: %q", result)
	}

	// List via combined tool.
	result, err = TaskTool().ExecuteCtx(ctx, `{"action":"list"}`)
	if err != nil {
		t.Fatalf("combined list: %v", err)
		return
	}
	if !strings.Contains(result, "#1 [pending] Test") {
		t.Fatalf("combined list unexpected: %q", result)
	}

	// Output via combined tool.
	result, err = TaskTool().ExecuteCtx(ctx, `{"action":"output","task_id":"1","block":false}`)
	if err != nil {
		t.Fatalf("combined output: %v", err)
		return
	}
	if !strings.Contains(result, "<task_id>1</task_id>") {
		t.Fatalf("combined output unexpected: %q", result)
	}

	// Stop via combined tool.
	result, err = TaskTool().ExecuteCtx(ctx, `{"action":"stop","task_id":"1"}`)
	if err != nil {
		t.Fatalf("combined stop: %v", err)
		return
	}
	if !strings.Contains(result, "Successfully stopped task") {
		t.Fatalf("combined stop unexpected: %q", result)
	}
}

// Verify tool schemas have correct names, descriptions, and execute functions.
func TestTaskToolSchemas(t *testing.T) {
	tools := []struct {
		name       string
		tool       ToolImpl
		descPrefix string
	}{
		{
			name:       "TaskCreate",
			tool:       TaskCreateTool(),
			descPrefix: "Use this tool to create a structured task list",
		},
		{
			name:       "TaskStop",
			tool:       TaskStopTool(),
			descPrefix: "Stops a running background task",
		},
		{
			name:       "TaskOutput",
			tool:       TaskOutputTool(),
			descPrefix: "Retrieve output from",
		},
		{
			name:       "TaskList",
			tool:       TaskListTool(),
			descPrefix: "Use this tool to list all tasks",
		},
	}

	for _, tc := range tools {
		t.Run(tc.name, func(t *testing.T) {
			if tc.tool.Info == nil {
				t.Fatal("tool info is nil")
				return
			}
			if tc.tool.Info.Name != tc.name {
				t.Fatalf("expected name %q, got %q", tc.name, tc.tool.Info.Name)
			}
			if tc.tool.Execute == nil {
				t.Fatal("execute function is nil")
				return
			}
			if !strings.Contains(tc.tool.Info.Desc, tc.descPrefix) {
				t.Fatalf("expected description to contain %q, got %q", tc.descPrefix, tc.tool.Info.Desc)
			}
		})
	}
}
