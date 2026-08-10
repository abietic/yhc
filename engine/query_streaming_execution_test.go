package engine

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type streamingChunksModel struct {
	chunks    [][]*schema.Message
	callCount int
}

func (m *streamingChunksModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *streamingChunksModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.callCount < len(m.chunks) {
		chunk := m.chunks[m.callCount]
		m.callCount++
		return schema.StreamReaderFromArray(chunk), nil
	}
	m.callCount++
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}), nil
}

func TestQueryStreamingExecutionRunsToolOnlyOnce(t *testing.T) {
	ctx := context.Background()
	model := &streamingChunksModel{chunks: [][]*schema.Message{
		{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_stream_once",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Bash",
					Arguments: `{"command":"echo hi"}`,
				},
			}},
		}},
		{{Role: schema.Assistant, Content: "done"}},
	}}

	maxTurns := 4
	var callCount int32
	var resultEvents int32
	terminal := Query(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "run a command"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			atomic.AddInt32(&callCount, 1)
			return "ok", nil
		},
	}, func(evt QueryEvent) {
		if evt.Type == EventToolResult && evt.ToolResultMessage != nil {
			atomic.AddInt32(&resultEvents, 1)
		}
	})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected terminal completed, got %q", terminal.Reason)
	}
	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Fatalf("expected exactly one streamed tool execution, got %d", got)
	}
	if got := atomic.LoadInt32(&resultEvents); got != 1 {
		t.Fatalf("expected exactly one tool_result event, got %d", got)
	}
}

func TestQueryStreamingConcurrentSafeToolsRunInParallelAndKeepOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	reg := tools.NewRegistry()
	tools.RegisterDefaults(reg)
	model := &streamingChunksModel{chunks: [][]*schema.Message{
		{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:       "read_stream_1",
				Type:     "function",
				Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"/tmp/a.txt"}`},
			}},
		}, {
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:       "read_stream_2",
				Type:     "function",
				Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"/tmp/b.txt"}`},
			}},
		}},
		{{Role: schema.Assistant, Content: "done"}},
	}}

	var current int32
	var maxConcurrent int32
	var started int32
	bothStarted := make(chan struct{})
	release := make(chan struct{})
	var results []*schema.Message

	maxTurns := 4
	done := make(chan Terminal, 1)
	go func() {
		terminal := Query(ctx, QueryParams{
			Messages:     []*schema.Message{{Role: schema.User, Content: "read two files"}},
			SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
			QuerySource:  QuerySourceSDK,
			MaxTurns:     &maxTurns,
			ChatModel:    model,
			ToolRegistry: reg,
			ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
				if toolName != "Read" {
					return "", fmt.Errorf("unexpected tool %s", toolName)
				}
				inFlight := atomic.AddInt32(&current, 1)
				defer atomic.AddInt32(&current, -1)
				for {
					prev := atomic.LoadInt32(&maxConcurrent)
					if inFlight <= prev || atomic.CompareAndSwapInt32(&maxConcurrent, prev, inFlight) {
						break
					}
				}
				if atomic.AddInt32(&started, 1) == 2 {
					close(bothStarted)
				}
				select {
				case <-release:
				case <-ctx.Done():
					return "", ctx.Err()
				}
				return "ok:" + jsonInput, nil
			},
		}, func(evt QueryEvent) {
			if evt.Type == EventToolResult && evt.ToolResultMessage != nil {
				results = append(results, evt.ToolResultMessage)
			}
		})
		done <- terminal
	}()

	select {
	case <-bothStarted:
		close(release)
	case <-ctx.Done():
		t.Fatal("timed out waiting for streaming concurrent tool execution to start")
	}

	var terminal Terminal
	select {
	case terminal = <-done:
	case <-ctx.Done():
		t.Fatal("query did not finish")
	}

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected terminal completed, got %q", terminal.Reason)
	}
	if atomic.LoadInt32(&maxConcurrent) < 2 {
		t.Fatalf("expected concurrent streaming read execution, max concurrency = %d", atomic.LoadInt32(&maxConcurrent))
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 tool results, got %d", len(results))
	}
	if results[0].ToolCallID != "read_stream_1" || results[1].ToolCallID != "read_stream_2" {
		t.Fatalf("expected stable streamed result ordering, got %q then %q", results[0].ToolCallID, results[1].ToolCallID)
	}
}

func TestQueryStreamingWaitsForPatchedToolCallBeforeExecuting(t *testing.T) {
	ctx := context.Background()
	model := &streamingChunksModel{chunks: [][]*schema.Message{
		{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:       "patched_call",
				Type:     "function",
				Function: schema.FunctionCall{Name: "Bash", Arguments: "{}"},
			}},
		}, {
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:       "patched_call",
				Type:     "function",
				Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"pwd"}`},
			}},
		}},
		{{Role: schema.Assistant, Content: "done"}},
	}}

	maxTurns := 4
	var executed int32
	terminal := Query(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "run pwd"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			atomic.AddInt32(&executed, 1)
			if jsonInput != `{"command":"pwd"}` {
				t.Fatalf("expected finalized arguments, got %q", jsonInput)
			}
			return "ok", nil
		},
	}, func(QueryEvent) {})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected terminal completed, got %q", terminal.Reason)
	}
	if got := atomic.LoadInt32(&executed); got != 1 {
		t.Fatalf("expected one execution after tool-call patching, got %d", got)
	}
}
