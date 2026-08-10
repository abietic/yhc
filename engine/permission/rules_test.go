package permission

import (
	"testing"
)

func TestRulesEvaluateBasicAllow(t *testing.T) {
	engine := NewRulesEngine([]PermissionRule{
		{ToolName: "Read", Action: ActionAllow, Source: "test"},
	})

	got := engine.Evaluate("Read", map[string]any{"file_path": "/tmp/foo.go"})
	if got != ActionAllow {
		t.Fatalf("expected ActionAllow, got %q", got)
	}
}

func TestRulesEvaluateBasicDeny(t *testing.T) {
	engine := NewRulesEngine([]PermissionRule{
		{ToolName: "Bash", InputPattern: "rm*", Action: ActionDeny, Source: "test"},
	})

	got := engine.Evaluate("Bash", map[string]any{"command": "rm -rf /"})
	if got != ActionDeny {
		t.Fatalf("expected ActionDeny, got %q", got)
	}
}

func TestRulesEvaluateBasicAsk(t *testing.T) {
	engine := NewRulesEngine([]PermissionRule{
		{ToolName: "Bash", Action: ActionAsk, Source: "test"},
	})

	got := engine.Evaluate("Bash", map[string]any{"command": "ls"})
	if got != ActionAsk {
		t.Fatalf("expected ActionAsk, got %q", got)
	}
}

func TestRulesEvaluateWildcardToolMatch(t *testing.T) {
	engine := NewRulesEngine([]PermissionRule{
		{ToolName: "*", Action: ActionAsk, Source: "test"},
	})

	// Wildcard should match any tool
	for _, tool := range []string{"Bash", "Read", "Edit", "Write", "CustomTool"} {
		got := engine.Evaluate(tool, nil)
		if got != ActionAsk {
			t.Fatalf("expected ActionAsk for tool %q, got %q", tool, got)
		}
	}
}

func TestRulesEvaluateInputPatternBash(t *testing.T) {
	engine := NewRulesEngine([]PermissionRule{
		{ToolName: "Bash", InputPattern: "git*", Action: ActionAllow, Source: "test"},
		{ToolName: "Bash", InputPattern: "rm*", Action: ActionDeny, Source: "test"},
	})

	// "git push" should match "git*" → allow
	got := engine.Evaluate("Bash", map[string]any{"command": "git push"})
	if got != ActionAllow {
		t.Fatalf("expected ActionAllow for 'git push', got %q", got)
	}

	// "rm -rf /" should match "rm*" → deny
	got = engine.Evaluate("Bash", map[string]any{"command": "rm -rf /"})
	if got != ActionDeny {
		t.Fatalf("expected ActionDeny for 'rm -rf /', got %q", got)
	}

	// "python script.py" matches neither → default ask
	got = engine.Evaluate("Bash", map[string]any{"command": "python script.py"})
	if got != ActionAsk {
		t.Fatalf("expected ActionAsk for 'python script.py', got %q", got)
	}
}

func TestRulesEvaluateInputPatternFilePath(t *testing.T) {
	engine := NewRulesEngine([]PermissionRule{
		{ToolName: "Edit", InputPattern: "/etc/*", Action: ActionAsk, Source: "test"},
		{ToolName: "Edit", InputPattern: "/home/user/project/*", Action: ActionAllow, Source: "test"},
	})

	got := engine.Evaluate("Edit", map[string]any{"file_path": "/etc/passwd"})
	if got != ActionAsk {
		t.Fatalf("expected ActionAsk for /etc/passwd, got %q", got)
	}

	got = engine.Evaluate("Edit", map[string]any{"file_path": "/home/user/project/main.go"})
	if got != ActionAllow {
		t.Fatalf("expected ActionAllow for project file, got %q", got)
	}
}

func TestRulesEvaluatePriorityDenyOverAskOverAllow(t *testing.T) {
	// All three actions match for the same tool — deny should win
	engine := NewRulesEngine([]PermissionRule{
		{ToolName: "Bash", Action: ActionAllow, Source: "allow-rule"},
		{ToolName: "Bash", Action: ActionAsk, Source: "ask-rule"},
		{ToolName: "Bash", Action: ActionDeny, Source: "deny-rule"},
	})

	got := engine.Evaluate("Bash", map[string]any{"command": "echo hello"})
	if got != ActionDeny {
		t.Fatalf("expected ActionDeny (highest priority), got %q", got)
	}
}

func TestRulesEvaluatePriorityAskOverAllow(t *testing.T) {
	engine := NewRulesEngine([]PermissionRule{
		{ToolName: "Bash", Action: ActionAllow, Source: "allow-rule"},
		{ToolName: "Bash", Action: ActionAsk, Source: "ask-rule"},
	})

	got := engine.Evaluate("Bash", map[string]any{"command": "echo hello"})
	if got != ActionAsk {
		t.Fatalf("expected ActionAsk (higher priority than allow), got %q", got)
	}
}

func TestRulesEvaluateNoRulesDefaultsToAsk(t *testing.T) {
	engine := NewRulesEngine(nil)
	got := engine.Evaluate("Bash", map[string]any{"command": "ls"})
	if got != ActionAsk {
		t.Fatalf("expected ActionAsk (default when no rules), got %q", got)
	}
}

func TestRulesEvaluateNoMatchDefaultsToAsk(t *testing.T) {
	engine := NewRulesEngine([]PermissionRule{
		{ToolName: "Read", Action: ActionAllow, Source: "test"},
	})

	// "Bash" doesn't match "Read" rule
	got := engine.Evaluate("Bash", map[string]any{"command": "ls"})
	if got != ActionAsk {
		t.Fatalf("expected ActionAsk (no matching rules), got %q", got)
	}
}

func TestRulesEvaluateNilEngine(t *testing.T) {
	var engine *RulesEngine
	got := engine.Evaluate("Bash", nil)
	if got != ActionAsk {
		t.Fatalf("expected ActionAsk for nil engine, got %q", got)
	}
}

func TestRulesParseRuleStringBasic(t *testing.T) {
	tests := []struct {
		input       string
		wantTool    string
		wantPattern string
		wantAction  PermissionAction
	}{
		{"Bash(rm*):deny", "Bash", "rm*", ActionDeny},
		{"Edit(/etc/*):ask", "Edit", "/etc/*", ActionAsk},
		{"Read:allow", "Read", "", ActionAllow},
		{"*:ask", "*", "", ActionAsk},
		{"Bash(git push):allow", "Bash", "git push", ActionAllow},
		{"Write(/tmp/test.txt):deny", "Write", "/tmp/test.txt", ActionDeny},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			rule, err := ParseRuleString(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
				return
			}
			if rule.ToolName != tt.wantTool {
				t.Errorf("ToolName = %q, want %q", rule.ToolName, tt.wantTool)
			}
			if rule.InputPattern != tt.wantPattern {
				t.Errorf("InputPattern = %q, want %q", rule.InputPattern, tt.wantPattern)
			}
			if rule.Action != tt.wantAction {
				t.Errorf("Action = %q, want %q", rule.Action, tt.wantAction)
			}
		})
	}
}

func TestRulesParseRuleStringErrors(t *testing.T) {
	tests := []struct {
		input string
	}{
		{""},
		{"noaction"},
		{"Bash:invalid"},
		{":allow"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			_, err := ParseRuleString(tt.input)
			if err == nil {
				t.Fatal("expected error, got nil")
				return
			}
		})
	}
}

func TestRulesParseAndEvaluateIntegration(t *testing.T) {
	ruleStrs := []string{
		"Bash(rm*):deny",
		"Bash(git*):allow",
		"Edit(/etc/*):ask",
		"Read:allow",
		"*:ask",
	}

	var rules []PermissionRule
	for _, s := range ruleStrs {
		rule, err := ParseRuleString(s)
		if err != nil {
			t.Fatalf("failed to parse %q: %v", s, err)
			return
		}
		rules = append(rules, rule)
	}

	engine := NewRulesEngine(rules)

	// Bash "rm -rf" matches "Bash(rm*):deny" and "*:ask" → deny wins
	got := engine.Evaluate("Bash", map[string]any{"command": "rm -rf /"})
	if got != ActionDeny {
		t.Fatalf("expected deny for rm command, got %q", got)
	}

	// Bash "git status" matches "Bash(git*):allow" and "*:ask" → ask wins (higher priority)
	got = engine.Evaluate("Bash", map[string]any{"command": "git status"})
	if got != ActionAsk {
		t.Fatalf("expected ask for git command (ask > allow), got %q", got)
	}

	// Read matches "Read:allow" and "*:ask" → ask wins
	got = engine.Evaluate("Read", map[string]any{"file_path": "/tmp/foo"})
	if got != ActionAsk {
		t.Fatalf("expected ask for Read (ask > allow from wildcard), got %q", got)
	}

	// Edit /etc/passwd matches "Edit(/etc/*):ask" and "*:ask" → ask
	got = engine.Evaluate("Edit", map[string]any{"file_path": "/etc/passwd"})
	if got != ActionAsk {
		t.Fatalf("expected ask for Edit /etc/passwd, got %q", got)
	}
}

func TestRulesMatchPatternExact(t *testing.T) {
	engine := NewRulesEngine([]PermissionRule{
		{ToolName: "Bash", InputPattern: "make test", Action: ActionAllow, Source: "test"},
	})

	got := engine.Evaluate("Bash", map[string]any{"command": "make test"})
	if got != ActionAllow {
		t.Fatalf("expected ActionAllow for exact match, got %q", got)
	}

	got = engine.Evaluate("Bash", map[string]any{"command": "make build"})
	if got != ActionAsk {
		t.Fatalf("expected ActionAsk for non-match, got %q", got)
	}
}

func TestRulesMatchPatternGlob(t *testing.T) {
	engine := NewRulesEngine([]PermissionRule{
		{ToolName: "Read", InputPattern: "/home/user/*.go", Action: ActionAllow, Source: "test"},
	})

	got := engine.Evaluate("Read", map[string]any{"file_path": "/home/user/main.go"})
	if got != ActionAllow {
		t.Fatalf("expected ActionAllow for glob match, got %q", got)
	}

	got = engine.Evaluate("Read", map[string]any{"file_path": "/home/user/main.py"})
	if got != ActionAsk {
		t.Fatalf("expected ActionAsk for glob non-match, got %q", got)
	}
}

func TestRulesEvaluatePathField(t *testing.T) {
	engine := NewRulesEngine([]PermissionRule{
		{ToolName: "Read", InputPattern: "/safe/*", Action: ActionAllow, Source: "test"},
	})

	// Should match "path" field as fallback for file tools
	got := engine.Evaluate("Read", map[string]any{"path": "/safe/file.txt"})
	if got != ActionAllow {
		t.Fatalf("expected ActionAllow for path field match, got %q", got)
	}
}

func TestRulesMatchPatternDoubleStarPath(t *testing.T) {
	engine := NewRulesEngine([]PermissionRule{
		{ToolName: "Write", InputPattern: "*/eino-agent/**", Action: ActionAllow, Source: "test"},
	})

	// Should match deep paths with ** wildcard
	got := engine.Evaluate("Write", map[string]any{"file_path": "/home/user/eino-agent/internal/tui/app.go"})
	if got != ActionAllow {
		t.Fatalf("expected ActionAllow for deep path with ** pattern, got %q", got)
	}

	// Should match single-level path too
	got = engine.Evaluate("Write", map[string]any{"file_path": "/workspace/user/eino-agent/main.go"})
	if got != ActionAllow {
		t.Fatalf("expected ActionAllow for single-level path with ** pattern, got %q", got)
	}

	// Should NOT match paths that don't contain eino-agent
	got = engine.Evaluate("Write", map[string]any{"file_path": "/home/user/other-project/main.go"})
	if got != ActionAsk {
		t.Fatalf("expected ActionAsk for non-matching path, got %q", got)
	}
}

func TestRulesMatchPatternSingleStarMatchesSlash(t *testing.T) {
	engine := NewRulesEngine([]PermissionRule{
		{ToolName: "Edit", InputPattern: "/home/user/project/*", Action: ActionAllow, Source: "test"},
	})

	// With wildcard semantics, * matches any characters including /
	got := engine.Evaluate("Edit", map[string]any{"file_path": "/home/user/project/subdir/deep/file.go"})
	if got != ActionAllow {
		t.Fatalf("expected ActionAllow for deep path with single * pattern, got %q", got)
	}
}

func TestRulesMatchPatternBashSpaceWildcard(t *testing.T) {
	engine := NewRulesEngine([]PermissionRule{
		{ToolName: "Bash", InputPattern: "go *", Action: ActionAllow, Source: "test"},
	})

	// "go *" should match "go test ./..."
	got := engine.Evaluate("Bash", map[string]any{"command": "go test ./..."})
	if got != ActionAllow {
		t.Fatalf("expected ActionAllow for 'go test ./...', got %q", got)
	}

	// "go *" should also match bare "go" (trailing space+wildcard optional)
	got = engine.Evaluate("Bash", map[string]any{"command": "go"})
	if got != ActionAllow {
		t.Fatalf("expected ActionAllow for bare 'go', got %q", got)
	}

	// Should not match "gopher"
	got = engine.Evaluate("Bash", map[string]any{"command": "gopher"})
	if got != ActionAsk {
		t.Fatalf("expected ActionAsk for 'gopher', got %q", got)
	}
}

func TestRulesMatchPatternEscapedStar(t *testing.T) {
	engine := NewRulesEngine([]PermissionRule{
		{ToolName: "Bash", InputPattern: `echo \*`, Action: ActionAllow, Source: "test"},
	})

	// Should match literal "echo *"
	got := engine.Evaluate("Bash", map[string]any{"command": "echo *"})
	if got != ActionAllow {
		t.Fatalf("expected ActionAllow for literal star match, got %q", got)
	}

	// Should NOT match "echo hello"
	got = engine.Evaluate("Bash", map[string]any{"command": "echo hello"})
	if got != ActionAsk {
		t.Fatalf("expected ActionAsk for non-match, got %q", got)
	}
}

func TestRulesEngineIsToolBlanketDenied(t *testing.T) {
	engine := NewRulesEngine([]PermissionRule{
		{ToolName: "Write", Action: ActionDeny, Source: "test"},
		{ToolName: "Bash", InputPattern: "rm *", Action: ActionDeny, Source: "test"},
		{ToolName: "mcp__github", Action: ActionDeny, Source: "test"},
		{ToolName: "Read", Action: ActionAllow, Source: "test"},
	})

	for _, name := range []string{"Write", "mcp__github__create_issue"} {
		if !engine.IsToolBlanketDenied(name) {
			t.Errorf("expected %q to be blanket denied", name)
		}
	}
	for _, name := range []string{"Bash", "Read", "mcp__gitlab__create_issue"} {
		if engine.IsToolBlanketDenied(name) {
			t.Errorf("did not expect %q to be blanket denied", name)
		}
	}
}
