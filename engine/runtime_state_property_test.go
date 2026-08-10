package engine

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestRuntimeStateStoreReplayProperties(t *testing.T) {
	const seeds = 128
	for seed := int64(0); seed < seeds; seed++ {
		t.Run(fmt.Sprintf("seed_%03d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			limits := RuntimeStoreLimits{
				EventsPerThread:       4 + rng.Intn(12),
				MessagesPerThread:     2 + rng.Intn(6),
				ToolsPerThread:        2 + rng.Intn(5),
				InteractionsPerThread: 2,
				Threads:               2,
				Agents:                3 + rng.Intn(4),
				Tasks:                 2 + rng.Intn(4),
			}
			events := runtimePropertyEvents(rng)

			incremental := NewRuntimeStateStore(limits)
			for i, event := range events {
				if err := incremental.Apply(event); err != nil {
					t.Fatalf("seed %d apply event %d (%s): %v", seed, i, event.Type, err)
				}
			}
			replayed := NewRuntimeStateStore(limits)
			if err := replayed.Replay(events); err != nil {
				t.Fatalf("seed %d replay: %v", seed, err)
			}

			got := incremental.Snapshot("thread-1")
			want := replayed.Snapshot("thread-1")
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("seed %d incremental/replay snapshots differ:\ngot=%#v\nwant=%#v", seed, got, want)
			}
			assertRuntimePropertyBounds(t, seed, limits, got)

			before := incremental.Snapshot("thread-1")
			invalid := events[len(events)-1]
			invalid.Sequence += uint64(2 + rng.Intn(4))
			if err := incremental.Apply(invalid); err == nil {
				t.Fatalf("seed %d accepted a sequence gap", seed)
			}
			if after := incremental.Snapshot("thread-1"); !reflect.DeepEqual(after, before) {
				t.Fatalf("seed %d rejected event mutated the store", seed)
			}
		})
	}
}

func runtimePropertyEvents(rng *rand.Rand) []QueryEvent {
	var events []QueryEvent
	sequence := uint64(0)
	next := func(turnID string, eventType QueryEventType, mutate func(*QueryEvent)) {
		sequence++
		events = append(events, runtimeTestEvent(sequence, turnID, eventType, mutate))
	}

	turns := 2 + rng.Intn(6)
	for turn := 0; turn < turns; turn++ {
		turnID := fmt.Sprintf("turn-%d", turn+1)
		phase := "launched"
		if turn > 0 {
			phase = "resumed"
		}
		next(fmt.Sprintf("agent-launch:agent-1:%d", turn+1), EventAgentLifecycle, func(evt *QueryEvent) {
			evt.AgentLifecycle = &AgentLifecycleEvent{
				Phase:       phase,
				Name:        "property-agent",
				Task:        fmt.Sprintf("property turn %d", turn+1),
				Description: "replay property sequence",
				Status:      "running",
				Generation:  int64(turn + 1),
				StartedAt:   evt.Timestamp,
			}
		})
		next(turnID, EventStreamRequestStart, nil)

		operations := 1 + rng.Intn(8)
		for operation := 0; operation < operations; operation++ {
			switch rng.Intn(6) {
			case 0:
				next(turnID, EventStream, func(evt *QueryEvent) {
					evt.StreamEvent = &schema.Message{Role: schema.Assistant, Content: fmt.Sprintf("delta-%d ", operation)}
				})
			case 1:
				toolID := fmt.Sprintf("tool-%d-%d", turn, operation)
				next(turnID, EventAssistant, func(evt *QueryEvent) {
					evt.AssistantMessage = &schema.Message{Role: schema.Assistant, Content: "calling tool", ToolCalls: []schema.ToolCall{{
						ID: toolID,
						Function: schema.FunctionCall{
							Name:      "Read",
							Arguments: fmt.Sprintf(`{"file_path":"file-%d.go"}`, operation),
						},
					}}}
				})
				next(turnID, EventToolProgress, func(evt *QueryEvent) {
					evt.ToolProgress = &ToolProgressEvent{ToolName: "Read", ToolUseID: toolID, Content: "reading"}
				})
				next(turnID, EventToolResult, func(evt *QueryEvent) {
					evt.ToolResultMessage = &schema.Message{Role: schema.Tool, ToolName: "Read", ToolCallID: toolID, Content: "contents"}
				})
			case 2:
				requestID := fmt.Sprintf("permission-%d-%d", turn, operation)
				next(turnID, EventPermissionRequest, func(evt *QueryEvent) {
					evt.PermissionRequest = &PermissionRequestEvent{
						ToolName:  "Bash",
						ToolUseID: requestID,
						Input:     map[string]any{"command": fmt.Sprintf("go test ./pkg%d", operation)},
						Message:   "run focused tests",
					}
				})
				next(turnID, EventPermissionResolved, func(evt *QueryEvent) {
					evt.PermissionResolved = &PermissionResolvedEvent{ToolUseID: requestID, Decision: "allow"}
				})
			case 3:
				next(turnID, EventTaskProgress, func(evt *QueryEvent) {
					evt.TaskProgress = &TaskProgressEvent{
						TaskID:       fmt.Sprintf("child-%d", operation%4),
						ToolUseID:    fmt.Sprintf("spawn-%d", operation),
						Description:  "checking repository",
						LastToolName: "Grep",
						Summary:      fmt.Sprintf("checked %d paths", operation+1),
						Usage:        TaskProgressUsage{ToolUses: operation + 1, TotalTokens: 100 * (operation + 1)},
						RecentActivities: []TaskProgressActivity{{
							ToolName: "Grep", Description: "searching", IsSearch: true,
						}},
					}
				})
			case 4:
				next(turnID, EventTaskLifecycle, func(evt *QueryEvent) {
					evt.TaskLifecycle = &TaskLifecycleEvent{
						Phase:       "updated",
						TaskID:      fmt.Sprintf("task-%d", operation%5),
						Subject:     fmt.Sprintf("Task %d", operation),
						Description: "property task",
						Status:      []string{"pending", "in_progress", "completed"}[rng.Intn(3)],
						UpdatedAt:   evt.Timestamp,
					}
				})
			default:
				next(turnID, EventAssistant, func(evt *QueryEvent) {
					evt.AssistantMessage = &schema.Message{Role: schema.Assistant, Content: fmt.Sprintf("answer-%d", operation)}
				})
			}
		}

		next(turnID, EventTerminal, func(evt *QueryEvent) {
			evt.TerminalInfo = &Terminal{Reason: TerminalCompleted, TurnCount: turn + 1}
		})
	}
	return events
}

func assertRuntimePropertyBounds(t *testing.T, seed int64, limits RuntimeStoreLimits, snapshot RuntimeSnapshot) {
	t.Helper()
	thread, ok := snapshot.Threads["thread-1"]
	if !ok {
		t.Fatalf("seed %d lost active thread", seed)
	}
	if len(thread.Events) > limits.EventsPerThread || len(thread.Messages) > limits.MessagesPerThread || len(thread.Tools) > limits.ToolsPerThread {
		t.Fatalf("seed %d exceeded bounds: events=%d/%d messages=%d/%d tools=%d/%d", seed,
			len(thread.Events), limits.EventsPerThread, len(thread.Messages), limits.MessagesPerThread, len(thread.Tools), limits.ToolsPerThread)
	}
	if len(snapshot.Agents) > limits.Agents || len(snapshot.Tasks) > limits.Tasks {
		t.Fatalf("seed %d exceeded global bounds: agents=%d/%d tasks=%d/%d", seed,
			len(snapshot.Agents), limits.Agents, len(snapshot.Tasks), limits.Tasks)
	}
	if len(thread.PendingInteractions) != 0 || snapshot.UnresolvedCount != 0 {
		t.Fatalf("seed %d replay resurrected resolved interactions: thread=%d snapshot=%d", seed, len(thread.PendingInteractions), snapshot.UnresolvedCount)
	}
	if thread.LastTerminal == nil || thread.LastTerminal.Reason != TerminalCompleted || thread.LastTerminal.Timestamp.IsZero() {
		t.Fatalf("seed %d terminal identity incomplete: %#v", seed, thread.LastTerminal)
	}
	for i := 1; i < len(thread.Events); i++ {
		if thread.Events[i].Sequence != thread.Events[i-1].Sequence+1 {
			t.Fatalf("seed %d retained event sequence is not contiguous at %d: %d -> %d", seed, i, thread.Events[i-1].Sequence, thread.Events[i].Sequence)
		}
	}
}
