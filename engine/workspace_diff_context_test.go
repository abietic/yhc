package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/commands"
)

type diffContextRunnerFunc func(context.Context, string, int, ...string) (WorkspaceCommandResult, error)

func (f diffContextRunnerFunc) Run(ctx context.Context, cwd string, args ...string) (WorkspaceCommandResult, error) {
	return f(ctx, cwd, 0, args...)
}

func (f diffContextRunnerFunc) RunBounded(ctx context.Context, cwd string, limit int, args ...string) (WorkspaceCommandResult, error) {
	return f(ctx, cwd, limit, args...)
}

func TestCaptureWorkspaceDiffSourcesAndSnapshot(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = root, safeWorkspaceGitEnvironment()
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %v (%s)", err, output)
		}
	}
	git("init")
	git("config", "user.name", "fixture")
	git("config", "user.email", "fixture@example.invalid")
	path := filepath.Join(root, "file.txt")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("base\n")
	git("add", "file.txt")
	git("commit", "-m", "fixture")
	write("staged\n")
	git("add", "file.txt")
	write("working\n")
	eng := NewQueryEngine(QueryEngineConfig{CWD: root, TranscriptDir: t.TempDir()})
	t.Cleanup(eng.Close)
	for _, tc := range []struct {
		source commands.DiffContextSource
		old    string
		new    string
	}{
		{commands.DiffContextAll, "base", "working"},
		{commands.DiffContextStaged, "base", "staged"},
		{commands.DiffContextUnstaged, "staged", "working"},
	} {
		snapshot, err := eng.CaptureWorkspaceDiff(t.Context(), tc.source)
		if err != nil || snapshot.State != commands.WorkspaceDiffReady || snapshot.Source != tc.source || !snapshot.HasHead {
			t.Fatalf("snapshot = %#v, %v", snapshot, err)
		}
		if !strings.Contains(snapshot.Patch, "-"+tc.old+"\n") || !strings.Contains(snapshot.Patch, "+"+tc.new+"\n") {
			t.Fatalf("wrong source %s: %q", tc.source, snapshot.Patch)
		}
		before := snapshot.Patch
		write("later disk mutation\n")
		if snapshot.Patch != before {
			t.Fatal("captured diff changed after disk mutation")
		}
		write("working\n")
	}
}

func TestCaptureWorkspaceDiffBoundsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		result    WorkspaceCommandResult
		wantState commands.WorkspaceDiffState
	}{
		{"complete", WorkspaceCommandResult{Stdout: "captured patch"}, commands.WorkspaceDiffReady},
		{"truncated", WorkspaceCommandResult{Stdout: "partial patch", Truncated: true}, commands.WorkspaceDiffRunnerFailed},
		{"oversize custom runner", WorkspaceCommandResult{Stdout: strings.Repeat("x", commands.MaxDiffContextBytes+1)}, commands.WorkspaceDiffRunnerFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			cwd := ""
			runner := diffContextRunnerFunc(func(_ context.Context, directory string, limit int, args ...string) (WorkspaceCommandResult, error) {
				calls++
				if directory != cwd || limit != commands.MaxDiffContextBytes {
					t.Fatalf("wrong bounded runner contract cwd=%q limit=%d", directory, limit)
				}
				if args[0] == "rev-parse" {
					return WorkspaceCommandResult{Stdout: "true\n"}, nil
				}
				joined := strings.Join(args, " ")
				for _, flag := range []string{"--no-ext-diff", "--no-textconv", "--no-color", "--src-prefix=a/", "--dst-prefix=b/", "--unified=3"} {
					if !strings.Contains(joined, flag) {
						t.Fatalf("capture omitted %s", flag)
					}
				}
				return tc.result, nil
			})
			eng := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir(), TranscriptDir: t.TempDir(), WorkspaceCommandRunner: runner})
			t.Cleanup(eng.Close)
			cwd = eng.GetCWD()
			snapshot, err := eng.CaptureWorkspaceDiff(t.Context(), commands.DiffContextAll)
			if err != nil || snapshot.State != tc.wantState || calls != 3 {
				t.Fatalf("capture state=%s calls=%d err=%v", snapshot.State, calls, err)
			}
			if tc.wantState != commands.WorkspaceDiffReady && snapshot.Patch != "" {
				t.Fatal("partial output was offered as complete attachment content")
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := eng.CaptureWorkspaceDiff(ctx, commands.DiffContextAll); !errors.Is(err, context.Canceled) || calls != 3 {
				t.Fatal("canceled capture started a Git command")
			}
		})
	}
}

func TestCaptureWorkspaceDiffUnavailableAndTimeout(t *testing.T) {
	oldRunner := workspaceCommandRunnerFunc(func(context.Context, string, ...string) (WorkspaceCommandResult, error) {
		t.Fatal("unbounded runner was used for context capture")
		return WorkspaceCommandResult{}, nil
	})
	eng := NewQueryEngine(QueryEngineConfig{CWD: t.TempDir(), TranscriptDir: t.TempDir(), WorkspaceCommandRunner: oldRunner})
	t.Cleanup(eng.Close)
	snapshot, err := eng.CaptureWorkspaceDiff(t.Context(), commands.DiffContextAll)
	if err != nil || snapshot.State != commands.WorkspaceDiffRunnerUnavailable {
		t.Fatalf("unbounded runner: %#v, %v", snapshot, err)
	}
	eng2 := NewQueryEngine(QueryEngineConfig{
		CWD: t.TempDir(), TranscriptDir: t.TempDir(), WorkspaceCommandTimeout: 5 * time.Millisecond,
		WorkspaceCommandRunner: diffContextRunnerFunc(func(ctx context.Context, _ string, _ int, _ ...string) (WorkspaceCommandResult, error) {
			<-ctx.Done()
			return WorkspaceCommandResult{}, ctx.Err()
		}),
	})
	t.Cleanup(eng2.Close)
	snapshot, err = eng2.CaptureWorkspaceDiff(t.Context(), commands.DiffContextAll)
	if err != nil || snapshot.State != commands.WorkspaceDiffRunnerTimedOut {
		t.Fatalf("timed out capture: %#v, %v", snapshot, err)
	}
}

func TestCaptureWorkspaceDiffUnbornAndNotGit(t *testing.T) {
	root := t.TempDir()
	eng := NewQueryEngine(QueryEngineConfig{CWD: root, TranscriptDir: t.TempDir()})
	t.Cleanup(eng.Close)
	snapshot, err := eng.CaptureWorkspaceDiff(t.Context(), commands.DiffContextAll)
	if err != nil || snapshot.State != commands.WorkspaceDiffNotGit {
		t.Fatalf("not Git: %#v, %v", snapshot, err)
	}
	for _, args := range [][]string{{"init"}, {"add", "new.txt"}} {
		if args[0] == "add" {
			if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = root, safeWorkspaceGitEnvironment()
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture Git: %v (%s)", err, output)
		}
	}
	for _, source := range []commands.DiffContextSource{commands.DiffContextAll, commands.DiffContextStaged, commands.DiffContextUnstaged} {
		snapshot, err = eng.CaptureWorkspaceDiff(t.Context(), source)
		if err != nil || snapshot.HasHead {
			t.Fatalf("unborn %s: %#v, %v", source, snapshot, err)
		}
		switch source {
		case commands.DiffContextAll:
			if snapshot.State != commands.WorkspaceDiffRunnerFailed || !strings.Contains(snapshot.Reason, "No HEAD") || snapshot.Patch != "" {
				t.Fatal("all silently substituted staged in an unborn repository")
			}
		case commands.DiffContextStaged:
			if snapshot.State != commands.WorkspaceDiffReady || !strings.Contains(snapshot.Patch, "+new\n") {
				t.Fatal("unborn staged capture missing its new file")
			}
		case commands.DiffContextUnstaged:
			if snapshot.State != commands.WorkspaceDiffReady || snapshot.Patch != "" {
				t.Fatal("clean unborn worktree was not empty")
			}
		}
	}
}

func TestCaptureWorkspaceDiffHeadProbeFailsClosed(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		calls := 0
		eng := NewQueryEngine(QueryEngineConfig{
			CWD: t.TempDir(), TranscriptDir: t.TempDir(),
			WorkspaceCommandRunner: diffContextRunnerFunc(func(_ context.Context, _ string, _ int, _ ...string) (WorkspaceCommandResult, error) {
				calls++
				if calls == 1 {
					return WorkspaceCommandResult{Stdout: "true"}, nil
				}
				if truncated {
					return WorkspaceCommandResult{Stdout: "partial", Truncated: true}, nil
				}
				return WorkspaceCommandResult{}, errors.New("head probe unavailable")
			}),
		})
		t.Cleanup(eng.Close)
		snapshot, err := eng.CaptureWorkspaceDiff(t.Context(), commands.DiffContextStaged)
		if err != nil || snapshot.State != commands.WorkspaceDiffRunnerFailed || snapshot.Patch != "" || calls != 2 {
			t.Fatalf("HEAD probe failure fell through to diff: %#v, calls=%d, %v", snapshot, calls, err)
		}
	}
}

func TestWorkspaceContextBufferRetainsOnlyBudget(t *testing.T) {
	buffer := &workspaceContextBuffer{limit: 7}
	for _, value := range []string{"abcd", "efgh", strings.Repeat("x", 1<<20)} {
		if n, err := buffer.Write([]byte(value)); err != nil || n != len(value) {
			t.Fatal("bounded writer short-wrote its discarded output")
		}
	}
	if string(buffer.data) != "abcdefg" || !buffer.truncated {
		t.Fatalf("bounded capture retained extra bytes: %d", len(buffer.data))
	}
}
