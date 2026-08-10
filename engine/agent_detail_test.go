package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

func TestAgentDetailSnapshotMergesRunningRetainedAndEvictedMessages(t *testing.T) {
	runner, runtimeState, entered, release := newBlockingSubagentRunner(t)
	started, err := tools.RunAgentBackground(context.Background(), runner, tools.AgentExecOptions{
		Task:            "inspect detail",
		Description:     "Inspecting detail",
		Name:            "detail-worker",
		SubagentType:    "Explore",
		Model:           "small-model",
		ParentSessionID: "leader-session",
		ParentThreadID:  "leader-thread",
		ParentAgentID:   "leader-agent",
		ToolUseID:       "spawn-detail",
	})
	if err != nil {
		t.Fatalf("run background Agent: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		snapshot, _ := runner.GetAgentSnapshot(started.ID)
		t.Fatalf("running Agent did not enter tool execution: %#v", snapshot)
	}
	if _, action, err := runner.SendOrResumeAgentMessage(started.ID, tools.MessagePayload{Content: "queued follow-up"}); err != nil || action != "queued" {
		t.Fatalf("queue running Agent input: action=%q err=%v", action, err)
	}

	eng := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir(), RuntimeState: runtimeState, AgentRunner: runner})
	t.Cleanup(eng.Close)
	running, ok := eng.AgentDetailSnapshot(started.ID)
	if !ok {
		t.Fatal("running Agent detail was not found")
	}
	if running.Storage != "retained" || running.Agent.Status != "running" || running.PendingMessageCount != 1 {
		t.Fatalf("running detail metadata = %#v", running)
	}
	if running.Agent.ParentThreadID != "leader-thread" || running.Agent.ParentToolUseID != "spawn-detail" || running.Agent.Model != "small-model" {
		t.Fatalf("running detail lineage/model = %#v", running.Agent)
	}
	if !agentDetailContainsRoleContent(running.Messages, "user", "inspect detail") || !agentDetailContainsTool(running.Messages, "Read") {
		t.Fatalf("running detail did not merge launch and live messages: %#v", running.Messages)
	}
	assertUniqueAgentDetailMessageIDs(t, running.Messages)
	running.Messages[0].Content = "mutated"
	if len(running.Messages[1].ToolCalls) > 0 {
		running.Messages[1].ToolCalls[0].Name = "mutated"
	}
	defensive, _ := eng.AgentDetailSnapshot(started.ID)
	if agentDetailContainsRoleContent(defensive.Messages, "user", "mutated") || agentDetailContainsTool(defensive.Messages, "mutated") {
		t.Fatalf("detail snapshot mutation leaked into runtime/runner state: %#v", defensive.Messages)
	}

	close(release)
	completed := waitForAgentStatus(t, runner, started.ID, "completed", 2*time.Second)
	if completed.Result != "done" {
		t.Fatalf("completed result = %q", completed.Result)
	}
	terminal, ok := eng.AgentDetailSnapshot(started.ID)
	if !ok {
		t.Fatal("terminal Agent detail was not found")
	}
	if terminal.Storage != "retained" || terminal.Agent.Status != "completed" || terminal.Thread.LastTerminal == nil || terminal.Output != "done" {
		t.Fatalf("terminal retained detail = %#v", terminal)
	}
	assertUniqueAgentDetailMessageIDs(t, terminal.Messages)
	retainedIDs := agentDetailMessageIDs(terminal.Messages)

	runner.Cleanup(-time.Second)
	evicted, ok := eng.AgentDetailSnapshot(started.ID)
	if !ok {
		t.Fatal("evicted Agent detail was not found")
	}
	if evicted.Storage != "evicted" || evicted.Agent.Status != "completed" || evicted.Output != "done" || evicted.LoadError != "" {
		t.Fatalf("evicted detail = %#v", evicted)
	}
	if got := agentDetailMessageIDs(evicted.Messages); !reflect.DeepEqual(got, retainedIDs) {
		t.Fatalf("stable message IDs changed after eviction:\nretained=%v\nevicted=%v", retainedIDs, got)
	}
}

func TestAgentDetailSnapshotSurfacesAttentionAndTerminalReason(t *testing.T) {
	store := NewRuntimeStateStore()
	launch := runtimeTestEvent(1, "agent-launch:agent-1:1", EventAgentLifecycle, func(evt *QueryEvent) {
		evt.AgentLifecycle = &AgentLifecycleEvent{Task: "wait for approval", Status: "running", Generation: 1, StartedAt: evt.Timestamp}
	})
	request := runtimeTestEvent(2, "turn-1", EventPermissionRequest, func(evt *QueryEvent) {
		evt.PermissionRequest = &PermissionRequestEvent{ToolName: "Bash", ToolUseID: "approval-1", Message: "Run tests"}
	})
	if err := store.Replay([]QueryEvent{launch, request}); err != nil {
		t.Fatal(err)
	}
	eng := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir(), RuntimeState: store})
	t.Cleanup(eng.Close)
	eng.agentRunner = nil
	detail, ok := eng.AgentDetailSnapshot("agent-1")
	if !ok || detail.UnresolvedCount != 1 || detail.Thread.Status != RuntimeThreadWaitingInput {
		t.Fatalf("attention detail = %#v, found=%v", detail, ok)
	}

	resolved := runtimeTestEvent(3, "turn-1", EventPermissionResolved, func(evt *QueryEvent) {
		evt.PermissionResolved = &PermissionResolvedEvent{ToolUseID: "approval-1", Decision: "deny"}
	})
	terminal := runtimeTestEvent(4, "turn-1", EventTerminal, func(evt *QueryEvent) {
		evt.TerminalInfo = &Terminal{Reason: TerminalModelError, Err: os.ErrPermission}
	})
	if err := store.Replay([]QueryEvent{resolved, terminal}); err != nil {
		t.Fatal(err)
	}
	detail, ok = eng.AgentDetailSnapshot("agent-1")
	if !ok || detail.UnresolvedCount != 0 || detail.Thread.LastTerminal == nil || detail.Thread.LastTerminal.Reason != TerminalModelError || detail.Thread.LastTerminal.Error == "" {
		t.Fatalf("terminal detail = %#v, found=%v", detail, ok)
	}
}

func TestMergeAgentDetailMessagesDeduplicatesStableContentAndExplicitIDs(t *testing.T) {
	persisted := agentDetailMessagesFromSchema([]*schema.Message{
		{Role: schema.Assistant, Content: "same"},
		{Role: schema.Assistant, Content: "same"},
		{Role: schema.Tool, ToolName: "Read", Content: "ok", Extra: map[string]any{"message_id": "tool-message"}},
	})
	runtimeMessages := []AgentDetailMessage{
		{ID: "thread-1:9", Role: "assistant", Content: "same", Completed: true, Source: "runtime"},
		{ID: "tool-message", Role: "tool", ToolName: "Read", Content: "ok", Completed: true, Source: "runtime", explicitID: true},
	}
	merged := mergeAgentDetailMessages(persisted, runtimeMessages, "thread-1")
	if len(merged) != 3 {
		t.Fatalf("merged messages = %#v, want three deduplicated messages", merged)
	}
	if merged[1].ID != "thread-1:9" || merged[2].ID != "tool-message" {
		t.Fatalf("runtime/explicit IDs were not retained: %#v", merged)
	}
	assertUniqueAgentDetailMessageIDs(t, merged)
}

func TestMergeAgentDetailMessagesBoundsAndDisambiguatesDuplicateIDs(t *testing.T) {
	messages := make([]AgentDetailMessage, 0, maxAgentDetailMessages+20)
	for i := 0; i < maxAgentDetailMessages+20; i++ {
		messages = append(messages, AgentDetailMessage{ID: "duplicate", Role: "assistant", Content: strings.Repeat("x", i%5+1), explicitID: true})
	}
	merged := mergeAgentDetailMessages(messages, nil, "thread-bounded")
	if len(merged) != maxAgentDetailMessages {
		t.Fatalf("bounded message count = %d, want %d", len(merged), maxAgentDetailMessages)
	}
	assertUniqueAgentDetailMessageIDs(t, merged)
}

func TestAgentExecutionDetailDoesNotReuseTerminalOutputForLiveGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.out")
	if err := os.WriteFile(path, []byte("previous-generation-secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewRuntimeStateStore()
	agent := RuntimeAgentSnapshot{
		AgentID: "agent-live", Generation: 7, SessionID: "session-live", ThreadID: "thread-live",
		Status: "running", OutputFile: path,
	}
	if err := store.RestoreAgentSnapshot(agent, nil, true); err != nil {
		t.Fatal(err)
	}
	want := store.TaskExplorerSnapshot("").Executions[RuntimeExecutionKey{AgentID: agent.AgentID, Generation: agent.Generation}].Agent
	engine := &QueryEngine{runtimeState: store}

	detail, found, err := engine.AgentExecutionDetail(AgentExecutionDetailRequest{
		AgentID: agent.AgentID, Generation: agent.Generation, SessionID: agent.SessionID, ThreadID: agent.ThreadID, IncludeOutput: true,
	})
	if err != nil || !found {
		t.Fatalf("live exact detail: found=%v err=%v", found, err)
	}
	if detail.Revision == 0 || !reflect.DeepEqual(detail.Agent, want) {
		t.Fatalf("live detail lineage = %#v, want exact %#v", detail, want)
	}
	if detail.Output != "" || detail.OutputTruncated || detail.LoadError != "" {
		t.Fatalf("live generation reused terminal output = %#v", detail)
	}
}

func TestAgentExecutionDetailRetainsTerminalCurrentGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.out")
	if err := os.WriteFile(path, []byte("terminal result"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewRuntimeStateStore()
	agent := RuntimeAgentSnapshot{
		AgentID: "agent-terminal", Generation: 3, SessionID: "session-terminal", ThreadID: "thread-terminal",
		Status: "completed", OutputFile: path,
	}
	if err := store.RestoreAgentSnapshot(agent, nil, false); err != nil {
		t.Fatal(err)
	}
	engine := &QueryEngine{runtimeState: store}

	detail, found, err := engine.AgentExecutionDetail(AgentExecutionDetailRequest{
		AgentID: agent.AgentID, Generation: agent.Generation, SessionID: agent.SessionID, ThreadID: agent.ThreadID, IncludeOutput: true,
	})
	if err != nil || !found || detail.Agent.Status != "completed" || detail.Output != "terminal result" || detail.LoadError != "" {
		t.Fatalf("terminal exact detail = %#v, found=%v err=%v", detail, found, err)
	}
}

func TestAgentExecutionDetailRejectsHistoricalGenerationBeforeOutputLoad(t *testing.T) {
	store := NewRuntimeStateStore()
	older := RuntimeAgentSnapshot{
		AgentID: "agent-reused", Generation: 1, SessionID: "session-old", ThreadID: "thread-old",
		Status: "completed", OutputFile: filepath.Join(t.TempDir(), "old.out"),
	}
	if err := store.RestoreAgentSnapshot(older, nil, false); err != nil {
		t.Fatal(err)
	}
	newer := older
	newer.Generation = 2
	newer.SessionID = "session-new"
	newer.ThreadID = "thread-new"
	newer.OutputFile = t.TempDir() // A directory would produce a distinct read error if I/O ran first.
	if err := store.RestoreAgentSnapshot(newer, nil, true); err != nil {
		t.Fatal(err)
	}
	engine := &QueryEngine{runtimeState: store}

	detail, found, err := engine.AgentExecutionDetail(AgentExecutionDetailRequest{
		AgentID: older.AgentID, Generation: older.Generation, SessionID: older.SessionID, ThreadID: older.ThreadID, IncludeOutput: true,
	})
	if !errors.Is(err, ErrAgentExecutionDetailSelectionChanged) {
		t.Fatalf("historical selection error = %v, want stale sentinel", err)
	}
	if found || !reflect.DeepEqual(detail.Agent, RuntimeAgentSnapshot{}) || detail.Output != "" || detail.LoadError != "" {
		t.Fatalf("historical selection exposed state = %#v, found=%v", detail, found)
	}
}

func TestAgentExecutionDetailRejectsMismatchedLineage(t *testing.T) {
	store := NewRuntimeStateStore()
	agent := RuntimeAgentSnapshot{AgentID: "agent-lineage", Generation: 4, SessionID: "session-ok", ThreadID: "thread-ok", Status: "running"}
	if err := store.RestoreAgentSnapshot(agent, nil, true); err != nil {
		t.Fatal(err)
	}
	engine := &QueryEngine{runtimeState: store}
	for _, request := range []AgentExecutionDetailRequest{
		{AgentID: agent.AgentID, Generation: agent.Generation, SessionID: "wrong-session", ThreadID: agent.ThreadID},
		{AgentID: agent.AgentID, Generation: agent.Generation, SessionID: agent.SessionID, ThreadID: "wrong-thread"},
	} {
		detail, found, err := engine.AgentExecutionDetail(request)
		if !errors.Is(err, ErrAgentExecutionDetailSelectionChanged) {
			t.Fatalf("mismatched lineage error = %v, want stale sentinel", err)
		}
		if found || !reflect.DeepEqual(detail.Agent, RuntimeAgentSnapshot{}) || detail.Output != "" || detail.LoadError != "" {
			t.Fatalf("mismatched lineage exposed state = %#v, found=%v", detail, found)
		}
	}
}

func TestAgentExecutionDetailSkipsOutputLoadWhenNotRequested(t *testing.T) {
	store := NewRuntimeStateStore()
	agent := RuntimeAgentSnapshot{
		AgentID: "agent-metadata", Generation: 8, SessionID: "session-metadata", ThreadID: "thread-metadata",
		Status: "running", OutputFile: t.TempDir(), // Reading a directory deterministically fails.
	}
	if err := store.RestoreAgentSnapshot(agent, nil, true); err != nil {
		t.Fatal(err)
	}
	want := store.TaskExplorerSnapshot("").Executions[RuntimeExecutionKey{AgentID: agent.AgentID, Generation: agent.Generation}].Agent
	engine := &QueryEngine{runtimeState: store}

	detail, found, err := engine.AgentExecutionDetail(AgentExecutionDetailRequest{
		AgentID: agent.AgentID, Generation: agent.Generation, SessionID: agent.SessionID, ThreadID: agent.ThreadID,
	})
	if err != nil || !found || !reflect.DeepEqual(detail.Agent, want) || detail.Output != "" || detail.OutputTruncated || detail.LoadError != "" {
		t.Fatalf("metadata-only exact detail = %#v, found=%v err=%v", detail, found, err)
	}
}

func TestAgentExecutionDetailRejectsSelectionReplacementDuringOutputRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.out")
	if err := os.WriteFile(path, []byte("generation-one-output"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewRuntimeStateStore()
	current := RuntimeAgentSnapshot{
		AgentID: "agent-race", Generation: 1, SessionID: "session-one", ThreadID: "thread-one",
		Status: "completed", OutputFile: path,
	}
	if err := store.RestoreAgentSnapshot(current, nil, false); err != nil {
		t.Fatal(err)
	}
	engine := &QueryEngine{runtimeState: store}
	request := AgentExecutionDetailRequest{
		AgentID: current.AgentID, Generation: current.Generation,
		SessionID: current.SessionID, ThreadID: current.ThreadID,
		IncludeOutput: true,
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	type readResult struct {
		detail AgentExecutionDetail
		found  bool
		err    error
	}
	result := make(chan readResult, 1)
	go func() {
		detail, found, err := engine.agentExecutionDetail(request, func(path string) (string, bool, error) {
			close(entered)
			<-release
			return readAgentDetailOutput(path)
		})
		result <- readResult{detail: detail, found: found, err: err}
	}()
	<-entered

	replacement := current
	replacement.Generation = 2
	replacement.SessionID = "session-two"
	replacement.ThreadID = "thread-two"
	if err := store.RestoreAgentSnapshot(replacement, nil, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("generation-two-secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	close(release)
	got := <-result
	if !errors.Is(got.err, ErrAgentExecutionDetailSelectionChanged) {
		t.Fatalf("replacement error = %v, want stale sentinel", got.err)
	}
	if got.found || !reflect.DeepEqual(got.detail, AgentExecutionDetail{}) {
		t.Fatalf("replacement exposed output = %#v, found=%v", got.detail, got.found)
	}
}

func TestReadAgentDetailOutputReturnsBoundedValidUTF8Tail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.out")
	if err := os.WriteFile(path, []byte(strings.Repeat("界", maxAgentDetailOutputBytes)), 0o644); err != nil {
		t.Fatal(err)
	}
	output, truncated, err := readAgentDetailOutput(path)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(output) > maxAgentDetailOutputBytes || !utf8.ValidString(output) {
		t.Fatalf("bounded output: truncated=%v bytes=%d utf8=%v", truncated, len(output), utf8.ValidString(output))
	}
}

func agentDetailContainsRoleContent(messages []AgentDetailMessage, role, content string) bool {
	for _, message := range messages {
		if message.Role == role && strings.Contains(message.Content, content) {
			return true
		}
	}
	return false
}

func agentDetailContainsTool(messages []AgentDetailMessage, toolName string) bool {
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if call.Name == toolName {
				return true
			}
		}
	}
	return false
}

func assertUniqueAgentDetailMessageIDs(t *testing.T, messages []AgentDetailMessage) {
	t.Helper()
	seen := make(map[string]struct{}, len(messages))
	for _, message := range messages {
		if message.ID == "" {
			t.Fatal("Agent detail message has no stable ID")
		}
		if _, exists := seen[message.ID]; exists {
			t.Fatalf("duplicate Agent detail message ID %q", message.ID)
		}
		seen[message.ID] = struct{}{}
	}
}

func agentDetailMessageIDs(messages []AgentDetailMessage) []string {
	ids := make([]string, len(messages))
	for i, message := range messages {
		ids[i] = message.ID
	}
	return ids
}
