package tools

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestAgentSteeringPauseResume(t *testing.T) {
	s := NewAgentSteering()
	s.Register("agent-1")

	// Pause.
	if err := s.Pause("agent-1"); err != nil {
		t.Fatalf("Pause failed: %v", err)
		return
	}

	info, ok := s.GetInfo("agent-1")
	if !ok {
		t.Fatal("expected agent to be registered")
	}
	if info.State != SteeringStatePaused {
		t.Fatalf("expected paused state, got %q", info.State)
	}

	// Resume.
	if err := s.Resume("agent-1"); err != nil {
		t.Fatalf("Resume failed: %v", err)
		return
	}

	info, _ = s.GetInfo("agent-1")
	if info.State != SteeringStateRunning {
		t.Fatalf("expected running state after resume, got %q", info.State)
	}
	if info.TotalPausedMs == 0 {
		// It might be 0 if the test runs fast enough, but let's verify it's non-negative.
		if info.TotalPausedMs < 0 {
			t.Fatalf("expected non-negative paused time, got %d", info.TotalPausedMs)
		}
	}
}

func TestAgentSteeringPauseIdempotent(t *testing.T) {
	s := NewAgentSteering()
	s.Register("agent-1")

	if err := s.Pause("agent-1"); err != nil {
		t.Fatalf("first pause failed: %v", err)
		return
	}
	// Second pause should be idempotent.
	if err := s.Pause("agent-1"); err != nil {
		t.Fatalf("second pause should be idempotent, got: %v", err)
		return
	}
}

func TestAgentSteeringResumeIdempotent(t *testing.T) {
	s := NewAgentSteering()
	s.Register("agent-1")

	// Resume when already running should be idempotent.
	if err := s.Resume("agent-1"); err != nil {
		t.Fatalf("resume when running should be idempotent, got: %v", err)
		return
	}
}

func TestAgentSteeringPauseNotFound(t *testing.T) {
	s := NewAgentSteering()

	if err := s.Pause("nonexistent"); err == nil {
		t.Fatal("expected error for nonexistent agent")
		return
	}
}

func TestAgentSteeringSetPriority(t *testing.T) {
	s := NewAgentSteering()
	s.Register("agent-1")

	if err := s.SetPriority("agent-1", PriorityHigh); err != nil {
		t.Fatalf("SetPriority failed: %v", err)
		return
	}

	info, _ := s.GetInfo("agent-1")
	if info.Priority != PriorityHigh {
		t.Fatalf("expected high priority, got %d", info.Priority)
	}
}

func TestAgentSteeringForceStop(t *testing.T) {
	runner := NewAgentRunner(4)
	runner.SetOutputDir(t.TempDir())

	entered := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}})

	s := NewAgentSteering()

	// Start a background agent.
	snapshot, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "long task",
		Description: "Long task",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground failed: %v", err)
		return
	}

	<-entered

	s.Register(snapshot.ID)

	// Force stop.
	if err := s.ForceStop(snapshot.ID, runner); err != nil {
		t.Fatalf("ForceStop failed: %v", err)
		return
	}

	info, _ := s.GetInfo(snapshot.ID)
	if info.State != SteeringStateStopped {
		t.Fatalf("expected stopped state, got %q", info.State)
	}

	// Verify the runner agent is aborted.
	waitForAgentStatus(t, runner, snapshot.ID, "aborted")
}

func TestAgentSteeringForceStopIdempotent(t *testing.T) {
	s := NewAgentSteering()
	s.Register("agent-1")

	if err := s.ForceStop("agent-1", nil); err != nil {
		t.Fatalf("first ForceStop failed: %v", err)
		return
	}
	// Second should be idempotent.
	if err := s.ForceStop("agent-1", nil); err != nil {
		t.Fatalf("second ForceStop should be idempotent, got: %v", err)
		return
	}
}

func TestAgentSteeringWaitIfPaused(t *testing.T) {
	s := NewAgentSteering()
	s.Register("agent-1")

	// Not paused: should return immediately.
	wasPaused, err := s.WaitIfPaused("agent-1")
	if err != nil {
		t.Fatalf("WaitIfPaused failed: %v", err)
		return
	}
	if wasPaused {
		t.Fatal("expected wasPaused=false when not paused")
	}

	// Pause, then resume from another goroutine.
	if err := s.Pause("agent-1"); err != nil {
		t.Fatalf("Pause failed: %v", err)
		return
	}

	done := make(chan bool, 1)
	go func() {
		paused, _ := s.WaitIfPaused("agent-1")
		done <- paused
	}()

	// Give the goroutine time to block.
	time.Sleep(10 * time.Millisecond)

	if err := s.Resume("agent-1"); err != nil {
		t.Fatalf("Resume failed: %v", err)
		return
	}

	select {
	case paused := <-done:
		if !paused {
			t.Fatal("expected wasPaused=true after being paused then resumed")
		}
	case <-time.After(time.Second):
		t.Fatal("WaitIfPaused did not return after resume")
	}
}

func TestAgentSteeringWaitIfPausedContextReleasesOnCancellation(t *testing.T) {
	s := NewAgentSteering()
	s.Register("agent-1")
	if err := s.Pause("agent-1"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wasPaused, err := s.WaitIfPausedContext(ctx, "agent-1")
	if !wasPaused || err != context.Canceled {
		t.Fatalf("cancelled pause wait = paused:%v err:%v, want true/context.Canceled", wasPaused, err)
	}
	if !s.IsPaused("agent-1") {
		t.Fatal("cancelled waiter mutated steering state")
	}
}

func TestAgentSteeringConcurrentPauseResume(t *testing.T) {
	s := NewAgentSteering()
	s.Register("agent-1")

	var wg sync.WaitGroup
	iterations := 100

	// Concurrent pause/resume should not panic or deadlock.
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = s.Pause("agent-1")
			_ = s.Resume("agent-1")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = s.Pause("agent-1")
			_ = s.Resume("agent-1")
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success: no deadlock.
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent pause/resume deadlocked")
	}

	// Final state should be running (last operation was resume).
	info, _ := s.GetInfo("agent-1")
	if info.State != SteeringStateRunning {
		t.Fatalf("expected running state after concurrent ops, got %q", info.State)
	}
}

func TestAgentSteeringListAll(t *testing.T) {
	s := NewAgentSteering()
	s.Register("agent-1")
	s.Register("agent-2")
	s.Register("agent-3")

	list := s.ListAll()
	if len(list) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(list))
	}
}

func TestAgentSteeringUnregister(t *testing.T) {
	s := NewAgentSteering()
	s.Register("agent-1")
	s.Unregister("agent-1")

	_, ok := s.GetInfo("agent-1")
	if ok {
		t.Fatal("expected agent to be unregistered")
	}
}

func TestAgentSteeringIsPaused(t *testing.T) {
	s := NewAgentSteering()
	s.Register("agent-1")

	if s.IsPaused("agent-1") {
		t.Fatal("expected not paused initially")
	}

	_ = s.Pause("agent-1")
	if !s.IsPaused("agent-1") {
		t.Fatal("expected paused after pause")
	}

	_ = s.Resume("agent-1")
	if s.IsPaused("agent-1") {
		t.Fatal("expected not paused after resume")
	}
}

func TestAgentSteeringAgentStatus(t *testing.T) {
	runner := NewAgentRunner(4)
	runner.SetOutputDir(t.TempDir())

	entered := make(chan struct{})
	release := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		close(entered)
		<-release
		return &AgentExecResult{Result: "done"}, nil
	}})

	s := NewAgentSteering()

	// Start a background agent.
	snapshot, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "status test",
		Description: "Status test agent",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground failed: %v", err)
		return
	}
	<-entered
	s.Register(snapshot.ID)

	status, err := s.AgentStatus(snapshot.ID, runner)
	if err != nil {
		t.Fatalf("AgentStatus failed: %v", err)
		return
	}
	if status.SteeringState != SteeringStateRunning {
		t.Fatalf("expected running steering state, got %q", status.SteeringState)
	}
	if status.RunnerStatus != "running" {
		t.Fatalf("expected runner status 'running', got %q", status.RunnerStatus)
	}
	if status.Description != "Status test agent" {
		t.Fatalf("expected description 'Status test agent', got %q", status.Description)
	}

	close(release)
	waitForAgentStatus(t, runner, snapshot.ID, "completed")
}

func TestAgentSteeringForceStopWhilePaused(t *testing.T) {
	runner := NewAgentRunner(4)
	runner.SetOutputDir(t.TempDir())

	entered := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}})

	s := NewAgentSteering()

	snapshot, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "pause-stop test",
		Description: "Pause then stop",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground failed: %v", err)
		return
	}
	<-entered
	s.Register(snapshot.ID)

	// Pause first.
	if err := s.Pause(snapshot.ID); err != nil {
		t.Fatalf("Pause failed: %v", err)
		return
	}

	// Force stop while paused.
	if err := s.ForceStop(snapshot.ID, runner); err != nil {
		t.Fatalf("ForceStop while paused failed: %v", err)
		return
	}

	info, _ := s.GetInfo(snapshot.ID)
	if info.State != SteeringStateStopped {
		t.Fatalf("expected stopped state, got %q", info.State)
	}

	waitForAgentStatus(t, runner, snapshot.ID, "aborted")
}
