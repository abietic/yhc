package tools

import (
	"context"
	"reflect"
	"testing"
)

func TestP31PromotionTaskCompatibilityBaseline(t *testing.T) {
	for _, shadowEnabled := range []bool{false, true} {
		name := "shadow_disabled"
		if shadowEnabled {
			name = "shadow_enabled"
		}
		t.Run(name, func(t *testing.T) {
			runP31PromotionTaskCompatibilityBaseline(t, shadowEnabled)
		})
	}
}

func runP31PromotionTaskCompatibilityBaseline(t *testing.T, shadowEnabled bool) {
	t.Helper()
	manager := NewTaskManager()
	ctx := WithTaskManager(context.Background(), manager)
	ctx = p31PromotionShadowContext(ctx, shadowEnabled)

	createResult, err := TaskCreateTool().ExecuteCtx(ctx, `{
		"subject":"Alpha",
		"description":"First task",
		"activeForm":"Working Alpha",
		"metadata":{"drop":"old"}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Task #1 created successfully: Alpha"; createResult != want {
		t.Fatalf("create result = %q, want %q", createResult, want)
	}
	if _, err := TaskCreateTool().ExecuteCtx(
		ctx,
		`{"subject":"Beta","description":"Second task"}`,
	); err != nil {
		t.Fatal(err)
	}

	getResult, err := TaskGetTool().ExecuteCtx(ctx, `{"taskId":"1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Task #1: Alpha\nStatus: pending\nDescription: First task"; getResult != want {
		t.Fatalf("initial get result = %q, want %q", getResult, want)
	}

	updateResult, err := TaskUpdateTool().ExecuteCtx(ctx, `{
		"taskId":"1",
		"status":"running",
		"owner":"agent-1",
		"add_blocks":["2","2"],
		"add_blocked_by":["missing","missing"],
		"metadata":{"drop":null,"keep":"value"},
		"output":"first"
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Task #1 updated: status, owner, blocks, blocked_by, metadata, output"; updateResult != want {
		t.Fatalf("first update result = %q, want %q", updateResult, want)
	}
	task, ok := manager.Get("1")
	if !ok {
		t.Fatal("updated task was removed")
	}
	if task.Status != TaskStatusInProgress {
		t.Fatalf("running alias status = %q, want %q", task.Status, TaskStatusInProgress)
	}
	if !reflect.DeepEqual(task.Blocks, []string{"2"}) ||
		!reflect.DeepEqual(task.BlockedBy, []string{"missing"}) {
		t.Fatalf("dependency append/dedup = blocks %#v blocked_by %#v", task.Blocks, task.BlockedBy)
	}
	if !reflect.DeepEqual(task.Metadata, map[string]any{"keep": "value"}) {
		t.Fatalf("metadata merge/delete = %#v", task.Metadata)
	}

	updateResult, err = TaskUpdateTool().ExecuteCtx(ctx, `{
		"task_id":"1",
		"status":"deleted",
		"add_blocks":["2"],
		"add_blocked_by":["missing"],
		"output":"second"
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Task #1 updated: status, blocks, blocked_by, output"; updateResult != want {
		t.Fatalf("second update result = %q, want %q", updateResult, want)
	}
	task, ok = manager.Get("1")
	if !ok {
		t.Fatal("legacy deleted status unexpectedly removed the record")
	}
	if task.Status != TaskStatus("deleted") {
		t.Fatalf("legacy status pass-through = %q, want deleted", task.Status)
	}
	if task.Output != "first\nsecond" {
		t.Fatalf("output append = %q, want %q", task.Output, "first\nsecond")
	}

	listResult, err := TaskListTool().ExecuteCtx(ctx, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "#1 [deleted] Alpha (agent-1) [blocked by #missing]\n#2 [pending] Beta"; listResult != want {
		t.Fatalf("list result = %q, want %q", listResult, want)
	}
	getResult, err = TaskGetTool().ExecuteCtx(ctx, `{"task_id":"1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Task #1: Alpha\nStatus: deleted\nDescription: First task\nBlocked by: #missing\nBlocks: #2\nOwner: agent-1"; getResult != want {
		t.Fatalf("final get result = %q, want %q", getResult, want)
	}

	unchangedResult, err := TaskUpdateTool().ExecuteCtx(ctx, `{"task_id":"1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Task #1 unchanged"; unchangedResult != want {
		t.Fatalf("unchanged result = %q, want %q", unchangedResult, want)
	}
	notFound, err := TaskGetTool().ExecuteCtx(ctx, `{"task_id":"404"}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Task not found"; notFound != want {
		t.Fatalf("missing get result = %q, want %q", notFound, want)
	}
	if _, err := TaskCreateTool().ExecuteCtx(ctx, `{"description":"missing subject"}`); err == nil ||
		err.Error() != "task create: subject is required" {
		t.Fatalf("missing-subject error = %v", err)
	}
	if _, err := TaskGetTool().ExecuteCtx(ctx, `{}`); err == nil ||
		err.Error() != "task get: task_id is required" {
		t.Fatalf("missing TaskGet ID error = %v", err)
	}
	if _, err := TaskUpdateTool().ExecuteCtx(ctx, `{}`); err == nil ||
		err.Error() != "task update: task_id is required" {
		t.Fatalf("missing TaskUpdate ID error = %v", err)
	}
}

func TestP31PromotionCombinedTaskStopOutputAliasesBaseline(t *testing.T) {
	for _, shadowEnabled := range []bool{false, true} {
		name := "shadow_disabled"
		if shadowEnabled {
			name = "shadow_enabled"
		}
		t.Run(name, func(t *testing.T) {
			runP31PromotionCombinedTaskStopOutputAliasesBaseline(t, shadowEnabled)
		})
	}
}

func runP31PromotionCombinedTaskStopOutputAliasesBaseline(
	t *testing.T,
	shadowEnabled bool,
) {
	t.Helper()
	manager := NewTaskManager()
	runner := NewAgentRunner(1)
	ctx := WithAgentRunner(
		WithTaskManager(context.Background(), manager),
		runner,
	)
	ctx = p31PromotionShadowContext(ctx, shadowEnabled)

	createResult, err := TaskTool().ExecuteCtx(
		ctx,
		`{"action":"create","subject":"Alias task","description":"Check aliases"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Task #1 created successfully: Alias task"; createResult != want {
		t.Fatalf("combined create result = %q, want %q", createResult, want)
	}

	monitorResult, err := TaskTool().ExecuteCtx(
		ctx,
		`{"action":"monitor","taskId":"1","block":false}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := "<retrieval_status>success</retrieval_status>\n\n" +
		"<task_id>1</task_id>\n\n" +
		"<task_type>local_task</task_type>\n\n" +
		"<status>pending</status>"; monitorResult != want {
		t.Fatalf("monitor result = %q, want %q", monitorResult, want)
	}

	cancelResult, err := TaskTool().ExecuteCtx(
		ctx,
		`{"action":"cancel","taskId":"1"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Successfully stopped task: 1 (Alias task)\n" +
		"task_id: 1\n" +
		"task_type: local_task"; cancelResult != want {
		t.Fatalf("cancel result = %q, want %q", cancelResult, want)
	}

	outputResult, err := TaskOutputTool().ExecuteCtx(
		ctx,
		`{"taskId":"1","block":false}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := "<retrieval_status>success</retrieval_status>\n\n" +
		"<task_id>1</task_id>\n\n" +
		"<task_type>local_task</task_type>\n\n" +
		"<status>killed</status>"; outputResult != want {
		t.Fatalf("taskId output result = %q, want %q", outputResult, want)
	}

	second := manager.Create("Shell alias", "Check shell_id", "", nil)
	stopResult, err := TaskStopTool().ExecuteCtx(ctx, `{"shell_id":"`+second.ID+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Successfully stopped task: 2 (Shell alias)\n" +
		"task_id: 2\n" +
		"task_type: local_task"; stopResult != want {
		t.Fatalf("shell_id stop result = %q, want %q", stopResult, want)
	}

	for name, tc := range map[string]struct {
		input string
		want  string
	}{
		"combined action required": {
			input: `{}`,
			want:  "task: action is required",
		},
		"combined unsupported": {
			input: `{"action":"unknown"}`,
			want:  `task: unsupported action "unknown"`,
		},
		"stop task ID required": {
			input: `{"action":"stop"}`,
			want:  "task stop: missing required parameter: task_id",
		},
		"output task ID required": {
			input: `{"action":"output","block":false}`,
			want:  "task output: task_id is required",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := TaskTool().ExecuteCtx(ctx, tc.input)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestP31PromotionTodoWriteCompatibilityBaseline(t *testing.T) {
	for _, shadowEnabled := range []bool{false, true} {
		name := "shadow_disabled"
		if shadowEnabled {
			name = "shadow_enabled"
		}
		t.Run(name, func(t *testing.T) {
			runP31PromotionTodoWriteCompatibilityBaseline(t, shadowEnabled)
		})
	}
}

func runP31PromotionTodoWriteCompatibilityBaseline(
	t *testing.T,
	shadowEnabled bool,
) {
	t.Helper()
	const (
		sessionID = "p31-promotion-root"
		agentID   = "p31-promotion-child"
	)
	tool := TodoWriteTool()
	authority := NewEphemeralTodoAuthority()
	rootCtx := WithNonSessionLogicalWorkScope(context.Background(), sessionID)
	rootCtx = WithLogicalWorkAuthority(rootCtx, authority)
	rootCtx = p31PromotionShadowContext(rootCtx, shadowEnabled)
	success := "Todos have been modified successfully. Ensure that you continue to use the todo list to track your progress. Please proceed with the current tasks if applicable"

	result, err := tool.ExecuteCtx(rootCtx, `{
		"session_id":"forged",
		"todos":[
			{"content":"First","status":"pending","activeForm":"Doing first"},
			{"content":"Second","status":"in_progress","activeForm":"Doing second"}
		]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if result != success {
		t.Fatalf("TodoWrite result = %q, want %q", result, success)
	}
	rootItems := []TodoItem{
		{Content: "First", Status: "pending", ActiveForm: "Doing first"},
		{Content: "Second", Status: "in_progress", ActiveForm: "Doing second"},
	}
	got, err := authority.Todos(TodoScope{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, rootItems) {
		t.Fatalf("root replacement = %#v, want %#v", got, rootItems)
	}
	got, err = authority.Todos(TodoScope{SessionID: "forged"})
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("model-supplied session selected state: %#v", got)
	}

	childCtx := WithAgentID(rootCtx, agentID)
	childItems := []TodoItem{{
		Content:    "Child",
		Status:     "pending",
		ActiveForm: "Doing child",
	}}
	if _, err := tool.ExecuteCtx(
		childCtx,
		`{"todos":[{"content":"Child","status":"pending","activeForm":"Doing child"}]}`,
	); err != nil {
		t.Fatal(err)
	}
	got, err = authority.Todos(TodoScope{
		SessionID: sessionID,
		AgentID:   agentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, childItems) {
		t.Fatalf("child replacement = %#v, want %#v", got, childItems)
	}
	got, err = authority.Todos(TodoScope{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, rootItems) {
		t.Fatalf("child write changed root list: %#v", got)
	}

	if _, err := tool.ExecuteCtx(
		rootCtx,
		`{"todos":[{"content":"First","status":"completed","activeForm":"Doing first"}]}`,
	); err != nil {
		t.Fatal(err)
	}
	got, err = authority.Todos(TodoScope{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("all-complete replacement was not cleared: %#v", got)
	}
	if result, err := tool.ExecuteCtx(rootCtx, `{"todos":[]}`); err != nil || result != success {
		t.Fatalf("empty replacement result = %q err=%v", result, err)
	}

	for name, tc := range map[string]struct {
		input string
		want  string
	}{
		"todos required": {
			input: `{}`,
			want:  "todo write: todos is required",
		},
		"content required": {
			input: `{"todos":[{"status":"pending","activeForm":"Doing"}]}`,
			want:  "todo write: item 0 missing content",
		},
		"active required": {
			input: `{"todos":[{"content":"Task","status":"pending"}]}`,
			want:  "todo write: item 0 missing activeForm",
		},
		"status enum": {
			input: `{"todos":[{"content":"Task","status":"failed","activeForm":"Doing"}]}`,
			want:  `todo write: item 0 invalid status "failed", must be pending|in_progress|completed`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tool.ExecuteCtx(rootCtx, tc.input)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func p31PromotionShadowContext(
	ctx context.Context,
	enabled bool,
) context.Context {
	if !enabled {
		return ctx
	}
	return WithWorkBoardShadowObserver(ctx, &workBoardShadowObserverFixture{})
}
