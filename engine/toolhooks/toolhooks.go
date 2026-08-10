// Package toolhooks wires the permission system and hooks into the tool
// execution flow, providing a unified pre/post tool-use hook runner.
// It integrates permission.RulesEngine checks, shell-based hooks, and
// programmatic Go hook functions into a single execution pipeline.
//
// Mirrors the behavior of toolHooks.ts and toolExecution.ts from the
// reference implementation.
package toolhooks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/permission"
)

// ToolHookContext carries contextual information about the current tool
// invocation. Passed to all pre/post hooks so they can make decisions based
// on tool identity, session state, and permission mode.
type ToolHookContext struct {
	// ToolName is the name of the tool being invoked (e.g., "Bash", "Edit").
	ToolName string

	// ToolInput is the parsed input map for the tool call.
	ToolInput map[string]any

	// SessionID identifies the current session.
	SessionID string

	// TurnNumber is the current conversation turn index.
	TurnNumber int

	// IsSubAgent indicates whether this tool call originates from a sub-agent.
	IsSubAgent bool

	// PermissionMode is the active permission mode (e.g., "auto", "default").
	PermissionMode string
}

// PreToolResult aggregates the outcome of all pre-tool hooks.
// The runner combines results from permission rules, shell hooks, and Go hooks.
type PreToolResult struct {
	// Allowed indicates whether tool execution may proceed.
	Allowed bool

	// Reason explains why tool execution was denied (empty if Allowed is true).
	Reason string

	// ModifiedInput is the potentially modified tool input.
	// Hooks may alter input parameters before the tool executes.
	// Nil means the original input should be used unchanged.
	ModifiedInput map[string]any

	// SkipExecution indicates that the tool should not be executed,
	// but without treating it as a hard denial (e.g., a hook handled
	// the operation itself).
	SkipExecution bool
}

// PostToolResult aggregates the outcome of all post-tool hooks.
type PostToolResult struct {
	// ModifiedOutput is the potentially rewritten tool output.
	// Empty string means the original output is unchanged.
	ModifiedOutput string

	// ShouldAbort indicates that the agent loop should stop after this tool.
	ShouldAbort bool

	// AbortReason explains why the loop should be aborted.
	AbortReason string
}

// PreHookFunc is the signature for programmatic pre-tool hooks.
type PreHookFunc func(ctx context.Context, hookCtx *ToolHookContext) (*PreToolResult, error)

// PostHookFunc is the signature for programmatic post-tool hooks.
type PostHookFunc func(ctx context.Context, hookCtx *ToolHookContext, result string) (*PostToolResult, error)

// ToolHookRunner orchestrates permission checks and hook execution around
// tool calls. It runs checks in the following order:
//
// Pre-hooks:
//  1. Permission rules (RulesEngine) — deny/ask blocks immediately
//  2. Shell pre-hooks — exit code 2 blocks execution
//  3. Registered Go pre-hooks — any denial blocks execution
//
// Post-hooks:
//  1. Shell post-hooks — exit code 2 triggers abort
//  2. Registered Go post-hooks — any abort signal stops the loop
type ToolHookRunner struct {
	permRules  *permission.RulesEngine
	shellHooks *hooks.ShellHookConfig
	preHooks   []PreHookFunc
	postHooks  []PostHookFunc
}

// NewToolHookRunner creates a ToolHookRunner with the given permission rules
// and shell hook configuration. Either parameter may be nil if that subsystem
// is not configured.
func NewToolHookRunner(rules *permission.RulesEngine, shellConfig *hooks.ShellHookConfig) *ToolHookRunner {
	return &ToolHookRunner{
		permRules:  rules,
		shellHooks: shellConfig,
	}
}

// RegisterPreHook adds a programmatic pre-tool hook. Hooks are executed in
// registration order after permission rules and shell hooks.
func (r *ToolHookRunner) RegisterPreHook(hook PreHookFunc) {
	r.preHooks = append(r.preHooks, hook)
}

// RegisterPostHook adds a programmatic post-tool hook. Hooks are executed in
// registration order after shell post-hooks.
func (r *ToolHookRunner) RegisterPostHook(hook PostHookFunc) {
	r.postHooks = append(r.postHooks, hook)
}

// RunPreHooks executes the full pre-tool hook pipeline:
//  1. Permission rules check — if the rules engine denies, return immediately.
//  2. Shell pre-hooks — if any exits with code 2, deny execution.
//  3. Go pre-hooks — if any returns Allowed=false, deny execution.
//
// The combined result uses deny-wins semantics: if any layer denies, the tool
// is blocked. Input modifications are chained: each hook sees the output of
// the previous one.
func (r *ToolHookRunner) RunPreHooks(ctx context.Context, hookCtx *ToolHookContext) (*PreToolResult, error) {
	result := &PreToolResult{
		Allowed:       true,
		ModifiedInput: nil,
	}

	currentInput := cloneMap(hookCtx.ToolInput)

	// Step 1: Check permission rules.
	if r.permRules != nil {
		action := r.permRules.Evaluate(hookCtx.ToolName, currentInput)
		switch action {
		case permission.ActionDeny:
			return &PreToolResult{
				Allowed: false,
				Reason:  fmt.Sprintf("permission rule denied tool %s", hookCtx.ToolName),
			}, nil
		case permission.ActionAsk:
			// In "ask" mode, we treat it as a soft denial that requires user
			// confirmation. For the hook runner, this means the tool is not
			// auto-allowed.
			return &PreToolResult{
				Allowed: false,
				Reason:  fmt.Sprintf("permission rule requires confirmation for tool %s", hookCtx.ToolName),
			}, nil
		case permission.ActionAllow:
			// Permitted — continue to hooks.
		}
	}

	// Step 2: Run shell pre-hooks.
	if r.shellHooks != nil {
		shellResults, err := hooks.RunPreToolHooks(ctx, r.shellHooks, hookCtx.ToolName, currentInput)
		if err != nil {
			return nil, fmt.Errorf("shell pre-hook error: %w", err)
		}

		for _, sr := range shellResults {
			// Exit code 2 means blocking error — deny execution.
			// Mirrors the reference: exit code 2 = blocking error.
			if sr.ExitCode == 2 {
				reason := strings.TrimSpace(sr.Stderr)
				if reason == "" {
					reason = strings.TrimSpace(sr.Stdout)
				}
				if reason == "" {
					reason = "blocked by shell pre-hook"
				}
				return &PreToolResult{
					Allowed: false,
					Reason:  reason,
				}, nil
			}

			if sr.TimedOut {
				return &PreToolResult{
					Allowed: false,
					Reason:  "shell pre-hook timed out",
				}, nil
			}

			// A successful shell hook may produce modified input on stdout
			// as JSON. Attempt to parse it; ignore if not valid JSON.
			if sr.ExitCode == 0 && sr.Stdout != "" {
				if modified := tryParseModifiedInput(sr.Stdout); modified != nil {
					currentInput = modified
					result.ModifiedInput = modified
				}
			}
		}
	}

	// Step 3: Run registered Go pre-hooks.
	for _, hook := range r.preHooks {
		// Update the hook context with the potentially modified input.
		hookCtx.ToolInput = currentInput

		hookResult, err := hook(ctx, hookCtx)
		if err != nil {
			return nil, fmt.Errorf("pre-hook error: %w", err)
		}
		if hookResult == nil {
			continue
		}

		// Deny wins: if any hook denies, stop immediately.
		if !hookResult.Allowed {
			return &PreToolResult{
				Allowed:       false,
				Reason:        hookResult.Reason,
				ModifiedInput: result.ModifiedInput,
			}, nil
		}

		// Chain input modifications.
		if hookResult.ModifiedInput != nil {
			currentInput = hookResult.ModifiedInput
			result.ModifiedInput = hookResult.ModifiedInput
		}

		// SkipExecution propagates (any hook can request skip).
		if hookResult.SkipExecution {
			result.SkipExecution = true
		}
	}

	// Final result: allowed, with potentially modified input.
	result.ModifiedInput = currentInput
	return result, nil
}

// RunPostHooks executes the full post-tool hook pipeline:
//  1. Shell post-hooks — exit code 2 triggers abort.
//  2. Go post-hooks — any ShouldAbort=true triggers abort.
//
// Output modifications are chained: each hook sees the result of the previous.
// Abort-wins semantics: if any hook signals abort, the combined result aborts.
func (r *ToolHookRunner) RunPostHooks(ctx context.Context, hookCtx *ToolHookContext, toolResult string) (*PostToolResult, error) {
	result := &PostToolResult{
		ModifiedOutput: "",
		ShouldAbort:    false,
	}

	currentOutput := toolResult

	// Step 1: Run shell post-hooks.
	if r.shellHooks != nil {
		shellResults, err := hooks.RunPostToolHooks(ctx, r.shellHooks, hookCtx.ToolName, hookCtx.ToolInput, currentOutput)
		if err != nil {
			return nil, fmt.Errorf("shell post-hook error: %w", err)
		}

		for _, sr := range shellResults {
			// Exit code 2 means blocking error — signal abort.
			if sr.ExitCode == 2 {
				reason := strings.TrimSpace(sr.Stderr)
				if reason == "" {
					reason = strings.TrimSpace(sr.Stdout)
				}
				if reason == "" {
					reason = "aborted by shell post-hook"
				}
				result.ShouldAbort = true
				result.AbortReason = reason
			}

			if sr.TimedOut {
				result.ShouldAbort = true
				result.AbortReason = "shell post-hook timed out"
			}

			// A successful shell hook may produce modified output on stdout.
			if sr.ExitCode == 0 && sr.Stdout != "" {
				trimmed := strings.TrimSpace(sr.Stdout)
				if trimmed != "" {
					currentOutput = trimmed
					result.ModifiedOutput = trimmed
				}
			}
		}
	}

	// Step 2: Run registered Go post-hooks.
	for _, hook := range r.postHooks {
		hookResult, err := hook(ctx, hookCtx, currentOutput)
		if err != nil {
			return nil, fmt.Errorf("post-hook error: %w", err)
		}
		if hookResult == nil {
			continue
		}

		// Chain output modifications.
		if hookResult.ModifiedOutput != "" {
			currentOutput = hookResult.ModifiedOutput
			result.ModifiedOutput = currentOutput
		}

		// Abort wins: if any hook signals abort, propagate.
		if hookResult.ShouldAbort {
			result.ShouldAbort = true
			if hookResult.AbortReason != "" {
				result.AbortReason = hookResult.AbortReason
			}
		}
	}

	return result, nil
}

// cloneMap creates a shallow copy of a map.
func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(m))
	for k, v := range m {
		cloned[k] = v
	}
	return cloned
}

// tryParseModifiedInput attempts to parse stdout from a shell hook as a JSON
// object representing modified tool input. Returns nil if parsing fails or
// the output is not a JSON object.
func tryParseModifiedInput(stdout string) map[string]any {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" || trimmed[0] != '{' {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil
	}
	return parsed
}
