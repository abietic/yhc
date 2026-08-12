package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/abietic/yhc/scripts/internal/ownedprocess"
)

const targetLogLimit int64 = 2 << 20

type TargetRunner interface {
	Run(context.Context, string, string, string) RunResult
}

var targetDeadlines = map[string]time.Duration{
	"fmt": 3 * time.Minute, "lint": 10 * time.Minute, "test": 15 * time.Minute, "build": 15 * time.Minute,
	"docs-check": 5 * time.Minute, "docs-check-ci": 5 * time.Minute, "test-contract": 5 * time.Minute,
	"test-race": 10 * time.Minute, "test-pty": 5 * time.Minute, "test-fuzz-smoke": 5 * time.Minute, "test-e2e": 10 * time.Minute,
	"desktop-check": 5 * time.Minute,
	"check-boundaries": 5 * time.Minute, "test-fault-injection": 10 * time.Minute,
	"test-fuzz-deep": 10 * time.Minute, "test-e2e-deep": 10 * time.Minute, "test-pty-deep": 10 * time.Minute,
}

// targetProcess is deliberately narrower than exec.Cmd so tests can control
// starts, cancellation, output, and elapsed time without sleeping.
type targetProcess interface {
	setDir(string)
	setEnv([]string)
	setOutput(io.Writer)
	Start() error
	Wait() error
}

type processFactory func(context.Context, string, ...string) targetProcess

type commandTargetRunner struct {
	focused     map[string][]string
	base        string
	factory     processFactory
	now         func() time.Time
	withTimeout func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	beforeStart func()
	afterStart  func()
}

func newCommandTargetRunner(plan Plan) *commandTargetRunner {
	r := &commandTargetRunner{
		focused: map[string][]string{}, factory: newExecProcess, now: time.Now, withTimeout: context.WithTimeout,
		beforeStart: func() {}, afterStart: func() {},
	}
	r.UsePlan(plan)
	return r
}

func (r *commandTargetRunner) UsePlan(plan Plan) {
	r.base = plan.Base
	r.focused = make(map[string][]string, len(plan.FocusedChecks))
	for _, check := range plan.FocusedChecks {
		r.focused["focused/"+check.Owner] = append([]string(nil), check.Packages...)
	}
}

func (r *commandTargetRunner) Run(parent context.Context, root, digest, target string) RunResult {
	args, timeout, ok := r.command(target)
	if !ok || parent.Err() != nil {
		return RunResult{Status: GateBlocked}
	}
	r.beforeStart()
	if parent.Err() != nil {
		return RunResult{Status: GateBlocked}
	}
	ctx, cancel := r.withTimeout(parent, timeout)
	defer cancel()
	logPath, log, err := targetLog(root, digest, target)
	if err != nil {
		return RunResult{Status: GateBlocked}
	}
	closed := false
	defer func() {
		if !closed {
			_ = log.Close()
		}
	}()

	startedAt := r.now()
	process := r.factory(ctx, args[0], args[1:]...)
	process.setDir(root)
	process.setEnv(scrubbedEnvironment(os.Environ()))
	limited := &limitedLog{w: log, max: targetLogLimit}
	process.setOutput(limited)
	if err := process.Start(); err != nil {
		result := r.result(parent, ctx, startedAt, logPath, limited, err)
		closed = true
		if closeErr := log.Close(); closeErr != nil && result.Status == GatePass {
			return RunResult{Status: GateBlocked, DurationMillis: result.DurationMillis, FailureLogPath: logPath}
		}
		return result
	}
	r.afterStart()
	err = process.Wait()
	result := r.result(parent, ctx, startedAt, logPath, limited, err)
	closed = true
	if closeErr := log.Close(); closeErr != nil && result.Status == GatePass {
		return RunResult{Status: GateBlocked, DurationMillis: result.DurationMillis, FailureLogPath: logPath}
	}
	if result.Status == GatePass {
		if err := removeTargetLog(root, logPath); err != nil {
			return RunResult{Status: GateBlocked, DurationMillis: result.DurationMillis, FailureLogPath: logPath}
		}
	}
	return result
}

func (r *commandTargetRunner) result(parent, ctx context.Context, startedAt time.Time, logPath string, limited *limitedLog, err error) RunResult {
	duration := r.now().Sub(startedAt).Milliseconds()
	if duration < 1 {
		duration = 1
	}
	if err == nil && !limited.exceeded {
		return RunResult{Status: GatePass, DurationMillis: duration}
	}
	result := RunResult{Status: GateFail, DurationMillis: duration, FailureLogPath: logPath}
	if errors.Is(parent.Err(), context.Canceled) {
		result.Status = GateBlocked
		return result
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(context.Cause(ctx), context.DeadlineExceeded) || limited.exceeded {
		return result
	}
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) {
		code := exit.ExitCode()
		result.ExitCode = &code
		result.FirstFailingSeed = fuzzSeed(string(limited.seed))
		return result
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		result.Status = GateBlocked
		return result
	}
	result.Status = GateBlocked
	return result
}

func (r *commandTargetRunner) command(target string) ([]string, time.Duration, bool) {
	if target == "git-diff-check" && r.base != "" {
		return []string{"git", "diff", "--check", r.base + "..HEAD", "--"}, 5 * time.Minute, true
	}
	if target == "check-boundaries" && r.base != "" {
		return []string{"make", target, "ITERATION_BASE=" + r.base}, targetDeadlines[target], true
	}
	if timeout, ok := targetDeadlines[target]; ok {
		return []string{"make", target}, timeout, true
	}
	if packages, ok := r.focused[target]; ok && len(packages) > 0 {
		return append([]string{"go", "test"}, append(packages, "-count=1")...), 15 * time.Minute, true
	}
	return nil, 0, false
}

func scrubbedEnvironment(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key := strings.ToUpper(strings.SplitN(entry, "=", 2)[0])
		if credentialEnvironmentKey(key) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func credentialEnvironmentKey(key string) bool {
	if strings.HasPrefix(key, "GIT_AUTHOR_") {
		return false
	}
	return strings.Contains(key, "TOKEN") ||
		strings.Contains(key, "SECRET") ||
		strings.Contains(key, "PASSWORD") ||
		strings.Contains(key, "API_KEY") ||
		strings.Contains(key, "ACCESS_KEY") ||
		strings.Contains(key, "PRIVATE_KEY") ||
		strings.Contains(key, "AUTH")
}

func targetLog(root, digest, target string) (string, *os.File, error) {
	if !digestPattern.MatchString(digest) || !safeLogTarget(target) {
		return "", nil, errors.New("invalid target log path")
	}
	repository, err := os.OpenRoot(root)
	if err != nil {
		return "", nil, err
	}
	defer repository.Close()
	for _, directory := range []string{"build", "build/iteration", "build/iteration/" + digest, "build/iteration/" + digest + "/logs"} {
		if err := ensureLogDirectory(repository, directory); err != nil {
			return "", nil, err
		}
	}
	relative := path.Join("build", "iteration", digest, "logs", strings.ReplaceAll(target, "/", "-")+".log")
	if info, err := repository.Lstat(relative); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", nil, errors.New("target log is not a regular file")
		}
		return "", nil, errors.New("target log already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", nil, err
	}
	file, err := repository.OpenFile(relative, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return "", nil, err
	}
	return relative, file, nil
}

func ensureLogDirectory(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return root.Mkdir(name, 0o700)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("target log directory is unsafe")
	}
	return nil
}

func removeTargetLog(root, relative string) error {
	repository, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer repository.Close()
	return repository.Remove(relative)
}

func safeLogTarget(target string) bool {
	return target == "git-diff-check" || targetDeadlines[target] > 0 || focusedTargetPattern.MatchString(target)
}

var focusedTargetPattern = regexp.MustCompile(`^focused/[A-Za-z0-9_.-]+$`)

type limitedLog struct {
	w        io.Writer
	max      int64
	n        int64
	exceeded bool
	seed     []byte
}

func (w *limitedLog) Write(p []byte) (int, error) {
	remaining := w.max - w.n
	if remaining <= 0 {
		w.exceeded = true
		return len(p), nil
	}
	write := p
	if int64(len(write)) > remaining {
		write = write[:remaining]
		w.exceeded = true
	}
	n, err := w.w.Write(write)
	w.n += int64(n)
	if n > 0 {
		w.captureSeed(write[:n])
	}
	if err != nil {
		return n, err
	}
	if n != len(write) {
		return n, io.ErrShortWrite
	}
	return len(p), nil
}

func (w *limitedLog) captureSeed(data []byte) {
	const seedWindow = 8 << 10
	if len(data) >= seedWindow {
		w.seed = append(w.seed[:0], data[len(data)-seedWindow:]...)
		return
	}
	w.seed = append(w.seed, data...)
	if overflow := len(w.seed) - seedWindow; overflow > 0 {
		copy(w.seed, w.seed[overflow:])
		w.seed = w.seed[:seedWindow]
	}
}

var fuzzSeedPattern = regexp.MustCompile(`Fuzz[A-Za-z0-9_]+/[0-9a-f]{16}`)

func fuzzSeed(value string) string { return fuzzSeedPattern.FindString(value) }

type execProcess struct {
	ctx context.Context
	cmd *exec.Cmd
}

func newExecProcess(ctx context.Context, name string, args ...string) targetProcess {
	return &execProcess{ctx: ctx, cmd: exec.CommandContext(context.WithoutCancel(ctx), name, args...)}
}
func (p *execProcess) setDir(dir string)          { p.cmd.Dir = dir }
func (p *execProcess) setEnv(env []string)        { p.cmd.Env = env }
func (p *execProcess) setOutput(writer io.Writer) { p.cmd.Stdout, p.cmd.Stderr = writer, writer }

// ownedprocess starts and waits as one operation so cancellation always owns
// the complete descendant tree. Start remains the injected test seam.
func (p *execProcess) Start() error { return nil }
func (p *execProcess) Wait() error  { return ownedprocess.Run(p.ctx, p.cmd) }
