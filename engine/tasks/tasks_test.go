package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitForStatus(t *testing.T, task Task, want TaskStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if task.Status() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s status = %s, want %s", task.ID(), task.Status(), want)
}

func TestTaskStatusTerminalGuard(t *testing.T) {
	terminal := []TaskStatus{
		TaskStatusCompleted,
		TaskStatusFailed,
		TaskStatusCancelled,
		TaskStatusKilled,
	}
	for _, status := range terminal {
		if !IsTerminalTaskStatus(status) {
			t.Fatalf("expected %s to be terminal", status)
		}
	}

	for _, status := range []TaskStatus{TaskStatusPending, TaskStatusRunning} {
		if IsTerminalTaskStatus(status) {
			t.Fatalf("did not expect %s to be terminal", status)
		}
	}
}

func TestAgentTaskLifecycleSuccessFailureAndCancel(t *testing.T) {
	orig := AgentExecutorFn
	t.Cleanup(func() { AgentExecutorFn = orig })

	t.Run("success passes execution options and completes", func(t *testing.T) {
		var gotPrompt, gotModel string
		var gotMaxTurns int
		var gotTools []string
		AgentExecutorFn = func(ctx context.Context, prompt, model string, maxTurns int, allowedTools []string) (string, error) {
			gotPrompt = prompt
			gotModel = model
			gotMaxTurns = maxTurns
			gotTools = append([]string(nil), allowedTools...)
			return "agent complete", nil
		}

		task := NewAgentTask("inspect repo", "claude-test")
		task.MaxTurns = 7
		task.AllowedTools = []string{"Read", "Grep"}
		if task.Status() != TaskStatusPending {
			t.Fatalf("new task status = %s, want pending", task.Status())
		}
		if err := task.Start(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
			return
		}

		result, err := task.Wait(context.Background())
		if err != nil {
			t.Fatalf("wait failed: %v", err)
			return
		}
		if task.Status() != TaskStatusCompleted {
			t.Fatalf("task status = %s, want completed", task.Status())
		}
		if result.Output != "agent complete" || result.Error != nil || result.ExitCode != 0 {
			t.Fatalf("unexpected result: %#v", result)
			return
		}
		if gotPrompt != "inspect repo" || gotModel != "claude-test" || gotMaxTurns != 7 || strings.Join(gotTools, ",") != "Read,Grep" {
			t.Fatalf("executor options not propagated: prompt=%q model=%q maxTurns=%d tools=%v", gotPrompt, gotModel, gotMaxTurns, gotTools)
		}
	})

	t.Run("executor error marks failed", func(t *testing.T) {
		AgentExecutorFn = func(ctx context.Context, prompt, model string, maxTurns int, allowedTools []string) (string, error) {
			return "", errors.New("agent failed")
		}

		task := NewAgentTask("fail", "claude-test")
		if err := task.Start(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
			return
		}
		result, err := task.Wait(context.Background())
		if err != nil {
			t.Fatalf("wait failed: %v", err)
			return
		}
		if task.Status() != TaskStatusFailed {
			t.Fatalf("task status = %s, want failed", task.Status())
		}
		if result.Error == nil || !strings.Contains(result.Error.Error(), "agent failed") || result.ExitCode != 1 {
			t.Fatalf("unexpected failure result: %#v", result)
			return
		}
	})

	t.Run("cancel transitions running task to cancelled", func(t *testing.T) {
		started := make(chan struct{})
		AgentExecutorFn = func(ctx context.Context, prompt, model string, maxTurns int, allowedTools []string) (string, error) {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		}

		task := NewAgentTask("cancel", "claude-test")
		if err := task.Start(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
			return
		}
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("executor did not start")
		}
		waitForStatus(t, task, TaskStatusRunning)
		if err := task.Cancel(); err != nil {
			t.Fatalf("cancel failed: %v", err)
			return
		}
		result, err := task.Wait(context.Background())
		if err != nil {
			t.Fatalf("wait failed: %v", err)
			return
		}
		if task.Status() != TaskStatusCancelled {
			t.Fatalf("task status = %s, want cancelled", task.Status())
		}
		if result.Error == nil || !errors.Is(result.Error, context.Canceled) || result.ExitCode != -1 {
			t.Fatalf("unexpected cancelled result: %#v", result)
			return
		}
	})
}

func TestAgentTaskDefaultMaxTurnsUnlimited(t *testing.T) {
	if got := NewAgentTask("inspect repo", "claude-test").MaxTurns; got != 0 {
		t.Fatalf("default MaxTurns = %d, want unlimited (0)", got)
	}
}

func TestDreamTaskLifecycleUsesConfiguredModelFn(t *testing.T) {
	orig := DreamModelFn
	t.Cleanup(func() { DreamModelFn = orig })

	DreamModelFn = func(ctx context.Context, prompt, model string) (string, error) {
		if prompt != "summarize session" || model != "dream-model" {
			t.Fatalf("unexpected dream args prompt=%q model=%q", prompt, model)
		}
		return "dream complete", nil
	}

	task := NewDreamTask("summarize session", "dream-model")
	if err := task.Start(context.Background()); err != nil {
		t.Fatalf("start failed: %v", err)
		return
	}
	result, err := task.Wait(context.Background())
	if err != nil {
		t.Fatalf("wait failed: %v", err)
		return
	}
	if task.Status() != TaskStatusCompleted {
		t.Fatalf("task status = %s, want completed", task.Status())
	}
	if result.Output != "dream complete" {
		t.Fatalf("unexpected dream output: %#v", result)
	}
}

func TestShellTaskOutputFailureAndOutputFileEviction(t *testing.T) {
	t.Run("captures stdout and completes", func(t *testing.T) {
		task := NewShellTask("printf 'hello from shell'", "")
		if err := task.Start(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
			return
		}
		result, err := task.Wait(context.Background())
		if err != nil {
			t.Fatalf("wait failed: %v", err)
			return
		}
		if task.Status() != TaskStatusCompleted {
			t.Fatalf("task status = %s, want completed", task.Status())
		}
		if result.Output != "hello from shell" || result.Error != nil || result.ExitCode != 0 {
			t.Fatalf("unexpected shell result: %#v", result)
			return
		}
	})

	t.Run("captures stderr and non-zero exit as failure", func(t *testing.T) {
		task := NewShellTask("printf 'bad news' >&2; exit 7", "")
		if err := task.Start(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
			return
		}
		result, err := task.Wait(context.Background())
		if err != nil {
			t.Fatalf("wait failed: %v", err)
			return
		}
		if task.Status() != TaskStatusFailed {
			t.Fatalf("task status = %s, want failed", task.Status())
		}
		if !strings.Contains(result.Output, "bad news") || result.Error == nil || result.ExitCode != 7 {
			t.Fatalf("unexpected failed shell result: %#v", result)
			return
		}
	})

	t.Run("output file append and task manager eviction", func(t *testing.T) {
		manager := NewTaskManager(1)
		task := NewAgentTask("write output", "claude-test")
		outputPath := filepath.Join(t.TempDir(), "task-output.txt")
		if err := task.SetOutputFile(outputPath); err != nil {
			t.Fatalf("set output file failed: %v", err)
			return
		}
		if err := task.AppendOutput([]byte("first chunk")); err != nil {
			t.Fatalf("append output failed: %v", err)
			return
		}
		if task.OutputOffset() != int64(len("first chunk")) {
			t.Fatalf("output offset = %d, want %d", task.OutputOffset(), len("first chunk"))
		}
		manager.tasks[task.ID()] = task
		manager.EvictTaskOutput(task.ID())
		if task.OutputFile() != "" || task.OutputOffset() != 0 {
			t.Fatalf("expected output fields to be cleared, file=%q offset=%d", task.OutputFile(), task.OutputOffset())
		}
		if _, err := os.Stat(outputPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected output file to be removed, stat err=%v", err)
		}
	})
}

func TestTaskManagerSubmitStopAndValidation(t *testing.T) {
	orig := AgentExecutorFn
	t.Cleanup(func() { AgentExecutorFn = orig })

	started := make(chan struct{})
	release := make(chan struct{})
	AgentExecutorFn = func(ctx context.Context, prompt, model string, maxTurns int, allowedTools []string) (string, error) {
		close(started)
		select {
		case <-release:
			return "released", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	manager := NewTaskManager(1)
	task := NewAgentTask("long running", "claude-test")
	task.SetDescription("Long running agent")
	if err := manager.Submit(task); err != nil {
		t.Fatalf("submit failed: %v", err)
		return
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("submitted task did not start")
	}
	waitForStatus(t, task, TaskStatusRunning)

	if got, ok := manager.Get(task.ID()); !ok || got.ID() != task.ID() {
		t.Fatalf("manager get failed: task=%#v ok=%v", got, ok)
	}
	if listed := manager.List(); len(listed) != 1 || listed[0].ID() != task.ID() {
		t.Fatalf("unexpected task list: %#v", listed)
	}
	if err := manager.Submit(NewAgentTask("second", "claude-test")); err == nil {
		t.Fatal("expected max-concurrency submit failure")
		return
	}

	stopResult, err := manager.StopTask(task.ID(), time.Second)
	if err != nil {
		t.Fatalf("stop task failed: %v", err)
		return
	}
	if stopResult.TaskID != task.ID() || stopResult.TaskType != TaskTypeAgent || stopResult.Command != "Long running agent" {
		t.Fatalf("unexpected stop result: %#v", stopResult)
	}
	if task.Status() != TaskStatusKilled {
		t.Fatalf("task status = %s, want killed", task.Status())
	}

	_, err = manager.StopTask("missing", time.Second)
	var stopErr *StopTaskError
	if !errors.As(err, &stopErr) || stopErr.Code != StopTaskNotFound {
		t.Fatalf("expected not_found stop error, got %T %v", err, err)
	}

	pending := NewAgentTask("pending", "claude-test")
	manager.tasks[pending.ID()] = pending
	_, err = manager.StopTask(pending.ID(), time.Second)
	if !errors.As(err, &stopErr) || stopErr.Code != StopTaskNotRunning {
		t.Fatalf("expected not_running stop error, got %T %v", err, err)
	}

	close(release)
}
