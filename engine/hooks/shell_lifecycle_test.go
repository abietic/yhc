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
	result, err := ExecuteShellHook(context.Background(), &ShellHook{
		Command: command,
		Timeout: 80 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatalf("ExecuteShellHook returned error: %v", err)
	}
	if !result.TimedOut || !result.TerminationEscalated {
		t.Fatalf("timeout result = %#v, want forced tree termination", result)
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
