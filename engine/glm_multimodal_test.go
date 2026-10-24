package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	engineconfig "github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/provider"
)

func TestGLMFlashPromptImageDefaultsReachWireAndReplay(t *testing.T) {
	for _, modelID := range []string{"glm-5.3-flash", "glm-5.3-flashx"} {
		for _, detail := range []PromptImageDetail{"", PromptImageDetailAuto} {
			t.Run(fmt.Sprintf("%s/detail=%q", modelID, detail), func(t *testing.T) {
				type request struct {
					Model    string `json:"model"`
					Stream   bool   `json:"stream"`
					Messages []struct {
						Role    string          `json:"role"`
						Content json.RawMessage `json:"content"`
					} `json:"messages"`
				}
				requests := make(chan request, 2)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body request
					if r.URL.Path != "/chat/completions" || json.NewDecoder(r.Body).Decode(&body) != nil {
						http.Error(w, "invalid chat request", http.StatusBadRequest)
						return
					}
					select {
					case requests <- body:
					default:
						t.Error("unexpected extra provider request")
						http.Error(w, "unexpected extra provider request", http.StatusInternalServerError)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: {\"id\":\"image-fixture\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"content\":\"image seen\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":2,\"total_tokens\":13}}\n\n", modelID)
					_, _ = io.WriteString(w, "data: [DONE]\n\n")
				}))
				defer server.Close()
				runtime, err := provider.NewConfiguredRuntime(t.Context(), provider.ConfiguredRuntimeOptions{
					Sources: &engineconfig.ConfigSources{User: &engineconfig.Config{}, Project: &engineconfig.Config{}},
					Resolution: provider.ResolveInput{Explicit: provider.Config{
						Provider: provider.ProviderAgenticGLM, Model: modelID,
						APIKey: "fixture-credential", BaseURL: server.URL,
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
				for _, input := range []UntrustedPromptInput{
					NewUntrustedPromptInput(
						NewPromptTextPart("before"),
						NewPromptImagePart(testUserImagePNGBase64, "image/png", detail),
						NewPromptTextPart("after"),
					),
					NewUntrustedPromptInput(NewPromptTextPart("follow up")),
				} {
					events, _ := eng.SubmitPromptInput(t.Context(), input)
					terminal, _ := collectPromptInputEvents(t, events)
					if terminal.Reason != TerminalCompleted || terminal.Err != nil {
						t.Fatalf("image turn terminal = %s, %v", terminal.Reason, terminal.Err)
					}
					assertNoActivePromptMedia(t, eng)
				}
				if len(requests) != 2 {
					t.Fatalf("provider calls = %d, want 2", len(requests))
				}
				want := []map[string]any{
					{"type": "text", "text": "before"},
					{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + testUserImagePNGBase64}},
					{"type": "text", "text": "after"},
				}
				for range 2 {
					body := <-requests
					if body.Model != modelID || !body.Stream {
						t.Fatal("runtime lost the exact model or streaming intent")
					}
					images := 0
					for _, message := range body.Messages {
						var parts []map[string]any
						if message.Role != "user" || json.Unmarshal(message.Content, &parts) != nil || len(parts) != 3 {
							continue
						}
						if !reflect.DeepEqual(parts, want) {
							t.Fatal("provider changed image bytes/order or emitted an unsupported detail field")
						}
						images++
					}
					if images != 1 {
						t.Fatalf("ordered image messages = %d, want 1", images)
					}
				}
			})
		}
	}
}
