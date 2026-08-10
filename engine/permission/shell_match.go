package permission

import (
	"regexp"
	"strings"
)

// ShellRuleType classifies how a shell permission rule should be interpreted.
type ShellRuleType int

const (
	// ShellRuleExact means the rule is an exact command match.
	ShellRuleExact ShellRuleType = iota
	// ShellRulePrefix means the rule uses legacy "cmd:*" prefix syntax.
	ShellRulePrefix
	// ShellRuleWildcard means the rule contains unescaped glob wildcards.
	ShellRuleWildcard
)

// ShellRule is a parsed shell permission rule.
type ShellRule struct {
	Type    ShellRuleType
	Raw     string // Original rule string
	Command string // For exact rules
	Prefix  string // For prefix rules
	Pattern string // For wildcard rules
}

// ParseShellRule parses a permission rule string into a structured ShellRule.
// Supports three formats:
//   - Legacy prefix: "npm:*" matches commands starting with "npm"
//   - Wildcard: "git *" matches "git add", "git commit", etc.
//   - Exact: "make test" matches only "make test"
//
// Mirrors shellRuleMatching.ts:parsePermissionRule.
func ParseShellRule(rule string) ShellRule {
	// Check for legacy ":*" prefix syntax first (backwards compatibility)
	if prefix := extractLegacyPrefix(rule); prefix != "" {
		return ShellRule{
			Type:   ShellRulePrefix,
			Raw:    rule,
			Prefix: prefix,
		}
	}

	// Check for unescaped wildcards
	if hasUnescapedWildcards(rule) {
		return ShellRule{
			Type:    ShellRuleWildcard,
			Raw:     rule,
			Pattern: rule,
		}
	}

	// Otherwise it's an exact match
	return ShellRule{
		Type:    ShellRuleExact,
		Raw:     rule,
		Command: rule,
	}
}

// MatchShellRule checks whether a command matches a shell permission rule pattern.
// Supports three matching modes:
//
//   - Exact match: rule "make test" matches only "make test"
//   - Legacy prefix: rule "npm:*" matches any command starting with "npm"
//     (also matches bare "npm" without arguments)
//   - Wildcard: rule "git *" matches "git add", "git commit", etc.
//     A single trailing wildcard "cmd *" also matches bare "cmd".
//     Use \* for a literal asterisk and \\ for a literal backslash.
//
// The ** pattern is treated identically to * (matches any characters including
// path separators), since shell commands are flat strings rather than paths.
//
// Mirrors shellRuleMatching.ts:matchWildcardPattern + parsePermissionRule.
func MatchShellRule(pattern, command string) bool {
	rule := ParseShellRule(pattern)

	switch rule.Type {
	case ShellRuleExact:
		return command == rule.Command
	case ShellRulePrefix:
		// Prefix match: "npm:*" matches "npm install" and also bare "npm"
		if command == rule.Prefix {
			return true
		}
		return strings.HasPrefix(command, rule.Prefix+" ")
	case ShellRuleWildcard:
		return matchWildcardPattern(rule.Pattern, command)
	default:
		return false
	}
}

// matchWildcardPattern converts a wildcard pattern to a regex and tests
// the command against it. Wildcards (*) match any sequence of characters.
// Use \* for a literal asterisk and \\ for a literal backslash.
// ** is treated identically to * (both match any character sequence).
//
// Mirrors shellRuleMatching.ts:matchWildcardPattern.
func matchWildcardPattern(pattern, command string) bool {
	trimmed := strings.TrimSpace(pattern)

	// Phase 1: Process escape sequences and replace with sentinels.
	// We use Unicode private-use chars as sentinels since they won't
	// appear in normal shell commands.
	const (
		escapedStarSentinel      = "\uf000"
		escapedBackslashSentinel = "\uf001"
	)

	var processed strings.Builder
	for i := 0; i < len(trimmed); i++ {
		ch := trimmed[i]
		if ch == '\\' && i+1 < len(trimmed) {
			next := trimmed[i+1]
			if next == '*' {
				processed.WriteString(escapedStarSentinel)
				i++
				continue
			} else if next == '\\' {
				processed.WriteString(escapedBackslashSentinel)
				i++
				continue
			}
		}
		processed.WriteByte(ch)
	}

	processedStr := processed.String()

	// Count unescaped stars (before converting ** to *)
	unescapedStarCount := strings.Count(processedStr, "*")

	// Normalize ** to * (both match any characters in shell context)
	normalized := strings.ReplaceAll(processedStr, "**", "*")

	// Phase 2: Escape regex-special characters (except our sentinels and *)
	escaped := regexp.QuoteMeta(normalized)
	// QuoteMeta escapes *, so we need to undo that for unescaped wildcards.
	// After QuoteMeta, literal * becomes \*
	escaped = strings.ReplaceAll(escaped, `\*`, ".*")

	// Phase 3: Replace sentinels with their regex equivalents
	escaped = strings.ReplaceAll(escaped, regexp.QuoteMeta(escapedStarSentinel), `\*`)
	escaped = strings.ReplaceAll(escaped, regexp.QuoteMeta(escapedBackslashSentinel), `\\`)

	// Phase 4: When pattern ends with " *" (space + single wildcard), make the
	// trailing space-and-args optional so "git *" matches both "git add" and bare "git".
	// This aligns wildcard matching with prefix rule semantics.
	// Only apply for patterns with a single unescaped wildcard.
	if strings.HasSuffix(escaped, " .*") && unescapedStarCount == 1 {
		escaped = escaped[:len(escaped)-3] + "( .*)?"
	}

	// Phase 5: Compile and test. Use (?s) so "." matches newlines.
	regexStr := "(?s)^" + escaped + "$"
	re, err := regexp.Compile(regexStr)
	if err != nil {
		// If we somehow produce an invalid regex, fall back to exact match.
		return pattern == command
	}

	return re.MatchString(command)
}

// extractLegacyPrefix extracts a prefix from legacy ":*" syntax.
// "npm:*" returns "npm". Returns "" if not in legacy format.
func extractLegacyPrefix(rule string) string {
	if !strings.HasSuffix(rule, ":*") {
		return ""
	}
	prefix := rule[:len(rule)-2]
	if prefix == "" {
		return ""
	}
	return prefix
}

// hasUnescapedWildcards checks if a pattern contains unescaped * characters.
// Returns false for legacy ":*" syntax (handled separately as prefix rules).
// An asterisk is considered escaped if preceded by an odd number of backslashes.
//
// Mirrors shellRuleMatching.ts:hasWildcards.
func hasUnescapedWildcards(pattern string) bool {
	// Legacy prefix syntax is not a wildcard pattern
	if strings.HasSuffix(pattern, ":*") {
		return false
	}

	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '*' {
			// Count preceding backslashes
			backslashes := 0
			for j := i - 1; j >= 0 && pattern[j] == '\\'; j-- {
				backslashes++
			}
			// Even number of backslashes (including 0) means unescaped
			if backslashes%2 == 0 {
				return true
			}
		}
	}
	return false
}
