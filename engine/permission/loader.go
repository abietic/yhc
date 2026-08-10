package permission

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// settingsPermissions represents the "permissions" section of a settings.json file.
// The format mirrors the TypeScript reference:
//
//	{
//	  "permissions": {
//	    "allow": ["Bash(npm *)","Read"],
//	    "deny":  ["Bash(rm -rf *)"],
//	    "ask":   ["Edit(/etc/*)"]
//	  }
//	}
type settingsPermissions struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
	Ask   []string `json:"ask,omitempty"`
}

// settingsJSON is the minimal shape of a .claude/settings.json file
// relevant to permission rule loading.
type settingsJSON struct {
	Permissions     *settingsPermissions `json:"permissions,omitempty"`
	PermissionRules []string             `json:"permission_rules,omitempty"`
}

// RuleSource identifies where a permission rule was loaded from.
const (
	SourceProject = "project-settings"
	SourceLocal   = "local-settings"
	SourceUser    = "user-settings"
)

// LoadPermissionRules loads permission rules from disk by reading both the
// project-level settings (.claude/settings.json within projectDir) and user-level
// settings (~/.claude/settings.json). Rules from the project settings file have
// higher priority and are returned first.
//
// Rule file format (settings.json):
//
//	{
//	  "permissions": {
//	    "allow": ["Bash(npm *)", "Read"],
//	    "deny":  ["Bash(rm -rf *)"],
//	    "ask":   ["Edit(/etc/*)"]
//	  },
//	  "permission_rules": ["Bash(npm*):allow"]  // legacy format also supported
//	}
//
// When merging, project rules come first (higher priority), followed by user rules.
// The RulesEngine evaluates deny > ask > allow across all rules, but ordering
// matters when rules of the same priority are present.
func LoadPermissionRules(projectDir string) ([]PermissionRule, error) {
	var allRules []PermissionRule

	// 1. Load local settings (highest priority — per-developer, git-ignored)
	localPath := filepath.Join(projectDir, ".claude", "settings.local.json")
	localRules, err := loadRulesFromFile(localPath, SourceLocal)
	if err != nil {
		return nil, err
	}
	allRules = append(allRules, localRules...)

	// 2. Load project-level rules (shared, committed)
	projectPath := filepath.Join(projectDir, ".claude", "settings.json")
	projectRules, err := loadRulesFromFile(projectPath, SourceProject)
	if err != nil {
		return nil, err
	}
	allRules = append(allRules, projectRules...)

	// 3. Load user-level rules (lowest priority)
	userPath := userSettingsPath()
	userRules, err := loadRulesFromFile(userPath, SourceUser)
	if err != nil {
		return nil, err
	}
	allRules = append(allRules, userRules...)

	return allRules, nil
}

// loadRulesFromFile reads a single settings.json file and converts its permission
// entries to PermissionRule structs. Returns (nil, nil) if the file does not exist.
func loadRulesFromFile(path, source string) ([]PermissionRule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var settings settingsJSON
	if err := json.Unmarshal(data, &settings); err != nil {
		// Malformed JSON — skip this file rather than hard-failing,
		// matching the lenient behavior in the TypeScript reference.
		return nil, nil
	}

	var rules []PermissionRule

	// Parse the structured "permissions" section (new format)
	if settings.Permissions != nil {
		rules = append(rules, parsePermissionSection(settings.Permissions, source)...)
	}

	// Parse legacy "permission_rules" array (old "Tool(pattern):action" format)
	for _, ruleStr := range settings.PermissionRules {
		rule, err := ParseRuleString(ruleStr)
		if err != nil {
			// Skip malformed rules to be lenient
			continue
		}
		rule.Source = source
		rules = append(rules, rule)
	}

	return rules, nil
}

// parsePermissionSection converts the structured permissions object into
// PermissionRule slices. Each behavior (allow/deny/ask) maps to the corresponding
// PermissionAction.
func parsePermissionSection(perms *settingsPermissions, source string) []PermissionRule {
	var rules []PermissionRule

	for _, ruleStr := range perms.Allow {
		rules = append(rules, parseRuleValue(ruleStr, ActionAllow, source))
	}
	for _, ruleStr := range perms.Deny {
		rules = append(rules, parseRuleValue(ruleStr, ActionDeny, source))
	}
	for _, ruleStr := range perms.Ask {
		rules = append(rules, parseRuleValue(ruleStr, ActionAsk, source))
	}

	return rules
}

// parseRuleValue converts a single permission rule string like "Bash(npm *)" or
// just "Read" into a PermissionRule with the given action and source.
//
// Supported formats:
//   - "ToolName"              — matches all inputs for that tool
//   - "ToolName(pattern)"     — matches inputs matching the glob pattern
//
// Escaped parentheses (\( and \)) in the content are unescaped.
func parseRuleValue(ruleStr string, action PermissionAction, source string) PermissionRule {
	toolName, inputPattern := parseRuleValueString(ruleStr)
	return PermissionRule{
		ToolName:     toolName,
		InputPattern: inputPattern,
		Action:       action,
		Source:       source,
	}
}

// parseRuleValueString splits "ToolName(content)" into tool name and content.
// Handles escaped parentheses inside content. If no parentheses, the entire
// string is the tool name and input pattern is empty.
func parseRuleValueString(s string) (toolName, inputPattern string) {
	// Find the first unescaped opening parenthesis
	openIdx := findFirstUnescaped(s, '(')
	if openIdx < 0 {
		return s, ""
	}

	// Find the last unescaped closing parenthesis
	closeIdx := findLastUnescaped(s, ')')
	if closeIdx <= openIdx || closeIdx != len(s)-1 {
		// Malformed — treat entire string as tool name
		return s, ""
	}

	toolName = s[:openIdx]
	if toolName == "" {
		return s, ""
	}

	rawContent := s[openIdx+1 : closeIdx]

	// Empty content or standalone wildcard means tool-wide rule (no input filter)
	if rawContent == "" || rawContent == "*" {
		return toolName, ""
	}

	// Unescape content: \( -> (, \) -> ), \\ -> backslash
	inputPattern = unescapeRuleContent(rawContent)
	return toolName, inputPattern
}

// unescapeRuleContent reverses the escaping applied to rule content.
// Order matters: parentheses first, then backslashes.
func unescapeRuleContent(s string) string {
	// Phase 1: replace \( and \) with literal parens
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			next := s[i+1]
			switch next {
			case '(':
				out = append(out, '(')
				i++
				continue
			case ')':
				out = append(out, ')')
				i++
				continue
			case '\\':
				out = append(out, '\\')
				i++
				continue
			}
		}
		out = append(out, s[i])
	}
	return string(out)
}

// findFirstUnescaped returns the index of the first occurrence of ch that is
// not preceded by an odd number of backslashes. Returns -1 if not found.
func findFirstUnescaped(s string, ch byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == ch {
			backslashes := 0
			for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
				backslashes++
			}
			if backslashes%2 == 0 {
				return i
			}
		}
	}
	return -1
}

// findLastUnescaped returns the index of the last occurrence of ch that is
// not preceded by an odd number of backslashes. Returns -1 if not found.
func findLastUnescaped(s string, ch byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ch {
			backslashes := 0
			for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
				backslashes++
			}
			if backslashes%2 == 0 {
				return i
			}
		}
	}
	return -1
}

// userSettingsPath returns the path to the user-level settings file (~/.claude/settings.json).
func userSettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".claude", "settings.json")
}
