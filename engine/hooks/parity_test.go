package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Config Loading parity tests
// ---------------------------------------------------------------------------

func TestConfigLoadingJSONToRegistration(t *testing.T) {
	configJSON := `{
		"PreToolUse": [
			{
				"matcher": "Bash",
				"hooks": [
					{"type": "command", "command": "echo hello"}
				]
			}
		],
		"PostToolUse": [
			{
				"matcher": "",
				"hooks": [
					{"type": "http", "url": "http://localhost/hook"}
				]
			}
		]
	}`

	cfg, err := ParseHooksConfig([]byte(configJSON))
	if err != nil {
		t.Fatalf("ParseHooksConfig: %v", err)
		return
	}

	if len(cfg.Events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(cfg.Events))
	}

	preMatchers := cfg.Events[HookEventPreToolUse]
	if len(preMatchers) != 1 {
		t.Fatalf("expected 1 PreToolUse matcher, got %d", len(preMatchers))
	}
	if preMatchers[0].Matcher != "Bash" {
		t.Fatalf("expected matcher 'Bash', got %q", preMatchers[0].Matcher)
	}
	if preMatchers[0].Hooks[0].Type != HookCommandTypeCommand {
		t.Fatalf("expected command type, got %q", preMatchers[0].Hooks[0].Type)
	}
}

func TestConfigLoadingSkipsUnknownEvents(t *testing.T) {
	configJSON := `{
		"PreToolUse": [{"matcher": "", "hooks": [{"type": "command", "command": "echo"}]}],
		"FutureUnknownEvent": [{"matcher": "", "hooks": [{"type": "command", "command": "echo"}]}]
	}`

	cfg, err := ParseHooksConfig([]byte(configJSON))
	if err != nil {
		t.Fatalf("ParseHooksConfig: %v", err)
		return
	}

	// Unknown events should be skipped, only PreToolUse remains
	if len(cfg.Events) != 1 {
		t.Fatalf("expected 1 event (unknown skipped), got %d", len(cfg.Events))
	}
}

// ---------------------------------------------------------------------------
// Event Matcher parity tests
// ---------------------------------------------------------------------------

func TestEventMatcherExactMatch(t *testing.T) {
	if !matchEventPattern("Bash", "Bash") {
		t.Fatal("exact match should return true")
	}
	if matchEventPattern("Bash", "Read") {
		t.Fatal("exact match for different values should return false")
	}
}

func TestEventMatcherGlobPattern(t *testing.T) {
	if !matchEventPattern("Bash*", "BashCommand") {
		t.Fatal("glob prefix should match")
	}
	if !matchEventPattern("*Edit", "MultiEdit") {
		t.Fatal("glob suffix should match")
	}
	if matchEventPattern("Bash*", "Read") {
		t.Fatal("glob should not match unrelated value")
	}
}

func TestEventMatcherPipeSeparated(t *testing.T) {
	if !matchEventPattern("Read|Write|Edit", "Write") {
		t.Fatal("pipe-separated should match included value")
	}
	if matchEventPattern("Read|Write|Edit", "Bash") {
		t.Fatal("pipe-separated should not match excluded value")
	}
}

func TestEventMatcherEmptyMatchesAll(t *testing.T) {
	if !matchEventPattern("", "anything") {
		t.Fatal("empty pattern should match everything")
	}
}

// ---------------------------------------------------------------------------
// HTTP Executor parity tests
// ---------------------------------------------------------------------------

func TestHTTPExecutorPostWithPayload(t *testing.T) {
	var receivedBody string
	var receivedContentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		receivedBody = string(body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "ok"}`))
	}))
	defer srv.Close()

	executor := NewHTTPHookExecutor()
	hook := &HookCommand{
		Type: HookCommandTypeHTTP,
		URL:  srv.URL,
	}

	payload := map[string]any{"tool": "Bash", "input": "ls"}
	result := executor.ExecuteWithPayload(context.Background(), hook, payload)

	if !result.OK {
		t.Fatalf("expected OK, got error: %s", result.Error)
	}
	if receivedContentType != "application/json" {
		t.Fatalf("expected application/json, got %q", receivedContentType)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(receivedBody), &parsed); err != nil {
		t.Fatalf("received body is not valid JSON: %v", err)
		return
	}
	if parsed["tool"] != "Bash" {
		t.Fatalf("expected tool=Bash in payload, got %v", parsed["tool"])
	}
}

func TestHTTPExecutorEnvVarInterpolation(t *testing.T) {
	_ = os.Setenv("TEST_HOOK_TOKEN", "secret123")
	defer os.Unsetenv("TEST_HOOK_TOKEN") //nolint:errcheck

	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	executor := NewHTTPHookExecutor()
	hook := &HookCommand{
		Type:           HookCommandTypeHTTP,
		URL:            srv.URL,
		Headers:        map[string]string{"Authorization": "Bearer $TEST_HOOK_TOKEN"},
		AllowedEnvVars: []string{"TEST_HOOK_TOKEN"},
	}

	result := executor.Execute(context.Background(), hook, `{}`)
	if !result.OK {
		t.Fatalf("expected OK, got error: %s", result.Error)
	}
	if receivedAuth != "Bearer secret123" {
		t.Fatalf("expected 'Bearer secret123', got %q", receivedAuth)
	}
}

func TestHTTPExecutorCRLFProtection(t *testing.T) {
	// Verify that CRLF characters are stripped from interpolated header values
	result := sanitizeHeaderValue("value\r\ninjected-header: evil")
	if result != "valueinjected-header: evil" {
		t.Fatalf("expected CRLF stripped, got %q", result)
	}
	// NUL should also be stripped
	result2 := sanitizeHeaderValue("normal\x00value")
	if result2 != "normalvalue" {
		t.Fatalf("expected NUL stripped, got %q", result2)
	}
}

// ---------------------------------------------------------------------------
// Async Registry parity tests
// ---------------------------------------------------------------------------

func TestAsyncRegistryFireAndForgetParity(t *testing.T) {
	reg := NewAsyncRegistry()
	var executed atomic.Bool

	future, err := reg.ExecuteAsync(context.Background(), "test", func(ctx context.Context) (any, error) {
		executed.Store(true)
		return nil, nil
	})
	if err != nil {
		t.Fatalf("ExecuteAsync: %v", err)
		return
	}

	// Wait for completion
	_, err = future.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
		return
	}
	if !executed.Load() {
		t.Fatal("hook should have executed")
	}
	if future.Status() != HookFutureStatusCompleted {
		t.Fatalf("expected completed, got %s", future.Status())
	}
}

func TestAsyncRegistryFireAndCollect(t *testing.T) {
	reg := NewAsyncRegistry()

	for i := 0; i < 5; i++ {
		val := i
		_, _ = reg.ExecuteAsync(context.Background(), "collect", func(ctx context.Context) (any, error) {
			return val, nil
		})
	}

	results := reg.CollectAll(context.Background())
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}
}

func TestAsyncRegistryShutdownSemantics(t *testing.T) {
	reg := NewAsyncRegistry()

	// Dispatch a slow hook
	_, _ = reg.ExecuteAsync(context.Background(), "slow", func(ctx context.Context) (any, error) {
		select {
		case <-time.After(50 * time.Millisecond):
			return "done", nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})

	// Shutdown with sufficient timeout
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := reg.Shutdown(ctx)
	if err != nil {
		t.Fatalf("Shutdown should succeed with sufficient timeout: %v", err)
		return
	}

	// After shutdown, new dispatches should fail
	_, err = reg.ExecuteAsync(context.Background(), "after_shutdown", func(ctx context.Context) (any, error) {
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected error dispatching after shutdown")
		return
	}
}

// ---------------------------------------------------------------------------
// Prompt Hooks parity tests
// ---------------------------------------------------------------------------

func TestPromptHookChainModification(t *testing.T) {
	exec := NewExecutor()

	// Hook 1: append context
	exec.RegisterPromptHook(func(ctx context.Context, prompt string, meta *PromptHookMetadata) *PromptHookResult {
		return &PromptHookResult{
			ModifiedPrompt: prompt + "\nAdded by hook 1",
		}
	})

	// Hook 2: append more
	exec.RegisterPromptHook(func(ctx context.Context, prompt string, meta *PromptHookMetadata) *PromptHookResult {
		return &PromptHookResult{
			ModifiedPrompt: prompt + "\nAdded by hook 2",
		}
	})

	result := exec.ExecutePromptHooks(context.Background(), "Original prompt", nil)
	if result.Blocked {
		t.Fatal("should not be blocked")
	}
	if result.HooksExecuted != 2 {
		t.Fatalf("expected 2 hooks executed, got %d", result.HooksExecuted)
	}
	expected := "Original prompt\nAdded by hook 1\nAdded by hook 2"
	if result.FinalPrompt != expected {
		t.Fatalf("expected %q, got %q", expected, result.FinalPrompt)
	}
}

func TestPromptHookPanicRecovery(t *testing.T) {
	exec := NewExecutor()

	// Hook 1: panics
	exec.RegisterPromptHook(func(ctx context.Context, prompt string, meta *PromptHookMetadata) *PromptHookResult {
		panic("hook exploded")
	})

	// Hook 2: succeeds
	exec.RegisterPromptHook(func(ctx context.Context, prompt string, meta *PromptHookMetadata) *PromptHookResult {
		return &PromptHookResult{ModifiedPrompt: "recovered prompt"}
	})

	result := exec.ExecutePromptHooks(context.Background(), "original", nil)
	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 error from panic, got %d", len(result.Errors))
	}
	// Despite panic, second hook should have run and the prompt should be modified
	if result.FinalPrompt != "recovered prompt" {
		t.Fatalf("expected 'recovered prompt', got %q", result.FinalPrompt)
	}
}

// ---------------------------------------------------------------------------
// Agent Hooks parity tests
// ---------------------------------------------------------------------------

func TestAgentHookStartLifecycle(t *testing.T) {
	exec := NewExecutor()

	exec.RegisterAgentStart(func(ctx context.Context, agentCtx *AgentHookContext) *AgentStartHookResult {
		return &AgentStartHookResult{
			AdditionalInstructions: []string{"Be concise."},
			AdditionalContext:      "Focus on Go code.",
		}
	})

	agentCtx := &AgentHookContext{
		AgentID:   "agent-1",
		AgentType: "reviewer",
		SessionID: "session-1",
	}

	result := exec.ExecuteAgentStart(context.Background(), agentCtx)
	if result.Blocked {
		t.Fatal("should not be blocked")
	}
	if len(result.AdditionalInstructions) != 1 {
		t.Fatalf("expected 1 instruction, got %d", len(result.AdditionalInstructions))
	}
	if result.AdditionalInstructions[0] != "Be concise." {
		t.Fatalf("expected 'Be concise.', got %q", result.AdditionalInstructions[0])
	}
}

func TestAgentHookCompletePostProcessing(t *testing.T) {
	exec := NewExecutor()

	exec.RegisterAgentComplete(func(ctx context.Context, agentCtx *AgentHookContext) *AgentCompleteHookResult {
		return &AgentCompleteHookResult{
			PostProcessedResult: "## Summary\n" + agentCtx.Result,
		}
	})

	agentCtx := &AgentHookContext{
		AgentID: "agent-1",
		Result:  "Found 3 bugs.",
	}

	result := exec.ExecuteAgentComplete(context.Background(), agentCtx)
	expected := "## Summary\nFound 3 bugs."
	if result.FinalResult != expected {
		t.Fatalf("expected %q, got %q", expected, result.FinalResult)
	}
}

func TestAgentHookFailRetry(t *testing.T) {
	exec := NewExecutor()

	exec.RegisterAgentFail(func(ctx context.Context, agentCtx *AgentHookContext) *AgentFailHookResult {
		return &AgentFailHookResult{
			Retry:       true,
			RetryReason: "transient network error",
		}
	})

	agentCtx := &AgentHookContext{
		AgentID: "agent-1",
		Error:   fmt.Errorf("connection reset"),
	}

	result := exec.ExecuteAgentFail(context.Background(), agentCtx)
	if !result.Retry {
		t.Fatal("expected retry recommendation")
	}
	if result.RetryReason != "transient network error" {
		t.Fatalf("expected 'transient network error', got %q", result.RetryReason)
	}
}

// ---------------------------------------------------------------------------
// Ordering Enforcer parity tests
// ---------------------------------------------------------------------------

func TestOrderingEnforcerPreBeforePost(t *testing.T) {
	enforcer := NewLifecycleOrderEnforcer()

	// Normal flow: pre(0) → pre(1) → post(0) → post(1)
	if v := enforcer.RecordPre("tool:Bash", 0); v != nil {
		t.Fatalf("unexpected violation: %v", v)
		return
	}
	if v := enforcer.RecordPre("tool:Bash", 1); v != nil {
		t.Fatalf("unexpected violation: %v", v)
		return
	}
	if v := enforcer.RecordPost("tool:Bash", 0); v != nil {
		t.Fatalf("unexpected violation: %v", v)
		return
	}
	if v := enforcer.RecordPost("tool:Bash", 1); v != nil {
		t.Fatalf("unexpected violation: %v", v)
		return
	}

	if enforcer.HasViolations() {
		t.Fatal("expected no violations for correct ordering")
	}
}

func TestOrderingEnforcerViolationDetection(t *testing.T) {
	enforcer := NewLifecycleOrderEnforcer()

	// Fire post without any pre — violation
	v := enforcer.RecordPost("tool:Read", 0)
	if v == nil {
		t.Fatal("expected violation for post without pre")
		return
	}
	if enforcer.ViolationCount() != 1 {
		t.Fatalf("expected 1 violation, got %d", enforcer.ViolationCount())
	}
}

func TestOrderingEnforcerNestedScope(t *testing.T) {
	enforcer := NewLifecycleOrderEnforcer()

	// Outer scope starts
	enforcer.BeginScope("query", "")
	enforcer.RecordPre("query", 0)

	// Inner scope starts and completes
	enforcer.BeginScope("tool:Bash", "query")
	enforcer.RecordPre("tool:Bash", 0)
	enforcer.RecordPost("tool:Bash", 0)
	enforcer.EndScope("tool:Bash")

	// Validate nesting before outer post
	v := enforcer.ValidateNesting("query", "tool:Bash")
	if v != nil {
		t.Fatalf("expected no nesting violation, got: %v", v)
		return
	}

	// Outer scope completes
	enforcer.RecordPost("query", 0)
	enforcer.EndScope("query")

	if enforcer.HasViolations() {
		t.Fatalf("expected no violations, got %d", enforcer.ViolationCount())
	}
}
