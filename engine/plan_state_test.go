package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/tools"
)

func TestP170ExternalPlanTransitionOwnsStateAndRuntimeProjection(
	t *testing.T,
) {
	fixed := time.Date(2026, 7, 18, 17, 0, 0, 0, time.UTC)
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID: "session",
		ThreadID:  "thread",
		AgentID:   "agent",
		CWD:       t.TempDir(),
		Clock:     func() time.Time { return fixed },
	})
	defer eng.Close()

	if err := eng.SetPermissionMode(permission.ModePlan); err != nil {
		t.Fatalf("enter Plan externally: %v", err)
	}
	state := eng.PlanState()
	if state.Phase != PlanPhaseActive ||
		state.Revision != 1 ||
		state.ReturnMode != permission.ModeDefault ||
		state.PlanFileIdentity != tools.GetPlanFilePath("session", "agent") ||
		eng.PermissionMode() != permission.ModePlan {
		t.Fatalf("active Plan state = %#v, mode=%q", state, eng.PermissionMode())
	}
	snapshot := eng.RuntimeSnapshot()
	thread := snapshot.Threads["thread"]
	if thread.Plan == nil ||
		thread.Plan.Phase != PlanPhaseActive ||
		thread.Plan.Revision != state.Revision ||
		thread.Plan.PermissionMode != string(permission.ModePlan) ||
		thread.ActiveTurnID != "" ||
		thread.Status != RuntimeThreadCompleted {
		t.Fatalf("external Plan runtime projection = %#v", thread)
	}
	sequence := thread.LastSequence
	if err := eng.SetPermissionMode(permission.ModePlan); err != nil {
		t.Fatalf("repeat external Plan: %v", err)
	}
	if eng.PlanState().Revision != state.Revision ||
		eng.RuntimeSnapshot().Threads["thread"].LastSequence != sequence {
		t.Fatal("idempotent external mode change advanced state or event sequence")
	}

	if err := eng.SetPermissionMode(permission.ModeDefault); err != nil {
		t.Fatalf("leave Plan externally: %v", err)
	}
	inactive := eng.PlanState()
	thread = eng.RuntimeSnapshot().Threads["thread"]
	if inactive.Phase != PlanPhaseInactive ||
		inactive.Revision != 2 ||
		eng.PermissionMode() != permission.ModeDefault ||
		thread.Plan == nil ||
		thread.Plan.Phase != PlanPhaseInactive ||
		thread.Plan.Revision != inactive.Revision ||
		thread.LastSequence != sequence+1 {
		t.Fatalf(
			"inactive state/runtime = %#v / %#v",
			inactive,
			thread.Plan,
		)
	}
	if err := eng.RuntimeStateError(); err != nil {
		t.Fatalf("external Plan reducer error: %v", err)
	}
}

func TestP170ToolTransitionCommitsBeforeNextSerialCallAndPublishesAfterResult(
	t *testing.T,
) {
	var executions atomic.Int32
	registry := p170PlanRegistry(&executions)
	model := &p170ToolSequenceModel{first: []schema.ToolCall{
		p135c2ToolCall("enter-1", "EnterPlanMode", `{}`),
		p135c2ToolCall("enter-2", "EnterPlanMode", `{}`),
	}}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:    "session",
		ThreadID:     "thread",
		CWD:          t.TempDir(),
		ChatModel:    model,
		ToolRegistry: registry,
		MaxTurns:     3,
		CanUseTool: func(
			context.Context,
			string,
			map[string]any,
			*ToolUseContext,
		) (bool, string) {
			return true, ""
		},
	})
	defer eng.Close()

	events, _ := eng.SubmitMessage(context.Background(), "enter Plan once")
	collected := drainEngineEvents(t, events)
	var resultIndexes []int
	transitionIndex := -1
	var transition QueryEvent
	for index, event := range collected {
		switch event.Type {
		case EventToolResult:
			if event.ToolResultMessage != nil &&
				strings.HasPrefix(
					event.ToolResultMessage.ToolCallID,
					"enter-",
				) {
				resultIndexes = append(resultIndexes, index)
			}
		case EventPlanStateTransition:
			transitionIndex = index
			transition = event
		}
	}
	if len(resultIndexes) != 2 || transitionIndex < 0 {
		t.Fatalf("Plan result/transition events = %#v", collected)
	}
	if transitionIndex <= resultIndexes[0] {
		t.Fatalf(
			"Plan transition event index %d did not follow result %d",
			transitionIndex,
			resultIndexes[0],
		)
	}
	if executions.Load() != 1 {
		t.Fatalf(
			"repeated EnterPlanMode executions = %d, want 1",
			executions.Load(),
		)
	}
	second := collected[resultIndexes[1]].ToolResultMessage
	if second == nil ||
		second.Extra == nil ||
		second.Extra["is_error"] != true ||
		!strings.Contains(second.Content, "unavailable") {
		t.Fatalf("repeated Enter result = %#v", second)
	}
	if transition.PlanStateTransition == nil ||
		transition.PlanStateTransition.RequestID != "enter-1" ||
		transition.CausationID != "enter-1" ||
		transition.SessionID != "session" ||
		transition.ThreadID != "thread" ||
		transition.TurnID == "" ||
		transition.PlanStateTransition.Phase != PlanPhaseActive ||
		transition.PlanStateTransition.Revision != 1 {
		t.Fatalf("Plan transition identity = %#v", transition)
	}
	state := eng.PlanState()
	runtimePlan := eng.RuntimeSnapshot().Threads["thread"].Plan
	if state.Phase != PlanPhaseActive ||
		state.Revision != 1 ||
		eng.PermissionMode() != permission.ModePlan ||
		runtimePlan == nil ||
		runtimePlan.Phase != state.Phase ||
		runtimePlan.Revision != state.Revision {
		t.Fatalf("engine/runtime Plan state = %#v / %#v", state, runtimePlan)
	}
	if got := poolToolNames(eng.modelVisibleTools()); !slices.Contains(
		got,
		"ExitPlanMode",
	) || slices.Contains(got, "EnterPlanMode") {
		t.Fatalf("active model-visible Plan tools = %#v", got)
	}
	modelTools := model.toolSnapshots()
	if len(modelTools) < 2 ||
		!slices.Contains(modelTools[0], "EnterPlanMode") ||
		slices.Contains(modelTools[0], "ExitPlanMode") ||
		!slices.Contains(modelTools[1], "ExitPlanMode") ||
		slices.Contains(modelTools[1], "EnterPlanMode") {
		t.Fatalf("model tool refresh snapshots = %#v", modelTools)
	}
}

func TestP170ProjectGraphToolRoundConsumesEngineOwnedPlanTransition(
	t *testing.T,
) {
	registry := p170PlanRegistry(nil)
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:    "graph-session",
		ThreadID:     "graph-thread",
		CWD:          t.TempDir(),
		ToolRegistry: registry,
	})
	defer eng.Close()
	turnID := "graph-turn"
	if _, err := eng.beginPlanTurn(turnID); err != nil {
		t.Fatal(err)
	}
	defer eng.endPlanTurn(turnID)

	toolCtx := &ToolUseContext{
		SessionID: "graph-session",
		ThreadID:  "graph-thread",
		Options: &ToolUseOptions{
			PermissionMode: permission.ModeDefault,
		},
	}
	var eventMu sync.Mutex
	var recorded []QueryEvent
	record := func(event QueryEvent) bool {
		event = eng.decorateRuntimeEvent(turnID, event)
		eventMu.Lock()
		recorded = append(recorded, event)
		eventMu.Unlock()
		return true
	}
	runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
		prepare: func(
			_ context.Context,
			round projectGraphRound,
		) (projectGraphPreparedRound, error) {
			return projectGraphPreparedRound{Values: round.Values}, nil
		},
		model: func(
			_ context.Context,
			round projectGraphRound,
		) (projectGraphModelRound, error) {
			if round.Number == 1 {
				return projectGraphModelRound{
					Decision: projectGraphModelToolCalls,
					ToolCalls: []schema.ToolCall{
						p135c2ToolCall(
							"graph-enter",
							"EnterPlanMode",
							`{}`,
						),
					},
				}, nil
			}
			return projectGraphModelRound{
				Decision: projectGraphModelTerminal,
				Value:    "done",
			}, nil
		},
		tool: bindProjectGraphCanonicalToolRound(func(
			ctx context.Context,
			_ projectGraphRound,
		) (canonicalToolRoundInput, error) {
			return canonicalToolRoundInput{
				params: QueryParams{
					ToolRegistry: registry,
					CanUseTool: func(
						context.Context,
						string,
						map[string]any,
						*ToolUseContext,
					) (bool, string) {
						return true, ""
					},
					ToolExecutor: eng.toolExecutor,
					TransitionPermissionMode: func(
						current *ToolUseContext,
						mode permission.Mode,
						requestID string,
					) (*ToolUseContext, func(), error) {
						return eng.transitionPermissionModeForTurn(
							turnID,
							record,
							current,
							mode,
							requestID,
						)
					},
					repeatedToolGuard: newRepeatedToolCallGuard(),
				},
				toolUseContext:    toolCtx,
				cancellationChain: NewCancellationChain(ctx),
				yield: func(event QueryEvent) {
					record(event)
				},
			}, nil
		}),
	})

	result, err := runnable.Invoke(
		context.Background(),
		projectGraphKernelInput{RunID: "graph-plan"},
	)
	if err != nil {
		t.Fatalf("invoke Graph Plan transition: %v", err)
	}
	if result.Kind != projectGraphResultTerminal || result.Value != "done" {
		t.Fatalf("Graph result = %#v", result)
	}
	eventMu.Lock()
	events := append([]QueryEvent(nil), recorded...)
	eventMu.Unlock()
	toolIndex, transitionIndex := -1, -1
	for index, event := range events {
		if event.Type == EventToolResult &&
			event.ToolResultMessage != nil &&
			event.ToolResultMessage.ToolCallID == "graph-enter" {
			toolIndex = index
		}
		if event.Type == EventPlanStateTransition {
			transitionIndex = index
		}
	}
	if toolIndex < 0 ||
		transitionIndex <= toolIndex ||
		eng.PlanState().Phase != PlanPhaseActive ||
		eng.PermissionMode() != permission.ModePlan ||
		!toolCtx.PlanMode ||
		toolCtx.Options.PermissionMode != permission.ModePlan {
		t.Fatalf(
			"Graph Plan commit ordering/state = %d/%d %#v %#v",
			toolIndex,
			transitionIndex,
			eng.PlanState(),
			toolCtx,
		)
	}
	runtimePlan := eng.RuntimeSnapshot().Threads["graph-thread"].Plan
	if runtimePlan == nil ||
		runtimePlan.Phase != PlanPhaseActive ||
		runtimePlan.RequestID != "graph-enter" {
		t.Fatalf("Graph Plan runtime projection = %#v", runtimePlan)
	}
}

func TestP170ProjectGraphCancellationWinsNonCooperativePlanTransition(
	t *testing.T,
) {
	started := make(chan struct{})
	release := make(chan struct{})
	registry := p170PlanRegistry(nil)
	registry.Register(tools.ToolImpl{
		Info:                 &schema.ToolInfo{Name: "EnterPlanMode"},
		IsPlanModeTransition: true,
		ExecuteCtx: func(context.Context, string) (string, error) {
			close(started)
			<-release
			return "success after cancellation", nil
		},
	})
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:    "graph-cancel-session",
		ThreadID:     "graph-cancel-thread",
		CWD:          t.TempDir(),
		ToolRegistry: registry,
	})
	defer eng.Close()
	turnID := "graph-cancel-turn"
	if _, err := eng.beginPlanTurn(turnID); err != nil {
		t.Fatal(err)
	}
	defer eng.endPlanTurn(turnID)

	abortCtx, cancelAbort := context.WithCancel(context.Background())
	defer cancelAbort()
	controller := &AbortController{Ctx: abortCtx, Cancel: cancelAbort}
	toolCtx := &ToolUseContext{
		SessionID:       "graph-cancel-session",
		ThreadID:        "graph-cancel-thread",
		AbortController: controller,
		Options: &ToolUseOptions{
			PermissionMode: permission.ModeDefault,
		},
	}
	var eventMu sync.Mutex
	var events []QueryEvent
	record := func(event QueryEvent) {
		eventMu.Lock()
		events = append(events, event)
		eventMu.Unlock()
	}
	chain := NewCancellationChain(context.Background())
	type graphResult struct {
		result canonicalToolRoundResult
		err    error
	}
	enterCall := p135c2ToolCall(
		"graph-cancel-enter",
		"EnterPlanMode",
		`{}`,
	)
	resultCh := make(chan graphResult, 1)
	go func() {
		result, err := runCanonicalToolRound(
			context.Background(),
			canonicalToolRoundInput{
				params: QueryParams{
					ToolRegistry: registry,
					CanUseTool: func(
						context.Context,
						string,
						map[string]any,
						*ToolUseContext,
					) (bool, string) {
						return true, ""
					},
					ToolExecutor: eng.toolExecutor,
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
					repeatedToolGuard: newRepeatedToolCallGuard(),
				},
				toolCalls: []*schema.ToolCall{
					&enterCall,
				},
				toolUseContext:    toolCtx,
				cancellationChain: chain,
				yield:             record,
			},
		)
		resultCh <- graphResult{result: result, err: err}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("Graph Plan transition did not start")
	}
	cancelAbort()
	close(release)

	var invocation graphResult
	select {
	case invocation = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled Graph Plan transition did not settle")
	}
	if invocation.err != nil {
		t.Fatalf("cancelled Graph Plan transition: %v", invocation.err)
	}
	if invocation.result.decision.Kind != afterToolInterrupt ||
		len(invocation.result.toolResults) != 1 ||
		invocation.result.toolResults[0].Content != "Interrupted by user" {
		t.Fatalf("cancelled Graph Plan result = %#v", invocation.result)
	}
	eventMu.Lock()
	defer eventMu.Unlock()
	for _, event := range events {
		if event.Type == EventPlanStateTransition {
			t.Fatalf("cancelled Graph call emitted transition: %#v", event)
		}
	}
	if state := eng.PlanState(); state.Phase != PlanPhaseInactive ||
		state.Revision != 0 ||
		eng.PermissionMode() != permission.ModeDefault {
		t.Fatalf("cancelled Graph call changed Plan state: %#v", state)
	}
}

func TestP170DeniedToolAndInFlightExternalChangeDoNotMutatePlanState(
	t *testing.T,
) {
	t.Run("denied tool", func(t *testing.T) {
		registry := p170PlanRegistry(nil)
		eng := NewQueryEngine(QueryEngineConfig{
			SessionID: "denied-session",
			CWD:       t.TempDir(),
			ChatModel: &p170ToolSequenceModel{first: []schema.ToolCall{
				p135c2ToolCall("denied-enter", "EnterPlanMode", `{}`),
			}},
			ToolRegistry: registry,
			MaxTurns:     2,
			CanUseTool: func(
				context.Context,
				string,
				map[string]any,
				*ToolUseContext,
			) (bool, string) {
				return false, "fixture denied"
			},
		})
		defer eng.Close()
		events, _ := eng.SubmitMessage(context.Background(), "deny Plan")
		collected := drainEngineEvents(t, events)
		for _, event := range collected {
			if event.Type == EventPlanStateTransition {
				t.Fatalf("denied tool emitted Plan transition: %#v", event)
			}
		}
		if state := eng.PlanState(); state.Phase != PlanPhaseInactive ||
			state.Revision != 0 ||
			eng.PermissionMode() != permission.ModeDefault {
			t.Fatalf("denied tool changed Plan state: %#v", state)
		}
	})

	t.Run("failed execution", func(t *testing.T) {
		registry := p170PlanRegistry(nil)
		registry.Register(tools.ToolImpl{
			Info:                 &schema.ToolInfo{Name: "EnterPlanMode"},
			IsPlanModeTransition: true,
			ExecuteCtx: func(context.Context, string) (string, error) {
				return "", errors.New("fixture execution failed")
			},
		})
		eng := NewQueryEngine(QueryEngineConfig{
			SessionID: "failed-session",
			CWD:       t.TempDir(),
			ChatModel: &p170ToolSequenceModel{first: []schema.ToolCall{
				p135c2ToolCall("failed-enter", "EnterPlanMode", `{}`),
			}},
			ToolRegistry: registry,
			MaxTurns:     2,
			CanUseTool: func(
				context.Context,
				string,
				map[string]any,
				*ToolUseContext,
			) (bool, string) {
				return true, ""
			},
		})
		defer eng.Close()
		events, _ := eng.SubmitMessage(context.Background(), "fail Plan")
		collected := drainEngineEvents(t, events)
		for _, event := range collected {
			if event.Type == EventPlanStateTransition {
				t.Fatalf("failed tool emitted Plan transition: %#v", event)
			}
		}
		if state := eng.PlanState(); state.Phase != PlanPhaseInactive ||
			state.Revision != 0 {
			t.Fatalf("failed tool changed Plan state: %#v", state)
		}
	})

	t.Run("cancelled non-cooperative success", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		registry := p170PlanRegistry(nil)
		registry.Register(tools.ToolImpl{
			Info:                 &schema.ToolInfo{Name: "EnterPlanMode"},
			IsPlanModeTransition: true,
			ExecuteCtx: func(context.Context, string) (string, error) {
				close(started)
				<-release
				return "success after cancellation", nil
			},
		})
		ctx, cancel := context.WithCancel(context.Background())
		eng := NewQueryEngine(QueryEngineConfig{
			SessionID: "cancelled-session",
			CWD:       t.TempDir(),
			ChatModel: &p170ToolSequenceModel{first: []schema.ToolCall{
				p135c2ToolCall("cancelled-enter", "EnterPlanMode", `{}`),
			}},
			ToolRegistry: registry,
			MaxTurns:     2,
			CanUseTool: func(
				context.Context,
				string,
				map[string]any,
				*ToolUseContext,
			) (bool, string) {
				return true, ""
			},
		})
		defer eng.Close()
		events, _ := eng.SubmitMessage(ctx, "cancel Plan")
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("EnterPlanMode execution did not start")
		}
		eng.Interrupt()
		cancel()
		close(release)
		collected := drainEngineEvents(t, events)
		for _, event := range collected {
			if event.Type == EventPlanStateTransition {
				t.Fatalf("cancelled call emitted Plan transition: %#v", event)
			}
		}
		if state := eng.PlanState(); state.Phase != PlanPhaseInactive ||
			state.Revision != 0 {
			t.Fatalf("cancelled result changed Plan state: %#v", state)
		}
	})

	t.Run("external loses active turn boundary", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		cwd := t.TempDir()
		transcriptDir := t.TempDir()
		recorder := writeEngineSelectedSession(
			t,
			transcriptDir,
			"blocked-resume",
			"resume later",
		)
		eng := NewQueryEngine(QueryEngineConfig{
			SessionID:     "in-flight-session",
			CWD:           cwd,
			TranscriptDir: transcriptDir,
			ChatModel: &p170BlockingModel{
				started: started,
				release: release,
			},
			MaxTurns: 1,
		})
		defer eng.Close()
		events, _ := eng.SubmitMessage(ctx, "hold turn")
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("model turn did not start")
		}
		err := eng.SetPermissionMode(permission.ModePlan)
		if !errors.Is(err, ErrPlanTransitionInFlight) {
			t.Fatalf("in-flight external mode error = %v", err)
		}
		if _, resumeErr := eng.ResumeSessionInfo(
			context.Background(),
			session.SessionInfo{
				SessionID:      "blocked-resume",
				CWD:            cwd,
				TranscriptDir:  transcriptDir,
				TranscriptPath: recorder.Path(),
			},
		); !errors.Is(resumeErr, ErrPlanTransitionInFlight) {
			t.Fatalf("in-flight resume error = %v", resumeErr)
		}
		if state := eng.PlanState(); state.Phase != PlanPhaseInactive ||
			state.Revision != 0 ||
			eng.PermissionMode() != permission.ModeDefault {
			t.Fatalf("in-flight external change mutated state: %#v", state)
		}
		cancel()
		close(release)
		_ = drainEngineEvents(t, events)
	})
}

func TestP170RuntimeReplayReconstructsPlanProjectionWithoutDispatch(
	t *testing.T,
) {
	fixed := time.Date(2026, 7, 18, 17, 30, 0, 0, time.UTC)
	event := QueryEvent{
		RuntimeEventEnvelope: RuntimeEventEnvelope{
			SessionID:   "session",
			ThreadID:    "thread",
			TurnID:      "turn",
			Sequence:    1,
			Timestamp:   fixed,
			CausationID: "enter",
		},
		Type: EventPlanStateTransition,
		PlanStateTransition: &PlanStateTransitionEvent{
			FromPhase:        PlanPhaseInactive,
			Phase:            PlanPhaseActive,
			PermissionMode:   permission.ModePlan,
			PlanFileIdentity: "/tmp/plan.md",
			ReturnMode:       permission.ModeDefault,
			RequestID:        "enter",
			Revision:         1,
			Source:           string(planTransitionTool),
		},
	}
	store := NewRuntimeStateStore()
	if err := store.Replay([]QueryEvent{event}); err != nil {
		t.Fatalf("replay Plan event: %v", err)
	}
	plan := store.Snapshot("thread").Threads["thread"].Plan
	if plan == nil ||
		plan.Phase != PlanPhaseActive ||
		plan.PermissionMode != string(permission.ModePlan) ||
		plan.RequestID != "enter" ||
		plan.Revision != 1 ||
		plan.Sequence != 1 ||
		!plan.UpdatedAt.Equal(fixed) {
		t.Fatalf("replayed Plan projection = %#v", plan)
	}
}

func TestP170ResumeRebuildsEngineOwnedPlanSnapshotFromPersistedMode(
	t *testing.T,
) {
	cwd := t.TempDir()
	transcriptDir := t.TempDir()
	recorder := writeEngineSelectedSession(
		t,
		transcriptDir,
		"resumed-session",
		"resume Plan",
	)
	writeProjectGraphRootTestMetadata(
		t,
		recorder,
		&session.SessionMetadataFull{
			SessionID:      "resumed-session",
			ThreadID:       "resumed-thread",
			AgentID:        "resumed-agent",
			CWD:            cwd,
			PermissionMode: string(permission.ModePlan),
			CreatedAt:      time.Now().UTC(),
			UpdatedAt:      time.Now().UTC(),
			MessageCount:   2,
		},
	)

	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "initial-session",
		ThreadID:      "initial-thread",
		CWD:           cwd,
		TranscriptDir: transcriptDir,
	})
	defer eng.Close()
	if _, err := eng.ResumeSessionInfo(
		context.Background(),
		session.SessionInfo{
			SessionID:      "resumed-session",
			CWD:            cwd,
			TranscriptDir:  transcriptDir,
			TranscriptPath: recorder.Path(),
		},
	); err != nil {
		t.Fatalf("resume persisted Plan mode: %v", err)
	}
	state := eng.PlanState()
	if state.Phase != PlanPhaseActive ||
		state.Revision != 1 ||
		state.PlanFileIdentity != tools.GetPlanFilePath(
			"resumed-session",
			"resumed-agent",
		) ||
		eng.PermissionMode() != permission.ModePlan {
		t.Fatalf("resumed engine-owned Plan state = %#v", state)
	}
}

type p170ToolSequenceModel struct {
	calls         atomic.Int32
	first         []schema.ToolCall
	toolOptionsMu sync.Mutex
	toolOptions   [][]string
}

func (m *p170ToolSequenceModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *p170ToolSequenceModel) Stream(
	_ context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	if m.calls.Add(1) == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role:      schema.Assistant,
			ToolCalls: append([]schema.ToolCall(nil), m.first...),
		}}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "done",
	}}), nil
}

func (m *p170ToolSequenceModel) WithTools(
	toolInfos []*schema.ToolInfo,
) (model.ToolCallingChatModel, error) {
	m.toolOptionsMu.Lock()
	m.toolOptions = append(
		m.toolOptions,
		poolToolNames(toolInfos),
	)
	m.toolOptionsMu.Unlock()
	return m, nil
}

func (m *p170ToolSequenceModel) toolSnapshots() [][]string {
	m.toolOptionsMu.Lock()
	defer m.toolOptionsMu.Unlock()
	snapshots := make([][]string, 0, len(m.toolOptions))
	for _, tools := range m.toolOptions {
		snapshots = append(snapshots, append([]string(nil), tools...))
	}
	return snapshots
}

type p170BlockingModel struct {
	started chan struct{}
	release chan struct{}
}

func (m *p170BlockingModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *p170BlockingModel) Stream(
	ctx context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	select {
	case <-m.started:
	default:
		close(m.started)
	}
	select {
	case <-m.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "done",
	}}), nil
}

func p170PlanRegistry(executions *atomic.Int32) *tools.Registry {
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info:                 &schema.ToolInfo{Name: "EnterPlanMode"},
		IsPlanModeTransition: true,
		ExecuteCtx: func(context.Context, string) (string, error) {
			if executions != nil {
				executions.Add(1)
			}
			return "Plan mode entered.", nil
		},
	})
	registry.Register(tools.ToolImpl{
		Info:                 &schema.ToolInfo{Name: "ExitPlanMode"},
		NeedsPermissions:     true,
		IsPlanModeTransition: true,
		ExecuteCtx: func(context.Context, string) (string, error) {
			return "Plan mode exited.", nil
		},
	})
	return registry
}
