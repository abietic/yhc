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

type multiToolCallModel struct {
	toolCalls []schema.ToolCall
	called    bool
}

func (m *multiToolCallModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *multiToolCallModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if !m.called {
		m.called = true
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role:      schema.Assistant,
			ToolCalls: append([]schema.ToolCall(nil), m.toolCalls...),
		}}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "done",
	}}), nil
}

func TestPartitionToolCallsBatchesByConcurrencySafety(t *testing.T) {
	reg := tools.NewRegistry()
	tools.RegisterDefaults(reg)

	batches := partitionToolCalls([]*schema.ToolCall{
		{
			ID:   "read_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "Read",
				Arguments: `{"file_path":"/tmp/a.txt"}`,
			},
		},
		{
			ID:   "read_2",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "Read",
				Arguments: `{"file_path":"/tmp/b.txt"}`,
			},
		},
		{
			ID:   "write_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "Write",
				Arguments: `{"file_path":"/tmp/c.txt","content":"x"}`,
			},
		},
		{
			ID:   "read_3",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "Read",
				Arguments: `{"file_path":"/tmp/d.txt"}`,
			},
		},
	}, reg)

	if len(batches) != 3 {
		t.Fatalf("expected 3 batches, got %d", len(batches))
	}
	if !batches[0].IsConcurrencySafe || len(batches[0].ToolCalls) != 2 {
		t.Fatalf("expected first batch to be concurrent with 2 calls, got %#v", batches[0])
	}
	if batches[1].IsConcurrencySafe || len(batches[1].ToolCalls) != 1 || batches[1].ToolCalls[0].ID != "write_1" {
		t.Fatalf("expected second batch to be the exclusive write call, got %#v", batches[1])
	}
	if !batches[2].IsConcurrencySafe || len(batches[2].ToolCalls) != 1 || batches[2].ToolCalls[0].ID != "read_3" {
		t.Fatalf("expected third batch to be a single concurrent-safe read, got %#v", batches[2])
	}
}

func TestQueryConcurrentSafeToolsRunInParallelPreserveOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	reg := tools.NewRegistry()
	tools.RegisterDefaults(reg)

	model := &multiToolCallModel{toolCalls: []schema.ToolCall{
		{
			ID:   "read_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "Read",
				Arguments: `{"file_path":"/tmp/a.txt"}`,
			},
		},
		{
			ID:   "read_2",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "Read",
				Arguments: `{"file_path":"/tmp/b.txt"}`,
			},
		},
	}}

	var current int32
	var maxConcurrent int32
	var started int32
	bothStarted := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var events []QueryEvent
	var terminal Terminal

	go func() {
		defer close(done)
		maxTurns := 4
		terminal = Query(ctx, QueryParams{
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
			events = append(events, evt)
		})
	}()

	select {
	case <-bothStarted:
		close(release)
	case <-ctx.Done():
		t.Fatal("timed out waiting for concurrent tool execution to start")
	}

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("query did not finish")
	}

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected terminal completed, got %q", terminal.Reason)
	}
	if atomic.LoadInt32(&maxConcurrent) < 2 {
		t.Fatalf("expected concurrent read execution, max concurrency = %d", atomic.LoadInt32(&maxConcurrent))
	}

	var results []*schema.Message
	for _, evt := range events {
		if evt.Type == EventToolResult && evt.ToolResultMessage != nil {
			results = append(results, evt.ToolResultMessage)
		}
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 tool results, got %d", len(results))
	}
	if results[0].ToolCallID != "read_1" || results[1].ToolCallID != "read_2" {
		t.Fatalf("expected stable result ordering by tool-call order, got %q then %q", results[0].ToolCallID, results[1].ToolCallID)
	}
}

func TestQueryUnsafeToolsRunSerially(t *testing.T) {
	ctx := context.Background()
	reg := tools.NewRegistry()
	tools.RegisterDefaults(reg)

	model := &multiToolCallModel{toolCalls: []schema.ToolCall{
		{
			ID:   "bash_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "Bash",
				Arguments: `{"command":"echo first"}`,
			},
		},
		{
			ID:   "bash_2",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "Bash",
				Arguments: `{"command":"echo second"}`,
			},
		},
	}}

	var current int32
	var maxConcurrent int32
	maxTurns := 4
	terminal := Query(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "run two commands"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ChatModel:    model,
		ToolRegistry: reg,
		ToolExecutor: func(ctx context.Context, toolName, jsonInput string) (string, error) {
			inFlight := atomic.AddInt32(&current, 1)
			defer atomic.AddInt32(&current, -1)
			for {
				prev := atomic.LoadInt32(&maxConcurrent)
				if inFlight <= prev || atomic.CompareAndSwapInt32(&maxConcurrent, prev, inFlight) {
					break
				}
			}
			time.Sleep(25 * time.Millisecond)
			return "ok:" + jsonInput, nil
		},
	}, func(QueryEvent) {})

	if terminal.Reason != TerminalCompleted {
		t.Fatalf("expected terminal completed, got %q", terminal.Reason)
	}
	if atomic.LoadInt32(&maxConcurrent) != 1 {
		t.Fatalf("expected unsafe tools to run serially, max concurrency = %d", atomic.LoadInt32(&maxConcurrent))
	}
}
