package permission

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/abietic/yhc/engine/config"
)

// SettingsDestination identifies which settings file to write to.
type SettingsDestination string

const (
	DestProjectSettings SettingsDestination = "projectSettings"
	DestUserSettings    SettingsDestination = "userSettings"
	DestLocalSettings   SettingsDestination = "localSettings"
)

// settingsPersistenceMu serializes process-local read-modify-write operations
// across every settings destination. Writes are rare and must not lose an
// exact rule when concurrent permission decisions settle in one process.
var settingsPersistenceMu sync.Mutex

// PersistPermissionRules adds permission rules to the specified settings file.
// It performs a read-merge-write cycle, preserving all other keys in the JSON.
// Duplicate rules (after normalization) are skipped.
func PersistPermissionRules(projectDir string, rules []string, action PermissionAction, dest SettingsDestination) error {
	filePath := settingsPathForDest(projectDir, dest)
	settingsPersistenceMu.Lock()
	defer settingsPersistenceMu.Unlock()
	return addRulesToSettingsFile(filePath, rules, action)
}

// RemovePermissionRules removes matching permission rules from the specified settings file.
func RemovePermissionRules(projectDir string, rules []string, action PermissionAction, dest SettingsDestination) error {
	filePath := settingsPathForDest(projectDir, dest)
	settingsPersistenceMu.Lock()
	defer settingsPersistenceMu.Unlock()
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
	settings, err := readSettingsMap(filePath)
	if err != nil {
		return err
	}

	// Get or create the permissions section.
	permsRaw, ok := settings["permissions"]
	if !ok {
		permsRaw = map[string]any{}
	}
	perms, ok := permsRaw.(map[string]any)
	if !ok {
		perms = map[string]any{}
	}

	// Get or create the behavior array (allow/deny/ask).
	key := string(action)
	existing := toStringSlice(perms[key])

	// Normalize existing rules for dedup.
	normalizedExisting := make(map[string]bool, len(existing))
	for _, r := range existing {
		normalizedExisting[normalizeRule(r)] = true
	}

	// Append only new rules.
	added := false
	for _, r := range rules {
		if normalizedExisting[normalizeRule(r)] {
			continue
		}
		existing = append(existing, r)
		added = true
	}
	if !added {
		return nil // all rules already present
	}

	perms[key] = existing
	settings["permissions"] = perms
	return writeSettingsMap(filePath, settings)
}

// removeRulesFromSettingsFile performs the read-modify-write cycle for removing rules.
func removeRulesFromSettingsFile(filePath string, rules []string, action PermissionAction) error {
	settings, err := readSettingsMap(filePath)
	if err != nil {
		return err
	}

	permsRaw, ok := settings["permissions"]
	if !ok {
		return nil // nothing to remove
	}
	perms, ok := permsRaw.(map[string]any)
	if !ok {
		return nil
	}

	key := string(action)
	existing := toStringSlice(perms[key])
	if len(existing) == 0 {
		return nil
	}

	// Build set of rules to remove (normalized).
	removeSet := make(map[string]bool, len(rules))
	for _, r := range rules {
		removeSet[normalizeRule(r)] = true
	}

	// Filter out matching rules.
	filtered := make([]string, 0, len(existing))
	for _, r := range existing {
		if !removeSet[normalizeRule(r)] {
			filtered = append(filtered, r)
		}
	}
	if len(filtered) == len(existing) {
		return nil // nothing changed
	}

	if len(filtered) == 0 {
		delete(perms, key)
	} else {
		perms[key] = filtered
	}
	settings["permissions"] = perms
	return writeSettingsMap(filePath, settings)
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

// readSettingsMap reads a settings JSON file into a generic map.
// Returns an empty map if the file does not exist.
func readSettingsMap(filePath string) (map[string]any, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("reading settings: %w", err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		// Lenient: start fresh if JSON is malformed.
		return map[string]any{}, nil
	}
	return result, nil
}

// writeSettingsMap writes a generic map to a JSON file atomically.
func writeSettingsMap(filePath string, settings map[string]any) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling settings: %w", err)
	}
	data = append(data, '\n')
	return atomicWriteFile(filePath, data)
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
