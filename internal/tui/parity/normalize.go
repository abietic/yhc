//go:build parity

package parity

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// StripANSI removes all ANSI escape sequences from the string.
func StripANSI(s string) string {
	return ansi.Strip(s)
}

// NormalizeForCompare prepares screen content for cross-project comparison:
// 1. Strips ANSI escape sequences
// 2. Collapses trailing whitespace on each line
// 3. Removes trailing blank lines
func NormalizeForCompare(s string) string {
	stripped := StripANSI(s)
	lines := strings.Split(stripped, "\n")

	// Trim trailing whitespace per line
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}

	// Remove trailing blank lines
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	return strings.Join(lines, "\n")
}

// NormalizeBranding replaces project-specific names with generic placeholders
// so that structural layout can be compared regardless of branding.
func NormalizeBranding(s string) string {
	replacer := strings.NewReplacer(
		"Yet Hooked on Coding", "[AGENT]",
		"YHC", "[AGENT]",
		"yhc", "[AGENT]",
		"crush", "[AGENT]",
		"Crush", "[AGENT]",
		"claude", "[AGENT]",
		"Claude", "[AGENT]",
		"claude-code", "[AGENT]",
		"Claude Code", "[AGENT]",
		"codex", "[AGENT]",
		"Codex", "[AGENT]",
	)
	return replacer.Replace(s)
}

// NormalizeStructural applies full normalization for parity comparison:
// strip ANSI, collapse whitespace, normalize branding.
func NormalizeStructural(s string) string {
	return NormalizeBranding(NormalizeForCompare(s))
}
