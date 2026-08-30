package tools

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTeamRunnerSequentialExecution(t *testing.T) {
	runner := NewAgentRunner(4)
	runner.SetOutputDir(t.TempDir())

	var order []string
	var orderMu sync.Mutex
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		orderMu.Lock()
		order = append(order, opts.Name)
		orderMu.Unlock()
		return &AgentExecResult{Result: fmt.Sprintf("result from %s", opts.Name)}, nil
	}})

	tr := NewTeamRunner(runner)

	config := TeamRunConfig{
		TeamID: "team-1",
		Goal:   "Test sequential execution",
		Mode:   TeamExecSequential,
		Members: []TeamMemberConfig{
			{Role: TeamRoleArchitect, Name: "architect", Task: "design the system"},
			{Role: TeamRoleImplementer, Name: "impl", Task: "implement the design"},
			{Role: TeamRoleReviewer, Name: "reviewer", Task: "review the implementation"},
		},
	}

	result, err := tr.RunTeam(context.Background(), config)
	if err != nil {
		t.Fatalf("RunTeam returned error: %v", err)
		return
	}

	if result.Status != "completed" {
		t.Fatalf("expected status 'completed', got %q", result.Status)
	}
	if len(result.Members) != 3 {
		t.Fatalf("expected 3 member results, got %d", len(result.Members))
	}

	// Verify sequential order.
	orderMu.Lock()
	defer orderMu.Unlock()
	if len(order) != 3 || order[0] != "architect" || order[1] != "impl" || order[2] != "reviewer" {
		t.Fatalf("expected sequential order [architect, impl, reviewer], got %v", order)
	}

	// Verify all completed.
	for _, m := range result.Members {
		if m.Status != "completed" {
			t.Fatalf("expected member %q to be completed, got %q", m.Name, m.Status)
		}
	}
}

func TestTeamRunnerParallelExecution(t *testing.T) {
	runner := NewAgentRunner(4)
	runner.SetOutputDir(t.TempDir())

	var started atomic.Int32
	gate := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		started.Add(1)
		<-gate
		return &AgentExecResult{Result: "done"}, nil
	}})

	tr := NewTeamRunner(runner)

	config := TeamRunConfig{
		TeamID: "team-parallel",
		Goal:   "Test parallel execution",
		Mode:   TeamExecParallel,
		Members: []TeamMemberConfig{
			{Role: TeamRoleImplementer, Name: "worker-1", Task: "task 1"},
			{Role: TeamRoleImplementer, Name: "worker-2", Task: "task 2"},
			{Role: TeamRoleImplementer, Name: "worker-3", Task: "task 3"},
		},
	}

	done := make(chan *TeamRunResult, 1)
	go func() {
		result, _ := tr.RunTeam(context.Background(), config)
		done <- result
	}()

	// Wait for all agents to start.
	deadline := time.After(2 * time.Second)
	for started.Load() != 3 {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for parallel agents to start, got %d", started.Load())
		default:
			time.Sleep(time.Millisecond)
		}
	}

	// Release all agents.
	close(gate)

	select {
	case result := <-done:
		if result.Status != "completed" {
			t.Fatalf("expected 'completed', got %q", result.Status)
		}
		if len(result.Members) != 3 {
			t.Fatalf("expected 3 members, got %d", len(result.Members))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for team result")
	}
}

func TestTeamRunnerMemberFailureGraceful(t *testing.T) {
	runner := NewAgentRunner(4)
	runner.SetOutputDir(t.TempDir())

	callCount := 0
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		callCount++
		if opts.Name == "failer" {
			return nil, fmt.Errorf("intentional failure")
		}
		return &AgentExecResult{Result: "ok"}, nil
	}})

	tr := NewTeamRunner(runner)

	config := TeamRunConfig{
		TeamID:        "team-fail",
		Goal:          "Test graceful failure",
		Mode:          TeamExecSequential,
		StopOnFailure: false, // Continue after failure.
		Members: []TeamMemberConfig{
			{Role: TeamRoleImplementer, Name: "worker-1", Task: "task 1"},
			{Role: TeamRoleImplementer, Name: "failer", Task: "fail task"},
			{Role: TeamRoleReviewer, Name: "reviewer", Task: "review"},
		},
	}

	result, err := tr.RunTeam(context.Background(), config)
	if err != nil {
		t.Fatalf("RunTeam returned error: %v", err)
		return
	}

	if result.Status != "partial" {
		t.Fatalf("expected 'partial' status, got %q", result.Status)
	}
	if result.Members[0].Status != "completed" {
		t.Fatalf("expected first member completed, got %q", result.Members[0].Status)
	}
	if result.Members[1].Status != "failed" {
		t.Fatalf("expected second member failed, got %q", result.Members[1].Status)
	}
	if result.Members[2].Status != "completed" {
		t.Fatalf("expected third member completed (graceful), got %q", result.Members[2].Status)
	}
}

func TestTeamRunnerStopOnFailure(t *testing.T) {
	runner := NewAgentRunner(4)
	runner.SetOutputDir(t.TempDir())

	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		if opts.Name == "failer" {
			return nil, fmt.Errorf("intentional failure")
		}
		return &AgentExecResult{Result: "ok"}, nil
	}})

	tr := NewTeamRunner(runner)

	config := TeamRunConfig{
		TeamID:        "team-stop",
		Goal:          "Test stop on failure",
		Mode:          TeamExecSequential,
		StopOnFailure: true,
		Members: []TeamMemberConfig{
			{Role: TeamRoleImplementer, Name: "failer", Task: "fail task"},
			{Role: TeamRoleReviewer, Name: "reviewer", Task: "should not run"},
		},
	}

	result, err := tr.RunTeam(context.Background(), config)
	if err != nil {
		t.Fatalf("RunTeam returned error: %v", err)
		return
	}

	if result.Members[0].Status != "failed" {
		t.Fatalf("expected first member failed, got %q", result.Members[0].Status)
	}
	if result.Members[1].Status != "cancelled" {
		t.Fatalf("expected second member cancelled, got %q", result.Members[1].Status)
	}
}

func TestTeamContextSharedFindings(t *testing.T) {
	tc := NewTeamContext("ctx-test", "shared findings test")

	tc.PostFinding("architect", "use microservices")
	tc.PostFinding("architect", "deploy on k8s")
	tc.PostFinding("reviewer", "needs more tests")

	archFindings := tc.GetFindings("architect")
	if len(archFindings) != 2 {
		t.Fatalf("expected 2 architect findings, got %d", len(archFindings))
	}
	if archFindings[0] != "use microservices" {
		t.Fatalf("unexpected finding: %q", archFindings[0])
	}

	all := tc.GetAllFindings()
	if len(all) != 2 {
		t.Fatalf("expected 2 members in findings, got %d", len(all))
	}

	tc.SetShared("decision", "approved")
	val, ok := tc.GetShared("decision")
	if !ok || val != "approved" {
		t.Fatalf("expected shared value 'approved', got %v (ok=%v)", val, ok)
	}

	summary := tc.Summary()
	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
}

func TestTeamRunnerCancelTeam(t *testing.T) {
	runner := NewAgentRunner(4)
	runner.SetOutputDir(t.TempDir())

	started := make(chan struct{})
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}})

	tr := NewTeamRunner(runner)

	config := TeamRunConfig{
		TeamID: "team-cancel",
		Goal:   "Test cancellation",
		Mode:   TeamExecSequential,
		Members: []TeamMemberConfig{
			{Role: TeamRoleImplementer, Name: "blocker", Task: "block forever"},
			{Role: TeamRoleReviewer, Name: "never-runs", Task: "should not run"},
		},
	}

	done := make(chan *TeamRunResult, 1)
	go func() {
		result, _ := tr.RunTeam(context.Background(), config)
		done <- result
	}()

	<-started

	if err := tr.CancelTeam("team-cancel"); err != nil {
		t.Fatalf("CancelTeam returned error: %v", err)
		return
	}

	select {
	case result := <-done:
		// The blocker should be failed/cancelled.
		if result == nil {
			t.Fatal("expected non-nil result")
			return
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancellation")
	}
}

func TestTeamRunnerDependencies(t *testing.T) {
	runner := NewAgentRunner(4)
	runner.SetOutputDir(t.TempDir())

	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		return &AgentExecResult{Result: "done"}, nil
	}})

	tr := NewTeamRunner(runner)

	config := TeamRunConfig{
		TeamID: "team-deps",
		Goal:   "Test dependencies",
		Mode:   TeamExecSequential,
		Members: []TeamMemberConfig{
			{Role: TeamRoleArchitect, Name: "arch", Task: "design"},
			{Role: TeamRoleImplementer, Name: "impl", Task: "implement", DependsOn: []string{"arch"}},
			{Role: TeamRoleTester, Name: "tester", Task: "test", DependsOn: []string{"nonexistent"}},
		},
	}

	result, err := tr.RunTeam(context.Background(), config)
	if err != nil {
		t.Fatalf("RunTeam returned error: %v", err)
		return
	}

	if result.Members[0].Status != "completed" {
		t.Fatalf("arch should be completed, got %q", result.Members[0].Status)
	}
	if result.Members[1].Status != "completed" {
		t.Fatalf("impl should be completed (dep met), got %q", result.Members[1].Status)
	}
	if result.Members[2].Status != "skipped" {
		t.Fatalf("tester should be skipped (dep not met), got %q", result.Members[2].Status)
	}
}

func TestTeamRunnerMaxDuration(t *testing.T) {
	runner := NewAgentRunner(4)
	runner.SetOutputDir(t.TempDir())

	runner.SetExecutor(fakeAgentExecutor{onExecute: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return &AgentExecResult{Result: "done"}, nil
		}
	}})

	tr := NewTeamRunner(runner)

	config := TeamRunConfig{
		TeamID:      "team-timeout",
		Goal:        "Test timeout",
		Mode:        TeamExecSequential,
		MaxDuration: 100 * time.Millisecond,
		Members: []TeamMemberConfig{
			{Role: TeamRoleImplementer, Name: "slow", Task: "takes too long"},
		},
	}

	result, err := tr.RunTeam(context.Background(), config)
	if err != nil {
		t.Fatalf("RunTeam returned error: %v", err)
		return
	}

	if result.Duration > 2*time.Second {
		t.Fatalf("expected fast timeout, got duration %v", result.Duration)
	}
}
