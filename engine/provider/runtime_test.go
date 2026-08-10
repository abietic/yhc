package provider

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type routeCall struct {
	model string
	tools int
}

type routeRecorder struct {
	mu    sync.Mutex
	calls []routeCall
}

func (r *routeRecorder) add(call routeCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

type routeModel struct {
	provider Provider
	recorder *routeRecorder
	tools    []*schema.ToolInfo
}

func (m *routeModel) Generate(_ context.Context, _ []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	common := model.GetCommonOptions(nil, opts...)
	modelName := ""
	if common.Model != nil {
		modelName = *common.Model
	}
	m.recorder.add(routeCall{model: modelName, tools: len(m.tools)})
	return &schema.Message{Role: schema.Assistant, Content: string(m.provider)}, nil
}

func (m *routeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream is not used by this test")
}

func (m *routeModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	clone := *m
	clone.tools = append([]*schema.ToolInfo(nil), tools...)
	return &clone, nil
}

func TestRuntimeRoutesAliasesAcrossProvidersLazily(t *testing.T) {
	ctx := context.Background()
	factoryCalls := make(map[Provider]int)
	recorders := make(map[Provider]*routeRecorder)
	var factoryMu sync.Mutex
	factory := func(_ context.Context, cfg Config) (model.BaseChatModel, error) {
		factoryMu.Lock()
		defer factoryMu.Unlock()
		factoryCalls[cfg.Provider]++
		recorder := &routeRecorder{}
		recorders[cfg.Provider] = recorder
		return &routeModel{provider: cfg.Provider, recorder: recorder}, nil
	}

	runtime, err := NewRuntime(ctx, RuntimeOptions{
		Resolution: ResolveInput{
			Explicit: Config{
				Provider: ProviderAgenticOpenAI,
				Model:    "gpt-4o",
				APIKey:   "openai-key",
				ModelAliases: map[string]string{
					"fast": "google:gemini-2.5-flash",
				},
			},
			Getenv: getenvMap(map[string]string{
				"GOOGLE_API_KEY": "google-key",
			}),
			CredentialLookup: credStore(nil),
		},
		factory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	if factoryCalls[ProviderAgenticOpenAI] != 1 || factoryCalls[ProviderAgenticGemini] != 0 {
		t.Fatalf("main route initialization = %#v, want only OpenAI once", factoryCalls)
	}

	prepared, err := runtime.PrepareModel(ctx, "fast")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Provider != ProviderAgenticGemini || prepared.Model != "gemini-2.5-flash" {
		t.Fatalf("prepared route = %s:%s", prepared.Provider, prepared.Model)
	}
	if _, err := runtime.PrepareModel(ctx, "fast"); err != nil {
		t.Fatal(err)
	}
	if factoryCalls[ProviderAgenticGemini] != 1 {
		t.Fatalf("Gemini factory calls = %d, want 1", factoryCalls[ProviderAgenticGemini])
	}

	toolModel, ok := runtime.ChatModel.(model.ToolCallingChatModel)
	if !ok {
		t.Fatal("routing model does not expose ToolCallingChatModel")
	}
	bound, err := toolModel.WithTools([]*schema.ToolInfo{{Name: "Read"}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := bound.Generate(ctx, nil, model.WithModel("fast"), model.WithTemperature(0.2))
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != string(ProviderAgenticGemini) {
		t.Fatalf("response provider = %q", response.Content)
	}
	recorder := recorders[ProviderAgenticGemini]
	if recorder == nil || len(recorder.calls) != 1 {
		t.Fatalf("Gemini calls = %#v", recorder)
	}
	if call := recorder.calls[0]; call.model != "gemini-2.5-flash" || call.tools != 1 {
		t.Fatalf("routed call = %#v", call)
	}
}

func TestRuntimePreflightRunsOncePerRouteAndFailsBeforeFactory(t *testing.T) {
	ctx := context.Background()
	preflightCalls := 0
	factoryCalls := 0
	check := func(_ context.Context, cfg ResolvedConfig, timeout time.Duration) error {
		preflightCalls++
		if cfg.Provider != ProviderAgenticOpenAI {
			t.Fatalf("preflight provider = %q", cfg.Provider)
		}
		if timeout != 2*time.Second {
			t.Fatalf("preflight timeout = %s", timeout)
		}
		return errors.New("authentication rejected")
	}
	factory := func(context.Context, Config) (model.BaseChatModel, error) {
		factoryCalls++
		return nil, errors.New("factory should not run")
	}

	_, err := NewRuntime(ctx, RuntimeOptions{
		Resolution: ResolveInput{
			Explicit: Config{Provider: ProviderAgenticOpenAI, Model: "gpt-4o", APIKey: "key"},
		},
		Preflight:        true,
		PreflightTimeout: 2 * time.Second,
		factory:          factory,
		preflight:        check,
	})
	if err == nil || !strings.Contains(err.Error(), "authentication rejected") {
		t.Fatalf("NewRuntime error = %v", err)
	}
	if preflightCalls != 1 || factoryCalls != 0 {
		t.Fatalf("preflight calls = %d, factory calls = %d", preflightCalls, factoryCalls)
	}
}
