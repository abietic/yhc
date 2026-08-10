package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
)

type p244GoalContinuationModel struct{}

func (p244GoalContinuationModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return p244GoalContinuationResponse(), nil
}

func (p244GoalContinuationModel) Stream(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray(
		[]*schema.Message{p244GoalContinuationResponse()},
	), nil
}

func p244GoalContinuationResponse() *schema.Message {
	return &schema.Message{
		Role:    schema.Assistant,
		Content: "one bounded Goal step completed",
		ResponseMeta: &schema.ResponseMeta{
			FinishReason: "stop",
			Usage: &schema.TokenUsage{
				PromptTokens:     8,
				CompletionTokens: 2,
				TotalTokens:      10,
			},
		},
	}
}

func TestP244StatusProjectsGoalFromRuntimeReducer(t *testing.T) {
	const threadID = "goal-status-thread"
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:         threadID,
		ThreadID:          threadID,
		CWD:               t.TempDir(),
		CommandEntrypoint: commands.EntrypointTUI,
		GoalCapability: &engine.GoalCapabilityConfig{
			Enabled: true,
		},
	})
	t.Cleanup(eng.Close)
	events, _ := eng.SubmitMessage(
		context.Background(),
		"/goal finish the migration ledger",
	)
	for range events {
	}
	app := New(Config{Engine: eng, Model: "test-model", ReducedMotion: true})
	app.width = 180
	app.height = 30
	app.state = StateChat
	app.updateLayout()

	if projection := app.goalStatusProjection(); projection !=
		"goal active 0 active:0s [finish the migration ledger]" {
		t.Fatalf("Goal status projection = %q", projection)
	}
	if status := stripANSIForTest(app.renderStatus()); !strings.Contains(
		status,
		"goal active 0 active:0s",
	) {
		t.Fatalf("status omitted reducer-owned Goal progress:\n%s", status)
	}
}

func TestP244GoalStatusProjectsDurableActiveTime(t *testing.T) {
	const threadID = "goal-active-time-thread"
	at := time.Date(2026, 7, 29, 7, 10, 0, 0, time.UTC)
	budget := uint64(200_000)
	store := engine.NewRuntimeStateStore()
	if err := store.Apply(engine.QueryEvent{
		Type: engine.EventGoalLifecycle,
		RuntimeEventEnvelope: engine.RuntimeEventEnvelope{
			SessionID:             threadID,
			ThreadID:              threadID,
			TurnID:                "goal-control-turn",
			Sequence:              1,
			Timestamp:             at,
			GoalID:                "11111111-1111-4111-8111-111111111111",
			GoalObjectiveRevision: 1,
			GoalRootSessionID:     threadID,
			GoalRootThreadID:      threadID,
		},
		GoalLifecycle: &engine.GoalLifecycleEvent{
			Phase: engine.GoalLifecycleCreated,
			Goal: engine.GoalSnapshot{
				GoalID:               "11111111-1111-4111-8111-111111111111",
				Objective:            "project durable Goal time",
				ObjectiveRevision:    1,
				Status:               "active",
				Revision:             1,
				TokenBudget:          &budget,
				UsageCoverage:        "complete",
				RootActiveTimeMillis: 90_500,
				CreatedAt:            at,
				UpdatedAt:            at,
				Available:            true,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:    threadID,
		ThreadID:     threadID,
		CWD:          t.TempDir(),
		RuntimeState: store,
	})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng, ReducedMotion: true})
	if projection := app.goalStatusProjection(); projection !=
		"goal active 0/200.0k active:1m30s [project durable Goal time]" {
		t.Fatalf("durable Goal active-time projection = %q", projection)
	}
}

func TestP244GoalSignalMakesIdleTUIClaimAndSubmitDedicatedContinuation(
	t *testing.T,
) {
	const threadID = "goal-wake-thread"
	dir := t.TempDir()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:         threadID,
		ThreadID:          threadID,
		CWD:               dir,
		TranscriptDir:     dir,
		CommandEntrypoint: commands.EntrypointTUI,
		ChatModel:         p244GoalContinuationModel{},
		Model:             "p24-4-test-provider",
		GoalCapability: &engine.GoalCapabilityConfig{
			Enabled: true,
		},
	})
	t.Cleanup(eng.Close)
	for range mustSubmitP244TUIMessage(
		t,
		eng,
		"/goal finish the migration ledger",
	) {
	}
	for range mustSubmitP244TUIMessage(t, eng, "perform one Goal step") {
	}
	items := eng.RuntimeItems()
	if len(items) != 1 ||
		items[0].Kind != engine.RuntimeItemGoalContinuation {
		t.Fatalf("eligible Goal turn did not enqueue one continuation: %#v", items)
	}
	firstItemID := items[0].ID

	app := New(Config{
		Engine:        eng,
		Model:         "p24-4-test-provider",
		Resumed:       true,
		ReducedMotion: true,
	})
	wait := app.waitForGoalContinuation()
	if wait == nil {
		t.Fatal("enabled TUI Goal subscription is unavailable")
	}
	ready := wait()
	_, scheduled := app.Update(ready)
	if scheduled == nil {
		t.Fatal("Goal signal did not schedule runtime work")
	}
	batch, ok := scheduled().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("Goal ready message scheduled %#v", scheduled())
	}
	start, ok := batch[0]().(startGoalContinuationMsg)
	if !ok {
		t.Fatalf("Goal ready first schedule = %T", batch[0]())
	}
	_, submit := app.Update(start)
	if submit == nil || !app.running {
		t.Fatalf(
			"Goal continuation was not claimed: submit=%v running=%v",
			submit != nil,
			app.running,
		)
	}
	started := p244EngineStartMessage(t, submit)
	var terminalSeen bool
	for event := range started.events {
		terminalSeen = terminalSeen || event.Type == engine.EventTerminal
	}
	if !terminalSeen {
		t.Fatal("Goal continuation emitted no terminal event")
	}
	items = eng.RuntimeItems()
	if len(items) != 1 ||
		items[0].Kind != engine.RuntimeItemGoalContinuation ||
		items[0].ID == firstItemID {
		t.Fatalf(
			"dedicated submit did not settle once and advance once: %#v",
			items,
		)
	}
}

func p244EngineStartMessage(t *testing.T, command tea.Cmd) engineStartMsg {
	t.Helper()
	message := command()
	if started, ok := message.(engineStartMsg); ok {
		return started
	}
	batch, ok := message.(tea.BatchMsg)
	if !ok {
		t.Fatalf("Goal continuation submit = %T", message)
	}
	for _, child := range batch {
		if child == nil {
			continue
		}
		if started, ok := child().(engineStartMsg); ok {
			return started
		}
	}
	t.Fatalf("Goal continuation batch contains no engineStartMsg: %#v", batch)
	return engineStartMsg{}
}

func mustSubmitP244TUIMessage(
	t *testing.T,
	eng *engine.QueryEngine,
	input string,
) <-chan engine.QueryEvent {
	t.Helper()
	events, terminal := eng.SubmitMessage(context.Background(), input)
	if terminal.Err != nil {
		t.Fatalf("submit %q: %v", input, terminal.Err)
	}
	return events
}
