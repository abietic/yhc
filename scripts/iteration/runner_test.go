package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTargetMapping(t *testing.T) {
	r := newCommandTargetRunner(Plan{Base: "abc", FocusedChecks: []FocusedCheck{{Owner: "engine-runtime", Packages: []string{"./engine/..."}}}})
	for target, want := range map[string][]string{
		"fmt":                    {"make", "fmt"},
		"focused/engine-runtime": {"go", "test", "./engine/...", "-count=1"},
		"git-diff-check":         {"git", "diff", "--check", "abc..HEAD", "--"},
		"check-boundaries":       {"make", "check-boundaries", "ITERATION_BASE=abc"},
	} {
		got, _, ok := r.command(target)
		if !ok || strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("command(%q) = %#v, %v; want %#v", target, got, ok, want)
		}
	}
	if _, _, ok := r.command("arbitrary"); ok {
		t.Fatal("unknown target accepted")
	}
	r.UsePlan(Plan{Base: "def", FocusedChecks: []FocusedCheck{{Owner: "governance", Packages: []string{"./scripts/iteration"}}}})
	if got, _, ok := r.command("git-diff-check"); !ok || strings.Join(got, " ") != "git diff --check def..HEAD --" {
		t.Fatalf("updated diff command = %#v, %v", got, ok)
	}
	if _, _, ok := r.command("focused/engine-runtime"); ok {
		t.Fatal("UsePlan retained a stale focused owner")
	}
}

func TestTargetRunnerStatuses(t *testing.T) {
	digest := strings.Repeat("a", 64)
	cases := []struct {
		name     string
		parent   func() (context.Context, context.CancelFunc)
		wait     error
		start    error
		want     GateStatus
		wantCode *int
	}{
		{name: "pass", parent: backgroundContext, want: GatePass},
		{name: "exit failure", parent: backgroundContext, wait: exitError{code: 1}, want: GateFail, wantCode: intPointer(1)},
		{name: "wrapped exit failure", parent: backgroundContext, wait: fmt.Errorf("owned: %w", exitError{code: 7}), want: GateFail, wantCode: intPointer(7)},
		{name: "missing executable", parent: backgroundContext, start: execNotFoundError{}, want: GateBlocked},
		{name: "parent canceled before start", parent: canceledContext, want: GateBlocked},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			parent, cancel := tc.parent()
			defer cancel()
			process := &fakeProcess{startErr: tc.start, waitErr: tc.wait}
			runner := testRunner(process)
			got := runner.Run(parent, root, digest, "fmt")
			if got.Status != tc.want || !equalIntPointer(got.ExitCode, tc.wantCode) {
				t.Fatalf("Run() = %#v, want status %q code %#v", got, tc.want, tc.wantCode)
			}
			if tc.name == "parent canceled before start" && process.started {
				t.Fatal("pre-canceled parent started process")
			}
			if got.DurationMillis < 1 && tc.name != "parent canceled before start" {
				t.Fatalf("executed duration = %d, want >= 1", got.DurationMillis)
			}
		})
	}
}

func TestTargetRunnerCancellationAndDeadline(t *testing.T) {
	root, digest := t.TempDir(), strings.Repeat("b", 64)
	t.Run("parent canceled during barrier", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		process := &fakeProcess{}
		runner := testRunner(process)
		runner.beforeStart = func() { cancel() }
		got := runner.Run(parent, root, digest, "fmt")
		if got.Status != GateBlocked || process.started || got.DurationMillis != 0 {
			t.Fatalf("Run() = %#v; process started=%v", got, process.started)
		}
	})
	t.Run("deadline is failure", func(t *testing.T) {
		deadline, trigger := context.WithCancelCause(context.Background())
		defer trigger(nil)
		process := &fakeProcess{wait: func() error { trigger(context.DeadlineExceeded); return context.DeadlineExceeded }}
		runner := testRunner(process)
		runner.withTimeout = func(context.Context, time.Duration) (context.Context, context.CancelFunc) { return deadline, func() {} }
		got := runner.Run(context.Background(), root, digest, "fmt")
		if got.Status != GateFail || got.DurationMillis < 1 {
			t.Fatalf("Run() = %#v", got)
		}
	})
}

func TestTargetRunnerOverflowAndLogs(t *testing.T) {
	root, digest := t.TempDir(), strings.Repeat("c", 64)
	process := &fakeProcess{write: strings.Repeat("x", int(targetLogLimit+1))}
	got := testRunner(process).Run(context.Background(), root, digest, "fmt")
	if got.Status != GateFail || got.FailureLogPath == "" {
		t.Fatalf("overflow Run() = %#v", got)
	}
	if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(got.FailureLogPath))); err != nil || info.Size() != targetLogLimit {
		t.Fatalf("log = %v, %v; want capped log", info, err)
	}
	pass := testRunner(&fakeProcess{}).Run(context.Background(), root, digest, "lint")
	if pass.Status != GatePass || pass.FailureLogPath != "" {
		t.Fatalf("pass Run() = %#v", pass)
	}
}

func TestTargetLogRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	digest := strings.Repeat("d", 64)
	if err := os.MkdirAll(filepath.Join(root, "build", "iteration"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "build", "iteration", digest)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := targetLog(root, digest, "fmt"); err == nil {
		t.Fatal("digest symlink accepted")
	}
}

func TestFuzzSeed(t *testing.T) {
	if got := fuzzSeed("bad FuzzName/0123456789abcdef trailing"); got != "FuzzName/0123456789abcdef" {
		t.Fatalf("seed = %q", got)
	}
	for _, value := range []string{"FuzzName/0123456789abcde", "Fuzz-Name/0123456789abcdef", "FuzzName/0123456789ABCDEF"} {
		if got := fuzzSeed(value); got != "" {
			t.Fatalf("seed(%q) = %q", value, got)
		}
	}
	log := &limitedLog{w: io.Discard, max: targetLogLimit}
	_, _ = log.Write([]byte(strings.Repeat("x", 9<<10) + " FuzzLate/0123456789abcdef"))
	if got := fuzzSeed(string(log.seed)); got != "FuzzLate/0123456789abcdef" {
		t.Fatalf("late seed = %q", got)
	}
}

func TestScrubbedEnvironment(t *testing.T) {
	got := scrubbedEnvironment([]string{"PATH=/bin", "api_key=x", "AUTH_TOKEN=x", "PRIVATE_KEY=x", "GIT_AUTHOR_NAME=Test", "normal=yes"})
	if strings.Join(got, ",") != "PATH=/bin,GIT_AUTHOR_NAME=Test,normal=yes" {
		t.Fatalf("environment = %#v", got)
	}
}

type fakeProcess struct {
	startErr, waitErr error
	write             string
	wait              func() error
	started           bool
	output            io.Writer
}

func (p *fakeProcess) setDir(string)         {}
func (p *fakeProcess) setEnv([]string)       {}
func (p *fakeProcess) setOutput(w io.Writer) { p.output = w }
func (p *fakeProcess) Start() error          { p.started = true; return p.startErr }
func (p *fakeProcess) Wait() error {
	if p.write != "" {
		_, _ = io.WriteString(p.output, p.write)
	}
	if p.wait != nil {
		return p.wait()
	}
	return p.waitErr
}

type exitError struct{ code int }

func (e exitError) Error() string { return "exit" }
func (e exitError) ExitCode() int { return e.code }

type execNotFoundError struct{}

func (execNotFoundError) Error() string { return "not found" }
func (execNotFoundError) Unwrap() error { return os.ErrNotExist }
func canceledContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx, func() {}
}

func backgroundContext() (context.Context, context.CancelFunc) {
	return context.Background(), func() {}
}

func testRunner(process targetProcess) *commandTargetRunner {
	r := newCommandTargetRunner(Plan{})
	r.factory = func(context.Context, string, ...string) targetProcess { return process }
	r.now = func() time.Time { return time.Unix(0, 0) }
	r.afterStart = func() { r.now = func() time.Time { return time.Unix(0, int64(time.Millisecond)) } }
	return r
}
func intPointer(value int) *int { return &value }
func equalIntPointer(a, b *int) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
