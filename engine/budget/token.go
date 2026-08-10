package budget

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// TokenBudget tracks token consumption per turn.
// Mirrors query.ts:1308-1355.
type TokenBudget struct {
	CurrentInputTokens  int
	CurrentOutputTokens int
	TurnTokens          int
	TurnBudget          int
	ContinuationCount   int
}

// BudgetDecision is the result of checking the token budget.
type BudgetDecision struct {
	Action             string // "continue" or "complete"
	ContinuationCount  int
	Pct                float64
	TurnTokens         int
	Budget             int
	NudgeMessage       string
	DiminishingReturns bool
}

// NewTokenBudget creates a new budget tracker.
func NewTokenBudget(turnBudget int) *TokenBudget {
	return &TokenBudget{TurnBudget: turnBudget}
}

// SetBudgetLevel sets the turn budget based on a named effort level.
// Levels: "low" (1000), "medium" (4000), "high" (16000), "max" (64000).
func (tb *TokenBudget) SetBudgetLevel(level string) {
	switch strings.ToLower(level) {
	case "low":
		tb.TurnBudget = 1000
	case "medium":
		tb.TurnBudget = 4000
	case "high":
		tb.TurnBudget = 16000
	case "max":
		tb.TurnBudget = 64000
	}
}

// GetBudgetLevel returns the named effort level for the current turn budget.
func (tb *TokenBudget) GetBudgetLevel() string {
	switch {
	case tb.TurnBudget <= 1000:
		return "low"
	case tb.TurnBudget <= 4000:
		return "medium"
	case tb.TurnBudget <= 16000:
		return "high"
	default:
		return "max"
	}
}

// RecordInput records input tokens spent.
func (tb *TokenBudget) RecordInput(tokens int) {
	tb.CurrentInputTokens += tokens
	tb.TurnTokens += tokens
}

// RecordOutput records output tokens spent.
func (tb *TokenBudget) RecordOutput(tokens int) {
	tb.CurrentOutputTokens += tokens
	tb.TurnTokens += tokens
}

// Check evaluates whether the turn should continue or stop.
func (tb *TokenBudget) Check() *BudgetDecision {
	pct := float64(tb.TurnTokens) / float64(tb.TurnBudget) * 100
	if pct < 80 {
		return &BudgetDecision{Action: "continue", Pct: pct}
	}
	if tb.ContinuationCount < 3 && pct < 150 {
		tb.ContinuationCount++
		tb.TurnTokens = 0
		return &BudgetDecision{
			Action:            "continue",
			ContinuationCount: tb.ContinuationCount,
			Pct:               pct,
			NudgeMessage:      "Output token budget consumed. Break remaining work into smaller pieces.",
		}
	}
	return &BudgetDecision{
		Action:             "complete",
		Pct:                pct,
		DiminishingReturns: tb.ContinuationCount > 0,
	}
}

// --- Token budget parsing from user text ---
// Mirrors utils/tokenBudget.ts parseTokenBudget.

var (
	// Shorthand anchored to start: "+500k", "+2M"
	shorthandStartRE = regexp.MustCompile(`(?i)^\s*\+(\d+(?:\.\d+)?)\s*(k|m|b)\b`)
	// Shorthand anchored to end: "fix this +2m"
	shorthandEndRE = regexp.MustCompile(`(?i)\s\+(\d+(?:\.\d+)?)\s*(k|m|b)\s*[.!?]?\s*$`)
	// Verbose anywhere: "use 2M tokens", "spend 500k tokens"
	verboseRE = regexp.MustCompile(`(?i)\b(?:use|spend)\s+(\d+(?:\.\d+)?)\s*(k|m|b)\s*tokens?\b`)
)

var multipliers = map[string]float64{
	"k": 1_000,
	"m": 1_000_000,
	"b": 1_000_000_000,
}

func parseBudgetMatch(value, suffix string) int {
	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	mult := multipliers[strings.ToLower(suffix)]
	return int(math.Round(f * mult))
}

// ParseTokenBudget extracts a token budget directive from user text.
// Returns 0 if no budget directive is found.
// Supports: "+500k", "+2M", "use 2M tokens", "spend 500k tokens".
// Mirrors reference utils/tokenBudget.ts parseTokenBudget.
func ParseTokenBudget(text string) int {
	if m := shorthandStartRE.FindStringSubmatch(text); m != nil {
		return parseBudgetMatch(m[1], m[2])
	}
	if m := shorthandEndRE.FindStringSubmatch(text); m != nil {
		return parseBudgetMatch(m[1], m[2])
	}
	if m := verboseRE.FindStringSubmatch(text); m != nil {
		return parseBudgetMatch(m[1], m[2])
	}
	return 0
}

// GetBudgetContinuationMessage returns the nudge message injected when
// auto-continuing under a token budget.
// Mirrors reference utils/tokenBudget.ts getBudgetContinuationMessage.
func GetBudgetContinuationMessage(pct, turnTokens, budget int) string {
	return fmt.Sprintf(
		"Stopped at %d%% of token target (%s / %s). Keep working — do not summarize.",
		pct, formatTokenCount(turnTokens), formatTokenCount(budget),
	)
}

func formatTokenCount(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%dk", n/1_000)
	}
	return strconv.Itoa(n)
}
