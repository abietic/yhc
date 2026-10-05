package compact

import (
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func freshRoundHistory() []*schema.Message {
	content := strings.Repeat("source line\n", 800)
	call := func(id string) schema.ToolCall {
		return schema.ToolCall{ID: id, Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: `{}`}}
	}
	return []*schema.Message{
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{call("old")}},
		{Role: schema.Tool, ToolCallID: "old", Content: content},
		{
			Role: schema.Assistant, ToolCalls: []schema.ToolCall{call("fresh_a"), call("fresh_b")}, ReasoningContent: content,
			Extra: map[string]any{"timestamp": time.Now().Add(-2 * time.Hour).UnixMilli()},
		},
		{Role: schema.Tool, ToolCallID: "fresh_a", Content: content},
		{Role: schema.Tool, ToolCallID: "fresh_b", Content: content},
	}
}

func TestMicroCompactPreservesUnconsumedRound(t *testing.T) {
	history := freshRoundHistory()
	original := history[1].Content
	result := MicroCompact(history, 1<<30)
	if !result.Applied || result.Messages[1].Content == original {
		t.Fatal("consumed history must remain eligible for compaction")
	}
	for i := 2; i < len(history); i++ {
		if result.Messages[i].Content != history[i].Content || result.Messages[i].ReasoningContent != history[i].ReasoningContent {
			t.Errorf("unconsumed round message %d changed", i)
		}
	}
	if history[1].Content != original {
		t.Fatal("input history mutated")
	}
	consumed := append(history, &schema.Message{Role: schema.Assistant, Content: "read results"})
	if MicroCompact(consumed, 1<<30).Messages[3].Content == original {
		t.Fatal("round should become compactable after an assistant consumes it")
	}
}

func TestTimeBasedMicrocompactPreservesWholeUnconsumedRound(t *testing.T) {
	t.Setenv("TIME_BASED_MC_ENABLED", "true")
	t.Setenv("TIME_BASED_MC_GAP_MINUTES", "1")
	t.Setenv("TIME_BASED_MC_KEEP_RECENT", "1")
	history := freshRoundHistory()
	result := TimeBasedMicrocompact(history, "main")
	if result == nil || !result.Applied || result.Messages[1].Content != timeBasedMCClearedMessage {
		t.Fatal("old consumed result should be cleared")
	}
	for _, i := range []int{3, 4} {
		if result.Messages[i].Content != history[i].Content {
			t.Errorf("fresh result %s cleared after a slow parallel tool round", history[i].ToolCallID)
		}
	}
}
