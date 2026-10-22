package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	engineconfig "github.com/abietic/yhc/engine/config"
	"github.com/cloudwego/eino/components/model"
)

const desktopVaultSecret = "desktop-vault-secret-sentinel"

func desktopFallbackTestEnvironment() map[string]string {
	return map[string]string{
		desktopProviderFallbackEnv: "openai",
		desktopModelFallbackEnv:    "gpt-4o-mini",
		desktopEndpointFallbackEnv: "https://vault.example/v1",
		desktopCredentialEnv:       desktopVaultSecret,
	}
}

func TestDesktopFallbackRouteWithoutSharedSelection(t *testing.T) {
	env := desktopFallbackTestEnvironment()
	resolved, err := ResolveConfig(ResolveInput{Configured: Config{Model: "default"}, Getenv: getenvMap(env), CredentialLookup: credStore(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Provider != ProviderAgenticOpenAI || resolved.Model != "gpt-4o-mini" || resolved.BaseURL != "https://vault.example/v1" || resolved.APIKey != desktopVaultSecret {
		t.Fatal("legacy vault fallback was not selected")
	}
	if resolved.Sources.Model != "desktop-profile-fallback" || resolved.Sources.APIKey != "env:"+desktopCredentialEnv {
		t.Fatalf("wrong source labels: %#v", resolved.Sources)
	}
	identity, err := legacyRouteIdentity(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if identity.AuthKind != "env" || identity.AuthReference != desktopCredentialEnv {
		t.Fatal("vault identity was not retained")
	}
	encoded, _ := json.Marshal(identity)
	if strings.Contains(string(encoded), desktopVaultSecret) {
		t.Fatal("identity leaked vault secret")
	}
}

func TestDesktopFallbackHonorsSharedModelAndEndpoint(t *testing.T) {
	env := desktopFallbackTestEnvironment()
	resolved, err := ResolveConfig(ResolveInput{Configured: Config{Provider: "openai", Model: "gpt-4.1", BaseURL: "HTTPS://VAULT.EXAMPLE:443/a/../v1/"}, Getenv: getenvMap(env), CredentialLookup: credStore(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Model != "gpt-4.1" || resolved.Sources.Model != "config" || resolved.Sources.BaseURL != "config" || resolved.APIKey != desktopVaultSecret {
		t.Fatal("vault metadata replaced shared settings")
	}
	for _, route := range []Config{
		{Provider: "anthropic", Model: "claude-sonnet-4-6", BaseURL: "https://vault.example/v1"},
		{Provider: "openai", Model: "gpt-4o", BaseURL: "https://other.example/v1"},
		{Provider: "openai", Model: "gpt-4o", BaseURL: "http://vault.example/v1"},
		{Provider: "openai", Model: "gpt-4o", BaseURL: "https://vault.example:8443/v1"},
		{Provider: "openai", Model: "gpt-4o", BaseURL: "https://vault.example/v2"},
		{Provider: "openai", Model: "gpt-4o", BaseURL: "https://vault.example/v1?token=x"},
	} {
		_, err := ResolveConfig(ResolveInput{Configured: route, Getenv: getenvMap(env), CredentialLookup: credStore(nil)})
		if err == nil || !strings.Contains(err.Error(), "API key required") || strings.Contains(err.Error(), desktopVaultSecret) {
			t.Fatalf("mismatching route did not reject vault key: %v", err)
		}
	}
}

func TestDesktopFallbackNeverOverridesOrdinaryCredentials(t *testing.T) {
	for _, source := range []string{"explicit", "generic-env", "provider-env", "config", "store"} {
		t.Run(source, func(t *testing.T) {
			env := desktopFallbackTestEnvironment()
			input := ResolveInput{Configured: Config{Provider: "openai", Model: "gpt-4o", BaseURL: "https://vault.example/v1"}, Getenv: getenvMap(env), CredentialLookup: credStore(nil)}
			switch source {
			case "explicit":
				input.Explicit.APIKey = "ordinary-key"
			case "generic-env":
				env["PROV_API_KEY"] = "ordinary-key"
			case "provider-env":
				env["OPENAI_API_KEY"] = "ordinary-key"
			case "config":
				input.Configured.APIKey = "ordinary-key"
			case "store":
				input.CredentialLookup = credStore(map[string]string{"openai": "ordinary-key"})
			}
			resolved, err := ResolveConfig(input)
			if err != nil {
				t.Fatal(err)
			}
			if resolved.APIKey != "ordinary-key" || resolved.Sources.APIKey == "env:"+desktopCredentialEnv {
				t.Fatal("vault key overrode higher authority")
			}
		})
	}
}

func TestDesktopFallbackDoesNotInventAnExplicitOrAmbientRoute(t *testing.T) {
	for _, source := range []string{"explicit", "generic-env", "provider-env", "store", "shared"} {
		t.Run(source, func(t *testing.T) {
			env := desktopFallbackTestEnvironment()
			input := ResolveInput{Getenv: getenvMap(env), CredentialLookup: credStore(nil)}
			switch source {
			case "explicit":
				input.Explicit = Config{Provider: "anthropic", Model: "claude-sonnet-4-6", APIKey: "ordinary-key"}
			case "generic-env":
				env["PROV"] = "anthropic"
				env["PROV_API_KEY"] = "ordinary-key"
			case "provider-env":
				env["ANTHROPIC_API_KEY"] = "ordinary-key"
			case "store":
				input.CredentialLookup = credStore(map[string]string{"anthropic": "ordinary-key"})
			case "shared":
				input.Configured = Config{Provider: "anthropic", Model: "claude-sonnet-4-6"}
				env["ANTHROPIC_API_KEY"] = "ordinary-key"
			}
			resolved, err := ResolveConfig(input)
			if err != nil {
				t.Fatal(err)
			}
			if resolved.Provider != ProviderAgenticClaude || resolved.Model == env[desktopModelFallbackEnv] || resolved.BaseURL == env[desktopEndpointFallbackEnv] || resolved.APIKey != "ordinary-key" {
				t.Fatal("desktop fallback replaced an explicit route")
			}
		})
	}
}

func TestDesktopFallbackMissingEndpointIsBoundToProviderDefault(t *testing.T) {
	env := desktopFallbackTestEnvironment()
	delete(env, desktopEndpointFallbackEnv)
	resolved, err := ResolveConfig(ResolveInput{Configured: Config{Provider: "openai", Model: "gpt-4.1"}, Getenv: getenvMap(env), CredentialLookup: credStore(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.APIKey != desktopVaultSecret || resolved.BaseURL == "" {
		t.Fatal("default vault endpoint binding failed")
	}
	env["OPENAI_BASE_URL"] = "https://other.example/v1"
	if _, err := ResolveConfig(ResolveInput{Configured: Config{Provider: "openai"}, Getenv: getenvMap(env), CredentialLookup: credStore(nil)}); err == nil {
		t.Fatal("ambient endpoint reused vault secret")
	}
}

func TestDesktopFallbackCannotTakeOverNamedModelProfile(t *testing.T) {
	env := desktopFallbackTestEnvironment()
	sources := &engineconfig.ConfigSources{User: &engineconfig.Config{
		ModelProfile:     "selected",
		ProviderAccounts: map[string]engineconfig.ProviderAccountConfig{"account": {Provider: "openai", BaseURL: "https://vault.example/v1", Auth: engineconfig.AccountAuthConfig{Kind: "provider_default"}}},
		ModelProfiles:    map[string]engineconfig.ModelProfileConfig{"selected": {Account: "account", APIModel: "gpt-4.1"}},
	}}
	calls := 0
	runtime, err := NewConfiguredRuntime(context.Background(), ConfiguredRuntimeOptions{Sources: sources, Resolution: ResolveInput{Getenv: getenvMap(env), CredentialLookup: credStore(nil)}, factory: func(_ context.Context, cfg Config) (model.BaseChatModel, error) {
		calls++
		if cfg.Model != "gpt-4.1" || cfg.APIKey != desktopVaultSecret {
			t.Fatal("named profile route was not retained")
		}
		return &routeModel{provider: cfg.Provider, recorder: &routeRecorder{}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !runtime.UsesNamedPortfolio() || runtime.Main.APIKey != "" {
		t.Fatal("named runtime leaked secret or lost profile")
	}
	encoded, _ := json.Marshal(runtime.InventorySnapshot())
	if strings.Contains(string(encoded), desktopVaultSecret) {
		t.Fatal("inventory leaked vault secret")
	}
	account := sources.User.ProviderAccounts["account"]
	account.BaseURL = "https://other.example/v1"
	sources.User.ProviderAccounts["account"] = account
	_, err = NewConfiguredRuntime(context.Background(), ConfiguredRuntimeOptions{Sources: sources, Resolution: ResolveInput{Getenv: getenvMap(env), CredentialLookup: credStore(nil)}, factory: func(context.Context, Config) (model.BaseChatModel, error) {
		t.Fatal("mismatching route reached model construction")
		return nil, nil
	}})
	if err == nil || strings.Contains(err.Error(), desktopVaultSecret) {
		t.Fatalf("mismatching named route error = %v", err)
	}
}
