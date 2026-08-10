package permission

import (
	"context"
	"testing"
	"time"
)

// --- Unified Evaluator Tests ---

func TestEvaluator_BasicAllow(t *testing.T) {
	eval := NewEvaluator(EvaluatorConfig{
		Rules: []PermissionRule{
			{ToolName: "Read", Action: ActionAllow, Source: "project"},
		},
		Mode: ModeDefault,
	})

	result := eval.Evaluate(context.Background(), "Read", map[string]any{"file_path": "/tmp/foo"})
	if result.Decision != ActionAllow {
		t.Fatalf("expected allow, got %s (reason: %s)", result.Decision, result.Reason)
	}
	if result.Source != SourceEvalRule {
		t.Fatalf("expected source rule, got %s", result.Source)
	}
}

func TestEvaluator_BasicDeny(t *testing.T) {
	eval := NewEvaluator(EvaluatorConfig{
		Rules: []PermissionRule{
			{ToolName: "Bash", InputPattern: "rm*", Action: ActionDeny, Source: "project"},
		},
		Mode: ModeDefault,
	})

	result := eval.Evaluate(context.Background(), "Bash", map[string]any{"command": "rm -rf /"})
	if result.Decision != ActionDeny {
		t.Fatalf("expected deny, got %s (reason: %s)", result.Decision, result.Reason)
	}
}

func TestEvaluator_SessionStoreBypassesFutureChecks(t *testing.T) {
	store := NewSessionStore()
	store.Add(SessionDecision{
		ToolName: "Bash",
		Action:   ActionAllow,
		Scope:    ScopeProject,
	})

	eval := NewEvaluator(EvaluatorConfig{
		SessionStore: store,
		Rules: []PermissionRule{
			// This rule would normally ask, but session store takes precedence
			{ToolName: "Bash", Action: ActionAsk, Source: "project"},
		},
		Mode: ModeDefault,
	})

	result := eval.Evaluate(context.Background(), "Bash", map[string]any{"command": "echo hello"})
	if result.Decision != ActionAllow {
		t.Fatalf("expected session store allow to win, got %s (reason: %s)", result.Decision, result.Reason)
	}
	if result.Source != SourceEvalSessionStore {
		t.Fatalf("expected session_store source, got %s", result.Source)
	}
}

func TestEvaluator_RulePrecedenceWinsOverModeDefault(t *testing.T) {
	// A specific "allow" rule should win over mode's default "ask" behavior
	eval := NewEvaluator(EvaluatorConfig{
		Rules: []PermissionRule{
			{ToolName: "*", Action: ActionAsk, Source: "global"},                             // broad rule
			{ToolName: "Bash", InputPattern: "git*", Action: ActionAllow, Source: "project"}, // specific rule
		},
		Mode: ModeDefault,
	})

	result := eval.Evaluate(context.Background(), "Bash", map[string]any{"command": "git status"})
	if result.Decision != ActionAllow {
		t.Fatalf("specific allow rule should win, got %s (reason: %s)", result.Decision, result.Reason)
	}
	if result.Source != SourceEvalRule {
		t.Fatalf("expected source rule, got %s", result.Source)
	}
}

func TestEvaluator_RateLimitedDenialSuppressesRepeatedPrompts(t *testing.T) {
	eval := NewEvaluator(EvaluatorConfig{
		Rules: []PermissionRule{},
		Mode:  ModeDefault,
	})

	toolInput := map[string]any{"command": "rm -rf /important"}

	// Record a denial for this operation
	eval.RecordDenial("Bash", toolInput, "user denied")

	// The same operation should now be rate-limited
	result := eval.Evaluate(context.Background(), "Bash", toolInput)
	if result.Decision != ActionDeny {
		t.Fatalf("expected rate-limited deny, got %s (reason: %s)", result.Decision, result.Reason)
	}
	if result.Source != SourceEvalRateLimit {
		t.Fatalf("expected rate_limit source, got %s", result.Source)
	}
}

func TestEvaluator_ModeBypassAllowsUnmatched(t *testing.T) {
	eval := NewEvaluator(EvaluatorConfig{
		Rules: []PermissionRule{},
		Mode:  ModeBypassPermissions,
	})

	result := eval.Evaluate(context.Background(), "Bash", map[string]any{"command": "echo hello"})
	if result.Decision != ActionAllow {
		t.Fatalf("bypass mode should allow unmatched, got %s (reason: %s)", result.Decision, result.Reason)
	}
}

func TestEvaluator_ModePlanDeniesUnmatched(t *testing.T) {
	eval := NewEvaluator(EvaluatorConfig{
		Rules: []PermissionRule{},
		Mode:  ModePlan,
	})

	result := eval.Evaluate(context.Background(), "Bash", map[string]any{"command": "echo hello"})
	if result.Decision != ActionDeny {
		t.Fatalf("plan mode should deny unmatched, got %s (reason: %s)", result.Decision, result.Reason)
	}
}

func TestEvaluator_ModeDontAskConvertsAskToDeny(t *testing.T) {
	eval := NewEvaluator(EvaluatorConfig{
		Rules: []PermissionRule{
			{ToolName: "Bash", Action: ActionAsk, Source: "project"},
		},
		Mode: ModeDontAsk,
	})

	result := eval.Evaluate(context.Background(), "Bash", map[string]any{"command": "npm install"})
	if result.Decision != ActionDeny {
		t.Fatalf("dontAsk mode should convert ask→deny, got %s (reason: %s)", result.Decision, result.Reason)
	}
}

func TestEvaluator_AcceptEditsAutoAllowsFileOps(t *testing.T) {
	eval := NewEvaluator(EvaluatorConfig{
		Rules:      []PermissionRule{},
		Mode:       ModeAcceptEdits,
		WorkingDir: "/home/user/project",
	})

	result := eval.Evaluate(context.Background(), "Write", map[string]any{
		"file_path": "/home/user/project/src/main.go",
	})
	if result.Decision != ActionAllow {
		t.Fatalf("acceptEdits should auto-allow Write within cwd, got %s (reason: %s)", result.Decision, result.Reason)
	}
	if result.Source != SourceEvalAcceptEdits {
		t.Fatalf("expected accept_edits source, got %s", result.Source)
	}
}

func TestEvaluator_AcceptEditsDeniesOutsideCwd(t *testing.T) {
	eval := NewEvaluator(EvaluatorConfig{
		Rules:      []PermissionRule{},
		Mode:       ModeAcceptEdits,
		WorkingDir: "/home/user/project",
	})

	// Write outside the working directory should NOT be auto-allowed
	result := eval.Evaluate(context.Background(), "Write", map[string]any{
		"file_path": "/etc/passwd",
	})
	// Should not be auto-allowed (falls through to mode default or classification)
	if result.Decision == ActionAllow && result.Source == SourceEvalAcceptEdits {
		t.Fatalf("acceptEdits should not auto-allow writes outside cwd")
	}
}

func TestEvaluator_ClassifierAutoAllowsReadOnlyTools(t *testing.T) {
	eval := NewEvaluator(EvaluatorConfig{
		Rules: []PermissionRule{},
		Mode:  ModeDefault,
	})

	// Read tool should be auto-allowed via classifier even with no rules
	result := eval.Evaluate(context.Background(), "Read", map[string]any{"file_path": "/tmp/foo"})
	if result.Decision != ActionAllow {
		t.Fatalf("classifier should auto-allow Read, got %s (reason: %s)", result.Decision, result.Reason)
	}
	if result.Source != SourceEvalClassification {
		t.Fatalf("expected classification source, got %s", result.Source)
	}
}

func TestEvaluator_ClassifierCategorizesDestructiveBash(t *testing.T) {
	eval := NewEvaluator(EvaluatorConfig{
		Rules: []PermissionRule{},
		Mode:  ModeDefault,
	})

	result := eval.Evaluate(context.Background(), "Bash", map[string]any{"command": "rm -rf /"})
	if result.Decision != ActionAsk {
		t.Fatalf("destructive bash should require ask, got %s (reason: %s)", result.Decision, result.Reason)
	}
	if result.RiskLevel != RiskDestructive {
		t.Fatalf("expected RiskDestructive, got %v", result.RiskLevel)
	}
}

func TestEvaluator_ClassifierCategorizesReadOnlyBash(t *testing.T) {
	eval := NewEvaluator(EvaluatorConfig{
		Rules: []PermissionRule{},
		Mode:  ModeDefault,
	})

	result := eval.Evaluate(context.Background(), "Bash", map[string]any{"command": "git status"})
	if result.Decision != ActionAllow {
		t.Fatalf("read-only bash should be auto-allowed, got %s (reason: %s)", result.Decision, result.Reason)
	}
	if result.RiskLevel != RiskReadOnly {
		t.Fatalf("expected RiskReadOnly, got %v", result.RiskLevel)
	}
}

func TestEvaluator_FullPipelineFlow(t *testing.T) {
	// Full evaluation pipeline test:
	// classifier → rules → precedence → mode → decision
	store := NewSessionStore()
	eval := NewEvaluator(EvaluatorConfig{
		SessionStore: store,
		Rules: []PermissionRule{
			{ToolName: "Read", Action: ActionAllow, Source: "project"},
			{ToolName: "Bash", InputPattern: "npm*", Action: ActionAllow, Source: "project"},
			{ToolName: "Bash", InputPattern: "rm*", Action: ActionDeny, Source: "project"},
		},
		Mode:       ModeDefault,
		WorkingDir: "/home/user/project",
	})

	tests := []struct {
		name         string
		tool         string
		input        map[string]any
		wantDecision PermissionAction
		wantSource   EvaluationSource
	}{
		{
			name:         "read tool allowed by rule",
			tool:         "Read",
			input:        map[string]any{"file_path": "/tmp/foo"},
			wantDecision: ActionAllow,
			wantSource:   SourceEvalRule,
		},
		{
			name:         "npm bash allowed by rule",
			tool:         "Bash",
			input:        map[string]any{"command": "npm install"},
			wantDecision: ActionAllow,
			wantSource:   SourceEvalRule,
		},
		{
			name:         "rm bash denied by rule",
			tool:         "Bash",
			input:        map[string]any{"command": "rm -rf /"},
			wantDecision: ActionDeny,
			wantSource:   SourceEvalRule,
		},
		{
			name:         "grep auto-allowed by classifier (read-only, no rule match)",
			tool:         "Grep",
			input:        map[string]any{"pattern": "foo", "path": "/tmp"},
			wantDecision: ActionAllow,
			wantSource:   SourceEvalClassification,
		},
		{
			name:         "unknown bash asks (write risk, no rule match)",
			tool:         "Bash",
			input:        map[string]any{"command": "python deploy.py"},
			wantDecision: ActionAsk,
			wantSource:   SourceEvalMode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := eval.Evaluate(context.Background(), tt.tool, tt.input)
			if result.Decision != tt.wantDecision {
				t.Errorf("decision = %s, want %s (reason: %s)", result.Decision, tt.wantDecision, result.Reason)
			}
			if result.Source != tt.wantSource {
				t.Errorf("source = %s, want %s", result.Source, tt.wantSource)
			}
		})
	}

	// Now persist an allow-always decision and verify it takes precedence
	eval.PersistDecision("Bash", "", ActionAllow, ScopeProject, "user said always allow")

	result := eval.Evaluate(context.Background(), "Bash", map[string]any{"command": "python deploy.py"})
	if result.Decision != ActionAllow {
		t.Fatalf("persisted allow should win over mode default, got %s (reason: %s)", result.Decision, result.Reason)
	}
	if result.Source != SourceEvalSessionStore {
		t.Fatalf("expected session_store source, got %s", result.Source)
	}
}

// --- Risk Classifier Tests ---

func TestToolRiskClassifier_ReadOnlyTools(t *testing.T) {
	c := NewToolRiskClassifier()

	readOnlyTools := []string{"Read", "Grep", "Glob", "WebFetch", "WebSearch", "Agent", "TodoWrite"}
	for _, tool := range readOnlyTools {
		result := c.Classify(tool, nil)
		if result.Level != RiskReadOnly {
			t.Errorf("tool %s: expected read_only, got %s", tool, result.Level)
		}
	}
}

func TestToolRiskClassifier_WriteTools(t *testing.T) {
	c := NewToolRiskClassifier()

	writeTools := []string{"Write", "Edit"}
	for _, tool := range writeTools {
		result := c.Classify(tool, nil)
		if result.Level != RiskWrite {
			t.Errorf("tool %s: expected write, got %s", tool, result.Level)
		}
	}
}

func TestToolRiskClassifier_BashReadOnly(t *testing.T) {
	c := NewToolRiskClassifier()

	readOnlyCmds := []string{"ls", "cat foo.txt", "git status", "grep foo bar.txt", "pwd"}
	for _, cmd := range readOnlyCmds {
		result := c.Classify("Bash", map[string]any{"command": cmd})
		if result.Level != RiskReadOnly {
			t.Errorf("cmd %q: expected read_only, got %s (reason: %s)", cmd, result.Level, result.Reason)
		}
	}
}

func TestToolRiskClassifier_BashDestructive(t *testing.T) {
	c := NewToolRiskClassifier()

	destructiveCmds := []string{"rm -rf /", "rmdir /tmp/foo", "kill -9 1234", "shutdown -h now"}
	for _, cmd := range destructiveCmds {
		result := c.Classify("Bash", map[string]any{"command": cmd})
		if result.Level != RiskDestructive {
			t.Errorf("cmd %q: expected destructive, got %s (reason: %s)", cmd, result.Level, result.Reason)
		}
	}
}

func TestToolRiskClassifier_BashWrite(t *testing.T) {
	c := NewToolRiskClassifier()

	writeCmds := []string{"npm install", "python deploy.py", "docker build ."}
	for _, cmd := range writeCmds {
		result := c.Classify("Bash", map[string]any{"command": cmd})
		if result.Level != RiskWrite {
			t.Errorf("cmd %q: expected write, got %s (reason: %s)", cmd, result.Level, result.Reason)
		}
	}
}

func TestToolRiskClassifier_MCPTools(t *testing.T) {
	c := NewToolRiskClassifier()

	result := c.Classify("mcp__server__tool", map[string]any{"arg": "value"})
	if result.Level != RiskWrite {
		t.Errorf("MCP tool: expected write, got %s", result.Level)
	}
}

func TestToolRiskClassifier_UnknownTool(t *testing.T) {
	c := NewToolRiskClassifier()

	result := c.Classify("UnknownTool", nil)
	if result.Level != RiskUnknown {
		t.Errorf("unknown tool: expected unknown, got %s", result.Level)
	}
}

// --- Mode Transition Tests ---

func TestModeTransition_SameModeNoOp(t *testing.T) {
	eval := NewEvaluator(EvaluatorConfig{Mode: ModeDefault})
	mgr := NewModeManager(eval, nil)

	result, err := mgr.TransitionTo(ModeDefault)
	if err != nil {
		t.Fatalf("same mode should not error: %v", err)
		return
	}
	if result.PreviousMode != ModeDefault || result.NewMode != ModeDefault {
		t.Fatalf("expected no-op, got %+v", result)
	}
}

func TestModeTransition_DefaultToPlan(t *testing.T) {
	store := NewSessionStore()
	store.Add(SessionDecision{
		ToolName: "Write",
		Action:   ActionAllow,
		Scope:    ScopeProject,
	})
	store.Add(SessionDecision{
		ToolName: "Read",
		Action:   ActionAllow,
		Scope:    ScopeProject,
	})

	eval := NewEvaluator(EvaluatorConfig{
		SessionStore: store,
		Mode:         ModeDefault,
	})
	mgr := NewModeManager(eval, nil)

	result, err := mgr.TransitionTo(ModePlan)
	if err != nil {
		t.Fatalf("default→plan should succeed: %v", err)
		return
	}
	if result.NewMode != ModePlan {
		t.Fatalf("expected plan mode, got %s", result.NewMode)
	}
	// Write decisions should be cleared, Read should remain
	if result.SessionDecisionsCleared != 1 {
		t.Fatalf("expected 1 write decision cleared, got %d", result.SessionDecisionsCleared)
	}
	if result.DenialTrackingReset != true {
		t.Fatal("expected denial tracking reset")
	}

	// Verify the store state: Read should still be there, Write should be gone
	decisions := store.List()
	if len(decisions) != 1 {
		t.Fatalf("expected 1 remaining decision, got %d", len(decisions))
	}
	if decisions[0].ToolName != "Read" {
		t.Fatalf("expected Read to remain, got %s", decisions[0].ToolName)
	}
}

func TestModeTransition_ToBypassRequiresConfirmation(t *testing.T) {
	eval := NewEvaluator(EvaluatorConfig{Mode: ModeDefault})

	// No confirm callback → rejected
	mgr := NewModeManager(eval, nil)
	_, err := mgr.TransitionTo(ModeBypassPermissions)
	if err == nil {
		t.Fatal("bypass without confirmation should fail")
		return
	}

	// Confirm returns false → rejected
	mgr = NewModeManager(eval, func() bool { return false })
	_, err = mgr.TransitionTo(ModeBypassPermissions)
	if err == nil {
		t.Fatal("bypass with rejected confirmation should fail")
		return
	}

	// Confirm returns true → accepted
	mgr = NewModeManager(eval, func() bool { return true })
	result, err := mgr.TransitionTo(ModeBypassPermissions)
	if err != nil {
		t.Fatalf("bypass with confirmation should succeed: %v", err)
		return
	}
	if result.NewMode != ModeBypassPermissions {
		t.Fatalf("expected bypass mode, got %s", result.NewMode)
	}
}

func TestModeTransition_LeavingBypassClearsAllDecisions(t *testing.T) {
	store := NewSessionStore()
	store.Add(SessionDecision{ToolName: "Bash", Action: ActionAllow, Scope: ScopeProject})
	store.Add(SessionDecision{ToolName: "Read", Action: ActionAllow, Scope: ScopeProject})
	store.Add(SessionDecision{ToolName: "Write", Action: ActionAllow, Scope: ScopeProject})

	eval := NewEvaluator(EvaluatorConfig{
		SessionStore: store,
		Mode:         ModeBypassPermissions,
	})
	mgr := NewModeManager(eval, nil)

	result, err := mgr.TransitionTo(ModeDefault)
	if err != nil {
		t.Fatalf("bypass→default should succeed: %v", err)
		return
	}
	if result.SessionDecisionsCleared != 3 {
		t.Fatalf("expected 3 decisions cleared, got %d", result.SessionDecisionsCleared)
	}

	// Store should be empty
	decisions := store.List()
	if len(decisions) != 0 {
		t.Fatalf("expected empty store after leaving bypass, got %d", len(decisions))
	}
}

func TestModeTransition_InvalidTargetMode(t *testing.T) {
	eval := NewEvaluator(EvaluatorConfig{Mode: ModeDefault})
	mgr := NewModeManager(eval, nil)

	_, err := mgr.TransitionTo(ModeAuto)
	if err == nil {
		t.Fatal("transition to internal mode should fail")
		return
	}

	_, err = mgr.TransitionTo(ModeBubble)
	if err == nil {
		t.Fatal("transition to internal mode should fail")
		return
	}
}

func TestModeTransition_ClearsRateLimits(t *testing.T) {
	eval := NewEvaluator(EvaluatorConfig{Mode: ModeDefault})
	mgr := NewModeManager(eval, nil)

	// Record a denial to create a rate limit entry
	eval.DenialTracking.RecordDenialWithDetails("Bash", map[string]any{"command": "npm install"}, ReasonRule, "test")

	// Verify rate limit is active
	if !eval.DenialTracking.IsRateLimited("Bash", map[string]any{"command": "npm install"}) {
		t.Fatal("expected rate limit to be active")
	}

	// Transition should clear rate limits
	result, err := mgr.TransitionTo(ModeDontAsk)
	if err != nil {
		t.Fatalf("transition should succeed: %v", err)
		return
	}
	if !result.RateLimitsCleared {
		t.Fatal("expected rate limits cleared")
	}

	// Rate limit should be gone
	if eval.DenialTracking.IsRateLimited("Bash", map[string]any{"command": "npm install"}) {
		t.Fatal("rate limit should be cleared after mode transition")
	}
}

func TestModeTransition_PlanModeBlocksWrites(t *testing.T) {
	eval := NewEvaluator(EvaluatorConfig{
		Rules: []PermissionRule{
			{ToolName: "Write", Action: ActionAllow, Source: "project"},
		},
		Mode: ModeDefault,
	})
	mgr := NewModeManager(eval, nil)

	// In default mode, Write is allowed by rule
	result := eval.Evaluate(context.Background(), "Write", map[string]any{"file_path": "/tmp/foo"})
	if result.Decision != ActionAllow {
		t.Fatalf("default mode: expected allow, got %s", result.Decision)
	}

	// Transition to plan mode
	_, err := mgr.TransitionTo(ModePlan)
	if err != nil {
		t.Fatalf("transition should succeed: %v", err)
		return
	}

	// Write rule still matches (allow) but plan mode doesn't transform allow rules
	// However for unmatched tools, plan denies by default
	unmatched := eval.Evaluate(context.Background(), "Bash", map[string]any{"command": "echo hello"})
	if unmatched.Decision != ActionDeny {
		t.Fatalf("plan mode: unmatched should deny, got %s (reason: %s)", unmatched.Decision, unmatched.Reason)
	}
}

// --- Scenario Tests: End-to-End Permission Behavior ---

func TestScenario_RulePrecedenceOverModeDefault(t *testing.T) {
	// Scenario: a specific allow rule should win over mode's default ask behavior,
	// even when a broad wildcard rule exists.
	eval := NewEvaluator(EvaluatorConfig{
		Rules: []PermissionRule{
			{ToolName: "*", Action: ActionAsk, Source: "global"},
			{ToolName: "Read", InputPattern: "/home/user/project/*", Action: ActionAllow, Source: "project"},
		},
		Mode: ModeDefault,
	})

	// Specific project path → allow (specific rule wins)
	result := eval.Evaluate(context.Background(), "Read", map[string]any{
		"file_path": "/home/user/project/main.go",
	})
	if result.Decision != ActionAllow {
		t.Fatalf("specific rule should win: got %s (reason: %s)", result.Decision, result.Reason)
	}

	// Different path → ask (only wildcard matches)
	result = eval.Evaluate(context.Background(), "Read", map[string]any{
		"file_path": "/etc/passwd",
	})
	if result.Decision != ActionAsk {
		t.Fatalf("wildcard rule should apply: got %s (reason: %s)", result.Decision, result.Reason)
	}
}

func TestScenario_PersistedAllowAlwaysBypassesFutureChecks(t *testing.T) {
	// Scenario: user says "always allow" for a tool → future checks skip rules entirely
	store := NewSessionStore()
	eval := NewEvaluator(EvaluatorConfig{
		SessionStore: store,
		Rules: []PermissionRule{
			{ToolName: "Bash", Action: ActionAsk, Source: "project"},
		},
		Mode: ModeDefault,
	})

	// First check: rule says ask
	result := eval.Evaluate(context.Background(), "Bash", map[string]any{"command": "go test ./..."})
	if result.Decision != ActionAsk {
		t.Fatalf("before persist: expected ask, got %s", result.Decision)
	}

	// User says "always allow for this tool"
	eval.PersistDecision("Bash", "", ActionAllow, ScopeProject, "user approved always")

	// Second check: session store wins immediately
	result = eval.Evaluate(context.Background(), "Bash", map[string]any{"command": "go test ./..."})
	if result.Decision != ActionAllow {
		t.Fatalf("after persist: expected allow, got %s (reason: %s)", result.Decision, result.Reason)
	}
	if result.Source != SourceEvalSessionStore {
		t.Fatalf("expected session_store source, got %s", result.Source)
	}
}

func TestScenario_RateLimitedDenialSuppressesRepeatedPrompts(t *testing.T) {
	// Scenario: after a denial, the same operation is auto-denied without re-prompting
	eval := NewEvaluator(EvaluatorConfig{
		Rules: []PermissionRule{},
		Mode:  ModeDefault,
	})

	input := map[string]any{"command": "sudo rm -rf /"}

	// Record denial (simulating user denied the prompt)
	eval.RecordDenial("Bash", input, "user denied")

	// Immediately after, same operation should be rate-limited
	result := eval.Evaluate(context.Background(), "Bash", input)
	if result.Decision != ActionDeny {
		t.Fatalf("expected rate-limited deny, got %s", result.Decision)
	}
	if result.Source != SourceEvalRateLimit {
		t.Fatalf("expected rate_limit source, got %s", result.Source)
	}

	// Different operation should NOT be rate-limited
	differentInput := map[string]any{"command": "ls -la"}
	result = eval.Evaluate(context.Background(), "Bash", differentInput)
	if result.Source == SourceEvalRateLimit {
		t.Fatal("different operation should not be rate-limited")
	}
}

func TestScenario_ModeTransitionClearsWritePermissions(t *testing.T) {
	// Scenario: transitioning to plan mode clears write-allow decisions
	store := NewSessionStore()
	store.Add(SessionDecision{ToolName: "Write", Action: ActionAllow, Scope: ScopeProject})
	store.Add(SessionDecision{ToolName: "Edit", Action: ActionAllow, Scope: ScopeProject})
	store.Add(SessionDecision{ToolName: "Read", Action: ActionAllow, Scope: ScopeProject})
	store.Add(SessionDecision{ToolName: "Bash", Action: ActionAllow, Scope: ScopeProject})

	eval := NewEvaluator(EvaluatorConfig{
		SessionStore: store,
		Mode:         ModeDefault,
	})
	mgr := NewModeManager(eval, nil)

	// Before transition: Write is allowed from session store
	result := eval.Evaluate(context.Background(), "Write", map[string]any{"file_path": "/tmp/foo"})
	if result.Decision != ActionAllow {
		t.Fatalf("before transition: expected allow, got %s", result.Decision)
	}

	// Transition to plan mode
	transResult, err := mgr.TransitionTo(ModePlan)
	if err != nil {
		t.Fatalf("transition failed: %v", err)
		return
	}

	// Write and Edit and Bash decisions should be cleared (they are write tools)
	if transResult.SessionDecisionsCleared != 3 {
		t.Fatalf("expected 3 write decisions cleared, got %d", transResult.SessionDecisionsCleared)
	}

	// After transition: Write is no longer in session store, and plan mode denies unmatched
	result = eval.Evaluate(context.Background(), "Write", map[string]any{"file_path": "/tmp/foo"})
	if result.Decision == ActionAllow && result.Source == SourceEvalSessionStore {
		t.Fatal("write decision should have been cleared by mode transition")
	}

	// Read should still be allowed from session store
	result = eval.Evaluate(context.Background(), "Read", map[string]any{"file_path": "/tmp/foo"})
	if result.Decision != ActionAllow {
		t.Fatalf("Read should still be allowed from session store, got %s (source: %s)", result.Decision, result.Source)
	}
}

func TestScenario_ClassifierCorrectlyCategorizesToolOperations(t *testing.T) {
	// Scenario: the classifier correctly categorizes various tool operations
	c := NewToolRiskClassifier()

	tests := []struct {
		tool      string
		input     map[string]any
		wantLevel ToolRiskLevel
	}{
		// Read-only tools
		{"Read", map[string]any{"file_path": "/tmp/foo"}, RiskReadOnly},
		{"Grep", map[string]any{"pattern": "TODO"}, RiskReadOnly},
		{"Glob", map[string]any{"pattern": "*.go"}, RiskReadOnly},
		{"WebSearch", map[string]any{"query": "golang"}, RiskReadOnly},
		// Write tools
		{"Write", map[string]any{"file_path": "/tmp/foo"}, RiskWrite},
		{"Edit", map[string]any{"file_path": "/tmp/foo"}, RiskWrite},
		// Bash: read-only commands
		{"Bash", map[string]any{"command": "ls -la"}, RiskReadOnly},
		{"Bash", map[string]any{"command": "cat /etc/hosts"}, RiskReadOnly},
		{"Bash", map[string]any{"command": "git status"}, RiskReadOnly},
		{"Bash", map[string]any{"command": "git log --oneline"}, RiskReadOnly},
		// Bash: write commands
		{"Bash", map[string]any{"command": "docker build ."}, RiskWrite},
		{"Bash", map[string]any{"command": "npm install"}, RiskWrite},
		// Bash: destructive commands
		{"Bash", map[string]any{"command": "rm -rf /tmp/important"}, RiskDestructive},
		{"Bash", map[string]any{"command": "kill -9 1234"}, RiskDestructive},
		// MCP tools
		{"mcp__github__create_issue", map[string]any{"title": "bug"}, RiskWrite},
	}

	for _, tt := range tests {
		t.Run(tt.tool+"_"+truncateForReason(extractInputString(tt.tool, tt.input)), func(t *testing.T) {
			result := c.Classify(tt.tool, tt.input)
			if result.Level != tt.wantLevel {
				t.Errorf("classify(%s, %v): got %s, want %s (reason: %s)",
					tt.tool, tt.input, result.Level, tt.wantLevel, result.Reason)
			}
		})
	}
}

func TestScenario_FullEvaluationPipeline(t *testing.T) {
	// Scenario: full pipeline exercising classifier → rules → precedence → mode → decision
	store := NewSessionStore()
	eval := NewEvaluator(EvaluatorConfig{
		SessionStore: store,
		Rules: []PermissionRule{
			// Broad deny for rm
			{ToolName: "Bash", InputPattern: "rm*", Action: ActionDeny, Source: "project"},
			// Specific allow for safe rm
			{ToolName: "Bash", InputPattern: "rm /tmp/test*", Action: ActionAllow, Source: "project"},
			// Allow git operations
			{ToolName: "Bash", InputPattern: "git*", Action: ActionAllow, Source: "project"},
		},
		Mode:       ModeDefault,
		WorkingDir: "/home/user/project",
	})

	// Test 1: "rm /tmp/test_output" should be allowed (specific rule wins via precedence)
	result := eval.Evaluate(context.Background(), "Bash", map[string]any{"command": "rm /tmp/test_output"})
	if result.Decision != ActionAllow {
		t.Fatalf("specific allow should win over broad deny: got %s (reason: %s)", result.Decision, result.Reason)
	}

	// Test 2: "rm -rf /" should be denied (only broad deny matches)
	result = eval.Evaluate(context.Background(), "Bash", map[string]any{"command": "rm -rf /"})
	if result.Decision != ActionDeny {
		t.Fatalf("broad deny should apply: got %s (reason: %s)", result.Decision, result.Reason)
	}

	// Test 3: "git push" should be allowed (specific git rule)
	result = eval.Evaluate(context.Background(), "Bash", map[string]any{"command": "git push"})
	if result.Decision != ActionAllow {
		t.Fatalf("git allow rule should apply: got %s (reason: %s)", result.Decision, result.Reason)
	}

	// Test 4: Read tool, no rule → classifier auto-allows
	result = eval.Evaluate(context.Background(), "Read", map[string]any{"file_path": "/tmp/foo"})
	if result.Decision != ActionAllow {
		t.Fatalf("Read should be auto-allowed: got %s (reason: %s)", result.Decision, result.Reason)
	}

	// Test 5: Unknown bash command → mode default (ask) with risk classification
	result = eval.Evaluate(context.Background(), "Bash", map[string]any{"command": "python deploy.py"})
	if result.Decision != ActionAsk {
		t.Fatalf("unknown write bash should ask: got %s (reason: %s)", result.Decision, result.Reason)
	}

	// Test 6: Persist an allow-always decision, then verify it takes precedence
	eval.PersistDecision("Bash", "python*", ActionAllow, ScopeProject, "user always allows python")
	result = eval.Evaluate(context.Background(), "Bash", map[string]any{"command": "python deploy.py"})
	if result.Decision != ActionAllow {
		t.Fatalf("persisted allow should take precedence: got %s (reason: %s)", result.Decision, result.Reason)
	}
	if result.Source != SourceEvalSessionStore {
		t.Fatalf("expected session_store source, got %s", result.Source)
	}
}

func TestScenario_ConcurrentAccess(t *testing.T) {
	// Verify thread safety of the evaluator under concurrent access
	eval := NewEvaluator(EvaluatorConfig{
		Rules: []PermissionRule{
			{ToolName: "Read", Action: ActionAllow, Source: "project"},
			{ToolName: "Bash", InputPattern: "rm*", Action: ActionDeny, Source: "project"},
		},
		Mode: ModeDefault,
	})

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 100; j++ {
				_ = eval.Evaluate(context.Background(), "Read", map[string]any{"file_path": "/tmp/foo"})
				_ = eval.Evaluate(context.Background(), "Bash", map[string]any{"command": "rm -rf /"})
				_ = eval.Evaluate(context.Background(), "Bash", map[string]any{"command": "ls -la"})
				eval.RecordDenial("Bash", map[string]any{"command": "rm"}, "test")
				eval.RecordSuccess()
			}
		}()
	}

	// Wait for all goroutines with timeout
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for i := 0; i < 10; i++ {
		select {
		case <-done:
		case <-timer.C:
			t.Fatal("concurrent test timed out")
		}
	}
}

// --- ValidateTransition Tests ---

func TestValidateTransition_InternalModesRejected(t *testing.T) {
	_, err := ValidateTransition(ModeDefault, ModeAuto)
	if err == nil {
		t.Fatal("should reject transition to auto")
		return
	}

	_, err = ValidateTransition(ModeDefault, ModeBubble)
	if err == nil {
		t.Fatal("should reject transition to bubble")
		return
	}
}

func TestValidateTransition_BypassRequiresConfirmation(t *testing.T) {
	v, err := ValidateTransition(ModeDefault, ModeBypassPermissions)
	if err != nil {
		t.Fatalf("validation should succeed: %v", err)
		return
	}
	if !v.RequiresConfirmation {
		t.Fatal("bypass should require confirmation")
	}
}

func TestValidateTransition_PlanClearsWriteDecisions(t *testing.T) {
	v, err := ValidateTransition(ModeDefault, ModePlan)
	if err != nil {
		t.Fatalf("validation should succeed: %v", err)
		return
	}
	if !v.ClearsWriteDecisions {
		t.Fatal("plan transition should clear write decisions")
	}
}

func TestValidateTransition_LeavingBypassClearsAll(t *testing.T) {
	v, err := ValidateTransition(ModeBypassPermissions, ModeDefault)
	if err != nil {
		t.Fatalf("validation should succeed: %v", err)
		return
	}
	if !v.ClearsAllDecisions {
		t.Fatal("leaving bypass should clear all decisions")
	}
}
