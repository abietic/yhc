package model

import (
	"sort"
	"strings"
)

// CostTier represents the pricing tier of a model for display purposes.
type CostTier string

const (
	CostTierFree     CostTier = "free"
	CostTierBudget   CostTier = "budget"
	CostTierStandard CostTier = "standard"
	CostTierPremium  CostTier = "premium"
)

// RegistryEntry describes a model with full metadata for UI display and filtering.
// This is the TUI-facing struct that enriches ModelCapabilities with provider
// grouping, display name, and capability flags useful for a model picker.
type RegistryEntry struct {
	// Provider is the human-friendly provider name (e.g., "Anthropic", "OpenAI").
	Provider string
	// ModelID is the canonical model identifier used in API calls.
	ModelID string
	// DisplayName is a human-friendly short name for UI display.
	DisplayName string
	// SupportsStreaming indicates whether the model supports streaming responses.
	SupportsStreaming bool
	// SupportsToolCalls indicates whether the model supports function/tool calling.
	SupportsToolCalls bool
	// SupportsThinking indicates whether the model supports extended thinking/reasoning.
	SupportsThinking bool
	// SupportsMedia indicates whether the model supports image/media inputs.
	SupportsMedia bool
	// MaxContextTokens is the maximum input context window size.
	MaxContextTokens int
	// MaxOutputTokens is the maximum number of output tokens.
	MaxOutputTokens int
	// CostTier is the pricing category for display (free, budget, standard, premium).
	CostTier CostTier
}

// ProviderGroup groups models under a single provider for display.
type ProviderGroup struct {
	Provider string
	Models   []RegistryEntry
}

// ModelRegistry holds registered models and provides query methods for the TUI.
// It is a read-only, pre-populated registry derived from the model capability table.
type ModelRegistry struct {
	entries []RegistryEntry
}

// DefaultRegistry returns the global model registry pre-populated with well-known models.
// The registry is built from the internal modelTable and augmented with provider/display metadata.
func DefaultRegistry() *ModelRegistry {
	return &ModelRegistry{entries: buildDefaultEntries()}
}

// All returns all registered model entries.
func (r *ModelRegistry) All() []RegistryEntry {
	result := make([]RegistryEntry, len(r.entries))
	copy(result, r.entries)
	return result
}

// ByProvider returns all models for the given provider (case-insensitive match).
func (r *ModelRegistry) ByProvider(provider string) []RegistryEntry {
	var result []RegistryEntry
	for _, e := range r.entries {
		if strings.EqualFold(e.Provider, provider) {
			result = append(result, e)
		}
	}
	return result
}

// ByCapability returns all models that satisfy the given predicate.
func (r *ModelRegistry) ByCapability(predicate func(RegistryEntry) bool) []RegistryEntry {
	var result []RegistryEntry
	for _, e := range r.entries {
		if predicate(e) {
			result = append(result, e)
		}
	}
	return result
}

// WithToolCalls returns all models that support tool/function calling.
func (r *ModelRegistry) WithToolCalls() []RegistryEntry {
	return r.ByCapability(func(e RegistryEntry) bool { return e.SupportsToolCalls })
}

// WithThinking returns all models that support extended thinking/reasoning.
func (r *ModelRegistry) WithThinking() []RegistryEntry {
	return r.ByCapability(func(e RegistryEntry) bool { return e.SupportsThinking })
}

// WithMedia returns all models that support image/media inputs.
func (r *ModelRegistry) WithMedia() []RegistryEntry {
	return r.ByCapability(func(e RegistryEntry) bool { return e.SupportsMedia })
}

// GroupedByProvider returns models grouped by provider, sorted by provider name.
// Within each group, models are ordered by cost tier (premium first) then by name.
func (r *ModelRegistry) GroupedByProvider() []ProviderGroup {
	groups := make(map[string][]RegistryEntry)
	for _, e := range r.entries {
		groups[e.Provider] = append(groups[e.Provider], e)
	}

	// Sort each group: premium first, then standard, budget, free; within same tier alphabetical.
	tierOrder := map[CostTier]int{
		CostTierPremium:  0,
		CostTierStandard: 1,
		CostTierBudget:   2,
		CostTierFree:     3,
	}
	for provider := range groups {
		entries := groups[provider]
		sort.Slice(entries, func(i, j int) bool {
			ti := tierOrder[entries[i].CostTier]
			tj := tierOrder[entries[j].CostTier]
			if ti != tj {
				return ti < tj
			}
			return entries[i].DisplayName < entries[j].DisplayName
		})
		groups[provider] = entries
	}

	// Build sorted provider groups.
	providerOrder := []string{"Anthropic", "OpenAI", "Google", "DeepSeek", "Qwen", "ByteDance"}
	var result []ProviderGroup
	seen := make(map[string]bool)
	for _, p := range providerOrder {
		if entries, ok := groups[p]; ok {
			result = append(result, ProviderGroup{Provider: p, Models: entries})
			seen[p] = true
		}
	}
	// Append any remaining providers not in the preferred order.
	var remaining []string
	for p := range groups {
		if !seen[p] {
			remaining = append(remaining, p)
		}
	}
	sort.Strings(remaining)
	for _, p := range remaining {
		result = append(result, ProviderGroup{Provider: p, Models: groups[p]})
	}

	return result
}

// Lookup returns the registry entry for a model ID, or nil if not found.
func (r *ModelRegistry) Lookup(modelID string) *RegistryEntry {
	normalized := strings.TrimSpace(modelID)
	for i := range r.entries {
		if strings.EqualFold(r.entries[i].ModelID, normalized) {
			return &r.entries[i]
		}
	}
	// Try alias resolution.
	if canonical, ok := aliases[strings.ToLower(normalized)]; ok {
		for i := range r.entries {
			if strings.EqualFold(r.entries[i].ModelID, canonical) {
				return &r.entries[i]
			}
		}
	}
	return nil
}

// Providers returns a sorted list of unique provider names in the registry.
func (r *ModelRegistry) Providers() []string {
	seen := make(map[string]bool)
	for _, e := range r.entries {
		seen[e.Provider] = true
	}
	result := make([]string, 0, len(seen))
	for p := range seen {
		result = append(result, p)
	}
	sort.Strings(result)
	return result
}

// buildDefaultEntries constructs the pre-populated registry entries from modelTable.
func buildDefaultEntries() []RegistryEntry {
	// Curated set of well-known models for the TUI picker.
	// We select representative models (not every dated variant) to keep the list manageable.
	type entryDef struct {
		modelID     string
		provider    string
		displayName string
		toolCalls   bool
		streaming   bool
	}

	// Define the curated model list with explicit provider and display names.
	curated := []entryDef{
		// Anthropic
		{modelID: "claude-opus-4-6", provider: "Anthropic", displayName: "Claude Opus 4.6", toolCalls: true, streaming: true},
		{modelID: "claude-opus-4-5-20251101", provider: "Anthropic", displayName: "Claude Opus 4.5", toolCalls: true, streaming: true},
		{modelID: "claude-opus-4-20250514", provider: "Anthropic", displayName: "Claude Opus 4", toolCalls: true, streaming: true},
		{modelID: "claude-sonnet-4-6", provider: "Anthropic", displayName: "Claude Sonnet 4.6", toolCalls: true, streaming: true},
		{modelID: "claude-sonnet-4-5-20250929", provider: "Anthropic", displayName: "Claude Sonnet 4.5", toolCalls: true, streaming: true},
		{modelID: "claude-sonnet-4-20250514", provider: "Anthropic", displayName: "Claude Sonnet 4", toolCalls: true, streaming: true},
		{modelID: "claude-haiku-4-5-20251001", provider: "Anthropic", displayName: "Claude Haiku 4.5", toolCalls: true, streaming: true},
		{modelID: "claude-3-5-haiku-20241022", provider: "Anthropic", displayName: "Claude 3.5 Haiku", toolCalls: true, streaming: true},

		// OpenAI
		{modelID: "gpt-4o", provider: "OpenAI", displayName: "GPT-4o", toolCalls: true, streaming: true},
		{modelID: "gpt-4o-mini", provider: "OpenAI", displayName: "GPT-4o Mini", toolCalls: true, streaming: true},
		{modelID: "o1", provider: "OpenAI", displayName: "o1", toolCalls: true, streaming: true},
		{modelID: "o3", provider: "OpenAI", displayName: "o3", toolCalls: true, streaming: true},
		{modelID: "o3-mini", provider: "OpenAI", displayName: "o3 Mini", toolCalls: true, streaming: true},
		{modelID: "o4-mini", provider: "OpenAI", displayName: "o4 Mini", toolCalls: true, streaming: true},

		// Google
		{modelID: "gemini-2.5-pro", provider: "Google", displayName: "Gemini 2.5 Pro", toolCalls: true, streaming: true},
		{modelID: "gemini-2.5-flash", provider: "Google", displayName: "Gemini 2.5 Flash", toolCalls: true, streaming: true},
		{modelID: "gemini-2.0-flash", provider: "Google", displayName: "Gemini 2.0 Flash", toolCalls: true, streaming: true},

		// DeepSeek
		{modelID: "deepseek-v4-pro", provider: "DeepSeek", displayName: "DeepSeek V4 Pro", toolCalls: true, streaming: true},
		{modelID: "deepseek-v4-flash", provider: "DeepSeek", displayName: "DeepSeek V4 Flash", toolCalls: true, streaming: true},
		{modelID: "deepseek-r1", provider: "DeepSeek", displayName: "DeepSeek R1", toolCalls: false, streaming: true},
		{modelID: "deepseek-v3", provider: "DeepSeek", displayName: "DeepSeek V3", toolCalls: true, streaming: true},

		// Qwen
		{modelID: "qwen-max", provider: "Qwen", displayName: "Qwen Max", toolCalls: true, streaming: true},
		{modelID: "qwen-plus", provider: "Qwen", displayName: "Qwen Plus", toolCalls: true, streaming: true},
		{modelID: "qwen-turbo", provider: "Qwen", displayName: "Qwen Turbo", toolCalls: true, streaming: true},
	}

	entries := make([]RegistryEntry, 0, len(curated))
	for _, def := range curated {
		cap := GetCapabilities(def.modelID)
		entries = append(entries, RegistryEntry{
			Provider:          def.provider,
			ModelID:           def.modelID,
			DisplayName:       def.displayName,
			SupportsStreaming: def.streaming,
			SupportsToolCalls: def.toolCalls,
			SupportsThinking:  cap.SupportsThinking,
			SupportsMedia:     cap.SupportsImages,
			MaxContextTokens:  cap.ContextWindow,
			MaxOutputTokens:   cap.MaxOutputTokens,
			CostTier:          deriveCostTier(cap),
		})
	}
	return entries
}

// deriveCostTier maps per-token cost to a human-friendly tier.
func deriveCostTier(cap *ModelCapabilities) CostTier {
	// Use output cost as the primary signal (it's typically higher and more differentiating).
	outputCost := cap.CostPerOutputToken
	switch {
	case outputCost <= 0:
		return CostTierFree
	case outputCost < 0.000002: // < $2/Mtok output
		return CostTierBudget
	case outputCost < 0.000020: // < $20/Mtok output
		return CostTierStandard
	default: // >= $20/Mtok output
		return CostTierPremium
	}
}
