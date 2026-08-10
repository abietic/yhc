package tools

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestAdapterFreeDurableLogicalWorkScopeFailsClosed(t *testing.T) {
	ctx := WithSessionID(context.Background(), "durable-session")
	taskTool := TaskListTool()
	if _, err := taskTool.ExecuteCtx(ctx, "{}"); err == nil ||
		!strings.Contains(err.Error(), "has no LogicalWorkAdapter") {
		t.Fatalf("Task durable-scope error = %v", err)
	}

	todoTool := TodoWriteTool()
	if _, err := todoTool.ExecuteCtx(
		ctx,
		`{"todos":[{"content":"unsafe","status":"pending","activeForm":"writing unsafe"}]}`,
	); err == nil || !strings.Contains(
		err.Error(),
		"has no LogicalWorkAdapter",
	) {
		t.Fatalf("Todo durable-scope error = %v", err)
	}
}

func TestExplicitNonSessionLogicalWorkScopeRetainsCompatibility(t *testing.T) {
	const scope = "opaque-process-scope"
	authority := NewEphemeralTodoAuthority()
	ctx := WithLogicalWorkAuthority(
		WithNonSessionLogicalWorkScope(context.Background(), scope),
		authority,
	)
	if _, err := TodoWriteTool().ExecuteCtx(
		ctx,
		`{"todos":[{"content":"local","status":"pending","activeForm":"writing local"}]}`,
	); err != nil {
		t.Fatalf("write process-local Todo: %v", err)
	}
	items, err := authority.Todos(TodoScope{SessionID: scope})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Content != "local" {
		t.Fatalf("process-local Todo state = %+v", items)
	}
}

func TestDirectLogicalWorkToolsFailClosedWithoutOwner(t *testing.T) {
	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "Task",
			run: func() error {
				_, err := TaskTool().Execute(
					`{"action":"create","subject":"unsafe","description":"unsafe"}`,
				)
				return err
			},
		},
		{
			name: "TaskCreate",
			run: func() error {
				_, err := TaskCreateTool().Execute(
					`{"subject":"unsafe","description":"unsafe"}`,
				)
				return err
			},
		},
		{
			name: "TaskList",
			run: func() error {
				_, err := TaskListTool().Execute(`{}`)
				return err
			},
		},
		{
			name: "TaskGet",
			run: func() error {
				_, err := TaskGetTool().Execute(`{"task_id":"1"}`)
				return err
			},
		},
		{
			name: "TaskUpdate",
			run: func() error {
				_, err := TaskUpdateTool().Execute(
					`{"task_id":"1","status":"in_progress"}`,
				)
				return err
			},
		},
		{
			name: "TaskStop",
			run: func() error {
				_, err := TaskStopTool().Execute(`{"task_id":"1"}`)
				return err
			},
		},
		{
			name: "TaskOutput",
			run: func() error {
				_, err := TaskOutputTool().Execute(
					`{"task_id":"1","block":false}`,
				)
				return err
			},
		},
		{
			name: "TodoWrite",
			run: func() error {
				_, err := TodoWriteTool().ExecuteCtx(
					context.Background(),
					`{"todos":[{"content":"unsafe","status":"pending","activeForm":"writing unsafe"}]}`,
				)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); err != ErrMissingToolOwner {
				t.Fatalf("error = %v, want stable %v", err, ErrMissingToolOwner)
			}
		})
	}
}

func TestEphemeralTodoAuthoritiesRemainIsolatedUnderConcurrency(t *testing.T) {
	left := NewEphemeralTodoAuthority()
	right := NewEphemeralTodoAuthority()
	const writers = 64
	var wg sync.WaitGroup
	for index := range writers {
		wg.Add(2)
		go func() {
			defer wg.Done()
			scope := TodoScope{SessionID: fmt.Sprintf("left-%d", index)}
			items := []TodoItem{{
				Content:    scope.SessionID,
				Status:     "pending",
				ActiveForm: "writing left",
			}}
			if err := left.ReplaceTodos(scope, items); err != nil {
				t.Errorf("left replacement: %v", err)
				return
			}
			got, err := left.Todos(scope)
			if err != nil || !reflect.DeepEqual(got, items) {
				t.Errorf("left scope %q = %#v, %v", scope.SessionID, got, err)
			}
			if leaked, err := right.Todos(scope); err != nil || leaked != nil {
				t.Errorf("left scope leaked right: %#v, %v", leaked, err)
			}
		}()
		go func() {
			defer wg.Done()
			scope := TodoScope{SessionID: fmt.Sprintf("right-%d", index)}
			items := []TodoItem{{
				Content:    scope.SessionID,
				Status:     "in_progress",
				ActiveForm: "writing right",
			}}
			if err := right.ReplaceTodos(scope, items); err != nil {
				t.Errorf("right replacement: %v", err)
				return
			}
			got, err := right.Todos(scope)
			if err != nil || !reflect.DeepEqual(got, items) {
				t.Errorf("right scope %q = %#v, %v", scope.SessionID, got, err)
			}
			if leaked, err := left.Todos(scope); err != nil || leaked != nil {
				t.Errorf("right scope leaked left: %#v, %v", leaked, err)
			}
		}()
	}
	wg.Wait()
}
