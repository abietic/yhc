package agenticdeepseek

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestTerminalUsageRequiresExplicitConsistentCounters(t *testing.T) {
	const valid = `{"input_tokens":20,"output_tokens":2,"total_tokens":22,"input_tokens_details":{"cached_tokens":4},"output_tokens_details":{"reasoning_tokens":1}}`
	const zero = `{"input_tokens":0,"output_tokens":0,"total_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}`
	cases := []struct {
		name, usage string
		known       bool
	}{
		{"valid", valid, true},
		{"explicit_zero", zero, true},
		{"null", `null`, false},
		{"empty", `{}`, false},
		{"missing_input", strings.Replace(valid, `"input_tokens":20,`, "", 1), false},
		{"missing_output", strings.Replace(valid, `"output_tokens":2,`, "", 1), false},
		{"missing_total", strings.Replace(valid, `"total_tokens":22,`, "", 1), false},
		{"missing_cache", strings.Replace(valid, `"cached_tokens":4`, "", 1), false},
		{"missing_thinking", strings.Replace(valid, `"reasoning_tokens":1`, "", 1), false},
		{"null_input", strings.Replace(valid, `"input_tokens":20`, `"input_tokens":null`, 1), false},
		{"null_output", strings.Replace(valid, `"output_tokens":2`, `"output_tokens":null`, 1), false},
		{"null_total", strings.Replace(valid, `"total_tokens":22`, `"total_tokens":null`, 1), false},
		{"null_cache", strings.Replace(valid, `"cached_tokens":4`, `"cached_tokens":null`, 1), false},
		{"null_thinking", strings.Replace(valid, `"reasoning_tokens":1`, `"reasoning_tokens":null`, 1), false},
		{"negative_input", strings.Replace(valid, `"input_tokens":20`, `"input_tokens":-1`, 1), false},
		{"short_total", strings.Replace(valid, `"total_tokens":22`, `"total_tokens":21`, 1), false},
		{"cache_over_input", strings.Replace(valid, `"cached_tokens":4`, `"cached_tokens":21`, 1), false},
		{"thinking_over_output", strings.Replace(valid, `"reasoning_tokens":1`, `"reasoning_tokens":3`, 1), false},
	}
	for _, status := range []ResponseStatus{ResponseStatusCompleted, ResponseStatusIncomplete} {
		for _, tc := range cases {
			for _, streamed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream_%t", status, tc.name, streamed), func(t *testing.T) {
					wire := fmt.Sprintf(`{"id":"receipt","object":"response","status":%q,"model":"deepseek-flash","output":[{"type":"message","content":[{"type":"output_text","text":"kept"}]}],"usage":%s}`, status, tc.usage)
					var response responseObject
					if err := json.Unmarshal([]byte(wire), &response); err != nil {
						t.Fatal(err)
					}
					message, err := responseToAgentic(&response)
					if streamed {
						message, err = terminalStreamMessage(&response)
					}
					if err != nil || message == nil || message.ResponseMeta == nil {
						t.Fatalf("valid terminal content/status was lost: message=%+v err=%v", message, err)
					}
					usage := message.ResponseMeta.TokenUsage
					if (usage != nil) != tc.known {
						t.Fatalf("receipt presence: got=%+v known=%t", usage, tc.known)
					}
					if tc.known {
						want := 22
						if tc.name == "explicit_zero" {
							want = 0
						}
						if usage.TotalTokens != want {
							t.Fatalf("usage=%+v", usage)
						}
					}
					if !streamed && (len(message.ContentBlocks) != 1 || message.ContentBlocks[0].AssistantGenText.Text != "kept") {
						t.Fatal("invalid accounting must not erase the provider's content")
					}
					meta, ok := message.ResponseMeta.Extension.(*ResponseMetaExtension)
					if !ok || meta.Status != status {
						t.Fatal("terminal status changed")
					}
				})
			}
		}
	}
}
