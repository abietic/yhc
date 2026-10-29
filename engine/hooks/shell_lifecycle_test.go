package hooks

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestExecuteShellHookTimeoutIsBoundedAndObservable(t *testing.T) {
	started := time.Now()
	result, err := ExecuteShellHook(context.Background(), &ShellHook{
		Command: "sleep 5",
		Timeout: 50 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatalf("ExecuteShellHook returned error: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("timed-out hook returned after %s", elapsed)
	}
	if !result.TimedOut || result.Cancelled {
		t.Fatalf("timeout flags = timed_out:%t cancelled:%t", result.TimedOut, result.Cancelled)
	}
	if result.ExitCode != 143 {
		t.Fatalf("ExitCode = %d, want timeout status 143", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "Hook timed out after 50ms") {
		t.Fatalf("Stderr = %q, want timeout detail", result.Stderr)
	}
}

func TestExecuteShellHookParentCancellationIsNonTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	result, err := ExecuteShellHook(ctx, &ShellHook{
		Command: "sleep 5",
		Timeout: 5 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("ExecuteShellHook returned error: %v", err)
	}
	if result.TimedOut || !result.Cancelled {
		t.Fatalf("cancellation flags = timed_out:%t cancelled:%t", result.TimedOut, result.Cancelled)
	}
	if result.ExitCode != 137 {
		t.Fatalf("ExitCode = %d, want cancellation status 137", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "Hook cancelled") {
		t.Fatalf("Stderr = %q, want cancellation detail", result.Stderr)
	}
}

func TestExecuteShellHookPreCancelledDoesNotStart(t *testing.T) {
	marker := fmt.Sprintf("%s/not-started", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := ExecuteShellHook(ctx, &ShellHook{
		Command: fmt.Sprintf("printf started > %s", shellQuote(marker)),
		Timeout: time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("ExecuteShellHook returned error: %v", err)
	}
	if result.TimedOut || !result.Cancelled || result.ExitCode != 137 {
		t.Fatalf("pre-cancelled result = %#v", result)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("pre-cancelled hook started and wrote marker: %v", err)
	}
}

func TestExecuteShellHookEscalatesAndStopsDescendantSideEffects(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signal escalation contract")
	}

	heartbeat := fmt.Sprintf("%s/heartbeat", t.TempDir())
	command := fmt.Sprintf("(trap '' TERM; while :; do printf x >> %s; sleep 0.02; done) & wait", shellQuote(heartbeat))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type outcome struct {
		result *ShellHookResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		defer close(done)
		result, err := ExecuteShellHook(ctx, &ShellHook{
			Command: command,
			Timeout: 10 * time.Second,
		}, nil)
		done <- outcome{result, err}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// The descendant must have installed its TERM trap before termination.
	// A nonempty heartbeat proves that boundary; startup speed is not the oracle.
	startup := time.NewTimer(5 * time.Second)
	defer startup.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
waitForDescendant:
	for {
		select {
		case got := <-done:
			t.Fatalf("hook returned before descendant readiness: result=%#v error=%v", got.result, got.err)
		case <-startup.C:
			t.Fatal("descendant did not become ready within the fixture startup budget")
		case <-ticker.C:
			data, err := os.ReadFile(heartbeat)
			if err == nil && len(data) > 0 {
				break waitForDescendant
			}
			if err != nil && !os.IsNotExist(err) {
				t.Fatalf("observe descendant readiness: %v", err)
			}
		}
	}
	cancel()
	got := <-done
	result, err := got.result, got.err
	if err != nil {
		t.Fatalf("ExecuteShellHook returned error: %v", err)
	}
	if result.TimedOut || !result.Cancelled || !result.TerminationEscalated {
		t.Fatalf("cancellation result = %#v, want forced tree termination", result)
	}

	before, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatalf("read heartbeat after hook return: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	after, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatalf("read heartbeat after settle: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("descendant kept writing after hook return: bytes %d -> %d", len(before), len(after))
	}
}
