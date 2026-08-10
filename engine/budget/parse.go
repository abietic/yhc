package budget

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Regex patterns for ParseTokenBudgetFromText.
// These extend the patterns in token.go with additional forms.
var (
	// "+500k", "+2M", "+1.5m" — shorthand with suffix, anywhere in text
	parseShorthandRE = regexp.MustCompile(`(?i)\+(\d+(?:\.\d+)?)\s*([kmKM])`)

	// "use 500k tokens", "use 2M tokens", "spend 100k tokens"
	parseVerboseRE = regexp.MustCompile(`(?i)\b(?:use|spend)\s+(\d+(?:\.\d+)?)\s*([kmKM])?\s*(?:more\s+)?tokens?`)

	// "500k more tokens", "2M more tokens"
	parseMoreTokensRE = regexp.MustCompile(`(?i)\b(\d+(?:\.\d+)?)\s*([kmKM])\s+more\s+tokens?`)

	// "+50000" — plain number with leading +, no suffix
	parsePlainPlusRE = regexp.MustCompile(`\+(\d+)\s*(?:tokens?)?`)

	// For IsTokenBudgetContinuation: text is primarily a budget pattern
	// Matches the entire trimmed string as a budget directive
	continuationRE = regexp.MustCompile(
		`(?i)^\s*` +
			`(?:\+\s*)?` + // optional leading +
			`(\d+(?:\.\d+)?)` + // number
			`\s*([kmKM])?` + // optional suffix
			`\s*(?:more\s+)?(?:tokens?)?\s*[.!?]?\s*$`, // optional "tokens"/"more tokens" + trailing punctuation
	)
)

// parseTextMultipliers maps suffix to multiplier for ParseTokenBudgetFromText.
var parseTextMultipliers = map[string]float64{
	"k": 1_000,
	"m": 1_000_000,
}

// applyMultiplier parses the numeric value and applies the suffix multiplier.
// Returns 0 if parsing fails or result is non-positive.
func applyMultiplier(numStr, suffix string) int {
	f, err := strconv.ParseFloat(numStr, 64)
	if err != nil || f <= 0 {
		return 0
	}
	if suffix == "" {
		return int(math.Round(f))
	}
	mult, ok := parseTextMultipliers[strings.ToLower(suffix)]
	if !ok {
		return 0
	}
	return int(math.Round(f * mult))
}

// ParseTokenBudgetFromText scans user text for token budget hints.
// Recognizes patterns like:
//   - "+500k" or "+500K" → 500,000 tokens
//   - "+2M" or "+2m" → 2,000,000 tokens
//   - "use 500k tokens" → 500,000
//   - "500k more tokens" → 500,000
//   - "+1.5M" → 1,500,000
//   - plain numbers: "+50000" → 50,000
//
// Returns 0 if no budget hint is found.
func ParseTokenBudgetFromText(text string) int {
	// Try shorthand with suffix first: "+500k", "+2M"
	if m := parseShorthandRE.FindStringSubmatch(text); m != nil {
		if v := applyMultiplier(m[1], m[2]); v > 0 {
			return v
		}
	}

	// Try verbose: "use 500k tokens", "spend 2M tokens"
	if m := parseVerboseRE.FindStringSubmatch(text); m != nil {
		if v := applyMultiplier(m[1], m[2]); v > 0 {
			return v
		}
	}

	// Try "N more tokens": "500k more tokens"
	if m := parseMoreTokensRE.FindStringSubmatch(text); m != nil {
		if v := applyMultiplier(m[1], m[2]); v > 0 {
			return v
		}
	}

	// Try plain number with +: "+50000"
	if m := parsePlainPlusRE.FindStringSubmatch(text); m != nil {
		if v := applyMultiplier(m[1], ""); v > 0 {
			return v
		}
	}

	return 0
}

// IsTokenBudgetContinuation checks if a user message is primarily a token
// budget continuation request (e.g., just "+500k" with no other content).
// Returns true if the text, after trimming, is primarily a token budget pattern.
// Used by the engine to detect continuation prompts vs. new instructions.
func IsTokenBudgetContinuation(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	return continuationRE.MatchString(trimmed)
}
