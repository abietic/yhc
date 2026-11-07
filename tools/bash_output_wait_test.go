package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func boundedWaitFixture(t *testing.T) (*ShellManager, string, *backgroundShellEntry) {
	t.Helper()
	manager := NewShellManager()
	id := t.Name()
	entry := &backgroundShellEntry{owner: manager.backgroundOwner, status: "running", done: make(chan struct{})}
	backgroundShells.Store(id, entry)
	t.Cleanup(func() { backgroundShells.Delete(id) })
	return manager, id, entry
}

func TestBashOutputWaitReturnsCompletedFeedback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager := NewShellManager()
		id := "test-bounded-wait-complete"
		entry := &backgroundShellEntry{owner: manager.backgroundOwner, status: "running", done: make(chan struct{})}
		backgroundShells.Store(id, entry)
		defer backgroundShells.Delete(id)
		go func() {
			time.Sleep(time.Second)
			entry.finish("fixture", &ShellResult{Stdout: "completion-proof", ExitCode: 7}, nil)
		}()
		started := time.Now()
		result, err := BashOutputTool().ExecuteCtx(WithShellManager(t.Context(), manager), fileToolInput(t, map[string]any{
			"bash_id": id, "wait": true, "timeout_ms": 5000,
		}))
		elapsed := time.Since(started)
		<-entry.done // Complete the fixture even when the implementation returns too early.
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Status: failed", "completion-proof", "[exit code: 7]"} {
			if !strings.Contains(result, want) {
				t.Errorf("wait returned without %q: %s", want, result)
			}
		}
		if elapsed != time.Second {
			t.Errorf("wait duration = %v; want completion at 1s", elapsed)
		}
	})
}

func TestBashOutputWaitExpiryDoesNotStopOrConsumeCommand(t *testing.T) {
	for _, timeoutMs := range []int{0, 50} {
		t.Run((time.Duration(timeoutMs) * time.Millisecond).String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				manager, id, entry := boundedWaitFixture(t)
				params := map[string]any{"bash_id": id, "wait": true}
				duration := 30 * time.Second
				if timeoutMs != 0 {
					params["timeout_ms"] = timeoutMs
					duration = time.Duration(timeoutMs) * time.Millisecond
				}
				started := time.Now()
				result, err := executeBashOutput(manager, fileToolInput(t, params))
				if err != nil || !strings.Contains(result, "Status: running") || !strings.Contains(result, "wait timed out; command continues") || time.Since(started) != duration {
					t.Fatalf("wait expiry changed command state or duration: %s, %v", result, err)
				}
				select {
				case <-entry.done:
					t.Fatal("wait timeout completed the command")
				default:
				}
				entry.finish("fixture", &ShellResult{Stdout: "later-proof"}, nil)
				input := fileToolInput(t, map[string]any{"bash_id": id})
				result, err = executeBashOutput(manager, input)
				if err != nil || !strings.Contains(result, "later-proof") || !strings.Contains(result, "Status: completed") || strings.Contains(result, "wait timed out") {
					t.Fatalf("late completion was lost: %s, %v", result, err)
				}
				next, err := executeBashOutput(manager, input)
				if err != nil || strings.Contains(next, "later-proof") || !strings.Contains(next, "Status: completed") {
					t.Fatalf("completion was consumed more than once: %s, %v", next, err)
				}
			})
		})
	}
}

func TestBashOutputWaitCancellationPreservesFeedback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager, id, entry := boundedWaitFixture(t)
		ctx, cancel := context.WithCancel(WithShellManager(t.Context(), manager))
		defer cancel()
		returned := make(chan error, 1)
		go func() {
			_, err := BashOutputTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"bash_id": id, "wait": true}))
			returned <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-returned; !errors.Is(err, context.Canceled) {
			t.Fatalf("wait cancellation = %v", err)
		}
		entry.finish("fixture", &ShellResult{Stdout: "pending-proof"}, nil)
		// Even an already-completed record must not be drained by a cancelled reader.
		if _, err := BashOutputTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"bash_id": id})); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled immediate read = %v", err)
		}
		result, err := executeBashOutput(manager, fileToolInput(t, map[string]any{"bash_id": id}))
		if err != nil || !strings.Contains(result, "pending-proof") {
			t.Fatalf("cancelled reader consumed output: %s, %v", result, err)
		}
	})
}

func TestBashOutputWaitOwnerAndReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager, id, entry := boundedWaitFixture(t)
		input := fileToolInput(t, map[string]any{"bash_id": id, "wait": true})
		started := time.Now()
		result, err := executeBashOutput(NewShellManager(), input)
		if err != nil || !strings.Contains(result, "No background shell found") || time.Since(started) != 0 {
			t.Fatalf("foreign owner waited for or accessed a shell: %s, %v", result, err)
		}
		returned := make(chan string, 1)
		go func() {
			result, err := executeBashOutput(manager, input)
			if err != nil {
				t.Errorf("wait failed: %v", err)
			}
			returned <- result
		}()
		synctest.Wait()
		replacement := &backgroundShellEntry{owner: manager.backgroundOwner, status: "completed", output: "replacement-proof"}
		backgroundShells.Store(id, replacement)
		entry.finish("fixture", &ShellResult{Stdout: "stale-proof"}, nil)
		if result := <-returned; !strings.Contains(result, "No background shell found") || strings.Contains(result, "stale-proof") {
			t.Fatalf("wait retained authority over a replaced record: %s", result)
		}
		result, err = executeBashOutput(manager, fileToolInput(t, map[string]any{"bash_id": id}))
		if err != nil || !strings.Contains(result, "replacement-proof") {
			t.Fatalf("stale wait consumed replacement: %s, %v", result, err)
		}
	})
}

func TestBashOutputConcurrentWaitersConsumeOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		manager, id, entry := boundedWaitFixture(t)
		input := fileToolInput(t, map[string]any{"bash_id": id, "wait": true})
		returned := make(chan string, 2)
		for range 2 {
			go func() {
				result, err := executeBashOutput(manager, input)
				if err != nil {
					t.Errorf("wait failed: %v", err)
				}
				returned <- result
			}()
		}
		synctest.Wait()
		entry.finish("fixture", &ShellResult{Stdout: "one-consumer-proof", ExitCode: 8}, nil)
		consumers := 0
		for range 2 {
			result := <-returned
			if !strings.Contains(result, "Status: failed") || !strings.Contains(result, "[exit code: 8]") {
				t.Errorf("waiter lost terminal facts: %s", result)
			}
			if strings.Contains(result, "one-consumer-proof") {
				consumers++
			}
		}
		if consumers != 1 {
			t.Errorf("output consumers = %d; want 1", consumers)
		}
	})
}

func TestBashOutputWaitInvalidParametersPreserveFeedback(t *testing.T) {
	manager, id, entry := boundedWaitFixture(t)
	entry.finish("fixture", &ShellResult{Stdout: "validated-proof"}, nil)
	for _, params := range []map[string]any{
		{"wait": true, "timeout_ms": 0},
		{"wait": true, "timeout_ms": -1},
		{"wait": true, "timeout_ms": 120001},
		{"timeout_ms": 50},
		{"wait": "true"},
		{"wait": true, "timeout_ms": 1.5},
		{"wait": true, "filter": "["},
	} {
		params["bash_id"] = id
		if _, err := executeBashOutput(manager, fileToolInput(t, params)); err == nil {
			t.Errorf("accepted invalid parameters: %v", params)
		}
	}
	result, err := executeBashOutput(manager, fileToolInput(t, map[string]any{"bash_id": id, "wait": true, "timeout_ms": 120000, "filter": "^validated-proof$"}))
	if err != nil || !strings.Contains(result, "validated-proof") || !strings.Contains(result, "Status: completed") {
		t.Fatalf("invalid reads consumed output: %s, %v", result, err)
	}
}

func TestBashOutputWaitRejectsUntrackedCompletion(t *testing.T) {
	manager, id, entry := boundedWaitFixture(t)
	entry.done = nil
	if _, err := executeBashOutput(manager, fileToolInput(t, map[string]any{"bash_id": id, "wait": true})); err == nil || !strings.Contains(err.Error(), "completion tracking unavailable") {
		t.Fatalf("untracked completion was allowed to wait: %v", err)
	}
	result, err := executeBashOutput(manager, fileToolInput(t, map[string]any{"bash_id": id}))
	if err != nil || !strings.Contains(result, "Status: running") {
		t.Fatalf("default immediate read changed: %s, %v", result, err)
	}
}

func TestBashOutputWaitLeavesManagedCommandAlive(t *testing.T) {
	manager := NewShellManager()
	t.Cleanup(func() { _ = manager.KillAll() })
	dir := t.TempDir()
	// The independent file gate keeps the real process running until after
	// both readers have returned, without relying on process-start timing.
	started, err := runBackgroundCommand(t.Context(), manager, dir,
		"while [ ! -f release ]; do sleep 0.02; done; printf 'survived-wait-proof\\n'; (exit 7)", "wait lifetime fixture")
	if err != nil {
		t.Fatal(err)
	}
	_, tail, ok := strings.Cut(started, "Shell ID: ")
	if !ok {
		t.Fatal("background command did not return a handle")
	}
	id := strings.SplitN(tail, "\n", 2)[0]
	result, err := executeBashOutput(manager, fileToolInput(t, map[string]any{"bash_id": id, "wait": true, "timeout_ms": 10}))
	if err != nil || !strings.Contains(result, "Status: running") {
		t.Fatalf("bounded wait stopped a gated command: %s, %v", result, err)
	}
	ctx, cancel := context.WithTimeout(WithShellManager(t.Context(), manager), 10*time.Millisecond)
	defer cancel()
	_, err = BashOutputTool().ExecuteCtx(ctx, fileToolInput(t, map[string]any{"bash_id": id, "wait": true}))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller deadline = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = executeBashOutput(manager, fileToolInput(t, map[string]any{"bash_id": id, "wait": true, "timeout_ms": 5000}))
	if err != nil || !strings.Contains(result, "survived-wait-proof") || !strings.Contains(result, "[exit code: 7]") {
		t.Fatalf("waiting changed the managed process lifetime: %s, %v", result, err)
	}
}
