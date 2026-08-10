package model

import (
	"fmt"
	"sort"
	"strings"
)

// MetadataOverrides is the user-owned, field-presence-aware metadata surface
// for one configured model profile.
type MetadataOverrides struct {
	ContextWindowTokens       *int                `json:"context_window_tokens,omitempty"`
	MaxOutputTokens           *int                `json:"max_output_tokens,omitempty"`
	Capabilities              CapabilityOverrides `json:"capabilities,omitempty"`
	SupportedReasoningEfforts []string            `json:"supported_reasoning_efforts,omitempty"`
	CostTier                  string              `json:"cost_tier,omitempty"`
	Deprecated                *bool               `json:"deprecated,omitempty"`
	Successor                 string              `json:"successor,omitempty"`
}

// CapabilityOverrides records explicit support facts without conflating false
// with an absent/unknown value.
type CapabilityOverrides struct {
	Text         *bool `json:"text,omitempty"`
	Streaming    *bool `json:"streaming,omitempty"`
	Tools        *bool `json:"tools,omitempty"`
	SystemPrompt *bool `json:"system_prompt,omitempty"`
	Images       *bool `json:"images,omitempty"`
	PDFs         *bool `json:"pdfs,omitempty"`
	Thinking     *bool `json:"thinking,omitempty"`
}

// MetadataField retains both one effective value and its per-field authority.
type MetadataField[T any] struct {
	Value  T      `json:"value"`
	Source string `json:"source"`
}

// EffectiveModelMetadata contains model facts admitted by the compiler. Zero
// values with source "unknown" are deliberately not presented as known facts.
type EffectiveModelMetadata struct {
	ContextWindowTokens       MetadataField[int]      `json:"context_window_tokens"`
	MaxOutputTokens           MetadataField[int]      `json:"max_output_tokens"`
	Text                      MetadataField[bool]     `json:"text"`
	Streaming                 MetadataField[bool]     `json:"streaming"`
	Tools                     MetadataField[bool]     `json:"tools"`
	SystemPrompt              MetadataField[bool]     `json:"system_prompt"`
	Images                    MetadataField[bool]     `json:"images"`
	PDFs                      MetadataField[bool]     `json:"pdfs"`
	Thinking                  MetadataField[bool]     `json:"thinking"`
	SupportedReasoningEfforts MetadataField[[]string] `json:"supported_reasoning_efforts"`
	CostTier                  MetadataField[string]   `json:"cost_tier"`
	Deprecated                MetadataField[bool]     `json:"deprecated"`
	Successor                 MetadataField[string]   `json:"successor"`
}

var portfolioReasoningEfforts = map[string]struct{}{
	"none": {}, "minimal": {}, "low": {}, "medium": {}, "high": {}, "xhigh": {}, "max": {},
}

// ResolvePortfolioMetadata validates explicit overrides and applies them
// field-by-field over a known built-in exact, alias, or pattern match.
func ResolvePortfolioMetadata(modelID string, overrides MetadataOverrides) (EffectiveModelMetadata, error) {
	metadata := unknownPortfolioMetadata()
	if capabilities, ok := knownPortfolioCapabilities(modelID); ok {
		source := "built-in"
		metadata.ContextWindowTokens = MetadataField[int]{Value: capabilities.ContextWindow, Source: source}
		metadata.MaxOutputTokens = MetadataField[int]{Value: capabilities.MaxOutputTokens, Source: source}
		metadata.Text = MetadataField[bool]{Value: true, Source: source}
		metadata.Streaming = MetadataField[bool]{Value: capabilities.SupportsStreaming, Source: source}
		metadata.Tools = MetadataField[bool]{Value: capabilities.SupportsTools, Source: source}
		metadata.SystemPrompt = MetadataField[bool]{Value: capabilities.SupportsSystemPrompt, Source: source}
		metadata.Images = MetadataField[bool]{Value: capabilities.SupportsImages, Source: source}
		metadata.PDFs = MetadataField[bool]{Value: capabilities.SupportsPDFs, Source: source}
		metadata.Thinking = MetadataField[bool]{Value: capabilities.SupportsThinking, Source: source}
		metadata.CostTier = MetadataField[string]{Value: string(deriveCostTier(capabilities)), Source: source}
		metadata.Deprecated = MetadataField[bool]{Value: capabilities.IsDeprecated(), Source: source}
		metadata.Successor = MetadataField[string]{Value: capabilities.Successor, Source: source}
	}

	const explicit = "profile-override"
	if overrides.ContextWindowTokens != nil {
		if *overrides.ContextWindowTokens <= 0 {
			return EffectiveModelMetadata{}, fmt.Errorf("context_window_tokens must be positive")
		}
		metadata.ContextWindowTokens = MetadataField[int]{Value: *overrides.ContextWindowTokens, Source: explicit}
	}
	if overrides.MaxOutputTokens != nil {
		if *overrides.MaxOutputTokens <= 0 {
			return EffectiveModelMetadata{}, fmt.Errorf("max_output_tokens must be positive")
		}
		metadata.MaxOutputTokens = MetadataField[int]{Value: *overrides.MaxOutputTokens, Source: explicit}
	}
	applyBoolOverride(&metadata.Text, overrides.Capabilities.Text)
	applyBoolOverride(&metadata.Streaming, overrides.Capabilities.Streaming)
	applyBoolOverride(&metadata.Tools, overrides.Capabilities.Tools)
	applyBoolOverride(&metadata.SystemPrompt, overrides.Capabilities.SystemPrompt)
	applyBoolOverride(&metadata.Images, overrides.Capabilities.Images)
	applyBoolOverride(&metadata.PDFs, overrides.Capabilities.PDFs)
	applyBoolOverride(&metadata.Thinking, overrides.Capabilities.Thinking)

	if overrides.SupportedReasoningEfforts != nil {
		seen := make(map[string]struct{}, len(overrides.SupportedReasoningEfforts))
		efforts := make([]string, 0, len(overrides.SupportedReasoningEfforts))
		for _, raw := range overrides.SupportedReasoningEfforts {
			effort := strings.ToLower(strings.TrimSpace(raw))
			if _, ok := portfolioReasoningEfforts[effort]; !ok {
				return EffectiveModelMetadata{}, fmt.Errorf("unsupported reasoning effort %q", raw)
			}
			if _, duplicate := seen[effort]; duplicate {
				return EffectiveModelMetadata{}, fmt.Errorf("duplicate reasoning effort %q", effort)
			}
			seen[effort] = struct{}{}
			efforts = append(efforts, effort)
		}
		sort.Strings(efforts)
		metadata.SupportedReasoningEfforts = MetadataField[[]string]{Value: efforts, Source: explicit}
	}
	if overrides.CostTier != "" {
		tier := strings.ToLower(strings.TrimSpace(overrides.CostTier))
		switch CostTier(tier) {
		case CostTierFree, CostTierBudget, CostTierStandard, CostTierPremium:
			metadata.CostTier = MetadataField[string]{Value: tier, Source: explicit}
		default:
			return EffectiveModelMetadata{}, fmt.Errorf("unsupported cost tier %q", overrides.CostTier)
		}
	}
	if overrides.Deprecated != nil {
		metadata.Deprecated = MetadataField[bool]{Value: *overrides.Deprecated, Source: explicit}
	}
	if overrides.Successor != "" {
		metadata.Successor = MetadataField[string]{Value: strings.TrimSpace(overrides.Successor), Source: explicit}
	}
	return metadata, nil
}

// ValidateReasoningEffort checks one configured default against the shared
// portfolio reasoning vocabulary.
func ValidateReasoningEffort(raw string) (string, error) {
	effort := strings.ToLower(strings.TrimSpace(raw))
	if effort == "" {
		return "", nil
	}
	if _, ok := portfolioReasoningEfforts[effort]; !ok {
		return "", fmt.Errorf("unsupported reasoning effort %q", raw)
	}
	return effort, nil
}

func unknownPortfolioMetadata() EffectiveModelMetadata {
	return EffectiveModelMetadata{
		ContextWindowTokens:       MetadataField[int]{Source: "unknown"},
		MaxOutputTokens:           MetadataField[int]{Source: "unknown"},
		Text:                      MetadataField[bool]{Source: "unknown"},
		Streaming:                 MetadataField[bool]{Source: "unknown"},
		Tools:                     MetadataField[bool]{Source: "unknown"},
		SystemPrompt:              MetadataField[bool]{Source: "unknown"},
		Images:                    MetadataField[bool]{Source: "unknown"},
		PDFs:                      MetadataField[bool]{Source: "unknown"},
		Thinking:                  MetadataField[bool]{Source: "unknown"},
		SupportedReasoningEfforts: MetadataField[[]string]{Source: "unknown"},
		CostTier:                  MetadataField[string]{Source: "unknown"},
		Deprecated:                MetadataField[bool]{Source: "unknown"},
		Successor:                 MetadataField[string]{Source: "unknown"},
	}
}

func applyBoolOverride(field *MetadataField[bool], value *bool) {
	if value != nil {
		*field = MetadataField[bool]{Value: *value, Source: "profile-override"}
	}
}

func knownPortfolioCapabilities(modelID string) (*ModelCapabilities, bool) {
	normalized := strings.ToLower(strings.TrimSpace(modelID))
	normalized, explicitWindow := splitContextSuffix(normalized)
	if capability, ok := modelTable[normalized]; ok {
		return withExplicitContextWindow(capability, explicitWindow), true
	}
	if canonical, ok := aliases[normalized]; ok {
		if capability, exists := modelTable[canonical]; exists {
			return withExplicitContextWindow(capability, explicitWindow), true
		}
	}
	var best *ModelCapabilities
	bestLen := 0
	for key, capability := range modelTable {
		if strings.Contains(normalized, key) && len(key) > bestLen {
			best = capability
			bestLen = len(key)
		}
	}
	if best == nil {
		return nil, false
	}
	return withExplicitContextWindow(best, explicitWindow), true
}
