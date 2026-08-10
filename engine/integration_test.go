package engine

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestQueryLoopCompletesImmediately(t *testing.T) {
	ctx := context.Background()
	params := QueryParams{
		Messages:     make([]*schema.Message, 0),
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are a helpful assistant."},
		QuerySource:  QuerySourceSDK,
	}

	events := make([]QueryEvent, 0)
	terminal := Query(ctx, params, func(evt QueryEvent) {
		events = append(events, evt)
	})

	if terminal.Reason != TerminalCompleted {
		t.Errorf("expected TerminalCompleted, got %q", terminal.Reason)
	}
	if len(events) == 0 {
		t.Error("expected at least 1 event")
	}

	// Verify stream_request_start was yielded
	hasStart := false
	for _, e := range events {
		if e.Type == EventStreamRequestStart {
			hasStart = true
			break
		}
	}
	if !hasStart {
		t.Error("expected stream_request_start event")
	}
}

func TestQueryEngineInterrupt(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		CustomSystemPrompt: "You are helpful.",
		MaxTurns:           10,
	})

	eng.Interrupt()
	// Interrupt should not panic — just verify it doesn't crash
}

func TestQueryEngineGetMessages(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		CustomSystemPrompt: "You are helpful.",
	})

	msgs := eng.GetMessages()
	if msgs == nil {
		t.Error("expected non-nil messages slice")
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages initially, got %d", len(msgs))
	}
}

func TestQueryLoopWithMaxTurns(t *testing.T) {
	ctx := context.Background()
	maxTurns := 1
	params := QueryParams{
		Messages:     make([]*schema.Message, 0),
		SystemPrompt: &schema.Message{Role: schema.System, Content: "You are helpful."},
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
	}

	terminal := Query(ctx, params, func(evt QueryEvent) {})

	if terminal.Reason != TerminalCompleted {
		t.Errorf("expected TerminalCompleted, got %q", terminal.Reason)
	}
}
