package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	engineconfig "github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/provider"
)

func TestCanonicalModelRoundDispatchesConfiguredLegacyFallbackTransport(t *testing.T) {
	var (
		requestMu sync.Mutex
		models    []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(io.LimitReader(request.Body, 64<<10)).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		requestMu.Lock()
		models = append(models, payload.Model)
		ordinal := len(models)
		requestMu.Unlock()
		if ordinal <= execution.Max529Retries {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(529)
			_, _ = writer.Write([]byte(`{"error":{"message":"overloaded","type":"overloaded_error"}}`))
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("event: response.output_text.delta\n" +
			`data: {"type":"response.output_text.delta","sequence_number":0,"item_id":"msg-fallback","output_index":0,"content_index":0,"delta":"fallback done"}` + "\n\n" +
			"event: response.completed\n" +
			`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_fallback","object":"response","created_at":1,"status":"completed","model":"gpt-4o-mini","output":[],"parallel_tool_calls":false,"tool_choice":"auto","tools":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	}))
	defer server.Close()

	runtime, err := provider.NewConfiguredRuntime(t.Context(), provider.ConfiguredRuntimeOptions{
		Sources: &engineconfig.ConfigSources{
			User:    &engineconfig.Config{},
			Project: &engineconfig.Config{},
		},
		LegacyFallbackModel: "gpt-4o-mini",
		Resolution: provider.ResolveInput{Explicit: provider.Config{
			Provider: provider.ProviderAgenticOpenAI,
			Model:    "gpt-4o",
			APIKey:   "fixture-secret",
			BaseURL:  server.URL,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	primary, err := runtime.ResolveRoleCall(provider.RoleResolutionInput{
		Role:         engineconfig.RoleMain,
		MainSelector: runtime.InventorySnapshot().Default,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := modelCallIdentityFromRoleSnapshot(primary)
	if err != nil {
		t.Fatal(err)
	}
	toolContext := &ToolUseContext{Options: &ToolUseOptions{
		MainLoopModel: primary.Selector,
		Tools:         []*schema.ToolInfo{{Name: "Write", Desc: "write a file"}},
	}}
	var optionModels []string
	result := runCanonicalModelRound(t.Context(), canonicalModelRoundInput{
		params: QueryParams{
			ChatModel:         runtime.ChatModel,
			modelResolver:     runtime,
			modelCall:         identity,
			commandEntrypoint: "headless",
			retryBaseDelay:    time.Millisecond,
			ToolUseContext:    toolContext,
		},
		deps: &QueryDeps{
			UUID: func() string { return "fallback-transport-id" },
			CallModel: func(
				ctx context.Context,
				chatModel model.BaseChatModel,
				messages []*schema.Message,
				systemPrompt *schema.Message,
				tools []*schema.ToolInfo,
				opts execution.CallModelOptions,
			) (*execution.CallModelResult, error) {
				optionModels = append(optionModels, opts.Model)
				return execution.CallModel(ctx, chatModel, messages, systemPrompt, tools, opts)
			},
		},
		messagesForQuery:  []*schema.Message{schema.UserMessage("probe")},
		toolUseContext:    toolContext,
		cancellationChain: NewCancellationChain(t.Context()),
		yield:             func(QueryEvent) {},
	})
	if result.terminal != nil || len(result.assistantMessages) == 0 {
		t.Fatalf("round result = %#v", result)
	}
	want := []string{"legacy:gpt-4o", "legacy:gpt-4o", "legacy:gpt-4o", "legacy:gpt-4o-mini"}
	if !reflect.DeepEqual(optionModels, want) {
		t.Fatalf("call option models = %v, want %v", optionModels, want)
	}
	requestMu.Lock()
	defer requestMu.Unlock()
	if !reflect.DeepEqual(models, []string{"gpt-4o", "gpt-4o", "gpt-4o", "gpt-4o-mini"}) {
		t.Fatalf("transport models = %v", models)
	}
}
