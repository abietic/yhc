package model

import (
	"fmt"
	"strings"
)

// ReasoningDialect identifies the provider wire contract used to lower one
// model-neutral reasoning effort. It deliberately does not expose SDK option
// types to configuration, session, or failover code.
type ReasoningDialect string

const (
	ReasoningDialectClaudeOutputConfig ReasoningDialect = "claude-output-config"
	ReasoningDialectOpenAIResponses    ReasoningDialect = "openai-responses"
	ReasoningDialectArkResponses       ReasoningDialect = "ark-responses"
	ReasoningDialectGeminiThinking     ReasoningDialect = "gemini-thinking"
	ReasoningDialectDeepSeek           ReasoningDialect = "deepseek"
)

// ReasoningMode carries an explicit provider thinking toggle when the wire
// protocol distinguishes it from the effort value itself.
type ReasoningMode string

const (
	ReasoningModeUnspecified ReasoningMode = ""
	ReasoningModeDisabled    ReasoningMode = "disabled"
	ReasoningModeEnabled     ReasoningMode = "enabled"
)

// ResolvedReasoningEffort is the adapter-owned lowering of one canonical
// request value. CanonicalEffort remains stable across failover; WireEffort and
// ThinkingMode are recomputed for every candidate adapter.
type ResolvedReasoningEffort struct {
	CanonicalEffort string
	WireEffort      string
	Dialect         ReasoningDialect
	ThinkingMode    ReasoningMode
}

type reasoningAdapterPolicy struct {
	provider string
	aliases  []string
	dialect  ReasoningDialect
	efforts  []string
}

var reasoningAdapterPolicies = []reasoningAdapterPolicy{
	{
		provider: "agenticclaude",
		aliases:  []string{"anthropic", "claude"},
		dialect:  ReasoningDialectClaudeOutputConfig,
		efforts:  []string{"low", "medium", "high", "xhigh", "max"},
	},
	{
		provider: "agenticopenai",
		aliases:  []string{"openai"},
		dialect:  ReasoningDialectOpenAIResponses,
		efforts:  []string{"none", "minimal", "low", "medium", "high", "xhigh"},
	},
	{
		provider: "agenticark",
		aliases:  []string{"ark", "volcengine"},
		dialect:  ReasoningDialectArkResponses,
		efforts:  []string{"minimal", "low", "medium", "high"},
	},
	{
		provider: "agenticgemini",
		aliases:  []string{"google", "gemini"},
		dialect:  ReasoningDialectGeminiThinking,
		efforts:  []string{"low", "high"},
	},
	{
		provider: "agenticdeepseek",
		aliases:  []string{"deepseek"},
		dialect:  ReasoningDialectDeepSeek,
		efforts:  []string{"none", "high", "max"},
	},
	{
		provider: "agenticqwen",
		aliases:  []string{"qwen", "dashscope"},
	},
}

// ResolveAdapterReasoningEffort validates one canonical effort against the
// selected adapter and returns its provider wire representation. Empty effort
// means provider default and intentionally produces no wire option.
func ResolveAdapterReasoningEffort(
	provider string,
	effort string,
) (ResolvedReasoningEffort, error) {
	policy, ok := reasoningPolicy(provider)
	if !ok {
		return ResolvedReasoningEffort{}, fmt.Errorf(
			"provider %q has no reasoning-effort adapter policy",
			strings.TrimSpace(provider),
		)
	}

	normalized, err := ValidateReasoningEffort(effort)
	if err != nil {
		return ResolvedReasoningEffort{}, err
	}
	if normalized == "" {
		return ResolvedReasoningEffort{Dialect: policy.dialect}, nil
	}
	if !containsReasoningEffort(policy.efforts, normalized) {
		return ResolvedReasoningEffort{}, fmt.Errorf(
			"provider %q cannot lower reasoning effort %q",
			policy.provider,
			normalized,
		)
	}

	resolved := ResolvedReasoningEffort{
		CanonicalEffort: normalized,
		WireEffort:      normalized,
		Dialect:         policy.dialect,
	}
	if policy.dialect == ReasoningDialectDeepSeek {
		switch normalized {
		case "none":
			resolved.WireEffort = ""
			resolved.ThinkingMode = ReasoningModeDisabled
		case "high", "max":
			resolved.ThinkingMode = ReasoningModeEnabled
		}
	}
	return resolved, nil
}

// SupportsAdapterReasoningEffort reports whether one provider adapter can
// lower an explicit effort value. It remains a small compatibility wrapper for
// callers that only need a predicate; new wire code should use the resolver.
func SupportsAdapterReasoningEffort(provider, effort string) bool {
	_, err := ResolveAdapterReasoningEffort(provider, effort)
	return err == nil
}

// AdapterReasoningEfforts returns the adapter's ordered canonical vocabulary.
// The returned slice is detached from the policy table.
func AdapterReasoningEfforts(provider string) []string {
	policy, ok := reasoningPolicy(provider)
	if !ok || len(policy.efforts) == 0 {
		return nil
	}
	return append([]string(nil), policy.efforts...)
}

// DefaultReasoningEfforts returns exact-model request capability metadata when
// both the built-in model catalog and its matching provider adapter establish
// the fact. Unknown or cross-provider model IDs deliberately remain unknown.
func DefaultReasoningEfforts(provider, modelID string) ([]string, bool) {
	capabilities, ok := knownExactPortfolioCapabilities(modelID)
	if !ok || !capabilities.SupportsThinking {
		return nil, false
	}
	policy, ok := reasoningPolicy(provider)
	if !ok || len(policy.efforts) == 0 {
		return nil, false
	}
	modelPolicy, ok := reasoningPolicy(string(DetectProvider(capabilities.Name)))
	if !ok || modelPolicy.provider != policy.provider {
		return nil, false
	}
	if policy.dialect == ReasoningDialectDeepSeek {
		switch strings.ToLower(capabilities.Name) {
		case "deepseek-v4-pro", "deepseek-v4-flash":
		default:
			return nil, false
		}
	}
	return append([]string(nil), policy.efforts...), true
}

func reasoningPolicy(provider string) (reasoningAdapterPolicy, bool) {
	normalized := strings.ToLower(strings.TrimSpace(provider))
	for _, policy := range reasoningAdapterPolicies {
		if normalized == policy.provider || containsReasoningEffort(policy.aliases, normalized) {
			return policy, true
		}
	}
	return reasoningAdapterPolicy{}, false
}

func containsReasoningEffort(values []string, effort string) bool {
	for _, value := range values {
		if value == effort {
			return true
		}
	}
	return false
}
