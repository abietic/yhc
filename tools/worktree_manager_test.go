package tools

import (
	"fmt"
	"testing"
)

// mockGitOperator implements GitOperator for testing without real git.
type mockGitOperator struct {
	repoRoot       string
	headCommit     string
	addErr         error
	removeErr      error
	branchDelErr   error
	statusOutput   string
	statusErr      error
	revListCount   int
	revListErr     error
	addCalls       []mockWorktreeAddCall
	removeCalls    []string
	branchDelCalls []string
}

type mockWorktreeAddCall struct {
	RepoRoot     string
	WorktreePath string
	Branch       string
	Base         string
}

func (m *mockGitOperator) RevParseShowToplevel(cwd string) (string, error) {
	if m.repoRoot == "" {
		return "", fmt.Errorf("not in a git repository")
	}
	return m.repoRoot, nil
}

func (m *mockGitOperator) WorktreeAdd(repoRoot, worktreePath, branch, base string) error {
	m.addCalls = append(m.addCalls, mockWorktreeAddCall{repoRoot, worktreePath, branch, base})
	return m.addErr
}

func (m *mockGitOperator) WorktreeRemove(repoRoot, worktreePath string) error {
	m.removeCalls = append(m.removeCalls, worktreePath)
	return m.removeErr
}

func (m *mockGitOperator) BranchDelete(repoRoot, branch string) error {
	m.branchDelCalls = append(m.branchDelCalls, branch)
	return m.branchDelErr
}

func (m *mockGitOperator) StatusPorcelain(worktreePath string) (string, error) {
	return m.statusOutput, m.statusErr
}

func (m *mockGitOperator) RevListCount(worktreePath, baseCommit string) (int, error) {
	return m.revListCount, m.revListErr
}

func (m *mockGitOperator) RevParseHEAD(cwd string) (string, error) {
	if m.headCommit == "" {
		return "", fmt.Errorf("no HEAD")
	}
	return m.headCommit, nil
}

func TestWorktreeManagerCreateSuccess(t *testing.T) {
	git := &mockGitOperator{
		repoRoot:   "/repo",
		headCommit: "abc123",
	}
	wm := NewWorktreeManagerWithGit(git)

	info, err := wm.CreateForAgent("/repo", "agent-1", "feature-branch")
	if err != nil {
		t.Fatalf("CreateForAgent failed: %v", err)
		return
	}

	if info.AgentID != "agent-1" {
		t.Fatalf("expected agent ID 'agent-1', got %q", info.AgentID)
	}
	if info.WorktreeBranch != "worktree-feature-branch" {
		t.Fatalf("expected branch 'worktree-feature-branch', got %q", info.WorktreeBranch)
	}
	if info.HeadCommit != "abc123" {
		t.Fatalf("expected head commit 'abc123', got %q", info.HeadCommit)
	}
	if info.RepoRoot != "/repo" {
		t.Fatalf("expected repo root '/repo', got %q", info.RepoRoot)
	}
	if len(git.addCalls) != 1 {
		t.Fatalf("expected 1 worktree add call, got %d", len(git.addCalls))
	}
}

func TestWorktreeManagerCreateIdempotent(t *testing.T) {
	git := &mockGitOperator{
		repoRoot:   "/repo",
		headCommit: "abc123",
	}
	wm := NewWorktreeManagerWithGit(git)

	info1, err := wm.CreateForAgent("/repo", "agent-1", "feature-branch")
	if err != nil {
		t.Fatalf("first create failed: %v", err)
		return
	}

	info2, err := wm.CreateForAgent("/repo", "agent-1", "feature-branch")
	if err != nil {
		t.Fatalf("second create failed: %v", err)
		return
	}

	// Should return the same info without calling git again.
	if info1.WorktreePath != info2.WorktreePath {
		t.Fatalf("expected same path, got %q and %q", info1.WorktreePath, info2.WorktreePath)
	}
	if len(git.addCalls) != 1 {
		t.Fatalf("expected only 1 git add call (idempotent), got %d", len(git.addCalls))
	}
}

func TestWorktreeManagerCreateNotGitRepo(t *testing.T) {
	git := &mockGitOperator{
		repoRoot: "", // Not a git repo.
	}
	wm := NewWorktreeManagerWithGit(git)

	_, err := wm.CreateForAgent("/some/dir", "agent-1", "branch")
	if err == nil {
		t.Fatal("expected error for non-git repo")
		return
	}
	if !contains(err.Error(), "not in a git repository") {
		t.Fatalf("expected 'not in a git repository' error, got: %v", err)
	}
}

func TestWorktreeManagerCreateInvalidSlug(t *testing.T) {
	git := &mockGitOperator{
		repoRoot:   "/repo",
		headCommit: "abc123",
	}
	wm := NewWorktreeManagerWithGit(git)

	tests := []struct {
		slug    string
		wantErr string
	}{
		{"", "must not be empty"},
		{"../escape", "must not contain"},
		{".", "must not contain"},
		{"valid/../../escape", "must not contain"},
		{"a b c", "must contain only"},
		{string(make([]byte, 100)), "must be 64 characters or fewer"},
	}

	for _, tt := range tests {
		_, err := wm.CreateForAgent("/repo", "agent-test", tt.slug)
		if err == nil {
			t.Fatalf("slug %q: expected error", tt.slug)
			return
		}
		if !contains(err.Error(), tt.wantErr) {
			t.Fatalf("slug %q: expected error containing %q, got: %v", tt.slug, tt.wantErr, err)
		}
	}
}

func TestWorktreeManagerCleanupNoChanges(t *testing.T) {
	git := &mockGitOperator{
		repoRoot:     "/repo",
		headCommit:   "abc123",
		statusOutput: "", // No dirty files.
		revListCount: 0,  // No new commits.
	}
	wm := NewWorktreeManagerWithGit(git)

	_, err := wm.CreateForAgent("/repo", "agent-1", "cleanup-test")
	if err != nil {
		t.Fatalf("CreateForAgent failed: %v", err)
		return
	}

	info, err := wm.CleanupForAgent("agent-1")
	if err != nil {
		t.Fatalf("CleanupForAgent failed: %v", err)
		return
	}

	if info.HasChanges {
		t.Fatal("expected HasChanges=false when no changes")
	}
	if len(git.removeCalls) != 1 {
		t.Fatalf("expected 1 worktree remove call, got %d", len(git.removeCalls))
	}
	if len(git.branchDelCalls) != 1 {
		t.Fatalf("expected 1 branch delete call, got %d", len(git.branchDelCalls))
	}
}

func TestWorktreeManagerCleanupWithChanges(t *testing.T) {
	git := &mockGitOperator{
		repoRoot:     "/repo",
		headCommit:   "abc123",
		statusOutput: "M file.go", // Dirty working tree.
	}
	wm := NewWorktreeManagerWithGit(git)

	_, err := wm.CreateForAgent("/repo", "agent-1", "changes-test")
	if err != nil {
		t.Fatalf("CreateForAgent failed: %v", err)
		return
	}

	info, err := wm.CleanupForAgent("agent-1")
	if err != nil {
		t.Fatalf("CleanupForAgent failed: %v", err)
		return
	}

	if !info.HasChanges {
		t.Fatal("expected HasChanges=true when files are dirty")
	}
	if len(git.removeCalls) != 0 {
		t.Fatalf("expected 0 remove calls when changes exist, got %d", len(git.removeCalls))
	}
}

func TestWorktreeManagerCleanupWithNewCommits(t *testing.T) {
	git := &mockGitOperator{
		repoRoot:     "/repo",
		headCommit:   "abc123",
		statusOutput: "", // Clean working tree.
		revListCount: 2,  // 2 new commits.
	}
	wm := NewWorktreeManagerWithGit(git)

	_, err := wm.CreateForAgent("/repo", "agent-1", "commits-test")
	if err != nil {
		t.Fatalf("CreateForAgent failed: %v", err)
		return
	}

	info, err := wm.CleanupForAgent("agent-1")
	if err != nil {
		t.Fatalf("CleanupForAgent failed: %v", err)
		return
	}

	if !info.HasChanges {
		t.Fatal("expected HasChanges=true when new commits exist")
	}
}

func TestWorktreeManagerCleanupNotFound(t *testing.T) {
	git := &mockGitOperator{repoRoot: "/repo", headCommit: "abc123"}
	wm := NewWorktreeManagerWithGit(git)

	_, err := wm.CleanupForAgent("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
		return
	}
}

func TestWorktreeManagerListWorktrees(t *testing.T) {
	git := &mockGitOperator{repoRoot: "/repo", headCommit: "abc123"}
	wm := NewWorktreeManagerWithGit(git)

	_, _ = wm.CreateForAgent("/repo", "agent-1", "branch-1")
	_, _ = wm.CreateForAgent("/repo", "agent-2", "branch-2")

	list := wm.ListWorktrees()
	if len(list) != 2 {
		t.Fatalf("expected 2 worktrees, got %d", len(list))
	}
}

func TestWorktreeManagerGetWorktreeInfo(t *testing.T) {
	git := &mockGitOperator{repoRoot: "/repo", headCommit: "abc123"}
	wm := NewWorktreeManagerWithGit(git)

	_, _ = wm.CreateForAgent("/repo", "agent-1", "branch-1")

	info, found := wm.GetWorktreeInfo("agent-1")
	if !found {
		t.Fatal("expected to find worktree for agent-1")
	}
	if info.Slug != "branch-1" {
		t.Fatalf("expected slug 'branch-1', got %q", info.Slug)
	}

	_, found = wm.GetWorktreeInfo("nonexistent")
	if found {
		t.Fatal("expected not to find worktree for nonexistent agent")
	}
}

func TestValidateSlug(t *testing.T) {
	valid := []string{
		"feature-branch",
		"my.feature",
		"user/feature",
		"a1_b2-c3",
	}
	for _, slug := range valid {
		if err := validateSlug(slug); err != nil {
			t.Fatalf("slug %q should be valid, got error: %v", slug, err)
			return
		}
	}
}

func TestFlattenSlug(t *testing.T) {
	if flattenSlug("user/feature") != "user+feature" {
		t.Fatalf("expected 'user+feature', got %q", flattenSlug("user/feature"))
	}
	if flattenSlug("simple") != "simple" {
		t.Fatalf("expected 'simple', got %q", flattenSlug("simple"))
	}
}

func TestWorktreeBranchName(t *testing.T) {
	if worktreeBranchName("my-feature") != "worktree-my-feature" {
		t.Fatalf("expected 'worktree-my-feature', got %q", worktreeBranchName("my-feature"))
	}
	if worktreeBranchName("user/feat") != "worktree-user+feat" {
		t.Fatalf("expected 'worktree-user+feat', got %q", worktreeBranchName("user/feat"))
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
