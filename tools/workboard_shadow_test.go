package tools

import (
	"context"
	"reflect"
	"testing"
)

type workBoardShadowObserverFixture struct {
	taskSnapshots [][]*TaskRecord
	todoScopes    []WorkBoardTodoScope
	todoSnapshots [][]TodoItem
	panicOnCall   bool
}

func (f *workBoardShadowObserverFixture) ObserveTasks(tasks []*TaskRecord) {
	if f.panicOnCall {
		panic("observer failure")
	}
	f.taskSnapshots = append(f.taskSnapshots, tasks)
}

func (f *workBoardShadowObserverFixture) ObserveTodos(
	scope WorkBoardTodoScope,
	items []TodoItem,
) {
	if f.panicOnCall {
		panic("observer failure")
	}
	f.todoScopes = append(f.todoScopes, scope)
	f.todoSnapshots = append(f.todoSnapshots, items)
}

func TestWorkBoardShadowObserverRunsOnlyAfterAcceptedTaskMutation(t *testing.T) {
	manager := NewTaskManager()
	observer := &workBoardShadowObserverFixture{}
	ctx := WithWorkBoardShadowObserver(
		WithTaskManager(context.Background(), manager),
		observer,
	)

	if _, err := TaskCreateTool().ExecuteCtx(
		ctx,
		`{"description":"missing subject"}`,
	); err == nil {
		t.Fatal("invalid TaskCreate unexpectedly succeeded")
	}
	if len(observer.taskSnapshots) != 0 {
		t.Fatalf("invalid TaskCreate observed %d snapshots", len(observer.taskSnapshots))
	}

	result, err := TaskCreateTool().ExecuteCtx(
		ctx,
		`{"subject":"Shadowed","description":"Observed"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != "Task #1 created successfully: Shadowed" {
		t.Fatalf("TaskCreate result = %q", result)
	}
	if len(observer.taskSnapshots) != 1 ||
		len(observer.taskSnapshots[0]) != 1 ||
		observer.taskSnapshots[0][0].Subject != "Shadowed" {
		t.Fatalf("TaskCreate snapshots = %#v", observer.taskSnapshots)
	}

	if _, err := TaskUpdateTool().ExecuteCtx(ctx, `{}`); err == nil {
		t.Fatal("invalid TaskUpdate unexpectedly succeeded")
	}
	if len(observer.taskSnapshots) != 1 {
		t.Fatalf("invalid TaskUpdate observed %d snapshots", len(observer.taskSnapshots))
	}
	if _, err := TaskUpdateTool().ExecuteCtx(
		ctx,
		`{"task_id":"1","status":"running"}`,
	); err != nil {
		t.Fatal(err)
	}
	if len(observer.taskSnapshots) != 2 ||
		observer.taskSnapshots[1][0].Status != TaskStatusInProgress {
		t.Fatalf("TaskUpdate snapshots = %#v", observer.taskSnapshots)
	}
	if _, err := TaskUpdateTool().ExecuteCtx(
		ctx,
		`{"task_id":"1"}`,
	); err != nil {
		t.Fatal(err)
	}
	if len(observer.taskSnapshots) != 2 {
		t.Fatalf("unchanged TaskUpdate observed %d snapshots", len(observer.taskSnapshots))
	}

	if _, err := TaskStopTool().ExecuteCtx(ctx, `{"task_id":"1"}`); err != nil {
		t.Fatal(err)
	}
	if len(observer.taskSnapshots) != 3 ||
		observer.taskSnapshots[2][0].Status != TaskStatusKilled {
		t.Fatalf("TaskStop snapshots = %#v", observer.taskSnapshots)
	}
	if _, err := TaskStopTool().ExecuteCtx(ctx, `{"task_id":"1"}`); err != nil {
		t.Fatal(err)
	}
	if len(observer.taskSnapshots) != 3 {
		t.Fatalf("terminal TaskStop observed %d snapshots", len(observer.taskSnapshots))
	}
}

func TestEphemeralTodoOwnerDoesNotPublishWorkBoardShadow(t *testing.T) {
	const (
		processScope = "workboard-shadow-process"
		agentID      = "workboard-shadow-agent"
	)
	observer := &workBoardShadowObserverFixture{}
	authority := NewEphemeralTodoAuthority()
	ctx := WithWorkBoardShadowObserver(
		WithLogicalWorkAuthority(
			WithAgentID(
				WithNonSessionLogicalWorkScope(
					context.Background(),
					processScope,
				),
				agentID,
			),
			authority,
		),
		observer,
	)
	if _, err := TodoWriteTool().ExecuteCtx(ctx, `{}`); err == nil {
		t.Fatal("invalid TodoWrite unexpectedly succeeded")
	}
	if len(observer.todoScopes) != 0 {
		t.Fatalf("invalid TodoWrite observed %d snapshots", len(observer.todoScopes))
	}

	result, err := TodoWriteTool().ExecuteCtx(ctx, `{
		"session_id":"forged",
		"todos":[{"content":"Observe","status":"pending","activeForm":"Observing"}]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if result != "Todos have been modified successfully. Ensure that you continue to use the todo list to track your progress. Please proceed with the current tasks if applicable" {
		t.Fatalf("TodoWrite result = %q", result)
	}
	if len(observer.todoScopes) != 0 ||
		len(observer.todoSnapshots) != 0 {
		t.Fatalf(
			"ephemeral owner published WorkBoard shadow: scopes=%#v snapshots=%#v",
			observer.todoScopes,
			observer.todoSnapshots,
		)
	}
	want := []TodoItem{{
		Content:    "Observe",
		Status:     "pending",
		ActiveForm: "Observing",
	}}
	got, err := authority.Todos(TodoScope{
		SessionID: processScope,
		AgentID:   agentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Todo state = %#v, want %#v", got, want)
	}
}

func TestWorkBoardShadowObserverPanicCannotChangeLegacyResult(t *testing.T) {
	manager := NewTaskManager()
	ctx := WithWorkBoardShadowObserver(
		WithTaskManager(context.Background(), manager),
		&workBoardShadowObserverFixture{panicOnCall: true},
	)
	result, err := TaskCreateTool().ExecuteCtx(
		ctx,
		`{"subject":"Legacy","description":"Must succeed"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != "Task #1 created successfully: Legacy" {
		t.Fatalf("TaskCreate result = %q", result)
	}
	if task, ok := manager.Get("1"); !ok || task.Subject != "Legacy" {
		t.Fatalf("legacy TaskManager state = %#v, found=%v", task, ok)
	}
}
