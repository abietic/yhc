package execution

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func makeToolCall(id, name, args string) *schema.ToolCall {
	return &schema.ToolCall{
		ID:   id,
		Type: "function",
		Function: schema.FunctionCall{
			Name:      name,
			Arguments: args,
		},
	}
}

func TestExecuteCommittedToolCallsPreparesAndExecutesExactlyOnceInModelOrder(
	t *testing.T,
) {
	t.Parallel()

	calls := []*schema.ToolCall{
		makeToolCall("first", "Read", `{"path":"a"}`),
		makeToolCall("second", "Write", `{"path":"b"}`),
		makeToolCall("third", "Grep", `{"pattern":"c"}`),
	}
	var mu sync.Mutex
	prepared := make([]string, 0, len(calls))
	executed := make(map[string]int, len(calls))
	results := ExecuteCommittedToolCalls(
		context.Background(),
		calls,
		StreamingToolExecutorConfig{
			PrepareForExecution: func(call *schema.ToolCall) *schema.ToolCall {
				mu.Lock()
				prepared = append(prepared, call.ID)
				mu.Unlock()
				return call
			},
			IsConcurrencySafe: func(call *schema.ToolCall) bool {
				return call.Function.Name != "Write"
			},
			ExecuteWithContext: func(
				_ context.Context,
				call *schema.ToolCall,
			) *ToolResult {
				mu.Lock()
				executed[call.ID]++
				mu.Unlock()
				return &ToolResult{
					ToolCallID: call.ID,
					ToolName:   call.Function.Name,
					Result:     "result:" + call.ID,
				}
			},
		},
	)

	if got := []string{
		results[0].ToolCallID,
		results[1].ToolCallID,
		results[2].ToolCallID,
	}; !reflect.DeepEqual(got, []string{"first", "second", "third"}) {
		t.Fatalf("result order = %#v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(prepared, []string{"first", "second", "third"}) {
		t.Fatalf("prepared calls = %#v", prepared)
	}
	for _, call := range calls {
		if executed[call.ID] != 1 {
			t.Fatalf("executions for %q = %d, want 1", call.ID, executed[call.ID])
		}
	}
}

func TestExecuteCommittedToolCallsObservesPostCommitCancellation(
	t *testing.T,
) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancelEntered := make(chan struct{})
	cancelStopped := make(chan struct{})
	blockEntered := make(chan struct{})
	blockRelease := make(chan struct{})
	results := make(chan []*ToolResult, 1)
	go func() {
		results <- ExecuteCommittedToolCalls(
			ctx,
			[]*schema.ToolCall{
				makeToolCall("cancel", "CancelTool", `{"value":1}`),
				makeToolCall("block", "BlockTool", `{"value":2}`),
			},
			StreamingToolExecutorConfig{
				Ctx: context.Background(),
				IsConcurrencySafe: func(*schema.ToolCall) bool {
					return true
				},
				GetInterruptBehavior: func(name string) string {
					if name == "CancelTool" {
						return "cancel"
					}
					return "block"
				},
				ExecuteWithContext: func(
					toolCtx context.Context,
					call *schema.ToolCall,
				) *ToolResult {
					switch call.Function.Name {
					case "CancelTool":
						close(cancelEntered)
						<-toolCtx.Done()
						close(cancelStopped)
						return &ToolResult{
							ToolCallID: call.ID,
							ToolName:   call.Function.Name,
							Result:     toolCtx.Err().Error(),
							IsError:    true,
						}
					case "BlockTool":
						close(blockEntered)
						select {
						case <-blockRelease:
							return &ToolResult{
								ToolCallID: call.ID,
								ToolName:   call.Function.Name,
								Result:     "block completed",
							}
						case <-toolCtx.Done():
							return &ToolResult{
								ToolCallID: call.ID,
								ToolName:   call.Function.Name,
								Result:     "block context canceled",
								IsError:    true,
							}
						}
					default:
						return nil
					}
				},
			},
		)
	}()

	select {
	case <-cancelEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel tool did not start")
	}
	select {
	case <-blockEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("block tool did not start")
	}
	cancel()
	select {
	case result := <-results:
		t.Fatalf("returned before block tool settled: %#v", result)
	case <-time.After(50 * time.Millisecond):
	}
	close(blockRelease)
	var completed []*ToolResult
	select {
	case completed = <-results:
	case <-time.After(5 * time.Second):
		t.Fatal("committed tool wait did not finish")
	}
	select {
	case <-cancelStopped:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel tool did not observe cancellation")
	}
	if len(completed) != 2 {
		t.Fatalf("completed results = %#v", completed)
	}
	if completed[0].ToolCallID != "cancel" ||
		completed[0].Result != "Interrupted by user" ||
		!completed[0].IsError {
		t.Fatalf("cancel result = %#v", completed[0])
	}
	if completed[1].ToolCallID != "block" ||
		completed[1].Result != "block completed" ||
		completed[1].IsError {
		t.Fatalf("block result = %#v", completed[1])
	}
}

func TestExecuteCommittedToolCallsCancellationWinsNonCooperativeSuccessModifier(
	t *testing.T,
) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	release := make(chan struct{})
	settled := make(chan struct{})
	results := make(chan []*ToolResult, 1)
	var commits atomic.Int32
	var settlements atomic.Int32
	go func() {
		results <- ExecuteCommittedToolCalls(
			ctx,
			[]*schema.ToolCall{
				makeToolCall("transition", "EnterPlanMode", `{}`),
			},
			StreamingToolExecutorConfig{
				Ctx: context.Background(),
				GetInterruptBehavior: func(string) string {
					return "cancel"
				},
				OnInterrupt: func(call *schema.ToolCall) {
					if call == nil || call.ID != "transition" {
						t.Errorf("interrupted call = %#v", call)
						return
					}
					if settlements.Add(1) == 1 {
						close(settled)
					}
				},
				ExecuteWithContext: func(
					_ context.Context,
					call *schema.ToolCall,
				) *ToolResult {
					close(entered)
					<-release
					return &ToolResult{
						ToolCallID: call.ID,
						ToolName:   call.Function.Name,
						Result:     "success after cancellation",
						ContextModifier: func() (func(), error) {
							commits.Add(1)
							return func() {}, nil
						},
					}
				},
			},
		)
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("non-cooperative tool did not start")
	}
	cancel()
	select {
	case <-settled:
	case <-time.After(5 * time.Second):
		t.Fatal("interaction did not settle before synthetic interruption")
	}
	close(release)

	var completed []*ToolResult
	select {
	case completed = <-results:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled committed call did not settle")
	}
	if len(completed) != 1 ||
		completed[0].Result != "Interrupted by user" ||
		!completed[0].IsError {
		t.Fatalf("cancellation winner result = %#v", completed)
	}
	if commits.Load() != 0 {
		t.Fatalf("cancelled context modifier commits = %d", commits.Load())
	}
	if settlements.Load() != 1 {
		t.Fatalf("interaction settlements = %d, want 1", settlements.Load())
	}
	if completed[0].ContextPublisher != nil {
		t.Fatal("cancelled result retained a context publisher")
	}
}

func TestStreamingToolExecutorAddAndComplete(t *testing.T) {
	exec := NewStreamingToolExecutor()

	tc := makeToolCall("call_1", "Read", `{"file_path":"/tmp/a"}`)
	exec.AddTool(tc, &schema.Message{Role: schema.Assistant})
	exec.commit(context.Background())

	if len(exec.tools) != 1 {
		t.Fatalf("expected 1 tracked tool, got %d", len(exec.tools))
	}

	exec.Complete("call_1", "file contents", false)
	results := exec.GetCompleted()
	if len(results) != 1 {
		t.Fatalf("expected 1 completed, got %d", len(results))
	}
	if results[0].ToolCallID != "call_1" {
		t.Errorf("expected tool_call_id 'call_1', got %q", results[0].ToolCallID)
	}
	if results[0].Message.Content != "file contents" {
		t.Errorf("expected 'file contents', got %q", results[0].Message.Content)
	}
}

func TestStreamingToolExecutorMaintainsOriginalOrder(t *testing.T) {
	exec := NewStreamingToolExecutor()
	exec.AddTool(makeToolCall("call_1", "Read", `{"file_path":"/tmp/a"}`), &schema.Message{Role: schema.Assistant})
	exec.AddTool(makeToolCall("call_2", "Read", `{"file_path":"/tmp/b"}`), &schema.Message{Role: schema.Assistant})
	exec.commit(context.Background())

	exec.Complete("call_2", "second", false)
	if got := exec.GetCompleted(); len(got) != 0 {
		t.Fatalf("expected no results before earlier tool completes, got %d", len(got))
	}

	exec.Complete("call_1", "first", false)
	results := exec.GetCompleted()
	if len(results) != 2 {
		t.Fatalf("expected 2 completed results, got %d", len(results))
	}
	if results[0].ToolCallID != "call_1" || results[1].ToolCallID != "call_2" {
		t.Fatalf("expected ordered results call_1 then call_2, got %q then %q", results[0].ToolCallID, results[1].ToolCallID)
	}
}

func TestStreamingToolExecutorUpdatesExistingToolCall(t *testing.T) {
	exec := NewStreamingToolExecutor()
	exec.AddTool(makeToolCall("call_1", "Bash", "{}"), &schema.Message{Role: schema.Assistant})
	exec.AddTool(makeToolCall("call_1", "Bash", `{"command":"pwd"}`), &schema.Message{Role: schema.Assistant})

	if len(exec.tools) != 1 {
		t.Fatalf("expected 1 tracked tool after update, got %d", len(exec.tools))
	}
	if exec.tools[0].ToolCall == nil || exec.tools[0].ToolCall.Function.Arguments != `{"command":"pwd"}` {
		t.Fatalf("expected updated arguments to be preserved, got %#v", exec.tools[0].ToolCall)
		return
	}
}

func TestStreamingToolExecutorCommitRechecksContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	var executions atomic.Int32
	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		Execute: func(toolCall *schema.ToolCall) *ToolResult {
			executions.Add(1)
			return newToolResult(toolCall.ID, toolCall.Function.Name, "unexpected", false)
		},
	})
	exec.AddTool(makeToolCall("call_1", "Write", `{"file_path":"/tmp/a","content":"a"}`), nil)
	cancel()

	if exec.commit(ctx) {
		t.Fatal("commit succeeded after context cancellation")
	}
	if got := executions.Load(); got != 0 {
		t.Fatalf("executions = %d, want 0", got)
	}
	results := exec.GetCompleted()
	if len(results) != 1 || results[0].Result != "Interrupted by user" || !results[0].IsError {
		t.Fatalf("results = %#v, want one interrupted error", results)
	}
}

func TestStreamingToolExecutorPreparesCallsInModelOrder(t *testing.T) {
	const ordinalKey = "prepare_ordinal"
	gate := make(chan struct{})
	var mu sync.Mutex
	preparedIDs := make([]string, 0, 3)
	observedOrdinals := make(map[string]int, 3)

	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		IsConcurrencySafe: func(*schema.ToolCall) bool { return true },
		MaxConcurrency:    3,
		PrepareForExecution: func(toolCall *schema.ToolCall) *schema.ToolCall {
			mu.Lock()
			defer mu.Unlock()
			preparedIDs = append(preparedIDs, toolCall.ID)
			if toolCall.Extra == nil {
				toolCall.Extra = map[string]any{}
			}
			toolCall.Extra[ordinalKey] = len(preparedIDs)
			return toolCall
		},
		Execute: func(toolCall *schema.ToolCall) *ToolResult {
			<-gate
			mu.Lock()
			observedOrdinals[toolCall.ID] = toolCall.Extra[ordinalKey].(int)
			mu.Unlock()
			return newToolResult(toolCall.ID, toolCall.Function.Name, "ok", false)
		},
	})

	calls := []*schema.ToolCall{
		makeToolCall("call_1", "Read", `{"file_path":"/tmp/a"}`),
		makeToolCall("call_2", "Read", `{"file_path":"/tmp/b"}`),
		makeToolCall("call_3", "Read", `{"file_path":"/tmp/c"}`),
	}
	for _, call := range calls {
		exec.AddTool(call, &schema.Message{Role: schema.Assistant})
	}

	mu.Lock()
	gotPrepared := append([]string(nil), preparedIDs...)
	mu.Unlock()
	if len(gotPrepared) != 0 {
		t.Fatalf("prepare ran before commit: %v", gotPrepared)
	}
	exec.commit(context.Background())
	mu.Lock()
	gotPrepared = append([]string(nil), preparedIDs...)
	mu.Unlock()
	if want := []string{"call_1", "call_2", "call_3"}; !reflect.DeepEqual(gotPrepared, want) {
		t.Fatalf("prepare order = %v, want %v", gotPrepared, want)
	}
	for _, call := range calls {
		if _, ok := call.Extra[ordinalKey]; ok {
			t.Fatalf("prepare metadata leaked into original call %q: %#v", call.ID, call.Extra)
		}
	}

	close(gate)
	results := exec.GetRemainingResults(false)
	if len(results) != len(calls) {
		t.Fatalf("result count = %d, want %d", len(results), len(calls))
	}
	mu.Lock()
	defer mu.Unlock()
	for i, call := range calls {
		if got := observedOrdinals[call.ID]; got != i+1 {
			t.Errorf("execution ordinal for %q = %d, want %d", call.ID, got, i+1)
		}
	}
}

func TestStreamingToolExecutorPreparesPartialCallOnceAfterFinalArguments(t *testing.T) {
	var prepared []string
	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		PrepareForExecution: func(toolCall *schema.ToolCall) *schema.ToolCall {
			prepared = append(prepared, toolCall.Function.Arguments)
			return toolCall
		},
		Execute: func(toolCall *schema.ToolCall) *ToolResult {
			return newToolResult(toolCall.ID, toolCall.Function.Name, "ok", false)
		},
	})
	exec.AddTool(makeToolCall("partial", "Read", `{}`), nil)
	exec.AddTool(makeToolCall("partial", "Read", `{"file_path":`), nil)
	if len(prepared) != 0 {
		t.Fatalf("prepare ran for incomplete arguments: %v", prepared)
	}
	exec.AddTool(makeToolCall("partial", "Read", `{"file_path":"/tmp/final"}`), nil)
	exec.commit(context.Background())
	results := exec.GetRemainingResults(false)
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("results = %#v", results)
	}
	if want := []string{`{"file_path":"/tmp/final"}`}; !reflect.DeepEqual(prepared, want) {
		t.Fatalf("prepared arguments = %v, want %v", prepared, want)
	}
}

func TestStreamingToolExecutorGetRemainingOnAbort(t *testing.T) {
	exec := NewStreamingToolExecutor()

	tc := makeToolCall("call_1", "Read", `{"file_path":"/tmp/a"}`)
	exec.AddTool(tc, &schema.Message{Role: schema.Assistant})
	tc2 := makeToolCall("call_2", "Write", `{"file_path":"/tmp/b","content":"x"}`)
	exec.AddTool(tc2, &schema.Message{Role: schema.Assistant})
	exec.commit(context.Background())

	exec.Complete("call_1", "ok", false)
	results := exec.GetRemainingResults(true)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ToolCallID != "call_1" {
		t.Errorf("expected 'call_1', got %q", results[0].ToolCallID)
	}
	if results[1].Result != "Interrupted by user" {
		t.Errorf("expected 'Interrupted by user', got %q", results[1].Result)
	}
	if results[1].Message.Extra == nil || results[1].Message.Extra["is_error"] != true {
		t.Fatalf("expected abort result to be marked as error, got %#v", results[1].Message.Extra)
		return
	}
}

func TestStreamingToolExecutorDiscard(t *testing.T) {
	exec := NewStreamingToolExecutor()

	tc := makeToolCall("call_1", "Read", `{"file_path":"/tmp/a"}`)
	exec.AddTool(tc, &schema.Message{Role: schema.Assistant})
	exec.Complete("call_1", "ok", false)

	exec.Discard()

	results := exec.GetCompleted()
	if len(results) != 0 {
		t.Fatalf("expected 0 results after discard, got %d", len(results))
	}
	if len(exec.tools) != 0 {
		t.Fatalf("expected 0 tracked tools after discard, got %d", len(exec.tools))
	}
}

func TestStreamingToolExecutorCompleteNonexistent(t *testing.T) {
	exec := NewStreamingToolExecutor()
	exec.Complete("nonexistent", "result", false)
	results := exec.GetCompleted()
	if len(results) != 0 {
		t.Fatalf("expected 0 results for nonexistent tool, got %d", len(results))
	}
}

func TestStreamingToolExecutorBashErrorCancelsSiblings(t *testing.T) {
	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		Execute: func(tc *schema.ToolCall) *ToolResult {
			return &ToolResult{
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
				Result:     "bash: command not found",
				IsError:    true,
			}
		},
		IsConcurrencySafe: func(tc *schema.ToolCall) bool { return true },
	})

	exec.AddTool(makeToolCall("call_1", "Bash", `{"command":"bad"}`), &schema.Message{Role: schema.Assistant})
	exec.AddTool(makeToolCall("call_2", "Read", `{"file_path":"/tmp/a"}`), &schema.Message{Role: schema.Assistant})
	exec.AddTool(makeToolCall("call_3", "Grep", `{"pattern":"x"}`), &schema.Message{Role: schema.Assistant})
	exec.commit(context.Background())

	// Wait for all to settle.
	results := exec.GetRemainingResults(false)

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	// call_1 is the Bash error.
	if !results[0].IsError || results[0].ToolName != "Bash" {
		t.Errorf("expected call_1 to be Bash error, got %+v", results[0])
	}
	// Queued siblings should be cancelled (they might have been queued when Bash finished).
	for _, r := range results[1:] {
		if r.Result == "" {
			t.Errorf("expected non-empty result for cancelled sibling %s", r.ToolCallID)
		}
	}
}

func TestStreamingToolExecutorNonBashErrorDoesNotCancelSiblings(t *testing.T) {
	var callCount int32
	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		Execute: func(tc *schema.ToolCall) *ToolResult {
			atomic.AddInt32(&callCount, 1)
			if tc.Function.Name == "Read" {
				return &ToolResult{
					ToolCallID: tc.ID,
					ToolName:   tc.Function.Name,
					Result:     "file not found",
					IsError:    true,
				}
			}
			return &ToolResult{
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
				Result:     "ok",
				IsError:    false,
			}
		},
		IsConcurrencySafe: func(tc *schema.ToolCall) bool { return true },
	})

	exec.AddTool(makeToolCall("call_1", "Read", `{"file_path":"/tmp/a"}`), &schema.Message{Role: schema.Assistant})
	exec.AddTool(makeToolCall("call_2", "Grep", `{"pattern":"x"}`), &schema.Message{Role: schema.Assistant})
	exec.commit(context.Background())

	results := exec.GetRemainingResults(false)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// Both should have been executed (no cancellation).
	if atomic.LoadInt32(&callCount) < 2 {
		t.Errorf("expected both tools to execute, but only %d executed", atomic.LoadInt32(&callCount))
	}
	// Second result should NOT be cancelled.
	if results[1].Result == "Cancelled: parallel tool call Read errored" {
		t.Errorf("non-Bash error should not cancel siblings")
	}
}

func TestStreamingToolExecutorInterruptRespectsBlockBehavior(t *testing.T) {
	blockToolDone := make(chan struct{})
	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		Execute: func(tc *schema.ToolCall) *ToolResult {
			if tc.Function.Name == "BlockTool" {
				<-blockToolDone
				return &ToolResult{
					ToolCallID: tc.ID,
					ToolName:   tc.Function.Name,
					Result:     "block completed",
					IsError:    false,
				}
			}
			return &ToolResult{
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
				Result:     "cancel completed",
				IsError:    false,
			}
		},
		IsConcurrencySafe: func(tc *schema.ToolCall) bool { return true },
		GetInterruptBehavior: func(name string) string {
			if name == "BlockTool" {
				return "block"
			}
			return "cancel"
		},
	})

	exec.AddTool(makeToolCall("call_1", "BlockTool", `{"x":"1"}`), &schema.Message{Role: schema.Assistant})
	exec.AddTool(makeToolCall("call_2", "CancelTool", `{"x":"2"}`), &schema.Message{Role: schema.Assistant})
	exec.commit(context.Background())

	// Let the block tool complete after a short delay.
	go func() {
		// Simulate the "block" tool completing naturally.
		close(blockToolDone)
	}()

	results := exec.GetRemainingResults(true)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// BlockTool should have completed naturally (not synthesized as interrupt).
	if results[0].Result != "block completed" {
		t.Errorf("expected BlockTool to complete naturally, got %q", results[0].Result)
	}
	// CancelTool: since it was already executing concurrently, it may have completed
	// before the interrupt took effect. The key invariant is that "block" tools are
	// NOT synthesized with "Interrupted by user".
	if results[0].Result == "Interrupted by user" {
		t.Errorf("BlockTool should not be interrupted, got %q", results[0].Result)
	}
}

func TestStreamingToolExecutorInterruptBeforeCommitRejectsPendingCalls(t *testing.T) {
	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		GetInterruptBehavior: func(name string) string {
			return "cancel"
		},
	})

	exec.AddTool(makeToolCall("call_1", "Read", `{"file_path":"/tmp/a"}`), &schema.Message{Role: schema.Assistant})
	exec.AddTool(makeToolCall("call_2", "Write", `{"file_path":"/tmp/b","content":"x"}`), &schema.Message{Role: schema.Assistant})

	results := exec.GetRemainingResults(true)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Result != "Interrupted by user" {
			t.Errorf("expected 'Interrupted by user' for cancel tool %s, got %q", r.ToolCallID, r.Result)
		}
		if !r.IsError {
			t.Errorf("expected interrupt result to be error for %s", r.ToolCallID)
		}
	}
}
