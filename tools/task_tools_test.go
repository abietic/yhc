package tools

import (
	"context"
	"strings"
	"testing"
)

func TestTaskToolsLifecycle(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)

	create, err := TaskCreateTool().ExecuteCtx(ctx, `{"subject":"Add tests","description":"Write focused regression tests"}`)
	if err != nil {
		t.Fatalf("create failed: %v", err)
		return
	}
	if !strings.Contains(create, "Task #1 created successfully") {
		t.Fatalf("unexpected create output: %q", create)
	}

	list, err := TaskListTool().ExecuteCtx(ctx, `{}`)
	if err != nil {
		t.Fatalf("list failed: %v", err)
		return
	}
	if !strings.Contains(list, "#1 [pending] Add tests") {
		t.Fatalf("unexpected list output: %q", list)
	}

	updated, err := TaskUpdateTool().ExecuteCtx(ctx, `{"task_id":"1","status":"in_progress","output":"running go test"}`)
	if err != nil {
		t.Fatalf("update failed: %v", err)
		return
	}
	if !strings.Contains(updated, "Task #1 updated") {
		t.Fatalf("unexpected update output: %q", updated)
	}

	output, err := TaskOutputTool().ExecuteCtx(ctx, `{"task_id":"1"}`)
	if err != nil {
		t.Fatalf("output failed: %v", err)
		return
	}
	if !strings.Contains(output, "running go test") {
		t.Fatalf("unexpected output text: %q", output)
	}

	stopped, err := TaskStopTool().ExecuteCtx(ctx, `{"task_id":"1"}`)
	if err != nil {
		t.Fatalf("stop failed: %v", err)
		return
	}
	if !strings.Contains(stopped, "Successfully stopped task: 1") {
		t.Fatalf("unexpected stop output: %q", stopped)
	}
}

func TestCombinedTaskToolDispatches(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)
	result, err := TaskTool().ExecuteCtx(ctx, `{"action":"create","subject":"Investigate bug","description":"Reproduce issue"}`)
	if err != nil {
		t.Fatalf("combined task tool failed: %v", err)
		return
	}
	if !strings.Contains(result, "Task #1 created successfully") {
		t.Fatalf("unexpected result: %q", result)
	}
}

func TestTaskManagerDrainsLifecycleEvents(t *testing.T) {
	manager := NewTaskManager()
	task := manager.Create("Add tests", "Write focused regression tests", "Writing tests", nil)

	status := TaskStatusInProgress
	if _, _, err := manager.Update(TaskUpdate{TaskID: task.ID, Status: &status}); err != nil {
		t.Fatalf("update failed: %v", err)
		return
	}
	if _, err := manager.Stop(task.ID); err != nil {
		t.Fatalf("stop failed: %v", err)
		return
	}

	events := manager.DrainLifecycleEvents()
	if len(events) != 3 {
		t.Fatalf("expected create/update/stop events, got %d: %#v", len(events), events)
	}
	if events[0].Phase != TaskLifecycleCreated || events[0].Task.Status != TaskStatusPending {
		t.Fatalf("unexpected create event: %#v", events[0])
	}
	if events[1].Phase != TaskLifecycleUpdated || events[1].Task.Status != TaskStatusInProgress {
		t.Fatalf("unexpected update event: %#v", events[1])
	}
	if events[2].Phase != TaskLifecycleStopped || events[2].Task.Status != TaskStatusKilled {
		t.Fatalf("unexpected stop event: %#v", events[2])
	}
	if drainedAgain := manager.DrainLifecycleEvents(); len(drainedAgain) != 0 {
		t.Fatalf("expected events to be drained, got %#v", drainedAgain)
	}
}
