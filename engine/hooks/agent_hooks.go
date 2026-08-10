package hooks

import (
	"context"
	"fmt"
	"log"
)

// ---------------------------------------------------------------------------
// Agent Hook Executor
//
// Agent hooks fire at sub-agent lifecycle boundaries: start, complete, and
// fail. They allow external code to observe or modify agent behavior.
//
// This mirrors the SubagentStart/SubagentStop hook events from the reference
// implementation (src/utils/hooks.ts executeSubagentStartHooks, executeStopHooks
// with subagentId). In the reference, these are wired through the same hook
// dispatch pipeline as tool hooks.
// ---------------------------------------------------------------------------

// AgentHookEvent represents the lifecycle phase of an agent hook.
type AgentHookEvent string

const (
	// AgentHookEventStart fires when a sub-agent is about to begin execution.
	AgentHookEventStart AgentHookEvent = "start"
	// AgentHookEventComplete fires when a sub-agent finishes successfully.
	AgentHookEventComplete AgentHookEvent = "complete"
	// AgentHookEventFail fires when a sub-agent fails with an error.
	AgentHookEventFail AgentHookEvent = "fail"
)

// AgentHookContext provides context about the agent lifecycle event.
type AgentHookContext struct {
	// AgentID is the unique identifier for the sub-agent instance.
	AgentID string
	// AgentType is the type/name of the sub-agent (e.g., "code_review", "test_runner").
	AgentType string
	// ParentAgentID is the ID of the parent agent that spawned this sub-agent.
	// Empty for top-level agents.
	ParentAgentID string
	// SessionID is the current session identifier.
	SessionID string
	// Event indicates which lifecycle phase triggered this hook.
	Event AgentHookEvent
	// Config holds the agent's configuration (for start hooks).
	Config map[string]any
	// Result holds the agent's result summary (for complete hooks).
	Result string
	// Error holds the error information (for fail hooks).
	Error error
}

// AgentStartHookResult is the result from an agent-start hook.
type AgentStartHookResult struct {
	// AdditionalInstructions are extra instructions to inject into the agent's prompt.
	AdditionalInstructions []string
	// AdditionalContext is context to inject as a user message.
	AdditionalContext string
	// LimitTools restricts the tools available to the agent.
	// Empty means no restriction.
	LimitTools []string
	// Block prevents the agent from starting.
	Block bool
	// BlockReason explains why the agent was blocked.
	BlockReason string
}

// AgentCompleteHookResult is the result from an agent-complete hook.
type AgentCompleteHookResult struct {
	// PostProcessedResult is a modified result string (replaces the original if non-empty).
	PostProcessedResult string
}

// AgentFailHookResult is the result from an agent-fail hook.
type AgentFailHookResult struct {
	// Retry indicates whether the agent should be retried.
	Retry bool
	// RetryReason explains why a retry is recommended.
	RetryReason string
}

// AgentStartHook fires when a sub-agent is about to start.
// It may inject instructions, limit tools, or block the agent entirely.
type AgentStartHook func(ctx context.Context, agentCtx *AgentHookContext) *AgentStartHookResult

// AgentCompleteHook fires when a sub-agent completes successfully.
// It may post-process the result.
type AgentCompleteHook func(ctx context.Context, agentCtx *AgentHookContext) *AgentCompleteHookResult

// AgentFailHook fires when a sub-agent fails.
// It may recommend a retry.
type AgentFailHook func(ctx context.Context, agentCtx *AgentHookContext) *AgentFailHookResult

// AggregatedAgentStartResult is the aggregated result from all agent-start hooks.
type AggregatedAgentStartResult struct {
	// AdditionalInstructions collects all injected instructions.
	AdditionalInstructions []string
	// AdditionalContexts collects all injected context strings.
	AdditionalContexts []string
	// LimitTools is the intersection of all tool restrictions.
	// Empty means no restriction.
	LimitTools []string
	// Blocked is true if any hook blocked the agent.
	Blocked bool
	// BlockReason is the reason for blocking.
	BlockReason string
	// HooksExecuted is the number of hooks that ran successfully.
	HooksExecuted int
	// Errors collects non-fatal hook errors.
	Errors []error
}

// AggregatedAgentCompleteResult is the aggregated result from all agent-complete hooks.
type AggregatedAgentCompleteResult struct {
	// FinalResult is the result after all post-processing hooks.
	FinalResult string
	// HooksExecuted is the number of hooks that ran successfully.
	HooksExecuted int
	// Errors collects non-fatal hook errors.
	Errors []error
}

// AggregatedAgentFailResult is the aggregated result from all agent-fail hooks.
type AggregatedAgentFailResult struct {
	// Retry is true if any hook recommends retrying.
	Retry bool
	// RetryReason is the reason for retry (from the first recommending hook).
	RetryReason string
	// HooksExecuted is the number of hooks that ran successfully.
	HooksExecuted int
	// Errors collects non-fatal hook errors.
	Errors []error
}

// RegisterAgentStart registers an agent-start hook.
func (e *Executor) RegisterAgentStart(h AgentStartHook) {
	e.agentStartHooks = append(e.agentStartHooks, h)
}

// RegisterAgentComplete registers an agent-complete hook.
func (e *Executor) RegisterAgentComplete(h AgentCompleteHook) {
	e.agentCompleteHooks = append(e.agentCompleteHooks, h)
}

// RegisterAgentFail registers an agent-fail hook.
func (e *Executor) RegisterAgentFail(h AgentFailHook) {
	e.agentFailHooks = append(e.agentFailHooks, h)
}

// ExecuteAgentStart runs all agent-start hooks and returns the aggregated result.
// Hook failures are non-fatal: the agent proceeds with whatever hooks succeeded.
func (e *Executor) ExecuteAgentStart(
	ctx context.Context,
	agentCtx *AgentHookContext,
) *AggregatedAgentStartResult {
	result := &AggregatedAgentStartResult{}

	if len(e.agentStartHooks) == 0 {
		return result
	}

	agentCtx.Event = AgentHookEventStart

	for i, h := range e.agentStartHooks {
		hookResult, err := executeAgentStartHookSafe(ctx, h, agentCtx)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("agent start hook %d: %w", i, err))
			continue
		}

		if hookResult == nil {
			result.HooksExecuted++
			continue
		}

		// Check for blocking.
		if hookResult.Block {
			result.Blocked = true
			result.BlockReason = hookResult.BlockReason
			result.HooksExecuted++
			return result
		}

		// Aggregate instructions.
		result.AdditionalInstructions = append(result.AdditionalInstructions, hookResult.AdditionalInstructions...)
		if hookResult.AdditionalContext != "" {
			result.AdditionalContexts = append(result.AdditionalContexts, hookResult.AdditionalContext)
		}
		if len(hookResult.LimitTools) > 0 {
			if len(result.LimitTools) == 0 {
				result.LimitTools = hookResult.LimitTools
			} else {
				// Intersect tool lists.
				result.LimitTools = intersectStrings(result.LimitTools, hookResult.LimitTools)
			}
		}

		result.HooksExecuted++
	}

	return result
}

// ExecuteAgentComplete runs all agent-complete hooks and returns the aggregated result.
func (e *Executor) ExecuteAgentComplete(
	ctx context.Context,
	agentCtx *AgentHookContext,
) *AggregatedAgentCompleteResult {
	result := &AggregatedAgentCompleteResult{
		FinalResult: agentCtx.Result,
	}

	if len(e.agentCompleteHooks) == 0 {
		return result
	}

	agentCtx.Event = AgentHookEventComplete

	for i, h := range e.agentCompleteHooks {
		hookResult, err := executeAgentCompleteHookSafe(ctx, h, agentCtx)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("agent complete hook %d: %w", i, err))
			continue
		}

		if hookResult == nil {
			result.HooksExecuted++
			continue
		}

		if hookResult.PostProcessedResult != "" {
			result.FinalResult = hookResult.PostProcessedResult
			// Update context for subsequent hooks.
			agentCtx.Result = hookResult.PostProcessedResult
		}

		result.HooksExecuted++
	}

	return result
}

// ExecuteAgentFail runs all agent-fail hooks and returns the aggregated result.
func (e *Executor) ExecuteAgentFail(
	ctx context.Context,
	agentCtx *AgentHookContext,
) *AggregatedAgentFailResult {
	result := &AggregatedAgentFailResult{}

	if len(e.agentFailHooks) == 0 {
		return result
	}

	agentCtx.Event = AgentHookEventFail

	for i, h := range e.agentFailHooks {
		hookResult, err := executeAgentFailHookSafe(ctx, h, agentCtx)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("agent fail hook %d: %w", i, err))
			continue
		}

		if hookResult == nil {
			result.HooksExecuted++
			continue
		}

		if hookResult.Retry && !result.Retry {
			result.Retry = true
			result.RetryReason = hookResult.RetryReason
		}

		result.HooksExecuted++
	}

	return result
}

// HasAgentHooks returns true if any agent hooks are registered.
func (e *Executor) HasAgentHooks() bool {
	return len(e.agentStartHooks) > 0 || len(e.agentCompleteHooks) > 0 || len(e.agentFailHooks) > 0
}

// ---------------------------------------------------------------------------
// Safe execution helpers with panic recovery
// ---------------------------------------------------------------------------

func executeAgentStartHookSafe(ctx context.Context, h AgentStartHook, agentCtx *AgentHookContext) (result *AgentStartHookResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic in agent start hook: %v", r)
			log.Printf("[hooks] agent start hook panicked: %v", r)
		}
	}()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	result = h(ctx, agentCtx)
	return result, nil
}

func executeAgentCompleteHookSafe(ctx context.Context, h AgentCompleteHook, agentCtx *AgentHookContext) (result *AgentCompleteHookResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic in agent complete hook: %v", r)
			log.Printf("[hooks] agent complete hook panicked: %v", r)
		}
	}()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	result = h(ctx, agentCtx)
	return result, nil
}

func executeAgentFailHookSafe(ctx context.Context, h AgentFailHook, agentCtx *AgentHookContext) (result *AgentFailHookResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic in agent fail hook: %v", r)
			log.Printf("[hooks] agent fail hook panicked: %v", r)
		}
	}()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	result = h(ctx, agentCtx)
	return result, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// intersectStrings returns elements present in both slices.
func intersectStrings(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, s := range b {
		set[s] = true
	}
	var result []string
	for _, s := range a {
		if set[s] {
			result = append(result, s)
		}
	}
	return result
}
