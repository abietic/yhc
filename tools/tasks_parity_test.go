package tools

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TeamRunner parity tests
// ---------------------------------------------------------------------------

func TestTeamRunnerSequentialExecutionParity(t *testing.T) {
	// Build a mock AgentRunner that records execution order
	var mu sync.Mutex
	var executionOrder []string

	mockRunner := NewAgentRunner(4)
	mockRunner.SetExecutor(&mockAgentExecutor{
		execFn: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
			mu.Lock()
			executionOrder = append(executionOrder, opts.Name)
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			return &AgentExecResult{Result: "done: " + opts.Name}, nil
		},
	})

	tr := NewTeamRunner(mockRunner)
	config := TeamRunConfig{
		TeamID: "test-team",
		Goal:   "Test sequential execution",
		Mode:   TeamExecSequential,
		Members: []TeamMemberConfig{
			{Name: "first", Role: TeamRoleImplementer, Task: "Do first thing"},
			{Name: "second", Role: TeamRoleReviewer, Task: "Do second thing"},
			{Name: "third", Role: TeamRoleTester, Task: "Do third thing"},
		},
	}

	result, err := tr.RunTeam(context.Background(), config)
	if err != nil {
		t.Fatalf("RunTeam: %v", err)
		return
	}

	if result.Status != "completed" {
		t.Fatalf("expected 'completed', got %q", result.Status)
	}
	if len(result.Members) != 3 {
		t.Fatalf("expected 3 member results, got %d", len(result.Members))
	}

	// Verify sequential order
	mu.Lock()
	defer mu.Unlock()
	if len(executionOrder) != 3 {
		t.Fatalf("expected 3 executions, got %d", len(executionOrder))
	}
	if executionOrder[0] != "first" || executionOrder[1] != "second" || executionOrder[2] != "third" {
		t.Fatalf("expected sequential order [first, second, third], got %v", executionOrder)
	}
}

func TestTeamRunnerParallelExecutionParity(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})

	mockRunner := NewAgentRunner(4)
	mockRunner.SetExecutor(&mockAgentExecutor{
		execFn: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
			select {
			case started <- opts.Name:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &AgentExecResult{Result: "done: " + opts.Name}, nil
		},
	})

	tr := NewTeamRunner(mockRunner)
	config := TeamRunConfig{
		TeamID: "parallel-team",
		Goal:   "Test parallel execution",
		Mode:   TeamExecParallel,
		Members: []TeamMemberConfig{
			{Name: "alpha", Role: TeamRoleImplementer, Task: "Do alpha"},
			{Name: "beta", Role: TeamRoleReviewer, Task: "Do beta"},
		},
	}

	type runResult struct {
		result *TeamRunResult
		err    error
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resultCh := make(chan runResult, 1)
	go func() {
		result, err := tr.RunTeam(ctx, config)
		resultCh <- runResult{result: result, err: err}
	}()

	seen := make(map[string]bool, 2)
	for len(seen) < 2 {
		select {
		case name := <-started:
			seen[name] = true
		case <-ctx.Done():
			close(release)
			<-resultCh
			t.Fatal("parallel members did not enter the executor concurrently")
		}
	}
	close(release)
	run := <-resultCh
	result, err := run.result, run.err
	if err != nil {
		t.Fatalf("RunTeam: %v", err)
	}

	if result.Status != "completed" {
		t.Fatalf("expected 'completed', got %q", result.Status)
	}
	if !seen["alpha"] || !seen["beta"] {
		t.Fatalf("expected alpha and beta to start, got %v", seen)
	}
}

func TestTeamRunnerStopOnFailureParity(t *testing.T) {
	callCount := 0
	mockRunner := NewAgentRunner(4)
	mockRunner.SetExecutor(&mockAgentExecutor{
		execFn: func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
			callCount++
			if opts.Name == "second" {
				return nil, fmt.Errorf("second agent failed")
			}
			return &AgentExecResult{Result: "done"}, nil
		},
	})

	tr := NewTeamRunner(mockRunner)
	config := TeamRunConfig{
		TeamID:        "fail-team",
		Goal:          "Test stop on failure",
		Mode:          TeamExecSequential,
		StopOnFailure: true,
		Members: []TeamMemberConfig{
			{Name: "first", Role: TeamRoleImplementer, Task: "Do first"},
			{Name: "second", Role: TeamRoleReviewer, Task: "Do second"},
			{Name: "third", Role: TeamRoleTester, Task: "Do third"},
		},
	}

	result, err := tr.RunTeam(context.Background(), config)
	if err != nil {
		t.Fatalf("RunTeam: %v", err)
		return
	}

	if result.Status != "partial" {
		t.Fatalf("expected 'partial', got %q", result.Status)
	}

	// third should be cancelled due to StopOnFailure
	if len(result.Members) != 3 {
		t.Fatalf("expected 3 member results, got %d", len(result.Members))
	}
	if result.Members[0].Status != "completed" {
		t.Fatalf("first should be completed, got %q", result.Members[0].Status)
	}
	if result.Members[1].Status != "failed" {
		t.Fatalf("second should be failed, got %q", result.Members[1].Status)
	}
	if result.Members[2].Status != "cancelled" {
		t.Fatalf("third should be cancelled, got %q", result.Members[2].Status)
	}
}

// ---------------------------------------------------------------------------
// WorktreeManager parity tests
// ---------------------------------------------------------------------------

func TestWorktreeManagerSlugValidation(t *testing.T) {
	cases := []struct {
		slug    string
		wantErr bool
	}{
		{"valid-slug", false},
		{"my_feature.v2", false},
		{"nested/slug", false},
		{"", true},                         // empty
		{"../traversal", true},             // path traversal
		{"a/../../escape", true},           // path traversal
		{string(make([]byte, 65)), true},   // too long
		{"invalid slug with spaces", true}, // spaces
		{"a/./b", true},                    // dot segment
	}

	for _, tc := range cases {
		err := validateSlug(tc.slug)
		if tc.wantErr && err == nil {
			t.Errorf("validateSlug(%q): expected error, got nil", tc.slug)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("validateSlug(%q): unexpected error: %v", tc.slug, err)
		}
	}
}

func TestWorktreeManagerIdempotentCreate(t *testing.T) {
	mockGit := &mockGitOp{
		revParseTopLevel: "/repo",
		revParseHEAD:     "abc123",
	}
	wm := NewWorktreeManagerWithGit(mockGit)

	// First create
	info1, err := wm.CreateForAgent("/repo", "agent-1", "feature-x")
	if err != nil {
		t.Fatalf("first CreateForAgent: %v", err)
		return
	}

	// Second create for same agent — should be idempotent
	info2, err := wm.CreateForAgent("/repo", "agent-1", "feature-x")
	if err != nil {
		t.Fatalf("second CreateForAgent: %v", err)
		return
	}

	if info1.WorktreePath != info2.WorktreePath {
		t.Fatal("expected same worktree path for idempotent create")
	}
	if mockGit.worktreeAddCount != 1 {
		t.Fatalf("expected exactly 1 git worktree add call, got %d", mockGit.worktreeAddCount)
	}
}

func TestWorktreeManagerCleanupWithChangesParity(t *testing.T) {
	mockGit := &mockGitOp{
		revParseTopLevel: "/repo",
		revParseHEAD:     "abc123",
		statusOutput:     "M file.go", // dirty working tree
	}
	wm := NewWorktreeManagerWithGit(mockGit)

	_, err := wm.CreateForAgent("/repo", "agent-1", "feature-y")
	if err != nil {
		t.Fatalf("CreateForAgent: %v", err)
		return
	}

	info, err := wm.CleanupForAgent("agent-1")
	if err != nil {
		t.Fatalf("CleanupForAgent: %v", err)
		return
	}

	// Should not remove worktree because there are changes
	if !info.HasChanges {
		t.Fatal("expected HasChanges=true for dirty worktree")
	}
	if mockGit.worktreeRemoveCount != 0 {
		t.Fatalf("expected 0 worktree remove calls (has changes), got %d", mockGit.worktreeRemoveCount)
	}
}

// ---------------------------------------------------------------------------
// AgentSteering parity tests
// ---------------------------------------------------------------------------

func TestAgentSteeringPriorityField(t *testing.T) {
	s := NewAgentSteering()
	s.Register("agent-1")

	info, _ := s.GetInfo("agent-1")
	if info.Priority != PriorityNormal {
		t.Fatalf("expected default priority PriorityNormal (%d), got %d", PriorityNormal, info.Priority)
	}

	_ = s.SetPriority("agent-1", PriorityHigh)
	info, _ = s.GetInfo("agent-1")
	if info.Priority != PriorityHigh {
		t.Fatalf("expected priority PriorityHigh (%d), got %d", PriorityHigh, info.Priority)
	}
}

func TestAgentSteeringForceStopParity(t *testing.T) {
	s := NewAgentSteering()
	s.Register("agent-1")

	if err := s.ForceStop("agent-1", nil); err != nil {
		t.Fatalf("ForceStop: %v", err)
		return
	}

	info, ok := s.GetInfo("agent-1")
	if !ok {
		t.Fatal("expected agent to still be registered")
	}
	if info.State != SteeringStateStopped {
		t.Fatalf("expected stopped state, got %q", info.State)
	}
}

// ---------------------------------------------------------------------------
// TeamContext parity tests
// ---------------------------------------------------------------------------

func TestTeamContextFindingsSharing(t *testing.T) {
	tc := NewTeamContext("team-1", "Build feature X")

	tc.PostFinding("reviewer", "Found a bug in auth.go")
	tc.PostFinding("reviewer", "Style issue in main.go")
	tc.PostFinding("tester", "Test coverage is 60%")

	findings := tc.GetFindings("reviewer")
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings for reviewer, got %d", len(findings))
	}

	all := tc.GetAllFindings()
	if len(all) != 2 {
		t.Fatalf("expected 2 members with findings, got %d", len(all))
	}

	summary := tc.Summary()
	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
}

func TestTeamContextSharedState(t *testing.T) {
	tc := NewTeamContext("team-1", "Goal")

	tc.SetShared("key1", "value1")
	tc.SetShared("key2", 42)

	val, ok := tc.GetShared("key1")
	if !ok || val != "value1" {
		t.Fatalf("expected 'value1', got %v", val)
	}

	val, ok = tc.GetShared("key2")
	if !ok || val != 42 {
		t.Fatalf("expected 42, got %v", val)
	}

	_, ok = tc.GetShared("nonexistent")
	if ok {
		t.Fatal("expected false for nonexistent key")
	}
}

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

type mockAgentExecutor struct {
	execFn func(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error)
}

func (m *mockAgentExecutor) ExecuteAgent(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error) {
	if m.execFn != nil {
		return m.execFn(ctx, opts)
	}
	return &AgentExecResult{Result: "mock result"}, nil
}

type mockGitOp struct {
	revParseTopLevel    string
	revParseHEAD        string
	statusOutput        string
	worktreeAddCount    int
	worktreeRemoveCount int
	mu                  sync.Mutex
}

func (m *mockGitOp) RevParseShowToplevel(cwd string) (string, error) {
	return m.revParseTopLevel, nil
}

func (m *mockGitOp) WorktreeAdd(repoRoot, worktreePath, branch, base string) error {
	m.mu.Lock()
	m.worktreeAddCount++
	m.mu.Unlock()
	return nil
}

func (m *mockGitOp) WorktreeRemove(repoRoot, worktreePath string) error {
	m.mu.Lock()
	m.worktreeRemoveCount++
	m.mu.Unlock()
	return nil
}

func (m *mockGitOp) BranchDelete(repoRoot, branch string) error {
	return nil
}

func (m *mockGitOp) StatusPorcelain(worktreePath string) (string, error) {
	return m.statusOutput, nil
}

func (m *mockGitOp) RevListCount(worktreePath, baseCommit string) (int, error) {
	return 0, nil
}

func (m *mockGitOp) RevParseHEAD(cwd string) (string, error) {
	return m.revParseHEAD, nil
}
