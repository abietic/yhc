package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/attachments"
	"github.com/abietic/yhc/engine/budget"
	"github.com/abietic/yhc/engine/compact"
	promptctx "github.com/abietic/yhc/engine/context"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/prefetch"
	"github.com/abietic/yhc/engine/recovery"
)

type canonicalLoopAction string

const (
	canonicalLoopContinue canonicalLoopAction = "continue"
	canonicalLoopModel    canonicalLoopAction = "model"
	canonicalLoopTool     canonicalLoopAction = "tool"
	canonicalLoopTerminal canonicalLoopAction = "terminal"
)

type canonicalLoopDecision struct {
	action   canonicalLoopAction
	terminal *Terminal
}

type canonicalRoundPreparationInput struct {
	params                 QueryParams
	deps                   *QueryDeps
	state                  *QueryState
	hookExecutor           *hooks.Executor
	compactTracking        **compact.CompactTracking
	taskBudgetRemaining    **int
	consumedRuntimeItemIDs *[]string
	yield                  func(QueryEvent)
}

type canonicalRoundPreparationResult struct {
	canonicalLoopDecision
	messagesForQuery  []*schema.Message
	fullSystemPrompt  *schema.Message
	queryTracking     *QueryTracking
	toolUseContext    *ToolUseContext
	cancellationChain *CancellationChain
	skillPrefetch     *prefetch.SkillPrefetch
}

// runCanonicalRoundPreparation owns the safe pre-model boundary: asynchronous
// attachments, child pause, tracking, compact/recovery preparation, transcript
// snapshot, post-compact context reinjection, and blocking-limit classification.
func runCanonicalRoundPreparation(
	ctx context.Context,
	input canonicalRoundPreparationInput,
) canonicalRoundPreparationResult {
	result := canonicalRoundPreparationResult{}
	state := input.state
	if state == nil {
		terminal := Terminal{
			Reason: TerminalModelError,
			Err:    fmt.Errorf("canonical round preparation requires query state"),
		}
		result.action = canonicalLoopTerminal
		result.terminal = &terminal
		return result
	}
	messages := state.Messages
	if terminal := claimRuntimeStopAtSafePoint(
		input.params,
		state.ToolUseContext,
		false,
		input.yield,
	); terminal != nil {
		result.action = canonicalLoopTerminal
		result.terminal = terminal
		return result
	}
	if state.MediaRecovery.PendingAttempt != mediaRecoveryAttemptNone {
		return prepareMediaRecoveryRound(ctx, input)
	}
	runtimeMessages, runtimeErr := collectRuntimeItemsAtSafePoint(
		ctx,
		input.params,
		state.ToolUseContext,
		false,
		state.TurnCount <= 1,
		input.yield,
		input.consumedRuntimeItemIDs,
	)
	if runtimeErr != nil {
		terminal := Terminal{Reason: TerminalModelError, Err: runtimeErr}
		result.action = canonicalLoopTerminal
		result.terminal = &terminal
		return result
	}
	messages = append(messages, runtimeMessages...)
	asyncHookMessages := drainAsyncHookMessages(input.hookExecutor)
	for _, message := range asyncHookMessages {
		if message != nil {
			messages = append(messages, message)
		}
	}
	state.Messages = messages
	toolUseContext := state.ToolUseContext

	if input.params.AgentPauseCheckpoint != nil &&
		toolUseContext != nil &&
		strings.TrimSpace(toolUseContext.AgentID) != "" {
		_, pauseErr := input.params.AgentPauseCheckpoint(
			ctx,
			toolUseContext.AgentID,
			func(lifecycle AgentLifecycleEvent) {
				transition := lifecycle
				input.yield(QueryEvent{
					Type:           EventAgentLifecycle,
					AgentLifecycle: &transition,
				})
			},
		)
		if pauseErr != nil {
			reason := TerminalModelError
			if errors.Is(pauseErr, context.Canceled) ||
				errors.Is(pauseErr, context.DeadlineExceeded) {
				input.yield(QueryEvent{
					Type:                EventUserInterruption,
					InterruptionToolUse: false,
				})
				reason = TerminalAbortedStreaming
			}
			terminal := Terminal{Reason: reason, Err: pauseErr}
			result.action = canonicalLoopTerminal
			result.terminal = &terminal
			return result
		}
	}

	cancellationChain := NewCancellationChain(ctx)
	skillPrefetch := prefetch.NewSkillPrefetch(input.params.SkillRegistry)
	skillPrefetch.Start(messages)
	input.yield(QueryEvent{Type: EventStreamRequestStart})
	for _, message := range asyncHookMessages {
		if message != nil {
			input.yield(QueryEvent{
				Type:              EventAttachment,
				AttachmentMessage: message,
			})
		}
	}

	var queryTracking *QueryTracking
	if toolUseContext != nil && toolUseContext.QueryTracking == nil {
		queryTracking = &QueryTracking{
			ChainID: input.deps.UUID(),
			Depth:   0,
		}
		toolUseContext.QueryTracking = queryTracking
	} else if toolUseContext != nil {
		queryTracking = &QueryTracking{
			ChainID: toolUseContext.QueryTracking.ChainID,
			Depth:   toolUseContext.QueryTracking.Depth + 1,
		}
		toolUseContext.QueryTracking = queryTracking
	}

	messagesForQuery := compact.GetMessagesAfterCompactBoundary(messages)
	messagesForQuery = compact.ApplyToolResultBudget(
		messagesForQuery,
		nil,
		nil,
		nil,
	)
	snipResult := compact.SnipCompactIfNeeded(messagesForQuery)
	messagesForQuery = snipResult.Messages
	snipTokensFreed := snipResult.TokensFreed
	if snipResult.BoundaryMessage != nil {
		input.yield(QueryEvent{
			Type:                   EventCompactBoundary,
			CompactBoundaryMessage: snipResult.BoundaryMessage,
		})
	}
	microResult := compact.Microcompact(
		messagesForQuery,
		string(input.params.QuerySource),
	)
	messagesForQuery = microResult.Messages
	collapseResult := compact.ApplyCollapsesIfNeeded(
		messagesForQuery,
		string(input.params.QuerySource),
	)
	messagesForQuery = collapseResult.Messages
	fullSystemPrompt := promptctx.AppendSystemContext(
		input.params.SystemPrompt,
		input.params.SystemContext,
	)
	modelName := ""
	if toolUseContext != nil && toolUseContext.Options != nil {
		modelName = toolUseContext.Options.MainLoopModel
	}

	preCompactCancelled := false
	if input.params.modelCompactionGuard != nil {
		preCompactCancelled = input.params.modelCompactionGuard(modelName) != nil
	}
	if input.hookExecutor != nil {
		tokenEstimate := compact.EstimateTokenCount(messagesForQuery) -
			snipTokensFreed
		preHookResult := input.hookExecutor.ExecutePreCompact(
			ctx,
			len(messagesForQuery),
			tokenEstimate,
		)
		if preHookResult != nil && preHookResult.Cancel {
			preCompactCancelled = true
		}
	}
	var compactResult *compact.AutoCompactResult
	var consecutiveFailures int
	if !preCompactCancelled {
		summaryModel := input.params.SummaryModel
		if input.params.promptRouteGuard != nil {
			// The current rich turn is admitted only for the selected main
			// route. SummaryModel has no independently bound route or media
			// capability in P30.1c, so keep auto-compaction deterministic
			// instead of forwarding rich parts to an unadmitted model.
			summaryModel = nil
		}
		var updatedTracking *compact.CompactTracking
		currentTracking := *input.compactTracking
		compactResult, consecutiveFailures, updatedTracking = compact.AutoCompact(
			messagesForQuery,
			string(input.params.QuerySource),
			currentTracking,
			snipTokensFreed,
			modelName,
			&compact.AutoCompactParams{
				Ctx:           ctx,
				ChatModel:     summaryModel,
				ProviderUsage: input.deps.ProviderUsage,
			},
		)
		*input.compactTracking = updatedTracking
		if consecutiveFailures >= 3 && compactResult == nil {
			input.yield(QueryEvent{
				Type: EventAttachment,
				AttachmentMessage: &schema.Message{
					Role: schema.User,
					Content: "Auto-compaction has failed multiple times. " +
						"Context window may be under pressure.",
					Extra: map[string]any{
						"is_meta":         true,
						"attachment_kind": "auto_compact_failure_warning",
						"level":           "warning",
					},
				},
			})
		}
	}

	if compactResult != nil {
		if input.hookExecutor != nil {
			postHookResult := input.hookExecutor.ExecutePostCompact(
				ctx,
				compactResult.PreCompactTokenCount,
				compactResult.PostCompactTokenCount,
			)
			if postHookResult != nil &&
				len(postHookResult.Attachments) > 0 {
				compactResult.HookResults = append(
					compactResult.HookResults,
					postHookResult.Attachments...,
				)
			}
		}
		if input.params.TaskBudget != nil {
			remaining := input.params.TaskBudget.Total
			if *input.taskBudgetRemaining != nil {
				remaining = **input.taskBudgetRemaining
			}
			remaining -= compactResult.PreCompactTokenCount
			if remaining < 0 {
				remaining = 0
			}
			*input.taskBudgetRemaining = &remaining
		}
		postCompactMessages := compact.BuildPostCompactMessages(compactResult)
		projectDir := ""
		if toolUseContext != nil && toolUseContext.Options != nil {
			projectDir = toolUseContext.Options.CWD
		}
		postCompactMessages, _ = compact.ApplyPostCompact(
			postCompactMessages,
			projectDir,
		)
		if toolUseContext != nil &&
			toolUseContext.Options != nil &&
			toolUseContext.Options.PlanFilePath != "" {
			if planMessage := compact.CreatePlanAttachmentIfNeeded(
				toolUseContext.Options.PlanFilePath,
			); planMessage != nil {
				postCompactMessages = append(
					postCompactMessages,
					planMessage,
				)
			}
		}
		for _, message := range postCompactMessages {
			input.yield(QueryEvent{
				Type:                   EventCompactBoundary,
				CompactBoundaryMessage: message,
			})
		}
		messagesForQuery = postCompactMessages
	}

	if compactResult == nil &&
		input.params.QuerySource != QuerySourceCompact {
		warningState := compact.CalculateTokenWarningState(
			compact.EstimateTokenCount(messagesForQuery),
			modelName,
		)
		if warningState.IsAtBlockingLimit {
			if !state.HasAttemptedReactiveCompact {
				reactiveResult := compact.TryReactiveCompact(
					messagesForQuery,
					string(input.params.QuerySource),
					"blocking_limit",
				)
				if reactiveResult != nil &&
					len(reactiveResult.Messages) > 0 {
					state.Messages = reactiveResult.Messages
					state.HasAttemptedReactiveCompact = true
					state.Transition = ContinueReactiveCompactRetry
					result.action = canonicalLoopContinue
					return result
				}
			}
			input.yield(QueryEvent{
				Type: EventAssistant,
				AssistantMessage: newAPIErrorMessage(
					"Prompt is too long",
					"invalid_request",
				),
			})
			terminal := Terminal{Reason: TerminalBlockingLimit}
			result.action = canonicalLoopTerminal
			result.terminal = &terminal
			return result
		}
	}

	if toolUseContext != nil {
		toolUseContext.Messages = messagesForQuery
	}
	result.action = canonicalLoopModel
	result.messagesForQuery = messagesForQuery
	result.fullSystemPrompt = fullSystemPrompt
	result.queryTracking = queryTracking
	result.toolUseContext = toolUseContext
	result.cancellationChain = cancellationChain
	result.skillPrefetch = skillPrefetch
	return result
}

func prepareMediaRecoveryRound(
	ctx context.Context,
	input canonicalRoundPreparationInput,
) canonicalRoundPreparationResult {
	state := input.state
	if state == nil {
		terminal := Terminal{
			Reason: TerminalModelError,
			Err:    errors.New("media recovery preparation requires query state"),
		}
		return canonicalRoundPreparationResult{
			canonicalLoopDecision: canonicalLoopDecision{
				action:   canonicalLoopTerminal,
				terminal: &terminal,
			},
		}
	}
	if err := ctx.Err(); err != nil {
		terminal := Terminal{Reason: TerminalAbortedStreaming, Err: err}
		return canonicalRoundPreparationResult{
			canonicalLoopDecision: canonicalLoopDecision{
				action:   canonicalLoopTerminal,
				terminal: &terminal,
			},
		}
	}
	messages := state.MediaRecovery.CanonicalMessages
	if len(messages) == 0 {
		messages = compact.GetMessagesAfterCompactBoundary(state.Messages)
	}
	toolUseContext := state.ToolUseContext
	var queryTracking *QueryTracking
	if toolUseContext != nil {
		toolUseContext.Messages = messages
		queryTracking = toolUseContext.QueryTracking
	}
	skillPrefetch := prefetch.NewSkillPrefetch(input.params.SkillRegistry)
	skillPrefetch.Start(messages)
	return canonicalRoundPreparationResult{
		canonicalLoopDecision: canonicalLoopDecision{
			action: canonicalLoopModel,
		},
		messagesForQuery: messages,
		fullSystemPrompt: promptctx.AppendSystemContext(
			input.params.SystemPrompt,
			input.params.SystemContext,
		),
		queryTracking:     queryTracking,
		toolUseContext:    toolUseContext,
		cancellationChain: NewCancellationChain(ctx),
		skillPrefetch:     skillPrefetch,
	}
}

type canonicalAfterModelInput struct {
	ctx               context.Context
	params            QueryParams
	deps              *QueryDeps
	state             *QueryState
	hookExecutor      *hooks.Executor
	recoveryManager   *RecoveryManager
	tokenBudget       *budget.TokenBudget
	cancellationChain *CancellationChain
	modelRound        canonicalModelRoundResult
	yield             func(QueryEvent)
}

type canonicalAfterModelResult struct {
	canonicalLoopDecision
	modelRound canonicalModelRoundResult
}

// runCanonicalAfterModelRound owns post-sampling, withheld-error recovery,
// stop-hook, and token-budget decisions. It returns a typed continue/tool/
// terminal action without putting live hooks, models, or cancellation owners
// into Graph local state.
func runCanonicalAfterModelRound(input canonicalAfterModelInput) canonicalAfterModelResult {
	modelRound := input.modelRound
	result := canonicalAfterModelResult{modelRound: modelRound}
	if modelRound.terminal != nil {
		result.action = canonicalLoopTerminal
		result.terminal = modelRound.terminal
		return result
	}
	state := input.state
	if state == nil {
		terminal := Terminal{
			Reason: TerminalModelError,
			Err:    fmt.Errorf("canonical after-model round requires query state"),
		}
		result.action = canonicalLoopTerminal
		result.terminal = &terminal
		return result
	}
	messagesForQuery := modelRound.messagesForQuery
	assistantMessages := modelRound.assistantMessages
	toolResults := modelRound.toolResults
	shouldPreventContinuation := modelRound.shouldPreventContinuation
	toolUseContext := modelRound.toolUseContext

	if len(assistantMessages) > 0 {
		allMessages := append(
			append([]*schema.Message{}, messagesForQuery...),
			assistantMessages...,
		)
		input.hookExecutor.ExecutePostSampling(allMessages)
	}

	if toolUseContext != nil &&
		toolUseContext.AbortController != nil &&
		toolUseContext.AbortController.Aborted() {
		if input.cancellationChain != nil {
			input.cancellationChain.Cancel("abort_streaming")
		}
		abortReason := toolUseContext.AbortController.Reason
		if abortReason != "interrupt" {
			input.yield(QueryEvent{
				Type:                EventUserInterruption,
				InterruptionToolUse: false,
			})
		}
		modelRound.toolResults = toolResults
		modelRound.shouldPreventContinuation = shouldPreventContinuation
		result.modelRound = modelRound
		terminal := Terminal{Reason: TerminalAbortedStreaming}
		result.action = canonicalLoopTerminal
		result.terminal = &terminal
		return result
	}

	if state.PendingToolUseSummary != nil {
		state.PendingToolUseSummary.Wait()
		if state.PendingToolUseSummary.Summary != "" {
			input.yield(QueryEvent{
				Type: EventToolUseSummary,
				ToolUseSummary: &ToolUseSummaryEvent{
					Summary: state.PendingToolUseSummary.Summary,
					PrecedingToolUseIDs: state.PendingToolUseSummary.
						ToolUseIDs,
					UUID:      input.deps.UUID(),
					Timestamp: time.Now().UTC().Format(time.RFC3339),
				},
			})
		}
		state.PendingToolUseSummary = nil
	}

	if modelRound.needsFollowUp {
		modelRound.toolResults = toolResults
		modelRound.shouldPreventContinuation = shouldPreventContinuation
		result.modelRound = modelRound
		result.action = canonicalLoopTool
		return result
	}

	withheldError := modelRound.withheldError
	withheldReason := modelRound.withheldReason
	hasAttemptedReactiveCompact := state.HasAttemptedReactiveCompact
	maxOutputTokensRecoveryCount := state.MaxOutputTokensRecoveryCount
	maxOutputTokensOverride := state.MaxOutputTokensOverride
	querySource := input.params.QuerySource
	if withheldError != nil {
		if modelRound.mediaRecoveryAttempt != mediaRecoveryAttemptNone &&
			withheldReason != "media_size" {
			mediaOutcome := handleActiveMediaRecoveryFailure(
				input,
				modelRound.mediaRecoveryAttempt,
			)
			result.action = canonicalLoopTerminal
			result.terminal = mediaOutcome.terminal
			return result
		}
		switch withheldReason {
		case "413":
			recoveryPlan := input.recoveryManager.TryRecover(
				RecoveryCategoryPTL,
			)
			if recoveryPlan.Action == RecoveryActionSurface {
				input.yield(QueryEvent{
					Type:             EventAssistant,
					AssistantMessage: withheldError,
				})
				input.hookExecutor.ExecuteStopFailure(withheldError)
				terminal := Terminal{Reason: TerminalPromptTooLong}
				result.action = canonicalLoopTerminal
				result.terminal = &terminal
				return result
			}
			input.recoveryManager.RecordAttempt(RecoveryCategoryPTL)
			ptlResult := recovery.TryPTLRecovery(
				withheldError,
				messagesForQuery,
				string(querySource),
				hasAttemptedReactiveCompact,
				recovery.RecoveryReason(state.Transition),
				func(
					messages []*schema.Message,
					source string,
				) *recovery.DrainResultT {
					drained := compact.RecoverFromOverflow(
						messages,
						source,
					)
					return &recovery.DrainResultT{
						Committed: drained.Committed,
						Messages:  drained.Messages,
					}
				},
				func(
					messages []*schema.Message,
					source string,
				) []*schema.Message {
					reactive := compact.TryReactiveCompact(
						messages,
						source,
						"prompt_too_long",
					)
					if reactive == nil {
						return nil
					}
					return reactive.Messages
				},
			)
			if ptlResult.Continue {
				input.yield(QueryEvent{
					Type: EventCompactBoundary,
					CompactBoundaryMessage: newRecoveryBoundaryMessage(
						withheldError,
						ptlResult.Reason,
					),
				})
				state.Messages = ptlResult.Messages
				if ptlResult.Reason == "collapse_drain_retry" {
					state.Transition = ContinueCollapseDrainRetry
				} else {
					state.HasAttemptedReactiveCompact = true
					state.Transition = ContinueReactiveCompactRetry
				}
				result.action = canonicalLoopContinue
				return result
			}
			if ptlResult.Terminal {
				input.yield(QueryEvent{
					Type:             EventAssistant,
					AssistantMessage: withheldError,
				})
				input.hookExecutor.ExecuteStopFailure(withheldError)
				terminal := Terminal{Reason: TerminalPromptTooLong}
				result.action = canonicalLoopTerminal
				result.terminal = &terminal
				return result
			}
		case "media_size":
			mediaOutcome := handleMediaSizeFailure(
				input,
				modelRound,
				messagesForQuery,
			)
			if mediaOutcome.continueRound {
				result.action = canonicalLoopContinue
				return result
			}
			if mediaOutcome.terminal != nil {
				result.action = canonicalLoopTerminal
				result.terminal = mediaOutcome.terminal
				return result
			}
		case "max_output_tokens":
			input.recoveryManager.RecordAttempt(RecoveryCategoryMaxTokens)
			maxResult := recovery.TryMaxTokensRecovery(
				maxOutputTokensRecoveryCount,
				maxOutputTokensOverride,
				true,
			)
			if maxResult.Continue {
				if maxResult.MaxOutputTokensOverride != nil {
					state.MaxOutputTokensOverride = maxResult.MaxOutputTokensOverride
				}
				if maxResult.Reason == "max_output_tokens_recovery" {
					state.MaxOutputTokensOverride = nil
					state.MaxOutputTokensRecoveryCount = maxOutputTokensRecoveryCount + 1
					nextMessages := make(
						[]*schema.Message,
						0,
						len(messagesForQuery)+
							len(assistantMessages)+
							len(toolResults)+1,
					)
					nextMessages = append(nextMessages, messagesForQuery...)
					nextMessages = append(
						nextMessages,
						assistantMessages...,
					)
					nextMessages = append(nextMessages, toolResults...)
					if maxResult.RecoveryMessage != nil {
						input.yield(QueryEvent{
							Type: EventAttachment,
							AttachmentMessage: maxResult.
								RecoveryMessage,
						})
						nextMessages = append(
							nextMessages,
							maxResult.RecoveryMessage,
						)
					}
					state.Messages = nextMessages
					state.Transition = ContinueMaxOutputTokensRecovery
				} else {
					state.Messages = messagesForQuery
					state.Transition = ContinueMaxOutputTokensEscalate
				}
				result.action = canonicalLoopContinue
				return result
			}
			if maxResult.Terminal {
				input.yield(QueryEvent{
					Type:             EventAssistant,
					AssistantMessage: withheldError,
				})
				input.hookExecutor.ExecuteStopFailure(withheldError)
				terminal := Terminal{Reason: TerminalCompleted}
				result.action = canonicalLoopTerminal
				result.terminal = &terminal
				return result
			}
		}

		if isAPIErrorMessage(withheldError) {
			input.yield(QueryEvent{
				Type:             EventAssistant,
				AssistantMessage: withheldError,
			})
			input.hookExecutor.ExecuteStopFailure(withheldError)
			terminal := Terminal{Reason: TerminalCompleted}
			result.action = canonicalLoopTerminal
			result.terminal = &terminal
			return result
		}
	}

	if lastAssistant := lastAssistantMessage(assistantMessages); isAPIErrorMessage(
		lastAssistant,
	) {
		input.hookExecutor.ExecuteStopFailure(lastAssistant)
		terminal := Terminal{Reason: TerminalCompleted}
		result.action = canonicalLoopTerminal
		result.terminal = &terminal
		return result
	}

	stopResult := input.hookExecutor.ExecuteStop(
		messagesForQuery,
		assistantMessages,
		state.StopHookActive,
	)
	if stopResult.PreventContinuation {
		terminal := Terminal{Reason: TerminalStopHookPrevented}
		result.action = canonicalLoopTerminal
		result.terminal = &terminal
		return result
	}
	if len(stopResult.BlockingErrors) > 0 {
		for _, message := range stopResult.BlockingErrors {
			if message != nil {
				input.yield(QueryEvent{
					Type:              EventAttachment,
					AttachmentMessage: message,
				})
			}
		}
		newMessages := make([]*schema.Message, 0,
			len(messagesForQuery)+
				len(assistantMessages)+
				len(stopResult.BlockingErrors),
		)
		newMessages = append(newMessages, messagesForQuery...)
		newMessages = append(newMessages, assistantMessages...)
		newMessages = append(newMessages, stopResult.BlockingErrors...)
		state.Messages = newMessages
		state.StopHookActive = true
		state.Transition = ContinueStopHookBlocking
		result.action = canonicalLoopContinue
		return result
	}

	budgetDecision := input.tokenBudget.Check()
	if budgetDecision.Action == "continue" &&
		budgetDecision.NudgeMessage != "" {
		nudge := &schema.Message{
			Role:    schema.User,
			Content: budgetDecision.NudgeMessage,
			Extra: map[string]any{
				"is_meta":         true,
				"attachment_kind": "token_budget_continuation",
			},
		}
		input.yield(QueryEvent{
			Type:              EventAttachment,
			AttachmentMessage: nudge,
		})
		newMessages := make([]*schema.Message, 0,
			len(messagesForQuery)+len(assistantMessages)+1,
		)
		newMessages = append(newMessages, messagesForQuery...)
		newMessages = append(newMessages, assistantMessages...)
		newMessages = append(newMessages, nudge)
		state.Messages = newMessages
		state.Transition = ContinueTokenBudgetContinuation
		result.action = canonicalLoopContinue
		return result
	}

	terminal := Terminal{Reason: TerminalCompleted}
	result.action = canonicalLoopTerminal
	result.terminal = &terminal
	return result
}

type canonicalAfterToolInput struct {
	params                    QueryParams
	deps                      *QueryDeps
	state                     *QueryState
	hookExecutor              *hooks.Executor
	attachmentProcessor       *attachments.Processor
	memoryPrefetch            *prefetch.MemoryPrefetch
	skillPrefetch             *prefetch.SkillPrefetch
	turnTracker               *TurnTracker
	recoveryManager           *RecoveryManager
	eventValidator            *EventOrderValidator
	compactTracking           *compact.CompactTracking
	consumedCommandUUIDs      *[]string
	messagesForQuery          []*schema.Message
	assistantMessages         []*schema.Message
	toolUseBlocks             []*schema.ToolCall
	toolResults               []*schema.Message
	toolUseContext            *ToolUseContext
	queryTracking             *QueryTracking
	cancellationChain         *CancellationChain
	shouldPreventContinuation bool
	toolDecision              *afterToolDecision
	yield                     func(QueryEvent)
}

// runCanonicalAfterToolRound owns the ProjectGraph between-round safe point.
// Live queue, prefetch, hook, and context owners stay on the node stack; only
// the resulting QueryState is eligible for the next Graph round.
func runCanonicalAfterToolRound(
	ctx context.Context,
	input canonicalAfterToolInput,
) canonicalLoopDecision {
	state := input.state
	if state == nil {
		terminal := Terminal{
			Reason: TerminalModelError,
			Err:    fmt.Errorf("canonical after-tool round requires query state"),
		}
		return canonicalLoopDecision{
			action:   canonicalLoopTerminal,
			terminal: &terminal,
		}
	}
	toolUseContext := input.toolUseContext
	if input.toolDecision != nil {
		switch input.toolDecision.Kind {
		case afterToolContinue:
		case afterToolReturn:
			reason := input.toolDecision.TerminalReason
			if reason == "" {
				reason = TerminalHookStopped
			}
			terminal := Terminal{Reason: reason}
			return canonicalLoopDecision{
				action:   canonicalLoopTerminal,
				terminal: &terminal,
			}
		case afterToolInterrupt:
			if toolUseContext != nil &&
				toolUseContext.AbortController != nil &&
				toolUseContext.AbortController.Reason != "interrupt" {
				input.yield(QueryEvent{
					Type:                EventUserInterruption,
					InterruptionToolUse: true,
				})
			}
			nextTurnCount := state.TurnCount + 1
			if input.params.MaxTurns != nil &&
				*input.params.MaxTurns > 0 &&
				nextTurnCount > *input.params.MaxTurns {
				input.yield(QueryEvent{
					Type: EventMaxTurnsReached,
					MaxTurnsInfo: &MaxTurnsInfo{
						MaxTurns:  *input.params.MaxTurns,
						TurnCount: nextTurnCount,
					},
				})
			}
			reason := input.toolDecision.TerminalReason
			if reason == "" {
				reason = TerminalAbortedTools
			}
			terminal := Terminal{Reason: reason}
			return canonicalLoopDecision{
				action:   canonicalLoopTerminal,
				terminal: &terminal,
			}
		default:
			terminal := Terminal{
				Reason: TerminalModelError,
				Err: fmt.Errorf(
					"canonical after-tool round received invalid decision %q",
					input.toolDecision.Kind,
				),
			}
			return canonicalLoopDecision{
				action:   canonicalLoopTerminal,
				terminal: &terminal,
			}
		}
	}

	turnCount := state.TurnCount
	maxTurns := input.params.MaxTurns
	stopHookActive := state.StopHookActive
	toolResults := append([]*schema.Message(nil), input.toolResults...)

	if toolUseContext != nil &&
		toolUseContext.AbortController != nil &&
		toolUseContext.AbortController.Aborted() {
		if input.cancellationChain != nil {
			input.cancellationChain.Cancel("abort_tools")
		}
		abortReason := toolUseContext.AbortController.Reason
		if abortReason != "interrupt" {
			input.yield(QueryEvent{
				Type:                EventUserInterruption,
				InterruptionToolUse: true,
			})
		}
		nextTurnCount := turnCount + 1
		if maxTurns != nil && *maxTurns > 0 && nextTurnCount > *maxTurns {
			input.yield(QueryEvent{
				Type: EventMaxTurnsReached,
				MaxTurnsInfo: &MaxTurnsInfo{
					MaxTurns:  *maxTurns,
					TurnCount: nextTurnCount,
				},
			})
		}
		terminal := Terminal{Reason: TerminalAbortedTools}
		return canonicalLoopDecision{
			action:   canonicalLoopTerminal,
			terminal: &terminal,
		}
	}

	if input.shouldPreventContinuation {
		terminal := Terminal{Reason: TerminalHookStopped}
		return canonicalLoopDecision{
			action:   canonicalLoopTerminal,
			terminal: &terminal,
		}
	}

	if input.compactTracking != nil && input.compactTracking.Compacted {
		input.compactTracking.TurnCounter++
	}

	isMainThread := toolUseContext == nil || toolUseContext.AgentID == ""
	sleepRan := checkSleepRan(toolResults)
	if terminal := claimRuntimeStopAtSafePoint(
		input.params,
		toolUseContext,
		true,
		input.yield,
	); terminal != nil {
		return canonicalLoopDecision{
			action:   canonicalLoopTerminal,
			terminal: terminal,
		}
	}
	if isMainThread && input.params.AgentProgressDrainer != nil {
		for _, progress := range drainAgentProgressEvents(
			input.params.AgentProgressDrainer,
		) {
			input.yield(QueryEvent{
				Type:         EventTaskProgress,
				TaskProgress: progress,
			})
		}
	}
	runtimeMessages, runtimeErr := collectRuntimeItemsAtSafePoint(
		ctx,
		input.params,
		toolUseContext,
		true,
		sleepRan,
		input.yield,
		input.consumedCommandUUIDs,
	)
	if runtimeErr != nil {
		terminal := Terminal{Reason: TerminalModelError, Err: runtimeErr}
		return canonicalLoopDecision{
			action:   canonicalLoopTerminal,
			terminal: &terminal,
		}
	}
	toolResults = append(toolResults, runtimeMessages...)

	for _, attachment := range input.attachmentProcessor.GetAttachments(
		input.messagesForQuery,
		toolResults,
	) {
		input.yield(QueryEvent{
			Type:              EventAttachment,
			AttachmentMessage: attachment,
		})
		toolResults = append(toolResults, attachment)
	}
	for _, message := range input.memoryPrefetch.Collect() {
		input.yield(QueryEvent{
			Type:              EventAttachment,
			AttachmentMessage: message,
		})
		toolResults = append(toolResults, message)
	}
	for _, message := range input.skillPrefetch.Collect() {
		input.yield(QueryEvent{
			Type:              EventAttachment,
			AttachmentMessage: message,
		})
		toolResults = append(toolResults, message)
	}

	if input.params.JSONSchema != nil {
		maxRetries := input.params.MaxStructuredOutputRetries
		if maxRetries <= 0 {
			maxRetries = 5
		}
		if countSyntheticOutputCalls(state.Messages) >= maxRetries {
			terminal := Terminal{
				Reason: TerminalMaxStructuredOutputRetries,
			}
			input.yield(QueryEvent{
				Type:         EventTerminal,
				TerminalInfo: &terminal,
			})
			return canonicalLoopDecision{
				action:   canonicalLoopTerminal,
				terminal: &terminal,
			}
		}
	}

	turnAdvance := input.turnTracker.Advance()
	nextTurnCount := turnCount + 1
	if !turnAdvance.Allowed ||
		maxTurns != nil && *maxTurns > 0 && nextTurnCount > *maxTurns {
		maxTurnsInfo := turnAdvance.ToMaxTurnsInfo()
		if maxTurnsInfo == nil {
			maxTurnsInfo = &MaxTurnsInfo{
				MaxTurns:  *maxTurns,
				TurnCount: nextTurnCount,
			}
		}
		input.yield(QueryEvent{
			Type:         EventMaxTurnsReached,
			MaxTurnsInfo: maxTurnsInfo,
		})
		input.eventValidator.MarkTurnEnding()
		input.hookExecutor.ExecuteNotification(
			ctx,
			"warning",
			fmt.Sprintf(
				"Max turns reached (%d/%d)",
				maxTurnsInfo.TurnCount,
				maxTurnsInfo.MaxTurns,
			),
			map[string]any{
				"max_turns":  maxTurnsInfo.MaxTurns,
				"turn_count": maxTurnsInfo.TurnCount,
			},
		)
		terminal := Terminal{
			Reason:    TerminalMaxTurns,
			TurnCount: maxTurnsInfo.TurnCount,
			MaxTurns:  maxTurnsInfo.MaxTurns,
		}
		return canonicalLoopDecision{
			action:   canonicalLoopTerminal,
			terminal: &terminal,
		}
	}

	if toolUseContext != nil &&
		toolUseContext.Options != nil &&
		toolUseContext.Options.RefreshTools != nil {
		if refreshed := toolUseContext.Options.RefreshTools(); refreshed != nil {
			toolUseContext.Options.Tools = refreshed
		}
	}

	var nextPendingToolUseSummary *ToolUseSummaryPromise
	if input.params.EmitToolUseSummaries &&
		input.params.ToolUseSummaryModel != nil &&
		len(input.toolUseBlocks) > 0 &&
		(toolUseContext == nil || toolUseContext.AgentID == "") &&
		(toolUseContext == nil ||
			toolUseContext.AbortController == nil ||
			!toolUseContext.AbortController.Aborted()) {
		nextPendingToolUseSummary = generateToolUseSummaryAsync(
			ctx,
			input.params.ToolUseSummaryModel,
			input.params.toolUseSummaryCall,
			input.toolUseBlocks,
			toolResults,
			input.assistantMessages,
			input.deps,
		)
	}

	input.recoveryManager.ResetAll()
	input.eventValidator.MarkTurnEnding()
	newMessages := make([]*schema.Message, 0,
		len(input.messagesForQuery)+
			len(input.assistantMessages)+
			len(toolResults),
	)
	newMessages = append(newMessages, input.messagesForQuery...)
	newMessages = append(newMessages, input.assistantMessages...)
	newMessages = append(newMessages, toolResults...)
	*state = QueryState{
		Messages:                     newMessages,
		ToolUseContext:               toolUseContext,
		TurnCount:                    nextTurnCount,
		MaxOutputTokensRecoveryCount: 0,
		HasAttemptedReactiveCompact:  false,
		StopHookActive:               stopHookActive,
		PendingToolUseSummary:        nextPendingToolUseSummary,
		Transition:                   ContinueNextTurn,
	}
	return canonicalLoopDecision{action: canonicalLoopContinue}
}

func collectRuntimeItemsAtSafePoint(
	ctx context.Context,
	params QueryParams,
	toolUseContext *ToolUseContext,
	includeUser bool,
	allowLater bool,
	yield func(QueryEvent),
	consumedRuntimeItemIDs *[]string,
) ([]*schema.Message, error) {
	if params.CollectRuntimeItems != nil {
		if err := params.CollectRuntimeItems(ctx); err != nil {
			return nil, fmt.Errorf("collect runtime inputs: %w", err)
		}
	}
	if params.InputCoordinator == nil {
		return nil, nil
	}
	items, err := params.InputCoordinator.ClaimSafePoint(
		runtimeInputScopeForQuery(params, toolUseContext),
		includeUser,
		allowLater,
	)
	if err != nil {
		return nil, fmt.Errorf("claim runtime inputs: %w", err)
	}
	if len(items) == 0 {
		return nil, nil
	}
	if params.AdmitRuntimeItem != nil {
		for _, item := range items {
			if err := params.AdmitRuntimeItem(ctx, item); err != nil {
				var releaseErr error
				for _, claimed := range items {
					if _, currentErr := params.InputCoordinator.Release(
						claimed.ID,
					); currentErr != nil && releaseErr == nil {
						releaseErr = currentErr
					}
				}
				if releaseErr != nil {
					return nil, fmt.Errorf(
						"release runtime inputs after admission failure: %w",
						releaseErr,
					)
				}
				return nil, fmt.Errorf(
					"admit runtime input: %w",
					err,
				)
			}
		}
	}
	if params.repeatedToolGuard != nil {
		params.repeatedToolGuard.reset()
	}
	messages := make([]*schema.Message, 0, len(items))
	for _, item := range items {
		if yield != nil {
			yield(QueryEvent{
				Type: EventCommandLifecycle,
				CommandLifecycle: &CommandLifecycleEvent{
					CommandUUID: item.ID,
					Phase:       CommandLifecycleStarted,
				},
			})
		}
		attachment := runtimeItemToAttachmentMessage(item)
		if yield != nil {
			yield(QueryEvent{
				Type:              EventAttachment,
				AttachmentMessage: attachment,
			})
		}
		messages = append(messages, attachment)
		if consumedRuntimeItemIDs != nil {
			*consumedRuntimeItemIDs = append(
				*consumedRuntimeItemIDs,
				item.ID,
			)
		}
	}
	return messages, nil
}

func claimRuntimeStopAtSafePoint(
	params QueryParams,
	toolUseContext *ToolUseContext,
	duringTools bool,
	yield func(QueryEvent),
) *Terminal {
	if params.InputCoordinator == nil {
		return nil
	}
	item, ok, err := params.InputCoordinator.ClaimStop(
		runtimeInputScopeForQuery(params, toolUseContext),
	)
	if err != nil {
		return &Terminal{Reason: TerminalModelError, Err: err}
	}
	if !ok || item.Stop == nil {
		return nil
	}
	if yield != nil {
		yield(QueryEvent{
			Type:                EventUserInterruption,
			InterruptionToolUse: duringTools,
		})
	}
	_ = params.InputCoordinator.Settle(item.ID)
	reason := TerminalAbortedStreaming
	if duringTools {
		reason = TerminalAbortedTools
	}
	return &Terminal{Reason: reason}
}

func runtimeInputScopeForQuery(
	params QueryParams,
	toolUseContext *ToolUseContext,
) RuntimeInputScope {
	scope := RuntimeInputScope{SessionID: params.SessionID}
	if toolUseContext != nil {
		scope.ThreadID = toolUseContext.ThreadID
		scope.AgentID = toolUseContext.AgentID
	}
	return scope
}
