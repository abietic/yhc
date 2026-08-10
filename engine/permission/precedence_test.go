package permission

import (
	"testing"
)

func TestResolvePrecedenceEmpty(t *testing.T) {
	action, matched := ResolvePrecedence(nil)
	if action != ActionAsk {
		t.Fatalf("expected ActionAsk for empty matches, got %q", action)
	}
	if matched {
		t.Fatal("expected matched=false for empty matches")
	}
}

func TestResolvePrecedenceDenyWins(t *testing.T) {
	matches := []RuleMatch{
		{Rule: PermissionRule{ToolName: "Bash", Action: ActionAllow}, ToolExact: true},
		{Rule: PermissionRule{ToolName: "Bash", Action: ActionDeny}, ToolExact: true},
		{Rule: PermissionRule{ToolName: "Bash", Action: ActionAsk}, ToolExact: true},
	}
	action, matched := ResolvePrecedence(matches)
	if action != ActionDeny {
		t.Fatalf("expected ActionDeny, got %q", action)
	}
	if !matched {
		t.Fatal("expected matched=true")
	}
}

func TestResolvePrecedenceAskBeatsAllow(t *testing.T) {
	matches := []RuleMatch{
		{Rule: PermissionRule{ToolName: "Bash", Action: ActionAllow}, ToolExact: true},
		{Rule: PermissionRule{ToolName: "Bash", Action: ActionAsk}, ToolExact: true},
	}
	action, matched := ResolvePrecedence(matches)
	if action != ActionAsk {
		t.Fatalf("expected ActionAsk, got %q", action)
	}
	if !matched {
		t.Fatal("expected matched=true")
	}
}

func TestResolvePrecedenceAllowOnly(t *testing.T) {
	matches := []RuleMatch{
		{Rule: PermissionRule{ToolName: "Read", Action: ActionAllow}, ToolExact: true},
	}
	action, matched := ResolvePrecedence(matches)
	if action != ActionAllow {
		t.Fatalf("expected ActionAllow, got %q", action)
	}
	if !matched {
		t.Fatal("expected matched=true")
	}
}

func TestResolvePrecedenceWithWinnerSpecificity(t *testing.T) {
	// Two allow rules — one specific (with input pattern), one broad (tool-wide).
	// The specific one should win.
	matches := []RuleMatch{
		{
			Rule:        PermissionRule{ToolName: "Bash", Action: ActionAllow, Source: "broad"},
			ToolExact:   true,
			Specificity: 0,
		},
		{
			Rule:        PermissionRule{ToolName: "Bash", InputPattern: "/home/user/project/*", Action: ActionAllow, Source: "specific"},
			ToolExact:   true,
			InputExact:  false,
			Specificity: 40, // computed from pattern length + path depth
		},
	}
	_, winner, matched := ResolvePrecedenceWithWinner(matches)
	if !matched {
		t.Fatal("expected matched=true")
	}
	if winner == nil {
		t.Fatal("expected non-nil winner")
		return
	}
	if winner.Source != "specific" {
		t.Fatalf("expected specific rule to win, got source=%q", winner.Source)
	}
}

func TestResolvePrecedenceToolExactBeatsWildcard(t *testing.T) {
	// Two deny rules — one from tool exact match, one from wildcard.
	// Tool-exact should win.
	matches := []RuleMatch{
		{
			Rule:      PermissionRule{ToolName: "*", Action: ActionDeny, Source: "wildcard"},
			ToolExact: false,
		},
		{
			Rule:      PermissionRule{ToolName: "Bash", Action: ActionDeny, Source: "exact"},
			ToolExact: true,
		},
	}
	_, winner, _ := ResolvePrecedenceWithWinner(matches)
	if winner.Source != "exact" {
		t.Fatalf("expected exact tool rule to win, got source=%q", winner.Source)
	}
}

func TestResolvePrecedenceInputPatternBeatsNoPattern(t *testing.T) {
	// Two rules same action — one with input pattern, one without.
	matches := []RuleMatch{
		{
			Rule:        PermissionRule{ToolName: "Bash", Action: ActionAllow, Source: "no-pattern"},
			ToolExact:   true,
			Specificity: 0,
		},
		{
			Rule:        PermissionRule{ToolName: "Bash", InputPattern: "git*", Action: ActionAllow, Source: "with-pattern"},
			ToolExact:   true,
			Specificity: 4,
		},
	}
	_, winner, _ := ResolvePrecedenceWithWinner(matches)
	if winner.Source != "with-pattern" {
		t.Fatalf("expected rule with input pattern to win, got source=%q", winner.Source)
	}
}

func TestResolvePrecedenceLongerPathBeatsShort(t *testing.T) {
	// More specific path should win.
	matches := []RuleMatch{
		{
			Rule:        PermissionRule{ToolName: "Edit", InputPattern: "/home/*", Action: ActionAllow, Source: "short-path"},
			ToolExact:   true,
			Specificity: computeSpecificity("/home/*"),
		},
		{
			Rule:        PermissionRule{ToolName: "Edit", InputPattern: "/home/user/project/*", Action: ActionAllow, Source: "long-path"},
			ToolExact:   true,
			Specificity: computeSpecificity("/home/user/project/*"),
		},
	}
	_, winner, _ := ResolvePrecedenceWithWinner(matches)
	if winner.Source != "long-path" {
		t.Fatalf("expected longer path to win, got source=%q", winner.Source)
	}
}

func TestEvaluateWithPrecedenceIntegration(t *testing.T) {
	// Scenario: broad wildcard ask + specific tool allow.
	// The deny>ask>allow priority means ask still wins here.
	engine := NewRulesEngine([]PermissionRule{
		{ToolName: "*", Action: ActionAsk, Source: "global-ask"},
		{ToolName: "Read", InputPattern: "/home/user/project/*", Action: ActionAllow, Source: "project-allow"},
	})

	// The broad "*:ask" matches everything. "Read(/home/user/project/*):allow" also matches.
	// By priority: ask > allow, so ask wins.
	action, winner, matched := engine.EvaluateWithPrecedence("Read", map[string]any{"file_path": "/home/user/project/main.go"})
	if !matched {
		t.Fatal("expected matched")
	}
	if action != ActionAsk {
		t.Fatalf("expected ActionAsk (ask > allow), got %q", action)
	}
	_ = winner
}

func TestEvaluateWithPrecedenceNoMatchReturnsAsk(t *testing.T) {
	engine := NewRulesEngine([]PermissionRule{
		{ToolName: "Write", Action: ActionDeny, Source: "test"},
	})

	action, _, matched := engine.EvaluateWithPrecedence("Read", map[string]any{"file_path": "/tmp/foo"})
	if matched {
		t.Fatal("expected no match for Read against Write rule")
	}
	if action != ActionAsk {
		t.Fatalf("expected ActionAsk for no match, got %q", action)
	}
}

func TestEvaluateWithPrecedenceNilEngine(t *testing.T) {
	var engine *RulesEngine
	action, _, matched := engine.EvaluateWithPrecedence("Bash", nil)
	if matched {
		t.Fatal("expected no match for nil engine")
	}
	if action != ActionAsk {
		t.Fatalf("expected ActionAsk for nil engine, got %q", action)
	}
}

func TestComputeSpecificityOrdering(t *testing.T) {
	// More specific paths should have higher scores.
	s1 := computeSpecificity("/home/*")
	s2 := computeSpecificity("/home/user/*")
	s3 := computeSpecificity("/home/user/project/*")

	if s1 >= s2 {
		t.Fatalf("expected /home/user/* > /home/*, got %d vs %d", s2, s1)
	}
	if s2 >= s3 {
		t.Fatalf("expected /home/user/project/* > /home/user/*, got %d vs %d", s3, s2)
	}
}

func TestComputeSpecificityWildcardPenalty(t *testing.T) {
	// Pattern without wildcard should be more specific than with wildcard.
	sExact := computeSpecificity("/home/user/file.go")
	sWild := computeSpecificity("/home/user/*")

	if sWild >= sExact {
		t.Fatalf("expected exact path > wildcard path, got %d vs %d", sExact, sWild)
	}
}

func TestComputeSpecificityEmpty(t *testing.T) {
	s := computeSpecificity("")
	if s != 0 {
		t.Fatalf("expected 0 for empty pattern, got %d", s)
	}
}

// Test determinism: same rules → same winner, always.
func TestResolvePrecedenceDeterministic(t *testing.T) {
	matches := []RuleMatch{
		{Rule: PermissionRule{ToolName: "Bash", InputPattern: "git*", Action: ActionAllow, Source: "a"}, ToolExact: true, Specificity: 4},
		{Rule: PermissionRule{ToolName: "Bash", InputPattern: "git push*", Action: ActionAllow, Source: "b"}, ToolExact: true, Specificity: 10},
	}

	// Run multiple times to verify determinism.
	for i := 0; i < 100; i++ {
		_, winner, _ := ResolvePrecedenceWithWinner(matches)
		if winner.Source != "b" {
			t.Fatalf("non-deterministic result on iteration %d: got source=%q", i, winner.Source)
		}
	}
}
