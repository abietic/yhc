package execution

import (
	"context"
	"errors"
	"sync"
	"time"
)

// PendingToolCall represents a single tool invocation tracked by the
// ConcurrentExecutor throughout its lifecycle.
type PendingToolCall struct {
	ID          string
	Name        string
	Arguments   string // JSON
	Status      string // "pending", "running", "completed", "aborted", "errored"
	Result      string
	Error       error
	StartedAt   time.Time
	CompletedAt time.Time
	cancel      context.CancelFunc
}

// ConcurrentExecutor manages concurrent tool execution with per-call abort,
// global abort, and progress tracking. Tools stream in via Submit and start
// executing immediately when concurrency slots are available.
//
// This complements the existing StreamingToolExecutor (which mirrors the
// reference StreamingToolExecutor.ts ordering semantics) by providing a
// simpler context-based abort and WaitGroup-based completion API useful for
// scenarios like sub-agent orchestration and batch tool dispatch.
type ConcurrentExecutor struct {
	mu              sync.Mutex
	pendingCalls    map[string]*PendingToolCall
	queuedExecutors map[string]func(ctx context.Context, name, args string) (string, error)
	queue           []string // ordered IDs of calls waiting for a slot
	maxConcurrent   int
	activeCalls     int
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
}

// NewConcurrentExecutor creates a new executor bound to the given context.
// maxConcurrent controls how many tool calls may run simultaneously.
func NewConcurrentExecutor(ctx context.Context, maxConcurrent int) *ConcurrentExecutor {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	childCtx, childCancel := context.WithCancel(ctx)
	return &ConcurrentExecutor{
		pendingCalls:    make(map[string]*PendingToolCall),
		queuedExecutors: make(map[string]func(ctx context.Context, name, args string) (string, error)),
		queue:           make([]string, 0),
		maxConcurrent:   maxConcurrent,
		ctx:             childCtx,
		cancel:          childCancel,
	}
}

// Submit adds a tool call to the executor. If a concurrency slot is available,
// execution starts immediately; otherwise the call is queued.
func (s *ConcurrentExecutor) Submit(call *PendingToolCall, executor func(ctx context.Context, name, args string) (string, error)) {
	if call == nil || call.ID == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ctx.Err() != nil {
		call.Status = "aborted"
		call.Error = context.Canceled
		call.CompletedAt = time.Now()
		s.pendingCalls[call.ID] = call
		return
	}

	call.Status = "pending"
	s.pendingCalls[call.ID] = call

	if s.activeCalls < s.maxConcurrent {
		s.startCallLocked(call, executor)
	} else {
		s.queue = append(s.queue, call.ID)
		s.queuedExecutors[call.ID] = executor
	}
}

// AbortAll cancels all running and pending calls.
func (s *ConcurrentExecutor) AbortAll() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Cancel the parent context which propagates to all running calls.
	s.cancel()

	// Mark all non-completed calls as aborted.
	for _, call := range s.pendingCalls {
		switch call.Status {
		case "pending":
			call.Status = "aborted"
			call.Error = context.Canceled
			call.CompletedAt = time.Now()
		case "running":
			if call.cancel != nil {
				call.cancel()
			}
			// Status will be set to "aborted" when the goroutine finishes.
		}
	}

	// Clear the queue since nothing will be scheduled.
	s.queue = nil
}

// AbortByID cancels a specific call by its ID.
func (s *ConcurrentExecutor) AbortByID(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	call, ok := s.pendingCalls[id]
	if !ok {
		return errors.New("tool call not found: " + id)
	}

	switch call.Status {
	case "pending":
		call.Status = "aborted"
		call.Error = context.Canceled
		call.CompletedAt = time.Now()
		// Remove from queue and clean up stored executor.
		s.removeFromQueueLocked(id)
		delete(s.queuedExecutors, id)
	case "running":
		if call.cancel != nil {
			call.cancel()
		}
		// Status will be set to "aborted" when the goroutine finishes.
	default:
		return errors.New("tool call already finished: " + id)
	}

	return nil
}

// Wait blocks until all submitted calls have completed (or been aborted),
// then returns a snapshot of all tracked calls.
func (s *ConcurrentExecutor) Wait() map[string]*PendingToolCall {
	s.wg.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()

	result := make(map[string]*PendingToolCall, len(s.pendingCalls))
	for k, v := range s.pendingCalls {
		result[k] = v
	}
	return result
}

// Progress returns the current counts of running, pending, and completed calls.
func (s *ConcurrentExecutor) Progress() (running, pending, completed int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, call := range s.pendingCalls {
		switch call.Status {
		case "running":
			running++
		case "pending":
			pending++
		case "completed", "aborted", "errored":
			completed++
		}
	}
	return
}

// startCallLocked starts execution of a tool call in a goroutine.
// Must be called with s.mu held.
func (s *ConcurrentExecutor) startCallLocked(call *PendingToolCall, executor func(ctx context.Context, name, args string) (string, error)) {
	callCtx, callCancel := context.WithCancel(s.ctx)
	call.cancel = callCancel
	call.Status = "running"
	call.StartedAt = time.Now()
	s.activeCalls++
	s.wg.Add(1)

	go func() {
		defer s.wg.Done()

		result, err := executor(callCtx, call.Name, call.Arguments)

		s.mu.Lock()
		defer s.mu.Unlock()

		s.activeCalls--
		call.CompletedAt = time.Now()
		call.cancel = nil

		if err != nil {
			if callCtx.Err() != nil {
				call.Status = "aborted"
				call.Error = callCtx.Err()
			} else {
				call.Status = "errored"
				call.Error = err
			}
		} else {
			call.Status = "completed"
			call.Result = result
		}

		callCancel() // clean up the context

		s.scheduleNext()
	}()
}

// scheduleNext pulls the next queued call and starts it if a slot is available.
// Must be called with s.mu held.
func (s *ConcurrentExecutor) scheduleNext() {
	for s.activeCalls < s.maxConcurrent && len(s.queue) > 0 {
		nextID := s.queue[0]
		s.queue = s.queue[1:]

		call, ok := s.pendingCalls[nextID]
		if !ok || call.Status != "pending" {
			continue
		}

		// If context is already cancelled, mark as aborted instead of starting.
		if s.ctx.Err() != nil {
			call.Status = "aborted"
			call.Error = context.Canceled
			call.CompletedAt = time.Now()
			continue
		}

		if fn, exists := s.queuedExecutors[nextID]; exists {
			delete(s.queuedExecutors, nextID)
			s.startCallLocked(call, fn)
		} else {
			call.Status = "errored"
			call.Error = errors.New("no executor registered for queued call")
			call.CompletedAt = time.Now()
		}
	}
}

// removeFromQueueLocked removes an ID from the pending queue.
func (s *ConcurrentExecutor) removeFromQueueLocked(id string) {
	for i, qid := range s.queue {
		if qid == id {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			return
		}
	}
}
