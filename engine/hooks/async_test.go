package hooks

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TestAsyncRegistryBasicExecution verifies basic async dispatch and await.
// ---------------------------------------------------------------------------

func TestAsyncRegistryBasicExecution(t *testing.T) {
	reg := NewAsyncRegistry()

	// Dispatch a simple hook that returns a value.
	future, err := reg.ExecuteAsync(context.Background(), "test_event", func(ctx context.Context) (any, error) {
		return "hello", nil
	})
	if err != nil {
		t.Fatalf("ExecuteAsync: %v", err)
		return
	}
	if future == nil {
		t.Fatal("expected non-nil future")
		return
	}
	if future.ID == "" {
		t.Fatal("expected non-empty future ID")
	}
	if future.Event != "test_event" {
		t.Fatalf("Event = %q, want %q", future.Event, "test_event")
	}

	// Wait for completion.
	result, err := future.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
		return
	}
	if result == nil {
		t.Fatal("expected non-nil result")
		return
	}
	if result.Output != "hello" {
		t.Fatalf("Output = %v, want %q", result.Output, "hello")
	}
	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
		return
	}
	if result.Duration < 0 {
		t.Fatalf("Duration = %v, want non-negative", result.Duration)
	}
	if future.Status() != HookFutureStatusCompleted {
		t.Fatalf("Status = %v, want %v", future.Status(), HookFutureStatusCompleted)
	}
}

// ---------------------------------------------------------------------------
// TestAsyncRegistryFutureWaitWithContext tests Wait with context cancellation.
// ---------------------------------------------------------------------------

func TestAsyncRegistryFutureWaitWithContext(t *testing.T) {
	reg := NewAsyncRegistry()

	// Dispatch a hook that blocks until cancelled.
	future, err := reg.ExecuteAsync(context.Background(), "slow_hook", func(ctx context.Context) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatalf("ExecuteAsync: %v", err)
		return
	}

	// Wait with a very short timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err = future.Wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait error = %v, want DeadlineExceeded", err)
	}

	// Result should be nil since the hook is still running.
	if future.Result() != nil {
		t.Fatal("expected nil result while hook is running")
		return
	}
	if future.IsDone() {
		t.Fatal("expected IsDone=false while hook is running")
	}

	// Clean up.
	reg.CancelAll()
	_ = reg.Shutdown(context.Background())
}

// ---------------------------------------------------------------------------
// TestAsyncRegistryFutureNonBlockingResult tests Result() non-blocking check.
// ---------------------------------------------------------------------------

func TestAsyncRegistryFutureNonBlockingResult(t *testing.T) {
	reg := NewAsyncRegistry()

	blocker := make(chan struct{})
	future, err := reg.ExecuteAsync(context.Background(), "blocking_hook", func(ctx context.Context) (any, error) {
		<-blocker
		return "done", nil
	})
	if err != nil {
		t.Fatalf("ExecuteAsync: %v", err)
		return
	}

	// Result should be nil before completion.
	if future.Result() != nil {
		t.Fatal("expected nil result before completion")
		return
	}

	// Unblock the hook.
	close(blocker)

	// Wait for completion.
	result, err := future.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
		return
	}
	if result.Output != "done" {
		t.Fatalf("Output = %v, want %q", result.Output, "done")
	}

	// Now Result() should be available.
	if future.Result() == nil {
		t.Fatal("expected non-nil result after completion")
		return
	}
}

// ---------------------------------------------------------------------------
// TestAsyncRegistryDoneChannel tests the Done() channel.
// ---------------------------------------------------------------------------

func TestAsyncRegistryDoneChannel(t *testing.T) {
	reg := NewAsyncRegistry()

	blocker := make(chan struct{})
	future, _ := reg.ExecuteAsync(context.Background(), "done_test", func(ctx context.Context) (any, error) {
		<-blocker
		return nil, nil
	})

	// Done channel should not be closed yet.
	select {
	case <-future.Done():
		t.Fatal("Done channel should not be closed yet")
	default:
	}

	// Unblock.
	close(blocker)

	// Wait on Done channel.
	select {
	case <-future.Done():
		// Success.
	case <-time.After(1 * time.Second):
		t.Fatal("Done channel not closed after hook completed")
	}
}

// ---------------------------------------------------------------------------
// TestAsyncRegistryHookError tests error propagation from hooks.
// ---------------------------------------------------------------------------

func TestAsyncRegistryHookError(t *testing.T) {
	reg := NewAsyncRegistry()

	expectedErr := errors.New("hook failed")
	future, err := reg.ExecuteAsync(context.Background(), "error_hook", func(ctx context.Context) (any, error) {
		return nil, expectedErr
	})
	if err != nil {
		t.Fatalf("ExecuteAsync: %v", err)
		return
	}

	result, err := future.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
		return
	}
	if result.Error == nil {
		t.Fatal("expected non-nil error in result")
		return
	}
	if result.Error.Error() != expectedErr.Error() {
		t.Fatalf("Error = %v, want %v", result.Error, expectedErr)
	}
	if future.Status() != HookFutureStatusFailed {
		t.Fatalf("Status = %v, want %v", future.Status(), HookFutureStatusFailed)
	}
}

// ---------------------------------------------------------------------------
// TestAsyncRegistryPanicRecovery tests that panics in hooks are recovered.
// ---------------------------------------------------------------------------

func TestAsyncRegistryPanicRecovery(t *testing.T) {
	reg := NewAsyncRegistry()

	future, err := reg.ExecuteAsync(context.Background(), "panic_hook", func(ctx context.Context) (any, error) {
		panic("something went wrong")
	})
	if err != nil {
		t.Fatalf("ExecuteAsync: %v", err)
		return
	}

	result, err := future.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
		return
	}
	if result.Error == nil {
		t.Fatal("expected panic to be recorded as error")
		return
	}
	if result.Error.Error() != "hook panicked: something went wrong" {
		t.Fatalf("Error = %q, want panic message", result.Error.Error())
	}
	if future.Status() != HookFutureStatusFailed {
		t.Fatalf("Status = %v, want %v", future.Status(), HookFutureStatusFailed)
	}
}

// ---------------------------------------------------------------------------
// TestAsyncRegistryCancelAll tests cancelling all in-flight hooks.
// ---------------------------------------------------------------------------

func TestAsyncRegistryCancelAll(t *testing.T) {
	reg := NewAsyncRegistry()

	// Dispatch several hooks that block until cancelled.
	const numHooks = 5
	futures := make([]*HookFuture, numHooks)
	for i := 0; i < numHooks; i++ {
		f, err := reg.ExecuteAsync(context.Background(), "cancel_test", func(ctx context.Context) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
		if err != nil {
			t.Fatalf("ExecuteAsync[%d]: %v", i, err)
			return
		}
		futures[i] = f
	}

	// Verify active count.
	// Give goroutines a moment to start.
	time.Sleep(10 * time.Millisecond)
	if count := reg.ActiveCount(); count != numHooks {
		t.Fatalf("ActiveCount = %d, want %d", count, numHooks)
	}

	// Cancel all.
	reg.CancelAll()

	// All futures should complete.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	for i, f := range futures {
		result, err := f.Wait(ctx)
		if err != nil {
			t.Fatalf("Wait[%d]: %v", i, err)
			return
		}
		if result.Error == nil {
			t.Fatalf("future[%d]: expected error after cancel", i)
			return
		}
		if f.Status() != HookFutureStatusCancelled {
			t.Fatalf("future[%d]: Status = %v, want %v", i, f.Status(), HookFutureStatusCancelled)
		}
	}

	// Active count should be 0.
	if count := reg.ActiveCount(); count != 0 {
		t.Fatalf("ActiveCount after cancel = %d, want 0", count)
	}
}

// ---------------------------------------------------------------------------
// TestAsyncRegistryShutdown tests graceful shutdown.
// ---------------------------------------------------------------------------

func TestAsyncRegistryShutdown(t *testing.T) {
	reg := NewAsyncRegistry()

	// Dispatch hooks that complete quickly.
	for i := 0; i < 3; i++ {
		_, err := reg.ExecuteAsync(context.Background(), "shutdown_test", func(ctx context.Context) (any, error) {
			time.Sleep(10 * time.Millisecond)
			return "ok", nil
		})
		if err != nil {
			t.Fatalf("ExecuteAsync[%d]: %v", i, err)
			return
		}
	}

	// Shutdown with generous timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := reg.Shutdown(ctx)
	if err != nil {
		t.Fatalf("Shutdown: %v", err)
		return
	}

	// All should be completed.
	if count := reg.ActiveCount(); count != 0 {
		t.Fatalf("ActiveCount after shutdown = %d, want 0", count)
	}

	// New dispatches should fail.
	_, err = reg.ExecuteAsync(context.Background(), "after_shutdown", func(ctx context.Context) (any, error) {
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected error dispatching after shutdown")
		return
	}
}

// ---------------------------------------------------------------------------
// TestAsyncRegistryShutdownTimeout tests shutdown with deadline exceeded.
// ---------------------------------------------------------------------------

func TestAsyncRegistryShutdownTimeout(t *testing.T) {
	reg := NewAsyncRegistry()

	// Dispatch a hook that blocks indefinitely (until cancelled).
	_, err := reg.ExecuteAsync(context.Background(), "blocking", func(ctx context.Context) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatalf("ExecuteAsync: %v", err)
		return
	}

	// Shutdown with very short timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err = reg.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v, want DeadlineExceeded", err)
	}

	// Registry should be closed.
	if !reg.IsClosed() {
		t.Fatal("expected registry to be closed after shutdown")
	}
}

// ---------------------------------------------------------------------------
// TestAsyncRegistryCollectAll tests collecting all results.
// ---------------------------------------------------------------------------

func TestAsyncRegistryCollectAll(t *testing.T) {
	reg := NewAsyncRegistry()

	// Dispatch several hooks with different results.
	for i := 0; i < 5; i++ {
		val := i
		_, err := reg.ExecuteAsync(context.Background(), "collect_test", func(ctx context.Context) (any, error) {
			time.Sleep(5 * time.Millisecond)
			return val, nil
		})
		if err != nil {
			t.Fatalf("ExecuteAsync[%d]: %v", i, err)
			return
		}
	}

	// Collect all results.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results := reg.CollectAll(ctx)
	if len(results) != 5 {
		t.Fatalf("CollectAll returned %d results, want 5", len(results))
	}

	// Verify all completed without error.
	for i, r := range results {
		if r.Error != nil {
			t.Fatalf("result[%d]: unexpected error: %v", i, r.Error)
			return
		}
	}
}

// ---------------------------------------------------------------------------
// TestAsyncRegistryConcurrentStress tests concurrent dispatching safety.
// ---------------------------------------------------------------------------

func TestAsyncRegistryConcurrentStress(t *testing.T) {
	reg := NewAsyncRegistry()

	const numGoroutines = 50
	const hooksPerGoroutine = 10
	var completedCount atomic.Int64

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		go func() {
			defer wg.Done()
			for h := 0; h < hooksPerGoroutine; h++ {
				f, err := reg.ExecuteAsync(context.Background(), "stress", func(ctx context.Context) (any, error) {
					completedCount.Add(1)
					return nil, nil
				})
				if err != nil {
					t.Errorf("ExecuteAsync: %v", err)
					return
				}
				// Randomly choose to await some futures immediately.
				if h%3 == 0 {
					_, _ = f.Wait(context.Background())
				}
			}
		}()
	}

	wg.Wait()

	// Wait for all hooks to complete.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := reg.Shutdown(ctx)
	if err != nil {
		t.Fatalf("Shutdown: %v", err)
		return
	}

	// All hooks should have completed.
	expected := int64(numGoroutines * hooksPerGoroutine)
	if got := completedCount.Load(); got != expected {
		t.Fatalf("completedCount = %d, want %d", got, expected)
	}

	// Active count should be 0.
	if count := reg.ActiveCount(); count != 0 {
		t.Fatalf("ActiveCount = %d, want 0", count)
	}
}

// ---------------------------------------------------------------------------
// TestAsyncRegistryPendingAndCompletedFutures tests the query methods.
// ---------------------------------------------------------------------------

func TestAsyncRegistryPendingAndCompletedFutures(t *testing.T) {
	reg := NewAsyncRegistry()

	blocker := make(chan struct{})

	// Dispatch some that complete immediately and some that block.
	for i := 0; i < 3; i++ {
		_, _ = reg.ExecuteAsync(context.Background(), "fast", func(ctx context.Context) (any, error) {
			return "fast", nil
		})
	}
	for i := 0; i < 2; i++ {
		_, _ = reg.ExecuteAsync(context.Background(), "slow", func(ctx context.Context) (any, error) {
			<-blocker
			return "slow", nil
		})
	}

	// Wait for fast hooks to complete.
	time.Sleep(20 * time.Millisecond)

	completed := reg.CompletedFutures()
	pending := reg.PendingFutures()

	if len(completed) != 3 {
		t.Fatalf("CompletedFutures = %d, want 3", len(completed))
	}
	if len(pending) != 2 {
		t.Fatalf("PendingFutures = %d, want 2", len(pending))
	}

	// Unblock and verify all complete.
	close(blocker)
	time.Sleep(20 * time.Millisecond)

	completed = reg.CompletedFutures()
	pending = reg.PendingFutures()
	if len(completed) != 5 {
		t.Fatalf("CompletedFutures after unblock = %d, want 5", len(completed))
	}
	if len(pending) != 0 {
		t.Fatalf("PendingFutures after unblock = %d, want 0", len(pending))
	}
}

// ---------------------------------------------------------------------------
// TestAsyncRegistryClear tests clearing completed futures.
// ---------------------------------------------------------------------------

func TestAsyncRegistryClear(t *testing.T) {
	reg := NewAsyncRegistry()

	// Dispatch and wait for some hooks.
	for i := 0; i < 5; i++ {
		f, _ := reg.ExecuteAsync(context.Background(), "clear_test", func(ctx context.Context) (any, error) {
			return nil, nil
		})
		_, _ = f.Wait(context.Background())
	}

	// Dispatch one that blocks.
	blocker := make(chan struct{})
	_, _ = reg.ExecuteAsync(context.Background(), "blocking", func(ctx context.Context) (any, error) {
		<-blocker
		return nil, nil
	})

	// Clear completed.
	reg.Clear()

	// Should only have the one active future.
	reg.mu.Lock()
	futureCount := len(reg.futures)
	reg.mu.Unlock()

	if futureCount != 1 {
		t.Fatalf("futures after Clear = %d, want 1 (the active one)", futureCount)
	}

	// Clean up.
	close(blocker)
	_ = reg.Shutdown(context.Background())
}

// ---------------------------------------------------------------------------
// TestAsyncRegistryHookTimeout tests context-based timeout for individual hooks.
// ---------------------------------------------------------------------------

func TestAsyncRegistryHookTimeout(t *testing.T) {
	reg := NewAsyncRegistry()

	// Use a context with a very short timeout for the hook.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	future, err := reg.ExecuteAsync(ctx, "timeout_hook", func(ctx context.Context) (any, error) {
		// This hook will block until cancelled by the timeout.
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatalf("ExecuteAsync: %v", err)
		return
	}

	// Wait for it to finish.
	result, err := future.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
		return
	}
	if result.Error == nil {
		t.Fatal("expected timeout error")
		return
	}
	if future.Status() != HookFutureStatusCancelled {
		t.Fatalf("Status = %v, want %v", future.Status(), HookFutureStatusCancelled)
	}
}

// ---------------------------------------------------------------------------
// TestAsyncRegistryFireAndForget tests fire-and-forget pattern.
// ---------------------------------------------------------------------------

func TestAsyncRegistryFireAndForget(t *testing.T) {
	reg := NewAsyncRegistry()
	var executed atomic.Bool

	// Fire and forget: don't await the future.
	_, err := reg.ExecuteAsync(context.Background(), "fire_forget", func(ctx context.Context) (any, error) {
		executed.Store(true)
		return nil, nil
	})
	if err != nil {
		t.Fatalf("ExecuteAsync: %v", err)
		return
	}

	// Shutdown ensures all hooks complete.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = reg.Shutdown(ctx)

	if !executed.Load() {
		t.Fatal("fire-and-forget hook was not executed")
	}
}

// ---------------------------------------------------------------------------
// TestAsyncRegistryMultiplePanicsDontCorruptState tests that multiple panics
// don't leave the registry in a broken state.
// ---------------------------------------------------------------------------

func TestAsyncRegistryMultiplePanicsDontCorruptState(t *testing.T) {
	reg := NewAsyncRegistry()

	// Dispatch several hooks that all panic.
	const numHooks = 10
	futures := make([]*HookFuture, numHooks)
	for i := 0; i < numHooks; i++ {
		idx := i
		f, err := reg.ExecuteAsync(context.Background(), "multi_panic", func(ctx context.Context) (any, error) {
			panic(errors.New("panic " + string(rune('A'+idx))))
		})
		if err != nil {
			t.Fatalf("ExecuteAsync[%d]: %v", i, err)
			return
		}
		futures[i] = f
	}

	// Wait for all to complete.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for i, f := range futures {
		result, err := f.Wait(ctx)
		if err != nil {
			t.Fatalf("Wait[%d]: %v", i, err)
			return
		}
		if result.Error == nil {
			t.Fatalf("future[%d]: expected error from panic", i)
			return
		}
		if f.Status() != HookFutureStatusFailed {
			t.Fatalf("future[%d]: Status = %v, want %v", i, f.Status(), HookFutureStatusFailed)
		}
	}

	// Registry should still be functional.
	f, err := reg.ExecuteAsync(context.Background(), "post_panic", func(ctx context.Context) (any, error) {
		return "still works", nil
	})
	if err != nil {
		t.Fatalf("ExecuteAsync after panics: %v", err)
		return
	}
	result, _ := f.Wait(context.Background())
	if result.Output != "still works" {
		t.Fatalf("Output = %v, want %q", result.Output, "still works")
	}

	// ActiveCount should be 0 after all complete.
	if count := reg.ActiveCount(); count != 0 {
		t.Fatalf("ActiveCount = %d, want 0", count)
	}
}

// ---------------------------------------------------------------------------
// TestAsyncRegistryStatusTransitions tests the status lifecycle.
// ---------------------------------------------------------------------------

func TestAsyncRegistryStatusTransitions(t *testing.T) {
	reg := NewAsyncRegistry()

	blocker := make(chan struct{})
	future, _ := reg.ExecuteAsync(context.Background(), "status_test", func(ctx context.Context) (any, error) {
		<-blocker
		return "done", nil
	})

	// Should transition through pending -> running.
	// Give the goroutine time to start.
	time.Sleep(10 * time.Millisecond)
	status := future.Status()
	if status != HookFutureStatusRunning {
		t.Fatalf("Status during execution = %v, want %v", status, HookFutureStatusRunning)
	}

	// Unblock.
	close(blocker)
	_, _ = future.Wait(context.Background())

	if future.Status() != HookFutureStatusCompleted {
		t.Fatalf("Status after completion = %v, want %v", future.Status(), HookFutureStatusCompleted)
	}
}
