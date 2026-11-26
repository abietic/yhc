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
	"unicode/utf8"
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

func TestBackgroundToolsReadOutputBeforeCommandExits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture requires Bash")
	}
	const signal = "fixture-ready"
	const command = `sleep 0.6
printf 'fixture-ready\n'
while [ ! -f release ]; do sleep 0.01; done
(exit 7)`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "check.sh"), []byte(command), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewShellManager()
	t.Cleanup(func() { _ = manager.KillAll() })
	ctx := WithShellManager(WithExecutionCWD(t.Context(), dir), manager)
	observed := make(chan struct{}, 1)
	ctx = WithProgressFn(ctx, func(progress ToolProgressEvent) {
		if progress.ToolName == "Bash" && strings.Contains(progress.Content, signal) {
			select {
			case observed <- struct{}{}:
			default:
			}
		}
	})
	launch, err := BashTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{
		"command": "bash check.sh", "run_in_background": true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, tail, found := strings.Cut(launch, "Shell ID: ")
	id, _, _ := strings.Cut(tail, "\n")
	if !found || id == "" {
		t.Fatal("background command did not supply a handle")
	}
	select {
	case <-observed:
	case <-time.After(3 * time.Second):
		t.Fatal("background reader did not release the complete short readiness line")
	}
	monitor, err := MonitorTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"process_id": id, "tail_lines": 1}))
	if err != nil || !strings.Contains(monitor, "Status: running") || !strings.Contains(monitor, signal) {
		t.Fatalf("running Monitor missed the readiness line: %q, %v", monitor, err)
	}
	read := func(params map[string]any) string {
		t.Helper()
		params["bash_id"] = id
		output, err := BashOutputTool().ExecuteCtx(ctx, fileToolInput(t, params))
		if err != nil {
			t.Fatal(err)
		}
		return output
	}
	peek := read(map[string]any{"wait": true, "timeout_ms": 10, "filter": "fixture-ready"})
	if !strings.Contains(peek, "Status: running") || !strings.Contains(peek, "wait timed out") || !strings.Contains(peek, signal) {
		t.Fatalf("BashOutput wait expiry missed the live preview: %q", peek)
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	result := read(map[string]any{"wait": true})
	if !strings.Contains(result, "Status: failed") || !strings.Contains(result, "[exit code: 7]") || !strings.Contains(result, signal) {
		t.Fatalf("live query consumed the terminal result: %q", result)
	}
	again := read(map[string]any{})
	if !strings.Contains(again, "[exit code: 7]") || strings.Contains(again, signal) {
		t.Fatalf("terminal result did not retain its existing one-read behavior: %q", again)
	}
}

func TestBackgroundBashShowsProgressWithoutNewline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture requires Bash")
	}
	dir := t.TempDir()
	script := `printf 'items processed: 17\r'
: > emitted
while [ ! -f release ]; do sleep 0.01; done
printf 'items processed: 42'
(exit 7)`
	if err := os.WriteFile(filepath.Join(dir, "progress.sh"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewShellManager()
	t.Cleanup(func() { _ = manager.KillAll() })
	ctx := WithShellManager(WithExecutionCWD(t.Context(), dir), manager)
	launch, err := BashTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{
		"command": "bash progress.sh", "run_in_background": true,
	}))
	_, tail, found := strings.Cut(launch, "Shell ID: ")
	id, _, _ := strings.Cut(tail, "\n")
	if err != nil || !found || id == "" {
		t.Fatalf("background launch failed: %q, %v", launch, err)
	}
	const progress = "items processed: 17"
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	var snapshot string
	for {
		snapshot, err = MonitorTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{
			"process_id": id, "tail_lines": 1,
		}))
		if err != nil || !strings.Contains(snapshot, "Status: running") {
			t.Fatalf("blocked command was not running: %q, %v", snapshot, err)
		}
		if strings.HasSuffix(snapshot, progress+"\n") {
			break
		}
		select {
		case <-deadline.C:
			if _, err := os.Stat(filepath.Join(dir, "emitted")); err != nil {
				t.Fatalf("fixture never reached its independent emission marker: %v", err)
			}
			t.Fatalf("running Monitor hid already emitted progress without newline: %q", snapshot)
		case <-tick.C:
		}
	}
	peek, err := BashOutputTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"bash_id": id}))
	if err != nil || !strings.Contains(peek, "Status: running") || !strings.Contains(peek, progress) {
		t.Fatalf("running BashOutput missed the same progress: %q, %v", peek, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	final, err := BashOutputTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{
		"bash_id": id, "wait": true,
	}))
	if err != nil || !strings.Contains(final, "Status: failed") || !strings.Contains(final, "[exit code: 7]") ||
		!strings.Contains(final, progress+"\ritems processed: 42") || strings.Contains(final, "___SENTINEL_") {
		t.Fatalf("background terminal output or exit was damaged: %q, %v", final, err)
	}
}

func TestRunningBashPreviewsWaitForCompleteUTF8(t *testing.T) {
	const id = "bg-utf8-preview-fixture"
	const prefix = "progress: "
	for _, character := range []string{"é", "界", "🙂"} {
		t.Run(character, func(t *testing.T) {
			manager := NewShellManager()
			entry := &backgroundShellEntry{ShellID: id, owner: manager.backgroundOwner, status: "running", StartedAt: time.Now()}
			backgroundShells.Store(id, entry)
			t.Cleanup(func() { backgroundShells.CompareAndDelete(id, entry) })
			ctx := WithShellManager(t.Context(), manager)
			bashInput := fileToolInput(t, map[string]any{"bash_id": id})
			monitorInput := fileToolInput(t, map[string]any{"process_id": id})
			check := func(want string) {
				t.Helper()
				bash, err := BashOutputTool().ExecuteCtx(ctx, bashInput)
				if err != nil || !utf8.ValidString(bash) || !strings.HasSuffix(bash, want+"\n") {
					t.Errorf("BashOutput should show %q: %q, %v", want, bash, err)
				}
				monitor, err := MonitorTool().ExecuteCtx(ctx, monitorInput)
				if err != nil || !utf8.ValidString(monitor) || !strings.HasSuffix(monitor, want+"\n") {
					t.Errorf("Monitor should show %q: %q, %v", want, monitor, err)
				}
			}
			entry.observeOutput(prefix)
			for i := range len(character) {
				entry.observeOutput(character[i : i+1])
				if i+1 < len(character) {
					check(prefix)
				}
			}
			check(prefix + character)
		})
	}
}

func TestMonitorReadsOwnedBashWithoutConsumingOutput(t *testing.T) {
	manager, id := backgroundFeedbackFixture(t, "printf 'first-monitor-line\\nlast-monitor-line\\n'; (exit 7)")
	other := NewShellManager()
	t.Cleanup(func() { _ = other.KillAll() })
	entry, ok := backgroundShells.Load(id)
	if !ok {
		t.Fatal("background Bash did not publish its ID")
	}
	select {
	case <-entry.(*backgroundShellEntry).done:
	case <-time.After(3 * time.Second):
		t.Fatal("background Bash did not publish a terminal result")
	}
	monitor := MonitorTool()
	input := fileToolInput(t, map[string]any{"process_id": id, "tail_lines": 1})
	call := func(owner *ShellManager) (string, error) {
		if monitor.ExecuteCtx == nil {
			return monitor.Execute(input)
		}
		return monitor.ExecuteCtx(WithShellManager(t.Context(), owner), input)
	}
	foreign, err := call(other)
	if err != nil || !strings.Contains(foreign, "not found") || strings.Contains(foreign, "last-monitor-line") {
		t.Fatalf("foreign monitor = %q, %v", foreign, err)
	}
	got, err := call(manager)
	_, tail, _ := strings.Cut(got, "\n--- Recent Output ---\n")
	if err != nil || !strings.Contains(got, "Status: failed") || !strings.Contains(got, "[exit code: 7]") ||
		!strings.Contains(tail, "last-monitor-line") || strings.Contains(tail, "first-monitor-line") {
		t.Fatalf("owner monitor = %q, %v", got, err)
	}
	output, err := executeBashOutput(manager, fileToolInput(t, map[string]any{"bash_id": id}))
	if err != nil || !strings.Contains(output, "first-monitor-line") || !strings.Contains(output, "last-monitor-line") {
		t.Fatalf("Monitor drained BashOutput: %q, %v", output, err)
	}
	consumed, err := call(manager)
	if err != nil || !strings.Contains(consumed, "[exit code: 7]") || strings.Contains(consumed, "--- Recent Output ---") {
		t.Fatalf("Monitor after BashOutput = %q, %v", consumed, err)
	}

	legacyID := t.Name()
	RegisterBackgroundProcess(&BackgroundProcess{ID: legacyID})
	AppendProcessOutput(legacyID, "legacy-output-proof")
	t.Cleanup(func() {
		bgMu.Lock()
		delete(backgroundProcesses, legacyID)
		bgMu.Unlock()
	})
	legacyInput := fileToolInput(t, map[string]any{"process_id": legacyID})
	if got, err := monitor.Execute(legacyInput); err != nil || !strings.Contains(got, "legacy-output-proof") {
		t.Fatalf("unscoped registered process = %q, %v", got, err)
	}
	if got, err := monitor.ExecuteCtx(WithShellManager(t.Context(), manager), legacyInput); err != nil ||
		!strings.Contains(got, "not found") || strings.Contains(got, "legacy-output-proof") {
		t.Fatalf("session-bound registered process = %q, %v", got, err)
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
	result, err := manager.executeBackground(t.Context(), shell, ": > started", time.Second, nil)
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
