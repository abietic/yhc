package engine

import (
	"errors"
	"fmt"
	"math/bits"

	"github.com/cloudwego/eino/schema"
)

// normalizedGoalProviderUsage is the canonical, provider-neutral form of a
// single provider token-usage report for goal accounting. All counts are
// non-negative and validated against the breakdown bounds.
type normalizedGoalProviderUsage struct {
	PromptTokens       uint64
	CachedPromptTokens uint64
	CompletionTokens   uint64
	ReasoningTokens    uint64
	TotalTokens        uint64
	BillableTokens     uint64
}

// errNilGoalProviderUsage is returned when the provider reports no usage at
// all. Missing usage is never estimated.
var errNilGoalProviderUsage = errors.New("normalize goal provider usage: nil usage")

// normalizeGoalProviderUsage validates a provider-reported usage record and
// converts it into the canonical non-negative form. It is pure and
// deterministic: no I/O, no globals, no provider-specific policy.
//
// Rules:
//   - nil usage is an error; a non-nil all-zero usage is valid.
//   - negative prompt, cached, completion, total, or reasoning counts are
//     rejected.
//   - cached tokens must not exceed prompt tokens; reasoning tokens must not
//     exceed completion tokens.
//   - TotalTokens is normalized to max(provider total, prompt+completion)
//     using checked uint64 arithmetic.
//   - BillableTokens is the normalized total minus reported cached tokens.
func normalizeGoalProviderUsage(usage *schema.TokenUsage) (normalizedGoalProviderUsage, error) {
	if usage == nil {
		return normalizedGoalProviderUsage{}, errNilGoalProviderUsage
	}

	counts := []struct {
		name  string
		value int
	}{
		{"prompt tokens", usage.PromptTokens},
		{"cached prompt tokens", usage.PromptTokenDetails.CachedTokens},
		{"completion tokens", usage.CompletionTokens},
		{"total tokens", usage.TotalTokens},
		{"reasoning tokens", usage.CompletionTokensDetails.ReasoningTokens},
	}
	for _, c := range counts {
		if c.value < 0 {
			return normalizedGoalProviderUsage{}, fmt.Errorf(
				"normalize goal provider usage: negative %s: %d", c.name, c.value)
		}
	}

	prompt := uint64(usage.PromptTokens)
	cached := uint64(usage.PromptTokenDetails.CachedTokens)
	completion := uint64(usage.CompletionTokens)
	reasoning := uint64(usage.CompletionTokensDetails.ReasoningTokens)
	providerTotal := uint64(usage.TotalTokens)

	if cached > prompt {
		return normalizedGoalProviderUsage{}, fmt.Errorf(
			"normalize goal provider usage: cached prompt tokens %d exceed prompt tokens %d",
			cached, prompt)
	}
	if reasoning > completion {
		return normalizedGoalProviderUsage{}, fmt.Errorf(
			"normalize goal provider usage: reasoning tokens %d exceed completion tokens %d",
			reasoning, completion)
	}

	breakdownTotal, carry := bits.Add64(prompt, completion, 0)
	if carry != 0 {
		return normalizedGoalProviderUsage{}, fmt.Errorf(
			"normalize goal provider usage: prompt tokens %d plus completion tokens %d overflow",
			prompt, completion)
	}

	total := providerTotal
	if breakdownTotal > total {
		total = breakdownTotal
	}

	return normalizedGoalProviderUsage{
		PromptTokens:       prompt,
		CachedPromptTokens: cached,
		CompletionTokens:   completion,
		ReasoningTokens:    reasoning,
		TotalTokens:        total,
		BillableTokens:     total - cached,
	}, nil
}
