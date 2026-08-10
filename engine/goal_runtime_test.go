package engine

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

func TestP242aGoalLifecycleOrderingAndExcludedRootTime(t *testing.T) {
	now := time.Date(2026, 7, 29, 1, 0, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	budget := uint64(10_000)
	created, err := eng.goalService.create(goalCreateRequest{
		Objective:   "finish P24.2a",
		TokenBudget: &budget,
	})
	if err != nil {
		t.Fatal(err)
	}

	events := make(chan QueryEvent, 32)
	emitter := beginP242aGoalTurn(t, eng, events, "goal-turn-1", false, now)

	now = now.Add(2 * time.Second)
	finishChildWait := eng.goalService.beginForegroundChildWait("agent-1", 1)
	now = now.Add(5 * time.Second)
	finishChildWait()

	now = now.Add(2 * time.Second)
	if !emitter.Emit(QueryEvent{
		Type: EventPermissionRequest,
		PermissionRequest: &PermissionRequestEvent{
			ToolUseID: "permission-1",
			ToolName:  "Bash",
		},
	}) {
		t.Fatal("permission request was not emitted")
	}
	now = now.Add(5 * time.Second)
	if !emitter.Emit(QueryEvent{
		Type: EventPermissionResolved,
		PermissionResolved: &PermissionResolvedEvent{
			ToolUseID: "permission-1",
			Decision:  "allow",
		},
	}) {
		t.Fatal("permission resolution was not emitted")
	}
	now = now.Add(3 * time.Second)
	if !emitter.Emit(QueryEvent{
		Type:         EventTerminal,
		TerminalInfo: &Terminal{Reason: TerminalCompleted},
	}) {
		t.Fatal("terminal event was not emitted")
	}
	emitter.Close()
	eng.endPlanTurn("goal-turn-1")
	close(events)

	var emitted []QueryEvent
	for event := range events {
		emitted = append(emitted, event)
	}
	if len(emitted) == 0 ||
		emitted[len(emitted)-1].Type != EventTerminal {
		t.Fatalf("terminal is not the final published event: %#v", emitted)
	}
	terminalSequence := emitted[len(emitted)-1].Sequence

	snapshot := eng.RuntimeSnapshot()
	thread := snapshot.Threads[eng.ThreadID()]
	if thread.Goal == nil ||
		thread.Goal.GoalID != created.GoalID ||
		thread.Goal.RootActiveTimeMillis != 7_000 ||
		thread.Goal.LastTerminalSequence != terminalSequence ||
		thread.LastSequence != terminalSequence ||
		thread.LastTerminal == nil ||
		thread.LastTerminal.Sequence != terminalSequence {
		t.Fatalf("unexpected Goal runtime projection: %#v", thread)
	}
	if thread.Goal.Status != string(goalStatusActive) {
		t.Fatalf("Goal status = %q, want active", thread.Goal.Status)
	}
	if err := eng.RuntimeStateError(); err != nil {
		t.Fatalf("runtime reducer error: %v", err)
	}
	assertP241PersistedGoal(t, eng, func(record *session.PersistedGoalState) {
		if record.RootActiveTimeMillis != 7_000 ||
			record.LastTerminalSequence != terminalSequence {
			t.Fatalf("persisted Goal terminal facts = %#v", record)
		}
	})
}

func TestP242aPermissionResumeRetainsLogicalGoalTurn(t *testing.T) {
	now := time.Date(2026, 7, 29, 1, 30, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	budget := uint64(10_000)
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "resume one logical Goal turn",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}

	initialEvents := make(chan QueryEvent, 16)
	initial := beginP242aGoalTurn(
		t,
		eng,
		initialEvents,
		"goal-turn-1",
		false,
		now,
	)
	now = now.Add(2 * time.Second)
	if !initial.Emit(QueryEvent{
		Type: EventPermissionRequest,
		PermissionRequest: &PermissionRequestEvent{
			ToolUseID: "permission-1",
			ToolName:  "Bash",
		},
	}) {
		t.Fatal("permission request rejected")
	}
	if !initial.Emit(QueryEvent{
		Type:         EventTerminal,
		TerminalInfo: &Terminal{Reason: TerminalWaitingInput},
	}) {
		t.Fatal("waiting terminal rejected")
	}
	initial.Close()
	eng.endPlanTurn("goal-turn-1")
	close(initialEvents)
	if identity := eng.currentGoalExecutionIdentity(); identity == nil ||
		identity.GoalTurnID != "goal-turn-1" {
		t.Fatalf("waiting terminal lost logical Goal turn: %#v", identity)
	}

	now = now.Add(5 * time.Second)
	if _, err := eng.beginPlanTurn("permission-resume-query-turn"); err != nil {
		t.Fatal(err)
	}
	resumedEvents := make(chan QueryEvent, 16)
	resumed := newTurnEventEmitter(
		context.Background(),
		eng,
		resumedEvents,
		"permission-resume-query-turn",
	)
	resumed.BindGoal(eng.currentGoalExecutionIdentity())
	if !resumed.Emit(QueryEvent{
		Type: EventPermissionResolved,
		PermissionResolved: &PermissionResolvedEvent{
			ToolUseID: "permission-1",
			Decision:  "allow",
		},
	}) {
		t.Fatal("permission resolution rejected")
	}
	now = now.Add(3 * time.Second)
	if !resumed.Emit(QueryEvent{
		Type:         EventTerminal,
		TerminalInfo: &Terminal{Reason: TerminalCompleted},
	}) {
		t.Fatal("completed terminal rejected")
	}
	resumed.Close()
	eng.endPlanTurn("permission-resume-query-turn")
	close(resumedEvents)

	var emitted []QueryEvent
	for event := range resumedEvents {
		emitted = append(emitted, event)
	}
	if len(emitted) == 0 ||
		emitted[len(emitted)-1].Type != EventTerminal ||
		emitted[len(emitted)-1].TurnID != "permission-resume-query-turn" ||
		emitted[len(emitted)-1].GoalTurnID != "goal-turn-1" {
		t.Fatalf("resumed terminal identity/order = %#v", emitted)
	}
	state := eng.goalService.snapshot()
	if state.RootActiveTimeMillis != 5_000 ||
		state.LastGoalTurnID != "goal-turn-1" ||
		eng.currentGoalExecutionIdentity() != nil {
		t.Fatalf("resumed logical Goal turn state = %#v", state)
	}
}

func TestP242aProjectGraphPermissionResumeKeepsGoalIdentityInProduction(t *testing.T) {
	now := time.Date(2026, 7, 29, 1, 40, 0, 0, time.UTC)
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{Name: "GraphWrite"},
		ExecuteCtx: func(context.Context, string) (string, error) {
			return "ok", nil
		},
	})
	model := &canonicalScriptModel{
		responses: []canonicalModelResponse{{
			chunks: []*schema.Message{{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "goal-write-1",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "GraphWrite",
						Arguments: `{}`,
					},
				}},
				ResponseMeta: &schema.ResponseMeta{
					FinishReason: "tool_calls",
					Usage:        &schema.TokenUsage{},
				},
			}},
		}, {
			chunks: []*schema.Message{{
				Role:         schema.Assistant,
				Content:      "done",
				ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{}},
			}},
		}},
	}
	config := projectGraphEngineConfig(
		t,
		t.TempDir(),
		"goal-graph-hitl",
		model,
		registry,
		&tools.ToolSelection{Names: []string{"GraphWrite"}},
	)
	config.Clock = func() time.Time { return now }
	config.PermissionMode = permission.ModeDefault
	config.PermissionPrompt = func(
		context.Context,
		PermissionPromptRequest,
	) PermissionInteractionResult {
		t.Fatal("ProjectGraph must not call the blocking permission adapter")
		return PermissionInteractionResult{Decision: PermissionDeny}
	}
	eng := NewQueryEngine(config)
	t.Cleanup(eng.Close)
	budget := uint64(10_000)
	created, err := eng.goalService.create(goalCreateRequest{
		Objective:   "retain Goal identity across Graph permission resume",
		TokenBudget: &budget,
	})
	if err != nil {
		t.Fatal(err)
	}

	initialEvents, _ := eng.SubmitMessage(
		context.Background(),
		"write after approval",
	)
	var initial []QueryEvent
	for event := range initialEvents {
		initial = append(initial, event)
	}
	if len(initial) == 0 ||
		initial[len(initial)-1].Type != EventTerminal ||
		initial[len(initial)-1].TerminalInfo == nil ||
		initial[len(initial)-1].TerminalInfo.Reason != TerminalWaitingInput {
		t.Fatalf("initial Goal Graph events = %#v", initial)
	}
	goalTurnID := ""
	for _, event := range initial {
		if event.Type == EventGoalLifecycle &&
			event.GoalLifecycle != nil &&
			event.GoalLifecycle.Phase == GoalLifecycleTurnStarted {
			goalTurnID = event.GoalTurnID
		}
	}
	if goalTurnID == "" {
		t.Fatalf("initial Goal turn identity missing: %#v", initial)
	}
	if identity := eng.currentGoalExecutionIdentity(); identity == nil ||
		identity.GoalTurnID != goalTurnID {
		t.Fatalf("waiting Graph Goal identity = %#v", identity)
	}
	if !eng.ResolvePermissionInteraction(
		"goal-write-1",
		PermissionInteractionResult{Decision: PermissionAllowOnce},
	) {
		t.Fatal("Goal Graph permission decision was not accepted")
	}
	item := mustClaimGraphDecision(t, eng)
	now = now.Add(5 * time.Second)
	resumedEvents, _ := eng.SubmitRuntimeItem(context.Background(), item)
	var resumed []QueryEvent
	for event := range resumedEvents {
		resumed = append(resumed, event)
		if event.GoalID != created.GoalID ||
			event.GoalObjectiveRevision != created.ObjectiveRevision ||
			event.GoalTurnID != goalTurnID {
			t.Fatalf("resumed Graph event lost Goal identity: %#v", event)
		}
	}
	if len(resumed) == 0 ||
		resumed[len(resumed)-1].Type != EventTerminal ||
		resumed[len(resumed)-1].TerminalInfo == nil ||
		resumed[len(resumed)-1].TerminalInfo.Reason != TerminalCompleted {
		t.Fatalf("resumed Goal Graph events = %#v", resumed)
	}
	state := eng.goalService.snapshot()
	if state.LastGoalTurnID != goalTurnID ||
		state.LastTerminalSequence != resumed[len(resumed)-1].Sequence ||
		state.UsageLedgerRevision != 2 ||
		state.TokensUsed != 0 ||
		state.PendingUsageAdmission != nil ||
		eng.currentGoalExecutionIdentity() != nil {
		t.Fatalf("completed Goal Graph state = %#v", state)
	}
	loaded, err := eng.transcript.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.GoalUsageRecords) != 2 {
		t.Fatalf(
			"Goal Graph provider usage records = %#v",
			loaded.GoalUsageRecords,
		)
	}
}

func TestP242aTerminalCheckpointFailureIsVisibleAndReleasesTurn(t *testing.T) {
	now := time.Date(2026, 7, 29, 1, 45, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	budget := uint64(10_000)
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "fail closed on terminal persistence",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}
	events := make(chan QueryEvent, 16)
	emitter := beginP242aGoalTurn(
		t,
		eng,
		events,
		"goal-turn-failing-terminal",
		false,
		now,
	)
	before := eng.goalService.snapshot()
	path := eng.transcript.Path()
	if err := eng.transcript.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Second)
	if !emitter.Emit(QueryEvent{
		Type:         EventTerminal,
		TerminalInfo: &Terminal{Reason: TerminalCompleted},
	}) {
		t.Fatal("persistence failure terminal was not delivered")
	}
	emitter.Close()
	eng.endPlanTurn("goal-turn-failing-terminal")
	close(events)

	var emitted []QueryEvent
	for event := range events {
		emitted = append(emitted, event)
	}
	if len(emitted) == 0 ||
		emitted[len(emitted)-1].Type != EventTerminal ||
		emitted[len(emitted)-1].TerminalInfo == nil ||
		emitted[len(emitted)-1].TerminalInfo.Reason != TerminalPersistenceError ||
		emitted[len(emitted)-1].TerminalInfo.Err == nil {
		t.Fatalf("terminal persistence failure = %#v", emitted)
	}
	after := eng.goalService.snapshot()
	if after.Revision != before.Revision ||
		after.LastTerminalSequence != before.LastTerminalSequence ||
		eng.currentGoalExecutionIdentity() != nil {
		t.Fatalf("failed terminal mutated or retained Goal turn: before=%#v after=%#v", before, after)
	}
}

func TestP242aGoalEventsReplayDeterministicallyAndRejectStaleRevision(t *testing.T) {
	base := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
	created := GoalSnapshot{
		GoalID:            "goal-1",
		Objective:         "ship projection",
		ObjectiveRevision: 1,
		Status:            string(goalStatusActive),
		Revision:          1,
		TokenBudget:       uint64Pointer(1_000),
		CreatedAt:         base,
		UpdatedAt:         base,
		Available:         true,
	}
	started := cloneGoalSnapshot(created)
	started.Revision = 2
	started.LastGoalTurnID = "goal-turn-1"
	started.UpdatedAt = base.Add(time.Second)
	finished := cloneGoalSnapshot(started)
	finished.Revision = 3
	finished.RootActiveTimeMillis = 2_000
	finished.LastTerminalSequence = 4
	finished.UpdatedAt = base.Add(3 * time.Second)

	events := []QueryEvent{
		p242aRuntimeGoalEvent(1, "external-goal", "", base, GoalLifecycleCreated, created),
		p242aRuntimeGoalEvent(2, "goal-turn-1", "goal-turn-1", base.Add(time.Second), GoalLifecycleTurnStarted, started),
		p242aRuntimeGoalEvent(3, "goal-turn-1", "goal-turn-1", base.Add(3*time.Second), GoalLifecycleTurnFinished, finished),
		{
			RuntimeEventEnvelope: RuntimeEventEnvelope{
				SessionID: "root", ThreadID: "root", TurnID: "goal-turn-1",
				Sequence: 4, Timestamp: base.Add(3 * time.Second),
				GoalID: "goal-1", GoalObjectiveRevision: 1,
				GoalRootSessionID: "root", GoalRootThreadID: "root",
				GoalTurnID: "goal-turn-1",
			},
			Type:         EventTerminal,
			TerminalInfo: &Terminal{Reason: TerminalCompleted},
		},
	}

	live := NewRuntimeStateStore()
	for _, event := range events {
		if err := live.Apply(event); err != nil {
			t.Fatalf("live apply: %v", err)
		}
	}
	replayed := NewRuntimeStateStore()
	if err := replayed.Replay(events); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if left, right := live.Snapshot("root"), replayed.Snapshot("root"); !reflect.DeepEqual(left, right) {
		t.Fatalf("live/replay mismatch:\nlive=%#v\nreplay=%#v", left, right)
	}

	stale := p242aRuntimeGoalEvent(
		5,
		"goal-turn-2",
		"goal-turn-2",
		base.Add(4*time.Second),
		GoalLifecycleTurnStarted,
		finished,
	)
	if err := live.Apply(stale); err == nil {
		t.Fatal("stale same-revision Goal event was accepted")
	}
	if got := live.Snapshot("root").Threads["root"].Goal; got == nil ||
		got.Revision != finished.Revision {
		t.Fatalf("stale event mutated projection: %#v", got)
	}
}

func TestP242aBlockerGuardRequiresThreeDistinctGoalTurns(t *testing.T) {
	now := time.Date(2026, 7, 29, 3, 0, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	budget := uint64(10_000)
	created, err := eng.goalService.create(goalCreateRequest{
		Objective:   "resolve blocker",
		TokenBudget: &budget,
	})
	if err != nil {
		t.Fatal(err)
	}

	for turn := 1; turn <= 3; turn++ {
		turnID := "blocker-turn-" + string(rune('0'+turn))
		events := make(chan QueryEvent, 16)
		emitter := beginP242aGoalTurn(t, eng, events, turnID, false, now)
		state, reportErr := eng.goalService.reportBlocker(
			goalBlockerRequest{
				GoalID:            created.GoalID,
				ObjectiveRevision: created.ObjectiveRevision,
				TurnID:            turnID,
				Reason:            "waiting for the same external dependency",
				BlockerKey:        "dependency:release",
			},
			now,
		)
		if reportErr != nil {
			t.Fatal(reportErr)
		}
		if turn < 3 {
			revision := state.Revision
			duplicate, duplicateErr := eng.goalService.reportBlocker(
				goalBlockerRequest{
					GoalID:            created.GoalID,
					ObjectiveRevision: created.ObjectiveRevision,
					TurnID:            turnID,
					Reason:            "duplicate report",
					BlockerKey:        "dependency:release",
				},
				now,
			)
			if duplicateErr != nil {
				t.Fatal(duplicateErr)
			}
			if duplicate.Revision != revision {
				t.Fatalf("duplicate same-turn blocker advanced revision: %d -> %d", revision, duplicate.Revision)
			}
		}
		now = now.Add(time.Second)
		if !emitter.Emit(QueryEvent{
			Type:         EventTerminal,
			TerminalInfo: &Terminal{Reason: TerminalCompleted},
		}) {
			t.Fatalf("turn %d terminal rejected", turn)
		}
		emitter.Close()
		eng.endPlanTurn(turnID)
		close(events)
	}

	state := eng.goalService.snapshot()
	if state.Status != goalStatusBlocked ||
		state.BlockerKey != "dependency:release" ||
		len(state.BlockerTurnIDs) != 3 {
		t.Fatalf("blocker guard state = %#v", state)
	}
}

func TestP242aBlockerEvidenceResetsOnResumeKeyChangeAndSteering(t *testing.T) {
	now := time.Date(2026, 7, 29, 3, 30, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	budget := uint64(10_000)
	created, err := eng.goalService.create(goalCreateRequest{
		Objective:   "reset stale blocker evidence",
		TokenBudget: &budget,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, turnID := range []string{"blocker-a-1", "blocker-a-2"} {
		runP242aBlockerTurn(
			t,
			eng,
			created,
			turnID,
			"dependency:a",
			false,
			&now,
		)
	}
	if _, err := eng.goalService.pause(); err != nil {
		t.Fatal(err)
	}
	resumed, err := eng.goalService.resume()
	if err != nil {
		t.Fatal(err)
	}
	if resumed.BlockerKey != "" || len(resumed.BlockerTurnIDs) != 0 {
		t.Fatalf("resume retained blocker evidence: %#v", resumed)
	}

	for _, turnID := range []string{"blocker-a-3", "blocker-a-4"} {
		runP242aBlockerTurn(
			t,
			eng,
			created,
			turnID,
			"dependency:a",
			false,
			&now,
		)
	}
	changed := runP242aBlockerTurn(
		t,
		eng,
		created,
		"blocker-b-1",
		"dependency:b",
		false,
		&now,
	)
	if changed.Status != goalStatusActive ||
		changed.BlockerKey != "dependency:b" ||
		!reflect.DeepEqual(changed.BlockerTurnIDs, []string{"blocker-b-1"}) {
		t.Fatalf("key change did not reset streak: %#v", changed)
	}

	events := make(chan QueryEvent, 16)
	emitter := beginP242aGoalTurn(
		t,
		eng,
		events,
		"steered-blocker-turn",
		true,
		now,
	)
	steered := eng.goalService.snapshot()
	if steered.BlockerKey != "" || len(steered.BlockerTurnIDs) != 0 {
		t.Fatalf("user steering retained blocker evidence: %#v", steered)
	}
	beforeRevision := steered.Revision
	for _, request := range []goalBlockerRequest{
		{
			GoalID: created.GoalID, ObjectiveRevision: created.ObjectiveRevision,
			TurnID: "steered-blocker-turn", Reason: "invalid key",
			BlockerKey: "not valid",
		},
		{
			GoalID: created.GoalID, ObjectiveRevision: created.ObjectiveRevision,
			TurnID: "steered-blocker-turn", Reason: string([]byte{0xff}),
			BlockerKey: "dependency:b",
		},
		{
			GoalID: created.GoalID, ObjectiveRevision: created.ObjectiveRevision,
			TurnID: "stale-blocker-turn", Reason: "stale turn",
			BlockerKey: "dependency:b",
		},
	} {
		if _, reportErr := eng.goalService.reportBlocker(request, now); reportErr == nil {
			t.Fatalf("invalid blocker request was accepted: %#v", request)
		}
		if state := eng.goalService.snapshot(); state.Revision != beforeRevision ||
			state.BlockerKey != "" ||
			len(state.BlockerTurnIDs) != 0 {
			t.Fatalf("rejected blocker mutated Goal: %#v", state)
		}
	}
	accepted, err := eng.goalService.reportBlocker(goalBlockerRequest{
		GoalID:            created.GoalID,
		ObjectiveRevision: created.ObjectiveRevision,
		TurnID:            "steered-blocker-turn",
		Reason:            "same dependency after steering",
		BlockerKey:        "DEPENDENCY:B",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != goalStatusActive ||
		accepted.BlockerKey != "dependency:b" ||
		!reflect.DeepEqual(
			accepted.BlockerTurnIDs,
			[]string{"steered-blocker-turn"},
		) {
		t.Fatalf("post-steering blocker evidence = %#v", accepted)
	}
	now = now.Add(time.Second)
	if !emitter.Emit(QueryEvent{
		Type:         EventTerminal,
		TerminalInfo: &Terminal{Reason: TerminalCompleted},
	}) {
		t.Fatal("steered blocker terminal rejected")
	}
	emitter.Close()
	eng.endPlanTurn("steered-blocker-turn")
	close(events)
}

func TestP242aCompletionIntentIsExactAndRemainsPendingWithoutAccounting(t *testing.T) {
	now := time.Date(2026, 7, 29, 4, 0, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	budget := uint64(10_000)
	created, err := eng.goalService.create(goalCreateRequest{
		Objective:   "finish safely",
		TokenBudget: &budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan QueryEvent, 16)
	emitter := beginP242aGoalTurn(t, eng, events, "complete-turn-1", false, now)

	before := eng.goalService.snapshot()
	if _, err := eng.goalService.requestCompletion(goalCompletionRequest{
		GoalID:            created.GoalID,
		ObjectiveRevision: created.ObjectiveRevision + 1,
		TurnID:            "complete-turn-1",
	}, now); err == nil {
		t.Fatal("stale objective revision was accepted")
	}
	if after := eng.goalService.snapshot(); after.Revision != before.Revision {
		t.Fatalf("rejected completion mutated revision: %d -> %d", before.Revision, after.Revision)
	}
	pending, err := eng.goalService.requestCompletion(goalCompletionRequest{
		GoalID:            created.GoalID,
		ObjectiveRevision: created.ObjectiveRevision,
		TurnID:            "complete-turn-1",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if pending.PendingCompleteTurnID != "complete-turn-1" {
		t.Fatalf("pending completion = %#v", pending)
	}
	now = now.Add(time.Second)
	if !emitter.Emit(QueryEvent{
		Type:         EventTerminal,
		TerminalInfo: &Terminal{Reason: TerminalCompleted},
	}) {
		t.Fatal("terminal rejected")
	}
	emitter.Close()
	eng.endPlanTurn("complete-turn-1")
	close(events)

	after := eng.goalService.snapshot()
	if after.Status != goalStatusActive ||
		after.PendingCompleteTurnID != "complete-turn-1" ||
		after.PendingCompleteObjectiveRevision != created.ObjectiveRevision {
		t.Fatalf("completion intent was incorrectly committed or cleared: %#v", after)
	}
}

func TestP242aChildGenerationCarriesGoalIdentityWithoutMutationAuthority(t *testing.T) {
	now := time.Date(2026, 7, 29, 5, 0, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	budget := uint64(10_000)
	created, err := eng.goalService.create(goalCreateRequest{
		Objective:   "delegate exact work",
		TokenBudget: &budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	rootEvents := make(chan QueryEvent, 16)
	rootEmitter := beginP242aGoalTurn(t, eng, rootEvents, "root-goal-turn", false, now)

	launch := tools.AgentLaunchSnapshot{
		Phase: "launched", AgentID: "agent-1", SessionID: "child-session",
		ThreadID: "child-thread", ParentSessionID: eng.SessionID(),
		ParentThreadID: eng.ThreadID(), ParentToolUseID: "agent-tool-1",
		Status: "running", Generation: 1, StartedAt: now,
	}
	subExec := NewSubAgentExecutor(nil, nil, eng.GetCWD())
	subExec.RuntimeState = eng.runtimeState
	subExec.goalBindingSnapshot = eng.currentGoalExecutionIdentity
	if err := subExec.recordAgentLifecycle(launch); err != nil {
		t.Fatal(err)
	}
	binding := subExec.frozenGoalBinding("agent-1", 1)
	if binding == nil ||
		binding.GoalID != created.GoalID ||
		binding.ObjectiveRevision != created.ObjectiveRevision ||
		binding.GoalTurnID != "root-goal-turn" {
		t.Fatalf("frozen child Goal binding = %#v", binding)
	}
	agent := eng.RuntimeSnapshot().Agents["agent-1"]
	if agent.Generation != 1 ||
		agent.GoalID != created.GoalID ||
		agent.GoalObjectiveRevision != created.ObjectiveRevision ||
		agent.GoalRootSessionID != eng.SessionID() ||
		agent.GoalRootThreadID != eng.ThreadID() ||
		agent.GoalTurnID != "root-goal-turn" {
		t.Fatalf("Goal-bound Agent projection = %#v", agent)
	}

	child := NewQueryEngine(QueryEngineConfig{
		SessionID: "child-session", ThreadID: "child-thread",
		AgentID: "agent-1", AgentGeneration: 1,
		ParentSessionID: eng.SessionID(), ParentThreadID: eng.ThreadID(),
		ParentToolUseID: "agent-tool-1",
		CWD:             t.TempDir(), TranscriptDir: t.TempDir(),
		Clock:        func() time.Time { return now },
		RuntimeState: eng.runtimeState,
		goalBinding:  cloneGoalExecutionIdentity(binding),
	})
	t.Cleanup(child.Close)
	childEvents := make(chan QueryEvent, 4)
	childEmitter := newTurnEventEmitter(
		context.Background(),
		child,
		childEvents,
		"child-turn-1",
	)
	if !childEmitter.Emit(QueryEvent{Type: EventStreamRequestStart}) {
		t.Fatal("Goal-bound child event rejected")
	}
	childEvent := <-childEvents
	if childEvent.AgentGeneration != 1 ||
		childEvent.GoalID != created.GoalID ||
		childEvent.GoalTurnID != "root-goal-turn" {
		t.Fatalf("child event Goal identity = %#v", childEvent.RuntimeEventEnvelope)
	}
	childEmitter.Close()
	if err := child.persistSessionCheckpoint(""); err != nil {
		t.Fatal(err)
	}
	loaded, err := child.transcript.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	metadata := session.ReadSessionMetadataFull(loaded)
	if metadata == nil ||
		metadata.GoalState != nil ||
		metadata.GoalBinding == nil ||
		metadata.GoalBinding.GoalID != created.GoalID ||
		metadata.GoalBinding.ObjectiveRevision != created.ObjectiveRevision ||
		metadata.GoalBinding.GoalTurnID != "root-goal-turn" {
		t.Fatalf("persisted child Goal binding = %#v", metadata)
	}
	if _, err := child.goalService.create(goalCreateRequest{
		Objective:   "forged child Goal",
		TokenBudget: &budget,
	}); err == nil {
		t.Fatal("Goal-bound child acquired root mutation authority")
	}
	if root := eng.goalService.snapshot(); root.GoalID != created.GoalID ||
		root.Objective != created.Objective {
		t.Fatalf("child mutated root Goal: %#v", root)
	}

	failedLaunch := tools.AgentLaunchSnapshot{
		Phase: "launched", AgentID: "agent-failed",
		SessionID: "failed-child-session", ThreadID: "failed-child-thread",
		ParentSessionID: eng.SessionID(), ParentThreadID: eng.ThreadID(),
		ParentToolUseID: "agent-tool-failed", Status: "running",
		Generation: 1, StartedAt: now,
	}
	if err := subExec.recordAgentLifecycle(failedLaunch); err != nil {
		t.Fatal(err)
	}
	if subExec.frozenGoalBinding("agent-failed", 1) == nil {
		t.Fatal("launch did not freeze Goal binding")
	}
	failedLaunch.Phase = "launch_failed"
	failedLaunch.Status = "failed"
	failedLaunch.Error = "admission rejected"
	if err := subExec.recordAgentLifecycle(failedLaunch); err != nil {
		t.Fatal(err)
	}
	if binding := subExec.frozenGoalBinding("agent-failed", 1); binding != nil {
		t.Fatalf("failed launch retained Goal binding: %#v", binding)
	}

	now = now.Add(time.Second)
	if !rootEmitter.Emit(QueryEvent{
		Type:         EventTerminal,
		TerminalInfo: &Terminal{Reason: TerminalCompleted},
	}) {
		t.Fatal("root terminal rejected")
	}
	rootEmitter.Close()
	eng.endPlanTurn("root-goal-turn")
	close(rootEvents)
	subExec.releaseGoalBinding("agent-1", 1)
}

func TestP242aPersistedGoalBindingRestoresOnlyForExactChild(t *testing.T) {
	record := &session.PersistedGoalBinding{
		Version:           session.PersistedGoalBindingVersion,
		GoalID:            "goal-1",
		ObjectiveRevision: 2,
		RootSessionID:     "root-session",
		RootThreadID:      "root-thread",
		GoalTurnID:        "goal-turn-1",
	}
	restored, warnings := restoreGoalBinding(record, "child-agent")
	if restored == nil ||
		restored.GoalID != record.GoalID ||
		restored.ObjectiveRevision != record.ObjectiveRevision ||
		restored.RootSessionID != record.RootSessionID ||
		restored.RootThreadID != record.RootThreadID ||
		restored.GoalTurnID != record.GoalTurnID ||
		len(warnings) != 0 {
		t.Fatalf("restored child Goal binding = %#v warnings=%v", restored, warnings)
	}

	if root, rootWarnings := restoreGoalBinding(record, ""); root != nil ||
		len(rootWarnings) != 1 {
		t.Fatalf("root accepted child Goal binding = %#v warnings=%v", root, rootWarnings)
	}
	invalid := *record
	invalid.GoalTurnID = ""
	if got, invalidWarnings := restoreGoalBinding(
		&invalid,
		"child-agent",
	); got != nil || len(invalidWarnings) != 1 {
		t.Fatalf("invalid child Goal binding = %#v warnings=%v", got, invalidWarnings)
	}
	unsupported := *record
	unsupported.Version++
	if got, unsupportedWarnings := restoreGoalBinding(
		&unsupported,
		"child-agent",
	); got != nil || len(unsupportedWarnings) != 1 {
		t.Fatalf("unsupported child Goal binding = %#v warnings=%v", got, unsupportedWarnings)
	}
}

func beginP242aGoalTurn(
	t *testing.T,
	eng *QueryEngine,
	events chan QueryEvent,
	turnID string,
	userSteering bool,
	now time.Time,
) *turnEventEmitter {
	t.Helper()
	if _, err := eng.beginPlanTurn(turnID); err != nil {
		t.Fatal(err)
	}
	emitter := newTurnEventEmitter(context.Background(), eng, events, turnID)
	event, identity, err := eng.goalService.beginTurn(turnID, userSteering, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if identity == nil {
		t.Fatal("active Goal turn did not receive identity")
	}
	emitter.BindGoal(identity)
	if !emitter.Emit(event) {
		t.Fatal("Goal turn-start event rejected")
	}
	return emitter
}

func runP242aBlockerTurn(
	t *testing.T,
	eng *QueryEngine,
	goal *goalState,
	turnID string,
	blockerKey string,
	userSteering bool,
	now *time.Time,
) *goalState {
	t.Helper()
	events := make(chan QueryEvent, 16)
	emitter := beginP242aGoalTurn(
		t,
		eng,
		events,
		turnID,
		userSteering,
		*now,
	)
	state, err := eng.goalService.reportBlocker(goalBlockerRequest{
		GoalID:            goal.GoalID,
		ObjectiveRevision: goal.ObjectiveRevision,
		TurnID:            turnID,
		Reason:            "waiting for " + blockerKey,
		BlockerKey:        blockerKey,
	}, *now)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	if !emitter.Emit(QueryEvent{
		Type:         EventTerminal,
		TerminalInfo: &Terminal{Reason: TerminalCompleted},
	}) {
		t.Fatalf("blocker turn %q terminal rejected", turnID)
	}
	emitter.Close()
	eng.endPlanTurn(turnID)
	close(events)
	return state
}

func p242aRuntimeGoalEvent(
	sequence uint64,
	turnID string,
	goalTurnID string,
	timestamp time.Time,
	phase GoalLifecyclePhase,
	goal GoalSnapshot,
) QueryEvent {
	return QueryEvent{
		RuntimeEventEnvelope: RuntimeEventEnvelope{
			SessionID: "root", ThreadID: "root", TurnID: turnID,
			Sequence: sequence, Timestamp: timestamp,
			GoalID: goal.GoalID, GoalObjectiveRevision: goal.ObjectiveRevision,
			GoalRootSessionID: "root", GoalRootThreadID: "root",
			GoalTurnID: goalTurnID,
		},
		Type: EventGoalLifecycle,
		GoalLifecycle: &GoalLifecycleEvent{
			Phase: phase,
			Goal:  cloneGoalSnapshot(goal),
		},
	}
}

func uint64Pointer(value uint64) *uint64 {
	return &value
}
