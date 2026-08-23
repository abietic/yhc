package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/abietic/yhc/engine/model"
)

// SetupResult holds the result of a setup utility operation.
type SetupResult struct {
	// Provider is the detected/selected provider.
	Provider model.ProviderID
	// Model is the selected model.
	Model string
	// Source describes where the credential was found (e.g., "env:OPENAI_API_KEY").
	Source string
	// Configured indicates whether the provider is fully configured.
	Configured bool
}

// DetectedProvider represents a provider found in the environment.
type DetectedProvider struct {
	// Provider is the provider identifier.
	Provider model.ProviderID
	// EnvVar is the environment variable that contains the API key.
	EnvVar string
	// HasKey indicates if a non-empty API key was found.
	HasKey bool
	// DefaultModel is the provider's default model.
	DefaultModel string
}

// GenerateDefaultConfig creates a default configuration with documentation comments.
// Returns the config as a formatted JSON string suitable for writing to a file.
func GenerateDefaultConfig() string {
	promptSuggestionsEnabled := true
	cfg := &Config{
		Model:             "default",
		MaxTurns:          DefaultMaxTurns,
		PermissionMode:    "default",
		AutoCompact:       true,
		Theme:             "dark",
		PromptSuggestions: &promptSuggestionsEnabled,
	}

	data, _ := json.MarshalIndent(cfg, "", "  ")
	return string(data)
}

// GenerateDefaultConfigWithComments returns a default config as JSON with
// explanatory comments suitable for a new user to understand each field.
func GenerateDefaultConfigWithComments() string {
	return `{
  // Provider is inferred from the model or configured credentials when omitted.
  // "provider": "anthropic",

  // "default" selects the provider's default model.
  "model": "default",

  // Optional overload fallback. Prefix cross-provider targets, e.g. "openai:gpt-4o".
  // "fallback_model": "",

  // Optional provider endpoint and local model aliases.
  // "api_base_url": "",
  // "model_aliases": {"fast": "openai:gpt-4o-mini"},

  // Maximum number of agent turns per interaction (0 = unlimited)
  "max_turns": 0,

  // Permission mode: "default", "plan", "bypass", or "auto"
  "permission_mode": "default",

  // Enable automatic conversation compaction when context grows large
  "auto_compact": true,

  // Show a model-generated next-prompt ghost after eligible TUI turns
  "prompt_suggestions": true,

  // UI theme: "dark" or "light"
  "theme": "dark",

  // Custom system prompt to append to the default
  // "custom_system_prompt": "",

  // MCP server configurations
  // "mcp_servers": {
  //   "example": {
  //     "command": "npx",
  //     "args": ["-y", "@example/mcp-server"]
  //   }
  // },

  // Restrict which tools are available (empty = all tools)
  // "allowed_tools": [],

  // Explicitly disable specific tools
  // "disabled_tools": [],

  // Enable verbose/debug output
  "verbose": false
}`
}

// DetectExistingProviders scans environment variables to find providers that
// already have API keys configured. Returns a list of detected providers
// sorted by preference (Anthropic first, then others alphabetically).
func DetectExistingProviders() []DetectedProvider {
	var detected []DetectedProvider

	allConfigs := model.AllProviderEnvConfigs()
	for providerID, envCfg := range allConfigs {
		for _, envVar := range envCfg.APIKeyEnvVars {
			// Skip the generic fallback variable — only count provider-specific ones.
			if envVar == "PROV_API_KEY" {
				continue
			}
			if v := os.Getenv(envVar); v != "" {
				detected = append(detected, DetectedProvider{
					Provider:     providerID,
					EnvVar:       envVar,
					HasKey:       true,
					DefaultModel: envCfg.DefaultModel,
				})
				break // Only need the first match per provider.
			}
		}
	}

	// Sort: Anthropic first, then alphabetically.
	sort.Slice(detected, func(i, j int) bool {
		if detected[i].Provider == model.ProviderAnthropic {
			return true
		}
		if detected[j].Provider == model.ProviderAnthropic {
			return false
		}
		return detected[i].Provider < detected[j].Provider
	})

	return detected
}

// SuggestProvider recommends a provider based on available credentials.
// Returns the best provider and its default model, or empty values if
// no providers are configured.
//
// Selection priority:
//  1. Anthropic (if available) — first-party models, best tool-calling support
//  2. OpenAI — broad tool support
//  3. Any other configured provider
func SuggestProvider() (model.ProviderID, string) {
	detected := DetectExistingProviders()
	if len(detected) == 0 {
		return model.ProviderUnknown, ""
	}

	// Priority list.
	priority := []model.ProviderID{
		model.ProviderAnthropic,
		model.ProviderOpenAI,
		model.ProviderGoogle,
		model.ProviderDeepSeek,
		model.ProviderQwen,
		model.ProviderArk,
	}

	for _, p := range priority {
		for _, d := range detected {
			if d.Provider == p && d.HasKey {
				return d.Provider, d.DefaultModel
			}
		}
	}

	// Fallback: first detected.
	return detected[0].Provider, detected[0].DefaultModel
}

// WriteDefaultConfig writes a default configuration file to the given path.
// Does not overwrite existing files (returns an error if the file already exists).
func WriteDefaultConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("config file already exists at %s", path)
	}

	// Ensure parent directory exists.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	content := GenerateDefaultConfig()
	return os.WriteFile(path, []byte(content), 0o600)
}

// ProviderSetupSummary returns a human-readable summary of the current provider
// configuration state, suitable for display during setup or troubleshooting.
func ProviderSetupSummary() string {
	detected := DetectExistingProviders()
	if len(detected) == 0 {
		return "No providers detected. Set an API key environment variable to get started.\n" +
			"  Example: export ANTHROPIC_API_KEY=sk-ant-...\n" +
			"  Example: export OPENAI_API_KEY=sk-..."
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Detected %d configured provider(s):\n", len(detected))
	for _, d := range detected {
		fmt.Fprintf(&sb, "  - %s (via %s, default model: %s)\n", d.Provider, d.EnvVar, d.DefaultModel)
	}

	suggested, suggestedModel := SuggestProvider()
	if suggested != model.ProviderUnknown {
		fmt.Fprintf(&sb, "\nRecommended: %s with model %s\n", suggested, suggestedModel)
	}

	return sb.String()
}
