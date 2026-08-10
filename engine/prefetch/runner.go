package prefetch

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// Default cache parameters for the runner.
	defaultMaxItems = 64
	defaultMaxAge   = 5 * time.Minute

	// Item type constants.
	TypeGit    = "git"
	TypeMemory = "memory"
	TypeSkill  = "skill"
	TypeFile   = "file"
)

// PrefetchRunner manages background prefetching of context between turns.
type PrefetchRunner struct {
	cache      *PrefetchCache
	projectDir string
	mu         sync.Mutex
}

// NewPrefetchRunner creates a new PrefetchRunner for the given project directory.
func NewPrefetchRunner(projectDir string) *PrefetchRunner {
	return &PrefetchRunner{
		cache:      NewPrefetchCache(defaultMaxItems, defaultMaxAge),
		projectDir: projectDir,
	}
}

// RunPrefetch executes all prefetch operations: git status, memory files,
// and skill metadata. Results are cached for retrieval via GetResults.
func (r *PrefetchRunner) RunPrefetch(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Invalidate stale items before repopulating.
	r.cache.Prune()

	var errs []string

	if err := r.prefetchGitStatus(ctx); err != nil {
		errs = append(errs, fmt.Sprintf("git: %v", err))
	}

	if err := r.prefetchMemoryFiles(ctx); err != nil {
		errs = append(errs, fmt.Sprintf("memory: %v", err))
	}

	if err := r.prefetchSkillMetadata(ctx); err != nil {
		errs = append(errs, fmt.Sprintf("skill: %v", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("prefetch errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// GetResults returns all valid cached prefetch items sorted by priority.
func (r *PrefetchRunner) GetResults() []*PrefetchItem {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.cache.All()
}

// prefetchGitStatus runs git status and caches the result.
func (r *PrefetchRunner) prefetchGitStatus(ctx context.Context) error { //nolint:unparam
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = r.projectDir

	out, err := cmd.Output()
	if err != nil {
		// Not a git repo or git not available — skip silently.
		return nil
	}

	content := strings.TrimSpace(string(out))
	if content == "" {
		content = "(clean working tree)"
	}

	r.cache.Set("git:status", &PrefetchItem{
		Type:     TypeGit,
		Content:  content,
		Priority: 10,
		Source:   "git status --porcelain",
		CachedAt: time.Now(),
		TTL:      30 * time.Second,
	})

	// Also cache the current branch.
	branchCmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	branchCmd.Dir = r.projectDir

	branchOut, err := branchCmd.Output()
	if err == nil {
		branch := strings.TrimSpace(string(branchOut))
		r.cache.Set("git:branch", &PrefetchItem{
			Type:     TypeGit,
			Content:  branch,
			Priority: 9,
			Source:   "git rev-parse --abbrev-ref HEAD",
			CachedAt: time.Now(),
			TTL:      60 * time.Second,
		})
	}

	return nil
}

// prefetchMemoryFiles looks for CLAUDE.md / AGENTS.md memory files and caches them.
func (r *PrefetchRunner) prefetchMemoryFiles(ctx context.Context) error {
	memoryFiles := []string{
		"CLAUDE.md",
		"AGENTS.md",
		".claude/settings.json",
	}

	for _, name := range memoryFiles {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		path := filepath.Join(r.projectDir, name)
		cmd := exec.CommandContext(ctx, "cat", path)
		out, err := cmd.Output()
		if err != nil {
			// File does not exist — skip.
			continue
		}

		content := strings.TrimSpace(string(out))
		if content == "" {
			continue
		}

		key := "memory:" + name
		r.cache.Set(key, &PrefetchItem{
			Type:     TypeMemory,
			Content:  content,
			Priority: 20,
			Source:   path,
			CachedAt: time.Now(),
			TTL:      2 * time.Minute,
		})
	}

	return nil
}

// prefetchSkillMetadata discovers available skills/tools metadata and caches them.
func (r *PrefetchRunner) prefetchSkillMetadata(ctx context.Context) error {
	// Look for skill definition files in common locations.
	skillDirs := []string{
		".claude/skills",
		".skills",
		"skills",
	}

	for _, dir := range skillDirs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		fullDir := filepath.Join(r.projectDir, dir)
		cmd := exec.CommandContext(ctx, "ls", fullDir)
		out, err := cmd.Output()
		if err != nil {
			// Directory does not exist — skip.
			continue
		}

		entries := strings.TrimSpace(string(out))
		if entries == "" {
			continue
		}

		key := "skill:" + dir
		r.cache.Set(key, &PrefetchItem{
			Type:     TypeSkill,
			Content:  entries,
			Priority: 15,
			Source:   fullDir,
			CachedAt: time.Now(),
			TTL:      2 * time.Minute,
		})
	}

	return nil
}
