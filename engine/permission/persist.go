package permission

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/abietic/yhc/engine/config"
)

// SettingsDestination identifies which settings file to write to.
type SettingsDestination string

const (
	DestProjectSettings SettingsDestination = "projectSettings"
	DestUserSettings    SettingsDestination = "userSettings"
	DestLocalSettings   SettingsDestination = "localSettings"
)

// PersistPermissionRules adds permission rules to the specified settings file.
// It performs a read-merge-write cycle, preserving all other keys in the JSON.
// Duplicate rules (after normalization) are skipped.
func PersistPermissionRules(projectDir string, rules []string, action PermissionAction, dest SettingsDestination) error {
	filePath := settingsPathForDest(projectDir, dest)
	return addRulesToSettingsFile(filePath, rules, action)
}

// RemovePermissionRules removes matching permission rules from the specified settings file.
func RemovePermissionRules(projectDir string, rules []string, action PermissionAction, dest SettingsDestination) error {
	filePath := settingsPathForDest(projectDir, dest)
	return removeRulesFromSettingsFile(filePath, rules, action)
}

// FormatRuleString serializes a tool name and input pattern into the rule string
// format used in settings.json, e.g. "Bash(npm *)" or "Read(/home/user/*)".
// This is the inverse of parseRuleValueString in loader.go.
func FormatRuleString(toolName, inputPattern string) string {
	if inputPattern == "" {
		return toolName
	}
	return toolName + "(" + escapeRuleContent(inputPattern) + ")"
}

// BuildRuleFromInvocation constructs a settings-file rule string from a tool
// invocation. For Bash it uses the command prefix; for file tools it uses a
// directory wildcard; for others it falls back to the tool name alone.
func BuildRuleFromInvocation(toolName string, input map[string]any, cwd string) string {
	switch toolName {
	case "Bash":
		cmd, _ := input["command"].(string)
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			return toolName
		}
		// Use the first word + wildcard as the pattern (e.g., "npm *")
		parts := strings.Fields(cmd)
		if len(parts) <= 1 {
			return FormatRuleString(toolName, cmd)
		}
		return FormatRuleString(toolName, parts[0]+" *")
	case "Read", "Write", "Edit":
		path, _ := input["file_path"].(string)
		if path == "" {
			return toolName
		}
		dir := filepath.Dir(resolvePath(cwd, path))
		return FormatRuleString(toolName, dir+"/*")
	case "Grep", "Glob":
		path, _ := input["path"].(string)
		if path == "" {
			path = cwd
		}
		dir := resolvePath(cwd, path)
		return FormatRuleString(toolName, dir+"/*")
	default:
		return toolName
	}
}

// ExactInvocationRule is the lossless existing-schema representation of one
// final permission action. Value is suitable for settings persistence; Rule
// is the same parsed authority used for process-local coalescing.
type ExactInvocationRule struct {
	Value string
	Rule  PermissionRule
}

// BuildExactRuleFromInvocation encodes one invocation without widening its
// authority. It never falls back to a tool-wide or wildcard rule.
func BuildExactRuleFromInvocation(
	toolName string,
	input map[string]any,
	cwd string,
) (ExactInvocationRule, error) {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return ExactInvocationRule{}, fmt.Errorf("cannot persist exact permission rule without a tool name")
	}
	if len(input) == 0 {
		return ExactInvocationRule{}, fmt.Errorf("cannot persist exact permission rule for %s without scoped input", toolName)
	}

	pattern, roundTripInput, err := exactInvocationRulePattern(toolName, input, cwd)
	if err != nil {
		return ExactInvocationRule{}, err
	}
	if pattern == "" {
		return ExactInvocationRule{}, fmt.Errorf("cannot persist exact permission rule for %s without a representable input", toolName)
	}
	value := FormatRuleString(toolName, escapeExactRuleWildcards(pattern))
	rule := parseRuleValue(value, ActionAllow, "")
	decision := NewRulesEngine([]PermissionRule{rule}).EvaluateDecision(
		toolName,
		roundTripInput,
	)
	if !decision.Matched ||
		decision.Action != ActionAllow ||
		!decision.ToolExact ||
		!decision.InputExact {
		return ExactInvocationRule{}, fmt.Errorf("exact permission rule for %s does not round-trip to the same action", toolName)
	}
	return ExactInvocationRule{Value: value, Rule: rule}, nil
}

func exactInvocationRulePattern(
	toolName string,
	input map[string]any,
	cwd string,
) (string, map[string]any, error) {
	roundTripInput := cloneRuleInput(input)
	switch toolName {
	case "Bash":
		command, _ := input["command"].(string)
		command = strings.TrimSpace(command)
		if command == "" {
			return "", nil, fmt.Errorf("cannot persist exact permission rule for Bash without a command")
		}
		roundTripInput["command"] = command
		return command, roundTripInput, nil
	case "Read", "Write", "Edit":
		return exactResolvedPathRule(toolName, "file_path", input, roundTripInput, cwd)
	case "NotebookEdit":
		return exactResolvedPathRule(toolName, "notebook_path", input, roundTripInput, cwd)
	case "Grep", "Glob":
		path, _ := input["path"].(string)
		if strings.TrimSpace(path) == "" {
			path = cwd
		}
		roundTripInput["path"] = path
		return exactResolvedPathRule(toolName, "path", roundTripInput, roundTripInput, cwd)
	default:
		encoded, err := json.Marshal(input)
		if err != nil {
			return "", nil, fmt.Errorf("cannot persist exact permission rule for %s: %w", toolName, err)
		}
		return string(encoded), roundTripInput, nil
	}
}

func exactResolvedPathRule(
	toolName string,
	key string,
	input map[string]any,
	roundTripInput map[string]any,
	cwd string,
) (string, map[string]any, error) {
	rawPath, _ := input[key].(string)
	if strings.TrimSpace(rawPath) == "" {
		return "", nil, fmt.Errorf("cannot persist exact permission rule for %s without %s", toolName, key)
	}
	resolution := ResolvePermissionPath(rawPath, cwd)
	if resolution.Unsafe || resolution.Effective() == "" {
		return "", nil, fmt.Errorf("cannot persist exact permission rule for %s with an unsafe or unresolved path", toolName)
	}
	effective := resolution.Effective()
	roundTripInput[key] = effective
	return effective, roundTripInput, nil
}

func cloneRuleInput(input map[string]any) map[string]any {
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func escapeExactRuleWildcards(pattern string) string {
	var builder strings.Builder
	builder.Grow(len(pattern))
	for index := 0; index < len(pattern); index++ {
		switch pattern[index] {
		case '*', '\\':
			builder.WriteByte('\\')
		}
		builder.WriteByte(pattern[index])
	}
	return builder.String()
}

// addRulesToSettingsFile performs the read-merge-write cycle for adding rules.
func addRulesToSettingsFile(filePath string, rules []string, action PermissionAction) error {
	return config.UpdateSettingsDocument(filePath, func(settings map[string]json.RawMessage) (bool, error) {
		return changePermissionSettings(settings, rules, action, "add")
	})
}

// removeRulesFromSettingsFile performs the read-modify-write cycle for removing rules.
func removeRulesFromSettingsFile(filePath string, rules []string, action PermissionAction) error {
	return config.UpdateSettingsDocument(filePath, func(settings map[string]json.RawMessage) (bool, error) {
		return changePermissionSettings(settings, rules, action, "remove")
	})
}

// strictPermissionSettingsSection validates the structured rule arrays before
// either persistence caller publishes a replacement shared document.
func strictPermissionSettingsSection(fields map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	perms := map[string]json.RawMessage{}
	if raw, ok := fields["permissions"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &perms); err != nil || perms == nil {
			return nil, config.ErrSettingsInvalid
		}
	}
	for _, action := range []PermissionAction{ActionAllow, ActionDeny, ActionAsk} {
		if raw, ok := perms[string(action)]; ok {
			var values []string
			if err := json.Unmarshal(raw, &values); err != nil {
				return nil, config.ErrSettingsInvalid
			}
		}
	}
	return perms, nil
}

// changePermissionSettings keeps add/remove semantics under the shared store's
// document lock, retaining unrelated keys and normalized deduplication.
func changePermissionSettings(fields map[string]json.RawMessage, rules []string, action PermissionAction, operation string) (bool, error) {
	perms, err := strictPermissionSettingsSection(fields)
	if err != nil {
		return false, err
	}
	key := string(action)
	existing := permissionSettingsRules(perms[key])
	normalized := map[string]bool{}
	for _, rule := range rules {
		normalized[normalizeRule(rule)] = true
	}
	changed := false
	if operation == "remove" {
		filtered := make([]string, 0, len(existing))
		for _, rule := range existing {
			if normalized[normalizeRule(rule)] {
				changed = true
			} else {
				filtered = append(filtered, rule)
			}
		}
		existing = filtered
	} else {
		seen := map[string]bool{}
		for _, rule := range existing {
			seen[normalizeRule(rule)] = true
		}
		for _, rule := range rules {
			n := normalizeRule(rule)
			if !seen[n] {
				existing = append(existing, rule)
				seen[n] = true
				changed = true
			}
		}
	}
	if !changed {
		return false, nil
	}
	if len(existing) == 0 {
		delete(perms, key)
	} else {
		perms[key], _ = json.Marshal(existing)
	}
	fields["permissions"], _ = json.Marshal(perms)
	return true, nil
}

func permissionSettingsRules(raw json.RawMessage) []string {
	var rules []any
	_ = json.Unmarshal(raw, &rules)
	return toStringSlice(rules)
}

// settingsPathForDest maps a destination to a file path.
func settingsPathForDest(projectDir string, dest SettingsDestination) string {
	switch dest {
	case DestUserSettings:
		return config.UserConfigPath()
	case DestProjectSettings:
		return config.ProjectConfigPath(projectDir)
	case DestLocalSettings:
		return config.ProjectLocalConfigPath(projectDir)
	default:
		return config.ProjectLocalConfigPath(projectDir)
	}
}

// atomicWriteFile writes data to a file atomically via write-to-tmp + rename.
func atomicWriteFile(filePath string, data []byte) error {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}
	tmpPath := filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming temp file: %w", err)
	}
	return nil
}

// normalizeRule normalizes a rule string for deduplication by round-tripping
// through parse and format.
func normalizeRule(rule string) string {
	toolName, inputPattern := parseRuleValueString(rule)
	return FormatRuleString(toolName, inputPattern)
}

// escapeRuleContent escapes parentheses and backslashes in rule content.
func escapeRuleContent(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			b.WriteString("\\(")
		case ')':
			b.WriteString("\\)")
		case '\\':
			b.WriteString("\\\\")
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// toStringSlice converts an interface{} to []string (from JSON array).
func toStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// resolvePath resolves a possibly-relative path against cwd.
func resolvePath(cwd, path string) string {
	if path == "" {
		return cwd
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}
