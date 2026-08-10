package engine

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/transcript"
	"github.com/cloudwego/eino/schema"
)

func TestAgentTranscriptPageBoundsExactGenerationAndMergesOnlyDurableIdentity(t *testing.T) {
	recorder := transcript.NewRecorder("child-session", t.TempDir())
	messages := []*schema.Message{
		{Role: schema.User, Content: "task"},
		{Role: schema.Assistant, Content: "same"},
		{Role: schema.Assistant, Content: "same"},
		{Role: schema.Tool, ToolCallID: "call-1", ToolName: "Read", Content: "result"},
	}
	if err := recorder.RecordMessages(messages); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	exact, ok := recorder.LatestMessageEntryIdentity(messages[2])
	if !ok {
		t.Fatal("durable message identity was not tracked")
	}

	store := NewRuntimeStateStore()
	launch := agentTranscriptTestEvent("agent-1", "child-session", "child-thread", 1, EventAgentLifecycle)
	launch.AgentLifecycle = &AgentLifecycleEvent{
		Status: "running", Generation: 1, TranscriptPath: recorder.Path(), StartedAt: launch.Timestamp,
	}
	live := agentTranscriptTestEvent("agent-1", "child-session", "child-thread", 2, EventAssistant)
	live.TranscriptEntryID = exact.Key()
	live.AssistantMessage = &schema.Message{Role: schema.Assistant, Content: "same-live"}
	if err := store.Replay([]QueryEvent{launch, live}); err != nil {
		t.Fatal(err)
	}
	engine := NewQueryEngine(QueryEngineConfig{
		CWD: t.TempDir(), SessionID: "leader", ThreadID: "leader", RuntimeState: store,
	})
	t.Cleanup(engine.Close)
	engine.agentRunner = nil

	first, found, err := engine.AgentTranscriptPage(AgentTranscriptPageRequest{
		AgentID: "agent-1", Generation: 1, Limit: 2,
	})
	if err != nil || !found {
		t.Fatalf("first page: found=%v err=%v", found, err)
	}
	if first.AttachMode != ThreadModeLiveAttach || first.Replay || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first page metadata = %#v", first)
	}
	if len(first.Messages) != 2 || first.Messages[0].Content != "same-live" || first.Messages[0].Source != "durable+runtime" ||
		first.Messages[1].Content != "result" {
		t.Fatalf("first messages = %#v", first.Messages)
	}
	if first.Messages[0].TranscriptEntryID != exact.Key() ||
		first.Messages[0].TranscriptEntryID == first.Messages[1].TranscriptEntryID {
		t.Fatalf("first identities = %#v", first.Messages)
	}

	if err := recorder.RecordMessages([]*schema.Message{{Role: schema.Assistant, Content: "late"}}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	secondRequest := AgentTranscriptPageRequest{
		AgentID: "agent-1", Generation: 1, Cursor: first.NextCursor, Limit: 1,
	}
	second, found, err := engine.AgentTranscriptPage(secondRequest)
	if err != nil || !found {
		t.Fatalf("second page: found=%v err=%v", found, err)
	}
	if len(second.Messages) != 1 || second.Messages[0].Content != "same" ||
		second.Messages[0].TranscriptEntryID == exact.Key() {
		t.Fatalf("second page = %#v", second)
	}
	repeated, found, err := engine.AgentTranscriptPage(secondRequest)
	if err != nil || !found || !reflect.DeepEqual(second.Messages, repeated.Messages) || second.NextCursor != repeated.NextCursor {
		t.Fatalf("idempotent cursor reuse: second=%#v repeated=%#v found=%v err=%v", second, repeated, found, err)
	}
	for _, message := range append(append([]AgentTranscriptMessage(nil), first.Messages...), second.Messages...) {
		if message.Content == "late" {
			t.Fatal("append after first page leaked into frozen selector snapshot")
		}
	}

	_, _, err = engine.AgentTranscriptPage(AgentTranscriptPageRequest{
		AgentID: "agent-1", Generation: 2, Cursor: first.NextCursor,
	})
	if !errors.Is(err, ErrAgentTranscriptSelectionChanged) {
		t.Fatalf("stale generation error = %v", err)
	}
	otherLaunch := agentTranscriptTestEvent("agent-2", "other-session", "other-thread", 1, EventAgentLifecycle)
	otherLaunch.AgentLifecycle = &AgentLifecycleEvent{
		Status: "running", Generation: 1, TranscriptPath: recorder.Path(), StartedAt: otherLaunch.Timestamp,
	}
	if err := store.Apply(otherLaunch); err != nil {
		t.Fatal(err)
	}
	_, _, err = engine.AgentTranscriptPage(AgentTranscriptPageRequest{
		AgentID: "agent-2", Generation: 1, Cursor: first.NextCursor,
	})
	if !errors.Is(err, ErrAgentTranscriptSelectionChanged) {
		t.Fatalf("cross-Agent cursor error = %v", err)
	}
}

func TestAgentTranscriptPageReservesBoundedLiveRowWithoutDisplayDedup(t *testing.T) {
	recorder := transcript.NewRecorder("live-session", t.TempDir())
	if err := recorder.RecordMessages([]*schema.Message{{Role: schema.Assistant, Content: "same"}}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	store := NewRuntimeStateStore()
	launch := agentTranscriptTestEvent("agent-live", "live-session", "live-thread", 1, EventAgentLifecycle)
	launch.AgentLifecycle = &AgentLifecycleEvent{
		Status: "running", Generation: 1, TranscriptPath: recorder.Path(), StartedAt: launch.Timestamp,
	}
	stream := agentTranscriptTestEvent("agent-live", "live-session", "live-thread", 2, EventStream)
	stream.StreamEvent = &schema.Message{Role: schema.Assistant, Content: "same"}
	if err := store.Replay([]QueryEvent{launch, stream}); err != nil {
		t.Fatal(err)
	}
	engine := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir(), RuntimeState: store})
	t.Cleanup(engine.Close)
	engine.agentRunner = nil

	page, found, err := engine.AgentTranscriptPage(AgentTranscriptPageRequest{
		AgentID: "agent-live", Generation: 1, Limit: 1,
	})
	if err != nil || !found {
		t.Fatalf("live page: found=%v err=%v", found, err)
	}
	if len(page.Messages) != 1 || page.Messages[0].Source != "runtime" || page.Messages[0].Completed ||
		page.Messages[0].TranscriptEntryID != "" || !page.HasMore {
		t.Fatalf("live page = %#v", page)
	}
	next, _, err := engine.AgentTranscriptPage(AgentTranscriptPageRequest{
		AgentID: "agent-live", Generation: 1, Cursor: page.NextCursor, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Messages) != 1 || next.Messages[0].Source != "durable" || next.Messages[0].Content != "same" {
		t.Fatalf("durable continuation was display-deduplicated: %#v", next)
	}
}

func TestAgentTranscriptPageMarksReplayOnlyAndEvictedWithoutRuntimeMerge(t *testing.T) {
	recorder := transcript.NewRecorder("replay-session", t.TempDir())
	message := &schema.Message{Role: schema.Assistant, Content: "durable"}
	if err := recorder.RecordMessages([]*schema.Message{message}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	identity, ok := recorder.LatestMessageEntryIdentity(message)
	if !ok {
		t.Fatal("missing transcript identity")
	}
	store := NewRuntimeStateStore(RuntimeStoreLimits{Threads: 1, Agents: 4})
	launch := agentTranscriptTestEvent("agent-replay", "replay-session", "replay-thread", 1, EventAgentLifecycle)
	launch.AgentLifecycle = &AgentLifecycleEvent{
		Status: "running", Generation: 1, TranscriptPath: recorder.Path(), StartedAt: launch.Timestamp,
	}
	runtimeMessage := agentTranscriptTestEvent("agent-replay", "replay-session", "replay-thread", 2, EventAssistant)
	runtimeMessage.TranscriptEntryID = identity.Key()
	runtimeMessage.AssistantMessage = &schema.Message{Role: schema.Assistant, Content: "runtime-must-not-win"}
	terminal := agentTranscriptTestEvent("agent-replay", "replay-session", "replay-thread", 3, EventTerminal)
	terminal.TerminalInfo = &Terminal{Reason: TerminalCompleted, TurnCount: 1}
	if err := store.Replay([]QueryEvent{launch, runtimeMessage, terminal}); err != nil {
		t.Fatal(err)
	}
	engine := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir(), RuntimeState: store})
	t.Cleanup(engine.Close)
	engine.agentRunner = nil

	replay, found, err := engine.AgentTranscriptPage(AgentTranscriptPageRequest{
		AgentID: "agent-replay", Generation: 1, Limit: 2,
	})
	if err != nil || !found || replay.AttachMode != ThreadModeReplayOnly || !replay.Replay {
		t.Fatalf("replay-only page = %#v found=%v err=%v", replay, found, err)
	}
	if len(replay.Messages) != 1 || replay.Messages[0].Content != "durable" ||
		replay.Messages[0].Source != "durable" || !replay.Messages[0].Replay {
		t.Fatalf("replay source = %#v", replay.Messages)
	}

	other := agentTranscriptTestEvent("agent-other", "other-session", "other-thread", 1, EventAgentLifecycle)
	other.AgentLifecycle = &AgentLifecycleEvent{Status: "running", Generation: 1, TranscriptPath: recorder.Path(), StartedAt: other.Timestamp}
	if err := store.Apply(other); err != nil {
		t.Fatal(err)
	}
	evicted, found, err := engine.AgentTranscriptPage(AgentTranscriptPageRequest{
		AgentID: "agent-replay", Generation: 1, Limit: 2,
	})
	if err != nil || !found || evicted.AttachMode != ThreadModeEvictedTranscript || !evicted.Replay {
		t.Fatalf("evicted page = %#v found=%v err=%v", evicted, found, err)
	}
}

func TestAgentTranscriptPageRejectsRewriteAndDoesNotUseAgentRunner(t *testing.T) {
	recorder := transcript.NewRecorder("rewrite-session", t.TempDir())
	if err := recorder.RecordMessages([]*schema.Message{
		{Role: schema.User, Content: "one"},
		{Role: schema.User, Content: "two"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	store := NewRuntimeStateStore()
	launch := agentTranscriptTestEvent("agent-rewrite", "rewrite-session", "rewrite-thread", 1, EventAgentLifecycle)
	launch.AgentLifecycle = &AgentLifecycleEvent{
		Status: "running", Generation: 1, TranscriptPath: recorder.Path(), StartedAt: launch.Timestamp,
	}
	if err := store.Apply(launch); err != nil {
		t.Fatal(err)
	}
	engine := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir(), RuntimeState: store})
	t.Cleanup(engine.Close)
	engine.agentRunner = nil
	first, found, err := engine.AgentTranscriptPage(AgentTranscriptPageRequest{
		AgentID: "agent-rewrite", Generation: 1, Limit: 1,
	})
	if err != nil || !found || first.NextCursor == "" {
		t.Fatalf("first page = %#v found=%v err=%v", first, found, err)
	}
	if err := recorder.Replace([]*schema.Message{{Role: schema.User, Content: "replacement"}}); err != nil {
		t.Fatal(err)
	}
	_, _, err = engine.AgentTranscriptPage(AgentTranscriptPageRequest{
		AgentID: "agent-rewrite", Generation: 1, Cursor: first.NextCursor, Limit: 1,
	})
	if !errors.Is(err, transcript.ErrTranscriptRevisionChanged) {
		t.Fatalf("rewrite cursor error = %v", err)
	}
}

func TestAttachTranscriptEntryIdentityProjectsDurableProvenance(t *testing.T) {
	dir := t.TempDir()
	engine := NewQueryEngine(QueryEngineConfig{
		CWD: dir, SessionID: "provenance-session", ThreadID: "provenance-thread", TranscriptDir: dir,
	})
	t.Cleanup(engine.Close)
	message := &schema.Message{Role: schema.Assistant, Content: "persisted"}
	if err := engine.recordTranscriptMessages([]*schema.Message{message}); err != nil {
		t.Fatal(err)
	}
	event := QueryEvent{Type: EventAssistant, AssistantMessage: message}
	engine.attachTranscriptEntryIdentity(&event)
	if event.TranscriptEntryID == "" {
		t.Fatal("durable provenance was not attached")
	}
	decorated := engine.decorateRuntimeEvent("turn-1", event)
	thread, ok := engine.runtimeState.ThreadSnapshot("provenance-thread")
	if !ok || len(thread.Messages) != 1 ||
		thread.Messages[0].TranscriptEntryID != event.TranscriptEntryID ||
		decorated.TranscriptEntryID != event.TranscriptEntryID {
		t.Fatalf("runtime provenance = event=%#v thread=%#v", decorated, thread)
	}
}

func TestSubmitMessagePublishesDurableTranscriptEntryIdentity(t *testing.T) {
	dir := t.TempDir()
	engine := NewQueryEngine(QueryEngineConfig{
		CWD: dir, SessionID: "publish-session", ThreadID: "publish-thread",
		TranscriptDir: dir, ChatModel: &failingTranscriptModel{},
	})
	t.Cleanup(engine.Close)
	events, _ := engine.SubmitMessage(t.Context(), "hello")
	found := false
	for event := range events {
		if event.Type == EventAssistant && event.AssistantMessage != nil && event.TranscriptEntryID != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("no durably persisted assistant event carried transcript provenance")
	}
	thread, ok := engine.runtimeState.ThreadSnapshot("publish-thread")
	if !ok || len(thread.Messages) == 0 || thread.Messages[len(thread.Messages)-1].TranscriptEntryID == "" {
		t.Fatalf("runtime transcript provenance = %#v", thread)
	}
}

func agentTranscriptTestEvent(
	agentID string,
	sessionID string,
	threadID string,
	sequence uint64,
	eventType QueryEventType,
) QueryEvent {
	return QueryEvent{
		RuntimeEventEnvelope: RuntimeEventEnvelope{
			SessionID:       sessionID,
			ThreadID:        threadID,
			TurnID:          "turn-1",
			AgentID:         agentID,
			ParentSessionID: "parent-session",
			ParentThreadID:  "parent-thread",
			ParentAgentID:   "parent-agent",
			ParentToolUseID: "spawn-" + agentID,
			Sequence:        sequence,
			Timestamp:       time.Date(2026, 7, 22, 12, 0, int(sequence), 0, time.UTC),
		},
		Type: eventType,
	}
}
