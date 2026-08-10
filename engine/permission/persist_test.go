package permission

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPersistRulesToNewFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, ".claude", "settings.local.json")

	err := addRulesToSettingsFile(filePath, []string{"Bash(npm *)", "Read"}, ActionAllow)
	if err != nil {
		t.Fatalf("addRulesToSettingsFile: %v", err)
		return
	}

	// Verify file contents.
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
		return
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("Unmarshal: %v", err)
		return
	}

	perms, ok := settings["permissions"].(map[string]any)
	if !ok {
		t.Fatal("permissions section missing")
	}
	allow := toStringSlice(perms["allow"])
	if len(allow) != 2 {
		t.Fatalf("expected 2 allow rules, got %d: %v", len(allow), allow)
	}
	if allow[0] != "Bash(npm *)" || allow[1] != "Read" {
		t.Fatalf("unexpected rules: %v", allow)
	}
}

func TestPersistRulesToExistingFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "settings.json")

	// Pre-create file with other keys.
	existing := map[string]any{
		"model": "claude-3-opus",
		"permissions": map[string]any{
			"allow": []any{"Read"},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	_ = os.MkdirAll(filepath.Dir(filePath), 0o755)
	_ = os.WriteFile(filePath, data, 0o644)

	err := addRulesToSettingsFile(filePath, []string{"Bash(go test *)"}, ActionAllow)
	if err != nil {
		t.Fatalf("addRulesToSettingsFile: %v", err)
		return
	}

	// Read back and verify.
	data, _ = os.ReadFile(filePath)
	var settings map[string]any
	_ = json.Unmarshal(data, &settings)

	// Other keys preserved.
	if settings["model"] != "claude-3-opus" {
		t.Fatal("model key lost")
	}

	perms := settings["permissions"].(map[string]any)
	allow := toStringSlice(perms["allow"])
	if len(allow) != 2 {
		t.Fatalf("expected 2 allow rules, got %d: %v", len(allow), allow)
	}
	if allow[0] != "Read" || allow[1] != "Bash(go test *)" {
		t.Fatalf("unexpected rules: %v", allow)
	}
}

func TestPersistPermissionRulesConcurrentMergeDoesNotLoseExactRules(
	t *testing.T,
) {
	projectDir := t.TempDir()
	const ruleCount = 32
	var waitGroup sync.WaitGroup
	errors := make(chan error, ruleCount)

	for index := range ruleCount {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			rule := fmt.Sprintf("Bash(echo exact-%02d)", index)
			if err := PersistPermissionRules(
				projectDir,
				[]string{rule},
				ActionAllow,
				DestLocalSettings,
			); err != nil {
				errors <- err
			}
		}()
	}
	waitGroup.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}

	rules, err := loadRulesFromFile(
		filepath.Join(projectDir, ".claude", "settings.local.json"),
		SourceLocal,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != ruleCount {
		t.Fatalf(
			"persisted rules = %d, want %d: %#v",
			len(rules),
			ruleCount,
			rules,
		)
	}
	seen := make(map[string]bool, ruleCount)
	for _, rule := range rules {
		seen[rule.InputPattern] = true
	}
	for index := range ruleCount {
		pattern := fmt.Sprintf("echo exact-%02d", index)
		if !seen[pattern] {
			t.Fatalf("missing concurrently persisted exact rule %q", pattern)
		}
	}
}

func TestPersistDuplicateRule(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "settings.json")

	// Add a rule.
	err := addRulesToSettingsFile(filePath, []string{"Bash(npm *)"}, ActionAllow)
	if err != nil {
		t.Fatal(err)
		return
	}

	// Add the same rule again.
	err = addRulesToSettingsFile(filePath, []string{"Bash(npm *)"}, ActionAllow)
	if err != nil {
		t.Fatal(err)
		return
	}

	// Should still have only one.
	data, _ := os.ReadFile(filePath)
	var settings map[string]any
	_ = json.Unmarshal(data, &settings)
	perms := settings["permissions"].(map[string]any)
	allow := toStringSlice(perms["allow"])
	if len(allow) != 1 {
		t.Fatalf("expected 1 allow rule after dedup, got %d: %v", len(allow), allow)
	}
}

func TestRemoveRules(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "settings.json")

	// Seed with rules.
	err := addRulesToSettingsFile(filePath, []string{"Bash(npm *)", "Read", "Edit"}, ActionAllow)
	if err != nil {
		t.Fatal(err)
		return
	}

	// Remove one rule.
	err = removeRulesFromSettingsFile(filePath, []string{"Read"}, ActionAllow)
	if err != nil {
		t.Fatal(err)
		return
	}

	data, _ := os.ReadFile(filePath)
	var settings map[string]any
	_ = json.Unmarshal(data, &settings)
	perms := settings["permissions"].(map[string]any)
	allow := toStringSlice(perms["allow"])
	if len(allow) != 2 {
		t.Fatalf("expected 2 allow rules after removal, got %d: %v", len(allow), allow)
	}
	for _, r := range allow {
		if r == "Read" {
			t.Fatal("Read rule should have been removed")
		}
	}
}

func TestFormatRuleString(t *testing.T) {
	cases := []struct {
		tool, pattern, want string
	}{
		{"Read", "", "Read"},
		{"Bash", "npm *", "Bash(npm *)"},
		{"Edit", "/home/user/project/*", "Edit(/home/user/project/*)"},
		{"Bash", "echo \\(hello\\)", "Bash(echo \\\\\\(hello\\\\\\))"},
	}
	for _, tc := range cases {
		got := FormatRuleString(tc.tool, tc.pattern)
		if got != tc.want {
			t.Errorf("FormatRuleString(%q, %q) = %q, want %q", tc.tool, tc.pattern, got, tc.want)
		}
	}
}

func TestFormatRuleRoundTrip(t *testing.T) {
	cases := []struct {
		rule string
	}{
		{"Bash(npm *)"},
		{"Read"},
		{"Edit(/home/user/project/*)"},
	}
	for _, tc := range cases {
		toolName, inputPattern := parseRuleValueString(tc.rule)
		got := FormatRuleString(toolName, inputPattern)
		if got != tc.rule {
			t.Errorf("round-trip failed: %q → (%q, %q) → %q", tc.rule, toolName, inputPattern, got)
		}
	}
}

func TestBuildRuleFromInvocation(t *testing.T) {
	cwd := "/home/user/project"

	cases := []struct {
		name  string
		tool  string
		input map[string]any
		want  string
	}{
		{
			name:  "bash single command",
			tool:  "Bash",
			input: map[string]any{"command": "npm install"},
			want:  "Bash(npm *)",
		},
		{
			name:  "bash single word",
			tool:  "Bash",
			input: map[string]any{"command": "ls"},
			want:  "Bash(ls)",
		},
		{
			name:  "read file",
			tool:  "Read",
			input: map[string]any{"file_path": "/home/user/project/src/main.go"},
			want:  "Read(/home/user/project/src/*)",
		},
		{
			name:  "edit file",
			tool:  "Edit",
			input: map[string]any{"file_path": "src/main.go"},
			want:  "Edit(/home/user/project/src/*)",
		},
		{
			name:  "unknown tool",
			tool:  "WebFetch",
			input: map[string]any{"url": "https://example.com"},
			want:  "WebFetch",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildRuleFromInvocation(tc.tool, tc.input, cwd)
			if got != tc.want {
				t.Errorf("BuildRuleFromInvocation(%q, %v, %q) = %q, want %q",
					tc.tool, tc.input, cwd, got, tc.want)
			}
		})
	}
}

func TestBuildExactRuleFromInvocation(t *testing.T) {
	cwd := t.TempDir()
	cases := []struct {
		name   string
		tool   string
		input  map[string]any
		mutate func(map[string]any)
	}{
		{
			name:  "full bash command with metacharacters",
			tool:  "Bash",
			input: map[string]any{"command": `printf '*?(value)\\:'`},
			mutate: func(input map[string]any) {
				input["command"] = `printf '*?(other)\\:'`
			},
		},
		{
			name:  "resolved file path with metacharacters",
			tool:  "Read",
			input: map[string]any{"file_path": `dir/(a)*?.txt`},
			mutate: func(input map[string]any) {
				input["file_path"] = `dir/(b)*?.txt`
			},
		},
		{
			name:  "search defaults to exact cwd",
			tool:  "Grep",
			input: map[string]any{"pattern": "needle"},
			mutate: func(input map[string]any) {
				input["path"] = filepath.Dir(cwd)
			},
		},
		{
			name: "generic canonical json",
			tool: "Config",
			input: map[string]any{
				"operation": "set",
				"value":     map[string]any{"pattern": `a*(b)\\c`},
			},
			mutate: func(input map[string]any) {
				input["operation"] = "get"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exact, err := BuildExactRuleFromInvocation(tc.tool, tc.input, cwd)
			if err != nil {
				t.Fatalf("BuildExactRuleFromInvocation: %v", err)
			}
			if exact.Value == tc.tool || exact.Rule.InputPattern == "" {
				t.Fatalf("exact rule widened to tool scope: %#v", exact)
			}
			if hasUnescapedWildcards(exact.Rule.InputPattern) {
				t.Fatalf("exact rule contains an unescaped wildcard: %q", exact.Rule.InputPattern)
			}
			loaded := parseRuleValue(exact.Value, ActionAllow, SourceLocal)
			if loaded.ToolName != exact.Rule.ToolName ||
				loaded.InputPattern != exact.Rule.InputPattern ||
				loaded.Action != exact.Rule.Action {
				t.Fatalf("settings round trip changed rule: got %#v want %#v", loaded, exact.Rule)
			}

			changed := cloneRuleInput(tc.input)
			tc.mutate(changed)
			if NewRulesEngine([]PermissionRule{exact.Rule}).EvaluateDecision(tc.tool, changed).Matched {
				t.Fatalf("exact rule unexpectedly matched changed input: value=%q changed=%v", exact.Value, changed)
			}
		})
	}
}

func TestBuildExactRuleFromInvocationRejectsUnscopedInput(t *testing.T) {
	cases := []struct {
		name  string
		tool  string
		input map[string]any
	}{
		{name: "empty generic input", tool: "Config", input: map[string]any{}},
		{name: "empty command", tool: "Bash", input: map[string]any{"command": "  "}},
		{name: "missing file path", tool: "Write", input: map[string]any{"content": "value"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := BuildExactRuleFromInvocation(tc.tool, tc.input, t.TempDir()); err == nil {
				t.Fatal("expected stable unrepresentable-input error")
			}
		})
	}
}
