package execution

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestSynthesizeMissingToolResults_BackfillsMissingToolCallID(t *testing.T) {
	var result *schema.Message
	YieldMissingToolResults([]*schema.Message{{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			Function: schema.FunctionCall{Name: "Read", Arguments: `{}`},
		}},
	}}, "stopped", func(evt QueryEvent) {
		result = evt.Message
	})

	if result == nil {
		t.Fatal("expected a synthetic tool result")
		return
	}
	if result.ToolCallID != "Read" {
		t.Fatalf("ToolCallID = %q, want fallback ID %q", result.ToolCallID, "Read")
	}
	if result.ToolName != "Read" {
		t.Fatalf("ToolName = %q, want %q", result.ToolName, "Read")
	}
}
