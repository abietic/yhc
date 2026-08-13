package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

type threadAttentionCaptureModel struct {
	received chan threadAttentionAnsweredMsg
	ready    chan struct{}
}

type threadAttentionProgramReadyMsg struct{}

func (m threadAttentionCaptureModel) Init() tea.Cmd {
	return func() tea.Msg {
		return threadAttentionProgramReadyMsg{}
	}
}

func (m threadAttentionCaptureModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case threadAttentionProgramReadyMsg:
		close(m.ready)
	case threadAttentionAnsweredMsg:
		answer := msg
		m.received <- answer
		return m, tea.Quit
	}
	return m, nil
}

func (m threadAttentionCaptureModel) View() tea.View {
	return tea.NewView("")
}

func TestThreadAttentionSubmittedResponseSurvivesPresentationSwitch(t *testing.T) {
	app := New(Config{Resumed: true})
	request := threadAttentionRequest{
		ID:         "permission-response-before-switch",
		ThreadID:   app.activeThreadViewID(),
		Kind:       threadAttentionPermission,
		Tool:       "Write",
		Source:     "callback",
		responseCh: make(chan PermissionResponse, 1),
	}
	app.enqueueThreadAttention(request)
	stored, ok := app.threadAttention.get(request.ID)
	if !ok {
		t.Fatal("attention request was not stored")
	}

	uiResponse := make(chan PermissionResponse, 1)
	stored.uiResponse = uiResponse
	app.dialog.responseCh = uiResponse
	app.dialog.respond(PermissionAllow)
	app.suspendThreadAttentionPresentation(request.ThreadID, "child-thread")

	var delivered []PermissionResponse
	forwardThreadAttentionResponse(uiResponse, func(response PermissionResponse) {
		delivered = append(delivered, response)
	})
	if len(delivered) != 1 || delivered[0] != PermissionAllow {
		t.Fatalf("forwarded responses = %v, want exactly [allow]", delivered)
	}
	if _, ok := app.threadAttention.get(request.ID); !ok {
		t.Fatal("presentation switch removed the unresolved canonical request")
	}
}

func TestThreadAttentionResponseForwarderUsesProgramAfterPresentationSwitch(t *testing.T) {
	received := make(chan threadAttentionAnsweredMsg, 1)
	ready := make(chan struct{})
	program := tea.NewProgram(
		threadAttentionCaptureModel{received: received, ready: ready},
		tea.WithInput(nil),
		tea.WithoutRenderer(),
	)
	runDone := make(chan error, 1)
	go func() {
		_, err := program.Run()
		runDone <- err
	}()
	t.Cleanup(program.Quit)
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("program did not initialize")
	}

	app := New(Config{Resumed: true})
	app.program = program
	request := threadAttentionRequest{
		ID:         "program-response-before-switch",
		ThreadID:   app.activeThreadViewID(),
		Kind:       threadAttentionPermission,
		Tool:       "Write",
		Source:     "callback",
		responseCh: make(chan PermissionResponse, 1),
	}
	app.enqueueThreadAttention(request)
	app.dialog.respond(PermissionAllow)
	app.suspendThreadAttentionPresentation(request.ThreadID, "child-thread")

	select {
	case answer := <-received:
		if answer.requestID != request.ID || answer.response != PermissionAllow {
			t.Fatalf("program answer = %#v", answer)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("program did not receive the submitted attention response")
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("program run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("program did not stop after receiving the response")
	}
}

func TestThreadAttentionLateOwnerResponsePreservesNewOwnerDialog(t *testing.T) {
	app := New(Config{Resumed: true})
	oldResponse := make(chan PermissionResponse, 1)
	oldRequest := threadAttentionRequest{
		ID: "old-owner-permission", ThreadID: app.activeThreadViewID(),
		Kind: threadAttentionPermission, Tool: "Write", Source: "callback",
		responseCh: oldResponse,
	}
	app.enqueueThreadAttention(oldRequest)
	updateApp(t, app, tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'a'})})

	if err := app.switchThreadView("new-owner-thread", engine.ThreadModeReplayOnly); err != nil {
		t.Fatal(err)
	}
	newResponse := make(chan PermissionResponse, 1)
	newRequest := threadAttentionRequest{
		ID: "new-owner-permission", ThreadID: app.activeThreadViewID(),
		Kind: threadAttentionPermission, Tool: "Bash", Source: "callback",
		responseCh: newResponse,
	}
	app.enqueueThreadAttention(newRequest)
	if app.threadAttention.activeID != newRequest.ID ||
		!app.hasDialog(StatePermission) ||
		!app.dialog.visible {
		t.Fatal("new owner permission was not presented")
	}

	updateApp(t, app, threadAttentionAnsweredMsg{
		requestID: oldRequest.ID,
		response:  PermissionAllow,
	})
	if app.threadAttention.activeID != newRequest.ID ||
		!app.hasDialog(StatePermission) ||
		!app.dialog.visible {
		t.Fatal("late old-owner response removed the new owner dialog")
	}
	if _, ok := app.threadAttention.get(newRequest.ID); !ok {
		t.Fatal("late old-owner response removed the new owner request")
	}
	if got := <-oldResponse; got != PermissionAllow {
		t.Fatalf("old owner response = %v, want allow", got)
	}
	select {
	case got := <-newResponse:
		t.Fatalf("late old-owner response settled new owner with %v", got)
	default:
	}
}

func TestThreadAttentionLatePlanResponseUsesFrozenOwnerData(t *testing.T) {
	app := New(Config{Resumed: true})
	oldRequest := threadAttentionRequest{
		ID: "old-owner-plan", ThreadID: app.activeThreadViewID(),
		Kind: threadAttentionPlan, Tool: "ExitPlanMode", Source: "callback",
		responseCh: make(chan PermissionResponse, 1),
	}
	app.enqueueThreadAttention(oldRequest)
	app.planDialog.focus = planFocusFeedback
	app.planDialog.feedbackEditor.SetValue("old owner feedback")
	updateApp(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
	stored, _ := app.threadAttention.get(oldRequest.ID)
	if !stored.dataCaptured || stored.responseData.feedback != "old owner feedback" {
		t.Fatalf("submitted Plan data was not frozen: %#v", stored)
	}

	if err := app.switchThreadView("new-plan-owner", engine.ThreadModeReplayOnly); err != nil {
		t.Fatal(err)
	}
	newRequest := threadAttentionRequest{
		ID: "new-owner-plan", ThreadID: app.activeThreadViewID(),
		Kind: threadAttentionPlan, Tool: "ExitPlanMode", Source: "callback",
		responseCh: make(chan PermissionResponse, 1),
	}
	app.enqueueThreadAttention(newRequest)
	app.planDialog.feedbackEditor.SetValue("new owner feedback")

	updateApp(t, app, threadAttentionAnsweredMsg{
		requestID: oldRequest.ID,
		response:  PermissionDeny,
	})
	if app.threadAttention.activeID != newRequest.ID ||
		!app.hasDialog(StatePlanApproval) ||
		!app.planDialog.IsVisible() {
		t.Fatal("late old-owner Plan response removed the new owner dialog")
	}
	responseData := app.takeThreadAttentionResponse(oldRequest.ID)
	if responseData.feedback != "old owner feedback" {
		t.Fatalf("old owner Plan feedback = %q", responseData.feedback)
	}
}

func TestThreadAttentionWaitsForOwnerSwitchAndReplaysOnlyUnresolved(t *testing.T) {
	app, catalog, _ := newThreadNavigationTestApp(t)
	for i := range catalog.Threads {
		if catalog.Threads[i].ThreadID == "child-alpha" {
			catalog.Threads[i].Mode = engine.ThreadModeLiveAttach
			catalog.Threads[i].QuestionCount = 1
		}
	}
	responseCh := make(chan PermissionResponse, 1)
	cmd := app.enqueueThreadAttention(threadAttentionRequest{
		ID: "question-1", ThreadID: "child-alpha", AgentID: "agent-alpha",
		Kind: threadAttentionQuestion, Tool: "AskUserQuestion",
		Input:  `{"questions":[{"question":"Choose","options":[{"label":"A"}]}]}`,
		Source: "callback", responseCh: responseCh,
	})
	if cmd != nil || app.state != StateChat || app.questionDialog.IsVisible() {
		t.Fatalf("inactive question opened globally: state=%v visible=%v", app.state, app.questionDialog.IsVisible())
	}
	if status := app.threadAttentionStatus(); !strings.Contains(status, "attention:1 @alpha scout") {
		t.Fatalf("inactive attention status = %q", status)
	}

	if err := app.activateThreadByID("child-alpha"); err != nil {
		t.Fatal(err)
	}
	if app.state != StateAskUser || !app.questionDialog.IsVisible() || app.activeThreadViewID() != "child-alpha" {
		t.Fatalf("owner switch did not present question: state=%v active=%q", app.state, app.activeThreadViewID())
	}
	app.resolveThreadAttention("question-1", PermissionAllow)
	select {
	case response := <-responseCh:
		if response != PermissionAllow {
			t.Fatalf("question response = %v", response)
		}
	default:
		t.Fatal("question callback was not released")
	}
	if app.threadAttentionStatus() != "" || app.state != StateChat {
		t.Fatalf("resolved question remained visible: status=%q state=%v", app.threadAttentionStatus(), app.state)
	}

	if err := app.activateThreadByID("leader-thread"); err != nil {
		t.Fatal(err)
	}
	if err := app.activateThreadByID("child-alpha"); err != nil {
		t.Fatal(err)
	}
	if app.state == StateAskUser {
		t.Fatal("resolved question replayed after thread switch")
	}
}

func TestThreadAttentionMergesCanonicalCallbackBeforePresentation(t *testing.T) {
	app := New(Config{Resumed: true})
	app.rebindLeaderThreadView("leader-thread")
	app.enqueueThreadAttention(threadAttentionRequest{
		ID: "permission-1", ThreadID: "leader-thread", Kind: threadAttentionPermission,
		Tool: "Bash", Input: `{"command":"make test"}`, Source: "callback",
	})
	if app.state == StatePermission {
		t.Fatal("passive callback event presented before its response handle arrived")
	}
	responseCh := make(chan PermissionResponse, 1)
	app.enqueueThreadAttention(threadAttentionRequest{
		ID: "permission-1", ThreadID: "leader-thread", Kind: threadAttentionPermission,
		Tool: "Bash", Input: `{"command":"make test"}`, SessionScope: "approve tests",
		Source: "callback", responseCh: responseCh,
	})
	if app.state != StatePermission {
		t.Fatalf("callback handle did not activate canonical request: state=%v", app.state)
	}
	app.resolveThreadAttention("permission-1", PermissionDeny)
	select {
	case response := <-responseCh:
		if response != PermissionDeny {
			t.Fatalf("permission response = %v", response)
		}
	default:
		t.Fatal("permission callback was not released")
	}
}

func TestThreadAttentionPreservesPlanTargetBeforeCallbackRelease(t *testing.T) {
	app := New(Config{Resumed: true})
	planBytes := []byte("# Reviewed Plan")
	planPath := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planPath, planBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	responseCh := make(chan PermissionResponse, 1)
	app.enqueueThreadAttention(threadAttentionRequest{
		ID: "plan-1", ThreadID: app.activeThreadViewID(),
		Kind: threadAttentionPlan, Tool: "ExitPlanMode", Source: "callback",
		PlanApproval: &engine.PlanApprovalRequest{
			RequestID: "plan-1", PlanRevision: 2,
			PlanFileIdentity:  planPath,
			InitialPlanDigest: engine.PlanBytesDigest(planBytes),
		},
		responseCh: responseCh,
	})
	app.planDialog.selectedIdx = 2
	app.planDialog.planResult = &engine.PlanApprovalDecision{RequestID: "plan-1", PlanRevision: 2, Outcome: engine.PlanApprovalApprove, ReviewedPlanDigest: engine.PlanBytesDigest(planBytes), TargetMode: permission.ModeBypassPermissions, Confirmed: true}
	app.resolveThreadAttention("plan-1", PermissionAllow)

	responseData := app.takeThreadAttentionResponse("plan-1")
	if responseData.planTarget != permission.ModeBypassPermissions ||
		!responseData.planConfirmed ||
		responseData.planReviewedDigest != engine.PlanBytesDigest(planBytes) {
		t.Fatalf("plan response data = %#v", responseData)
	}
	if response := <-responseCh; response != PermissionAllow {
		t.Fatalf("callback response = %v", response)
	}
}

func TestTUIProjectGraphPlanDecisionUsesProductionEventPath(t *testing.T) {
	tests := []struct {
		name         string
		response     PermissionResponse
		wantOutcome  engine.PlanApprovalOutcome
		wantExecuted int64
		wantPhase    engine.PlanPhase
		wantMode     permission.Mode
		wantDecision engine.PermissionInteractionDecision
	}{
		{
			name:         "approve",
			response:     PermissionAllow,
			wantOutcome:  engine.PlanApprovalApprove,
			wantExecuted: 1,
			wantPhase:    engine.PlanPhaseInactive,
			wantMode:     permission.ModeDefault,
			wantDecision: engine.PermissionAllowOnce,
		},
		{
			name:         "cancel",
			response:     PermissionDeny,
			wantOutcome:  engine.PlanApprovalCancel,
			wantExecuted: 0,
			wantPhase:    engine.PlanPhaseActive,
			wantMode:     permission.ModePlan,
			wantDecision: engine.PermissionDeny,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			sessionID := "tui-plan-" + test.name
			if err := tools.SavePlan(
				sessionID,
				"",
				"# Reviewed TUI Plan\n",
			); err != nil {
				t.Fatal(err)
			}

			var executions atomic.Int64
			registry := tools.NewRegistry()
			registry.Register(tools.ToolImpl{
				Info:                 &schema.ToolInfo{Name: "ExitPlanMode"},
				IsPlanModeTransition: true,
				Execute: func(string) (string, error) {
					executions.Add(1)
					return "exited", nil
				},
			})
			root := t.TempDir()
			query := engine.NewQueryEngine(engine.QueryEngineConfig{
				SessionID:     sessionID,
				ThreadID:      sessionID + "-thread",
				TranscriptDir: filepath.Join(root, "transcripts"),
				CWD:           root,
				ChatModel: &tuiRecoveryModel{responses: []*schema.Message{{
					Role: schema.Assistant,
					ToolCalls: []schema.ToolCall{{
						ID:   "tui-plan-exit-" + test.name,
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "ExitPlanMode",
							Arguments: `{}`,
						},
					}},
				}}},
				ToolRegistry:   registry,
				ToolSelection:  &tools.ToolSelection{Names: []string{"ExitPlanMode"}},
				PermissionMode: permission.ModePlan,
				MaxTurns:       2,
				PermissionPrompt: func(
					context.Context,
					engine.PermissionPromptRequest,
				) engine.PermissionInteractionResult {
					t.Fatal("ProjectGraph called the blocking TUI Plan adapter")
					return engine.PermissionInteractionResult{
						Decision: engine.PermissionDeny,
					}
				},
			})
			t.Cleanup(query.Close)

			events, _ := query.SubmitMessage(
				context.Background(),
				"review Plan",
			)
			var requestEvent engine.QueryEvent
			for event := range events {
				if event.Type == engine.EventPermissionRequest &&
					event.PermissionRequest != nil {
					requestEvent = event
				}
			}
			if requestEvent.PermissionRequest == nil ||
				requestEvent.PermissionRequest.Source != "project_graph" ||
				requestEvent.PermissionRequest.PlanApproval == nil {
				t.Fatalf("ProjectGraph Plan request = %#v", requestEvent)
			}

			app := New(Config{Resumed: true, Engine: query})
			app.rebindLeaderThreadView(sessionID + "-thread")
			app.handleEngineEvent(requestEvent)
			requestID := requestEvent.PermissionRequest.ToolUseID
			if app.state != StatePlanApproval ||
				app.threadAttention.activeID != requestID ||
				!app.planDialog.IsVisible() {
				t.Fatalf(
					"Plan request presentation state=%v active=%q visible=%v",
					app.state,
					app.threadAttention.activeID,
					app.planDialog.IsVisible(),
				)
			}
			app.planDialog.planResult = &engine.PlanApprovalDecision{
				RequestID:    requestEvent.PermissionRequest.PlanApproval.RequestID,
				PlanRevision: requestEvent.PermissionRequest.PlanApproval.PlanRevision,
				Outcome:      test.wantOutcome, TargetMode: permission.ModePlan,
				ReviewedPlanDigest: app.planDialog.ReviewedPlanDigest(),
			}
			if test.wantOutcome == engine.PlanApprovalApprove {
				app.planDialog.planResult.TargetMode = permission.ModeDefault
			}
			app.resolveThreadAttention(requestID, test.response)

			deadline := time.Now().Add(5 * time.Second)
			var items []engine.RuntimeItem
			for len(items) == 0 && time.Now().Before(deadline) {
				items = query.RuntimeItems()
				if len(items) == 0 {
					time.Sleep(time.Millisecond)
				}
			}
			if len(items) != 1 ||
				items[0].PermissionDecision == nil ||
				items[0].PermissionDecision.Result.Decision !=
					test.wantDecision ||
				items[0].PermissionDecision.Result.PlanApproval == nil ||
				items[0].PermissionDecision.Result.PlanApproval.Outcome !=
					test.wantOutcome ||
				items[0].PermissionDecision.Result.PlanApproval.Approved {
				t.Fatalf("typed Plan runtime item = %#v", items)
			}

			item, ok, err := query.ClaimNextRuntimeItem()
			if err != nil || !ok {
				t.Fatalf(
					"claim Plan decision item=%#v ok=%v err=%v",
					item,
					ok,
					err,
				)
			}
			resumed, _ := query.SubmitRuntimeItem(context.Background(), item)
			for range resumed {
			}
			if executions.Load() != test.wantExecuted ||
				query.PlanState().Phase != test.wantPhase ||
				query.PermissionMode() != test.wantMode ||
				query.GetApprovalTracker().Count() != 0 {
				t.Fatalf(
					"Plan result executions=%d state=%#v mode=%q grants=%d",
					executions.Load(),
					query.PlanState(),
					query.PermissionMode(),
					query.GetApprovalTracker().Count(),
				)
			}
		})
	}
}

func TestP138ColdProjectGraphAttentionEnqueuesTargetedResume(t *testing.T) {
	registry := tools.NewRegistry()
	var executions atomic.Int64
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "TUIWrite"},
		ExecuteCtx: func(context.Context, string) (string, error) {
			executions.Add(1)
			return "ok", nil
		},
	})
	chatModel := &tuiRecoveryModel{responses: []*schema.Message{{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:   "tui-graph-write",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "TUIWrite",
				Arguments: `{}`,
			},
		}},
	}}}
	root := t.TempDir()
	query := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:      "tui-graph-session",
		ThreadID:       "tui-graph-thread",
		TranscriptDir:  root,
		CWD:            root,
		ChatModel:      chatModel,
		ToolRegistry:   registry,
		ToolSelection:  &tools.ToolSelection{Names: []string{"TUIWrite"}},
		PermissionMode: permission.ModeDefault,
		MaxTurns:       2,
		PermissionPrompt: func(
			context.Context,
			engine.PermissionPromptRequest,
		) engine.PermissionInteractionResult {
			t.Fatal("ProjectGraph must not call the blocking TUI adapter")
			return engine.PermissionInteractionResult{
				Decision: engine.PermissionDeny,
			}
		},
	})
	t.Cleanup(query.Close)
	events, _ := query.SubmitMessage(
		context.Background(),
		"request graph permission",
	)
	var request *engine.PermissionRequestEvent
	for event := range events {
		if event.Type == engine.EventPermissionRequest {
			request = event.PermissionRequest
		}
	}
	if request == nil || request.Source != "project_graph" {
		t.Fatalf("project graph request = %#v", request)
	}

	app := New(Config{Resumed: true, Engine: query})
	app.rebindLeaderThreadView("tui-graph-thread")
	app.enqueueThreadAttention(threadAttentionRequest{
		ID:           request.ToolUseID,
		ThreadID:     "tui-graph-thread",
		Kind:         threadAttentionPermission,
		Tool:         request.ToolName,
		Input:        `{}`,
		SessionScope: request.Message,
		SessionID:    "tui-graph-session",
		Source:       "project_graph",
	})
	beforeSwitch := query.RuntimeSnapshot()
	if err := app.switchThreadView("other-thread", engine.ThreadModeReplayOnly); err != nil {
		t.Fatal(err)
	}
	if app.threadAttention.activeID != "" || app.hasDialog(StatePermission) {
		t.Fatal("ProjectGraph attention presentation remained active after owner switch")
	}
	if _, ok := app.threadAttention.get(request.ToolUseID); !ok {
		t.Fatal("owner switch removed ProjectGraph attention")
	}
	afterSwitch := query.RuntimeSnapshot()
	if beforeSwitch.Revision != afterSwitch.Revision || executions.Load() != 0 {
		t.Fatalf(
			"owner switch mutated ProjectGraph runtime: revision %d -> %d, executions=%d",
			beforeSwitch.Revision,
			afterSwitch.Revision,
			executions.Load(),
		)
	}
	if err := app.switchThreadView("tui-graph-thread", engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	app.presentNextThreadAttention()
	if app.threadAttention.activeID != request.ToolUseID || !app.hasDialog(StatePermission) {
		t.Fatal("returning to ProjectGraph owner did not re-present attention")
	}
	app.resolveThreadAttention(request.ToolUseID, PermissionAllow)
	app.resolveThreadAttention(request.ToolUseID, PermissionDeny)

	items := query.RuntimeItems()
	if len(items) != 1 ||
		items[0].Kind != engine.RuntimeItemPermissionDecision ||
		items[0].PermissionDecision == nil ||
		items[0].PermissionDecision.RequestID != request.ToolUseID {
		t.Fatalf("targeted graph resume items = %#v", items)
	}
	if executions.Load() != 0 {
		t.Fatalf("attention navigation or settlement executed tool %d times", executions.Load())
	}
}

func TestRepeatedToolAttentionMergesCallbackAndEngineEventByID(t *testing.T) {
	app := New(Config{Resumed: true})
	app.rebindLeaderThreadView("leader-thread")
	app.handleEngineEvent(engine.QueryEvent{Type: engine.EventPermissionRequest, PermissionRequest: &engine.PermissionRequestEvent{
		ToolName: "Bash", ToolUseID: "repeat-1", Message: "engine repeated call", Source: "callback", Kind: engine.PermissionInteractionKindRepeatedTool, Attempt: 3,
	}})
	if app.state == StatePermission {
		t.Fatal("engine event without callback handle presented a dialog")
	}
	responseCh := make(chan PermissionResponse, 1)
	app.enqueueThreadAttention(threadAttentionRequest{
		ID: "repeat-1", ThreadID: "leader-thread", Kind: threadAttentionRepeatedTool, Tool: "Bash",
		SessionScope: "This is the third consecutive identical tool call. Run this call once, or stop and change strategy.", Attempt: 3, Source: "callback", responseCh: responseCh,
	})
	if app.state != StatePermission || !app.dialog.repeatedTool || len(app.threadAttention.requests) != 1 {
		t.Fatalf("repeated callback/event did not merge: state=%v repeated=%v requests=%d", app.state, app.dialog.repeatedTool, len(app.threadAttention.requests))
	}
	app.resolveThreadAttention("repeat-1", PermissionAllow)
	if response := <-responseCh; response != PermissionAllow {
		t.Fatalf("merged repeated-tool response = %v", response)
	}
}

func TestRuntimeRepeatedToolAttentionUsesRepeatedToolDialog(t *testing.T) {
	app := New(Config{Resumed: true})
	app.rebindLeaderThreadView("leader-thread")
	app.threadAttentionProvider = func() []engine.RuntimeThreadAttentionSnapshot {
		return []engine.RuntimeThreadAttentionSnapshot{{ThreadID: "leader-thread", Requests: []engine.RuntimeInteractionSnapshot{{
			ID: "repeat-runtime", Kind: "repeated_tool", ToolName: "Read", Message: "engine repeated call", Attempt: 3, Source: "runtime",
			Input: map[string]any{"file_path": "do-not-render-this-input"},
		}}}}
	}
	app.syncRuntimeThreadAttention()
	if app.state != StatePermission || !app.dialog.repeatedTool || app.dialog.toolInput != "" {
		t.Fatalf("runtime repeated-tool mapping failed: state=%v repeated=%v input=%q", app.state, app.dialog.repeatedTool, app.dialog.toolInput)
	}
	if plain := stripANSIForTest(app.dialog.Overlay("", 100, 30)); strings.Contains(plain, "do-not-render-this-input") {
		t.Fatalf("repeated-tool dialog rendered raw tool input: %q", plain)
	}
}

func TestP172RuntimePlanAttentionReprojectsExactApprovalIdentity(t *testing.T) {
	app := New(Config{Resumed: true})
	app.rebindLeaderThreadView("leader-thread")
	digest := engine.PlanBytesDigest([]byte("# Reviewed Plan"))
	app.threadAttentionProvider = func() []engine.RuntimeThreadAttentionSnapshot {
		return []engine.RuntimeThreadAttentionSnapshot{{
			SessionID: "session-1",
			ThreadID:  "leader-thread",
			Requests: []engine.RuntimeInteractionSnapshot{{
				ID: "exit-1", Kind: "plan_approval",
				ToolName: "ExitPlanMode", Source: "runtime",
				PlanRevision:      5,
				PlanFile:          "/tmp/.claude/plans/session-1.md",
				PlanInitialDigest: digest,
				PlanReturnMode:    string(permission.ModeBypassPermissions),
			}},
		}}
	}
	app.syncRuntimeThreadAttention()
	request, ok := app.threadAttention.get("exit-1")
	if !ok || request.PlanApproval == nil ||
		request.PlanApproval.RequestID != "exit-1" ||
		request.PlanApproval.PlanRevision != 5 ||
		request.PlanApproval.PlanFileIdentity !=
			"/tmp/.claude/plans/session-1.md" ||
		request.PlanApproval.InitialPlanDigest != digest ||
		request.PlanApproval.ReturnMode != permission.ModeBypassPermissions {
		t.Fatalf("runtime Plan attention = %#v", request)
	}
}

func TestThreadAttentionCancellationAndCanonicalResolutionDoNotReplay(t *testing.T) {
	app, _, _ := newThreadNavigationTestApp(t)
	app.enqueueThreadAttention(threadAttentionRequest{
		ID: "cancel-1", ThreadID: "child-alpha", AgentID: "agent-alpha",
		Kind: threadAttentionPermission, Tool: "Write", Source: "callback", responseCh: make(chan PermissionResponse, 1),
	})
	app.cancelThreadAttention("cancel-1")
	if app.threadAttentionStatus() != "" {
		t.Fatalf("canceled attention remained: %q", app.threadAttentionStatus())
	}
	if err := app.activateThreadByID("child-alpha"); err != nil {
		t.Fatal(err)
	}
	if app.state == StatePermission {
		t.Fatal("canceled request replayed on owner switch")
	}

	canonical := []engine.RuntimeThreadAttentionSnapshot{{
		SessionID: "session-1", ThreadID: "child-alpha", AgentID: "agent-alpha", Status: engine.RuntimeThreadCompleted,
		Requests: []engine.RuntimeInteractionSnapshot{{
			ID: "terminal-question", Kind: "question", ToolName: "AskUserQuestion", Source: "prompter",
			Input: map[string]any{"questions": []any{}}, Sequence: 3,
		}},
	}}
	app.threadAttentionProvider = func() []engine.RuntimeThreadAttentionSnapshot { return canonical }
	app.syncRuntimeThreadAttention()
	if app.state != StateAskUser {
		t.Fatalf("terminal unresolved question was not replayed: state=%v", app.state)
	}
	canonical = nil
	app.syncRuntimeThreadAttention()
	if app.state != StateChat {
		t.Fatalf("canonically resolved question remained open: state=%v", app.state)
	}
	if _, exists := app.threadAttention.get("terminal-question"); exists {
		t.Fatal("canonically resolved question remained in TUI store")
	}
}

func TestThreadAttentionStoreRejectsOverflowWithoutDroppingExistingOwner(t *testing.T) {
	store := newThreadAttentionStore(1)
	first := threadAttentionRequest{ID: "first", ThreadID: "thread-1", Kind: threadAttentionPermission}
	if !store.upsert(first) {
		t.Fatal("first attention request rejected")
	}
	overflowResponse := make(chan PermissionResponse, 1)
	if store.upsert(threadAttentionRequest{ID: "second", ThreadID: "thread-2", responseCh: overflowResponse}) {
		t.Fatal("overflow attention request was accepted")
	}
	if _, exists := store.get("first"); !exists {
		t.Fatal("existing attention owner was dropped on overflow")
	}
	select {
	case response := <-overflowResponse:
		if response != PermissionDeny {
			t.Fatalf("overflow response = %v", response)
		}
	default:
		t.Fatal("overflow callback was not denied")
	}
}

func TestThreadAttentionPreservesQuestionAnswerBeforePresentingNextRequest(t *testing.T) {
	app := New(Config{Resumed: true})
	app.rebindLeaderThreadView("leader-thread")
	firstResponse := make(chan PermissionResponse, 1)
	app.enqueueThreadAttention(threadAttentionRequest{
		ID: "question-1", ThreadID: "leader-thread", Kind: threadAttentionQuestion,
		Input: `{"questions":[]}`, Source: "callback", responseCh: firstResponse,
	})
	app.enqueueThreadAttention(threadAttentionRequest{
		ID: "question-2", ThreadID: "leader-thread", Kind: threadAttentionQuestion,
		Input: `{"questions":[]}`, Source: "callback", responseCh: make(chan PermissionResponse, 1),
	})
	app.questionDialog.answerJSON = `{"answers":{"Choose":"A"}}`
	app.resolveThreadAttention("question-1", PermissionAllow)
	if app.threadAttention.activeID != "question-2" || app.questionDialog.AnswerJSON() != "" {
		t.Fatalf("next question was not presented/reset: active=%q answer=%q", app.threadAttention.activeID, app.questionDialog.AnswerJSON())
	}
	response := app.takeThreadAttentionResponse("question-1")
	if response.answerJSON != `{"answers":{"Choose":"A"}}` {
		t.Fatalf("stashed answer = %q", response.answerJSON)
	}
	select {
	case <-firstResponse:
	default:
		t.Fatal("first callback was not released")
	}
}
