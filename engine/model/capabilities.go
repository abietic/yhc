// Package model provides model capability metadata and lookup functions.
//
// The model table is ported from the TypeScript reference (claude-code-ripe)
// and includes Anthropic Claude models, OpenAI GPT/o-series, Google Gemini,
// DeepSeek, and Qwen model families.
package model

import "strings"

// ModelCapabilities describes the known capabilities and limits of an LLM.
type ModelCapabilities struct {
	Name                 string
	ContextWindow        int // max input tokens
	MaxOutputTokens      int // max output tokens (upper limit)
	SupportsImages       bool
	SupportsPDFs         bool
	SupportsThinking     bool
	SupportsTools        bool    // supports function/tool calling
	SupportsStreaming    bool    // supports streaming responses
	SupportsSystemPrompt bool    // supports system-level messages
	IsFirstParty         bool    // Anthropic model
	CostPerInputToken    float64 // USD per token (for budget tracking)
	CostPerOutputToken   float64 // USD per token
	// DeprecatedAt indicates when this model was deprecated (zero = not deprecated).
	DeprecatedAt string
	// Successor is the recommended replacement model when deprecated.
	Successor string
}

// IsDeprecated returns true if this model has been marked as deprecated.
func (c *ModelCapabilities) IsDeprecated() bool {
	return c.DeprecatedAt != ""
}

// ModelTier represents the capability tier of a model.
// Higher tiers are more capable (and typically more expensive).
// Mirrors the reference's aliasMatchesParentTier tier comparison.
type ModelTier int

const (
	TierUnknown  ModelTier = 0
	TierSmall    ModelTier = 1 // Haiku-class, GPT-4o-mini, Flash
	TierMedium   ModelTier = 2 // Sonnet-class, GPT-4o, Gemini Pro
	TierLarge    ModelTier = 3 // Opus-class, o1, o3
	TierFrontier ModelTier = 4 // Latest frontier (Opus 4.5+, o3 full)
)

// GetModelTier maps a model name to its capability tier.
func GetModelTier(modelName string) ModelTier {
	cap := GetCapabilities(modelName)
	name := strings.ToLower(cap.Name)

	// Anthropic Opus family → Large/Frontier
	if strings.Contains(name, "opus-4-5") || strings.Contains(name, "opus-4-6") {
		return TierFrontier
	}
	if strings.Contains(name, "opus") {
		return TierLarge
	}

	// Anthropic Sonnet family → Medium
	if strings.Contains(name, "sonnet") {
		return TierMedium
	}

	// Anthropic Haiku family → Small
	if strings.Contains(name, "haiku") {
		return TierSmall
	}

	// OpenAI o-series reasoning → Large
	if strings.HasPrefix(name, "o1") || strings.HasPrefix(name, "o3") {
		return TierLarge
	}
	if strings.HasPrefix(name, "o4-mini") {
		return TierMedium
	}

	// OpenAI GPT-4o → Medium
	if strings.Contains(name, "gpt-4o-mini") {
		return TierSmall
	}
	if strings.Contains(name, "gpt-4") {
		return TierMedium
	}

	// Gemini Pro/Flash
	if strings.Contains(name, "gemini") && strings.Contains(name, "pro") {
		return TierMedium
	}
	if strings.Contains(name, "gemini") && strings.Contains(name, "flash") {
		return TierSmall
	}

	// DeepSeek reasoner → Medium
	if strings.Contains(name, "deepseek-r1") || strings.Contains(name, "deepseek-reasoner") {
		return TierMedium
	}
	if strings.Contains(name, "deepseek") {
		return TierSmall
	}

	// Qwen
	if strings.Contains(name, "qwen-max") {
		return TierMedium
	}
	if strings.Contains(name, "qwen") {
		return TierSmall
	}

	return TierUnknown
}

// AliasMatchesParentTier checks whether an agent's specified model is at or above
// the parent model's tier. Returns true if the alias is acceptable (no downgrade).
// Unknown models are allowed through (don't block custom/new models).
// Mirrors the reference's aliasMatchesParentTier() in src/agent/agentTypes.ts.
func AliasMatchesParentTier(agentModel, parentModel string) bool {
	if agentModel == "" {
		return true // inherit = always ok
	}
	agentTier := GetModelTier(agentModel)
	parentTier := GetModelTier(parentModel)

	// Don't block unknown models.
	if agentTier == TierUnknown || parentTier == TierUnknown {
		return true
	}

	return agentTier >= parentTier
}

// CheckDeprecation returns a deprecation warning message if the model is deprecated,
// or empty string if the model is current.
func CheckDeprecation(modelName string) string {
	cap := GetCapabilities(modelName)
	if !cap.IsDeprecated() {
		return ""
	}
	msg := "Model " + modelName + " was deprecated"
	if cap.DeprecatedAt != "" {
		msg += " on " + cap.DeprecatedAt
	}
	if cap.Successor != "" {
		msg += ". Consider switching to: " + cap.Successor
	}
	return msg
}

// API limits from the Anthropic API reference.
const (
	MaxImageBase64Size  = 5 * 1024 * 1024 // 5MB base64 encoded
	MaxImageRawSize     = 3750 * 1024     // ~3.75MB raw (base64 overhead)
	MaxImageDimensionPx = 2000
	MaxPDFRawSize       = 20 * 1024 * 1024 // 20MB raw PDF target size
	MaxPDFPages         = 100
	MaxMediaPerRequest  = 100
	PDFExtractThreshold = 3 * 1024 * 1024   // 3MB — above this, extract pages
	PDFMaxExtractSize   = 100 * 1024 * 1024 // 100MB absolute max for extraction
)

// Default capabilities for unknown models.
var defaultCapabilities = ModelCapabilities{
	Name:                 "unknown",
	ContextWindow:        200000,
	MaxOutputTokens:      32000,
	SupportsImages:       true,
	SupportsPDFs:         false,
	SupportsThinking:     false,
	SupportsTools:        true,
	SupportsStreaming:    true,
	SupportsSystemPrompt: true,
	IsFirstParty:         false,
	CostPerInputToken:    0.000005, // $5/Mtok
	CostPerOutputToken:   0.000025, // $25/Mtok
}

// modelTable is the comprehensive capability registry.
// Pricing is in USD per token (derived from per-million-token rates).
// All models listed here support tools, streaming, and system prompts unless
// noted otherwise. The fields are set explicitly for clarity.
var modelTable = map[string]*ModelCapabilities{
	// =========================================================================
	// Anthropic Claude — Opus family
	// =========================================================================
	"claude-opus-4-6": {
		Name:                 "claude-opus-4-6",
		ContextWindow:        200000,
		MaxOutputTokens:      128000,
		SupportsImages:       true,
		SupportsPDFs:         true,
		SupportsThinking:     true,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         true,
		CostPerInputToken:    0.000005, // $5/Mtok
		CostPerOutputToken:   0.000025, // $25/Mtok
	},
	"claude-opus-4-5-20251101": {
		Name:                 "claude-opus-4-5-20251101",
		ContextWindow:        200000,
		MaxOutputTokens:      64000,
		SupportsImages:       true,
		SupportsPDFs:         true,
		SupportsThinking:     true,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         true,
		CostPerInputToken:    0.000005, // $5/Mtok
		CostPerOutputToken:   0.000025, // $25/Mtok
	},
	"claude-opus-4-1-20250805": {
		Name:                 "claude-opus-4-1-20250805",
		ContextWindow:        200000,
		MaxOutputTokens:      32000,
		SupportsImages:       true,
		SupportsPDFs:         true,
		SupportsThinking:     true,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         true,
		CostPerInputToken:    0.000015, // $15/Mtok
		CostPerOutputToken:   0.000075, // $75/Mtok
	},
	"claude-opus-4-20250514": {
		Name:                 "claude-opus-4-20250514",
		ContextWindow:        200000,
		MaxOutputTokens:      32000,
		SupportsImages:       true,
		SupportsPDFs:         true,
		SupportsThinking:     true,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         true,
		CostPerInputToken:    0.000015, // $15/Mtok
		CostPerOutputToken:   0.000075, // $75/Mtok
	},

	// =========================================================================
	// Anthropic Claude — Sonnet family
	// =========================================================================
	"claude-sonnet-4-6": {
		Name:                 "claude-sonnet-4-6",
		ContextWindow:        200000,
		MaxOutputTokens:      128000,
		SupportsImages:       true,
		SupportsPDFs:         true,
		SupportsThinking:     true,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         true,
		CostPerInputToken:    0.000003, // $3/Mtok
		CostPerOutputToken:   0.000015, // $15/Mtok
	},
	"claude-sonnet-4-5-20250929": {
		Name:                 "claude-sonnet-4-5-20250929",
		ContextWindow:        200000,
		MaxOutputTokens:      64000,
		SupportsImages:       true,
		SupportsPDFs:         true,
		SupportsThinking:     true,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         true,
		CostPerInputToken:    0.000003, // $3/Mtok
		CostPerOutputToken:   0.000015, // $15/Mtok
	},
	"claude-sonnet-4-20250514": {
		Name:                 "claude-sonnet-4-20250514",
		ContextWindow:        200000,
		MaxOutputTokens:      64000,
		SupportsImages:       true,
		SupportsPDFs:         true,
		SupportsThinking:     true,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         true,
		CostPerInputToken:    0.000003, // $3/Mtok
		CostPerOutputToken:   0.000015, // $15/Mtok
	},
	"claude-3-7-sonnet-20250219": {
		Name:                 "claude-3-7-sonnet-20250219",
		ContextWindow:        200000,
		MaxOutputTokens:      64000,
		SupportsImages:       true,
		SupportsPDFs:         true,
		SupportsThinking:     true,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         true,
		CostPerInputToken:    0.000003, // $3/Mtok
		CostPerOutputToken:   0.000015, // $15/Mtok
	},
	"claude-3-5-sonnet-20241022": {
		Name:                 "claude-3-5-sonnet-20241022",
		ContextWindow:        200000,
		MaxOutputTokens:      8192,
		SupportsImages:       true,
		SupportsPDFs:         true,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         true,
		CostPerInputToken:    0.000003, // $3/Mtok
		CostPerOutputToken:   0.000015, // $15/Mtok
	},
	"claude-3-5-sonnet-20240620": {
		Name:                 "claude-3-5-sonnet-20240620",
		ContextWindow:        200000,
		MaxOutputTokens:      8192,
		SupportsImages:       true,
		SupportsPDFs:         false,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         true,
		CostPerInputToken:    0.000003, // $3/Mtok
		CostPerOutputToken:   0.000015, // $15/Mtok
	},

	// =========================================================================
	// Anthropic Claude — Haiku family
	// =========================================================================
	"claude-haiku-4-5-20251001": {
		Name:                 "claude-haiku-4-5-20251001",
		ContextWindow:        200000,
		MaxOutputTokens:      64000,
		SupportsImages:       true,
		SupportsPDFs:         true,
		SupportsThinking:     true,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         true,
		CostPerInputToken:    0.000001, // $1/Mtok
		CostPerOutputToken:   0.000005, // $5/Mtok
	},
	"claude-3-5-haiku-20241022": {
		Name:                 "claude-3-5-haiku-20241022",
		ContextWindow:        200000,
		MaxOutputTokens:      8192,
		SupportsImages:       true,
		SupportsPDFs:         false,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         true,
		CostPerInputToken:    0.0000008, // $0.80/Mtok
		CostPerOutputToken:   0.000004,  // $4/Mtok
	},

	// =========================================================================
	// Anthropic Claude — Legacy 3.x family
	// =========================================================================
	"claude-3-opus-20240229": {
		Name:                 "claude-3-opus-20240229",
		ContextWindow:        200000,
		MaxOutputTokens:      4096,
		SupportsImages:       true,
		SupportsPDFs:         false,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         true,
		CostPerInputToken:    0.000015, // $15/Mtok
		CostPerOutputToken:   0.000075, // $75/Mtok
	},
	"claude-3-sonnet-20240229": {
		Name:                 "claude-3-sonnet-20240229",
		ContextWindow:        200000,
		MaxOutputTokens:      4096,
		SupportsImages:       true,
		SupportsPDFs:         false,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         true,
		CostPerInputToken:    0.000003, // $3/Mtok
		CostPerOutputToken:   0.000015, // $15/Mtok
	},
	"claude-3-haiku-20240307": {
		Name:                 "claude-3-haiku-20240307",
		ContextWindow:        200000,
		MaxOutputTokens:      4096,
		SupportsImages:       true,
		SupportsPDFs:         false,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         true,
		CostPerInputToken:    0.00000025, // $0.25/Mtok
		CostPerOutputToken:   0.00000125, // $1.25/Mtok
	},

	// =========================================================================
	// OpenAI — GPT-4 family
	// =========================================================================
	"gpt-4": {
		Name:                 "gpt-4",
		ContextWindow:        8192,
		MaxOutputTokens:      8192,
		SupportsImages:       false,
		SupportsPDFs:         false,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.00003, // $30/Mtok
		CostPerOutputToken:   0.00006, // $60/Mtok
	},
	"gpt-4-turbo": {
		Name:                 "gpt-4-turbo",
		ContextWindow:        128000,
		MaxOutputTokens:      4096,
		SupportsImages:       true,
		SupportsPDFs:         false,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.00001, // $10/Mtok
		CostPerOutputToken:   0.00003, // $30/Mtok
	},
	"gpt-4-turbo-preview": {
		Name:                 "gpt-4-turbo-preview",
		ContextWindow:        128000,
		MaxOutputTokens:      4096,
		SupportsImages:       true,
		SupportsPDFs:         false,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.00001, // $10/Mtok
		CostPerOutputToken:   0.00003, // $30/Mtok
	},
	"gpt-4o": {
		Name:                 "gpt-4o",
		ContextWindow:        128000,
		MaxOutputTokens:      16384,
		SupportsImages:       true,
		SupportsPDFs:         false,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.0000025, // $2.50/Mtok
		CostPerOutputToken:   0.00001,   // $10/Mtok
	},
	"gpt-4o-mini": {
		Name:                 "gpt-4o-mini",
		ContextWindow:        128000,
		MaxOutputTokens:      16384,
		SupportsImages:       true,
		SupportsPDFs:         false,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.00000015, // $0.15/Mtok
		CostPerOutputToken:   0.0000006,  // $0.60/Mtok
	},

	// =========================================================================
	// OpenAI — o-series (reasoning models)
	// =========================================================================
	"o1": {
		Name:                 "o1",
		ContextWindow:        200000,
		MaxOutputTokens:      100000,
		SupportsImages:       true,
		SupportsPDFs:         false,
		SupportsThinking:     true,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.000015, // $15/Mtok
		CostPerOutputToken:   0.00006,  // $60/Mtok
	},
	"o1-mini": {
		Name:                 "o1-mini",
		ContextWindow:        128000,
		MaxOutputTokens:      65536,
		SupportsImages:       false,
		SupportsPDFs:         false,
		SupportsThinking:     true,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.000003, // $3/Mtok
		CostPerOutputToken:   0.000012, // $12/Mtok
	},
	"o3": {
		Name:                 "o3",
		ContextWindow:        200000,
		MaxOutputTokens:      100000,
		SupportsImages:       true,
		SupportsPDFs:         false,
		SupportsThinking:     true,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.00001, // $10/Mtok
		CostPerOutputToken:   0.00004, // $40/Mtok
	},
	"o3-mini": {
		Name:                 "o3-mini",
		ContextWindow:        200000,
		MaxOutputTokens:      100000,
		SupportsImages:       false,
		SupportsPDFs:         false,
		SupportsThinking:     true,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.0000011, // $1.10/Mtok
		CostPerOutputToken:   0.0000044, // $4.40/Mtok
	},
	"o4-mini": {
		Name:                 "o4-mini",
		ContextWindow:        200000,
		MaxOutputTokens:      100000,
		SupportsImages:       true,
		SupportsPDFs:         false,
		SupportsThinking:     true,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.0000011, // $1.10/Mtok
		CostPerOutputToken:   0.0000044, // $4.40/Mtok
	},

	// =========================================================================
	// Google Gemini
	// =========================================================================
	"gemini-2.5-pro": {
		Name:                 "gemini-2.5-pro",
		ContextWindow:        1000000,
		MaxOutputTokens:      65536,
		SupportsImages:       true,
		SupportsPDFs:         true,
		SupportsThinking:     true,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.00000125, // $1.25/Mtok
		CostPerOutputToken:   0.00001,    // $10/Mtok
	},
	"gemini-2.5-flash": {
		Name:                 "gemini-2.5-flash",
		ContextWindow:        1000000,
		MaxOutputTokens:      65536,
		SupportsImages:       true,
		SupportsPDFs:         true,
		SupportsThinking:     true,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.00000015, // $0.15/Mtok
		CostPerOutputToken:   0.0000006,  // $0.60/Mtok
	},
	"gemini-2.0-flash": {
		Name:                 "gemini-2.0-flash",
		ContextWindow:        1000000,
		MaxOutputTokens:      8192,
		SupportsImages:       true,
		SupportsPDFs:         true,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.0000001, // $0.10/Mtok
		CostPerOutputToken:   0.0000004, // $0.40/Mtok
	},
	"gemini-1.5-pro": {
		Name:                 "gemini-1.5-pro",
		ContextWindow:        1000000,
		MaxOutputTokens:      8192,
		SupportsImages:       true,
		SupportsPDFs:         true,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.00000125, // $1.25/Mtok
		CostPerOutputToken:   0.000005,   // $5/Mtok
	},
	"gemini-1.5-flash": {
		Name:                 "gemini-1.5-flash",
		ContextWindow:        1000000,
		MaxOutputTokens:      8192,
		SupportsImages:       true,
		SupportsPDFs:         true,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.000000075, // $0.075/Mtok
		CostPerOutputToken:   0.0000003,   // $0.30/Mtok
	},

	// =========================================================================
	// DeepSeek
	// =========================================================================
	"deepseek-v4-pro": {
		Name:                 "deepseek-v4-pro",
		ContextWindow:        1000000,
		MaxOutputTokens:      384000,
		SupportsImages:       false,
		SupportsPDFs:         false,
		SupportsThinking:     true,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.000000435, // $0.435/Mtok cache miss
		CostPerOutputToken:   0.00000087,  // $0.87/Mtok
	},
	"deepseek-v4-flash": {
		Name:                 "deepseek-v4-flash",
		ContextWindow:        1000000,
		MaxOutputTokens:      384000,
		SupportsImages:       false,
		SupportsPDFs:         false,
		SupportsThinking:     true,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.00000014, // $0.14/Mtok cache miss
		CostPerOutputToken:   0.00000028, // $0.28/Mtok
	},
	"deepseek-v3": {
		Name:                 "deepseek-v3",
		ContextWindow:        128000,
		MaxOutputTokens:      8192,
		SupportsImages:       false,
		SupportsPDFs:         false,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.00000027, // $0.27/Mtok
		CostPerOutputToken:   0.0000011,  // $1.10/Mtok
	},
	"deepseek-chat": {
		Name:                 "deepseek-chat",
		ContextWindow:        128000,
		MaxOutputTokens:      8192,
		SupportsImages:       false,
		SupportsPDFs:         false,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.00000027, // $0.27/Mtok
		CostPerOutputToken:   0.0000011,  // $1.10/Mtok
	},
	"deepseek-r1": {
		Name:                 "deepseek-r1",
		ContextWindow:        128000,
		MaxOutputTokens:      8192,
		SupportsImages:       false,
		SupportsPDFs:         false,
		SupportsThinking:     true,
		SupportsTools:        false, // R1 does not support tool calling
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.00000055, // $0.55/Mtok
		CostPerOutputToken:   0.00000219, // $2.19/Mtok
	},
	"deepseek-reasoner": {
		Name:                 "deepseek-reasoner",
		ContextWindow:        128000,
		MaxOutputTokens:      8192,
		SupportsImages:       false,
		SupportsPDFs:         false,
		SupportsThinking:     true,
		SupportsTools:        false, // Reasoner does not support tool calling
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.00000055, // $0.55/Mtok
		CostPerOutputToken:   0.00000219, // $2.19/Mtok
	},

	// =========================================================================
	// Qwen (Alibaba)
	// =========================================================================
	"qwen-max": {
		Name:                 "qwen-max",
		ContextWindow:        32000,
		MaxOutputTokens:      8192,
		SupportsImages:       false,
		SupportsPDFs:         false,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.0000016, // $1.60/Mtok
		CostPerOutputToken:   0.0000064, // $6.40/Mtok
	},
	"qwen-plus": {
		Name:                 "qwen-plus",
		ContextWindow:        131072,
		MaxOutputTokens:      8192,
		SupportsImages:       false,
		SupportsPDFs:         false,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.0000004, // $0.40/Mtok
		CostPerOutputToken:   0.0000012, // $1.20/Mtok
	},
	"qwen-turbo": {
		Name:                 "qwen-turbo",
		ContextWindow:        131072,
		MaxOutputTokens:      8192,
		SupportsImages:       false,
		SupportsPDFs:         false,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.0000002, // $0.20/Mtok
		CostPerOutputToken:   0.0000006, // $0.60/Mtok
	},
	"qwen2.5-72b-instruct": {
		Name:                 "qwen2.5-72b-instruct",
		ContextWindow:        131072,
		MaxOutputTokens:      8192,
		SupportsImages:       false,
		SupportsPDFs:         false,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.0000004, // $0.40/Mtok
		CostPerOutputToken:   0.0000012, // $1.20/Mtok
	},
	"qwen2.5-coder-32b-instruct": {
		Name:                 "qwen2.5-coder-32b-instruct",
		ContextWindow:        131072,
		MaxOutputTokens:      8192,
		SupportsImages:       false,
		SupportsPDFs:         false,
		SupportsThinking:     false,
		SupportsTools:        true,
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
		IsFirstParty:         false,
		CostPerInputToken:    0.0000002, // $0.20/Mtok
		CostPerOutputToken:   0.0000006, // $0.60/Mtok
	},
}

// aliases maps short/common names to canonical model identifiers.
var aliases = map[string]string{
	// Claude Opus aliases
	"claude-opus-4":   "claude-opus-4-20250514",
	"claude-opus-4-1": "claude-opus-4-1-20250805",
	"claude-opus-4-5": "claude-opus-4-5-20251101",

	// Claude Sonnet aliases
	"claude-sonnet-4":   "claude-sonnet-4-20250514",
	"claude-sonnet-4-5": "claude-sonnet-4-5-20250929",
	"claude-3-7-sonnet": "claude-3-7-sonnet-20250219",
	"claude-3-5-sonnet": "claude-3-5-sonnet-20241022",

	// Claude Haiku aliases
	"claude-haiku-4-5": "claude-haiku-4-5-20251001",
	"claude-3-5-haiku": "claude-3-5-haiku-20241022",
	"claude-3-opus":    "claude-3-opus-20240229",
	"claude-3-sonnet":  "claude-3-sonnet-20240229",
	"claude-3-haiku":   "claude-3-haiku-20240307",

	// Convenience short names
	"deepseek": "deepseek-v3",
}

// ResolveModelAlias returns the canonical identifier for a built-in model
// alias. Unknown names are returned unchanged. Explicit context suffixes are
// preserved so routing and capability resolution see the same model identity.
func ResolveModelAlias(name string) string {
	trimmed := strings.TrimSpace(name)
	normalized := strings.ToLower(trimmed)
	suffix := ""
	if idx := strings.LastIndex(normalized, "["); idx > 0 && strings.HasSuffix(normalized, "]") {
		suffix = normalized[idx:]
		normalized = normalized[:idx]
	}
	if canonical, ok := aliases[normalized]; ok {
		return canonical + suffix
	}
	return trimmed
}

// GetCapabilities returns model capabilities for the given model name.
// It tries exact match, alias resolution, and substring matching.
// Returns a sensible default for unknown models.
func GetCapabilities(modelName string) *ModelCapabilities {
	if modelName == "" {
		return &defaultCapabilities
	}

	normalized := strings.ToLower(strings.TrimSpace(modelName))
	normalized, explicitContextWindow := splitContextSuffix(normalized)

	// 1. Exact match
	if cap, ok := modelTable[normalized]; ok {
		return withExplicitContextWindow(cap, explicitContextWindow)
	}

	// 2. Alias resolution
	if canonical, ok := aliases[normalized]; ok {
		if cap, ok := modelTable[canonical]; ok {
			return withExplicitContextWindow(cap, explicitContextWindow)
		}
	}

	// 3. Substring match: find the longest key that is contained in the input
	// This handles cases like "us.anthropic.claude-sonnet-4-20250514-v1:0"
	var bestMatch *ModelCapabilities
	bestLen := 0
	for key, cap := range modelTable {
		if strings.Contains(normalized, key) && len(key) > bestLen {
			bestMatch = cap
			bestLen = len(key)
		}
	}
	if bestMatch != nil {
		return withExplicitContextWindow(bestMatch, explicitContextWindow)
	}

	// 4. Reverse substring: check if any key contains the input
	// Handles short inputs like "claude-sonnet-4" matching "claude-sonnet-4-20250514"
	for key, cap := range modelTable {
		if strings.Contains(key, normalized) && len(key) > bestLen {
			bestMatch = cap
			bestLen = len(key)
		}
	}
	if bestMatch != nil {
		return withExplicitContextWindow(bestMatch, explicitContextWindow)
	}

	// 5. Return default for unknown models
	result := defaultCapabilities
	result.Name = modelName
	if explicitContextWindow > 0 {
		result.ContextWindow = explicitContextWindow
	}
	return &result
}

// ContextWindow returns the context window (max input tokens) for a model.
func ContextWindow(modelName string) int {
	return GetCapabilities(modelName).ContextWindow
}

// KnownContextWindow returns a context limit only when it comes from an
// explicit model suffix or a matched capability-table entry. Unlike
// ContextWindow, it never presents the unknown-model default as model fact.
func KnownContextWindow(modelName string) (int, bool) {
	if strings.TrimSpace(modelName) == "" {
		return 0, false
	}
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	normalized, explicitContextWindow := splitContextSuffix(normalized)
	if explicitContextWindow > 0 {
		return explicitContextWindow, true
	}
	if cap, ok := modelTable[normalized]; ok {
		return cap.ContextWindow, cap.ContextWindow > 0
	}
	if canonical, ok := aliases[normalized]; ok {
		if cap, exists := modelTable[canonical]; exists {
			return cap.ContextWindow, cap.ContextWindow > 0
		}
	}
	bestLen := 0
	window := 0
	for key, cap := range modelTable {
		if strings.Contains(normalized, key) && len(key) > bestLen {
			bestLen = len(key)
			window = cap.ContextWindow
		}
	}
	if window > 0 {
		return window, true
	}
	return 0, false
}

// MaxOutputTokens returns the maximum output tokens for a model.
func MaxOutputTokens(modelName string) int {
	return GetCapabilities(modelName).MaxOutputTokens
}

// splitContextSuffix removes an explicit extended-context marker from a model
// name and returns its token count. The marker is provider-neutral: it is a
// local capability override and must not be limited to Anthropic models.
func splitContextSuffix(name string) (string, int) {
	trimmed := strings.TrimSpace(name)
	lower := strings.ToLower(trimmed)
	for suffix, window := range map[string]int{"[1m]": 1000000, "[2m]": 2000000} {
		if strings.HasSuffix(lower, suffix) {
			return strings.TrimSpace(trimmed[:len(trimmed)-len(suffix)]), window
		}
	}
	return trimmed, 0
}

func withExplicitContextWindow(cap *ModelCapabilities, window int) *ModelCapabilities {
	if window <= 0 {
		return cap
	}
	result := *cap
	result.ContextWindow = window
	return &result
}

// stripContextSuffix removes [1m] or [2m] suffixes from model names.
func stripContextSuffix(name string) string {
	base, _ := splitContextSuffix(name)
	return base
}
