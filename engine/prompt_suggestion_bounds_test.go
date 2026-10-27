package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/engine/services"
	"github.com/abietic/yhc/engine/session"
)

func TestPromptSuggestionDeepSeekShortBudgetUsesNonThinkingWire(t *testing.T) {
	for _, tc := range []struct {
		name, source, wantEffort string
	}{
		{"ghost", "prompt_suggestion_generation", "none"},
		{"argument suggestion", "command_argument_suggestion", "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type request struct {
				Model           string `json:"model"`
				MaxOutputTokens int    `json:"max_output_tokens"`
				Reasoning       *struct {
					Effort string `json:"effort"`
				} `json:"reasoning"`
				Tools []json.RawMessage `json:"tools"`
			}
			requests := make(chan request, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body request
				if r.Method != http.MethodPost || r.URL.Path != "/responses" || json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("invalid suggestion request")
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				requests <- body
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "event: response.created\ndata: "+`{"type":"response.created","sequence_number":0,"response":{"id":"suggestion-bound","object":"response","status":"in_progress","model":"deepseek-flash"}}`+"\n\n")
				_, _ = fmt.Fprint(w, "event: response.output_text.delta\ndata: "+`{"type":"response.output_text.delta","sequence_number":1,"item_id":"suggestion-message","output_index":0,"content_index":0,"delta":"run the focused tests"}`+"\n\n")
				_, _ = fmt.Fprint(w, "event: response.completed\ndata: "+`{"type":"response.completed","sequence_number":2,"response":{"id":"suggestion-bound","object":"response","status":"completed","model":"deepseek-flash","output":[],"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":0},"output_tokens":4,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":14}}}`+"\n\n")
			}))
			defer server.Close()
			chatModel, err := provider.NewChatModel(t.Context(), provider.Config{
				Provider: provider.ProviderAgenticDeepSeek, Model: "deepseek-flash", APIKey: "fixture-key", BaseURL: server.URL,
			})
			if err != nil {
				t.Fatal(err)
			}
			eng := NewQueryEngine(QueryEngineConfig{
				CWD: t.TempDir(), TranscriptDir: t.TempDir(), ChatModel: chatModel, Model: "deepseek-flash",
				CommandEntrypoint: commands.EntrypointTUI, EnablePromptSuggestions: true,
			})
			t.Cleanup(eng.Close)
			binding := &session.PersistedModelBinding{
				Version: session.PersistedModelBindingVersion, Kind: session.ModelBindingKindProfile, Value: "primary",
				Provider: string(provider.ProviderAgenticDeepSeek), APIModel: "deepseek-flash", ReasoningEffort: "high",
				PortfolioRevision: strings.Repeat("a", 64), RouteIdentityDigest: strings.Repeat("b", 64), MetadataDigest: strings.Repeat("c", 64),
			}
			eng.mu.Lock()
			eng.modelBinding = binding.Clone()
			eng.modelDispatchBlock = nil
			eng.messages = []*schema.Message{{Role: schema.Assistant, Content: "first"}, {Role: schema.Assistant, Content: "done"}}
			eng.mu.Unlock()
			before, _ := json.Marshal(eng.GetMessages())
			got, err := eng.generateAuxiliarySuggestionProvider(t.Context(), []string{"run the focused tests next"}, services.GetSuggestionPrompt(), tc.source)
			if err != nil || got != "run the focused tests" {
				t.Fatalf("suggestion = %q, %v", got, err)
			}
			body := <-requests
			effort := ""
			if body.Reasoning != nil {
				effort = body.Reasoning.Effort
			}
			if body.Model != "deepseek-flash" || body.MaxOutputTokens != 64 || len(body.Tools) != 0 || effort != tc.wantEffort {
				t.Fatalf("suggestion wire model=%q max_output_tokens=%d tools=%d effort=%q, want %q", body.Model, body.MaxOutputTokens, len(body.Tools), effort, tc.wantEffort)
			}
			after, _ := json.Marshal(eng.GetMessages())
			eng.mu.Lock()
			bindingAfter := eng.modelBinding.Clone()
			eng.mu.Unlock()
			if !bytes.Equal(before, after) || !reflect.DeepEqual(binding, bindingAfter) {
				t.Fatal("auxiliary call changed the main conversation or reasoning binding")
			}
		})
	}
}

func TestPromptSuggestionRejectsIncompleteProviderCandidate(t *testing.T) {
	for _, reason := range []string{"length", "max_tokens", "max_output_tokens"} {
		t.Run(reason, func(t *testing.T) {
			probe := &promptSuggestionProbeModel{result: "run the focused tests", finishReason: reason}
			eng := NewQueryEngine(QueryEngineConfig{
				CWD: t.TempDir(), TranscriptDir: t.TempDir(), ChatModel: probe,
				CommandEntrypoint: commands.EntrypointTUI, EnablePromptSuggestions: true,
			})
			t.Cleanup(eng.Close)
			eng.SetResumedMessages([]*schema.Message{{Role: schema.Assistant, Content: "first"}, {Role: schema.Assistant, Content: "done"}})
			result, err := eng.GeneratePromptSuggestionResult(t.Context())
			if err != nil || result.Status != services.PromptSuggestionEmpty || result.Suggestion != "" {
				t.Fatalf("incomplete candidate was exposed: %+v, %v", result, err)
			}
			calls, _, _, tools, maxTokens, _ := probe.snapshot()
			if calls != 1 || tools != 0 || maxTokens != 64 {
				t.Fatal("discarding an incomplete suggestion retried or changed request bounds")
			}
			if usage := eng.providerUsageSummary(); usage.TotalTokens != 12 || usage.ResponsesWithMetadata != 1 {
				t.Fatal("discarding an incomplete suggestion lost its provider usage")
			}
		})
	}
}
