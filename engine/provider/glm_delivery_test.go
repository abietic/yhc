package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/abietic/yhc/engine/execution"
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
	collector, _ := execution.NewRunUsage(execution.RunUsageLimits{})
	out, err := execution.GenerateWithUsage(t.Context(), client, []*schema.Message{schema.UserMessage("hello")}, collector, execution.ProviderUsageDescriptor{Provider: "agenticglm", Model: "glm-5.3-flashx", QuerySource: "approval_review"})
	if err != nil {
		t.Fatal(err)
	}
	record := collector.Snapshot().CallLedger.Records[0]
	if record.ResolvedModel != "glm-5.3-flashx" || record.State != "known" || record.Tokens.UncachedPromptTokens != 8 {
		t.Fatalf("record=%+v", record)
	}
	u := out.ResponseMeta.Usage
	if u == nil || u.TotalTokens != 18 || u.PromptTokenDetails.CachedTokens != 3 || u.CompletionTokensDetails.ReasoningTokens != 4 {
		t.Fatalf("classic runtime usage = %#v", u)
	}
}

func TestGLMStreamLedgerUsesTerminalResponseModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"fixture","model":"glm-5.3-flash","choices":[{"index":0,"delta":{"content":"ok"}}]}

data: {"id":"fixture","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"fixture","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":4}}}

data: [DONE]

`))
	}))
	defer server.Close()
	client, err := NewChatModel(t.Context(), Config{Provider: ProviderAgenticGLM, Model: "glm-5.3-flash", APIKey: "test", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	collector, _ := execution.NewRunUsage(execution.RunUsageLimits{})
	out, err := execution.SideQuery(t.Context(), client, execution.SideQueryOptions{Messages: []*schema.Message{schema.UserMessage("fixture")}, ProviderUsage: collector, UsageLogicalRoundID: collector.NewLogicalRoundID(), Provider: "agenticglm", Model: "glm-5.3-flash", QuerySource: "tool_use_summary_generation"})
	if err != nil {
		t.Fatal(err)
	}
	record := collector.Snapshot().CallLedger.Records[0]
	if out.Content != "ok" || record.State != "known" || record.ResolvedModel != "glm-5.3-flash" || record.Tokens.TotalTokens != 18 || record.Tokens.UncachedPromptTokens != 8 || record.Tokens.ReasoningTokens != 4 {
		t.Fatalf("out=%+v record=%+v", out, record)
	}
}
