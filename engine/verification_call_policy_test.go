package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	engineconfig "github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Observe the real DeepSeek adapter, prepared HTTP body, settled ledger, and
// production query kernel. No live provider or task-specific oracle is used.
func TestVerificationPolicyWireAndReservations(t *testing.T) {
	for _, variant := range []string{"inherit", "all", "checker-only"} {
		t.Run(variant, func(t *testing.T) {
			var mu sync.Mutex
			var bodies []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				bodies = append(bodies, body)
				n := len(bodies)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				sequence := 0
				write := func(kind string, value map[string]any) {
					value["sequence_number"] = sequence
					sequence++
					value["type"] = kind
					encoded, _ := json.Marshal(value)
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encoded)
				}
				if n == 2 {
					write("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"type": "function_call", "id": "fc-1", "call_id": "verify-command", "name": "Bash", "arguments": ""}})
					write("response.function_call_arguments.delta", map[string]any{"item_id": "fc-1", "output_index": 0, "delta": `{"command":"independent check"}`})
				} else {
					content := "done"
					if n == 3 {
						content = verificationPass
					}
					if n == 4 {
						content = `{"verdict":"SUPPORTED","missing":[]}`
					}
					write("response.output_text.delta", map[string]any{"delta": content, "output_index": 0, "content_index": 0, "item_id": "msg-1"})
				}
				write("response.completed", map[string]any{"response": map[string]any{"id": fmt.Sprint(n), "object": "response", "status": "completed", "model": "deepseek-flash", "output": []any{}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 2, "total_tokens": 12}}})
			}))
			defer server.Close()
			runtime, err := provider.NewConfiguredRuntime(t.Context(), provider.ConfiguredRuntimeOptions{
				Sources:    &engineconfig.ConfigSources{User: &engineconfig.Config{}, Project: &engineconfig.Config{}},
				Resolution: provider.ResolveInput{Explicit: provider.Config{Provider: provider.ProviderAgenticDeepSeek, Model: "deepseek-flash", APIKey: "fixture", BaseURL: server.URL}},
			})
			if err != nil {
				t.Fatal(err)
			}
			call, err := runtime.ResolveRoleCall(provider.RoleResolutionInput{Role: engineconfig.RoleMain, MainSelector: runtime.InventorySnapshot().Default, Requirements: provider.RoleRequirements{RequestedEffort: "high"}})
			if err != nil {
				t.Fatal(err)
			}
			identity, err := modelCallIdentityFromRoleSnapshot(call)
			if err != nil {
				t.Fatal(err)
			}
			card := execution.RunPriceCard{Version: 1, Currency: "CNY", Source: "https://example.com/pricing", AsOf: "2026-10-12T00:00:00Z", Window: "upper_bound", Routes: []execution.RunPriceRoute{{Provider: "agenticdeepseek", Model: call.Selector, APIModel: "deepseek-flash", CachedInputPerMillion: "0.04", UncachedInputPerMillion: "2", OutputPerMillion: "8"}}}
			usage, err := execution.NewPricedRunUsage(execution.RunUsageLimits{MaxProviderCalls: 4, MaxEstimatedCost: "3", ReserveCostBeforeDispatch: true, MaxOutputTokensPerCall: 16384}, card)
			if err != nil {
				t.Fatal(err)
			}
			cfg := IndependentVerificationConfig{MaxTurns: 2, CoverageReview: true}
			if variant != "inherit" {
				cfg.CheckerEffort = "low"
				cfg.CheckerMaxOutputTokens = 8192
				cfg.CoverageEffort = "none"
				cfg.CoverageMaxOutputTokens = 2048
				if variant == "checker-only" {
					cfg.CoverageEffort = ""
					cfg.CoverageMaxOutputTokens = 0
				}
			}
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			params := QueryParams{Messages: []*schema.Message{schema.UserMessage("original contract")}, ChatModel: runtime.ChatModel, modelCall: identity, modelResolver: runtime, RunUsage: usage, IndependentVerification: cfg, ToolRegistry: registry, ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{MainLoopModel: call.Selector, EffortValue: "high", Tools: registry.List()}}, ToolExecutor: func(context.Context, string, string) (string, error) { return "ok", nil }}
			bad := params
			bad.IndependentVerification.CheckerEffort = "medium"
			_, rejected := collectEvents(t.Context(), bad)
			if rejected.Err == nil || usage.Snapshot().ProviderCalls != 0 {
				t.Fatal("unsupported DeepSeek effort dispatched")
			}
			bad = params
			bad.RunUsage, _ = execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 4})
			bad.IndependentVerification.CheckerMaxOutputTokens = 393217
			_, rejected = collectEvents(t.Context(), bad)
			if rejected.Err == nil || bad.RunUsage.Snapshot().ProviderCalls != 0 {
				t.Fatal("output beyond admitted model capacity dispatched")
			}
			_, terminal := collectEvents(t.Context(), params)
			if terminal.Err != nil {
				t.Fatal(terminal.Err)
			}
			wantEfforts := []string{"high", "high", "high", "high"}
			wantCaps := []int{16384, 16384, 16384, 16384}
			if variant != "inherit" {
				wantEfforts = []string{"high", "low", "low", "none"}
				wantCaps = []int{16384, 8192, 8192, 2048}
				if variant == "checker-only" {
					wantEfforts[3] = "high"
					wantCaps[3] = 16384
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(bodies) != 4 {
				t.Fatalf("requests=%d", len(bodies))
			}
			for i, body := range bodies {
				if body["model"] != "deepseek-flash" || body["max_output_tokens"] != float64(wantCaps[i]) {
					t.Fatalf("request %d model=%v cap=%v want=%d", i, body["model"], body["max_output_tokens"], wantCaps[i])
				}
				reasoning, _ := body["reasoning"].(map[string]any)
				if reasoning["effort"] != wantEfforts[i] {
					t.Fatalf("request %d reasoning=%v want=%s", i, reasoning, wantEfforts[i])
				}
			}
			ledger := usage.Snapshot().CallLedger.Records
			if len(ledger) != 4 || !usage.Snapshot().Cost.Complete {
				t.Fatal("incomplete settled ledger")
			}
			for i, record := range ledger {
				if record.Effort != wantEfforts[i] {
					t.Fatalf("ledger %d effort=%s", i, record.Effort)
				}
				if record.Cost.Reservation.OutputTokens != uint64(wantCaps[i]) {
					t.Fatalf("reservation %d=%+v", i, record.Cost.Reservation)
				}
			}
			if identity.Reasoning != "high" || identity.binding.ReasoningEffort != "high" {
				t.Fatal("root binding mutated")
			}
		})
	}
}

func TestVerificationPolicyValidationAndResumeIdentity(t *testing.T) {
	limits := execution.RunUsageLimits{MaxProviderCalls: 8, MaxOutputTokensPerCall: 16384}
	for _, cfg := range []IndependentVerificationConfig{
		{CheckerEffort: "low"},
		{MaxTurns: 2, CheckerEffort: "default"},
		{MaxTurns: 2, CheckerMaxOutputTokens: -1},
		{MaxTurns: 2, CheckerMaxOutputTokens: 16385},
		{MaxTurns: 2, CoverageEffort: "low"},
		{MaxTurns: 2, CoverageMaxOutputTokens: 100},
	} {
		if cfg.Validate(limits) == nil {
			t.Fatalf("invalid config accepted: %+v", cfg)
		}
	}
	params := QueryParams{SessionID: "fixture", verificationWorkspace: "/fixture", Messages: []*schema.Message{schema.UserMessage("original")}, IndependentVerification: IndependentVerificationConfig{MaxTurns: 2}, Deps: defaultDeps()}
	params.RunUsage, _ = execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 8})
	if err := prepareIndependentVerification(&params); err != nil {
		t.Fatal(err)
	}
	cursor := params.independentVerification.cursor
	if err := cursor.validate(&params); err != nil {
		t.Fatal("legacy default cursor rejected", err)
	}
	params.IndependentVerification.CheckerMaxOutputTokens = 8192
	if cursor.validate(&params) == nil {
		t.Fatal("resumed with changed policy")
	}
	before := params.RunUsage.Snapshot().ProviderCalls
	params.IndependentVerification = IndependentVerificationConfig{MaxTurns: 2, CheckerEffort: "low"}
	if prepareIndependentVerification(&params) == nil || before != params.RunUsage.Snapshot().ProviderCalls {
		t.Fatal("unadmitted effort dispatched or accepted")
	}
}

func TestVerificationPolicyRecoveryCeiling(t *testing.T) {
	for _, requested := range []int{64000, 512} {
		mdl := &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done")}}
		deps := defaultDeps()
		deps.CallModel = func(ctx context.Context, m model.BaseChatModel, messages []*schema.Message, system *schema.Message, infos []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
			want := min(requested, 8192)
			if opts.MaxOutputTokens == nil || *opts.MaxOutputTokens != want {
				t.Fatalf("recovery cap=%v want=%d", opts.MaxOutputTokens, want)
			}
			return execution.CallModel(ctx, m, messages, system, infos, opts)
		}
		result := runCanonicalModelRound(t.Context(), canonicalModelRoundInput{params: QueryParams{ChatModel: mdl, modelOutputTokenLimit: 8192}, deps: deps, messagesForQuery: []*schema.Message{schema.UserMessage("fixture")}, maxOutputTokensOverride: &requested, yield: func(QueryEvent) {}})
		if result.terminal != nil {
			t.Fatal(result.terminal)
		}
	}
}

func TestVerificationPolicyBudgetContinuation(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) { testVerificationPolicyBudgetContinuation(t, legacy) })
	}
}

func testVerificationPolicyBudgetContinuation(t *testing.T, legacy bool) {
	cfg := IndependentVerificationConfig{MaxTurns: 2, CoverageReview: true, CheckerMaxOutputTokens: 8192, CoverageMaxOutputTokens: 2048}
	if legacy {
		cfg.CheckerMaxOutputTokens = 0
		cfg.CoverageMaxOutputTokens = 0
	}
	usage, _ := execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 1})
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	var saved verificationCursor
	params := QueryParams{SessionID: "fixture", verificationWorkspace: "/fixture", Messages: []*schema.Message{schema.UserMessage("original contract")}, ChatModel: &canonicalScriptModel{responses: []canonicalModelResponse{verificationResponse("done")}}, RunUsage: usage, ToolRegistry: registry, IndependentVerification: cfg, ToolExecutor: func(context.Context, string, string) (string, error) { return "ok", nil }, commitVerificationCursor: func(c verificationCursor) error { saved = c; return nil }}
	_, first := collectEvents(t.Context(), params)
	if first.Err == nil || saved.Phase != "check" || saved.CheckerMaxOutputTokens != cfg.CheckerMaxOutputTokens || saved.CoverageMaxOutputTokens != cfg.CoverageMaxOutputTokens {
		t.Fatalf("pause=%+v cursor=%+v", first, saved)
	}
	tool := verificationResponse("")
	tool.chunks[0].ToolCalls = []schema.ToolCall{{ID: "verify-command", Type: "function", Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"independent check"}`}}}
	params.ChatModel = &canonicalScriptModel{responses: []canonicalModelResponse{tool, verificationResponse(verificationPass), verificationResponse(`{"verdict":"SUPPORTED","missing":[]}`)}}
	params.independentVerificationContinuation = true
	params.loadVerificationCursor = func() (*verificationCursor, error) {
		encoded, _ := json.Marshal(saved)
		if legacy && (strings.Contains(string(encoded), "checker_effort") || strings.Contains(string(encoded), "checker_max_output_tokens") || strings.Contains(string(encoded), "coverage_effort") || strings.Contains(string(encoded), "coverage_max_output_tokens")) {
			t.Fatal("default cursor wrote new policy fields")
		}
		var c verificationCursor
		err := json.Unmarshal(encoded, &c)
		return &c, err
	}
	params.RunUsage, _ = execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 3})
	params.Deps = &QueryDeps{ProviderUsage: params.RunUsage, CallModel: func(ctx context.Context, m model.BaseChatModel, messages []*schema.Message, system *schema.Message, infos []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
		want := 8192
		if opts.QuerySource == "independent_verification_coverage" {
			want = 2048
		} else if opts.QuerySource != "independent_verification" {
			t.Fatal("resume dispatched solver")
		}
		if legacy {
			if opts.MaxOutputTokens != nil {
				t.Fatal("legacy cursor changed output defaults")
			}
		} else if opts.MaxOutputTokens == nil || *opts.MaxOutputTokens != want {
			t.Fatal("resume lost stage ceiling")
		}
		return execution.CallModel(ctx, m, messages, system, infos, opts)
	}}
	_, resumed := collectEvents(t.Context(), params)
	if resumed.Err != nil || params.RunUsage.Snapshot().ProviderCalls != 3 {
		t.Fatal("resume failed", resumed)
	}
	saved.Phase = "check"
	params.IndependentVerification.CoverageMaxOutputTokens = 1024
	params.RunUsage, _ = execution.NewRunUsage(execution.RunUsageLimits{MaxProviderCalls: 3})
	params.Deps = &QueryDeps{ProviderUsage: params.RunUsage}
	_, mismatch := collectEvents(t.Context(), params)
	if mismatch.Err == nil || params.RunUsage.Snapshot().ProviderCalls != 0 {
		t.Fatal("policy mismatch dispatched")
	}
}

func TestVerificationPolicyOverloadKeepsEffort(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	handler := func(overloaded bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			calls++
			mu.Unlock()
			reasoning, _ := body["reasoning"].(map[string]any)
			if reasoning["effort"] != "high" || body["max_output_tokens"] != float64(1024) {
				t.Errorf("attempt effort=%v cap=%v", reasoning, body["max_output_tokens"])
			}
			if overloaded {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(529)
				fmt.Fprint(w, `{"error":{"message":"overloaded","type":"overloaded_error"}}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: response.output_text.delta\ndata: "+`{"type":"response.output_text.delta","sequence_number":0,"item_id":"msg","output_index":0,"content_index":0,"delta":"done"}`+"\n\nevent: response.completed\ndata: "+`{"type":"response.completed","sequence_number":1,"response":{"id":"resp","object":"response","status":"completed","model":"deepseek-flash","output":[],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`+"\n\n")
		}
	}
	primary := httptest.NewServer(handler(true))
	defer primary.Close()
	alternate := httptest.NewServer(handler(false))
	defer alternate.Close()
	t.Setenv("YHC_STAGE_POLICY_FIXTURE_KEY", "fixture")
	config := &engineconfig.Config{ModelProfile: "primary", ProviderAccounts: map[string]engineconfig.ProviderAccountConfig{
		"primary":   {Provider: "agenticdeepseek", BaseURL: primary.URL, Auth: engineconfig.AccountAuthConfig{Kind: "env", Name: "YHC_STAGE_POLICY_FIXTURE_KEY"}},
		"alternate": {Provider: "agenticdeepseek", BaseURL: alternate.URL, Auth: engineconfig.AccountAuthConfig{Kind: "env", Name: "YHC_STAGE_POLICY_FIXTURE_KEY"}},
	}, ModelProfiles: map[string]engineconfig.ModelProfileConfig{
		"primary":   {Account: "primary", APIModel: "deepseek-flash", Reasoning: engineconfig.ReasoningDefaultsConfig{DefaultEffort: "low"}},
		"alternate": {Account: "alternate", APIModel: "deepseek-flash", Reasoning: engineconfig.ReasoningDefaultsConfig{DefaultEffort: "none"}},
	}, FailoverPolicies: map[string]engineconfig.FailoverPolicyConfig{"main": {Alternates: []string{"alternate"}, On: []string{"overloaded"}, MaxSwitches: 1, MaxProviderCalls: 4, MaxElapsedMS: 2000}}}
	runtime, err := provider.NewConfiguredRuntime(t.Context(), provider.ConfiguredRuntimeOptions{Sources: &engineconfig.ConfigSources{User: config, Project: &engineconfig.Config{}}})
	if err != nil {
		t.Fatal(err)
	}
	call, err := runtime.ResolveRoleCall(provider.RoleResolutionInput{Role: engineconfig.RoleMain, MainSelector: "primary"})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := modelCallIdentityFromRoleSnapshot(call)
	if err != nil {
		t.Fatal(err)
	}
	root := QueryParams{ChatModel: runtime.ChatModel, modelResolver: runtime, modelCall: identity, IndependentVerification: IndependentVerificationConfig{MaxTurns: 2, CheckerEffort: "high", CheckerMaxOutputTokens: 1024}, ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{MainLoopModel: "primary"}}, retryBaseDelay: time.Millisecond}
	checker := root
	if err := applyVerificationCallPolicy(&checker, root, false); err != nil {
		t.Fatal(err)
	}
	result := runCanonicalModelRound(t.Context(), canonicalModelRoundInput{params: checker, deps: defaultDeps(), toolUseContext: checker.ToolUseContext, messagesForQuery: []*schema.Message{schema.UserMessage("fixture")}, yield: func(QueryEvent) {}})
	if result.terminal != nil || result.attemptedModel != "alternate" || identity.Reasoning != "low" {
		t.Fatalf("round=%+v root effort=%s", result, identity.Reasoning)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 4 {
		t.Fatalf("dispatches=%d", calls)
	}
}
