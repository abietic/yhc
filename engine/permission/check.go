package permission

import (
	"context"
	"sync"
	"sync/atomic"
)

// PermissionDecision represents the outcome of a permission check.
// Mirrors reference types/permissions.ts PermissionDecisionType.
type PermissionDecision string

const (
	DecisionAllow PermissionDecision = "allow"
	DecisionDeny  PermissionDecision = "deny"
	DecisionAsk   PermissionDecision = "ask"
)

// PermissionReason indicates why a permission decision was made.
// Mirrors reference types/permissions.ts PermissionDecisionReason.
type PermissionReason string

const (
	ReasonRule             PermissionReason = "rule"
	ReasonMode             PermissionReason = "mode"
	ReasonSafetyCheck      PermissionReason = "safety_check"
	ReasonClassifier       PermissionReason = "classifier"
	ReasonHook             PermissionReason = "hook"
	ReasonSandboxOverride  PermissionReason = "sandbox_override"
	ReasonSubcommand       PermissionReason = "subcommand_results"
	ReasonToolSpecific     PermissionReason = "tool_specific"
	ReasonWorkingDir       PermissionReason = "working_dir"
	ReasonAsyncAgent       PermissionReason = "async_agent"
	ReasonPermissionPrompt PermissionReason = "permission_prompt"
	ReasonOther            PermissionReason = "other"
)

// PermissionResult is the discriminated union return from permission checks.
// Mirrors reference types/permissions.ts PermissionResult.
type PermissionResult struct {
	Decision     PermissionDecision
	Reason       PermissionReason
	Message      string             // human-readable explanation
	UpdatedInput map[string]any     // optionally modified input from hooks
	RuleName     string             // which rule matched, if any
	IsBypassed   bool               // true if bypassPermissions mode
	ToolName     string             // the tool that was checked
	SubResults   []PermissionResult // for subcommand-level results
}

// IsAllowed returns whether the result permits tool execution.
func (r PermissionResult) IsAllowed() bool {
	return r.Decision == DecisionAllow
}

// IsDenied returns whether the result explicitly denies tool execution.
func (r PermissionResult) IsDenied() bool {
	return r.Decision == DecisionDeny
}

// NeedsAsk returns whether the result requires interactive prompting.
func (r PermissionResult) NeedsAsk() bool {
	return r.Decision == DecisionAsk
}

// CanUseTool is the permission check function signature.
// Returns a PermissionResult with the full decision context.
type CanUseTool func(ctx context.Context, toolName string, input map[string]any) PermissionResult

// SimpleCanUseTool adapts a simple (bool, string) check function to CanUseTool.
// This provides backward compatibility with existing code.
func SimpleCanUseTool(fn func(ctx context.Context, toolName string, input map[string]any) (bool, string)) CanUseTool {
	return func(ctx context.Context, toolName string, input map[string]any) PermissionResult {
		allowed, reason := fn(ctx, toolName, input)
		if allowed {
			return PermissionResult{
				Decision: DecisionAllow,
				Reason:   ReasonOther,
				Message:  reason,
				ToolName: toolName,
			}
		}
		return PermissionResult{
			Decision: DecisionDeny,
			Reason:   ReasonOther,
			Message:  reason,
			ToolName: toolName,
		}
	}
}

// Checker wraps CanUseTool to track permission denials.
// Mirrors QueryEngine.ts:244-271.
type Checker struct {
	Denials []Denial
	inner   CanUseTool
}

// Denial records a single permission denial.
type Denial struct {
	ToolName  string
	ToolUseID string
	Input     map[string]any
}

// NewChecker creates a permission checker.
func NewChecker(inner CanUseTool) *Checker {
	return &Checker{inner: inner}
}

// Check runs the permission check and tracks denials.
func (c *Checker) Check(ctx context.Context, toolName, toolUseID string, input map[string]any) PermissionResult {
	result := c.inner(ctx, toolName, input)
	if result.IsDenied() {
		c.Denials = append(c.Denials, Denial{
			ToolName:  toolName,
			ToolUseID: toolUseID,
			Input:     input,
		})
	}
	return result
}

// Reset clears denial tracking for a new turn.
func (c *Checker) Reset() { c.Denials = nil }

// GetDenials returns permission denials for the current turn.
func (c *Checker) GetDenials() []Denial { return c.Denials }

// --- Interactive Permission Prompting ---

// PermissionRequest represents a pending interactive permission prompt.
// The engine blocks on the Response channel until the UI resolves it.
// Mirrors the Promise-based pattern in useCanUseTool.tsx.
type PermissionRequest struct {
	ToolName  string
	ToolUseID string
	Input     map[string]any
	Message   string // description of what the tool wants to do

	// Response receives the user's decision. Buffered channel (cap 1).
	Response chan PermissionResult
}

// PermissionPrompter manages interactive permission requests.
// The engine sends requests via RequestPermission; the UI resolves via Resolve.
// Mirrors the Promise<PermissionDecision> + resolveOnce pattern from the reference.
type PermissionPrompter struct {
	mu       sync.Mutex
	pending  map[string]*PermissionRequest // keyed by toolUseID
	onChange func(*PermissionRequest)      // called when a new request arrives
}

// NewPermissionPrompter creates a new prompter.
// onChange is called (on a goroutine) whenever a new request arrives,
// allowing the UI to display a permission prompt.
func NewPermissionPrompter(onChange func(*PermissionRequest)) *PermissionPrompter {
	return &PermissionPrompter{
		pending:  make(map[string]*PermissionRequest),
		onChange: onChange,
	}
}

// RequestPermission blocks until the user resolves the permission request.
// Returns the user's decision. Respects context cancellation.
// Mirrors the new Promise(resolve => ...) pattern from useCanUseTool.tsx.
func (p *PermissionPrompter) RequestPermission(ctx context.Context, toolName, toolUseID string, input map[string]any, message string) PermissionResult {
	req := &PermissionRequest{
		ToolName:  toolName,
		ToolUseID: toolUseID,
		Input:     input,
		Message:   message,
		Response:  make(chan PermissionResult, 1),
	}

	p.mu.Lock()
	p.pending[toolUseID] = req
	p.mu.Unlock()

	// Notify the UI that a permission prompt is waiting.
	if p.onChange != nil {
		p.onChange(req)
	}

	// Block until resolved or context cancelled.
	select {
	case result := <-req.Response:
		p.mu.Lock()
		delete(p.pending, toolUseID)
		p.mu.Unlock()
		return result
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.pending, toolUseID)
		p.mu.Unlock()
		return PermissionResult{
			Decision: DecisionDeny,
			Reason:   ReasonPermissionPrompt,
			Message:  "permission request cancelled",
			ToolName: toolName,
		}
	}
}

// Resolve delivers a permission decision for a pending request.
// Uses atomic claim to ensure at most one resolution (resolveOnce pattern).
// Returns false if the request was already resolved or not found.
func (p *PermissionPrompter) Resolve(toolUseID string, result PermissionResult) bool {
	p.mu.Lock()
	req, ok := p.pending[toolUseID]
	p.mu.Unlock()

	if !ok {
		return false
	}

	// Non-blocking send — buffered channel ensures exactly-once delivery.
	select {
	case req.Response <- result:
		return true
	default:
		return false // already resolved
	}
}

// Pending returns a snapshot of all pending permission requests.
func (p *PermissionPrompter) Pending() []*PermissionRequest {
	p.mu.Lock()
	defer p.mu.Unlock()

	reqs := make([]*PermissionRequest, 0, len(p.pending))
	for _, req := range p.pending {
		reqs = append(reqs, req)
	}
	return reqs
}

// ResolveOnce is an atomic guard that ensures a permission decision is
// delivered exactly once, even when multiple racers (user, hook, classifier)
// compete to resolve the same request.
// Mirrors createResolveOnce from PermissionContext.ts.
type ResolveOnce struct {
	claimed int32
	ch      chan PermissionResult
}

// NewResolveOnce wraps a response channel with atomic claiming.
func NewResolveOnce(ch chan PermissionResult) *ResolveOnce {
	return &ResolveOnce{ch: ch}
}

// Claim attempts to atomically claim the right to resolve.
// Returns true if this caller won the race; false if already claimed.
func (r *ResolveOnce) Claim() bool {
	return atomic.CompareAndSwapInt32(&r.claimed, 0, 1)
}

// Resolve delivers the decision if not already claimed.
// Returns true if the decision was delivered.
func (r *ResolveOnce) Resolve(result PermissionResult) bool {
	if !r.Claim() {
		return false
	}
	select {
	case r.ch <- result:
		return true
	default:
		return false
	}
}

// IsResolved returns whether the decision has already been delivered.
func (r *ResolveOnce) IsResolved() bool {
	return atomic.LoadInt32(&r.claimed) != 0
}
