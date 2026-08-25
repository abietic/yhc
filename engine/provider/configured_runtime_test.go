package provider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	engineconfig "github.com/abietic/yhc/engine/config"
	"github.com/cloudwego/eino/components/model"
)

func TestConfiguredRuntimeNamedRouteIsolationReuseAndLaziness(t *testing.T) {
	ctx := context.Background()
	sources := namedRuntimeSources()
	env := map[string]string{
		"OPENAI_A": portfolioRuntimeSecret,
		"OPENAI_B": "secondary-secret",
		"OPENAI_D": "distinct-auth-secret",
	}
	var (
		mu           sync.Mutex
		factoryCalls []Config
		recorders    []*routeRecorder
	)
	runtime, err := NewConfiguredRuntime(ctx, ConfiguredRuntimeOptions{
		Sources: sources,
		Resolution: ResolveInput{
			Getenv: getenvMap(env),
		},
		factory: func(_ context.Context, config Config) (model.BaseChatModel, error) {
			if strings.TrimSpace(config.APIKey) == "" {
				return nil, fmt.Errorf("named route factory received no credential")
			}
			mu.Lock()
			defer mu.Unlock()
			factoryCalls = append(factoryCalls, Config{
				Provider: config.Provider,
				Model:    config.Model,
				BaseURL:  config.BaseURL,
			})
			recorder := &routeRecorder{}
			recorders = append(recorders, recorder)
			return &routeModel{provider: config.Provider, recorder: recorder}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Main.APIKey != "" || !runtime.Main.CredentialConfigured {
		t.Fatalf("runtime retained or lost credential state: %#v", runtime.Main)
	}
	if diagnostics := runtime.PortfolioDiagnostics(); len(diagnostics) != 0 {
		t.Fatalf("unexpected portfolio diagnostics: %#v", diagnostics)
	}
	if _, err := runtime.ResolveModel("gpt-4o"); err != nil {
		t.Fatal(err)
	}
	if resolved, resolveErr := runtime.ResolveModel("same-route"); resolveErr != nil || resolved.Model != "gpt-4.1" {
		t.Fatalf("configured manual profile resolution = %#v, %v", resolved, resolveErr)
	}
	mu.Lock()
	if len(factoryCalls) != 1 {
		mu.Unlock()
		t.Fatalf("diagnostics or side-effect-free resolution initialized another route: %d", len(factoryCalls))
	}
	mu.Unlock()
	mainIdentity := runtime.routes.accountRoutes["account-a"]
	mainClient := runtime.routes.models[mainIdentity]

	sameRouteConfig, sameRouteClient, err := runtime.routes.routeNamedProfile(ctx, "same-route")
	if err != nil {
		t.Fatal(err)
	}
	if sameRouteClient != mainClient {
		t.Fatal("same account route with a different API model did not reuse its client")
	}
	if sameRouteConfig.Model != "gpt-4.1" {
		t.Fatalf("same route API model = %q", sameRouteConfig.Model)
	}
	if _, err := sameRouteClient.Generate(ctx, nil, model.WithModel(sameRouteConfig.Model)); err != nil {
		t.Fatal(err)
	}
	if calls := recorders[0].calls; len(calls) != 1 || calls[0].model != "gpt-4.1" {
		t.Fatalf("provider-local API model was not forwarded: %#v", calls)
	}

	_, endpointClient, err := runtime.routes.routeNamedProfile(ctx, "other-endpoint")
	if err != nil {
		t.Fatal(err)
	}
	_, authClient, err := runtime.routes.routeNamedProfile(ctx, "other-auth")
	if err != nil {
		t.Fatal(err)
	}
	if endpointClient == mainClient || authClient == mainClient || endpointClient == authClient {
		t.Fatal("distinct endpoint/auth routes reused a client")
	}
	if _, initialized := runtime.routes.accountRoutes["unused"]; initialized {
		t.Fatal("unused account route was initialized")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(factoryCalls) != 3 {
		t.Fatalf("factory calls = %d, want selected plus two distinct routes", len(factoryCalls))
	}
	for _, call := range factoryCalls {
		if call.APIKey != "" {
			t.Fatal("test recorder unexpectedly retained an API key")
		}
	}
	encoded, err := json.Marshal(struct {
		Portfolio *engineconfig.PortfolioSnapshot
		Routes    map[engineconfig.AccountID]RouteIdentity
	}{runtime.portfolio, runtime.routes.accountRoutes})
	if err != nil {
		t.Fatal(err)
	}
	secretHash := fmt.Sprintf("%x", sha256.Sum256([]byte(portfolioRuntimeSecret)))
	if strings.Contains(string(encoded), portfolioRuntimeSecret) || strings.Contains(string(encoded), secretHash) {
		t.Fatalf("non-secret runtime state exposed secret-derived data: %s", encoded)
	}
}

func TestConfiguredRuntimeRepairsNilRoutePublicationMaps(t *testing.T) {
	t.Parallel()
	runtime, err := NewConfiguredRuntime(t.Context(), ConfiguredRuntimeOptions{
		Sources: namedRuntimeSources(),
		Resolution: ResolveInput{Getenv: getenvMap(map[string]string{
			"OPENAI_A": portfolioRuntimeSecret,
			"OPENAI_B": "secondary-secret",
			"OPENAI_D": "distinct-auth-secret",
		})},
		factory: func(_ context.Context, config Config) (model.BaseChatModel, error) {
			return &routeModel{
				provider: config.Provider,
				recorder: &routeRecorder{},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.routes.mu.Lock()
	runtime.routes.models = nil
	runtime.routes.published = nil
	runtime.routes.accountRoutes = nil
	runtime.routes.mu.Unlock()
	if _, err := runtime.PrepareModel(t.Context(), "primary"); err != nil {
		t.Fatal(err)
	}
	runtime.routes.mu.Lock()
	defer runtime.routes.mu.Unlock()
	if runtime.routes.models == nil || runtime.routes.published == nil ||
		runtime.routes.accountRoutes == nil {
		t.Fatalf("route maps were not repaired: %#v", runtime.routes)
	}
}

func TestP294LegacyFallbackCompilesCanonicalCompatibilityBudget(t *testing.T) {
	snapshot, err := compileLegacyPortfolio(
		ResolveInput{
			Explicit: Config{
				Provider: ProviderAgenticOpenAI,
				Model:    "gpt-4o",
				APIKey:   "test-key",
			},
			Getenv: func(string) string { return "" },
		},
		"gpt-4o-mini",
	)
	if err != nil {
		t.Fatal(err)
	}
	policy, ok := snapshot.Failover[engineconfig.RoleMain]
	if !ok {
		t.Fatal("legacy fallback did not compile a main failover policy")
	}
	if len(policy.Alternates) != 1 ||
		policy.Alternates[0] != "legacy.fallback" ||
		len(policy.On) != 1 ||
		policy.On[0] != "overloaded" ||
		policy.MaxSwitches != 1 ||
		policy.MaxProviderCalls != 6 ||
		policy.MaxElapsedMS != 45000 {
		t.Fatalf("legacy compatibility policy = %#v", policy)
	}
}

func TestP294LegacyFallbackChainPreservesAlternateSelector(t *testing.T) {
	runtime, err := NewConfiguredRuntime(t.Context(), ConfiguredRuntimeOptions{
		Sources: &engineconfig.ConfigSources{
			User:    &engineconfig.Config{},
			Project: &engineconfig.Config{},
		},
		LegacyFallbackModel: "gpt-4o-mini",
		Resolution: ResolveInput{
			Explicit: Config{
				Provider: ProviderAgenticOpenAI,
				Model:    "gpt-4o",
				APIKey:   "test-key",
			},
			Getenv: func(string) string { return "" },
		},
		factory: func(_ context.Context, config Config) (model.BaseChatModel, error) {
			return &routeModel{provider: config.Provider, recorder: &routeRecorder{}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	chain, err := runtime.ResolveFailoverChain(RoleResolutionInput{
		Role:         engineconfig.RoleMain,
		MainSelector: "gpt-4o",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chain.Alternates) != 1 {
		t.Fatalf("alternates = %#v", chain.Alternates)
	}
	alternate := chain.Alternates[0]
	if alternate.AdmissionCode != "" ||
		alternate.Call.ProfileID != "legacy.fallback" ||
		alternate.Call.Selector != "legacy:gpt-4o-mini" ||
		alternate.Call.APIModel != "gpt-4o-mini" {
		t.Fatalf("legacy fallback call = %#v", alternate)
	}
}

func TestConfiguredRuntimeConstructsNamedRouteOnceConcurrently(t *testing.T) {
	ctx := context.Background()
	sources := namedRuntimeSources()
	var (
		mu            sync.Mutex
		constructions = make(map[string]int)
	)
	runtime, err := NewConfiguredRuntime(ctx, ConfiguredRuntimeOptions{
		Sources: sources,
		Resolution: ResolveInput{
			Getenv: getenvMap(map[string]string{
				"OPENAI_A": portfolioRuntimeSecret,
				"OPENAI_B": "secondary-secret",
				"OPENAI_D": "distinct-auth-secret",
			}),
		},
		factory: func(_ context.Context, config Config) (model.BaseChatModel, error) {
			mu.Lock()
			constructions[config.BaseURL]++
			mu.Unlock()
			return &routeModel{provider: config.Provider, recorder: &routeRecorder{}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 32
	start := make(chan struct{})
	results := make(chan model.BaseChatModel, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			<-start
			_, chatModel, routeErr := runtime.routes.routeNamedProfile(ctx, "other-endpoint")
			if routeErr != nil {
				errs <- routeErr
				return
			}
			results <- chatModel
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errs)
	for routeErr := range errs {
		t.Fatal(routeErr)
	}
	var first model.BaseChatModel
	for chatModel := range results {
		if first == nil {
			first = chatModel
		}
		if chatModel != first {
			t.Fatal("concurrent named route returned distinct clients")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if constructions["https://b.example/v1"] != 1 {
		t.Fatalf("secondary route constructions = %d", constructions["https://b.example/v1"])
	}
}

func TestConfiguredRuntimeProviderDefaultLowersConcreteIdentity(t *testing.T) {
	sources := &engineconfig.ConfigSources{
		User: &engineconfig.Config{
			ModelProfile: "primary",
			ProviderAccounts: map[string]engineconfig.ProviderAccountConfig{
				"default-openai": {
					Provider: "openai",
					Auth:     engineconfig.AccountAuthConfig{Kind: "provider_default"},
				},
			},
			ModelProfiles: map[string]engineconfig.ModelProfileConfig{
				"primary": {Account: "default-openai", APIModel: "gpt-4o"},
			},
		},
		Project: &engineconfig.Config{},
	}
	runtime, err := NewConfiguredRuntime(context.Background(), ConfiguredRuntimeOptions{
		Sources: sources,
		Resolution: ResolveInput{
			Getenv: getenvMap(map[string]string{
				"OPENAI_API_KEY": portfolioRuntimeSecret,
				"PROV_API_KEY":   "generic-must-not-own-named-route",
			}),
		},
		factory: func(_ context.Context, config Config) (model.BaseChatModel, error) {
			if config.APIKey != portfolioRuntimeSecret {
				return nil, fmt.Errorf("provider_default selected the wrong credential source")
			}
			return &routeModel{provider: config.Provider, recorder: &routeRecorder{}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := runtime.routes.accountRoutes["default-openai"]
	if identity.AuthKind != "env" || identity.AuthReference != "OPENAI_API_KEY" {
		t.Fatalf("provider_default identity was not lowered: %#v", identity)
	}
	envDigest, err := identity.Digest()
	if err != nil {
		t.Fatal(err)
	}
	inventory := runtime.InventorySnapshot()
	if len(inventory.Entries) != 1 ||
		inventory.Entries[0].RouteIdentityDigest != envDigest {
		t.Fatalf(
			"provider_default inventory digest = %#v, want %q",
			inventory,
			envDigest,
		)
	}

	credentialRuntime, err := NewConfiguredRuntime(
		context.Background(),
		ConfiguredRuntimeOptions{
			Sources: sources,
			Resolution: ResolveInput{
				Getenv: getenvMap(map[string]string{}),
				CredentialLookup: func(provider string) (string, bool, error) {
					if provider == "openai" {
						return "stored-provider-default", true, nil
					}
					return "", false, nil
				},
			},
			factory: func(
				_ context.Context,
				config Config,
			) (model.BaseChatModel, error) {
				if config.APIKey != "stored-provider-default" {
					return nil, fmt.Errorf(
						"provider_default did not select the credential store",
					)
				}
				return &routeModel{
					provider: config.Provider,
					recorder: &routeRecorder{},
				}, nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	credentialIdentity := credentialRuntime.routes.accountRoutes["default-openai"]
	if credentialIdentity.AuthKind != "credential" ||
		credentialIdentity.AuthReference != "openai" {
		t.Fatalf(
			"credential-store provider_default identity = %#v",
			credentialIdentity,
		)
	}
	credentialDigest, err := credentialIdentity.Digest()
	if err != nil {
		t.Fatal(err)
	}
	credentialInventory := credentialRuntime.InventorySnapshot()
	if len(credentialInventory.Entries) != 1 ||
		credentialInventory.Entries[0].RouteIdentityDigest != credentialDigest {
		t.Fatalf(
			"credential provider_default inventory = %#v, want %q",
			credentialInventory,
			credentialDigest,
		)
	}
	if credentialDigest == envDigest {
		t.Fatal("provider_default source drift retained the prior route digest")
	}
}

func TestConfiguredRuntimeResolvesNamedCredentialOnlyForConstructedRoute(t *testing.T) {
	sources := &engineconfig.ConfigSources{
		User: &engineconfig.Config{
			ModelProfile: "primary",
			ProviderAccounts: map[string]engineconfig.ProviderAccountConfig{
				"primary-account": {
					Provider: "openai",
					Auth:     engineconfig.AccountAuthConfig{Kind: "credential", Name: "openai-primary"},
				},
				"unused-account": {
					Provider: "openai",
					Auth:     engineconfig.AccountAuthConfig{Kind: "credential", Name: "openai-unused"},
				},
			},
			ModelProfiles: map[string]engineconfig.ModelProfileConfig{
				"primary": {Account: "primary-account", APIModel: "gpt-4o"},
				"unused":  {Account: "unused-account", APIModel: "gpt-4o-mini"},
			},
		},
		Project: &engineconfig.Config{},
	}
	var lookups []string
	runtime, err := NewConfiguredRuntime(context.Background(), ConfiguredRuntimeOptions{
		Sources: sources,
		Resolution: ResolveInput{
			Getenv: getenvMap(nil),
		},
		NamedCredentialLookup: func(name string) (string, error) {
			lookups = append(lookups, name)
			return portfolioRuntimeSecret, nil
		},
		factory: func(_ context.Context, config Config) (model.BaseChatModel, error) {
			if config.APIKey != portfolioRuntimeSecret {
				return nil, fmt.Errorf("named credential did not reach construction")
			}
			return &routeModel{provider: config.Provider, recorder: &routeRecorder{}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lookups) != 1 || lookups[0] != "openai-primary" {
		t.Fatalf("named credential lookups = %#v", lookups)
	}
	if _, initialized := runtime.routes.accountRoutes["unused-account"]; initialized {
		t.Fatal("unused named credential route was initialized")
	}
}

func TestConfiguredRuntimeRedactsNamedConstructionFailure(t *testing.T) {
	secretHash := fmt.Sprintf("%x", sha256.Sum256([]byte(portfolioRuntimeSecret)))
	_, err := NewConfiguredRuntime(context.Background(), ConfiguredRuntimeOptions{
		Sources: namedRuntimeSources(),
		Resolution: ResolveInput{
			Getenv: getenvMap(map[string]string{
				"OPENAI_A": portfolioRuntimeSecret,
				"OPENAI_B": "secondary-secret",
				"OPENAI_D": "distinct-auth-secret",
			}),
		},
		factory: func(context.Context, Config) (model.BaseChatModel, error) {
			return nil, fmt.Errorf("unsafe upstream key=%s hash=%s", portfolioRuntimeSecret, secretHash)
		},
	})
	if err == nil {
		t.Fatal("construction failure fixture did not fail")
	}
	if strings.Contains(err.Error(), portfolioRuntimeSecret) || strings.Contains(err.Error(), secretHash) {
		t.Fatalf("construction error exposed secret-derived data: %v", err)
	}
}

func TestConfiguredRuntimeLowersNamedProfilesThroughSixAdapters(t *testing.T) {
	t.Setenv("PORTFOLIO_ADAPTER_TEST_KEY", portfolioRuntimeSecret)
	cases := []struct {
		provider string
		model    string
		want     Provider
	}{
		{"anthropic", "claude-sonnet-4-6", ProviderAgenticClaude},
		{"openai", "gpt-4o", ProviderAgenticOpenAI},
		{"google", "gemini-2.5-flash", ProviderAgenticGemini},
		{"deepseek", "deepseek-v4-flash", ProviderAgenticDeepSeek},
		{"qwen", "qwen-max", ProviderAgenticQwen},
		{"ark", "doubao-1.5-pro-32k", ProviderAgenticArk},
	}
	for _, testCase := range cases {
		t.Run(testCase.provider, func(t *testing.T) {
			sources := &engineconfig.ConfigSources{
				User: &engineconfig.Config{
					ModelProfile: "primary",
					ProviderAccounts: map[string]engineconfig.ProviderAccountConfig{
						"primary-account": {
							Provider: testCase.provider,
							Auth: engineconfig.AccountAuthConfig{
								Kind: "env",
								Name: "PORTFOLIO_ADAPTER_TEST_KEY",
							},
						},
					},
					ModelProfiles: map[string]engineconfig.ModelProfileConfig{
						"primary": {Account: "primary-account", APIModel: testCase.model},
					},
				},
				Project: &engineconfig.Config{},
			}
			runtime, err := NewConfiguredRuntime(context.Background(), ConfiguredRuntimeOptions{
				Sources: sources,
				Resolution: ResolveInput{
					Getenv: getenvMap(map[string]string{
						"PORTFOLIO_ADAPTER_TEST_KEY": portfolioRuntimeSecret,
					}),
				},
			})
			if err != nil {
				t.Fatalf("named %s adapter construction: %v", testCase.provider, err)
			}
			if runtime.Main.Provider != testCase.want || runtime.Main.Model != testCase.model {
				t.Fatalf("named %s route = %#v", testCase.provider, runtime.Main)
			}
			if runtime.Main.APIKey != "" {
				t.Fatal("named runtime retained adapter credential")
			}
		})
	}
}

func TestConfiguredRuntimeLegacyCompilerPreservesResolutionAndSafeInventory(t *testing.T) {
	sources := &engineconfig.ConfigSources{
		User: &engineconfig.Config{
			ProviderAccounts: map[string]engineconfig.ProviderAccountConfig{
				"future-account": {
					Provider: "openai",
					Auth:     engineconfig.AccountAuthConfig{Kind: "env", Name: "FUTURE_OPENAI_KEY"},
				},
			},
			ModelProfiles: map[string]engineconfig.ModelProfileConfig{
				"future-profile": {Account: "future-account", APIModel: "gpt-4o-mini"},
			},
		},
		Project:     &engineconfig.Config{APIBaseURL: "https://project.example/v1"},
		Effective:   &engineconfig.Config{APIBaseURL: "https://project.example/v1"},
		ProjectPath: "/project/.claude/settings.json",
	}
	runtime, err := NewConfiguredRuntime(context.Background(), ConfiguredRuntimeOptions{
		Sources: sources,
		Resolution: ResolveInput{
			Explicit: Config{
				Provider: ProviderAgenticOpenAI,
				Model:    "gpt-4o",
				APIKey:   portfolioRuntimeSecret,
			},
			Configured: Config{BaseURL: "https://project.example/v1"},
			Getenv:     getenvMap(nil),
		},
		factory: func(_ context.Context, config Config) (model.BaseChatModel, error) {
			return &routeModel{provider: config.Provider, recorder: &routeRecorder{}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Main.Provider != ProviderAgenticOpenAI ||
		runtime.Main.Model != "gpt-4o" ||
		runtime.Main.BaseURL != "https://project.example/v1" {
		t.Fatalf("legacy resolution changed: %#v", runtime.Main)
	}
	if runtime.portfolio.Default != "legacy.main" ||
		runtime.portfolio.Profiles["future-profile"].APIModel != "gpt-4o-mini" {
		t.Fatalf("legacy canonical inventory = %#v", runtime.portfolio)
	}
	diagnostics := runtime.PortfolioDiagnostics()
	if len(diagnostics) != 1 || diagnostics[0].Code != "legacy_project_route_authority" {
		t.Fatalf("legacy compatibility diagnostics = %#v", diagnostics)
	}
	encoded, err := json.Marshal(runtime.portfolio)
	if err != nil {
		t.Fatal(err)
	}
	secretHash := fmt.Sprintf("%x", sha256.Sum256([]byte(portfolioRuntimeSecret)))
	if strings.Contains(string(encoded), portfolioRuntimeSecret) || strings.Contains(string(encoded), secretHash) {
		t.Fatalf("legacy portfolio exposed secret-derived data: %s", encoded)
	}
}

func TestConfiguredRuntimeLegacyPortfolioRoutesAfterAttachment(t *testing.T) {
	ctx := context.Background()
	sources := &engineconfig.ConfigSources{
		User: &engineconfig.Config{
			ProviderAccounts: map[string]engineconfig.ProviderAccountConfig{
				"future-account": {
					Provider: "openai",
					Auth: engineconfig.AccountAuthConfig{
						Kind: "env",
						Name: "FUTURE_OPENAI_KEY",
					},
				},
			},
			ModelProfiles: map[string]engineconfig.ModelProfileConfig{
				"future-profile": {
					Account:  "future-account",
					APIModel: "gpt-4o-mini",
				},
			},
		},
		Project: &engineconfig.Config{},
	}
	var factoryCalls []Config
	runtime, err := NewConfiguredRuntime(ctx, ConfiguredRuntimeOptions{
		Sources: sources,
		Resolution: ResolveInput{
			Explicit: Config{
				Provider: ProviderAgenticOpenAI,
				Model:    "gpt-4o",
			},
			Getenv: getenvMap(map[string]string{
				"OPENAI_API_KEY":    portfolioRuntimeSecret,
				"FUTURE_OPENAI_KEY": "future-secret",
			}),
		},
		factory: func(_ context.Context, config Config) (model.BaseChatModel, error) {
			factoryCalls = append(factoryCalls, config)
			return &routeModel{
				provider: config.Provider,
				recorder: &routeRecorder{},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(factoryCalls) != 1 {
		t.Fatalf("initial factory calls = %d, want 1", len(factoryCalls))
	}

	response, err := runtime.ChatModel.Generate(
		ctx,
		nil,
		model.WithModel("gpt-4o"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != string(ProviderAgenticOpenAI) {
		t.Fatalf("main response provider = %q", response.Content)
	}
	if len(factoryCalls) != 1 {
		t.Fatalf("main route factory calls = %d, want cached client", len(factoryCalls))
	}

	future, err := runtime.PrepareModel(ctx, "future-profile")
	if err != nil {
		t.Fatal(err)
	}
	if future.Provider != ProviderAgenticOpenAI || future.Model != "gpt-4o-mini" {
		t.Fatalf("future route = %#v", future)
	}
	if len(factoryCalls) != 2 || factoryCalls[1].APIKey != "future-secret" {
		t.Fatalf("future factory calls = %#v", factoryCalls)
	}
}

func TestConfiguredRuntimeLegacyPortfolioReusesExplicitMainRoute(t *testing.T) {
	ctx := context.Background()
	factoryCalls := 0
	runtime, err := NewConfiguredRuntime(ctx, ConfiguredRuntimeOptions{
		Sources: &engineconfig.ConfigSources{
			User:    &engineconfig.Config{},
			Project: &engineconfig.Config{},
		},
		Resolution: ResolveInput{
			Explicit: Config{
				Provider: ProviderAgenticOpenAI,
				Model:    "gpt-4o",
				APIKey:   portfolioRuntimeSecret,
			},
			Getenv: getenvMap(nil),
		},
		factory: func(_ context.Context, config Config) (model.BaseChatModel, error) {
			factoryCalls++
			return &routeModel{
				provider: config.Provider,
				recorder: &routeRecorder{},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ChatModel.Generate(
		ctx,
		nil,
		model.WithModel("gpt-4o"),
	); err != nil {
		t.Fatal(err)
	}
	if factoryCalls != 1 {
		t.Fatalf("main route factory calls = %d, want cached client", factoryCalls)
	}
}

const portfolioRuntimeSecret = "runtime-secret-sentinel-35b7aa"

func namedRuntimeSources() *engineconfig.ConfigSources {
	return &engineconfig.ConfigSources{
		User: &engineconfig.Config{
			ModelProfile: "primary",
			ProviderAccounts: map[string]engineconfig.ProviderAccountConfig{
				"account-a": {
					Provider: "openai",
					BaseURL:  "https://a.example/v1",
					Auth:     engineconfig.AccountAuthConfig{Kind: "env", Name: "OPENAI_A"},
				},
				"account-b": {
					Provider: "openai",
					BaseURL:  "https://b.example/v1",
					Auth:     engineconfig.AccountAuthConfig{Kind: "env", Name: "OPENAI_B"},
				},
				"account-d": {
					Provider: "openai",
					BaseURL:  "https://a.example/v1",
					Auth:     engineconfig.AccountAuthConfig{Kind: "env", Name: "OPENAI_D"},
				},
				"unused": {
					Provider: "openai",
					BaseURL:  "https://unused.example/v1",
					Auth:     engineconfig.AccountAuthConfig{Kind: "credential", Name: "unused"},
				},
			},
			ModelProfiles: map[string]engineconfig.ModelProfileConfig{
				"primary":        {Account: "account-a", APIModel: "gpt-4o"},
				"same-route":     {Account: "account-a", APIModel: "gpt-4.1"},
				"other-endpoint": {Account: "account-b", APIModel: "gpt-4o"},
				"other-auth":     {Account: "account-d", APIModel: "gpt-4o"},
				"unused":         {Account: "unused", APIModel: "gpt-4o-mini"},
			},
		},
		Project: &engineconfig.Config{},
	}
}
