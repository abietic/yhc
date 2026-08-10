package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/tools"
)

func TestQueryEngineEmitsStableRuntimeIdentityAcrossTurns(t *testing.T) {
	fixed := time.Date(2026, 7, 10, 14, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID: "leader-session",
		CWD:       t.TempDir(),
		Clock:     func() time.Time { return fixed },
	})
	defer eng.Close()

	first := drainRuntimeEvents(t, eng, "/clear")
	second := drainRuntimeEvents(t, eng, "/clear")
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("command event counts = %d/%d, want 2/2", len(first), len(second))
	}
	if first[0].SessionID != "leader-session" || first[0].ThreadID != "leader-session" || first[0].AgentID != "" {
		t.Fatalf("leader identity = %#v", first[0].RuntimeEventEnvelope)
	}
	if first[0].TurnID == "" || second[0].TurnID == "" || first[0].TurnID == second[0].TurnID {
		t.Fatalf("turn identities = %q/%q, want distinct non-empty values", first[0].TurnID, second[0].TurnID)
	}
	if first[0].Sequence != 1 || first[1].Sequence != 2 ||
		second[0].Sequence != 3 || second[1].Sequence != 4 {
		t.Fatalf("sequences = %d,%d/%d,%d, want 1,2/3,4", first[0].Sequence, first[1].Sequence, second[0].Sequence, second[1].Sequence)
	}
	if !first[0].Timestamp.Equal(fixed.UTC()) || !second[0].Timestamp.Equal(fixed.UTC()) {
		t.Fatalf("timestamps = %v/%v, want %v", first[0].Timestamp, second[0].Timestamp, fixed.UTC())
	}
	if first[0].Type != EventCommandResult || first[1].Type != EventTerminal ||
		second[0].Type != EventCommandResult || second[1].Type != EventTerminal {
		t.Fatalf("command event order = %q,%q/%q,%q", first[0].Type, first[1].Type, second[0].Type, second[1].Type)
	}
	if err := eng.RuntimeStateError(); err != nil {
		t.Fatalf("engine runtime reducer rejected generated events: %v", err)
	}
	snapshot := eng.RuntimeSnapshot()
	thread, ok := snapshot.Threads["leader-session"]
	if !ok {
		t.Fatalf("leader thread missing from snapshot: %#v", snapshot)
	}
	if snapshot.ActiveThreadID != "leader-session" || snapshot.SessionID != "leader-session" || snapshot.Revision != 4 {
		t.Fatalf("snapshot identity/revision = %#v", snapshot)
	}
	if thread.Status != RuntimeThreadCompleted || thread.ActiveTurnID != second[0].TurnID || thread.LastSequence != 4 {
		t.Fatalf("leader thread state = %#v", thread)
	}
}

func TestQueryEngineSharesChildRuntimeSnapshotWithLineage(t *testing.T) {
	store := NewRuntimeStateStore()
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:       "child-session",
		ThreadID:        "child-thread",
		AgentID:         "child-agent",
		ParentSessionID: "leader-session",
		ParentThreadID:  "leader-thread",
		ParentAgentID:   "parent-agent",
		ParentToolUseID: "spawn-call",
		RuntimeState:    store,
		CWD:             t.TempDir(),
	})
	defer eng.Close()

	events := drainRuntimeEvents(t, eng, "/clear")
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2", len(events))
	}
	evt := events[1]
	if evt.SessionID != "child-session" || evt.ThreadID != "child-thread" || evt.AgentID != "child-agent" ||
		evt.ParentSessionID != "leader-session" || evt.ParentThreadID != "leader-thread" ||
		evt.ParentAgentID != "parent-agent" || evt.ParentToolUseID != "spawn-call" || evt.CausationID != "spawn-call" {
		t.Fatalf("child event lineage = %#v", evt.RuntimeEventEnvelope)
	}
	snapshot := store.Snapshot("leader-thread")
	child, ok := snapshot.Threads["child-thread"]
	if !ok {
		t.Fatalf("shared store missing child thread: %#v", snapshot.Threads)
	}
	if child.AgentID != "child-agent" || child.ParentThreadID != "leader-thread" || child.ParentToolUseID != "spawn-call" {
		t.Fatalf("child snapshot lineage = %#v", child)
	}
	agent, ok := snapshot.Agents["child-agent"]
	if !ok || agent.ThreadID != "child-thread" || agent.Status != string(RuntimeThreadCompleted) {
		t.Fatalf("child Agent projection = %#v, ok=%v", agent, ok)
	}
}

func TestTurnEmitterDoesNotPublishReducerRejectedEvent(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "reducer-rejection",
		CWD:           t.TempDir(),
		TranscriptDir: t.TempDir(),
	})
	t.Cleanup(eng.Close)

	firstEvents := make(chan QueryEvent, 4)
	first := newTurnEventEmitter(
		context.Background(),
		eng,
		firstEvents,
		"turn-one",
	)
	if !first.Emit(QueryEvent{
		Type: EventCommandResult,
		CommandResult: &CommandResultEvent{
			Command: "clear",
			Action:  commands.ActionClear,
			Status:  CommandResultSucceeded,
		},
	}) {
		t.Fatal("first turn command result was rejected")
	}
	var progressObservations atomic.Int32
	tracker := eng.configureSubagentProgress(
		nil,
		"rejected progress",
		"parent-tool",
		func(tools.AgentProgress) {
			progressObservations.Add(1)
		},
	)

	rejectedEvents := make(chan QueryEvent, 1)
	rejected := newTurnEventEmitter(
		context.Background(),
		eng,
		rejectedEvents,
		"turn-two",
	)
	if rejected.Emit(QueryEvent{
		Type: EventAssistant,
		AssistantMessage: &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID: "rejected-tool",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path":"rejected.txt"}`,
				},
			}},
		},
	}) {
		t.Fatal("concurrent turn event was accepted")
	}
	if len(rejectedEvents) != 0 {
		t.Fatalf("reducer-rejected event was published: %#v", <-rejectedEvents)
	}
	if progressObservations.Load() != 0 ||
		tracker.Progress().ToolUseCount != 0 ||
		tracker.HasProgress() {
		t.Fatalf(
			"reducer-rejected event mutated derived progress: observations=%d progress=%#v",
			progressObservations.Load(),
			tracker.Progress(),
		)
	}

	if !first.Emit(QueryEvent{
		Type:         EventTerminal,
		TerminalInfo: &Terminal{Reason: TerminalCompleted},
	}) {
		t.Fatal("owning turn terminal was rejected")
	}
	firstResult := <-firstEvents
	firstTerminal := <-firstEvents
	if firstResult.Sequence != 1 ||
		firstTerminal.Sequence != 2 ||
		firstTerminal.Type != EventTerminal {
		t.Fatalf(
			"accepted events after rejection = %#v/%#v",
			firstResult,
			firstTerminal,
		)
	}
	if got := eng.runtimeState.LastSequence("reducer-rejection"); got != 2 {
		t.Fatalf("runtime sequence after rejected event = %d, want 2", got)
	}
	if eng.RuntimeStateError() == nil {
		t.Fatal("reducer rejection was not retained for diagnostics")
	}
}

func drainRuntimeEvents(t *testing.T, eng *QueryEngine, prompt string) []QueryEvent {
	t.Helper()
	events, _ := eng.SubmitMessage(context.Background(), prompt)
	out := make([]QueryEvent, 0)
	for evt := range events {
		out = append(out, evt)
	}
	return out
}
