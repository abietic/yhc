package provider

import (
	"strings"
	"unicode"

	enginemodel "github.com/abietic/yhc/engine/model"
)

// Desktop launches carry their encrypted vault's values in a separate channel.
// They must never masquerade as explicit PROV overrides or unbound API keys.
const (
	desktopProviderFallbackEnv = "YHC_DESKTOP_PROVIDER_FALLBACK"
	desktopModelFallbackEnv    = "YHC_DESKTOP_MODEL_FALLBACK"
	desktopEndpointFallbackEnv = "YHC_DESKTOP_BASE_URL_FALLBACK"
	desktopCredentialEnv       = "YHC_DESKTOP_API_KEY" //nolint:gosec // Environment variable name, not a credential.
)

func desktopFallbackRoute(input ResolveInput, getenv func(string) string, aliases map[string]string) (Config, bool) {
	// An explicit default model is still a higher-priority user selection.
	for _, value := range []string{
		string(input.Explicit.Provider), input.Explicit.Model, input.Explicit.BaseURL,
		getenv("PROV"), getenv("PROV_MODEL"), getenv("PROV_BASE_URL"), getenv("PROV_MODEL_PROFILE"),
		string(input.Configured.Provider), input.Configured.BaseURL,
	} {
		if strings.TrimSpace(value) != "" {
			return Config{}, false
		}
	}
	if model := strings.TrimSpace(input.Configured.Model); model != "" && !strings.EqualFold(model, "default") {
		return Config{}, false
	}
	for _, provider := range providerPriority {
		if env := enginemodel.GetProviderEnvConfig(providerID(provider)); env != nil && env.BaseURLEnvVar != "" && strings.TrimSpace(getenv(env.BaseURLEnvVar)) != "" {
			return Config{}, false
		}
	}
	provider, endpoint, ok := desktopFallbackIdentity(getenv)
	if !ok {
		return Config{}, false
	}
	model := strings.TrimSpace(getenv(desktopModelFallbackEnv))
	if model == "" {
		model = "default"
	}
	if len(model) > 512 || strings.IndexFunc(model, unicode.IsControl) >= 0 {
		return Config{}, false
	}
	modelProvider, model, err := splitProviderModel(resolveAlias(model, aliases))
	if err != nil || (modelProvider != "" && modelProvider != provider) {
		return Config{}, false
	}
	if detected := providerFromModel(model); detected != "" && detected != provider {
		return Config{}, false
	}
	return Config{Provider: provider, Model: model, BaseURL: endpoint}, true
}

func desktopFallbackIdentity(getenv func(string) string) (Provider, string, bool) {
	provider, err := NormalizeProvider(Provider(getenv(desktopProviderFallbackEnv)))
	if err != nil {
		return "", "", false
	}
	endpoint := strings.TrimSpace(getenv(desktopEndpointFallbackEnv))
	if endpoint == "" {
		environment := enginemodel.GetProviderEnvConfig(providerID(provider))
		if environment == nil {
			return "", "", false
		}
		endpoint = environment.DefaultBaseURL
	}
	endpoint, err = canonicalRouteEndpoint(endpoint)
	if err != nil {
		return "", "", false
	}
	return provider, endpoint, true
}

// desktopFallbackCredential binds the vault key to its original provider and
// full canonical endpoint. A settings change cannot send that key to a new host,
// scheme, port, or path. The caller has already tried every ordinary key source.
func desktopFallbackCredential(getenv func(string) string, provider Provider, endpoint string) string {
	fallbackProvider, fallbackEndpoint, ok := desktopFallbackIdentity(getenv)
	if !ok || provider != fallbackProvider {
		return ""
	}
	canonical, err := canonicalRouteEndpoint(endpoint)
	if err != nil || canonical != fallbackEndpoint {
		return ""
	}
	key := getenv(desktopCredentialEnv)
	if len(key) > 4096 || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		return ""
	}
	return strings.TrimSpace(key)
}
