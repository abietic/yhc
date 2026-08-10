package execution

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestProcessStreamAggregatesToolCallChunks(t *testing.T) {
	sr := schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "Let me"},
		{Role: schema.Assistant, Content: " check"},
		{
			Role: schema.Assistant,
			ResponseMeta: &schema.ResponseMeta{
				FinishReason: "tool_calls",
				Usage:        &schema.TokenUsage{PromptTokens: 12, CompletionTokens: 4, TotalTokens: 16},
			},
			ToolCalls: []schema.ToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Bash",
					Arguments: "{}",
				},
			}},
		},
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Bash",
					Arguments: `{"command":"pwd"}`,
				},
			}},
		},
	})

	var yielded int
	result, err := ProcessStream(context.Background(), sr, nil, func(QueryEvent) {
		yielded++
	})
	if err != nil {
		t.Fatalf("ProcessStream returned error: %v", err)
		return
	}
	if yielded != 4 {
		t.Fatalf("expected 4 yielded chunks, got %d", yielded)
	}
	if len(result.AssistantMessages) != 1 {
		t.Fatalf("expected 1 finalized assistant message, got %d", len(result.AssistantMessages))
	}
	final := result.AssistantMessages[0]
	if final.Content != "Let me check" {
		t.Fatalf("expected merged content, got %q", final.Content)
	}
	if len(final.ToolCalls) != 1 {
		t.Fatalf("expected 1 merged tool call, got %d", len(final.ToolCalls))
	}
	if final.ToolCalls[0].Function.Arguments != `{"command":"pwd"}` {
		t.Fatalf("expected finalized tool args, got %q", final.ToolCalls[0].Function.Arguments)
	}
	if final.ResponseMeta == nil || final.ResponseMeta.Usage == nil || final.ResponseMeta.Usage.TotalTokens != 16 || final.ResponseMeta.FinishReason != "tool_calls" {
		t.Fatalf("expected response metadata to survive stream aggregation, got %#v", final.ResponseMeta)
	}
	if len(result.ToolUseBlocks) != 1 {
		t.Fatalf("expected 1 tool use block, got %d", len(result.ToolUseBlocks))
	}
	if !result.NeedsFollowUp {
		t.Fatal("expected NeedsFollowUp=true")
	}
}

func TestProcessStreamRejectsTruncatedToolCallsWithoutExecution(t *testing.T) {
	t.Parallel()

	var executions atomic.Int32
	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		Execute: func(toolCall *schema.ToolCall) *ToolResult {
			executions.Add(1)
			return newToolResult(toolCall.ID, toolCall.Function.Name, "unexpected", false)
		},
		IsConcurrencySafe: func(*schema.ToolCall) bool { return true },
	})
	sr := schema.StreamReaderFromArray([]*schema.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				*makeToolCall("call_1", "Write", `{"file_path":"/tmp/a","content":"a"}`),
				*makeToolCall("call_2", "Bash", `{"command":"touch /tmp/b"}`),
			},
		},
		{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{FinishReason: "length"}},
	})

	var yielded []*schema.Message
	result, err := ProcessStream(context.Background(), sr, exec, func(event QueryEvent) {
		if event.Type == QueryEventType("tool_result") {
			yielded = append(yielded, event.Message)
		}
	})
	if err != nil {
		t.Fatalf("ProcessStream returned error: %v", err)
	}
	if got := executions.Load(); got != 0 {
		t.Fatalf("executions = %d, want 0", got)
	}
	if len(yielded) != 2 || len(result.ToolResults) != 2 {
		t.Fatalf("yielded/results = %d/%d, want 2/2", len(yielded), len(result.ToolResults))
	}
	for i, id := range []string{"call_1", "call_2"} {
		if yielded[i].ToolCallID != id {
			t.Fatalf("result[%d] id = %q, want %q", i, yielded[i].ToolCallID, id)
		}
		if yielded[i].Extra == nil || yielded[i].Extra["is_error"] != true || !strings.Contains(yielded[i].Content, "truncated") {
			t.Fatalf("result[%d] is not a truncation error: %#v", i, yielded[i])
		}
	}
}

func TestProcessStreamWithheldTruncationOverridesCleanEOFCommit(t *testing.T) {
	t.Parallel()

	var executions atomic.Int32
	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		Execute: func(toolCall *schema.ToolCall) *ToolResult {
			executions.Add(1)
			return newToolResult(toolCall.ID, toolCall.Function.Name, "unexpected", false)
		},
	})
	sr := schema.StreamReaderFromArray([]*schema.Message{
		{
			Role:      schema.Assistant,
			ToolCalls: []schema.ToolCall{*makeToolCall("call_1", "Write", `{"file_path":"/tmp/a","content":"a"}`)},
		},
		{
			Role:    schema.Assistant,
			Content: "output limit",
			Extra: map[string]any{
				"api_error":  true,
				"error_type": "max_output_tokens",
			},
		},
	})

	result, err := ProcessStream(context.Background(), sr, exec, func(QueryEvent) {})
	if err != nil {
		t.Fatalf("ProcessStream returned error: %v", err)
	}
	if got := executions.Load(); got != 0 {
		t.Fatalf("executions = %d, want 0", got)
	}
	if result.Withheld == nil || result.WithheldReason != "max_output_tokens" || result.NeedsFollowUp {
		t.Fatalf("withheld result = %#v, reason=%q, needsFollowUp=%v", result.Withheld, result.WithheldReason, result.NeedsFollowUp)
	}
	if result.ToolCallsCommitted {
		t.Fatal("withheld truncation must not report committed tool calls")
	}
	if len(result.ToolResults) != 1 || !strings.Contains(result.ToolResults[0].Content, "truncated") {
		t.Fatalf("tool results = %#v, want one truncation rejection", result.ToolResults)
	}
}

func TestProcessStreamWithheldWithoutErrorTypeFailsClosed(t *testing.T) {
	t.Parallel()

	var executions atomic.Int32
	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		Execute: func(toolCall *schema.ToolCall) *ToolResult {
			executions.Add(1)
			return newToolResult(toolCall.ID, toolCall.Function.Name, "unexpected", false)
		},
	})
	sr := schema.StreamReaderFromArray([]*schema.Message{
		{
			Role:      schema.Assistant,
			ToolCalls: []schema.ToolCall{*makeToolCall("call_1", "Write", `{"file_path":"/tmp/a","content":"a"}`)},
		},
		{Role: schema.Assistant, Content: "provider error", Extra: map[string]any{"api_error": true}},
	})

	result, err := ProcessStream(context.Background(), sr, exec, func(QueryEvent) {})
	if err != nil {
		t.Fatalf("ProcessStream returned error: %v", err)
	}
	if got := executions.Load(); got != 0 {
		t.Fatalf("executions = %d, want 0", got)
	}
	if result.Withheld == nil || result.WithheldReason != "" || result.NeedsFollowUp {
		t.Fatalf("withheld result = %#v, reason=%q, needsFollowUp=%v", result.Withheld, result.WithheldReason, result.NeedsFollowUp)
	}
	if len(result.ToolResults) != 1 || !strings.Contains(result.ToolResults[0].Content, "api_error") {
		t.Fatalf("tool results = %#v, want one api_error rejection", result.ToolResults)
	}
}

func TestProcessStreamCommitsToolCallsOnceAfterCleanTerminal(t *testing.T) {
	t.Parallel()

	var executions atomic.Int32
	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		Execute: func(toolCall *schema.ToolCall) *ToolResult {
			executions.Add(1)
			return newToolResult(toolCall.ID, toolCall.Function.Name, "ok", false)
		},
	})
	sr := schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{*makeToolCall("call_1", "Read", `{"file_path":"/tmp/a"}`)}},
		{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"}},
	})

	result, err := ProcessStream(context.Background(), sr, exec, func(event QueryEvent) {
		if event.Type != QueryEventType("assistant") || event.Message == nil || len(event.Message.ToolCalls) == 0 {
			return
		}
		exec.mu.Lock()
		defer exec.mu.Unlock()
		if exec.commitState != toolCommitPending || len(exec.tools) != 1 || exec.tools[0].Status != toolStatusQueued {
			t.Fatalf("tool crossed commit boundary during streaming: state=%v tools=%#v", exec.commitState, exec.tools)
		}
	})
	if err != nil {
		t.Fatalf("ProcessStream returned error: %v", err)
	}
	if len(result.ToolUseBlocks) != 1 {
		t.Fatalf("tool use blocks = %d, want 1", len(result.ToolUseBlocks))
	}
	if !result.ToolCallsCommitted {
		t.Fatal("clean terminal must report committed tool calls")
	}
	results := exec.GetRemainingResults(false)
	if got := executions.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
	if len(results)+len(result.ToolResults) != 1 {
		t.Fatalf("total tool results = %d, want 1", len(results)+len(result.ToolResults))
	}
	if exec.commit(context.Background()) != true || executions.Load() != 1 {
		t.Fatal("repeated commit must be idempotent")
	}
}

func TestProcessStreamErrorRejectsPendingToolCall(t *testing.T) {
	t.Parallel()

	streamFailure := errors.New("stream failed after tool call")
	var executions atomic.Int32
	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		Execute: func(toolCall *schema.ToolCall) *ToolResult {
			executions.Add(1)
			return newToolResult(toolCall.ID, toolCall.Function.Name, "unexpected", false)
		},
	})
	sr, sw := schema.Pipe[*schema.Message](2)
	go func() {
		defer sw.Close()
		sw.Send(&schema.Message{
			Role:      schema.Assistant,
			ToolCalls: []schema.ToolCall{*makeToolCall("call_1", "Write", `{"file_path":"/tmp/a","content":"a"}`)},
		}, nil)
		sw.Send(nil, streamFailure)
	}()

	var yielded []*schema.Message
	_, err := ProcessStream(context.Background(), sr, exec, func(event QueryEvent) {
		if event.Type == QueryEventType("tool_result") {
			yielded = append(yielded, event.Message)
		}
	})
	if !errors.Is(err, streamFailure) {
		t.Fatalf("error = %v, want %v", err, streamFailure)
	}
	if got := executions.Load(); got != 0 {
		t.Fatalf("executions = %d, want 0", got)
	}
	if len(yielded) != 1 || yielded[0].Extra["is_error"] != true || !strings.Contains(yielded[0].Content, streamFailure.Error()) {
		t.Fatalf("yielded = %#v, want one stream error result", yielded)
	}
}

func TestProcessStreamUnknownFinishReasonFailsClosed(t *testing.T) {
	t.Parallel()

	var executions atomic.Int32
	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		Execute: func(toolCall *schema.ToolCall) *ToolResult {
			executions.Add(1)
			return newToolResult(toolCall.ID, toolCall.Function.Name, "unexpected", false)
		},
	})
	sr := schema.StreamReaderFromArray([]*schema.Message{{
		Role:         schema.Assistant,
		ResponseMeta: &schema.ResponseMeta{FinishReason: "content_filter"},
		ToolCalls:    []schema.ToolCall{*makeToolCall("call_1", "Write", `{"file_path":"/tmp/a","content":"a"}`)},
	}})

	var yielded []*schema.Message
	_, err := ProcessStream(context.Background(), sr, exec, func(event QueryEvent) {
		if event.Type == QueryEventType("tool_result") {
			yielded = append(yielded, event.Message)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported model finish reason") {
		t.Fatalf("error = %v, want unsupported finish reason", err)
	}
	if got := executions.Load(); got != 0 {
		t.Fatalf("executions = %d, want 0", got)
	}
	if len(yielded) != 1 || yielded[0].Extra["is_error"] != true {
		t.Fatalf("yielded = %#v, want one error result", yielded)
	}
}

func TestProcessStreamDeferredExecutorClassifiesWithoutExecuting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		finishReason      string
		wantCommitted     bool
		wantResult        string
		wantResultIsError bool
	}{
		{
			name:          "committed call is left for external owner",
			finishReason:  "tool_calls",
			wantCommitted: true,
		},
		{
			name:              "truncated call retains rejection result",
			finishReason:      "max_tokens",
			wantResult:        "Tool call rejected: model response was truncated before the assistant turn committed",
			wantResultIsError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var executions atomic.Int32
			executor := NewStreamingToolExecutor(
				StreamingToolExecutorConfig{
					DeferExecution: true,
					Execute: func(toolCall *schema.ToolCall) *ToolResult {
						executions.Add(1)
						return newToolResult(
							toolCall.ID,
							toolCall.Function.Name,
							"unexpected",
							false,
						)
					},
				},
			)
			t.Cleanup(executor.Discard)
			stream := schema.StreamReaderFromArray([]*schema.Message{{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{*makeToolCall(
					"call_1",
					"Write",
					`{"file_path":"/tmp/a","content":"a"}`,
				)},
				ResponseMeta: &schema.ResponseMeta{
					FinishReason: test.finishReason,
				},
			}})
			result, err := ProcessStream(
				context.Background(),
				stream,
				executor,
				func(QueryEvent) {},
			)
			if err != nil {
				t.Fatalf("ProcessStream returned error: %v", err)
			}
			if got := executions.Load(); got != 0 {
				t.Fatalf("executions = %d, want 0", got)
			}
			if result.ToolCallsCommitted != test.wantCommitted {
				t.Fatalf(
					"committed = %v, want %v",
					result.ToolCallsCommitted,
					test.wantCommitted,
				)
			}
			if test.wantResult == "" {
				if len(result.ToolResults) != 0 {
					t.Fatalf(
						"committed deferred results = %#v, want none",
						result.ToolResults,
					)
				}
				return
			}
			if len(result.ToolResults) != 1 ||
				result.ToolResults[0].Content != test.wantResult ||
				result.ToolResults[0].Extra["is_error"] !=
					test.wantResultIsError {
				t.Fatalf(
					"deferred rejection result = %#v",
					result.ToolResults,
				)
			}
		})
	}
}

func TestProcessStreamCancellationBeforeCommitRejectsToolCall(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	var executions atomic.Int32
	exec := NewStreamingToolExecutor(StreamingToolExecutorConfig{
		Execute: func(toolCall *schema.ToolCall) *ToolResult {
			executions.Add(1)
			return newToolResult(toolCall.ID, toolCall.Function.Name, "unexpected", false)
		},
	})
	sr := schema.StreamReaderFromArray([]*schema.Message{{
		Role:      schema.Assistant,
		ToolCalls: []schema.ToolCall{*makeToolCall("call_1", "Write", `{"file_path":"/tmp/a","content":"a"}`)},
	}})

	result, err := ProcessStream(ctx, sr, exec, func(event QueryEvent) {
		if event.Type == QueryEventType("assistant") {
			cancel()
		}
	})
	if err != nil {
		t.Fatalf("ProcessStream returned error: %v", err)
	}
	if got := executions.Load(); got != 0 {
		t.Fatalf("executions = %d, want 0", got)
	}
	if len(result.ToolResults) != 1 || result.ToolResults[0].Content != "Interrupted by user" {
		t.Fatalf("tool results = %#v, want interrupted result", result.ToolResults)
	}
	if result.ToolCallsCommitted {
		t.Fatal("cancelled terminal must not report committed tool calls")
	}
}

func TestProcessStreamAggregatesAssistantMultiContent(t *testing.T) {
	sr := schema.StreamReaderFromArray([]*schema.Message{
		{
			Role:             schema.Assistant,
			ReasoningContent: "rea",
			AssistantGenMultiContent: []schema.MessageOutputPart{{
				Type: schema.ChatMessagePartTypeReasoning,
				Reasoning: &schema.MessageOutputReasoning{
					Text:      "rea",
					Signature: "sig",
				},
				StreamingMeta: &schema.MessageStreamingMeta{Index: 0},
			}},
		},
		{
			Role:    schema.Assistant,
			Content: "answer",
			AssistantGenMultiContent: []schema.MessageOutputPart{{
				Type:          schema.ChatMessagePartTypeText,
				Text:          "answer",
				StreamingMeta: &schema.MessageStreamingMeta{Index: 1},
			}},
		},
	})

	result, err := ProcessStream(context.Background(), sr, nil, func(QueryEvent) {})
	if err != nil {
		t.Fatalf("ProcessStream returned error: %v", err)
		return
	}
	if len(result.AssistantMessages) != 1 {
		t.Fatalf("expected 1 finalized assistant message, got %d", len(result.AssistantMessages))
	}
	final := result.AssistantMessages[0]
	if final.ReasoningContent != "rea" {
		t.Fatalf("expected merged reasoning content, got %q", final.ReasoningContent)
	}
	if final.Content != "answer" {
		t.Fatalf("expected merged content, got %q", final.Content)
	}
	if len(final.AssistantGenMultiContent) != 2 {
		t.Fatalf("expected 2 assistant output parts, got %d", len(final.AssistantGenMultiContent))
	}
	if final.AssistantGenMultiContent[0].Reasoning == nil || final.AssistantGenMultiContent[0].Reasoning.Signature != "sig" {
		t.Fatalf("expected reasoning signature to survive stream aggregation, got %#v", final.AssistantGenMultiContent[0])
		return
	}
}

func TestProcessStreamPreservesProviderTextByteForByte(t *testing.T) {
	const content = "Yes\n\nNo\n\nMaybe\n\n```text\nalpha\n\nbeta\n```"
	sr := schema.StreamReaderFromArray([]*schema.Message{
		{
			Role:    schema.Assistant,
			Content: content,
			AssistantGenMultiContent: []schema.MessageOutputPart{{
				Type:          schema.ChatMessagePartTypeText,
				Text:          content,
				StreamingMeta: &schema.MessageStreamingMeta{Index: 0},
			}},
		},
	})

	result, err := ProcessStream(context.Background(), sr, nil, func(QueryEvent) {})
	if err != nil {
		t.Fatalf("ProcessStream returned error: %v", err)
	}
	if len(result.AssistantMessages) != 1 {
		t.Fatalf("assistant messages = %d, want 1", len(result.AssistantMessages))
	}
	final := result.AssistantMessages[0]
	if final.Content != content {
		t.Fatalf("provider content changed:\n got %q\nwant %q", final.Content, content)
	}
	if len(final.AssistantGenMultiContent) != 1 || final.AssistantGenMultiContent[0].Text != content {
		t.Fatalf("structured provider content changed: %#v", final.AssistantGenMultiContent)
	}
}

func TestProcessStreamTracksAndDrainsStreamingToolResultsInOrder(t *testing.T) {
	exec := NewStreamingToolExecutor()
	sr := schema.StreamReaderFromArray([]*schema.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path":"/tmp/a"}`,
				},
			}},
		},
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_2",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path":"/tmp/b"}`,
				},
			}},
		},
	})

	var yieldedToolResults []*schema.Message
	result, err := ProcessStream(context.Background(), sr, exec, func(evt QueryEvent) {
		if evt.Type == QueryEventType("assistant") && evt.Message != nil && len(evt.Message.ToolCalls) > 0 {
			switch evt.Message.ToolCalls[0].ID {
			case "call_1":
				exec.Complete("call_1", "first", false)
			case "call_2":
				exec.Complete("call_2", "second", false)
			}
		}
		if evt.Type == QueryEventType("tool_result") && evt.Message != nil {
			yieldedToolResults = append(yieldedToolResults, evt.Message)
		}
	})
	if err != nil {
		t.Fatalf("ProcessStream returned error: %v", err)
		return
	}
	if len(yieldedToolResults) != 2 {
		t.Fatalf("expected 2 streamed tool results, got %d", len(yieldedToolResults))
	}
	if yieldedToolResults[0].ToolCallID != "call_1" || yieldedToolResults[1].ToolCallID != "call_2" {
		t.Fatalf("expected ordered streamed tool results, got %q then %q", yieldedToolResults[0].ToolCallID, yieldedToolResults[1].ToolCallID)
	}
	if len(result.ToolResults) != 2 {
		t.Fatalf("expected 2 collected tool results, got %d", len(result.ToolResults))
	}
}

func TestProcessStreamAbortDrainsSyntheticToolResults(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	exec := NewStreamingToolExecutor()
	sr := schema.StreamReaderFromArray([]*schema.Message{{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:   "call_abort",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "Write",
				Arguments: `{"file_path":"/tmp/x","content":"y"}`,
			},
		}},
	}})

	var yieldedToolResults []*schema.Message
	result, err := ProcessStream(ctx, sr, exec, func(evt QueryEvent) {
		if evt.Type == QueryEventType("assistant") {
			cancel()
		}
		if evt.Type == QueryEventType("tool_result") && evt.Message != nil {
			yieldedToolResults = append(yieldedToolResults, evt.Message)
		}
	})
	if err != nil {
		t.Fatalf("ProcessStream returned error: %v", err)
		return
	}
	if len(yieldedToolResults) != 1 {
		t.Fatalf("expected 1 synthetic tool result, got %d", len(yieldedToolResults))
	}
	if yieldedToolResults[0].Content != "Interrupted by user" {
		t.Fatalf("expected interruption content, got %q", yieldedToolResults[0].Content)
	}
	if yieldedToolResults[0].Extra == nil || yieldedToolResults[0].Extra["is_error"] != true {
		t.Fatalf("expected interrupted tool result to be marked as error, got %#v", yieldedToolResults[0].Extra)
		return
	}
	if len(result.ToolResults) != 1 {
		t.Fatalf("expected 1 collected synthetic tool result, got %d", len(result.ToolResults))
	}
}
