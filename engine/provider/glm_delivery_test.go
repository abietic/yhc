package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestGLMFactoryPreservesExplicitOutputCapAndNativeUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "glm-5.3-flashx" || request.MaxTokens != 64 {
			t.Errorf("native GLM request = %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fixture","model":"glm-5.3-flashx","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":4}}}`))
	}))
	defer server.Close()
	client, err := NewChatModel(t.Context(), Config{
		Provider: ProviderAgenticGLM, Model: "glm-5.3-flashx",
		APIKey: "test", BaseURL: server.URL, MaxTokens: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := client.Generate(t.Context(), []*schema.Message{schema.UserMessage("hello")})
	if err != nil {
		t.Fatal(err)
	}
	u := out.ResponseMeta.Usage
	if u == nil || u.TotalTokens != 18 || u.PromptTokenDetails.CachedTokens != 3 || u.CompletionTokensDetails.ReasoningTokens != 4 {
		t.Fatalf("classic runtime usage = %#v", u)
	}
}
