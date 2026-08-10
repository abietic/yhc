package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/tools"
)

// canonicalToolRoundInput contains the live dependencies for one committed
// tool-call set. It stays on the Graph node stack and must not be persisted in
// Compose local state.
type canonicalToolRoundInput struct {
	params            QueryParams
	toolCalls         []*schema.ToolCall
	toolUseContext    *ToolUseContext
	cancellationChain *CancellationChain
	hookExecutor      *hooks.Executor
	queryTracking     *QueryTracking
	yield             func(QueryEvent)
}

// canonicalToolRoundResult retains rich project outcomes through the
// complete-round decision. The Graph adapter projects only cloned messages and
// tagged control data into local state.
type canonicalToolRoundResult struct {
	toolResults    []*schema.Message
	outcomes       []toolRoundOutcome
	decision       afterToolDecision
	toolUseContext *ToolUseContext
}

// runCanonicalToolRound executes one model-committed call set through the
// P13.3 strict schedule, the project stable scheduler, and executeToolCall. It
// preserves call identity and rich outcomes until the typed after-tool
// decision is complete.
func runCanonicalToolRound(
	ctx context.Context,
	input canonicalToolRoundInput,
) (canonicalToolRoundResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(input.toolCalls) == 0 {
		return canonicalToolRoundResult{}, fmt.Errorf(
			"canonical tool round requires at least one committed call",
		)
	}
	yield := input.yield
	if yield == nil {
		yield = func(QueryEvent) {}
	}

	schedule, err := newToolSchedule(
		input.toolCalls,
		func(call *schema.ToolCall) bool {
			return isToolConcurrencySafe(call, input.params.ToolRegistry)
		},
	)
	if err != nil {
		return canonicalToolRoundResult{}, err
	}
	scheduledByID := make(map[string]toolScheduleCall, len(schedule.Calls))
	for _, call := range schedule.Calls {
		scheduledByID[call.CallID] = call
	}

	rootCtx, releaseRoot := canonicalToolRoundContext(
		ctx,
		input.toolUseContext,
		input.cancellationChain,
	)
	defer releaseRoot()
	executionCtx, releaseExecution := canonicalToolExecutionContext(
		ctx,
		input.cancellationChain,
	)
	defer releaseExecution()

	result := canonicalToolRoundResult{
		outcomes:       make([]toolRoundOutcome, 0, len(schedule.Calls)),
		toolResults:    make([]*schema.Message, 0, len(schedule.Calls)),
		toolUseContext: input.toolUseContext,
	}
	var toolContextMu sync.Mutex
	var outcomesMu sync.Mutex
	outcomeByID := make(map[string]*toolExecutionOutcome, len(schedule.Calls))
	var projectionErrMu sync.Mutex
	var lifecycleProjectionErr error

	completed := execution.ExecuteCommittedToolCalls(
		rootCtx,
		input.toolCalls,
		execution.StreamingToolExecutorConfig{
			Ctx: executionCtx,
			IsInterrupted: func() bool {
				if ctx.Err() != nil {
					return true
				}
				if input.toolUseContext != nil &&
					input.toolUseContext.AbortController != nil &&
					input.toolUseContext.AbortController.Aborted() {
					return true
				}
				return input.cancellationChain != nil &&
					input.cancellationChain.RootContext().Err() != nil
			},
			OnInterrupt: func(toolCall *schema.ToolCall) {
				if toolCall != nil &&
					input.params.CancelToolInteraction != nil {
					input.params.CancelToolInteraction(toolCall.ID)
				}
			},
			PrepareForExecution: func(toolCall *schema.ToolCall) *schema.ToolCall {
				return reserveRepeatedToolCall(toolCall, input.params.repeatedToolGuard)
			},
			IsConcurrencySafe: func(toolCall *schema.ToolCall) bool {
				if toolCall == nil {
					return false
				}
				planned, ok := scheduledByID[toolCall.ID]
				return ok &&
					planned.ToolName == toolCall.Function.Name &&
					planned.ArgumentsDigest == toolArgumentsDigest(
						toolCall.Function.Arguments,
					) &&
					planned.ConcurrencySafe
			},
			GetInterruptBehavior: func(toolName string) string {
				return canonicalToolInterruptBehavior(
					input.params.ToolRegistry,
					toolName,
				)
			},
			ExecuteWithContext: func(
				execCtx context.Context,
				toolCall *schema.ToolCall,
			) *execution.ToolResult {
				if toolCall == nil {
					return nil
				}
				toolCtx, releaseTool := canonicalToolCallContext(
					execCtx,
					input.cancellationChain,
					toolCall.ID,
				)
				defer releaseTool()
				toolCtx = tools.WithProgressFn(
					toolCtx,
					func(event tools.ToolProgressEvent) {
						projection, projectionErr := buildCanonicalToolProgressProjection(
							toolCall.ID,
							event.Content,
						)
						if projectionErr != nil {
							projectionErrMu.Lock()
							if lifecycleProjectionErr == nil {
								lifecycleProjectionErr = projectionErr
							}
							projectionErrMu.Unlock()
							return
						}
						yield(projection)
						yield(QueryEvent{
							Type: EventToolProgress,
							ToolProgress: &ToolProgressEvent{
								ToolName:  event.ToolName,
								ToolUseID: event.ToolUseID,
								Content:   event.Content,
								IsFinal:   event.IsFinal,
							},
						})
					},
				)

				toolContextMu.Lock()
				currentToolContext := result.toolUseContext
				toolContextMu.Unlock()
				outcome := executeToolCall(
					toolCtx,
					input.params,
					input.hookExecutor,
					currentToolContext,
					toolCall,
					yield,
				)
				if outcome == nil {
					return nil
				}
				outcomesMu.Lock()
				outcomeByID[toolCall.ID] = outcome
				outcomesMu.Unlock()
				executionResult := canonicalExecutionToolResult(
					toolCall,
					outcome,
				)
				if outcome.ContextModifier != nil {
					executionResult.ContextModifier = func() (func(), error) {
						toolContextMu.Lock()
						defer toolContextMu.Unlock()
						updated, publish, transitionErr := outcome.ContextModifier(
							result.toolUseContext,
						)
						if transitionErr != nil {
							return nil, transitionErr
						}
						result.toolUseContext = updated
						if result.toolUseContext != nil &&
							input.queryTracking != nil {
							result.toolUseContext.QueryTracking = input.queryTracking
						}
						return publish, nil
					}
				}
				return executionResult
			},
		},
	)
	projectionErrMu.Lock()
	projectionErr := lifecycleProjectionErr
	projectionErrMu.Unlock()
	if projectionErr != nil {
		return canonicalToolRoundResult{}, fmt.Errorf(
			"project canonical tool progress: %w",
			projectionErr,
		)
	}
	if len(completed) != len(schedule.Calls) {
		return canonicalToolRoundResult{}, fmt.Errorf(
			"canonical tool round returned %d results for %d scheduled calls",
			len(completed),
			len(schedule.Calls),
		)
	}

	for index, planned := range schedule.Calls {
		toolResult := completed[index]
		if toolResult == nil || toolResult.ToolCallID != planned.CallID {
			actual := ""
			if toolResult != nil {
				actual = toolResult.ToolCallID
			}
			return canonicalToolRoundResult{}, fmt.Errorf(
				"canonical tool round result %d has call ID %q, want %q",
				index,
				actual,
				planned.CallID,
			)
		}
		outcomesMu.Lock()
		outcome := outcomeByID[planned.CallID]
		outcomesMu.Unlock()
		if outcome == nil {
			outcome = canonicalOutcomeFromExecutionResult(toolResult)
		}
		result.outcomes = append(result.outcomes, toolRoundOutcome{
			CallID:  planned.CallID,
			Outcome: outcome,
		})
		if err := emitStreamingToolResult(
			yield,
			&result.toolResults,
			toolResult,
		); err != nil {
			return canonicalToolRoundResult{}, err
		}
	}
	emitTaskLifecycleEvents(yield, input.params.TaskLifecycleDrainer)

	decision, err := decideAfterToolRound(schedule, result.outcomes)
	if err != nil {
		return canonicalToolRoundResult{}, err
	}
	var controller *AbortController
	if input.toolUseContext != nil {
		controller = input.toolUseContext.AbortController
	}
	if controller != nil && controller.Aborted() {
		if input.cancellationChain != nil {
			input.cancellationChain.Cancel("abort_tools")
		}
		interruptID := strings.TrimSpace(controller.Reason)
		if interruptID == "" {
			interruptID = "abort_tools"
		}
		decision = afterToolDecision{
			Kind:           afterToolInterrupt,
			InterruptID:    interruptID,
			TerminalReason: TerminalAbortedTools,
		}
	} else if err := ctx.Err(); err != nil {
		return canonicalToolRoundResult{}, err
	} else if err := rootCtx.Err(); err != nil {
		return canonicalToolRoundResult{}, err
	}
	result.decision = decision
	return result, nil
}

func canonicalToolInterruptBehavior(
	registry *tools.Registry,
	toolName string,
) string {
	if registry == nil {
		return "block"
	}
	impl, ok := registry.Get(toolName)
	if !ok {
		return "block"
	}
	if impl.IsPlanModeTransition || impl.InterruptBehavior == "cancel" {
		return "cancel"
	}
	return "block"
}

func canonicalToolRoundContext(
	ctx context.Context,
	toolUseContext *ToolUseContext,
	cancellationChain *CancellationChain,
) (context.Context, func()) {
	sources := make([]context.Context, 0, 2)
	if toolUseContext != nil &&
		toolUseContext.AbortController != nil &&
		toolUseContext.AbortController.Ctx != nil {
		sources = append(sources, toolUseContext.AbortController.Ctx)
	}
	if cancellationChain != nil {
		sources = append(sources, cancellationChain.RootContext())
	}
	return linkCanonicalContexts(ctx, sources...)
}

func canonicalToolExecutionContext(
	ctx context.Context,
	cancellationChain *CancellationChain,
) (context.Context, func()) {
	if cancellationChain == nil {
		return linkCanonicalContexts(ctx)
	}
	return linkCanonicalContexts(ctx, cancellationChain.RootContext())
}

func canonicalToolCallContext(
	ctx context.Context,
	cancellationChain *CancellationChain,
	toolUseID string,
) (context.Context, func()) {
	if cancellationChain == nil || strings.TrimSpace(toolUseID) == "" {
		return ctx, func() {}
	}
	toolCtx := cancellationChain.ToolContext(toolUseID)
	linked, release := linkCanonicalContexts(ctx, toolCtx)
	return linked, func() {
		release()
		cancellationChain.ReleaseTool(toolUseID)
	}
}

func linkCanonicalContexts(
	parent context.Context,
	sources ...context.Context,
) (context.Context, func()) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	stops := make([]func() bool, 0, len(sources))
	for _, source := range sources {
		if source == nil {
			continue
		}
		stops = append(stops, context.AfterFunc(source, cancel))
	}
	return ctx, func() {
		for _, stop := range stops {
			stop()
		}
		cancel()
	}
}

func canonicalExecutionToolResult(
	toolCall *schema.ToolCall,
	outcome *toolExecutionOutcome,
) *execution.ToolResult {
	if outcome == nil {
		return nil
	}
	result := &execution.ToolResult{
		BeforeMessages:      append([]*schema.Message(nil), outcome.BeforeResults...),
		AfterMessages:       append([]*schema.Message(nil), outcome.AfterResults...),
		PreventContinuation: outcome.PreventContinuation,
	}
	if outcome.Result == nil {
		return result
	}
	result.Message = outcome.Result
	result.Result = outcome.Result.Content
	result.ToolCallID = outcome.Result.ToolCallID
	result.ToolName = outcome.Result.ToolName
	if result.ToolCallID == "" && toolCall != nil {
		result.ToolCallID = toolCall.ID
	}
	if result.ToolName == "" && toolCall != nil {
		result.ToolName = toolCall.Function.Name
	}
	if outcome.Result.Extra != nil {
		result.IsError, _ = outcome.Result.Extra["is_error"].(bool)
	}
	return result
}

func canonicalOutcomeFromExecutionResult(
	result *execution.ToolResult,
) *toolExecutionOutcome {
	if result == nil {
		return nil
	}
	return &toolExecutionOutcome{
		BeforeResults:       append([]*schema.Message(nil), result.BeforeMessages...),
		Result:              result.Message,
		AfterResults:        append([]*schema.Message(nil), result.AfterMessages...),
		PreventContinuation: result.PreventContinuation,
	}
}
