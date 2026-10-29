package provider

import (
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/compact"
	"github.com/cloudwego/eino/schema"
)

func TestReasoningCompactionChangesActualProviderInput(t *testing.T) {
	original := strings.Repeat("synthetic reasoning ", 1000)
	incoming := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{{Type: schema.ContentBlockTypeReasoning, Reasoning: &schema.Reasoning{Text: original}}}}
	history := []*schema.Message{agenticToMessage(incoming)}
	result := compact.MicroCompact(history, 1<<30)
	converted, err := messagesToAgentic(result.Messages)
	if err != nil {
		t.Fatal(err)
	}
	wire := ""
	for _, block := range converted[0].ContentBlocks {
		if block.Reasoning != nil {
			wire += block.Reasoning.Text
		}
	}
	if len(wire) >= len(original) || wire != result.Messages[0].ReasoningContent || result.TokensFreed <= 0 {
		t.Fatalf("provider bytes=%d flat bytes=%d freed=%d", len(wire), len(result.Messages[0].ReasoningContent), result.TokensFreed)
	}
	if history[0].ReasoningContent != original || history[0].AssistantGenMultiContent[0].Reasoning.Text != original {
		t.Fatal("input mutated")
	}
}
