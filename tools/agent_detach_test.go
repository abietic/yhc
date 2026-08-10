package tools

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type detachTestExecutor struct {
	entered chan struct{}
	release chan struct{}

	calls atomic.Int32

	lifecycleMu      sync.Mutex
	lifecycles       []AgentLaunchSnapshot
	lifecycleEntered chan struct{}
	lifecycleRelease <-chan struct{}
	lifecycleErr     error
	onLifecycle      func(AgentLaunchSnapshot)
}

func newDetachTestExecutor() *detachTestExecutor {
	return &detachTestExecutor{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
}

func (e *detachTestExecutor) ExecuteAgent(
	ctx context.Context,
	_ AgentExecOptions,
) (*AgentExecResult, error) {
	e.calls.Add(1)
	select {
	case e.entered <- struct{}{}:
	default:
	}
	select {
	case <-e.release:
		return &AgentExecResult{Result: "done"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e *detachTestExecutor) RecordAgentLifecycle(
	_ context.Context,
	lifecycle AgentLaunchSnapshot,
) error {
	if e.lifecycleEntered != nil {
		select {
		case e.lifecycleEntered <- struct{}{}:
		default:
		}
	}
	if e.lifecycleRelease != nil {
		<-e.lifecycleRelease
	}
	if e.onLifecycle != nil {
		e.onLifecycle(lifecycle)
	}
	if e.lifecycleErr != nil {
		return e.lifecycleErr
	}
	e.lifecycleMu.Lock()
	e.lifecycles = append(e.lifecycles, lifecycle)
	e.lifecycleMu.Unlock()
	return nil
}

func (e *detachTestExecutor) lifecycleSnapshots() []AgentLaunchSnapshot {
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()
	return append([]AgentLaunchSnapshot(nil), e.lifecycles...)
}

type foregroundRunOutcome struct {
	result *AgentExecResult
	err    error
}

type deadlineSignalContext struct {
	context.Context
	done <-chan struct{}
}

func (c deadlineSignalContext) Done() <-chan struct{} {
	return c.done
}

func (c deadlineSignalContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func startDetachTestForeground(
	ctx context.Context,
	t *testing.T,
	runner *AgentRunner,
	executor *detachTestExecutor,
	agentID string,
) <-chan foregroundRunOutcome {
	t.Helper()
	outcome := make(chan foregroundRunOutcome, 1)
	go func() {
		result, err := RunAgent(ctx, runner, AgentExecOptions{
			Task:            "keep running",
			AgentID:         agentID,
			SessionID:       agentID + "-session",
			ThreadID:        agentID + "-thread",
			ParentSessionID: "parent-session",
			ParentThreadID:  "parent-thread",
		})
		outcome <- foregroundRunOutcome{result: result, err: err}
	}()
	select {
	case <-executor.entered:
	case <-time.After(time.Second):
		t.Fatal("foreground executor did not start")
	}
	return outcome
}

func detachTestRequest(agentID string, generation int64) AgentDetachRequest {
	return AgentDetachRequest{
		AgentID:         agentID,
		Generation:      generation,
		ParentSessionID: "parent-session",
	}
}

func TestDetachAgentReleasesWaitWithoutRestartingOrCancelingChild(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	executor := newDetachTestExecutor()
	runner.SetExecutor(executor)
	parentCtx, cancelParent := context.WithCancel(context.Background())
	outcome := startDetachTestForeground(
		parentCtx,
		t,
		runner,
		executor,
		"foreground-detach",
	)

	detached, err := runner.DetachAgent(detachTestRequest("foreground-detach", 1))
	if err != nil {
		t.Fatalf("detach foreground Agent: %v", err)
	}
	if detached.Outcome != AgentExecOutcomeBackgrounded ||
		detached.AgentID != "foreground-detach" ||
		detached.SessionID != "foreground-detach-session" ||
		detached.ThreadID != "foreground-detach-thread" ||
		detached.Generation != 1 ||
		detached.Status != "running" {
		t.Fatalf("detach result = %#v", detached)
	}
	select {
	case got := <-outcome:
		if got.err != nil || got.result == nil {
			t.Fatalf("foreground wait outcome: result=%#v err=%v", got.result, got.err)
		}
		if got.result.Outcome != AgentExecOutcomeBackgrounded ||
			got.result.AgentID != detached.AgentID ||
			got.result.SessionID != detached.SessionID ||
			got.result.ThreadID != detached.ThreadID ||
			got.result.Generation != detached.Generation {
			t.Fatalf("foreground wait result = %#v, detach = %#v", got.result, detached)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground wait was not released")
	}

	if _, err := runner.DetachAgent(detachTestRequest("foreground-detach", 1)); err == nil ||
		!strings.Contains(err.Error(), "already backgrounded") {
		t.Fatalf("second detach error = %v, want already backgrounded", err)
	}
	cancelParent()
	select {
	case <-time.After(25 * time.Millisecond):
	case <-executor.entered:
		t.Fatal("executor restarted after detach")
	}
	if snapshot, _ := runner.GetAgentSnapshot("foreground-detach"); snapshot.Status != "running" {
		t.Fatalf("parent cancellation reached detached child: %#v", snapshot)
	}
	if calls := executor.calls.Load(); calls != 1 {
		t.Fatalf("executor calls = %d, want 1", calls)
	}
	lifecycles := executor.lifecycleSnapshots()
	if len(lifecycles) != 1 ||
		lifecycles[0].Phase != "backgrounded" ||
		lifecycles[0].AgentID != detached.AgentID ||
		lifecycles[0].Generation != detached.Generation {
		t.Fatalf("background lifecycle snapshots = %#v", lifecycles)
	}

	close(executor.release)
	completed := waitForAgentStatus(t, runner, "foreground-detach", "completed")
	if completed.ExecutionGeneration() != 1 || completed.Result != "done" {
		t.Fatalf("detached child terminal snapshot = %#v", completed)
	}
}

func TestForegroundCompletionReturnsStableIdentity(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	runner.SetExecutor(fakeAgentExecutor{onExecute: func(
		context.Context,
		AgentExecOptions,
	) (*AgentExecResult, error) {
		return &AgentExecResult{Result: "done"}, nil
	}})

	result, err := RunAgent(context.Background(), runner, AgentExecOptions{
		Task:            "complete",
		AgentID:         "foreground-complete",
		SessionID:       "foreground-complete-session",
		ThreadID:        "foreground-complete-thread",
		ParentSessionID: "parent-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != AgentExecOutcomeCompleted ||
		result.AgentID != "foreground-complete" ||
		result.SessionID != "foreground-complete-session" ||
		result.ThreadID != "foreground-complete-thread" ||
		result.Generation != 1 {
		t.Fatalf("foreground completion result = %#v", result)
	}
	if _, err := runner.DetachAgent(detachTestRequest("foreground-complete", 1)); err == nil ||
		!strings.Contains(err.Error(), "not running") {
		t.Fatalf("terminal detach error = %v, want not running", err)
	}
}

func TestFormatAgentResultReportsBackgroundedIdentity(t *testing.T) {
	formatted := formatAgentResult("inspect repository", &AgentExecResult{
		Outcome:    AgentExecOutcomeBackgrounded,
		AgentID:    "agent-format",
		Generation: 7,
	})
	for _, want := range []string{
		"Agent moved to background.",
		"Agent ID: agent-format",
		"Generation: 7",
		"Status: running",
		"Task: inspect repository",
		"The same Agent continues running",
	} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("formatted backgrounded result %q does not contain %q", formatted, want)
		}
	}
}

func TestParentCancelBeforeDetachCancelsForegroundChild(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	executor := newDetachTestExecutor()
	runner.SetExecutor(executor)
	parentCtx, cancelParent := context.WithCancel(context.Background())
	outcome := startDetachTestForeground(
		parentCtx,
		t,
		runner,
		executor,
		"foreground-parent-cancel",
	)

	cancelParent()
	select {
	case got := <-outcome:
		if got.err == nil || !strings.Contains(got.err.Error(), "context canceled") {
			t.Fatalf("foreground cancellation outcome: result=%#v err=%v", got.result, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground parent cancellation did not settle")
	}
	aborted := waitForAgentStatus(t, runner, "foreground-parent-cancel", "aborted")
	if aborted.Error == nil {
		t.Fatalf("aborted child did not retain cancellation error: %#v", aborted)
	}
	if _, err := runner.DetachAgent(detachTestRequest("foreground-parent-cancel", 1)); err == nil {
		t.Fatal("detach succeeded after parent cancellation")
	}
}

func TestParentDeadlineBeforeDetachPreservesDeadlineError(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	executor := newDetachTestExecutor()
	runner.SetExecutor(executor)
	parentDone := make(chan struct{})
	outcome := startDetachTestForeground(
		deadlineSignalContext{
			Context: context.Background(),
			done:    parentDone,
		},
		t,
		runner,
		executor,
		"foreground-parent-deadline",
	)

	close(parentDone)
	select {
	case got := <-outcome:
		if !errors.Is(got.err, context.DeadlineExceeded) {
			t.Fatalf(
				"foreground deadline outcome: result=%#v err=%v",
				got.result,
				got.err,
			)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground parent deadline did not settle")
	}
	waitForAgentStatus(t, runner, "foreground-parent-deadline", "aborted")
}

func TestAbortAndShutdownStillOwnDetachedChildCancellation(t *testing.T) {
	for _, test := range []struct {
		name   string
		cancel func(*AgentRunner, string) error
	}{
		{
			name: "abort",
			cancel: func(runner *AgentRunner, agentID string) error {
				return runner.AbortAgent(agentID)
			},
		},
		{
			name: "shutdown",
			cancel: func(runner *AgentRunner, _ string) error {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				return runner.Shutdown(ctx)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := NewAgentRunner(1)
			runner.SetOutputDir(t.TempDir())
			executor := newDetachTestExecutor()
			runner.SetExecutor(executor)
			agentID := "foreground-" + test.name
			outcome := startDetachTestForeground(
				context.Background(),
				t,
				runner,
				executor,
				agentID,
			)
			if _, err := runner.DetachAgent(detachTestRequest(agentID, 1)); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-outcome:
				if got.err != nil || got.result.Outcome != AgentExecOutcomeBackgrounded {
					t.Fatalf("detach outcome = %#v, err=%v", got.result, got.err)
				}
			case <-time.After(time.Second):
				t.Fatal("foreground wait was not released")
			}
			if err := test.cancel(runner, agentID); err != nil {
				t.Fatalf("%s detached Agent: %v", test.name, err)
			}
			joinCtx, cancelJoin := context.WithTimeout(context.Background(), time.Second)
			if err := runner.Shutdown(joinCtx); err != nil {
				cancelJoin()
				t.Fatalf("join %s detached Agent: %v", test.name, err)
			}
			cancelJoin()
			aborted, _ := runner.GetAgentSnapshot(agentID)
			if aborted.Error == nil {
				t.Fatalf("%s did not settle detached execution: %#v", test.name, aborted)
			}
		})
	}
}

func TestDetachAgentRejectsWrongOwnerGenerationAndOriginalBackground(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	executor := newDetachTestExecutor()
	runner.SetExecutor(executor)
	started, err := RunAgentBackground(context.Background(), runner, AgentExecOptions{
		Task:            "already background",
		AgentID:         "original-background",
		ParentSessionID: "parent-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-executor.entered:
	case <-time.After(time.Second):
		t.Fatal("background executor did not start")
	}

	for _, test := range []struct {
		name    string
		request AgentDetachRequest
		want    string
	}{
		{
			name:    "original background",
			request: detachTestRequest(started.ID, 1),
			want:    "no foreground wait",
		},
		{
			name: "wrong owner",
			request: AgentDetachRequest{
				AgentID:         started.ID,
				Generation:      1,
				ParentSessionID: "other-parent",
			},
			want: "not owned",
		},
		{
			name:    "stale generation",
			request: detachTestRequest(started.ID, 2),
			want:    "generation is 1",
		},
		{
			name:    "unknown Agent",
			request: detachTestRequest("missing", 1),
			want:    "not found",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := runner.DetachAgent(test.request); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("detach error = %v, want %q", err, test.want)
			}
		})
	}

	if err := runner.AbortAgent(started.ID); err != nil {
		t.Fatal(err)
	}
	joinCtx, cancelJoin := context.WithTimeout(context.Background(), time.Second)
	defer cancelJoin()
	if err := runner.Shutdown(joinCtx); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentDetachHasOneWinnerAndOneLifecycleEvent(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	executor := newDetachTestExecutor()
	runner.SetExecutor(executor)
	outcome := startDetachTestForeground(
		context.Background(),
		t,
		runner,
		executor,
		"foreground-concurrent-detach",
	)

	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := runner.DetachAgent(
				detachTestRequest("foreground-concurrent-detach", 1),
			)
			results <- err
		}()
	}
	close(start)
	var successes int
	var alreadyBackgrounded int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case strings.Contains(err.Error(), "already backgrounded"),
			strings.Contains(err.Error(), "already in progress"):
			alreadyBackgrounded++
		default:
			t.Fatalf("unexpected concurrent detach error: %v", err)
		}
	}
	if successes != 1 || alreadyBackgrounded != 1 {
		t.Fatalf(
			"concurrent detach winners=%d already-backgrounded=%d",
			successes,
			alreadyBackgrounded,
		)
	}
	select {
	case got := <-outcome:
		if got.err != nil || got.result.Outcome != AgentExecOutcomeBackgrounded {
			t.Fatalf("detach outcome = %#v, err=%v", got.result, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground wait was not released")
	}
	if lifecycles := executor.lifecycleSnapshots(); len(lifecycles) != 1 {
		t.Fatalf("lifecycle event count = %d, want 1", len(lifecycles))
	}
	close(executor.release)
	waitForAgentStatus(t, runner, "foreground-concurrent-detach", "completed")
}

func TestDetachLifecycleRecorderMayReenterRunner(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	executor := newDetachTestExecutor()
	executor.onLifecycle = func(lifecycle AgentLaunchSnapshot) {
		snapshot, ok := runner.GetAgentSnapshot(lifecycle.AgentID)
		if !ok || snapshot.ExecutionGeneration() != lifecycle.Generation {
			t.Errorf(
				"reentrant lifecycle snapshot = %#v, found=%v",
				snapshot,
				ok,
			)
		}
	}
	runner.SetExecutor(executor)
	outcome := startDetachTestForeground(
		context.Background(),
		t,
		runner,
		executor,
		"foreground-reentrant-recorder",
	)

	if _, err := runner.DetachAgent(
		detachTestRequest("foreground-reentrant-recorder", 1),
	); err != nil {
		t.Fatalf("detach with reentrant recorder: %v", err)
	}
	select {
	case got := <-outcome:
		if got.err != nil || got.result.Outcome != AgentExecOutcomeBackgrounded {
			t.Fatalf("reentrant recorder outcome = %#v, err=%v", got.result, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("reentrant lifecycle recorder deadlocked detach")
	}
	close(executor.release)
	waitForAgentStatus(t, runner, "foreground-reentrant-recorder", "completed")
}

func TestDetachLifecycleFailureReleasesConcurrentCompletion(t *testing.T) {
	runner := NewAgentRunner(1)
	runner.SetOutputDir(t.TempDir())
	executor := newDetachTestExecutor()
	executor.lifecycleEntered = make(chan struct{}, 1)
	releaseLifecycle := make(chan struct{})
	executor.lifecycleRelease = releaseLifecycle
	executor.lifecycleErr = errors.New("projection unavailable")
	runner.SetExecutor(executor)
	outcome := startDetachTestForeground(
		context.Background(),
		t,
		runner,
		executor,
		"foreground-lifecycle-failure",
	)

	detachErr := make(chan error, 1)
	go func() {
		_, err := runner.DetachAgent(
			detachTestRequest("foreground-lifecycle-failure", 1),
		)
		detachErr <- err
	}()
	select {
	case <-executor.lifecycleEntered:
	case <-time.After(time.Second):
		t.Fatal("detach did not enter lifecycle recorder")
	}
	close(executor.release)
	waitForAgentStatus(t, runner, "foreground-lifecycle-failure", "completed")
	close(releaseLifecycle)
	if err := <-detachErr; err == nil ||
		!strings.Contains(err.Error(), "projection unavailable") {
		t.Fatalf("detach lifecycle error = %v", err)
	}
	select {
	case got := <-outcome:
		if got.err != nil ||
			got.result == nil ||
			got.result.Outcome != AgentExecOutcomeCompleted ||
			got.result.Result != "done" {
			t.Fatalf("completion after lifecycle failure = %#v, err=%v", got.result, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("lifecycle failure leaked foreground wait")
	}
}

func TestAbortAndShutdownCanCancelWhileDetachLifecycleIsBlocked(t *testing.T) {
	for _, test := range []struct {
		name   string
		cancel func(*AgentRunner, string) error
	}{
		{
			name: "abort",
			cancel: func(runner *AgentRunner, agentID string) error {
				return runner.AbortAgent(agentID)
			},
		},
		{
			name: "shutdown",
			cancel: func(runner *AgentRunner, _ string) error {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				return runner.Shutdown(ctx)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := NewAgentRunner(1)
			runner.SetOutputDir(t.TempDir())
			executor := newDetachTestExecutor()
			executor.lifecycleEntered = make(chan struct{}, 1)
			releaseLifecycle := make(chan struct{})
			executor.lifecycleRelease = releaseLifecycle
			runner.SetExecutor(executor)
			agentID := "foreground-blocked-lifecycle-" + test.name
			outcome := startDetachTestForeground(
				context.Background(),
				t,
				runner,
				executor,
				agentID,
			)

			detachErr := make(chan error, 1)
			go func() {
				_, err := runner.DetachAgent(detachTestRequest(agentID, 1))
				detachErr <- err
			}()
			select {
			case <-executor.lifecycleEntered:
			case <-time.After(time.Second):
				t.Fatal("detach did not enter lifecycle recorder")
			}
			if err := test.cancel(runner, agentID); err != nil {
				t.Fatalf("%s during blocked lifecycle: %v", test.name, err)
			}
			close(releaseLifecycle)
			if err := <-detachErr; err != nil {
				t.Fatalf("linearized detach lost to %s: %v", test.name, err)
			}
			select {
			case got := <-outcome:
				if got.err != nil ||
					got.result == nil ||
					got.result.Outcome != AgentExecOutcomeBackgrounded {
					t.Fatalf(
						"blocked lifecycle %s outcome = %#v, err=%v",
						test.name,
						got.result,
						got.err,
					)
				}
			case <-time.After(time.Second):
				t.Fatalf("%s leaked foreground wait", test.name)
			}
			joinCtx, cancelJoin := context.WithTimeout(
				context.Background(),
				time.Second,
			)
			if err := runner.Shutdown(joinCtx); err != nil {
				cancelJoin()
				t.Fatalf("join after %s: %v", test.name, err)
			}
			cancelJoin()
			aborted, _ := runner.GetAgentSnapshot(agentID)
			if aborted.Status != "aborted" || aborted.Error == nil {
				t.Fatalf("%s did not cancel detached child: %#v", test.name, aborted)
			}
		})
	}
}

func TestDetachAndCompletionRaceSettlesForegroundWaitOnce(t *testing.T) {
	for iteration := range 25 {
		runner := NewAgentRunner(1)
		runner.SetOutputDir(t.TempDir())
		executor := newDetachTestExecutor()
		runner.SetExecutor(executor)
		agentID := "foreground-race-" + time.Now().Format("150405.000000000")
		outcome := startDetachTestForeground(
			context.Background(),
			t,
			runner,
			executor,
			agentID,
		)

		start := make(chan struct{})
		detachResult := make(chan error, 1)
		go func() {
			<-start
			_, err := runner.DetachAgent(detachTestRequest(agentID, 1))
			detachResult <- err
		}()
		go func() {
			<-start
			close(executor.release)
		}()
		close(start)

		detachErr := <-detachResult
		select {
		case got := <-outcome:
			if got.err != nil {
				t.Fatalf("iteration %d foreground outcome error: %v", iteration, got.err)
			}
			if detachErr == nil {
				if got.result == nil || got.result.Outcome != AgentExecOutcomeBackgrounded {
					t.Fatalf(
						"iteration %d detach won but outcome = %#v",
						iteration,
						got.result,
					)
				}
			} else {
				if !strings.Contains(detachErr.Error(), "not running") {
					t.Fatalf(
						"iteration %d unexpected losing detach error: %v",
						iteration,
						detachErr,
					)
				}
				if got.result == nil || got.result.Outcome != AgentExecOutcomeCompleted {
					t.Fatalf(
						"iteration %d completion won but outcome = %#v",
						iteration,
						got.result,
					)
				}
			}
		case <-time.After(time.Second):
			t.Fatalf("iteration %d foreground wait did not settle", iteration)
		}
		waitForAgentStatus(t, runner, agentID, "completed")
		wantLifecycles := 0
		if detachErr == nil {
			wantLifecycles = 1
		}
		if got := len(executor.lifecycleSnapshots()); got != wantLifecycles {
			t.Fatalf(
				"iteration %d lifecycle count = %d, want %d",
				iteration,
				got,
				wantLifecycles,
			)
		}
		if calls := executor.calls.Load(); calls != 1 {
			t.Fatalf("iteration %d executor calls = %d, want 1", iteration, calls)
		}
	}
}
