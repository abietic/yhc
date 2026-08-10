package engine

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type repeatedToolSequenceModel struct {
	calls atomic.Int32
}

func (m *repeatedToolSequenceModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *repeatedToolSequenceModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	call := m.calls.Add(1)
	if call <= 3 {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:       string('a' + call - 1),
				Type:     "function",
				Function: schema.FunctionCall{Name: "Count", Arguments: `{"count":5}`},
			}},
		}}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}), nil
}

func newRepeatedToolTestRegistry() *tools.Registry {
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{
			Name: "Count", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"count": {Type: schema.Integer, Required: true},
				"flag":  {Type: schema.Boolean},
			}),
		},
		Aliases:           []string{"count_alias"},
		IsConcurrencySafe: func(map[string]any) bool { return true },
	})
	return registry
}

func repeatedToolTestCall(id, name, arguments string) *schema.ToolCall {
	return &schema.ToolCall{ID: id, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: arguments}}
}

func TestRepeatedToolGuardStopsBeforeHooksPermissionAndExecution(t *testing.T) {
	registry := newRepeatedToolTestRegistry()
	guard := newRepeatedToolCallGuard()
	var hookCalls atomic.Int32
	var permissionCalls atomic.Int32
	var executionCalls atomic.Int32
	var promptCalls atomic.Int32
	hookExecutor := hooks.NewExecutor()
	hookExecutor.RegisterPreTool(func(context.Context, string, string, map[string]any) *hooks.PreToolHookResult {
		hookCalls.Add(1)
		return &hooks.PreToolHookResult{}
	})

	params := QueryParams{
		ToolRegistry:      registry,
		repeatedToolGuard: guard,
		CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
			permissionCalls.Add(1)
			return true, ""
		},
		ToolExecutor: func(context.Context, string, string) (string, error) {
			executionCalls.Add(1)
			return "ok", nil
		},
		RepeatedToolCallPrompt: func(context.Context, string, string, int, *ToolUseContext) (bool, string) {
			promptCalls.Add(1)
			return false, "stop and change strategy"
		},
	}
	var events []QueryEvent
	yield := func(event QueryEvent) { events = append(events, event) }
	calls := []*schema.ToolCall{
		repeatedToolTestCall("one", "count_alias", `{"count":"5","flag":"true"}`),
		repeatedToolTestCall("two", "Count", `{"flag":true,"count":5}`),
		repeatedToolTestCall("three", "count_alias", `{"count":5,"flag":true}`),
		repeatedToolTestCall("four", "Count", `{"count":5,"flag":true}`),
	}
	for index, call := range calls {
		outcome := executeToolCall(context.Background(), params, hookExecutor, nil, call, yield)
		if outcome == nil || outcome.Result == nil {
			t.Fatalf("call %d outcome = %#v", index+1, outcome)
		}
		if index >= 2 && !strings.Contains(outcome.Result.Content, "Repeated identical tool call blocked") {
			t.Fatalf("call %d result = %q", index+1, outcome.Result.Content)
		}
	}

	if got := hookCalls.Load(); got != 2 {
		t.Fatalf("pre-hook calls = %d, want 2", got)
	}
	if got := permissionCalls.Load(); got != 2 {
		t.Fatalf("permission calls = %d, want 2", got)
	}
	if got := executionCalls.Load(); got != 2 {
		t.Fatalf("execution calls = %d, want 2", got)
	}
	if got := promptCalls.Load(); got != 1 {
		t.Fatalf("override prompts = %d, want 1", got)
	}
	var interactions []QueryEvent
	for _, event := range events {
		if event.Type == EventPermissionRequest ||
			event.Type == EventPermissionResolved {
			interactions = append(interactions, event)
		}
	}
	if len(interactions) != 2 ||
		interactions[0].Type != EventPermissionRequest ||
		interactions[1].Type != EventPermissionResolved {
		t.Fatalf("interaction events = %#v", events)
	}
	if canonicalProjectionKindCount(
		events,
		CanonicalProjectionToolStart,
	) != 4 ||
		canonicalProjectionKindCount(
			events,
			CanonicalProjectionToolInput,
		) != 2 {
		t.Fatalf("canonical lifecycle events = %#v", events)
	}
	request := interactions[0].PermissionRequest
	if request == nil || request.Kind != "repeated_tool" || request.Attempt != 3 || request.ToolName != "Count" || request.Input != nil {
		t.Fatalf("safe repeated-tool request = %#v", request)
	}
}

func TestRepeatedToolOverrideStillRunsOrdinaryPermissionChain(t *testing.T) {
	registry := newRepeatedToolTestRegistry()
	guard := newRepeatedToolCallGuard()
	var hookCalls atomic.Int32
	var permissionCalls atomic.Int32
	var executionCalls atomic.Int32
	hookExecutor := hooks.NewExecutor()
	hookExecutor.RegisterPreTool(func(context.Context, string, string, map[string]any) *hooks.PreToolHookResult {
		hookCalls.Add(1)
		return &hooks.PreToolHookResult{}
	})
	params := QueryParams{
		ToolRegistry:      registry,
		repeatedToolGuard: guard,
		CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
			permissionCalls.Add(1)
			return true, ""
		},
		ToolExecutor: func(context.Context, string, string) (string, error) {
			executionCalls.Add(1)
			return "ok", nil
		},
		RepeatedToolCallPrompt: func(context.Context, string, string, int, *ToolUseContext) (bool, string) {
			return true, "run once"
		},
	}
	for index := 0; index < 4; index++ {
		outcome := executeToolCall(context.Background(), params, hookExecutor, nil,
			repeatedToolTestCall(string(rune('a'+index)), "Count", `{"count":5}`), nil)
		if outcome == nil || outcome.Result == nil || outcome.Result.Content != "ok" {
			t.Fatalf("call %d outcome = %#v", index+1, outcome)
		}
	}
	if hookCalls.Load() != 4 || permissionCalls.Load() != 4 || executionCalls.Load() != 4 {
		t.Fatalf("chain counts hook=%d permission=%d execution=%d, want 4 each",
			hookCalls.Load(), permissionCalls.Load(), executionCalls.Load())
	}
}

func TestRepeatedToolBatchAdmissionUsesModelOrder(t *testing.T) {
	registry := newRepeatedToolTestRegistry()
	guard := newRepeatedToolCallGuard()
	promptedID := make(chan string, 1)
	params := QueryParams{
		ToolRegistry:      registry,
		repeatedToolGuard: guard,
		RepeatedToolCallPrompt: func(_ context.Context, _, toolUseID string, _ int, _ *ToolUseContext) (bool, string) {
			promptedID <- toolUseID
			return false, "stop"
		},
		ToolExecutor: func(context.Context, string, string) (string, error) { return "ok", nil },
	}
	calls := reserveRepeatedToolCalls([]*schema.ToolCall{
		repeatedToolTestCall("one", "Count", `{"count":5}`),
		repeatedToolTestCall("two", "Count", `{"count":5}`),
		repeatedToolTestCall("three", "Count", `{"count":5}`),
	}, guard)
	batches := partitionToolCalls(calls, registry)
	if len(batches) != 1 || !batches[0].IsConcurrencySafe {
		t.Fatalf("batches = %#v", batches)
	}
	outcomes := executeToolBatch(context.Background(), params, nil, nil, batches[0], nil)
	if len(outcomes) != 3 {
		t.Fatalf("outcomes = %d, want 3", len(outcomes))
	}
	if got := <-promptedID; got != "three" {
		t.Fatalf("prompted tool use ID = %q, want third model call", got)
	}
}

func TestRepeatedToolQueryStreamingFailsClosedWithoutPrompt(t *testing.T) {
	registry := newRepeatedToolTestRegistry()
	chatModel := &repeatedToolSequenceModel{}
	maxTurns := 5
	var executions atomic.Int32
	var eventMu sync.Mutex
	var events []QueryEvent
	terminal := Query(context.Background(), QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "count"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "test"},
		ChatModel:    chatModel,
		MaxTurns:     &maxTurns,
		ToolRegistry: registry,
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			Tools: registry.List(),
		}},
		ToolExecutor: func(context.Context, string, string) (string, error) {
			executions.Add(1)
			return "ok", nil
		},
	}, func(event QueryEvent) {
		eventMu.Lock()
		events = append(events, event)
		eventMu.Unlock()
	})
	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %#v", terminal)
	}
	if got := executions.Load(); got != 2 {
		t.Fatalf("executions = %d, want 2", got)
	}
	var request *PermissionRequestEvent
	var blocked bool
	eventMu.Lock()
	defer eventMu.Unlock()
	for _, event := range events {
		if event.Type == EventPermissionRequest {
			request = event.PermissionRequest
		}
		if event.Type == EventToolResult && event.ToolResultMessage != nil && strings.Contains(event.ToolResultMessage.Content, "no interactive one-call override") {
			blocked = true
		}
	}
	if request == nil || request.Kind != "repeated_tool" || request.Attempt != 3 || request.Input != nil {
		t.Fatalf("request = %#v", request)
	}
	if !blocked {
		t.Fatal("missing model-visible fail-closed tool result")
	}
}

func TestRepeatedToolQueryQueuedInputResetsStreak(t *testing.T) {
	registry := newRepeatedToolTestRegistry()
	chatModel := &repeatedToolSequenceModel{}
	coordinator := newTestRuntimeInputCoordinator(t, "repeated-tool", "")
	_, err := coordinator.Enqueue(RuntimeItem{
		ID:       "follow-up",
		Kind:     RuntimeItemUserPrompt,
		Priority: RuntimePriorityNext,
		Scope:    RuntimeInputScope{SessionID: "repeated-tool"},
		Origin:   "sdk",
		UserPrompt: &RuntimeUserPrompt{
			Prompt: "continue with this input",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	maxTurns := 5
	var executions atomic.Int32
	var promptCalls atomic.Int32
	Query(context.Background(), QueryParams{
		Messages:         []*schema.Message{{Role: schema.User, Content: "count"}},
		SystemPrompt:     &schema.Message{Role: schema.System, Content: "test"},
		ChatModel:        chatModel,
		MaxTurns:         &maxTurns,
		SessionID:        "repeated-tool",
		InputCoordinator: coordinator,
		ToolRegistry:     registry,
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			Tools: registry.List(),
		}},
		ToolExecutor: func(context.Context, string, string) (string, error) {
			executions.Add(1)
			return "ok", nil
		},
		RepeatedToolCallPrompt: func(context.Context, string, string, int, *ToolUseContext) (bool, string) {
			promptCalls.Add(1)
			return false, "unexpected"
		},
	}, func(QueryEvent) {})
	if got := executions.Load(); got != 3 {
		t.Fatalf("executions = %d, want 3 after queued-input reset", got)
	}
	if got := promptCalls.Load(); got != 0 {
		t.Fatalf("override prompts = %d, want 0 after queued-input reset", got)
	}
}

func TestRepeatedToolRuntimeProjectionIsTypedAndInputFree(t *testing.T) {
	request := runtimeTestEvent(1, "turn-1", EventPermissionRequest, func(event *QueryEvent) {
		event.PermissionRequest = &PermissionRequestEvent{
			ToolName: "Count", ToolUseID: "third", Kind: "repeated_tool", Attempt: 3,
			Message: "third identical call", Source: "callback",
		}
	})
	store := NewRuntimeStateStore()
	if err := store.Replay([]QueryEvent{request}); err != nil {
		t.Fatal(err)
	}
	interaction := store.Snapshot("thread-1").Threads["thread-1"].PendingInteractions["third"]
	if interaction.Kind != "repeated_tool" || interaction.Attempt != 3 || interaction.Input != nil {
		t.Fatalf("interaction = %#v", interaction)
	}
	resolved := runtimeTestEvent(2, "turn-1", EventPermissionResolved, func(event *QueryEvent) {
		event.PermissionResolved = &PermissionResolvedEvent{
			ToolUseID: "third", Kind: "repeated_tool", Attempt: 3, Decision: "deny",
		}
	})
	if err := store.Replay([]QueryEvent{resolved}); err != nil {
		t.Fatal(err)
	}
	if pending := store.Snapshot("thread-1").Threads["thread-1"].PendingInteractions; len(pending) != 0 {
		t.Fatalf("resolved interaction remained pending: %#v", pending)
	}
}

func TestRepeatedToolExecuteCancellationDoesNotReleaseBeforePredecessor(t *testing.T) {
	registry := newRepeatedToolTestRegistry()
	guard := newRepeatedToolCallGuard()
	fingerprint := repeatedToolCallFingerprint("Count", map[string]any{"count": 5})
	_ = runRepeatedToolCall(t, guard, fingerprint, false)
	_ = runRepeatedToolCall(t, guard, fingerprint, false)

	third := reserveRepeatedToolCall(repeatedToolTestCall("third", "Count", `{"count":5}`), guard)
	canceled := reserveRepeatedToolCall(repeatedToolTestCall("canceled", "Count", `{"count":5}`), guard)
	successor := reserveRepeatedToolCall(repeatedToolTestCall("successor", "Count", `{"count":5}`), guard)
	promptEntered := make(chan struct{})
	promptRelease := make(chan struct{})
	params := QueryParams{
		ToolRegistry:      registry,
		repeatedToolGuard: guard,
		RepeatedToolCallPrompt: func(context.Context, string, string, int, *ToolUseContext) (bool, string) {
			close(promptEntered)
			<-promptRelease
			return false, "stop"
		},
		ToolExecutor: func(context.Context, string, string) (string, error) { return "ok", nil },
	}
	thirdDone := make(chan *toolExecutionOutcome, 1)
	go func() {
		thirdDone <- executeToolCall(context.Background(), params, nil, nil, third, nil)
	}()
	<-promptEntered

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	canceledOutcome := executeToolCall(canceledCtx, params, nil, nil, canceled, nil)
	if canceledOutcome == nil || canceledOutcome.Result == nil || !strings.Contains(canceledOutcome.Result.Content, "context canceled") {
		t.Fatalf("canceled outcome = %#v", canceledOutcome)
	}
	canceledTicket := repeatedToolTicket(canceled)
	select {
	case <-canceledTicket.done:
		t.Fatal("canceled ticket released before unresolved predecessor")
	default:
	}

	successorDone := make(chan *toolExecutionOutcome, 1)
	go func() {
		successorDone <- executeToolCall(context.Background(), params, nil, nil, successor, nil)
	}()
	select {
	case outcome := <-successorDone:
		t.Fatalf("successor bypassed unresolved predecessor: %#v", outcome)
	default:
	}

	close(promptRelease)
	if outcome := <-thirdDone; outcome == nil || outcome.Result == nil || !strings.Contains(outcome.Result.Content, "Repeated identical tool call blocked") {
		t.Fatalf("third outcome = %#v", outcome)
	}
	if outcome := <-successorDone; outcome == nil || outcome.Result == nil || !strings.Contains(outcome.Result.Content, "Repeated identical tool call blocked") {
		t.Fatalf("successor outcome = %#v", outcome)
	}
}
