package config

import (
	"os"
	"strings"

	"github.com/abietic/yhc/engine/model"
)

// ProviderConfig holds provider-specific configuration for model API access.
// This is resolved from the unified config and environment variables.
type ProviderConfig struct {
	// Provider is the detected or configured provider.
	Provider model.ProviderID
	// APIKey is the resolved API key (from config or environment).
	APIKey string
	// BaseURL is the API endpoint (from config, env, or provider default).
	BaseURL string
	// Model is the model identifier to use.
	Model string
	// Headers are additional HTTP headers to include in requests.
	Headers map[string]string
	// ModelAliases maps local alias names to actual model IDs.
	// Example: {"fast": "gpt-4o-mini", "smart": "claude-opus-4-6"}
	ModelAliases map[string]string
}

// ResolveProviderConfig builds a ProviderConfig by combining the settings,
// environment variables, and provider-specific defaults. It handles:
//   - Provider auto-detection from model name
//   - API key resolution from multiple env var sources
//   - Base URL from env or defaults
//   - Model alias resolution
//
// The returned ProviderConfig may have an empty APIKey if none could be found;
// use ValidateConfig to check for completeness before use.
func ResolveProviderConfig(s *Settings) *ProviderConfig {
	if s == nil {
		s = DefaultSettings()
	}

	modelName := s.Model
	pc := &ProviderConfig{
		Model:   modelName,
		Headers: make(map[string]string),
	}

	// Auto-detect provider from model name
	pc.Provider = model.DetectProvider(modelName)

	// Resolve API key and base URL from environment
	envCfg := model.GetProviderEnvConfig(pc.Provider)
	if envCfg != nil {
		// Resolve API key: check each env var in priority order
		for _, envVar := range envCfg.APIKeyEnvVars {
			if v := os.Getenv(envVar); v != "" {
				pc.APIKey = v
				break
			}
		}

		// Resolve base URL: env override > settings > provider default
		if s.APIBaseURL != "" {
			pc.BaseURL = s.APIBaseURL
		} else if envCfg.BaseURLEnvVar != "" {
			if v := os.Getenv(envCfg.BaseURLEnvVar); v != "" {
				pc.BaseURL = v
			}
		}
		if pc.BaseURL == "" {
			pc.BaseURL = envCfg.DefaultBaseURL
		}
	} else {
		// Unknown provider: try generic env vars
		if v := os.Getenv("PROV_API_KEY"); v != "" {
			pc.APIKey = v
		}
		if s.APIBaseURL != "" {
			pc.BaseURL = s.APIBaseURL
		}
	}

	return pc
}

// ResolveModelAlias resolves a model name through the alias table.
// If the name is a known alias, it returns the actual model ID.
// Otherwise, it returns the input unchanged.
func (pc *ProviderConfig) ResolveModelAlias(name string) string {
	if pc.ModelAliases == nil {
		return name
	}
	normalized := strings.ToLower(strings.TrimSpace(name))
	if actual, ok := pc.ModelAliases[normalized]; ok {
		return actual
	}
	return name
}

// EffectiveModel returns the actual model to use, after resolving aliases.
func (pc *ProviderConfig) EffectiveModel() string {
	return pc.ResolveModelAlias(pc.Model)
}

// IsConfigured returns true if the provider config has the minimum required
// fields to attempt an API call (provider known and API key present).
func (pc *ProviderConfig) IsConfigured() bool {
	return pc.Provider != model.ProviderUnknown && pc.APIKey != ""
}

// SanitizedString returns a human-readable representation of the provider config
// with sensitive values (API key) masked.
func (pc *ProviderConfig) SanitizedString() string {
	key := "(not set)"
	if pc.APIKey != "" {
		// Show only last 4 characters
		if len(pc.APIKey) > 4 {
			key = "..." + pc.APIKey[len(pc.APIKey)-4:]
		} else {
			key = "****"
		}
	}
	return "provider=" + string(pc.Provider) +
		" model=" + pc.Model +
		" api_key=" + key +
		" base_url=" + pc.BaseURL
}
