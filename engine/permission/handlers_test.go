package permission

import (
	"context"
	"testing"
	"time"
)

// --- Speculative Classifier Tests ---

func TestSpeculativeClassifierStartAndPeek(t *testing.T) {
	// Pre-populate a check directly to test Peek behavior.
	sc := NewSpeculativeClassifier(nil)
	sc.mu.Lock()
	check := &speculativeCheck{done: make(chan struct{})}
	check.decision = ClassifierAllow
	close(check.done)
	sc.pending["echo hello"] = check
	sc.mu.Unlock()

	// Peek should return the pre-populated result.
	decision, ok := sc.Peek("echo hello", 1*time.Second)
	if !ok {
		t.Fatal("expected Peek to succeed within timeout")
	}
	if decision != ClassifierAllow {
		t.Errorf("expected ClassifierAllow, got %q", decision)
	}
}

func TestSpeculativeClassifierPeekTimeout(t *testing.T) {
	sc := NewSpeculativeClassifier(&ClassifierConfig{})

	// Peek for a command that was never started should timeout.
	decision, ok := sc.Peek("nonexistent", 10*time.Millisecond)
	if ok {
		t.Fatalf("expected Peek to timeout for unknown command, got %q", decision)
	}
}

func TestSpeculativeClassifierNilSafe(t *testing.T) {
	var sc *SpeculativeClassifier

	// All methods should be nil-safe.
	sc.Start(context.Background(), "Bash", "echo", nil)
	decision, ok := sc.Peek("echo", 10*time.Millisecond)
	if ok {
		t.Fatalf("expected nil classifier Peek to return false, got %q", decision)
	}
	ch := sc.Consume("echo")
	if ch != nil {
		t.Fatal("expected nil classifier Consume to return nil")
		return
	}
	sc.Cancel("echo") // should not panic
}

func TestSpeculativeClassifierConsume(t *testing.T) {
	sc := NewSpeculativeClassifier(nil)

	// Pre-populate a check.
	sc.mu.Lock()
	check := &speculativeCheck{done: make(chan struct{})}
	check.decision = ClassifierDeny
	close(check.done)
	sc.pending["pwd"] = check
	sc.mu.Unlock()

	ch := sc.Consume("pwd")
	if ch == nil {
		t.Fatal("expected Consume to return non-nil channel")
		return
	}

	// Should receive the result.
	select {
	case decision := <-ch:
		if decision != ClassifierDeny {
			t.Errorf("expected ClassifierDeny, got %q", decision)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Consume channel did not deliver result within 1s")
	}

	// Second consume for same command should return nil (already consumed).
	ch2 := sc.Consume("pwd")
	if ch2 != nil {
		t.Fatal("expected second Consume to return nil")
		return
	}
}

func TestSpeculativeClassifierOnlyBash(t *testing.T) {
	sc := NewSpeculativeClassifier(&ClassifierConfig{})
	ctx := context.Background()

	// Non-Bash tools should not start speculative checks.
	sc.Start(ctx, "Read", "/tmp/a", nil)
	decision, ok := sc.Peek("/tmp/a", 10*time.Millisecond)
	if ok {
		t.Fatalf("expected non-Bash tool to not trigger speculative, got %q", decision)
	}
}

func TestSpeculativeClassifierCancel(t *testing.T) {
	sc := NewSpeculativeClassifier(&ClassifierConfig{})
	ctx := context.Background()

	sc.Start(ctx, "Bash", "rm -rf /", nil)
	sc.Cancel("rm -rf /")

	// After cancel, Peek should not find it.
	_, ok := sc.Peek("rm -rf /", 10*time.Millisecond)
	if ok {
		t.Fatal("expected cancelled check to not be peekable")
	}
}

// --- Interactive Handler Tests ---

func TestInteractiveHandlerUserResolves(t *testing.T) {
	prompter := NewPermissionPrompter(nil)
	handler := &InteractiveHandler{
		Prompter:   prompter,
		GraceDelay: 10 * time.Millisecond,
	}

	ctx := context.Background()

	// Simulate user resolving after a short delay.
	go func() {
		time.Sleep(20 * time.Millisecond)
		prompter.Resolve("call_1", PermissionResult{
			Decision: DecisionAllow,
			Reason:   ReasonPermissionPrompt,
			Message:  "user approved",
			ToolName: "Bash",
		})
	}()

	result := handler.HandlePermission(ctx, "Bash", "call_1", map[string]any{"command": "ls"}, "run ls?", "")
	if result.Decision != DecisionAllow {
		t.Errorf("expected allow, got %q", result.Decision)
	}
	if result.Source != ApprovalSourceUser {
		t.Errorf("expected source user, got %q", result.Source)
	}
}

func TestInteractiveHandlerClassifierWins(t *testing.T) {
	prompter := NewPermissionPrompter(nil)
	// Create a speculative classifier that will return Allow quickly.
	sc := NewSpeculativeClassifier(&ClassifierConfig{})
	// We pre-populate a "done" check that returns Allow.
	sc.mu.Lock()
	check := &speculativeCheck{done: make(chan struct{})}
	check.decision = ClassifierAllow
	close(check.done) // already resolved
	sc.pending["ls -la"] = check
	sc.mu.Unlock()

	handler := &InteractiveHandler{
		Prompter:    prompter,
		Speculative: sc,
		GraceDelay:  10 * time.Millisecond, // short grace for test
	}

	ctx := context.Background()
	result := handler.HandlePermission(ctx, "Bash", "call_2", map[string]any{"command": "ls -la"}, "run ls -la?", "ls -la")

	if result.Decision != DecisionAllow {
		t.Errorf("expected allow from classifier, got %q", result.Decision)
	}
	if result.Source != ApprovalSourceClassifier {
		t.Errorf("expected source classifier, got %q", result.Source)
	}
}

func TestInteractiveHandlerContextCancellation(t *testing.T) {
	prompter := NewPermissionPrompter(nil)
	handler := &InteractiveHandler{
		Prompter:   prompter,
		GraceDelay: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	result := handler.HandlePermission(ctx, "Bash", "call_3", map[string]any{"command": "rm"}, "run rm?", "")
	if result.Decision != DecisionDeny {
		t.Errorf("expected deny on cancellation, got %q", result.Decision)
	}
}

// --- Coordinator Handler Tests ---

func TestCoordinatorHandlerHookResolves(t *testing.T) {
	handler := &CoordinatorHandler{
		RunHooks: func(ctx context.Context, toolName string, input map[string]any) *PermissionResult {
			return &PermissionResult{
				Decision: DecisionAllow,
				Reason:   ReasonHook,
				Message:  "hook approved",
				ToolName: toolName,
			}
		},
	}

	req := &PermissionRequest{
		ToolName:  "Bash",
		ToolUseID: "call_1",
		Input:     map[string]any{"command": "echo hi"},
	}

	result := handler.HandleAsk(context.Background(), req, nil)
	if result == nil {
		t.Fatal("expected non-nil result from hook")
		return
	}
	if result.Decision != DecisionAllow || result.Reason != ReasonHook {
		t.Errorf("expected hook allow, got %+v", result)
	}
}

func TestCoordinatorHandlerClassifierResolves(t *testing.T) {
	// No hooks, but speculative classifier has result.
	sc := NewSpeculativeClassifier(&ClassifierConfig{})
	sc.mu.Lock()
	check := &speculativeCheck{done: make(chan struct{})}
	check.decision = ClassifierAllow
	close(check.done)
	sc.pending["echo hi"] = check
	sc.mu.Unlock()

	handler := &CoordinatorHandler{}

	req := &PermissionRequest{
		ToolName:  "Bash",
		ToolUseID: "call_2",
		Input:     map[string]any{"command": "echo hi"},
	}

	result := handler.HandleAsk(context.Background(), req, sc)
	if result == nil {
		t.Fatal("expected non-nil result from classifier")
		return
	}
	if result.Decision != DecisionAllow || result.Reason != ReasonClassifier {
		t.Errorf("expected classifier allow, got %+v", result)
	}
}

func TestCoordinatorHandlerFallsThrough(t *testing.T) {
	// No hooks, no classifier, no speculative → should return nil.
	handler := &CoordinatorHandler{}

	req := &PermissionRequest{
		ToolName:  "Bash",
		ToolUseID: "call_3",
		Input:     map[string]any{"command": "rm -rf /"},
	}

	result := handler.HandleAsk(context.Background(), req, nil)
	if result != nil {
		t.Errorf("expected nil (fall through), got %+v", result)
	}
}

// --- Swarm Worker Handler Tests ---

func TestSwarmWorkerHandlerClassifierApproves(t *testing.T) {
	sc := NewSpeculativeClassifier(&ClassifierConfig{})
	sc.mu.Lock()
	check := &speculativeCheck{done: make(chan struct{})}
	check.decision = ClassifierAllow
	close(check.done)
	sc.pending["echo test"] = check
	sc.mu.Unlock()

	handler := &SwarmWorkerHandler{}

	req := &PermissionRequest{
		ToolName:  "Bash",
		ToolUseID: "call_1",
		Input:     map[string]any{"command": "echo test"},
	}

	result := handler.HandleAsk(context.Background(), req, sc)
	if result == nil {
		t.Fatal("expected non-nil result")
		return
	}
	if result.Decision != DecisionAllow {
		t.Errorf("expected allow, got %q", result.Decision)
	}
}

func TestSwarmWorkerHandlerMailboxDelegation(t *testing.T) {
	mailbox := &mockMailbox{
		responseCh: make(chan PermissionResult, 1),
	}
	mailbox.responseCh <- PermissionResult{
		Decision: DecisionAllow,
		Reason:   ReasonPermissionPrompt,
		Message:  "leader approved",
		ToolName: "Bash",
	}

	handler := &SwarmWorkerHandler{
		Mailbox: mailbox,
	}

	req := &PermissionRequest{
		ToolName:  "Bash",
		ToolUseID: "call_2",
		Input:     map[string]any{"command": "dangerous cmd"},
	}

	result := handler.HandleAsk(context.Background(), req, nil)
	if result == nil {
		t.Fatal("expected non-nil result from mailbox")
		return
	}
	if result.Decision != DecisionAllow {
		t.Errorf("expected allow from leader, got %q", result.Decision)
	}
	if !mailbox.sent {
		t.Error("expected Send to be called")
	}
}

func TestSwarmWorkerHandlerNoMailboxFallsThrough(t *testing.T) {
	handler := &SwarmWorkerHandler{}

	req := &PermissionRequest{
		ToolName:  "Write",
		ToolUseID: "call_3",
		Input:     map[string]any{"file_path": "/tmp/x"},
	}

	result := handler.HandleAsk(context.Background(), req, nil)
	if result != nil {
		t.Errorf("expected nil (fall through) with no mailbox, got %+v", result)
	}
}

// mockMailbox implements PermissionMailbox for testing.
type mockMailbox struct {
	sent       bool
	responseCh chan PermissionResult
}

func (m *mockMailbox) Send(req *PermissionRequest) error {
	m.sent = true
	return nil
}

func (m *mockMailbox) Receive(toolUseID string) <-chan PermissionResult {
	return m.responseCh
}
