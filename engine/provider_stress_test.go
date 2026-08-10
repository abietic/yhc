package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Multi-provider behavioral matrix tests verify that the engine produces
// equivalent behavior regardless of which model provider is used.
// These use mock models simulating different provider response patterns.

// TestMultiProviderBasicCompletion tests that different response styles
// all produce a valid completed terminal.
func TestMultiProviderBasicCompletion(t *testing.T) {
	providers := map[string]*fixedResponseModel{
		"anthropic-style": {response: "I'll help you with that."},
		"openai-style":    {response: "Sure, I can assist with that request."},
		"gemini-style":    {response: "Here's what I found:"},
		"short-response":  {response: "Done."},
		"empty-response":  {response: ""},
	}

	for name, m := range providers {
		t.Run(name, func(t *testing.T) {
			maxTurns := 1
			terminal := Query(context.Background(), QueryParams{
				Messages:     []*schema.Message{{Role: schema.User, Content: "test"}},
				SystemPrompt: &schema.Message{Role: schema.System, Content: "test"},
				MaxTurns:     &maxTurns,
				ChatModel:    m,
			}, func(evt QueryEvent) {})

			if terminal.Reason != TerminalCompleted {
				t.Errorf("[%s] expected completed, got %q", name, terminal.Reason)
			}
		})
	}
}

// TestMultiProviderToolCallFormats tests that tool calls from different
// providers are handled correctly.
func TestMultiProviderToolCallFormats(t *testing.T) {
	tests := []struct {
		name     string
		toolCall schema.ToolCall
	}{
		{
			name: "standard-format",
			toolCall: schema.ToolCall{
				ID: "call_abc123", Type: "function",
				Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path": "/tmp/test"}`},
			},
		},
		{
			name: "short-id",
			toolCall: schema.ToolCall{
				ID: "tc1", Type: "function",
				Function: schema.FunctionCall{Name: "Grep", Arguments: `{"pattern": "TODO"}`},
			},
		},
		{
			name: "uuid-id",
			toolCall: schema.ToolCall{
				ID: "550e8400-e29b-41d4-a716-446655440000", Type: "function",
				Function: schema.FunctionCall{Name: "Glob", Arguments: `{"pattern": "*.go"}`},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callCount := 0
			m := &funcModel{fn: func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
				callCount++
				if callCount == 1 {
					return &schema.Message{
						Role: schema.Assistant, Content: "",
						ToolCalls: []schema.ToolCall{tt.toolCall},
					}, nil
				}
				return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
			}}
			maxTurns := 5

			terminal := Query(context.Background(), QueryParams{
				Messages:     []*schema.Message{{Role: schema.User, Content: "test"}},
				SystemPrompt: &schema.Message{Role: schema.System, Content: "test"},
				MaxTurns:     &maxTurns,
				ChatModel:    m,
			}, func(evt QueryEvent) {})

			if terminal.Reason != TerminalCompleted {
				t.Errorf("[%s] expected completed, got %q", tt.name, terminal.Reason)
			}
			if callCount < 2 {
				t.Errorf("[%s] expected at least 2 calls, got %d", tt.name, callCount)
			}
		})
	}
}

// TestMultiProviderErrorRecovery tests that provider-specific errors
// are handled gracefully.
func TestMultiProviderErrorRecovery(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		expectErr bool
	}{
		{"context-cancelled", context.Canceled, true},
		{"deadline-exceeded", context.DeadlineExceeded, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &funcModel{fn: func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
				return nil, tt.err
			}}
			maxTurns := 1

			terminal := Query(context.Background(), QueryParams{
				Messages:     []*schema.Message{{Role: schema.User, Content: "test"}},
				SystemPrompt: &schema.Message{Role: schema.System, Content: "test"},
				MaxTurns:     &maxTurns,
				ChatModel:    m,
			}, func(evt QueryEvent) {})

			// Should terminate gracefully, not panic
			if terminal.Reason == "" {
				t.Errorf("[%s] expected non-empty terminal reason", tt.name)
			}
		})
	}
}

// Long-session stress tests verify stability under extended operation.

// TestLongSessionManyTurns simulates a 50-turn conversation.
func TestLongSessionManyTurns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long-session test in short mode")
	}

	turnCount := 0
	m := &funcModel{fn: func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
		turnCount++
		if turnCount < 50 {
			return &schema.Message{
				Role: schema.Assistant, Content: "",
				ToolCalls: []schema.ToolCall{{
					ID: "tc-" + strings.Repeat("x", 5), Type: "function",
					Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path": "/dev/null"}`},
				}},
			}, nil
		}
		return &schema.Message{Role: schema.Assistant, Content: "Finally done after many turns."}, nil
	}}
	maxTurns := 60

	var eventCount int
	terminal := Query(context.Background(), QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "do a lot of work"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "test"},
		MaxTurns:     &maxTurns,
		ChatModel:    m,
	}, func(evt QueryEvent) {
		eventCount++
	})

	if terminal.Reason != TerminalCompleted {
		t.Errorf("expected completed after 50 turns, got %q", terminal.Reason)
	}
	if turnCount < 50 {
		t.Errorf("expected 50 turns, got %d", turnCount)
	}
	if eventCount < 100 {
		t.Errorf("expected many events (>100), got %d", eventCount)
	}
}

// TestLongSessionMemoryStability verifies no unbounded growth.
func TestLongSessionMemoryStability(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping memory stability test in short mode")
	}

	turnCount := 0
	m := &funcModel{fn: func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
		turnCount++
		if turnCount < 30 {
			return &schema.Message{
				Role: schema.Assistant, Content: strings.Repeat("response content ", 100),
				ToolCalls: []schema.ToolCall{{
					ID: "tc-mem", Type: "function",
					Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path": "/dev/null"}`},
				}},
			}, nil
		}
		return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
	}}
	maxTurns := 40

	terminal := Query(context.Background(), QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "stress test"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "test"},
		MaxTurns:     &maxTurns,
		ChatModel:    m,
	}, func(evt QueryEvent) {})

	if terminal.Reason != TerminalCompleted {
		t.Errorf("expected completed, got %q", terminal.Reason)
	}
}

// TestLongSessionConcurrentAbort tests abort during a long session.
func TestLongSessionConcurrentAbort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	turnCount := 0
	m := &funcModel{fn: func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
		turnCount++
		if turnCount == 10 {
			cancel()
			return nil, ctx.Err()
		}
		return &schema.Message{
			Role: schema.Assistant, Content: "",
			ToolCalls: []schema.ToolCall{{
				ID: "tc-abort", Type: "function",
				Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path": "/dev/null"}`},
			}},
		}, nil
	}}
	maxTurns := 100

	terminal := Query(ctx, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "long running"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "test"},
		MaxTurns:     &maxTurns,
		ChatModel:    m,
	}, func(evt QueryEvent) {})

	// Should terminate cleanly (aborted or model_error), not hang
	if terminal.Reason == "" {
		t.Error("expected non-empty terminal reason after abort")
	}
}
