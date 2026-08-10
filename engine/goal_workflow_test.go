package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
)

func TestP244GoalCommandControlsDurableState(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		CommandEntrypoint: commands.EntrypointTUI,
		GoalCapability:    &GoalCapabilityConfig{Enabled: true},
		ToolRegistry:      registry,
	})

	result := submitP244Command(t, eng, "/goal finish every migration slice")
	if result.Status != CommandResultSucceeded ||
		result.Action != commands.ActionGoal ||
		result.FollowUpPrompt != goalInitialPrompt {
		t.Fatalf("create Goal result = %#v", result)
	}
	goal, ok := eng.GoalSnapshot()
	if !ok ||
		goal.Status != string(goalStatusActive) ||
		goal.StatusReasonCode != "" ||
		goal.TokenBudget != nil {
		t.Fatalf("created Goal draft = %#v, command=%#v", goal, result)
	}

	if result = submitP244Command(t, eng, "/goal budget 10000"); result.Status != CommandResultSucceeded {
		t.Fatalf("budget Goal result = %#v", result)
	}
	result = submitP244Command(t, eng, "/goal resume")
	if result.Status != CommandResultSucceeded ||
		result.FollowUpPrompt != goalInitialPrompt {
		t.Fatalf("resume Goal result = %#v", result)
	}
	goal, _ = eng.GoalSnapshot()
	if goal.Status != string(goalStatusActive) ||
		goal.TokenBudget == nil ||
		*goal.TokenBudget != 10_000 {
		t.Fatalf("resumed Goal = %#v", goal)
	}

	result = submitP244Command(t, eng, "/goal replace unfinished work")
	if result.Status != CommandResultFailed ||
		!strings.Contains(result.Output, "unfinished goal already exists") {
		t.Fatalf("unfinished Goal replacement = %#v", result)
	}
	result = submitP244Command(t, eng, "/goal edit finish the verified migration ledger")
	if result.Status != CommandResultSucceeded {
		t.Fatalf("edit Goal result = %#v", result)
	}
	goal, _ = eng.GoalSnapshot()
	if goal.Objective != "finish the verified migration ledger" {
		t.Fatalf("edited Goal = %#v", goal)
	}
	result = submitP244Command(t, eng, "/goal pause")
	if result.Status != CommandResultSucceeded {
		t.Fatalf("pause Goal result = %#v", result)
	}
	goal, _ = eng.GoalSnapshot()
	if goal.Status != string(goalStatusPaused) ||
		goal.StatusReasonCode != goalReasonUserPaused {
		t.Fatalf("paused Goal = %#v", goal)
	}
	result = submitP244Command(t, eng, "/goal resume")
	if result.Status != CommandResultSucceeded ||
		result.FollowUpPrompt != goalInitialPrompt {
		t.Fatalf("second resume Goal result = %#v", result)
	}
	result = submitP244Command(t, eng, "/goal clear")
	if result.Status != CommandResultSucceeded {
		t.Fatalf("clear Goal result = %#v", result)
	}
	if _, ok := eng.GoalSnapshot(); ok {
		t.Fatal("clear Goal command retained durable state")
	}
}

func TestP49GoalCommandCreateWithoutBudgetRequestsOneInitialTurn(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		CommandEntrypoint: commands.EntrypointTUI,
		GoalCapability:    &GoalCapabilityConfig{Enabled: true},
		ToolRegistry:      registry,
	})
	result := submitP244Command(t, eng, "/goal run immediately without a cap")
	if result.Status != CommandResultSucceeded ||
		result.FollowUpPrompt != goalInitialPrompt ||
		!strings.Contains(result.Output, "unbounded") {
		t.Fatalf("unbudgeted Goal create result = %#v", result)
	}
	goal, ok := eng.GoalSnapshot()
	if !ok || goal.Status != string(goalStatusActive) || goal.TokenBudget != nil {
		t.Fatalf("unbudgeted Goal command state = %#v", goal)
	}
}

func TestP244GoalToolsAreDynamicRootTUIAuthorities(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	enabled := newP241GoalEngine(t, QueryEngineConfig{
		CommandEntrypoint: commands.EntrypointTUI,
		GoalCapability:    &GoalCapabilityConfig{Enabled: true},
		ToolRegistry:      registry,
	})
	if got := poolToolNames(enabled.modelVisibleTools()); slices.Contains(got, tools.GetGoalToolName) ||
		slices.Contains(got, tools.UpdateGoalToolName) {
		t.Fatalf("idle runtime leaked Goal tools = %v", got)
	}
	budget := uint64(10_000)
	if _, err := enabled.goalService.create(goalCreateRequest{
		Objective:   "inspect an active Goal turn",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 6, 20, 0, 0, time.UTC)
	events := make(chan QueryEvent, 16)
	emitter := beginP242aGoalTurn(
		t,
		enabled,
		events,
		"p24-4-visible-tools",
		true,
		now,
	)
	names := poolToolNames(enabled.modelVisibleTools())
	if !slices.Contains(names, tools.GetGoalToolName) ||
		!slices.Contains(names, tools.UpdateGoalToolName) {
		t.Fatalf("enabled root TUI Goal tools = %v", names)
	}
	encoded, err := enabled.toolExecutor(
		context.Background(),
		tools.GetGoalToolName,
		`{}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	var active goalToolSnapshot
	if err := json.Unmarshal([]byte(encoded), &active); err != nil {
		t.Fatalf("active get_goal = %q, %v", encoded, err)
	}
	if !active.Exists || active.Status != string(goalStatusActive) {
		t.Fatalf("active get_goal snapshot = %#v", active)
	}
	if !emitter.Emit(QueryEvent{
		Type:         EventTerminal,
		TerminalInfo: &Terminal{Reason: TerminalMaxTurns},
	}) {
		t.Fatal("Goal terminal rejected")
	}
	emitter.Close()
	enabled.endPlanTurn("p24-4-visible-tools")
	close(events)
	for range events {
	}

	disabled := newP241GoalEngine(t, QueryEngineConfig{
		CommandEntrypoint: commands.EntrypointTUI,
		GoalCapability:    &GoalCapabilityConfig{Enabled: false},
		ToolRegistry:      registry,
	})
	if got := poolToolNames(disabled.modelVisibleTools()); slices.Contains(got, tools.GetGoalToolName) ||
		slices.Contains(got, tools.UpdateGoalToolName) {
		t.Fatalf("disabled Goal leaked tools = %v", got)
	}
	if _, err := disabled.toolExecutor(
		context.Background(),
		tools.GetGoalToolName,
		`{}`,
	); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("disabled direct Goal tool error = %v", err)
	}

	child := newP241GoalEngine(t, QueryEngineConfig{
		AgentID:           "child",
		CommandEntrypoint: commands.EntrypointTUI,
		GoalCapability:    &GoalCapabilityConfig{Enabled: true},
		ToolRegistry:      registry,
	})
	if got := poolToolNames(child.modelVisibleTools()); slices.Contains(got, tools.GetGoalToolName) ||
		slices.Contains(got, tools.UpdateGoalToolName) {
		t.Fatalf("child Goal leaked tools = %v", got)
	}
	childExecutor := &SubAgentExecutor{ToolRegistry: registry}
	if got := poolToolNames(childExecutor.buildScopedTools(nil, "general-purpose")); slices.Contains(
		got,
		tools.GetGoalToolName,
	) || slices.Contains(got, tools.UpdateGoalToolName) {
		t.Fatalf("child Goal leaked into scoped prompt tools = %v", got)
	}
}

func TestP244PlanAndDisabledScopesExposeNoGoalAuthority(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	budget := uint64(10_000)
	for _, test := range []struct {
		name       string
		permission permission.Mode
		enabled    bool
		wantReason string
	}{
		{
			name:       "plan",
			permission: permission.ModePlan,
			enabled:    true,
			wantReason: "Plan mode",
		},
		{
			name:       "disabled",
			permission: permission.ModeDefault,
			enabled:    false,
			wantReason: "saved root TUI, Plain, or dedicated headless Goal session",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			eng := newP241GoalEngine(t, QueryEngineConfig{
				CommandEntrypoint: commands.EntrypointTUI,
				GoalCapability: &GoalCapabilityConfig{
					Enabled:            test.enabled,
					DefaultTokenBudget: &budget,
				},
				PermissionMode: test.permission,
				ToolRegistry:   registry,
			})
			available, reason := eng.GoalCommandAvailability()
			if available || !strings.Contains(reason, test.wantReason) {
				t.Fatalf("Goal availability = %v, %q", available, reason)
			}
			if got := poolToolNames(eng.modelVisibleTools()); slices.Contains(
				got,
				tools.GetGoalToolName,
			) || slices.Contains(got, tools.UpdateGoalToolName) {
				t.Fatalf("contained scope leaked Goal tools = %v", got)
			}
			result := submitP244Command(t, eng, "/goal contained objective")
			if result.Status != CommandResultUnsupported ||
				!strings.Contains(result.Output, test.wantReason) {
				t.Fatalf("contained Goal command = %#v", result)
			}
			if _, ok := eng.GoalSnapshot(); ok {
				t.Fatal("contained Goal command mutated durable state")
			}
		})
	}
}

func TestP244UpdateGoalRequiresCurrentGoalTurnAndRecordsCompletion(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	now := time.Date(2026, 7, 29, 6, 30, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock:             func() time.Time { return now },
		CommandEntrypoint: commands.EntrypointTUI,
		GoalCapability:    &GoalCapabilityConfig{Enabled: true},
		ToolRegistry:      registry,
	})
	budget := uint64(10_000)
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "prove model completion authority",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.toolExecutor(
		context.Background(),
		tools.UpdateGoalToolName,
		`{"status":"complete"}`,
	); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("out-of-turn update_goal error = %v", err)
	}

	events := make(chan QueryEvent, 16)
	emitter := beginP242aGoalTurn(t, eng, events, "p24-4-tool-turn", true, now)
	encoded, err := eng.toolExecutor(
		context.Background(),
		tools.UpdateGoalToolName,
		`{"status":"complete","reason":"all accepted work verified"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	var reported goalToolSnapshot
	if err := json.Unmarshal([]byte(encoded), &reported); err != nil ||
		reported.Status != string(goalStatusActive) {
		t.Fatalf("completion evidence response = %q, %v", encoded, err)
	}
	state := eng.goalService.snapshot()
	if state.PendingCompleteTurnID != "p24-4-tool-turn" {
		t.Fatalf("completion evidence = %#v", state)
	}
	now = now.Add(time.Second)
	if !emitter.Emit(QueryEvent{
		Type:         EventTerminal,
		TerminalInfo: &Terminal{Reason: TerminalCompleted},
	}) {
		t.Fatal("Goal terminal rejected")
	}
	emitter.Close()
	eng.endPlanTurn("p24-4-tool-turn")
	close(events)
	for range events {
	}
	if state = eng.goalService.snapshot(); state.Status != goalStatusComplete {
		t.Fatalf("completed Goal state = %#v", state)
	}
}

func TestP244QueuedUserSteeringPreventsGoalCompletion(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	now := time.Date(2026, 7, 29, 6, 40, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock:             func() time.Time { return now },
		CommandEntrypoint: commands.EntrypointTUI,
		GoalCapability:    &GoalCapabilityConfig{Enabled: true},
		ToolRegistry:      registry,
	})
	budget := uint64(10_000)
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "finish only after incorporating user steering",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}
	events := make(chan QueryEvent, 16)
	emitter := beginP242aGoalTurn(
		t,
		eng,
		events,
		"p24-4-completion-steering",
		true,
		now,
	)
	if _, err := eng.toolExecutor(
		context.Background(),
		tools.UpdateGoalToolName,
		`{"status":"complete","reason":"work appears finished"}`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.EnqueueUserInput(UserTurnInput{
		Display: "verify the final migration gate",
		Prompt:  "verify the final migration gate",
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if !emitter.Emit(QueryEvent{
		Type:         EventTerminal,
		TerminalInfo: &Terminal{Reason: TerminalCompleted},
	}) {
		t.Fatal("Goal terminal rejected")
	}
	emitter.Close()
	eng.endPlanTurn("p24-4-completion-steering")
	close(events)
	for range events {
	}

	state := eng.goalService.snapshot()
	if state.Status != goalStatusActive ||
		state.PendingCompleteTurnID != "p24-4-completion-steering" {
		t.Fatalf("queued steering incorrectly completed Goal: %#v", state)
	}
	if queued := eng.QueuedUserInputs(); len(queued) != 1 ||
		queued[0].Prompt != "verify the final migration gate" {
		t.Fatalf("queued steering was not preserved: %#v", queued)
	}
}

func TestP49DedicatedUnbudgetedGoalWakeClaimAndKillSwitch(t *testing.T) {
	now := time.Date(2026, 7, 29, 6, 45, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock:             func() time.Time { return now },
		CommandEntrypoint: commands.EntrypointTUI,
		GoalCapability:    &GoalCapabilityConfig{Enabled: true},
	})
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective: "prove dedicated continuation ownership",
	}); err != nil {
		t.Fatal(err)
	}
	genericReady := eng.SubscribeRuntimeItems()
	goalReady := eng.SubscribeGoalContinuations()
	finishP243EligibleTurn(t, eng, "p24-4-predecessor", &now)
	select {
	case <-genericReady:
		t.Fatal("Goal continuation signalled the generic runtime channel")
	default:
	}
	select {
	case <-goalReady:
	default:
		t.Fatal("Goal continuation did not signal its dedicated channel")
	}
	if item, ok, err := eng.ClaimNextRuntimeItem(); err != nil || ok {
		t.Fatalf("generic claim = %#v, %v, %v", item, ok, err)
	}

	eng.mu.Lock()
	eng.config.GoalCapability = &GoalCapabilityConfig{Enabled: false}
	coordinator := eng.inputCoordinator
	eng.mu.Unlock()
	warnings, err := eng.reconcileRestoredGoalContinuation(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 ||
		!strings.Contains(warnings[0], "disabled") {
		t.Fatalf("kill-switch warnings = %v", warnings)
	}
	state := eng.goalService.snapshot()
	if state.Status != goalStatusPaused ||
		state.TokenBudget != nil ||
		state.Continuation == nil ||
		state.Continuation.Disposition != goalContinuationDispositionRejected ||
		len(eng.RuntimeItems()) != 0 {
		t.Fatalf("kill-switch Goal state = %#v, items=%#v", state, eng.RuntimeItems())
	}
}

func TestP49QuiescedUnbudgetedGoalStaysPausedAfterDisabledRestart(t *testing.T) {
	now := time.Date(2026, 8, 7, 3, 0, 0, 0, time.UTC)
	cwd := t.TempDir()
	transcriptDir := t.TempDir()
	creator := NewQueryEngine(QueryEngineConfig{
		SessionID:         "p49-unbudgeted-rollback",
		ThreadID:          "p49-unbudgeted-rollback",
		CWD:               cwd,
		TranscriptDir:     transcriptDir,
		Clock:             func() time.Time { return now },
		CommandEntrypoint: commands.EntrypointTUI,
		GoalCapability:    &GoalCapabilityConfig{Enabled: true},
	})
	if err := creator.recordTranscriptMessages([]*schema.Message{{
		Role:    schema.User,
		Content: "start rollback proof",
	}}); err != nil {
		creator.Close()
		t.Fatal(err)
	}
	if _, err := creator.goalService.create(goalCreateRequest{
		Objective: "prove rollback quiescence",
	}); err != nil {
		creator.Close()
		t.Fatal(err)
	}
	finishP243EligibleTurn(t, creator, "p49-rollback-predecessor", &now)
	if state := creator.goalService.snapshot(); state == nil ||
		state.TokenBudget != nil ||
		state.Continuation == nil ||
		state.Continuation.Disposition != goalContinuationDispositionPending {
		creator.Close()
		t.Fatalf("pre-quiesce Goal state = %#v", state)
	}
	if _, err := creator.goalService.pause(); err != nil {
		creator.Close()
		t.Fatal(err)
	}
	if state := creator.goalService.snapshot(); state == nil ||
		state.Status != goalStatusPaused ||
		state.TokenBudget != nil ||
		state.Continuation == nil ||
		state.Continuation.Disposition != goalContinuationDispositionRejected ||
		len(creator.RuntimeItems()) != 0 {
		creator.Close()
		t.Fatalf("quiesced Goal state = %#v, items=%#v", state, creator.RuntimeItems())
	}
	creator.Close()

	restarted := NewQueryEngine(QueryEngineConfig{
		SessionID:         "p49-disabled-host",
		ThreadID:          "p49-disabled-host",
		CWD:               cwd,
		TranscriptDir:     transcriptDir,
		Clock:             func() time.Time { return now.Add(time.Hour) },
		CommandEntrypoint: commands.EntrypointTUI,
		GoalCapability:    &GoalCapabilityConfig{Enabled: false},
	})
	t.Cleanup(restarted.Close)
	if _, err := restarted.ResumeSession(
		context.Background(),
		"p49-unbudgeted-rollback",
	); err != nil {
		t.Fatal(err)
	}
	state := restarted.goalService.snapshot()
	if state == nil ||
		state.Status != goalStatusPaused ||
		state.TokenBudget != nil ||
		state.Continuation == nil ||
		state.Continuation.Disposition != goalContinuationDispositionRejected ||
		len(restarted.RuntimeItems()) != 0 {
		t.Fatalf("disabled restart Goal state = %#v, items=%#v", state, restarted.RuntimeItems())
	}
	if restarted.SubscribeGoalContinuations() != nil {
		t.Fatal("disabled restart exposed the Goal wake channel")
	}
	if item, ok, err := restarted.ClaimNextGoalContinuation(); err == nil || ok {
		t.Fatalf("disabled restart Goal claim = %#v, %v, %v", item, ok, err)
	}
}

func TestP244RecoveredGoalItemSignalsDedicatedOnly(t *testing.T) {
	now := time.Date(2026, 7, 29, 6, 48, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock:             func() time.Time { return now },
		CommandEntrypoint: commands.EntrypointTUI,
		GoalCapability:    &GoalCapabilityConfig{Enabled: true},
	})
	budget := uint64(10_000)
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "wake exactly once after restart",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}
	finishP243EligibleTurn(t, eng, "p24-4-recovery-predecessor", &now)

	recovered, err := NewRuntimeInputCoordinator(
		RuntimeInputCoordinatorConfig{
			SessionID: eng.SessionID(),
			ThreadID:  eng.ThreadID(),
			Path:      RuntimeInputPersistencePath(eng.transcript.Path()),
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-recovered.Subscribe():
		t.Fatal("recovered Goal item signalled the generic channel")
	default:
	}
	select {
	case <-recovered.SubscribeGoalContinuation():
	default:
		t.Fatal("recovered Goal item did not signal the dedicated channel")
	}

	eng.mu.Lock()
	eng.inputCoordinator = recovered
	eng.mu.Unlock()
	item, ok, err := eng.ClaimNextGoalContinuation()
	if err != nil || !ok || item.Kind != RuntimeItemGoalContinuation {
		t.Fatalf("recovered Goal claim = %#v, %v, %v", item, ok, err)
	}
	if duplicate, duplicateOK, duplicateErr := eng.ClaimNextGoalContinuation(); duplicateErr != nil ||
		duplicateOK {
		t.Fatalf("duplicate recovered Goal claim = %#v, %v, %v", duplicate, duplicateOK, duplicateErr)
	}
}

func TestP245aPlainUsesDedicatedGoalCapabilityWithoutGenericWidening(t *testing.T) {
	now := time.Date(2026, 7, 29, 7, 45, 0, 0, time.UTC)
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock:             func() time.Time { return now },
		CommandEntrypoint: commands.EntrypointPlain,
		GoalCapability:    &GoalCapabilityConfig{Enabled: true},
		ToolRegistry:      registry,
	})
	if available, reason := eng.GoalCommandAvailability(); !available {
		t.Fatalf("Plain Goal command unavailable: %s", reason)
	}
	result := submitP244Command(t, eng, "/goal finish the Plain Goal consumer")
	if result.Status != CommandResultSucceeded {
		t.Fatalf("Plain Goal command = %#v", result)
	}
	result = submitP244Command(t, eng, "/goal budget 10000")
	if result.Status != CommandResultSucceeded {
		t.Fatalf("Plain Goal budget = %#v", result)
	}
	result = submitP244Command(t, eng, "/goal resume")
	if result.Status != CommandResultSucceeded ||
		result.FollowUpPrompt != goalInitialPrompt {
		t.Fatalf("Plain Goal resume = %#v", result)
	}

	genericReady := eng.SubscribeRuntimeItems()
	goalReady := eng.SubscribeGoalContinuations()
	finishP243EligibleTurn(t, eng, "p24-5a-plain-predecessor", &now)
	select {
	case <-genericReady:
		t.Fatal("Plain Goal continuation signalled the generic runtime channel")
	default:
	}
	select {
	case <-goalReady:
	default:
		t.Fatal("Plain Goal continuation did not signal its dedicated channel")
	}
	if item, ok, err := eng.ClaimNextRuntimeItem(); err != nil || ok {
		t.Fatalf("generic Plain claim = %#v, %v, %v", item, ok, err)
	}
	item, ok, err := eng.ClaimNextGoalContinuation()
	if err != nil || !ok || item.Kind != RuntimeItemGoalContinuation {
		t.Fatalf("dedicated Plain claim = %#v, %v, %v", item, ok, err)
	}
	if duplicate, duplicateOK, duplicateErr := eng.ClaimNextGoalContinuation(); duplicateErr != nil ||
		duplicateOK {
		t.Fatalf(
			"duplicate Plain Goal claim = %#v, %v, %v",
			duplicate,
			duplicateOK,
			duplicateErr,
		)
	}
}

func TestP244UnsupportedEntrypointLeavesEnabledGoalDormant(t *testing.T) {
	for _, entrypoint := range []commands.Entrypoint{
		commands.EntrypointHeadless,
		commands.EntrypointACP,
		commands.EntrypointAdministration,
	} {
		t.Run(string(entrypoint), func(t *testing.T) {
			registry := tools.NewRegistry()
			tools.RegisterDefaults(registry)
			now := time.Date(2026, 7, 29, 6, 50, 0, 0, time.UTC)
			eng := newP241GoalEngine(t, QueryEngineConfig{
				Clock:             func() time.Time { return now },
				CommandEntrypoint: commands.EntrypointTUI,
				GoalCapability:    &GoalCapabilityConfig{Enabled: true},
				ToolRegistry:      registry,
			})
			budget := uint64(10_000)
			if _, err := eng.goalService.create(goalCreateRequest{
				Objective:   "remain dormant outside the TUI",
				TokenBudget: &budget,
			}); err != nil {
				t.Fatal(err)
			}
			finishP243EligibleTurn(t, eng, "p24-4-unsupported", &now)
			before := eng.goalService.snapshot()

			eng.mu.Lock()
			eng.config.CommandEntrypoint = entrypoint
			coordinator := eng.inputCoordinator
			eng.mu.Unlock()
			warnings, err := eng.reconcileRestoredGoalContinuation(coordinator)
			if err != nil {
				t.Fatal(err)
			}
			if len(warnings) != 0 {
				t.Fatalf("unsupported entrypoint warnings = %v", warnings)
			}
			after := eng.goalService.snapshot()
			if after.Status != goalStatusActive ||
				after.Revision != before.Revision ||
				after.Continuation == nil ||
				after.Continuation.Disposition != goalContinuationDispositionPending {
				t.Fatalf("unsupported entrypoint mutated Goal: before=%#v after=%#v", before, after)
			}
			if available, _ := eng.GoalCommandAvailability(); available {
				t.Fatal("unsupported entrypoint exposed the Goal command")
			}
			if eng.SubscribeGoalContinuations() != nil {
				t.Fatal("unsupported entrypoint exposed the Goal wake")
			}
			if item, ok, claimErr := eng.ClaimNextGoalContinuation(); claimErr == nil || ok {
				t.Fatalf("unsupported Goal claim = %#v, %v, %v", item, ok, claimErr)
			}
			if item, ok, claimErr := eng.ClaimNextRuntimeItem(); claimErr != nil || ok {
				t.Fatalf("generic claim observed dormant Goal = %#v, %v, %v", item, ok, claimErr)
			}
			names := poolToolNames(eng.modelVisibleTools())
			if slices.Contains(names, tools.GetGoalToolName) ||
				slices.Contains(names, tools.UpdateGoalToolName) {
				t.Fatalf("unsupported entrypoint leaked Goal tools = %v", names)
			}
		})
	}
}

func TestP245bDedicatedHeadlessGoalEntrypointDoesNotWidenSlashCommands(
	t *testing.T,
) {
	eng := newP241GoalEngine(t, QueryEngineConfig{
		CommandEntrypoint: commands.EntrypointHeadlessGoal,
		GoalCapability:    &GoalCapabilityConfig{Enabled: true},
	})
	if available, reason := eng.GoalCommandAvailability(); !available {
		t.Fatalf("dedicated headless Goal unavailable: %s", reason)
	}
	if command := eng.GetCommandRegistry().GetFor(
		commands.EntrypointHeadlessGoal,
		"goal",
	); command != nil {
		t.Fatalf("dedicated headless Goal exposed slash command: %#v", command)
	}

	eng.mu.Lock()
	eng.config.CommandEntrypoint = commands.EntrypointHeadless
	eng.mu.Unlock()
	if available, _ := eng.GoalCommandAvailability(); available {
		t.Fatal("ordinary headless unexpectedly gained Goal capability")
	}
}

func TestP244UpdateGoalEnforcesThreeDistinctBlockedTurns(t *testing.T) {
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock:             func() time.Time { return now },
		CommandEntrypoint: commands.EntrypointTUI,
		GoalCapability:    &GoalCapabilityConfig{Enabled: true},
		ToolRegistry:      registry,
	})
	budget := uint64(50_000)
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "wait for the same external dependency",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}

	var continuation *RuntimeItem
	for turn := 1; turn <= 3; turn++ {
		turnID := fmt.Sprintf("p24-4-blocker-%d", turn)
		if continuation != nil {
			turnID = continuation.GoalContinuation.ContinuationTurnID
		}
		events := make(chan QueryEvent, 16)
		if _, err := eng.beginPlanTurn(turnID); err != nil {
			t.Fatal(err)
		}
		emitter := newTurnEventEmitter(
			context.Background(),
			eng,
			events,
			turnID,
		)
		event, identity, err := eng.goalService.beginTurn(
			turnID,
			continuation == nil,
			continuation,
			now,
		)
		if err != nil {
			t.Fatal(err)
		}
		emitter.BindGoal(identity)
		if !emitter.Emit(event) {
			t.Fatal("Goal turn-start event rejected")
		}
		if continuation != nil {
			if err := eng.goalService.markContinuationDelivered(
				continuation.ID,
				turnID,
				now,
			); err != nil {
				t.Fatal(err)
			}
			coordinator, _, ownerErr := eng.runtimeInputOwner()
			if ownerErr != nil {
				t.Fatal(ownerErr)
			}
			if err := coordinator.Settle(continuation.ID); err != nil {
				t.Fatal(err)
			}
		}
		encoded, err := eng.toolExecutor(
			context.Background(),
			tools.UpdateGoalToolName,
			`{"status":"blocked","reason":"dependency is still unavailable","blocker_key":"dependency:release"}`,
		)
		if err != nil {
			t.Fatal(err)
		}
		var reported goalToolSnapshot
		if err := json.Unmarshal([]byte(encoded), &reported); err != nil {
			t.Fatal(err)
		}
		wantStatus := string(goalStatusActive)
		if turn == 3 {
			wantStatus = string(goalStatusBlocked)
		}
		if reported.Status != wantStatus ||
			reported.BlockerDistinctTurns != turn {
			t.Fatalf("turn %d blocker response = %#v", turn, reported)
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
		for range events {
		}
		now = now.Add(time.Second)
		continuation = nil
		if turn < 3 {
			item, ok, claimErr := eng.ClaimNextGoalContinuation()
			if claimErr != nil || !ok {
				t.Fatalf(
					"turn %d continuation claim = %#v, %v, %v",
					turn,
					item,
					ok,
					claimErr,
				)
			}
			continuation = &item
		}
	}

	state := eng.goalService.snapshot()
	if state.Status != goalStatusBlocked ||
		state.BlockerKey != "dependency:release" ||
		len(state.BlockerTurnIDs) != 3 ||
		len(eng.RuntimeItems()) != 0 {
		t.Fatalf("blocked Goal state = %#v, items=%#v", state, eng.RuntimeItems())
	}
}

func submitP244Command(
	t *testing.T,
	eng *QueryEngine,
	input string,
) *CommandResultEvent {
	t.Helper()
	events, _ := eng.SubmitMessage(context.Background(), input)
	for _, event := range drainEngineEvents(t, events) {
		if event.Type == EventCommandResult && event.CommandResult != nil {
			return event.CommandResult
		}
	}
	t.Fatalf("command %q emitted no result", input)
	return nil
}
