package engine

import (
	"context"
	"reflect"
	"testing"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

func TestPermissionPromptReporterEmitsCallbackLifecycle(t *testing.T) {
	var events []QueryEvent
	ctx := withToolUseID(context.Background(), "tool-1")
	ctx = withPermissionPromptEmitter(ctx, func(event QueryEvent) {
		events = append(events, event)
	})
	input := map[string]any{"command": "go test ./..."}
	ReportPermissionPromptRequested(ctx, "Bash", input, "approve command")
	ReportPermissionPromptResolved(ctx, true, "")

	if len(events) != 2 || events[0].Type != EventPermissionRequest || events[1].Type != EventPermissionResolved {
		t.Fatalf("permission lifecycle = %#v", events)
	}
	request := events[0].PermissionRequest
	if request == nil || request.ToolUseID != "tool-1" || request.ToolName != "Bash" ||
		request.CanonicalToolName != "Bash" || request.Source != "callback" ||
		request.Kind != PermissionInteractionKindPermission ||
		request.Message != "approve command" {
		t.Fatalf("permission request = %#v", request)
	}
	input["command"] = "mutated"
	if request.Input["command"] != "go test ./..." {
		t.Fatalf("permission input was not cloned: %#v", request.Input)
	}
	resolved := events[1].PermissionResolved
	if resolved == nil || resolved.ToolUseID != "tool-1" || resolved.Decision != string(permission.DecisionAllow) {
		t.Fatalf("permission resolution = %#v", resolved)
	}
}

func TestPermissionPromptReporterClassifiesAskUserQuestionAtProducer(t *testing.T) {
	var event QueryEvent
	ctx := withToolUseID(context.Background(), "question-1")
	ctx = withPermissionPromptEmitter(ctx, func(received QueryEvent) { event = received })
	ReportPermissionPromptRequested(ctx, " askuserquestion ", nil, "choose")
	if event.PermissionRequest == nil || event.PermissionRequest.Kind != PermissionInteractionKindQuestion ||
		event.PermissionRequest.Source != "callback" || event.PermissionRequest.CanonicalToolName != "askuserquestion" {
		t.Fatalf("permission request = %#v", event.PermissionRequest)
	}
}

func TestExecuteToolCallConnectsInteractiveReporterToYieldStream(t *testing.T) {
	var events []QueryEvent
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	outcome := executeToolCall(context.Background(), QueryParams{
		ToolRegistry: registry,
		CanUseTool: func(ctx context.Context, toolName string, input map[string]any, _ *ToolUseContext) (bool, string) {
			ReportPermissionPromptRequested(ctx, toolName, input, "approve")
			ReportPermissionPromptResolved(ctx, true, "")
			return true, ""
		},
		ToolExecutor: func(context.Context, string, string) (string, error) { return "ok", nil },
	}, nil, &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeDefault}}, &schema.ToolCall{
		ID: "tool-stream-1", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"make test"}`},
	}, func(event QueryEvent) {
		events = append(events, event)
	})
	if outcome == nil || outcome.Result == nil || outcome.Result.Content != "ok" {
		t.Fatalf("tool outcome = %#v", outcome)
	}
	var permissionEvents []QueryEvent
	for _, event := range events {
		if event.Type == EventPermissionRequest ||
			event.Type == EventPermissionResolved {
			permissionEvents = append(permissionEvents, event)
		}
	}
	if len(permissionEvents) != 2 ||
		permissionEvents[0].Type != EventPermissionRequest ||
		permissionEvents[1].Type != EventPermissionResolved ||
		permissionEvents[0].PermissionRequest.ToolUseID != "tool-stream-1" {
		t.Fatalf("yielded permission lifecycle = %#v", events)
	}
	if request := permissionEvents[0].PermissionRequest; request.Kind != PermissionInteractionKindPermission ||
		request.Source != "callback" || request.CanonicalToolName != "Bash" {
		t.Fatalf("callback producer identity = %#v", request)
	}
	if canonicalProjectionKindCount(
		events,
		CanonicalProjectionToolStart,
	) != 1 ||
		canonicalProjectionKindCount(
			events,
			CanonicalProjectionToolInput,
		) != 1 {
		t.Fatalf("yielded canonical lifecycle = %#v", events)
	}
}

func TestThreadAttentionSnapshotsAreUnresolvedOnlyAndRetainTerminalOwner(t *testing.T) {
	store := NewRuntimeStateStore(RuntimeStoreLimits{Threads: 2, Agents: 4})
	apply := func(event QueryEvent) {
		t.Helper()
		if err := store.Apply(event); err != nil {
			t.Fatal(err)
		}
	}
	apply(threadCatalogEvent("leader-thread", "leader-turn", 1, EventPermissionRequest, func(event *QueryEvent) {
		event.PermissionRequest = &PermissionRequestEvent{
			ToolName: "Bash", ToolUseID: "permission-1", Input: map[string]any{"command": "make test"},
			Message: "approve tests", Source: "callback",
		}
	}))
	apply(threadCatalogEvent("child-thread", "agent-launch:agent-1:1", 1, EventAgentLifecycle, func(event *QueryEvent) {
		event.AgentID = "agent-1"
		event.ParentThreadID = "leader-thread"
		event.AgentLifecycle = &AgentLifecycleEvent{Phase: "launched", Status: "running", TranscriptPath: "/tmp/agent-1.jsonl", StartedAt: event.Timestamp}
	}))
	apply(threadCatalogEvent("child-thread", "child-turn", 2, EventPermissionRequest, func(event *QueryEvent) {
		event.AgentID = "agent-1"
		event.ParentThreadID = "leader-thread"
		event.PermissionRequest = &PermissionRequestEvent{
			ToolName: "AskUserQuestion", ToolUseID: "question-1", Input: map[string]any{"questions": []any{"choose"}},
			Message: "choose", Source: "callback", Kind: PermissionInteractionKindQuestion,
		}
	}))
	apply(threadCatalogEvent("child-thread", "child-turn", 3, EventTerminal, func(event *QueryEvent) {
		event.AgentID = "agent-1"
		event.ParentThreadID = "leader-thread"
		event.TerminalInfo = &Terminal{Reason: TerminalCompleted}
	}))

	attention := store.ThreadAttentionSnapshots()
	if len(attention) != 2 {
		t.Fatalf("attention order = %#v", attention)
	}
	rows := make(map[string]RuntimeThreadAttentionSnapshot, len(attention))
	for _, row := range attention {
		rows[row.ThreadID] = row
	}
	child := rows["child-thread"]
	if child.Status != RuntimeThreadCompleted || len(child.Requests) != 1 ||
		child.Requests[0].Kind != "question" || child.Requests[0].Source != "callback" {
		t.Fatalf("terminal question attention = %#v", child)
	}
	child.Requests[0].Input["questions"] = nil
	again := store.ThreadAttentionSnapshots()
	againRows := make(map[string]RuntimeThreadAttentionSnapshot, len(again))
	for _, row := range again {
		againRows[row.ThreadID] = row
	}
	if reflect.DeepEqual(child.Requests[0].Input, againRows["child-thread"].Requests[0].Input) {
		t.Fatal("attention snapshot input was not defensive")
	}

	apply(threadCatalogEvent("leader-thread", "leader-turn", 2, EventPermissionResolved, func(event *QueryEvent) {
		event.PermissionResolved = &PermissionResolvedEvent{ToolUseID: "permission-1", Decision: string(permission.DecisionAllow)}
	}))
	apply(threadCatalogEvent("child-thread", "child-turn", 4, EventPermissionResolved, func(event *QueryEvent) {
		event.AgentID = "agent-1"
		event.ParentThreadID = "leader-thread"
		event.PermissionResolved = &PermissionResolvedEvent{ToolUseID: "question-1", Decision: string(permission.DecisionDeny)}
	}))
	if got := store.ThreadAttentionSnapshots(); len(got) != 0 {
		t.Fatalf("resolved attention replayed = %#v", got)
	}

	apply(threadCatalogEvent("other-thread", "other-turn", 1, EventStreamRequestStart, nil))
	if _, exists := store.ThreadSnapshot("child-thread"); exists {
		t.Fatal("resolved terminal child was not eligible for bounded eviction")
	}
}
