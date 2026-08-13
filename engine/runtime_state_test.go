package engine

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
)

func TestRuntimeStateStoreDeterministicReplayAndDefensiveSnapshot(t *testing.T) {
	events := []QueryEvent{
		runtimeTestEvent(1, "turn-1", EventAssistant, func(evt *QueryEvent) {
			evt.AssistantMessage = &schema.Message{
				Role:    schema.Assistant,
				Content: "inspect files",
				ToolCalls: []schema.ToolCall{{
					ID: "call-read",
					Function: schema.FunctionCall{
						Name:      "Read",
						Arguments: `{"file_path":"README.md"}`,
					},
				}},
			}
		}),
		runtimeTestEvent(2, "turn-1", EventToolProgress, func(evt *QueryEvent) {
			evt.ToolProgress = &ToolProgressEvent{ToolName: "Read", ToolUseID: "call-read", Content: "reading"}
		}),
		runtimeTestEvent(3, "turn-1", EventPermissionRequest, func(evt *QueryEvent) {
			evt.PermissionRequest = &PermissionRequestEvent{
				ToolName: "Bash", ToolUseID: "call-bash", Message: "run tests", Input: map[string]any{"command": "go test ./..."},
			}
		}),
		runtimeTestEvent(4, "turn-1", EventPermissionResolved, func(evt *QueryEvent) {
			evt.PermissionResolved = &PermissionResolvedEvent{ToolUseID: "call-bash", Decision: "allow"}
		}),
		runtimeTestEvent(5, "turn-1", EventToolResult, func(evt *QueryEvent) {
			evt.ToolResultMessage = &schema.Message{Role: schema.Tool, ToolCallID: "call-read", ToolName: "Read", Content: "contents"}
		}),
		runtimeTestEvent(6, "turn-1", EventTerminal, func(evt *QueryEvent) {
			evt.TerminalInfo = &Terminal{Reason: TerminalCompleted, TurnCount: 1}
		}),
	}

	left := NewRuntimeStateStore()
	right := NewRuntimeStateStore()
	if err := left.Replay(events); err != nil {
		t.Fatal(err)
	}
	if err := right.Replay(events); err != nil {
		t.Fatal(err)
	}
	want := right.Snapshot("thread-1")
	got := left.Snapshot("thread-1")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replay snapshots differ:\nleft=%#v\nright=%#v", got, want)
	}

	thread := got.Threads["thread-1"]
	thread.Events[0].Summary = "mutated"
	thread.Messages[0].Content = "mutated"
	thread.Messages[0].ToolCalls[0].Name = "Write"
	tool := thread.Tools["call-read"]
	tool.Name = "Write"
	thread.Tools["call-read"] = tool
	got.Threads["thread-1"] = thread
	delete(got.Agents, "agent-1")

	if after := left.Snapshot("thread-1"); !reflect.DeepEqual(after, want) {
		t.Fatalf("snapshot mutation leaked into store:\nafter=%#v\nwant=%#v", after, want)
	}
}

func TestRuntimeInteractionUsesExplicitQuestionKind(t *testing.T) {
	store := NewRuntimeStateStore()
	if err := store.Replay([]QueryEvent{runtimeTestEvent(1, "turn-question", EventPermissionRequest, func(event *QueryEvent) {
		event.PermissionRequest = &PermissionRequestEvent{
			ToolName: "AskUserQuestion", ToolUseID: "question-1",
			Kind: PermissionInteractionKindQuestion,
		}
	})}); err != nil {
		t.Fatal(err)
	}
	interaction := store.Snapshot("thread-1").Threads["thread-1"].PendingInteractions["question-1"]
	if interaction.Kind != PermissionInteractionKindQuestion {
		t.Fatalf("interaction kind = %q, want %q", interaction.Kind, PermissionInteractionKindQuestion)
	}
	if err := store.Apply(runtimeTestEvent(2, "turn-legacy-question", EventPermissionRequest, func(event *QueryEvent) {
		event.PermissionRequest = &PermissionRequestEvent{
			ToolName: "AskUserQuestion", ToolUseID: "legacy-question",
		}
	})); err != nil {
		t.Fatal(err)
	}
	legacy := store.Snapshot("thread-1").Threads["thread-1"].PendingInteractions["legacy-question"]
	if legacy.Kind != PermissionInteractionKindPermission {
		t.Fatalf("legacy tool-name-only kind = %q, want %q", legacy.Kind, PermissionInteractionKindPermission)
	}
}

func TestRuntimeInteractionPreservesCanonicalToolIdentity(t *testing.T) {
	store := NewRuntimeStateStore()
	if err := store.Replay([]QueryEvent{runtimeTestEvent(1, "turn-alias", EventPermissionRequest, func(event *QueryEvent) {
		event.PermissionRequest = &PermissionRequestEvent{
			ToolName: "write_alias", CanonicalToolName: "Write", ToolUseID: "permission-alias",
			Kind: PermissionInteractionKindPermission,
		}
	})}); err != nil {
		t.Fatal(err)
	}
	interaction := store.Snapshot("thread-1").Threads["thread-1"].PendingInteractions["permission-alias"]
	if interaction.ToolName != "write_alias" || interaction.CanonicalToolName != "Write" {
		t.Fatalf("runtime canonical identity = %#v", interaction)
	}
	attention := store.ThreadAttentionSnapshots()
	if len(attention) != 1 || len(attention[0].Requests) != 1 || attention[0].Requests[0].CanonicalToolName != "Write" {
		t.Fatalf("attention canonical identity = %#v", attention)
	}
}

func TestRuntimeStateStoreRejectsInvalidTransitionsWithoutMutation(t *testing.T) {
	store := NewRuntimeStateStore()
	first := runtimeTestEvent(1, "turn-1", EventAssistant, func(evt *QueryEvent) {
		evt.AssistantMessage = &schema.Message{Role: schema.Assistant, Content: "done"}
	})
	if err := store.Apply(first); err != nil {
		t.Fatal(err)
	}

	gap := runtimeTestEvent(3, "turn-1", EventToolProgress, func(evt *QueryEvent) {
		evt.ToolProgress = &ToolProgressEvent{ToolName: "Read", ToolUseID: "call-1"}
	})
	if err := store.Apply(gap); err == nil || !strings.Contains(err.Error(), "want 2") {
		t.Fatalf("sequence gap error = %v, want expected sequence", err)
	}
	if got := store.LastSequence("thread-1"); got != 1 {
		t.Fatalf("failed apply mutated sequence: got %d, want 1", got)
	}

	terminal := runtimeTestEvent(2, "turn-1", EventTerminal, func(evt *QueryEvent) {
		evt.TerminalInfo = &Terminal{Reason: TerminalCompleted}
	})
	if err := store.Apply(terminal); err != nil {
		t.Fatal(err)
	}
	afterTerminal := runtimeTestEvent(3, "turn-1", EventAssistant, func(evt *QueryEvent) {
		evt.AssistantMessage = &schema.Message{Role: schema.Assistant, Content: "late"}
	})
	if err := store.Apply(afterTerminal); err == nil || !strings.Contains(err.Error(), "follows terminal") {
		t.Fatalf("post-terminal error = %v, want terminal transition rejection", err)
	}

	newTurn := runtimeTestEvent(3, "turn-2", EventStreamRequestStart, nil)
	if err := store.Apply(newTurn); err != nil {
		t.Fatalf("new turn after terminal rejected: %v", err)
	}
	changedIdentity := runtimeTestEvent(4, "turn-2", EventStreamRequestStart, nil)
	changedIdentity.ParentThreadID = "different-parent"
	if err := store.Apply(changedIdentity); err == nil || !strings.Contains(err.Error(), "immutable identity") {
		t.Fatalf("identity mutation error = %v, want immutable identity rejection", err)
	}
}

func TestRuntimeStateStoreRetainsOnlyCanonicalProjectionMetadata(t *testing.T) {
	event := runtimeTestEvent(
		1,
		"turn-1",
		EventCanonicalProjection,
		func(evt *QueryEvent) {
			evt.CanonicalProjection = &CanonicalProjectionEvent{
				Version: CanonicalProjectionVersion,
				Kind:    CanonicalProjectionToolInput,
				Tool: &CanonicalToolPayload{
					ToolCallID: "call-1",
					EffectiveInput: []byte(
						`{"authorization":"[redacted]","path":"private"}`,
					),
				},
			}
		},
	)
	store := NewRuntimeStateStore()
	if err := store.Apply(event); err != nil {
		t.Fatal(err)
	}
	thread, ok := store.ThreadSnapshot("thread-1")
	if !ok || len(thread.Events) != 1 {
		t.Fatalf("thread snapshot = %#v", thread)
	}
	record := thread.Events[0]
	if record.ToolUseID != "call-1" ||
		record.Summary != string(CanonicalProjectionToolInput) {
		t.Fatalf("canonical event record = %#v", record)
	}
	if strings.Contains(record.Summary, "private") ||
		strings.Contains(record.Summary, "authorization") {
		t.Fatalf("canonical event summary retained raw input: %#v", record)
	}

	invalid := runtimeTestEvent(
		1,
		"turn-invalid",
		EventCanonicalProjection,
		nil,
	)
	invalid.ThreadID = "thread-invalid"
	if err := NewRuntimeStateStore().Apply(invalid); err == nil ||
		!strings.Contains(err.Error(), "no payload") {
		t.Fatalf("missing canonical payload error = %v", err)
	}
}

func TestRuntimeStateStoreRetainsTerminalAfterEventEviction(t *testing.T) {
	store := NewRuntimeStateStore(RuntimeStoreLimits{EventsPerThread: 2, MessagesPerThread: 1, ToolsPerThread: 1})
	events := []QueryEvent{
		runtimeTestEvent(1, "turn-1", EventAssistant, func(evt *QueryEvent) {
			evt.AssistantMessage = &schema.Message{Role: schema.Assistant, Content: "one"}
		}),
		runtimeTestEvent(2, "turn-1", EventToolProgress, func(evt *QueryEvent) {
			evt.ToolProgress = &ToolProgressEvent{ToolName: "Read", ToolUseID: "call-1", Content: strings.Repeat("x", 4096)}
		}),
		runtimeTestEvent(3, "turn-1", EventTerminal, func(evt *QueryEvent) {
			evt.TerminalInfo = &Terminal{Reason: TerminalModelError, Err: errors.New("model failed")}
		}),
	}
	if err := store.Replay(events); err != nil {
		t.Fatal(err)
	}
	thread, ok := store.ThreadSnapshot("thread-1")
	if !ok {
		t.Fatal("terminal thread identity was evicted")
	}
	if len(thread.Events) != 2 || thread.DroppedEvents != 1 {
		t.Fatalf("event bounds = len %d dropped %d, want 2/1", len(thread.Events), thread.DroppedEvents)
	}
	if thread.LastTerminal == nil || thread.LastTerminal.Reason != TerminalModelError || thread.LastTerminal.Error != "model failed" {
		t.Fatalf("terminal metadata not retained: %#v", thread.LastTerminal)
	}
	if thread.Status != RuntimeThreadFailed || thread.SessionID != "session-1" || thread.ThreadID != "thread-1" {
		t.Fatalf("terminal identity/status = %#v", thread)
	}
	if len(thread.Events[0].Summary) > maxRuntimeEventSummaryRunes {
		t.Fatalf("event summary is not bounded: %d runes", len([]rune(thread.Events[0].Summary)))
	}
}

func TestRuntimeStateStoreFiltersResolvedRequests(t *testing.T) {
	huge := strings.Repeat("x", maxRuntimeInteractionInputBytes*2)
	events := []QueryEvent{
		runtimeTestEvent(1, "turn-1", EventPermissionRequest, func(evt *QueryEvent) {
			evt.PermissionRequest = &PermissionRequestEvent{ToolName: "Bash", ToolUseID: "request-a", Input: map[string]any{"huge": huge}}
		}),
		runtimeTestEvent(2, "turn-1", EventPermissionRequest, func(evt *QueryEvent) {
			evt.PermissionRequest = &PermissionRequestEvent{ToolName: "Write", ToolUseID: "request-b", Input: map[string]any{"file": map[string]any{"path": "README.md"}}}
		}),
		runtimeTestEvent(3, "turn-1", EventPermissionResolved, func(evt *QueryEvent) {
			evt.PermissionResolved = &PermissionResolvedEvent{ToolUseID: "request-a", Decision: "deny"}
		}),
	}
	store := NewRuntimeStateStore()
	if err := store.Replay(events); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot("thread-1")
	requests := snapshot.UnresolvedRequests("thread-1")
	if len(requests) != 1 || requests[0].ID != "request-b" {
		t.Fatalf("unresolved requests = %#v, want only request-b", requests)
	}
	if snapshot.UnresolvedCount != 1 {
		t.Fatalf("unresolved count = %d, want 1", snapshot.UnresolvedCount)
	}
	thread := snapshot.Threads["thread-1"]
	if _, ok := thread.PendingInteractions["request-a"]; ok {
		t.Fatal("resolved request remains in unresolved projection")
	}
	requests[0].Input["file"].(map[string]any)["path"] = "mutated"
	after := store.Snapshot("thread-1").UnresolvedRequests("thread-1")
	if got := after[0].Input["file"].(map[string]any)["path"]; got != "README.md" {
		t.Fatalf("request input mutation leaked into store: %v", got)
	}
}

func TestRuntimeStateStoreResumesWaitingInputOnNewTurn(t *testing.T) {
	store := NewRuntimeStateStore()
	request := runtimeTestEvent(
		1,
		"turn-interrupt",
		EventPermissionRequest,
		func(event *QueryEvent) {
			event.PermissionRequest = &PermissionRequestEvent{
				ToolName:  "Write",
				ToolUseID: "graph-request",
				Source:    "project_graph",
				Kind:      "permission",
			}
		},
	)
	waiting := runtimeTestEvent(
		2,
		"turn-interrupt",
		EventTerminal,
		func(event *QueryEvent) {
			event.TerminalInfo = &Terminal{
				Reason: TerminalWaitingInput,
			}
		},
	)
	resolved := runtimeTestEvent(
		3,
		"turn-resume",
		EventPermissionResolved,
		func(event *QueryEvent) {
			event.PermissionResolved = &PermissionResolvedEvent{
				ToolUseID: "graph-request",
				Decision:  string(PermissionAllowOnce),
				Kind:      "permission",
			}
		},
	)
	completed := runtimeTestEvent(
		4,
		"turn-resume",
		EventTerminal,
		func(event *QueryEvent) {
			event.TerminalInfo = &Terminal{
				Reason: TerminalCompleted,
			}
		},
	)
	for _, event := range []QueryEvent{
		request,
		waiting,
		resolved,
		completed,
	} {
		if err := store.Apply(event); err != nil {
			t.Fatalf("apply %q: %v", event.Type, err)
		}
	}
	thread := store.Snapshot("thread-1").Threads["thread-1"]
	if thread.Status != RuntimeThreadCompleted ||
		thread.ActiveTurnID != "turn-resume" ||
		len(thread.PendingInteractions) != 0 {
		t.Fatalf("resumed thread = %#v", thread)
	}
}

func TestRuntimeStateStoreBoundsThreadsAndUnresolvedInteractions(t *testing.T) {
	store := NewRuntimeStateStore(RuntimeStoreLimits{Threads: 1, InteractionsPerThread: 1})
	request := runtimeTestEvent(1, "turn-1", EventPermissionRequest, func(evt *QueryEvent) {
		evt.PermissionRequest = &PermissionRequestEvent{ToolName: "Bash", ToolUseID: "request-a"}
	})
	if err := store.Apply(request); err != nil {
		t.Fatal(err)
	}
	secondRequest := runtimeTestEvent(2, "turn-1", EventPermissionRequest, func(evt *QueryEvent) {
		evt.PermissionRequest = &PermissionRequestEvent{ToolName: "Write", ToolUseID: "request-b"}
	})
	if err := store.Apply(secondRequest); err == nil || !strings.Contains(err.Error(), "unresolved interactions") {
		t.Fatalf("interaction capacity error = %v", err)
	}
	if got := store.LastSequence("thread-1"); got != 1 {
		t.Fatalf("rejected interaction mutated sequence: got %d, want 1", got)
	}

	threadTwo := runtimeTestEvent(1, "turn-2", EventTerminal, func(evt *QueryEvent) {
		evt.SessionID = "session-2"
		evt.ThreadID = "thread-2"
		evt.AgentID = "agent-2"
		evt.TerminalInfo = &Terminal{Reason: TerminalCompleted}
	})
	if err := store.Apply(threadTwo); err == nil || !strings.Contains(err.Error(), "no terminal thread") {
		t.Fatalf("active thread capacity error = %v", err)
	}

	terminal := runtimeTestEvent(2, "turn-1", EventTerminal, func(evt *QueryEvent) {
		evt.TerminalInfo = &Terminal{Reason: TerminalCompleted}
	})
	if err := store.Apply(terminal); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(threadTwo); err == nil || !strings.Contains(err.Error(), "no terminal thread") {
		t.Fatalf("terminal thread with unresolved attention was evicted: %v", err)
	}
	resolved := runtimeTestEvent(3, "turn-1", EventPermissionResolved, func(evt *QueryEvent) {
		evt.PermissionResolved = &PermissionResolvedEvent{ToolUseID: "request-a", Decision: "allow"}
	})
	if err := store.Apply(resolved); err != nil {
		t.Fatalf("terminal request could not be resolved: %v", err)
	}
	if err := store.Apply(threadTwo); err != nil {
		t.Fatalf("terminal thread was not evictable: %v", err)
	}
	snapshot := store.Snapshot("thread-2")
	if len(snapshot.Threads) != 1 || snapshot.DroppedThreads != 1 {
		t.Fatalf("thread bounds = len %d dropped %d, want 1/1", len(snapshot.Threads), snapshot.DroppedThreads)
	}
	if _, ok := snapshot.Threads["thread-1"]; ok {
		t.Fatal("oldest terminal thread was not evicted")
	}
	if _, ok := snapshot.Threads["thread-2"]; !ok {
		t.Fatal("new terminal thread missing after bounded eviction")
	}
}

func TestRuntimeStateStoreReducesAndBoundsLocalTasks(t *testing.T) {
	store := NewRuntimeStateStore(RuntimeStoreLimits{Tasks: 1})
	completed := runtimeTestEvent(1, "turn-1", EventTaskLifecycle, func(evt *QueryEvent) {
		evt.TaskLifecycle = &TaskLifecycleEvent{
			Phase:       "updated",
			TaskID:      "task-completed",
			Subject:     "Completed task",
			Description: "old terminal row",
			Status:      "completed",
			Owner:       "agent-a",
			UpdatedAt:   evt.Timestamp,
		}
	})
	if err := store.Apply(completed); err != nil {
		t.Fatal(err)
	}
	running := runtimeTestEvent(2, "turn-1", EventTaskLifecycle, func(evt *QueryEvent) {
		evt.TaskLifecycle = &TaskLifecycleEvent{
			Phase:      "updated",
			TaskID:     "task-running",
			Subject:    "Running task",
			ActiveForm: "Running focused tests",
			Status:     "in_progress",
			UpdatedAt:  evt.Timestamp,
		}
	})
	if err := store.Apply(running); err != nil {
		t.Fatal(err)
	}

	snapshot := store.TaskExplorerSnapshot("thread-1").Runtime
	if len(snapshot.Tasks) != 1 {
		t.Fatalf("bounded tasks = %#v, want one active task", snapshot.Tasks)
	}
	task, ok := snapshot.Tasks["task-running"]
	if !ok || task.Status != "in_progress" || task.ActiveForm != "Running focused tests" {
		t.Fatalf("unexpected canonical task projection: %#v", task)
	}
	if _, ok := snapshot.Tasks["task-completed"]; ok {
		t.Fatal("oldest terminal task was not evicted before active task")
	}

	task.Subject = "mutated"
	snapshot.Tasks["task-running"] = task
	if got := store.TaskExplorerSnapshot("thread-1").Runtime.Tasks["task-running"].Subject; got != "Running task" {
		t.Fatalf("task snapshot mutation leaked into store: %q", got)
	}
}

func TestRuntimeStateStoreAgentLifecyclePrecedesTurnsAndSupportsResume(t *testing.T) {
	store := NewRuntimeStateStore()
	launch := runtimeTestEvent(1, "agent-launch:agent-1:1", EventAgentLifecycle, func(evt *QueryEvent) {
		evt.AgentLifecycle = &AgentLifecycleEvent{
			Phase:          "launched",
			Name:           "explorer",
			Task:           "inspect runtime",
			Description:    "Inspecting runtime",
			AgentType:      "Explore",
			Model:          "small-model",
			PermissionMode: "plan",
			Isolation:      "worktree",
			CWD:            "/repo",
			WorktreePath:   "/repo/.worktrees/agent-1",
			WorktreeBranch: "agent/agent-1",
			TranscriptPath: "/state/transcripts/session-1.jsonl",
			OutputFile:     "/state/agent-1.out",
			Status:         "running",
			Generation:     1,
			StartedAt:      evt.Timestamp,
		}
	})
	firstTurn := runtimeTestEvent(2, "turn-1", EventStreamRequestStart, nil)
	terminal := runtimeTestEvent(3, "turn-1", EventTerminal, func(evt *QueryEvent) {
		evt.TerminalInfo = &Terminal{Reason: TerminalCompleted}
	})
	resume := runtimeTestEvent(4, "agent-launch:agent-1:2", EventAgentLifecycle, func(evt *QueryEvent) {
		evt.AgentLifecycle = &AgentLifecycleEvent{
			Phase:       "resumed",
			Name:        "explorer",
			Task:        "inspect runtime again",
			Description: "Inspecting resumed runtime",
			Status:      "running",
			Generation:  2,
			StartedAt:   evt.Timestamp,
		}
	})
	secondTurn := runtimeTestEvent(5, "turn-2", EventStreamRequestStart, nil)
	if err := store.Replay([]QueryEvent{launch, firstTurn, terminal, resume, secondTurn}); err != nil {
		t.Fatalf("replay launch/terminal/resume lifecycle: %v", err)
	}

	snapshot := store.TaskExplorerSnapshot("thread-1").Runtime
	thread := snapshot.Threads["thread-1"]
	if thread.Status != RuntimeThreadRunning || thread.ActiveTurnID != "turn-2" || thread.LastSequence != 5 {
		t.Fatalf("resumed thread state = %#v", thread)
	}
	agent := snapshot.Agents["agent-1"]
	if agent.Status != "running" || agent.Task != "inspect runtime again" || agent.Description != "Inspecting resumed runtime" || !agent.CompletedAt.IsZero() {
		t.Fatalf("resumed Agent state = %#v", agent)
	}
	if agent.Name != "explorer" || agent.Model != "small-model" || agent.TranscriptPath != "/state/transcripts/session-1.jsonl" {
		t.Fatalf("resume discarded launch metadata: %#v", agent)
	}
	fullThread := store.Snapshot("thread-1").Threads["thread-1"]
	for i, event := range fullThread.Events {
		if event.Sequence != uint64(i+1) {
			t.Fatalf("event sequence[%d] = %d, want %d", i, event.Sequence, i+1)
		}
	}
	agentDetail, detailThread, revision, ok := store.AgentThreadSnapshot("agent-1")
	if !ok || revision != snapshot.Revision || agentDetail.Task != agent.Task || detailThread.LastSequence != thread.LastSequence || len(detailThread.Events) != len(fullThread.Events) {
		t.Fatalf("narrow Agent/thread snapshot mismatch: agent=%#v thread=%#v revision=%d found=%v", agentDetail, detailThread, revision, ok)
	}
	detailThread.Events[0].Summary = "mutated"
	_, afterMutation, _, _ := store.AgentThreadSnapshot("agent-1")
	if afterMutation.Events[0].Summary == "mutated" {
		t.Fatal("Agent/thread detail snapshot mutation leaked into runtime store")
	}
}

func TestRuntimeStateStoreRetainsImmutableExecutionGenerations(t *testing.T) {
	events := []QueryEvent{
		runtimeTestEvent(1, "agent-launch:agent-1:1", EventAgentLifecycle, func(evt *QueryEvent) {
			evt.AgentGeneration = 1
			evt.AgentLifecycle = &AgentLifecycleEvent{
				Phase:      "launched",
				Status:     "running",
				Generation: 1,
				StartedAt:  evt.Timestamp,
			}
		}),
		runtimeTestEvent(2, "turn-1", EventTerminal, func(evt *QueryEvent) {
			evt.AgentGeneration = 1
			evt.TerminalInfo = &Terminal{Reason: TerminalCompleted}
		}),
		runtimeTestEvent(3, "agent-launch:agent-1:2", EventAgentLifecycle, func(evt *QueryEvent) {
			evt.AgentGeneration = 2
			evt.AgentLifecycle = &AgentLifecycleEvent{
				Phase:       "resumed",
				Status:      "running",
				Generation:  2,
				Description: "second generation",
				StartedAt:   evt.Timestamp,
			}
		}),
	}
	left := NewRuntimeStateStore()
	right := NewRuntimeStateStore()
	if err := left.Replay(events); err != nil {
		t.Fatal(err)
	}
	if err := right.Replay(events); err != nil {
		t.Fatal(err)
	}
	got := left.TaskExplorerSnapshot("thread-1")
	want := right.TaskExplorerSnapshot("thread-1")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("execution replay differs:\ngot=%#v\nwant=%#v", got, want)
	}
	if len(got.Executions) != 2 {
		t.Fatalf("execution generations = %#v, want two", got.Executions)
	}
	first := got.Executions[RuntimeExecutionKey{AgentID: "agent-1", Generation: 1}]
	second := got.Executions[RuntimeExecutionKey{AgentID: "agent-1", Generation: 2}]
	if first.Agent.Status != string(RuntimeThreadCompleted) ||
		second.Agent.Description != "second generation" ||
		!first.OrdinalPresent || !second.OrdinalPresent ||
		first.ObservationOrdinal >= second.ObservationOrdinal {
		t.Fatalf("unexpected immutable generations: first=%#v second=%#v", first, second)
	}

	first.Agent.Description = "mutated"
	got.Executions[first.Key] = first
	after := left.TaskExplorerSnapshot("thread-1")
	if after.Executions[first.Key].Agent.Description == "mutated" {
		t.Fatal("execution snapshot mutation leaked into runtime store")
	}
}

func TestRuntimeStateStoreExecutionOverflowDoesNotRejectLiveAgent(t *testing.T) {
	store := NewRuntimeStateStore(RuntimeStoreLimits{Threads: 256, Agents: 128})
	for index := 0; index < 129; index++ {
		agentID := fmt.Sprintf("agent-%03d", index)
		event := runtimeTestEvent(1, "launch:"+agentID, EventAgentLifecycle, func(evt *QueryEvent) {
			evt.AgentID = agentID
			evt.ThreadID = "thread-" + agentID
			evt.AgentGeneration = 1
			evt.AgentLifecycle = &AgentLifecycleEvent{
				Phase:      "launched",
				Status:     "running",
				Generation: 1,
				StartedAt:  evt.Timestamp,
			}
		})
		if err := store.Apply(event); err != nil {
			t.Fatalf("live execution %d was rejected: %v", index, err)
		}
	}
	snapshot := store.TaskExplorerSnapshot("thread-agent-128")
	if len(snapshot.Executions) != 128 ||
		snapshot.HiddenLiveExecutions != 1 ||
		snapshot.EvictedExecutionGenerations != 1 {
		t.Fatalf(
			"execution overflow = rows %d hidden-live %d evicted %d, want 128/1/1",
			len(snapshot.Executions),
			snapshot.HiddenLiveExecutions,
			snapshot.EvictedExecutionGenerations,
		)
	}
	if _, exists := snapshot.Executions[RuntimeExecutionKey{
		AgentID: "agent-000", Generation: 1,
	}]; exists {
		t.Fatal("oldest live execution remained after deterministic overflow")
	}
	if _, exists := snapshot.Executions[RuntimeExecutionKey{
		AgentID: "agent-128", Generation: 1,
	}]; !exists {
		t.Fatal("129th admitted live execution missing from retained projection")
	}

	progress := runtimeTestEvent(2, "turn-agent-000", EventTaskProgress, func(evt *QueryEvent) {
		evt.AgentID = "agent-000"
		evt.ThreadID = "thread-agent-000"
		evt.AgentGeneration = 1
		evt.TaskProgress = &TaskProgressEvent{Summary: "still running"}
	})
	if err := store.Apply(progress); err != nil {
		t.Fatalf("update hidden live execution: %v", err)
	}
	afterUpdate := store.TaskExplorerSnapshot("thread-agent-000")
	if got := afterUpdate.HiddenLiveExecutions; got != 1 {
		t.Fatalf("hidden live executions after re-observation = %d, want 1", got)
	}
	hiddenConflict := RuntimeAgentSnapshot{
		AgentID:        "agent-001",
		Generation:     1,
		SessionID:      "session-1",
		ThreadID:       "thread-agent-001",
		ParentThreadID: "conflicting-parent",
		Status:         "running",
		UpdatedAt:      time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC),
	}
	if err := store.RestoreAgentSnapshot(hiddenConflict, nil, false); err == nil ||
		!strings.Contains(err.Error(), "immutable lineage") {
		t.Fatalf("hidden execution lineage conflict = %v", err)
	}
	if got := store.TaskExplorerSnapshot("thread-agent-000").HiddenLiveExecutions; got != 1 {
		t.Fatalf("hidden conflict changed hidden count to %d", got)
	}
}

func TestRuntimeStateStoreRejectsRestoredExecutionLineageConflictWithoutMutation(t *testing.T) {
	store := NewRuntimeStateStore()
	agent := RuntimeAgentSnapshot{
		AgentID:         "agent-restore",
		Generation:      3,
		SessionID:       "child-session",
		ThreadID:        "child-thread",
		ParentSessionID: "root-session",
		ParentThreadID:  "root-thread",
		Status:          "completed",
		CompletedAt:     time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
	}
	if err := store.RestoreAgentSnapshot(agent, nil, false); err != nil {
		t.Fatal(err)
	}
	before := store.TaskExplorerSnapshot("root-thread")
	conflict := agent
	conflict.ParentThreadID = "different-parent"
	if err := store.RestoreAgentSnapshot(conflict, nil, false); err == nil ||
		!strings.Contains(err.Error(), "immutable lineage") {
		t.Fatalf("restore conflict error = %v, want immutable lineage", err)
	}
	after := store.TaskExplorerSnapshot("root-thread")
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected restore changed execution projection:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestRuntimeStateStorePauseLifecyclePreservesActiveTurn(t *testing.T) {
	store := NewRuntimeStateStore()
	launch := runtimeTestEvent(1, "agent-launch:agent-1:1", EventAgentLifecycle, func(evt *QueryEvent) {
		evt.AgentLifecycle = &AgentLifecycleEvent{Phase: "launched", Status: "running", Generation: 1, StartedAt: evt.Timestamp}
	})
	stream := runtimeTestEvent(2, "turn-1", EventStreamRequestStart, nil)
	paused := runtimeTestEvent(3, "turn-1", EventAgentLifecycle, func(evt *QueryEvent) {
		evt.AgentLifecycle = &AgentLifecycleEvent{Phase: "paused", Status: "paused"}
	})
	if err := store.Replay([]QueryEvent{launch, stream, paused}); err != nil {
		t.Fatalf("reduce pause lifecycle: %v", err)
	}
	snapshot := store.Snapshot("thread-1")
	thread := snapshot.Threads["thread-1"]
	agent := snapshot.Agents["agent-1"]
	if thread.ActiveTurnID != "turn-1" || thread.Status != RuntimeThreadPaused || !thread.CompletedAt.IsZero() {
		t.Fatalf("paused thread lost active turn: %#v", thread)
	}
	if agent.Status != "paused" || !agent.CompletedAt.IsZero() {
		t.Fatalf("paused Agent looked terminal: %#v", agent)
	}

	resumed := runtimeTestEvent(4, "turn-1", EventAgentLifecycle, func(evt *QueryEvent) {
		evt.AgentLifecycle = &AgentLifecycleEvent{Phase: "resumed_control", Status: "running"}
	})
	if err := store.Apply(resumed); err != nil {
		t.Fatalf("reduce control resume: %v", err)
	}
	snapshot = store.Snapshot("thread-1")
	thread = snapshot.Threads["thread-1"]
	agent = snapshot.Agents["agent-1"]
	if thread.ActiveTurnID != "turn-1" || thread.Status != RuntimeThreadRunning || agent.Status != "running" || !agent.CompletedAt.IsZero() {
		t.Fatalf("resumed control state did not converge: thread=%#v agent=%#v", thread, agent)
	}
	if got := []string{thread.Events[2].Summary, thread.Events[3].Summary}; !reflect.DeepEqual(got, []string{"paused", "resumed_control"}) {
		t.Fatalf("pause lifecycle summaries = %#v", got)
	}
}

func runtimeTestEvent(sequence uint64, turnID string, eventType QueryEventType, mutate func(*QueryEvent)) QueryEvent {
	evt := QueryEvent{
		RuntimeEventEnvelope: RuntimeEventEnvelope{
			SessionID:       "session-1",
			ThreadID:        "thread-1",
			TurnID:          turnID,
			AgentID:         "agent-1",
			ParentSessionID: "parent-session",
			ParentThreadID:  "parent-thread",
			ParentAgentID:   "parent-agent",
			ParentToolUseID: "spawn-call",
			Sequence:        sequence,
			Timestamp:       time.Date(2026, 7, 10, 12, 0, int(sequence), 0, time.UTC),
		},
		Type: eventType,
	}
	if mutate != nil {
		mutate(&evt)
	}
	return evt
}

func TestP172RuntimePlanApprovalRequiresExactDurableIdentity(t *testing.T) {
	store := NewRuntimeStateStore()
	digest := PlanBytesDigest([]byte("# Plan"))
	event := runtimeTestEvent(
		1,
		"turn-plan",
		EventPermissionRequest,
		func(evt *QueryEvent) {
			evt.PermissionRequest = &PermissionRequestEvent{
				ToolName:  "ExitPlanMode",
				ToolUseID: "exit-1",
				Kind:      "plan_approval",
				PlanApproval: &PlanApprovalRequest{
					RequestID:         "exit-1",
					PlanRevision:      4,
					PlanFileIdentity:  "/tmp/.claude/plans/session-1-agent-agent-1.md",
					InitialPlanDigest: digest,
					ReturnMode:        permission.ModeAcceptEdits,
				},
			}
		},
	)
	if err := store.Apply(event); err != nil {
		t.Fatal(err)
	}
	record := &session.PersistedPlanState{
		Version:               session.PersistedPlanStateVersion,
		Phase:                 string(PlanPhaseAwaitingApproval),
		PlanFileIdentity:      "/tmp/.claude/plans/session-1-agent-agent-1.md",
		ReturnMode:            string(permission.ModeAcceptEdits),
		ApprovalRequestID:     "exit-1",
		ApprovalInitialDigest: digest,
		Revision:              4,
	}
	if !store.HasLivePlanApproval("session-1", "thread-1", record) {
		t.Fatal("exact live Plan approval did not match")
	}
	record.Revision++
	if store.HasLivePlanApproval("session-1", "thread-1", record) {
		t.Fatal("revision-mismatched Plan approval matched")
	}
	record.Revision--
	record.ApprovalInitialDigest = PlanBytesDigest([]byte("# Other Plan"))
	if store.HasLivePlanApproval("session-1", "thread-1", record) {
		t.Fatal("digest-mismatched Plan approval matched")
	}
	record.ApprovalInitialDigest = digest
	record.PlanFileIdentity += ".stale"
	if store.HasLivePlanApproval("session-1", "thread-1", record) {
		t.Fatal("file-mismatched Plan approval matched")
	}
}
