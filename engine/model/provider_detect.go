package model

import "strings"

// ProviderID identifies a model provider for routing purposes.
// These match the Provider constants in engine/provider but are defined here
// to avoid a circular dependency (model -> provider).
type ProviderID string

const (
	ProviderAnthropic ProviderID = "anthropic"
	ProviderOpenAI    ProviderID = "openai"
	ProviderGoogle    ProviderID = "google"
	ProviderDeepSeek  ProviderID = "deepseek"
	ProviderQwen      ProviderID = "qwen"
	ProviderArk       ProviderID = "ark"
	ProviderUnknown   ProviderID = "unknown"
)

// ProviderEnvConfig describes provider-specific environment variable sources
// and default settings for API key resolution and endpoint configuration.
type ProviderEnvConfig struct {
	// Provider is the provider identifier.
	Provider ProviderID
	// APIKeyEnvVars lists environment variable names to check for the API key,
	// in priority order (first non-empty wins).
	APIKeyEnvVars []string
	// BaseURLEnvVar is the environment variable for overriding the base URL.
	BaseURLEnvVar string
	// DefaultBaseURL is the default API endpoint.
	DefaultBaseURL string
	// DefaultModel is the default model if none is specified.
	DefaultModel string
}

// providerEnvConfigs maps provider IDs to their environment configuration.
var providerEnvConfigs = map[ProviderID]*ProviderEnvConfig{
	ProviderAnthropic: {
		Provider:       ProviderAnthropic,
		APIKeyEnvVars:  []string{"ANTHROPIC_API_KEY", "PROV_API_KEY"},
		BaseURLEnvVar:  "ANTHROPIC_BASE_URL",
		DefaultBaseURL: "https://api.anthropic.com",
		DefaultModel:   "claude-sonnet-4-6",
	},
	ProviderOpenAI: {
		Provider:       ProviderOpenAI,
		APIKeyEnvVars:  []string{"OPENAI_API_KEY", "PROV_API_KEY"},
		BaseURLEnvVar:  "OPENAI_BASE_URL",
		DefaultBaseURL: "https://api.openai.com/v1",
		DefaultModel:   "gpt-4o",
	},
	ProviderGoogle: {
		Provider:       ProviderGoogle,
		APIKeyEnvVars:  []string{"GOOGLE_API_KEY", "GEMINI_API_KEY", "PROV_API_KEY"},
		BaseURLEnvVar:  "GOOGLE_BASE_URL",
		DefaultBaseURL: "https://generativelanguage.googleapis.com",
		DefaultModel:   "gemini-2.5-flash",
	},
	ProviderDeepSeek: {
		Provider:       ProviderDeepSeek,
		APIKeyEnvVars:  []string{"DEEPSEEK_API_KEY", "PROV_API_KEY"},
		BaseURLEnvVar:  "DEEPSEEK_BASE_URL",
		DefaultBaseURL: "https://api.deepseek.com",
		DefaultModel:   "deepseek-v4-flash",
	},
	ProviderQwen: {
		Provider:       ProviderQwen,
		APIKeyEnvVars:  []string{"DASHSCOPE_API_KEY", "QWEN_API_KEY", "PROV_API_KEY"},
		BaseURLEnvVar:  "QWEN_BASE_URL",
		DefaultBaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		DefaultModel:   "qwen-max",
	},
	ProviderArk: {
		Provider:       ProviderArk,
		APIKeyEnvVars:  []string{"ARK_API_KEY", "PROV_API_KEY"},
		BaseURLEnvVar:  "ARK_BASE_URL",
		DefaultBaseURL: "https://ark.cn-beijing.volces.com/api/v3",
		DefaultModel:   "doubao-1.5-pro-32k",
	},
}

// DetectProvider determines the provider from a model name using prefix matching.
// It handles common model naming patterns:
//   - claude-* → anthropic
//   - gpt-*, o1*, o3*, o4* → openai
//   - gemini-* → google
//   - deepseek-* → deepseek
//   - qwen* → qwen
//   - doubao-*, ep-* (endpoint IDs) → ark
//
// Returns ProviderUnknown if the model name doesn't match any known pattern.
func DetectProvider(modelName string) ProviderID {
	if modelName == "" {
		return ProviderUnknown
	}

	normalized := strings.ToLower(strings.TrimSpace(modelName))

	// Strip provider prefix if present (e.g., "agenticclaude:claude-sonnet-4-6")
	if idx := strings.Index(normalized, ":"); idx >= 0 {
		prefix := normalized[:idx]
		// Check if the prefix itself is a provider identifier.
		switch {
		case strings.Contains(prefix, "claude") || strings.Contains(prefix, "anthropic"):
			return ProviderAnthropic
		case strings.Contains(prefix, "openai"):
			return ProviderOpenAI
		case strings.Contains(prefix, "gemini") || strings.Contains(prefix, "google"):
			return ProviderGoogle
		case strings.Contains(prefix, "deepseek"):
			return ProviderDeepSeek
		case strings.Contains(prefix, "qwen"):
			return ProviderQwen
		case strings.Contains(prefix, "ark"):
			return ProviderArk
		}
		// Use the model name part after the colon for detection.
		normalized = normalized[idx+1:]
	}

	// Strip version suffixes like [1m], [2m] for matching
	normalized, _ = splitContextSuffix(normalized)

	// Check provider-specific prefixes
	switch {
	case strings.HasPrefix(normalized, "claude"):
		return ProviderAnthropic
	case strings.HasPrefix(normalized, "gpt-"):
		return ProviderOpenAI
	case strings.HasPrefix(normalized, "o1"), strings.HasPrefix(normalized, "o3"), strings.HasPrefix(normalized, "o4"):
		return ProviderOpenAI
	case strings.HasPrefix(normalized, "gemini"):
		return ProviderGoogle
	case strings.HasPrefix(normalized, "deepseek"):
		return ProviderDeepSeek
	case strings.HasPrefix(normalized, "qwen"):
		return ProviderQwen
	case strings.HasPrefix(normalized, "doubao"):
		return ProviderArk
	case strings.HasPrefix(normalized, "ep-"):
		// Ark endpoint IDs start with "ep-"
		return ProviderArk
	}

	// Check if the model name contains known provider patterns (for Bedrock ARNs, etc.)
	switch {
	case strings.Contains(normalized, "anthropic") || strings.Contains(normalized, "claude"):
		return ProviderAnthropic
	case strings.Contains(normalized, "openai") || strings.Contains(normalized, "gpt-4"):
		return ProviderOpenAI
	case strings.Contains(normalized, "gemini"):
		return ProviderGoogle
	case strings.Contains(normalized, "deepseek"):
		return ProviderDeepSeek
	case strings.Contains(normalized, "qwen"):
		return ProviderQwen
	}

	return ProviderUnknown
}

// GetProviderEnvConfig returns the environment configuration for a provider.
// Returns nil if the provider is unknown.
func GetProviderEnvConfig(provider ProviderID) *ProviderEnvConfig {
	cfg, ok := providerEnvConfigs[provider]
	if !ok {
		return nil
	}
	// Return a copy to prevent mutation.
	result := *cfg
	envVars := make([]string, len(cfg.APIKeyEnvVars))
	copy(envVars, cfg.APIKeyEnvVars)
	result.APIKeyEnvVars = envVars
	return &result
}

// AllProviderEnvConfigs returns all known provider configurations.
func AllProviderEnvConfigs() map[ProviderID]*ProviderEnvConfig {
	result := make(map[ProviderID]*ProviderEnvConfig, len(providerEnvConfigs))
	for k := range providerEnvConfigs {
		result[k] = GetProviderEnvConfig(k)
	}
	return result
}

// DetectProviderForConfig auto-detects the provider from a model name and
// returns the corresponding ProviderEnvConfig. Returns nil for unknown models.
func DetectProviderForConfig(modelName string) *ProviderEnvConfig {
	provider := DetectProvider(modelName)
	if provider == ProviderUnknown {
		return nil
	}
	return GetProviderEnvConfig(provider)
}
