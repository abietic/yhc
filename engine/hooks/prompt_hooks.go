package hooks

import (
	"context"
	"fmt"
	"log"
)

// ---------------------------------------------------------------------------
// Prompt Hook Executor
//
// Prompt hooks receive the assembled system prompt before it is sent to the
// model. They can inspect and modify the prompt text. Multiple prompt hooks
// are applied in registration order (pipeline style).
//
// This mirrors the prompt hook concept from the reference implementation
// (src/utils/hooks/execPromptHook.ts), adapted for the Go port's synchronous
// hook execution model. The reference uses an LLM to evaluate prompt hooks;
// this port provides a function-based interface that can be backed by any
// implementation (including LLM calls if desired).
// ---------------------------------------------------------------------------

// PromptHookResult holds the outcome of a prompt hook execution.
type PromptHookResult struct {
	// ModifiedPrompt is the potentially modified system prompt.
	// If empty, the original prompt passes through unchanged.
	ModifiedPrompt string

	// AdditionalContext is extra context to inject alongside the prompt.
	// This is appended to the system prompt rather than replacing it.
	AdditionalContext string

	// Block indicates the hook wants to block the query from proceeding.
	// When true, BlockReason should explain why.
	Block bool

	// BlockReason explains why the hook is blocking.
	BlockReason string

	// Attachments are additional metadata messages to inject.
	Attachments []string
}

// PromptHook is a function that receives the current system prompt and
// returns a result that may modify it. Hooks are applied in registration
// order; each hook receives the output of the previous one.
//
// Parameters:
//   - ctx: context for cancellation/timeout
//   - prompt: the current system prompt text (may have been modified by prior hooks)
//   - metadata: additional context about the prompt (model name, agent ID, etc.)
//
// Returns nil to pass through unchanged, or a result to modify the prompt.
type PromptHook func(ctx context.Context, prompt string, metadata *PromptHookMetadata) *PromptHookResult

// PromptHookMetadata provides context about the prompt being processed.
type PromptHookMetadata struct {
	// ModelName is the model that will receive this prompt.
	ModelName string
	// AgentID is the agent (if any) that owns this prompt.
	AgentID string
	// SessionID is the current session identifier.
	SessionID string
	// TurnNumber is the current turn number.
	TurnNumber int
	// IsSubagent indicates whether this prompt is for a sub-agent.
	IsSubagent bool
}

// AggregatedPromptHookResult is the final result after all prompt hooks run.
type AggregatedPromptHookResult struct {
	// FinalPrompt is the prompt after all hooks have been applied.
	FinalPrompt string
	// AdditionalContexts collects all additional context strings from hooks.
	AdditionalContexts []string
	// Blocked is true if any hook blocked the query.
	Blocked bool
	// BlockReason is the reason for blocking (from the first blocking hook).
	BlockReason string
	// HooksExecuted is the number of hooks that ran successfully.
	HooksExecuted int
	// Errors collects non-fatal errors from hooks that failed.
	Errors []error
}

// RegisterPromptHook registers a prompt hook. Hooks fire in registration order.
func (e *Executor) RegisterPromptHook(h PromptHook) {
	e.promptHooks = append(e.promptHooks, h)
}

// ExecutePromptHooks runs all registered prompt hooks in order, threading the
// prompt through each one. If a hook fails (panics or returns an error via
// context), the original prompt is preserved and execution continues with the
// next hook. If a hook blocks, execution stops and the block result is returned.
//
// This implements graceful degradation: hook failures use the original prompt
// rather than crashing the system.
func (e *Executor) ExecutePromptHooks(
	ctx context.Context,
	prompt string,
	metadata *PromptHookMetadata,
) *AggregatedPromptHookResult {
	result := &AggregatedPromptHookResult{
		FinalPrompt: prompt,
	}

	if len(e.promptHooks) == 0 {
		return result
	}

	currentPrompt := prompt

	for i, h := range e.promptHooks {
		// Execute with panic recovery per hook.
		hookResult, err := executePromptHookSafe(ctx, h, currentPrompt, metadata)
		if err != nil {
			// Hook failed: log and continue with current prompt.
			result.Errors = append(result.Errors, fmt.Errorf("prompt hook %d: %w", i, err))
			continue
		}

		if hookResult == nil {
			// nil = pass-through, no modification.
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

		// Apply modifications.
		if hookResult.ModifiedPrompt != "" {
			currentPrompt = hookResult.ModifiedPrompt
		}
		if hookResult.AdditionalContext != "" {
			result.AdditionalContexts = append(result.AdditionalContexts, hookResult.AdditionalContext)
		}

		result.HooksExecuted++
	}

	result.FinalPrompt = currentPrompt
	return result
}

// executePromptHookSafe runs a single prompt hook with panic recovery.
func executePromptHookSafe(
	ctx context.Context,
	h PromptHook,
	prompt string,
	metadata *PromptHookMetadata,
) (result *PromptHookResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic in prompt hook: %v", r)
			log.Printf("[hooks] prompt hook panicked: %v", r)
		}
	}()

	// Check context before execution.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	result = h(ctx, prompt, metadata)
	return result, nil
}

// HasPromptHooks returns true if any prompt hooks are registered.
func (e *Executor) HasPromptHooks() bool {
	return len(e.promptHooks) > 0
}
