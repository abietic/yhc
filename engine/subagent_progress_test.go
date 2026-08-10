package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestSubagentProgressTrackerDerivesReferenceShapedProgress(t *testing.T) {
	tracker := newSubagentProgressTracker()
	tracker.Observe(QueryEvent{Type: EventStreamRequestStart})
	tracker.Observe(QueryEvent{Type: EventAssistant, Message: &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:       "read-1",
			Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":`},
		}},
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11}},
	}})
	tracker.Observe(QueryEvent{Type: EventAssistant, Message: &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:       "read-1",
			Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"/tmp/a.go"}`},
		}},
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 3, TotalTokens: 13}},
	}})
	if changed := tracker.Observe(QueryEvent{Type: EventAssistant, Message: &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:       "read-1",
			Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"/tmp/a.go"}`},
		}},
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 3, TotalTokens: 13}},
	}}); changed {
		t.Fatal("identical streamed tool chunk should not emit duplicate progress")
	}

	tracker.Observe(QueryEvent{Type: EventStreamRequestStart})
	calls := make([]schema.ToolCall, 0, 7)
	for i := 0; i < 6; i++ {
		calls = append(calls, schema.ToolCall{
			ID:       "tool-" + string(rune('0'+i)),
			Function: schema.FunctionCall{Name: "Tool" + string(rune('0'+i)), Arguments: `{}`},
		})
	}
	calls = append(calls, schema.ToolCall{ID: "synthetic", Function: schema.FunctionCall{Name: "SyntheticOutput", Arguments: `{}`}})
	tracker.Observe(QueryEvent{Type: EventAssistant, Message: &schema.Message{
		Role:         schema.Assistant,
		ToolCalls:    calls,
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 20, CompletionTokens: 4, TotalTokens: 24}},
	}})

	progress := tracker.Progress()
	if progress.ToolUseCount != 8 {
		t.Fatalf("tool use count = %d, want 8", progress.ToolUseCount)
	}
	if progress.TokenCount != 27 {
		t.Fatalf("token count = %d, want 27", progress.TokenCount)
	}
	if len(progress.RecentActivities) != tools.MaxAgentRecentActivities {
		t.Fatalf("recent activities = %d, want %d", len(progress.RecentActivities), tools.MaxAgentRecentActivities)
	}
	if progress.RecentActivities[0].ToolName != "Tool1" || progress.LastActivity == nil || progress.LastActivity.ToolName != "Tool5" {
		t.Fatalf("unexpected recent activity window: %#v", progress.RecentActivities)
	}
	if progress.Summary != "" || progress.ActivitySummary != "Using Tool5" || progress.DisplaySummary() != "Using Tool5" {
		t.Fatalf("unexpected dedicated/activity summary state: %#v", progress)
	}
}

func TestSubagentProgressTrackerBoundsOversizedRawToolInput(t *testing.T) {
	tracker := newSubagentProgressTracker()
	tracker.StartRequest()
	raw := `{"command":"` + strings.Repeat("界", maxSubagentProgressRawInputBytes) + `"}`
	if !tracker.Observe(QueryEvent{Type: EventAssistant, Message: &schema.Message{
		Role:      schema.Assistant,
		ToolCalls: []schema.ToolCall{{ID: "huge", Function: schema.FunctionCall{Name: "Bash", Arguments: raw}}},
	}}) {
		t.Fatal("oversized tool input did not produce bounded progress")
	}
	progress := tracker.Progress()
	if progress.LastActivity == nil || progress.LastActivity.Input["_input_truncated"] != true {
		t.Fatalf("oversized input was not marked truncated: %#v", progress.LastActivity)
	}
	encoded, err := json.Marshal(progress.LastActivity.Input)
	if err != nil {
		t.Fatalf("marshal bounded input: %v", err)
	}
	if len(encoded) > maxSubagentProgressInputPreviewBytes {
		t.Fatalf("bounded input bytes = %d, want <= %d", len(encoded), maxSubagentProgressInputPreviewBytes)
	}
}

func TestSubagentProgressTrackerBootstrapsResumeMessages(t *testing.T) {
	tracker := newSubagentProgressTracker()
	tracker.Bootstrap([]*schema.Message{
		{
			Role:         schema.Assistant,
			ToolCalls:    []schema.ToolCall{{ID: "read", Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"a.go"}`}}},
			ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 3}},
		},
		{Role: schema.Tool, ToolName: "Read", Content: "ok"},
		{
			Role:         schema.Assistant,
			ToolCalls:    []schema.ToolCall{{ID: "bash", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"go test ./..."}`}}},
			ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 20, CompletionTokens: 4}},
		},
	})

	progress := tracker.Progress()
	if progress.ToolUseCount != 2 || progress.TokenCount != 27 {
		t.Fatalf("resume progress = %#v, want 2 tools and 27 tokens", progress)
	}
	tracker.StartRequest()
	tracker.Observe(QueryEvent{Type: EventAssistant, Message: &schema.Message{
		Role:         schema.Assistant,
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 5, CompletionTokens: 2}},
	}})
	if got := tracker.TokenCount(); got != 14 {
		t.Fatalf("post-compaction resumed tokens = %d, want 14", got)
	}
}

func TestConversationHistoryPreservesUsageForProgressResume(t *testing.T) {
	history := newConversationHistory(nil)
	history.Observe(QueryEvent{Type: EventAssistant, Message: &schema.Message{
		Role:         schema.Assistant,
		Content:      "working",
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11}},
	}})
	history.Observe(QueryEvent{Type: EventAssistant, Message: &schema.Message{
		Role:         schema.Assistant,
		Content:      " done",
		ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 3, TotalTokens: 13}},
	}})
	history.Observe(QueryEvent{Type: EventStreamRequestStart})

	messages := history.Messages()
	if len(messages) != 1 || messages[0].Content != "working done" {
		t.Fatalf("unexpected merged history: %#v", messages)
	}
	meta := messages[0].ResponseMeta
	if meta == nil || meta.Usage == nil || meta.Usage.PromptTokens != 10 || meta.Usage.CompletionTokens != 3 || meta.Usage.TotalTokens != 13 || meta.FinishReason != "stop" {
		t.Fatalf("response metadata was not retained for resume: %#v", meta)
	}
}

type blockingSubagentProgressModel struct {
	mu    sync.Mutex
	calls int
}

type preResponseBlockingModel struct {
	entered chan struct{}
	release chan struct{}
}

func (m *preResponseBlockingModel) Generate(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if err := m.wait(ctx); err != nil {
		return nil, err
	}
	return &schema.Message{Role: schema.Assistant, Content: "launch metadata complete"}, nil
}

func (m *preResponseBlockingModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if err := m.wait(ctx); err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "launch metadata complete"}}), nil
}

func (m *preResponseBlockingModel) wait(ctx context.Context) error {
	select {
	case m.entered <- struct{}{}:
	default:
	}
	select {
	case <-m.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *blockingSubagentProgressModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *blockingSubagentProgressModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:       "read-live",
				Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"/tmp/live.go"}`},
			}},
			ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 3, TotalTokens: 13}},
		}}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:         schema.Assistant,
		Content:      "done",
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 20, CompletionTokens: 4, TotalTokens: 24}},
	}}), nil
}

type failingSubagentProgressModel struct {
	mu    sync.Mutex
	calls int
	err   error
}

type resumableSubagentProgressModel struct {
	mu    sync.Mutex
	calls int
}

func (m *resumableSubagentProgressModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *resumableSubagentProgressModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	switch call {
	case 1:
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role:         schema.Assistant,
			ToolCalls:    []schema.ToolCall{{ID: "resume-read", Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"resume.go"}`}}},
			ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 3}},
		}}), nil
	case 2:
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role:         schema.Assistant,
			Content:      "initial done",
			ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 20, CompletionTokens: 4}},
		}}), nil
	case 3:
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role:         schema.Assistant,
			ToolCalls:    []schema.ToolCall{{ID: "resume-bash", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"go test ./..."}`}}},
			ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 30, CompletionTokens: 5}},
		}}), nil
	default:
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role:         schema.Assistant,
			Content:      "resumed done",
			ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 40, CompletionTokens: 6}},
		}}), nil
	}
}

func (m *failingSubagentProgressModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, m.err
}

func (m *failingSubagentProgressModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role:         schema.Assistant,
			ToolCalls:    []schema.ToolCall{{ID: "read-before-failure", Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"failure.go"}`}}},
			ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 3}},
		}}), nil
	}
	return nil, m.err
}

func TestSubAgentExecutorPublishesProgressBeforeCompletion(t *testing.T) {
	runner, runtimeState, entered, release := newBlockingSubagentRunner(t)
	ctx := tools.WithAgentRunner(context.Background(), runner)
	started, err := tools.RunAgentBackground(ctx, runner, tools.AgentExecOptions{Task: "inspect", Description: "inspect files"})
	if err != nil {
		t.Fatalf("run background agent: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}

	progress := waitForRunningAgentProgress(t, runner, started.ID, 500*time.Millisecond)
	if progress.ToolUseCount != 1 || progress.TokenCount != 13 {
		t.Fatalf("live progress = %#v, want 1 tool and 13 tokens", progress)
	}
	if progress.LastActivity == nil || progress.LastActivity.ActivityDescription != "Reading /tmp/live.go" {
		t.Fatalf("unexpected live activity: %#v", progress.LastActivity)
	}
	if events := runner.PollAgentProgress(); len(events) != 1 || events[0].TaskID != started.ID {
		t.Fatalf("pollable progress events = %#v", events)
	}
	runtimeSnapshot := runtimeState.Snapshot(started.ThreadID)
	runtimeAgent, ok := runtimeSnapshot.Agents[started.ID]
	if !ok || runtimeAgent.Progress.LastToolName != "Read" || len(runtimeAgent.Progress.RecentActivities) != 1 {
		t.Fatalf("runtime progress was not reduced from child events: %#v", runtimeAgent)
	}
	thread := runtimeSnapshot.Threads[started.ThreadID]
	sawProgressEvent := false
	for i, event := range thread.Events {
		if event.Sequence != uint64(i+1) {
			t.Fatalf("runtime event sequence[%d] = %d, want %d", i, event.Sequence, i+1)
		}
		if event.Type == EventTaskProgress {
			sawProgressEvent = true
		}
	}
	if !sawProgressEvent {
		t.Fatalf("child runtime events do not contain task progress: %#v", thread.Events)
	}

	close(release)
	completed := waitForAgentStatus(t, runner, started.ID, "completed", 2*time.Second)
	if completed.Progress.TokenCount != 27 || completed.Progress.ToolUseCount != 1 {
		t.Fatalf("terminal progress = %#v, want 27 tokens and 1 tool", completed.Progress)
	}
	if completed.Result != "done" {
		t.Fatalf("result = %q, want done", completed.Result)
	}
}

func TestSubAgentLaunchMetadataIsDurableBeforeFirstResponse(t *testing.T) {
	model := &preResponseBlockingModel{entered: make(chan struct{}, 1), release: make(chan struct{})}
	registry := tools.NewRegistry()
	runtimeState := NewRuntimeStateStore()
	cwd := t.TempDir()
	outputDir := t.TempDir()
	executor := NewSubAgentExecutor(model, registry, cwd)
	executor.RuntimeState = runtimeState
	runner := tools.NewAgentRunner(1)
	runner.SetOutputDir(outputDir)
	runner.SetExecutor(executor)

	opts := tools.AgentExecOptions{
		AgentID:         "agent-launch-1",
		SessionID:       "child-session-1",
		ThreadID:        "child-thread-1",
		ParentSessionID: "parent-session-1",
		ParentThreadID:  "parent-thread-1",
		ParentAgentID:   "parent-agent-1",
		ToolUseID:       "spawn-call-1",
		Name:            "launch-auditor",
		Task:            "inspect launch metadata",
		Description:     "Inspecting launch metadata",
		SubagentType:    "Explore",
		Model:           "small-model",
		Mode:            "plan",
		Isolation:       "none",
		CWD:             cwd,
	}
	started, err := tools.RunAgentBackground(context.Background(), runner, opts)
	if err != nil {
		t.Fatalf("run background agent: %v", err)
	}
	select {
	case <-model.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("sub-agent model request did not start")
	}

	snapshot := runtimeState.TaskExplorerSnapshot(started.ThreadID).Runtime
	agent, ok := snapshot.Agents[started.ID]
	if !ok {
		t.Fatalf("canonical runtime does not contain launched Agent: %#v", snapshot.Agents)
	}
	if agent.Status != "running" || agent.Name != opts.Name || agent.Task != opts.Task || agent.AgentType != opts.SubagentType ||
		agent.Model != opts.Model || agent.PermissionMode != opts.Mode || agent.Isolation != opts.Isolation || agent.CWD != cwd {
		t.Fatalf("incomplete canonical launch metadata: %#v", agent)
	}
	if agent.SessionID != opts.SessionID || agent.ThreadID != opts.ThreadID || agent.ParentSessionID != opts.ParentSessionID ||
		agent.ParentThreadID != opts.ParentThreadID || agent.ParentAgentID != opts.ParentAgentID || agent.ParentToolUseID != opts.ToolUseID {
		t.Fatalf("incomplete canonical launch identity: %#v", agent)
	}
	if agent.TranscriptPath == "" || agent.OutputFile == "" || agent.StartedAt.IsZero() {
		t.Fatalf("canonical launch paths/timestamp are incomplete: %#v", agent)
	}
	thread := runtimeState.Snapshot(started.ThreadID).Threads[started.ThreadID]
	if len(thread.Events) == 0 || thread.Events[0].Type != EventAgentLifecycle || thread.Events[0].Sequence != 1 {
		t.Fatalf("launch event is not the first child event: %#v", thread.Events)
	}
	for _, event := range thread.Events {
		if event.Type == EventAssistant {
			t.Fatalf("assistant response arrived before the launch assertion: %#v", thread.Events)
		}
	}

	fresh := tools.NewAgentRunner(1)
	fresh.SetOutputDir(outputDir)
	persisted, err := fresh.LoadPersistedAgentSnapshot(started.ID)
	if err != nil {
		t.Fatalf("load running launch metadata from a fresh runner: %v", err)
	}
	if persisted.Status != "running" || persisted.Name != opts.Name || persisted.Task != opts.Task ||
		persisted.Options.Model != opts.Model || persisted.Options.SubagentType != opts.SubagentType ||
		persisted.Options.Mode != opts.Mode || persisted.Options.Isolation != opts.Isolation || persisted.Options.CWD != cwd {
		t.Fatalf("incomplete durable running metadata: %#v", persisted)
	}
	if persisted.SessionID != opts.SessionID || persisted.ThreadID != opts.ThreadID || persisted.ParentSessionID != opts.ParentSessionID ||
		persisted.ParentThreadID != opts.ParentThreadID || persisted.ParentAgentID != opts.ParentAgentID || persisted.ToolUseID != opts.ToolUseID {
		t.Fatalf("incomplete durable running identity: %#v", persisted)
	}
	if len(persisted.Messages) != 1 || persisted.Messages[0].Role != schema.User || persisted.TranscriptPath != agent.TranscriptPath || persisted.OutputFile != agent.OutputFile {
		t.Fatalf("durable launch transcript/path mismatch: %#v", persisted)
	}
	if matches, globErr := filepath.Glob(filepath.Join(outputDir, "agents", ".agent-launch-1.json.tmp-*")); globErr != nil || len(matches) != 0 {
		t.Fatalf("launch metadata left temporary files: matches=%v err=%v", matches, globErr)
	}

	selected := runtimeState.TaskExplorerSnapshot(started.ThreadID).Runtime
	row := selected.Agents[started.ID]
	if row.Name != opts.Name ||
		row.Model != opts.Model ||
		row.TranscriptPath != agent.TranscriptPath {
		t.Fatalf("canonical runtime row = %#v", row)
	}

	close(model.release)
	completed := waitForAgentStatus(t, runner, started.ID, "completed", 2*time.Second)
	terminal, err := fresh.LoadPersistedAgentSnapshot(started.ID)
	if err != nil {
		t.Fatalf("reload terminal metadata: %v", err)
	}
	output, readErr := os.ReadFile(terminal.OutputFile)
	if terminal.Status != "completed" || terminal.CompletedAt.IsZero() || readErr != nil || string(output) != "launch metadata complete" || completed.Result != "launch metadata complete" {
		t.Fatalf("durable terminal metadata did not converge: persisted=%#v active=%#v", terminal, completed)
	}
}

func TestSubAgentLaunchPersistenceFailureDoesNotStartExecutor(t *testing.T) {
	model := &preResponseBlockingModel{entered: make(chan struct{}, 1), release: make(chan struct{})}
	runtimeState := NewRuntimeStateStore()
	executor := NewSubAgentExecutor(model, tools.NewRegistry(), t.TempDir())
	executor.RuntimeState = runtimeState
	outputDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outputDir, "agents"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("create metadata path blocker: %v", err)
	}
	runner := tools.NewAgentRunner(1)
	runner.SetOutputDir(outputDir)
	runner.SetExecutor(executor)

	_, err := tools.RunAgentBackground(context.Background(), runner, tools.AgentExecOptions{
		AgentID:         "agent-launch-failure",
		SessionID:       "child-session-failure",
		ThreadID:        "child-thread-failure",
		ParentSessionID: "parent-session-failure",
		ParentThreadID:  "parent-thread-failure",
		ParentAgentID:   "parent-agent-failure",
		ToolUseID:       "spawn-call-failure",
		Task:            "must not execute",
	})
	if err == nil || !strings.Contains(err.Error(), "persist launch metadata") {
		t.Fatalf("launch persistence error = %v, want durable launch failure", err)
	}
	if runner.ActiveCount() != 0 {
		t.Fatalf("failed launch registered an active Agent: %d", runner.ActiveCount())
	}
	select {
	case <-model.entered:
		t.Fatal("executor started after launch persistence failed")
	default:
	}
	snapshot := runtimeState.TaskExplorerSnapshot("child-thread-failure").Runtime
	agent := snapshot.Agents["agent-launch-failure"]
	if agent.Status != "failed" || !strings.Contains(agent.Error, "create metadata dir") {
		t.Fatalf("launch failure did not converge canonical state: %#v", agent)
	}
	thread := runtimeState.Snapshot("child-thread-failure").Threads["child-thread-failure"]
	if len(thread.Events) != 2 || thread.Events[0].Type != EventAgentLifecycle || thread.Events[1].Type != EventAgentLifecycle || thread.LastSequence != 2 {
		t.Fatalf("unexpected launch failure event sequence: %#v", thread)
	}
}

func TestSubAgentExecutorRetainsProgressWhenAborted(t *testing.T) {
	runner, _, entered, _ := newBlockingSubagentRunner(t)
	ctx := tools.WithAgentRunner(context.Background(), runner)
	started, err := tools.RunAgentBackground(ctx, runner, tools.AgentExecOptions{Task: "inspect"})
	if err != nil {
		t.Fatalf("run background agent: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}
	wantProgress := waitForRunningAgentProgress(t, runner, started.ID, 500*time.Millisecond)
	if err := runner.AbortAgent(started.ID); err != nil {
		t.Fatalf("abort agent: %v", err)
	}
	aborted := waitForAgentStatus(t, runner, started.ID, "aborted", 2*time.Second)
	deadline := time.Now().Add(2 * time.Second)
	for aborted.Error == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		aborted, _ = runner.GetAgentSnapshot(started.ID)
	}
	if aborted.Error == nil {
		t.Fatal("aborted execution did not settle")
	}
	if aborted.Progress.ToolUseCount != wantProgress.ToolUseCount || aborted.Progress.TokenCount != wantProgress.TokenCount {
		t.Fatalf("abort discarded progress: before=%#v after=%#v", wantProgress, aborted.Progress)
	}
}

func TestSubAgentExecutorPropagatesTerminalFailureToRunner(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info:       &schema.ToolInfo{Name: "Read", Desc: "read"},
		ExecuteCtx: func(context.Context, string) (string, error) { return "ok", nil },
	})
	executor := NewSubAgentExecutor(&failingSubagentProgressModel{err: errors.New("upstream unavailable")}, registry, t.TempDir())
	runner := tools.NewAgentRunner(1)
	outputDir := t.TempDir()
	runner.SetOutputDir(outputDir)
	runner.SetExecutor(executor)
	ctx := tools.WithAgentRunner(context.Background(), runner)
	started, err := tools.RunAgentBackground(ctx, runner, tools.AgentExecOptions{Task: "fail", MaxTurns: 2})
	if err != nil {
		t.Fatalf("run background agent: %v", err)
	}
	failed := waitForAgentStatus(t, runner, started.ID, "failed", 2*time.Second)
	if failed.Error == nil || !strings.Contains(failed.Error.Error(), "upstream unavailable") {
		t.Fatalf("failed agent error = %v", failed.Error)
	}
	if failed.Progress.ToolUseCount != 1 || failed.Progress.TokenCount != 13 || failed.Progress.LastActivity == nil || failed.Progress.LastActivity.ToolName != "Read" {
		t.Fatalf("failed agent discarded pre-terminal progress: %#v", failed.Progress)
	}
	fresh := tools.NewAgentRunner(1)
	fresh.SetOutputDir(outputDir)
	persisted, loadErr := fresh.LoadPersistedAgentSnapshot(started.ID)
	if loadErr != nil {
		t.Fatalf("reload failed Agent metadata: %v", loadErr)
	}
	if persisted.Status != "failed" || persisted.Error == nil || !strings.Contains(persisted.Error.Error(), "upstream unavailable") {
		t.Fatalf("durable failure metadata = %#v", persisted)
	}
}

func TestSubAgentExecutorRebuildsAndContinuesProgressOnResume(t *testing.T) {
	bashEntered := make(chan struct{}, 1)
	releaseBash := make(chan struct{})
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info:       &schema.ToolInfo{Name: "Read", Desc: "read"},
		ExecuteCtx: func(context.Context, string) (string, error) { return "ok", nil },
	})
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "Bash", Desc: "run"},
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			select {
			case bashEntered <- struct{}{}:
			default:
			}
			select {
			case <-releaseBash:
				return "ok", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	})
	executor := NewSubAgentExecutor(&resumableSubagentProgressModel{}, registry, t.TempDir())
	runtimeState := NewRuntimeStateStore()
	executor.RuntimeState = runtimeState
	runner := tools.NewAgentRunner(1)
	outputDir := t.TempDir()
	runner.SetOutputDir(outputDir)
	runner.SetExecutor(executor)

	started, err := tools.RunAgentBackground(context.Background(), runner, tools.AgentExecOptions{Task: "initial", Description: "resume progress"})
	if err != nil {
		t.Fatalf("run initial agent: %v", err)
	}
	initial := waitForAgentStatus(t, runner, started.ID, "completed", 2*time.Second)
	if initial.Progress.ToolUseCount != 1 || initial.Progress.TokenCount != 27 {
		t.Fatalf("initial progress = %#v, want 1 tool and 27 tokens", initial.Progress)
	}

	resumedID, action, err := runner.SendOrResumeAgentMessage(started.ID, tools.MessagePayload{Content: "continue"})
	if err != nil {
		t.Fatalf("resume agent: %v", err)
	}
	if resumedID != started.ID || action != "resumed" {
		t.Fatalf("resume result = (%q, %q), want (%q, resumed)", resumedID, action, started.ID)
	}
	select {
	case <-bashEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("resumed Bash tool did not start")
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	var resumed tools.RunningAgent
	for time.Now().Before(deadline) {
		resumed, _ = runner.GetAgentSnapshot(started.ID)
		if resumed.Status == "running" && resumed.Progress.LastActivity != nil && resumed.Progress.LastActivity.ToolName == "Bash" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if resumed.Progress.LastActivity == nil || resumed.Progress.LastActivity.ToolName != "Bash" {
		t.Fatalf("resumed live progress did not advance: %#v", resumed.Progress)
	}
	if resumed.Progress.ToolUseCount != 2 || resumed.Progress.TokenCount != 42 {
		t.Fatalf("resumed live progress = %#v, want 2 tools and 42 tokens", resumed.Progress)
	}
	if resumed.Progress.ActivitySummary != "Running go test ./..." {
		t.Fatalf("resumed activity summary = %q", resumed.Progress.ActivitySummary)
	}
	runtimeAgent := runtimeState.TaskExplorerSnapshot(started.ThreadID).Runtime.Agents[started.ID]
	if runtimeAgent.Status != "running" || runtimeAgent.Generation != 2 || runtimeAgent.Task != "continue" {
		t.Fatalf("resume launch metadata did not converge before completion: %#v", runtimeAgent)
	}
	var runningMetadata struct {
		Status     string `json:"status"`
		Task       string `json:"task"`
		Generation int64  `json:"generation"`
	}
	metadataBytes, readErr := os.ReadFile(filepath.Join(outputDir, "agents", started.ID+".json"))
	if readErr != nil {
		t.Fatalf("read resumed launch metadata: %v", readErr)
	}
	if unmarshalErr := json.Unmarshal(metadataBytes, &runningMetadata); unmarshalErr != nil {
		t.Fatalf("decode resumed launch metadata: %v", unmarshalErr)
	}
	if runningMetadata.Status != "running" || runningMetadata.Task != "continue" || runningMetadata.Generation != 2 {
		t.Fatalf("durable resumed launch metadata = %#v", runningMetadata)
	}

	close(releaseBash)
	completed := waitForAgentStatus(t, runner, started.ID, "completed", 2*time.Second)
	if completed.Progress.ToolUseCount != 2 || completed.Progress.TokenCount != 58 {
		t.Fatalf("resumed terminal progress = %#v, want 2 tools and 58 tokens", completed.Progress)
	}
	if completed.Result != "resumed done" {
		t.Fatalf("resumed result = %q", completed.Result)
	}
	metadataBytes, readErr = os.ReadFile(filepath.Join(outputDir, "agents", started.ID+".json"))
	if readErr != nil {
		t.Fatalf("read resumed terminal metadata: %v", readErr)
	}
	if unmarshalErr := json.Unmarshal(metadataBytes, &runningMetadata); unmarshalErr != nil {
		t.Fatalf("decode resumed terminal metadata: %v", unmarshalErr)
	}
	if runningMetadata.Status != "completed" || runningMetadata.Generation != 2 {
		t.Fatalf("durable resumed terminal metadata = %#v", runningMetadata)
	}
}

func newBlockingSubagentRunner(t *testing.T) (*tools.AgentRunner, *RuntimeStateStore, <-chan struct{}, chan struct{}) {
	t.Helper()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "Read", Desc: "read a file"},
		ExecuteCtx: func(ctx context.Context, input string) (string, error) {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
				return "ok", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	})
	executor := NewSubAgentExecutor(&blockingSubagentProgressModel{}, registry, t.TempDir())
	runtimeState := NewRuntimeStateStore()
	executor.RuntimeState = runtimeState
	runner := tools.NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	runner.SetExecutor(executor)
	return runner, runtimeState, entered, release
}

func waitForRunningAgentProgress(t *testing.T, runner *tools.AgentRunner, id string, timeout time.Duration) tools.AgentProgress {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		snapshot, ok := runner.GetAgentSnapshot(id)
		if ok && snapshot.Status == "running" && snapshot.Progress.LastActivity != nil {
			return snapshot.Progress
		}
		time.Sleep(5 * time.Millisecond)
	}
	snapshot, _ := runner.GetAgentSnapshot(id)
	t.Fatalf("agent progress was not visible before terminal state: %#v", snapshot)
	return tools.AgentProgress{}
}

func waitForAgentStatus(t *testing.T, runner *tools.AgentRunner, id, status string, timeout time.Duration) tools.RunningAgent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		snapshot, ok := runner.GetAgentSnapshot(id)
		if ok && snapshot.Status == status {
			return snapshot
		}
		time.Sleep(5 * time.Millisecond)
	}
	snapshot, _ := runner.GetAgentSnapshot(id)
	t.Fatalf("agent status = %q, want %q", snapshot.Status, status)
	return tools.RunningAgent{}
}
