package engine

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
)

func TestP171PlanApprovalTargetCommitsOnlyAfterToolResult(t *testing.T) {
	p200PreparePlan(t, "plan-target-session", "", "# Plan")
	var executions atomic.Int32
	registry := p170PlanRegistry(&executions)
	registry.Register(planExitTestTool(&executions))
	model := &p170ToolSequenceModel{first: []schema.ToolCall{
		p135c2ToolCall("exit-target", "ExitPlanMode", `{}`),
	}}
	var blockingPromptCalls atomic.Int32
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:      "plan-target-session",
		ThreadID:       "plan-target-thread",
		TranscriptDir:  t.TempDir(),
		CWD:            t.TempDir(),
		PermissionMode: permission.ModePlan,
		ChatModel:      model,
		ToolRegistry:   registry,
		ToolSelection: &tools.ToolSelection{
			Names: []string{"ExitPlanMode"},
		},
		MaxTurns: 2,
		PermissionPrompt: func(
			_ context.Context,
			_ PermissionPromptRequest,
		) PermissionInteractionResult {
			blockingPromptCalls.Add(1)
			return PermissionInteractionResult{Decision: PermissionDeny}
		},
	})
	defer eng.Close()

	events, _ := eng.SubmitMessage(context.Background(), "approve Plan")
	collected := drainEngineEvents(t, events)
	request := p171PlanInterruptRequest(t, collected)
	if request.RequestID != "exit-target" ||
		request.PlanRevision != 2 ||
		request.PlanFileIdentity == "" ||
		!validPlanDigest(request.InitialPlanDigest) ||
		request.ReturnMode != permission.ModeDefault {
		t.Fatalf("Plan approval request = %#v", request)
	}
	if blockingPromptCalls.Load() != 0 {
		t.Fatal("ProjectGraph called the retired blocking Plan adapter")
	}
	if !eng.ResolvePermissionInteraction(
		request.RequestID,
		approvedPlanInteraction(
			request,
			permission.ModeBypassPermissions,
		),
	) {
		t.Fatal("targeted Plan decision was not accepted")
	}
	resumedEvents, _ := eng.SubmitRuntimeItem(
		context.Background(),
		mustClaimGraphDecision(t, eng),
	)
	collected = append(collected, drainEngineEvents(t, resumedEvents)...)
	if terminal := terminalFromEvents(collected); terminal == nil ||
		terminal.Reason != TerminalCompleted {
		t.Fatalf("Plan resume terminal = %#v", terminal)
	}

	awaitingIndex := -1
	activeIndex := -1
	resolvedIndex := -1
	toolIndex := -1
	inactiveIndex := -1
	for index, event := range collected {
		switch event.Type {
		case EventPlanStateTransition:
			if event.PlanStateTransition == nil {
				continue
			}
			switch event.PlanStateTransition.Phase {
			case PlanPhaseAwaitingApproval:
				awaitingIndex = index
			case PlanPhaseActive:
				activeIndex = index
			case PlanPhaseInactive:
				inactiveIndex = index
			}
		case EventPermissionResolved:
			if event.PermissionResolved != nil &&
				event.PermissionResolved.ToolUseID == "exit-target" {
				resolvedIndex = index
				decision := event.PermissionResolved.PlanApproval
				if event.PermissionResolved.Kind != "plan_approval" ||
					decision == nil ||
					decision.Outcome != PlanApprovalApprove ||
					!planApprovalAllowsExit(decision, "exit-target") ||
					decision.TargetMode != permission.ModeBypassPermissions {
					t.Fatalf(
						"Plan approval resolution = %#v",
						event.PermissionResolved,
					)
				}
			}
		case EventToolResult:
			if event.ToolResultMessage != nil &&
				event.ToolResultMessage.ToolCallID == "exit-target" {
				toolIndex = index
			}
		}
	}
	if awaitingIndex < 0 ||
		activeIndex <= awaitingIndex ||
		resolvedIndex <= activeIndex ||
		toolIndex <= resolvedIndex ||
		inactiveIndex <= toolIndex {
		t.Fatalf(
			"Plan approval ordering awaiting=%d active=%d resolved=%d tool=%d inactive=%d events=%#v",
			awaitingIndex,
			activeIndex,
			resolvedIndex,
			toolIndex,
			inactiveIndex,
			collected,
		)
	}
	state := eng.PlanState()
	runtimePlan := eng.RuntimeSnapshot().Threads["plan-target-thread"].Plan
	if executions.Load() != 1 ||
		state.Phase != PlanPhaseInactive ||
		state.Revision != 4 ||
		eng.PermissionMode() != permission.ModeBypassPermissions ||
		eng.approvalTracker.Count() != 0 ||
		runtimePlan == nil ||
		runtimePlan.Phase != PlanPhaseInactive ||
		runtimePlan.PermissionMode != string(permission.ModeBypassPermissions) {
		t.Fatalf(
			"final Plan approval state executions=%d state=%#v runtime=%#v",
			executions.Load(),
			state,
			runtimePlan,
		)
	}
}

func TestP171GenericAllowAndRejectRemainActive(t *testing.T) {
	tests := []struct {
		name     string
		decision func(*PlanApprovalRequest) PermissionInteractionResult
		want     string
	}{
		{
			name: "generic allow",
			decision: func(
				*PlanApprovalRequest,
			) PermissionInteractionResult {
				return PermissionInteractionResult{
					Decision: PermissionAllowOnce,
				}
			},
			want: "structured Plan approval decision required",
		},
		{
			name: "reject with feedback",
			decision: func(
				request *PlanApprovalRequest,
			) PermissionInteractionResult {
				return PermissionInteractionResult{
					Decision: PermissionDeny,
					Message:  "User rejected the plan with feedback: add rollback",
					PlanApproval: &PlanApprovalDecision{
						RequestID:    request.RequestID,
						PlanRevision: request.PlanRevision,
						Outcome:      PlanApprovalRevise,
						TargetMode:   permission.ModePlan,
						Feedback:     "add rollback",
					},
				}
			},
			want: "add rollback",
		},
		{
			name: "unconfirmed permission expansion",
			decision: func(
				request *PlanApprovalRequest,
			) PermissionInteractionResult {
				return PermissionInteractionResult{
					Decision: PermissionAllowOnce,
					PlanApproval: &PlanApprovalDecision{
						RequestID:          request.RequestID,
						PlanRevision:       request.PlanRevision,
						Outcome:            PlanApprovalApprove,
						ReviewedPlanDigest: request.InitialPlanDigest,
						TargetMode:         permission.ModeBypassPermissions,
					},
				}
			},
			want: "explicit confirmation",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p200PreparePlan(t, "reject-session", "", "# Plan")
			var executions atomic.Int32
			eng := NewQueryEngine(QueryEngineConfig{
				SessionID:      "reject-session",
				ThreadID:       "reject-thread",
				TranscriptDir:  t.TempDir(),
				CWD:            t.TempDir(),
				PermissionMode: permission.ModePlan,
				ChatModel: &p170ToolSequenceModel{first: []schema.ToolCall{
					p135c2ToolCall("exit-reject", "ExitPlanMode", `{}`),
				}},
				ToolRegistry: p170PlanRegistry(&executions),
				ToolSelection: &tools.ToolSelection{
					Names: []string{"ExitPlanMode"},
				},
				MaxTurns: 2,
				PermissionPrompt: func(
					context.Context,
					PermissionPromptRequest,
				) PermissionInteractionResult {
					t.Fatal("ProjectGraph called the retired blocking Plan adapter")
					return PermissionInteractionResult{
						Decision: PermissionDeny,
					}
				},
			})
			defer eng.Close()

			events, _ := eng.SubmitMessage(context.Background(), "review Plan")
			collected := drainEngineEvents(t, events)
			request := p171PlanInterruptRequest(t, collected)
			if !eng.ResolvePermissionInteraction(
				request.RequestID,
				test.decision(request),
			) {
				t.Fatal("Plan decision was not accepted")
			}
			resumedEvents, _ := eng.SubmitRuntimeItem(
				context.Background(),
				mustClaimGraphDecision(t, eng),
			)
			collected = append(
				collected,
				drainEngineEvents(t, resumedEvents)...,
			)
			var result *schema.Message
			var resolution *PermissionResolvedEvent
			for _, event := range collected {
				if event.Type == EventToolResult &&
					event.ToolResultMessage != nil &&
					event.ToolResultMessage.ToolCallID == "exit-reject" {
					result = event.ToolResultMessage
				}
				if event.Type == EventPermissionResolved &&
					event.PermissionResolved != nil &&
					event.PermissionResolved.ToolUseID == "exit-reject" {
					resolution = event.PermissionResolved
				}
			}
			if result == nil ||
				result.Extra == nil ||
				result.Extra["is_error"] != true ||
				!strings.Contains(result.Content, test.want) {
				t.Fatalf("rejected ExitPlanMode result = %#v", result)
			}
			if resolution == nil ||
				resolution.PlanApproval == nil ||
				resolution.PlanApproval.Approved ||
				resolution.PlanApproval.RequestID != "exit-reject" {
				t.Fatalf("rejected Plan resolution = %#v", resolution)
			}
			state := eng.PlanState()
			if executions.Load() != 0 ||
				state.Phase != PlanPhaseActive ||
				state.ApprovalRequestID != "" ||
				state.Revision != 3 ||
				eng.PermissionMode() != permission.ModePlan ||
				eng.approvalTracker.Count() != 0 {
				t.Fatalf(
					"rejected Plan state executions=%d state=%#v mode=%q",
					executions.Load(),
					state,
					eng.PermissionMode(),
				)
			}
		})
	}
}

func TestP171MissingInteractionProviderFailsClosedAgainstAllowCallback(
	t *testing.T,
) {
	var executions atomic.Int32
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:      "headless-plan-session",
		ThreadID:       "headless-plan-thread",
		TranscriptDir:  t.TempDir(),
		CWD:            t.TempDir(),
		PermissionMode: permission.ModePlan,
		ChatModel: &p170ToolSequenceModel{first: []schema.ToolCall{
			p135c2ToolCall("exit-headless", "ExitPlanMode", `{}`),
		}},
		ToolRegistry: p170PlanRegistry(&executions),
		ToolSelection: &tools.ToolSelection{
			Names: []string{"ExitPlanMode"},
		},
		MaxTurns: 2,
		CanUseTool: func(
			context.Context,
			string,
			map[string]any,
			*ToolUseContext,
		) (bool, string) {
			return true, "headless bypass callback"
		},
	})
	defer eng.Close()

	events, _ := eng.SubmitMessage(context.Background(), "headless Plan")
	collected := drainEngineEvents(t, events)
	var result *schema.Message
	for _, event := range collected {
		if event.Type == EventToolResult &&
			event.ToolResultMessage != nil &&
			event.ToolResultMessage.ToolCallID == "exit-headless" {
			result = event.ToolResultMessage
		}
		if event.Type == EventPlanStateTransition {
			t.Fatalf(
				"missing Plan interaction provider emitted transition: %#v",
				event,
			)
		}
	}
	if result == nil ||
		result.Extra == nil ||
		result.Extra["is_error"] != true ||
		!strings.Contains(
			result.Content,
			"structured Plan approval prompting not available",
		) {
		t.Fatalf("headless Plan result = %#v", result)
	}
	state := eng.PlanState()
	if executions.Load() != 0 ||
		state.Phase != PlanPhaseActive ||
		state.Revision != 1 ||
		eng.PermissionMode() != permission.ModePlan {
		t.Fatalf(
			"headless Plan state executions=%d state=%#v mode=%q",
			executions.Load(),
			state,
			eng.PermissionMode(),
		)
	}
}

func TestP171WrongOwnerAndIdempotentDuplicateResponsesAreInert(t *testing.T) {
	p200PreparePlan(t, "stale-session", "", "# Plan")
	var executions atomic.Int32
	registry := p170PlanRegistry(&executions)
	registry.Register(planExitTestTool(&executions))
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:      "stale-session",
		ThreadID:       "stale-thread",
		TranscriptDir:  t.TempDir(),
		CWD:            t.TempDir(),
		PermissionMode: permission.ModePlan,
		ChatModel: &p170ToolSequenceModel{first: []schema.ToolCall{
			p135c2ToolCall("exit-stale", "ExitPlanMode", `{}`),
		}},
		ToolRegistry: registry,
		ToolSelection: &tools.ToolSelection{
			Names: []string{"ExitPlanMode"},
		},
		MaxTurns: 2,
		PermissionPrompt: func(
			context.Context,
			PermissionPromptRequest,
		) PermissionInteractionResult {
			t.Fatal("ProjectGraph called the retired blocking Plan adapter")
			return PermissionInteractionResult{
				Decision: PermissionDeny,
			}
		},
	})
	defer eng.Close()

	events, _ := eng.SubmitMessage(context.Background(), "review Plan")
	collected := drainEngineEvents(t, events)
	request := p171PlanInterruptRequest(t, collected)
	eng.permissionCoordinator.mu.Lock()
	pending := eng.permissionCoordinator.pending[permissionRequestKey{
		engineID: eng.permissionEngineID, toolUseID: "exit-stale",
	}]
	eng.permissionCoordinator.mu.Unlock()
	if pending != nil {
		t.Fatal("durable Graph Plan approval leaked into blocking coordinator")
	}
	if eng.ResolvePermissionInteraction(
		"wrong-owner",
		approvedPlanInteraction(
			request,
			permission.ModeDefault,
		),
	) {
		t.Fatal("wrong-owner Plan response resolved a request")
	}
	awaiting := eng.PlanState()
	if awaiting.Phase != PlanPhaseAwaitingApproval ||
		awaiting.ApprovalRequestID != "exit-stale" {
		t.Fatalf("wrong-owner response changed Plan state: %#v", awaiting)
	}
	decision := approvedPlanInteraction(
		request,
		permission.ModeDefault,
	)
	if !eng.ResolvePermissionInteraction("exit-stale", decision) {
		t.Fatal("matching Plan response did not resolve")
	}
	if !eng.ResolvePermissionInteraction("exit-stale", decision) {
		t.Fatal("idempotent Plan response retry was not acknowledged")
	}
	conflicting := approvedPlanInteraction(
		request,
		permission.ModeBypassPermissions,
	)
	if eng.ResolvePermissionInteraction("exit-stale", conflicting) {
		t.Fatal("conflicting duplicate Plan response replaced the first decision")
	}
	resumedEvents, _ := eng.SubmitRuntimeItem(
		context.Background(),
		mustClaimGraphDecision(t, eng),
	)
	_ = drainEngineEvents(t, resumedEvents)
	if state := eng.PlanState(); state.Phase != PlanPhaseInactive ||
		state.Revision != 4 ||
		eng.PermissionMode() != permission.ModeDefault ||
		executions.Load() != 1 {
		t.Fatalf("matching Plan response did not commit once: %#v", state)
	}
	if item, ok, err := eng.ClaimNextRuntimeItem(); err != nil || ok {
		t.Fatalf(
			"duplicate Plan response left another item: item=%#v ok=%v err=%v",
			item,
			ok,
			err,
		)
	}
}

func TestP171WaitingApprovalSurvivesCallerCancellationUntilTargetedResume(
	t *testing.T,
) {
	p200PreparePlan(t, "cancel-session", "", "# Plan")
	var executions atomic.Int32
	registry := p170PlanRegistry(&executions)
	registry.Register(planExitTestTool(&executions))
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:      "cancel-session",
		ThreadID:       "cancel-thread",
		TranscriptDir:  t.TempDir(),
		CWD:            t.TempDir(),
		PermissionMode: permission.ModePlan,
		ChatModel: &p170ToolSequenceModel{first: []schema.ToolCall{
			p135c2ToolCall("exit-cancel", "ExitPlanMode", `{}`),
		}},
		ToolRegistry: registry,
		ToolSelection: &tools.ToolSelection{
			Names: []string{"ExitPlanMode"},
		},
		MaxTurns: 2,
		PermissionPrompt: func(
			_ context.Context,
			_ PermissionPromptRequest,
		) PermissionInteractionResult {
			t.Fatal("ProjectGraph called the retired blocking Plan adapter")
			return PermissionInteractionResult{
				Decision: PermissionDeny,
			}
		},
	})
	defer eng.Close()

	ctx, cancel := context.WithCancel(context.Background())
	events, _ := eng.SubmitMessage(ctx, "cancel Plan approval")
	initial := drainEngineEvents(t, events)
	request := p171PlanInterruptRequest(t, initial)
	cancel()
	if state := eng.PlanState(); state.Phase != PlanPhaseAwaitingApproval ||
		state.ApprovalRequestID != request.RequestID ||
		state.Revision != request.PlanRevision ||
		executions.Load() != 0 {
		t.Fatalf(
			"caller cancellation lost durable approval: state=%#v executions=%d",
			state,
			executions.Load(),
		)
	}
	if !eng.ResolvePermissionInteraction(
		request.RequestID,
		approvedPlanInteraction(request, permission.ModeDefault),
	) {
		t.Fatal("durable approval was not resumable after caller cancellation")
	}
	resumedEvents, _ := eng.SubmitRuntimeItem(
		context.Background(),
		mustClaimGraphDecision(t, eng),
	)
	resumed := drainEngineEvents(t, resumedEvents)
	if terminal := terminalFromEvents(resumed); terminal == nil ||
		terminal.Reason != TerminalCompleted ||
		executions.Load() != 1 {
		t.Fatalf(
			"resumed approval terminal=%#v executions=%d events=%#v",
			terminal,
			executions.Load(),
			resumed,
		)
	}
	if state := eng.PlanState(); state.Phase != PlanPhaseInactive ||
		state.Revision != 4 ||
		eng.PermissionMode() != permission.ModeDefault {
		t.Fatalf("resumed approval state = %#v", state)
	}
}

func TestP171ProjectGraphUsesStructuredPlanApproval(t *testing.T) {
	p200PreparePlan(t, "graph-approval-session", "", "# Plan")
	registry := p170PlanRegistry(nil)
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:      "graph-approval-session",
		ThreadID:       "graph-approval-thread",
		CWD:            t.TempDir(),
		PermissionMode: permission.ModePlan,
		ToolRegistry:   registry,
		PermissionPrompt: func(
			_ context.Context,
			request PermissionPromptRequest,
		) PermissionInteractionResult {
			return approvedPlanInteraction(
				request.PlanApproval,
				permission.ModeAcceptEdits,
			)
		},
	})
	defer eng.Close()
	turnID := "graph-approval-turn"
	if _, err := eng.beginPlanTurn(turnID); err != nil {
		t.Fatal(err)
	}
	defer eng.endPlanTurn(turnID)

	toolCtx := &ToolUseContext{
		SessionID: "graph-approval-session",
		ThreadID:  "graph-approval-thread",
		PlanMode:  true,
		Options: &ToolUseOptions{
			PermissionMode: permission.ModePlan,
		},
	}
	var eventMu sync.Mutex
	var events []QueryEvent
	record := func(event QueryEvent) {
		event = eng.decorateRuntimeEvent(turnID, event)
		eventMu.Lock()
		events = append(events, event)
		eventMu.Unlock()
	}
	exitCall := p135c2ToolCall(
		"graph-exit-approval",
		"ExitPlanMode",
		`{}`,
	)
	result, err := runCanonicalToolRound(
		context.Background(),
		canonicalToolRoundInput{
			params: QueryParams{
				ToolRegistry:  registry,
				CanUseTool:    eng.wrappedCanUseTool,
				ToolExecutor:  eng.toolExecutor,
				HookExecutor:  eng.config.HookExecutor,
				ResultStorage: eng.resultStorage,
				TransitionPermissionMode: func(
					current *ToolUseContext,
					mode permission.Mode,
					requestID string,
				) (*ToolUseContext, func(), error) {
					return eng.transitionPermissionModeForTurn(
						turnID,
						func(event QueryEvent) bool {
							record(event)
							return true
						},
						current,
						mode,
						requestID,
					)
				},
				CancelToolInteraction: func(toolUseID string) bool {
					return eng.ResolvePermissionInteraction(
						toolUseID,
						PermissionInteractionResult{
							Decision: PermissionCancelled,
							Message:  "permission request cancelled",
						},
					)
				},
				repeatedToolGuard: newRepeatedToolCallGuard(),
			},
			toolCalls: []*schema.ToolCall{
				&exitCall,
			},
			toolUseContext:    toolCtx,
			cancellationChain: NewCancellationChain(context.Background()),
			yield:             record,
		},
	)
	if err != nil {
		t.Fatalf("Graph Plan approval: %v", err)
	}
	if len(result.toolResults) != 1 ||
		result.toolResults[0].Extra["is_error"] == true {
		t.Fatalf("Graph Plan approval result = %#v", result)
	}
	eventMu.Lock()
	collected := append([]QueryEvent(nil), events...)
	eventMu.Unlock()
	toolIndex := -1
	inactiveIndex := -1
	for index, event := range collected {
		if event.Type == EventToolResult &&
			event.ToolResultMessage != nil &&
			event.ToolResultMessage.ToolCallID == "graph-exit-approval" {
			toolIndex = index
		}
		if event.Type == EventPlanStateTransition &&
			event.PlanStateTransition != nil &&
			event.PlanStateTransition.Phase == PlanPhaseInactive {
			inactiveIndex = index
		}
	}
	if toolIndex < 0 ||
		inactiveIndex <= toolIndex ||
		eng.PermissionMode() != permission.ModeAcceptEdits ||
		eng.PlanState().Phase != PlanPhaseInactive ||
		toolCtx.Options.PermissionMode != permission.ModeAcceptEdits ||
		toolCtx.PlanMode {
		t.Fatalf(
			"Graph Plan approval ordering/state tool=%d inactive=%d state=%#v ctx=%#v events=%#v",
			toolIndex,
			inactiveIndex,
			eng.PlanState(),
			toolCtx,
			collected,
		)
	}
}

func p171PlanInterruptRequest(
	t *testing.T,
	events []QueryEvent,
) *PlanApprovalRequest {
	t.Helper()
	var request *PlanApprovalRequest
	var terminal *Terminal
	for _, event := range events {
		if event.Type == EventPermissionRequest &&
			event.PermissionRequest != nil &&
			event.PermissionRequest.PlanApproval != nil {
			request = clonePlanApprovalRequest(
				event.PermissionRequest.PlanApproval,
			)
		}
		if event.Type == EventTerminal {
			terminal = event.TerminalInfo
		}
	}
	if request == nil ||
		terminal == nil ||
		terminal.Reason != TerminalWaitingInput {
		t.Fatalf(
			"Plan interrupt request=%#v terminal=%#v events=%#v",
			request,
			terminal,
			events,
		)
	}
	return request
}

func terminalFromEvents(events []QueryEvent) *Terminal {
	var terminal *Terminal
	for _, event := range events {
		if event.Type == EventTerminal {
			terminal = event.TerminalInfo
		}
	}
	return terminal
}

func approvedPlanInteraction(
	request *PlanApprovalRequest,
	target permission.Mode,
) PermissionInteractionResult {
	if request == nil {
		return PermissionInteractionResult{
			Decision: PermissionDeny,
			Message:  "missing Plan approval request",
		}
	}
	return PermissionInteractionResult{
		Decision: PermissionAllowOnce,
		PlanApproval: &PlanApprovalDecision{
			RequestID:          request.RequestID,
			PlanRevision:       request.PlanRevision,
			Outcome:            PlanApprovalApprove,
			ReviewedPlanDigest: request.InitialPlanDigest,
			Confirmed:          true,
			TargetMode:         target,
		},
	}
}

func p200PreparePlan(
	t *testing.T,
	sessionID string,
	agentID string,
	content string,
) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := tools.SavePlan(sessionID, agentID, content); err != nil {
		t.Fatal(err)
	}
	return tools.GetPlanFilePath(sessionID, agentID)
}

func planExitTestTool(executions *atomic.Int32) tools.ToolImpl {
	return tools.ToolImpl{
		Info:                 &schema.ToolInfo{Name: "ExitPlanMode"},
		NeedsPermissions:     true,
		IsPlanModeTransition: true,
		ExecuteCtx: func(context.Context, string) (string, error) {
			if executions != nil {
				executions.Add(1)
			}
			return "Plan mode exited.", nil
		},
	}
}
