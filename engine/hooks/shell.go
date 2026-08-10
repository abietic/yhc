package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/abietic/yhc/engine/containment"
	"github.com/cloudwego/eino/schema"
)

// DefaultShellHookTimeout is the default timeout for shell hook execution.
// Mirrors TOOL_HOOK_EXECUTION_TIMEOUT_MS in the reference (10 minutes).
const DefaultShellHookTimeout = 10 * time.Minute

// HookStatusEmitter is called when a hook with a StatusMessage starts or completes.
// The engine sets this to emit EventHookStatus events to the UI.
// Parameters: hookCommand, statusMessage, phase ("running"|"completed").
var HookStatusEmitter func(hookCommand, statusMessage, phase string)

type (
	hookStatusEmitterContextKey        struct{}
	hookTurnIDContextKey               struct{}
	asyncShellHookDispatcherContextKey struct{}
	executionPolicyMismatchContextKey  struct{}
	executionBindingContextKey         struct{}
)

type asyncShellHookDispatcher func(event, toolName string, hook ShellHook, env map[string]string)

// WithHookStatusEmitter scopes hook progress to one query/session. This avoids
// cross-session routing through the legacy process-global emitter.
func WithHookStatusEmitter(ctx context.Context, emitter func(hookCommand, statusMessage, phase string)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, hookStatusEmitterContextKey{}, emitter)
}

func withExecutionBinding(ctx context.Context, binding *containment.Binding) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, executionBindingContextKey{}, binding)
}

func executionBinding(ctx context.Context) *containment.Binding {
	if ctx == nil {
		return nil
	}
	binding, _ := ctx.Value(executionBindingContextKey{}).(*containment.Binding)
	return binding
}

// WithHookTurnID associates asynchronously completed hooks with the turn that
// launched them, even when completion occurs after the turn event stream ends.
func WithHookTurnID(ctx context.Context, turnID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, hookTurnIDContextKey{}, turnID)
}

func hookTurnID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	turnID, _ := ctx.Value(hookTurnIDContextKey{}).(string)
	return turnID
}

func withAsyncShellHookDispatcher(ctx context.Context, dispatcher asyncShellHookDispatcher) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, asyncShellHookDispatcherContextKey{}, dispatcher)
}

func withExecutionPolicyMismatch(ctx context.Context) context.Context {
	return context.WithValue(ctx, executionPolicyMismatchContextKey{}, true)
}

func executionPolicyMismatch(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	mismatch, _ := ctx.Value(executionPolicyMismatchContextKey{}).(bool)
	return mismatch
}

func dispatchAsyncShellHook(ctx context.Context, event, toolName string, hook *ShellHook, env map[string]string) {
	if hook == nil {
		return
	}
	if ctx != nil {
		if dispatcher, ok := ctx.Value(asyncShellHookDispatcherContextKey{}).(asyncShellHookDispatcher); ok && dispatcher != nil {
			dispatcher(event, toolName, *hook, env)
			return
		}
	}
	// Compatibility path for direct callers of Run*Hooks. Production engine
	// execution installs an executor-owned dispatcher with bounded lifecycle.
	go func(h ShellHook, e map[string]string) {
		_, _ = ExecuteShellHook(context.Background(), &h, e)
	}(*hook, env)
}

func emitHookStatus(ctx context.Context, hookCommand, statusMessage, phase string) {
	if ctx != nil {
		if emitter, ok := ctx.Value(hookStatusEmitterContextKey{}).(func(string, string, string)); ok && emitter != nil {
			emitter(hookCommand, statusMessage, phase)
			return
		}
	}
	if HookStatusEmitter != nil {
		HookStatusEmitter(hookCommand, statusMessage, phase)
	}
}

// ShellHook represents a user-defined shell command that executes at a
// specific hook point. Mirrors the HookCommand type from the reference.
type ShellHook struct {
	// Command is the shell command to run (passed to sh -c).
	Command string `json:"command"`

	// Timeout is the maximum duration the hook may run before being killed.
	// Zero means use DefaultShellHookTimeout.
	Timeout time.Duration `json:"timeout"`

	// Phase is "pre" or "post", indicating when the hook fires relative to
	// tool execution.
	Phase string `json:"phase"`

	// ToolPattern is a glob/regex pattern that matches tool names.
	// Empty or "*" matches all tools.
	// Supports: exact match, pipe-separated list ("Write|Edit"),
	// and regex patterns ("^Bash.*").
	ToolPattern string `json:"tool_pattern"`

	// If is an optional conditional filter. When set, the hook only fires
	// if the condition matches the current tool call context.
	If *ShellHookCondition `json:"if,omitempty"`

	// Async indicates the hook should run in a background goroutine.
	// Async hooks cannot modify input/output (fire-and-forget).
	Async bool `json:"async,omitempty"`

	// AsyncRewake wakes an idle model only when the asynchronous command exits
	// with code 2. It implies Async.
	AsyncRewake bool `json:"asyncRewake,omitempty"`

	// StatusMessage is displayed as spinner text while the hook executes.
	StatusMessage string `json:"status_message,omitempty"`
}

// ShellHookCondition defines conditional filtering for shell hooks.
// All non-empty fields must match for the hook to fire (AND logic).
type ShellHookCondition struct {
	// ToolName matches against the tool name (exact, glob, or pipe-separated list).
	ToolName string `json:"tool_name,omitempty"`

	// CommandPattern is a regex matched against the Bash tool's command argument.
	// Only evaluated when the tool is Bash.
	CommandPattern string `json:"command_pattern,omitempty"`

	// FilePattern is a regex matched against file path arguments of file tools
	// (Read, Write, Edit, Glob, Grep).
	FilePattern string `json:"file_pattern,omitempty"`
}

// MatchesCondition checks whether the given tool call context satisfies the
// hook's If condition. Returns true if no condition is set (unconditional).
func (h *ShellHook) MatchesCondition(toolName string, toolInput map[string]any) bool {
	if h.If == nil {
		return true
	}

	cond := h.If

	// Check tool_name filter.
	if cond.ToolName != "" {
		if !matchToolPattern(cond.ToolName, toolName) {
			return false
		}
	}

	// Check command_pattern filter (only for Bash-like tools).
	if cond.CommandPattern != "" {
		if toolName != "Bash" && toolName != "BashOutput" && toolName != "KillShell" {
			return false
		}
		cmdVal, _ := toolInput["command"].(string)
		if cmdVal == "" {
			return false
		}
		matched, err := regexp.MatchString(cond.CommandPattern, cmdVal)
		if err != nil || !matched {
			return false
		}
	}

	// Check file_pattern filter (for file tools).
	if cond.FilePattern != "" {
		filePath := extractFilePathFromInput(toolInput)
		if filePath == "" {
			return false
		}
		matched, err := regexp.MatchString(cond.FilePattern, filePath)
		if err != nil || !matched {
			return false
		}
	}

	return true
}

// extractFilePathFromInput tries to get a file path from tool input.
func extractFilePathFromInput(toolInput map[string]any) string {
	// Try common field names for file paths.
	for _, key := range []string{"file_path", "path", "filename", "pattern"} {
		if v, ok := toolInput[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// ShellHookConfig holds all shell-based hooks loaded from configuration.
// Mirrors the hooks settings structure from the reference.
type ShellHookConfig struct {
	Source          string      `json:"-"`
	PreToolHooks    []ShellHook `json:"pre_tool_hooks"`
	PostToolHooks   []ShellHook `json:"post_tool_hooks"`
	UserPromptHooks []ShellHook `json:"user_prompt_hooks"`
}

// ShellHookResult captures the outcome of a shell hook execution.
// Mirrors the exit-code / stdout / stderr protocol from the reference.
type ShellHookResult struct {
	// Command identifies the configured hook command that produced this result.
	Command string

	// ExitCode is the process exit code. 0 = success, 2 = blocking error.
	ExitCode int

	// Stdout is the captured standard output.
	Stdout string

	// Stderr is the captured standard error.
	Stderr string

	// TimedOut indicates whether the hook was killed due to timeout.
	TimedOut bool

	// Cancelled indicates whether the parent context cancelled the hook.
	Cancelled bool

	// StartFailed indicates that the shell process could not be started.
	StartFailed bool

	// TerminationEscalated indicates that graceful tree termination required a
	// forced kill after the grace period.
	TerminationEscalated bool

	// ExecutionPolicyDigest is the immutable identity pinned before spawn.
	ExecutionPolicyDigest string
}

const shellHookTerminationGracePeriod = 250 * time.Millisecond

// hooksJSON is the on-disk representation of .claude/hooks.json.
// It uses string durations for timeout (e.g., "30s", "5m").
type hooksJSON struct {
	PreToolUse       []hookEntryJSON `json:"PreToolUse"`
	PostToolUse      []hookEntryJSON `json:"PostToolUse"`
	UserPromptSubmit []hookEntryJSON `json:"UserPromptSubmit"`
}

type hookEntryJSON struct {
	Matcher string            `json:"matcher"`
	Hooks   []hookCommandJSON `json:"hooks"`
}

type hookCommandJSON struct {
	Command       string              `json:"command"`
	Timeout       int                 `json:"timeout"` // seconds, 0 means default
	If            *ShellHookCondition `json:"if,omitempty"`
	Async         bool                `json:"async,omitempty"`
	AsyncRewake   bool                `json:"asyncRewake,omitempty"`
	StatusMessage string              `json:"status_message,omitempty"`
}

// LoadShellHooks reads shell hook definitions from <configDir>/.claude/hooks.json.
// Returns nil config (no error) if the file does not exist.
func LoadShellHooks(configDir string) (*ShellHookConfig, error) {
	path := filepath.Join(configDir, ".claude", "hooks.json")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ShellHookConfig{Source: path}, nil
		}
		return nil, fmt.Errorf("read hooks config: %w", err)
	}

	var raw hooksJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse hooks config: %w", err)
	}

	cfg := &ShellHookConfig{Source: path}

	for _, entry := range raw.PreToolUse {
		for _, h := range entry.Hooks {
			timeout := DefaultShellHookTimeout
			if h.Timeout > 0 {
				timeout = time.Duration(h.Timeout) * time.Second
			}
			cfg.PreToolHooks = append(cfg.PreToolHooks, ShellHook{
				Command:       h.Command,
				Timeout:       timeout,
				Phase:         "pre",
				ToolPattern:   entry.Matcher,
				If:            h.If,
				Async:         h.Async || h.AsyncRewake,
				AsyncRewake:   h.AsyncRewake,
				StatusMessage: h.StatusMessage,
			})
		}
	}

	for _, entry := range raw.PostToolUse {
		for _, h := range entry.Hooks {
			timeout := DefaultShellHookTimeout
			if h.Timeout > 0 {
				timeout = time.Duration(h.Timeout) * time.Second
			}
			cfg.PostToolHooks = append(cfg.PostToolHooks, ShellHook{
				Command:       h.Command,
				Timeout:       timeout,
				Phase:         "post",
				ToolPattern:   entry.Matcher,
				If:            h.If,
				Async:         h.Async || h.AsyncRewake,
				AsyncRewake:   h.AsyncRewake,
				StatusMessage: h.StatusMessage,
			})
		}
	}

	for _, entry := range raw.UserPromptSubmit {
		for _, h := range entry.Hooks {
			timeout := DefaultShellHookTimeout
			if h.Timeout > 0 {
				timeout = time.Duration(h.Timeout) * time.Second
			}
			cfg.UserPromptHooks = append(cfg.UserPromptHooks, ShellHook{
				Command:       h.Command,
				Timeout:       timeout,
				Phase:         "user_prompt",
				ToolPattern:   entry.Matcher,
				If:            h.If,
				Async:         h.Async || h.AsyncRewake,
				AsyncRewake:   h.AsyncRewake,
				StatusMessage: h.StatusMessage,
			})
		}
	}

	return cfg, nil
}

// ExecuteShellHook runs a single shell hook command with the provided
// environment variables. It enforces the hook's timeout and captures output.
//
// The hook receives its input via environment variables (env map) and via
// stdin as a JSON-encoded blob of the env map for richer access.
//
// Mirrors execCommandHook from the reference implementation.
func ExecuteShellHook(ctx context.Context, hook *ShellHook, env map[string]string) (*ShellHookResult, error) {
	if hook == nil {
		return nil, fmt.Errorf("hook is nil")
	}
	if hook.Command == "" {
		return nil, fmt.Errorf("hook command is empty")
	}

	timeout := hook.Timeout
	if timeout <= 0 {
		timeout = DefaultShellHookTimeout
	}

	if ctx == nil {
		ctx = context.Background()
	}
	policy, ok := containment.FromContext(ctx)
	if !ok {
		policy = containment.DisabledCompatibilitySnapshot("", containment.EntrypointEmbedded)
		ctx = containment.WithSnapshot(ctx, policy)
	}
	if executionPolicyMismatch(ctx) {
		return nil, fmt.Errorf("hook execution policy mismatch")
	}

	// Keep timeout ownership here instead of exec.CommandContext so cancellation
	// can terminate the whole process tree rather than only the outer shell.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result := &ShellHookResult{
		Command:               hook.Command,
		ExecutionPolicyDigest: policy.Digest(),
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		markShellHookContextDone(result, timeout, ctxErr)
		return result, nil
	}

	// Build environment: inherit current process env, then overlay hook env.
	environment := os.Environ()
	for k, v := range env {
		environment = append(environment, k+"="+v)
	}
	path, args, dir := "sh", []string{"-c", hook.Command}, ""
	if binding := executionBinding(ctx); binding != nil {
		if !validHookExecutionBinding(binding) || binding.PolicyDigest() != policy.Digest() {
			return nil, fmt.Errorf("hook execution binding mismatch")
		}
		spec, err := binding.Prepare(ctx, containment.SpawnRequest{Binding: binding, Executable: path, Args: args, Dir: dir, Env: environment})
		if err != nil || spec.BindingDigest != binding.Digest() {
			return nil, fmt.Errorf("hook execution binding unavailable")
		}
		path, args, dir, environment = spec.Path, spec.Args, spec.Dir, spec.Env
	}
	// Cancellation below owns process-tree termination. Detach only the exec
	// helper's automatic cancellation so it cannot kill the group leader first.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), path, args...)
	cmd.Dir, cmd.Env = dir, environment
	prepareShellProcess(cmd)

	// Pass the env map as JSON on stdin (matches reference behavior of writing
	// jsonInput to stdin).
	stdinData, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal hook stdin: %w", err)
	}
	cmd.Stdin = bytes.NewReader(append(stdinData, '\n'))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Close the prepare-to-start cancellation window without giving os/exec
	// ownership of leader-only termination after the process has started.
	if ctxErr := ctx.Err(); ctxErr != nil {
		markShellHookContextDone(result, timeout, ctxErr)
		return result, nil
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start hook command: %w", err)
	}

	wait := make(chan error, 1)
	go func() {
		wait <- cmd.Wait()
	}()

	var runErr error
	select {
	case runErr = <-wait:
	case <-ctx.Done():
		escalated := terminateShellProcessTree(cmd, wait, shellHookTerminationGracePeriod)
		result.Stdout = stdout.String()
		result.Stderr = stderr.String()
		result.TerminationEscalated = escalated
		markShellHookContextDone(result, timeout, ctx.Err())
		return result, nil
	}

	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return nil, fmt.Errorf("wait for hook command: %w", runErr)
	}

	result.ExitCode = 0
	return result, nil
}

func markShellHookContextDone(result *ShellHookResult, timeout time.Duration, ctxErr error) {
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		result.TimedOut = true
		result.ExitCode = 143
		result.Stderr = prependHookStderr(fmt.Sprintf("Hook timed out after %s", timeout), result.Stderr)
		return
	}
	result.Cancelled = true
	result.ExitCode = 137
	result.Stderr = prependHookStderr("Hook cancelled", result.Stderr)
}

func prependHookStderr(prefix, stderr string) string {
	if stderr == "" {
		return prefix
	}
	return prefix + ": " + stderr
}

func failedShellHookResult(hook *ShellHook, err error) *ShellHookResult {
	command := ""
	if hook != nil {
		command = hook.Command
	}
	return &ShellHookResult{
		Command:     command,
		ExitCode:    -1,
		Stderr:      err.Error(),
		StartFailed: true,
	}
}

// RunPreToolHooks runs all pre-tool hooks that match the given tool name.
// Returns results from all matching hooks. If any hook exits with code 2,
// that indicates a blocking error (tool should not execute).
// Async hooks are fired in background goroutines and do not contribute results.
//
// Mirrors the PreToolUse hook execution from the reference.
func RunPreToolHooks(
	ctx context.Context,
	config *ShellHookConfig,
	toolName string,
	toolInput map[string]any,
) ([]*ShellHookResult, error) {
	if config == nil {
		return nil, nil
	}

	var results []*ShellHookResult

	for i := range config.PreToolHooks {
		hook := &config.PreToolHooks[i]
		if !matchToolPattern(hook.ToolPattern, toolName) {
			continue
		}
		if !hook.MatchesCondition(toolName, toolInput) {
			continue
		}

		env := buildHookEnv("PreToolUse", toolName, toolInput, "")

		// Async hooks transfer ownership to the engine's hook executor.
		if hook.Async || hook.AsyncRewake {
			dispatchAsyncShellHook(ctx, "PreToolUse", toolName, hook, env)
			continue
		}

		// Emit status if configured.
		if hook.StatusMessage != "" {
			emitHookStatus(ctx, hook.Command, hook.StatusMessage, "running")
		}

		result, err := ExecuteShellHook(ctx, hook, env)

		if hook.StatusMessage != "" {
			emitHookStatus(ctx, hook.Command, hook.StatusMessage, "completed")
		}

		if err != nil {
			results = append(results, failedShellHookResult(hook, err))
			continue
		}
		results = append(results, result)
	}

	return results, nil
}

// RunPostToolHooks runs all post-tool hooks that match the given tool name.
// Returns results from all matching hooks.
// Async hooks are fired in background goroutines and do not contribute results.
//
// Mirrors the PostToolUse hook execution from the reference.
func RunPostToolHooks(
	ctx context.Context,
	config *ShellHookConfig,
	toolName string,
	toolInput map[string]any,
	toolResult string,
) ([]*ShellHookResult, error) {
	if config == nil {
		return nil, nil
	}

	var results []*ShellHookResult

	for i := range config.PostToolHooks {
		hook := &config.PostToolHooks[i]
		if !matchToolPattern(hook.ToolPattern, toolName) {
			continue
		}
		if !hook.MatchesCondition(toolName, toolInput) {
			continue
		}

		env := buildHookEnv("PostToolUse", toolName, toolInput, toolResult)

		// Async hooks transfer ownership to the engine's hook executor.
		if hook.Async || hook.AsyncRewake {
			dispatchAsyncShellHook(ctx, "PostToolUse", toolName, hook, env)
			continue
		}

		// Emit status if configured.
		if hook.StatusMessage != "" {
			emitHookStatus(ctx, hook.Command, hook.StatusMessage, "running")
		}

		result, err := ExecuteShellHook(ctx, hook, env)

		if hook.StatusMessage != "" {
			emitHookStatus(ctx, hook.Command, hook.StatusMessage, "completed")
		}

		if err != nil {
			results = append(results, failedShellHookResult(hook, err))
			continue
		}
		results = append(results, result)
	}

	return results, nil
}

// matchToolPattern checks whether toolName matches the given pattern.
//
// Pattern semantics (mirrors matchesPattern from the reference):
//   - Empty string or "*": matches everything.
//   - Simple alphanumeric string: exact match.
//   - Pipe-separated list (e.g. "Write|Edit"): matches if toolName is in list.
//   - Otherwise treated as a filepath glob pattern for simple cases, or
//     matched as a prefix/suffix glob.
func matchToolPattern(pattern, toolName string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}

	// Check if it's a simple string or pipe-separated list (no special glob/regex chars except |).
	if isSimplePattern(pattern) {
		if strings.Contains(pattern, "|") {
			parts := strings.Split(pattern, "|")
			for _, p := range parts {
				if strings.TrimSpace(p) == toolName {
					return true
				}
			}
			return false
		}
		// Simple exact match.
		return pattern == toolName
	}

	// Use filepath.Match for glob-style patterns.
	matched, err := filepath.Match(pattern, toolName)
	if err == nil && matched {
		return true
	}

	// Fallback: try prefix/suffix matching for patterns like "Bash*" or "*Edit".
	if strings.HasPrefix(pattern, "*") && strings.HasSuffix(toolName, pattern[1:]) {
		return true
	}
	if strings.HasSuffix(pattern, "*") && strings.HasPrefix(toolName, pattern[:len(pattern)-1]) {
		return true
	}

	return false
}

// isSimplePattern returns true if the pattern contains only alphanumeric
// characters, underscores, and pipes (no glob or regex metacharacters).
func isSimplePattern(pattern string) bool {
	for _, c := range pattern {
		if !isSimplePatternChar(c) {
			return false
		}
	}
	return true
}

func isSimplePatternChar(c rune) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') ||
		c == '_' || c == '|'
}

// buildHookEnv creates the environment variable map passed to shell hooks.
// Mirrors the env vars set in execCommandHook from the reference.
func buildHookEnv(hookEvent, toolName string, toolInput map[string]any, toolResult string) map[string]string {
	env := map[string]string{
		"HOOK_EVENT": hookEvent,
		"TOOL_NAME":  toolName,
	}

	if toolInput != nil {
		inputJSON, err := json.Marshal(toolInput)
		if err == nil {
			env["TOOL_INPUT"] = string(inputJSON)
		}
	}

	if toolResult != "" {
		env["TOOL_RESULT"] = toolResult
	}

	return env
}

// HookJSONOutput is the structured JSON output a shell hook may write to stdout.
// Mirrors hookJSONOutputSchema from the reference (src/types/hooks.ts).
type HookJSONOutput struct {
	// Continue controls whether the engine loop continues after this hook.
	// false → preventContinuation.
	Continue *bool `json:"continue,omitempty"`

	// SuppressOutput hides the hook's output from the user.
	SuppressOutput bool `json:"suppressOutput,omitempty"`

	// StopReason is set when Continue=false to explain why the loop stopped.
	StopReason string `json:"stopReason,omitempty"`

	// Decision is "approve" or "block" — a permission verdict from the hook.
	Decision string `json:"decision,omitempty"`

	// Reason is a human-readable explanation for the decision.
	Reason string `json:"reason,omitempty"`

	// SystemMessage is injected as a system-level attachment after the hook.
	SystemMessage string `json:"systemMessage,omitempty"`

	// HookSpecificOutput carries hook-event-specific structured fields.
	HookSpecificOutput *HookSpecificOutput `json:"hookSpecificOutput,omitempty"`

	// Async indicates the hook is running asynchronously (first-line detection).
	Async bool `json:"async,omitempty"`
}

// HookSpecificOutput carries event-specific fields from hook JSON output.
// Mirrors hookSpecificOutput from the reference.
type HookSpecificOutput struct {
	// For PreToolUse hooks:
	PermissionDecision string         `json:"permissionDecision,omitempty"` // "allow", "deny", "ask"
	UpdatedInput       map[string]any `json:"updatedInput,omitempty"`
	AdditionalContext  string         `json:"additionalContext,omitempty"`

	// For PostToolUse hooks:
	UpdatedMCPToolOutput string `json:"updatedMCPToolOutput,omitempty"`

	// For PermissionDenied hooks:
	Retry bool `json:"retry,omitempty"`
}

// ParsedHookOutput is the result of parsing shell hook stdout.
type ParsedHookOutput struct {
	// JSON is non-nil if the output was valid JSON.
	JSON *HookJSONOutput

	// PlainText is the raw stdout when not JSON.
	PlainText string

	// ValidationError is set when stdout looks like JSON but fails validation.
	ValidationError string
}

// ParseShellHookOutput detects whether stdout is JSON or plain text.
// If JSON, validates the structure. Mirrors parseHookOutput from the reference.
func ParseShellHookOutput(stdout string) *ParsedHookOutput {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" || trimmed[0] != '{' {
		return &ParsedHookOutput{PlainText: stdout}
	}

	var output HookJSONOutput
	if err := json.Unmarshal([]byte(trimmed), &output); err != nil {
		return &ParsedHookOutput{
			PlainText:       stdout,
			ValidationError: fmt.Sprintf("hook JSON parse error: %v", err),
		}
	}

	return &ParsedHookOutput{JSON: &output}
}

// ApplyHookJSON maps a parsed HookJSONOutput to PreToolHookResult fields.
// Mirrors processHookJSONOutput from the reference for PreToolUse events.
func ApplyHookJSON(output *HookJSONOutput) *PreToolHookResult {
	result := &PreToolHookResult{}

	if output.Continue != nil && !*output.Continue {
		result.PreventContinuation = true
		result.StopReason = output.StopReason
	}

	if output.Decision == "block" {
		reason := output.Reason
		if reason == "" {
			reason = "blocked by hook"
		}
		result.DenyReason = reason
	}

	if output.Decision == "approve" { //nolint:staticcheck // intentionally empty
	}

	if output.SystemMessage != "" {
		result.Attachments = append(result.Attachments, &schema.Message{
			Role:    schema.User,
			Content: output.SystemMessage,
			Extra: map[string]any{
				"is_meta":         true,
				"attachment_kind": "hook_system_message",
			},
		})
	}

	if output.HookSpecificOutput != nil {
		hso := output.HookSpecificOutput
		if hso.UpdatedInput != nil {
			result.UpdatedInput = hso.UpdatedInput
		}
		if hso.PermissionDecision == "deny" {
			reason := output.Reason
			if reason == "" {
				reason = "denied by hook"
			}
			result.DenyReason = reason
		}
	}

	return result
}

// ApplyPostToolHookJSON maps HookJSONOutput to PostToolHookResult fields.
// Mirrors processHookJSONOutput for PostToolUse events.
func ApplyPostToolHookJSON(output *HookJSONOutput) *PostToolHookResult {
	result := &PostToolHookResult{}

	if output.Continue != nil && !*output.Continue {
		result.PreventContinuation = true
		result.StopReason = output.StopReason
	}

	if output.SystemMessage != "" {
		result.Attachments = append(result.Attachments, &schema.Message{
			Role:    schema.User,
			Content: output.SystemMessage,
			Extra: map[string]any{
				"is_meta":         true,
				"attachment_kind": "hook_system_message",
			},
		})
	}

	if output.HookSpecificOutput != nil {
		if output.HookSpecificOutput.UpdatedMCPToolOutput != "" {
			result.UpdatedResult = output.HookSpecificOutput.UpdatedMCPToolOutput
			result.ReplaceResult = true
		}
	}

	return result
}

// ApplyPermissionDeniedHookJSON maps HookJSONOutput to PermissionDeniedHookResult.
// Mirrors processHookJSONOutput for PermissionDenied events.
func ApplyPermissionDeniedHookJSON(output *HookJSONOutput) *PermissionDeniedHookResult {
	result := &PermissionDeniedHookResult{}
	if output.HookSpecificOutput != nil && output.HookSpecificOutput.Retry {
		result.Retry = true
	}
	return result
}

// ---------------------------------------------------------------------------
// UserPromptSubmit hooks
// ---------------------------------------------------------------------------

// UserPromptHookResult holds the aggregated result of UserPromptSubmit shell hooks.
type UserPromptHookResult struct {
	// UpdatedPrompt replaces the user's input if non-empty.
	UpdatedPrompt string
	// AdditionalContext is appended to the system context for this turn.
	AdditionalContext string
	// Reject indicates the hook rejects the submission (exit code 2).
	Reject bool
	// RejectReason is the reason for rejection.
	RejectReason string
	// Attachments are system messages injected alongside the prompt.
	Attachments []*schema.Message
}

// RunUserPromptHooks runs all UserPromptSubmit shell hooks.
// These fire before the user's message is processed by the engine.
// Async hooks are fired in background goroutines and do not contribute results.
func RunUserPromptHooks(
	ctx context.Context,
	config *ShellHookConfig,
	userPrompt string,
) (*UserPromptHookResult, error) {
	if config == nil || len(config.UserPromptHooks) == 0 {
		return nil, nil
	}

	result := &UserPromptHookResult{}

	for i := range config.UserPromptHooks {
		hook := &config.UserPromptHooks[i]

		env := map[string]string{
			"HOOK_EVENT":  "UserPromptSubmit",
			"USER_PROMPT": userPrompt,
		}

		// Async hooks transfer ownership to the engine's hook executor.
		if hook.Async || hook.AsyncRewake {
			dispatchAsyncShellHook(ctx, "UserPromptSubmit", "", hook, env)
			continue
		}

		// Emit status if configured.
		if hook.StatusMessage != "" {
			emitHookStatus(ctx, hook.Command, hook.StatusMessage, "running")
		}

		sr, err := ExecuteShellHook(ctx, hook, env)

		if hook.StatusMessage != "" {
			emitHookStatus(ctx, hook.Command, hook.StatusMessage, "completed")
		}
		if err != nil {
			result.Attachments = append(result.Attachments, shellHookFailureAttachment(failedShellHookResult(hook, err)))
			continue
		}
		if sr == nil {
			continue
		}

		// Exit code 2 = rejection.
		if sr.ExitCode == 2 {
			result.Reject = true
			result.RejectReason = sr.Stderr
			if result.RejectReason == "" {
				result.RejectReason = "rejected by user-prompt-submit hook"
			}
			return result, nil
		}
		if sr.ExitCode != 0 {
			result.Attachments = append(result.Attachments, shellHookFailureAttachment(sr))
			continue
		}

		// Parse stdout for JSON protocol.
		parsed := ParseShellHookOutput(sr.Stdout)
		if parsed.JSON != nil {
			if parsed.JSON.HookSpecificOutput != nil {
				if parsed.JSON.HookSpecificOutput.AdditionalContext != "" {
					result.AdditionalContext = parsed.JSON.HookSpecificOutput.AdditionalContext
				}
				if parsed.JSON.HookSpecificOutput.UpdatedInput != nil {
					if updatedPrompt, ok := parsed.JSON.HookSpecificOutput.UpdatedInput["prompt"].(string); ok {
						result.UpdatedPrompt = updatedPrompt
					}
				}
			}
			if parsed.JSON.SystemMessage != "" {
				result.Attachments = append(result.Attachments, &schema.Message{
					Role:    schema.User,
					Content: parsed.JSON.SystemMessage,
					Extra: map[string]any{
						"is_meta":         true,
						"attachment_kind": "user_prompt_hook_message",
					},
				})
			}
			if parsed.JSON.Decision == "block" {
				result.Reject = true
				result.RejectReason = parsed.JSON.Reason
				if result.RejectReason == "" {
					result.RejectReason = "blocked by user-prompt-submit hook"
				}
				return result, nil
			}
			if parsed.JSON.Continue != nil && !*parsed.JSON.Continue {
				result.Reject = true
				result.RejectReason = parsed.JSON.StopReason
				if result.RejectReason == "" {
					result.RejectReason = "stopped by user-prompt-submit hook"
				}
				return result, nil
			}
		} else if sr.Stdout != "" {
			// Non-JSON output: emit as attachment.
			result.Attachments = append(result.Attachments, &schema.Message{
				Role:    schema.User,
				Content: sr.Stdout,
				Extra: map[string]any{
					"is_meta":         true,
					"attachment_kind": "user_prompt_hook_output",
				},
			})
		}
	}

	return result, nil
}
