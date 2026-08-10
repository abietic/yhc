package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
)

const sessionRestartReplayHelperEnv = "YHC_SESSION_RESTART_REPLAY_HELPER"

func TestSessionCheckpointPersistsExecutionReferencesWithoutRequestPayloads(t *testing.T) {
	cwd := t.TempDir()
	additional := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	store := NewRuntimeStateStore()
	if err := store.Apply(QueryEvent{
		RuntimeEventEnvelope: RuntimeEventEnvelope{
			SessionID: "session", ThreadID: "leader", TurnID: "turn", Sequence: 1, Timestamp: time.Now().UTC(),
		},
		Type: EventPermissionRequest,
		PermissionRequest: &PermissionRequestEvent{
			ToolUseID: "request-live", ToolName: "Bash", Input: map[string]any{"command": "secret payload"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(QueryEvent{
		RuntimeEventEnvelope: RuntimeEventEnvelope{
			SessionID: "agent-session", ThreadID: "agent-thread", TurnID: "agent-launch:agent-1:1",
			AgentID: "agent-1", ParentSessionID: "session", ParentThreadID: "leader",
			Sequence: 1, Timestamp: time.Now().UTC(),
		},
		Type:           EventAgentLifecycle,
		AgentLifecycle: &AgentLifecycleEvent{Phase: "launched", Status: "running", StartedAt: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID: "session", ThreadID: "leader", CWD: cwd, TranscriptDir: dir,
		RuntimeState: store, AdditionalDirs: []string{additional},
		WorktreePath: cwd, WorktreeBranch: "agent/test",
		ModelResolver: ModelResolverFunc(func(modelSpec string) (provider.ResolvedConfig, error) {
			return provider.ResolvedConfig{Config: provider.Config{
				Provider: provider.ProviderAgenticOpenAI,
				Model:    modelSpec,
			}}, nil
		}),
	})
	t.Cleanup(engine.Close)
	engine.SetResumedMessages([]*schema.Message{{Role: schema.User, Content: "hello"}})
	if _, err := engine.ChangeModel(context.Background(), "gpt-5"); err != nil {
		t.Fatal(err)
	}
	engine.SetPermissionMode(permission.ModePlan)

	loaded, err := transcript.NewRecorder("session", dir).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	metadata := session.ReadSessionMetadataFull(loaded)
	if metadata == nil {
		t.Fatal("checkpoint metadata missing")
	}
	if metadata.Model != "gpt-5" || metadata.PermissionMode != "plan" || metadata.ThreadID != "leader" || metadata.CWD != cwd {
		t.Fatalf("checkpoint context = %#v", metadata)
	}
	if len(metadata.AdditionalDirs) != 1 || metadata.AdditionalDirs[0] != additional || metadata.WorktreePath != cwd {
		t.Fatalf("checkpoint working scope = %#v", metadata)
	}
	if len(metadata.AgentIDs) != 1 || metadata.AgentIDs[0] != "agent-1" || len(metadata.PendingRequestIDs) != 1 || metadata.PendingRequestIDs[0] != "request-live" {
		t.Fatalf("checkpoint references = %#v", metadata)
	}
	data, err := os.ReadFile(transcript.NewRecorder("session", dir).Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret payload") {
		t.Fatal("checkpoint persisted interactive request payload")
	}
}

func TestResumeIntersectsPersistedRequestsWithLiveRuntime(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	recorder := writeEngineSelectedSession(t, dir, "selected", "prompt")
	writeProjectGraphRootTestMetadata(t, recorder, &session.SessionMetadataFull{
		SessionID: "selected", ThreadID: "selected-thread", CWD: cwd,
		PendingRequestIDs: []string{"live", "stale"},
		CreatedAt:         time.Now().UTC(), UpdatedAt: time.Now().UTC(), MessageCount: 2,
	})
	store := NewRuntimeStateStore()
	if err := store.Apply(QueryEvent{
		RuntimeEventEnvelope: RuntimeEventEnvelope{
			SessionID: "selected", ThreadID: "selected-thread", TurnID: "turn", Sequence: 1, Timestamp: time.Now().UTC(),
		},
		Type:              EventPermissionRequest,
		PermissionRequest: &PermissionRequestEvent{ToolUseID: "live", ToolName: "Read"},
	}); err != nil {
		t.Fatal(err)
	}
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID: "current", CWD: cwd, TranscriptDir: dir, RuntimeState: store,
	})
	t.Cleanup(engine.Close)
	liveKey := permissionRequestKey{
		engineID:  engine.permissionEngineID,
		toolUseID: "live",
	}
	engine.permissionCoordinator.mu.Lock()
	engine.permissionCoordinator.pending[liveKey] = &permissionPendingRequest{
		request: PermissionPromptRequest{
			ToolUseID: "live",
			SessionID: "selected",
			ThreadID:  "selected-thread",
		},
	}
	engine.permissionCoordinator.mu.Unlock()
	t.Cleanup(func() {
		engine.permissionCoordinator.mu.Lock()
		delete(engine.permissionCoordinator.pending, liveKey)
		engine.permissionCoordinator.mu.Unlock()
	})
	resumed, err := engine.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID: "selected", CWD: cwd, TranscriptDir: dir, TranscriptPath: recorder.Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.ActionableRequestIDs) != 1 || resumed.ActionableRequestIDs[0] != "live" {
		t.Fatalf("actionable requests = %#v", resumed.ActionableRequestIDs)
	}
	if !containsSessionWarning(resumed.Warnings, "ignored 1 persisted request") {
		t.Fatalf("stale request warning missing: %#v", resumed.Warnings)
	}
}

type replayAgentExecutor struct{}

func (replayAgentExecutor) ExecuteAgent(_ context.Context, _ tools.AgentExecOptions) (*tools.AgentExecResult, error) {
	return &tools.AgentExecResult{
		Result: "done",
		Messages: []*schema.Message{
			{Role: schema.User, Content: "inspect"},
			{Role: schema.Assistant, Content: "done"},
		},
	}, nil
}

type failingReplayAgentExecutor struct{}

func (failingReplayAgentExecutor) ExecuteAgent(_ context.Context, _ tools.AgentExecOptions) (*tools.AgentExecResult, error) {
	return nil, errors.New("expected child failure")
}

type replayAgentExecutorFunc func(context.Context, tools.AgentExecOptions) (*tools.AgentExecResult, error)

func (fn replayAgentExecutorFunc) ExecuteAgent(
	ctx context.Context,
	opts tools.AgentExecOptions,
) (*tools.AgentExecResult, error) {
	return fn(ctx, opts)
}

type liveRestoreAgentExecutor struct {
	started chan struct{}
}

func (e liveRestoreAgentExecutor) ExecuteAgent(ctx context.Context, _ tools.AgentExecOptions) (*tools.AgentExecResult, error) {
	close(e.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestResumeReattachesAgentStillOwnedByCurrentRunner(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	runner := tools.NewAgentRunner(1)
	runner.SetOutputDir(filepath.Join(cwd, "agent-output"))
	started := make(chan struct{})
	runner.SetExecutor(liveRestoreAgentExecutor{started: started})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() {
		cancel()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			snapshot, ok := runner.GetAgentSnapshot("agent-live")
			if !ok || snapshot.Status != "running" {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	if _, err := tools.RunAgentBackground(ctx, runner, tools.AgentExecOptions{
		AgentID: "agent-live", SessionID: "agent-session", ThreadID: "agent-thread",
		ParentSessionID: "selected", ParentThreadID: "selected-thread", Task: "wait",
	}); err != nil {
		t.Fatal(err)
	}
	<-started

	recorder := writeEngineSelectedSession(t, dir, "selected", "prompt")
	writeProjectGraphRootTestMetadata(t, recorder, &session.SessionMetadataFull{
		SessionID: "selected", ThreadID: "selected-thread", CWD: cwd,
		AgentIDs: []string{"agent-live"}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), MessageCount: 2,
	})
	query := NewQueryEngine(QueryEngineConfig{SessionID: "current", CWD: cwd, TranscriptDir: dir, AgentRunner: runner})
	t.Cleanup(query.Close)

	resumed, err := query.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID: "selected", CWD: cwd, TranscriptDir: dir, TranscriptPath: recorder.Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.RestoredAgents) != 1 || resumed.RestoredAgents[0].Mode != string(ThreadModeLiveAttach) || resumed.RestoredAgents[0].Status != "running" {
		t.Fatalf("live restored Agents = %#v warnings=%#v", resumed.RestoredAgents, resumed.Warnings)
	}
	catalog := query.ThreadCatalogSnapshot()
	found := false
	for _, entry := range catalog.Threads {
		if entry.ThreadID == "agent-thread" {
			found = entry.Mode == ThreadModeLiveAttach && entry.Status == RuntimeThreadRunning
		}
	}
	if !found {
		t.Fatalf("live Agent thread missing from catalog: %#v", catalog.Threads)
	}
}

func TestDiskRestoredRunningAgentBecomesNonActionableReplay(t *testing.T) {
	store := NewRuntimeStateStore()
	if err := store.RestoreAgentSnapshot(RuntimeAgentSnapshot{
		AgentID: "agent", SessionID: "agent-session", ThreadID: "agent-thread",
		ParentSessionID: "parent", Status: "running", StartedAt: time.Now().Add(-time.Minute),
	}, []*schema.Message{{Role: schema.Assistant, Content: "partial"}}, false); err != nil {
		t.Fatal(err)
	}
	agent, thread, _, ok := store.AgentThreadSnapshot("agent")
	if !ok || agent.Status != "aborted" || thread.Status != RuntimeThreadAborted || len(thread.PendingInteractions) != 0 {
		t.Fatalf("disk replay restored actionable running state: agent=%#v thread=%#v", agent, thread)
	}
}

func TestResumeRestoresPersistedAgentAsReplayOnly(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	outputDir := filepath.Join(cwd, "agent-output")
	producer := tools.NewAgentRunner(1)
	producer.SetOutputDir(outputDir)
	producer.SetExecutor(replayAgentExecutor{})
	if _, err := tools.RunAgent(t.Context(), producer, tools.AgentExecOptions{
		AgentID: "agent-1", SessionID: "agent-session", ThreadID: "agent-thread",
		ParentSessionID: "selected", ParentThreadID: "selected-thread", Task: "inspect",
	}); err != nil {
		t.Fatal(err)
	}

	recorder := writeEngineSelectedSession(t, dir, "selected", "prompt")
	writeProjectGraphRootTestMetadata(t, recorder, &session.SessionMetadataFull{
		SessionID: "selected", ThreadID: "selected-thread", CWD: cwd,
		AgentIDs: []string{"agent-1"}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), MessageCount: 2,
	})
	fresh := tools.NewAgentRunner(1)
	fresh.SetOutputDir(outputDir)
	engine := NewQueryEngine(QueryEngineConfig{SessionID: "current", CWD: cwd, TranscriptDir: dir, AgentRunner: fresh})
	t.Cleanup(engine.Close)

	resumed, err := engine.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID: "selected", CWD: cwd, TranscriptDir: dir, TranscriptPath: recorder.Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.RestoredAgents) != 1 || resumed.RestoredAgents[0].Mode != string(ThreadModeReplayOnly) || resumed.RestoredAgents[0].ThreadID != "agent-thread" {
		t.Fatalf("restored Agents = %#v warnings=%#v", resumed.RestoredAgents, resumed.Warnings)
	}
	catalog := engine.ThreadCatalogSnapshot()
	found := false
	for _, entry := range catalog.Threads {
		if entry.ThreadID == "agent-thread" {
			found = entry.Mode == ThreadModeReplayOnly && entry.Status == RuntimeThreadCompleted
		}
	}
	if !found {
		t.Fatalf("replay Agent thread missing from catalog: %#v", catalog.Threads)
	}
}

func TestProcessRestartRestoresAgentReplayAndLineage(t *testing.T) {
	if cwd := os.Getenv(sessionRestartReplayHelperEnv); cwd != "" {
		assertProcessRestartAgentReplay(t, cwd)
		return
	}

	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	outputDir := filepath.Join(cwd, "agent-output")
	producer := tools.NewAgentRunner(1)
	producer.SetOutputDir(outputDir)
	started := make(chan struct{})
	producer.SetExecutor(liveRestoreAgentExecutor{started: started})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() {
		cancel()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			snapshot, ok := producer.GetAgentSnapshot("agent-restart")
			if !ok || snapshot.Status != "running" {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	if _, err := tools.RunAgentBackground(ctx, producer, tools.AgentExecOptions{
		AgentID: "agent-restart", SessionID: "agent-session", ThreadID: "agent-thread",
		ParentSessionID: "selected", ParentThreadID: "selected-thread", Task: "inspect after restart",
	}); err != nil {
		t.Fatal(err)
	}
	<-started

	recorder := writeEngineSelectedSession(t, dir, "selected", "prompt")
	writeProjectGraphRootTestMetadata(t, recorder, &session.SessionMetadataFull{
		SessionID: "selected", ThreadID: "selected-thread", CWD: cwd,
		AgentIDs: []string{"agent-restart"}, PendingRequestIDs: []string{"stale-permission"},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), MessageCount: 2,
	})

	command := exec.Command(os.Args[0], "-test.run=^TestProcessRestartRestoresAgentReplayAndLineage$")
	command.Env = append(os.Environ(), sessionRestartReplayHelperEnv+"="+cwd)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("restart helper failed: %v\n%s", err, output)
	}
}

func TestProjectGraphTerminalGenerationsRestoreOnceWithoutDispatch(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	outputDir := filepath.Join(cwd, "agent-output")
	producer := tools.NewAgentRunner(3)
	producer.SetOutputDir(outputDir)

	producer.SetExecutor(replayAgentExecutor{})
	if _, err := tools.RunAgent(t.Context(), producer, tools.AgentExecOptions{
		AgentID: "agent-completed", SessionID: "session-completed", ThreadID: "thread-completed",
		ParentSessionID: "selected", ParentThreadID: "selected-thread", Task: "complete",
	}); err != nil {
		t.Fatal(err)
	}
	writeProjectGraphChildMetadata(
		t, outputDir, "agent-completed", "session-completed", "thread-completed",
		"selected", "selected-thread", queryKernelStageForegroundChild,
	)

	producer.SetExecutor(failingReplayAgentExecutor{})
	if _, err := tools.RunAgent(t.Context(), producer, tools.AgentExecOptions{
		AgentID: "agent-failed", SessionID: "session-failed", ThreadID: "thread-failed",
		ParentSessionID: "selected", ParentThreadID: "selected-thread", Task: "fail",
	}); err == nil {
		t.Fatal("failed child unexpectedly succeeded")
	}
	writeProjectGraphChildMetadata(
		t, outputDir, "agent-failed", "session-failed", "thread-failed",
		"selected", "selected-thread", queryKernelStageForegroundChild,
	)

	started := make(chan struct{})
	producer.SetExecutor(liveRestoreAgentExecutor{started: started})
	childCtx, cancelChild := context.WithCancel(t.Context())
	t.Cleanup(cancelChild)
	if _, err := tools.RunAgentBackground(childCtx, producer, tools.AgentExecOptions{
		AgentID: "agent-aborted", SessionID: "session-aborted", ThreadID: "thread-aborted",
		ParentSessionID: "selected", ParentThreadID: "selected-thread", Task: "abort",
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := producer.AbortAgent("agent-aborted"); err != nil {
		t.Fatal(err)
	}
	waitForAgentStatus(t, producer, "agent-aborted", "aborted", 2*time.Second)
	writeProjectGraphChildMetadata(
		t, outputDir, "agent-aborted", "session-aborted", "thread-aborted",
		"selected", "selected-thread", queryKernelStageBackgroundChild,
	)

	parent := writeEngineSelectedSession(t, dir, "selected", "prompt")
	writeProjectGraphRootTestMetadata(t, parent, &session.SessionMetadataFull{
		SessionID: "selected", ThreadID: "selected-thread", CWD: cwd,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), MessageCount: 2,
	})
	fresh := tools.NewAgentRunner(3)
	fresh.SetOutputDir(outputDir)
	var executeCalls atomic.Int64
	fresh.SetExecutor(replayAgentExecutorFunc(func(_ context.Context, _ tools.AgentExecOptions) (*tools.AgentExecResult, error) {
		executeCalls.Add(1)
		return nil, errors.New("restore must not dispatch")
	}))
	query := NewQueryEngine(QueryEngineConfig{
		SessionID: "fresh", CWD: cwd, TranscriptDir: dir, AgentRunner: fresh,
	})
	t.Cleanup(query.Close)

	resumed, err := query.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID: "selected", CWD: cwd, TranscriptDir: dir, TranscriptPath: parent.Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantStatus := map[string]string{
		"agent-completed": "completed",
		"agent-failed":    "failed",
		"agent-aborted":   "aborted",
	}
	if len(resumed.RestoredAgents) != len(wantStatus) {
		t.Fatalf("restored Agents = %#v warnings=%#v", resumed.RestoredAgents, resumed.Warnings)
	}
	for _, restored := range resumed.RestoredAgents {
		if restored.Mode != string(ThreadModeReplayOnly) ||
			restored.Status != wantStatus[restored.AgentID] {
			t.Fatalf("restored Agent = %#v", restored)
		}
		detail, ok := query.AgentDetailSnapshot(restored.AgentID)
		if !ok || detail.Agent.Generation != 1 ||
			detail.Agent.ParentSessionID != "selected" ||
			detail.Agent.ParentThreadID != "selected-thread" ||
			len(detail.Thread.PendingInteractions) != 0 {
			t.Fatalf("restored detail = ok:%v %#v", ok, detail)
		}
	}
	if executeCalls.Load() != 0 {
		t.Fatalf("restore dispatched %d child executions", executeCalls.Load())
	}

	_, beforeThread, _, ok := query.runtimeState.AgentThreadSnapshot("agent-completed")
	if !ok {
		t.Fatal("completed replay missing before repeated resume")
	}
	if _, err := query.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID: "selected", CWD: cwd, TranscriptDir: dir, TranscriptPath: parent.Path(),
	}); err != nil {
		t.Fatal(err)
	}
	_, afterThread, _, ok := query.runtimeState.AgentThreadSnapshot("agent-completed")
	if !ok || afterThread.Revision != beforeThread.Revision ||
		len(afterThread.Messages) != len(beforeThread.Messages) {
		t.Fatalf("repeated restore changed terminal projection: before=%#v after=%#v", beforeThread, afterThread)
	}
	if executeCalls.Load() != 0 {
		t.Fatalf("repeated restore dispatched %d child executions", executeCalls.Load())
	}
}

func TestProjectGraphSessionOnlyOrphanConvergesToInertReplay(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	outputDir := filepath.Join(cwd, "agent-output")
	parent := writeEngineSelectedSession(t, dir, "selected", "prompt")
	writeProjectGraphRootTestMetadata(t, parent, &session.SessionMetadataFull{
		SessionID: "selected", ThreadID: "selected-thread", CWD: cwd,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), MessageCount: 2,
	})
	childDir := filepath.Join(outputDir, "transcripts")
	child := transcript.NewRecorder("orphan-session", childDir)
	if err := child.Replace([]*schema.Message{{Role: schema.User, Content: "admitted but not dispatched"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := session.WriteSessionMetadata(child, &session.SessionMetadataFull{
		SessionID: "orphan-session", ThreadID: "orphan-thread", AgentID: "orphan-agent",
		ParentSessionID: "selected", ParentThreadID: "selected-thread",
		QueryKernelVersion: queryKernelVersionProjectGraph,
		QueryKernelStage:   string(queryKernelStageForegroundChild),
		GoalBinding: &session.PersistedGoalBinding{
			Version: session.PersistedGoalBindingVersion,
			GoalID:  "untrusted-legacy-goal", ObjectiveRevision: 1,
			RootSessionID: "selected", RootThreadID: "selected-thread",
			GoalTurnID: "untrusted-legacy-turn",
		},
		Status: "running", CWD: cwd, CreatedAt: now, UpdatedAt: now, MessageCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(child.Path())
	if err != nil {
		t.Fatal(err)
	}

	runner := tools.NewAgentRunner(1)
	runner.SetOutputDir(outputDir)
	var executeCalls atomic.Int64
	runner.SetExecutor(replayAgentExecutorFunc(func(_ context.Context, _ tools.AgentExecOptions) (*tools.AgentExecResult, error) {
		executeCalls.Add(1)
		return nil, errors.New("orphan replay must not dispatch")
	}))
	query := NewQueryEngine(QueryEngineConfig{
		SessionID: "fresh", CWD: cwd, TranscriptDir: dir, AgentRunner: runner,
	})
	t.Cleanup(query.Close)
	resumed, err := query.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID: "selected", CWD: cwd, TranscriptDir: dir, TranscriptPath: parent.Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.RestoredAgents) != 1 ||
		resumed.RestoredAgents[0].AgentID != "orphan-agent" ||
		resumed.RestoredAgents[0].Mode != string(ThreadModeReplayOnly) ||
		resumed.RestoredAgents[0].Status != "aborted" ||
		!containsSessionWarning(resumed.Warnings, "project_graph_orphan") ||
		!containsSessionWarning(resumed.Warnings, "inferred generation 1") {
		t.Fatalf("orphan restore = %#v warnings=%#v", resumed.RestoredAgents, resumed.Warnings)
	}
	detail, ok := query.AgentDetailSnapshot("orphan-agent")
	if !ok || detail.Agent.Generation != 1 ||
		detail.Agent.GoalID != "" ||
		detail.Agent.GoalObjectiveRevision != 0 ||
		detail.Agent.GoalRootSessionID != "" ||
		detail.Agent.GoalRootThreadID != "" ||
		detail.Agent.GoalTurnID != "" ||
		detail.Agent.Error != "project_graph_orphan: child Session committed before durable Agent metadata" ||
		detail.Thread.Status != RuntimeThreadAborted ||
		len(detail.Thread.PendingInteractions) != 0 ||
		len(detail.Messages) != 1 ||
		detail.Messages[0].Content != "admitted but not dispatched" {
		t.Fatalf("orphan detail = ok:%v %#v", ok, detail)
	}
	if _, _, err := runner.SendOrResumeAgentMessage("orphan-agent", tools.MessagePayload{Content: "continue"}); err == nil {
		t.Fatal("orphan replay unexpectedly became controllable")
	}
	if executeCalls.Load() != 0 {
		t.Fatalf("orphan restore dispatched %d child executions", executeCalls.Load())
	}
	after, err := os.ReadFile(child.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("orphan recovery mutated the admitted child transcript")
	}
}

func TestProjectGraphLiveAttachRejectsDurableGenerationMismatch(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	outputDir := filepath.Join(cwd, "agent-output")
	runner := tools.NewAgentRunner(1)
	runner.SetOutputDir(outputDir)
	started := make(chan struct{})
	runner.SetExecutor(liveRestoreAgentExecutor{started: started})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = runner.Shutdown(shutdownCtx)
	})
	if _, err := tools.RunAgentBackground(ctx, runner, tools.AgentExecOptions{
		AgentID: "agent-live", SessionID: "agent-session", ThreadID: "agent-thread",
		ParentSessionID: "selected", ParentThreadID: "selected-thread", Task: "wait",
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	metadataPath := filepath.Join(outputDir, "agents", "agent-live.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	persisted["generation"] = float64(2)
	data, err = json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	parent := writeEngineSelectedSession(t, dir, "selected", "prompt")
	writeProjectGraphRootTestMetadata(t, parent, &session.SessionMetadataFull{
		SessionID: "selected", ThreadID: "selected-thread", CWD: cwd,
		AgentIDs: []string{"agent-live"}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), MessageCount: 2,
	})
	query := NewQueryEngine(QueryEngineConfig{
		SessionID: "fresh", CWD: cwd, TranscriptDir: dir, AgentRunner: runner,
	})
	t.Cleanup(query.Close)
	resumed, err := query.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID: "selected", CWD: cwd, TranscriptDir: dir, TranscriptPath: parent.Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.RestoredAgents) != 1 ||
		resumed.RestoredAgents[0].Mode != string(ThreadModeReplayOnly) ||
		resumed.RestoredAgents[0].Status != "aborted" ||
		!containsSessionWarning(resumed.Warnings, "generation differs") {
		t.Fatalf("generation mismatch restore = %#v warnings=%#v", resumed.RestoredAgents, resumed.Warnings)
	}
}

func TestProjectGraphPartialAgentMetadataDoesNotBecomeOrphan(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	outputDir := filepath.Join(cwd, "agent-output")
	producer := tools.NewAgentRunner(1)
	producer.SetOutputDir(outputDir)
	producer.SetExecutor(replayAgentExecutor{})
	if _, err := tools.RunAgent(t.Context(), producer, tools.AgentExecOptions{
		AgentID: "partial-agent", SessionID: "partial-session", ThreadID: "partial-thread",
		ParentSessionID: "selected", ParentThreadID: "selected-thread", Task: "complete",
	}); err != nil {
		t.Fatal(err)
	}
	writeProjectGraphChildMetadata(
		t, outputDir, "partial-agent", "partial-session", "partial-thread",
		"selected", "selected-thread", queryKernelStageForegroundChild,
	)
	metadataPath := filepath.Join(outputDir, "agents", "partial-agent.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	delete(persisted, "generation")
	data, err = json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	parent := writeEngineSelectedSession(t, dir, "selected", "prompt")
	writeProjectGraphRootTestMetadata(t, parent, &session.SessionMetadataFull{
		SessionID: "selected", ThreadID: "selected-thread", CWD: cwd,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), MessageCount: 2,
	})
	fresh := tools.NewAgentRunner(1)
	fresh.SetOutputDir(outputDir)
	query := NewQueryEngine(QueryEngineConfig{
		SessionID: "fresh", CWD: cwd, TranscriptDir: dir, AgentRunner: fresh,
	})
	t.Cleanup(query.Close)
	resumed, err := query.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID: "selected", CWD: cwd, TranscriptDir: dir, TranscriptPath: parent.Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.RestoredAgents) != 0 ||
		!containsSessionWarning(resumed.Warnings, "execution generation is missing") ||
		containsSessionWarning(resumed.Warnings, "project_graph_orphan") {
		t.Fatalf("partial Agent metadata restore = %#v warnings=%#v", resumed.RestoredAgents, resumed.Warnings)
	}
	if _, _, _, ok := query.runtimeState.AgentThreadSnapshot("partial-agent"); ok {
		t.Fatal("partial Agent metadata created a runtime projection")
	}
}

func TestProjectGraphOrphanDiscoveryRejectsNonRegularAndConflictingLineage(t *testing.T) {
	outputDir := t.TempDir()
	childDir := filepath.Join(outputDir, "transcripts")
	conflicting := transcript.NewRecorder("conflicting-session", childDir)
	if err := conflicting.Replace([]*schema.Message{{Role: schema.User, Content: "wrong parent"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := session.WriteSessionMetadata(conflicting, &session.SessionMetadataFull{
		SessionID: "conflicting-session", ThreadID: "conflicting-thread", AgentID: "conflicting-agent",
		AgentGeneration: 1, ParentSessionID: "selected", ParentThreadID: "other-thread",
		QueryKernelVersion: queryKernelVersionProjectGraph,
		QueryKernelStage:   string(queryKernelStageForegroundChild),
		Status:             "running", CreatedAt: now, UpdatedAt: now, MessageCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := conflicting.Close(); err != nil {
		t.Fatal(err)
	}

	externalDir := t.TempDir()
	external := transcript.NewRecorder("symlink-session", externalDir)
	if err := external.Replace([]*schema.Message{{Role: schema.User, Content: "outside runner storage"}}); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteSessionMetadata(external, &session.SessionMetadataFull{
		SessionID: "symlink-session", ThreadID: "symlink-thread", AgentID: "symlink-agent",
		AgentGeneration: 1, ParentSessionID: "selected", ParentThreadID: "selected-thread",
		QueryKernelVersion: queryKernelVersionProjectGraph,
		QueryKernelStage:   string(queryKernelStageBackgroundChild),
		Status:             "running", CreatedAt: now, UpdatedAt: now, MessageCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := external.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external.Path(), filepath.Join(childDir, "symlink-session.jsonl")); err != nil {
		t.Fatal(err)
	}

	discovered, warnings := discoverDurableProjectGraphChildren(
		childDir,
		durableChildParent{sessionID: "selected", threadID: "selected-thread"},
	)
	if len(discovered) != 0 ||
		!containsSessionWarning(warnings, "non-regular") ||
		!containsSessionWarning(warnings, "conflicting parent lineage") {
		t.Fatalf("discovered=%#v warnings=%#v", discovered, warnings)
	}
}

func TestProjectGraphDuplicateAgentIdentityRejectsEveryCandidate(t *testing.T) {
	childDir := filepath.Join(t.TempDir(), "transcripts")
	now := time.Now().UTC()
	for index, sessionID := range []string{"duplicate-a", "duplicate-b"} {
		recorder := transcript.NewRecorder(sessionID, childDir)
		if err := recorder.Replace([]*schema.Message{{
			Role: schema.User, Content: "duplicate",
		}}); err != nil {
			t.Fatal(err)
		}
		if err := session.WriteSessionMetadata(recorder, &session.SessionMetadataFull{
			SessionID: sessionID, ThreadID: "thread-" + sessionID, AgentID: "duplicate-agent",
			AgentGeneration: 1, ParentSessionID: "selected", ParentThreadID: "selected-thread",
			QueryKernelVersion: queryKernelVersionProjectGraph,
			QueryKernelStage:   string(queryKernelStageForegroundChild),
			Status:             "running",
			CreatedAt:          now.Add(time.Duration(index) * time.Second),
			UpdatedAt:          now.Add(time.Duration(index) * time.Second),
			MessageCount:       1,
		}); err != nil {
			t.Fatal(err)
		}
		if err := recorder.Close(); err != nil {
			t.Fatal(err)
		}
	}

	discovered, warnings := discoverDurableProjectGraphChildren(
		childDir,
		durableChildParent{sessionID: "selected", threadID: "selected-thread"},
	)
	if len(discovered) != 0 ||
		!containsSessionWarning(warnings, "ignored all") ||
		!containsSessionWarning(warnings, "duplicate Agent identity duplicate-agent") {
		t.Fatalf("discovered=%#v warnings=%#v", discovered, warnings)
	}
}

func TestProjectGraphNegativeAdmissionGenerationFailsClosed(t *testing.T) {
	childDir := filepath.Join(t.TempDir(), "transcripts")
	recorder := transcript.NewRecorder("negative-generation", childDir)
	if err := recorder.Replace([]*schema.Message{{
		Role: schema.User, Content: "invalid generation",
	}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := session.WriteSessionMetadata(recorder, &session.SessionMetadataFull{
		SessionID: "negative-generation", ThreadID: "negative-thread", AgentID: "negative-agent",
		AgentGeneration: -1, ParentSessionID: "selected", ParentThreadID: "selected-thread",
		QueryKernelVersion: queryKernelVersionProjectGraph,
		QueryKernelStage:   string(queryKernelStageForegroundChild),
		Status:             "running", CreatedAt: now, UpdatedAt: now, MessageCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}

	discovered, warnings := discoverDurableProjectGraphChildren(
		childDir,
		durableChildParent{sessionID: "selected", threadID: "selected-thread"},
	)
	if len(discovered) != 0 ||
		!containsSessionWarning(warnings, "invalid ProjectGraph child Session negative-generation") ||
		containsSessionWarning(warnings, "inferred generation 1") {
		t.Fatalf("discovered=%#v warnings=%#v", discovered, warnings)
	}
}

func TestP242aRootRestoreRequiresExactResumedChildGoalGeneration(t *testing.T) {
	cwd := t.TempDir()
	rootTranscriptDir := filepath.Join(cwd, "root-transcripts")
	outputDir := filepath.Join(cwd, "agent-output")
	const (
		rootSessionID  = "goal-root-session"
		rootThreadID   = "goal-root-thread"
		childAgentID   = "goal-child-agent"
		childSessionID = "goal-child-session"
		childThreadID  = "goal-child-thread"
		parentToolID   = "goal-child-tool"
	)

	secondStarted := make(chan struct{})
	producer := tools.NewAgentRunner(1)
	producer.SetOutputDir(outputDir)
	producer.SetExecutor(replayAgentExecutorFunc(func(
		ctx context.Context,
		opts tools.AgentExecOptions,
	) (*tools.AgentExecResult, error) {
		if opts.Generation == 2 {
			close(secondStarted)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return &tools.AgentExecResult{Result: "generation one complete"}, nil
	}))
	if _, err := tools.RunAgent(t.Context(), producer, tools.AgentExecOptions{
		AgentID: childAgentID, SessionID: childSessionID,
		ThreadID: childThreadID, ParentSessionID: rootSessionID,
		ParentThreadID: rootThreadID, ToolUseID: parentToolID,
		Task: "generation one",
	}); err != nil {
		t.Fatal(err)
	}
	if _, disposition, err := producer.SendOrResumeAgentMessage(
		childAgentID,
		tools.MessagePayload{Content: "generation two"},
	); err != nil || disposition != "resumed" {
		t.Fatalf("resume child = disposition %q, err %v", disposition, err)
	}
	<-secondStarted
	if err := producer.AbortAgent(childAgentID); err != nil {
		t.Fatal(err)
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(
		context.Background(),
		2*time.Second,
	)
	defer cancelShutdown()
	if err := producer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	durable, err := producer.LoadPersistedAgentSnapshot(childAgentID)
	if err != nil || durable.ExecutionGeneration() != 2 {
		t.Fatalf("durable resumed child = %#v, err %v", durable, err)
	}

	parent := writeEngineSelectedSession(
		t,
		rootTranscriptDir,
		rootSessionID,
		"restore Goal descendants",
	)
	now := time.Now().UTC()
	writeProjectGraphRootTestMetadata(t, parent, &session.SessionMetadataFull{
		SessionID: rootSessionID, ThreadID: rootThreadID, CWD: cwd,
		AgentIDs: []string{childAgentID},
		GoalState: &session.PersistedGoalState{
			Version: session.PersistedGoalStateVersion,
			GoalID:  "goal-restore", Objective: "restore exact generation",
			ObjectiveRevision: 1, Status: string(goalStatusPaused),
			Revision: 4, LastGoalTurnID: "goal-turn-2",
			CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
		},
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now, MessageCount: 2,
	})
	childRecorder := transcript.NewRecorder(
		childSessionID,
		filepath.Join(outputDir, "transcripts"),
	)
	if err := childRecorder.Replace([]*schema.Message{{
		Role: schema.User, Content: "generation two",
	}}); err != nil {
		t.Fatal(err)
	}
	writeChildMetadata := func(generation int64) {
		t.Helper()
		if err := session.WriteSessionMetadata(
			childRecorder,
			&session.SessionMetadataFull{
				SessionID: childSessionID, ThreadID: childThreadID,
				AgentID: childAgentID, AgentGeneration: generation,
				ParentSessionID: rootSessionID, ParentThreadID: rootThreadID,
				ParentToolUseID:    parentToolID,
				QueryKernelVersion: queryKernelVersionProjectGraph,
				QueryKernelStage:   string(queryKernelStageBackgroundChild),
				Status:             "aborted",
				GoalBinding: &session.PersistedGoalBinding{
					Version: session.PersistedGoalBindingVersion,
					GoalID:  "goal-restore", ObjectiveRevision: 1,
					RootSessionID: rootSessionID, RootThreadID: rootThreadID,
					GoalTurnID: "goal-turn-2",
				},
				CWD: cwd, CreatedAt: now.Add(-time.Minute),
				UpdatedAt: now, MessageCount: 1,
			},
		); err != nil {
			t.Fatal(err)
		}
		if err := childRecorder.Flush(); err != nil {
			t.Fatal(err)
		}
	}

	writeChildMetadata(3)
	mismatchRunner := tools.NewAgentRunner(1)
	mismatchRunner.SetOutputDir(outputDir)
	mismatch := NewQueryEngine(QueryEngineConfig{
		SessionID: "mismatch-process", CWD: cwd,
		TranscriptDir: rootTranscriptDir, AgentRunner: mismatchRunner,
	})
	mismatchResult, err := mismatch.ResumeSessionInfo(
		t.Context(),
		session.SessionInfo{
			SessionID: rootSessionID, CWD: cwd,
			TranscriptDir:  rootTranscriptDir,
			TranscriptPath: parent.Path(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatchResult.RestoredAgents) != 0 ||
		!containsSessionWarning(
			mismatchResult.Warnings,
			"identity conflicts with its ProjectGraph child Session",
		) {
		t.Fatalf(
			"mismatched generation restore = %#v warnings=%#v",
			mismatchResult.RestoredAgents,
			mismatchResult.Warnings,
		)
	}
	mismatch.Close()

	writeChildMetadata(2)
	restoreRunner := tools.NewAgentRunner(1)
	restoreRunner.SetOutputDir(outputDir)
	restoredEngine := NewQueryEngine(QueryEngineConfig{
		SessionID: "restored-process", CWD: cwd,
		TranscriptDir: rootTranscriptDir, AgentRunner: restoreRunner,
	})
	t.Cleanup(restoredEngine.Close)
	restored, err := restoredEngine.ResumeSessionInfo(
		t.Context(),
		session.SessionInfo{
			SessionID: rootSessionID, CWD: cwd,
			TranscriptDir:  rootTranscriptDir,
			TranscriptPath: parent.Path(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.RestoredAgents) != 1 ||
		restored.RestoredAgents[0].AgentID != childAgentID {
		t.Fatalf(
			"exact generation restore = %#v warnings=%#v",
			restored.RestoredAgents,
			restored.Warnings,
		)
	}
	detail, ok := restoredEngine.AgentDetailSnapshot(childAgentID)
	if !ok ||
		detail.Agent.Generation != 2 ||
		detail.Agent.GoalID != "goal-restore" ||
		detail.Agent.GoalObjectiveRevision != 1 ||
		detail.Agent.GoalRootSessionID != rootSessionID ||
		detail.Agent.GoalRootThreadID != rootThreadID ||
		detail.Agent.GoalTurnID != "goal-turn-2" {
		t.Fatalf("restored Goal-bound generation = ok:%v %#v", ok, detail.Agent)
	}
}

func TestProjectGraphBoundedDiscoveryKeepsNewestTranscript(t *testing.T) {
	childDir := filepath.Join(t.TempDir(), "transcripts")
	now := time.Now().UTC()
	for _, fixture := range []struct {
		sessionID string
		agentID   string
		modified  time.Time
	}{
		{sessionID: "older-session", agentID: "older-agent", modified: now.Add(-time.Hour)},
		{sessionID: "newer-session", agentID: "newer-agent", modified: now},
	} {
		recorder := transcript.NewRecorder(fixture.sessionID, childDir)
		if err := recorder.Replace([]*schema.Message{{
			Role: schema.User, Content: fixture.sessionID,
		}}); err != nil {
			t.Fatal(err)
		}
		if err := session.WriteSessionMetadata(recorder, &session.SessionMetadataFull{
			SessionID: fixture.sessionID, ThreadID: "thread-" + fixture.sessionID,
			AgentID: fixture.agentID, AgentGeneration: 1,
			ParentSessionID: "selected", ParentThreadID: "selected-thread",
			QueryKernelVersion: queryKernelVersionProjectGraph,
			QueryKernelStage:   string(queryKernelStageBackgroundChild),
			Status:             "running", CreatedAt: fixture.modified,
			UpdatedAt: fixture.modified, MessageCount: 1,
		}); err != nil {
			t.Fatal(err)
		}
		if err := recorder.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(recorder.Path(), fixture.modified, fixture.modified); err != nil {
			t.Fatal(err)
		}
	}

	discovered, warnings := discoverDurableProjectGraphChildrenWithLimit(
		childDir,
		durableChildParent{sessionID: "selected", threadID: "selected-thread"},
		1,
	)
	if len(discovered) != 1 ||
		discovered[0].metadata.AgentID != "newer-agent" ||
		!containsSessionWarning(warnings, "newest bounded set") {
		t.Fatalf("discovered=%#v warnings=%#v", discovered, warnings)
	}
}

func writeProjectGraphChildMetadata(
	t *testing.T,
	outputDir, agentID, sessionID, threadID, parentSessionID, parentThreadID string,
	stage queryKernelStage,
) {
	t.Helper()
	recorder := transcript.NewRecorder(sessionID, filepath.Join(outputDir, "transcripts"))
	loaded, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := session.WriteSessionMetadata(recorder, &session.SessionMetadataFull{
		SessionID: sessionID, ThreadID: threadID, AgentID: agentID, AgentGeneration: 1,
		ParentSessionID: parentSessionID, ParentThreadID: parentThreadID,
		QueryKernelVersion: queryKernelVersionProjectGraph,
		QueryKernelStage:   string(stage),
		Status:             "running", CreatedAt: now, UpdatedAt: now, MessageCount: len(loaded.Messages),
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertProcessRestartAgentReplay(t *testing.T, cwd string) {
	t.Helper()
	dir := filepath.Join(cwd, "transcripts")
	fresh := tools.NewAgentRunner(1)
	fresh.SetOutputDir(filepath.Join(cwd, "agent-output"))
	query := NewQueryEngine(QueryEngineConfig{
		SessionID: "new-process", CWD: cwd, TranscriptDir: dir, AgentRunner: fresh,
	})
	t.Cleanup(query.Close)

	resumed, err := query.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID: "selected", CWD: cwd, TranscriptDir: dir,
		TranscriptPath: transcript.NewRecorder("selected", dir).Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.RestoredAgents) != 1 || resumed.RestoredAgents[0].Mode != string(ThreadModeReplayOnly) ||
		resumed.RestoredAgents[0].Status != "aborted" || len(resumed.ActionableRequestIDs) != 0 ||
		!containsSessionWarning(resumed.Warnings, "ignored 1 persisted request") {
		t.Fatalf("restart restored Agents = %#v warnings=%#v", resumed.RestoredAgents, resumed.Warnings)
	}
	detail, ok := query.AgentDetailSnapshot("agent-restart")
	if !ok || detail.Agent.ParentSessionID != "selected" || detail.Agent.ParentThreadID != "selected-thread" ||
		detail.Agent.Status != "aborted" || detail.Thread.Status != RuntimeThreadAborted || len(detail.Messages) != 1 ||
		detail.Messages[0].Role != string(schema.User) || detail.Messages[0].Content != "inspect after restart" ||
		len(detail.Thread.PendingInteractions) != 0 {
		t.Fatalf("restart detail = ok:%v %#v", ok, detail)
	}
}
