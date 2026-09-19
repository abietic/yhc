package compact

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestDeterministicCompactionPreservesUserRequirements(t *testing.T) {
	const requirement = "Implement every requested behavior.\n" +
		"    Preserve indentation and the exact output contract.\n" +
		"The last requirement must remain visible after a long investigation."
	const correction = "Correction: keep the existing interface; only repair its behavior."
	transforms := []struct {
		name string
		run  func([]*schema.Message) []*schema.Message
	}{
		{"auto", func(messages []*schema.Message) []*schema.Message {
			return BuildPostCompactMessages(buildDeterministicAutoCompact(messages, EstimateTokenCount(messages)))
		}},
		{"reactive", func(messages []*schema.Message) []*schema.Message {
			result := TryReactiveCompact(messages, "sdk", "prompt_too_long")
			if result == nil {
				t.Fatal("expected a material reactive reduction")
			}
			return result.Messages
		}},
		{"overflow-drain", func(messages []*schema.Message) []*schema.Message {
			return RecoverFromOverflow(messages, "sdk").Messages
		}},
	}
	for _, transform := range transforms {
		t.Run(transform.name, func(t *testing.T) {
			messages := []*schema.Message{
				{Role: schema.User, Content: requirement},
				{Role: schema.Assistant, Content: strings.Repeat("old investigation details ", 400)},
				{Role: schema.User, Content: correction},
				{Role: schema.User, Content: "transient metadata", Extra: map[string]any{"is_meta": true}},
				{Role: schema.Assistant, Content: "recent action"},
				{Role: schema.Assistant, Content: "recent observation"},
			}
			out := transform.run(messages)
			assertRequirementOrder(t, out, requirement, correction)
			if EstimateTokenCount(out) >= EstimateTokenCount(messages) {
				t.Fatal("compaction did not reduce the context")
			}
			for _, msg := range out {
				if msg.Content == "transient metadata" {
					t.Fatal("historical metadata was pinned as a user request")
				}
				if msg.Role == schema.User && msg.Content == requirement {
					msg.Content = "mutated output"
				}
			}
			if messages[0].Content != requirement {
				t.Fatal("compaction output aliases the original user message")
			}
		})
	}
}

func TestDeterministicCompactionPreservesParallelToolGroup(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "original requirements"},
		{Role: schema.Assistant, Content: strings.Repeat("old details ", 400)},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
			{ID: "a", Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: `{}`}},
			{ID: "b", Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: `{}`}},
			{ID: "c", Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: `{}`}},
		}},
		{Role: schema.Tool, ToolCallID: "a", Content: "first result"},
		{Role: schema.Tool, ToolCallID: "b", Content: "second result"},
		{Role: schema.Tool, ToolCallID: "c", Content: "third result"},
	}
	result := buildDeterministicAutoCompact(messages, EstimateTokenCount(messages))
	out := BuildPostCompactMessages(result)
	if len(out) != 7 || out[2].Role != schema.User || len(out[3].ToolCalls) != 3 {
		t.Fatalf("expected requirement and complete three-call group, got %d messages", len(out))
	}
	for i, id := range []string{"a", "b", "c"} {
		if out[4+i].ToolCallID != id {
			t.Fatalf("tool result %d lost its position or identity", i)
		}
	}
}

func TestRepeatedDeterministicCompactionDoesNotDuplicateRequirements(t *testing.T) {
	const requirement = "Keep the original requirement verbatim."
	messages := []*schema.Message{{Role: schema.User, Content: requirement}}
	for range 3 {
		messages = append(messages,
			&schema.Message{Role: schema.Assistant, Content: strings.Repeat("discardable details ", 400)},
			&schema.Message{Role: schema.Assistant, Content: "recent action"},
			&schema.Message{Role: schema.Assistant, Content: "recent result"},
		)
		messages = BuildPostCompactMessages(buildDeterministicAutoCompact(messages, EstimateTokenCount(messages)))
		assertRequirementOrder(t, messages, requirement)
	}
}

func TestReactiveCompactionDoesNotDiscardRequirementsToClaimProgress(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: strings.Repeat("required input ", 400)},
		{Role: schema.Assistant, Content: "recent answer"},
	}
	if result := TryReactiveCompact(messages, "sdk", "prompt_too_long"); result != nil {
		t.Fatal("must decline recovery when preserving requirements cannot reduce input")
	}
}

func assertRequirementOrder(t *testing.T, messages []*schema.Message, want ...string) {
	t.Helper()
	var got []string
	for _, message := range messages {
		if message != nil && message.Role == schema.User && !isMetaMessage(message) {
			got = append(got, message.Content)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("preserved %d user requests, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("user request %d changed or moved", index)
		}
	}
}
