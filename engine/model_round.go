package engine

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/cloudwego/eino/schema"

	promptctx "github.com/abietic/yhc/engine/context"
	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/internal/providerorigin"
	"github.com/abietic/yhc/engine/recovery"
)

// canonicalModelRoundInput contains the live dependencies for one model
// boundary invocation. It is deliberately passed on the node execution stack
// and must never be persisted in Compose Graph local state.
type canonicalModelRoundInput struct {
	params                    QueryParams
	deps                      *QueryDeps
	messagesForQuery          []*schema.Message
	fullSystemPrompt          *schema.Message
	userContext               map[string]string
	queryTracking             *QueryTracking
	taskBudgetRemaining       *int
	maxOutputTokensOverride   *int
	toolUseContext            *ToolUseContext
	cancellationChain         *CancellationChain
	shouldPreventContinuation bool
	providerMessagesForCall   []*schema.Message
	modelOverride             string
	routeGuard                func(string) error
	disableGenericFallback    bool
	usageLogicalRoundID       string
	mediaRecoveryAttempt      mediaRecoveryAttempt
	yield                     func(QueryEvent)
}

// canonicalModelRoundResult is returned only after ProcessStream has classified
// and committed or rejected the complete provider stream. Graph branches may
// inspect this value; partial stream chunks never cross this boundary.
type canonicalModelRoundResult struct {
	messagesForQuery          []*schema.Message
	assistantMessages         []*schema.Message
	toolUseBlocks             []*schema.ToolCall
	toolResults               []*schema.Message
	needsFollowUp             bool
	toolCallsCommitted        bool
	shouldPreventContinuation bool
	withheldError             *schema.Message
	withheldReason            string
	toolUseContext            *ToolUseContext
	attemptedModel            string
	usageLogicalRoundID       string
	mediaRecoveryAttempt      mediaRecoveryAttempt
	terminal                  *Terminal
}

// runCanonicalModelRound owns the model-facing part of one query iteration:
// request preparation, model-visible tool projection, exact provider options,
// retry/fallback, streamed-message normalization, and the terminal commit
// classification. ProjectGraph uses this as its single model-round boundary.
func runCanonicalModelRound(
	ctx context.Context,
	input canonicalModelRoundInput,
) canonicalModelRoundResult {
	result := canonicalModelRoundResult{
		messagesForQuery:          input.messagesForQuery,
		shouldPreventContinuation: input.shouldPreventContinuation,
		toolUseContext:            input.toolUseContext,
		mediaRecoveryAttempt:      input.mediaRecoveryAttempt,
	}
	if input.params.ChatModel == nil {
		result.assistantMessages = make([]*schema.Message, 0)
		result.toolUseBlocks = make([]*schema.ToolCall, 0)
		result.toolResults = make([]*schema.Message, 0)
		return result
	}

	toolInfos := make([]*schema.ToolInfo, 0)
	var thinkingConfig *ThinkingConfig
	toolChoice := ""
	forcedToolName := ""
	currentModel := ""
	isNonInteractive := false
	effortValue := ""
	if result.toolUseContext != nil && result.toolUseContext.Options != nil {
		toolInfos = result.toolUseContext.Options.Tools
		thinkingConfig = result.toolUseContext.Options.ThinkingConfig
		toolChoice = result.toolUseContext.Options.ToolChoice
		forcedToolName = result.toolUseContext.Options.ForcedToolName
		currentModel = getRuntimeMainLoopModel(result.toolUseContext.Options, result.messagesForQuery)
		isNonInteractive = result.toolUseContext.Options.IsNonInteractiveSession
		effortValue = result.toolUseContext.Options.EffortValue
	}
	if override := strings.TrimSpace(input.modelOverride); override != "" {
		currentModel = override
	}
	modelRole := ""
	modelProfile := ""
	modelProvider := ""
	modelEffort := effortValue
	if identity := input.params.modelCall; identity != nil {
		modelRole = identity.Role
		if modelCallIdentityMatches(identity, currentModel) {
			modelProfile = identity.Profile
			modelProvider = identity.Provider
			modelEffort = identity.Reasoning
		} else {
			modelEffort = ""
		}
	}

	modelCtx := ctx
	if result.toolUseContext != nil &&
		result.toolUseContext.AbortController != nil &&
		result.toolUseContext.AbortController.Ctx != nil {
		modelCtx = result.toolUseContext.AbortController.Ctx
	} else if input.cancellationChain != nil {
		modelCtx = input.cancellationChain.ModelContext()
	}

	callOpts := execution.CallModelOptions{
		SystemPrompt:     input.fullSystemPrompt,
		UserContext:      input.userContext,
		ThinkingConfig:   toExecutionThinkingConfig(thinkingConfig),
		Tools:            toolInfos,
		Signal:           modelCtx,
		Model:            currentModel,
		Provider:         modelProvider,
		ModelRole:        modelRole,
		ModelProfile:     modelProfile,
		ToolChoice:       toolChoice,
		ForcedToolName:   forcedToolName,
		IsNonInteractive: isNonInteractive,
		FallbackModel:    input.params.FallbackModel,
		QuerySource:      string(input.params.QuerySource),
		QueryTracking:    toExecutionQueryTracking(input.queryTracking),
		AgentID:          getAgentID(result.toolUseContext),
		SessionID:        getSessionID(input.params, result.toolUseContext),
		SkipCacheWrite:   input.params.SkipCacheWrite,
		MaxOutputTokens:  input.maxOutputTokensOverride,
		EffortValue:      modelEffort,
		ProviderUsage:    input.deps.ProviderUsage,
	}
	if input.usageLogicalRoundID != "" {
		callOpts.UsageLogicalRoundID = input.usageLogicalRoundID
	} else if input.deps.ProviderUsage != nil {
		callOpts.UsageLogicalRoundID = input.deps.ProviderUsage.NewLogicalRoundID()
	}
	result.usageLogicalRoundID = callOpts.UsageLogicalRoundID
	initialModel := currentModel
	if input.params.TaskBudget != nil {
		callOpts.TaskBudget = &execution.TaskBudget{
			Total: input.params.TaskBudget.Total,
		}
		if input.taskBudgetRemaining != nil {
			callOpts.TaskBudget.Remaining = input.taskBudgetRemaining
		}
	}

	if input.providerMessagesForCall == nil &&
		result.toolUseContext != nil &&
		result.toolUseContext.ContentReplacementState != nil {
		var skipTools map[string]bool
		if result.toolUseContext.Options != nil {
			skipTools = buildSkipToolNames(result.toolUseContext.Options.Tools, input.params.ToolRegistry)
		}
		budgetResult := ApplyToolResultBudget(
			result.messagesForQuery,
			result.toolUseContext.ContentReplacementState,
			skipTools,
		)
		result.messagesForQuery = budgetResult.Messages
		if len(budgetResult.NewReplacements) > 0 && input.deps.Transcript != nil {
			_ = input.deps.Transcript.RecordContentReplacements(budgetResult.NewReplacements)
		}
	}

	messagesForCall := result.messagesForQuery
	if input.providerMessagesForCall != nil {
		messagesForCall = input.providerMessagesForCall
	}
	preparedMessagesForCall := promptctx.PrependUserContext(
		messagesForCall,
		input.userContext,
	)
	preparedMessagesForCall = normalizeMessagesForAPI(preparedMessagesForCall)
	immutablePreparedMessages, cloneErr := cloneModelRequestMessages(
		preparedMessagesForCall,
	)
	if cloneErr != nil {
		terminal := Terminal{
			Reason: TerminalPersistenceError,
			Err:    fmt.Errorf("snapshot model request: %w", cloneErr),
		}
		result.terminal = &terminal
		return result
	}
	immutableSystemPrompt, cloneErr := cloneModelRequestSystemPrompt(
		input.fullSystemPrompt,
	)
	if cloneErr != nil {
		terminal := Terminal{
			Reason: TerminalPersistenceError,
			Err:    fmt.Errorf("snapshot model system prompt: %w", cloneErr),
		}
		result.terminal = &terminal
		return result
	}
	immutableToolInfos, cloneErr := cloneModelRequestToolInfos(toolInfos)
	if cloneErr != nil {
		terminal := Terminal{
			Reason: TerminalPersistenceError,
			Err:    fmt.Errorf("snapshot model tool schemas: %w", cloneErr),
		}
		result.terminal = &terminal
		return result
	}
	var originResolver providerorigin.BindingResolver
	if input.deps != nil && input.deps.Transcript != nil {
		originResolver = input.deps.Transcript.AssistantOriginResolver()
	}

	routeGuard := input.routeGuard
	if routeGuard == nil {
		routeGuard = input.params.promptRouteGuard
	}
	dispatchGuard := input.params.modelDispatchGuard
	callOpts.FallbackModel = ""
	coordinator, coordinatorErr := newModelAttemptCoordinator(
		input.params,
		modelFailoverRequest{
			messages:     immutablePreparedMessages,
			systemPrompt: immutableSystemPrompt,
			toolInfos:    immutableToolInfos,
		},
		callOpts.UsageLogicalRoundID,
		input.deps.UUID,
	)
	if coordinatorErr != nil {
		terminal := Terminal{
			Reason: TerminalPromptInputError,
			Err:    coordinatorErr,
		}
		result.terminal = &terminal
		return result
	}
	if input.disableGenericFallback ||
		input.mediaRecoveryAttempt != mediaRecoveryAttemptNone {
		coordinator.enabled = false
	}
	var activeAttempt *activeModelAttempt
	var modelPreparer runtimeModelPreparer
	if preparer, ok := input.params.modelResolver.(runtimeModelPreparer); ok {
		modelPreparer = preparer
	}
	if coordinator.enabled {
		var ok bool
		activeAttempt, ok = coordinator.next(input.yield)
		if !ok {
			terminal := Terminal{
				Reason: TerminalModelError,
				Err:    fmt.Errorf("model failover policy has no admitted primary"),
			}
			result.terminal = &terminal
			return result
		}
	}
	for {
		result.assistantMessages = nil
		result.toolUseBlocks = nil
		result.toolResults = nil
		result.needsFollowUp = false
		result.shouldPreventContinuation = input.shouldPreventContinuation
		result.withheldError = nil
		result.withheldReason = ""
		if activeAttempt != nil {
			currentModel = activeAttempt.candidate.call.Selector
			modelRole = string(activeAttempt.candidate.call.Role)
			modelProfile = activeAttempt.candidate.call.ProfileID
			modelProvider = activeAttempt.candidate.call.Provider
			modelEffort = activeAttempt.candidate.call.ReasoningEffort
			preparedMessagesForCall, cloneErr = cloneModelRequestMessages(
				immutablePreparedMessages,
			)
			if cloneErr != nil {
				terminal := Terminal{
					Reason: TerminalPersistenceError,
					Err: fmt.Errorf(
						"restore immutable model request: %w",
						cloneErr,
					),
				}
				result.terminal = &terminal
				return result
			}
			if activeAttempt.index > 0 {
				preparedMessagesForCall = stripSignatureBlocks(
					preparedMessagesForCall,
				)
			}
		}

		if input.mediaRecoveryAttempt != mediaRecoveryAttemptNone {
			if err := modelCtx.Err(); err != nil {
				terminal := Terminal{
					Reason: TerminalAbortedStreaming,
					Err:    err,
				}
				result.terminal = &terminal
				return result
			}
		}
		if dispatchGuard != nil {
			if err := dispatchGuard(currentModel); err != nil {
				terminal := Terminal{
					Reason: TerminalPromptInputError,
					Err:    err,
				}
				result.terminal = &terminal
				return result
			}
		}
		if routeGuard != nil {
			if err := routeGuard(currentModel); err != nil {
				if input.mediaRecoveryAttempt != mediaRecoveryAttemptNone {
					result.withheldError = recovery.TerminalMessage(
						mediaRecoveryStage(input.mediaRecoveryAttempt),
					)
					result.withheldReason = "media_recovery_failure"
					result.attemptedModel = currentModel
					return result
				}
				terminal := Terminal{
					Reason: TerminalPromptInputError,
					Err:    err,
				}
				result.terminal = &terminal
				return result
			}
		}
		if activeAttempt != nil && !activeAttempt.routePrepared {
			if modelPreparer != nil {
				if _, err := modelPreparer.PrepareModel(
					modelCtx,
					currentModel,
				); err != nil {
					coordinator.emit(
						input.yield,
						activeAttempt,
						ModelAttemptFailed,
						execution.ModelFailureUnknown,
						"route_construction",
						ModelAttemptOutputNeverStarted,
					)
					terminal := Terminal{
						Reason: TerminalModelError,
						Err: fmt.Errorf(
							"construct admitted model route: %w",
							err,
						),
					}
					result.terminal = &terminal
					return result
				}
			}
		}
		callOpts.Model = currentModel
		callOpts.ModelRole = modelRole
		callOpts.ModelProfile = modelProfile
		callOpts.Provider = modelProvider
		callOpts.EffortValue = modelEffort
		if activeAttempt == nil &&
			(currentModel != initialModel ||
				input.mediaRecoveryAttempt == mediaRecoveryAttemptFallback) {
			// The active-model capability gate does not authorize a fallback
			// provider. A fallback is therefore sent without provider-specific
			// reasoning effort unless a future resolver proves that route too.
			callOpts.EffortValue = ""
			callOpts.Provider = ""
			callOpts.ModelProfile = ""
		}
		retryConfig := execution.RetryConfig{
			MaxRetries: 0,
			BaseDelay:  input.params.retryBaseDelay,
		}
		var attemptDispatchErr error
		if dispatchGuard != nil {
			retryConfig.BeforeDispatch = func(
				_ context.Context,
				_ int,
			) error {
				attemptDispatchErr = dispatchGuard(currentModel)
				return attemptDispatchErr
			}
		}
		if activeAttempt != nil {
			retryConfig.MaxConsecutiveOverloadErrors = execution.Max529Retries
			retryConfig.Budget = coordinator.budget
			callOpts.ProviderCallBudget = coordinator.budget
		}
		if input.mediaRecoveryAttempt != mediaRecoveryAttemptNone {
			if err := modelCtx.Err(); err != nil {
				terminal := Terminal{
					Reason: TerminalAbortedStreaming,
					Err:    err,
				}
				result.terminal = &terminal
				return result
			}
		}
		callResult, callErr := execution.CallModelWithRetry(
			modelCtx,
			retryConfig,
			func(retryCtx context.Context, retryIndex int) (*execution.CallModelResult, error) {
				dispatchMessages, restoreErr := cloneModelRequestMessages(
					preparedMessagesForCall,
				)
				if restoreErr != nil {
					return nil, fmt.Errorf(
						"restore immutable model messages: %w",
						restoreErr,
					)
				}
				dispatchSystemPrompt, restoreErr := cloneModelRequestSystemPrompt(
					immutableSystemPrompt,
				)
				if restoreErr != nil {
					return nil, fmt.Errorf(
						"restore immutable model system prompt: %w",
						restoreErr,
					)
				}
				dispatchToolInfos, restoreErr := cloneModelRequestToolInfos(
					immutableToolInfos,
				)
				if restoreErr != nil {
					return nil, fmt.Errorf(
						"restore immutable model tool schemas: %w",
						restoreErr,
					)
				}
				dispatchCallOpts := callOpts
				dispatchCallOpts.SystemPrompt = dispatchSystemPrompt
				dispatchCallOpts.Tools = dispatchToolInfos
				if activeAttempt != nil {
					activeAttempt.retries = retryIndex
					dispatchCallOpts.UsageLogicalRequestID = coordinator.logicalRequestID
					dispatchCallOpts.UsageModelAttemptID = activeAttempt.id
					dispatchCallOpts.UsageModelAttemptIndex = activeAttempt.index
					dispatchCallOpts.UsageModelRetryIndex = retryIndex
				}
				dispatchCtx := providerorigin.WithBindingResolver(
					retryCtx,
					originResolver,
				)
				return input.deps.CallModel(
					dispatchCtx,
					input.params.ChatModel,
					dispatchMessages,
					dispatchSystemPrompt,
					dispatchToolInfos,
					dispatchCallOpts,
				)
			},
			func(info execution.RetryWaitInfo) {
				if input.mediaRecoveryAttempt != mediaRecoveryAttemptNone {
					return
				}
				if activeAttempt != nil {
					activeAttempt.retries = info.Attempt + 1
					coordinator.emit(
						input.yield,
						activeAttempt,
						ModelAttemptRetryWait,
						execution.ClassifyModelFailure(info.Error),
						"",
						ModelAttemptOutputNeverStarted,
					)
				}
				event := QueryEvent{
					Type: EventAttachment,
					AttachmentMessage: &schema.Message{
						Role:    schema.User,
						Content: execution.FormatRetryMessage(info),
						Extra: map[string]any{
							"is_meta":         true,
							"attachment_kind": "system_api_error",
							"level":           "warning",
							"attempt":         info.Attempt,
							"is_429":          info.Is429,
							"is_529":          info.Is529,
						},
					},
				}
				input.yield(coordinator.annotate(event, activeAttempt))
			},
		)
		if attemptDispatchErr != nil {
			if activeAttempt != nil {
				coordinator.emit(
					input.yield,
					activeAttempt,
					ModelAttemptFailed,
					execution.ModelFailureUnknown,
					"dispatch_guard",
					ModelAttemptOutputNeverStarted,
				)
			}
			terminal := Terminal{
				Reason: TerminalPromptInputError,
				Err:    attemptDispatchErr,
			}
			result.terminal = &terminal
			return result
		}
		callFailure := execution.ClassifyModelFailure(callErr)
		nextCandidate, canSwitch := coordinator.nextSwitchCandidate(
			modelCtx,
			activeAttempt,
			callFailure,
			modelPreparer,
			input.yield,
		)
		if canSwitch {
			coordinator.discard(input.yield, activeAttempt, callFailure)
			activeAttempt = coordinator.startCandidate(
				input.yield,
				nextCandidate,
				true,
			)
			continue
		}
		if callErr != nil {
			if activeAttempt != nil &&
				callFailure == execution.ModelFailureOverloaded {
				callErr = coordinator.safeTerminalError(
					activeAttempt,
					callFailure,
				)
			}
			if activeAttempt != nil {
				coordinator.emit(
					input.yield,
					activeAttempt,
					ModelAttemptFailed,
					callFailure,
					"",
					coordinator.terminalDisposition(activeAttempt),
				)
			}
			if input.mediaRecoveryAttempt != mediaRecoveryAttemptNone {
				result.withheldError = recovery.TerminalMessage(
					mediaRecoveryStage(input.mediaRecoveryAttempt),
				)
				result.withheldReason = "media_recovery_failure"
				result.attemptedModel = currentModel
				return result
			}
			execution.YieldMissingToolResults(
				result.assistantMessages,
				callErr.Error(),
				func(event execution.QueryEvent) {
					input.yield(toEngineQueryEvent(event))
				},
			)
			input.yield(QueryEvent{
				Type: EventAssistant,
				AssistantMessage: &schema.Message{
					Role:    schema.Assistant,
					Content: "Model error: " + callErr.Error(),
					Extra:   map[string]any{"api_error": true},
				},
			})
			terminal := Terminal{Reason: TerminalModelError, Err: callErr}
			result.terminal = &terminal
			return result
		}

		streamingExecutor := execution.NewStreamingToolExecutor(
			execution.StreamingToolExecutorConfig{
				Ctx:            modelCtx,
				DeferExecution: true,
			},
		)
		streamResult, streamErr := execution.ProcessStream(
			modelCtx,
			callResult.StreamReader,
			streamingExecutor,
			func(event execution.QueryEvent) {
				input.yield(
					coordinator.annotate(
						toEngineQueryEvent(event),
						activeAttempt,
					),
				)
			},
		)
		streamingExecutor.Discard()
		if streamErr != nil {
			streamErr = execution.MarkProviderUsageAmbiguous(
				callResult.ProviderUsageCall,
				streamErr,
			)
		}
		streamFailure := execution.ClassifyModelFailure(streamErr)
		nextCandidate, canSwitch = coordinator.nextSwitchCandidate(
			modelCtx,
			activeAttempt,
			streamFailure,
			modelPreparer,
			input.yield,
		)
		if canSwitch {
			coordinator.discard(
				input.yield,
				activeAttempt,
				streamFailure,
			)
			activeAttempt = coordinator.startCandidate(
				input.yield,
				nextCandidate,
				true,
			)
			continue
		}
		if streamErr != nil {
			if activeAttempt != nil &&
				streamFailure == execution.ModelFailureOverloaded {
				streamErr = coordinator.safeTerminalError(
					activeAttempt,
					streamFailure,
				)
			}
			if activeAttempt != nil {
				coordinator.emit(
					input.yield,
					activeAttempt,
					ModelAttemptFailed,
					streamFailure,
					"",
					coordinator.terminalDisposition(activeAttempt),
				)
			}
			if input.mediaRecoveryAttempt != mediaRecoveryAttemptNone {
				result.withheldError = recovery.TerminalMessage(
					mediaRecoveryStage(input.mediaRecoveryAttempt),
				)
				result.withheldReason = "media_recovery_failure"
				result.attemptedModel = currentModel
				return result
			}
			execution.YieldMissingToolResults(
				result.assistantMessages,
				streamErr.Error(),
				func(event execution.QueryEvent) {
					input.yield(toEngineQueryEvent(event))
				},
			)
			input.yield(QueryEvent{
				Type: EventAssistant,
				AssistantMessage: &schema.Message{
					Role:    schema.Assistant,
					Content: "Stream error: " + streamErr.Error(),
					Extra:   map[string]any{"api_error": true},
				},
			})
			terminal := Terminal{Reason: TerminalModelError, Err: streamErr}
			result.terminal = &terminal
			return result
		}
		if usageErr := execution.CompleteProviderUsage(
			callResult.ProviderUsageCall,
			streamResult.AssistantMessages,
		); usageErr != nil {
			if activeAttempt != nil {
				coordinator.emit(
					input.yield,
					activeAttempt,
					ModelAttemptFailed,
					execution.ModelFailureUsageAmbiguous,
					"",
					coordinator.terminalDisposition(activeAttempt),
				)
			}
			if input.mediaRecoveryAttempt != mediaRecoveryAttemptNone {
				result.withheldError = recovery.TerminalMessage(
					mediaRecoveryStage(input.mediaRecoveryAttempt),
				)
				result.withheldReason = "media_recovery_failure"
				result.attemptedModel = currentModel
				return result
			}
			input.yield(QueryEvent{
				Type: EventAttachment,
				AttachmentMessage: &schema.Message{
					Role:    schema.User,
					Content: "Provider usage error: " + usageErr.Error(),
					Extra: map[string]any{
						"is_meta":         true,
						"attachment_kind": "provider_usage_error",
						"level":           "error",
						"api_error":       true,
					},
				},
			})
			terminal := Terminal{Reason: TerminalModelError, Err: usageErr}
			result.terminal = &terminal
			return result
		}
		if callResult.ProviderOrigin != nil &&
			len(streamResult.AssistantMessages) > 0 &&
			input.params.commitProviderOrigin != nil {
			if err := input.params.commitProviderOrigin(
				*callResult.ProviderOrigin,
			); err != nil {
				terminal := Terminal{
					Reason: TerminalPersistenceError,
					Err: fmt.Errorf(
						"persist provider reasoning origin: %w",
						err,
					),
				}
				result.terminal = &terminal
				return result
			}
		}

		result.assistantMessages = streamResult.AssistantMessages
		result.toolUseBlocks = streamResult.ToolUseBlocks
		result.toolResults = streamResult.ToolResults
		result.needsFollowUp = streamResult.NeedsFollowUp
		result.toolCallsCommitted = streamResult.ToolCallsCommitted
		result.shouldPreventContinuation = result.shouldPreventContinuation || streamResult.PreventContinuation
		if streamResult.Withheld != nil {
			result.withheldError = streamResult.Withheld
			result.withheldReason = streamResult.WithheldReason
		}
		result.attemptedModel = currentModel
		if activeAttempt != nil {
			coordinator.commit(input.yield, activeAttempt)
		}
		return result
	}
}

func cloneModelRequestSystemPrompt(
	systemPrompt *schema.Message,
) (*schema.Message, error) {
	if systemPrompt == nil {
		return nil, nil
	}
	cloned, err := cloneModelRequestMessages([]*schema.Message{systemPrompt})
	if err != nil {
		return nil, err
	}
	return cloned[0], nil
}

func cloneModelRequestToolInfos(
	toolInfos []*schema.ToolInfo,
) ([]*schema.ToolInfo, error) {
	if toolInfos == nil {
		return nil, nil
	}
	var cloned []*schema.ToolInfo
	if err := cloneProjectGraphJSON(toolInfos, &cloned); err != nil {
		return nil, fmt.Errorf("clone tool schemas: %w", err)
	}
	preserveModelRequestMetadata(
		reflect.ValueOf(toolInfos),
		reflect.ValueOf(cloned),
	)
	return cloned, nil
}

func cloneModelRequestMessages(
	messages []*schema.Message,
) ([]*schema.Message, error) {
	cloned, err := cloneProjectGraphMessages(messages)
	if err != nil {
		return nil, err
	}
	preserveModelRequestMetadata(
		reflect.ValueOf(messages),
		reflect.ValueOf(cloned),
	)
	return cloned, nil
}

var modelRequestMetadataType = reflect.TypeOf(map[string]any(nil))

func preserveModelRequestMetadata(source, target reflect.Value) {
	if !source.IsValid() || !target.IsValid() {
		return
	}
	if source.Type() == modelRequestMetadataType {
		if target.CanSet() {
			target.Set(cloneModelRequestMetadataValue(source))
		}
		return
	}
	if source.Type() != target.Type() {
		return
	}
	switch source.Kind() {
	case reflect.Interface:
		if source.IsNil() || target.IsNil() {
			return
		}
		preserveModelRequestMetadata(source.Elem(), target.Elem())
	case reflect.Pointer:
		if source.IsNil() || target.IsNil() {
			return
		}
		preserveModelRequestMetadata(source.Elem(), target.Elem())
	case reflect.Struct:
		for index := 0; index < source.NumField(); index++ {
			if target.Field(index).CanSet() {
				preserveModelRequestMetadata(
					source.Field(index),
					target.Field(index),
				)
			}
		}
	case reflect.Slice, reflect.Array:
		for index := 0; index < source.Len() && index < target.Len(); index++ {
			preserveModelRequestMetadata(
				source.Index(index),
				target.Index(index),
			)
		}
	}
}

func cloneModelRequestMetadataValue(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := cloneModelRequestMetadataValue(value.Elem())
		result := reflect.New(value.Type()).Elem()
		result.Set(cloned)
		return result
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.New(value.Type().Elem())
		result.Elem().Set(cloneModelRequestMetadataValue(value.Elem()))
		return result
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			result.SetMapIndex(
				cloneModelRequestMetadataValue(iterator.Key()),
				cloneModelRequestMetadataValue(iterator.Value()),
			)
		}
		return result
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			result.Index(index).Set(
				cloneModelRequestMetadataValue(value.Index(index)),
			)
		}
		return result
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			result.Index(index).Set(
				cloneModelRequestMetadataValue(value.Index(index)),
			)
		}
		return result
	default:
		return value
	}
}

func mediaRecoveryStage(attempt mediaRecoveryAttempt) string {
	if attempt == mediaRecoveryAttemptFallback {
		return recovery.MediaStageFallback
	}
	return recovery.MediaStageSelected
}
