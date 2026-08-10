package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/abietic/yhc/engine/commands"
)

const workspaceCommandTimeout = 10 * time.Second

type WorkspaceCommandResult struct {
	Stdout string
	Stderr string
}

// WorkspaceCommandRunner is the engine-owned, cancellable read-only process
// boundary used by workspace diagnostics. Command handlers never start Git or
// shell processes directly.
type WorkspaceCommandRunner interface {
	Run(context.Context, string, ...string) (WorkspaceCommandResult, error)
}

type execWorkspaceCommandRunner struct{}

func (execWorkspaceCommandRunner) Run(
	ctx context.Context,
	cwd string,
	args ...string,
) (WorkspaceCommandResult, error) {
	gitArgs := append([]string{"-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	cmd.Dir = cwd
	cmd.Env = safeWorkspaceGitEnvironment()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return WorkspaceCommandResult{Stdout: stdout.String(), Stderr: stderr.String()}, err
}

func safeWorkspaceGitEnvironment() []string {
	blocked := map[string]struct{}{
		"GIT_DIR":                 {},
		"GIT_WORK_TREE":           {},
		"GIT_COMMON_DIR":          {},
		"GIT_INDEX_FILE":          {},
		"GIT_OBJECT_DIRECTORY":    {},
		"GIT_CEILING_DIRECTORIES": {},
	}
	env := make([]string, 0, len(os.Environ())+3)
	for _, pair := range os.Environ() {
		key, _, ok := strings.Cut(pair, "=")
		if ok {
			if _, skip := blocked[key]; skip {
				continue
			}
		}
		env = append(env, pair)
	}
	return append(env, "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat", "LC_ALL=C")
}

func (e *QueryEngine) WorkspaceDiff(
	ctx context.Context,
	mode commands.WorkspaceDiffMode,
) (commands.WorkspaceDiffSnapshot, error) {
	snapshot := commands.WorkspaceDiffSnapshot{Mode: mode}
	if e == nil {
		snapshot.State = commands.WorkspaceDiffRunnerUnavailable
		snapshot.Reason = "query engine is unavailable"
		return snapshot, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return snapshot, fmt.Errorf("workspace diff canceled before command start: %w", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, e.config.WorkspaceCommandTimeout)
	defer cancel()
	runner := e.config.WorkspaceCommandRunner
	if runner == nil {
		snapshot.State = commands.WorkspaceDiffRunnerUnavailable
		snapshot.Reason = "no command runner is configured"
		return snapshot, nil
	}
	cwd := e.GetCWD()
	probe, err := runner.Run(runCtx, cwd, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return classifyWorkspaceGitError(ctx, runCtx, snapshot, probe, err, true)
	}
	if strings.TrimSpace(probe.Stdout) != "true" {
		snapshot.State = commands.WorkspaceDiffNotGit
		return snapshot, nil
	}
	hasHead := true
	headResult, headErr := runner.Run(runCtx, cwd, "rev-parse", "--verify", "HEAD")
	if headErr != nil {
		if parentErr := ctx.Err(); parentErr != nil {
			return snapshot, fmt.Errorf("workspace diff canceled: %w", parentErr)
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) || errors.Is(headErr, exec.ErrNotFound) {
			return classifyWorkspaceGitError(ctx, runCtx, snapshot, headResult, headErr, false)
		}
		hasHead = false
	}
	snapshot.HasHead = hasHead

	switch mode {
	case commands.WorkspaceDiffFull:
		args := []string{"diff", "--no-ext-diff", "--no-textconv", "HEAD"}
		if !hasHead {
			args = []string{"diff", "--no-ext-diff", "--no-textconv", "--cached"}
		}
		result, runErr := runner.Run(runCtx, cwd, args...)
		if runErr != nil {
			return classifyWorkspaceGitError(ctx, runCtx, snapshot, result, runErr, false)
		}
		snapshot.Patch = result.Stdout
	case commands.WorkspaceDiffStaged:
		result, runErr := runner.Run(runCtx, cwd, "diff", "--no-ext-diff", "--no-textconv", "--cached", "--numstat")
		if runErr != nil {
			return classifyWorkspaceGitError(ctx, runCtx, snapshot, result, runErr, false)
		}
		snapshot.Numstat = result.Stdout
	case commands.WorkspaceDiffStat:
		args := []string{"diff", "--no-ext-diff", "--no-textconv", "HEAD", "--numstat"}
		if !hasHead {
			args = []string{"diff", "--no-ext-diff", "--no-textconv", "--cached", "--numstat"}
		}
		result, runErr := runner.Run(runCtx, cwd, args...)
		if runErr != nil {
			return classifyWorkspaceGitError(ctx, runCtx, snapshot, result, runErr, false)
		}
		snapshot.Numstat = result.Stdout
		untracked, untrackedErr := runner.Run(runCtx, cwd, "ls-files", "--others", "--exclude-standard")
		if untrackedErr != nil {
			return classifyWorkspaceGitError(ctx, runCtx, snapshot, untracked, untrackedErr, false)
		}
		snapshot.Untracked = untracked.Stdout
	default:
		return snapshot, fmt.Errorf("unsupported workspace diff mode %q", mode)
	}
	snapshot.State = commands.WorkspaceDiffReady
	return snapshot, nil
}

func classifyWorkspaceGitError(
	parentCtx context.Context,
	runCtx context.Context,
	snapshot commands.WorkspaceDiffSnapshot,
	result WorkspaceCommandResult,
	err error,
	probing bool,
) (commands.WorkspaceDiffSnapshot, error) {
	if ctxErr := parentCtx.Err(); ctxErr != nil {
		return snapshot, fmt.Errorf("workspace diff canceled: %w", ctxErr)
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		snapshot.State = commands.WorkspaceDiffRunnerTimedOut
		return snapshot, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		snapshot.State = commands.WorkspaceDiffRunnerUnavailable
		snapshot.Reason = "git executable was not found"
		return snapshot, nil
	}
	reason := strings.TrimSpace(result.Stderr)
	if reason == "" {
		reason = err.Error()
	}
	if probing && strings.Contains(strings.ToLower(reason), "not a git repository") {
		snapshot.State = commands.WorkspaceDiffNotGit
		return snapshot, nil
	}
	snapshot.State = commands.WorkspaceDiffRunnerFailed
	snapshot.Reason = reason
	return snapshot, nil
}
