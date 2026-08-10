package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/budget"
	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

func commandControlModelResolver(modelSpec string) (provider.ResolvedConfig, error) {
	switch strings.TrimSpace(modelSpec) {
	case "claude-sonnet-4-6", "smart":
		return provider.ResolvedConfig{Config: provider.Config{
			Provider: provider.ProviderAgenticClaude,
			Model:    "claude-sonnet-4-6",
		}}, nil
	case "gpt-4o":
		return provider.ResolvedConfig{Config: provider.Config{
			Provider: provider.ProviderAgenticOpenAI,
			Model:    "gpt-4o",
		}}, nil
	default:
		return provider.ResolvedConfig{}, fmt.Errorf("model %q is absent from resolved inventory", modelSpec)
	}
}

func TestCommandExecutorDispatchesAndAppliesExactlyOnce(t *testing.T) {
	root := t.TempDir()
	additional := t.TempDir()
	canonicalAdditional, err := filepath.EvalSymlinks(additional)
	if err != nil {
		t.Fatal(err)
	}
	var dispatches atomic.Int32
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         "command-owner-once",
		CWD:               root,
		TranscriptDir:     filepath.Join(root, "transcripts"),
		CommandEntrypoint: commands.EntrypointHeadless,
	})
	t.Cleanup(eng.Close)
	if err := eng.GetCommandRegistry().Register(&commands.Command{
		Name:           "counted-add-dir",
		Kind:           commands.CommandKindRuntimeMutation,
		Entrypoints:    commands.EntrypointsHeadless,
		SideEffect:     commands.SideEffectProcessLocal,
		ResultKind:     commands.ResultKindAction,
		ExecutionOwner: commands.ExecutionOwnerEngine,
		Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
			dispatches.Add(1)
			return &commands.CommandResult{
				Output: "directory added",
				Action: commands.ActionAddDir,
				Data:   map[string]any{"path": additional},
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	events := drainRuntimeEvents(t, eng, "/counted-add-dir")
	assertSingleCommandResultThenTerminal(t, events, CommandResultSucceeded)
	if got := dispatches.Load(); got != 1 {
		t.Fatalf("command dispatches = %d, want 1", got)
	}
	dirs := eng.GetWorkingDirectories()
	if len(dirs) != 2 || dirs[1] != canonicalAdditional {
		t.Fatalf("working directories = %#v", dirs)
	}
}

func TestCommandExecutorRejectsInvalidTypedPayloadBeforeMutation(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         "command-invalid-payload",
		CWD:               t.TempDir(),
		TranscriptDir:     t.TempDir(),
		Model:             "model-before",
		CommandEntrypoint: commands.EntrypointHeadless,
	})
	t.Cleanup(eng.Close)
	if err := eng.GetCommandRegistry().Register(&commands.Command{
		Name:           "bad-model-payload",
		Kind:           commands.CommandKindRuntimeMutation,
		Entrypoints:    commands.EntrypointsHeadless,
		SideEffect:     commands.SideEffectProcessLocal,
		ResultKind:     commands.ResultKindAction,
		ExecutionOwner: commands.ExecutionOwnerEngine,
		Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
			return &commands.CommandResult{
				Action: commands.ActionChangeModel,
				Data:   map[string]any{"model": 42},
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	events := drainRuntimeEvents(t, eng, "/bad-model-payload")
	result := assertSingleCommandResultThenTerminal(t, events, CommandResultFailed)
	if !strings.Contains(result.Error, `"model" must be a string`) {
		t.Fatalf("typed payload error = %q", result.Error)
	}
	if got := eng.GetModelName(); got != "model-before" {
		t.Fatalf("model mutated after invalid payload: %q", got)
	}
}

func TestP165aModelAndEffortCapabilitiesFailBeforeMutation(t *testing.T) {
	tracker := budget.NewTokenBudget(100000)
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "p165a-model-effort",
		CWD:                t.TempDir(),
		TranscriptDir:      t.TempDir(),
		Model:              "claude-sonnet-4-6",
		ModelResolver:      ModelResolverFunc(commandControlModelResolver),
		TokenBudgetTracker: tracker,
		CommandEntrypoint:  commands.EntrypointHeadless,
	})
	t.Cleanup(eng.Close)

	effortEvents := drainRuntimeEvents(t, eng, "/effort high")
	effortResult := assertSingleCommandResultThenTerminal(
		t,
		effortEvents,
		CommandResultSucceeded,
	)
	if effortResult.Action != commands.ActionSetEffort ||
		eng.ReasoningEffort() != "high" {
		t.Fatalf("effort result/state = %#v / %q", effortResult, eng.ReasoningEffort())
	}
	if tracker.TurnBudget != 100000 {
		t.Fatalf("reasoning effort mutated continuation token budget: %d", tracker.TurnBudget)
	}

	rejectedEvents := drainRuntimeEvents(t, eng, "/model missing-model")
	rejected := assertSingleCommandResultThenTerminal(
		t,
		rejectedEvents,
		CommandResultFailed,
	)
	if !strings.Contains(rejected.Error, "absent from resolved inventory") {
		t.Fatalf("model rejection = %#v", rejected)
	}
	if got := eng.GetModelName(); got != "claude-sonnet-4-6" {
		t.Fatalf("rejected model mutated state to %q", got)
	}
	if got := eng.ReasoningEffort(); got != "high" {
		t.Fatalf("rejected model cleared reasoning effort: %q", got)
	}

	changedEvents := drainRuntimeEvents(t, eng, "/model gpt-4o")
	changed := assertSingleCommandResultThenTerminal(
		t,
		changedEvents,
		CommandResultSucceeded,
	)
	if changed.Action != commands.ActionChangeModel ||
		!strings.Contains(changed.Output, "agenticopenai:gpt-4o") {
		t.Fatalf("model change result = %#v", changed)
	}
	if got := eng.GetModelName(); got != "gpt-4o" {
		t.Fatalf("effective model = %q", got)
	}
	if got := eng.ReasoningEffort(); got != "" {
		t.Fatalf("incompatible model retained effort %q", got)
	}

	unsupportedEvents := drainRuntimeEvents(t, eng, "/effort low")
	unsupported := assertSingleCommandResultThenTerminal(
		t,
		unsupportedEvents,
		CommandResultUnsupported,
	)
	if !strings.Contains(unsupported.Output, "does not expose compatible reasoning effort") {
		t.Fatalf("unsupported effort result = %#v", unsupported)
	}
	if got := eng.ReasoningEffort(); got != "" {
		t.Fatalf("unsupported effort mutated state to %q", got)
	}
}

func TestP165aSetModelCompatibilityAdapterFailsClosed(t *testing.T) {
	withoutResolver := NewQueryEngine(QueryEngineConfig{
		SessionID:     "p165a-set-model-without-resolver",
		CWD:           t.TempDir(),
		TranscriptDir: t.TempDir(),
		Model:         "claude-sonnet-4-6",
	})
	t.Cleanup(withoutResolver.Close)
	withoutResolver.SetModel("gpt-4o")
	if got := withoutResolver.GetModelName(); got != "claude-sonnet-4-6" {
		t.Fatalf("legacy setter bypassed missing inventory: %q", got)
	}

	withResolver := NewQueryEngine(QueryEngineConfig{
		SessionID:     "p165a-set-model-with-resolver",
		CWD:           t.TempDir(),
		TranscriptDir: t.TempDir(),
		Model:         "claude-sonnet-4-6",
		ModelResolver: ModelResolverFunc(commandControlModelResolver),
	})
	t.Cleanup(withResolver.Close)
	withResolver.SetModel("gpt-4o")
	if got := withResolver.GetModelName(); got != "gpt-4o" {
		t.Fatalf("legacy setter did not reuse validated model control: %q", got)
	}
}

func TestP165aDirectExecutionControlsRejectAnActiveTurn(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "p165a-active-turn-controls",
		CWD:           t.TempDir(),
		TranscriptDir: t.TempDir(),
		Model:         "claude-sonnet-4-6",
		ModelResolver: ModelResolverFunc(commandControlModelResolver),
	})
	t.Cleanup(eng.Close)
	if _, err := eng.ChangeReasoningEffort(context.Background(), "high"); err != nil {
		t.Fatal(err)
	}

	eng.planMu.Lock()
	eng.planActiveTurnID = "active-turn"
	eng.planMu.Unlock()
	if _, err := eng.ChangeModel(context.Background(), "gpt-4o"); err == nil ||
		!strings.Contains(err.Error(), "active-turn") {
		t.Fatalf("direct model control during active turn = %v", err)
	}
	if _, err := eng.ChangeReasoningEffort(context.Background(), "low"); err == nil ||
		!strings.Contains(err.Error(), "active-turn") {
		t.Fatalf("direct effort control during active turn = %v", err)
	}
	if got := eng.GetModelName(); got != "claude-sonnet-4-6" {
		t.Fatalf("rejected direct model control mutated model to %q", got)
	}
	if got := eng.ReasoningEffort(); got != "high" {
		t.Fatalf("rejected direct effort control mutated effort to %q", got)
	}

	eng.planMu.Lock()
	eng.planActiveTurnID = ""
	eng.planMu.Unlock()
}

func TestP165aPermissionAndPlanControlsAgreeAcrossEntrypoints(t *testing.T) {
	entrypoints := []commands.Entrypoint{
		commands.EntrypointTUI,
		commands.EntrypointPlain,
		commands.EntrypointHeadless,
		commands.EntrypointACP,
	}
	for _, entrypoint := range entrypoints {
		t.Run(string(entrypoint), func(t *testing.T) {
			eng := NewQueryEngine(QueryEngineConfig{
				SessionID:         "p165a-mode-" + string(entrypoint),
				CWD:               t.TempDir(),
				TranscriptDir:     t.TempDir(),
				PermissionMode:    permission.ModeDefault,
				CommandEntrypoint: entrypoint,
			})
			t.Cleanup(eng.Close)

			events := drainRuntimeEvents(t, eng, "/permissions mode plan")
			if len(events) != 3 || events[0].Type != EventPlanStateTransition {
				t.Fatalf("mode transition event order = %#v", events)
			}
			result := commandResultFromEvents(t, events)
			if result.Status != CommandResultSucceeded ||
				!strings.Contains(result.Output, "Effective permission mode: plan") {
				t.Fatalf("mode result = %#v", result)
			}
			if eng.PermissionMode() != permission.ModePlan ||
				eng.PlanState().Phase != PlanPhaseActive {
				t.Fatalf("effective mode/state = %q / %#v", eng.PermissionMode(), eng.PlanState())
			}

			rejectedEvents := drainRuntimeEvents(t, eng, "/permissions mode bypassPermissions")
			rejected := assertSingleCommandResultThenTerminal(
				t,
				rejectedEvents,
				CommandResultFailed,
			)
			if !strings.Contains(rejected.Error, "explicit confirmation") ||
				eng.PermissionMode() != permission.ModePlan {
				t.Fatalf("unconfirmed bypass result/state = %#v / %q", rejected, eng.PermissionMode())
			}

			confirmedEvents := drainRuntimeEvents(t, eng, "/permissions bypass confirm")
			if len(confirmedEvents) != 3 || confirmedEvents[0].Type != EventPlanStateTransition {
				t.Fatalf("confirmed bypass event order = %#v", confirmedEvents)
			}
			confirmed := commandResultFromEvents(t, confirmedEvents)
			if confirmed.Status != CommandResultSucceeded ||
				eng.PermissionMode() != permission.ModeBypassPermissions ||
				eng.PlanState().Phase != PlanPhaseInactive ||
				!strings.Contains(confirmed.Output, "Effective permission mode: bypassPermissions") {
				t.Fatalf("confirmed bypass result/state = %#v / %q / %#v", confirmed, eng.PermissionMode(), eng.PlanState())
			}
		})
	}
}

func TestCommandExecutorRejectsEntrypointOwnerBeforeDispatch(t *testing.T) {
	var dispatches atomic.Int32
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         "command-entrypoint-owner",
		CWD:               t.TempDir(),
		TranscriptDir:     t.TempDir(),
		CommandEntrypoint: commands.EntrypointHeadless,
	})
	t.Cleanup(eng.Close)
	if err := eng.GetCommandRegistry().Register(&commands.Command{
		Name:           "entrypoint-side-effect",
		Kind:           commands.CommandKindUIAction,
		Entrypoints:    commands.EntrypointsHeadless,
		SideEffect:     commands.SideEffectProcessLocal,
		ResultKind:     commands.ResultKindUI,
		ExecutionOwner: commands.ExecutionOwnerEntrypoint,
		Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
			dispatches.Add(1)
			return &commands.CommandResult{Output: "should not execute"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	events := drainRuntimeEvents(t, eng, "/entrypoint-side-effect")
	assertSingleCommandResultThenTerminal(t, events, CommandResultUnsupported)
	if got := dispatches.Load(); got != 0 {
		t.Fatalf("entrypoint-owned handler dispatches = %d, want 0", got)
	}
}

func TestCommandExecutorRejectsHiddenEntrypointOwnerBeforeDispatch(t *testing.T) {
	var dispatches atomic.Int32
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         "command-hidden-entrypoint-owner",
		CWD:               t.TempDir(),
		TranscriptDir:     t.TempDir(),
		CommandEntrypoint: commands.EntrypointHeadless,
	})
	t.Cleanup(eng.Close)
	if err := eng.GetCommandRegistry().Register(&commands.Command{
		Name:           "hidden-entrypoint-side-effect",
		Kind:           commands.CommandKindUIAction,
		Entrypoints:    commands.EntrypointsHeadless,
		Availability:   commands.AvailabilityHidden,
		SideEffect:     commands.SideEffectProcessLocal,
		ResultKind:     commands.ResultKindUI,
		ExecutionOwner: commands.ExecutionOwnerEntrypoint,
		Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
			dispatches.Add(1)
			return &commands.CommandResult{Output: "should not execute"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	events := drainRuntimeEvents(t, eng, "/hidden-entrypoint-side-effect")
	assertSingleCommandResultThenTerminal(t, events, CommandResultUnsupported)
	if got := dispatches.Load(); got != 0 {
		t.Fatalf("hidden entrypoint-owned handler dispatches = %d, want 0", got)
	}
}

func TestCommandExecutorPlanModeReturnsTypedFollowUp(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         "command-plan",
		CWD:               t.TempDir(),
		TranscriptDir:     t.TempDir(),
		PermissionMode:    permission.ModeDefault,
		CommandEntrypoint: commands.EntrypointPlain,
	})
	t.Cleanup(eng.Close)

	events := drainRuntimeEvents(t, eng, "/plan design the migration")
	if len(events) != 3 ||
		events[0].Type != EventPlanStateTransition ||
		events[1].Type != EventCommandResult ||
		events[2].Type != EventTerminal {
		t.Fatalf("plan command event order = %#v", events)
	}
	result := events[1].CommandResult
	if result == nil || result.Status != CommandResultSucceeded {
		t.Fatalf("plan command result = %#v", result)
	}
	if result.Action != commands.ActionPlanMode ||
		result.FollowUpPrompt != "design the migration" {
		t.Fatalf("plan result = %#v", result)
	}
	if got := eng.PermissionMode(); got != permission.ModePlan {
		t.Fatalf("permission mode = %q, want plan", got)
	}
	for index, event := range events {
		if event.Sequence != uint64(index+1) {
			t.Fatalf("plan command sequence[%d] = %d, want %d", index, event.Sequence, index+1)
		}
	}
	replayed := NewRuntimeStateStore()
	if err := replayed.Replay(events); err != nil {
		t.Fatalf("replay plan command events: %v", err)
	}
	thread, ok := replayed.ThreadSnapshot("command-plan")
	if !ok || thread.Plan == nil ||
		thread.Plan.Phase != PlanPhaseActive ||
		thread.Plan.PermissionMode != string(permission.ModePlan) ||
		thread.Plan.Sequence != 1 ||
		thread.LastSequence != 3 {
		t.Fatalf("replayed plan command thread = %#v, ok=%v", thread, ok)
	}
}

func TestCommandTurnAdmissionRejectsConcurrentMutationsWithoutSequenceGap(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         "command-concurrent-admission",
		CWD:               t.TempDir(),
		TranscriptDir:     t.TempDir(),
		PermissionMode:    permission.ModeDefault,
		CommandEntrypoint: commands.EntrypointPlain,
		ChatModel: &p170BlockingModel{
			started: started,
			release: release,
		},
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages([]*schema.Message{
		{Role: schema.User, Content: "preserve this message"},
	})

	modelEvents, _ := eng.SubmitMessage(context.Background(), "block the turn")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("model turn did not reach the blocking model")
	}
	beforeSequence := eng.runtimeState.LastSequence("command-concurrent-admission")

	for _, input := range []string{"/clear", "/plan concurrent change"} {
		events := drainRuntimeEvents(t, eng, input)
		result := assertSingleCommandResultThenTerminal(
			t,
			events,
			CommandResultFailed,
		)
		if !strings.Contains(result.Error, ErrPlanTransitionInFlight.Error()) {
			t.Fatalf("%s admission error = %q", input, result.Error)
		}
		for _, event := range events {
			if event.Sequence != 0 || event.TurnID != "" {
				t.Fatalf("%s unadmitted event gained runtime identity: %#v", input, event)
			}
		}
	}
	if got := eng.runtimeState.LastSequence("command-concurrent-admission"); got != beforeSequence {
		t.Fatalf("unadmitted commands advanced sequence from %d to %d", beforeSequence, got)
	}
	if got := eng.GetMessages(); len(got) != 1 ||
		got[0].Content != "preserve this message" {
		t.Fatalf("concurrent clear mutated messages: %#v", got)
	}
	if got := eng.PermissionMode(); got != permission.ModeDefault {
		t.Fatalf("concurrent plan mutated permission mode to %q", got)
	}
	if err := eng.RuntimeStateError(); err != nil {
		t.Fatalf("unadmitted command reached runtime reducer: %v", err)
	}

	close(release)
	var accepted []QueryEvent
	for event := range modelEvents {
		accepted = append(accepted, event)
	}
	for index, event := range accepted {
		want := uint64(index + 1)
		if index > 0 {
			want = accepted[index-1].Sequence + 1
		}
		if event.Sequence != want {
			t.Fatalf(
				"accepted model sequence[%d] = %d, want %d",
				index,
				event.Sequence,
				want,
			)
		}
	}
	if err := eng.RuntimeStateError(); err != nil {
		t.Fatalf("runtime reducer rejected accepted model turn: %v", err)
	}
}

func TestCommandTerminalReleasesAdmissionBeforeFollowUp(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         "command-terminal-boundary",
		CWD:               t.TempDir(),
		TranscriptDir:     t.TempDir(),
		CommandEntrypoint: commands.EntrypointPlain,
	})
	t.Cleanup(eng.Close)

	firstStream, _ := eng.SubmitMessage(context.Background(), "/clear")
	var firstEvents []QueryEvent
	var followUpStream <-chan QueryEvent
	for event := range firstStream {
		firstEvents = append(firstEvents, event)
		if event.Type == EventTerminal {
			followUpStream, _ = eng.SubmitMessage(
				context.Background(),
				"/clear",
			)
		}
	}
	if followUpStream == nil {
		t.Fatal("first command did not publish terminal")
	}
	var followUpEvents []QueryEvent
	for event := range followUpStream {
		followUpEvents = append(followUpEvents, event)
	}
	assertSingleCommandResultThenTerminal(
		t,
		firstEvents,
		CommandResultSucceeded,
	)
	assertSingleCommandResultThenTerminal(
		t,
		followUpEvents,
		CommandResultSucceeded,
	)
	if followUpEvents[0].Sequence != firstEvents[1].Sequence+1 ||
		followUpEvents[0].TurnID == "" {
		t.Fatalf(
			"terminal follow-up was not admitted contiguously: first=%#v follow-up=%#v",
			firstEvents,
			followUpEvents,
		)
	}
}

func TestCommandExecutorForkCreatesAndSwitchesOnce(t *testing.T) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	sourceID := "11111111-1111-4111-8111-111111111111"
	messages := []*schema.Message{
		{Role: schema.User, Content: "source question"},
		{Role: schema.Assistant, Content: "source answer"},
	}
	recorder := transcript.NewRecorder(sourceID, transcriptDir)
	if err := recorder.ReplaceWithReplacements(messages, nil); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteSessionMetadata(
		recorder,
		&session.SessionMetadataFull{
			SessionID:          sourceID,
			ThreadID:           sourceID,
			QueryKernelVersion: queryKernelVersionProjectGraph,
			QueryKernelStage:   string(queryKernelStageFull),
			CreatedAt:          time.Now().UTC(),
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         sourceID,
		CWD:               root,
		TranscriptDir:     transcriptDir,
		CommandEntrypoint: commands.EntrypointPlain,
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages(messages)

	events := drainRuntimeEvents(t, eng, "/fork one-owner")
	result := commandResultFromEvents(t, events)
	if result.Status != CommandResultSucceeded || result.Action != commands.ActionFork {
		t.Fatalf("fork result = %#v", result)
	}
	if eng.SessionID() == sourceID {
		t.Fatal("fork did not switch to the child session")
	}
	for _, event := range events {
		if event.SessionID != sourceID || event.ThreadID != sourceID {
			t.Fatalf(
				"fork source turn drifted to session/thread %q/%q: %#v",
				event.SessionID,
				event.ThreadID,
				event,
			)
		}
	}
	if events[len(events)-1].Type != EventTerminal {
		t.Fatalf("fork source turn did not terminate: %#v", events)
	}
	sourceThread, ok := eng.runtimeState.ThreadSnapshot(sourceID)
	if !ok || sourceThread.Status != RuntimeThreadCompleted {
		t.Fatalf("fork source runtime thread = %#v, ok=%v", sourceThread, ok)
	}
	branches, err := session.ListBranches(sourceID, transcriptDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 1 {
		t.Fatalf("fork branches = %#v, want exactly one", branches)
	}
}

func TestRuntimeReplayRetainsTypedCommandOutcomes(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         "command-typed-replay",
		CWD:               t.TempDir(),
		TranscriptDir:     t.TempDir(),
		Model:             "model-before",
		CommandEntrypoint: commands.EntrypointPlain,
	})
	t.Cleanup(eng.Close)
	if err := eng.GetCommandRegistry().Register(&commands.Command{
		Name:           "bad-replay-model",
		Kind:           commands.CommandKindRuntimeMutation,
		Entrypoints:    commands.EntrypointsPlain,
		SideEffect:     commands.SideEffectProcessLocal,
		ResultKind:     commands.ResultKindAction,
		ExecutionOwner: commands.ExecutionOwnerEngine,
		Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
			return &commands.CommandResult{
				Action: commands.ActionChangeModel,
				Data:   map[string]any{"model": 42},
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	var events []QueryEvent
	events = append(events, drainRuntimeEvents(t, eng, "/clear")...)
	events = append(events, drainRuntimeEvents(t, eng, "/undo")...)
	events = append(events, drainRuntimeEvents(t, eng, "/bad-replay-model")...)

	replayed := NewRuntimeStateStore()
	if err := replayed.Replay(events); err != nil {
		t.Fatal(err)
	}
	thread, ok := replayed.ThreadSnapshot("command-typed-replay")
	if !ok {
		t.Fatal("replay omitted command thread")
	}
	var records []RuntimeEventRecord
	for _, record := range thread.Events {
		if record.Type == EventCommandResult {
			records = append(records, record)
		}
	}
	if len(records) != 3 {
		t.Fatalf("typed command records = %#v", records)
	}
	if records[0].Command != "clear" ||
		records[0].CommandAction != commands.ActionClear ||
		records[0].CommandStatus != CommandResultSucceeded {
		t.Fatalf("succeeded command replay = %#v", records[0])
	}
	if records[1].Command != "undo" ||
		records[1].CommandStatus != CommandResultUnsupported {
		t.Fatalf("unsupported command replay = %#v", records[1])
	}
	if records[2].Command != "bad-replay-model" ||
		records[2].CommandAction != commands.ActionChangeModel ||
		records[2].CommandStatus != CommandResultFailed ||
		records[2].CommandError == "" {
		t.Fatalf("failed command replay = %#v", records[2])
	}
}

func TestCommandExecutorSessionsRenamePersistsOnce(t *testing.T) {
	root := t.TempDir()
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         "command-rename-once",
		CWD:               root,
		TranscriptDir:     filepath.Join(root, "transcripts"),
		CommandEntrypoint: commands.EntrypointPlain,
	})
	t.Cleanup(eng.Close)

	events := drainRuntimeEvents(t, eng, "/sessions rename current canonical-owner")
	assertSingleCommandResultThenTerminal(t, events, CommandResultSucceeded)
	loaded, err := eng.GetTranscript().LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	var titles int
	for _, metadata := range loaded.Metadata {
		if metadata.Key == "customTitle" && metadata.Value == "canonical-owner" {
			titles++
		}
	}
	if titles != 1 {
		t.Fatalf("persisted customTitle entries = %d, want 1", titles)
	}
}

func TestCommandExecutorSessionsSurfaceUsesOneService(t *testing.T) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	writeServiceSession(t, transcriptDir, "current", "current prompt")
	writeServiceSession(t, transcriptDir, "saved", "needle migration plan")
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         "current",
		CWD:               root,
		TranscriptDir:     transcriptDir,
		CommandEntrypoint: commands.EntrypointHeadless,
	})
	t.Cleanup(eng.Close)

	listed := commandResultFromEvents(
		t,
		drainRuntimeEvents(t, eng, `/sessions search "migration plan"`),
	)
	if listed.Status != CommandResultSucceeded ||
		listed.Action != commands.ActionSessions ||
		!strings.Contains(listed.Output, "saved") ||
		strings.Contains(listed.Output, "current prompt") {
		t.Fatalf("sessions search result = %#v", listed)
	}

	renamed := commandResultFromEvents(
		t,
		drainRuntimeEvents(t, eng, `/sessions rename saved "release plan"`),
	)
	if renamed.Status != CommandResultSucceeded ||
		renamed.Action != commands.ActionRename ||
		!strings.Contains(renamed.Output, "release plan") {
		t.Fatalf("sessions rename result = %#v", renamed)
	}
	renamedTranscript, err := transcript.NewRecorder("saved", transcriptDir).LoadFull()
	if err != nil || countTranscriptMetadata(renamedTranscript, "customTitle", "release plan") != 1 {
		t.Fatalf("renamed session = %#v, err=%v", renamedTranscript, err)
	}

	exported := commandResultFromEvents(
		t,
		drainRuntimeEvents(t, eng, `/sessions export saved "saved report.txt"`),
	)
	if exported.Status != CommandResultSucceeded ||
		exported.Action != commands.ActionExport ||
		!strings.Contains(exported.Output, filepath.Join(root, "saved report.md")) {
		t.Fatalf("sessions export result = %#v", exported)
	}
	content, err := os.ReadFile(filepath.Join(root, "saved report.md"))
	if err != nil || !strings.Contains(string(content), "needle migration plan") {
		t.Fatalf("exported content = %q, err=%v", content, err)
	}
}

func TestCommandExecutorSessionsActionsPreserveCatalogSource(t *testing.T) {
	root := t.TempDir()
	currentDir := filepath.Join(root, "current-transcripts")
	importedDir := filepath.Join(root, "imported-transcripts")
	catalog := filepath.Join(root, "session-roots.json")
	writeServiceSession(t, currentDir, "current", "current prompt")
	writeServiceSession(t, importedDir, "imported", "imported migration plan")
	if err := session.RegisterSessionRoot(catalog, root, importedDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "current",
		CWD:                root,
		TranscriptDir:      currentDir,
		SessionCatalogPath: catalog,
		CommandEntrypoint:  commands.EntrypointHeadless,
	})
	t.Cleanup(eng.Close)

	listed := commandResultFromEvents(
		t,
		drainRuntimeEvents(t, eng, `/sessions search imported`),
	)
	if listed.Status != CommandResultSucceeded ||
		!strings.Contains(listed.Output, "imported") {
		t.Fatalf("sessions list result = %#v", listed)
	}
	renamed := commandResultFromEvents(
		t,
		drainRuntimeEvents(t, eng, `/sessions rename imported "catalog source"`),
	)
	if renamed.Status != CommandResultSucceeded {
		t.Fatalf("sessions rename result = %#v", renamed)
	}
	importedTranscript, err := transcript.NewRecorder("imported", importedDir).LoadFull()
	if err != nil ||
		countTranscriptMetadata(importedTranscript, "customTitle", "catalog source") != 1 {
		t.Fatalf("imported rename = %#v, err=%v", importedTranscript, err)
	}
	if _, err := os.Stat(filepath.Join(currentDir, "imported.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("rename created a transcript in the current root: %v", err)
	}

	exported := commandResultFromEvents(
		t,
		drainRuntimeEvents(t, eng, `/sessions export imported "imported report.md"`),
	)
	if exported.Status != CommandResultSucceeded {
		t.Fatalf("sessions export result = %#v", exported)
	}
	content, err := os.ReadFile(filepath.Join(root, "imported report.md"))
	if err != nil || !strings.Contains(string(content), "imported migration plan") {
		t.Fatalf("imported export = %q, err=%v", content, err)
	}

	resumed := commandResultFromEvents(
		t,
		drainRuntimeEvents(t, eng, `/sessions resume imported`),
	)
	if resumed.Status != CommandResultSucceeded ||
		eng.SessionID() != "imported" ||
		canonicalSessionDirectory(eng.GetTranscriptDir()) !=
			canonicalSessionDirectory(importedDir) {
		t.Fatalf(
			"sessions resume result=%#v id=%q dir=%q",
			resumed,
			eng.SessionID(),
			eng.GetTranscriptDir(),
		)
	}
}

func countTranscriptMetadata(result *transcript.LoadResult, key, value string) int {
	if result == nil {
		return 0
	}
	count := 0
	for _, metadata := range result.Metadata {
		if metadata.Key == key && metadata.Value == value {
			count++
		}
	}
	return count
}

func TestCommandExecutorNewCreatesDurableEmptyIdentityWithoutErasingSource(t *testing.T) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	sourceID := "new-source-session"
	sourceMessages := []*schema.Message{
		{Role: schema.User, Content: "source question"},
		{Role: schema.Assistant, Content: "source answer"},
	}
	sourceRecorder := transcript.NewRecorder(sourceID, transcriptDir)
	if err := sourceRecorder.RecordLifecycleBoundary(
		transcript.LifecycleCheckpoint,
		sourceMessages,
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteSessionMetadata(
		sourceRecorder,
		&session.SessionMetadataFull{
			SessionID:          sourceID,
			ThreadID:           sourceID,
			QueryKernelVersion: queryKernelVersionProjectGraph,
			QueryKernelStage:   string(queryKernelStageFull),
			CreatedAt:          time.Now().UTC(),
		},
	); err != nil {
		t.Fatal(err)
	}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         sourceID,
		CWD:               root,
		TranscriptDir:     transcriptDir,
		Model:             "test-model",
		CommandEntrypoint: commands.EntrypointPlain,
	})
	t.Cleanup(eng.Close)
	if eng.queryKernelSelection.kernel == nil ||
		eng.queryKernelSelection.kernel.kind() != queryKernelProjectGraph {
		t.Fatalf("source session kernel = %#v", eng.queryKernelSelection)
	}

	events := drainRuntimeEvents(t, eng, "/new")
	result := commandResultFromEvents(t, events)
	if result.Status != CommandResultSucceeded ||
		result.Action != commands.ActionNew {
		t.Fatalf("new result = %#v", result)
	}
	newID := eng.SessionID()
	if newID == "" || newID == sourceID || eng.ThreadID() != newID {
		t.Fatalf(
			"new identity = session %q thread %q, source %q",
			newID,
			eng.ThreadID(),
			sourceID,
		)
	}
	if len(eng.GetMessages()) != 0 {
		t.Fatalf("new session inherited messages: %#v", eng.GetMessages())
	}
	for _, event := range events {
		if event.SessionID != sourceID || event.ThreadID != sourceID {
			t.Fatalf("new command source identity drifted: %#v", event)
		}
	}

	sourceLoaded, err := sourceRecorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(sourceLoaded.Messages) != 2 ||
		sourceLoaded.Messages[0].Content != "source question" {
		t.Fatalf("source transcript was erased: %#v", sourceLoaded.Messages)
	}
	newLoaded, err := transcript.NewRecorder(newID, transcriptDir).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(newLoaded.Messages) != 0 ||
		len(newLoaded.LifecycleBoundaries) != 1 ||
		newLoaded.LifecycleBoundaries[0].Kind != transcript.LifecycleSessionStart {
		t.Fatalf("new durable transcript = %#v", newLoaded)
	}
	newMetadata := session.ReadSessionMetadataFull(newLoaded)
	if newMetadata == nil ||
		newMetadata.QueryKernelVersion != queryKernelVersionProjectGraph ||
		newMetadata.QueryKernelStage != string(queryKernelStageFull) {
		t.Fatalf("new session query kernel metadata = %#v", newMetadata)
	}
	resumed, err := session.ResumeSession(context.Background(), session.ResumeOptions{
		SessionID:        newID,
		SessionDir:       transcriptDir,
		ProjectDir:       root,
		ValidateMessages: true,
	})
	if err != nil {
		t.Fatalf("resume durable empty session: %v", err)
	}
	if len(resumed.Messages) != 0 {
		t.Fatalf("resumed new messages = %#v", resumed.Messages)
	}
}

func TestCommandExecutorClearCommitsResetBeforeMemoryMutation(t *testing.T) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	sessionID := "durable-clear"
	messages := []*schema.Message{
		{Role: schema.User, Content: "retain in audit"},
		{Role: schema.Assistant, Content: "audit answer"},
	}
	recorder := transcript.NewRecorder(sessionID, transcriptDir)
	if err := recorder.RecordLifecycleBoundary(
		transcript.LifecycleCheckpoint,
		messages,
		[]transcript.Replacement{{ToolUseID: "old-tool", Replacement: "old"}},
		map[string]transcript.FileState{
			"/tmp/old.go": {Path: "/tmp/old.go", WasRead: true},
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteSessionMetadata(
		recorder,
		&session.SessionMetadataFull{
			SessionID:          sessionID,
			QueryKernelVersion: queryKernelVersionProjectGraph,
			QueryKernelStage:   string(queryKernelStageFull),
			CreatedAt:          time.Now().UTC(),
		},
	); err != nil {
		t.Fatal(err)
	}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         sessionID,
		CWD:               root,
		TranscriptDir:     transcriptDir,
		CommandEntrypoint: commands.EntrypointPlain,
	})
	t.Cleanup(eng.Close)

	result := assertSingleCommandResultThenTerminal(
		t,
		drainRuntimeEvents(t, eng, "/clear"),
		CommandResultSucceeded,
	)
	if result.Action != commands.ActionClear || len(eng.GetMessages()) != 0 {
		t.Fatalf("clear result=%#v messages=%#v", result, eng.GetMessages())
	}
	loaded, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 0 ||
		len(loaded.Replacements) != 0 ||
		len(loaded.FileSnapshots) != 0 {
		t.Fatalf("active reset state = %#v", loaded)
	}
	if len(loaded.LifecycleBoundaries) != 2 ||
		loaded.LifecycleBoundaries[1].Kind != transcript.LifecycleReset ||
		loaded.LifecycleBoundaries[0].Messages[0].Content != "retain in audit" {
		t.Fatalf("reset audit boundaries = %#v", loaded.LifecycleBoundaries)
	}
	resumed, err := session.ResumeSession(context.Background(), session.ResumeOptions{
		SessionID:        sessionID,
		SessionDir:       transcriptDir,
		ProjectDir:       root,
		ValidateMessages: true,
	})
	if err != nil || len(resumed.Messages) != 0 {
		t.Fatalf("restart after clear: resumed=%#v err=%v", resumed, err)
	}
}

func TestCommandExecutorCompactAppendsExactlyOneDurableBoundary(t *testing.T) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	sessionID := "durable-compact"
	messages := []*schema.Message{
		{Role: schema.User, Content: strings.Repeat("question ", 200)},
		{Role: schema.Assistant, Content: strings.Repeat("answer ", 200)},
	}
	recorder := transcript.NewRecorder(sessionID, transcriptDir)
	if err := recorder.RecordLifecycleBoundary(
		transcript.LifecycleCheckpoint,
		messages,
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteSessionMetadata(
		recorder,
		&session.SessionMetadataFull{
			SessionID:          sessionID,
			QueryKernelVersion: queryKernelVersionProjectGraph,
			QueryKernelStage:   string(queryKernelStageFull),
			CreatedAt:          time.Now().UTC(),
		},
	); err != nil {
		t.Fatal(err)
	}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         sessionID,
		CWD:               root,
		TranscriptDir:     transcriptDir,
		ChatModel:         &fixedResponseModel{response: "durable summary"},
		Model:             "test-model",
		CommandEntrypoint: commands.EntrypointPlain,
	})
	t.Cleanup(eng.Close)
	eng.fileStateCache.ReadFiles["/tmp/compact.go"] = true

	result := assertSingleCommandResultThenTerminal(
		t,
		drainRuntimeEvents(t, eng, "/compact"),
		CommandResultSucceeded,
	)
	if result.Action != commands.ActionCompact {
		t.Fatalf("compact result = %#v", result)
	}
	loaded, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	var compactBoundaries int
	for _, boundary := range loaded.LifecycleBoundaries {
		if boundary.Kind == transcript.LifecycleCompact {
			compactBoundaries++
		}
	}
	if compactBoundaries != 1 {
		t.Fatalf("compact boundaries = %#v", loaded.LifecycleBoundaries)
	}
	if len(loaded.Messages) < 2 ||
		loaded.Messages[0].Extra["subtype"] != "compact_boundary" ||
		len(loaded.FileSnapshots) != 1 ||
		!loaded.FileSnapshots[0]["/tmp/compact.go"].WasRead {
		t.Fatalf("durable compact state = %#v", loaded)
	}
	restarted := NewQueryEngine(QueryEngineConfig{
		SessionID:     sessionID,
		CWD:           root,
		TranscriptDir: transcriptDir,
		Model:         "test-model",
	})
	t.Cleanup(restarted.Close)
	if got := restarted.GetMessages(); len(got) != len(loaded.Messages) ||
		got[0].Extra["subtype"] != "compact_boundary" {
		t.Fatalf("restarted compact messages = %#v", got)
	}
}

func TestP242bCommandCompactFailsBeforeProviderWithoutExactGoalTurn(
	t *testing.T,
) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	sessionID := "goal-compact-without-turn"
	recorder := transcript.NewRecorder(sessionID, transcriptDir)
	messages := []*schema.Message{
		{Role: schema.User, Content: strings.Repeat("question ", 200)},
		{Role: schema.Assistant, Content: strings.Repeat("answer ", 200)},
	}
	if err := recorder.RecordLifecycleBoundary(
		transcript.LifecycleCheckpoint,
		messages,
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteSessionMetadata(
		recorder,
		&session.SessionMetadataFull{
			SessionID:          sessionID,
			QueryKernelVersion: queryKernelVersionProjectGraph,
			QueryKernelStage:   string(queryKernelStageFull),
			CreatedAt:          time.Now().UTC(),
		},
	); err != nil {
		t.Fatal(err)
	}
	model := &canonicalScriptModel{responses: []canonicalModelResponse{{
		chunks: []*schema.Message{{
			Role:    schema.Assistant,
			Content: "must not dispatch",
			ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
				TotalTokens: 10,
			}},
		}},
	}}}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         sessionID,
		ThreadID:          sessionID,
		CWD:               root,
		TranscriptDir:     transcriptDir,
		ChatModel:         model,
		Model:             "test-model",
		CommandEntrypoint: commands.EntrypointPlain,
	})
	t.Cleanup(eng.Close)
	budget := uint64(100)
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "keep every provider call exactly accounted",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}

	result := assertSingleCommandResultThenTerminal(
		t,
		drainRuntimeEvents(t, eng, "/compact"),
		CommandResultFailed,
	)
	if !strings.Contains(result.Error, "provider accounting") ||
		!strings.Contains(result.Error, "capability is unavailable") {
		t.Fatalf("compact Goal accounting error = %#v", result)
	}
	if model.callCount != 0 {
		t.Fatalf("compact provider calls = %d, want zero", model.callCount)
	}
	state := eng.goalService.snapshot()
	if state.Status != goalStatusActive ||
		state.PendingUsageAdmission != nil ||
		state.UsageLedgerRevision != 0 ||
		state.TokensUsed != 0 {
		t.Fatalf("compact rejection mutated Goal usage = %#v", state)
	}
}

func TestIdentityChangingCommandsRejectACPBeforeMutation(t *testing.T) {
	root := t.TempDir()
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         "acp-stable-identity",
		CWD:               root,
		TranscriptDir:     filepath.Join(root, "transcripts"),
		CommandEntrypoint: commands.EntrypointACP,
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages([]*schema.Message{{Role: schema.User, Content: "keep"}})

	for _, input := range []string{"/new", "/resume some-other-session"} {
		result := assertSingleCommandResultThenTerminal(
			t,
			drainRuntimeEvents(t, eng, input),
			CommandResultUnsupported,
		)
		if !strings.Contains(result.Error, "supported entrypoint scope") {
			t.Fatalf("%s ACP rejection = %#v", input, result)
		}
	}
	if eng.SessionID() != "acp-stable-identity" ||
		len(eng.GetMessages()) != 1 {
		t.Fatalf(
			"ACP rejection mutated engine: session=%q messages=%#v",
			eng.SessionID(),
			eng.GetMessages(),
		)
	}
}

func TestClearPersistenceFailureLeavesActiveContextUntouched(t *testing.T) {
	root := t.TempDir()
	blockedDir := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blockedDir, []byte("block transcript mkdir"), 0o600); err != nil {
		t.Fatal(err)
	}
	taskManager, logicalWorkAdapter := p311bBoundLogicalWorkFixture(
		t,
		"clear-persist-failure",
	)
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "clear-persist-failure",
		CWD:                root,
		TranscriptDir:      blockedDir,
		CommandEntrypoint:  commands.EntrypointPlain,
		TaskManager:        taskManager,
		logicalWorkAdapter: logicalWorkAdapter,
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages([]*schema.Message{
		{Role: schema.User, Content: "must survive"},
	})

	result := assertSingleCommandResultThenTerminal(
		t,
		drainRuntimeEvents(t, eng, "/clear"),
		CommandResultFailed,
	)
	if !strings.Contains(result.Error, "persist reset boundary") {
		t.Fatalf("clear persistence error = %#v", result)
	}
	if got := eng.GetMessages(); len(got) != 1 ||
		got[0].Content != "must survive" {
		t.Fatalf("failed clear mutated context: %#v", got)
	}
}

func TestNewPersistenceFailureLeavesSourceIdentityUntouched(t *testing.T) {
	root := t.TempDir()
	blockedDir := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blockedDir, []byte("block transcript mkdir"), 0o600); err != nil {
		t.Fatal(err)
	}
	taskManager, logicalWorkAdapter := p311bBoundLogicalWorkFixture(
		t,
		"new-persist-failure",
	)
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "new-persist-failure",
		CWD:                root,
		TranscriptDir:      blockedDir,
		CommandEntrypoint:  commands.EntrypointPlain,
		TaskManager:        taskManager,
		logicalWorkAdapter: logicalWorkAdapter,
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages([]*schema.Message{
		{Role: schema.User, Content: "source context"},
	})

	result := assertSingleCommandResultThenTerminal(
		t,
		drainRuntimeEvents(t, eng, "/new"),
		CommandResultFailed,
	)
	if !strings.Contains(result.Error, "inspect new session transcript") {
		t.Fatalf("new persistence error = %#v", result)
	}
	if eng.SessionID() != "new-persist-failure" ||
		eng.ThreadID() != "new-persist-failure" {
		t.Fatalf(
			"failed new switched identity: session=%q thread=%q",
			eng.SessionID(),
			eng.ThreadID(),
		)
	}
	if got := eng.GetMessages(); len(got) != 1 ||
		got[0].Content != "source context" {
		t.Fatalf("failed new mutated source context: %#v", got)
	}
}

func TestCommandExecutorResumeReplacesSessionLocalStateWithDurableTargetState(t *testing.T) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	targetID := "resume-durable-target"
	targetMessages := []*schema.Message{
		{Role: schema.User, Content: "target question"},
		{
			Role:    schema.Assistant,
			Content: "target tool call",
			ToolCalls: []schema.ToolCall{{
				ID: "target-tool",
				Function: schema.FunctionCall{
					Name: "Read",
				},
			}},
		},
		{Role: schema.Tool, ToolCallID: "target-tool", Content: "target result"},
	}
	targetRecorder := transcript.NewRecorder(targetID, transcriptDir)
	if err := targetRecorder.RecordLifecycleBoundary(
		transcript.LifecycleCompact,
		targetMessages,
		[]transcript.Replacement{{
			ToolUseID:   "target-tool",
			Replacement: "target replacement",
		}},
		map[string]transcript.FileState{
			"/target.go": {
				Path:    "/target.go",
				WasRead: true,
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteSessionMetadata(
		targetRecorder,
		&session.SessionMetadataFull{
			SessionID:          targetID,
			ThreadID:           targetID,
			QueryKernelVersion: queryKernelVersionProjectGraph,
			QueryKernelStage:   string(queryKernelStageFull),
			CreatedAt:          time.Now().UTC(),
		},
	); err != nil {
		t.Fatal(err)
	}

	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         "resume-dirty-source",
		CWD:               root,
		TranscriptDir:     transcriptDir,
		CommandEntrypoint: commands.EntrypointPlain,
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages([]*schema.Message{
		{Role: schema.User, Content: "source question"},
	})
	eng.totalUsage = &schema.TokenUsage{
		PromptTokens:     99,
		CompletionTokens: 42,
	}
	eng.contentReplacementState.Replacements["source-tool"] = "source replacement"
	eng.fileStateCache.WriteFiles["/source.go"] = true
	eng.permissionDenials = append(eng.permissionDenials, permission.Denial{
		ToolName:  "Bash",
		ToolUseID: "source-tool",
	})
	eng.denialTracking.RecordDenial()
	events := drainRuntimeEvents(t, eng, "/resume "+targetID)
	result := commandResultFromEvents(t, events)
	if result.Status != CommandResultSucceeded {
		t.Fatalf("resume status = %#v", result)
	}
	if result.Action != commands.ActionResume || eng.SessionID() != targetID {
		t.Fatalf("resume result=%#v session=%q", result, eng.SessionID())
	}
	if got := eng.GetMessages(); len(got) != len(targetMessages) ||
		got[0].Content != "target question" {
		t.Fatalf("resumed messages = %#v", got)
	}
	if eng.contentReplacementState.Replacements["target-tool"] !=
		"target replacement" ||
		len(eng.contentReplacementState.Replacements) != 1 {
		t.Fatalf(
			"resumed replacement state = %#v",
			eng.contentReplacementState.Replacements,
		)
	}
	if !eng.fileStateCache.ReadFiles["/target.go"] ||
		eng.fileStateCache.WriteFiles["/source.go"] {
		t.Fatalf(
			"resumed file state: reads=%#v writes=%#v",
			eng.fileStateCache.ReadFiles,
			eng.fileStateCache.WriteFiles,
		)
	}
	if prompt, completion := eng.GetTotalUsage(); prompt != 0 || completion != 0 {
		t.Fatalf("resumed usage = (%d, %d)", prompt, completion)
	}
	if len(eng.GetPermissionDenials()) != 0 {
		t.Fatalf("resumed permission denials = %#v", eng.GetPermissionDenials())
	}
	if consecutive, total := eng.GetDenialTrackingState(); consecutive != 0 ||
		total != 0 {
		t.Fatalf("resumed denial counters = (%d, %d)", consecutive, total)
	}
}

func TestTruthfulDiagnosticsShareOneEngineOwnerAcrossEntrypoints(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	clock := func() time.Time { return time.Date(2026, 7, 22, 5, 0, 0, 0, time.UTC) }
	transcriptDir := filepath.Join(root, "transcripts")
	recorder := transcript.NewRecorder("diagnostic-entrypoints", transcriptDir)
	if err := session.WriteSessionMetadata(recorder, &session.SessionMetadataFull{
		SessionID: "diagnostic-entrypoints", ThreadID: "diagnostic-entrypoints",
		QueryKernelVersion: queryKernelVersionProjectGraph,
		QueryKernelStage:   string(queryKernelStageFull),
		CreatedAt:          clock(),
	}); err != nil {
		t.Fatalf("prepare diagnostic metadata: %v", err)
	}
	if err := recorder.RecordLifecycleBoundaryWithUsage(
		transcript.LifecycleSessionStart,
		nil,
		nil,
		nil,
		transcript.UsageSummary{},
		true,
	); err != nil {
		t.Fatalf("prepare diagnostic transcript: %v", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("close diagnostic transcript: %v", err)
	}
	if err := os.Chtimes(recorder.Path(), clock(), clock()); err != nil {
		t.Fatalf("fix diagnostic transcript time: %v", err)
	}
	entrypoints := []commands.Entrypoint{
		commands.EntrypointTUI,
		commands.EntrypointPlain,
		commands.EntrypointHeadless,
		commands.EntrypointACP,
	}
	inputs := []string{"/status", "/context", "/usage", "/config", "/doctor"}
	wantOutputs := make(map[string]string, len(inputs))
	observedPattern := regexp.MustCompile(`observed=[^;\]]+`)
	for _, entrypoint := range entrypoints {
		for _, input := range inputs {
			eng := NewQueryEngine(QueryEngineConfig{
				SessionID: "diagnostic-entrypoints", TranscriptDir: transcriptDir,
				CWD: root, Model: "gpt-4o", Clock: clock, CommandEntrypoint: entrypoint,
				ModelResolver: ModelResolverFunc(func(string) (provider.ResolvedConfig, error) {
					return provider.ResolvedConfig{
						Config:  provider.Config{Provider: provider.ProviderAgenticOpenAI, Model: "gpt-4o"},
						Sources: provider.ResolutionSources{Provider: "explicit", Model: "explicit"},
					}, nil
				}),
			})
			events := drainRuntimeEvents(t, eng, input)
			result := commandResultFromEvents(t, events)
			eng.Close()
			if result.Status != CommandResultSucceeded || result.Action != commands.ActionNone {
				t.Fatalf("%s %s result = %#v", entrypoint, input, result)
			}
			normalizedOutput := observedPattern.ReplaceAllString(result.Output, "observed=<freshness>")
			if want, exists := wantOutputs[input]; exists {
				if normalizedOutput != want {
					t.Fatalf("%s output differs for %s\n--- got ---\n%s\n--- want ---\n%s", input, entrypoint, result.Output, want)
				}
			} else {
				wantOutputs[input] = normalizedOutput
			}
		}
	}
}

func TestCommandExecutorPermissionMutationPersistsOnce(t *testing.T) {
	root := t.TempDir()
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         "command-permission-once",
		CWD:               root,
		TranscriptDir:     filepath.Join(root, "transcripts"),
		CommandEntrypoint: commands.EntrypointPlain,
	})
	t.Cleanup(eng.Close)

	events := drainRuntimeEvents(
		t,
		eng,
		`/permissions add allow "Read(/tmp/*)" --local`,
	)
	result := assertSingleCommandResultThenTerminal(
		t,
		events,
		CommandResultSucceeded,
	)
	if result.Action != commands.ActionPermissions {
		t.Fatalf("permission action = %q", result.Action)
	}
	rules, err := permission.LoadPermissionRules(root)
	if err != nil {
		t.Fatal(err)
	}
	var matching int
	for _, rule := range rules {
		if rule.Action == permission.ActionAllow &&
			permission.FormatRuleString(rule.ToolName, rule.InputPattern) == "Read(/tmp/*)" {
			matching++
		}
	}
	if matching != 1 {
		t.Fatalf("matching persisted rules = %d, rules=%#v", matching, rules)
	}
}

func TestDeferredHistoryCommandFailsBeforeMutation(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:         "command-deferred-history",
		CWD:               t.TempDir(),
		TranscriptDir:     t.TempDir(),
		CommandEntrypoint: commands.EntrypointPlain,
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages([]*schema.Message{
		{Role: schema.User, Content: "keep me"},
		{Role: schema.Assistant, Content: "still here"},
	})

	events := drainRuntimeEvents(t, eng, "/undo")
	assertSingleCommandResultThenTerminal(t, events, CommandResultUnsupported)
	if got := eng.GetMessages(); len(got) != 2 ||
		got[0].Content != "keep me" ||
		got[1].Content != "still here" {
		t.Fatalf("deferred history command mutated messages: %#v", got)
	}
}

func assertSingleCommandResultThenTerminal(
	t *testing.T,
	events []QueryEvent,
	status CommandResultStatus,
) *CommandResultEvent {
	t.Helper()
	if len(events) != 2 ||
		events[0].Type != EventCommandResult ||
		events[1].Type != EventTerminal {
		t.Fatalf("command event order = %#v", events)
	}
	result := events[0].CommandResult
	if result == nil || result.Status != status {
		t.Fatalf("command result = %#v, want status %q", result, status)
	}
	return result
}

func commandResultFromEvents(t *testing.T, events []QueryEvent) *CommandResultEvent {
	t.Helper()
	var result *CommandResultEvent
	for _, event := range events {
		if event.Type == EventCommandResult {
			if result != nil {
				t.Fatal("received duplicate command result events")
			}
			result = event.CommandResult
		}
	}
	if result == nil {
		t.Fatal("missing command result event")
	}
	return result
}
