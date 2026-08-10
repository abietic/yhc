package tools

import "strings"

// ToolValidationResult holds the result of validating a tool permission pattern.
//
// Reference: src/utils/settings/toolValidationConfig.ts (103 lines)
type ToolValidationResult struct {
	Valid      bool
	Error      string
	Suggestion string
	Examples   []string
}

var filePatternTools = map[string]bool{
	"Read":         true,
	"Write":        true,
	"Edit":         true,
	"Glob":         true,
	"NotebookRead": true,
	"NotebookEdit": true,
}

var bashPrefixTools = map[string]bool{
	"Bash": true,
}

// IsFilePatternTool returns true if the tool accepts file glob patterns.
func IsFilePatternTool(toolName string) bool {
	return filePatternTools[toolName]
}

// IsBashPrefixTool returns true if the tool accepts bash wildcard/prefix patterns.
func IsBashPrefixTool(toolName string) bool {
	return bashPrefixTools[toolName]
}

// ValidateToolPermissionPattern validates a permission pattern string for a
// specific tool, returning errors and suggestions for invalid patterns.
func ValidateToolPermissionPattern(toolName, content string) ToolValidationResult {
	switch toolName {
	case "WebSearch":
		if strings.Contains(content, "*") || strings.Contains(content, "?") {
			return ToolValidationResult{
				Valid:      false,
				Error:      "WebSearch does not support wildcards",
				Suggestion: "Use exact search terms without * or ?",
				Examples:   []string{"WebSearch(claude ai)", "WebSearch(typescript tutorial)"},
			}
		}
	case "WebFetch":
		if strings.Contains(content, "://") || strings.HasPrefix(content, "http") {
			return ToolValidationResult{
				Valid:      false,
				Error:      "WebFetch permissions use domain format, not URLs",
				Suggestion: `Use "domain:hostname" format`,
				Examples:   []string{"WebFetch(domain:example.com)", "WebFetch(domain:github.com)"},
			}
		}
		if !strings.HasPrefix(content, "domain:") {
			return ToolValidationResult{
				Valid:      false,
				Error:      `WebFetch permissions must use "domain:" prefix`,
				Suggestion: `Use "domain:hostname" format`,
				Examples:   []string{"WebFetch(domain:example.com)", "WebFetch(domain:*.google.com)"},
			}
		}
	}
	return ToolValidationResult{Valid: true}
}
