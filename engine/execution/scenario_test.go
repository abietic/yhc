package execution

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

// ============================================================================
// Stream Accumulator Edge Case Tests
// ============================================================================

func TestStreamAccumulator_NormalFlow(t *testing.T) {
	acc := NewStreamAccumulator(StreamAccumulatorConfig{})

	if err := acc.AppendChunk("call_1", "Read", "hello "); err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if err := acc.AppendChunk("call_1", "Read", "world"); err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	acc.MarkCompleted("call_1")

	content, isEmpty, overflowed, err := acc.GetResult("call_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if isEmpty {
		t.Error("expected non-empty")
	}
	if overflowed {
		t.Error("expected no overflow")
	}
	if content != "hello world" {
		t.Errorf("expected 'hello world', got %q", content)
	}
}

func TestStreamAccumulator_EmptyStream(t *testing.T) {
	acc := NewStreamAccumulator(StreamAccumulatorConfig{})

	// Complete without any chunks — empty stream.
	acc.MarkCompleted("call_1")

	content, isEmpty, overflowed, err := acc.GetResult("call_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if !isEmpty {
		t.Error("expected empty")
	}
	if overflowed {
		t.Error("expected no overflow")
	}
	if content != "" {
		t.Errorf("expected empty content, got %q", content)
	}
}

func TestStreamAccumulator_NonexistentTool(t *testing.T) {
	acc := NewStreamAccumulator(StreamAccumulatorConfig{})

	content, isEmpty, overflowed, err := acc.GetResult("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if !isEmpty {
		t.Error("expected empty for nonexistent tool")
	}
	if overflowed {
		t.Error("expected no overflow")
	}
	if content != "" {
		t.Errorf("expected empty content, got %q", content)
	}
}

func TestStreamAccumulator_MidStreamFailure(t *testing.T) {
	var failureCalled bool
	var failurePartial string

	acc := NewStreamAccumulator(StreamAccumulatorConfig{
		PreservePartialOnFailure: true,
		OnMidStreamFailure: func(toolCallID, partialResult string, err error) {
			failureCalled = true
			failurePartial = partialResult
		},
	})

	_ = acc.AppendChunk("call_1", "Read", "partial ")
	_ = acc.AppendChunk("call_1", "Read", "data")
	acc.MarkFailed("call_1", fmt.Errorf("connection reset"))

	content, isEmpty, _, err := acc.GetResult("call_1")
	if err == nil {
		t.Fatal("expected error for failed stream")
		return
	}
	if err.Error() != "connection reset" {
		t.Errorf("expected 'connection reset', got %q", err.Error())
	}
	if isEmpty {
		t.Error("expected non-empty with PreservePartialOnFailure")
	}
	if content != "partial data" {
		t.Errorf("expected 'partial data', got %q", content)
	}
	if !failureCalled {
		t.Error("expected OnMidStreamFailure callback")
	}
	if failurePartial != "partial data" {
		t.Errorf("expected partial 'partial data', got %q", failurePartial)
	}
}

func TestStreamAccumulator_MidStreamFailure_NoPreserve(t *testing.T) {
	acc := NewStreamAccumulator(StreamAccumulatorConfig{
		PreservePartialOnFailure: false,
	})

	_ = acc.AppendChunk("call_1", "Read", "partial data")
	acc.MarkFailed("call_1", fmt.Errorf("timeout"))

	content, isEmpty, _, err := acc.GetResult("call_1")
	if err == nil {
		t.Fatal("expected error")
		return
	}
	if !isEmpty {
		t.Error("expected empty when PreservePartialOnFailure is false")
	}
	if content != "" {
		t.Errorf("expected empty content, got %q", content)
	}
}

func TestStreamAccumulator_Overflow(t *testing.T) {
	var overflowCount int
	acc := NewStreamAccumulator(StreamAccumulatorConfig{
		MaxBufferSize: 3,
		OnOverflow: func(discarded int) {
			overflowCount = discarded
		},
	})

	// Add 5 chunks to a buffer of 3.
	for i := 0; i < 5; i++ {
		_ = acc.AppendChunk("call_1", "Read", fmt.Sprintf("chunk%d ", i))
	}
	acc.MarkCompleted("call_1")

	content, isEmpty, overflowed, err := acc.GetResult("call_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if isEmpty {
		t.Error("expected non-empty")
	}
	if !overflowed {
		t.Error("expected overflow flag to be set")
	}
	// Only last 3 chunks should be preserved.
	if content != "chunk2 chunk3 chunk4 " {
		t.Errorf("expected last 3 chunks, got %q", content)
	}
	if overflowCount != 2 {
		t.Errorf("expected 2 discarded, got %d", overflowCount)
	}
}

func TestStreamAccumulator_ConcurrentCancellation(t *testing.T) {
	acc := NewStreamAccumulator(StreamAccumulatorConfig{
		PreservePartialOnFailure: true,
	})

	// Simulate concurrent writers.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = acc.AppendChunk("call_1", "Read", fmt.Sprintf("chunk%d", n))
		}(i)
	}
	wg.Wait()

	// Cancel via context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	acc.CloseWithCancel(ctx)

	_, _, _, err := acc.GetResult("call_1")
	if err == nil {
		t.Error("expected error after cancellation")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestStreamAccumulator_ClosePreventsFurtherWrites(t *testing.T) {
	acc := NewStreamAccumulator(StreamAccumulatorConfig{})

	_ = acc.AppendChunk("call_1", "Read", "before close")
	acc.Close()

	err := acc.AppendChunk("call_1", "Read", "after close")
	if err == nil {
		t.Error("expected error after close")
	}
	if !strings.Contains(err.Error(), "accumulator closed") {
		t.Errorf("expected 'accumulator closed' error, got %q", err.Error())
	}
}

func TestStreamAccumulator_AppendAfterCompleted(t *testing.T) {
	acc := NewStreamAccumulator(StreamAccumulatorConfig{})

	_ = acc.AppendChunk("call_1", "Read", "data")
	acc.MarkCompleted("call_1")

	err := acc.AppendChunk("call_1", "Read", "more data")
	if err == nil {
		t.Error("expected error when appending after completion")
	}
	if !strings.Contains(err.Error(), "already finished") {
		t.Errorf("expected 'already finished' error, got %q", err.Error())
	}
}

func TestStreamAccumulator_AppendAfterFailed(t *testing.T) {
	acc := NewStreamAccumulator(StreamAccumulatorConfig{})

	_ = acc.AppendChunk("call_1", "Read", "data")
	acc.MarkFailed("call_1", fmt.Errorf("oops"))

	err := acc.AppendChunk("call_1", "Read", "more data")
	if err == nil {
		t.Error("expected error when appending after failure")
	}
}

func TestStreamAccumulator_ConcurrentAccess(t *testing.T) {
	acc := NewStreamAccumulator(StreamAccumulatorConfig{
		MaxBufferSize: 100,
	})

	var wg sync.WaitGroup
	// Multiple tools writing concurrently.
	for tool := 0; tool < 5; tool++ {
		toolID := fmt.Sprintf("call_%d", tool)
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				_ = acc.AppendChunk(id, "Read", fmt.Sprintf("chunk%d", i))
			}
			acc.MarkCompleted(id)
		}(toolID)
	}
	wg.Wait()

	// All tools should have completed successfully.
	for tool := 0; tool < 5; tool++ {
		toolID := fmt.Sprintf("call_%d", tool)
		_, isEmpty, _, err := acc.GetResult(toolID)
		if err != nil {
			t.Errorf("tool %s: unexpected error: %v", toolID, err)
		}
		if isEmpty {
			t.Errorf("tool %s: unexpected empty result", toolID)
		}
	}
}

func TestStreamAccumulator_GetStatus(t *testing.T) {
	acc := NewStreamAccumulator(StreamAccumulatorConfig{})

	_ = acc.AppendChunk("call_1", "Read", "hello")
	_ = acc.AppendChunk("call_1", "Read", " world")

	chunks, totalSize, completed, failed := acc.GetStatus("call_1")
	if chunks != 2 {
		t.Errorf("expected 2 chunks, got %d", chunks)
	}
	if totalSize != 11 {
		t.Errorf("expected totalSize=11, got %d", totalSize)
	}
	if completed {
		t.Error("expected not completed")
	}
	if failed {
		t.Error("expected not failed")
	}

	acc.MarkCompleted("call_1")
	_, _, completed, _ = acc.GetStatus("call_1")
	if !completed {
		t.Error("expected completed after MarkCompleted")
	}
}

// ============================================================================
// Full Pipeline Scenario Tests
// ============================================================================

func TestScenario_FullPipelineFlow(t *testing.T) {
	// Scenario: submit → validate → execute → normalize → return
	items := []ToolBatchItem{
		{
			ToolCall: &schema.ToolCall{
				ID:   "call_read",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path":"/tmp/test.txt"}`,
				},
			},
			Execute: func(ctx context.Context) (string, error) {
				return "file content line 1\nfile content line 2", nil
			},
		},
		{
			ToolCall: &schema.ToolCall{
				ID:   "call_bash",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Bash",
					Arguments: `{"command":"echo hello"}`,
				},
			},
			Execute: func(ctx context.Context) (string, error) {
				return "hello\n", nil
			},
		},
	}

	var preHookCalls, postHookCalls int32
	cfg := ToolBatchConfig{
		MaxConcurrency: 5,
		PreToolHook: func(ctx context.Context, toolName, toolCallID string) error {
			atomic.AddInt32(&preHookCalls, 1)
			return nil
		},
		PostToolHook: func(ctx context.Context, toolName, toolCallID, result string, isError bool) {
			atomic.AddInt32(&postHookCalls, 1)
		},
	}

	results := ExecuteToolBatch(context.Background(), items, cfg)

	// Verify results.
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// First result: Read.
	if results[0].ToolCallID != "call_read" {
		t.Errorf("expected call_read, got %q", results[0].ToolCallID)
	}
	if results[0].ToolName != "Read" {
		t.Errorf("expected Read, got %q", results[0].ToolName)
	}
	if results[0].Normalized.IsError {
		t.Errorf("expected success for Read, got error: %s", results[0].Normalized.Content)
	}
	if results[0].Normalized.Content != "file content line 1\nfile content line 2" {
		t.Errorf("unexpected Read content: %q", results[0].Normalized.Content)
	}

	// Second result: Bash.
	if results[1].ToolCallID != "call_bash" {
		t.Errorf("expected call_bash, got %q", results[1].ToolCallID)
	}
	if results[1].Normalized.Content != "hello\n" {
		t.Errorf("unexpected Bash content: %q", results[1].Normalized.Content)
	}

	// Verify hooks were called.
	if atomic.LoadInt32(&preHookCalls) != 2 {
		t.Errorf("expected 2 pre-hook calls, got %d", atomic.LoadInt32(&preHookCalls))
	}
	if atomic.LoadInt32(&postHookCalls) != 2 {
		t.Errorf("expected 2 post-hook calls, got %d", atomic.LoadInt32(&postHookCalls))
	}
}

func TestScenario_RetryThenSuccess(t *testing.T) {
	// Scenario: transient failure → retry → success
	attempts := 0
	result, err := CallModelWithRetry(
		context.Background(),
		RetryConfig{
			MaxRetries: 5,
			BaseDelay:  10 * time.Millisecond, // fast for testing but avoids jitter→0 truncation
		},
		func(ctx context.Context, attempt int) (*CallModelResult, error) {
			attempts++
			if attempt < 2 {
				return nil, fmt.Errorf("overloaded_error: server busy")
			}
			return &CallModelResult{Model: "test-model"}, nil
		},
		func(info RetryWaitInfo) {
			// Verify retry info is populated.
			if info.Delay <= 0 {
				t.Errorf("expected positive delay, got %v", info.Delay)
			}
		},
	)
	if err != nil {
		t.Fatalf("expected success after retries, got error: %v", err)
		return
	}
	if result == nil {
		t.Fatal("expected non-nil result")
		return
	}
	if result.Model != "test-model" {
		t.Errorf("expected test-model, got %q", result.Model)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts (2 failures + 1 success), got %d", attempts)
	}
}

func TestScenario_OverloadCeilingReturnsToCoordinator(t *testing.T) {
	_, err := CallModelWithRetry(
		context.Background(),
		RetryConfig{
			MaxRetries:                   10,
			MaxConsecutiveOverloadErrors: Max529Retries,
			BaseDelay:                    1 * time.Millisecond,
		},
		func(ctx context.Context, attempt int) (*CallModelResult, error) {
			// Always return 529 overloaded.
			return nil, fmt.Errorf("overloaded_error: model at capacity")
		},
		nil,
	)

	if err == nil {
		t.Fatal("expected overload error")
		return
	}
	if ClassifyModelFailure(err) != ModelFailureOverloaded {
		t.Fatalf("failure class = %q, want overloaded", ClassifyModelFailure(err))
	}
}

func TestScenario_ConcurrencyLimitWithVerification(t *testing.T) {
	// Scenario: 10 tools in parallel with concurrency limit of 3,
	// plus output verification.
	const numTools = 10
	const maxConcurrency = 3

	var peak int32
	var current int32

	items := make([]ToolBatchItem, numTools)
	for i := 0; i < numTools; i++ {
		idx := i
		items[i] = ToolBatchItem{
			ToolCall: &schema.ToolCall{
				ID:   fmt.Sprintf("call_%d", idx),
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: fmt.Sprintf(`{"file_path":"/tmp/file_%d.txt"}`, idx),
				},
			},
			Execute: func(ctx context.Context) (string, error) {
				cur := atomic.AddInt32(&current, 1)
				for {
					p := atomic.LoadInt32(&peak)
					if cur <= p {
						break
					}
					if atomic.CompareAndSwapInt32(&peak, p, cur) {
						break
					}
				}
				time.Sleep(5 * time.Millisecond)
				atomic.AddInt32(&current, -1)
				return fmt.Sprintf(`{"content":"file_%d contents","path":"/tmp/file_%d.txt"}`, idx, idx), nil
			},
		}
	}

	results := ExecuteToolBatch(context.Background(), items, ToolBatchConfig{
		MaxConcurrency: maxConcurrency,
	})

	// Verify concurrency was respected.
	observedPeak := atomic.LoadInt32(&peak)
	if observedPeak > int32(maxConcurrency) {
		t.Errorf("peak concurrency %d exceeded limit %d", observedPeak, maxConcurrency)
	}

	// Verify all results came back successfully.
	if len(results) != numTools {
		t.Fatalf("expected %d results, got %d", numTools, len(results))
	}
	for i, r := range results {
		if r.Normalized.IsError {
			t.Errorf("result[%d] unexpected error: %s", i, r.Normalized.Content)
		}
		expectedID := fmt.Sprintf("call_%d", i)
		if r.ToolCallID != expectedID {
			t.Errorf("result[%d] expected ID %q, got %q", i, expectedID, r.ToolCallID)
		}
	}

	// Now verify outputs match expected schema.
	verifyConfig := OutputVerificationConfig{
		Mode: VerifyModeError,
		Schemas: map[string]OutputSchema{
			"Read": {Type: "json", RequiredFields: []string{"content", "path"}},
		},
	}
	for i, r := range results {
		vr := VerifyToolOutput("Read", r.Normalized.Content, r.Normalized.IsError, verifyConfig)
		if !vr.Valid {
			t.Errorf("result[%d] failed verification: %v", i, vr.Violations)
		}
	}
}

func TestScenario_CancellationDuringExecution(t *testing.T) {
	// Scenario: cancel during execution → partial results preserved.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var started int32
	items := make([]ToolBatchItem, 5)
	for i := 0; i < 5; i++ {
		idx := i
		items[i] = ToolBatchItem{
			ToolCall: &schema.ToolCall{
				ID:   fmt.Sprintf("call_%d", idx),
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path":"/tmp/test"}`,
				},
			},
			Execute: func(ctx context.Context) (string, error) {
				n := atomic.AddInt32(&started, 1)
				// First tool triggers cancellation after starting.
				if n == 1 {
					cancel()
				}
				// All tools respect context cancellation.
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(5 * time.Second):
					return fmt.Sprintf("result_%d", idx), nil
				}
			},
		}
	}

	start := time.Now()
	results := ExecuteToolBatch(ctx, items, ToolBatchConfig{
		MaxConcurrency: 2, // limit concurrency to ensure some tools queue
	})
	elapsed := time.Since(start)

	// Should complete quickly due to cancellation.
	if elapsed > 3*time.Second {
		t.Errorf("took too long (%v) — cancellation may not have propagated", elapsed)
	}

	// All results should be present.
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}

	// At least some should be cancelled/errored.
	cancelledOrErrored := 0
	for _, r := range results {
		if r.Normalized.IsError || r.Cancelled {
			cancelledOrErrored++
		}
	}
	if cancelledOrErrored == 0 {
		t.Error("expected at least some tools to be cancelled/errored")
	}
}

func TestScenario_MixedSuccessAndFailure(t *testing.T) {
	// Scenario: batch with mixed success/failure results, verifying normalization.
	items := []ToolBatchItem{
		{
			ToolCall: &schema.ToolCall{
				ID:   "call_ok",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path":"/tmp/exists"}`,
				},
			},
			Execute: func(ctx context.Context) (string, error) {
				return "file contents", nil
			},
		},
		{
			ToolCall: &schema.ToolCall{
				ID:   "call_err",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path":"/tmp/missing"}`,
				},
			},
			Execute: func(ctx context.Context) (string, error) {
				return "", fmt.Errorf("file not found: /tmp/missing")
			},
		},
		{
			ToolCall: &schema.ToolCall{
				ID:   "call_empty",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Bash",
					Arguments: `{"command":"true"}`,
				},
			},
			Execute: func(ctx context.Context) (string, error) {
				return "", nil // empty output
			},
		},
	}

	results := ExecuteToolBatch(context.Background(), items, ToolBatchConfig{
		MaxConcurrency: 10,
	})

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Success case.
	if results[0].Normalized.IsError {
		t.Error("expected success for existing file")
	}
	if results[0].Normalized.Content != "file contents" {
		t.Errorf("expected 'file contents', got %q", results[0].Normalized.Content)
	}

	// Error case.
	if !results[1].Normalized.IsError {
		t.Error("expected error for missing file")
	}
	if !strings.Contains(results[1].Normalized.Content, "file not found") {
		t.Errorf("expected error message, got %q", results[1].Normalized.Content)
	}

	// Empty output case — should be normalized with "(completed with no output)".
	if results[2].Normalized.IsError {
		t.Error("expected non-error for empty output")
	}
	if !strings.Contains(results[2].Normalized.Content, "completed with no output") {
		t.Errorf("expected empty output normalization, got %q", results[2].Normalized.Content)
	}
}

func TestScenario_StreamingExecutorFullLifecycle(t *testing.T) {
	// Scenario: Full streaming executor lifecycle mimicking a real model stream.
	var executionOrder []string
	var mu sync.Mutex

	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		Ctx:            context.Background(),
		MaxConcurrency: 5,
		IsConcurrencySafe: func(toolCall *schema.ToolCall) bool {
			return true
		},
		ExecuteWithContext: func(ctx context.Context, toolCall *schema.ToolCall) *ToolResult {
			mu.Lock()
			executionOrder = append(executionOrder, toolCall.ID)
			mu.Unlock()

			// Simulate varying execution times.
			switch toolCall.Function.Name {
			case "Read":
				time.Sleep(10 * time.Millisecond)
				return newToolResult(toolCall.ID, "Read", "file contents", false)
			case "Bash":
				time.Sleep(20 * time.Millisecond)
				return newToolResult(toolCall.ID, "Bash", "command output", false)
			case "Write":
				time.Sleep(5 * time.Millisecond)
				return newToolResult(toolCall.ID, "Write", "written", false)
			default:
				return newToolResult(toolCall.ID, toolCall.Function.Name, "ok", false)
			}
		},
	})

	// Simulate streaming: tools arrive incrementally as model outputs them.
	exec.AddTool(makeToolCall("call_1", "Read", `{"file_path":"/tmp/a"}`), &schema.Message{Role: schema.Assistant})
	time.Sleep(2 * time.Millisecond) // simulate streaming delay
	exec.AddTool(makeToolCall("call_2", "Bash", `{"command":"ls"}`), &schema.Message{Role: schema.Assistant})
	time.Sleep(2 * time.Millisecond)
	exec.AddTool(makeToolCall("call_3", "Write", `{"file_path":"/tmp/b","content":"x"}`), &schema.Message{Role: schema.Assistant})
	exec.commit(context.Background())

	// Get all remaining results (blocks until all complete).
	results := exec.GetRemainingResults(false)

	// Verify all 3 results returned in order.
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if results[0].ToolCallID != "call_1" {
		t.Errorf("expected call_1 first, got %q", results[0].ToolCallID)
	}
	if results[1].ToolCallID != "call_2" {
		t.Errorf("expected call_2 second, got %q", results[1].ToolCallID)
	}
	if results[2].ToolCallID != "call_3" {
		t.Errorf("expected call_3 third, got %q", results[2].ToolCallID)
	}

	// Verify results are correct.
	if results[0].Result != "file contents" {
		t.Errorf("expected 'file contents', got %q", results[0].Result)
	}
	if results[1].Result != "command output" {
		t.Errorf("expected 'command output', got %q", results[1].Result)
	}
	if results[2].Result != "written" {
		t.Errorf("expected 'written', got %q", results[2].Result)
	}

	// Verify all tools were executed.
	mu.Lock()
	if len(executionOrder) != 3 {
		t.Errorf("expected 3 executions, got %d", len(executionOrder))
	}
	mu.Unlock()
}

func TestScenario_StreamingMidFailurePreservesPartial(t *testing.T) {
	// Scenario: First tool succeeds, second tool fails mid-stream,
	// third tool should still produce results (non-Bash failure doesn't cascade).
	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		Ctx:            context.Background(),
		MaxConcurrency: 5,
		IsConcurrencySafe: func(toolCall *schema.ToolCall) bool {
			return true
		},
		ExecuteWithContext: func(ctx context.Context, toolCall *schema.ToolCall) *ToolResult {
			switch toolCall.Function.Name {
			case "Read":
				return newToolResult(toolCall.ID, "Read", "success", false)
			case "WebFetch":
				// Non-Bash failure — should not cascade.
				return newToolResult(toolCall.ID, "WebFetch", "connection timeout", true)
			case "Grep":
				time.Sleep(10 * time.Millisecond)
				return newToolResult(toolCall.ID, "Grep", "grep results", false)
			}
			return newToolResult(toolCall.ID, toolCall.Function.Name, "ok", false)
		},
	})

	exec.AddTool(makeToolCall("call_1", "Read", `{"file_path":"/tmp/a"}`), &schema.Message{Role: schema.Assistant})
	exec.AddTool(makeToolCall("call_2", "WebFetch", `{"url":"http://example.com"}`), &schema.Message{Role: schema.Assistant})
	exec.AddTool(makeToolCall("call_3", "Grep", `{"pattern":"test"}`), &schema.Message{Role: schema.Assistant})
	exec.commit(context.Background())

	results := exec.GetRemainingResults(false)

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// First tool: success.
	if results[0].IsError {
		t.Error("expected Read to succeed")
	}

	// Second tool: error (non-Bash, so no sibling cancellation).
	if !results[1].IsError {
		t.Error("expected WebFetch to be error")
	}
	if results[1].Result != "connection timeout" {
		t.Errorf("expected 'connection timeout', got %q", results[1].Result)
	}

	// Third tool: should complete normally (non-Bash error doesn't cascade).
	if results[2].IsError {
		t.Errorf("expected Grep to succeed, got error: %q", results[2].Result)
	}
	if results[2].Result != "grep results" {
		t.Errorf("expected 'grep results', got %q", results[2].Result)
	}
}

func TestScenario_VerifyThenNormalizePipeline(t *testing.T) {
	// Scenario: Full verify → normalize pipeline for multiple tools.
	verifyConfig := OutputVerificationConfig{
		Mode: VerifyModeError,
		Schemas: map[string]OutputSchema{
			"Read": {Type: "json", RequiredFields: []string{"content"}},
			"Bash": {Type: "text"},
			"Grep": {Type: "text", MaxSize: 50},
		},
	}
	normConfig := ResultNormalizationConfig{
		MaxResultSize:         100,
		TruncationPreviewSize: 20,
	}

	tests := []struct {
		name       string
		toolName   string
		result     string
		isError    bool
		wantError  bool
		wantSubstr string
	}{
		{
			name:       "valid JSON with required fields",
			toolName:   "Read",
			result:     `{"content":"hello world"}`,
			isError:    false,
			wantError:  false,
			wantSubstr: `{"content":"hello world"}`,
		},
		{
			name:       "invalid JSON triggers verification error",
			toolName:   "Read",
			result:     "not json",
			isError:    false,
			wantError:  true,
			wantSubstr: "Output verification failed",
		},
		{
			name:       "valid text output",
			toolName:   "Bash",
			result:     "command output here",
			isError:    false,
			wantError:  false,
			wantSubstr: "command output here",
		},
		{
			name:       "empty text output",
			toolName:   "Bash",
			result:     "",
			isError:    false,
			wantError:  true,
			wantSubstr: "Output verification failed",
		},
		{
			name:       "oversized grep result",
			toolName:   "Grep",
			result:     strings.Repeat("x", 60),
			isError:    false,
			wantError:  true,
			wantSubstr: "exceeds max size",
		},
		{
			name:       "error result skips type check",
			toolName:   "Read",
			result:     "file not found",
			isError:    true,
			wantError:  true, // error pass-through
			wantSubstr: "file not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := VerifyAndNormalize(tt.toolName, tt.result, tt.isError, verifyConfig, normConfig)
			if result.IsError != tt.wantError {
				t.Errorf("expected IsError=%v, got IsError=%v (content=%q)", tt.wantError, result.IsError, result.Content)
			}
			if !strings.Contains(result.Content, tt.wantSubstr) {
				t.Errorf("expected content containing %q, got %q", tt.wantSubstr, result.Content)
			}
		})
	}
}

func TestScenario_RetryExhausted(t *testing.T) {
	// Scenario: all retries exhausted → error returned.
	attempts := 0
	_, err := CallModelWithRetry(
		context.Background(),
		RetryConfig{
			MaxRetries: 3,
			BaseDelay:  1 * time.Millisecond,
		},
		func(ctx context.Context, attempt int) (*CallModelResult, error) {
			attempts++
			return nil, fmt.Errorf("rate_limit_error: too many requests")
		},
		nil,
	)

	if err == nil {
		t.Fatal("expected error after exhausting retries")
		return
	}
	if !strings.Contains(err.Error(), "rate_limit_error") {
		t.Errorf("expected rate limit error, got %v", err)
	}
	// Should have attempted maxRetries + 1 (initial attempt + retries).
	if attempts != 4 {
		t.Errorf("expected 4 attempts, got %d", attempts)
	}
}

func TestScenario_NonTransientErrorNoRetry(t *testing.T) {
	// Scenario: non-transient error is not retried.
	attempts := 0
	_, err := CallModelWithRetry(
		context.Background(),
		RetryConfig{
			MaxRetries: 5,
			BaseDelay:  1 * time.Millisecond,
		},
		func(ctx context.Context, attempt int) (*CallModelResult, error) {
			attempts++
			return nil, fmt.Errorf("invalid_request_error: bad prompt")
		},
		nil,
	)

	if err == nil {
		t.Fatal("expected error")
		return
	}
	if attempts != 1 {
		t.Errorf("expected 1 attempt (no retry for non-transient), got %d", attempts)
	}
}

func TestScenario_ConcurrentExecutorBatchLifecycle(t *testing.T) {
	// Scenario: ConcurrentExecutor with submission, completion, and abort.
	ctx := context.Background()
	exec := NewConcurrentExecutor(ctx, 3)

	// Submit 5 calls.
	for i := 0; i < 5; i++ {
		call := &PendingToolCall{
			ID:        fmt.Sprintf("call_%d", i),
			Name:      "Read",
			Arguments: fmt.Sprintf(`{"file_path":"/tmp/file_%d"}`, i),
		}
		exec.Submit(call, func(ctx context.Context, name, args string) (string, error) {
			time.Sleep(10 * time.Millisecond)
			return "result for " + args, nil
		})
	}

	// Wait for all to complete.
	results := exec.Wait()

	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}

	completed := 0
	for _, call := range results {
		if call.Status == "completed" {
			completed++
		}
	}
	if completed != 5 {
		t.Errorf("expected 5 completed calls, got %d", completed)
	}
}

func TestScenario_ConcurrentExecutorAbort(t *testing.T) {
	// Scenario: abort all pending calls.
	ctx := context.Background()
	exec := NewConcurrentExecutor(ctx, 1) // only 1 at a time

	// Submit 5 calls (4 will be queued).
	for i := 0; i < 5; i++ {
		call := &PendingToolCall{
			ID:        fmt.Sprintf("call_%d", i),
			Name:      "Read",
			Arguments: fmt.Sprintf(`{"n":%d}`, i),
		}
		exec.Submit(call, func(ctx context.Context, name, args string) (string, error) {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(100 * time.Millisecond):
				return "done", nil
			}
		})
	}

	// Abort immediately.
	time.Sleep(5 * time.Millisecond) // let first one start
	exec.AbortAll()

	results := exec.Wait()
	aborted := 0
	for _, call := range results {
		if call.Status == "aborted" {
			aborted++
		}
	}
	// At least the queued ones should be aborted.
	if aborted < 3 {
		t.Errorf("expected at least 3 aborted calls, got %d", aborted)
	}
}
