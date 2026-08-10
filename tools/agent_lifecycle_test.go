package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

// =============================================================================
// Scenario 1: Full Agent Lifecycle
// create -> run -> tool calls -> complete -> read transcript
// =============================================================================

func TestScenarioAgentFullLifecycle(t *testing.T) {
	runner := NewAgentRunner(4)
	runner.SetOutputDir(t.TempDir())

	// Simulate an agent that uses tools and produces structured output.
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		return &AgentExecResult{
			Result:     "Task completed successfully. Found 3 issues in the codebase.",
			TurnCount:  4,
			TokensUsed: 2500,
			ToolsUsed:  []string{"Read", "Grep", "Edit"},
			Messages: []*schema.Message{
				{Role: schema.User, Content: opts.Task},
				{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
					ID: "call_1", Type: "function",
					Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"main.go"}`},
				}}},
				{Role: schema.Tool, ToolCallID: "call_1", ToolName: "Read", Content: "package main\n..."},
				{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
					ID: "call_2", Type: "function",
					Function: schema.FunctionCall{Name: "Grep", Arguments: `{"pattern":"TODO"}`},
				}}},
				{Role: schema.Tool, ToolCallID: "call_2", ToolName: "Grep", Content: "main.go:10: // TODO: fix"},
				{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
					ID: "call_3", Type: "function",
					Function: schema.FunctionCall{Name: "Edit", Arguments: `{"file":"main.go"}`},
				}}},
				{Role: schema.Tool, ToolCallID: "call_3", ToolName: "Edit", Content: "File edited successfully"},
				{Role: schema.Assistant, Content: "Task completed successfully. Found 3 issues in the codebase."},
			},
		}, nil
	}})

	// Phase 1: Create and run the agent.
	result, err := RunAgent(context.Background(), runner, AgentExecOptions{
		Task:        "Find and fix TODO comments in the codebase",
		Description: "Fix TODOs",
		Name:        "todo-fixer",
		ToolUseID:   "toolu_lifecycle",
	})
	if err != nil {
		t.Fatalf("RunAgent failed: %v", err)
		return
	}
	if result.Result != "Task completed successfully. Found 3 issues in the codebase." {
		t.Fatalf("unexpected result: %q", result.Result)
	}
	if result.TurnCount != 4 {
		t.Fatalf("expected 4 turns, got %d", result.TurnCount)
	}
	if result.TokensUsed != 2500 {
		t.Fatalf("expected 2500 tokens, got %d", result.TokensUsed)
	}

	// Phase 2: Verify lifecycle state.
	_, _ = runner.GetAgentSnapshot(result.Result[:0] + "")
	// Look up by name since IDs are auto-generated.
	runner.mu.RLock()
	var agentID string
	for id := range runner.activeAgents {
		agentID = id
	}
	runner.mu.RUnlock()

	snapshot, ok := runner.GetAgentSnapshot(agentID)
	if !ok {
		t.Fatal("expected agent to be tracked after completion")
	}
	if snapshot.Status != "completed" {
		t.Fatalf("expected completed status, got %q", snapshot.Status)
	}
	if snapshot.Description != "Fix TODOs" {
		t.Fatalf("expected description 'Fix TODOs', got %q", snapshot.Description)
	}
	if snapshot.Name != "todo-fixer" {
		t.Fatalf("expected name 'todo-fixer', got %q", snapshot.Name)
	}

	// Phase 3: Read transcript via accessor.
	accessor := NewAgentTranscriptAccess(runner)
	view, err := accessor.GetTranscript(agentID)
	if err != nil {
		t.Fatalf("GetTranscript failed: %v", err)
		return
	}
	if view.Status != "completed" {
		t.Fatalf("expected completed in transcript view, got %q", view.Status)
	}
	if view.IsRunning {
		t.Fatal("expected IsRunning=false for completed agent")
	}
	if len(view.Messages) != 8 {
		t.Fatalf("expected 8 messages in transcript, got %d", len(view.Messages))
	}

	// Phase 4: Export as markdown.
	md, err := accessor.ExportMarkdown(agentID)
	if err != nil {
		t.Fatalf("ExportMarkdown failed: %v", err)
		return
	}
	if !strings.Contains(md, "# Agent Transcript: Fix TODOs") {
		t.Fatalf("expected markdown header, got:\n%s", md)
	}
	if !strings.Contains(md, "**Tool Call:** `Read`") {
		t.Fatalf("expected tool call in markdown, got:\n%s", md)
	}
	if !strings.Contains(md, "**Status:** completed") {
		t.Fatalf("expected completed status in markdown, got:\n%s", md)
	}
}

// =============================================================================
// Scenario 2: Background Agent with Foreground/Background Transitions
// start -> move to background -> check progress -> move to foreground
// =============================================================================

func TestScenarioBackgroundForegroundTransition(t *testing.T) {
	runner := NewAgentRunner(4)
	runner.SetOutputDir(t.TempDir())

	entered := make(chan struct{})
	release := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		close(entered)
		<-release
		return &AgentExecResult{Result: "background work done", TokensUsed: 100}, nil
	}})

	displayState := NewAgentDisplayState()
	progressStream := NewAgentProgressStream(20)

	// Phase 1: Start agent in foreground.
	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "Long running analysis",
		Description: "Long analysis",
		Name:        "analyzer",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground failed: %v", err)
		return
	}

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("executor was not entered")
	}

	displayState.Register(agent.ID, DisplayModeForeground)
	progressStream.RegisterAgent(agent.ID)

	// Phase 2: Verify foreground state.
	if !displayState.IsForeground(agent.ID) {
		t.Fatal("expected agent to be in foreground")
	}
	if displayState.ForegroundAgent() != agent.ID {
		t.Fatalf("expected foreground agent to be %q", agent.ID)
	}

	// Phase 3: Move to background.
	if err := displayState.MoveToBackground(agent.ID); err != nil {
		t.Fatalf("MoveToBackground failed: %v", err)
		return
	}
	if displayState.IsForeground(agent.ID) {
		t.Fatal("expected agent to no longer be in foreground")
	}
	if displayState.ForegroundAgent() != "" {
		t.Fatalf("expected no foreground agent, got %q", displayState.ForegroundAgent())
	}
	mode, ok := displayState.GetMode(agent.ID)
	if !ok || mode != DisplayModeBackground {
		t.Fatalf("expected background mode, got %v", mode)
	}

	// Phase 4: Emit progress while in background.
	progressStream.Emit(StreamProgressEvent{
		AgentID:  agent.ID,
		Type:     ProgressEventToolStart,
		ToolName: "Read",
	})
	progressStream.Emit(StreamProgressEvent{
		AgentID:  agent.ID,
		Type:     ProgressEventToolEnd,
		ToolName: "Read",
	})

	// Phase 5: Check buffered progress.
	buffered := progressStream.BufferedEvents(agent.ID)
	if len(buffered) != 2 {
		t.Fatalf("expected 2 buffered events, got %d", len(buffered))
	}

	// Phase 6: Move back to foreground.
	if err := displayState.MoveToForeground(agent.ID); err != nil {
		t.Fatalf("MoveToForeground failed: %v", err)
		return
	}
	if !displayState.IsForeground(agent.ID) {
		t.Fatal("expected agent back in foreground")
	}

	// Phase 7: Subscribe to progress stream (late-joining gets buffer).
	listener := progressStream.Subscribe(agent.ID, 50)
	if listener == nil {
		t.Fatal("expected non-nil listener")
		return
	}
	defer listener.Close()

	// Should receive buffered events.
	received := 0
	drainTimer := time.After(100 * time.Millisecond)
drainLoop:
	for {
		select {
		case <-listener.Events:
			received++
		case <-drainTimer:
			break drainLoop
		}
	}
	if received != 2 {
		t.Fatalf("expected 2 buffered events from subscribe, got %d", received)
	}

	// Phase 8: Complete.
	close(release)
	waitForAgentStatus(t, runner, agent.ID, "completed")

	progressStream.UnregisterAgent(agent.ID)
	displayState.Unregister(agent.ID)
}

// =============================================================================
// Scenario 3: Team Execution (3 agents with roles -> parallel -> all complete)
// =============================================================================

func TestScenarioTeamParallelExecution(t *testing.T) {
	runner := NewAgentRunner(4)
	runner.SetOutputDir(t.TempDir())

	var callCount int32
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		atomic.AddInt32(&callCount, 1)
		// Simulate varying execution times.
		time.Sleep(10 * time.Millisecond)
		role := "unknown"
		if strings.Contains(opts.Description, "architect") {
			role = "architect"
		} else if strings.Contains(opts.Description, "implementer") {
			role = "implementer"
		} else if strings.Contains(opts.Description, "tester") {
			role = "tester"
		}
		return &AgentExecResult{
			Result:     fmt.Sprintf("%s: completed analysis", role),
			TokensUsed: 500,
			ToolsUsed:  []string{"Read"},
		}, nil
	}})

	teamRunner := NewTeamRunner(runner)
	config := TeamRunConfig{
		TeamID: "team-feature-1",
		Goal:   "Implement and test the authentication module",
		Mode:   TeamExecParallel,
		Members: []TeamMemberConfig{
			{Role: TeamRoleArchitect, Name: "arch", Task: "Design auth module architecture"},
			{Role: TeamRoleImplementer, Name: "impl", Task: "Implement auth handlers"},
			{Role: TeamRoleTester, Name: "test", Task: "Write auth tests"},
		},
		MaxDuration: 5 * time.Second,
	}

	result, err := teamRunner.RunTeam(context.Background(), config)
	if err != nil {
		t.Fatalf("RunTeam failed: %v", err)
		return
	}

	// Verify all members completed.
	if result.Status != "completed" {
		t.Fatalf("expected team status 'completed', got %q", result.Status)
	}
	if len(result.Members) != 3 {
		t.Fatalf("expected 3 member results, got %d", len(result.Members))
	}
	for _, member := range result.Members {
		if member.Status != "completed" {
			t.Fatalf("expected member %q status 'completed', got %q", member.Name, member.Status)
		}
		if member.Result == "" {
			t.Fatalf("expected non-empty result for member %q", member.Name)
		}
		if member.Duration <= 0 {
			t.Fatalf("expected positive duration for member %q", member.Name)
		}
	}

	// Verify all 3 executors were called.
	if atomic.LoadInt32(&callCount) != 3 {
		t.Fatalf("expected 3 executor calls, got %d", callCount)
	}

	// Verify team result duration is reasonable (parallel should be ~10ms, not ~30ms).
	if result.Duration > 2*time.Second {
		t.Fatalf("parallel team took too long: %v (expected near-parallel execution)", result.Duration)
	}
}

// =============================================================================
// Scenario 4: Agent Failure with Retry
// start -> error -> new agent -> eventual success
// =============================================================================

func TestScenarioAgentFailureAndRetry(t *testing.T) {
	runner := NewAgentRunner(4)
	runner.SetOutputDir(t.TempDir())

	var attempts int32
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		attempt := atomic.AddInt32(&attempts, 1)
		if attempt <= 2 {
			return nil, fmt.Errorf("transient error on attempt %d", attempt)
		}
		return &AgentExecResult{
			Result:     "Success after retries",
			TokensUsed: 100,
			Messages: []*schema.Message{
				{Role: schema.User, Content: opts.Task},
				{Role: schema.Assistant, Content: "Success after retries"},
			},
		}, nil
	}})

	accessor := NewAgentTranscriptAccess(runner)

	// Attempt 1: Fails.
	_, err := RunAgent(context.Background(), runner, AgentExecOptions{
		Task:        "Flaky operation",
		Description: "Retry test attempt 1",
		Name:        "retry-1",
	})
	if err == nil {
		t.Fatal("expected first attempt to fail")
		return
	}
	if !strings.Contains(err.Error(), "transient error on attempt 1") {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify failed state via transcript access.
	runner.mu.RLock()
	var failedID string
	for id, a := range runner.activeAgents {
		a.mu.Lock()
		if a.Name == "retry-1" {
			failedID = id
		}
		a.mu.Unlock()
	}
	runner.mu.RUnlock()

	view, err := accessor.GetTranscript(failedID)
	if err != nil {
		t.Fatalf("GetTranscript for failed agent: %v", err)
		return
	}
	if view.Status != "failed" {
		t.Fatalf("expected failed status in transcript, got %q", view.Status)
	}

	// Attempt 2: Also fails.
	_, err = RunAgent(context.Background(), runner, AgentExecOptions{
		Task:        "Flaky operation retry",
		Description: "Retry test attempt 2",
		Name:        "retry-2",
	})
	if err == nil {
		t.Fatal("expected second attempt to fail")
		return
	}

	// Attempt 3: Succeeds.
	result, err := RunAgent(context.Background(), runner, AgentExecOptions{
		Task:        "Flaky operation final retry",
		Description: "Retry test attempt 3",
		Name:        "retry-3",
	})
	if err != nil {
		t.Fatalf("expected third attempt to succeed, got: %v", err)
		return
	}
	if result.Result != "Success after retries" {
		t.Fatalf("unexpected result: %q", result.Result)
	}
}

// =============================================================================
// Scenario 5: Agent Steering (start -> pause -> check state -> resume -> complete)
// =============================================================================

func TestScenarioAgentSteeringFullCycle(t *testing.T) {
	runner := NewAgentRunner(4)
	runner.SetOutputDir(t.TempDir())

	steering := NewAgentSteering()
	progressStream := NewAgentProgressStream(50)

	// Track execution phases.
	phase1Done := make(chan struct{})
	phase2Gate := make(chan struct{})
	executionComplete := make(chan struct{})

	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		// Phase 1: Initial work.
		close(phase1Done)

		// Wait for steering to release (simulates pause checkpoint).
		<-phase2Gate

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		close(executionComplete)
		return &AgentExecResult{
			Result:     "Steered to completion",
			TokensUsed: 200,
			ToolsUsed:  []string{"Read", "Edit"},
			Messages: []*schema.Message{
				{Role: schema.User, Content: opts.Task},
				{Role: schema.Assistant, Content: "Steered to completion"},
			},
		}, nil
	}})

	// Start agent in background.
	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "Steered task",
		Description: "Steering test",
		Name:        "steered-agent",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground failed: %v", err)
		return
	}

	select {
	case <-phase1Done:
	case <-time.After(time.Second):
		t.Fatal("phase 1 was not reached")
	}

	// Register for steering.
	steering.Register(agent.ID)
	progressStream.RegisterAgent(agent.ID)

	// Verify running state.
	info, ok := steering.GetInfo(agent.ID)
	if !ok {
		t.Fatal("expected agent to be registered")
	}
	if info.State != SteeringStateRunning {
		t.Fatalf("expected running state, got %q", info.State)
	}

	// Pause the agent.
	if err := steering.Pause(agent.ID); err != nil {
		t.Fatalf("Pause failed: %v", err)
		return
	}
	if !steering.IsPaused(agent.ID) {
		t.Fatal("expected paused state")
	}

	// Emit progress event while paused.
	progressStream.Emit(StreamProgressEvent{
		AgentID:   agent.ID,
		Type:      ProgressEventStatusChange,
		OldStatus: "running",
		NewStatus: "paused",
	})

	// Check status.
	status, err := steering.AgentStatus(agent.ID, runner)
	if err != nil {
		t.Fatalf("AgentStatus failed: %v", err)
		return
	}
	if status.SteeringState != SteeringStatePaused {
		t.Fatalf("expected paused steering state, got %q", status.SteeringState)
	}

	// Resume the agent.
	if err := steering.Resume(agent.ID); err != nil {
		t.Fatalf("Resume failed: %v", err)
		return
	}
	if steering.IsPaused(agent.ID) {
		t.Fatal("expected not paused after resume")
	}

	// Let the agent continue.
	close(phase2Gate)

	// Wait for completion.
	select {
	case <-executionComplete:
	case <-time.After(time.Second):
		t.Fatal("execution did not complete after resume")
	}

	completed := waitForAgentStatus(t, runner, agent.ID, "completed")
	if completed.Result != "Steered to completion" {
		t.Fatalf("unexpected result: %q", completed.Result)
	}

	// Verify total paused time accumulated.
	finalInfo, _ := steering.GetInfo(agent.ID)
	if finalInfo.TotalPausedMs < 0 {
		t.Fatalf("expected non-negative total paused time, got %d", finalInfo.TotalPausedMs)
	}

	// Verify progress was buffered.
	buffered := progressStream.BufferedEvents(agent.ID)
	if len(buffered) != 1 {
		t.Fatalf("expected 1 buffered progress event, got %d", len(buffered))
	}
	if buffered[0].Type != ProgressEventStatusChange {
		t.Fatalf("expected status_change event, got %q", buffered[0].Type)
	}

	// Cleanup.
	steering.Unregister(agent.ID)
	progressStream.UnregisterAgent(agent.ID)
}

// =============================================================================
// Scenario 6: Progress Streaming with Multiple Listeners
// =============================================================================

func TestScenarioProgressStreamMultipleListeners(t *testing.T) {
	stream := NewAgentProgressStream(10)
	agentID := "test-agent-progress"
	stream.RegisterAgent(agentID)

	// Emit initial events before any listener.
	for i := 0; i < 5; i++ {
		stream.Emit(StreamProgressEvent{
			AgentID:  agentID,
			Type:     ProgressEventToolStart,
			ToolName: fmt.Sprintf("Tool%d", i),
		})
	}

	// Subscribe listener 1 (late-joiner should get buffered events).
	l1 := stream.Subscribe(agentID, 50)
	if l1 == nil {
		t.Fatal("expected non-nil listener 1")
		return
	}
	defer l1.Close()

	// Drain buffered events from l1.
	l1Events := drainListenerEvents(l1, 100*time.Millisecond)
	if len(l1Events) != 5 {
		t.Fatalf("listener 1 expected 5 buffered events, got %d", len(l1Events))
	}

	// Subscribe listener 2 (also gets buffer).
	l2 := stream.Subscribe(agentID, 50)
	if l2 == nil {
		t.Fatal("expected non-nil listener 2")
		return
	}
	defer l2.Close()

	l2BufferedEvents := drainListenerEvents(l2, 100*time.Millisecond)
	if len(l2BufferedEvents) != 5 {
		t.Fatalf("listener 2 expected 5 buffered events, got %d", len(l2BufferedEvents))
	}

	// Emit new event - both listeners should receive it.
	stream.Emit(StreamProgressEvent{
		AgentID: agentID,
		Type:    ProgressEventModelChunk,
		Chunk:   "Hello",
		ModelID: "gpt-4",
	})

	// Both should get the new event.
	l1New := drainListenerEvents(l1, 100*time.Millisecond)
	l2New := drainListenerEvents(l2, 100*time.Millisecond)
	if len(l1New) != 1 || l1New[0].Chunk != "Hello" {
		t.Fatalf("listener 1 expected 1 new event with chunk 'Hello', got %v", l1New)
	}
	if len(l2New) != 1 || l2New[0].Chunk != "Hello" {
		t.Fatalf("listener 2 expected 1 new event with chunk 'Hello', got %v", l2New)
	}

	// Unsubscribe listener 1.
	stream.Unsubscribe(l1)
	if !l1.IsClosed() {
		t.Fatal("expected listener 1 to be closed after unsubscribe")
	}

	// Emit another event - only l2 should get it.
	stream.Emit(StreamProgressEvent{
		AgentID: agentID,
		Type:    ProgressEventModelEnd,
	})

	l2Final := drainListenerEvents(l2, 100*time.Millisecond)
	if len(l2Final) != 1 {
		t.Fatalf("listener 2 expected 1 event after l1 unsubscribed, got %d", len(l2Final))
	}

	// Verify listener count.
	if count := stream.ListenerCount(agentID); count != 1 {
		t.Fatalf("expected 1 active listener, got %d", count)
	}

	stream.UnregisterAgent(agentID)
}

// =============================================================================
// Scenario 7: Agent Transcript Access During Execution
// =============================================================================

func TestScenarioTranscriptAccessWhileRunning(t *testing.T) {
	runner := NewAgentRunner(4)
	runner.SetOutputDir(t.TempDir())

	entered := make(chan struct{})
	release := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		close(entered)
		<-release
		return &AgentExecResult{
			Result: "done",
			Messages: []*schema.Message{
				{Role: schema.User, Content: opts.Task},
				{Role: schema.Assistant, Content: "thinking..."},
				{Role: schema.Assistant, Content: "done"},
			},
		}, nil
	}})

	agent, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:        "Running task",
		Description: "Running transcript test",
		Name:        "running-agent",
	})
	if err != nil {
		t.Fatalf("RunAgentBackground failed: %v", err)
		return
	}

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("executor was not entered")
	}

	// Access transcript while running.
	accessor := NewAgentTranscriptAccess(runner)
	view, err := accessor.GetTranscript(agent.ID)
	if err != nil {
		t.Fatalf("GetTranscript while running failed: %v", err)
		return
	}
	if !view.IsRunning {
		t.Fatal("expected IsRunning=true while agent is executing")
	}
	if view.Status != "running" {
		t.Fatalf("expected running status, got %q", view.Status)
	}
	// Initial messages from launch should be present.
	if len(view.Messages) == 0 {
		t.Fatal("expected at least initial messages in running transcript")
	}

	// Export markdown while running.
	md, err := accessor.ExportMarkdown(agent.ID)
	if err != nil {
		t.Fatalf("ExportMarkdown while running failed: %v", err)
		return
	}
	if !strings.Contains(md, "Agent is still running") {
		t.Fatalf("expected running note in markdown, got:\n%s", md)
	}

	close(release)
	waitForAgentStatus(t, runner, agent.ID, "completed")

	// Access transcript after completion.
	finalView, err := accessor.GetTranscript(agent.ID)
	if err != nil {
		t.Fatalf("GetTranscript after completion failed: %v", err)
		return
	}
	if finalView.IsRunning {
		t.Fatal("expected IsRunning=false after completion")
	}
	if len(finalView.Messages) != 3 {
		t.Fatalf("expected 3 messages in completed transcript, got %d", len(finalView.Messages))
	}
}

// =============================================================================
// Scenario 8: Display State Multiple Agents
// =============================================================================

func TestScenarioDisplayStateMultipleAgents(t *testing.T) {
	state := NewAgentDisplayState()

	// Register multiple agents.
	state.Register("agent-1", DisplayModeBackground)
	state.Register("agent-2", DisplayModeBackground)
	state.Register("agent-3", DisplayModeForeground)

	// Verify initial state.
	if state.ForegroundAgent() != "agent-3" {
		t.Fatalf("expected agent-3 as foreground, got %q", state.ForegroundAgent())
	}

	modes := state.ListModes()
	if modes["agent-1"] != DisplayModeBackground {
		t.Fatal("expected agent-1 in background")
	}
	if modes["agent-2"] != DisplayModeBackground {
		t.Fatal("expected agent-2 in background")
	}
	if modes["agent-3"] != DisplayModeForeground {
		t.Fatal("expected agent-3 in foreground")
	}

	// Move agent-1 to foreground (agent-3 goes to background).
	if err := state.MoveToForeground("agent-1"); err != nil {
		t.Fatalf("MoveToForeground agent-1 failed: %v", err)
		return
	}
	if state.ForegroundAgent() != "agent-1" {
		t.Fatalf("expected agent-1 as new foreground, got %q", state.ForegroundAgent())
	}
	mode3, _ := state.GetMode("agent-3")
	if mode3 != DisplayModeBackground {
		t.Fatal("expected agent-3 to be moved to background when agent-1 takes foreground")
	}

	// Move agent-1 to background.
	if err := state.MoveToBackground("agent-1"); err != nil {
		t.Fatalf("MoveToBackground agent-1 failed: %v", err)
		return
	}
	if state.ForegroundAgent() != "" {
		t.Fatalf("expected no foreground agent, got %q", state.ForegroundAgent())
	}

	// MoveToBackground on already-background is idempotent.
	if err := state.MoveToBackground("agent-2"); err != nil {
		t.Fatalf("MoveToBackground on background agent should be idempotent: %v", err)
		return
	}

	// Error for unknown agent.
	if err := state.MoveToForeground("unknown"); err == nil {
		t.Fatal("expected error for unknown agent")
		return
	}
}

// =============================================================================
// Scenario 9: Progress Stream Buffer Eviction
// =============================================================================

func TestScenarioProgressStreamBufferEviction(t *testing.T) {
	// Small buffer to test eviction.
	stream := NewAgentProgressStream(5)
	agentID := "buffer-test"
	stream.RegisterAgent(agentID)

	// Emit 10 events (buffer should keep only last 5).
	for i := 0; i < 10; i++ {
		stream.Emit(StreamProgressEvent{
			AgentID: agentID,
			Type:    ProgressEventTextOutput,
			Text:    fmt.Sprintf("chunk-%d", i),
		})
	}

	buffered := stream.BufferedEvents(agentID)
	if len(buffered) != 5 {
		t.Fatalf("expected 5 buffered events (cap=5), got %d", len(buffered))
	}
	// Should be the LAST 5 events (5,6,7,8,9).
	if buffered[0].Text != "chunk-5" {
		t.Fatalf("expected first buffered event to be chunk-5, got %q", buffered[0].Text)
	}
	if buffered[4].Text != "chunk-9" {
		t.Fatalf("expected last buffered event to be chunk-9, got %q", buffered[4].Text)
	}

	// Late-joiner listener should receive last 5 buffered events.
	listener := stream.Subscribe(agentID, 50)
	if listener == nil {
		t.Fatal("expected non-nil listener")
		return
	}
	defer listener.Close()

	events := drainListenerEvents(listener, 100*time.Millisecond)
	if len(events) != 5 {
		t.Fatalf("expected 5 events for late-joining listener, got %d", len(events))
	}
	if events[0].Text != "chunk-5" {
		t.Fatalf("expected first replayed event to be chunk-5, got %q", events[0].Text)
	}

	stream.UnregisterAgent(agentID)
}

// =============================================================================
// Scenario 10: Team Execution with Failure and StopOnFailure
// =============================================================================

func TestScenarioTeamStopOnFailure(t *testing.T) {
	runner := NewAgentRunner(4)
	runner.SetOutputDir(t.TempDir())

	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		if strings.Contains(opts.Task, "TRIGGER_ERROR") {
			return nil, errors.New("critical error in this member")
		}
		return &AgentExecResult{Result: "member done"}, nil
	}})

	teamRunner := NewTeamRunner(runner)
	config := TeamRunConfig{
		TeamID: "team-stop",
		Goal:   "Test stop on member error",
		Mode:   TeamExecSequential,
		Members: []TeamMemberConfig{
			{Role: TeamRoleArchitect, Name: "first", Task: "First task succeeds"},
			{Role: TeamRoleImplementer, Name: "second", Task: "TRIGGER_ERROR in this member"},
			{Role: TeamRoleTester, Name: "third", Task: "Third task never runs"},
		},
		StopOnFailure: true,
	}

	result, err := teamRunner.RunTeam(context.Background(), config)
	if err != nil {
		t.Fatalf("RunTeam failed: %v", err)
		return
	}

	if result.Status != "partial" {
		t.Fatalf("expected partial status when one member fails with StopOnFailure, got %q", result.Status)
	}
	if len(result.Members) != 3 {
		t.Fatalf("expected 3 member results, got %d", len(result.Members))
	}
	if result.Members[0].Status != "completed" {
		t.Fatalf("expected first member completed, got %q", result.Members[0].Status)
	}
	if result.Members[1].Status != "failed" {
		t.Fatalf("expected second member failed, got %q", result.Members[1].Status)
	}
	if result.Members[2].Status != "cancelled" {
		t.Fatalf("expected third member cancelled, got %q", result.Members[2].Status)
	}
}

// =============================================================================
// Scenario 11: Concurrent Display State Operations
// =============================================================================

func TestScenarioDisplayStateConcurrency(t *testing.T) {
	state := NewAgentDisplayState()
	agents := []string{"a1", "a2", "a3", "a4"}

	for _, id := range agents {
		state.Register(id, DisplayModeBackground)
	}

	var wg sync.WaitGroup
	iterations := 100

	// Concurrent foreground/background transitions.
	for _, id := range agents {
		wg.Add(1)
		go func(agentID string) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = state.MoveToForeground(agentID)
				_ = state.MoveToBackground(agentID)
			}
		}(id)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent display state operations deadlocked")
	}

	// Final state should be consistent (all background since last op was MoveToBackground).
	modes := state.ListModes()
	for _, id := range agents {
		if modes[id] != DisplayModeBackground {
			t.Fatalf("expected all agents in background after concurrent ops, %q is %q", id, modes[id])
		}
	}
}

// =============================================================================
// Scenario 12: Progress Stream Agent Isolation
// Events for one agent never leak to another agent's listeners.
// =============================================================================

func TestScenarioProgressStreamAgentIsolation(t *testing.T) {
	stream := NewAgentProgressStream(20)
	stream.RegisterAgent("agent-a")
	stream.RegisterAgent("agent-b")

	listenerA := stream.Subscribe("agent-a", 50)
	listenerB := stream.Subscribe("agent-b", 50)
	defer listenerA.Close()
	defer listenerB.Close()

	// Emit events for agent-a only.
	stream.Emit(StreamProgressEvent{
		AgentID:  "agent-a",
		Type:     ProgressEventToolStart,
		ToolName: "Read",
	})
	stream.Emit(StreamProgressEvent{
		AgentID:  "agent-a",
		Type:     ProgressEventToolEnd,
		ToolName: "Read",
	})

	// Emit events for agent-b only.
	stream.Emit(StreamProgressEvent{
		AgentID: "agent-b",
		Type:    ProgressEventModelChunk,
		Chunk:   "Hello from B",
	})

	eventsA := drainListenerEvents(listenerA, 100*time.Millisecond)
	eventsB := drainListenerEvents(listenerB, 100*time.Millisecond)

	if len(eventsA) != 2 {
		t.Fatalf("expected 2 events for agent-a, got %d", len(eventsA))
	}
	if len(eventsB) != 1 {
		t.Fatalf("expected 1 event for agent-b, got %d", len(eventsB))
	}

	// Verify no cross-contamination.
	for _, e := range eventsA {
		if e.AgentID != "agent-a" {
			t.Fatalf("agent-a listener received event for %q", e.AgentID)
		}
	}
	for _, e := range eventsB {
		if e.AgentID != "agent-b" {
			t.Fatalf("agent-b listener received event for %q", e.AgentID)
		}
	}

	stream.UnregisterAgent("agent-a")
	stream.UnregisterAgent("agent-b")
}

// =============================================================================
// Helpers
// =============================================================================

func drainListenerEvents(l *ProgressListener, timeout time.Duration) []StreamProgressEvent {
	var events []StreamProgressEvent
	timer := time.After(timeout)
	for {
		select {
		case e := <-l.Events:
			events = append(events, e)
		case <-timer:
			return events
		}
	}
}
