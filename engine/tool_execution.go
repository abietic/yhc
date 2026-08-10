package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/storage"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

type toolExecutionOutcome struct {
	BeforeResults       []*schema.Message
	Result              *schema.Message
	AfterResults        []*schema.Message
	PreventContinuation bool
	// ContextModifier optionally modifies the ToolUseContext after this tool
	// completes. Only honored for tools that run serially (not concurrency-safe).
	// Mirrors ToolResult.contextModifier from the reference.
	ContextModifier func(
		*ToolUseContext,
	) (*ToolUseContext, func(), error)
}

func executeToolCall(
	ctx context.Context,
	params QueryParams,
	hookExecutor *hooks.Executor,
	toolCtx *ToolUseContext,
	toolCall *schema.ToolCall,
	yieldFn func(QueryEvent),
) *toolExecutionOutcome {
	if toolCall == nil {
		return nil
	}
	if yieldFn != nil {
		startEvent, err := buildCanonicalToolStartProjection(toolCall)
		if err != nil {
			return &toolExecutionOutcome{
				Result: newToolResultMessage(
					toolCall,
					fmt.Sprintf("failed to project tool start: %v", err),
					true,
				),
			}
		}
		yieldFn(startEvent)
	}

	toolName := toolCall.Function.Name
	toolUseID := toolCall.ID
	if reason, unavailable := tools.UnavailableBuiltinToolReason(strings.TrimSpace(toolName)); unavailable {
		return &toolExecutionOutcome{
			Result: newToolResultMessage(
				toolCall,
				fmt.Sprintf("Error: Tool unavailable: %s: %s", strings.TrimSpace(toolName), reason),
				true,
			),
		}
	}
	ticket := repeatedToolTicket(toolCall)
	if ticket == nil && params.repeatedToolGuard != nil {
		ticket = params.repeatedToolGuard.reserve()
	}
	ticketReleased := false
	defer func() {
		if ticket != nil && !ticketReleased {
			ticket.release(false)
		}
	}()
	input, parseErr := parseToolInput(toolCall.Function.Arguments)
	if parseErr != nil {
		return &toolExecutionOutcome{
			Result: newToolResultMessage(toolCall, fmt.Sprintf("invalid tool input JSON for %s: %v", toolName, parseErr), true),
		}
	}

	preparedInput, prepareErr := prepareToolInputForExecution(
		params.ToolRegistry,
		toolName,
		input,
		params.CanUseTool != nil,
	)
	if prepareErr != nil {
		return &toolExecutionOutcome{
			Result: newToolResultMessage(toolCall, prepareErr.Error(), true),
		}
	}
	canonicalToolName := preparedInput.resolution.CanonicalName
	input = preparedInput.input

	if ticket != nil {
		fingerprint := repeatedToolCallFingerprint(canonicalToolName, input)
		decision, attempt, err := ticket.await(ctx, fingerprint)
		if err != nil {
			// await owns deferred release when cancellation wins before the
			// predecessor. Do not let the fallback defer bypass that ordering.
			ticketReleased = true
			return &toolExecutionOutcome{
				Result: newToolResultMessage(toolCall, "Repeated identical tool call blocked while waiting for admission: "+err.Error(), true),
			}
		}
		switch decision {
		case repeatedToolAllow:
			ticket.release(false)
			ticketReleased = true
		case repeatedToolRequestOverride:
			message := "This is the third consecutive identical tool call. Run this call once, or stop and change strategy."
			if yieldFn != nil {
				yieldFn(QueryEvent{
					Type: EventPermissionRequest,
					PermissionRequest: &PermissionRequestEvent{
						ToolName: canonicalToolName, ToolUseID: toolUseID, Message: message,
						Source: "callback", Kind: "repeated_tool", Attempt: attempt,
					},
				})
			}
			allowed := false
			reason := "no interactive one-call override is available"
			if params.RepeatedToolCallPrompt != nil {
				allowed, reason = params.RepeatedToolCallPrompt(ctx, canonicalToolName, toolUseID, attempt, toolCtx)
			}
			if yieldFn != nil {
				resolvedMessage := reason
				if strings.TrimSpace(resolvedMessage) == "" {
					if allowed {
						resolvedMessage = "one-call override granted"
					} else {
						resolvedMessage = "stop and change strategy"
					}
				}
				resolvedDecision := "deny"
				if allowed {
					resolvedDecision = "allow"
				}
				yieldFn(QueryEvent{
					Type: EventPermissionResolved,
					PermissionResolved: &PermissionResolvedEvent{
						ToolUseID: toolUseID, Decision: resolvedDecision,
						Reason: "repeated_tool", Message: resolvedMessage, Kind: "repeated_tool", Attempt: attempt,
					},
				})
			}
			ticket.release(allowed)
			ticketReleased = true
			if !allowed {
				if strings.TrimSpace(reason) == "" {
					reason = "user chose to stop and change strategy"
				}
				return &toolExecutionOutcome{
					Result: newToolResultMessage(toolCall, "Repeated identical tool call blocked: "+reason+". Change the tool or its input before retrying.", true),
				}
			}
		case repeatedToolBlock:
			ticket.release(false)
			ticketReleased = true
			return &toolExecutionOutcome{
				Result: newToolResultMessage(toolCall, "Repeated identical tool call blocked after 3 consecutive attempts. Change the tool or its input before retrying.", true),
			}
		}
	}

	outcome := &toolExecutionOutcome{}
	currentInput := cloneInputMap(input)
	var planApprovalDecision *PlanApprovalDecision
	preToolPreventContinuation := false
	preToolStopReason := ""
	hookPermBehavior := hooks.HookPermissionNone

	if hookExecutor != nil {
		preResult := hookExecutor.ExecutePreTool(ctx, toolName, toolUseID, cloneInputMap(currentInput))
		if preResult != nil {
			outcome.BeforeResults = append(outcome.BeforeResults, preResult.Attachments...)
			if preResult.UpdatedInput != nil {
				currentInput = cloneInputMap(preResult.UpdatedInput)
			}
			if preResult.PreventContinuation {
				outcome.PreventContinuation = true
				preToolPreventContinuation = true
				preToolStopReason = preResult.StopReason
			}
			if preResult.Stop {
				reason := preResult.StopReason
				if reason == "" {
					reason = fmt.Sprintf("tool execution stopped by pre-tool hook for %s", toolName)
				}
				outcome.Result = newToolResultMessage(toolCall, reason, true)
				return outcome
			}
			if preResult.DenyReason != "" {
				outcome.Result = newToolResultMessage(toolCall, preResult.DenyReason, true)
				return outcome
			}
			// Capture hook permission behavior for the permission check below.
			// Mirrors TS resolveHookPermissionDecision: hook allow does NOT
			// bypass deny rules (inc-4788 invariant).
			hookPermBehavior = preResult.PermissionBehavior
			// If hook explicitly denies via permission behavior, deny immediately.
			if hookPermBehavior == hooks.HookPermissionDeny {
				outcome.Result = newToolResultMessage(toolCall,
					formatPermissionDenied(toolName, "denied by pre-tool hook"), true)
				return outcome
			}
		}
	}
	preparedInput, prepareErr = prepareToolInputForExecution(
		params.ToolRegistry,
		toolName,
		currentInput,
		params.CanUseTool != nil,
	)
	if prepareErr != nil {
		outcome.Result = newToolResultMessage(toolCall, prepareErr.Error(), true)
		return outcome
	}
	canonicalToolName = preparedInput.resolution.CanonicalName
	currentInput = preparedInput.input
	planDecision := evaluateToolContextPlanPolicy(
		toolCtx,
		params.ToolRegistry,
		canonicalToolName,
		currentInput,
	)
	if !planDecision.Allowed {
		outcome.Result = newToolResultMessage(
			toolCall,
			planDecision.Reason,
			true,
		)
		return outcome
	}

	var settledPermissionAction *PermissionActionDescriptor
	if params.CanUseTool != nil {
		// If hook says "allow", we still check rule-based deny but skip
		// interactive prompting. Pass this via context so the permission
		// checker can distinguish.
		permCtx := ctx
		if hookPermBehavior == hooks.HookPermissionAllow {
			permCtx = permission.WithHookAllowed(ctx)
		}
		if yieldFn != nil {
			permCtx = withClassifierStatusEmitter(permCtx, yieldFn)
			permCtx = withPermissionPromptEmitter(permCtx, yieldFn)
			permCtx = withPermissionReviewEmitter(permCtx, yieldFn)
		}
		var canUseUpdatedInput map[string]any
		adapted := permission.SimpleCanUseTool(func(innerCtx context.Context, toolName string, input map[string]any) (bool, string) {
			callCtx := withUpdatedInputPtr(
				withToolUseID(innerCtx, toolUseID),
				&canUseUpdatedInput,
			)
			callCtx = withSettledPermissionActionPtr(
				callCtx,
				&settledPermissionAction,
			)
			callCtx = withPlanApprovalDecisionPtr(
				callCtx,
				&planApprovalDecision,
			)
			return params.CanUseTool(callCtx, toolName, input, toolCtx)
		})
		checker := permission.NewChecker(adapted)
		permResult := checker.Check(permCtx, toolName, toolUseID, cloneInputMap(currentInput))
		if canUseUpdatedInput != nil && permResult.UpdatedInput == nil {
			permResult.UpdatedInput = canUseUpdatedInput
		}

		if permResult.NeedsAsk() {
			// Structured prompting is owned by QueryEngine before the legacy
			// bool adapter returns. A raw ask at this boundary has no canonical
			// live request and therefore fails closed.
			permResult = permission.PermissionResult{
				Decision: permission.DecisionDeny,
				Reason:   permission.ReasonPermissionPrompt,
				Message:  "interactive permission prompting not available",
				ToolName: toolName,
			}
		}

		if permResult.IsDenied() {
			outcome.Result = newToolResultMessage(toolCall, formatPermissionDenied(toolName, permResult.Message), true)

			// Run PermissionDenied hooks. If any hook returns Retry=true,
			// tell the model it may retry the command.
			// Mirrors toolExecution.ts permission-denied hook path.
			if hookExecutor != nil && hookExecutor.HasPermissionDeniedHooks() {
				hookResult := hookExecutor.ExecutePermissionDenied(ctx, toolName, toolUseID, cloneInputMap(currentInput), permResult.Message)
				if hookResult != nil {
					outcome.AfterResults = append(outcome.AfterResults, hookResult.Attachments...)
					if hookResult.Retry {
						outcome.AfterResults = append(outcome.AfterResults, newPermissionDeniedRetryMessage(toolName, toolUseID))
					}
				}
			}

			return outcome
		}
		// Apply updated input from permission result (e.g. hooks modifying input).
		// Mirrors toolExecution.ts updatedInput handling.
		if permResult.UpdatedInput != nil {
			currentInput = cloneInputMap(permResult.UpdatedInput)
		}
	}
	preparedInput, prepareErr = prepareToolInputForExecution(
		params.ToolRegistry,
		toolName,
		currentInput,
		params.CanUseTool != nil,
	)
	if prepareErr != nil {
		outcome.Result = newToolResultMessage(toolCall, prepareErr.Error(), true)
		return outcome
	}
	canonicalToolName = preparedInput.resolution.CanonicalName
	currentInput = preparedInput.input
	if settledPermissionAction != nil &&
		!permissionActionMatchesPreparedInput(
			*settledPermissionAction,
			preparedInput,
		) {
		outcome.Result = newToolResultMessage(
			toolCall,
			"permission action changed before tool dispatch",
			true,
		)
		return outcome
	}
	if canonicalToolName == "ExitPlanMode" &&
		!planApprovalAllowsExit(planApprovalDecision, toolUseID) {
		outcome.Result = newToolResultMessage(
			toolCall,
			"ExitPlanMode requires a structured Plan approval decision",
			true,
		)
		return outcome
	}
	planDecision = evaluateToolContextPlanPolicy(
		toolCtx,
		params.ToolRegistry,
		canonicalToolName,
		currentInput,
	)
	if !planDecision.Allowed {
		outcome.Result = newToolResultMessage(
			toolCall,
			planDecision.Reason,
			true,
		)
		return outcome
	}

	encoded, err := json.Marshal(currentInput)
	if err != nil {
		outcome.Result = newToolResultMessage(toolCall, fmt.Sprintf("failed to encode tool input for %s: %v", toolName, err), true)
		return outcome
	}
	jsonInput := string(encoded)

	if params.ToolExecutor == nil {
		outcome.Result = newToolResultMessage(toolCall, "tool executor not configured", true)
		return outcome
	}
	if toolCtx != nil && toolCtx.Options != nil && toolCtx.Options.PermissionMode != "" {
		ctx = tools.WithInheritedPermissionMode(ctx, string(toolCtx.Options.PermissionMode))
	}
	if toolCtx != nil && toolCtx.Options != nil &&
		strings.TrimSpace(toolCtx.Options.PlanFilePath) != "" {
		ctx = tools.WithPlanFileIdentity(
			ctx,
			toolCtx.Options.PlanFilePath,
		)
	}
	if settledPermissionAction != nil {
		ctx = withPermissionDispatchAction(
			ctx,
			*settledPermissionAction,
			toolCtx,
		)
	}
	ctx = tools.WithToolUseID(ctx, toolUseID)
	var attachmentMu sync.Mutex
	toolAttachments := make([]*schema.Message, 0)
	attachmentOpen := true
	ctx = tools.WithAttachmentFn(ctx, func(message *schema.Message) {
		if message == nil {
			return
		}
		attachmentMu.Lock()
		defer attachmentMu.Unlock()
		if !attachmentOpen {
			return
		}
		toolAttachments = append(toolAttachments, message)
	})

	if yieldFn != nil {
		inputEvent, projectionErr := buildCanonicalToolInputProjection(
			toolUseID,
			currentInput,
		)
		if projectionErr != nil {
			outcome.Result = newToolResultMessage(
				toolCall,
				fmt.Sprintf(
					"failed to project effective tool input: %v",
					projectionErr,
				),
				true,
			)
			return outcome
		}
		yieldFn(inputEvent)
	}
	result, execErr := params.ToolExecutor(ctx, toolName, jsonInput)
	attachmentMu.Lock()
	attachmentOpen = false
	collectedAttachments := append([]*schema.Message(nil), toolAttachments...)
	attachmentMu.Unlock()
	if execErr != nil {
		if hookExecutor != nil {
			failureResult := hookExecutor.ExecutePostToolFailure(ctx, toolName, toolUseID, cloneInputMap(currentInput), execErr)
			if failureResult != nil {
				outcome.AfterResults = append(outcome.AfterResults, failureResult.Attachments...)
				if failureResult.PreventContinuation {
					outcome.PreventContinuation = true
				}
			}
		}
		outcome.Result = newToolResultMessage(toolCall, formatToolError(execErr.Error()), true)
		return outcome
	}

	// Level 1 offloading: persist large tool results to disk at execution time.
	// Mirrors processToolResultBlock → maybePersistLargeToolResult in the reference.
	// Tools with maxResultSizeChars=Infinity (like Read) are not offloaded here;
	// that is handled by the per-tool skipToolNames in ApplyToolResultBudget.
	finalResult := maybeOffloadToolResult(params.ResultStorage, toolName, result)

	// Track file state and persist snapshot for resume reconstruction.
	// Only fires for file-mutating tools (Write, Edit) and Read.
	maybeRecordFileState(toolName, currentInput, toolCtx, params.Deps)

	// Empty result injection: prevents model stop-sequence confusion on silent-success commands.
	// Mirrors TS toolResultStorage.ts:287 — "(toolName completed with no output)".
	if strings.TrimSpace(finalResult) == "" {
		finalResult = fmt.Sprintf("(%s completed with no output)", toolName)
	}
	postToolAttachments := make([]*schema.Message, 0)
	postToolPreventContinuation := false
	postToolStopReason := ""
	if hookExecutor != nil {
		postResult := hookExecutor.ExecutePostTool(ctx, toolName, toolUseID, cloneInputMap(currentInput), finalResult)
		if postResult != nil {
			if postResult.ReplaceResult {
				finalResult = postResult.UpdatedResult
			}
			postToolAttachments = append(postToolAttachments, postResult.Attachments...)
			if postResult.PreventContinuation {
				outcome.PreventContinuation = true
				postToolPreventContinuation = true
				postToolStopReason = postResult.StopReason
			}
		}
	}

	outcome.AfterResults = append(outcome.AfterResults, collectedAttachments...)
	if preToolPreventContinuation {
		outcome.AfterResults = append(outcome.AfterResults, newHookStoppedContinuationMessage(
			toolName,
			toolUseID,
			"PreToolUse",
			preToolStopReason,
			"Execution stopped by hook",
		))
	}
	outcome.AfterResults = append(outcome.AfterResults, postToolAttachments...)
	if postToolPreventContinuation {
		outcome.AfterResults = append(outcome.AfterResults, newHookStoppedContinuationMessage(
			toolName,
			toolUseID,
			"PostToolUse",
			postToolStopReason,
			"Execution stopped by PostToolUse hook",
		))
	}

	outcome.Result = newToolResultMessage(toolCall, finalResult, false)
	if mode, ok := planModeTransition(
		toolName,
		toolUseID,
		planApprovalDecision,
	); ok {
		var once sync.Once
		var modified *ToolUseContext
		var publish func()
		var transitionErr error
		outcome.ContextModifier = func(
			current *ToolUseContext,
		) (*ToolUseContext, func(), error) {
			once.Do(func() {
				if params.TransitionPermissionMode != nil {
					modified, publish, transitionErr = params.TransitionPermissionMode(
						current,
						mode,
						toolUseID,
					)
				} else {
					applyPermissionModeToToolContext(current, mode)
					modified = current
				}
			})
			return modified, publish, transitionErr
		}
	}
	return outcome
}

func planModeTransition(
	toolName string,
	requestID string,
	planApproval *PlanApprovalDecision,
) (permission.Mode, bool) {
	switch toolName {
	case "EnterPlanMode":
		return permission.ModePlan, true
	case "ExitPlanMode":
		if !planApprovalAllowsExit(planApproval, requestID) ||
			!knownPlanExitTargetMode(planApproval.TargetMode) {
			return "", false
		}
		return planApproval.TargetMode, true
	default:
		return "", false
	}
}

func planApprovalAllowsExit(
	decision *PlanApprovalDecision,
	requestID string,
) bool {
	return decision != nil &&
		decision.Outcome == PlanApprovalApprove &&
		decision.settled &&
		strings.TrimSpace(requestID) != "" &&
		decision.RequestID == requestID
}

func newClassifierStatusEvent(toolName, toolUseID string, phase ClassifierStatusPhase, result permission.PermissionResult) QueryEvent {
	return QueryEvent{
		Type: EventClassifierStatus,
		ClassifierStatus: &ClassifierStatusEvent{
			ToolName:  toolName,
			ToolUseID: toolUseID,
			Phase:     phase,
			Decision:  string(result.Decision),
			Reason:    string(result.Reason),
			Message:   result.Message,
			UpdatedAt: time.Now().UTC(),
		},
	}
}

type classifierStatusEmitterContextKey struct{}

type permissionPromptEmitterContextKey struct{}

func withPermissionPromptEmitter(ctx context.Context, yield func(QueryEvent)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if yield == nil {
		return ctx
	}
	return context.WithValue(ctx, permissionPromptEmitterContextKey{}, yield)
}

func permissionPromptEmitter(ctx context.Context) func(QueryEvent) {
	if ctx == nil {
		return nil
	}
	emit, _ := ctx.Value(permissionPromptEmitterContextKey{}).(func(QueryEvent))
	return emit
}

// ReportPermissionPromptRequested records the exact point at which an
// interactive adapter starts waiting for the user. Non-interactive permission
// callbacks must not call this helper.
func ReportPermissionPromptRequested(ctx context.Context, toolName string, input map[string]any, message string) {
	if ctx == nil || isCoordinatorOwnedPermissionPrompt(ctx) {
		return
	}
	emit, _ := ctx.Value(permissionPromptEmitterContextKey{}).(func(QueryEvent))
	toolUseID := currentToolUseID(ctx)
	if emit == nil || strings.TrimSpace(toolUseID) == "" {
		return
	}
	emit(QueryEvent{
		Type: EventPermissionRequest,
		PermissionRequest: &PermissionRequestEvent{
			ToolName: toolName, ToolUseID: toolUseID, Input: cloneInputMap(input), Message: message, Source: "callback",
		},
	})
}

// ReportPermissionPromptResolved closes a callback-sourced interactive
// request through the same ordered event stream as the request.
func ReportPermissionPromptResolved(ctx context.Context, allowed bool, reason string) {
	if ctx == nil || isCoordinatorOwnedPermissionPrompt(ctx) {
		return
	}
	emit, _ := ctx.Value(permissionPromptEmitterContextKey{}).(func(QueryEvent))
	toolUseID := currentToolUseID(ctx)
	if emit == nil || strings.TrimSpace(toolUseID) == "" {
		return
	}
	decision := string(permission.DecisionDeny)
	if allowed {
		decision = string(permission.DecisionAllow)
	}
	emit(QueryEvent{
		Type: EventPermissionResolved,
		PermissionResolved: &PermissionResolvedEvent{
			ToolUseID: toolUseID, Decision: decision, Reason: string(permission.ReasonPermissionPrompt), Message: reason,
		},
	})
}

func withClassifierStatusEmitter(ctx context.Context, yield func(QueryEvent)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if yield == nil {
		return ctx
	}
	return context.WithValue(ctx, classifierStatusEmitterContextKey{}, yield)
}

// ReportClassifierStatusChecking emits the classifier checking phase for a
// CanUseTool callback that is about to run actual auto-mode classifier work.
// Generic permission prompts/callbacks must not call this helper.
func ReportClassifierStatusChecking(ctx context.Context, toolName string) {
	emitClassifierStatus(ctx, toolName, currentToolUseID(ctx), ClassifierStatusChecking, permission.PermissionResult{})
}

// ReportClassifierStatusCompleted emits the classifier completed phase with the
// real classifier permission result. Results from non-classifier permission
// sources are ignored so classifier_status does not overclaim its source.
func ReportClassifierStatusCompleted(ctx context.Context, result permission.PermissionResult) {
	if result.Reason != permission.ReasonClassifier {
		return
	}
	emitClassifierStatus(ctx, result.ToolName, currentToolUseID(ctx), ClassifierStatusCompleted, result)
}

// ReportClassifierStatusCleared emits the terminal cleared phase that bounds a
// classifier shimmer started by ReportClassifierStatusChecking.
func ReportClassifierStatusCleared(ctx context.Context, toolName string) {
	emitClassifierStatus(ctx, toolName, currentToolUseID(ctx), ClassifierStatusCleared, permission.PermissionResult{})
}

func emitClassifierStatus(ctx context.Context, toolName, toolUseID string, phase ClassifierStatusPhase, result permission.PermissionResult) {
	if ctx == nil {
		return
	}
	yield, ok := ctx.Value(classifierStatusEmitterContextKey{}).(func(QueryEvent))
	if !ok || yield == nil {
		return
	}
	yield(newClassifierStatusEvent(toolName, toolUseID, phase, result))
}

func parseToolInput(raw string) (map[string]any, error) {
	if raw == "" {
		raw = "{}"
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return nil, err
	}
	if input == nil {
		input = make(map[string]any)
	}
	return input, nil
}

func cloneInputMap(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(input))
	for k, v := range input {
		cloned[k] = v
	}
	return cloned
}

func newToolResultMessage(toolCall *schema.ToolCall, content string, isError bool) *schema.Message {
	msg := &schema.Message{
		Role:    schema.Tool,
		Content: content,
	}
	if toolCall != nil {
		msg.ToolCallID = toolCall.ID
		msg.ToolName = toolCall.Function.Name
	}
	if isError {
		msg.Extra = map[string]any{"is_error": true}
	}
	return msg
}

func newHookStoppedContinuationMessage(toolName, toolUseID, hookEvent, stopReason, fallback string) *schema.Message {
	message := stopReason
	if message == "" {
		message = fallback
	}
	return &schema.Message{
		Role:    schema.User,
		Content: message,
		Extra: map[string]any{
			"is_meta":         true,
			"attachment_kind": "hook_stopped_continuation",
			"hook_name":       fmt.Sprintf("%s:%s", hookEvent, toolName),
			"hook_event":      hookEvent,
			"tool_use_id":     toolUseID,
		},
	}
}

func formatPermissionDenied(toolName, reason string) string {
	if reason == "" {
		return fmt.Sprintf("permission denied for tool %s", toolName)
	}
	return fmt.Sprintf("permission denied for tool %s: %s", toolName, reason)
}

func newPermissionDeniedRetryMessage(toolName, toolUseID string) *schema.Message {
	return &schema.Message{
		Role:    schema.User,
		Content: "The PermissionDenied hook indicated this command is now approved. You may retry it if you would like.",
		Extra: map[string]any{
			"is_meta":         true,
			"attachment_kind": "permission_denied_retry",
			"tool_name":       toolName,
			"tool_use_id":     toolUseID,
		},
	}
}

// maybeOffloadToolResult persists a large tool result to disk and returns a
// preview message. If the result is small enough or storage is nil, returns
// the original result unchanged.
// Mirrors processToolResultBlock → maybePersistLargeToolResult from the reference.
func maybeOffloadToolResult(rs *storage.ResultStorage, toolName, result string) string {
	if rs == nil || !rs.ShouldStore(result) {
		return result
	}
	stored, err := rs.Store(toolName, result)
	if err != nil || stored == nil {
		return result
	}
	return fmt.Sprintf("<persisted-output>\nOutput too large (%s). Full output saved to: %s\nPreview (first %s):\n%s\n</persisted-output>",
		formatByteSize(len(result)),
		stored.FilePath,
		formatByteSize(len(stored.Preview)),
		stored.Preview,
	)
}

const (
	// maxToolErrorLength is the maximum length for tool error messages before truncation.
	// Mirrors TS utils/toolErrors.ts MAX_ERROR_LENGTH.
	maxToolErrorLength  = 10000
	halfToolErrorLength = 5000
)

// formatToolError truncates long error messages to prevent context bloat.
// Shows first 5000 + last 5000 chars with truncation notice in between.
// Mirrors TS utils/toolErrors.ts:5-22 formatError().
func formatToolError(errMsg string) string {
	if len(errMsg) <= maxToolErrorLength {
		return errMsg
	}
	head := errMsg[:halfToolErrorLength]
	tail := errMsg[len(errMsg)-halfToolErrorLength:]
	return fmt.Sprintf("%s\n\n... [%d characters truncated] ...\n\n%s",
		head, len(errMsg)-maxToolErrorLength, tail)
}

// maybeRecordFileState updates the engine's FileStateCache and persists a snapshot
// to the transcript after file-related tool executions. This enables resume
// reconstruction of the file state. Only fires for Read, Write, and Edit tools.
func maybeRecordFileState(toolName string, input map[string]any, toolCtx *ToolUseContext, deps *QueryDeps) {
	if toolCtx == nil || toolCtx.ReadFileState == nil {
		return
	}

	filePath, _ := input["file_path"].(string)
	if filePath == "" {
		return
	}

	fsc := toolCtx.ReadFileState
	fsc.mu.Lock()
	defer fsc.mu.Unlock()
	changed := false

	switch toolName {
	case "Read":
		if !fsc.ReadFiles[filePath] {
			fsc.ReadFiles[filePath] = true
			changed = true
		}
	case "Edit":
		if !fsc.EditFiles[filePath] {
			fsc.EditFiles[filePath] = true
			changed = true
		}
		if !fsc.ReadFiles[filePath] {
			fsc.ReadFiles[filePath] = true
			changed = true
		}
	case "Write":
		if !fsc.WriteFiles[filePath] {
			fsc.WriteFiles[filePath] = true
			changed = true
		}
		if !fsc.ReadFiles[filePath] {
			fsc.ReadFiles[filePath] = true
			changed = true
		}
	default:
		return
	}

	if !changed || deps == nil {
		return
	}
	recordSnapshot := deps.RecordFileStateSnapshot
	if recordSnapshot == nil && deps.Transcript != nil {
		recordSnapshot = deps.Transcript.RecordFileHistorySnapshot
	}
	if recordSnapshot == nil {
		return
	}

	// Build the snapshot map from the current cache state.
	snapshot := make(map[string]transcript.FileState)
	for p := range fsc.ReadFiles {
		fs := snapshot[p]
		fs.Path = p
		fs.WasRead = true
		snapshot[p] = fs
	}
	for p := range fsc.EditFiles {
		fs := snapshot[p]
		fs.Path = p
		fs.WasEdit = true
		snapshot[p] = fs
	}
	for p := range fsc.WriteFiles {
		fs := snapshot[p]
		fs.Path = p
		fs.WasWrite = true
		snapshot[p] = fs
	}

	if err := recordSnapshot(snapshot); err != nil &&
		deps.ReportFileStateSnapshotFailure != nil {
		deps.ReportFileStateSnapshotFailure(err)
	}
}
