package provider

import (
	"fmt"
	"os"
	"strings"

	"github.com/abietic/yhc/engine/auth"
	enginemodel "github.com/abietic/yhc/engine/model"
)

// ResolutionSources records where each effective provider setting came from.
// Values are safe to show in diagnostics and never contain credential values.
type ResolutionSources struct {
	Provider string
	Model    string
	APIKey   string
	BaseURL  string
}

// ResolvedConfig is a complete provider configuration plus source metadata.
type ResolvedConfig struct {
	Config
	Sources              ResolutionSources
	CredentialConfigured bool
}

// CredentialLookup returns a stored credential for a canonical provider ID.
type CredentialLookup func(provider string) (key string, ok bool, err error)

// CredentialOriginLookup resolves a stored credential together with an
// optional opaque, rotation-sensitive identity. Empty origin keeps model use
// available but is ineligible for private provider continuation.
type CredentialOriginLookup func(provider string) (key, originID string, ok bool, err error)

// ResolveInput separates explicit CLI/API values from lower-priority config
// file values so precedence and incompatible-model handling remain testable.
type ResolveInput struct {
	Explicit               Config
	Configured             Config
	Getenv                 func(string) string
	CredentialLookup       CredentialLookup
	CredentialOriginLookup CredentialOriginLookup
}

var providerPriority = []Provider{
	ProviderAgenticClaude,
	ProviderAgenticOpenAI,
	ProviderAgenticGemini,
	ProviderAgenticDeepSeek,
	ProviderAgenticQwen,
	ProviderAgenticArk,
}

var providerAliases = map[string]Provider{
	"anthropic":       ProviderAgenticClaude,
	"claude":          ProviderAgenticClaude,
	"agenticclaude":   ProviderAgenticClaude,
	"openai":          ProviderAgenticOpenAI,
	"agenticopenai":   ProviderAgenticOpenAI,
	"google":          ProviderAgenticGemini,
	"gemini":          ProviderAgenticGemini,
	"agenticgemini":   ProviderAgenticGemini,
	"deepseek":        ProviderAgenticDeepSeek,
	"agenticdeepseek": ProviderAgenticDeepSeek,
	"qwen":            ProviderAgenticQwen,
	"dashscope":       ProviderAgenticQwen,
	"agenticqwen":     ProviderAgenticQwen,
	"ark":             ProviderAgenticArk,
	"volcengine":      ProviderAgenticArk,
	"agenticark":      ProviderAgenticArk,
}

// NormalizeProvider accepts both public short names and internal agentic names.
func NormalizeProvider(value Provider) (Provider, error) {
	normalized := strings.ToLower(strings.TrimSpace(string(value)))
	if provider, ok := providerAliases[normalized]; ok {
		return provider, nil
	}
	return "", fmt.Errorf("unknown provider %q (supported: anthropic/claude, openai, google/gemini, deepseek, qwen, ark)", value)
}

// ResolveConfig resolves one provider configuration with deterministic priority:
// explicit values, generic PROV_* environment, provider-specific environment,
// configured values, credential store, and provider defaults.
func ResolveConfig(input ResolveInput) (ResolvedConfig, error) {
	getenv := input.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	lookup := input.CredentialLookup
	if lookup == nil {
		lookup = storedCredential
	}

	aliases := mergeAliases(input.Configured.ModelAliases, input.Explicit.ModelAliases)
	modelValue, modelSource, modelRank := firstValue(
		input.Explicit.Model, "explicit", 4,
		getenv("PROV_MODEL"), "env:PROV_MODEL", 3,
		input.Configured.Model, "config", 2,
	)
	modelValue = resolveAlias(modelValue, aliases)
	modelProvider, modelValue, err := splitProviderModel(modelValue)
	if err != nil {
		return ResolvedConfig{}, err
	}

	providerValue, providerSource, providerRank := firstProvider(
		input.Explicit.Provider, "explicit", 4,
		Provider(getenv("PROV")), "env:PROV", 3,
		input.Configured.Provider, "config", 2,
	)
	if providerValue != "" {
		providerValue, err = NormalizeProvider(providerValue)
		if err != nil {
			return ResolvedConfig{}, err
		}
	}
	if modelProvider != "" {
		if providerValue != "" && providerValue != modelProvider {
			if providerRank > modelRank {
				modelProvider = ""
				modelValue = ""
				modelSource = "provider-default"
			} else {
				return ResolvedConfig{}, fmt.Errorf("model provider %q conflicts with selected provider %q", modelProvider, providerValue)
			}
		}
		if modelProvider != "" {
			providerValue = modelProvider
			providerSource = modelSource + ":prefix"
			providerRank = modelRank
		}
	}

	detectedProvider := providerFromModel(modelValue)
	if providerValue == "" && detectedProvider != "" {
		providerValue = detectedProvider
		providerSource = modelSource + ":model"
		providerRank = modelRank
	}
	if providerValue == "" {
		providerValue, providerSource, err = firstConfiguredProvider(getenv, lookup)
		if err != nil {
			return ResolvedConfig{}, err
		}
		providerRank = 1
	}
	if providerValue == "" {
		return ResolvedConfig{}, fmt.Errorf("provider could not be determined; set --provider, PROV, a provider-qualified model, or a provider API key")
	}

	if detectedProvider != "" && detectedProvider != providerValue {
		if modelRank > providerRank {
			providerValue = detectedProvider
			providerSource = modelSource + ":model"
		} else if providerRank > modelRank {
			modelValue = ""
			modelSource = "provider-default"
		} else {
			return ResolvedConfig{}, fmt.Errorf("model %q belongs to provider %q, not selected provider %q", modelValue, detectedProvider, providerValue)
		}
	}
	providerEnv := enginemodel.GetProviderEnvConfig(providerID(providerValue))
	if providerEnv == nil {
		return ResolvedConfig{}, fmt.Errorf("provider %q has no environment configuration", providerValue)
	}
	if modelValue == "" || strings.EqualFold(modelValue, "default") {
		modelValue = providerEnv.DefaultModel
		modelSource = "provider-default"
	}

	genericAPIKey := genericProviderValue(getenv, "PROV_API_KEY", providerValue)
	apiKey, apiKeySource := firstValueNoRank(
		input.Explicit.APIKey, "explicit",
		genericAPIKey, "env:PROV_API_KEY",
	)
	if apiKey == "" {
		for _, envName := range providerEnv.APIKeyEnvVars {
			if envName == "PROV_API_KEY" {
				continue
			}
			if value := strings.TrimSpace(getenv(envName)); value != "" {
				apiKey = value
				apiKeySource = "env:" + envName
				break
			}
		}
	}
	if apiKey == "" && input.Configured.APIKey != "" {
		apiKey = input.Configured.APIKey
		apiKeySource = "config"
	}
	if apiKey == "" {
		stored, ok, lookupErr := lookup(providerCredentialID(providerValue))
		if lookupErr != nil {
			return ResolvedConfig{}, fmt.Errorf("load credential for %s: %w", providerValue, lookupErr)
		}
		if ok && strings.TrimSpace(stored) != "" {
			apiKey = stored
			apiKeySource = "credential-store" //nolint:gosec // This is a source label, not a credential.
		}
	}
	if apiKey == "" {
		return ResolvedConfig{}, fmt.Errorf("API key required for %s; checked --api-key, PROV_API_KEY, %s, and credential store",
			providerValue, strings.Join(providerSpecificEnvNames(providerEnv.APIKeyEnvVars), ", "))
	}

	genericBaseURL := genericProviderValue(getenv, "PROV_BASE_URL", providerValue)
	baseURL, baseURLSource := firstValueNoRank(
		input.Explicit.BaseURL, "explicit",
		genericBaseURL, "env:PROV_BASE_URL",
		input.Configured.BaseURL, "config",
	)
	if baseURL == "" && providerEnv.BaseURLEnvVar != "" {
		if value := strings.TrimSpace(getenv(providerEnv.BaseURLEnvVar)); value != "" {
			baseURL = value
			baseURLSource = "env:" + providerEnv.BaseURLEnvVar
		}
	}
	if baseURL == "" {
		baseURL = providerEnv.DefaultBaseURL
		baseURLSource = "provider-default"
	}

	maxTokens := input.Explicit.MaxTokens
	if maxTokens == 0 {
		maxTokens = input.Configured.MaxTokens
	}
	return ResolvedConfig{
		Config: Config{
			Provider:     providerValue,
			Model:        modelValue,
			APIKey:       apiKey,
			BaseURL:      baseURL,
			MaxTokens:    maxTokens,
			ModelAliases: aliases,
		},
		Sources: ResolutionSources{
			Provider: providerSource,
			Model:    modelSource,
			APIKey:   apiKeySource,
			BaseURL:  baseURLSource,
		},
		CredentialConfigured: true,
	}, nil
}

func splitProviderModel(value string) (Provider, string, error) {
	trimmed := strings.TrimSpace(value)
	idx := strings.Index(trimmed, ":")
	if idx <= 0 {
		return "", trimmed, nil
	}
	prefix := Provider(trimmed[:idx])
	provider, err := NormalizeProvider(prefix)
	if err != nil {
		return "", trimmed, nil
	}
	modelName := strings.TrimSpace(trimmed[idx+1:])
	if modelName == "" {
		return "", "", fmt.Errorf("provider-qualified model %q has an empty model name", value)
	}
	return provider, modelName, nil
}

func providerFromModel(modelName string) Provider {
	switch enginemodel.DetectProvider(modelName) {
	case enginemodel.ProviderAnthropic:
		return ProviderAgenticClaude
	case enginemodel.ProviderOpenAI:
		return ProviderAgenticOpenAI
	case enginemodel.ProviderGoogle:
		return ProviderAgenticGemini
	case enginemodel.ProviderDeepSeek:
		return ProviderAgenticDeepSeek
	case enginemodel.ProviderQwen:
		return ProviderAgenticQwen
	case enginemodel.ProviderArk:
		return ProviderAgenticArk
	default:
		return ""
	}
}

func providerID(provider Provider) enginemodel.ProviderID {
	switch provider {
	case ProviderAgenticClaude:
		return enginemodel.ProviderAnthropic
	case ProviderAgenticOpenAI:
		return enginemodel.ProviderOpenAI
	case ProviderAgenticGemini:
		return enginemodel.ProviderGoogle
	case ProviderAgenticDeepSeek:
		return enginemodel.ProviderDeepSeek
	case ProviderAgenticQwen:
		return enginemodel.ProviderQwen
	case ProviderAgenticArk:
		return enginemodel.ProviderArk
	default:
		return enginemodel.ProviderUnknown
	}
}

func providerCredentialID(provider Provider) string {
	return string(providerID(provider))
}

func storedCredential(provider string) (string, bool, error) {
	store := auth.NewCredentialStore(auth.DefaultCredentialPath())
	if err := store.Load(); err != nil {
		return "", false, err
	}
	credential := store.Get(provider)
	if credential == nil || strings.TrimSpace(credential.Key) == "" {
		return "", false, nil
	}
	return credential.Key, true, nil
}

func storedCredentialOrigin(provider string) (string, string, bool, error) {
	store := auth.NewCredentialStore(auth.DefaultCredentialPath())
	if err := store.Load(); err != nil {
		return "", "", false, err
	}
	credential := store.Get(provider)
	if credential == nil || strings.TrimSpace(credential.Key) == "" {
		return "", "", false, nil
	}
	originID := ""
	if credential.OriginID != "" && credential.OriginRevision > 0 {
		originID = fmt.Sprintf(
			"%s/r%d",
			credential.OriginID,
			credential.OriginRevision,
		)
	}
	return credential.Key, originID, true, nil
}

func firstConfiguredProvider(getenv func(string) string, lookup CredentialLookup) (Provider, string, error) {
	for _, provider := range providerPriority {
		envCfg := enginemodel.GetProviderEnvConfig(providerID(provider))
		for _, envName := range providerSpecificEnvNames(envCfg.APIKeyEnvVars) {
			if strings.TrimSpace(getenv(envName)) != "" {
				return provider, "env:" + envName, nil
			}
		}
	}
	for _, provider := range providerPriority {
		_, ok, err := lookup(providerCredentialID(provider))
		if err != nil {
			return "", "", err
		}
		if ok {
			return provider, "credential-store", nil
		}
	}
	return "", "", nil
}

func providerSpecificEnvNames(names []string) []string {
	result := make([]string, 0, len(names))
	for _, name := range names {
		if name != "PROV_API_KEY" {
			result = append(result, name)
		}
	}
	return result
}

func genericProviderValue(getenv func(string) string, name string, selected Provider) string {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return ""
	}
	rawProvider := strings.TrimSpace(getenv("PROV"))
	if rawProvider == "" {
		return value
	}
	provider, err := NormalizeProvider(Provider(rawProvider))
	if err != nil || provider != selected {
		return ""
	}
	return value
}

func mergeAliases(configured, explicit map[string]string) map[string]string {
	if len(configured) == 0 && len(explicit) == 0 {
		return nil
	}
	result := make(map[string]string)
	for name, value := range configured {
		result[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(value)
	}
	for name, value := range explicit {
		result[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(value)
	}
	return result
}

func resolveAlias(name string, aliases map[string]string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if actual, ok := aliases[normalized]; ok && actual != "" {
		return actual
	}
	return enginemodel.ResolveModelAlias(name)
}

func firstValue(value1, source1 string, rank1 int, value2, source2 string, rank2 int, value3, source3 string, rank3 int) (string, string, int) {
	for _, candidate := range []struct {
		value  string
		source string
		rank   int
	}{{value1, source1, rank1}, {value2, source2, rank2}, {value3, source3, rank3}} {
		if value := strings.TrimSpace(candidate.value); value != "" {
			return value, candidate.source, candidate.rank
		}
	}
	return "", "", 0
}

func firstValueNoRank(values ...string) (string, string) {
	for i := 0; i+1 < len(values); i += 2 {
		if value := strings.TrimSpace(values[i]); value != "" {
			return value, values[i+1]
		}
	}
	return "", ""
}

func firstProvider(value1 Provider, source1 string, rank1 int, value2 Provider, source2 string, rank2 int, value3 Provider, source3 string, rank3 int) (Provider, string, int) {
	value, source, rank := firstValue(string(value1), source1, rank1, string(value2), source2, rank2, string(value3), source3, rank3)
	return Provider(value), source, rank
}
