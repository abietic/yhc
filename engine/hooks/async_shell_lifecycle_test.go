package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/abietic/yhc/engine/containment"
)

func TestExecutorOwnsAsyncShellResultUntilExactlyOnceDrain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell command assertions require a POSIX shell")
	}
	executor := NewExecutor()
	policy := containment.DisabledCompatibilitySnapshot(t.TempDir(), containment.EntrypointHeadless)
	if err := executor.BindExecutionPolicy(policy); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = executor.ShutdownAsyncShellHooks(ctx)
	})
	updates := make(chan AsyncShellHookCompletion, 4)
	executor.SetAsyncShellCompletionHandler(func(update AsyncShellHookCompletion) { updates <- update })
	executor.RegisterShellHooks(&ShellHookConfig{PreToolHooks: []ShellHook{{
		Command: "printf 'async result'", ToolPattern: "Bash", Async: true,
		StatusMessage: "Checking asynchronously",
	}}})

	ctx := WithHookTurnID(context.Background(), "turn-async")
	started := time.Now()
	result := executor.ExecutePreTool(ctx, "Bash", "tool-1", map[string]any{"command": "pwd"})
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("async hook blocked caller for %v", elapsed)
	}
	if len(result.Attachments) != 0 || result.DenyReason != "" {
		t.Fatalf("async hook changed launching result: %#v", result)
	}

	running := waitAsyncShellUpdate(t, updates, "running")
	completed := waitAsyncShellUpdate(t, updates, "completed")
	if running.ID == "" || completed.ID != running.ID || completed.TurnID != "turn-async" {
		t.Fatalf("completion identity mismatch: running=%#v completed=%#v", running, completed)
	}
	if running.ExecutionPolicyDigest != policy.Digest() ||
		completed.ExecutionPolicyDigest != policy.Digest() ||
		completed.Result.ExecutionPolicyDigest != policy.Digest() {
		t.Fatalf("async policy capture running=%q completed=%q result=%q want=%q",
			running.ExecutionPolicyDigest,
			completed.ExecutionPolicyDigest,
			completed.Result.ExecutionPolicyDigest,
			policy.Digest(),
		)
	}
	if completed.Outcome() != "completed" || strings.TrimSpace(completed.Result.Stdout) != "async result" {
		t.Fatalf("unexpected completion: %#v", completed)
	}

	messages := executor.DrainAsyncShellMessages()
	if len(messages) != 1 || !strings.Contains(messages[0].Content, "async result") {
		t.Fatalf("drained messages = %#v", messages)
	}
	if second := executor.DrainAsyncShellMessages(); len(second) != 0 {
		t.Fatalf("completion delivered more than once: %#v", second)
	}
}

func TestLoadShellHooksAsyncRewakeImpliesAsync(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	data := []byte(`{"PostToolUse":[{"matcher":"Read","hooks":[{"command":"check-policy","asyncRewake":true}]}]}`)
	if err := os.WriteFile(filepath.Join(configDir, "hooks.json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	config, err := LoadShellHooks(dir)
	if err != nil {
		t.Fatalf("LoadShellHooks: %v", err)
	}
	if len(config.PostToolHooks) != 1 || !config.PostToolHooks[0].Async || !config.PostToolHooks[0].AsyncRewake {
		t.Fatalf("async rewake config = %#v", config.PostToolHooks)
	}
}

func TestP420ExecutorPinsSynchronousShellPolicy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell command assertions require a POSIX shell")
	}
	executor := NewExecutor()
	policy := containment.DisabledCompatibilitySnapshot(t.TempDir(), containment.EntrypointACP)
	if err := executor.BindExecutionPolicy(policy); err != nil {
		t.Fatal(err)
	}
	results, err := RunPreToolHooks(
		executor.asyncShellContext(context.Background()),
		&ShellHookConfig{PreToolHooks: []ShellHook{{
			Command: "printf sync", ToolPattern: "Bash",
		}}},
		"Bash",
		map[string]any{"command": "pwd"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ExecutionPolicyDigest != policy.Digest() {
		t.Fatalf("synchronous hook results = %#v, want policy %q", results, policy.Digest())
	}
}

func TestP420ExecutorRejectsSharedRootReplacementAndMismatchedDispatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell command assertions require a POSIX shell")
	}
	executor := NewExecutor()
	first := containment.DisabledCompatibilitySnapshot(t.TempDir(), containment.EntrypointHeadless)
	second := containment.DisabledCompatibilitySnapshot(t.TempDir(), containment.EntrypointACP)
	if err := executor.BindExecutionPolicy(first); err != nil {
		t.Fatal(err)
	}
	if err := executor.BindExecutionPolicy(second); err == nil {
		t.Fatal("shared unstarted executor accepted a second root policy")
	}

	wrote := filepath.Join(t.TempDir(), "sync-wrote")
	results, err := RunPreToolHooks(
		executor.asyncShellContext(containment.WithSnapshot(context.Background(), second)),
		&ShellHookConfig{PreToolHooks: []ShellHook{{
			Command: fmt.Sprintf("printf wrong > %s", shellQuote(wrote)), ToolPattern: "Bash",
		}}},
		"Bash",
		map[string]any{"command": "pwd"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].StartFailed ||
		!strings.Contains(results[0].Stderr, "execution policy mismatch") {
		t.Fatalf("mismatched synchronous hook result = %#v", results)
	}
	if _, err := os.Stat(wrote); !os.IsNotExist(err) {
		t.Fatalf("mismatched synchronous hook spawned: %v", err)
	}

	updates := make(chan AsyncShellHookCompletion, 4)
	executor.SetAsyncShellCompletionHandler(func(update AsyncShellHookCompletion) { updates <- update })
	executor.RegisterShellHooks(&ShellHookConfig{PreToolHooks: []ShellHook{{
		Command: fmt.Sprintf("printf wrong > %s", shellQuote(wrote)), ToolPattern: "Bash", Async: true,
	}}})
	executor.ExecutePreTool(
		containment.WithSnapshot(context.Background(), second),
		"Bash",
		"tool-mismatch",
		map[string]any{"command": "pwd"},
	)
	_ = waitAsyncShellUpdate(t, updates, "running")
	completed := waitAsyncShellUpdate(t, updates, "completed")
	if completed.Outcome() != "failed" || !completed.Result.StartFailed {
		t.Fatalf("mismatched async completion = %#v", completed)
	}
	if _, err := os.Stat(wrote); !os.IsNotExist(err) {
		t.Fatalf("mismatched async hook spawned: %v", err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := executor.ShutdownAsyncShellHooks(shutdownCtx); err != nil {
		t.Fatal(err)
	}
}

func TestBoundAsyncHookOutputPreservesUTF8AndCapsPayload(t *testing.T) {
	value := strings.Repeat("x", maxAsyncHookOutputBytes-1) + "界" + strings.Repeat("z", 32)
	bounded := boundAsyncHookOutput(value)
	if !strings.Contains(bounded, "...[truncated]") {
		t.Fatalf("bounded output was not marked truncated")
	}
	if !utf8.ValidString(bounded) {
		t.Fatal("bounded output is not valid UTF-8")
	}
}

func TestAsyncRegistryTransientFuturesAreRemovedBeforeDone(t *testing.T) {
	registry := NewAsyncRegistry()
	futures := make([]*HookFuture, 0, 128)
	for range 128 {
		future, err := registry.ExecuteAsyncTransient(context.Background(), "long-session", func(context.Context) (any, error) {
			return "done", nil
		})
		if err != nil {
			t.Fatalf("ExecuteAsyncTransient: %v", err)
		}
		futures = append(futures, future)
	}
	for _, future := range futures {
		select {
		case <-future.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("transient future did not complete")
		}
	}
	if active := registry.ActiveCount(); active != 0 {
		t.Fatalf("active transient futures = %d", active)
	}
	if completed := registry.CompletedFutures(); len(completed) != 0 {
		t.Fatalf("registry retained %d transient futures", len(completed))
	}
}

func TestExecutorAsyncRewakeClaimsOnlyExitCodeTwo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell command assertions require a POSIX shell")
	}
	executor := NewExecutor()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = executor.ShutdownAsyncShellHooks(ctx)
	})
	updates := make(chan AsyncShellHookCompletion, 4)
	executor.SetAsyncShellCompletionHandler(func(update AsyncShellHookCompletion) { updates <- update })
	executor.RegisterShellHooks(&ShellHookConfig{PostToolHooks: []ShellHook{{
		Command: "printf 'rewake reason' >&2; exit 2", ToolPattern: "Read",
		AsyncRewake: true,
	}}})

	executor.ExecutePostTool(WithHookTurnID(context.Background(), "turn-rewake"), "Read", "tool-2", nil, "result")
	_ = waitAsyncShellUpdate(t, updates, "running")
	completed := waitAsyncShellUpdate(t, updates, "completed")
	if completed.Result.ExitCode != 2 || !completed.AsyncRewake {
		t.Fatalf("unexpected rewake completion: %#v", completed)
	}
	if messages := executor.DrainAsyncShellMessages(); len(messages) != 0 {
		t.Fatalf("coordinator-owned rewake entered ordinary drain: %#v", messages)
	}
	if !executor.AcknowledgeAsyncShellDelivery(completed.ID) {
		t.Fatal("rewake delivery was not acknowledged")
	}
	if executor.AcknowledgeAsyncShellDelivery(completed.ID) {
		t.Fatal("rewake delivery was acknowledged more than once")
	}
}

func TestExecutorCancelTerminatesOwnedAsyncShellProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process lifecycle assertions require a POSIX shell")
	}
	executor := NewExecutor()
	updates := make(chan AsyncShellHookCompletion, 4)
	executor.SetAsyncShellCompletionHandler(func(update AsyncShellHookCompletion) { updates <- update })
	startedFile := filepath.Join(t.TempDir(), "started")
	executor.RegisterShellHooks(&ShellHookConfig{UserPromptHooks: []ShellHook{{
		Command: fmt.Sprintf("printf started > %s; sleep 30", shellQuote(startedFile)),
		Async:   true,
	}}})

	executor.ExecuteUserPromptSubmit(WithHookTurnID(context.Background(), "turn-cancel"), "hello")
	_ = waitAsyncShellUpdate(t, updates, "running")
	waitForFile(t, startedFile)
	executor.CancelAsyncShellHooks()
	completed := waitAsyncShellUpdate(t, updates, "completed")
	if completed.Outcome() != "cancelled" || !completed.Result.Cancelled {
		t.Fatalf("cancelled completion = %#v", completed)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := executor.ShutdownAsyncShellHooks(shutdownCtx); err != nil {
		t.Fatalf("ShutdownAsyncShellHooks: %v", err)
	}
}

func waitAsyncShellUpdate(t *testing.T, updates <-chan AsyncShellHookCompletion, phase string) AsyncShellHookCompletion {
	t.Helper()
	select {
	case update := <-updates:
		if update.Phase != phase {
			t.Fatalf("update phase = %q, want %q (%#v)", update.Phase, phase, update)
		}
		return update
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s async hook update", phase)
		return AsyncShellHookCompletion{}
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
