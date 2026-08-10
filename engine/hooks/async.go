package hooks

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// HookFuture represents the result of an asynchronous hook execution.
// It is safe for concurrent access from multiple readers.
// ---------------------------------------------------------------------------

// HookFutureStatus represents the current state of a hook future.
type HookFutureStatus string

const (
	// HookFutureStatusPending means the hook has not started executing yet.
	HookFutureStatusPending HookFutureStatus = "pending"
	// HookFutureStatusRunning means the hook is currently executing.
	HookFutureStatusRunning HookFutureStatus = "running"
	// HookFutureStatusCompleted means the hook finished successfully.
	HookFutureStatusCompleted HookFutureStatus = "completed"
	// HookFutureStatusFailed means the hook finished with an error.
	HookFutureStatusFailed HookFutureStatus = "failed"
	// HookFutureStatusCancelled means the hook was cancelled before completion.
	HookFutureStatusCancelled HookFutureStatus = "cancelled"
)

// HookFutureResult holds the outcome of an async hook execution.
type HookFutureResult struct {
	// Output is the result data produced by the hook (may be nil for fire-and-forget).
	Output any
	// Error is set if the hook failed or panicked.
	Error error
	// Duration is how long the hook took to execute.
	Duration time.Duration
}

// HookFuture represents the pending result of an asynchronous hook execution.
// Multiple goroutines may safely call Wait, Result, Done, and Status concurrently.
type HookFuture struct {
	// ID is a unique identifier for this future.
	ID string
	// Event is the hook event that triggered this execution.
	Event string
	// CreatedAt is when the future was created.
	CreatedAt time.Time

	// done is closed when the hook completes (success, failure, or cancel).
	done chan struct{}
	// cancel cancels the hook's execution context.
	cancel context.CancelFunc

	// mu protects status and result.
	mu     sync.RWMutex
	status HookFutureStatus
	result *HookFutureResult
	retain bool
}

// Wait blocks until the hook completes or the provided context is cancelled.
// Returns the hook result on success, or a context error if cancelled/timed out.
func (f *HookFuture) Wait(ctx context.Context) (*HookFutureResult, error) {
	select {
	case <-f.done:
		f.mu.RLock()
		defer f.mu.RUnlock()
		return f.result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Result returns the hook result if ready, or nil if still running.
// This is a non-blocking check.
func (f *HookFuture) Result() *HookFutureResult {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.result
}

// Done returns a channel that is closed when the hook execution completes.
// Callers can select on this channel to be notified of completion.
func (f *HookFuture) Done() <-chan struct{} {
	return f.done
}

// Status returns the current status of the hook execution.
func (f *HookFuture) Status() HookFutureStatus {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.status
}

// IsDone returns true if the hook has finished (completed, failed, or cancelled).
func (f *HookFuture) IsDone() bool {
	select {
	case <-f.done:
		return true
	default:
		return false
	}
}

// setResult sets the final result and closes the done channel.
// Must only be called once.
func (f *HookFuture) setResult(status HookFutureStatus, result *HookFutureResult, beforeClose func()) {
	f.mu.Lock()
	f.status = status
	f.result = result
	f.mu.Unlock()
	if beforeClose != nil {
		beforeClose()
	}
	close(f.done)
}

// setStatus updates the status without completing the future.
func (f *HookFuture) setStatus(status HookFutureStatus) {
	f.mu.Lock()
	f.status = status
	f.mu.Unlock()
}

// ---------------------------------------------------------------------------
// AsyncHookFunc is the function signature for hooks executed asynchronously.
// It receives a context (which is cancelled on CancelAll/Shutdown) and returns
// an optional result and an error.
// ---------------------------------------------------------------------------

// AsyncHookFunc is the function type that can be executed asynchronously.
// The context is derived from the registry and will be cancelled on shutdown.
type AsyncHookFunc func(ctx context.Context) (any, error)

// ---------------------------------------------------------------------------
// AsyncRegistry manages asynchronous hook execution with lifecycle control.
// It dispatches hooks as goroutines, tracks their futures, and provides
// shutdown and cancellation semantics.
//
// This mirrors the AsyncHookRegistry pattern from the reference implementation,
// allowing hooks to run in the background without blocking the main query loop.
// ---------------------------------------------------------------------------

// AsyncRegistry manages the lifecycle of asynchronous hook executions.
// It is safe for concurrent use.
type AsyncRegistry struct {
	mu      sync.Mutex
	futures []*HookFuture
	nextID  atomic.Uint64

	// closed is set to true after Shutdown is called.
	closed bool

	// wg tracks active goroutines for shutdown.
	wg sync.WaitGroup
}

// NewAsyncRegistry creates a new async hook registry.
func NewAsyncRegistry() *AsyncRegistry {
	return &AsyncRegistry{}
}

// ExecuteAsync dispatches a hook function for asynchronous execution.
// The hook runs in a separate goroutine. The returned HookFuture can be used
// to await the result or check completion status.
//
// Parameters:
//   - ctx: parent context for the hook execution (used for timeout/cancellation)
//   - event: descriptive name of the hook event (for tracking)
//   - fn: the hook function to execute
//
// Returns a HookFuture that can be awaited, or an error if the registry is shut down.
func (r *AsyncRegistry) ExecuteAsync(ctx context.Context, event string, fn AsyncHookFunc) (*HookFuture, error) {
	return r.executeAsync(ctx, event, fn, true)
}

// ExecuteAsyncTransient dispatches a hook whose future is removed from the
// registry immediately after completion. Callers must retain the returned
// future themselves if they need to await it.
func (r *AsyncRegistry) ExecuteAsyncTransient(ctx context.Context, event string, fn AsyncHookFunc) (*HookFuture, error) {
	return r.executeAsync(ctx, event, fn, false)
}

func (r *AsyncRegistry) executeAsync(ctx context.Context, event string, fn AsyncHookFunc, retain bool) (*HookFuture, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, fmt.Errorf("async registry is shut down")
	}

	id := r.nextID.Add(1)
	hookCtx, cancel := context.WithCancel(ctx)

	future := &HookFuture{
		ID:        fmt.Sprintf("hook_%d", id),
		Event:     event,
		CreatedAt: time.Now(),
		done:      make(chan struct{}),
		cancel:    cancel,
		status:    HookFutureStatusPending,
		retain:    retain,
	}

	r.futures = append(r.futures, future)
	r.wg.Add(1)
	r.mu.Unlock()

	go r.runHook(hookCtx, future, fn)

	return future, nil
}

// runHook executes the hook function with panic recovery and result tracking.
func (r *AsyncRegistry) runHook(ctx context.Context, future *HookFuture, fn AsyncHookFunc) {
	defer r.wg.Done()

	start := time.Now()
	future.setStatus(HookFutureStatusRunning)

	// Execute with panic recovery.
	var (
		output any
		err    error
	)

	func() {
		defer func() {
			if rec := recover(); rec != nil {
				err = fmt.Errorf("hook panicked: %v", rec)
			}
		}()
		output, err = fn(ctx)
	}()

	duration := time.Since(start)

	result := &HookFutureResult{
		Output:   output,
		Error:    err,
		Duration: duration,
	}

	// Check context cancellation first: if the context was cancelled, the hook
	// error is likely just ctx.Err() propagation, so classify as cancelled.
	var status HookFutureStatus
	if ctx.Err() != nil {
		if result.Error == nil {
			result.Error = ctx.Err()
		}
		status = HookFutureStatusCancelled
	} else if err != nil {
		status = HookFutureStatusFailed
	} else {
		status = HookFutureStatusCompleted
	}
	var beforeClose func()
	if !future.retain {
		beforeClose = func() { r.removeFuture(future) }
	}
	future.setResult(status, result, beforeClose)
}

func (r *AsyncRegistry) removeFuture(target *HookFuture) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index, future := range r.futures {
		if future == target {
			r.futures = append(r.futures[:index], r.futures[index+1:]...)
			return
		}
	}
}

// ActiveCount returns the number of hook executions currently in-flight
// (pending or running, not yet completed/failed/cancelled).
func (r *AsyncRegistry) ActiveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	count := 0
	for _, f := range r.futures {
		if !f.IsDone() {
			count++
		}
	}
	return count
}

// PendingFutures returns all futures that have not yet completed.
func (r *AsyncRegistry) PendingFutures() []*HookFuture {
	r.mu.Lock()
	defer r.mu.Unlock()

	var pending []*HookFuture
	for _, f := range r.futures {
		if !f.IsDone() {
			pending = append(pending, f)
		}
	}
	return pending
}

// CompletedFutures returns all futures that have finished (completed, failed, or cancelled).
func (r *AsyncRegistry) CompletedFutures() []*HookFuture {
	r.mu.Lock()
	defer r.mu.Unlock()

	var completed []*HookFuture
	for _, f := range r.futures {
		if f.IsDone() {
			completed = append(completed, f)
		}
	}
	return completed
}

// CollectAll waits for all pending futures to complete (or the context to expire)
// and returns their results. This is useful for collecting results from
// fire-and-collect hooks at a synchronization point.
func (r *AsyncRegistry) CollectAll(ctx context.Context) []*HookFutureResult {
	r.mu.Lock()
	futures := make([]*HookFuture, len(r.futures))
	copy(futures, r.futures)
	r.mu.Unlock()

	var results []*HookFutureResult
	for _, f := range futures {
		result, err := f.Wait(ctx)
		if err != nil {
			// Context cancelled — stop collecting.
			break
		}
		if result != nil {
			results = append(results, result)
		}
	}
	return results
}

// CancelAll cancels all in-flight hook executions. Already-completed hooks
// are not affected. This does not wait for goroutines to finish — use
// Shutdown for graceful waiting.
func (r *AsyncRegistry) CancelAll() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, f := range r.futures {
		if !f.IsDone() {
			f.cancel()
		}
	}
}

// Shutdown gracefully shuts down the registry:
//  1. Marks the registry as closed (no new hooks can be dispatched)
//  2. Waits for all in-flight hooks to complete (subject to ctx timeout)
//  3. Cancels any remaining hooks if the context expires
//
// Returns nil if all hooks completed within the deadline, or context.DeadlineExceeded
// if the timeout was hit and remaining hooks were force-cancelled.
func (r *AsyncRegistry) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()

	// Wait for all goroutines to finish, respecting the context deadline.
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		// Timeout hit — cancel all remaining hooks.
		r.CancelAll()
		// Give a brief moment for goroutines to react to cancellation.
		// Use a short secondary wait so we don't leak goroutines.
		secondaryDone := make(chan struct{})
		go func() {
			r.wg.Wait()
			close(secondaryDone)
		}()
		select {
		case <-secondaryDone:
		case <-time.After(100 * time.Millisecond):
			// Goroutines did not finish in time — they will eventually finish
			// when their hook functions observe the cancelled context.
		}
		return ctx.Err()
	}
}

// Clear removes all completed futures from the registry's tracking list.
// Active futures are retained. This is useful for preventing unbounded
// memory growth in long-running sessions.
func (r *AsyncRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()

	var active []*HookFuture
	for _, f := range r.futures {
		if !f.IsDone() {
			active = append(active, f)
		}
	}
	r.futures = active
}

// IsClosed returns true if the registry has been shut down.
func (r *AsyncRegistry) IsClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}
