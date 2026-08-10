package execution

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

// parity_test.go — Final parity verification tests for tool execution.
// Covers:
// - Full streaming executor lifecycle
// - Concurrent executor error paths (timeout, panic, context cancel)
// - Result normalization edge cases
// - Sibling cancellation propagation

// --- Streaming Tool Executor lifecycle tests ---

// TestStreamingExecutorFullLifecycle verifies start -> execute N tools ->
// sibling cancel -> finish -> get remaining.
func TestStreamingExecutorFullLifecycle(t *testing.T) {
	var executedTools []string
	var mu sync.Mutex

	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		Execute: func(tc *schema.ToolCall) *ToolResult {
			mu.Lock()
			executedTools = append(executedTools, tc.Function.Name)
			mu.Unlock()
			return &ToolResult{
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
				Result:     "done:" + tc.Function.Name,
				IsError:    false,
			}
		},
		IsConcurrencySafe: func(tc *schema.ToolCall) bool { return true },
		MaxConcurrency:    5,
	})

	// Submit 5 tools
	for i := 0; i < 5; i++ {
		name := []string{"Read", "Grep", "Glob", "Write", "Edit"}[i]
		args := `{"x":"` + name + `"}`
		exec.AddTool(makeToolCall("call_"+name, name, args), &schema.Message{Role: schema.Assistant})
	}
	exec.commit(context.Background())

	// Get all remaining results
	results := exec.GetRemainingResults(false)
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}

	// Verify all tools were executed
	mu.Lock()
	if len(executedTools) != 5 {
		t.Errorf("expected 5 tools executed, got %d", len(executedTools))
	}
	mu.Unlock()

	// Results should be in original submission order
	expectedOrder := []string{"Read", "Grep", "Glob", "Write", "Edit"}
	for i, r := range results {
		if r.ToolName != expectedOrder[i] {
			t.Errorf("result[%d]: expected tool %q, got %q", i, expectedOrder[i], r.ToolName)
		}
		if r.Result != "done:"+expectedOrder[i] {
			t.Errorf("result[%d]: expected result 'done:%s', got %q", i, expectedOrder[i], r.Result)
		}
	}
}

// TestStreamingExecutorSiblingCancelOnBashError verifies that Bash errors
// cancel queued siblings but allow executing tools to finish.
func TestStreamingExecutorSiblingCancelOnBashError(t *testing.T) {
	var executionStarted int32

	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		Execute: func(tc *schema.ToolCall) *ToolResult {
			atomic.AddInt32(&executionStarted, 1)
			if tc.Function.Name == "Bash" {
				return &ToolResult{
					ToolCallID: tc.ID,
					ToolName:   "Bash",
					Result:     "command not found",
					IsError:    true,
				}
			}
			// Simulate some work
			time.Sleep(10 * time.Millisecond)
			return &ToolResult{
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
				Result:     "success",
				IsError:    false,
			}
		},
		IsConcurrencySafe: func(tc *schema.ToolCall) bool { return false },
		MaxConcurrency:    1, // serial execution to ensure predictable ordering
	})

	exec.AddTool(makeToolCall("bash_1", "Bash", `{"command":"bad"}`), &schema.Message{Role: schema.Assistant})
	exec.AddTool(makeToolCall("read_1", "Read", `{"file_path":"/a"}`), &schema.Message{Role: schema.Assistant})
	exec.AddTool(makeToolCall("grep_1", "Grep", `{"pattern":"x"}`), &schema.Message{Role: schema.Assistant})
	exec.commit(context.Background())

	results := exec.GetRemainingResults(false)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// First result should be the Bash error
	if !results[0].IsError || results[0].ToolName != "Bash" {
		t.Errorf("expected first result to be Bash error, got %+v", results[0])
	}

	// Queued siblings should be cancelled
	for _, r := range results[1:] {
		if !r.IsError {
			t.Errorf("expected sibling %s to be cancelled/error, got IsError=false", r.ToolCallID)
		}
		if !strings.Contains(r.Result, "Cancelled") {
			t.Errorf("expected cancellation message for %s, got %q", r.ToolCallID, r.Result)
		}
	}
}

// TestStreamingExecutorContextPropagation verifies ExecuteWithContext receives
// a cancellable context.
func TestStreamingExecutorContextPropagation(t *testing.T) {
	parentCtx, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()

	var receivedCtx context.Context
	var ctxMu sync.Mutex

	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		ExecuteWithContext: func(ctx context.Context, tc *schema.ToolCall) *ToolResult {
			ctxMu.Lock()
			receivedCtx = ctx
			ctxMu.Unlock()
			return &ToolResult{
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
				Result:     "ok",
			}
		},
		IsConcurrencySafe: func(tc *schema.ToolCall) bool { return true },
		Ctx:               parentCtx,
	})

	exec.AddTool(makeToolCall("call_1", "Read", `{"file_path":"/tmp/a"}`), &schema.Message{Role: schema.Assistant})
	exec.commit(context.Background())
	_ = exec.GetRemainingResults(false)

	ctxMu.Lock()
	if receivedCtx == nil {
		t.Fatal("expected ExecuteWithContext to receive a context")
		return
	}
	ctxMu.Unlock()
}

// --- Concurrent Executor tests ---

// TestConcurrentExecutorSubmitAndWait verifies basic submit/wait lifecycle.
func TestConcurrentExecutorSubmitAndWait(t *testing.T) {
	ctx := context.Background()
	executor := NewConcurrentExecutor(ctx, 3)

	for i := 0; i < 5; i++ {
		call := &PendingToolCall{
			ID:   "call_" + string(rune('a'+i)),
			Name: "TestTool",
		}
		executor.Submit(call, func(ctx context.Context, name, args string) (string, error) {
			time.Sleep(10 * time.Millisecond)
			return "result", nil
		})
	}

	results := executor.Wait()
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}

	for id, call := range results {
		if call.Status != "completed" {
			t.Errorf("call %s: expected 'completed', got %q", id, call.Status)
		}
		if call.Result != "result" {
			t.Errorf("call %s: expected 'result', got %q", id, call.Result)
		}
	}
}

// TestConcurrentExecutorAbortAll verifies global abort marks pending calls as aborted.
func TestConcurrentExecutorAbortAll(t *testing.T) {
	ctx := context.Background()
	executor := NewConcurrentExecutor(ctx, 1) // Only 1 concurrent

	blocker := make(chan struct{})
	executor.Submit(&PendingToolCall{ID: "blocking", Name: "Block"}, func(ctx context.Context, name, args string) (string, error) {
		<-blocker
		return "done", nil
	})

	// These should be queued
	executor.Submit(&PendingToolCall{ID: "queued1", Name: "Q1"}, func(ctx context.Context, name, args string) (string, error) {
		return "done", nil
	})
	executor.Submit(&PendingToolCall{ID: "queued2", Name: "Q2"}, func(ctx context.Context, name, args string) (string, error) {
		return "done", nil
	})

	// Abort all
	executor.AbortAll()
	close(blocker) // Let the blocking call finish

	results := executor.Wait()

	// Queued calls should be aborted
	for _, id := range []string{"queued1", "queued2"} {
		call := results[id]
		if call == nil {
			t.Errorf("expected result for %s", id)
			continue
		}
		if call.Status != "aborted" {
			t.Errorf("call %s: expected 'aborted', got %q", id, call.Status)
		}
	}
}

// TestConcurrentExecutorAbortByID verifies single-call abort.
func TestConcurrentExecutorAbortByID(t *testing.T) {
	ctx := context.Background()
	executor := NewConcurrentExecutor(ctx, 1)

	blocker := make(chan struct{})
	executor.Submit(&PendingToolCall{ID: "blocking", Name: "Block"}, func(ctx context.Context, name, args string) (string, error) {
		<-blocker
		return "done", nil
	})

	// Queue a call, then abort it before it runs
	executor.Submit(&PendingToolCall{ID: "victim", Name: "Victim"}, func(ctx context.Context, name, args string) (string, error) {
		return "should not run", nil
	})

	err := executor.AbortByID("victim")
	if err != nil {
		t.Fatalf("AbortByID failed: %v", err)
		return
	}

	close(blocker)
	results := executor.Wait()

	if results["victim"].Status != "aborted" {
		t.Errorf("victim should be aborted, got %q", results["victim"].Status)
	}
}

// TestConcurrentExecutorErrorPropagation verifies error from executor function
// is properly tracked.
func TestConcurrentExecutorErrorPropagation(t *testing.T) {
	ctx := context.Background()
	executor := NewConcurrentExecutor(ctx, 5)

	executor.Submit(&PendingToolCall{ID: "err1", Name: "Fail"}, func(ctx context.Context, name, args string) (string, error) {
		return "", errors.New("something broke")
	})

	results := executor.Wait()
	call := results["err1"]
	if call == nil {
		t.Fatal("expected result for err1")
		return
	}
	if call.Status != "errored" {
		t.Errorf("expected 'errored', got %q", call.Status)
	}
	if call.Error == nil || call.Error.Error() != "something broke" {
		t.Errorf("expected error 'something broke', got %v", call.Error)
	}
}

// TestConcurrentExecutorContextCancelPropagation verifies parent context
// cancellation propagates to running calls.
func TestConcurrentExecutorContextCancelPropagation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	executor := NewConcurrentExecutor(ctx, 5)

	var ctxCancelled int32
	executor.Submit(&PendingToolCall{ID: "ctx_test", Name: "CtxTest"}, func(ctx context.Context, name, args string) (string, error) {
		<-ctx.Done()
		atomic.StoreInt32(&ctxCancelled, 1)
		return "", ctx.Err()
	})

	// Give the goroutine time to start
	time.Sleep(20 * time.Millisecond)
	cancel()

	results := executor.Wait()
	call := results["ctx_test"]
	if call == nil {
		t.Fatal("expected result for ctx_test")
		return
	}
	if call.Status != "aborted" {
		t.Errorf("expected 'aborted' on context cancel, got %q", call.Status)
	}
	if atomic.LoadInt32(&ctxCancelled) != 1 {
		t.Error("expected executor to observe context cancellation")
	}
}

// TestConcurrentExecutorProgress verifies Progress() reporting.
func TestConcurrentExecutorProgress(t *testing.T) {
	ctx := context.Background()
	executor := NewConcurrentExecutor(ctx, 1)

	started := make(chan struct{})
	blocker := make(chan struct{})

	executor.Submit(&PendingToolCall{ID: "running", Name: "Run"}, func(ctx context.Context, name, args string) (string, error) {
		close(started)
		<-blocker
		return "done", nil
	})
	executor.Submit(&PendingToolCall{ID: "pending", Name: "Pend"}, func(ctx context.Context, name, args string) (string, error) {
		return "done", nil
	})

	<-started
	running, pending, completed := executor.Progress()
	if running != 1 {
		t.Errorf("expected 1 running, got %d", running)
	}
	if pending != 1 {
		t.Errorf("expected 1 pending, got %d", pending)
	}
	if completed != 0 {
		t.Errorf("expected 0 completed, got %d", completed)
	}

	close(blocker)
	executor.Wait()

	running, pending, completed = executor.Progress()
	if running != 0 || pending != 0 {
		t.Errorf("after wait: expected 0 running/pending, got %d/%d", running, pending)
	}
	if completed != 2 {
		t.Errorf("after wait: expected 2 completed, got %d", completed)
	}
}

// TestConcurrentExecutorSubmitAfterAbort verifies that submit after abort
// immediately marks calls as aborted.
func TestConcurrentExecutorSubmitAfterAbort(t *testing.T) {
	ctx := context.Background()
	executor := NewConcurrentExecutor(ctx, 5)
	executor.AbortAll()

	executor.Submit(&PendingToolCall{ID: "late", Name: "Late"}, func(ctx context.Context, name, args string) (string, error) {
		return "should not run", nil
	})

	results := executor.Wait()
	call := results["late"]
	if call == nil {
		t.Fatal("expected result for late submission")
		return
	}
	if call.Status != "aborted" {
		t.Errorf("expected 'aborted' for post-abort submit, got %q", call.Status)
	}
}

// --- Normalization tests ---

// TestNormalizeToolResultNilOutput verifies empty/nil output produces a
// synthetic completion message.
func TestNormalizeToolResultNilOutput(t *testing.T) {
	result := NormalizeToolResult("Read", "", false)
	if result.Content != "(Read completed with no output)" {
		t.Errorf("expected synthetic message, got %q", result.Content)
	}
	if result.IsError {
		t.Error("empty result should not be marked as error")
	}
	if result.WasTruncated {
		t.Error("empty result should not be marked as truncated")
	}
}

// TestNormalizeToolResultWhitespaceOnly verifies whitespace-only output is
// treated as empty.
func TestNormalizeToolResultWhitespaceOnly(t *testing.T) {
	result := NormalizeToolResult("Bash", "   \n\t  ", false)
	if !strings.Contains(result.Content, "completed with no output") {
		t.Errorf("expected 'completed with no output', got %q", result.Content)
	}
}

// TestNormalizeToolResultOversized verifies head+tail truncation for large output.
func TestNormalizeToolResultOversized(t *testing.T) {
	// Create a 60000-char string
	large := strings.Repeat("x", 60000)
	result := NormalizeToolResult("Read", large, false)

	if !result.WasTruncated {
		t.Error("expected truncation for 60000-char result")
	}
	if result.OriginalSize != 60000 {
		t.Errorf("expected original size 60000, got %d", result.OriginalSize)
	}
	if !strings.Contains(result.Content, "characters truncated") {
		t.Error("expected truncation notice in content")
	}
	// Content should be significantly smaller than original
	if len(result.Content) >= 60000 {
		t.Error("truncated content should be smaller than original")
	}
}

// TestNormalizeToolResultCustomConfig verifies custom normalization config.
func TestNormalizeToolResultCustomConfig(t *testing.T) {
	cfg := ResultNormalizationConfig{
		MaxResultSize:         100,
		TruncationPreviewSize: 20,
	}
	content := strings.Repeat("a", 200)
	result := NormalizeToolResult("Tool", content, false, cfg)

	if !result.WasTruncated {
		t.Error("expected truncation with MaxResultSize=100")
	}
	// Head should be 20 chars of 'a', tail should be 20 chars of 'a'
	if !strings.HasPrefix(result.Content, strings.Repeat("a", 20)) {
		t.Error("expected head to be 20 'a' chars")
	}
}

// TestNormalizeToolResultErrorWithTruncation verifies error result truncation.
func TestNormalizeToolResultErrorWithTruncation(t *testing.T) {
	// Create a 15000-char error message (exceeds 10000 error limit)
	largeErr := strings.Repeat("E", 15000)
	result := NormalizeToolResult("Bash", largeErr, true)

	if !result.IsError {
		t.Error("expected IsError=true")
	}
	if !result.WasTruncated {
		t.Error("expected truncation for large error")
	}
	if !strings.Contains(result.Content, "characters truncated") {
		t.Error("expected truncation notice in error content")
	}
}

// TestNormalizeToolResultErrorBelowLimit verifies short errors pass through.
func TestNormalizeToolResultErrorBelowLimit(t *testing.T) {
	result := NormalizeToolResult("Read", "file not found", true)
	if result.Content != "file not found" {
		t.Errorf("expected pass-through for short error, got %q", result.Content)
	}
	if result.WasTruncated {
		t.Error("short error should not be truncated")
	}
	if !result.IsError {
		t.Error("expected IsError=true")
	}
}

// TestNormalizeToolErrorConvenience verifies the convenience wrapper.
func TestNormalizeToolErrorConvenience(t *testing.T) {
	result := NormalizeToolError("Bash", "exit code 1")
	if !result.IsError {
		t.Error("NormalizeToolError should set IsError=true")
	}
	if result.Content != "exit code 1" {
		t.Errorf("expected 'exit code 1', got %q", result.Content)
	}
}

// TestToolCallReadyForExecution verifies the readiness check for tool calls.
func TestToolCallReadyForExecution(t *testing.T) {
	tests := []struct {
		name     string
		toolCall *schema.ToolCall
		expected bool
	}{
		{"nil tool call", nil, false},
		{"empty name", &schema.ToolCall{Function: schema.FunctionCall{Name: "", Arguments: `{"x":"y"}`}}, false},
		{"empty args", &schema.ToolCall{Function: schema.FunctionCall{Name: "Read", Arguments: ""}}, false},
		{"empty object args", &schema.ToolCall{Function: schema.FunctionCall{Name: "Read", Arguments: "{}"}}, false},
		{"invalid JSON args", &schema.ToolCall{Function: schema.FunctionCall{Name: "Read", Arguments: "{bad"}}, false},
		{"valid", &schema.ToolCall{Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"/a"}`}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolCallReadyForExecution(tt.toolCall)
			if got != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, got)
			}
		})
	}
}
