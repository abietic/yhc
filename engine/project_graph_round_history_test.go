package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
)

type roundHistoryCaptureModel struct {
	mu        sync.Mutex
	responses []*schema.Message
	inputs    [][]*schema.Message
}

func (m *roundHistoryCaptureModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *roundHistoryCaptureModel) Stream(
	_ context.Context,
	input []*schema.Message,
	_ ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	index := len(m.inputs) - 1
	response := &schema.Message{Role: schema.Assistant, Content: "done"}
	if index < len(m.responses) {
		response = m.responses[index]
	}
	m.mu.Unlock()
	return schema.StreamReaderFromArray([]*schema.Message{response}), nil
}

func (m *roundHistoryCaptureModel) snapshot() [][]*schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([][]*schema.Message, len(m.inputs))
	for index := range m.inputs {
		result[index] = append([]*schema.Message(nil), m.inputs[index]...)
	}
	return result
}

func TestProjectGraphCarriesCommittedToolTurnIntoNextModelRequest(t *testing.T) {
	chatModel := &roundHistoryCaptureModel{responses: []*schema.Message{
		assistantToolCall("enter-plan", "EnterPlanMode", `{}`),
		assistantToolCall(
			"todo-plan",
			"TodoWrite",
			`{"todos":[{"content":"write the plan","status":"in_progress","activeForm":"Writing the plan"}]}`,
		),
		{Role: schema.Assistant, Content: "done"},
	}}
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	maxTurns := 4
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID:      "round-history",
		ThreadID:       "round-history",
		CWD:            t.TempDir(),
		TranscriptDir:  t.TempDir(),
		PermissionMode: permission.ModeBypassPermissions,
		ChatModel:      chatModel,
		ToolRegistry:   registry,
		ToolSelection: &tools.ToolSelection{
			Names: []string{"EnterPlanMode", "TodoWrite"},
		},
		MaxTurns: maxTurns,
	})
	t.Cleanup(engine.Close)

	events, _ := engine.SubmitMessage(t.Context(), "enter plan mode")
	for range events {
	}

	inputs := chatModel.snapshot()
	if len(inputs) != 3 {
		t.Fatalf("model requests = %d, want 3", len(inputs))
	}
	assertToolTurnInModelInput(t, inputs[1], "enter-plan", "EnterPlanMode")
	assertToolTurnInModelInput(t, inputs[2], "enter-plan", "EnterPlanMode")
	assertToolTurnInModelInput(t, inputs[2], "todo-plan", "TodoWrite")
}

func TestProjectGraphResumeCarriesInterruptedToolTurnIntoNextModelRequest(t *testing.T) {
	chatModel := &roundHistoryCaptureModel{responses: []*schema.Message{
		assistantToolCall("enter-plan", "EnterPlanMode", `{}`),
		assistantToolCall(
			"todo-plan",
			"TodoWrite",
			`{"todos":[{"content":"write the plan","status":"in_progress","activeForm":"Writing the plan"}]}`,
		),
		{Role: schema.Assistant, Content: "done"},
	}}
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	maxTurns := 4
	queryEngine := NewQueryEngine(QueryEngineConfig{
		SessionID:      "round-history-resume",
		ThreadID:       "round-history-resume",
		CWD:            t.TempDir(),
		TranscriptDir:  t.TempDir(),
		PermissionMode: permission.ModeDefault,
		ChatModel:      chatModel,
		ToolRegistry:   registry,
		ToolSelection: &tools.ToolSelection{
			Names: []string{"EnterPlanMode", "TodoWrite"},
		},
		MaxTurns: maxTurns,
		PermissionPrompt: func(
			context.Context,
			PermissionPromptRequest,
		) PermissionInteractionResult {
			t.Fatal("ProjectGraph must not use the blocking permission adapter")
			return PermissionInteractionResult{Decision: PermissionDeny}
		},
	})
	t.Cleanup(queryEngine.Close)

	events, _ := queryEngine.SubmitMessage(t.Context(), "enter plan mode")
	initial := collectGraphHITLEvents(events)
	if initial.terminal == nil ||
		initial.terminal.Reason != TerminalWaitingInput ||
		initial.request == nil ||
		initial.request.ToolUseID != "enter-plan" {
		t.Fatalf(
			"initial interrupt = terminal %#v request %#v",
			initial.terminal,
			initial.request,
		)
	}
	if !queryEngine.ResolvePermissionInteraction(
		"enter-plan",
		PermissionInteractionResult{Decision: PermissionAllowOnce},
	) {
		t.Fatal("permission decision was not accepted")
	}
	item, ok, err := queryEngine.ClaimNextRuntimeItem()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || item.Kind != RuntimeItemPermissionDecision {
		t.Fatalf("claimed item = %#v ok=%v", item, ok)
	}
	resumedEvents, _ := queryEngine.SubmitRuntimeItem(t.Context(), item)
	resumed := collectGraphHITLEvents(resumedEvents)
	if resumed.terminal == nil || resumed.terminal.Reason != TerminalCompleted {
		t.Fatalf("resumed terminal = %#v", resumed.terminal)
	}

	inputs := chatModel.snapshot()
	if len(inputs) != 3 {
		t.Fatalf("model requests = %d, want 3", len(inputs))
	}
	assertToolTurnInModelInput(t, inputs[1], "enter-plan", "EnterPlanMode")
	assertToolTurnInModelInput(t, inputs[2], "enter-plan", "EnterPlanMode")
	assertToolTurnInModelInput(t, inputs[2], "todo-plan", "TodoWrite")
}

func TestProjectGraphEachResumeRestoresOnlyItsInterruptedToolRound(t *testing.T) {
	chatModel := &roundHistoryCaptureModel{responses: []*schema.Message{
		assistantToolCall("write-a", "GraphWriteA", `{"path":"a"}`),
		assistantToolCall("write-b", "GraphWriteB", `{"path":"b"}`),
		{Role: schema.Assistant, Content: "done"},
	}}
	registry := tools.NewRegistry()
	var executions atomic.Int32
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "GraphWriteA"},
		ExecuteCtx: func(context.Context, string) (string, error) {
			executions.Add(1)
			return "ok", nil
		},
	})
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "GraphWriteB"},
		ExecuteCtx: func(context.Context, string) (string, error) {
			executions.Add(1)
			return "ok", nil
		},
	})
	maxTurns := 4
	queryEngine := NewQueryEngine(QueryEngineConfig{
		SessionID:      "round-history-chained-resume",
		ThreadID:       "round-history-chained-resume",
		CWD:            t.TempDir(),
		TranscriptDir:  t.TempDir(),
		PermissionMode: permission.ModeDefault,
		ChatModel:      chatModel,
		ToolRegistry:   registry,
		ToolSelection: &tools.ToolSelection{
			Names: []string{"GraphWriteA", "GraphWriteB"},
		},
		MaxTurns: maxTurns,
		PermissionPrompt: func(
			context.Context,
			PermissionPromptRequest,
		) PermissionInteractionResult {
			t.Fatal("ProjectGraph must not use the blocking permission adapter")
			return PermissionInteractionResult{Decision: PermissionDeny}
		},
	})
	t.Cleanup(queryEngine.Close)

	events, _ := queryEngine.SubmitMessage(t.Context(), "write twice")
	first := collectGraphHITLEvents(events)
	if first.terminal == nil ||
		first.terminal.Reason != TerminalWaitingInput ||
		first.request == nil ||
		first.request.ToolUseID != "write-a" {
		t.Fatalf(
			"first interrupt = terminal %#v request %#v",
			first.terminal,
			first.request,
		)
	}
	resolveProjectGraphRoundHistoryPermission(t, queryEngine, "write-a")
	firstResumeEvents, _ := queryEngine.SubmitRuntimeItem(
		t.Context(),
		claimProjectGraphRoundHistoryDecision(t, queryEngine),
	)
	second := collectGraphHITLEvents(firstResumeEvents)
	if second.terminal == nil ||
		second.terminal.Reason != TerminalWaitingInput ||
		second.request == nil ||
		second.request.ToolUseID != "write-b" {
		t.Fatalf(
			"second interrupt = terminal %#v request %#v",
			second.terminal,
			second.request,
		)
	}
	if executions.Load() != 1 {
		t.Fatalf("executions before second decision = %d, want 1", executions.Load())
	}

	resolveProjectGraphRoundHistoryPermission(t, queryEngine, "write-b")
	secondResumeEvents, _ := queryEngine.SubmitRuntimeItem(
		t.Context(),
		claimProjectGraphRoundHistoryDecision(t, queryEngine),
	)
	final := collectGraphHITLEvents(secondResumeEvents)
	if final.terminal == nil || final.terminal.Reason != TerminalCompleted {
		t.Fatalf("final terminal = %#v", final.terminal)
	}
	if executions.Load() != 2 {
		t.Fatalf("tool executions = %d, want 2", executions.Load())
	}

	inputs := chatModel.snapshot()
	if len(inputs) != 3 {
		t.Fatalf("model requests = %d, want 3", len(inputs))
	}
	assertToolTurnInModelInput(t, inputs[1], "write-a", "GraphWriteA")
	assertToolTurnInModelInput(t, inputs[2], "write-a", "GraphWriteA")
	assertToolTurnInModelInput(t, inputs[2], "write-b", "GraphWriteB")
}

func resolveProjectGraphRoundHistoryPermission(
	t *testing.T,
	queryEngine *QueryEngine,
	toolUseID string,
) {
	t.Helper()
	if !queryEngine.ResolvePermissionInteraction(
		toolUseID,
		PermissionInteractionResult{Decision: PermissionAllowOnce},
	) {
		t.Fatalf("permission decision for %q was not accepted", toolUseID)
	}
}

func claimProjectGraphRoundHistoryDecision(
	t *testing.T,
	queryEngine *QueryEngine,
) RuntimeItem {
	t.Helper()
	item, ok, err := queryEngine.ClaimNextRuntimeItem()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || item.Kind != RuntimeItemPermissionDecision {
		t.Fatalf("claimed item = %#v ok=%v", item, ok)
	}
	return item
}

func assistantToolCall(id, name, arguments string) *schema.Message {
	return &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:   id,
			Type: "function",
			Function: schema.FunctionCall{
				Name:      name,
				Arguments: arguments,
			},
		}},
	}
}

func assertToolTurnInModelInput(
	t *testing.T,
	input []*schema.Message,
	callID string,
	toolName string,
) {
	t.Helper()
	var assistantFound bool
	var resultFound bool
	for _, message := range input {
		if message == nil {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.ID == callID && call.Function.Name == toolName {
				assistantFound = true
			}
		}
		if message.Role == schema.Tool &&
			message.ToolCallID == callID &&
			message.ToolName == toolName {
			resultFound = true
		}
	}
	if !assistantFound || !resultFound {
		t.Fatalf(
			"model input omitted committed %s turn: assistant=%v result=%v input=%#v",
			toolName,
			assistantFound,
			resultFound,
			input,
		)
	}
}
