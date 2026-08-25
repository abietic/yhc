package engine

import (
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestThreadCatalogClassifiesLiveReplayQuestionAndEvictedModes(t *testing.T) {
	store := NewRuntimeStateStore(RuntimeStoreLimits{Threads: 2, Agents: 4})
	apply := func(evt QueryEvent) {
		t.Helper()
		if err := store.Apply(evt); err != nil {
			t.Fatal(err)
		}
	}
	apply(threadCatalogEvent("leader-thread", "leader-turn", 1, EventStreamRequestStart, nil))
	apply(threadCatalogEvent("leader-thread", "leader-turn", 2, EventStream, func(evt *QueryEvent) {
		evt.StreamEvent = &schema.Message{Role: schema.Assistant, Content: "partial response"}
	}))
	apply(threadCatalogEvent("leader-thread", "leader-turn", 3, EventPermissionRequest, func(evt *QueryEvent) {
		evt.PermissionRequest = &PermissionRequestEvent{
			ToolUseID: "question-1", ToolName: "AskUserQuestion",
			Kind: PermissionInteractionKindQuestion, Message: "choose a path",
		}
	}))

	apply(threadCatalogEvent("child-thread", "agent-launch:agent-1:1", 1, EventAgentLifecycle, func(evt *QueryEvent) {
		evt.AgentID = "agent-1"
		evt.ParentThreadID = "leader-thread"
		evt.ParentToolUseID = "spawn-agent"
		evt.AgentLifecycle = &AgentLifecycleEvent{
			Phase: "launched", Status: "running", Name: "runtime scout", Description: "inspect runtime", AgentType: "Explore",
			TranscriptPath: "/tmp/agent-1.jsonl", StartedAt: evt.Timestamp,
		}
	}))
	apply(threadCatalogEvent("child-thread", "child-turn", 2, EventStreamRequestStart, func(evt *QueryEvent) {
		evt.AgentID = "agent-1"
		evt.ParentThreadID = "leader-thread"
		evt.ParentToolUseID = "spawn-agent"
	}))
	apply(threadCatalogEvent("child-thread", "child-turn", 3, EventTerminal, func(evt *QueryEvent) {
		evt.AgentID = "agent-1"
		evt.ParentThreadID = "leader-thread"
		evt.ParentToolUseID = "spawn-agent"
		evt.TerminalInfo = &Terminal{Reason: TerminalCompleted}
	}))

	catalog := store.ThreadCatalogSnapshot("leader-thread")
	if len(catalog.Threads) != 2 || !catalog.Threads[0].IsActive || catalog.Threads[0].Mode != ThreadModeLiveAttach {
		t.Fatalf("live catalog = %#v", catalog)
	}
	if catalog.Threads[0].QuestionCount != 1 || catalog.Threads[0].PermissionCount != 0 || catalog.Threads[0].MessageCount != 0 {
		t.Fatalf("question interaction was not independent from history: %#v", catalog.Threads[0])
	}
	if !catalog.Threads[0].HasLiveMessage || catalog.Threads[0].ActiveTurnID != "leader-turn" || catalog.Threads[0].EventCount != 3 || catalog.Threads[0].LastSequence != 3 {
		t.Fatalf("live tail/turn catalog fields = %#v", catalog.Threads[0])
	}
	child := catalog.Threads[1]
	if child.ThreadID != "child-thread" || child.Mode != ThreadModeReplayOnly || child.TranscriptPath != "/tmp/agent-1.jsonl" || child.ActiveTurnID != "child-turn" ||
		child.Name != "runtime scout" || child.Description != "inspect runtime" || child.AgentType != "Explore" {
		t.Fatalf("replay-only child = %#v", child)
	}

	apply(threadCatalogEvent("other-thread", "other-turn", 1, EventStreamRequestStart, nil))
	catalog = store.ThreadCatalogSnapshot("leader-thread")
	modes := make(map[string]ThreadAttachmentMode, len(catalog.Threads))
	for _, thread := range catalog.Threads {
		modes[thread.ThreadID] = thread.Mode
	}
	if modes["leader-thread"] != ThreadModeLiveAttach || modes["other-thread"] != ThreadModeLiveAttach || modes["child-thread"] != ThreadModeEvictedTranscript {
		t.Fatalf("catalog modes after bounded eviction = %#v", modes)
	}
}

func TestThreadCatalogKeepsTerminalAttentionLiveAttach(t *testing.T) {
	store := NewRuntimeStateStore(RuntimeStoreLimits{})
	apply := func(evt QueryEvent) {
		t.Helper()
		if err := store.Apply(evt); err != nil {
			t.Fatal(err)
		}
	}

	apply(threadCatalogEvent("attention-thread", "turn-1", 1, EventStreamRequestStart, nil))
	apply(threadCatalogEvent("attention-thread", "turn-1", 2, EventPermissionRequest, func(evt *QueryEvent) {
		evt.PermissionRequest = &PermissionRequestEvent{
			ToolUseID: "question-1", ToolName: "AskUserQuestion",
			Kind: PermissionInteractionKindQuestion, Message: "choose a path",
		}
	}))
	apply(threadCatalogEvent("attention-thread", "turn-1", 3, EventTerminal, func(evt *QueryEvent) {
		evt.TerminalInfo = &Terminal{Reason: TerminalCompleted}
	}))

	catalog := store.ThreadCatalogSnapshot("")
	if len(catalog.Threads) != 1 {
		t.Fatalf("catalog = %#v", catalog)
	}
	thread := catalog.Threads[0]
	if thread.Mode != ThreadModeLiveAttach || thread.QuestionCount != 1 || thread.PermissionCount != 0 {
		t.Fatalf("terminal attention thread = %#v", thread)
	}
}

func threadCatalogEvent(threadID, turnID string, sequence uint64, eventType QueryEventType, mutate func(*QueryEvent)) QueryEvent {
	evt := QueryEvent{
		RuntimeEventEnvelope: RuntimeEventEnvelope{
			SessionID: "session-1", ThreadID: threadID, TurnID: turnID, Sequence: sequence,
			Timestamp: time.Date(2026, 7, 10, 13, 0, int(sequence), 0, time.UTC),
		},
		Type: eventType,
	}
	if mutate != nil {
		mutate(&evt)
	}
	return evt
}
