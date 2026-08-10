package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/session"
)

type workspaceCommandRunnerFunc func(
	context.Context,
	string,
	...string,
) (WorkspaceCommandResult, error)

func (f workspaceCommandRunnerFunc) Run(
	ctx context.Context,
	cwd string,
	args ...string,
) (WorkspaceCommandResult, error) {
	return f(ctx, cwd, args...)
}

func TestWorkspaceDiffTerminalStates(t *testing.T) {
	tests := []struct {
		name      string
		runner    WorkspaceCommandRunner
		wantState commands.WorkspaceDiffState
	}{
		{
			name: "non git",
			runner: workspaceCommandRunnerFunc(func(context.Context, string, ...string) (WorkspaceCommandResult, error) {
				return WorkspaceCommandResult{Stderr: "fatal: not a git repository"}, errors.New("exit status 128")
			}),
			wantState: commands.WorkspaceDiffNotGit,
		},
		{
			name: "runner unavailable",
			runner: workspaceCommandRunnerFunc(func(context.Context, string, ...string) (WorkspaceCommandResult, error) {
				return WorkspaceCommandResult{}, exec.ErrNotFound
			}),
			wantState: commands.WorkspaceDiffRunnerUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := NewQueryEngine(QueryEngineConfig{
				CWD:                    t.TempDir(),
				TranscriptDir:          t.TempDir(),
				WorkspaceCommandRunner: tt.runner,
			})
			t.Cleanup(eng.Close)
			snapshot, err := eng.WorkspaceDiff(context.Background(), commands.WorkspaceDiffStat)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.State != tt.wantState {
				t.Fatalf("state = %s, want %s (%#v)", snapshot.State, tt.wantState, snapshot)
			}
		})
	}
}

func TestWorkspaceDiffReportsRunnerTimeout(t *testing.T) {
	runner := workspaceCommandRunnerFunc(func(ctx context.Context, _ string, _ ...string) (WorkspaceCommandResult, error) {
		<-ctx.Done()
		return WorkspaceCommandResult{}, ctx.Err()
	})
	eng := NewQueryEngine(QueryEngineConfig{
		CWD:                     t.TempDir(),
		TranscriptDir:           t.TempDir(),
		WorkspaceCommandRunner:  runner,
		WorkspaceCommandTimeout: 5 * time.Millisecond,
	})
	t.Cleanup(eng.Close)
	snapshot, err := eng.WorkspaceDiff(context.Background(), commands.WorkspaceDiffStat)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != commands.WorkspaceDiffRunnerTimedOut {
		t.Fatalf("timeout state = %s, want %s", snapshot.State, commands.WorkspaceDiffRunnerTimedOut)
	}
}

func TestWorkspaceDiffUsesCancellableEngineRunner(t *testing.T) {
	var calls atomic.Int32
	runner := workspaceCommandRunnerFunc(func(ctx context.Context, _ string, args ...string) (WorkspaceCommandResult, error) {
		calls.Add(1)
		switch strings.Join(args, " ") {
		case "rev-parse --is-inside-work-tree":
			return WorkspaceCommandResult{Stdout: "true\n"}, nil
		case "rev-parse --verify HEAD":
			return WorkspaceCommandResult{Stdout: "abc123\n"}, nil
		case "diff --no-ext-diff --no-textconv HEAD --numstat":
			return WorkspaceCommandResult{Stdout: "2\t1\tpkg/file.go\n"}, nil
		case "ls-files --others --exclude-standard":
			return WorkspaceCommandResult{Stdout: "new.txt\n"}, nil
		default:
			return WorkspaceCommandResult{}, errors.New("unexpected Git command")
		}
	})
	eng := NewQueryEngine(QueryEngineConfig{
		CWD:                    t.TempDir(),
		TranscriptDir:          t.TempDir(),
		WorkspaceCommandRunner: runner,
	})
	t.Cleanup(eng.Close)
	snapshot, err := eng.WorkspaceDiff(context.Background(), commands.WorkspaceDiffStat)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != commands.WorkspaceDiffReady || snapshot.Numstat == "" || snapshot.Untracked == "" || calls.Load() != 4 {
		t.Fatalf("workspace snapshot = %#v, calls=%d", snapshot, calls.Load())
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	before := calls.Load()
	if _, err := eng.WorkspaceDiff(canceled, commands.WorkspaceDiffStat); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled diff error = %v", err)
	}
	if calls.Load() != before {
		t.Fatalf("runner invoked after pre-start cancellation: %d -> %d", before, calls.Load())
	}
}

func TestWorkspaceDiffHandlesRepositoryBeforeInitialCommit(t *testing.T) {
	var sawCached atomic.Bool
	runner := workspaceCommandRunnerFunc(func(_ context.Context, _ string, args ...string) (WorkspaceCommandResult, error) {
		switch strings.Join(args, " ") {
		case "rev-parse --is-inside-work-tree":
			return WorkspaceCommandResult{Stdout: "true\n"}, nil
		case "rev-parse --verify HEAD":
			return WorkspaceCommandResult{Stderr: "fatal: Needed a single revision"}, errors.New("exit status 128")
		case "diff --no-ext-diff --no-textconv --cached --numstat":
			sawCached.Store(true)
			return WorkspaceCommandResult{Stdout: "1\t0\tfirst.txt\n"}, nil
		case "ls-files --others --exclude-standard":
			return WorkspaceCommandResult{}, nil
		default:
			return WorkspaceCommandResult{}, errors.New("unexpected Git command")
		}
	})
	eng := NewQueryEngine(QueryEngineConfig{
		CWD:                    t.TempDir(),
		TranscriptDir:          t.TempDir(),
		WorkspaceCommandRunner: runner,
	})
	t.Cleanup(eng.Close)
	snapshot, err := eng.WorkspaceDiff(context.Background(), commands.WorkspaceDiffStat)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != commands.WorkspaceDiffReady || snapshot.HasHead || !sawCached.Load() {
		t.Fatalf("initial repository snapshot = %#v, cached=%v", snapshot, sawCached.Load())
	}
}

func TestWorkspaceGitEnvironmentIgnoresRepositoryRedirects(t *testing.T) {
	t.Setenv("GIT_DIR", "/tmp/redirected")
	t.Setenv("GIT_WORK_TREE", "/tmp/other-tree")
	for _, pair := range safeWorkspaceGitEnvironment() {
		if strings.HasPrefix(pair, "GIT_DIR=") || strings.HasPrefix(pair, "GIT_WORK_TREE=") {
			t.Fatalf("unsafe Git redirect leaked into runner environment: %q", pair)
		}
	}
}

func TestDiffCommandUsesEngineOwnedWorkspaceService(t *testing.T) {
	var calls atomic.Int32
	runner := workspaceCommandRunnerFunc(func(_ context.Context, _ string, args ...string) (WorkspaceCommandResult, error) {
		calls.Add(1)
		switch strings.Join(args, " ") {
		case "rev-parse --is-inside-work-tree":
			return WorkspaceCommandResult{Stdout: "true\n"}, nil
		case "rev-parse --verify HEAD":
			return WorkspaceCommandResult{Stdout: "abc123\n"}, nil
		case "diff --no-ext-diff --no-textconv HEAD --numstat":
			return WorkspaceCommandResult{Stdout: "1\t0\tengine/query.go\n"}, nil
		case "ls-files --others --exclude-standard":
			return WorkspaceCommandResult{}, nil
		default:
			return WorkspaceCommandResult{}, errors.New("unexpected Git command")
		}
	})
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:              "workspace-diff-command",
		CWD:                    t.TempDir(),
		TranscriptDir:          t.TempDir(),
		CommandEntrypoint:      commands.EntrypointHeadless,
		WorkspaceCommandRunner: runner,
	})
	t.Cleanup(eng.Close)
	events := drainRuntimeEvents(t, eng, "/diff")
	result := assertSingleCommandResultThenTerminal(t, events, CommandResultSucceeded)
	if !strings.Contains(result.Output, "engine/query.go") || calls.Load() != 4 {
		t.Fatalf("diff command result=%q calls=%d", result.Output, calls.Load())
	}
}

func TestAddDirCanonicalizesAndAtomicallyUpdatesActiveRoots(t *testing.T) {
	root := t.TempDir()
	extra := t.TempDir()
	canonicalExtra, err := filepath.EvalSymlinks(extra)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked-extra")
	if err := os.Symlink(extra, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         "workspace-add-dir",
		CWD:               root,
		TranscriptDir:     filepath.Join(root, "transcripts"),
		CommandEntrypoint: commands.EntrypointHeadless,
	})
	t.Cleanup(eng.Close)
	events := drainRuntimeEvents(t, eng, "/add-dir linked-extra")
	result := assertSingleCommandResultThenTerminal(t, events, CommandResultSucceeded)
	if !strings.Contains(result.Output, canonicalExtra) {
		t.Fatalf("add-dir output = %q, want canonical %q", result.Output, canonicalExtra)
	}
	roots := eng.GetWorkingDirectories()
	if len(roots) != 2 || roots[1] != canonicalExtra {
		t.Fatalf("active roots = %#v", roots)
	}
	loaded, err := eng.GetTranscript().LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	metadata := session.ReadSessionMetadataFull(loaded)
	if metadata == nil || len(metadata.AdditionalDirs) != 1 || metadata.AdditionalDirs[0] != canonicalExtra {
		t.Fatalf("persisted additional roots = %#v", metadata)
	}

	events = drainRuntimeEvents(t, eng, "/add-dir linked-extra")
	result = assertSingleCommandResultThenTerminal(t, events, CommandResultSucceeded)
	if !strings.Contains(result.Output, "already accessible") || len(eng.GetWorkingDirectories()) != 2 {
		t.Fatalf("duplicate add-dir result=%q roots=%#v", result.Output, eng.GetWorkingDirectories())
	}
}

func TestAddDirFailurePrecedesActiveRootMutation(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         "workspace-add-dir-invalid",
		CWD:               root,
		TranscriptDir:     filepath.Join(root, "transcripts"),
		CommandEntrypoint: commands.EntrypointHeadless,
	})
	t.Cleanup(eng.Close)
	events := drainRuntimeEvents(t, eng, "/add-dir file.txt")
	result := assertSingleCommandResultThenTerminal(t, events, CommandResultFailed)
	if !strings.Contains(result.Error, "not a directory") {
		t.Fatalf("invalid add-dir error = %q", result.Error)
	}
	if roots := eng.GetWorkingDirectories(); len(roots) != 1 || roots[0] != root {
		t.Fatalf("active roots mutated after validation failure: %#v", roots)
	}
}
