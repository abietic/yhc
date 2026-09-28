package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestQueryPreservesFreshToolResultsBeforeFirstConsumption(t *testing.T) {
	for _, historySize := range []int{0, 40} {
		t.Run(fmt.Sprintf("history_%d", historySize), func(t *testing.T) {
			messages := make([]*schema.Message, 0, historySize+1)
			for i := 0; i < historySize; i++ {
				role := schema.User
				if i%2 == 1 {
					role = schema.Assistant
				}
				messages = append(messages, &schema.Message{Role: role, Content: "earlier exchange"})
			}
			messages = append(messages, &schema.Message{Role: schema.User, Content: "inspect both files"})
			calls := []schema.ToolCall{
				{ID: "read_a", Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"a"}`}},
				{ID: "read_b", Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"b"}`}},
			}
			model := &scriptedOverflowModel{streams: [][]*schema.Message{
				{{Role: schema.Assistant, ToolCalls: calls}},
				{{Role: schema.Assistant, Content: "inspected"}},
			}}
			content := strings.Repeat("header\n", 600) + "critical middle function\n" + strings.Repeat("footer\n", 600)
			_, terminal := collectEvents(context.Background(), QueryParams{
				Messages: messages, QuerySource: QuerySourceSDK, ChatModel: model,
				ToolExecutor: func(context.Context, string, string) (string, error) { return content, nil },
			})
			if terminal.Reason != TerminalCompleted || len(model.inputs) != 2 {
				t.Fatalf("terminal = %s, provider calls = %d", terminal.Reason, len(model.inputs))
			}
			seen := map[string]bool{}
			for _, msg := range model.inputs[1] {
				if msg.Role == schema.Tool {
					seen[msg.ToolCallID] = true
					if msg.Content != content {
						t.Errorf("fresh %s truncated before first consumption: got %d bytes, want %d", msg.ToolCallID, len(msg.Content), len(content))
					}
				}
			}
			if !seen["read_a"] || !seen["read_b"] {
				t.Fatalf("missing parallel results: %v", seen)
			}
		})
	}
}
