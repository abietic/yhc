package tools

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// GitOperator abstracts git operations for testability.
// The default implementation shells out to git; tests provide a mock.
type GitOperator interface {
	// RevParseShowToplevel returns the git repository root, or error if not in a repo.
	RevParseShowToplevel(cwd string) (string, error)
	// WorktreeAdd creates a new worktree at worktreePath on the given branch from base.
	WorktreeAdd(repoRoot, worktreePath, branch, base string) error
	// WorktreeRemove removes a worktree directory and its git tracking.
	WorktreeRemove(repoRoot, worktreePath string) error
	// BranchDelete deletes a local branch.
	BranchDelete(repoRoot, branch string) error
	// StatusPorcelain returns the porcelain status output for the given path.
	StatusPorcelain(worktreePath string) (string, error)
	// RevListCount returns the number of commits between base and HEAD.
	RevListCount(worktreePath, baseCommit string) (int, error)
	// RevParseHEAD returns the current HEAD commit sha.
	RevParseHEAD(cwd string) (string, error)
}

// defaultGitOperator shells out to the real git binary.
type defaultGitOperator struct{}

func (d *defaultGitOperator) RevParseShowToplevel(cwd string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not in a git repository: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (d *defaultGitOperator) WorktreeAdd(repoRoot, worktreePath, branch, base string) error {
	cmd := exec.Command("git", "worktree", "add", "-b", branch, worktreePath, base)
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Try without -b (branch already exists).
		cmd2 := exec.Command("git", "worktree", "add", worktreePath, branch)
		cmd2.Dir = repoRoot
		output2, err2 := cmd2.CombinedOutput()
		if err2 != nil {
			return fmt.Errorf("git worktree add failed: %s: %w", string(output)+string(output2), err2)
		}
	}
	_ = output
	return nil
}

func (d *defaultGitOperator) WorktreeRemove(repoRoot, worktreePath string) error {
	cmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove failed: %s: %w", string(output), err)
	}
	return nil
}

func (d *defaultGitOperator) BranchDelete(repoRoot, branch string) error {
	cmd := exec.Command("git", "branch", "-D", branch)
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git branch -D failed: %s: %w", string(output), err)
	}
	return nil
}

func (d *defaultGitOperator) StatusPorcelain(worktreePath string) (string, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git status failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (d *defaultGitOperator) RevListCount(worktreePath, baseCommit string) (int, error) {
	cmd := exec.Command("git", "rev-list", "--count", baseCommit+"..HEAD")
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("git rev-list failed: %w", err)
	}
	var count int
	_, _ = fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &count)
	return count, nil
}

func (d *defaultGitOperator) RevParseHEAD(cwd string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// WorktreeInfo tracks state for a managed worktree.
type WorktreeInfo struct {
	AgentID        string
	Slug           string
	WorktreePath   string
	WorktreeBranch string
	RepoRoot       string
	HeadCommit     string
	CreatedAt      time.Time
	HasChanges     bool // Set during cleanup check.
}

// WorktreeManager manages git worktrees for agent isolation.
// It creates worktrees for agents, tracks them, and cleans up when done.
// Operations are idempotent: creating the same worktree twice is a no-op.
type WorktreeManager struct {
	mu          sync.Mutex
	git         GitOperator
	worktrees   map[string]*WorktreeInfo // keyed by agent ID
	worktreeDir string                   // base directory for worktrees (e.g., .claude/worktrees)
}

// NewWorktreeManager creates a WorktreeManager with the default git operator.
func NewWorktreeManager() *WorktreeManager {
	return &WorktreeManager{
		git:         &defaultGitOperator{},
		worktrees:   make(map[string]*WorktreeInfo),
		worktreeDir: ".claude/worktrees",
	}
}

// NewWorktreeManagerWithGit creates a WorktreeManager with a custom git operator.
// Used by tests to mock git operations.
func NewWorktreeManagerWithGit(git GitOperator) *WorktreeManager {
	return &WorktreeManager{
		git:         git,
		worktrees:   make(map[string]*WorktreeInfo),
		worktreeDir: ".claude/worktrees",
	}
}

// validSlugSegment matches reference validation: alphanumeric, dots, underscores, dashes.
var validSlugSegment = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

const maxWorktreeSlugLength = 64

// validateSlug validates a worktree slug to prevent path traversal.
// Mirrors the reference's validateWorktreeSlug.
func validateSlug(slug string) error {
	if len(slug) > maxWorktreeSlugLength {
		return fmt.Errorf("invalid worktree name: must be %d characters or fewer (got %d)", maxWorktreeSlugLength, len(slug))
	}
	if slug == "" {
		return fmt.Errorf("invalid worktree name: must not be empty")
	}
	for _, segment := range strings.Split(slug, "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("invalid worktree name %q: must not contain \".\" or \"..\" path segments", slug)
		}
		if !validSlugSegment.MatchString(segment) {
			return fmt.Errorf("invalid worktree name %q: each segment must contain only letters, digits, dots, underscores, and dashes", slug)
		}
	}
	return nil
}

// flattenSlug converts nested slugs (user/feature) to flat form (user+feature)
// for both branch names and directory paths. Mirrors the reference.
func flattenSlug(slug string) string {
	return strings.ReplaceAll(slug, "/", "+")
}

// worktreeBranchName generates the branch name for a worktree slug.
func worktreeBranchName(slug string) string {
	return "worktree-" + flattenSlug(slug)
}

// CreateForAgent creates a git worktree for an agent, providing isolated
// filesystem access. The operation is idempotent: if a worktree already
// exists for this agent, it returns the existing info.
//
// Parameters:
//   - cwd: current working directory (used to find git root)
//   - agentID: unique identifier for the agent
//   - slug: worktree name slug (validated for safety)
//
// Returns WorktreeInfo with the path and branch name, or error.
func (wm *WorktreeManager) CreateForAgent(cwd, agentID, slug string) (*WorktreeInfo, error) {
	if err := validateSlug(slug); err != nil {
		return nil, fmt.Errorf("worktree_manager: %w", err)
	}

	wm.mu.Lock()
	defer wm.mu.Unlock()

	// Idempotent: if already created for this agent, return existing.
	if existing, ok := wm.worktrees[agentID]; ok {
		return existing, nil
	}

	// Find git root.
	repoRoot, err := wm.git.RevParseShowToplevel(cwd)
	if err != nil {
		return nil, fmt.Errorf("worktree_manager: cannot create worktree: %w", err)
	}

	// Get HEAD commit for later change detection.
	headCommit, err := wm.git.RevParseHEAD(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("worktree_manager: cannot determine HEAD: %w", err)
	}

	// Compute paths.
	flat := flattenSlug(slug)
	worktreePath := filepath.Join(repoRoot, wm.worktreeDir, flat)
	branch := worktreeBranchName(slug)

	// Create the worktree.
	if err := wm.git.WorktreeAdd(repoRoot, worktreePath, branch, "HEAD"); err != nil {
		return nil, fmt.Errorf("worktree_manager: %w", err)
	}

	info := &WorktreeInfo{
		AgentID:        agentID,
		Slug:           slug,
		WorktreePath:   worktreePath,
		WorktreeBranch: branch,
		RepoRoot:       repoRoot,
		HeadCommit:     headCommit,
		CreatedAt:      time.Now(),
	}
	wm.worktrees[agentID] = info

	return info, nil
}

// CleanupForAgent removes the worktree created for an agent.
// If the worktree has uncommitted changes or new commits, it is left in place
// and HasChanges is set to true in the returned info. Otherwise, the worktree
// and its branch are deleted.
//
// The returned WorktreeInfo reflects the final state (HasChanges indicates
// whether the worktree was kept or removed).
func (wm *WorktreeManager) CleanupForAgent(agentID string) (*WorktreeInfo, error) {
	wm.mu.Lock()
	info, ok := wm.worktrees[agentID]
	if !ok {
		wm.mu.Unlock()
		return nil, fmt.Errorf("worktree_manager: no worktree found for agent %q", agentID)
	}
	// Remove from tracking regardless of cleanup outcome.
	delete(wm.worktrees, agentID)
	wm.mu.Unlock()

	// Check for changes.
	hasChanges, err := wm.hasChanges(info)
	if err != nil {
		// Fail-closed: assume changes exist.
		info.HasChanges = true
		return info, nil
	}

	if hasChanges {
		info.HasChanges = true
		return info, nil
	}

	// No changes: remove worktree and branch.
	info.HasChanges = false
	if removeErr := wm.git.WorktreeRemove(info.RepoRoot, info.WorktreePath); removeErr != nil {
		// Best-effort: report but don't fail.
		return info, fmt.Errorf("worktree_manager: cleanup warning: %w", removeErr)
	}

	// Delete the temporary branch.
	_ = wm.git.BranchDelete(info.RepoRoot, info.WorktreeBranch)

	return info, nil
}

// GetWorktreeInfo returns the worktree info for an agent, if one exists.
func (wm *WorktreeManager) GetWorktreeInfo(agentID string) (*WorktreeInfo, bool) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	info, ok := wm.worktrees[agentID]
	if !ok {
		return nil, false
	}
	cp := *info
	return &cp, true
}

// ListWorktrees returns all currently tracked worktrees.
func (wm *WorktreeManager) ListWorktrees() []WorktreeInfo {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	out := make([]WorktreeInfo, 0, len(wm.worktrees))
	for _, info := range wm.worktrees {
		out = append(out, *info)
	}
	return out
}

// hasChanges checks whether a worktree has uncommitted changes or new commits
// since its creation. Mirrors the reference's hasWorktreeChanges.
func (wm *WorktreeManager) hasChanges(info *WorktreeInfo) (bool, error) {
	// Check dirty working tree.
	status, err := wm.git.StatusPorcelain(info.WorktreePath)
	if err != nil {
		return true, err // fail-closed
	}
	if status != "" {
		return true, nil
	}

	// Check for new commits since creation.
	count, err := wm.git.RevListCount(info.WorktreePath, info.HeadCommit)
	if err != nil {
		return true, err // fail-closed
	}
	return count > 0, nil
}
