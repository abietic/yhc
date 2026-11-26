package tools

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func backgroundFeedbackFixture(t *testing.T, body string) (*ShellManager, string) {
	t.Helper()
	manager := NewShellManager()
	t.Cleanup(func() { _ = manager.KillAll() })
	dir := t.TempDir()
	marker := filepath.Join(dir, "command-finished")
	quoted := "'" + strings.ReplaceAll(marker, "'", "'\\''") + "'"
	command := body + "\nyhc_feedback_code=$?\n: > " + quoted + "\n(exit \"$yhc_feedback_code\")"
	result, err := runBackgroundCommand(t.Context(), manager, dir, command, "feedback fixture", bashMaxExecutionTimeout)
	if err != nil {
		t.Fatal(err)
	}
	_, tail, ok := strings.Cut(result, "Shell ID: ")
	if !ok {
		t.Fatal("background start omitted its handle")
	}
	id := strings.SplitN(tail, "\n", 2)[0]
	t.Cleanup(func() { backgroundShells.Delete(id) })
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("background command did not reach its independent completion marker")
		case <-tick.C:
		}
	}
	// The same-shell command lock is an independent barrier after the marker.
	if _, err := manager.ExecuteAt(context.Background(), id, dir, ":", time.Second); err != nil {
		t.Fatal(err)
	}
	return manager, id
}

func waitBackgroundFeedback(t *testing.T, manager *ShellManager, id, status string) string {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		result, err := executeBashOutput(manager, fileToolInput(t, map[string]any{"bash_id": id}))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(result, "Status: "+status) {
			return result
		}
		select {
		case <-deadline.C:
			t.Fatalf("finished command has no %s feedback: %s", status, result)
		case <-tick.C:
		}
	}
}

func TestBashExplicitExecutionTimeoutInForegroundAndBackground(t *testing.T) {
	for _, mode := range []struct {
		name       string
		background bool
	}{
		{name: "foreground"},
		{name: "background", background: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			dir := t.TempDir()
			manager := NewShellManager()
			t.Cleanup(func() {
				// Release the fixture even if the execution bound regresses.
				if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
					t.Error(err)
				}
				_ = manager.KillAll()
			})
			ctx := WithExecutionCWD(WithShellManager(t.Context(), manager), dir)
			// Neither arm can exit normally. The wait bound is longer than
			// the requested execution timeout and cannot terminate the job.
			result, err := BashTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{
				"command":           "while [ ! -f release ]; do sleep 0.02; done",
				"timeout":           250,
				"run_in_background": mode.background,
			}))
			if err != nil {
				t.Fatal(err)
			}
			if mode.background {
				_, tail, ok := strings.Cut(result, "Shell ID: ")
				if !ok {
					t.Fatal("background start omitted its handle")
				}
				id, _, _ := strings.Cut(tail, "\n")
				result, err = BashOutputTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{
					"bash_id": id, "wait": true, "timeout_ms": 2000,
				}))
				if err != nil || !strings.Contains(result, "Status: timed_out") {
					t.Fatalf("background execution did not reach a timed-out terminal: %s, %v", result, err)
				}
			}
			if !strings.Contains(result, "[timeout]") || strings.Contains(result, "[exit code:") {
				t.Fatalf("execution timeout did not preserve the shell outcome: %s", result)
			}
		})
	}
}

func TestBackgroundBashRetainsOutputAndFailure(t *testing.T) {
	manager, id := backgroundFeedbackFixture(t, "printf 'stdout-proof\\n'; printf 'stderr-proof\\n' >&2; (exit 7)")
	result := waitBackgroundFeedback(t, manager, id, "failed")
	for _, want := range []string{"stdout-proof", "stderr-proof", "[exit code: 7]"} {
		if !strings.Contains(result, want) {
			t.Errorf("background feedback lost %q", want)
		}
	}
	next, err := executeBashOutput(manager, fileToolInput(t, map[string]any{"bash_id": id}))
	if err != nil || !strings.Contains(next, "Status: failed") || !strings.Contains(next, "[exit code: 7]") {
		t.Fatal("reading output erased the terminal state")
	}
	if strings.Contains(next, "stdout-proof") {
		t.Fatal("a repeated read replayed already consumed output")
	}
}

func TestCancelAnActiveBackgroundBash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture checks Unix process group cancellation")
	}
	for _, mode := range []string{"KillShell", "KillAll"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			manager := NewShellManager()
			release := filepath.Join(dir, "release")
			t.Cleanup(func() {
				_ = os.WriteFile(release, nil, 0o600)
				_ = manager.KillAll()
			})
			ctx := WithExecutionCWD(WithShellManager(t.Context(), manager), dir)
			started, err := BashTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{
				"command":           "(end=$((SECONDS+8)); : > active; while [ ! -f release ] && [ \"$SECONDS\" -lt \"$end\" ]; do sleep 0.02; done) & printf '%s' \"$!\" > child.pid; wait",
				"timeout":           5000,
				"run_in_background": true,
			}))
			if err != nil {
				t.Fatal(err)
			}
			_, tail, ok := strings.Cut(started, "Shell ID: ")
			if !ok {
				t.Fatal("background start omitted its handle")
			}
			id, _, _ := strings.Cut(tail, "\n")
			shell, err := manager.GetOrCreate(id)
			if err != nil {
				t.Fatal(err)
			}
			value, ok := backgroundShells.Load(id)
			if !ok {
				t.Fatal("missing background result record")
			}
			entry := value.(*backgroundShellEntry)
			ready := filepath.Join(dir, "active")
			deadline := time.NewTimer(2 * time.Second)
			defer deadline.Stop()
			poll := time.NewTicker(5 * time.Millisecond)
			defer poll.Stop()
			var childPID int
			for {
				if _, err := os.Stat(ready); err == nil {
					rawPID, _ := os.ReadFile(filepath.Join(dir, "child.pid"))
					childPID, _ = strconv.Atoi(string(rawPID))
					if childPID > 1 {
						break
					}
				}
				select {
				case <-poll.C:
				case <-deadline.C:
					t.Fatal("child process never reached its gate")
				}
			}
			input := fileToolInput(t, map[string]any{"shell_id": id})
			done := make(chan error, 1)
			go func() {
				if mode == "KillAll" {
					done <- manager.KillAll()
					return
				}
				result, err := KillShellTool().ExecuteCtx(ctx, input)
				if err == nil && !strings.Contains(result, "terminated successfully") {
					err = errors.New(result)
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				_ = os.WriteFile(release, nil, 0o600)
				<-done
				t.Fatal("cancellation waited for the active command")
			}
			select {
			case <-entry.done:
				if entry.status != "canceled" || !strings.Contains(entry.outcome, "[canceled]") {
					t.Fatalf("unexpected interrupted outcome: %s %s", entry.status, entry.outcome)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("canceled command did not publish an outcome")
			}
			if shell.cmd.ProcessState == nil || len(manager.ListShells()) != 0 {
				t.Fatal("cancellation did not reap the shell generation")
			}
			deadline.Reset(2 * time.Second)
			for {
				// An orphan can remain a zombie until init reaps it. It can
				// no longer execute shell code once the process exits.
				state, err := exec.Command("ps", "-p", strconv.Itoa(childPID), "-o", "stat=").Output()
				var exitErr *exec.ExitError
				if (errors.As(err, &exitErr) && exitErr.ExitCode() == 1) ||
					(err == nil && strings.HasPrefix(strings.TrimSpace(string(state)), "Z")) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-poll.C:
				case <-deadline.C:
					t.Fatal("background child stayed alive after cancellation")
				}
			}
		})
	}
}

func TestCanceledBackgroundLaunchDoesNotReplaceShell(t *testing.T) {
	dir := t.TempDir()
	manager := NewShellManager()
	t.Cleanup(func() { _ = manager.KillAll() })
	shell, err := manager.GetOrCreateAt("background-fixture", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.KillAll(); err != nil {
		t.Fatal(err)
	}
	result, err := manager.executeBackground(t.Context(), shell, ": > started", time.Second)
	if err != nil || result == nil || !result.Canceled || len(manager.ListShells()) != 0 {
		t.Fatalf("detached execution did not stay canceled: %+v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "started")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled background command reached the workspace")
	}
}

func TestBackgroundBashOwnerFilterAndCleanup(t *testing.T) {
	manager, id := backgroundFeedbackFixture(t, "printf 'keep-proof\\nskip-proof\\n'; (exit 9)")
	other := NewShellManager()
	t.Cleanup(func() { _ = other.KillAll() })
	input := fileToolInput(t, map[string]any{"bash_id": id})
	if result, err := executeBashOutput(other, input); err != nil || !strings.Contains(result, "No background shell found") || strings.Contains(result, "keep-proof") {
		t.Fatal("another manager accessed background results")
	}
	if _, err := executeBashOutput(manager, fileToolInput(t, map[string]any{"bash_id": id, "filter": "["})); err == nil {
		t.Fatal("invalid filter was accepted")
	}
	// Invalid/foreign reads cannot consume the owner's pending output. Wait
	// using the completion record, without draining it through BashOutput.
	entryValue, _ := backgroundShells.Load(id)
	entry := entryValue.(*backgroundShellEntry)
	select {
	case <-entry.done:
	case <-time.After(3 * time.Second):
		t.Fatal("background result was not published")
	}
	result, err := executeBashOutput(manager, fileToolInput(t, map[string]any{"bash_id": id, "filter": "^keep-proof$"}))
	if err != nil || !strings.Contains(result, "keep-proof") || strings.Contains(result, "skip-proof") || !strings.Contains(result, "[exit code: 9]") {
		t.Fatal("filter lost pending output or concealed terminal facts")
	}
	if err := manager.KillAll(); err != nil {
		t.Fatal(err)
	}
	if _, exists := backgroundShells.Load(id); exists {
		t.Fatal("closing the owner retained its background output")
	}
}

func TestBackgroundBashTerminalSnapshotsAreBounded(t *testing.T) {
	for _, tc := range []struct {
		name, status, marker string
		result               *ShellResult
		err                  error
	}{
		{name: "completed", status: "completed", result: &ShellResult{}},
		{name: "failed", status: "failed", marker: "[exit code: 7]", result: &ShellResult{ExitCode: 7}},
		{name: "timeout", status: "timed_out", marker: "[timeout]", result: &ShellResult{TimedOut: true}},
		{name: "cancelled", status: "canceled", marker: "[canceled]", result: &ShellResult{Canceled: true}},
		{name: "execution-error", status: "failed", marker: "[execution failed]", err: errors.New("execution unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewShellManager()
			id := "test-bg-" + tc.name
			entry := &backgroundShellEntry{owner: manager.backgroundOwner, status: "running"}
			backgroundShells.Store(id, entry)
			t.Cleanup(func() { backgroundShells.Delete(id) })
			input := fileToolInput(t, map[string]any{"bash_id": id})
			running, err := executeBashOutput(manager, input)
			if err != nil || !strings.Contains(running, "Status: running") || strings.Contains(running, "Status: completed") {
				t.Fatal("pending work was reported as completed")
			}
			if tc.result != nil {
				tc.result.Stdout = "start-proof" + strings.Repeat("界", 20000) + "end-proof"
			}
			entry.finish("command", tc.result, tc.err)
			result, err := executeBashOutput(manager, input)
			if err != nil || !strings.Contains(result, "Status: "+tc.status) || !strings.Contains(result, tc.marker) || len(result) > bashMaxOutputDefault {
				t.Fatal("background terminal snapshot lost status or exceeded its preview budget")
			}
			if tc.result != nil && (!strings.Contains(result, "start-proof") || !strings.Contains(result, "end-proof")) {
				t.Fatal("background preview lost stream ends")
			}
		})
	}
}

func TestBackgroundKillDoesNotDeleteAnotherOwnersResult(t *testing.T) {
	manager, id := backgroundFeedbackFixture(t, "printf 'owner-proof\\n'")
	other := NewShellManager()
	t.Cleanup(func() { _ = other.KillAll() })
	if _, err := other.ExecuteAt(t.Context(), id, t.TempDir(), ":", time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := executeKillShell(other, fileToolInput(t, map[string]any{"shell_id": id})); err != nil {
		t.Fatal(err)
	}
	if _, ok := backgroundShells.Load(id); !ok {
		t.Fatal("killing a same-ID shell in another manager erased the owner's result")
	}
	result := waitBackgroundFeedback(t, manager, id, "completed")
	if !strings.Contains(result, "owner-proof") {
		t.Fatal("owner output was lost after another manager's KillShell")
	}
}
