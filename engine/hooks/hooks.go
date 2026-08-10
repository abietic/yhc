package hooks

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/abietic/yhc/engine/containment"
	"github.com/cloudwego/eino/schema"
)

// PostSamplingHook fires after model response, before tool execution.
// Non-blocking (fire-and-forget). Mirrors query.ts:999-1009.
type PostSamplingHook func(messages []*schema.Message)

// HookPermissionBehavior represents a hook's decision about tool permissions.
// Mirrors the TS PreToolUse hook permission behavior values.
type HookPermissionBehavior string

const (
	// HookPermissionNone means the hook does not influence permission decisions.
	HookPermissionNone HookPermissionBehavior = ""
	// HookPermissionAllow means the hook wants to allow the tool call,
	// but deny rules from settings still take precedence (inc-4788 invariant).
	HookPermissionAllow HookPermissionBehavior = "allow"
	// HookPermissionDeny means the hook denies the tool call.
	HookPermissionDeny HookPermissionBehavior = "deny"
	// HookPermissionAsk means the hook wants to force an interactive prompt.
	HookPermissionAsk HookPermissionBehavior = "ask"
)

// PreToolHookResult aggregates a single pre-tool hook decision.
// Minimal Go port of the TS PreToolUse lifecycle.
type PreToolHookResult struct {
	UpdatedInput        map[string]any
	DenyReason          string
	Stop                bool
	PreventContinuation bool
	StopReason          string
	Attachments         []*schema.Message
	// PermissionBehavior indicates the hook's permission decision.
	// When set to HookPermissionAllow, the interactive permission prompt is
	// skipped BUT deny rules from settings.json still take precedence.
	// This implements the inc-4788 invariant from the TS reference.
	PermissionBehavior HookPermissionBehavior
}

// PreToolHook fires before tool execution. Hooks may mutate input, block
// execution, or request that the loop stop after the current tool result.
type PreToolHook func(
	ctx context.Context,
	toolName string,
	toolUseID string,
	input map[string]any,
) *PreToolHookResult

// PostToolHookResult aggregates a single post-tool hook decision.
type PostToolHookResult struct {
	UpdatedResult       string
	ReplaceResult       bool
	PreventContinuation bool
	StopReason          string
	Attachments         []*schema.Message
}

// PostToolHook fires after a successful tool execution. Hooks may rewrite the
// result payload and emit attachments.
type PostToolHook func(
	ctx context.Context,
	toolName string,
	toolUseID string,
	input map[string]any,
	result string,
) *PostToolHookResult

// PostToolFailureHookResult aggregates a single failed-tool hook decision.
type PostToolFailureHookResult struct {
	PreventContinuation bool
	StopReason          string
	Attachments         []*schema.Message
}

// PostToolFailureHook fires after a failed tool execution.
type PostToolFailureHook func(
	ctx context.Context,
	toolName string,
	toolUseID string,
	input map[string]any,
	err error,
) *PostToolFailureHookResult

// StopHookResult aggregates stop hook evaluations.
type StopHookResult struct {
	PreventContinuation bool
	BlockingErrors      []*schema.Message
}

// StopHook evaluates whether to end the turn after model output.
// Two outcomes: PreventContinuation (end turn) or BlockingErrors (inject errors, retry).
// Mirrors query.ts:1267-1306.
type StopHook func(
	messagesForQuery []*schema.Message,
	assistantMessages []*schema.Message,
	stopHookActive bool,
) *StopHookResult

// StopFailureHook fires when model never produced a valid response.
// Cleanup only, never retries. Mirrors query.ts:1174, :1263.
type StopFailureHook func(lastMessage *schema.Message)

// PermissionDeniedHookResult aggregates a single permission-denied hook decision.
// Mirrors src/utils/hooks.ts executePermissionDeniedHooks output.
type PermissionDeniedHookResult struct {
	Retry       bool
	Attachments []*schema.Message
}

// PermissionDeniedHook fires after an auto-mode classifier denies a tool call.
// If the hook returns Retry=true, the engine tells the model it may retry.
type PermissionDeniedHook func(
	ctx context.Context,
	toolName string,
	toolUseID string,
	input map[string]any,
	reason string,
) *PermissionDeniedHookResult

// Executor manages hook execution.
type Executor struct {
	postSamplingHooks     []PostSamplingHook
	preToolHooks          []PreToolHook
	postToolHooks         []PostToolHook
	postToolFailureHooks  []PostToolFailureHook
	stopHooks             []StopHook
	stopFailureHooks      []StopFailureHook
	permissionDeniedHooks []PermissionDeniedHook
	userPromptSubmitHooks []UserPromptSubmitHook

	// Lifecycle hooks
	sessionStartHooks []SessionStartHook
	sessionEndHooks   []SessionEndHook
	preCompactHooks   []PreCompactHook
	postCompactHooks  []PostCompactHook
	notificationHooks []NotificationHook
	commandHooks      []CommandHook
	turnStartHooks    []TurnStartHook
	turnEndHooks      []TurnEndHook

	// Prompt hooks (modify system prompt before model call).
	promptHooks []PromptHook

	// Agent lifecycle hooks (sub-agent start/complete/fail).
	agentStartHooks    []AgentStartHook
	agentCompleteHooks []AgentCompleteHook
	agentFailHooks     []AgentFailHook

	// Lifecycle ordering enforcer (optional, for validation).
	orderEnforcer *LifecycleOrderEnforcer

	// Shell hooks loaded from .claude/hooks.json.
	shellHookMu       sync.RWMutex
	shellHookConfig   *ShellHookConfig
	asyncShell        *asyncShellRuntime
	executionPolicyMu sync.Mutex
	executionPolicy   *containment.Snapshot
	executionBinding  *containment.Binding
}

// NewExecutor creates a new hook executor.
func NewExecutor() *Executor {
	return &Executor{asyncShell: newAsyncShellRuntime()}
}

// BindExecutionPolicy binds the immutable identity used by shell hooks. Once
// bound, a different identity is always rejected, including before execution.
func (e *Executor) BindExecutionPolicy(policy *containment.Snapshot) error {
	if e == nil || policy == nil || policy.Digest() == "" {
		return fmt.Errorf("hook execution policy must be valid")
	}
	e.executionPolicyMu.Lock()
	defer e.executionPolicyMu.Unlock()
	if e.executionBinding != nil && e.executionBinding.PolicyDigest() != policy.Digest() {
		return fmt.Errorf("hook execution policy replacement rejected")
	}
	if e.executionPolicy != nil && e.executionPolicy.Digest() != policy.Digest() {
		return fmt.Errorf("hook execution policy replacement rejected")
	}
	if e.executionPolicy == nil {
		e.executionPolicy = policy
	}
	if e.executionPolicy.Digest() != policy.Digest() {
		return fmt.Errorf("hook execution policy replacement rejected")
	}
	return nil
}

// BindExecutionBinding pins the explicit ambient process identity used by
// shell hooks. A policy-only caller remains supported for embedded callers,
// but cannot replace a binding once one is present.
func (e *Executor) BindExecutionBinding(binding *containment.Binding) error {
	if e == nil || !validHookExecutionBinding(binding) {
		return fmt.Errorf("hook execution binding must be an available ambient shell-hooks binding")
	}
	e.executionPolicyMu.Lock()
	defer e.executionPolicyMu.Unlock()
	if e.executionBinding != nil && e.executionBinding.Digest() != binding.Digest() {
		return fmt.Errorf("hook execution binding replacement rejected")
	}
	if e.executionPolicy != nil && e.executionPolicy.Digest() != binding.PolicyDigest() {
		return fmt.Errorf("hook execution policy replacement rejected")
	}
	e.executionBinding = binding
	e.executionPolicy = binding.Policy()
	return nil
}

func validHookExecutionBinding(binding *containment.Binding) bool {
	if binding == nil || binding.ProcessClass() != containment.ProcessClassShellHooks || binding.Availability() != containment.BindingAvailable || binding.AdapterFamily() != containment.AdapterAmbientHost {
		return false
	}
	diagnostic := binding.Policy().Diagnostic()
	return diagnostic.Profile == containment.ProfileDangerFullAccess && diagnostic.State == containment.StateDisabled
}

// ExecutionBindingDigest returns the explicit shell-hook binding identity.
func (e *Executor) ExecutionBindingDigest() string {
	if e == nil {
		return ""
	}
	e.executionPolicyMu.Lock()
	defer e.executionPolicyMu.Unlock()
	return e.executionBinding.Digest()
}

// ExecutionPolicyDigest returns the hook executor's bound identity.
func (e *Executor) ExecutionPolicyDigest() string {
	if e == nil {
		return ""
	}
	e.executionPolicyMu.Lock()
	defer e.executionPolicyMu.Unlock()
	if e.executionPolicy == nil {
		return ""
	}
	return e.executionPolicy.Digest()
}

func (e *Executor) executionPolicyContext(ctx context.Context) context.Context {
	if e == nil {
		return ctx
	}
	e.executionPolicyMu.Lock()
	policy := e.executionPolicy
	binding := e.executionBinding
	e.executionPolicyMu.Unlock()
	if binding != nil {
		// The class-specific binding is the authority for hook processes. Query
		// contexts normally carry the independent Guest policy, so inheriting it
		// must not be treated as an attempted hook-policy replacement.
		ctx = containment.WithSnapshot(ctx, policy)
		ctx = context.WithValue(ctx, executionPolicyMismatchContextKey{}, false)
		return withExecutionBinding(ctx, binding)
	}
	if existing, ok := containment.FromContext(ctx); ok {
		if policy == nil || existing.Digest() == policy.Digest() {
			return withExecutionBinding(ctx, binding)
		}
		return withExecutionBinding(withExecutionPolicyMismatch(
			containment.WithSnapshot(ctx, policy),
		), binding)
	}
	if policy == nil {
		policy = containment.DisabledCompatibilitySnapshot("", containment.EntrypointEmbedded)
	}
	ctx = containment.WithSnapshot(ctx, policy)
	return withExecutionBinding(ctx, binding)
}

func (e *Executor) asyncShellContext(ctx context.Context) context.Context {
	if e == nil || e.asyncShell == nil {
		return ctx
	}
	turnID := hookTurnID(ctx)
	boundCtx := e.executionPolicyContext(ctx)
	return withAsyncShellHookDispatcher(boundCtx, func(event, toolName string, hook ShellHook, env map[string]string) {
		e.asyncShell.dispatch(boundCtx, turnID, event, toolName, hook, env)
	})
}

// SetAsyncShellCompletionHandler installs the engine-owned presentation sink.
func (e *Executor) SetAsyncShellCompletionHandler(handler func(AsyncShellHookCompletion)) {
	if e != nil && e.asyncShell != nil {
		e.asyncShell.setHandler(handler)
	}
}

// DrainAsyncShellMessages atomically claims completed model-visible results.
func (e *Executor) DrainAsyncShellMessages() []*schema.Message {
	if e == nil || e.asyncShell == nil {
		return nil
	}
	return e.asyncShell.drainModelMessages()
}

// AcknowledgeAsyncShellDelivery removes one completion only after another
// durable owner has accepted its model-visible payload.
func (e *Executor) AcknowledgeAsyncShellDelivery(id string) bool {
	if e == nil || e.asyncShell == nil {
		return false
	}
	return e.asyncShell.acknowledgeDeliverable(id)
}

// CancelAsyncShellHooks cancels all executor-owned asynchronous shell hooks.
func (e *Executor) CancelAsyncShellHooks() {
	if e != nil && e.asyncShell != nil {
		e.asyncShell.cancelAll()
	}
}

// ShutdownAsyncShellHooks stops accepting work and waits for owned hooks.
func (e *Executor) ShutdownAsyncShellHooks(ctx context.Context) error {
	if e == nil || e.asyncShell == nil {
		return nil
	}
	return e.asyncShell.shutdown(ctx)
}

// RegisterShellHooks loads shell hook configuration into the executor.
// Shell hooks are run alongside programmatic hooks in ExecutePreTool/ExecutePostTool.
func (e *Executor) RegisterShellHooks(config *ShellHookConfig) {
	if e == nil {
		return
	}
	e.shellHookMu.Lock()
	e.shellHookConfig = cloneShellHookConfig(config)
	e.shellHookMu.Unlock()
}

// ShellHookSnapshot returns the exact shell-hook generation used by this
// executor. The returned config is detached from the live runtime.
func (e *Executor) ShellHookSnapshot() *ShellHookConfig {
	if e == nil {
		return nil
	}
	e.shellHookMu.RLock()
	defer e.shellHookMu.RUnlock()
	return cloneShellHookConfig(e.shellHookConfig)
}

func cloneShellHookConfig(config *ShellHookConfig) *ShellHookConfig {
	if config == nil {
		return nil
	}
	return &ShellHookConfig{
		Source:          config.Source,
		PreToolHooks:    cloneShellHooks(config.PreToolHooks),
		PostToolHooks:   cloneShellHooks(config.PostToolHooks),
		UserPromptHooks: cloneShellHooks(config.UserPromptHooks),
	}
}

func cloneShellHooks(source []ShellHook) []ShellHook {
	result := make([]ShellHook, len(source))
	for i := range source {
		result[i] = source[i]
		if source[i].If != nil {
			condition := *source[i].If
			result[i].If = &condition
		}
	}
	return result
}

// RegisterPostSampling registers a post-sampling hook.
func (e *Executor) RegisterPostSampling(h PostSamplingHook) {
	e.postSamplingHooks = append(e.postSamplingHooks, h)
}

// RegisterPreTool registers a pre-tool hook.
func (e *Executor) RegisterPreTool(h PreToolHook) {
	e.preToolHooks = append(e.preToolHooks, h)
}

// RegisterPostTool registers a post-tool hook.
func (e *Executor) RegisterPostTool(h PostToolHook) {
	e.postToolHooks = append(e.postToolHooks, h)
}

// RegisterPostToolFailure registers a post-tool-failure hook.
func (e *Executor) RegisterPostToolFailure(h PostToolFailureHook) {
	e.postToolFailureHooks = append(e.postToolFailureHooks, h)
}

// RegisterStop registers a stop hook.
func (e *Executor) RegisterStop(h StopHook) {
	e.stopHooks = append(e.stopHooks, h)
}

// RegisterStopFailure registers a stop-failure hook.
func (e *Executor) RegisterStopFailure(h StopFailureHook) {
	e.stopFailureHooks = append(e.stopFailureHooks, h)
}

// ExecutePostSampling fires all post-sampling hooks (goroutine, non-blocking).
func (e *Executor) ExecutePostSampling(messages []*schema.Message) {
	for _, h := range e.postSamplingHooks {
		go h(messages)
	}
}

// ExecutePreTool runs all pre-tool hooks (programmatic + shell) and returns the aggregated result.
// Permission behavior precedence follows TS: deny > ask > allow.
func (e *Executor) ExecutePreTool(
	ctx context.Context,
	toolName string,
	toolUseID string,
	input map[string]any,
) *PreToolHookResult {
	result := &PreToolHookResult{UpdatedInput: input}
	currentInput := input

	// 1. Run programmatic hooks.
	for _, h := range e.preToolHooks {
		r := h(ctx, toolName, toolUseID, currentInput)
		if r == nil {
			continue
		}
		mergePreToolResult(result, r, &currentInput)
	}

	// 2. Run shell hooks and parse their JSON output.
	if shellHooks := e.ShellHookSnapshot(); shellHooks != nil {
		shellResults, _ := RunPreToolHooks(
			e.asyncShellContext(ctx),
			shellHooks,
			toolName,
			currentInput,
		)
		for _, sr := range shellResults {
			r := shellPreToolResultToHookResult(sr)
			if r == nil {
				continue
			}
			mergePreToolResult(result, r, &currentInput)
		}
	}

	return result
}

// mergePreToolResult merges a single PreToolHookResult into an aggregate result.
func mergePreToolResult(result, r *PreToolHookResult, currentInput *map[string]any) {
	if r.UpdatedInput != nil {
		*currentInput = r.UpdatedInput
		result.UpdatedInput = r.UpdatedInput
	}
	if r.DenyReason != "" {
		result.DenyReason = r.DenyReason
	}
	if r.Stop {
		result.Stop = true
	}
	if r.PreventContinuation {
		result.PreventContinuation = true
	}
	if r.StopReason != "" {
		result.StopReason = r.StopReason
	}
	result.Attachments = append(result.Attachments, r.Attachments...)
	// Aggregate permission behavior with deny > ask > allow precedence.
	if r.PermissionBehavior != HookPermissionNone {
		result.PermissionBehavior = mergePermissionBehavior(
			result.PermissionBehavior, r.PermissionBehavior,
		)
	}
}

// shellPreToolResultToHookResult converts a ShellHookResult to a PreToolHookResult
// by parsing JSON output through ParseShellHookOutput/ApplyHookJSON.
// Mirrors the TS reference's processHookJSONOutput for PreToolUse hooks.
func shellPreToolResultToHookResult(sr *ShellHookResult) *PreToolHookResult {
	if sr == nil {
		return nil
	}

	// Exit code 2 = blocking error: tool should not execute.
	if sr.ExitCode == 2 {
		reason := sr.Stderr
		if reason == "" {
			reason = "blocked by pre-tool shell hook (exit code 2)"
		}
		return &PreToolHookResult{
			DenyReason: reason,
		}
	}
	if sr.ExitCode != 0 {
		return &PreToolHookResult{Attachments: []*schema.Message{shellHookFailureAttachment(sr)}}
	}

	// Parse stdout for JSON protocol output.
	parsed := ParseShellHookOutput(sr.Stdout)
	if parsed.JSON != nil {
		r := ApplyHookJSON(parsed.JSON)
		// Map hookSpecificOutput.permissionDecision to PermissionBehavior.
		if parsed.JSON.HookSpecificOutput != nil {
			switch parsed.JSON.HookSpecificOutput.PermissionDecision {
			case "allow":
				r.PermissionBehavior = HookPermissionAllow
			case "deny":
				r.PermissionBehavior = HookPermissionDeny
			case "ask":
				r.PermissionBehavior = HookPermissionAsk
			}
		}
		return r
	}

	// Non-JSON output: if there's stdout text and not suppressed, emit as attachment.
	if sr.Stdout != "" {
		return &PreToolHookResult{
			Attachments: []*schema.Message{
				{
					Role:    schema.User,
					Content: sr.Stdout,
					Extra: map[string]any{
						"is_meta":         true,
						"attachment_kind": "shell_hook_output",
					},
				},
			},
		}
	}

	return nil
}

// mergePermissionBehavior merges two permission behaviors with deny > ask > allow precedence.
func mergePermissionBehavior(current, incoming HookPermissionBehavior) HookPermissionBehavior {
	if current == HookPermissionDeny || incoming == HookPermissionDeny {
		return HookPermissionDeny
	}
	if current == HookPermissionAsk || incoming == HookPermissionAsk {
		return HookPermissionAsk
	}
	if incoming != HookPermissionNone {
		return incoming
	}
	return current
}

// ExecutePostTool runs all post-tool hooks (programmatic + shell) and returns the aggregated result.
func (e *Executor) ExecutePostTool(
	ctx context.Context,
	toolName string,
	toolUseID string,
	input map[string]any,
	resultText string,
) *PostToolHookResult {
	result := &PostToolHookResult{}
	currentResult := resultText

	// 1. Run programmatic hooks.
	for _, h := range e.postToolHooks {
		r := h(ctx, toolName, toolUseID, input, currentResult)
		if r == nil {
			continue
		}
		mergePostToolResult(result, r, &currentResult)
	}

	// 2. Run shell hooks and parse their JSON output.
	if shellHooks := e.ShellHookSnapshot(); shellHooks != nil {
		shellResults, _ := RunPostToolHooks(
			e.asyncShellContext(ctx),
			shellHooks,
			toolName,
			input,
			currentResult,
		)
		for _, sr := range shellResults {
			r := shellPostToolResultToHookResult(sr)
			if r == nil {
				continue
			}
			mergePostToolResult(result, r, &currentResult)
		}
	}

	return result
}

// mergePostToolResult merges a single PostToolHookResult into an aggregate result.
func mergePostToolResult(result, r *PostToolHookResult, currentResult *string) {
	if r.ReplaceResult {
		*currentResult = r.UpdatedResult
		result.UpdatedResult = r.UpdatedResult
		result.ReplaceResult = true
	}
	if r.PreventContinuation {
		result.PreventContinuation = true
	}
	if r.StopReason != "" {
		result.StopReason = r.StopReason
	}
	result.Attachments = append(result.Attachments, r.Attachments...)
}

// shellPostToolResultToHookResult converts a ShellHookResult to a PostToolHookResult
// by parsing JSON output through ParseShellHookOutput/ApplyPostToolHookJSON.
func shellPostToolResultToHookResult(sr *ShellHookResult) *PostToolHookResult {
	if sr == nil {
		return nil
	}
	if sr.ExitCode == 2 {
		reason := strings.TrimSpace(sr.Stderr)
		if reason == "" {
			reason = "blocked by post-tool shell hook (exit code 2)"
		}
		return &PostToolHookResult{PreventContinuation: true, StopReason: reason}
	}
	if sr.ExitCode != 0 {
		return &PostToolHookResult{Attachments: []*schema.Message{shellHookFailureAttachment(sr)}}
	}

	// Parse stdout for JSON protocol output.
	parsed := ParseShellHookOutput(sr.Stdout)
	if parsed.JSON != nil {
		return ApplyPostToolHookJSON(parsed.JSON)
	}

	// Non-JSON output: emit as attachment if non-empty.
	if sr.Stdout != "" {
		return &PostToolHookResult{
			Attachments: []*schema.Message{
				{
					Role:    schema.User,
					Content: sr.Stdout,
					Extra: map[string]any{
						"is_meta":         true,
						"attachment_kind": "shell_hook_output",
					},
				},
			},
		}
	}

	return nil
}

func shellHookFailureAttachment(sr *ShellHookResult) *schema.Message {
	if sr == nil {
		return nil
	}

	detail := strings.TrimSpace(sr.Stderr)
	if detail == "" {
		detail = fmt.Sprintf("shell hook exited with status %d", sr.ExitCode)
	}
	return &schema.Message{
		Role:    schema.User,
		Content: detail,
		Extra: map[string]any{
			"is_meta":               true,
			"attachment_kind":       "hook_non_blocking_error",
			"hook_command":          sr.Command,
			"exit_code":             sr.ExitCode,
			"timed_out":             sr.TimedOut,
			"cancelled":             sr.Cancelled,
			"start_failed":          sr.StartFailed,
			"termination_escalated": sr.TerminationEscalated,
		},
	}
}

// ExecutePostToolFailure runs all post-tool-failure hooks and aggregates the result.
func (e *Executor) ExecutePostToolFailure(
	ctx context.Context,
	toolName string,
	toolUseID string,
	input map[string]any,
	err error,
) *PostToolFailureHookResult {
	result := &PostToolFailureHookResult{}
	for _, h := range e.postToolFailureHooks {
		r := h(ctx, toolName, toolUseID, input, err)
		if r == nil {
			continue
		}
		if r.PreventContinuation {
			result.PreventContinuation = true
		}
		if r.StopReason != "" {
			result.StopReason = r.StopReason
		}
		result.Attachments = append(result.Attachments, r.Attachments...)
	}
	return result
}

// ExecuteStop runs all stop hooks and returns aggregated results.
func (e *Executor) ExecuteStop(
	messagesForQuery []*schema.Message,
	assistantMessages []*schema.Message,
	stopHookActive bool,
) *StopHookResult {
	result := &StopHookResult{}
	for _, h := range e.stopHooks {
		r := h(messagesForQuery, assistantMessages, stopHookActive)
		if r == nil {
			continue
		}
		if r.PreventContinuation {
			result.PreventContinuation = true
		}
		result.BlockingErrors = append(result.BlockingErrors, r.BlockingErrors...)
	}
	return result
}

// ExecuteStopFailure fires all stop-failure hooks (goroutine, non-blocking).
func (e *Executor) ExecuteStopFailure(lastMessage *schema.Message) {
	for _, h := range e.stopFailureHooks {
		go h(lastMessage)
	}
}

// RegisterPermissionDenied registers a permission-denied hook.
func (e *Executor) RegisterPermissionDenied(h PermissionDeniedHook) {
	e.permissionDeniedHooks = append(e.permissionDeniedHooks, h)
}

// ExecutePermissionDenied runs all permission-denied hooks and returns the aggregated result.
// Mirrors src/utils/hooks.ts executePermissionDeniedHooks.
func (e *Executor) ExecutePermissionDenied(
	ctx context.Context,
	toolName string,
	toolUseID string,
	input map[string]any,
	reason string,
) *PermissionDeniedHookResult {
	if len(e.permissionDeniedHooks) == 0 {
		return nil
	}
	result := &PermissionDeniedHookResult{}
	for _, h := range e.permissionDeniedHooks {
		r := h(ctx, toolName, toolUseID, input, reason)
		if r == nil {
			continue
		}
		if r.Retry {
			result.Retry = true
		}
		result.Attachments = append(result.Attachments, r.Attachments...)
	}
	return result
}

// HasPermissionDeniedHooks returns true if any permission-denied hooks are registered.
func (e *Executor) HasPermissionDeniedHooks() bool {
	return len(e.permissionDeniedHooks) > 0
}

// UserPromptSubmitHookResult aggregates results from user-prompt-submit hooks.
type UserPromptSubmitHookResult struct {
	// UpdatedPrompt replaces the user's input if non-empty.
	UpdatedPrompt string
	// AdditionalContext is injected into system context for this turn.
	AdditionalContext string
	// Reject indicates the hook rejects the submission.
	Reject bool
	// RejectReason is the reason for rejection.
	RejectReason string
	// Attachments are system messages injected alongside the prompt.
	Attachments []*schema.Message
}

// UserPromptSubmitHook fires before the user's message is processed.
// It may modify the prompt, inject context, or reject the submission.
type UserPromptSubmitHook func(
	ctx context.Context,
	prompt string,
) *UserPromptSubmitHookResult

// RegisterUserPromptSubmit registers a user-prompt-submit hook.
func (e *Executor) RegisterUserPromptSubmit(h UserPromptSubmitHook) {
	e.userPromptSubmitHooks = append(e.userPromptSubmitHooks, h)
}

// ExecuteUserPromptSubmit runs all user-prompt-submit hooks (programmatic + shell)
// and returns the aggregated result. The first rejection wins.
func (e *Executor) ExecuteUserPromptSubmit(
	ctx context.Context,
	prompt string,
) *UserPromptSubmitHookResult {
	result := &UserPromptSubmitHookResult{}
	currentPrompt := prompt

	// 1. Run programmatic hooks.
	for _, h := range e.userPromptSubmitHooks {
		r := h(ctx, currentPrompt)
		if r == nil {
			continue
		}
		if r.Reject {
			return r
		}
		if r.UpdatedPrompt != "" {
			currentPrompt = r.UpdatedPrompt
			result.UpdatedPrompt = r.UpdatedPrompt
		}
		if r.AdditionalContext != "" {
			result.AdditionalContext = r.AdditionalContext
		}
		result.Attachments = append(result.Attachments, r.Attachments...)
	}

	// 2. Run shell hooks.
	if shellHooks := e.ShellHookSnapshot(); shellHooks != nil {
		shellResult, _ := RunUserPromptHooks(
			e.asyncShellContext(ctx),
			shellHooks,
			currentPrompt,
		)
		if shellResult != nil {
			if shellResult.Reject {
				result.Reject = true
				result.RejectReason = shellResult.RejectReason
				return result
			}
			if shellResult.UpdatedPrompt != "" {
				result.UpdatedPrompt = shellResult.UpdatedPrompt
			}
			if shellResult.AdditionalContext != "" {
				result.AdditionalContext = shellResult.AdditionalContext
			}
			result.Attachments = append(result.Attachments, shellResult.Attachments...)
		}
	}

	return result
}
