package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	engineconfig "github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/provider"
	"github.com/cloudwego/eino/schema"
)

func TestDeepSeekFlashRichTurnSurvivesHistorySnip(t *testing.T) {
	for _, modelID := range []string{"deepseek-flash", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp"} {
		for _, failTransport := range []bool{false, true} {
			scenario := "complete"
			if failTransport {
				scenario = "connection_closed"
			}
			t.Run(modelID+"/"+scenario, func(t *testing.T) {
				type part struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					ImageURL string `json:"image_url"`
					Detail   string `json:"detail"`
				}
				type request struct {
					Model     string `json:"model"`
					Stream    bool   `json:"stream"`
					Reasoning struct {
						Effort string `json:"effort"`
					} `json:"reasoning"`
					Input []struct {
						Role    string `json:"role"`
						Content []part `json:"content"`
					} `json:"input"`
				}
				requests := make(chan request, 2)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body request
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					requests <- body
					if failTransport {
						connection, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = connection.Close()
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w,
						"event: response.output_text.delta\n"+
							`data: {"type":"response.output_text.delta","sequence_number":0,"item_id":"msg-1","delta":"image seen"}`+"\n\n"+
							"event: response.completed\n"+
							`data: {"type":"response.completed","sequence_number":1,"response":{"id":"multimodal-fixture","object":"response","status":"completed","output":[],"usage":{"input_tokens":1024,"output_tokens":2,"total_tokens":1026}}}`+"\n\n")
				}))
				defer server.Close()
				runtime, err := provider.NewConfiguredRuntime(t.Context(), provider.ConfiguredRuntimeOptions{
					Sources: &engineconfig.ConfigSources{User: &engineconfig.Config{}, Project: &engineconfig.Config{}},
					Resolution: provider.ResolveInput{Explicit: provider.Config{
						Provider: provider.ProviderAgenticDeepSeek, Model: modelID,
						APIKey: "sentinel-provider-credential", BaseURL: server.URL,
					}, Getenv: func(string) string { return "" }},
				})
				if err != nil {
					t.Fatal(err)
				}
				eng := NewQueryEngine(QueryEngineConfig{
					Model: modelID, ModelResolver: runtime, ChatModel: runtime.ChatModel,
					PromptCapabilityResolver: DefaultPromptCapabilityResolver(),
					CWD:                      t.TempDir(), TranscriptDir: t.TempDir(), MaxTurns: 2,
					HookExecutor: hooks.NewExecutor(),
				})
				t.Cleanup(eng.Close)
				if _, err := eng.ChangeReasoningEffort(t.Context(), "max"); err != nil {
					t.Fatal(err)
				}
				// Forty messages and oversized historical tool results reproduce
				// the snip boundary in the screenshot without a private transcript.
				for index := range 10 {
					id := fmt.Sprintf("history-%d", index)
					eng.messages = append(eng.messages,
						schema.UserMessage("older question"),
						&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: id, Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: `{}`}}}},
						&schema.Message{Role: schema.Tool, ToolCallID: id, ToolName: "Read", Content: strings.Repeat("historical result ", 500)},
						schema.AssistantMessage("older answer", nil),
					)
				}
				events, _ := eng.SubmitPromptInput(t.Context(), NewUntrustedPromptInput(
					NewPromptTextPart("before"),
					NewPromptImagePart(testUserImagePNGBase64, "image/png", PromptImageDetailHigh),
					NewPromptTextPart("after"),
				))
				terminal, observed := collectPromptInputEvents(t, events)
				if failTransport {
					if terminal.Reason != TerminalModelError || terminal.Err == nil ||
						!strings.Contains(terminal.Err.Error(), "reason=connection_closed") {
						t.Fatalf("transport failure terminal = %s, %v", terminal.Reason, terminal.Err)
					}
				} else if terminal.Reason != TerminalCompleted || terminal.Err != nil {
					t.Fatalf("rich turn terminal = %s, %v", terminal.Reason, terminal.Err)
				}
				var boundaries, terminals int
				for _, event := range observed {
					if event.Type == EventCompactBoundary {
						boundaries++
					}
					if event.Type == EventTerminal {
						terminals++
					}
				}
				if boundaries != 1 || terminals != 1 || len(requests) != 1 {
					t.Fatalf("boundaries=%d terminals=%d provider calls=%d", boundaries, terminals, len(requests))
				}
				body := <-requests
				if body.Model != modelID || body.Reasoning.Effort != "max" || !body.Stream {
					t.Fatal("runtime lost the exact route, max effort, or streaming intent")
				}
				last := body.Input[len(body.Input)-1]
				want := []part{
					{Type: "input_text", Text: "before"},
					{Type: "input_image", ImageURL: "data:image/png;base64," + testUserImagePNGBase64, Detail: "high"},
					{Type: "input_text", Text: "after"},
				}
				if last.Role != "user" || !reflect.DeepEqual(last.Content, want) {
					t.Fatal("snip or provider conversion changed current image bytes or part order")
				}
				assertNoActivePromptMedia(t, eng)
			})
		}
	}
}
