package engine

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/abietic/yhc/engine/commands"
)

// BoundedWorkspaceCommandRunner is an optional capability: context capture
// never falls back to an unbounded runner. Existing workspace diagnostics keep
// their original Run contract.
type BoundedWorkspaceCommandRunner interface {
	RunBounded(context.Context, string, int, ...string) (WorkspaceCommandResult, error)
}

type workspaceContextBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

var errDiffContextCaptureLimit = errors.New("git diff context exceeds the 5 MiB capture limit")

func (b *workspaceContextBuffer) Write(p []byte) (int, error) {
	count := len(p)
	remaining := max(0, b.limit-len(b.data))
	if len(p) > remaining {
		b.truncated = true
		p = p[:remaining]
	}
	b.data = append(b.data, p...)
	return count, nil
}

func (execWorkspaceCommandRunner) RunBounded(ctx context.Context, cwd string, limit int, args ...string) (WorkspaceCommandResult, error) {
	if limit <= 0 {
		return WorkspaceCommandResult{}, errors.New("workspace command output limit must be positive")
	}
	gitArgs := append([]string{"-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	cmd.Dir, cmd.Env = cwd, safeWorkspaceGitEnvironment()
	stdout := &workspaceContextBuffer{limit: limit}
	stderr := &workspaceContextBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	return WorkspaceCommandResult{Stdout: string(stdout.data), Stderr: string(stderr.data), Truncated: stdout.truncated || stderr.truncated}, err
}

// CaptureWorkspaceDiff is a read-only snapshot for explicit context selection.
// A partial command result never becomes selectable. All requires HEAD so an
// unborn repository cannot silently substitute a staged-only view for all.
func (e *QueryEngine) CaptureWorkspaceDiff(ctx context.Context, source commands.DiffContextSource) (commands.DiffContextSnapshot, error) {
	snapshot := commands.DiffContextSnapshot{Source: source}
	switch source {
	case commands.DiffContextAll, commands.DiffContextStaged, commands.DiffContextUnstaged:
	default:
		return snapshot, fmt.Errorf("unsupported diff context source %q", source)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return snapshot, fmt.Errorf("diff context canceled before command start: %w", err)
	}
	if e == nil {
		snapshot.State, snapshot.Reason = commands.WorkspaceDiffRunnerUnavailable, "query engine is unavailable"
		return snapshot, nil
	}
	runner, ok := e.config.WorkspaceCommandRunner.(BoundedWorkspaceCommandRunner)
	if !ok {
		snapshot.State, snapshot.Reason = commands.WorkspaceDiffRunnerUnavailable, "a bounded Git command runner is required"
		return snapshot, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, e.config.WorkspaceCommandTimeout)
	defer cancel()
	cwd := e.GetCWD()
	snapshot.CWD = cwd
	failure := func(result WorkspaceCommandResult, err error, probing bool) (commands.DiffContextSnapshot, error) {
		classified, failureErr := classifyWorkspaceGitError(ctx, runCtx, commands.WorkspaceDiffSnapshot{}, result, err, probing)
		snapshot.State, snapshot.Reason = classified.State, classified.Reason
		return snapshot, failureErr
	}
	run := func(args ...string) (WorkspaceCommandResult, error) {
		result, err := runner.RunBounded(runCtx, cwd, commands.MaxDiffContextBytes, args...)
		if runCtx.Err() != nil {
			return WorkspaceCommandResult{}, runCtx.Err()
		}
		if result.Truncated || len(result.Stdout) > commands.MaxDiffContextBytes {
			return WorkspaceCommandResult{}, errDiffContextCaptureLimit
		}
		return result, err
	}
	probe, err := run("rev-parse", "--is-inside-work-tree")
	if err != nil {
		return failure(probe, err, true)
	}
	if strings.TrimSpace(probe.Stdout) != "true" {
		snapshot.State = commands.WorkspaceDiffNotGit
		return snapshot, nil
	}
	head, headErr := run("rev-parse", "--verify", "--quiet", "HEAD")
	if headErr != nil {
		var exitErr *exec.ExitError
		// --quiet uses exit 1 for a missing revision. Other failures (including
		// truncation) must not be mistaken for an unborn repository.
		if !errors.As(headErr, &exitErr) || exitErr.ExitCode() != 1 {
			return failure(head, headErr, false)
		}
	}
	snapshot.HasHead = headErr == nil
	if !snapshot.HasHead && source == commands.DiffContextAll {
		snapshot.State, snapshot.Reason = commands.WorkspaceDiffRunnerFailed, "No HEAD; choose staged or unstaged changes"
		return snapshot, nil
	}
	args := []string{"diff", "--patch", "--no-ext-diff", "--no-textconv", "--no-color", "--src-prefix=a/", "--dst-prefix=b/", "--unified=3"}
	switch source {
	case commands.DiffContextAll:
		args = append(args, "HEAD")
	case commands.DiffContextStaged:
		args = append(args, "--cached")
	}
	result, err := run(args...)
	if err != nil {
		return failure(result, err, false)
	}
	snapshot.State, snapshot.Patch = commands.WorkspaceDiffReady, result.Stdout
	return snapshot, nil
}
