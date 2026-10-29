package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/provider/agentickimi"
)

const kimiDeliveryResponse = `{"id":"resp-kimi-delivery","object":"response","model":"kimi-k3","status":"completed","output":[{"type":"reasoning","status":"completed","summary":[{"type":"summary_text","text":"think"}],"encrypted_content":null},{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":3},"output_tokens_details":{"reasoning_tokens":1}},"prompt_cache_options":{"mode":"implicit","ttl":"5m"}}`

func TestKimiFactoryPreservesWireReasoningAndUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			requests := make(chan map[string]any, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					http.Error(w, "invalid", 400)
					return
				}
				if r.URL.Path != "/v1/responses" {
					t.Error("wrong protocol path")
				}
				select {
				case requests <- body:
				default:
					t.Error("unexpected request")
					http.Error(w, "unexpected", 500)
					return
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":0,\"response\":%s}\n\n", kimiDeliveryResponse)
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, kimiDeliveryResponse)
				}
			}))
			defer server.Close()
			client, err := NewChatModel(t.Context(), Config{Provider: Provider("agentickimi"), Model: "kimi-k3", APIKey: "sentinel-provider-credential", BaseURL: server.URL + "/v1"})
			if err != nil {
				t.Fatal(err)
			}
			input := []*schema.Message{{Role: schema.Assistant, Content: "prior", ReasoningContent: "preserved thinking"}, schema.UserMessage("fixture")}
			var output *schema.Message
			if stream {
				reader, err := client.Stream(t.Context(), input, agentickimi.WithReasoningEffort(agentickimi.ReasoningEffortMax))
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				for {
					chunk, err := reader.Recv()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					output = chunk
				}
			} else {
				output, err = client.Generate(t.Context(), input, agentickimi.WithReasoningEffort(agentickimi.ReasoningEffortMax))
				if err != nil {
					t.Fatal(err)
				}
			}
			if output == nil || output.Content != "ok" || output.ReasoningContent != "think" || output.ResponseMeta == nil || output.ResponseMeta.FinishReason != "stop" || output.ResponseMeta.Usage.PromptTokenDetails.CacheWriteTokens != 3 {
				t.Fatalf("factory output dropped Kimi facts: %#v", output)
			}
			body := <-requests
			inputItems := body["input"].([]any)
			if inputItems[0].(map[string]any)["type"] != "reasoning" || inputItems[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] != "preserved thinking" {
				t.Fatal("classic bridge lost preserved plain thinking")
			}
		})
	}
}

func TestKimiAutomaticDetectionPreservesExistingProviderPriority(t *testing.T) {
	for _, keyName := range []string{"MOONSHOT_API_KEY", "KIMI_API_KEY"} {
		for _, withQwen := range []bool{false, true} {
			values := map[string]string{keyName: "sentinel-provider-credential"}
			if withQwen {
				values["QWEN_API_KEY"] = "sentinel-provider-credential"
			}
			resolved, err := ResolveConfig(ResolveInput{Getenv: func(name string) string { return values[name] }, CredentialLookup: func(string) (string, bool, error) { return "", false, nil }})
			if err != nil {
				t.Fatal(err)
			}
			want := ProviderAgenticKimi
			if withQwen {
				want = ProviderAgenticQwen
			}
			if resolved.Provider != want {
				t.Fatalf("automatic provider=%s, want %s", resolved.Provider, want)
			}
		}
	}
}
