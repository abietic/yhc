package engine

import (
	"context"
	"testing"

	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type taskLifecycleModel struct {
	callCount int
}

func (m *taskLifecycleModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *taskLifecycleModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.callCount++
	if m.callCount == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "task_create_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "TaskCreate",
					Arguments: `{"subject":"Add tests","description":"Write focused tests","activeForm":"Writing tests"}`,
				},
			}},
		}}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}), nil
}

func TestQueryEmitsTaskLifecycleEventsAfterTaskToolExecution(t *testing.T) {
	manager := tools.NewTaskManager()
	model := &taskLifecycleModel{}
	maxTurns := 3
	var lifecycleEvents []*TaskLifecycleEvent

	terminal := Query(context.Background(), QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "create a task"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			if toolName != "TaskCreate" {
				t.Fatalf("unexpected tool: %s", toolName)
			}
			task := manager.Create("Add tests", "", "Writing tests", nil)
			return "Task #" + task.ID + " created successfully: " + task.Subject, nil
		},
		TaskLifecycleDrainer: manager.DrainLifecycleEvents,
	}, func(evt QueryEvent) {
		if evt.Type == EventTaskLifecycle && evt.TaskLifecycle != nil {
			lifecycleEvents = append(lifecycleEvents, evt.TaskLifecycle)
		}
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected terminal completed, got %q", terminal.Reason)
	}
	if len(lifecycleEvents) != 1 {
		t.Fatalf("expected one task lifecycle event, got %d: %#v", len(lifecycleEvents), lifecycleEvents)
	}
	got := lifecycleEvents[0]
	if got.Phase != "created" || got.TaskID != "1" || got.Subject != "Add tests" || got.Status != "pending" {
		t.Fatalf("unexpected lifecycle event: %#v", got)
	}
}

func TestChildTaskLifecycleReachesSharedRootProjectionExactlyOnce(t *testing.T) {
	manager := tools.NewTaskManager()
	runtimeState := NewRuntimeStateStore()
	root := NewQueryEngine(QueryEngineConfig{
		SessionID:     "root-task-lineage",
		ThreadID:      "root-task-lineage",
		CWD:           t.TempDir(),
		TranscriptDir: p311bPrivateDir(t),
		TaskManager:   manager,
		RuntimeState:  runtimeState,
	})
	t.Cleanup(root.Close)

	registry := tools.NewRegistry()
	registry.Register(tools.TaskCreateTool())
	child := NewQueryEngine(QueryEngineConfig{
		SessionID:       "child-task-lineage",
		ThreadID:        "child-task-lineage",
		ParentSessionID: "root-task-lineage",
		ParentThreadID:  "root-task-lineage",
		AgentID:         "agent-task-lineage",
		CWD:             t.TempDir(),
		TranscriptDir:   p311bPrivateDir(t),
		ChatModel:       &taskLifecycleModel{},
		Model:           "test-model",
		MaxTurns:        3,
		ToolRegistry:    registry,
		Tools:           registry.List(),
		TaskManager:     manager,
		RuntimeState:    runtimeState,
	})
	t.Cleanup(child.Close)

	events, terminal := child.SubmitMessage(context.Background(), "create a task")
	if terminal.Err != nil {
		t.Fatalf("submit child task: %v", terminal.Err)
	}
	lifecycleEvents := 0
	for evt := range events {
		if evt.Type == EventTaskLifecycle {
			lifecycleEvents++
		}
	}
	if lifecycleEvents != 1 {
		t.Fatalf("child lifecycle events = %d, want 1", lifecycleEvents)
	}
	if pending := manager.DrainLifecycleEvents(); len(pending) != 0 {
		t.Fatalf("child left duplicate lifecycle events: %#v", pending)
	}

	snapshot := root.TaskExplorerSnapshot()
	found := false
	for _, task := range snapshot.WorkItems {
		if task.Title == "Add tests" && task.Status == "pending" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("root projection missed child task: %#v", snapshot.WorkItems)
	}
}
