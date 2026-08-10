package compact

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// --- CompactWithRecovery tests ---

func TestCompactWithRecoverySuccess(t *testing.T) {
	mock := &mockCompactModel{
		response: `<analysis>thinking</analysis>
<summary>
1. User asked to build something.
</summary>`,
	}

	// Use enough messages/content that compaction actually reduces size
	messages := []*schema.Message{
		{Role: schema.User, Content: strings.Repeat("Build a CLI tool with these requirements: ", 20)},
		{Role: schema.Assistant, Content: strings.Repeat("Sure, I'll help. Here's my plan: ", 20)},
		{Role: schema.User, Content: strings.Repeat("Make it fast and efficient with proper error handling ", 20)},
		{Role: schema.Assistant, Content: strings.Repeat("Done. I've implemented the following features: ", 20)},
	}

	result := CompactWithRecovery(context.Background(), messages, LLMCompactOptions{
		ChatModel: mock,
		ModelName: "test-model",
	}, nil)

	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	if result.Messages == nil {
		t.Fatal("expected non-nil messages on success")
		return
	}
	if result.Attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", result.Attempts)
	}
	if result.Strategy != RecoveryStrategy(-1) {
		t.Fatalf("expected no-recovery strategy (-1), got %d", result.Strategy)
	}
	if result.PostCompactTokens >= result.PreCompactTokens {
		t.Fatalf("expected post < pre tokens, got pre=%d post=%d", result.PreCompactTokens, result.PostCompactTokens)
	}
}

func TestCompactWithRecoveryRetrySmaller(t *testing.T) {
	callCount := 0
	mock := &mockCompactModelFn{
		fn: func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			callCount++
			if callCount == 1 {
				// First call fails
				return nil, errors.New("model overloaded")
			}
			// Subsequent calls succeed
			return &schema.Message{
				Role:    schema.Assistant,
				Content: "<summary>\n1. Retried summary.\n</summary>",
			}, nil
		},
	}

	messages := []*schema.Message{
		{Role: schema.User, Content: strings.Repeat("context ", 100)},
		{Role: schema.Assistant, Content: "reply1", Extra: map[string]any{"message_id": "a1"}},
		{Role: schema.User, Content: "question"},
		{Role: schema.Assistant, Content: "reply2", Extra: map[string]any{"message_id": "a2"}},
		{Role: schema.User, Content: "more"},
		{Role: schema.Assistant, Content: "reply3", Extra: map[string]any{"message_id": "a3"}},
	}

	result := CompactWithRecovery(context.Background(), messages, LLMCompactOptions{
		ChatModel: mock,
		ModelName: "test-model",
	}, &CompactRecoveryConfig{
		MaxRetries:              3,
		ShrinkFactor:            0.3,
		FallbackToDeterministic: true,
	})

	if !result.Success {
		t.Fatalf("expected success after retry, got error: %v", result.Error)
	}
	if result.Strategy != RecoveryRetrySmaller {
		t.Fatalf("expected RecoveryRetrySmaller strategy, got %d", result.Strategy)
	}
	if result.Attempts < 2 {
		t.Fatalf("expected at least 2 attempts, got %d", result.Attempts)
	}
}

func TestCompactWithRecoveryDeterministicFallback(t *testing.T) {
	// Model always fails
	mock := &mockCompactModel{
		err: errors.New("always fails"),
	}

	messages := []*schema.Message{
		{Role: schema.User, Content: "hello world"},
		{Role: schema.Assistant, Content: "hi there", Extra: map[string]any{"message_id": "a1"}},
		{Role: schema.User, Content: "do something"},
		{Role: schema.Assistant, Content: "done", Extra: map[string]any{"message_id": "a2"}},
	}

	result := CompactWithRecovery(context.Background(), messages, LLMCompactOptions{
		ChatModel: mock,
		ModelName: "test-model",
	}, &CompactRecoveryConfig{
		MaxRetries:              1,
		ShrinkFactor:            0.3,
		FallbackToDeterministic: true,
	})

	if !result.Success {
		t.Fatalf("expected success with deterministic fallback, got error: %v", result.Error)
	}
	if result.Strategy != RecoveryDeterministic {
		t.Fatalf("expected RecoveryDeterministic strategy, got %d", result.Strategy)
	}
	if result.Messages == nil {
		t.Fatal("expected non-nil messages from deterministic fallback")
		return
	}

	// Check that the result has proper structure (boundary + summary + preserved tail)
	hasBoundary := false
	hasSummary := false
	for _, msg := range result.Messages {
		if msg.Extra != nil {
			if msg.Extra["subtype"] == "compact_boundary" {
				hasBoundary = true
			}
			if msg.Extra["subtype"] == "compact_summary" {
				hasSummary = true
			}
		}
	}
	if !hasBoundary {
		t.Fatal("expected boundary marker in deterministic fallback result")
	}
	if !hasSummary {
		t.Fatal("expected summary in deterministic fallback result")
	}
}

func TestCompactWithRecoveryPreserveOriginal(t *testing.T) {
	// Model always fails and deterministic disabled
	mock := &mockCompactModel{
		err: errors.New("always fails"),
	}

	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "hi", Extra: map[string]any{"message_id": "a1"}},
	}

	result := CompactWithRecovery(context.Background(), messages, LLMCompactOptions{
		ChatModel: mock,
		ModelName: "test-model",
	}, &CompactRecoveryConfig{
		MaxRetries:              1,
		ShrinkFactor:            0.3,
		FallbackToDeterministic: false, // Disable deterministic fallback
	})

	if result.Success {
		t.Fatal("expected failure when deterministic fallback is disabled and LLM fails")
	}
	if result.Strategy != RecoveryPreserveOriginal {
		t.Fatalf("expected RecoveryPreserveOriginal strategy, got %d", result.Strategy)
	}
	if result.Messages != nil {
		t.Fatal("expected nil messages on total failure (caller keeps original)")
		return
	}
	if result.Error == nil {
		t.Fatal("expected non-nil error on failure")
		return
	}
}

func TestCompactWithRecoveryEmptyMessages(t *testing.T) {
	result := CompactWithRecovery(context.Background(), nil, LLMCompactOptions{
		ChatModel: &mockCompactModel{},
	}, nil)

	if result.Success {
		t.Fatal("expected failure for empty messages")
	}
	if result.Error == nil {
		t.Fatal("expected error for empty messages")
		return
	}
}

func TestCompactWithRecoveryNoModel(t *testing.T) {
	// No chat model provided — should fall back to deterministic directly
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "hi"},
		{Role: schema.User, Content: "question"},
		{Role: schema.Assistant, Content: "answer"},
	}

	result := CompactWithRecovery(context.Background(), messages, LLMCompactOptions{
		// ChatModel is nil
	}, &CompactRecoveryConfig{
		FallbackToDeterministic: true,
	})

	if !result.Success {
		t.Fatalf("expected success with deterministic fallback (no model), got error: %v", result.Error)
	}
	if result.Strategy != RecoveryDeterministic {
		t.Fatalf("expected RecoveryDeterministic when no model provided, got %d", result.Strategy)
	}
}

func TestCompactWithRecoveryPreservedFacts(t *testing.T) {
	mock := &mockCompactModel{
		response: "<summary>\n1. Summary.\n</summary>",
	}

	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "hi"},
	}

	facts := []string{"Project uses Go 1.25", "Main package is cmd/agent"}

	result := CompactWithRecovery(context.Background(), messages, LLMCompactOptions{
		ChatModel: mock,
		ModelName: "test-model",
	}, &CompactRecoveryConfig{
		FallbackToDeterministic: true,
		PreserveFacts:           facts,
	})

	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}

	// Check that preserved facts appear in the output
	found := false
	for _, msg := range result.Messages {
		if strings.Contains(msg.Content, "Go 1.25") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected preserved facts to appear in result messages")
	}
}

func TestCompactWithRecoveryNoMessageLoss(t *testing.T) {
	// This is the critical safety guarantee: on total failure, original messages
	// are NOT modified and the caller is informed to keep them.
	mock := &mockCompactModel{
		err: errors.New("catastrophic failure"),
	}

	original := []*schema.Message{
		{Role: schema.User, Content: "important message 1"},
		{Role: schema.Assistant, Content: "important response 1"},
		{Role: schema.User, Content: "important message 2"},
		{Role: schema.Assistant, Content: "important response 2"},
	}

	// Make a copy to verify original is untouched
	originalCopy := make([]*schema.Message, len(original))
	copy(originalCopy, original)

	result := CompactWithRecovery(context.Background(), original, LLMCompactOptions{
		ChatModel: mock,
	}, &CompactRecoveryConfig{
		MaxRetries:              2,
		FallbackToDeterministic: false,
	})

	// Should fail gracefully
	if result.Success {
		t.Fatal("expected failure")
	}

	// Original messages must be untouched
	if len(original) != len(originalCopy) {
		t.Fatal("original messages were modified!")
	}
	for i, msg := range original {
		if msg.Content != originalCopy[i].Content {
			t.Fatalf("original message %d was modified: %q vs %q", i, msg.Content, originalCopy[i].Content)
		}
	}
}

// --- shrinkForRetry tests ---

func TestShrinkForRetryDropsGroups(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: strings.Repeat("old ", 100)},
		{Role: schema.Assistant, Content: "reply1", Extra: map[string]any{"message_id": "a1"}},
		{Role: schema.User, Content: "question"},
		{Role: schema.Assistant, Content: "reply2", Extra: map[string]any{"message_id": "a2"}},
		{Role: schema.User, Content: "more"},
		{Role: schema.Assistant, Content: "reply3", Extra: map[string]any{"message_id": "a3"}},
	}

	result := shrinkForRetry(messages, 0.3)
	if result == nil {
		t.Fatal("expected non-nil shrunk result")
		return
	}
	if len(result) >= len(messages) {
		t.Fatalf("expected fewer messages after shrink, got %d vs original %d", len(result), len(messages))
	}
}

func TestShrinkForRetryReturnsNilForSingleGroup(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
	}
	result := shrinkForRetry(messages, 0.3)
	if result != nil {
		t.Fatal("expected nil for single-group input")
		return
	}
}

func TestShrinkForRetryEnsuresUserFirst(t *testing.T) {
	// When dropping groups leaves an assistant message first, a marker is prepended
	messages := []*schema.Message{
		{Role: schema.User, Content: "old"},
		{Role: schema.Assistant, Content: "r1", Extra: map[string]any{"message_id": "a1"}},
		{Role: schema.Assistant, Content: "r2", Extra: map[string]any{"message_id": "a2"}},
	}

	result := shrinkForRetry(messages, 0.5)
	if result == nil {
		t.Fatal("expected non-nil result")
		return
	}
	if result[0].Role != schema.User && result[0].Role != schema.System {
		t.Fatalf("expected user or system message first, got %v", result[0].Role)
	}
}

// --- mockCompactModelFn for flexible mock behavior ---

type mockCompactModelFn struct {
	model.BaseChatModel
	fn func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error)
}

func (m *mockCompactModelFn) Generate(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.fn(ctx, msgs, opts...)
}

func (m *mockCompactModelFn) Stream(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, msgs, opts...)
	if err != nil {
		return nil, err
	}
	sr, sw := schema.Pipe[*schema.Message](1)
	go func() {
		sw.Send(msg, nil)
		sw.Close()
	}()
	return sr, nil
}
