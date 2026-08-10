package permission

import "testing"

func TestModeDefaultBehavior(t *testing.T) {
	tests := []struct {
		mode Mode
		want PermissionAction
	}{
		{ModeDefault, ActionAsk},
		{ModePlan, ActionDeny},
		{ModeAcceptEdits, ActionAsk},
		{ModeBypassPermissions, ActionAllow},
		{ModeDontAsk, ActionDeny},
		{ModeAuto, ActionAsk},
		{ModeBubble, ActionAsk},
		{Mode("unknown"), ActionAsk},
	}
	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			got := tt.mode.DefaultBehavior()
			if got != tt.want {
				t.Fatalf("Mode(%q).DefaultBehavior() = %q, want %q", tt.mode, got, tt.want)
			}
		})
	}
}

func TestModeAllowsToolByDefault(t *testing.T) {
	if !ModeBypassPermissions.AllowsToolByDefault() {
		t.Fatal("bypassPermissions should allow by default")
	}
	for _, m := range []Mode{ModeDefault, ModePlan, ModeAcceptEdits, ModeDontAsk, ModeAuto} {
		if m.AllowsToolByDefault() {
			t.Fatalf("mode %q should not allow by default", m)
		}
	}
}

func TestModeDeniesToolByDefault(t *testing.T) {
	if !ModePlan.DeniesToolByDefault() {
		t.Fatal("plan should deny by default")
	}
	if !ModeDontAsk.DeniesToolByDefault() {
		t.Fatal("dontAsk should deny by default")
	}
	for _, m := range []Mode{ModeDefault, ModeBypassPermissions, ModeAcceptEdits, ModeAuto} {
		if m.DeniesToolByDefault() {
			t.Fatalf("mode %q should not deny by default", m)
		}
	}
}

func TestEvaluateWithModeNoRuleMatch(t *testing.T) {
	// No rule matched → mode's default behavior applies.
	tests := []struct {
		mode Mode
		want PermissionAction
	}{
		{ModeDefault, ActionAsk},
		{ModeBypassPermissions, ActionAllow},
		{ModePlan, ActionDeny},
		{ModeDontAsk, ActionDeny},
	}
	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			got := EvaluateWithMode(tt.mode, ActionAsk, false)
			if got != tt.want {
				t.Fatalf("EvaluateWithMode(%q, _, false) = %q, want %q", tt.mode, got, tt.want)
			}
		})
	}
}

func TestEvaluateWithModeDontAskConvertsAskToDeny(t *testing.T) {
	// In dontAsk mode, "ask" decisions become "deny".
	got := EvaluateWithMode(ModeDontAsk, ActionAsk, true)
	if got != ActionDeny {
		t.Fatalf("expected dontAsk to convert ask→deny, got %q", got)
	}
}

func TestEvaluateWithModeDontAskPreservesDeny(t *testing.T) {
	// Deny rules are preserved in dontAsk mode.
	got := EvaluateWithMode(ModeDontAsk, ActionDeny, true)
	if got != ActionDeny {
		t.Fatalf("expected deny to remain deny in dontAsk, got %q", got)
	}
}

func TestEvaluateWithModeDontAskPreservesAllow(t *testing.T) {
	// Allow rules are respected even in dontAsk mode.
	got := EvaluateWithMode(ModeDontAsk, ActionAllow, true)
	if got != ActionAllow {
		t.Fatalf("expected allow to remain allow in dontAsk, got %q", got)
	}
}

func TestEvaluateWithModeBypassConvertsAskToAllow(t *testing.T) {
	// In bypassPermissions mode, "ask" becomes "allow".
	got := EvaluateWithMode(ModeBypassPermissions, ActionAsk, true)
	if got != ActionAllow {
		t.Fatalf("expected bypass to convert ask→allow, got %q", got)
	}
}

func TestEvaluateWithModeBypassPreservesDeny(t *testing.T) {
	// Deny rules are still respected in bypassPermissions mode.
	got := EvaluateWithMode(ModeBypassPermissions, ActionDeny, true)
	if got != ActionDeny {
		t.Fatalf("expected deny to remain deny in bypass, got %q", got)
	}
}

func TestEvaluateWithModeDefaultPreservesRuleAction(t *testing.T) {
	// In default mode, rule actions pass through unchanged.
	for _, action := range []PermissionAction{ActionAllow, ActionDeny, ActionAsk} {
		got := EvaluateWithMode(ModeDefault, action, true)
		if got != action {
			t.Fatalf("expected default mode to preserve %q, got %q", action, got)
		}
	}
}

func TestModeTitle(t *testing.T) {
	tests := []struct {
		mode Mode
		want string
	}{
		{ModeDefault, "Default"},
		{ModePlan, "Plan Mode"},
		{ModeAcceptEdits, "Accept Edits"},
		{ModeBypassPermissions, "Bypass Permissions"},
		{ModeDontAsk, "Don't Ask"},
		{ModeAuto, "Auto Mode"},
		{ModeBubble, "Bubble"},
	}
	for _, tt := range tests {
		got := tt.mode.ModeTitle()
		if got != tt.want {
			t.Fatalf("Mode(%q).ModeTitle() = %q, want %q", tt.mode, got, tt.want)
		}
	}
}

func TestParseMode(t *testing.T) {
	tests := []struct {
		input string
		want  Mode
	}{
		{"default", ModeDefault},
		{"plan", ModePlan},
		{"acceptEdits", ModeAcceptEdits},
		{"bypassPermissions", ModeBypassPermissions},
		{"dontAsk", ModeDontAsk},
		{"auto", ModeAuto},
		{"bubble", ModeBubble},
		{"invalid", ModeDefault},
		{"", ModeDefault},
	}
	for _, tt := range tests {
		got := ParseMode(tt.input)
		if got != tt.want {
			t.Fatalf("ParseMode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestValidModes(t *testing.T) {
	modes := ValidModes()
	if len(modes) != 5 {
		t.Fatalf("expected 5 valid modes, got %d", len(modes))
	}
	// Should not include internal modes.
	for _, m := range modes {
		if m == ModeAuto || m == ModeBubble {
			t.Fatalf("internal mode %q should not be in ValidModes", m)
		}
	}
}

func TestModeRuleInteractionEndToEnd(t *testing.T) {
	// End-to-end test: rules + mode interaction.
	rules := []PermissionRule{
		{ToolName: "Bash", InputPattern: "git*", Action: ActionAllow, Source: "project"},
		{ToolName: "Bash", InputPattern: "rm*", Action: ActionDeny, Source: "project"},
	}
	engine := NewRulesEngine(rules)

	// In dontAsk mode:
	// "git push" matches allow rule → allow (dontAsk doesn't affect allow)
	action := engine.Evaluate("Bash", map[string]any{"command": "git push"})
	got := EvaluateWithMode(ModeDontAsk, action, true)
	if got != ActionAllow {
		t.Fatalf("git push in dontAsk: expected allow, got %q", got)
	}

	// "rm -rf" matches deny rule → deny (unchanged by dontAsk)
	action = engine.Evaluate("Bash", map[string]any{"command": "rm -rf /"})
	got = EvaluateWithMode(ModeDontAsk, action, true)
	if got != ActionDeny {
		t.Fatalf("rm in dontAsk: expected deny, got %q", got)
	}

	// "python script.py" matches no rule → dontAsk mode default = deny
	action, matched := engine.EvaluateMatch("Bash", map[string]any{"command": "python script.py"})
	got = EvaluateWithMode(ModeDontAsk, action, matched)
	if got != ActionDeny {
		t.Fatalf("unmatched in dontAsk: expected deny, got %q", got)
	}

	// In bypassPermissions mode:
	// "python script.py" no rule → bypass default = allow
	action, matched = engine.EvaluateMatch("Bash", map[string]any{"command": "python script.py"})
	got = EvaluateWithMode(ModeBypassPermissions, action, matched)
	if got != ActionAllow {
		t.Fatalf("unmatched in bypass: expected allow, got %q", got)
	}

	// "rm -rf" matches deny rule → deny still wins in bypass
	action = engine.Evaluate("Bash", map[string]any{"command": "rm -rf /"})
	got = EvaluateWithMode(ModeBypassPermissions, action, true)
	if got != ActionDeny {
		t.Fatalf("rm in bypass: expected deny, got %q", got)
	}
}
