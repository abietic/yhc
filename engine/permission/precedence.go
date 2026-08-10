package permission

import (
	"path/filepath"
	"strings"
)

// RuleMatch pairs a rule with match metadata used for precedence resolution.
// Created during rule evaluation to carry specificity information.
type RuleMatch struct {
	Rule        PermissionRule
	ToolExact   bool // tool name matched exactly (not wildcard)
	InputExact  bool // input pattern matched exactly (not wildcard prefix)
	Specificity int  // higher = more specific (length of input pattern as heuristic)
}

// RuleDecision is the typed winning permission-rule result consumed by the
// invocation-policy owner. It retains provenance and exactness instead of
// collapsing the result to (action, matched).
type RuleDecision struct {
	Action      PermissionAction
	Rule        *PermissionRule
	Matched     bool
	ToolExact   bool
	InputExact  bool
	Specificity int
}

// ResolvePrecedence determines the winning rule when multiple rules match the
// same tool invocation. This implements the reference behavior:
//
//  1. Explicit deny beats everything
//  2. Explicit ask beats allow
//  3. Among rules of the same action: more specific rules beat less specific ones
//  4. Tool-specific rules beat wildcard tool rules
//  5. Rules with input patterns beat rules without input patterns
//  6. Longer/more-specific path patterns beat shorter/less-specific ones
//
// Returns the winning action and whether any rule matched.
// If no rules match, returns ActionAsk (fail-safe default).
//
// Mirrors the implicit precedence logic in permissions.ts hasPermissionsToUseToolInner:
// - Step 1a: entire-tool deny rules checked first
// - Step 1b: entire-tool ask rules checked next
// - Step 2b: entire-tool allow rules checked after mode bypass
// - Tool-specific content rules (e.g., Bash(rm*)) checked via checkPermissions
func ResolvePrecedence(matches []RuleMatch) (PermissionAction, bool) {
	if len(matches) == 0 {
		return ActionAsk, false
	}

	// Group matches by action
	var denyMatches, askMatches, allowMatches []RuleMatch
	for _, m := range matches {
		switch m.Rule.Action {
		case ActionDeny:
			denyMatches = append(denyMatches, m)
		case ActionAsk:
			askMatches = append(askMatches, m)
		case ActionAllow:
			allowMatches = append(allowMatches, m)
		}
	}

	// Priority ordering: deny > ask > allow
	// Within the same action category, the most specific match wins.
	switch {
	case len(denyMatches) > 0:
		return ActionDeny, true
	case len(askMatches) > 0:
		return ActionAsk, true
	case len(allowMatches) > 0:
		return ActionAllow, true
	default:
		return ActionAsk, false
	}
}

// ResolvePrecedenceWithWinner returns both the winning action and the most
// specific matching rule (for attribution / denial messages).
func ResolvePrecedenceWithWinner(matches []RuleMatch) (PermissionAction, *PermissionRule, bool) {
	decision := ResolvePrecedenceDecision(matches)
	return decision.Action, decision.Rule, decision.Matched
}

// ResolvePrecedenceDecision returns the winning rule together with the match
// facts required to decide whether it is narrow user authority.
func ResolvePrecedenceDecision(matches []RuleMatch) RuleDecision {
	if len(matches) == 0 {
		return RuleDecision{Action: ActionAsk}
	}

	// Group by action
	var denyMatches, askMatches, allowMatches []RuleMatch
	for _, m := range matches {
		switch m.Rule.Action {
		case ActionDeny:
			denyMatches = append(denyMatches, m)
		case ActionAsk:
			askMatches = append(askMatches, m)
		case ActionAllow:
			allowMatches = append(allowMatches, m)
		}
	}

	// Select the group by priority
	var chosen []RuleMatch
	switch {
	case len(denyMatches) > 0:
		chosen = denyMatches
	case len(askMatches) > 0:
		chosen = askMatches
	case len(allowMatches) > 0:
		chosen = allowMatches
	default:
		return RuleDecision{Action: ActionAsk}
	}

	// Within the chosen group, find the most specific match.
	best := chosen[0]
	for _, m := range chosen[1:] {
		if isMoreSpecific(m, best) {
			best = m
		}
	}

	rule := best.Rule
	return RuleDecision{
		Action:      best.Rule.Action,
		Rule:        &rule,
		Matched:     true,
		ToolExact:   best.ToolExact,
		InputExact:  best.InputExact,
		Specificity: best.Specificity,
	}
}

// isMoreSpecific returns true if a is more specific than b.
// Specificity ordering:
//  1. Tool-exact beats tool-wildcard
//  2. Has input pattern beats no input pattern
//  3. Longer input pattern beats shorter (proxy for path depth / command specificity)
//  4. Input-exact beats input-prefix match
func isMoreSpecific(a, b RuleMatch) bool {
	// Tool specificity: exact tool name > wildcard
	if a.ToolExact != b.ToolExact {
		return a.ToolExact
	}

	// Input pattern presence: has pattern > no pattern
	aHasInput := a.Rule.InputPattern != ""
	bHasInput := b.Rule.InputPattern != ""
	if aHasInput != bHasInput {
		return aHasInput
	}

	// Input pattern specificity: longer pattern = more specific
	if a.Specificity != b.Specificity {
		return a.Specificity > b.Specificity
	}

	// Exact input match > prefix/glob match
	if a.InputExact != b.InputExact {
		return a.InputExact
	}

	return false
}

// EvaluateWithPrecedence performs rule matching and returns the winning action
// using the full precedence algorithm. This is the recommended evaluation path
// for permission checks that need specificity-aware resolution.
//
// Compared to RulesEngine.Evaluate (which uses simple deny>ask>allow), this
// method tracks match specificity and can distinguish between:
//   - A broad "*:ask" rule vs a specific "Read(/home/user/project/*):allow" rule
//   - "Bash:deny" vs "Bash(git*):allow"
func (e *RulesEngine) EvaluateWithPrecedence(toolName string, toolInput map[string]any) (PermissionAction, *PermissionRule, bool) {
	decision := e.EvaluateDecision(toolName, toolInput)
	return decision.Action, decision.Rule, decision.Matched
}

// EvaluateDecision evaluates one candidate and preserves its winning rule's
// source and exactness for the QueryEngine policy boundary.
func (e *RulesEngine) EvaluateDecision(toolName string, toolInput map[string]any) RuleDecision {
	if e == nil || len(e.rules) == 0 {
		return RuleDecision{Action: ActionAsk}
	}

	matches := e.collectMatches(toolName, toolInput)
	return ResolvePrecedenceDecision(matches)
}

// collectMatches finds all matching rules and records specificity metadata.
func (e *RulesEngine) collectMatches(toolName string, toolInput map[string]any) []RuleMatch {
	var matches []RuleMatch

	inputStr := extractInputString(toolName, toolInput)

	for i := range e.rules {
		rule := &e.rules[i]

		// Check tool name match
		toolExact := false
		if rule.ToolName == toolName {
			toolExact = true
		} else if !matchPattern(rule.ToolName, toolName) {
			continue
		}

		// Check input pattern match
		if rule.InputPattern == "" {
			// Tool-wide rule (no input filter)
			matches = append(matches, RuleMatch{
				Rule:        *rule,
				ToolExact:   toolExact,
				InputExact:  false,
				Specificity: 0,
			})
			continue
		}

		// Match input pattern
		if !matchPattern(rule.InputPattern, inputStr) {
			continue
		}

		// Determine match exactness and specificity
		inputExact := !hasUnescapedWildcards(rule.InputPattern)
		specificity := computeSpecificity(rule.InputPattern)

		matches = append(matches, RuleMatch{
			Rule:        *rule,
			ToolExact:   toolExact,
			InputExact:  inputExact,
			Specificity: specificity,
		})
	}

	return matches
}

// computeSpecificity calculates a numeric specificity score for an input pattern.
// Higher values mean more specific:
//   - Longer patterns are more specific (path depth proxy)
//   - Patterns without wildcards are more specific than those with wildcards
//   - Absolute paths get a bonus for depth
func computeSpecificity(pattern string) int {
	if pattern == "" {
		return 0
	}

	// Base specificity is the pattern length
	score := len(pattern)

	// Penalize wildcards — they make a pattern less specific
	score -= strings.Count(pattern, "*") * 10
	score -= strings.Count(pattern, "?") * 5

	// Bonus for absolute paths (they are anchored)
	if filepath.IsAbs(pattern) {
		score += 10
	}

	// Bonus for path depth (number of separators)
	score += strings.Count(pattern, "/") * 5

	// Ensure non-negative
	if score < 1 {
		score = 1
	}

	return score
}
