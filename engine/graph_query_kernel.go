package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type projectGraphQueryRuntimeContextKey struct{}

type projectGraphQueryKernel struct {
	runnable compose.Runnable[projectGraphKernelInput, projectGraphKernelResult]
}

func (*projectGraphQueryKernel) kind() queryKernelKind {
	return queryKernelProjectGraph
}

// newProjectGraphQueryKernel compiles the project-owned Compose Graph over the
// project-owned canonical lifecycle boundaries. The returned Runnable is
// stateless across invocations; every live owner remains in invocation context.
func newProjectGraphQueryKernel(
	ctx context.Context,
) (*projectGraphQueryKernel, error) {
	nodes := projectGraphKernelNodes{
		prepare:   prepareProjectGraphQueryRound,
		model:     runProjectGraphQueryModelRound,
		tool:      runProjectGraphQueryToolRound,
		reconcile: reconcileProjectGraphQueryRound,
		finalize:  finalizeProjectGraphQuery,
	}
	runnable, err := buildProjectGraphKernel(ctx, projectGraphKernelConfig{
		nodes: nodes,
	})
	if err != nil {
		return nil, fmt.Errorf("build project graph query kernel: %w", err)
	}
	return &projectGraphQueryKernel{runnable: runnable}, nil
}

func (kernel *projectGraphQueryKernel) run(
	ctx context.Context,
	request queryKernelRequest,
) Terminal {
	if kernel == nil || kernel.runnable == nil {
		return Terminal{
			Reason: TerminalModelError,
			Err:    fmt.Errorf("engine: project graph query kernel is not compiled"),
		}
	}
	runtime := newCanonicalQueryRuntime(
		request.params,
		request.deps,
		request.consumedCommandUUIDs,
		request.yield,
	)
	runtime.beforeModelRound = request.beforeModelRound
	invokeCtx := context.WithValue(
		ctx,
		projectGraphQueryRuntimeContextKey{},
		runtime,
	)
	checkpointStore := request.params.ProjectGraphCheckpoint
	if checkpointStore == nil {
		checkpointStore, _ = newProjectGraphCheckpointStore(
			"",
			runtimeInputScopeForQuery(
				request.params,
				request.params.ToolUseContext,
			),
			time.Now,
		)
	}
	invokeCtx = withProjectGraphCheckpointStore(invokeCtx, checkpointStore)
	input := projectGraphKernelInput{RunID: request.params.SessionID}
	if request.params.InputCoordinator != nil {
		input.InputRevision = request.params.InputCoordinator.Revision()
	}
	invokeOptions := []compose.Option{
		compose.WithCheckPointID(checkpointStore.checkpointID),
	}
	if decision := request.params.RuntimePermissionDecision; decision != nil {
		active, ok := checkpointStore.ActiveInterrupt()
		if !ok {
			return Terminal{
				Reason: TerminalPersistenceError,
				Err: fmt.Errorf(
					"engine: project graph resume has no active interrupt",
				),
			}
		}
		if err := validateProjectGraphResumeDecision(active, *decision); err != nil {
			return Terminal{
				Reason: TerminalPersistenceError,
				Err:    err,
			}
		}
		invokeCtx = compose.ResumeWithData(
			invokeCtx,
			active.InterruptID,
			*decision,
		)
		input = projectGraphKernelInput{}
	} else {
		if _, ok := checkpointStore.ActiveInterrupt(); ok ||
			checkpointStore.HasOpaqueCheckpoint() {
			return Terminal{
				Reason: TerminalPersistenceError,
				Err: fmt.Errorf(
					"engine: project graph interrupt requires a targeted runtime decision",
				),
			}
		}
		invokeOptions = append(invokeOptions, compose.WithForceNewRun())
	}
	_, err := kernel.runnable.Invoke(invokeCtx, input, invokeOptions...)
	if err != nil {
		if interrupt, ok := compose.ExtractInterruptInfo(err); ok {
			request, interruptErr := projectGraphRootInterrupt(interrupt)
			if interruptErr != nil {
				return Terminal{
					Reason: TerminalPersistenceError,
					Err:    interruptErr,
				}
			}
			if markErr := checkpointStore.MarkInterrupt(
				context.WithoutCancel(ctx),
				request,
			); markErr != nil {
				return Terminal{
					Reason: TerminalPersistenceError,
					Err: fmt.Errorf(
						"persist project graph interrupt: %w",
						markErr,
					),
				}
			}
			runtime.yield(QueryEvent{
				Type: EventPermissionRequest,
				PermissionRequest: &PermissionRequestEvent{
					ToolName:          request.ToolName,
					CanonicalToolName: request.CanonicalToolName,
					ToolUseID:         request.RequestID,
					Input:             cloneInputMap(request.Input),
					Message:           request.Message,
					Source:            "project_graph",
					Kind:              request.Kind,
					Attempt:           request.Attempt,
					PlanApproval:      request.PlanApproval,
					Presentation:      clonePermissionPresentation(request.Presentation),
				},
			})
			runtime.terminal = &Terminal{
				Reason: TerminalWaitingInput,
			}
			return *runtime.terminal
		}
		if runtime.terminal != nil {
			switch runtime.terminal.Reason {
			case TerminalAbortedStreaming, TerminalAbortedTools:
				return *runtime.terminal
			default:
				if runtime.terminal.Err != nil {
					return *runtime.terminal
				}
			}
		}
		if ctx.Err() != nil {
			reason := TerminalAbortedStreaming
			if runtime.model.toolCallsCommitted &&
				runtime.model.needsFollowUp {
				reason = TerminalAbortedTools
			}
			runtime.terminal = &Terminal{Reason: reason}
			return *runtime.terminal
		}
		return Terminal{Reason: TerminalModelError, Err: err}
	}
	if runtime.terminal == nil {
		return Terminal{
			Reason: TerminalModelError,
			Err:    fmt.Errorf("engine: project graph completed without terminal"),
		}
	}
	return *runtime.terminal
}

func projectGraphQueryRuntime(
	ctx context.Context,
) (*canonicalQueryRuntime, error) {
	runtime, ok := ctx.Value(projectGraphQueryRuntimeContextKey{}).(*canonicalQueryRuntime)
	if !ok || runtime == nil {
		return nil, fmt.Errorf("project graph query runtime is missing")
	}
	return runtime, nil
}

func prepareProjectGraphQueryRound(
	ctx context.Context,
	_ projectGraphRound,
) (projectGraphPreparedRound, error) {
	runtime, err := projectGraphQueryRuntime(ctx)
	if err != nil {
		return projectGraphPreparedRound{}, err
	}
	runtime.prepared = runCanonicalRoundPreparation(
		ctx,
		canonicalRoundPreparationInput{
			params:                 runtime.params,
			deps:                   runtime.deps,
			state:                  runtime.state,
			hookExecutor:           runtime.hookExecutor,
			compactTracking:        &runtime.compactTracking,
			taskBudgetRemaining:    &runtime.taskBudgetRemaining,
			consumedRuntimeItemIDs: runtime.consumedCommandUUIDs,
			yield:                  runtime.yield,
		},
	)
	switch runtime.prepared.action {
	case canonicalLoopContinue:
		return projectGraphPreparedRound{
			Decision: projectGraphPrepareContinue,
		}, nil
	case canonicalLoopModel:
		return projectGraphPreparedRound{
			Decision: projectGraphPrepareModel,
		}, nil
	case canonicalLoopTerminal:
		runtime.terminal = runtime.prepared.terminal
		return projectGraphPreparedRound{
			Decision:       projectGraphPrepareTerminal,
			Value:          string(runtime.terminal.Reason),
			TerminalReason: runtime.terminal.Reason,
		}, nil
	default:
		return projectGraphPreparedRound{}, fmt.Errorf(
			"canonical round preparation returned invalid action %q",
			runtime.prepared.action,
		)
	}
}

func runProjectGraphQueryModelRound(
	ctx context.Context,
	_ projectGraphRound,
) (projectGraphModelRound, error) {
	runtime, err := projectGraphQueryRuntime(ctx)
	if err != nil {
		return projectGraphModelRound{}, err
	}
	prepared := runtime.prepared
	if runtime.beforeModelRound != nil {
		if err := runtime.beforeModelRound(prepared.toolUseContext); err != nil {
			runtime.model = canonicalModelRoundResult{
				toolUseContext: prepared.toolUseContext,
				terminal: &Terminal{
					Reason: TerminalModelError,
					Err:    err,
				},
			}
			runtime.terminal = runtime.model.terminal
			return projectGraphModelRound{
				Decision: projectGraphModelTerminal,
				Value:    string(TerminalModelError),
			}, nil
		}
	}
	attempt := runtime.state.MediaRecovery.PendingAttempt
	var routeGuard func(string) error
	usageLogicalRoundID := ""
	if attempt == mediaRecoveryAttemptFallback &&
		runtime.params.mediaRecovery != nil {
		routeGuard = runtime.params.mediaRecovery.fallbackRouteGuard
	} else if attempt == mediaRecoveryAttemptSelected {
		routeGuard = runtime.params.promptRouteGuard
	}
	if attempt != mediaRecoveryAttemptNone {
		usageLogicalRoundID = runtime.state.MediaRecovery.UsageLogicalRoundID
	}
	runtime.model = runCanonicalModelRound(ctx, canonicalModelRoundInput{
		params:                    runtime.params,
		deps:                      runtime.deps,
		messagesForQuery:          prepared.messagesForQuery,
		fullSystemPrompt:          prepared.fullSystemPrompt,
		userContext:               runtime.params.UserContext,
		queryTracking:             prepared.queryTracking,
		taskBudgetRemaining:       runtime.taskBudgetRemaining,
		maxOutputTokensOverride:   runtime.state.MaxOutputTokensOverride,
		toolUseContext:            prepared.toolUseContext,
		cancellationChain:         prepared.cancellationChain,
		shouldPreventContinuation: runtime.state.ShouldPreventContinuation,
		providerMessagesForCall:   runtime.state.MediaRecovery.ProviderMessages,
		modelOverride:             runtime.state.MediaRecovery.RouteModel,
		routeGuard:                routeGuard,
		disableGenericFallback:    attempt != mediaRecoveryAttemptNone,
		usageLogicalRoundID:       usageLogicalRoundID,
		mediaRecoveryAttempt:      attempt,
		yield:                     runtime.yield,
	})
	if attempt != mediaRecoveryAttemptNone {
		releasePendingMediaRecovery(runtime.state)
	}
	runtime.cancellationChain = prepared.cancellationChain
	if runtime.model.terminal != nil {
		return projectGraphModelRound{
			Decision: projectGraphModelTerminal,
			Value:    string(runtime.model.terminal.Reason),
		}, nil
	}
	toolCalls, err := cloneProjectGraphToolCallPointers(
		runtime.model.toolUseBlocks,
	)
	if err != nil {
		return projectGraphModelRound{}, fmt.Errorf(
			"freeze project graph query tool calls: %w",
			err,
		)
	}
	if runtime.model.needsFollowUp && runtime.model.toolCallsCommitted {
		return projectGraphModelRound{
			Decision:  projectGraphModelToolCalls,
			Value:     committedModelRoundValue(runtime.model),
			ToolCalls: toolCalls,
		}, nil
	}
	return projectGraphModelRound{
		Decision: projectGraphModelTerminal,
		Value:    committedModelRoundValue(runtime.model),
	}, nil
}

func runProjectGraphQueryToolRound(
	ctx context.Context,
	round projectGraphRound,
) (projectGraphToolRound, error) {
	runtime, err := projectGraphQueryRuntime(ctx)
	if err != nil {
		return projectGraphToolRound{}, err
	}
	decisions, interrupted, err := projectGraphHITLStateForNode(ctx)
	if err != nil {
		return projectGraphToolRound{}, err
	}
	if interrupted != nil {
		return projectGraphToolRound{}, compose.StatefulInterrupt(
			ctx,
			projectGraphHITLInterruptInfo{Request: interrupted.Request},
			interrupted,
		)
	}
	if runtime.params.RuntimePermissionDecision != nil &&
		!runtime.projectGraphResumeRestored {
		restoreProjectGraphRuntimeForToolResume(runtime, round)
		runtime.projectGraphResumeRestored = true
	}
	if !runtime.model.toolCallsCommitted {
		runtime.tool = canonicalToolRoundResult{
			decision: afterToolDecision{
				Kind: afterToolContinue,
			},
			toolUseContext: runtime.model.toolUseContext,
		}
		return projectGraphToolRound{
			Decision: projectGraphAfterToolContinue,
		}, nil
	}
	request := probeProjectGraphHITL(
		ctx,
		runtime,
		round.ToolCalls,
		decisions,
	)
	if request != nil {
		state := &projectGraphHITLInterruptState{
			Version:   projectGraphHITLStateVersion,
			Request:   *request,
			Decisions: cloneRuntimePermissionDecisions(decisions),
		}
		return projectGraphToolRound{}, compose.StatefulInterrupt(
			ctx,
			projectGraphHITLInterruptInfo{Request: *request},
			state,
		)
	}
	executionCtx := ctx
	if runtime.params.ProjectGraphHITLEnabled {
		executionCtx = withProjectGraphHITLExecution(ctx, decisions)
	}
	runtime.tool, err = runCanonicalToolRound(executionCtx, canonicalToolRoundInput{
		params:            runtime.params,
		toolCalls:         projectGraphToolCallPointers(round.ToolCalls),
		toolUseContext:    runtime.model.toolUseContext,
		cancellationChain: runtime.cancellationChain,
		hookExecutor:      runtime.hookExecutor,
		queryTracking:     runtime.prepared.queryTracking,
		yield:             runtime.yield,
	})
	if err != nil {
		return projectGraphToolRound{}, err
	}
	runtime.model.toolUseContext = runtime.tool.toolUseContext
	for _, outcome := range runtime.tool.outcomes {
		if outcome.Outcome != nil && outcome.Outcome.PreventContinuation {
			runtime.model.shouldPreventContinuation = true
		}
	}
	result := projectGraphToolRound{
		TerminalReason: runtime.tool.decision.TerminalReason,
		Messages:       runtime.tool.toolResults,
	}
	switch runtime.tool.decision.Kind {
	case afterToolContinue:
		result.Decision = projectGraphAfterToolContinue
	case afterToolReturn:
		result.Decision = projectGraphAfterToolReturn
		result.Value = canonicalToolReturnValue(
			runtime.tool.outcomes,
			runtime.tool.decision.ReturnCallID,
		)
	case afterToolInterrupt:
		result.Decision = projectGraphAfterToolInterrupt
		result.Value = runtime.tool.decision.InterruptID
	default:
		return projectGraphToolRound{}, fmt.Errorf(
			"canonical tool round returned invalid decision %q",
			runtime.tool.decision.Kind,
		)
	}
	return result, nil
}

func reconcileProjectGraphQueryRound(
	ctx context.Context,
	_ projectGraphRound,
	phase projectGraphReconcilePhase,
	_ projectGraphModelRound,
	_ projectGraphToolRound,
) (projectGraphReconcileRound, error) {
	runtime, err := projectGraphQueryRuntime(ctx)
	if err != nil {
		return projectGraphReconcileRound{}, err
	}
	var decision canonicalLoopDecision
	switch phase {
	case projectGraphReconcileAfterModel:
		afterModel := runCanonicalAfterModelRound(
			canonicalAfterModelInput{
				ctx:               ctx,
				params:            runtime.params,
				deps:              runtime.deps,
				state:             runtime.state,
				hookExecutor:      runtime.hookExecutor,
				recoveryManager:   runtime.recoveryManager,
				tokenBudget:       runtime.tokenBudget,
				cancellationChain: runtime.cancellationChain,
				modelRound:        runtime.model,
				yield:             runtime.yield,
			},
		)
		runtime.model = afterModel.modelRound
		decision = afterModel.canonicalLoopDecision
	case projectGraphReconcileAfterTool:
		ensureProjectGraphAfterToolRuntime(runtime)
		toolResults := make(
			[]*schema.Message,
			0,
			len(runtime.model.toolResults)+len(runtime.tool.toolResults),
		)
		toolResults = append(toolResults, runtime.model.toolResults...)
		toolResults = append(toolResults, runtime.tool.toolResults...)
		decision = runCanonicalAfterToolRound(
			ctx,
			canonicalAfterToolInput{
				params:               runtime.params,
				deps:                 runtime.deps,
				state:                runtime.state,
				hookExecutor:         runtime.hookExecutor,
				attachmentProcessor:  runtime.attachmentProcessor,
				memoryPrefetch:       runtime.memoryPrefetch,
				skillPrefetch:        runtime.prepared.skillPrefetch,
				turnTracker:          runtime.turnTracker,
				recoveryManager:      runtime.recoveryManager,
				eventValidator:       runtime.eventValidator,
				compactTracking:      runtime.compactTracking,
				consumedCommandUUIDs: runtime.consumedCommandUUIDs,
				messagesForQuery:     runtime.model.messagesForQuery,
				assistantMessages:    runtime.model.assistantMessages,
				toolUseBlocks:        runtime.model.toolUseBlocks,
				toolResults:          toolResults,
				toolUseContext:       runtime.model.toolUseContext,
				queryTracking:        runtime.prepared.queryTracking,
				cancellationChain:    runtime.cancellationChain,
				shouldPreventContinuation: runtime.model.
					shouldPreventContinuation,
				toolDecision: &runtime.tool.decision,
				yield:        runtime.yield,
			},
		)
	default:
		return projectGraphReconcileRound{}, fmt.Errorf(
			"invalid project graph query reconcile phase %q",
			phase,
		)
	}
	result := projectGraphReconcileRound{Phase: phase}
	switch decision.action {
	case canonicalLoopContinue:
		result.Decision = projectGraphReconcilePrepare
	case canonicalLoopTool:
		result.Decision = projectGraphReconcileTool
	case canonicalLoopTerminal:
		runtime.terminal = decision.terminal
		result.Decision = projectGraphReconcileFinalize
		result.Kind = projectGraphResultTerminal
		if runtime.terminal != nil {
			result.Value = string(runtime.terminal.Reason)
			result.TerminalReason = runtime.terminal.Reason
		}
	default:
		return projectGraphReconcileRound{}, fmt.Errorf(
			"canonical lifecycle returned invalid action %q after %s",
			decision.action,
			phase,
		)
	}
	return result, nil
}

func finalizeProjectGraphQuery(
	ctx context.Context,
	_ projectGraphKernelResult,
) error {
	runtime, err := projectGraphQueryRuntime(ctx)
	if err != nil {
		return err
	}
	if runtime.terminal == nil {
		return fmt.Errorf("project graph query finalizer requires terminal")
	}
	if runtime.cancellationChain != nil {
		runtime.cancellationChain.Cancel("query_terminal")
	}
	if runtime.deps.Transcript != nil {
		return runtime.deps.Transcript.Flush()
	}
	return nil
}
