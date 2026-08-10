package permission

import (
	"encoding/json"
	"fmt"
	"strings"
)

// PermissionAction represents the outcome of a permission rule evaluation.
// Mirrors PermissionRule.ts action field.
type PermissionAction string

const (
	// ActionAllow means the tool use is permitted without user confirmation.
	ActionAllow PermissionAction = "allow"
	// ActionDeny means the tool use is blocked.
	ActionDeny PermissionAction = "deny"
	// ActionAsk means the user should be prompted for permission.
	ActionAsk PermissionAction = "ask"
)

// PermissionRule defines a single permission rule that matches tool invocations
// and specifies an action (allow/deny/ask).
// Mirrors PermissionRule.ts PermissionRule interface.
type PermissionRule struct {
	// ToolName is the tool this rule applies to. "*" matches all tools.
	// Supports glob-style patterns (e.g., "Bash", "Edit", "Read*").
	ToolName string

	// InputPattern is an optional pattern that matches against the tool input.
	// For Bash, this matches the command. For file tools, this matches the path.
	// Empty string means match all inputs.
	InputPattern string

	// Action is what to do when this rule matches.
	Action PermissionAction

	// Source identifies where this rule came from (for debugging/display).
	// e.g., "project-settings", "user-settings", "cli-flag"
	Source string
}

// RulesEngine evaluates a set of permission rules against tool invocations.
// Rules are evaluated with priority ordering: deny > ask > allow.
// If no rules match, the default action is ActionAsk (fail safe).
// Mirrors permissionsLoader.ts rule evaluation logic.
type RulesEngine struct {
	rules []PermissionRule
}

// EvaluateMatch evaluates rules and reports whether any rule matched.
func (e *RulesEngine) EvaluateMatch(toolName string, toolInput map[string]any) (PermissionAction, bool) {
	if e == nil || len(e.rules) == 0 {
		return ActionAsk, false
	}

	var hasDeny, hasAsk, hasAllow bool
	matched := false
	for i := range e.rules {
		if matchRule(&e.rules[i], toolName, toolInput) {
			matched = true
			switch e.rules[i].Action {
			case ActionDeny:
				hasDeny = true
			case ActionAsk:
				hasAsk = true
			case ActionAllow:
				hasAllow = true
			}
		}
	}
	switch {
	case hasDeny:
		return ActionDeny, matched
	case hasAsk:
		return ActionAsk, matched
	case hasAllow:
		return ActionAllow, matched
	default:
		return ActionAsk, false
	}
}

// NewRulesEngine creates a new RulesEngine with the given rules.
// Rules can come from project settings, user settings, or CLI flags.
func NewRulesEngine(rules []PermissionRule) *RulesEngine {
	return &RulesEngine{rules: append([]PermissionRule(nil), rules...)}
}

// Snapshot returns a detached copy of the effective rules in load order.
// Callers may hash this plain data when they need to detect policy drift;
// evaluation remains owned by RulesEngine.
func (e *RulesEngine) Snapshot() []PermissionRule {
	if e == nil {
		return nil
	}
	return append([]PermissionRule(nil), e.rules...)
}

// Evaluate checks the tool invocation against all rules and returns the
// highest-priority matching action.
//
// Priority ordering: deny > ask > allow.
// If no rules match, returns ActionAsk (default safe behavior).
func (e *RulesEngine) Evaluate(toolName string, toolInput map[string]any) PermissionAction {
	action, _ := e.EvaluateMatch(toolName, toolInput)
	return action
}

// IsToolBlanketDenied reports whether a deny rule removes the entire tool,
// independent of invocation input. Input-scoped denies remain runtime checks
// and must not hide the whole tool from the model.
func (e *RulesEngine) IsToolBlanketDenied(toolName string) bool {
	if e == nil {
		return false
	}
	for i := range e.rules {
		rule := &e.rules[i]
		if rule.Action == ActionDeny && rule.InputPattern == "" && matchPattern(rule.ToolName, toolName) {
			return true
		}
	}
	return false
}

// matchRule checks whether a single rule matches the given tool invocation.
func matchRule(rule *PermissionRule, toolName string, toolInput map[string]any) bool {
	// Check tool name match
	if !matchPattern(rule.ToolName, toolName) {
		return false
	}

	// If no input pattern specified, match all inputs
	if rule.InputPattern == "" {
		return true
	}

	// Extract the relevant input field based on tool type
	inputStr := extractInputString(toolName, toolInput)
	return matchPattern(rule.InputPattern, inputStr)
}

// matchPattern performs wildcard pattern matching.
// Wildcards (*) match any sequence of characters (including path separators).
// Use \* to match a literal asterisk. Use \\ to match a literal backslash.
// Also supports MCP server-level matching: rule "mcp__server" matches "mcp__server__tool".
// Mirrors shellRuleMatching.ts matchWildcardPattern behavior.
func matchPattern(pattern, value string) bool {
	if pattern == "*" {
		return true
	}

	// MCP server-level matching: "mcp__server" matches "mcp__server__toolname"
	if strings.HasPrefix(pattern, "mcp__") && strings.HasPrefix(value, "mcp__") {
		patternParts := strings.SplitN(pattern, "__", 3)
		valueParts := strings.SplitN(value, "__", 3)
		if len(patternParts) == 2 && len(valueParts) == 3 {
			if patternParts[1] == valueParts[1] {
				return true
			}
		}
		if len(patternParts) == 3 && patternParts[2] == "*" && len(valueParts) == 3 {
			if patternParts[1] == valueParts[1] {
				return true
			}
		}
	}

	return matchWildcardPattern(pattern, value)
}

// extractInputString extracts the relevant string from tool input for pattern matching.
// For Bash: matches against "command" field.
// For file tools (Read, Edit, Write): matches against "file_path" or "path" field.
// For others: matches against JSON serialization of input.
func extractInputString(toolName string, toolInput map[string]any) string {
	if len(toolInput) == 0 {
		return ""
	}

	switch toolName {
	case "Bash":
		if cmd, ok := toolInput["command"]; ok {
			return fmt.Sprint(cmd)
		}
	case "Read", "Edit", "Write":
		if fp, ok := toolInput["file_path"]; ok {
			return fmt.Sprint(fp)
		}
		if p, ok := toolInput["path"]; ok {
			return fmt.Sprint(p)
		}
	case "Glob", "Grep", "LS":
		if p, ok := toolInput["path"]; ok {
			return fmt.Sprint(p)
		}
	}

	// Fallback: serialize the entire input as JSON for matching
	encoded, err := json.Marshal(toolInput)
	if err != nil {
		return fmt.Sprint(toolInput)
	}
	return string(encoded)
}

// ParseRuleString parses "tool(pattern):action" format into a PermissionRule.
// Supported formats:
//   - "Bash(rm*):deny"      — deny Bash commands starting with "rm"
//   - "Edit(/etc/*):ask"    — ask before editing files under /etc/
//   - "Read:allow"          — allow all Read operations
//   - "*:ask"               — ask for all tools
//   - "*(*):deny"           — deny everything (tool and input both wildcard)
//
// Mirrors permissionsLoader.ts rule string parsing.
func ParseRuleString(s string) (PermissionRule, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return PermissionRule{}, fmt.Errorf("empty rule string")
	}

	// Split on the last ":" to get action
	lastColon := strings.LastIndex(s, ":")
	if lastColon < 0 {
		return PermissionRule{}, fmt.Errorf("invalid rule format %q: missing ':action' suffix", s)
	}

	actionStr := strings.TrimSpace(s[lastColon+1:])
	toolPart := strings.TrimSpace(s[:lastColon])

	// Parse action
	action, err := parseAction(actionStr)
	if err != nil {
		return PermissionRule{}, fmt.Errorf("invalid rule format %q: %w", s, err)
	}

	// Parse tool name and optional input pattern
	toolName, inputPattern := parseToolPart(toolPart)
	if toolName == "" {
		return PermissionRule{}, fmt.Errorf("invalid rule format %q: empty tool name", s)
	}

	return PermissionRule{
		ToolName:     toolName,
		InputPattern: inputPattern,
		Action:       action,
	}, nil
}

// parseAction converts a string to a PermissionAction.
func parseAction(s string) (PermissionAction, error) {
	switch strings.ToLower(s) {
	case "allow":
		return ActionAllow, nil
	case "deny":
		return ActionDeny, nil
	case "ask":
		return ActionAsk, nil
	default:
		return "", fmt.Errorf("unknown action %q (must be allow, deny, or ask)", s)
	}
}

// parseToolPart splits "ToolName(pattern)" or "ToolName(param:pattern)" into tool name and pattern.
// If no parentheses, the input pattern is empty.
// The extended format "ToolName(param_name:pattern)" is also supported for specificity,
// where param_name indicates which parameter to match against (e.g., "command", "file_path").
// When param_name is omitted, the default parameter for that tool type is used.
func parseToolPart(s string) (toolName, inputPattern string) {
	openParen := strings.Index(s, "(")
	if openParen < 0 {
		return s, ""
	}

	closeParen := strings.LastIndex(s, ")")
	if closeParen < openParen {
		// Malformed — treat entire string as tool name
		return s, ""
	}

	toolName = s[:openParen]
	inputPattern = s[openParen+1 : closeParen]

	// Strip param_name prefix if present (e.g., "command:git *" → "git *")
	// This is syntactic sugar — the RulesEngine always extracts the correct param
	// based on tool type via extractInputString.
	if colonIdx := strings.Index(inputPattern, ":"); colonIdx > 0 {
		paramName := inputPattern[:colonIdx]
		// Only strip if it looks like a known param name (not a file path like /etc/foo)
		if isKnownParamName(paramName) {
			inputPattern = inputPattern[colonIdx+1:]
		}
	}

	return toolName, inputPattern
}

// isKnownParamName returns true if the string is a recognized tool parameter name.
func isKnownParamName(s string) bool {
	switch strings.ToLower(s) {
	case "command", "file_path", "path", "pattern", "query", "uri", "url":
		return true
	}
	return false
}
