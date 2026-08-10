package worktree

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type Repository struct {
	Root      string
	CommonDir string
}

type Inspection struct {
	Repository
	Head   string
	Branch string
}

// ReadOnlyGit is the complete Git observation boundary used by legacy
// inspection. It deliberately cannot create, restore, remove, or delete
// lifecycle-owned state. Every operation must honor ctx.
type ReadOnlyGit interface {
	Repository(ctx context.Context, cwd string) (Repository, error)
	ResolveCommit(ctx context.Context, cwd, ref string) (string, error)
	BranchCommit(ctx context.Context, repoRoot, branch string) (string, bool, error)
	InspectWorktree(ctx context.Context, path string) (Inspection, error)
	StatusPorcelain(ctx context.Context, path string) (string, error)
	CountCommits(ctx context.Context, path, baseCommit string) (int, error)
	Diff(ctx context.Context, path, baseCommit string, maxBytes int) (string, bool, error)
}

// Git exposes the explicit-directory mutations owned by the canonical
// lifecycle service in addition to the observation boundary.
type Git interface {
	ReadOnlyGit
	AddWorktree(ctx context.Context, repoRoot, path, branch, baseCommit string) error
	RemoveWorktree(ctx context.Context, repoRoot, path string) error
	RestoreWorktree(ctx context.Context, repoRoot, path, branch string) error
	DeleteBranch(ctx context.Context, repoRoot, branch, expectedCommit string) error
}

type CommandGit struct{}

func (CommandGit) Repository(ctx context.Context, cwd string) (Repository, error) {
	root, err := runGit(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return Repository{}, fmt.Errorf("resolve repository root: %w", err)
	}
	commonDir, err := runGit(ctx, cwd, "rev-parse", "--git-common-dir")
	if err != nil {
		return Repository{}, fmt.Errorf("resolve repository common directory: %w", err)
	}
	canonicalRoot, err := canonicalExistingPath(strings.TrimSpace(root))
	if err != nil {
		return Repository{}, fmt.Errorf("canonicalize repository root: %w", err)
	}
	common := strings.TrimSpace(commonDir)
	if !filepath.IsAbs(common) {
		common = filepath.Join(cwd, common)
	}
	canonicalCommon, err := canonicalExistingPath(common)
	if err != nil {
		return Repository{}, fmt.Errorf("canonicalize repository common directory: %w", err)
	}
	return Repository{Root: canonicalRoot, CommonDir: canonicalCommon}, nil
}

func (CommandGit) ResolveCommit(ctx context.Context, cwd, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		ref = "HEAD"
	}
	out, err := runGit(ctx, cwd, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve commit %q: %w", ref, err)
	}
	commit := strings.TrimSpace(out)
	if commit == "" {
		return "", fmt.Errorf("resolve commit %q: empty result", ref)
	}
	return commit, nil
}

func (CommandGit) BranchCommit(
	ctx context.Context,
	repoRoot string,
	branch string,
) (string, bool, error) {
	out, err := runGit(
		ctx,
		repoRoot,
		"rev-parse",
		"--verify",
		"--quiet",
		"refs/heads/"+branch+"^{commit}",
	)
	if err != nil {
		var commandErr *gitCommandError
		if errors.As(err, &commandErr) && commandErr.ExitCode == 1 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("inspect branch %q: %w", branch, err)
	}
	return strings.TrimSpace(out), true, nil
}

func (CommandGit) AddWorktree(
	ctx context.Context,
	repoRoot string,
	path string,
	branch string,
	baseCommit string,
) error {
	_, err := runGit(ctx, repoRoot, "worktree", "add", "-b", branch, path, baseCommit)
	if err != nil {
		return fmt.Errorf("add worktree: %w", err)
	}
	return nil
}

func (git CommandGit) InspectWorktree(ctx context.Context, path string) (Inspection, error) {
	repository, err := git.Repository(ctx, path)
	if err != nil {
		return Inspection{}, err
	}
	head, err := git.ResolveCommit(ctx, path, "HEAD")
	if err != nil {
		return Inspection{}, err
	}
	branch, err := runGit(ctx, path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return Inspection{}, fmt.Errorf("resolve worktree branch: %w", err)
	}
	return Inspection{
		Repository: repository,
		Head:       strings.TrimSpace(head),
		Branch:     strings.TrimSpace(branch),
	}, nil
}

func (CommandGit) StatusPorcelain(ctx context.Context, path string) (string, error) {
	status, err := runGit(
		ctx,
		path,
		"status",
		"--porcelain=v1",
		"--untracked-files=all",
	)
	if err != nil {
		return "", fmt.Errorf("inspect worktree status: %w", err)
	}
	// `git status --ignored=matching` can collapse an ignored ancestor such as
	// `.eino-agent/` into one `!!` row. That makes it impossible for callers
	// to exclude service-owned descendants without also hiding unrelated user
	// files under the same ignored directory. ls-files returns individual
	// ignored files, so synthesize their porcelain rows and retain that
	// distinction.
	ignored, err := runGit(
		ctx,
		path,
		"ls-files",
		"--others",
		"--ignored",
		"--exclude-standard",
	)
	if err != nil {
		return "", fmt.Errorf("inspect ignored worktree files: %w", err)
	}
	var combined strings.Builder
	combined.WriteString(strings.TrimSpace(status))
	for _, ignoredPath := range strings.Split(strings.TrimSpace(ignored), "\n") {
		if strings.TrimSpace(ignoredPath) == "" {
			continue
		}
		if combined.Len() > 0 {
			combined.WriteByte('\n')
		}
		combined.WriteString("!! ")
		combined.WriteString(ignoredPath)
	}
	return combined.String(), nil
}

func (CommandGit) CountCommits(
	ctx context.Context,
	path string,
	baseCommit string,
) (int, error) {
	out, err := runGit(ctx, path, "rev-list", "--count", baseCommit+"..HEAD")
	if err != nil {
		return 0, fmt.Errorf("count worktree commits: %w", err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parse worktree commit count: %w", err)
	}
	return count, nil
}

func (CommandGit) Diff(
	ctx context.Context,
	path string,
	baseCommit string,
	maxBytes int,
) (string, bool, error) {
	if maxBytes <= 0 {
		return "", false, nil
	}
	command := exec.CommandContext(
		ctx,
		"git",
		"diff",
		"--no-ext-diff",
		"--no-color",
		"--binary",
		baseCommit,
		"--",
	)
	command.Dir = path
	stdout := newBoundedGitBuffer(maxBytes)
	stderr := newBoundedGitBuffer(2048)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		exitCode := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		return "", stdout.truncated, &gitCommandError{
			Args:     append([]string(nil), command.Args[1:]...),
			ExitCode: exitCode,
			Output:   stderr.String(),
			Err:      err,
		}
	}
	return stdout.String(), stdout.truncated, nil
}

func (CommandGit) RemoveWorktree(ctx context.Context, repoRoot, path string) error {
	if _, err := runGit(ctx, repoRoot, "worktree", "remove", path); err != nil {
		return fmt.Errorf("remove worktree: %w", err)
	}
	return nil
}

func (CommandGit) RestoreWorktree(
	ctx context.Context,
	repoRoot string,
	path string,
	branch string,
) error {
	if _, err := runGit(ctx, repoRoot, "worktree", "add", path, branch); err != nil {
		return fmt.Errorf("restore retained worktree: %w", err)
	}
	return nil
}

func (CommandGit) DeleteBranch(
	ctx context.Context,
	repoRoot string,
	branch string,
	expectedCommit string,
) error {
	if _, err := runGit(
		ctx,
		repoRoot,
		"update-ref",
		"-d",
		"refs/heads/"+branch,
		expectedCommit,
	); err != nil {
		return fmt.Errorf("delete owned branch %q: %w", branch, err)
	}
	return nil
}

type gitCommandError struct {
	Args     []string
	ExitCode int
	Output   string
	Err      error
}

type boundedGitBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func newBoundedGitBuffer(limit int) *boundedGitBuffer {
	return &boundedGitBuffer{limit: limit}
}

func (b *boundedGitBuffer) Write(data []byte) (int, error) {
	copied := 0
	if b.limit > len(b.data) {
		remaining := b.limit - len(b.data)
		if remaining > len(data) {
			remaining = len(data)
		}
		b.data = append(b.data, data[:remaining]...)
		copied = remaining
	}
	if copied < len(data) {
		b.truncated = true
	}
	return len(data), nil
}

func (b *boundedGitBuffer) String() string {
	return string(b.data)
}

func (e *gitCommandError) Error() string {
	output := strings.TrimSpace(e.Output)
	if len(output) > 2048 {
		output = output[:2048]
	}
	if output == "" {
		return fmt.Sprintf("git %s: %v", strings.Join(e.Args, " "), e.Err)
	}
	return fmt.Sprintf("git %s: %s: %v", strings.Join(e.Args, " "), output, e.Err)
}

func (e *gitCommandError) Unwrap() error {
	return e.Err
}

func runGit(ctx context.Context, cwd string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = cwd
	output, err := command.CombinedOutput()
	if err == nil {
		return string(output), nil
	}
	exitCode := -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	}
	return "", &gitCommandError{
		Args:     append([]string(nil), args...),
		ExitCode: exitCode,
		Output:   string(output),
		Err:      err,
	}
}
