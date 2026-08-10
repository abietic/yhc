package execution

import (
	"context"
	"fmt"
	"sync"
)

// StreamAccumulatorConfig configures the StreamAccumulator.
type StreamAccumulatorConfig struct {
	// MaxBufferSize is the maximum number of events that can be buffered.
	// When exceeded, older events are discarded (overflow handling).
	// 0 means unlimited (default behavior).
	MaxBufferSize int

	// OnOverflow is called when the buffer overflows. Receives the number of
	// events discarded. If nil, overflow is silent.
	OnOverflow func(discarded int)

	// OnMidStreamFailure is called when a tool fails mid-stream. Receives the
	// tool call ID and the partial result accumulated so far.
	OnMidStreamFailure func(toolCallID, partialResult string, err error)

	// PreservePartialOnFailure when true keeps partial results available even
	// when the stream terminates with an error. Default (false) discards partial
	// results on failure for consistency.
	PreservePartialOnFailure bool
}

// StreamAccumulator handles edge cases in streaming result accumulation:
// - Mid-stream tool failure (partial results preserved based on config)
// - Empty streams (tool produces no output events)
// - Stream overflow (buffer limit exceeded)
// - Concurrent cancellation during accumulation
//
// This complements the StreamingToolExecutor by providing a buffer-level
// accumulation layer that tracks partial progress and handles failures gracefully.
type StreamAccumulator struct {
	mu     sync.Mutex
	config StreamAccumulatorConfig

	// Per-tool accumulation state.
	buffers map[string]*toolBuffer

	// closed prevents further writes after the accumulator is shut down.
	closed bool
}

// toolBuffer tracks the streaming accumulation state for a single tool.
type toolBuffer struct {
	toolCallID string
	toolName   string
	chunks     []string
	totalSize  int
	failed     bool
	failError  error
	completed  bool
	overflow   bool
	discarded  int
}

// NewStreamAccumulator creates a new accumulator with the given configuration.
func NewStreamAccumulator(config StreamAccumulatorConfig) *StreamAccumulator {
	return &StreamAccumulator{
		config:  config,
		buffers: make(map[string]*toolBuffer),
	}
}

// AppendChunk adds a streaming chunk to the buffer for a specific tool call.
// Returns an error if the accumulator is closed or if overflow handling applies.
// Thread-safe.
func (a *StreamAccumulator) AppendChunk(toolCallID, toolName, chunk string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return fmt.Errorf("accumulator closed: cannot append to %s", toolCallID)
	}

	buf, ok := a.buffers[toolCallID]
	if !ok {
		buf = &toolBuffer{
			toolCallID: toolCallID,
			toolName:   toolName,
			chunks:     make([]string, 0, 32),
		}
		a.buffers[toolCallID] = buf
	}

	if buf.completed || buf.failed {
		return fmt.Errorf("tool %s already finished", toolCallID)
	}

	// Check buffer overflow.
	if a.config.MaxBufferSize > 0 && len(buf.chunks) >= a.config.MaxBufferSize {
		// Discard the oldest chunk to make room (ring-buffer style).
		discardedChunk := buf.chunks[0]
		buf.chunks = buf.chunks[1:]
		buf.discarded++
		buf.totalSize -= len(discardedChunk)
		buf.overflow = true

		if a.config.OnOverflow != nil {
			a.config.OnOverflow(buf.discarded)
		}
	}

	buf.chunks = append(buf.chunks, chunk)
	buf.totalSize += len(chunk)
	return nil
}

// MarkFailed marks a tool's stream as failed mid-accumulation.
// If PreservePartialOnFailure is true, partial chunks remain accessible.
// Thread-safe.
func (a *StreamAccumulator) MarkFailed(toolCallID string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	buf, ok := a.buffers[toolCallID]
	if !ok {
		buf = &toolBuffer{
			toolCallID: toolCallID,
			chunks:     make([]string, 0),
		}
		a.buffers[toolCallID] = buf
	}

	buf.failed = true
	buf.failError = err

	if !a.config.PreservePartialOnFailure {
		buf.chunks = nil
		buf.totalSize = 0
	}

	if a.config.OnMidStreamFailure != nil {
		partial := a.assembleChunksLocked(toolCallID)
		a.config.OnMidStreamFailure(toolCallID, partial, err)
	}
}

// MarkCompleted marks a tool's stream as successfully completed.
// Thread-safe.
func (a *StreamAccumulator) MarkCompleted(toolCallID string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	buf, ok := a.buffers[toolCallID]
	if !ok {
		// Empty stream: tool completed without producing any chunks.
		buf = &toolBuffer{
			toolCallID: toolCallID,
			chunks:     make([]string, 0),
			completed:  true,
		}
		a.buffers[toolCallID] = buf
		return
	}
	buf.completed = true
}

// GetResult retrieves the accumulated result for a tool call.
// Returns the assembled content, whether the stream was empty, whether it
// overflowed, and any failure error.
// Thread-safe.
func (a *StreamAccumulator) GetResult(toolCallID string) (content string, isEmpty, overflowed bool, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	buf, ok := a.buffers[toolCallID]
	if !ok {
		return "", true, false, nil
	}

	if buf.failed {
		partial := a.assembleChunksLocked(toolCallID)
		return partial, partial == "", buf.overflow, buf.failError
	}

	content = a.assembleChunksLocked(toolCallID)
	return content, content == "", buf.overflow, nil
}

// GetStatus returns the current status of a tool's accumulation buffer.
// Thread-safe.
func (a *StreamAccumulator) GetStatus(toolCallID string) (chunks, totalSize int, completed, failed bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	buf, ok := a.buffers[toolCallID]
	if !ok {
		return 0, 0, false, false
	}
	return len(buf.chunks), buf.totalSize, buf.completed, buf.failed
}

// Close shuts down the accumulator, preventing further writes.
// Existing accumulated data remains readable.
// Thread-safe.
func (a *StreamAccumulator) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
}

// CloseWithCancel shuts down the accumulator and marks all incomplete tool
// streams as failed with the given context error. This handles the case where
// a context cancellation occurs during accumulation.
// Thread-safe.
func (a *StreamAccumulator) CloseWithCancel(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true

	cancelErr := ctx.Err()
	if cancelErr == nil {
		cancelErr = context.Canceled
	}

	for _, buf := range a.buffers {
		if !buf.completed && !buf.failed {
			buf.failed = true
			buf.failError = cancelErr
			if !a.config.PreservePartialOnFailure {
				buf.chunks = nil
				buf.totalSize = 0
			}
		}
	}
}

// assembleChunksLocked concatenates all chunks for a tool call.
// Must be called with a.mu held.
func (a *StreamAccumulator) assembleChunksLocked(toolCallID string) string {
	buf, ok := a.buffers[toolCallID]
	if !ok || len(buf.chunks) == 0 {
		return ""
	}

	// Fast path: single chunk.
	if len(buf.chunks) == 1 {
		return buf.chunks[0]
	}

	total := 0
	for _, c := range buf.chunks {
		total += len(c)
	}
	result := make([]byte, 0, total)
	for _, c := range buf.chunks {
		result = append(result, c...)
	}
	return string(result)
}
