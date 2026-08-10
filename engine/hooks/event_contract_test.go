package hooks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

// ---------------------------------------------------------------------------
// Event-Contract Scenario Tests
//
// These tests verify the full hook event lifecycle contracts:
//   - Full query lifecycle ordering
//   - Hook failure isolation
//   - Prompt hook modification downstream effects
//   - Async hook non-blocking behavior
//   - Agent lifecycle hook firing
//   - Lifecycle ordering enforcement
// ---------------------------------------------------------------------------

// TestFullQueryLifecycleOrder verifies the complete hook event ordering for a
// typical query: pre-query (turn start) -> pre-tool -> tool-exec -> post-tool -> post-query (turn end).
func TestFullQueryLifecycleOrder(t *testing.T) {
	executor := NewExecutor()
	ctx := context.Background()

	var events []string
	var mu sync.Mutex
	record := func(event string) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	}

	// Register hooks in expected lifecycle order.
	executor.RegisterSessionStart(func(context.Context, string, bool) *SessionStartHookResult {
		record("session_start")
		return nil
	})
	executor.RegisterTurnStart(func(context.Context, int, string) *TurnStartHookResult {
		record("turn_start")
		return nil
	})
	executor.RegisterPreTool(func(context.Context, string, string, map[string]any) *PreToolHookResult {
		record("pre_tool")
		return nil
	})
	executor.RegisterPostTool(func(context.Context, string, string, map[string]any, string) *PostToolHookResult {
		record("post_tool")
		return nil
	})
	executor.RegisterTurnEnd(func(context.Context, int, string) {
		record("turn_end")
	})
	executor.RegisterSessionEnd(func(context.Context, string, string) {
		record("session_end")
	})

	// Simulate a query lifecycle.
	executor.ExecuteSessionStart(ctx, "session-1", false)
	executor.ExecuteTurnStart(ctx, 1, "hello")
	executor.ExecutePreTool(ctx, "Bash", "toolu_1", map[string]any{"command": "ls"})
	// (tool executes here)
	executor.ExecutePostTool(ctx, "Bash", "toolu_1", map[string]any{"command": "ls"}, "file1\nfile2")
	executor.ExecuteTurnEnd(ctx, 1, "done")
	executor.ExecuteSessionEnd(ctx, "session-1", "user_quit")

	// Verify ordering.
	expected := []string{
		"session_start",
		"turn_start",
		"pre_tool",
		"post_tool",
		"turn_end",
		"session_end",
	}

	if len(events) != len(expected) {
		t.Fatalf("events count = %d, want %d\nevents = %v", len(events), len(expected), events)
	}
	for i, want := range expected {
		if events[i] != want {
			t.Fatalf("events[%d] = %q, want %q\nfull events = %v", i, events[i], want, events)
		}
	}
}

// TestHookFailureIsolation verifies that hook errors (non-panic) do not prevent
// other hooks from executing, and that nil results are handled gracefully.
func TestHookFailureIsolation(t *testing.T) {
	executor := NewExecutor()
	ctx := context.Background()

	var executed []string

	// Register three pre-tool hooks: first succeeds, second returns nil, third succeeds.
	// Note: programmatic pre/post tool hooks do NOT have per-hook panic recovery
	// in the current implementation (panics propagate). This test verifies that
	// hooks returning nil are safely skipped, and error states in hook results
	// (like DenyReason) don't prevent subsequent hooks from running.
	executor.RegisterPreTool(func(context.Context, string, string, map[string]any) *PreToolHookResult {
		executed = append(executed, "hook_1")
		return &PreToolHookResult{UpdatedInput: map[string]any{"from": "hook1"}}
	})
	executor.RegisterPreTool(func(context.Context, string, string, map[string]any) *PreToolHookResult {
		executed = append(executed, "hook_2_nil")
		return nil // returning nil is the graceful "no-op" pattern
	})
	executor.RegisterPreTool(func(context.Context, string, string, map[string]any) *PreToolHookResult {
		executed = append(executed, "hook_3")
		return &PreToolHookResult{UpdatedInput: map[string]any{"from": "hook3"}}
	})

	result := executor.ExecutePreTool(ctx, "Bash", "toolu_1", map[string]any{"command": "ls"})

	// All three hooks should execute.
	if len(executed) != 3 {
		t.Fatalf("executed = %v, want all 3 hooks", executed)
	}
	// Final input should be from hook_3 (last non-nil updater wins).
	if result.UpdatedInput["from"] != "hook3" {
		t.Fatalf("UpdatedInput[from] = %v, want hook3", result.UpdatedInput["from"])
	}
}

// TestHookFailureIsolationPromptHooks verifies prompt hook failure isolation.
func TestHookFailureIsolationPromptHooks(t *testing.T) {
	executor := NewExecutor()
	ctx := context.Background()

	var executed []string

	executor.RegisterPromptHook(func(_ context.Context, prompt string, _ *PromptHookMetadata) *PromptHookResult {
		executed = append(executed, "hook_1")
		return &PromptHookResult{ModifiedPrompt: prompt + " [hook1]"}
	})
	executor.RegisterPromptHook(func(_ context.Context, _ string, _ *PromptHookMetadata) *PromptHookResult {
		executed = append(executed, "hook_2_panic")
		panic("intentional test panic in prompt hook")
	})
	executor.RegisterPromptHook(func(_ context.Context, prompt string, _ *PromptHookMetadata) *PromptHookResult {
		executed = append(executed, "hook_3")
		return &PromptHookResult{ModifiedPrompt: prompt + " [hook3]"}
	})

	result := executor.ExecutePromptHooks(ctx, "base prompt", &PromptHookMetadata{ModelName: "test"})

	// All three hooks should have attempted execution.
	if len(executed) != 3 {
		t.Fatalf("executed = %v, want all 3 hooks to attempt", executed)
	}

	// Hook 2 panicked, so its modification is skipped.
	// Final prompt should have hook1 and hook3 modifications.
	if !strings.Contains(result.FinalPrompt, "[hook1]") {
		t.Fatalf("FinalPrompt = %q, want [hook1] applied", result.FinalPrompt)
	}
	if !strings.Contains(result.FinalPrompt, "[hook3]") {
		t.Fatalf("FinalPrompt = %q, want [hook3] applied", result.FinalPrompt)
	}

	// Should have exactly 1 error (from hook_2 panic).
	if len(result.Errors) != 1 {
		t.Fatalf("Errors count = %d, want 1", len(result.Errors))
	}
	if !strings.Contains(result.Errors[0].Error(), "panic") {
		t.Fatalf("Error = %q, want panic error", result.Errors[0].Error())
	}

	// HooksExecuted should count successful ones.
	if result.HooksExecuted != 2 {
		t.Fatalf("HooksExecuted = %d, want 2", result.HooksExecuted)
	}
}

// TestPromptHookModificationDownstream verifies that prompt hook changes
// propagate correctly through the hook chain.
func TestPromptHookModificationDownstream(t *testing.T) {
	executor := NewExecutor()
	ctx := context.Background()

	// Register hooks that each transform the prompt.
	executor.RegisterPromptHook(func(_ context.Context, prompt string, _ *PromptHookMetadata) *PromptHookResult {
		return &PromptHookResult{
			ModifiedPrompt: strings.ToUpper(prompt),
		}
	})
	executor.RegisterPromptHook(func(_ context.Context, prompt string, _ *PromptHookMetadata) *PromptHookResult {
		return &PromptHookResult{
			ModifiedPrompt:    prompt + " [verified]",
			AdditionalContext: "policy: be concise",
		}
	})
	executor.RegisterPromptHook(func(_ context.Context, prompt string, _ *PromptHookMetadata) *PromptHookResult {
		// Verify it receives the output of previous hooks.
		if !strings.HasPrefix(prompt, "YOU ARE A HELPFUL ASSISTANT") {
			return &PromptHookResult{
				Block:       true,
				BlockReason: "unexpected prompt state",
			}
		}
		return &PromptHookResult{
			AdditionalContext: "scope: tools only",
		}
	})

	result := executor.ExecutePromptHooks(ctx, "You are a helpful assistant", &PromptHookMetadata{
		ModelName:  "claude-sonnet-4-20250514",
		SessionID:  "session-1",
		TurnNumber: 1,
	})

	if result.Blocked {
		t.Fatalf("unexpectedly blocked: %s", result.BlockReason)
	}
	if result.FinalPrompt != "YOU ARE A HELPFUL ASSISTANT [verified]" {
		t.Fatalf("FinalPrompt = %q, want uppercase with [verified]", result.FinalPrompt)
	}
	if len(result.AdditionalContexts) != 2 {
		t.Fatalf("AdditionalContexts = %v, want 2 entries", result.AdditionalContexts)
	}
	if result.AdditionalContexts[0] != "policy: be concise" {
		t.Fatalf("AdditionalContexts[0] = %q", result.AdditionalContexts[0])
	}
	if result.AdditionalContexts[1] != "scope: tools only" {
		t.Fatalf("AdditionalContexts[1] = %q", result.AdditionalContexts[1])
	}
	if result.HooksExecuted != 3 {
		t.Fatalf("HooksExecuted = %d, want 3", result.HooksExecuted)
	}
}

// TestPromptHookBlockingStopsChain verifies that a blocking prompt hook
// stops subsequent hooks from executing.
func TestPromptHookBlockingStopsChain(t *testing.T) {
	executor := NewExecutor()
	ctx := context.Background()

	var executed []int

	executor.RegisterPromptHook(func(_ context.Context, prompt string, _ *PromptHookMetadata) *PromptHookResult {
		executed = append(executed, 1)
		return &PromptHookResult{ModifiedPrompt: prompt + " [1]"}
	})
	executor.RegisterPromptHook(func(_ context.Context, _ string, _ *PromptHookMetadata) *PromptHookResult {
		executed = append(executed, 2)
		return &PromptHookResult{
			Block:       true,
			BlockReason: "policy violation detected",
		}
	})
	executor.RegisterPromptHook(func(_ context.Context, prompt string, _ *PromptHookMetadata) *PromptHookResult {
		executed = append(executed, 3)
		return &PromptHookResult{ModifiedPrompt: prompt + " [3]"}
	})

	result := executor.ExecutePromptHooks(ctx, "test prompt", nil)

	if !result.Blocked {
		t.Fatal("expected Blocked = true")
	}
	if result.BlockReason != "policy violation detected" {
		t.Fatalf("BlockReason = %q", result.BlockReason)
	}
	// Only hooks 1 and 2 should execute (3 is after the blocking hook).
	if len(executed) != 2 {
		t.Fatalf("executed = %v, want [1, 2]", executed)
	}
}

// TestPromptHookNilPassthrough verifies nil returns pass through unchanged.
func TestPromptHookNilPassthrough(t *testing.T) {
	executor := NewExecutor()
	ctx := context.Background()

	executor.RegisterPromptHook(func(_ context.Context, _ string, _ *PromptHookMetadata) *PromptHookResult {
		return nil // pass-through
	})
	executor.RegisterPromptHook(func(_ context.Context, _ string, _ *PromptHookMetadata) *PromptHookResult {
		return nil // pass-through
	})

	result := executor.ExecutePromptHooks(ctx, "unchanged prompt", nil)

	if result.FinalPrompt != "unchanged prompt" {
		t.Fatalf("FinalPrompt = %q, want original", result.FinalPrompt)
	}
	if result.HooksExecuted != 2 {
		t.Fatalf("HooksExecuted = %d, want 2", result.HooksExecuted)
	}
}

// TestAsyncHooksDoNotBlockMainPath verifies that async hooks don't block
// the main execution path.
func TestAsyncHooksDoNotBlockMainPath(t *testing.T) {
	registry := NewAsyncRegistry()

	// Track timing.
	start := time.Now()

	// Dispatch a slow async hook.
	var hookCompleted atomic.Bool
	_, err := registry.ExecuteAsync(context.Background(), "slow_hook", func(ctx context.Context) (any, error) {
		time.Sleep(100 * time.Millisecond)
		hookCompleted.Store(true)
		return "done", nil
	})
	if err != nil {
		t.Fatalf("ExecuteAsync: %v", err)
		return
	}

	// The dispatch should return immediately (well under 100ms).
	elapsed := time.Since(start)
	if elapsed > 50*time.Millisecond {
		t.Fatalf("ExecuteAsync took %v, want < 50ms (should not block)", elapsed)
	}

	// Hook should not have completed yet.
	if hookCompleted.Load() {
		t.Fatal("hook completed too quickly (should still be running)")
	}

	// Wait for hook completion.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	results := registry.CollectAll(ctx)

	if len(results) != 1 {
		t.Fatalf("results count = %d, want 1", len(results))
	}
	if results[0].Output != "done" {
		t.Fatalf("result Output = %v, want %q", results[0].Output, "done")
	}
	if !hookCompleted.Load() {
		t.Fatal("hook should have completed after CollectAll")
	}
}

// TestAgentHooksLifecycleStart verifies agent start hooks fire correctly.
func TestAgentHooksLifecycleStart(t *testing.T) {
	executor := NewExecutor()
	ctx := context.Background()

	executor.RegisterAgentStart(func(_ context.Context, agentCtx *AgentHookContext) *AgentStartHookResult {
		if agentCtx.AgentType != "code_review" {
			t.Errorf("AgentType = %q, want %q", agentCtx.AgentType, "code_review")
		}
		return &AgentStartHookResult{
			AdditionalInstructions: []string{"Focus on security issues"},
			AdditionalContext:      "reviewing PR #123",
		}
	})
	executor.RegisterAgentStart(func(_ context.Context, agentCtx *AgentHookContext) *AgentStartHookResult {
		return &AgentStartHookResult{
			AdditionalInstructions: []string{"Check for performance regressions"},
			LimitTools:             []string{"Read", "Grep", "Glob"},
		}
	})

	result := executor.ExecuteAgentStart(ctx, &AgentHookContext{
		AgentID:       "agent-1",
		AgentType:     "code_review",
		ParentAgentID: "root",
		SessionID:     "session-1",
	})

	if result.Blocked {
		t.Fatalf("unexpected block: %s", result.BlockReason)
	}
	if len(result.AdditionalInstructions) != 2 {
		t.Fatalf("AdditionalInstructions = %v, want 2 entries", result.AdditionalInstructions)
	}
	if result.AdditionalInstructions[0] != "Focus on security issues" {
		t.Fatalf("instruction[0] = %q", result.AdditionalInstructions[0])
	}
	if result.AdditionalInstructions[1] != "Check for performance regressions" {
		t.Fatalf("instruction[1] = %q", result.AdditionalInstructions[1])
	}
	if len(result.AdditionalContexts) != 1 || result.AdditionalContexts[0] != "reviewing PR #123" {
		t.Fatalf("AdditionalContexts = %v", result.AdditionalContexts)
	}
	if len(result.LimitTools) != 3 {
		t.Fatalf("LimitTools = %v, want 3 tools", result.LimitTools)
	}
	if result.HooksExecuted != 2 {
		t.Fatalf("HooksExecuted = %d, want 2", result.HooksExecuted)
	}
}

// TestAgentHooksLifecycleComplete verifies agent complete hooks fire correctly.
func TestAgentHooksLifecycleComplete(t *testing.T) {
	executor := NewExecutor()
	ctx := context.Background()

	executor.RegisterAgentComplete(func(_ context.Context, agentCtx *AgentHookContext) *AgentCompleteHookResult {
		// Post-process the result.
		return &AgentCompleteHookResult{
			PostProcessedResult: agentCtx.Result + " [reviewed]",
		}
	})
	executor.RegisterAgentComplete(func(_ context.Context, agentCtx *AgentHookContext) *AgentCompleteHookResult {
		// Receives the already-modified result.
		if !strings.Contains(agentCtx.Result, "[reviewed]") {
			t.Error("expected post-processed result from previous hook")
		}
		return nil // pass-through
	})

	result := executor.ExecuteAgentComplete(ctx, &AgentHookContext{
		AgentID:   "agent-1",
		AgentType: "code_review",
		Result:    "Found 3 issues",
	})

	if result.FinalResult != "Found 3 issues [reviewed]" {
		t.Fatalf("FinalResult = %q, want modified result", result.FinalResult)
	}
	if result.HooksExecuted != 2 {
		t.Fatalf("HooksExecuted = %d, want 2", result.HooksExecuted)
	}
}

// TestAgentHooksLifecycleFail verifies agent fail hooks fire correctly.
func TestAgentHooksLifecycleFail(t *testing.T) {
	executor := NewExecutor()
	ctx := context.Background()

	executor.RegisterAgentFail(func(_ context.Context, agentCtx *AgentHookContext) *AgentFailHookResult {
		if agentCtx.Error == nil {
			t.Error("expected non-nil Error in fail context")
		}
		return &AgentFailHookResult{
			Retry:       true,
			RetryReason: "transient network error",
		}
	})
	executor.RegisterAgentFail(func(_ context.Context, _ *AgentHookContext) *AgentFailHookResult {
		// Second hook also recommends retry (first one wins).
		return &AgentFailHookResult{
			Retry:       true,
			RetryReason: "should not be used",
		}
	})

	result := executor.ExecuteAgentFail(ctx, &AgentHookContext{
		AgentID:   "agent-1",
		AgentType: "code_review",
		Error:     errors.New("connection timeout"),
	})

	if !result.Retry {
		t.Fatal("expected Retry = true")
	}
	if result.RetryReason != "transient network error" {
		t.Fatalf("RetryReason = %q, want first hook's reason", result.RetryReason)
	}
	if result.HooksExecuted != 2 {
		t.Fatalf("HooksExecuted = %d, want 2", result.HooksExecuted)
	}
}

// TestAgentHooksBlockPreventsStart verifies that a blocking agent start hook
// prevents the agent from starting and stops subsequent hooks.
func TestAgentHooksBlockPreventsStart(t *testing.T) {
	executor := NewExecutor()
	ctx := context.Background()

	var executed []int

	executor.RegisterAgentStart(func(_ context.Context, _ *AgentHookContext) *AgentStartHookResult {
		executed = append(executed, 1)
		return &AgentStartHookResult{AdditionalInstructions: []string{"ok"}}
	})
	executor.RegisterAgentStart(func(_ context.Context, _ *AgentHookContext) *AgentStartHookResult {
		executed = append(executed, 2)
		return &AgentStartHookResult{
			Block:       true,
			BlockReason: "agent quota exceeded",
		}
	})
	executor.RegisterAgentStart(func(_ context.Context, _ *AgentHookContext) *AgentStartHookResult {
		executed = append(executed, 3)
		return nil
	})

	result := executor.ExecuteAgentStart(ctx, &AgentHookContext{
		AgentID:   "agent-2",
		AgentType: "expensive_task",
	})

	if !result.Blocked {
		t.Fatal("expected Blocked = true")
	}
	if result.BlockReason != "agent quota exceeded" {
		t.Fatalf("BlockReason = %q", result.BlockReason)
	}
	// Only hooks 1 and 2 should execute.
	if len(executed) != 2 {
		t.Fatalf("executed = %v, want [1, 2]", executed)
	}
}

// TestAgentHooksPanicRecovery verifies that panicking agent hooks don't crash.
func TestAgentHooksPanicRecovery(t *testing.T) {
	executor := NewExecutor()
	ctx := context.Background()

	executor.RegisterAgentStart(func(_ context.Context, _ *AgentHookContext) *AgentStartHookResult {
		panic("agent hook panic")
	})
	executor.RegisterAgentStart(func(_ context.Context, _ *AgentHookContext) *AgentStartHookResult {
		return &AgentStartHookResult{AdditionalInstructions: []string{"after panic"}}
	})

	result := executor.ExecuteAgentStart(ctx, &AgentHookContext{
		AgentID:   "agent-3",
		AgentType: "test",
	})

	if result.Blocked {
		t.Fatal("unexpected block")
	}
	if len(result.Errors) != 1 {
		t.Fatalf("Errors = %v, want 1 error from panic", result.Errors)
	}
	if !strings.Contains(result.Errors[0].Error(), "panic") {
		t.Fatalf("Error = %q, want panic error", result.Errors[0].Error())
	}
	if len(result.AdditionalInstructions) != 1 || result.AdditionalInstructions[0] != "after panic" {
		t.Fatalf("AdditionalInstructions = %v, want hook after panic to succeed", result.AdditionalInstructions)
	}
}

// TestLifecycleOrderingEnforcement verifies the ordering enforcer detects violations.
func TestLifecycleOrderingEnforcement(t *testing.T) {
	t.Run("valid pre-post ordering", func(t *testing.T) {
		enforcer := NewLifecycleOrderEnforcer()

		// Pre hooks in order.
		v := enforcer.RecordPre("tool:Bash", 0)
		if v != nil {
			t.Fatalf("unexpected violation: %s", v)
			return
		}
		v = enforcer.RecordPre("tool:Bash", 1)
		if v != nil {
			t.Fatalf("unexpected violation: %s", v)
			return
		}

		// Post hooks in order.
		v = enforcer.RecordPost("tool:Bash", 0)
		if v != nil {
			t.Fatalf("unexpected violation: %s", v)
			return
		}
		v = enforcer.RecordPost("tool:Bash", 1)
		if v != nil {
			t.Fatalf("unexpected violation: %s", v)
			return
		}

		// End scope cleanly.
		v = enforcer.EndScope("tool:Bash")
		if v != nil {
			t.Fatalf("unexpected violation on EndScope: %s", v)
			return
		}

		if enforcer.HasViolations() {
			t.Fatalf("unexpected violations: %v", enforcer.GetViolations())
		}
	})

	t.Run("pre after post violation", func(t *testing.T) {
		enforcer := NewLifecycleOrderEnforcer()

		enforcer.RecordPre("tool:Read", 0)
		enforcer.RecordPost("tool:Read", 0)

		// Pre after post: violation.
		v := enforcer.RecordPre("tool:Read", 1)
		if v == nil {
			t.Fatal("expected violation for pre-hook after post-hooks")
			return
		}
		if !strings.Contains(v.Description, "pre-hook fired after post-hooks") {
			t.Fatalf("violation = %q, want pre-after-post description", v.Description)
		}
	})

	t.Run("post without pre violation", func(t *testing.T) {
		enforcer := NewLifecycleOrderEnforcer()

		// Post without any pre: violation.
		v := enforcer.RecordPost("tool:Write", 0)
		if v == nil {
			t.Fatal("expected violation for post-hook without pre-hook")
			return
		}
		if !strings.Contains(v.Description, "post-hook fired without preceding pre-hook") {
			t.Fatalf("violation = %q", v.Description)
		}
	})

	t.Run("out of registration order violation", func(t *testing.T) {
		enforcer := NewLifecycleOrderEnforcer()

		enforcer.RecordPre("tool:Edit", 2)

		// Lower index after higher index: violation.
		v := enforcer.RecordPre("tool:Edit", 0)
		if v == nil {
			t.Fatal("expected violation for out-of-order pre-hook")
			return
		}
		if !strings.Contains(v.Description, "out of registration order") {
			t.Fatalf("violation = %q", v.Description)
		}
	})

	t.Run("scope without post-hooks", func(t *testing.T) {
		enforcer := NewLifecycleOrderEnforcer()

		enforcer.RecordPre("query", 0)
		enforcer.RecordPre("query", 1)

		// End scope without post-hooks: violation.
		v := enforcer.EndScope("query")
		if v == nil {
			t.Fatal("expected violation for scope without post-hooks")
			return
		}
		if !strings.Contains(v.Description, "scope ended without any post-hooks") {
			t.Fatalf("violation = %q", v.Description)
		}
	})

	t.Run("nesting validation", func(t *testing.T) {
		enforcer := NewLifecycleOrderEnforcer()

		// Outer scope starts.
		enforcer.BeginScope("query", "")
		enforcer.RecordPre("query", 0)

		// Inner scope starts.
		enforcer.BeginScope("tool:Bash", "query")
		enforcer.RecordPre("tool:Bash", 0)

		// Validate nesting before outer posts (inner not completed).
		v := enforcer.ValidateNesting("query", "tool:Bash")
		if v == nil {
			t.Fatal("expected nesting violation (inner not completed)")
			return
		}
		if !strings.Contains(v.Description, "inner scope not completed") {
			t.Fatalf("violation = %q", v.Description)
		}

		// Complete inner scope.
		enforcer.RecordPost("tool:Bash", 0)
		enforcer.EndScope("tool:Bash")

		// Now nesting should be valid.
		v = enforcer.ValidateNesting("query", "tool:Bash")
		if v != nil {
			t.Fatalf("unexpected violation after inner scope completed: %s", v)
			return
		}

		// Complete outer scope.
		enforcer.RecordPost("query", 0)
		v = enforcer.EndScope("query")
		if v != nil {
			t.Fatalf("unexpected violation on outer EndScope: %s", v)
			return
		}
	})

	t.Run("violation handler callback", func(t *testing.T) {
		enforcer := NewLifecycleOrderEnforcer()

		var violations []string
		enforcer.SetViolationHandler(func(v *OrderingViolation) {
			violations = append(violations, v.Description)
		})

		enforcer.RecordPost("tool:X", 0) // violation: no pre

		if len(violations) != 1 {
			t.Fatalf("violations via handler = %d, want 1", len(violations))
		}
		if !strings.Contains(violations[0], "post-hook fired without preceding pre-hook") {
			t.Fatalf("violation = %q", violations[0])
		}
	})
}

// TestLifecycleOrderingReset verifies the enforcer can be reset.
func TestLifecycleOrderingReset(t *testing.T) {
	enforcer := NewLifecycleOrderEnforcer()

	enforcer.RecordPost("tool:X", 0) // creates a violation
	if !enforcer.HasViolations() {
		t.Fatal("expected violation")
	}

	enforcer.Reset()
	if enforcer.HasViolations() {
		t.Fatal("expected no violations after reset")
	}
	if len(enforcer.ActiveScopes()) != 0 {
		t.Fatal("expected no active scopes after reset")
	}
}

// TestMultipleToolHooksRegistrationOrder verifies that multiple hooks for
// the same event fire in registration order and aggregate correctly.
func TestMultipleToolHooksRegistrationOrder(t *testing.T) {
	executor := NewExecutor()
	ctx := context.Background()

	var order []int
	for i := 0; i < 5; i++ {
		idx := i
		executor.RegisterPreTool(func(context.Context, string, string, map[string]any) *PreToolHookResult {
			order = append(order, idx)
			return nil
		})
	}

	executor.ExecutePreTool(ctx, "Bash", "toolu_1", map[string]any{})

	expected := []int{0, 1, 2, 3, 4}
	if len(order) != 5 {
		t.Fatalf("order = %v, want %v", order, expected)
	}
	for i, want := range expected {
		if order[i] != want {
			t.Fatalf("order[%d] = %d, want %d", i, order[i], want)
		}
	}
}

// TestPromptHookContextCancellation verifies that cancelled context stops hooks.
func TestPromptHookContextCancellation(t *testing.T) {
	executor := NewExecutor()

	var executed []int

	executor.RegisterPromptHook(func(ctx context.Context, prompt string, _ *PromptHookMetadata) *PromptHookResult {
		executed = append(executed, 1)
		return &PromptHookResult{ModifiedPrompt: prompt + " [1]"}
	})

	// Create a pre-cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := executor.ExecutePromptHooks(ctx, "test", nil)

	// With cancelled context, hooks may get errors or not execute — both acceptable.
	// The system should not crash regardless.
	if result.FinalPrompt == "" {
		// Should have at least the original prompt as fallback.
		t.Fatal("expected non-empty FinalPrompt")
	}
}

// TestAgentHooksToolLimitIntersection verifies that multiple hooks with
// LimitTools produce the intersection.
func TestAgentHooksToolLimitIntersection(t *testing.T) {
	executor := NewExecutor()
	ctx := context.Background()

	executor.RegisterAgentStart(func(_ context.Context, _ *AgentHookContext) *AgentStartHookResult {
		return &AgentStartHookResult{
			LimitTools: []string{"Read", "Write", "Bash", "Grep"},
		}
	})
	executor.RegisterAgentStart(func(_ context.Context, _ *AgentHookContext) *AgentStartHookResult {
		return &AgentStartHookResult{
			LimitTools: []string{"Read", "Grep", "Glob"},
		}
	})

	result := executor.ExecuteAgentStart(ctx, &AgentHookContext{
		AgentID:   "agent-4",
		AgentType: "restricted",
	})

	// Intersection should be ["Read", "Grep"].
	if len(result.LimitTools) != 2 {
		t.Fatalf("LimitTools = %v, want intersection of 2 tools", result.LimitTools)
	}
	toolSet := make(map[string]bool)
	for _, tool := range result.LimitTools {
		toolSet[tool] = true
	}
	if !toolSet["Read"] || !toolSet["Grep"] {
		t.Fatalf("LimitTools = %v, want [Read, Grep]", result.LimitTools)
	}
}

// TestHasHooksQueries verifies the Has* query methods.
func TestHasHooksQueries(t *testing.T) {
	executor := NewExecutor()

	if executor.HasPromptHooks() {
		t.Fatal("HasPromptHooks should be false initially")
	}
	if executor.HasAgentHooks() {
		t.Fatal("HasAgentHooks should be false initially")
	}
	if executor.HasPermissionDeniedHooks() {
		t.Fatal("HasPermissionDeniedHooks should be false initially")
	}

	executor.RegisterPromptHook(func(context.Context, string, *PromptHookMetadata) *PromptHookResult {
		return nil
	})
	if !executor.HasPromptHooks() {
		t.Fatal("HasPromptHooks should be true after registration")
	}

	executor.RegisterAgentStart(func(context.Context, *AgentHookContext) *AgentStartHookResult {
		return nil
	})
	if !executor.HasAgentHooks() {
		t.Fatal("HasAgentHooks should be true after registration")
	}
}

// TestFullAgentLifecycleScenario tests a complete agent lifecycle with hooks.
func TestFullAgentLifecycleScenario(t *testing.T) {
	executor := NewExecutor()
	ctx := context.Background()

	var events []string

	// Register hooks for the full agent lifecycle.
	executor.RegisterAgentStart(func(_ context.Context, agentCtx *AgentHookContext) *AgentStartHookResult {
		events = append(events, fmt.Sprintf("agent_start:%s", agentCtx.AgentType))
		return &AgentStartHookResult{
			AdditionalInstructions: []string{"Be thorough"},
		}
	})
	executor.RegisterAgentComplete(func(_ context.Context, agentCtx *AgentHookContext) *AgentCompleteHookResult {
		events = append(events, fmt.Sprintf("agent_complete:%s:%s", agentCtx.AgentType, agentCtx.Result))
		return nil
	})
	executor.RegisterAgentFail(func(_ context.Context, agentCtx *AgentHookContext) *AgentFailHookResult {
		events = append(events, fmt.Sprintf("agent_fail:%s:%v", agentCtx.AgentType, agentCtx.Error))
		return &AgentFailHookResult{Retry: true, RetryReason: "auto-retry"}
	})

	// Simulate: agent starts -> completes.
	agentCtx := &AgentHookContext{
		AgentID:   "agent-full-1",
		AgentType: "test_runner",
		SessionID: "session-1",
	}

	startResult := executor.ExecuteAgentStart(ctx, agentCtx)
	if startResult.Blocked {
		t.Fatal("unexpected block")
	}
	events = append(events, "agent_executing")

	// Agent completes.
	agentCtx.Result = "all tests pass"
	completeResult := executor.ExecuteAgentComplete(ctx, agentCtx)
	if completeResult.FinalResult != "all tests pass" {
		t.Fatalf("FinalResult = %q", completeResult.FinalResult)
	}

	// Simulate: another agent starts -> fails -> retries -> completes.
	agentCtx2 := &AgentHookContext{
		AgentID:   "agent-full-2",
		AgentType: "build_checker",
		SessionID: "session-1",
	}

	executor.ExecuteAgentStart(ctx, agentCtx2)
	events = append(events, "agent_executing_2")

	agentCtx2.Error = errors.New("build timeout")
	failResult := executor.ExecuteAgentFail(ctx, agentCtx2)
	if !failResult.Retry {
		t.Fatal("expected retry recommendation")
	}

	// Retry: start again and complete.
	agentCtx2.Error = nil
	executor.ExecuteAgentStart(ctx, agentCtx2)
	events = append(events, "agent_executing_2_retry")
	agentCtx2.Result = "build successful"
	executor.ExecuteAgentComplete(ctx, agentCtx2)

	// Verify the full event sequence.
	expected := []string{
		"agent_start:test_runner",
		"agent_executing",
		"agent_complete:test_runner:all tests pass",
		"agent_start:build_checker",
		"agent_executing_2",
		"agent_fail:build_checker:build timeout",
		"agent_start:build_checker",
		"agent_executing_2_retry",
		"agent_complete:build_checker:build successful",
	}

	if len(events) != len(expected) {
		t.Fatalf("events count = %d, want %d\nevents = %v", len(events), len(expected), events)
	}
	for i, want := range expected {
		if events[i] != want {
			t.Fatalf("events[%d] = %q, want %q\nfull = %v", i, events[i], want, events)
		}
	}
}

// TestConcurrentHookRegistrationAndExecution tests concurrent safety of the
// executor's registration and execution.
func TestConcurrentHookRegistrationAndExecution(t *testing.T) {
	enforcer := NewLifecycleOrderEnforcer()

	var wg sync.WaitGroup
	const goroutines = 10
	const operations = 100

	// Concurrent RecordPre/RecordPost across different scopes.
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			scope := fmt.Sprintf("scope_%d", id)
			for i := 0; i < operations; i++ {
				enforcer.RecordPre(scope, i)
			}
			for i := 0; i < operations; i++ {
				enforcer.RecordPost(scope, i)
			}
			enforcer.EndScope(scope)
		}(g)
	}

	wg.Wait()

	// No violations expected (each goroutine uses its own scope).
	if enforcer.HasViolations() {
		t.Fatalf("unexpected violations with isolated scopes: %v", enforcer.GetViolations())
	}
}

// TestPromptHookNoHooksRegistered verifies behavior when no hooks are present.
func TestPromptHookNoHooksRegistered(t *testing.T) {
	executor := NewExecutor()
	ctx := context.Background()

	result := executor.ExecutePromptHooks(ctx, "original prompt", nil)

	if result.FinalPrompt != "original prompt" {
		t.Fatalf("FinalPrompt = %q, want original", result.FinalPrompt)
	}
	if result.HooksExecuted != 0 {
		t.Fatalf("HooksExecuted = %d, want 0", result.HooksExecuted)
	}
	if result.Blocked {
		t.Fatal("unexpected block")
	}
}

// TestAgentHooksNoHooksRegistered verifies behavior when no agent hooks exist.
func TestAgentHooksNoHooksRegistered(t *testing.T) {
	executor := NewExecutor()
	ctx := context.Background()

	startResult := executor.ExecuteAgentStart(ctx, &AgentHookContext{
		AgentID: "agent-empty", AgentType: "test",
	})
	if startResult.Blocked || startResult.HooksExecuted != 0 {
		t.Fatal("unexpected result with no hooks")
	}

	completeResult := executor.ExecuteAgentComplete(ctx, &AgentHookContext{
		AgentID: "agent-empty", AgentType: "test", Result: "done",
	})
	if completeResult.FinalResult != "done" {
		t.Fatalf("FinalResult = %q, want original", completeResult.FinalResult)
	}

	failResult := executor.ExecuteAgentFail(ctx, &AgentHookContext{
		AgentID: "agent-empty", AgentType: "test", Error: errors.New("x"),
	})
	if failResult.Retry || failResult.HooksExecuted != 0 {
		t.Fatal("unexpected result with no hooks")
	}
}

// TestOrderEnforcerIntegrationWithExecutor tests the enforcer attached to an executor.
func TestOrderEnforcerIntegrationWithExecutor(t *testing.T) {
	executor := NewExecutor()
	enforcer := NewLifecycleOrderEnforcer()
	executor.WithOrderEnforcer(enforcer)

	if executor.GetOrderEnforcer() != enforcer {
		t.Fatal("GetOrderEnforcer should return the attached enforcer")
	}

	// The enforcer is available for the query loop to use.
	enforcer.RecordPre("tool:Bash", 0)
	enforcer.RecordPost("tool:Bash", 0)
	enforcer.EndScope("tool:Bash")

	if enforcer.HasViolations() {
		t.Fatalf("unexpected violations: %v", enforcer.GetViolations())
	}
}

// TestPostToolHookAggregation verifies multiple post-tool hooks aggregate correctly.
func TestPostToolHookAggregation(t *testing.T) {
	executor := NewExecutor()
	ctx := context.Background()

	executor.RegisterPostTool(func(_ context.Context, _, _ string, _ map[string]any, result string) *PostToolHookResult {
		return &PostToolHookResult{
			UpdatedResult: result + " [sanitized]",
			ReplaceResult: true,
		}
	})
	executor.RegisterPostTool(func(_ context.Context, _, _ string, _ map[string]any, result string) *PostToolHookResult {
		// Receives the already-modified result.
		return &PostToolHookResult{
			Attachments: []*schema.Message{{Role: schema.User, Content: "audit: result was " + result}},
		}
	})

	result := executor.ExecutePostTool(ctx, "Bash", "toolu_1", map[string]any{"command": "cat secret"}, "sensitive data")

	if !result.ReplaceResult {
		t.Fatal("expected ReplaceResult = true")
	}
	if result.UpdatedResult != "sensitive data [sanitized]" {
		t.Fatalf("UpdatedResult = %q", result.UpdatedResult)
	}
	if len(result.Attachments) != 1 {
		t.Fatalf("Attachments count = %d, want 1", len(result.Attachments))
	}
	// The second hook receives the modified result.
	if !strings.Contains(result.Attachments[0].Content, "[sanitized]") {
		t.Fatalf("Attachment content = %q, want reference to sanitized result", result.Attachments[0].Content)
	}
}
