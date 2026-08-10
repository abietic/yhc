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
// Result Normalization Tests
// ============================================================================

func TestNormalizeToolResult_EmptyResult(t *testing.T) {
	result := NormalizeToolResult("Read", "", false)
	if result.Content != "(Read completed with no output)" {
		t.Errorf("expected empty result injection, got %q", result.Content)
	}
	if result.IsError {
		t.Error("expected non-error")
	}
	if result.WasTruncated {
		t.Error("expected not truncated")
	}
}

func TestNormalizeToolResult_WhitespaceOnlyResult(t *testing.T) {
	result := NormalizeToolResult("Bash", "   \n\t  ", false)
	if result.Content != "(Bash completed with no output)" {
		t.Errorf("expected empty result injection for whitespace, got %q", result.Content)
	}
}

func TestNormalizeToolResult_NormalResult(t *testing.T) {
	content := "file contents here"
	result := NormalizeToolResult("Read", content, false)
	if result.Content != content {
		t.Errorf("expected pass-through, got %q", result.Content)
	}
	if result.IsError {
		t.Error("expected non-error")
	}
	if result.WasTruncated {
		t.Error("expected not truncated")
	}
	if result.OriginalSize != 18 {
		t.Errorf("expected OriginalSize=18, got %d", result.OriginalSize)
	}
}

func TestNormalizeToolResult_OversizedResult(t *testing.T) {
	// Use a small config to make testing tractable.
	cfg := ResultNormalizationConfig{
		MaxResultSize:         100,
		TruncationPreviewSize: 20,
	}
	content := strings.Repeat("x", 200)
	result := NormalizeToolResult("Read", content, false, cfg)
	if !result.WasTruncated {
		t.Error("expected truncation")
	}
	if !strings.Contains(result.Content, "characters truncated") {
		t.Errorf("expected truncation notice, got %q", result.Content)
	}
	// The head and tail should each be 20 chars.
	if !strings.HasPrefix(result.Content, "xxxxxxxxxxxxxxxxxxxx") {
		t.Error("expected head to start with 20 x's")
	}
	if !strings.HasSuffix(result.Content, "xxxxxxxxxxxxxxxxxxxx") {
		t.Error("expected tail to end with 20 x's")
	}
}

func TestNormalizeToolResult_ErrorResult(t *testing.T) {
	result := NormalizeToolResult("Bash", "command not found", true)
	if result.Content != "command not found" {
		t.Errorf("expected error pass-through, got %q", result.Content)
	}
	if !result.IsError {
		t.Error("expected is_error=true")
	}
	if result.WasTruncated {
		t.Error("expected not truncated")
	}
}

func TestNormalizeToolResult_LongError(t *testing.T) {
	// Error truncation uses 10000 char limit with 5000 head + 5000 tail.
	longError := strings.Repeat("e", 15000)
	result := NormalizeToolResult("Bash", longError, true)
	if !result.WasTruncated {
		t.Error("expected error truncation")
	}
	if !strings.Contains(result.Content, "characters truncated") {
		t.Error("expected truncation notice in error")
	}
	if result.OriginalSize != 15000 {
		t.Errorf("expected OriginalSize=15000, got %d", result.OriginalSize)
	}
}

func TestNormalizeToolResult_Deterministic(t *testing.T) {
	// Same input produces same output.
	cfg := ResultNormalizationConfig{
		MaxResultSize:         50,
		TruncationPreviewSize: 10,
	}
	content := strings.Repeat("a", 100)
	r1 := NormalizeToolResult("Read", content, false, cfg)
	r2 := NormalizeToolResult("Read", content, false, cfg)
	if r1.Content != r2.Content {
		t.Error("expected deterministic results")
	}
}

// ============================================================================
// Tool Batch Pipeline Tests
// ============================================================================

func TestExecuteToolBatch_EmptyBatch(t *testing.T) {
	results := ExecuteToolBatch(context.Background(), nil, ToolBatchConfig{})
	if results != nil {
		t.Errorf("expected nil for empty batch, got %v", results)
	}
}

func TestExecuteToolBatch_SingleTool(t *testing.T) {
	item := ToolBatchItem{
		ToolCall: &schema.ToolCall{
			ID:   "call_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "Read",
				Arguments: `{"file_path":"/tmp/a"}`,
			},
		},
		Execute: func(ctx context.Context) (string, error) {
			return "file contents", nil
		},
	}

	results := ExecuteToolBatch(context.Background(), []ToolBatchItem{item}, ToolBatchConfig{})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ToolCallID != "call_1" {
		t.Errorf("expected tool_call_id 'call_1', got %q", results[0].ToolCallID)
	}
	if results[0].Normalized.Content != "file contents" {
		t.Errorf("expected 'file contents', got %q", results[0].Normalized.Content)
	}
	if results[0].Normalized.IsError {
		t.Error("expected non-error")
	}
}

func TestExecuteToolBatch_ConcurrencyLimit(t *testing.T) {
	const numTools = 10
	const maxConcurrency = 3

	var peak int32
	var current int32

	items := make([]ToolBatchItem, numTools)
	for i := 0; i < numTools; i++ {
		id := fmt.Sprintf("call_%d", i)
		items[i] = ToolBatchItem{
			ToolCall: &schema.ToolCall{
				ID:   id,
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path":"/tmp"}`,
				},
			},
			Execute: func(ctx context.Context) (string, error) {
				cur := atomic.AddInt32(&current, 1)
				// Atomically update peak.
				for {
					p := atomic.LoadInt32(&peak)
					if cur <= p {
						break
					}
					if atomic.CompareAndSwapInt32(&peak, p, cur) {
						break
					}
				}
				time.Sleep(10 * time.Millisecond) // simulate work
				atomic.AddInt32(&current, -1)
				return "ok", nil
			},
		}
	}

	results := ExecuteToolBatch(context.Background(), items, ToolBatchConfig{
		MaxConcurrency: maxConcurrency,
	})

	if len(results) != numTools {
		t.Fatalf("expected %d results, got %d", numTools, len(results))
	}

	observedPeak := atomic.LoadInt32(&peak)
	if observedPeak > int32(maxConcurrency) {
		t.Errorf("peak concurrency %d exceeded limit %d", observedPeak, maxConcurrency)
	}
	if observedPeak == 0 {
		t.Error("peak concurrency was 0 — tools did not run")
	}

	// Verify all results are successful.
	for i, r := range results {
		if r.Normalized.IsError {
			t.Errorf("result[%d] unexpected error: %s", i, r.Normalized.Content)
		}
	}
}

func TestExecuteToolBatch_SiblingCancellation(t *testing.T) {
	// Tool 0 is a Bash tool that errors. Tools 1-3 should be cancelled.
	items := []ToolBatchItem{
		{
			ToolCall: &schema.ToolCall{
				ID:   "call_bash",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Bash",
					Arguments: `{"command":"exit 1"}`,
				},
			},
			Execute: func(ctx context.Context) (string, error) {
				return "", fmt.Errorf("exit code 1")
			},
		},
		{
			ToolCall: &schema.ToolCall{
				ID:   "call_read_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path":"/tmp/a"}`,
				},
			},
			Execute: func(ctx context.Context) (string, error) {
				// Wait for context cancellation.
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(5 * time.Second):
					return "should not reach here", nil
				}
			},
		},
		{
			ToolCall: &schema.ToolCall{
				ID:   "call_read_2",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path":"/tmp/b"}`,
				},
			},
			Execute: func(ctx context.Context) (string, error) {
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(5 * time.Second):
					return "should not reach here", nil
				}
			},
		},
	}

	cfg := ToolBatchConfig{
		MaxConcurrency:    10, // all start immediately
		CancelOnBashError: true,
	}

	start := time.Now()
	results := ExecuteToolBatch(context.Background(), items, cfg)
	elapsed := time.Since(start)

	// Should complete quickly (not wait 5 seconds).
	if elapsed > 2*time.Second {
		t.Errorf("took too long (%v) — sibling cancellation may not have fired", elapsed)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// First result: Bash error.
	if !results[0].Normalized.IsError {
		t.Error("expected Bash result to be an error")
	}

	// Remaining results: should be cancelled or errored.
	for i := 1; i < len(results); i++ {
		if !results[i].Normalized.IsError && !results[i].Cancelled {
			t.Errorf("result[%d] expected cancelled/error, got success: %q", i, results[i].Normalized.Content)
		}
	}
}

func TestExecuteToolBatch_NonBashErrorDoesNotCancel(t *testing.T) {
	// A Read error should NOT cancel siblings.
	items := []ToolBatchItem{
		{
			ToolCall: &schema.ToolCall{
				ID:   "call_read_err",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path":"/nonexistent"}`,
				},
			},
			Execute: func(ctx context.Context) (string, error) {
				return "", fmt.Errorf("file not found")
			},
		},
		{
			ToolCall: &schema.ToolCall{
				ID:   "call_read_ok",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path":"/tmp/a"}`,
				},
			},
			Execute: func(ctx context.Context) (string, error) {
				time.Sleep(20 * time.Millisecond)
				return "success", nil
			},
		},
	}

	results := ExecuteToolBatch(context.Background(), items, ToolBatchConfig{
		MaxConcurrency:    10,
		CancelOnBashError: true,
	})

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// Second tool should succeed.
	if results[1].Normalized.IsError {
		t.Errorf("expected second tool to succeed, got error: %s", results[1].Normalized.Content)
	}
	if results[1].Cancelled {
		t.Error("expected second tool not to be cancelled")
	}
}

func TestExecuteToolBatch_HookOrdering(t *testing.T) {
	var mu sync.Mutex
	var events []string

	items := []ToolBatchItem{
		{
			ToolCall: &schema.ToolCall{
				ID:   "call_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path":"/tmp/a"}`,
				},
			},
			Execute: func(ctx context.Context) (string, error) {
				mu.Lock()
				events = append(events, "execute:call_1")
				mu.Unlock()
				return "ok", nil
			},
		},
	}

	cfg := ToolBatchConfig{
		MaxConcurrency: 1,
		PreToolHook: func(ctx context.Context, toolName, toolCallID string) error {
			mu.Lock()
			events = append(events, fmt.Sprintf("pre:%s:%s", toolName, toolCallID))
			mu.Unlock()
			return nil
		},
		PostToolHook: func(ctx context.Context, toolName, toolCallID, result string, isError bool) {
			mu.Lock()
			events = append(events, fmt.Sprintf("post:%s:%s", toolName, toolCallID))
			mu.Unlock()
		},
	}

	ExecuteToolBatch(context.Background(), items, cfg)

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d: %v", len(events), events)
	}
	if events[0] != "pre:Read:call_1" {
		t.Errorf("expected pre hook first, got %q", events[0])
	}
	if events[1] != "execute:call_1" {
		t.Errorf("expected execute second, got %q", events[1])
	}
	if events[2] != "post:Read:call_1" {
		t.Errorf("expected post hook third, got %q", events[2])
	}
}

func TestExecuteToolBatch_PreHookDenial(t *testing.T) {
	items := []ToolBatchItem{
		{
			ToolCall: &schema.ToolCall{
				ID:   "call_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Bash",
					Arguments: `{"command":"rm -rf /"}`,
				},
			},
			Execute: func(ctx context.Context) (string, error) {
				t.Error("execute should not be called when pre-hook denies")
				return "", nil
			},
		},
	}

	cfg := ToolBatchConfig{
		PreToolHook: func(ctx context.Context, toolName, toolCallID string) error {
			return fmt.Errorf("permission denied for %s", toolName)
		},
	}

	results := ExecuteToolBatch(context.Background(), items, cfg)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].Normalized.IsError {
		t.Error("expected error when pre-hook denies")
	}
	if !strings.Contains(results[0].Normalized.Content, "permission denied") {
		t.Errorf("expected permission denied message, got %q", results[0].Normalized.Content)
	}
}

func TestExecuteToolBatch_HookPanicRecovery(t *testing.T) {
	var hookErrors []string
	var mu sync.Mutex

	items := []ToolBatchItem{
		{
			ToolCall: &schema.ToolCall{
				ID:   "call_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path":"/tmp"}`,
				},
			},
			Execute: func(ctx context.Context) (string, error) {
				return "ok", nil
			},
		},
	}

	cfg := ToolBatchConfig{
		PreToolHook: func(ctx context.Context, toolName, toolCallID string) error {
			panic("pre-hook exploded")
		},
		PostToolHook: func(ctx context.Context, toolName, toolCallID, result string, isError bool) {
			panic("post-hook exploded")
		},
		OnHookError: func(hookPhase, toolName string, err error) {
			mu.Lock()
			hookErrors = append(hookErrors, fmt.Sprintf("%s:%s:%v", hookPhase, toolName, err))
			mu.Unlock()
		},
	}

	results := ExecuteToolBatch(context.Background(), items, cfg)

	// Tool should still complete despite hook panics.
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Normalized.IsError {
		t.Errorf("expected success despite hook panics, got error: %s", results[0].Normalized.Content)
	}
	if results[0].Normalized.Content != "ok" {
		t.Errorf("expected 'ok', got %q", results[0].Normalized.Content)
	}

	// Hook errors should have been reported.
	mu.Lock()
	defer mu.Unlock()
	if len(hookErrors) != 2 {
		t.Errorf("expected 2 hook errors reported, got %d: %v", len(hookErrors), hookErrors)
	}
}

func TestExecuteToolBatch_ResultOrder(t *testing.T) {
	// Results should be returned in the same order as input items,
	// regardless of completion order.
	items := make([]ToolBatchItem, 5)
	for i := 0; i < 5; i++ {
		items[i] = ToolBatchItem{
			ToolCall: &schema.ToolCall{
				ID:   fmt.Sprintf("call_%d", i),
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path":"/tmp"}`,
				},
			},
			Execute: func(ctx context.Context) (string, error) {
				// Stagger completion times (reverse order).
				time.Sleep(time.Duration(5-i) * 5 * time.Millisecond)
				return fmt.Sprintf("result_%d", i), nil
			},
		}
	}

	results := ExecuteToolBatch(context.Background(), items, ToolBatchConfig{
		MaxConcurrency: 10,
	})

	for i, r := range results {
		expected := fmt.Sprintf("result_%d", i)
		if r.Normalized.Content != expected {
			t.Errorf("results[%d] expected %q, got %q", i, expected, r.Normalized.Content)
		}
		expectedID := fmt.Sprintf("call_%d", i)
		if r.ToolCallID != expectedID {
			t.Errorf("results[%d] expected ID %q, got %q", i, expectedID, r.ToolCallID)
		}
	}
}

func TestExecuteToolBatch_NilExecutor(t *testing.T) {
	items := []ToolBatchItem{
		{
			ToolCall: &schema.ToolCall{
				ID:   "call_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{}`,
				},
			},
			Execute: nil,
		},
	}

	results := ExecuteToolBatch(context.Background(), items, ToolBatchConfig{})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].Normalized.IsError {
		t.Error("expected error for nil executor")
	}
	if !strings.Contains(results[0].Normalized.Content, "no executor") {
		t.Errorf("expected 'no executor' message, got %q", results[0].Normalized.Content)
	}
}

func TestExecuteToolBatch_ContextAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	items := []ToolBatchItem{
		{
			ToolCall: &schema.ToolCall{
				ID:   "call_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{}`,
				},
			},
			Execute: func(ctx context.Context) (string, error) {
				return "", ctx.Err()
			},
		},
	}

	results := ExecuteToolBatch(ctx, items, ToolBatchConfig{})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	// Should handle gracefully (either cancelled or error from ctx.Err).
	if !results[0].Normalized.IsError && !results[0].Cancelled {
		t.Error("expected error or cancellation when parent context is cancelled")
	}
}

// ============================================================================
// Streaming Tool Executor - Sibling Cancellation Tests
// ============================================================================

func TestStreamingToolExecutor_SiblingCancellationViaContext(t *testing.T) {
	var executedTools sync.Map
	bashDone := make(chan struct{})

	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		Ctx:            context.Background(),
		MaxConcurrency: 10,
		IsConcurrencySafe: func(toolCall *schema.ToolCall) bool {
			return true
		},
		ExecuteWithContext: func(ctx context.Context, toolCall *schema.ToolCall) *ToolResult {
			name := toolCall.Function.Name
			if name == "Bash" {
				result := newToolResult(toolCall.ID, name, "exit code 1", true)
				close(bashDone)
				return result
			}
			// Other tools wait for context cancellation.
			select {
			case <-ctx.Done():
				executedTools.Store(name, "cancelled")
				return newToolResult(toolCall.ID, name, "context cancelled", true)
			case <-time.After(5 * time.Second):
				executedTools.Store(name, "completed")
				return newToolResult(toolCall.ID, name, "done", false)
			}
		},
	})

	// Add tools: Bash will error, Read should be cancelled via context.
	exec.AddTool(makeToolCall("call_bash", "Bash", `{"command":"fail"}`), &schema.Message{Role: schema.Assistant})
	exec.AddTool(makeToolCall("call_read", "Read", `{"file_path":"/tmp"}`), &schema.Message{Role: schema.Assistant})
	exec.commit(context.Background())

	// Wait for all results.
	start := time.Now()
	results := exec.GetRemainingResults(false)
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Errorf("took too long (%v) — context cancellation may not have propagated", elapsed)
	}

	// We should have 2 results.
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// First result: Bash error.
	if !results[0].IsError {
		t.Error("expected Bash result to be error")
	}

	// Second result: should be error (cancelled by sibling).
	if !results[1].IsError {
		t.Error("expected Read result to be error due to sibling cancellation")
	}
}

func TestStreamingToolExecutor_MaxConcurrencyRespected(t *testing.T) {
	const maxConcurrency = 2
	const numTools = 6
	var peak int32
	var current int32

	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		Ctx:            context.Background(),
		MaxConcurrency: maxConcurrency,
		IsConcurrencySafe: func(toolCall *schema.ToolCall) bool {
			return true
		},
		ExecuteWithContext: func(ctx context.Context, toolCall *schema.ToolCall) *ToolResult {
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
			time.Sleep(10 * time.Millisecond)
			atomic.AddInt32(&current, -1)
			return newToolResult(toolCall.ID, toolCall.Function.Name, "ok", false)
		},
	})

	for i := 0; i < numTools; i++ {
		tc := makeToolCall(fmt.Sprintf("call_%d", i), "Read", `{"file_path":"/tmp"}`)
		exec.AddTool(tc, &schema.Message{Role: schema.Assistant})
	}
	exec.commit(context.Background())

	results := exec.GetRemainingResults(false)
	if len(results) != numTools {
		t.Fatalf("expected %d results, got %d", numTools, len(results))
	}

	observedPeak := atomic.LoadInt32(&peak)
	if observedPeak > int32(maxConcurrency) {
		t.Errorf("peak concurrency %d exceeded limit %d", observedPeak, maxConcurrency)
	}
}
