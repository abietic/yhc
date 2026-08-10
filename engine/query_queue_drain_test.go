package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// queueDrainModel returns a tool call on turn one, then completes.
type queueDrainModel struct {
	callCount int
}

func (m *queueDrainModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *queueDrainModel) Stream(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	m.callCount++
	if m.callCount == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:       "tc1",
				Function: schema.FunctionCall{Name: "noop", Arguments: "{}"},
			}},
		}}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role: schema.Assistant, Content: "done",
	}}), nil
}

func queueTestDeps() *QueryDeps {
	return &QueryDeps{
		UUID: func() string { return "queue-test-uuid" },
		CallModel: func(
			ctx context.Context,
			chatModel model.BaseChatModel,
			messages []*schema.Message,
			systemPrompt *schema.Message,
			tools []*schema.ToolInfo,
			opts execution.CallModelOptions,
		) (*execution.CallModelResult, error) {
			reader, err := chatModel.Stream(ctx, messages)
			return &execution.CallModelResult{StreamReader: reader}, err
		},
	}
}

func queueTestParams(
	sessionID string,
	chatModel model.BaseChatModel,
	coordinator *RuntimeInputCoordinator,
	maxTurns *int,
) QueryParams {
	return QueryParams{
		Messages:         []*schema.Message{{Role: schema.User, Content: "start"}},
		SystemPrompt:     &schema.Message{Role: schema.System, Content: "sys"},
		ChatModel:        chatModel,
		SessionID:        sessionID,
		InputCoordinator: coordinator,
		MaxTurns:         maxTurns,
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			Tools: []*schema.ToolInfo{{Name: "noop"}},
		}},
		ToolExecutor: func(context.Context, string, string) (string, error) {
			return "ok", nil
		},
		Deps: queueTestDeps(),
	}
}

func TestQueryProjectsRuntimeInputAtSafeBoundary(t *testing.T) {
	coordinator := newTestRuntimeInputCoordinator(t, "query-runtime-input", "")
	_, err := coordinator.Enqueue(RuntimeItem{
		ID:       "queued-user",
		Kind:     RuntimeItemUserPrompt,
		Priority: RuntimePriorityNext,
		Scope:    RuntimeInputScope{SessionID: "query-runtime-input"},
		Origin:   "user",
		UserPrompt: &RuntimeUserPrompt{
			Prompt: "queued user message",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mdl := &queueDrainModel{}
	maxTurns := 5
	var lifecycle []CommandLifecycleEvent
	var attachments []*schema.Message
	terminal := Query(
		context.Background(),
		queueTestParams("query-runtime-input", mdl, coordinator, &maxTurns),
		func(event QueryEvent) {
			if event.CommandLifecycle != nil {
				lifecycle = append(lifecycle, *event.CommandLifecycle)
			}
			if event.Type == EventAttachment && event.AttachmentMessage != nil &&
				event.AttachmentMessage.Extra["runtime_item_id"] == "queued-user" {
				attachments = append(attachments, event.AttachmentMessage)
			}
		},
	)

	if terminal.Reason != TerminalCompleted || mdl.callCount != 2 {
		t.Fatalf("terminal=%#v calls=%d", terminal, mdl.callCount)
	}
	if len(attachments) != 1 || attachments[0].Content != "queued user message" {
		t.Fatalf("runtime attachments = %#v", attachments)
	}
	if len(lifecycle) != 2 ||
		lifecycle[0].CommandUUID != "queued-user" ||
		lifecycle[0].Phase != CommandLifecycleStarted ||
		lifecycle[1].Phase != CommandLifecycleCompleted {
		t.Fatalf("runtime lifecycle = %#v", lifecycle)
	}
	if attachments[0].Extra["command_priority"] != string(RuntimePriorityNext) ||
		attachments[0].Extra["is_meta"] != false {
		t.Fatalf("runtime metadata = %#v", attachments[0].Extra)
	}
	if len(coordinator.Snapshot(RuntimeInputScope{SessionID: "query-runtime-input"})) != 0 {
		t.Fatal("ephemeral coordinator retained a completed runtime item")
	}
}

func TestQueryProjectsInputEnqueuedDuringToolOnlyAfterToolSettles(t *testing.T) {
	coordinator := newTestRuntimeInputCoordinator(t, "query-mid-tool", "")
	mdl := &queueDrainModel{}
	maxTurns := 5
	params := queueTestParams("query-mid-tool", mdl, coordinator, &maxTurns)
	params.ToolExecutor = func(context.Context, string, string) (string, error) {
		_, err := coordinator.Enqueue(RuntimeItem{
			ID:       "busy-user",
			Kind:     RuntimeItemUserPrompt,
			Priority: RuntimePriorityNext,
			Scope:    RuntimeInputScope{SessionID: "query-mid-tool"},
			Origin:   "user",
			UserPrompt: &RuntimeUserPrompt{
				Prompt: "steer during tool",
			},
		})
		return "ok", err
	}
	var order []string
	terminal := Query(context.Background(), params, func(event QueryEvent) {
		switch {
		case event.Type == EventToolResult:
			order = append(order, "tool_result")
		case event.CommandLifecycle != nil &&
			event.CommandLifecycle.CommandUUID == "busy-user" &&
			event.CommandLifecycle.Phase == CommandLifecycleStarted:
			order = append(order, "runtime_started")
		}
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %#v", terminal)
	}
	if len(order) < 2 || order[0] != "tool_result" || order[1] != "runtime_started" {
		t.Fatalf("unsafe projection order = %v", order)
	}
}

func TestQueryRuntimeInputScopeDoesNotLeakAcrossAgents(t *testing.T) {
	coordinator := newTestRuntimeInputCoordinator(t, "query-scope", "")
	mustEnqueueRuntimePrompt(
		t, coordinator, "main-only", RuntimePriorityNext, "", "for main",
	)
	mdl := &queueDrainModel{}
	maxTurns := 5
	params := queueTestParams("query-scope", mdl, coordinator, &maxTurns)
	params.QuerySource = QuerySourceAgent
	params.ToolUseContext.AgentID = "child-agent"
	var consumed bool
	Query(context.Background(), params, func(event QueryEvent) {
		if event.CommandLifecycle != nil &&
			event.CommandLifecycle.CommandUUID == "main-only" {
			consumed = true
		}
	})
	if consumed {
		t.Fatal("main runtime input leaked into child scope")
	}
	if len(coordinator.Snapshot(RuntimeInputScope{SessionID: "query-scope"})) != 1 {
		t.Fatal("main runtime input was removed by child query")
	}
}

func TestQueryProjectsTerminalAgentNotificationAsMetaInput(t *testing.T) {
	coordinator := newTestRuntimeInputCoordinator(t, "query-notification", "")
	_, err := coordinator.Enqueue(RuntimeItem{
		ID:         "agent-notification:agent-123:1",
		Kind:       RuntimeItemAgentNotification,
		Priority:   RuntimePriorityNext,
		Scope:      RuntimeInputScope{SessionID: "query-notification"},
		IsMeta:     true,
		Origin:     "task-notification",
		Provenance: "agent_notification",
		AgentNotification: &RuntimeAgentNotification{
			AgentID: "agent-123", ToolUseID: "tool-use-123",
			Status: "completed", Description: "metadata worker",
			OutputFile: "/tmp/agent-123.out",
			Message:    "<task-notification><status>completed</status></task-notification>",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mdl := &queueDrainModel{}
	maxTurns := 5
	var attachment *schema.Message
	Query(
		context.Background(),
		queueTestParams("query-notification", mdl, coordinator, &maxTurns),
		func(event QueryEvent) {
			if event.AttachmentMessage != nil &&
				event.AttachmentMessage.Extra["runtime_item_kind"] ==
					string(RuntimeItemAgentNotification) {
				attachment = event.AttachmentMessage
			}
		},
	)
	if attachment == nil ||
		!strings.HasPrefix(attachment.Content, "<task-notification>") {
		t.Fatalf("notification attachment = %#v", attachment)
	}
	if attachment.Extra["command_mode"] != "task-notification" ||
		attachment.Extra["is_meta"] != true ||
		attachment.Extra["task_notification_agent_id"] != "agent-123" ||
		attachment.Extra["task_notification_tool_use_id"] != "tool-use-123" {
		t.Fatalf("notification metadata = %#v", attachment.Extra)
	}
}

func TestQueryEmitsAgentProgressAlongsideCoordinatorInput(t *testing.T) {
	coordinator := newTestRuntimeInputCoordinator(t, "query-progress", "")
	mustEnqueueRuntimePrompt(
		t, coordinator, "progress-input", RuntimePriorityNext, "", "continue",
	)
	mdl := &queueDrainModel{}
	maxTurns := 5
	params := queueTestParams("query-progress", mdl, coordinator, &maxTurns)
	params.AgentProgressDrainer = func() []tools.AgentProgressEvent {
		return []tools.AgentProgressEvent{{
			Type: "system", Subtype: "task_progress",
			TaskID: "agent-progress-123", ToolUseID: "tool-use-progress-123",
			Description: "Progress worker",
			Usage: tools.AgentProgressUsage{
				TotalTokens: 42, ToolUses: 3, DurationMS: 100,
			},
			LastToolName: "Read", Summary: "Reading files",
		}}
	}
	var progress *TaskProgressEvent
	Query(context.Background(), params, func(event QueryEvent) {
		if event.TaskProgress != nil {
			progress = event.TaskProgress
		}
	})
	if progress == nil ||
		progress.TaskID != "agent-progress-123" ||
		progress.Usage.TotalTokens != 42 ||
		progress.LastToolName != "Read" {
		t.Fatalf("progress projection = %#v", progress)
	}
}
