package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/engine/worktree"
	"github.com/cloudwego/eino/schema"
)

type fakeAgentExecutor struct {
	onExecute func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error)
}

func (f fakeAgentExecutor) ExecuteAgent(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
	return f.onExecute(ctx, opts)
}

type fakeAgentWorktreeLifecycle struct {
	mu sync.Mutex

	createRecord   worktree.Record
	createErr      error
	removeRecord   worktree.Record
	removeErr      error
	recoverRecord  worktree.Record
	recoverErr     error
	createCalls    []worktree.CreateRequest
	removeCalls    []string
	removeOwners   []worktree.Owner
	recoverCalls   []string
	recoverOwners  []worktree.Owner
	recoverEntered chan struct{}
	recoverRelease <-chan struct{}
	createEntered  chan struct{}
	createRelease  <-chan struct{}
}

func (f *fakeAgentWorktreeLifecycle) Create(
	ctx context.Context,
	request worktree.CreateRequest,
) (worktree.Record, error) {
	f.mu.Lock()
	f.createCalls = append(f.createCalls, request)
	record := f.createRecord
	err := f.createErr
	entered := f.createEntered
	release := f.createRelease
	f.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return worktree.Record{}, ctx.Err()
		}
	}
	record.Owner = request.Owner
	return record, err
}

func (f *fakeAgentWorktreeLifecycle) Remove(
	_ context.Context,
	id string,
	owner worktree.Owner,
) (worktree.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeCalls = append(f.removeCalls, id)
	f.removeOwners = append(f.removeOwners, owner)
	return f.removeRecord, f.removeErr
}

func (f *fakeAgentWorktreeLifecycle) RecoverForContinuation(
	_ context.Context,
	id string,
	owner worktree.Owner,
) (worktree.Record, error) {
	f.mu.Lock()
	f.recoverCalls = append(f.recoverCalls, id)
	f.recoverOwners = append(f.recoverOwners, owner)
	record := f.recoverRecord
	err := f.recoverErr
	entered := f.recoverEntered
	release := f.recoverRelease
	f.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if release != nil {
		<-release
	}
	record.Owner = owner
	return record, err
}

type worktreeBoundExecutor struct {
	fakeAgentExecutor
	lifecycle       *fakeAgentWorktreeLifecycle
	sourceDir       string
	parentSessionID string
}

type rebindingWorktreeExecutor struct {
	fakeAgentExecutor
	mu       sync.RWMutex
	snapshot AgentWorktreeBindingSnapshot
}

func (e *rebindingWorktreeExecutor) AgentWorktreeBindingSnapshot() AgentWorktreeBindingSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.snapshot
}

func (e *rebindingWorktreeExecutor) rebind(
	snapshot AgentWorktreeBindingSnapshot,
) {
	e.mu.Lock()
	e.snapshot = snapshot
	e.mu.Unlock()
}

func (e worktreeBoundExecutor) AgentWorktreeLifecycle() AgentWorktreeLifecycle {
	return e.lifecycle
}

func (e worktreeBoundExecutor) AgentWorktreeSourceDir() string {
	return e.sourceDir
}

func (e worktreeBoundExecutor) AgentWorktreeBindingSnapshot() AgentWorktreeBindingSnapshot {
	return AgentWorktreeBindingSnapshot{
		Lifecycle:       e.lifecycle,
		SourceDir:       e.sourceDir,
		ParentSessionID: e.parentSessionID,
	}
}

func readyAgentWorktreeRecord(id, path, branch string) worktree.Record {
	return worktree.Record{
		Version:            worktree.RecordVersion,
		ID:                 id,
		RepositoryIdentity: "/repo/.git",
		RepoRoot:           "/repo",
		Path:               path,
		Branch:             branch,
		BaseCommit:         "abc123",
		State:              worktree.StateReady,
		Revision:           2,
		CreatedAt:          time.Unix(1, 0),
		UpdatedAt:          time.Unix(1, 0),
	}
}

func TestForegroundAndBackgroundAgentsPreserveUnlimitedMaxTurns(t *testing.T) {
	runner := NewAgentRunner(2)
	runner.SetOutputDir(t.TempDir())
	executedTurns := make(chan int, 2)
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		if opts.MaxTurns != 0 {
			return nil, fmt.Errorf("MaxTurns = %d, want unlimited (0)", opts.MaxTurns)
		}
		executedTurns <- 101
		return &AgentExecResult{Result: "done", TurnCount: 101}, nil
	}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	foreground, err := RunAgent(ctx, runner, AgentExecOptions{Task: "foreground"})
	if err != nil {
		t.Fatal(err)
		return
	}
	if foreground.TurnCount != 101 {
		t.Fatalf("foreground turns = %d, want 101", foreground.TurnCount)
	}

	background, err := RunAgentBackground(ctx, runner, AgentExecOptions{Task: "background"})
	if err != nil {
		t.Fatal(err)
		return
	}
	completed := waitForAgentStatus(t, runner, background.ID, "completed")
	if completed.Status != "completed" {
		t.Fatalf("background status = %q", completed.Status)
	}
	for i := 0; i < 2; i++ {
		select {
		case turns := <-executedTurns:
			if turns != 101 {
				t.Fatalf("executed turns = %d, want 101", turns)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestAgentExecutionModeIsProcessLocalAndEntrypointExact(t *testing.T) {
	runner := NewAgentRunner(2)
	runner.SetOutputDir(t.TempDir())
	executed := make(chan AgentExecOptions, 2)
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(
		_ context.Context,
		opts AgentExecOptions,
	) (*AgentExecResult, error) {
		executed <- opts
		return &AgentExecResult{Result: "done"}, nil
	}})

	if _, err := RunAgent(
		context.Background(),
		runner,
		AgentExecOptions{Task: "foreground"},
	); err != nil {
		t.Fatal(err)
	}
	foreground := <-executed
	if !foreground.IsForegroundExecution() {
		t.Fatal("foreground entrypoint did not carry its process-local marker")
	}
	if foreground.IsBackgroundExecution() {
		t.Fatal("foreground entrypoint carried both execution markers")
	}

	background, err := RunAgentBackground(
		context.Background(),
		runner,
		AgentExecOptions{Task: "background"},
	)
	if err != nil {
		t.Fatal(err)
	}
	backgroundOpts := <-executed
	if backgroundOpts.IsForegroundExecution() {
		t.Fatal("background entrypoint inherited the foreground marker")
	}
	if !backgroundOpts.IsBackgroundExecution() {
		t.Fatal("background entrypoint did not carry its process-local marker")
	}
	waitForAgentStatus(t, runner, background.ID, "completed")

	var unknown AgentExecOptions
	if unknown.IsForegroundExecution() || unknown.IsBackgroundExecution() {
		t.Fatal("zero-value options unexpectedly select a child execution mode")
	}

	encoded, err := json.Marshal(foreground)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "executionMode") ||
		strings.Contains(string(encoded), "execution_mode") {
		t.Fatalf("process-local marker leaked into durable JSON: %s", encoded)
	}
}

func TestPersistAgentTranscriptStatePreservesSessionAuthority(t *testing.T) {
	recorder := transcript.NewRecorder("child", t.TempDir())
	if err := recorder.Replace([]*schema.Message{{
		Role: schema.User, Content: "seed",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMetadata(
		"session",
		`{"query_kernel_version":"project_graph/v1","query_kernel_canary_stage":"foreground_child"}`,
	); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordLifecycleBoundary(
		transcript.LifecycleSessionStart,
		[]*schema.Message{{Role: schema.User, Content: "seed"}},
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}

	wantMessages := []*schema.Message{
		{Role: schema.User, Content: "seed"},
		{Role: schema.Assistant, Content: "done"},
	}
	if err := persistAgentTranscriptState(recorder, wantMessages); err != nil {
		t.Fatal(err)
	}
	loaded, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Metadata) != 1 ||
		!strings.Contains(loaded.Metadata[0].Value, "foreground_child") {
		t.Fatalf("session metadata was not preserved: %#v", loaded.Metadata)
	}
	if len(loaded.LifecycleBoundaries) != 2 ||
		loaded.LifecycleBoundaries[1].Kind != transcript.LifecycleCheckpoint {
		t.Fatalf(
			"lifecycle boundaries = %#v",
			loaded.LifecycleBoundaries,
		)
	}
	if len(loaded.Messages) != 2 ||
		loaded.Messages[1].Content != "done" {
		t.Fatalf("active transcript = %#v", loaded.Messages)
	}
}

func TestAgentRunnerShutdownCancelsJoinsAndRejectsLaunches(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	entered := make(chan struct{})
	returned := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, _ AgentExecOptions) (*AgentExecResult, error) {
		close(entered)
		<-ctx.Done()
		close(returned)
		return nil, ctx.Err()
	}})

	started, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{Task: "wait for shutdown"})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case <-returned:
	default:
		t.Fatal("shutdown returned before executor joined")
	}
	snapshot, ok := runner.GetAgentSnapshot(started.ID)
	if !ok || snapshot.Status != "aborted" {
		t.Fatalf("shutdown Agent snapshot = %#v, found=%v", snapshot, ok)
	}
	if _, err := RunAgent(context.Background(), runner, AgentExecOptions{Task: "late launch"}); err == nil || !strings.Contains(err.Error(), "runner is closed") {
		t.Fatalf("launch after shutdown error = %v", err)
	}
}

func TestAgentRunnerShutdownTimeoutDoesNotSettleGenerationBeforeExecutorReturns(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	entered := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(
		ctx context.Context,
		_ AgentExecOptions,
	) (*AgentExecResult, error) {
		close(entered)
		<-release
		close(returned)
		return nil, ctx.Err()
	}})

	started, err := RunAgentBackground(
		context.Background(),
		runner,
		AgentExecOptions{Task: "ignore cancellation until released"},
	)
	if err != nil {
		t.Fatal(err)
	}
	<-entered

	shutdownCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.Shutdown(shutdownCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown timeout error = %v, want context cancellation", err)
	}
	snapshot, ok := runner.GetAgentSnapshot(started.ID)
	if !ok || snapshot.Status != "running" || !snapshot.CompletedAt.IsZero() {
		t.Fatalf(
			"timed-out join settled generation before executor return: %#v, found=%v",
			snapshot,
			ok,
		)
	}
	select {
	case <-returned:
		t.Fatal("executor returned before its deterministic release")
	default:
	}

	close(release)
	finalCtx, finalCancel := context.WithTimeout(context.Background(), time.Second)
	defer finalCancel()
	if err := runner.Shutdown(finalCtx); err != nil {
		t.Fatalf("join after executor release: %v", err)
	}
	snapshot, ok = runner.GetAgentSnapshot(started.ID)
	if !ok || snapshot.Status != "aborted" || snapshot.CompletedAt.IsZero() {
		t.Fatalf(
			"eventual executor return did not settle generation: %#v, found=%v",
			snapshot,
			ok,
		)
	}
}

func TestAgentRunnerRegistersLifecycleForSyncExecution(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())

	entered := make(chan struct{})
	release := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		close(entered)
		<-release
		return &AgentExecResult{Result: "done"}, nil
	}})

	done := make(chan error, 1)
	go func() {
		_, err := RunAgent(context.Background(), runner, AgentExecOptions{
			Task:        "inspect lifecycle",
			Description: "Inspect lifecycle",
			ToolUseID:   "toolu_123",
		})
		done <- err
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("executor was not entered")
	}

	agent := onlyTrackedAgent(t, runner)
	if agent.Type != "local_agent" {
		t.Fatalf("expected local_agent type, got %q", agent.Type)
	}
	if agent.Status != "running" {
		t.Fatalf("expected running status while executor is active, got %q", agent.Status)
	}
	if agent.Description != "Inspect lifecycle" {
		t.Fatalf("expected description to be recorded, got %q", agent.Description)
	}
	if agent.ToolUseID != "toolu_123" {
		t.Fatalf("expected tool use id to be recorded, got %q", agent.ToolUseID)
	}
	if agent.OutputFile == "" {
		t.Fatal("expected output file metadata to be set at registration")
	}
	if agent.OutputOffset != 0 {
		t.Fatalf("expected zero output offset before completion, got %d", agent.OutputOffset)
	}
	if agent.Notified {
		t.Fatal("expected new sync lifecycle state to start unnotified")
	}
	if agent.StartedAt.IsZero() {
		t.Fatal("expected start time to be set")
	}
	if !agent.CompletedAt.IsZero() {
		t.Fatalf("expected no completed time before terminal transition, got %v", agent.CompletedAt)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("RunAgent returned error: %v", err)
		return
	}
}

func TestNewAgentRunnerAvoidsTemporaryExecutablePathCollision(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)

	runner := NewAgentRunner(1)
	want := filepath.Join(cacheDir, "eino-agent", "agent-output")
	if runner.outputDir != want {
		t.Fatalf("outputDir = %q, want %q", runner.outputDir, want)
	}
	if runner.outputDir == filepath.Join(os.TempDir(), "eino-agent", "agent-output") {
		t.Fatal("default output directory still collides with /tmp/eino-agent executables")
	}
}

func TestAgentRunnerRejectsInvalidOutputDirectoryBeforeExecution(t *testing.T) {
	root := t.TempDir()
	blockingFile := filepath.Join(root, "eino-agent")
	if err := os.WriteFile(blockingFile, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
		return
	}

	runner := NewAgentRunner(1)
	runner.SetOutputDir(filepath.Join(blockingFile, "agent-output"))
	executed := false
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		executed = true
		return &AgentExecResult{Result: "should not run"}, nil
	}})

	_, err := RunAgent(context.Background(), runner, AgentExecOptions{Task: "inspect"})
	if err == nil || !strings.Contains(err.Error(), "prepare output dir") {
		t.Fatalf("RunAgent error = %v, want output directory preparation error", err)
		return
	}
	if executed {
		t.Fatal("executor ran before output directory validation")
	}
}

func TestAgentRunnerMarksLifecycleCompletedOnSuccess(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		return &AgentExecResult{
			Result:     "successful result",
			TurnCount:  2,
			TokensUsed: 17,
			ToolsUsed:  []string{"Read"},
			Messages: []*schema.Message{
				{Role: schema.User, Content: "do work"},
				{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
					ID:   "call_read",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "Read",
						Arguments: `{"file_path":"README.md"}`,
					},
				}}},
				{Role: schema.Tool, ToolCallID: "call_read", ToolName: "Read", Content: "file contents"},
				{Role: schema.Assistant, Content: "successful result"},
			},
		}, nil
	}})

	result, err := RunAgent(context.Background(), runner, AgentExecOptions{Task: "do work", Description: "Do work"})
	if err != nil {
		t.Fatalf("RunAgent returned error: %v", err)
		return
	}
	if result.Result != "successful result" {
		t.Fatalf("unexpected result: %q", result.Result)
	}

	agent := onlyTrackedAgent(t, runner)
	if agent.Status != "completed" {
		t.Fatalf("expected completed status, got %q", agent.Status)
	}
	if agent.Result != "successful result" {
		t.Fatalf("expected stored result, got %q", agent.Result)
	}
	if agent.Error != nil {
		t.Fatalf("expected no stored error, got %v", agent.Error)
		return
	}
	if agent.CompletedAt.IsZero() || agent.CompletedAt.Before(agent.StartedAt) {
		t.Fatalf("expected completed time after start, start=%v completed=%v", agent.StartedAt, agent.CompletedAt)
	}
	if len(agent.Messages) != 4 {
		t.Fatalf("expected executor messages to be recorded, got %#v", agent.Messages)
	}
	if len(agent.Messages[1].ToolCalls) != 1 || agent.Messages[1].ToolCalls[0].Function.Name != "Read" {
		t.Fatalf("expected executor assistant tool call history to be retained, got %#v", agent.Messages[1])
	}
	if agent.Messages[2].Role != schema.Tool || agent.Messages[2].ToolName != "Read" {
		t.Fatalf("expected executor tool result history to be retained, got %#v", agent.Messages[2])
	}
	if agent.Messages[3].Role != schema.Assistant || agent.Messages[3].Content != "successful result" {
		t.Fatalf("expected final assistant message from executor history, got %#v", agent.Messages[3])
	}
}

func TestAgentRunnerWorktreeIsolationSetsExecutorCWDAndCleansCleanWorktree(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	worktreePath := "/repo/.yhc/worktrees/v1/trees/worktree-1"
	ready := readyAgentWorktreeRecord(
		"worktree-1",
		worktreePath,
		"yhc/worktree/worktree-1",
	)
	removed := ready
	removed.State = worktree.StateRemoved
	lifecycle := &fakeAgentWorktreeLifecycle{
		createRecord: ready,
		removeRecord: removed,
	}
	var seen AgentExecOptions
	runner.SetExecutor(worktreeBoundExecutor{
		lifecycle: lifecycle,
		sourceDir: "/repo/subdir",
		fakeAgentExecutor: fakeAgentExecutor{
			onExecute: func(
				_ context.Context,
				opts AgentExecOptions,
			) (*AgentExecResult, error) {
				seen = opts
				return &AgentExecResult{Result: "done"}, nil
			},
		},
	})

	result, err := RunAgent(context.Background(), runner, AgentExecOptions{
		Task:        "isolate",
		Description: "Isolated worker",
		Name:        "feature worker",
		Isolation:   "worktree",
	})
	if err != nil {
		t.Fatalf("RunAgent returned error: %v", err)
		return
	}
	if len(lifecycle.createCalls) != 1 {
		t.Fatalf("expected one worktree create call, got %d", len(lifecycle.createCalls))
	}
	if lifecycle.createCalls[0].SourceDir != "/repo/subdir" {
		t.Fatalf(
			"worktree source = %q, want parent executor cwd",
			lifecycle.createCalls[0].SourceDir,
		)
	}
	if seen.CWD != worktreePath {
		t.Fatalf("executor CWD = %q, want worktree %q", seen.CWD, worktreePath)
	}
	if result.WorktreePath != "" || result.WorktreeBranch != "" {
		t.Fatalf("clean worktree should be cleaned and omitted from result, got %#v", result)
	}
	if result.Worktree == nil || result.Worktree.State != worktree.StateRemoved {
		t.Fatalf("clean worktree result = %#v", result.Worktree)
	}
	if len(lifecycle.removeCalls) != 1 ||
		lifecycle.removeCalls[0] != "worktree-1" {
		t.Fatalf("expected clean worktree removal, got %#v", lifecycle.removeCalls)
	}
	agent := onlyTrackedAgent(t, runner)
	if agent.WorktreePath != "" || agent.WorktreeBranch != "" {
		t.Fatalf("cleaned worktree should not remain in lifecycle metadata: %#v", agent)
	}
}

func TestAgentRunnerWorktreeIsolationPreservesChangedWorktreeInResult(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	wantPath := "/repo/.yhc/worktrees/v1/trees/worktree-2"
	ready := readyAgentWorktreeRecord(
		"worktree-2",
		wantPath,
		"yhc/worktree/worktree-2",
	)
	retained := ready
	retained.State = worktree.StateRetained
	retained.ResultDirtyReport = &worktree.DirtyReport{
		Dirty:        true,
		ChangedFiles: []string{"file.go"},
		Patch:        "diff --git a/file.go b/file.go\n",
	}
	lifecycle := &fakeAgentWorktreeLifecycle{
		createRecord: ready,
		removeRecord: retained,
		removeErr:    errors.New("worktree contains changes"),
	}
	runner.SetExecutor(worktreeBoundExecutor{
		lifecycle: lifecycle,
		sourceDir: "/repo",
		fakeAgentExecutor: fakeAgentExecutor{
			onExecute: func(
				context.Context,
				AgentExecOptions,
			) (*AgentExecResult, error) {
				return &AgentExecResult{Result: "changed"}, nil
			},
		},
	})

	result, err := RunAgent(context.Background(), runner, AgentExecOptions{
		AgentID:     "agent-worktree",
		Task:        "isolate",
		Description: "Changed worker",
		Isolation:   "worktree",
	})
	if err != nil {
		t.Fatalf("RunAgent returned error: %v", err)
		return
	}
	if result.WorktreePath != wantPath ||
		result.WorktreeBranch != "yhc/worktree/worktree-2" {
		t.Fatalf("changed worktree should be returned, got path=%q branch=%q", result.WorktreePath, result.WorktreeBranch)
	}
	if result.Worktree == nil ||
		result.Worktree.State != worktree.StateRetained ||
		len(result.Worktree.ResultDirtyReport.ChangedFiles) != 1 {
		t.Fatalf("retained handoff = %#v", result.Worktree)
	}
	agent := onlyTrackedAgent(t, runner)
	if agent.WorktreePath != wantPath ||
		agent.WorktreeBranch != "yhc/worktree/worktree-2" {
		t.Fatalf("changed worktree should remain in lifecycle metadata: %#v", agent)
	}
}

func TestAgentRunnerWorktreeIsolationCleansOnExecutorError(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	worktreePath := "/repo/.yhc/worktrees/v1/trees/worktree-3"
	ready := readyAgentWorktreeRecord(
		"worktree-3",
		worktreePath,
		"yhc/worktree/worktree-3",
	)
	removed := ready
	removed.State = worktree.StateRemoved
	lifecycle := &fakeAgentWorktreeLifecycle{
		createRecord: ready,
		removeRecord: removed,
	}
	executorErr := errors.New("boom")
	runner.SetExecutor(worktreeBoundExecutor{
		lifecycle: lifecycle,
		sourceDir: "/repo",
		fakeAgentExecutor: fakeAgentExecutor{
			onExecute: func(
				context.Context,
				AgentExecOptions,
			) (*AgentExecResult, error) {
				return nil, executorErr
			},
		},
	})

	_, err := RunAgent(context.Background(), runner, AgentExecOptions{
		Task:        "isolate",
		Description: "Error worker",
		Isolation:   "worktree",
	})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected executor error, got %v", err)
		return
	}
	if len(lifecycle.removeCalls) != 1 ||
		lifecycle.removeCalls[0] != "worktree-3" {
		t.Fatalf("failed clean worktree should be removed, got %#v", lifecycle.removeCalls)
	}
	agent := onlyTrackedAgent(t, runner)
	if agent.WorktreePath != "" || agent.WorktreeBranch != "" {
		t.Fatalf("clean failed worktree should not remain in lifecycle metadata: %#v", agent)
	}
}

func TestAgentRunnerRecoversOwnedWorktreeBeforeContinuation(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	worktreePath := "/repo/.yhc/worktrees/v1/trees/recover-agent"
	ready := readyAgentWorktreeRecord(
		"recover-agent",
		worktreePath,
		"yhc/worktree/recover-agent",
	)
	retained := ready
	retained.State = worktree.StateRetained
	removed := ready
	removed.State = worktree.StateRemoved
	lifecycle := &fakeAgentWorktreeLifecycle{
		createRecord:  ready,
		removeRecord:  retained,
		removeErr:     errors.New("worktree contains changes"),
		recoverRecord: retained,
	}
	var mu sync.Mutex
	executions := make([]AgentExecOptions, 0, 2)
	executor := worktreeBoundExecutor{
		lifecycle:       lifecycle,
		sourceDir:       "/repo",
		parentSessionID: "parent-session",
		fakeAgentExecutor: fakeAgentExecutor{onExecute: func(
			_ context.Context,
			opts AgentExecOptions,
		) (*AgentExecResult, error) {
			mu.Lock()
			executions = append(executions, opts)
			mu.Unlock()
			return &AgentExecResult{Result: "done"}, nil
		}},
	}
	runner.SetExecutor(executor)
	_, err := RunAgent(context.Background(), runner, AgentExecOptions{
		AgentID:         "recover-agent",
		SessionID:       "agent-session",
		ThreadID:        "agent-thread",
		ParentSessionID: "parent-session",
		ParentThreadID:  "parent-thread",
		Task:            "first",
		Isolation:       "worktree",
	})
	if err != nil {
		t.Fatalf("initial Agent: %v", err)
	}
	lifecycle.mu.Lock()
	lifecycle.removeRecord = removed
	lifecycle.removeErr = nil
	lifecycle.mu.Unlock()

	id, status, err := runner.SendOrResumeAgentMessage(
		"recover-agent",
		MessagePayload{Content: "continue"},
	)
	if err != nil {
		t.Fatalf("continue Agent: %v", err)
	}
	if id != "recover-agent" || status != "resumed" {
		t.Fatalf("continuation result = %q, %q", id, status)
	}
	waitForAgentStatus(t, runner, id, "completed")
	mu.Lock()
	defer mu.Unlock()
	if len(executions) != 2 ||
		executions[1].CWD != worktreePath {
		t.Fatalf("executions = %#v", executions)
	}
	if len(lifecycle.recoverCalls) != 1 ||
		lifecycle.recoverCalls[0] != ready.ID {
		t.Fatalf("recovery calls = %#v", lifecycle.recoverCalls)
	}
	if len(lifecycle.removeCalls) != 2 {
		t.Fatalf("cleanup calls = %#v", lifecycle.removeCalls)
	}
}

func TestAgentRunnerForkIdentityCannotRecoverSourceWorktree(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	ready := readyAgentWorktreeRecord(
		"fork-owned",
		"/repo/.yhc/worktrees/v1/trees/fork-owned",
		"yhc/worktree/fork-owned",
	)
	retained := ready
	retained.State = worktree.StateRetained
	lifecycle := &fakeAgentWorktreeLifecycle{
		createRecord:  ready,
		removeRecord:  retained,
		removeErr:     errors.New("retained"),
		recoverRecord: retained,
	}
	baseExecutor := worktreeBoundExecutor{
		lifecycle:       lifecycle,
		sourceDir:       "/repo",
		parentSessionID: "source-session",
		fakeAgentExecutor: fakeAgentExecutor{onExecute: func(
			context.Context,
			AgentExecOptions,
		) (*AgentExecResult, error) {
			return &AgentExecResult{Result: "done"}, nil
		}},
	}
	runner.SetExecutor(baseExecutor)
	if _, err := RunAgent(
		context.Background(),
		runner,
		AgentExecOptions{
			AgentID:         "fork-owned",
			SessionID:       "agent-session",
			ThreadID:        "agent-thread",
			ParentSessionID: "source-session",
			Task:            "first",
			Isolation:       "worktree",
		},
	); err != nil {
		t.Fatal(err)
	}
	baseExecutor.parentSessionID = "fork-session"
	runner.SetExecutor(baseExecutor)
	_, _, err := runner.SendOrResumeAgentMessage(
		"fork-owned",
		MessagePayload{Content: "continue"},
	)
	if err == nil || !strings.Contains(err.Error(), "does not own") {
		t.Fatalf("fork recovery error = %v", err)
	}
	if len(lifecycle.recoverCalls) != 0 {
		t.Fatalf("fork reached lifecycle recovery: %#v", lifecycle.recoverCalls)
	}
}

func TestAgentRunnerRejectsRecoveryWhenBindingRebindsInFlight(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	ready := readyAgentWorktreeRecord(
		"rebind-owned",
		"/repo/.yhc/worktrees/v1/trees/rebind-owned",
		"yhc/worktree/rebind-owned",
	)
	retained := ready
	retained.State = worktree.StateRetained
	recoverEntered := make(chan struct{}, 1)
	recoverRelease := make(chan struct{})
	lifecycle := &fakeAgentWorktreeLifecycle{
		createRecord:   ready,
		removeRecord:   retained,
		removeErr:      errors.New("retained"),
		recoverRecord:  retained,
		recoverEntered: recoverEntered,
		recoverRelease: recoverRelease,
	}
	var executionMu sync.Mutex
	executions := 0
	executor := &rebindingWorktreeExecutor{
		fakeAgentExecutor: fakeAgentExecutor{onExecute: func(
			context.Context,
			AgentExecOptions,
		) (*AgentExecResult, error) {
			executionMu.Lock()
			executions++
			executionMu.Unlock()
			return &AgentExecResult{Result: "done"}, nil
		}},
		snapshot: AgentWorktreeBindingSnapshot{
			Lifecycle:       lifecycle,
			SourceDir:       "/repo",
			ParentSessionID: "source-session",
			Generation:      1,
		},
	}
	runner.SetExecutor(executor)
	if _, err := RunAgent(
		context.Background(),
		runner,
		AgentExecOptions{
			AgentID:         "rebind-agent",
			SessionID:       "rebind-agent-session",
			ThreadID:        "rebind-agent-thread",
			ParentSessionID: "source-session",
			Task:            "first",
			Isolation:       "worktree",
		},
	); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		_, _, err := runner.SendOrResumeAgentMessage(
			"rebind-agent",
			MessagePayload{Content: "continue"},
		)
		result <- err
	}()
	<-recoverEntered
	executor.rebind(AgentWorktreeBindingSnapshot{
		Lifecycle:       lifecycle,
		SourceDir:       "/other-repo",
		ParentSessionID: "fork-session",
		Generation:      2,
	})
	close(recoverRelease)
	err := <-result
	if err == nil || !strings.Contains(
		err.Error(),
		"binding changed during admission",
	) {
		t.Fatalf("rebind recovery error = %v", err)
	}
	executionMu.Lock()
	defer executionMu.Unlock()
	if executions != 1 {
		t.Fatalf("executor ran %d times, want only initial execution", executions)
	}
}

func TestAgentRunnerProcessRestartRecoversPersistedWorktreeContinuation(t *testing.T) {
	outputDir := t.TempDir()
	ready := readyAgentWorktreeRecord(
		"restart-owned",
		"/repo/.yhc/worktrees/v1/trees/restart-owned",
		"yhc/worktree/restart-owned",
	)
	retained := ready
	retained.State = worktree.StateRetained
	lifecycle := &fakeAgentWorktreeLifecycle{
		createRecord:  ready,
		removeRecord:  retained,
		removeErr:     errors.New("retained"),
		recoverRecord: retained,
	}
	executor := worktreeBoundExecutor{
		lifecycle:       lifecycle,
		sourceDir:       "/repo",
		parentSessionID: "parent-session",
		fakeAgentExecutor: fakeAgentExecutor{onExecute: func(
			context.Context,
			AgentExecOptions,
		) (*AgentExecResult, error) {
			return &AgentExecResult{Result: "done"}, nil
		}},
	}
	producer := NewAgentRunner(1)
	producer.SetOutputDir(outputDir)
	producer.SetExecutor(executor)
	if _, err := RunAgent(
		context.Background(),
		producer,
		AgentExecOptions{
			AgentID:         "restart-agent",
			SessionID:       "restart-agent-session",
			ThreadID:        "restart-agent-thread",
			ParentSessionID: "parent-session",
			ParentThreadID:  "parent-thread",
			Task:            "first",
			Isolation:       "worktree",
		},
	); err != nil {
		t.Fatal(err)
	}

	fresh := NewAgentRunner(1)
	fresh.SetOutputDir(outputDir)
	fresh.SetExecutor(executor)
	if _, err := fresh.RegisterPersistedAgent("restart-agent"); err != nil {
		t.Fatalf("register persisted Agent: %v", err)
	}
	firstTerminal, err := fresh.LoadPersistedAgentSnapshot("restart-agent")
	if err != nil || firstTerminal.Completion == nil ||
		firstTerminal.TerminalSequence != 1 {
		t.Fatalf("first terminal snapshot = %#v, err=%v", firstTerminal, err)
	}
	id, status, err := fresh.SendOrResumeAgentMessage(
		"restart-agent",
		MessagePayload{Content: "continue after restart"},
	)
	if err != nil {
		t.Fatalf("resume persisted Agent: %v", err)
	}
	if id != "restart-agent" || status != "resumed" {
		t.Fatalf("resume result = %q, %q", id, status)
	}
	secondTerminal := waitForAgentStatus(t, fresh, id, "completed")
	if secondTerminal.TerminalSequence != 2 ||
		secondTerminal.Completion == nil ||
		secondTerminal.Completion.CompletionID ==
			firstTerminal.Completion.CompletionID {
		t.Fatalf("second terminal snapshot = %#v", secondTerminal)
	}
	if len(lifecycle.recoverCalls) != 1 {
		t.Fatalf("recovery calls = %#v", lifecycle.recoverCalls)
	}
}

func TestAgentRunnerRejectsWorktreeIsolationWithExplicitCWDBeforeSideEffects(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	executed := false
	lifecycle := &fakeAgentWorktreeLifecycle{}
	runner.SetExecutor(worktreeBoundExecutor{
		lifecycle: lifecycle,
		sourceDir: "/repo",
		fakeAgentExecutor: fakeAgentExecutor{
			onExecute: func(
				context.Context,
				AgentExecOptions,
			) (*AgentExecResult, error) {
				executed = true
				return &AgentExecResult{Result: "unexpected"}, nil
			},
		},
	})

	_, err := RunAgent(context.Background(), runner, AgentExecOptions{
		Task:      "ambiguous",
		Isolation: "worktree",
		CWD:       "/other",
	})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("ambiguous worktree error = %v", err)
	}
	if executed || len(lifecycle.createCalls) != 0 {
		t.Fatalf(
			"ambiguous worktree reached side effects: executed=%v creates=%d",
			executed,
			len(lifecycle.createCalls),
		)
	}
}

func TestAgentRunnerDoesNotHoldRunnerMutexAcrossWorktreeCreate(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	path := "/repo/.yhc/worktrees/v1/trees/slow-create"
	ready := readyAgentWorktreeRecord(
		"slow-create",
		path,
		"yhc/worktree/slow-create",
	)
	removed := ready
	removed.State = worktree.StateRemoved
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	lifecycle := &fakeAgentWorktreeLifecycle{
		createRecord:  ready,
		removeRecord:  removed,
		createEntered: entered,
		createRelease: release,
	}
	runner.SetExecutor(worktreeBoundExecutor{
		lifecycle: lifecycle,
		sourceDir: "/repo",
		fakeAgentExecutor: fakeAgentExecutor{
			onExecute: func(
				context.Context,
				AgentExecOptions,
			) (*AgentExecResult, error) {
				return &AgentExecResult{Result: "done"}, nil
			},
		},
	})
	done := make(chan error, 1)
	go func() {
		_, err := RunAgent(context.Background(), runner, AgentExecOptions{
			Task:      "slow create",
			Isolation: "worktree",
		})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worktree create did not start")
	}
	readReturned := make(chan struct{})
	go func() {
		_ = runner.ActiveCount()
		close(readReturned)
	}()
	select {
	case <-readReturned:
	case <-time.After(time.Second):
		t.Fatal("runner mutex was held across worktree creation")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAgentToolBackgroundPreservesScopedWorktreeBinding(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	fallbackCalled := make(chan struct{}, 1)
	runner.SetExecutor(fakeAgentExecutor{
		onExecute: func(
			context.Context,
			AgentExecOptions,
		) (*AgentExecResult, error) {
			fallbackCalled <- struct{}{}
			return nil, errors.New("fallback executor called")
		},
	})
	path := "/repo/.yhc/worktrees/v1/trees/background"
	ready := readyAgentWorktreeRecord(
		"background",
		path,
		"yhc/worktree/background",
	)
	removed := ready
	removed.State = worktree.StateRemoved
	lifecycle := &fakeAgentWorktreeLifecycle{
		createRecord: ready,
		removeRecord: removed,
	}
	executed := make(chan AgentExecOptions, 1)
	scoped := worktreeBoundExecutor{
		lifecycle: lifecycle,
		sourceDir: "/repo/scoped",
		fakeAgentExecutor: fakeAgentExecutor{
			onExecute: func(
				_ context.Context,
				opts AgentExecOptions,
			) (*AgentExecResult, error) {
				executed <- opts
				return &AgentExecResult{Result: "done"}, nil
			},
		},
	}
	ctx := WithAgentRunner(context.Background(), runner)
	ctx = WithAgentExecutor(ctx, scoped)
	_, err := executeAgentTool(
		ctx,
		`{"description":"background worktree","prompt":"work","run_in_background":true,"isolation":"worktree","worktree_source":"ignore_dirty"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case opts := <-executed:
		if opts.CWD != path {
			t.Fatalf("background executor cwd = %q, want %q", opts.CWD, path)
		}
	case <-time.After(time.Second):
		t.Fatal("scoped background executor was not called")
	}
	select {
	case <-fallbackCalled:
		t.Fatal("background launch lost scoped executor")
	default:
	}
	if len(lifecycle.createCalls) != 1 ||
		lifecycle.createCalls[0].SourceDir != "/repo/scoped" ||
		lifecycle.createCalls[0].SourceMode != worktree.SourceIgnoreDirty {
		t.Fatalf("background create calls = %#v", lifecycle.createCalls)
	}
	agent := onlyTrackedAgent(t, runner)
	waitForAgentStatus(t, runner, agent.ID, "completed")
}

func TestAgentRunnerMarksLifecycleFailedOnExecutorError(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	executorErr := errors.New("executor failed")
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		return nil, executorErr
	}})

	_, err := RunAgent(context.Background(), runner, AgentExecOptions{Task: "fail work", Description: "Fail work"})
	if err == nil || !strings.Contains(err.Error(), "executor failed") {
		t.Fatalf("expected wrapped executor error, got %v", err)
		return
	}

	agent := onlyTrackedAgent(t, runner)
	if agent.Status != "failed" {
		t.Fatalf("expected failed status, got %q", agent.Status)
	}
	if !errors.Is(agent.Error, executorErr) {
		t.Fatalf("expected stored executor error, got %v", agent.Error)
	}
	if agent.Result != "" {
		t.Fatalf("expected no success result on failure, got %q", agent.Result)
	}
	if agent.CompletedAt.IsZero() || agent.CompletedAt.Before(agent.StartedAt) {
		t.Fatalf("expected completed time after start, start=%v completed=%v", agent.StartedAt, agent.CompletedAt)
	}
}

func TestAgentRunnerPersistsAgentOutputForTaskOutput(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		return &AgentExecResult{Result: "persist me"}, nil
	}})

	_, err := RunAgent(context.Background(), runner, AgentExecOptions{Task: "persist output", Description: "Persist output"})
	if err != nil {
		t.Fatalf("RunAgent returned error: %v", err)
		return
	}

	agent := onlyTrackedAgent(t, runner)
	if agent.OutputFile == "" {
		t.Fatal("expected output file path")
	}
	data, err := os.ReadFile(agent.OutputFile)
	if err != nil {
		t.Fatalf("read output file: %v", err)
		return
	}
	if string(data) != "persist me" {
		t.Fatalf("unexpected persisted output: %q", string(data))
	}
	if agent.OutputOffset != int64(len(data)) {
		t.Fatalf("expected output offset %d, got %d", len(data), agent.OutputOffset)
	}
}

func TestAgentRunnerRunAgentBackgroundRegistersLifecycleState(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())

	entered := make(chan struct{})
	release := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		close(entered)
		<-release
		return &AgentExecResult{Result: "background done"}, nil
	}})

	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "background lifecycle",
		Description: "Background lifecycle",
		ToolUseID:   "toolu_background",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground returned error: %v", err)
		return
	}
	if agent.ID == "" {
		t.Fatal("expected stable background agent id")
	}
	if agent.OutputFile == "" {
		t.Fatal("expected output file metadata at launch")
	}

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("executor was not entered")
	}

	snapshot, ok := runner.GetAgentSnapshot(agent.ID)
	if !ok {
		t.Fatalf("expected background agent %q to be registered", agent.ID)
	}
	if snapshot.Status != "running" {
		t.Fatalf("expected running status while background executor is blocked, got %q", snapshot.Status)
	}
	if snapshot.Description != "Background lifecycle" {
		t.Fatalf("expected description to be recorded, got %q", snapshot.Description)
	}
	if snapshot.ToolUseID != "toolu_background" {
		t.Fatalf("expected tool use id to be recorded, got %q", snapshot.ToolUseID)
	}
	if snapshot.OutputFile != agent.OutputFile {
		t.Fatalf("expected launch output file %q, got %q", agent.OutputFile, snapshot.OutputFile)
	}

	close(release)
	waitForAgentStatus(t, runner, agent.ID, "completed")
}

func TestAgentRunnerRunAgentBackgroundPersistsFinalOutput(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		return &AgentExecResult{Result: "persisted background output"}, nil
	}})

	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{Task: "persist", Description: "Persist"})
	if err != nil {
		t.Fatalf("RunAgentBackground returned error: %v", err)
		return
	}
	snapshot := waitForAgentStatus(t, runner, agent.ID, "completed")

	data, err := os.ReadFile(snapshot.OutputFile)
	if err != nil {
		t.Fatalf("read output file: %v", err)
		return
	}
	if string(data) != "persisted background output" {
		t.Fatalf("unexpected persisted background output: %q", string(data))
	}
	if snapshot.Result != "persisted background output" {
		t.Fatalf("expected stored background result, got %q", snapshot.Result)
	}
	if snapshot.OutputOffset != int64(len(data)) {
		t.Fatalf("expected output offset %d, got %d", len(data), snapshot.OutputOffset)
	}
}

func TestAgentRunnerRunAgentBackgroundMarksFailedOnExecutorError(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	executorErr := errors.New("background executor failed")
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		return nil, executorErr
	}})

	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{Task: "fail", Description: "Fail"})
	if err != nil {
		t.Fatalf("RunAgentBackground returned launch error: %v", err)
		return
	}
	snapshot := waitForAgentStatus(t, runner, agent.ID, "failed")
	if !errors.Is(snapshot.Error, executorErr) {
		t.Fatalf("expected stored executor error, got %v", snapshot.Error)
	}
	if snapshot.Result != "" {
		t.Fatalf("expected no success result on failure, got %q", snapshot.Result)
	}
	if snapshot.CompletedAt.IsZero() {
		t.Fatal("expected completed time on background failure")
	}
}

func TestAgentRunnerProgressUpdatesAreVisibleInSnapshots(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())

	entered := make(chan struct{})
	release := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		close(entered)
		<-release
		return &AgentExecResult{Result: "progress complete", TokensUsed: 33, ToolsUsed: []string{"Read"}}, nil
	}})

	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{Task: "track progress", Description: "Track progress"})
	if err != nil {
		t.Fatalf("RunAgentBackground returned error: %v", err)
		return
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("executor was not entered")
	}

	if err := runner.UpdateAgentProgress(agent.ID, AgentProgress{
		ToolUseCount: 1,
		TokenCount:   17,
		LastActivity: &ToolActivity{ToolName: "Read", ActivityDescription: "Reading README.md"},
		Summary:      "Reading project docs",
	}); err != nil {
		t.Fatalf("UpdateAgentProgress returned error: %v", err)
		return
	}

	snapshot, ok := runner.GetAgentSnapshot(agent.ID)
	if !ok {
		t.Fatalf("expected background agent %q to be registered", agent.ID)
	}
	if snapshot.Progress.ToolUseCount != 1 || snapshot.Progress.TokenCount != 17 {
		t.Fatalf("unexpected progress snapshot: %#v", snapshot.Progress)
	}
	if snapshot.Progress.LastActivity == nil || snapshot.Progress.LastActivity.ActivityDescription != "Reading README.md" {
		t.Fatalf("expected last activity to be copied into snapshot, got %#v", snapshot.Progress.LastActivity)
		return
	}
	if snapshot.Progress.Summary != "Reading project docs" {
		t.Fatalf("expected progress summary to be preserved, got %q", snapshot.Progress.Summary)
	}

	close(release)
	completed := waitForAgentStatus(t, runner, agent.ID, "completed")
	if completed.Progress.Summary != "Reading project docs" {
		t.Fatalf("expected terminal progress to preserve running summary, got %q", completed.Progress.Summary)
	}
	if completed.Progress.TokenCount != 33 || completed.Progress.ToolUseCount != 1 {
		t.Fatalf("expected terminal progress to reflect result usage, got %#v", completed.Progress)
	}
}

func TestAgentRunnerPollsRunningProgressWithReferenceShape(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())

	entered := make(chan struct{})
	release := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		close(entered)
		<-release
		return &AgentExecResult{Result: "progress complete"}, nil
	}})

	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "track runtime progress",
		Description: "Track runtime progress",
		ToolUseID:   "toolu_progress",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground returned error: %v", err)
		return
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("executor was not entered")
	}

	if err := runner.UpdateAgentProgress(agent.ID, AgentProgress{
		ToolUseCount: 2,
		TokenCount:   123,
		LastActivity: &ToolActivity{ToolName: "Read", ActivityDescription: "Reading README.md"},
		Summary:      "Reading project docs",
	}); err != nil {
		t.Fatalf("UpdateAgentProgress returned error: %v", err)
		return
	}

	progressEvents := runner.PollAgentProgress()
	if len(progressEvents) != 1 {
		t.Fatalf("expected one progress event, got %d: %#v", len(progressEvents), progressEvents)
	}
	progress := progressEvents[0]
	if progress.Type != "system" || progress.Subtype != "task_progress" {
		t.Fatalf("expected SDK system task_progress shape, got %#v", progress)
	}
	if progress.TaskID != agent.ID || progress.ToolUseID != "toolu_progress" || progress.Description != "Track runtime progress" {
		t.Fatalf("unexpected progress identity fields: %#v", progress)
	}
	if progress.Usage.TotalTokens != 123 || progress.Usage.ToolUses != 2 {
		t.Fatalf("unexpected progress usage fields: %#v", progress.Usage)
	}
	if progress.Usage.DurationMS < 0 {
		t.Fatalf("expected non-negative duration, got %d", progress.Usage.DurationMS)
	}
	if progress.LastToolName != "Read" || progress.Summary != "Reading project docs" {
		t.Fatalf("unexpected progress activity fields: %#v", progress)
	}
	snapshot, ok := runner.GetAgentSnapshot(agent.ID)
	if !ok {
		t.Fatalf("expected background agent %q to remain registered", agent.ID)
	}
	if snapshot.Notified {
		t.Fatal("progress polling must not mark terminal notification delivered")
	}

	close(release)
	waitForAgentStatus(t, runner, agent.ID, "completed")
	notifications := runner.PollAgentNotifications()
	if len(notifications) != 1 {
		t.Fatalf("expected terminal notification after progress polling, got %d: %#v", len(notifications), notifications)
	}
}

func TestAgentRunnerPollProgressSkipsUnknownNoProgressAndTerminalAgents(t *testing.T) {
	if got := (*AgentRunner)(nil).PollAgentProgress(); len(got) != 0 {
		t.Fatalf("expected nil runner to emit no progress, got %#v", got)
	}

	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	entered := make(chan struct{})
	release := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		close(entered)
		<-release
		return &AgentExecResult{Result: "done"}, nil
	}})
	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "no progress yet",
		Description: "No progress yet",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground returned error: %v", err)
		return
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("executor was not entered")
	}

	if got := runner.PollAgentProgress(); len(got) != 0 {
		t.Fatalf("expected running agent without progress to emit no event, got %#v", got)
	}
	if err := runner.UpdateAgentProgress(agent.ID, AgentProgress{Summary: "now has progress"}); err != nil {
		t.Fatalf("UpdateAgentProgress returned error: %v", err)
		return
	}
	if got := runner.PollAgentProgress(); len(got) != 1 || got[0].Summary != "now has progress" {
		t.Fatalf("expected summary-only progress to emit deterministically, got %#v", got)
	}

	close(release)
	waitForAgentStatus(t, runner, agent.ID, "completed")
	if got := runner.PollAgentProgress(); len(got) != 0 {
		t.Fatalf("expected terminal agent to emit no non-terminal progress, got %#v", got)
	}
}

func TestAgentRunnerPollsTerminalNotificationsOnceWithFailureStatus(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		return nil, errors.New("boom")
	}})

	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "notify failure",
		Description: "Notify failure",
		ToolUseID:   "toolu_notify",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground returned launch error: %v", err)
		return
	}
	waitForAgentStatus(t, runner, agent.ID, "failed")

	notifications := runner.PollAgentNotifications()
	if len(notifications) != 1 {
		t.Fatalf("expected one terminal notification, got %d: %#v", len(notifications), notifications)
	}
	notification := notifications[0]
	if notification.AgentID != agent.ID || notification.Status != "failed" || notification.ToolUseID != "toolu_notify" {
		t.Fatalf("unexpected notification metadata: %#v", notification)
	}
	for _, want := range []string{
		"<task-notification>",
		"<task-id>" + agent.ID + "</task-id>",
		"<tool-use-id>toolu_notify</tool-use-id>",
		"<output-file>" + notification.OutputFile + "</output-file>",
		"<status>failed</status>",
		"</task-notification>",
	} {
		if !strings.Contains(notification.Message, want) {
			t.Fatalf("expected reference-shaped failure notification XML to contain %q, got %q", want, notification.Message)
		}
	}
	for _, legacy := range []string{"task_notification", "task_id", "tool_use_id", "output_file"} {
		if strings.Contains(notification.Message, legacy) {
			t.Fatalf("expected reference-shaped failure notification XML not to contain legacy tag %q, got %q", legacy, notification.Message)
		}
	}
	if !strings.Contains(notification.Message, "boom") {
		t.Fatalf("expected failure notification to include error, got %q", notification.Message)
	}

	again := runner.PollAgentNotifications()
	if len(again) != 0 {
		t.Fatalf("expected notification to be marked delivered, got %#v", again)
	}
	snapshot, ok := runner.GetAgentSnapshot(agent.ID)
	if !ok {
		t.Fatalf("expected background agent %q to remain pollable", agent.ID)
	}
	if !snapshot.Notified {
		t.Fatal("expected polling notifications to mark agent notified")
	}
}

func TestSendMessageQueuesForRunningBackgroundAgentByName(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	entered := make(chan struct{})
	release := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		close(entered)
		<-release
		return &AgentExecResult{Result: "message target complete"}, nil
	}})
	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "receive updates",
		Description: "Receive updates",
		Name:        "worker-one",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground returned error: %v", err)
		return
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("executor was not entered")
	}

	resp, err := executeSendMessageWithRuntime(
		`{"recipient":"worker-one","content":"please continue","metadata":{"command_uuid":"send-uuid-1","origin":"coordinator","priority":"next"}}`,
		runner,
		NewTaskManager(),
	)
	if err != nil {
		t.Fatalf("executeSendMessage returned error: %v", err)
		return
	}
	if !strings.Contains(resp, "Message delivered") {
		t.Fatalf("unexpected SendMessage response: %q", resp)
	}
	snapshot, ok := runner.GetAgentSnapshot(agent.ID)
	if !ok {
		t.Fatalf("expected background agent %q to be registered", agent.ID)
	}
	if snapshot.PendingMessageCount != 1 {
		t.Fatalf("expected pending message count to be visible, got %d", snapshot.PendingMessageCount)
	}
	drained, err := runner.DrainAgentMessages(agent.ID)
	if err != nil {
		t.Fatalf("DrainAgentMessages returned error: %v", err)
		return
	}
	if len(drained) != 1 || drained[0].Content != "please continue" || drained[0].To != agent.ID {
		t.Fatalf("unexpected drained agent messages: %#v", drained)
	}
	if drained[0].Metadata["command_uuid"] != "send-uuid-1" || drained[0].Metadata["origin"] != "coordinator" || drained[0].Metadata["priority"] != "next" {
		t.Fatalf("expected SendMessage metadata to survive pending payload handoff, got %#v", drained[0].Metadata)
	}

	close(release)
	waitForAgentStatus(t, runner, agent.ID, "completed")
}

func TestAgentRunnerCancelAgentMessageRemovesPendingByCommandUUID(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	entered := make(chan struct{})
	release := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		close(entered)
		<-release
		return &AgentExecResult{Result: "done"}, nil
	}})
	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task: "wait", Description: "Wait", Name: "cancel-message-worker",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("executor was not entered")
	}
	if _, err := runner.QueueAgentMessage(agent.ID, MessagePayload{
		Content: "remove", Metadata: map[string]any{"command_uuid": "queued-1"},
	}); err != nil {
		t.Fatal(err)
	}
	cancelled, err := runner.CancelAgentMessage(agent.ID, "queued-1")
	if err != nil || !cancelled {
		t.Fatalf("cancel = %v, %v", cancelled, err)
	}
	snapshot, ok := runner.GetAgentSnapshot(agent.ID)
	if !ok || snapshot.PendingMessageCount != 0 || len(snapshot.PendingMessages) != 0 {
		t.Fatalf("pending snapshot = %#v", snapshot)
	}
	if cancelled, err := runner.CancelAgentMessage(agent.ID, "queued-1"); err != nil || cancelled {
		t.Fatalf("second cancel = %v, %v", cancelled, err)
	}
	close(release)
	waitForAgentStatus(t, runner, agent.ID, "completed")
}

func TestAgentRunnerExactGenerationMessageRetryAndCancellation(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	entered := make(chan struct{})
	release := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(
		context.Context,
		AgentExecOptions,
	) (*AgentExecResult, error) {
		close(entered)
		<-release
		return &AgentExecResult{Result: "done"}, nil
	}})
	agent, err := RunAgentBackground(
		context.Background(),
		runner,
		AgentExecOptions{
			Task: "wait", Description: "Wait", Name: "exact-message-worker",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("executor was not entered")
	}
	generation := agent.ExecutionGeneration()
	message := MessagePayload{
		Content:  "retry-safe",
		Metadata: map[string]any{"command_uuid": "exact-message-1"},
	}
	for range 2 {
		if _, err := runner.QueueAgentMessageGeneration(
			agent.ID,
			generation,
			message,
		); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, ok := runner.GetAgentSnapshot(agent.ID)
	if !ok || snapshot.PendingMessageCount != 1 {
		t.Fatalf("idempotent exact queue snapshot = %#v", snapshot)
	}
	outcome, err := runner.CancelAgentMessageGeneration(
		agent.ID,
		generation,
		"exact-message-1",
	)
	if err != nil || outcome != "input_cancelled" {
		t.Fatalf("first exact cancel = %q, %v", outcome, err)
	}
	outcome, err = runner.CancelAgentMessageGeneration(
		agent.ID,
		generation,
		"exact-message-1",
	)
	if err != nil || outcome != "input_not_pending" {
		t.Fatalf("second exact cancel = %q, %v", outcome, err)
	}
	outcome, err = runner.CancelAgentMessageGeneration(
		agent.ID,
		generation+1,
		"exact-message-1",
	)
	if err != nil || outcome != "stale_generation" {
		t.Fatalf("stale exact cancel = %q, %v", outcome, err)
	}
	close(release)
	waitForAgentStatus(t, runner, agent.ID, "completed")
}

func TestAgentRunnerExactCancellationLinearizesWithDrain(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	entered := make(chan struct{})
	release := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(
		context.Context,
		AgentExecOptions,
	) (*AgentExecResult, error) {
		close(entered)
		<-release
		return &AgentExecResult{Result: "done"}, nil
	}})
	agent, err := RunAgentBackground(
		context.Background(),
		runner,
		AgentExecOptions{
			Task: "wait", Description: "Wait", Name: "drain-race-worker",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("executor was not entered")
	}
	generation := agent.ExecutionGeneration()
	for iteration := range 100 {
		messageID := fmt.Sprintf("race-message-%d", iteration)
		if _, err := runner.QueueAgentMessageGeneration(
			agent.ID,
			generation,
			MessagePayload{
				Content:  messageID,
				Metadata: map[string]any{"command_uuid": messageID},
			},
		); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		outcomeCh := make(chan string, 1)
		drainedCh := make(chan []MessagePayload, 1)
		errCh := make(chan error, 2)
		go func() {
			<-start
			outcome, cancelErr := runner.CancelAgentMessageGeneration(
				agent.ID,
				generation,
				messageID,
			)
			outcomeCh <- outcome
			errCh <- cancelErr
		}()
		go func() {
			<-start
			drained, drainErr := runner.DrainAgentMessages(agent.ID)
			drainedCh <- drained
			errCh <- drainErr
		}()
		close(start)
		outcome := <-outcomeCh
		drained := <-drainedCh
		for range 2 {
			if err := <-errCh; err != nil {
				t.Fatal(err)
			}
		}
		switch outcome {
		case "input_cancelled":
			if len(drained) != 0 {
				t.Fatalf(
					"cancel won but drain returned messages: %#v",
					drained,
				)
			}
		case "input_not_pending":
			if len(drained) != 1 ||
				agentMessageCommandUUID(drained[0]) != messageID {
				t.Fatalf(
					"drain won but cancellation observed %#v / %q",
					drained,
					outcome,
				)
			}
		default:
			t.Fatalf("race outcome = %q", outcome)
		}
	}
	close(release)
	waitForAgentStatus(t, runner, agent.ID, "completed")
}

func TestAgentRunnerQueueAgentMessageRejectsTerminalAgentDeterministically(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		return &AgentExecResult{Result: "done"}, nil
	}})
	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "terminal queue",
		Description: "Terminal queue",
		Name:        "terminal-worker",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground returned error: %v", err)
		return
	}
	waitForAgentStatus(t, runner, agent.ID, "completed")

	_, err = runner.QueueAgentMessage("terminal-worker", MessagePayload{Content: "too late"})
	if err == nil || !strings.Contains(err.Error(), "is not running (status: completed)") {
		t.Fatalf("expected deterministic terminal QueueAgentMessage rejection, got %v", err)
		return
	}
}

func TestSendMessageResumesStoppedBackgroundAgentWithPriorMessages(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	call := 0
	resumeEntered := make(chan struct{})
	resumeRelease := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		call++
		switch call {
		case 1:
			return &AgentExecResult{Result: "first result", Messages: []*schema.Message{
				{Role: schema.User, Content: "original task"},
				{Role: schema.Assistant, Content: "first result"},
			}}, nil
		case 2:
			if opts.AgentID == "" {
				t.Fatal("expected stopped resume to reuse the existing agent id")
			}
			if opts.Task != "resume with this" {
				t.Fatalf("expected resume prompt to be SendMessage content, got %q", opts.Task)
			}
			if len(opts.ResumeMessages) != 2 || opts.ResumeMessages[0].Content != "original task" || opts.ResumeMessages[1].Content != "first result" {
				t.Fatalf("expected resume to receive prior in-memory messages, got %#v", opts.ResumeMessages)
			}
			close(resumeEntered)
			<-resumeRelease
			return &AgentExecResult{Result: "resumed result"}, nil
		default:
			t.Fatalf("unexpected executor call %d", call)
			return nil, nil
		}
	}})
	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "original task",
		Description: "Stopped worker",
		Name:        "stopped-worker",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground returned error: %v", err)
		return
	}
	waitForAgentStatus(t, runner, agent.ID, "completed")

	manager := NewTaskManager()
	resp, err := executeSendMessageWithRuntime(
		`{"recipient":"stopped-worker","content":"resume with this"}`,
		runner,
		manager,
	)
	if err != nil {
		t.Fatalf("executeSendMessage returned error for stopped agent resume: %v", err)
		return
	}
	if !strings.Contains(resp, "resumed") || !strings.Contains(resp, agent.ID) {
		t.Fatalf("expected stopped resume response with agent id, got %q", resp)
	}
	select {
	case <-resumeEntered:
	case <-time.After(time.Second):
		t.Fatal("resumed executor was not entered")
	}
	snapshot, ok := runner.GetAgentSnapshot(agent.ID)
	if !ok {
		t.Fatalf("expected resumed agent %q to stay tracked", agent.ID)
	}
	if snapshot.Status != "running" {
		t.Fatalf("expected stopped agent to be running after auto-resume, got %q", snapshot.Status)
	}
	if len(snapshot.Messages) != 3 || snapshot.Messages[2].Content != "resume with this" {
		t.Fatalf("expected resumed running snapshot to include prior history plus resume prompt, got %#v", snapshot.Messages)
	}

	queuedResp, err := executeSendMessageWithRuntime(
		`{"recipient":"stopped-worker","content":"queued after resume"}`,
		runner,
		manager,
	)
	if err != nil {
		t.Fatalf("executeSendMessage returned error while resumed agent running: %v", err)
		return
	}
	if !strings.Contains(queuedResp, "Message delivered") {
		t.Fatalf("expected running resumed agent delivery response, got %q", queuedResp)
	}
	drained, err := runner.DrainAgentMessages(agent.ID)
	if err != nil {
		t.Fatalf("DrainAgentMessages returned error: %v", err)
		return
	}
	if len(drained) != 1 || drained[0].Content != "queued after resume" {
		t.Fatalf("expected FIFO pending message after resume, got %#v", drained)
	}

	close(resumeRelease)
	waitForAgentStatus(t, runner, agent.ID, "completed")
}

func TestAgentRunnerConcurrentStoppedResumeStartsOnlyOneExecution(t *testing.T) {
	runner := NewAgentRunner(64)
	runner.SetOutputDir(t.TempDir())

	var mu sync.Mutex
	executions := 0
	resumeEntered := make(chan struct{}, 64)
	resumeRelease := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		mu.Lock()
		executions++
		call := executions
		mu.Unlock()

		if call == 1 {
			return &AgentExecResult{Result: "first result", Messages: []*schema.Message{
				{Role: schema.User, Content: "original task"},
				{Role: schema.Assistant, Content: "first result"},
			}}, nil
		}

		resumeEntered <- struct{}{}
		<-resumeRelease
		return &AgentExecResult{Result: fmt.Sprintf("resume result %d", call)}, nil
	}})

	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "original task",
		Description: "Concurrent resume worker",
		Name:        "concurrent-resume-worker",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground returned error: %v", err)
		return
	}
	waitForAgentStatus(t, runner, agent.ID, "completed")

	const senders = 32
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan string, senders)
	errs := make(chan error, senders)
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, disposition, err := runner.SendOrResumeAgentMessage("concurrent-resume-worker", MessagePayload{
				Content: fmt.Sprintf("message %02d", i),
			})
			if err != nil {
				errs <- err
				return
			}
			results <- disposition
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("SendOrResumeAgentMessage returned error under concurrent stopped resume: %v", err)
	}

	resumed := 0
	queued := 0
	for disposition := range results {
		switch disposition {
		case "resumed":
			resumed++
		case "queued":
			queued++
		default:
			t.Fatalf("unexpected disposition %q", disposition)
		}
	}
	if resumed != 1 {
		t.Fatalf("expected exactly one stopped-agent resume, got %d resumed and %d queued", resumed, queued)
	}
	if queued != senders-1 {
		t.Fatalf("expected remaining messages to queue after the single resume, got %d queued", queued)
	}

	select {
	case <-resumeEntered:
	case <-time.After(time.Second):
		t.Fatal("resumed executor was not entered")
	}
	select {
	case <-resumeEntered:
		t.Fatal("expected only one resumed executor to start")
	default:
	}
	close(resumeRelease)
	waitForAgentStatus(t, runner, agent.ID, "completed")
}

func TestAgentRunnerConcurrentDifferentStoppedResumesRespectGlobalMaxAgents(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())

	const stoppedAgents = 16
	var mu sync.Mutex
	executions := 0
	resumeEntered := make(chan string, 2)
	resumeRelease := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		mu.Lock()
		executions++
		call := executions
		mu.Unlock()

		if call <= stoppedAgents {
			return &AgentExecResult{Result: "initial result", Messages: []*schema.Message{
				{Role: schema.User, Content: opts.Task},
				{Role: schema.Assistant, Content: "initial result"},
			}}, nil
		}

		resumeEntered <- opts.AgentID
		<-resumeRelease
		return &AgentExecResult{Result: "resumed result"}, nil
	}})

	agents := make([]RunningAgent, 0, stoppedAgents)
	names := make([]string, 0, stoppedAgents)
	for i := 0; i < stoppedAgents; i++ {
		name := fmt.Sprintf("stopped-worker-%02d", i)
		agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
			Task:        fmt.Sprintf("original task %02d", i),
			Description: fmt.Sprintf("Stopped worker %02d", i),
			Name:        name,
		})
		if err != nil {
			t.Fatalf("RunAgentBackground %d returned error: %v", i, err)
			return
		}
		waitForAgentStatus(t, runner, agent.ID, "completed")
		agents = append(agents, agent)
		names = append(names, name)
	}

	start := make(chan struct{})
	type sendResult struct {
		disposition string
		err         error
	}
	results := make(chan sendResult, stoppedAgents)
	for _, name := range names {
		go func() {
			<-start
			_, disposition, err := runner.SendOrResumeAgentMessage(name, MessagePayload{Content: "resume " + name})
			results <- sendResult{disposition: disposition, err: err}
		}()
	}
	close(start)

	resumed := 0
	maxConcurrencyErrors := 0
	for i := 0; i < stoppedAgents; i++ {
		select {
		case result := <-results:
			if result.err != nil {
				if strings.Contains(result.err.Error(), "max concurrent agents reached (1)") {
					maxConcurrencyErrors++
					continue
				}
				t.Fatalf("unexpected SendOrResumeAgentMessage error: %v", result.err)
			}
			if result.disposition != "resumed" {
				t.Fatalf("expected successful call to resume, got disposition %q", result.disposition)
			}
			resumed++
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for concurrent resume calls")
		}
	}
	if resumed != 1 || maxConcurrencyErrors != stoppedAgents-1 {
		t.Fatalf("expected one resumed call and one max-concurrency error, got resumed=%d maxConcurrencyErrors=%d", resumed, maxConcurrencyErrors)
	}

	select {
	case <-resumeEntered:
	case <-time.After(time.Second):
		t.Fatal("expected one resumed executor to start")
	}
	select {
	case id := <-resumeEntered:
		t.Fatalf("expected maxAgents=1 to prevent second stopped resume from starting, but agent %q started", id)
	default:
	}
	close(resumeRelease)
	for _, agent := range agents {
		waitForAgentStatus(t, runner, agent.ID, "completed")
	}
}

func TestSendMessageSurfacesKnownStoppedAgentMaxConcurrencyError(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	entered := make(chan struct{})
	release := make(chan struct{})
	call := 0
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		call++
		if opts.Name == "busy-worker" {
			close(entered)
			<-release
			return &AgentExecResult{Result: "busy done"}, nil
		}
		return &AgentExecResult{Result: fmt.Sprintf("stopped result %d", call), Messages: []*schema.Message{
			{Role: schema.User, Content: opts.Task},
			{Role: schema.Assistant, Content: "stopped result"},
		}}, nil
	}})
	stopped, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "finish first",
		Description: "Stopped worker",
		Name:        "known-stopped-worker",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground stopped returned error: %v", err)
		return
	}
	waitForAgentStatus(t, runner, stopped.ID, "completed")

	busy, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "stay busy",
		Description: "Busy worker",
		Name:        "busy-worker",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground busy returned error: %v", err)
		return
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("busy executor was not entered")
	}

	_, err = executeSendMessageWithRuntime(
		`{"recipient":"known-stopped-worker","content":"resume should be rejected"}`,
		runner,
		NewTaskManager(),
	)
	if err == nil || !strings.Contains(err.Error(), "max concurrent agents reached (1)") {
		t.Fatalf("expected known stopped agent max-concurrency error to surface, got %v", err)
		return
	}
	if strings.Contains(err.Error(), "not found as a task") {
		t.Fatalf("expected runner error, got task fallback masking error: %v", err)
	}

	close(release)
	waitForAgentStatus(t, runner, busy.ID, "completed")
}

func TestAgentRunnerAbortResumeIgnoresStaleOriginalFinish(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())

	originalEntered := make(chan struct{})
	originalRelease := make(chan struct{})
	resumeEntered := make(chan struct{})
	resumeRelease := make(chan struct{})
	call := 0
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		call++
		switch call {
		case 1:
			close(originalEntered)
			<-originalRelease
			return &AgentExecResult{Result: "stale original result", Messages: []*schema.Message{
				{Role: schema.User, Content: opts.Task},
				{Role: schema.Assistant, Content: "stale original result"},
			}}, nil
		case 2:
			close(resumeEntered)
			<-resumeRelease
			return &AgentExecResult{Result: "fresh resumed result", Messages: []*schema.Message{
				{Role: schema.User, Content: "original task"},
				{Role: schema.User, Content: opts.Task},
				{Role: schema.Assistant, Content: "fresh resumed result"},
			}}, nil
		default:
			t.Fatalf("unexpected executor call %d", call)
			return nil, nil
		}
	}})

	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "original task",
		Description: "Abort/resume worker",
		Name:        "abort-resume-worker",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground returned error: %v", err)
		return
	}
	select {
	case <-originalEntered:
	case <-time.After(time.Second):
		t.Fatal("original executor was not entered")
	}
	if err := runner.AbortAgent(agent.ID); err != nil {
		t.Fatalf("AbortAgent returned error: %v", err)
		return
	}

	_, disposition, err := runner.SendOrResumeAgentMessage("abort-resume-worker", MessagePayload{Content: "resume after abort"})
	if err != nil {
		t.Fatalf("SendOrResumeAgentMessage returned error: %v", err)
		return
	}
	if disposition != "resumed" {
		t.Fatalf("expected aborted agent to resume for regression exercise, got %q", disposition)
	}
	select {
	case <-resumeEntered:
	case <-time.After(time.Second):
		t.Fatal("resumed executor was not entered")
	}

	close(originalRelease)
	time.Sleep(20 * time.Millisecond)
	snapshot, ok := runner.GetAgentSnapshot(agent.ID)
	if !ok {
		t.Fatalf("expected resumed agent %q to remain tracked", agent.ID)
	}
	if snapshot.Status != "running" || snapshot.Result != "" {
		t.Fatalf("stale original finish clobbered resumed execution: status=%q result=%q", snapshot.Status, snapshot.Result)
	}

	close(resumeRelease)
	completed := waitForAgentStatus(t, runner, agent.ID, "completed")
	if completed.Result != "fresh resumed result" {
		t.Fatalf("expected fresh resumed result to win, got %q", completed.Result)
	}
}

func TestAgentRunnerCleanupDuringResumeCannotUnregisterResumedAgent(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())

	resumeEntered := make(chan struct{})
	resumeRelease := make(chan struct{})
	call := 0
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		call++
		if call == 1 {
			return &AgentExecResult{Result: "initial done", Messages: []*schema.Message{
				{Role: schema.User, Content: opts.Task},
				{Role: schema.Assistant, Content: "initial done"},
			}}, nil
		}
		close(resumeEntered)
		<-resumeRelease
		return &AgentExecResult{Result: "resumed done"}, nil
	}})

	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "initial task",
		Description: "Cleanup race worker",
		Name:        "cleanup-race-worker",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground returned error: %v", err)
		return
	}
	waitForAgentStatus(t, runner, agent.ID, "completed")

	runner.afterResumeLookupForTest = func() {
		runner.Cleanup(-time.Second)
	}
	_, disposition, err := runner.SendOrResumeAgentMessage("cleanup-race-worker", MessagePayload{Content: "resume through cleanup"})
	if err != nil {
		t.Fatalf("SendOrResumeAgentMessage returned error: %v", err)
		return
	}
	if disposition != "resumed" {
		t.Fatalf("expected resume disposition, got %q", disposition)
	}
	select {
	case <-resumeEntered:
	case <-time.After(time.Second):
		t.Fatal("resumed executor was not entered")
	}
	if _, ok := runner.GetAgentSnapshot(agent.ID); !ok {
		t.Fatalf("cleanup unregistered resumed agent %q", agent.ID)
	}
	if _, err := runner.QueueAgentMessage("cleanup-race-worker", MessagePayload{Content: "still addressable"}); err != nil {
		t.Fatalf("expected resumed agent to remain addressable by name, got %v", err)
		return
	}

	close(resumeRelease)
	waitForAgentStatus(t, runner, agent.ID, "completed")
}

func TestSendMessageResumesEvictedBackgroundAgentFromPersistedTranscriptByName(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	call := 0
	resumeEntered := make(chan struct{})
	resumeRelease := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		call++
		switch call {
		case 1:
			return &AgentExecResult{Result: "done", Messages: []*schema.Message{
				{Role: schema.User, Content: "evict me"},
				{Role: schema.Assistant, Content: "done"},
			}}, nil
		case 2:
			if opts.AgentID == "" {
				t.Fatal("expected evicted resume to reuse the persisted agent id")
			}
			if opts.Task != "resume from disk" {
				t.Fatalf("expected SendMessage content as resume prompt, got %q", opts.Task)
			}
			if len(opts.ResumeMessages) != 2 || opts.ResumeMessages[0].Content != "evict me" || opts.ResumeMessages[1].Content != "done" {
				t.Fatalf("expected persisted transcript as ResumeMessages, got %#v", opts.ResumeMessages)
			}
			close(resumeEntered)
			<-resumeRelease
			return &AgentExecResult{Result: "resumed from disk"}, nil
		default:
			t.Fatalf("unexpected executor call %d", call)
			return nil, nil
		}
	}})
	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:         "evict me",
		Description:  "Evicted worker",
		Name:         "evicted-worker",
		SubagentType: "general-purpose",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground returned error: %v", err)
		return
	}
	waitForAgentStatus(t, runner, agent.ID, "completed")
	runner.Cleanup(-time.Second)

	manager := NewTaskManager()
	resp, err := executeSendMessageWithRuntime(
		`{"recipient":"evicted-worker","content":"resume from disk"}`,
		runner,
		manager,
	)
	if err != nil {
		t.Fatalf("executeSendMessage returned error for evicted persisted resume: %v", err)
		return
	}
	if !strings.Contains(resp, "resumed") || !strings.Contains(resp, agent.ID) {
		t.Fatalf("expected evicted resume response with agent id, got %q", resp)
	}
	select {
	case <-resumeEntered:
	case <-time.After(time.Second):
		t.Fatal("evicted resumed executor was not entered")
	}
	snapshot, ok := runner.GetAgentSnapshot(agent.ID)
	if !ok {
		t.Fatalf("expected evicted agent %q to be re-registered", agent.ID)
	}
	if snapshot.Status != "running" {
		t.Fatalf("expected evicted agent to be running after resume, got %q", snapshot.Status)
	}
	if len(snapshot.Messages) != 3 || snapshot.Messages[2].Content != "resume from disk" {
		t.Fatalf("expected running snapshot to include persisted transcript plus resume prompt, got %#v", snapshot.Messages)
	}

	queuedResp, err := executeSendMessageWithRuntime(
		`{"recipient":"evicted-worker","content":"queued after evicted resume"}`,
		runner,
		manager,
	)
	if err != nil {
		t.Fatalf("executeSendMessage returned error while evicted-resumed agent running: %v", err)
		return
	}
	if !strings.Contains(queuedResp, "Message delivered") {
		t.Fatalf("expected running evicted-resumed agent delivery response, got %q", queuedResp)
	}
	drained, err := runner.DrainAgentMessages(agent.ID)
	if err != nil {
		t.Fatalf("DrainAgentMessages returned error: %v", err)
		return
	}
	if len(drained) != 1 || drained[0].Content != "queued after evicted resume" {
		t.Fatalf("expected FIFO pending message after evicted resume, got %#v", drained)
	}

	close(resumeRelease)
	waitForAgentStatus(t, runner, agent.ID, "completed")
}

func TestSendMessageResumesEvictedBackgroundAgentFromPersistedTranscriptByRawID(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	call := 0
	resumeEntered := make(chan struct{})
	resumeRelease := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		call++
		if call == 1 {
			return &AgentExecResult{Result: "raw done", Messages: []*schema.Message{
				{Role: schema.User, Content: "raw original"},
				{Role: schema.Assistant, Content: "raw done"},
			}}, nil
		}
		if opts.Task != "raw id resume" {
			t.Fatalf("expected raw SendMessage content as resume prompt, got %q", opts.Task)
		}
		if len(opts.ResumeMessages) != 2 || opts.ResumeMessages[0].Content != "raw original" || opts.ResumeMessages[1].Content != "raw done" {
			t.Fatalf("expected raw-id persisted transcript as ResumeMessages, got %#v", opts.ResumeMessages)
		}
		close(resumeEntered)
		<-resumeRelease
		return &AgentExecResult{Result: "raw resumed"}, nil
	}})

	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "raw original",
		Description: "Raw evicted worker",
		Name:        "raw-evicted-worker",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground returned error: %v", err)
		return
	}
	waitForAgentStatus(t, runner, agent.ID, "completed")
	runner.Cleanup(-time.Second)

	_, disposition, err := runner.SendOrResumeAgentMessage(agent.ID, MessagePayload{Content: "raw id resume"})
	if err != nil {
		t.Fatalf("SendOrResumeAgentMessage returned error for raw-id evicted resume: %v", err)
		return
	}
	if disposition != "resumed" {
		t.Fatalf("expected raw-id evicted resume disposition, got %q", disposition)
	}
	select {
	case <-resumeEntered:
	case <-time.After(time.Second):
		t.Fatal("raw-id evicted resumed executor was not entered")
	}
	close(resumeRelease)
	waitForAgentStatus(t, runner, agent.ID, "completed")
}

func TestSendMessageReportsDeterministicEvictedMissingPersistedData(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		return &AgentExecResult{Result: "done"}, nil
	}})
	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "evict missing data",
		Description: "Evicted missing data worker",
		Name:        "evicted-missing-data-worker",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground returned error: %v", err)
		return
	}
	waitForAgentStatus(t, runner, agent.ID, "completed")
	runner.Cleanup(-time.Second)
	if err := os.Remove(filepath.Join(runner.outputDir, "agents", agent.ID+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove metadata: %v", err)
		return
	}

	_, err = executeSendMessageWithRuntime(
		`{"recipient":"evicted-missing-data-worker","content":"resume missing data"}`,
		runner,
		NewTaskManager(),
	)
	if err == nil || !strings.Contains(err.Error(), "cannot resume evicted local agent") || !strings.Contains(err.Error(), "missing persisted metadata") {
		t.Fatalf("expected deterministic missing persisted metadata error, got %v", err)
		return
	}
	if strings.Contains(err.Error(), "not found as a task") {
		t.Fatalf("expected runner error, got task fallback masking error: %v", err)
	}
}

func TestSendMessageReportsDeterministicEvictedCorruptPersistedData(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		return &AgentExecResult{Result: "done"}, nil
	}})
	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "evict corrupt data",
		Description: "Evicted corrupt data worker",
		Name:        "evicted-corrupt-data-worker",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground returned error: %v", err)
		return
	}
	waitForAgentStatus(t, runner, agent.ID, "completed")
	runner.Cleanup(-time.Second)
	metadataPath := filepath.Join(runner.outputDir, "agents", agent.ID+".json")
	if err := os.MkdirAll(filepath.Dir(metadataPath), 0o755); err != nil {
		t.Fatalf("create metadata dir: %v", err)
		return
	}
	if err := os.WriteFile(metadataPath, []byte(`{"agent_id":`), 0o644); err != nil {
		t.Fatalf("corrupt metadata: %v", err)
		return
	}

	_, err = executeSendMessageWithRuntime(
		`{"recipient":"evicted-corrupt-data-worker","content":"resume corrupt data"}`,
		runner,
		NewTaskManager(),
	)
	if err == nil || !strings.Contains(err.Error(), "cannot resume evicted local agent") || !strings.Contains(err.Error(), "corrupt persisted metadata") {
		t.Fatalf("expected deterministic corrupt persisted metadata error, got %v", err)
		return
	}
}

func TestSendMessageReportsDeterministicEvictedMissingPersistedTranscript(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		return &AgentExecResult{Result: "done", Messages: []*schema.Message{
			{Role: schema.User, Content: opts.Task},
			{Role: schema.Assistant, Content: "done"},
		}}, nil
	}})
	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "evict missing transcript",
		Description: "Evicted missing transcript worker",
		Name:        "evicted-missing-transcript-worker",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground returned error: %v", err)
		return
	}
	waitForAgentStatus(t, runner, agent.ID, "completed")
	runner.Cleanup(-time.Second)
	if err := os.Remove(filepath.Join(runner.outputDir, "transcripts", agent.ID+".jsonl")); err != nil {
		t.Fatalf("remove transcript: %v", err)
		return
	}

	_, err = executeSendMessageWithRuntime(
		`{"recipient":"evicted-missing-transcript-worker","content":"resume missing transcript"}`,
		runner,
		NewTaskManager(),
	)
	if err == nil || !strings.Contains(err.Error(), "cannot resume evicted local agent") || !strings.Contains(err.Error(), "missing persisted transcript") {
		t.Fatalf("expected deterministic missing persisted transcript error, got %v", err)
		return
	}
	if strings.Contains(err.Error(), "not found as a task") {
		t.Fatalf("expected runner error, got task fallback masking error: %v", err)
	}
}

func TestSendMessageReportsDeterministicEvictedCorruptPersistedTranscript(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		return &AgentExecResult{Result: "done", Messages: []*schema.Message{
			{Role: schema.User, Content: opts.Task},
			{Role: schema.Assistant, Content: "done"},
		}}, nil
	}})
	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "evict corrupt transcript",
		Description: "Evicted corrupt transcript worker",
		Name:        "evicted-corrupt-transcript-worker",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground returned error: %v", err)
		return
	}
	waitForAgentStatus(t, runner, agent.ID, "completed")
	runner.Cleanup(-time.Second)
	transcriptPath := filepath.Join(runner.outputDir, "transcripts", agent.ID+".jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"timestamp":`), 0o644); err != nil {
		t.Fatalf("corrupt transcript: %v", err)
		return
	}

	_, err = executeSendMessageWithRuntime(
		`{"recipient":"evicted-corrupt-transcript-worker","content":"resume corrupt transcript"}`,
		runner,
		NewTaskManager(),
	)
	if err == nil || !strings.Contains(err.Error(), "cannot resume evicted local agent") || !strings.Contains(err.Error(), "corrupt persisted transcript") && !strings.Contains(err.Error(), "missing persisted transcript") {
		t.Fatalf("expected deterministic corrupt persisted transcript error, got %v", err)
		return
	}
	if strings.Contains(err.Error(), "not found as a task") {
		t.Fatalf("expected runner error, got task fallback masking error: %v", err)
	}
}

func TestSendMessageRoutesDuplicateNamesToLatestBackgroundAgent(t *testing.T) {
	runner := NewAgentRunner(2)
	runner.SetOutputDir(t.TempDir())
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		entered <- struct{}{}
		<-release
		return &AgentExecResult{Result: "done"}, nil
	}})
	first, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "first worker",
		Description: "First worker",
		Name:        "worker",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground first returned error: %v", err)
		return
	}
	second, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "second worker",
		Description: "Second worker",
		Name:        "worker",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground second returned error: %v", err)
		return
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("executor was not entered for both agents")
		}
	}

	resp, err := executeSendMessageWithRuntime(
		`{"recipient":"worker","content":"latest only"}`,
		runner,
		NewTaskManager(),
	)
	if err != nil {
		t.Fatalf("executeSendMessage returned error: %v", err)
		return
	}
	if !strings.Contains(resp, second.ID) {
		t.Fatalf("expected SendMessage response to identify latest agent %q, got %q", second.ID, resp)
	}

	firstSnapshot, ok := runner.GetAgentSnapshot(first.ID)
	if !ok {
		t.Fatalf("expected first agent %q to remain tracked", first.ID)
	}
	secondSnapshot, ok := runner.GetAgentSnapshot(second.ID)
	if !ok {
		t.Fatalf("expected second agent %q to remain tracked", second.ID)
	}
	if firstSnapshot.PendingMessageCount != 0 {
		t.Fatalf("expected first duplicate-name agent to receive no messages, got %d", firstSnapshot.PendingMessageCount)
	}
	if secondSnapshot.PendingMessageCount != 1 || secondSnapshot.PendingMessages[0].Content != "latest only" {
		t.Fatalf("expected latest duplicate-name agent to receive message, got %#v", secondSnapshot.PendingMessages)
	}

	close(release)
	waitForAgentStatus(t, runner, first.ID, "completed")
	waitForAgentStatus(t, runner, second.ID, "completed")
}

func TestAgentToolBackgroundResponseIncludesAgentIDAndOutputFile(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	entered := make(chan struct{})
	release := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		close(entered)
		<-release
		return &AgentExecResult{Result: "tool background done"}, nil
	}})
	resp, err := AgentTool().ExecuteCtx(
		WithAgentRunner(context.Background(), runner),
		`{"description":"Tool background","prompt":"run in background","run_in_background":true}`,
	)
	if err != nil {
		t.Fatalf("AgentTool background Execute returned error: %v", err)
		return
	}
	agentID := responseValue(t, resp, "Agent ID")
	outputFile := responseValue(t, resp, "Output file")
	if agentID == "" {
		t.Fatalf("expected response to include Agent ID, got:\n%s", resp)
	}
	if outputFile == "" {
		t.Fatalf("expected response to include output file, got:\n%s", resp)
	}

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("executor was not entered")
	}
	snapshot, ok := runner.GetAgentSnapshot(agentID)
	if !ok {
		t.Fatalf("expected response agent id %q to identify tracked lifecycle state", agentID)
	}
	if snapshot.OutputFile != outputFile {
		t.Fatalf("expected response output file %q, got tracked %q", outputFile, snapshot.OutputFile)
	}

	close(release)
	waitForAgentStatus(t, runner, agentID, "completed")
}

func TestAgentToolBackgroundExecutionSurvivesParentTurnCancellation(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	entered := make(chan struct{})
	probe := make(chan struct{})
	survived := make(chan bool, 1)
	returned := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(
		ctx context.Context,
		_ AgentExecOptions,
	) (*AgentExecResult, error) {
		close(entered)
		<-probe
		survived <- ctx.Err() == nil
		<-ctx.Done()
		close(returned)
		return nil, ctx.Err()
	}})

	parentCtx, cancelParent := context.WithCancel(
		WithAgentRunner(context.Background(), runner),
	)
	response, err := AgentTool().ExecuteCtx(
		parentCtx,
		`{"description":"survive parent","prompt":"keep running","run_in_background":true}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	agentID := responseValue(t, response, "Agent ID")
	<-entered
	cancelParent()
	close(probe)
	if ok := <-survived; !ok {
		t.Fatal("background generation inherited parent turn cancellation")
	}

	if err := runner.AbortAgent(agentID); err != nil {
		t.Fatalf("targeted abort: %v", err)
	}
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("targeted abort did not cancel background generation")
	}
	snapshot := waitForAgentStatus(t, runner, agentID, "aborted")
	if snapshot.Error == nil {
		t.Fatalf("aborted background generation did not settle its error: %#v", snapshot)
	}
}

func TestAgentToolCarriesInheritedPermissionMode(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		if opts.InheritedPermissionMode != "bypassPermissions" {
			t.Fatalf("InheritedPermissionMode = %q, want bypassPermissions", opts.InheritedPermissionMode)
		}
		return &AgentExecResult{Result: "done"}, nil
	}})
	ctx := WithInheritedPermissionMode(context.Background(), "bypassPermissions")
	ctx = WithAgentRunner(ctx, runner)
	if _, err := AgentTool().ExecuteCtx(ctx, `{"description":"inherit mode","prompt":"inspect"}`); err != nil {
		t.Fatalf("AgentTool ExecuteCtx returned error: %v", err)
		return
	}
}

func onlyTrackedAgent(t *testing.T, runner *AgentRunner) *RunningAgent {
	t.Helper()
	runner.mu.RLock()
	defer runner.mu.RUnlock()
	if len(runner.activeAgents) != 1 {
		t.Fatalf("expected one tracked agent, got %d", len(runner.activeAgents))
	}
	for _, agent := range runner.activeAgents {
		agent.mu.Lock()
		defer agent.mu.Unlock()
		copy := *agent
		copy.Messages = append([]*schema.Message(nil), agent.Messages...)
		return &copy
	}
	t.Fatal("unreachable")
	return nil
}

func waitForAgentStatus(t *testing.T, runner *AgentRunner, agentID, status string) RunningAgent {
	t.Helper()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			snapshot, ok := runner.GetAgentSnapshot(agentID)
			if !ok {
				t.Fatalf("agent %q was not registered", agentID)
			}
			t.Fatalf("timed out waiting for status %q, got %q", status, snapshot.Status)
		case <-ticker.C:
			snapshot, ok := runner.GetAgentSnapshot(agentID)
			if ok && snapshot.Status == status {
				return snapshot
			}
		}
	}
}

func responseValue(t *testing.T, response, key string) string {
	t.Helper()
	prefix := key + ": "
	for _, line := range strings.Split(response, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func TestAgentRunnerNotificationRequiresExplicitAcknowledgment(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.activeAgents["agent-notify"] = &RunningAgent{
		ID:               "agent-notify",
		Status:           "completed",
		Description:      "notify",
		CompletedAt:      time.Now().UTC(),
		TerminalSequence: 3,
		terminalDurable:  true,
		runGeneration:    3,
		mu:               &sync.Mutex{},
	}

	first := runner.PendingAgentNotifications()
	second := runner.PendingAgentNotifications()
	if len(first) != 1 || len(second) != 1 ||
		!strings.HasPrefix(first[0].DeliveryID(), "agent-completion:v1:") ||
		first[0].TerminalSequence != 3 ||
		second[0].DeliveryID() != first[0].DeliveryID() {
		t.Fatalf("pending notifications = %#v / %#v", first, second)
	}
	runner.AcknowledgeAgentNotifications([]string{first[0].DeliveryID()})
	if remaining := runner.PendingAgentNotifications(); len(remaining) != 0 {
		t.Fatalf("acknowledged notification remains: %#v", remaining)
	}
}

func TestAgentRunnerReconstructsDurableCompletionForExactParent(t *testing.T) {
	outputDir := t.TempDir()
	runner := NewAgentRunner(1)
	runner.SetOutputDir(outputDir)
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(
		context.Context,
		AgentExecOptions,
	) (*AgentExecResult, error) {
		return &AgentExecResult{Result: "durable result"}, nil
	}})

	started, err := RunAgentBackground(
		context.Background(),
		runner,
		AgentExecOptions{
			Task:            "persist completion",
			Description:     "Persist completion",
			ParentSessionID: "parent-session",
			ParentThreadID:  "parent-thread",
			ToolUseID:       "parent-tool",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	terminal := waitForAgentStatus(t, runner, started.ID, "completed")
	if terminal.Completion == nil ||
		terminal.TerminalSequence != 1 ||
		!terminal.terminalDurable {
		t.Fatalf("terminal completion = %#v", terminal)
	}

	first, err := runner.PendingAgentNotificationsForParent(
		"parent-session",
		"parent-thread",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 ||
		first[0].DeliveryID() != terminal.Completion.CompletionID ||
		first[0].TerminalSequence != 1 {
		t.Fatalf("first durable notification = %#v", first)
	}
	if wrong, err := runner.PendingAgentNotificationsForParent(
		"other-session",
		"parent-thread",
		"",
	); err != nil || len(wrong) != 0 {
		t.Fatalf("wrong-parent notifications = %#v, err=%v", wrong, err)
	}

	fresh := NewAgentRunner(1)
	fresh.SetOutputDir(outputDir)
	if _, err := fresh.RegisterPersistedAgent(started.ID); err != nil {
		t.Fatal(err)
	}
	reconstructed, err := fresh.PendingAgentNotificationsForParent(
		"parent-session",
		"parent-thread",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(reconstructed) != 1 ||
		reconstructed[0].DeliveryID() != first[0].DeliveryID() ||
		reconstructed[0].Message != first[0].Message {
		t.Fatalf(
			"reconstructed notification = %#v, want %#v",
			reconstructed,
			first,
		)
	}
}

func TestAgentRunnerLoadsLegacyTerminalMetadataWithStableMigrationIdentity(
	t *testing.T,
) {
	outputDir := t.TempDir()
	producer := NewAgentRunner(1)
	producer.SetOutputDir(outputDir)
	producer.SetExecutor(fakeAgentExecutor{onExecute: func(
		context.Context,
		AgentExecOptions,
	) (*AgentExecResult, error) {
		return &AgentExecResult{Result: "legacy result"}, nil
	}})
	started, err := RunAgentBackground(
		context.Background(),
		producer,
		AgentExecOptions{
			Task:            "legacy metadata",
			ParentSessionID: "legacy-parent",
			ParentThreadID:  "legacy-thread",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	waitForAgentStatus(t, producer, started.ID, "completed")

	metadataPath := filepath.Join(outputDir, "agents", started.ID+".json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "terminal_sequence")
	delete(legacy, "completion")
	data, err = json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	fresh := NewAgentRunner(1)
	fresh.SetOutputDir(outputDir)
	if _, err := fresh.RegisterPersistedAgent(started.ID); err != nil {
		t.Fatal(err)
	}
	notifications, err := fresh.PendingAgentNotificationsForParent(
		"legacy-parent",
		"legacy-thread",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(notifications) != 1 ||
		notifications[0].TerminalSequence != 1 ||
		notifications[0].LegacyDeliveryID() !=
			"agent-notification:"+started.ID+":1" {
		t.Fatalf("legacy notification migration = %#v", notifications)
	}
}

func TestAgentRunnerDoesNotPublishTerminalWhenPersistenceFails(t *testing.T) {
	root := t.TempDir()
	outputDir := filepath.Join(root, "agent-output")
	runner := NewAgentRunner(1)
	runner.SetOutputDir(outputDir)
	entered := make(chan struct{})
	release := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(
		context.Context,
		AgentExecOptions,
	) (*AgentExecResult, error) {
		close(entered)
		<-release
		return &AgentExecResult{Result: "must not publish"}, nil
	}})

	started, err := RunAgentBackground(
		context.Background(),
		runner,
		AgentExecOptions{
			Task:            "fail terminal persistence",
			ParentSessionID: "parent-session",
			ParentThreadID:  "parent-thread",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	backup := outputDir + "-backup"
	if err := os.Rename(outputDir, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputDir, []byte("block directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitForAgentStatus(t, runner, started.ID, "failed")

	notifications, err := runner.PendingAgentNotificationsForParent(
		"parent-session",
		"parent-thread",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(notifications) != 0 {
		t.Fatalf("non-durable terminal was published: %#v", notifications)
	}
}

func TestAgentRunnerMessagesRequireExplicitAcknowledgment(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.activeAgents["agent-message"] = &RunningAgent{
		ID:     "agent-message",
		Status: "running",
		PendingMessages: []MessagePayload{
			{
				From: "coordinator", To: "agent-message", Content: "first",
				Metadata: map[string]any{"command_uuid": "message-1"},
			},
			{
				From: "coordinator", To: "agent-message", Content: "second",
				Metadata: map[string]any{"command_uuid": "message-2"},
			},
		},
		PendingMessageCount: 2,
		mu:                  &sync.Mutex{},
	}

	first, err := runner.PendingAgentMessages("agent-message")
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.PendingAgentMessages("agent-message")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("pending messages = %#v / %#v", first, second)
	}
	removed, err := runner.AcknowledgeAgentMessages(
		"agent-message",
		[]string{"message-1"},
	)
	if err != nil || removed != 1 {
		t.Fatalf("acknowledge: removed=%d err=%v", removed, err)
	}
	remaining, err := runner.PendingAgentMessages("agent-message")
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 ||
		agentMessageCommandUUID(remaining[0]) != "message-2" {
		t.Fatalf("remaining messages = %#v", remaining)
	}
}
