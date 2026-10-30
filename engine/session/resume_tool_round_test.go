package session

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestValidateHistoryAcceptsAssistantContinuationAfterMatchedToolResults(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User, Content: "inspect two files"},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "one"}, {ID: "two"}}},
		{Role: schema.Tool, ToolCallID: "two", Content: "second result"},
		{Role: schema.System, Content: "durable metadata"},
		{Role: schema.Tool, ToolCallID: "one", Content: "first result"},
		{Role: schema.Assistant, Content: "completed"},
	}
	if warnings := ValidateMessageHistory(messages); len(warnings) != 0 {
		t.Fatalf("valid tool round rejected: %v", warnings)
	}
}

func TestValidateHistoryStillRejectsUnmatchedOrMissingToolContinuation(t *testing.T) {
	for _, messages := range [][]*schema.Message{
		{{Role: schema.User}, {Role: schema.Assistant}, {Role: schema.Assistant}},
		{{Role: schema.User}, {Role: schema.Assistant}, {Role: schema.Tool, ToolCallID: "foreign"}, {Role: schema.Assistant}},
		{{Role: schema.User}, {Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "missing"}}}, {Role: schema.Assistant}},
		{{Role: schema.User}, {Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "one"}, {ID: "two"}}}, {Role: schema.Tool, ToolCallID: "one"}, {Role: schema.Assistant}, {Role: schema.Tool, ToolCallID: "two"}},
	} {
		warnings := ValidateMessageHistory(messages)
		if len(warnings) == 0 || !strings.Contains(strings.Join(warnings, ";"), "consecutive") {
			t.Fatalf("invalid continuation accepted: %v", warnings)
		}
	}
}

func TestValidateHistoryRejectsDuplicateToolResultWithoutAnotherCall(t *testing.T) {
	messages := []*schema.Message{
		{Role: schema.User},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "one"}}},
		{Role: schema.Tool, ToolCallID: "one"},
		{Role: schema.Tool, ToolCallID: "one"},
		{Role: schema.Assistant},
	}
	if warnings := ValidateMessageHistory(messages); !strings.Contains(strings.Join(warnings, ";"), "orphaned") {
		t.Fatalf("duplicate tool result accepted: %v", warnings)
	}
}
