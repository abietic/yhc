package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	engineconfig "github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/tools"
)

func TestKimiConfiguredQueryEngineToolLoopAndReasoningReplay(t *testing.T) {
	type item struct {
		Type    string                        `json:"type"`
		CallID  string                        `json:"call_id"`
		Output  *string                       `json:"output"`
		Content []struct{ Type, Text string } `json:"content"`
	}
	type request struct {
		Model     string `json:"model"`
		Stream    bool   `json:"stream"`
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		Input []item `json:"input"`
	}
	requests := make(chan request, 3)
	var dispatches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body request
		if r.URL.Path != "/v1/responses" || json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		select {
		case requests <- body:
		default:
			t.Error("unexpected request")
			http.Error(w, "unexpected", 500)
			return
		}
		call := dispatches.Add(1)
		output := `{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done"}]}`
		if call == 1 {
			output = `{"type":"function_call","id":"fc-fixture","call_id":"call-fixture","name":"KimiFixture","arguments":"{}","status":"completed"}`
		}
		response := fmt.Sprintf(`{"id":"resp-fixture-%d","object":"response","model":"kimi-k3","status":"completed","output":[{"type":"reasoning","status":"completed","summary":[{"type":"summary_text","text":"thought-%d"}],"encrypted_content":null},%s],"usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":3},"output_tokens_details":{"reasoning_tokens":1}},"prompt_cache_options":{"mode":"implicit","ttl":"5m"}}`, call, call, output)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":0,\"response\":%s}\n\n", response)
	}))
	defer server.Close()
	runtime, err := provider.NewConfiguredRuntime(t.Context(), provider.ConfiguredRuntimeOptions{
		Sources:    &engineconfig.ConfigSources{User: &engineconfig.Config{}, Project: &engineconfig.Config{}},
		Resolution: provider.ResolveInput{Explicit: provider.Config{Provider: provider.ProviderAgenticKimi, Model: "kimi-k3", APIKey: "sentinel-provider-credential", BaseURL: server.URL + "/v1"}, Getenv: func(string) string { return "" }},
	})
	if err != nil {
		t.Fatal(err)
	}
	info := &schema.ToolInfo{Name: "KimiFixture", Desc: "fixture tool"}
	registry := tools.NewRegistry()
	var executions atomic.Int32
	registry.Register(tools.ToolImpl{Info: info, IsReadOnly: true, DefaultPermissionAllowed: true, Capabilities: tools.ToolCapabilities{Declared: true, Origin: tools.ToolOriginApp, ActionKind: tools.ToolActionRead}, ExecuteCtx: func(context.Context, string) (string, error) { executions.Add(1); return "fixture-result", nil }})
	eng := NewQueryEngine(QueryEngineConfig{Model: "kimi-k3", ModelResolver: runtime, ChatModel: runtime.ChatModel, Tools: []*schema.ToolInfo{info}, ToolRegistry: registry, CWD: t.TempDir(), TranscriptDir: t.TempDir(), MaxTurns: 3, HookExecutor: hooks.NewExecutor()})
	t.Cleanup(eng.Close)
	if _, err := eng.ChangeReasoningEffort(t.Context(), "medium"); err == nil {
		t.Fatal("Kimi accepted unsupported effort")
	}
	if _, err := eng.ChangeReasoningEffort(t.Context(), "max"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for _, prompt := range []string{"tool round", "follow up"} {
		events, _ := eng.SubmitPromptInput(ctx, NewUntrustedPromptInput(NewPromptTextPart(prompt)))
		terminal, _ := collectPromptInputEvents(t, events)
		if terminal.Reason != TerminalCompleted || terminal.Err != nil {
			t.Fatalf("terminal=%s, %v", terminal.Reason, terminal.Err)
		}
	}
	if executions.Load() != 1 || dispatches.Load() != 3 {
		t.Fatalf("tools=%d provider calls=%d", executions.Load(), dispatches.Load())
	}
	for call := 0; call < 3; call++ {
		body := <-requests
		if body.Model != "kimi-k3" || !body.Stream || body.Reasoning.Effort != "max" {
			t.Fatalf("wire policy=%#v", body)
		}
		thoughts := 0
		results := 0
		for _, part := range body.Input {
			if part.Type == "reasoning" {
				thoughts++
			}
			if part.Type == "function_call_output" {
				results++
				if part.CallID != "call-fixture" || part.Output == nil || *part.Output != "fixture-result" {
					t.Fatalf("tool correlation=%#v", part)
				}
			}
		}
		if thoughts != call || call > 0 && results != 1 {
			t.Fatalf("call %d replay thoughts=%d results=%d", call, thoughts, results)
		}
	}
}
