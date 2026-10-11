package engine

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/session"
)

func TestGoalProviderUsageLifecycleProjection(t *testing.T) {
	for _, test := range []struct {
		name     string
		budget   uint64
		settle   func(execution.ProviderUsageCall) error
		status   string
		coverage string
		tokens   uint64
		ledger   uint64
		pending  bool
	}{
		{"recorded", 100, func(call execution.ProviderUsageCall) error {
			return call.CompleteProviderUsage(&schema.TokenUsage{TotalTokens: 25})
		}, "active", "complete", 25, 1, false},
		{"budget-limited", 25, func(call execution.ProviderUsageCall) error {
			return call.CompleteProviderUsage(&schema.TokenUsage{TotalTokens: 25})
		}, "budget_limited", "complete", 25, 1, false},
		{"usage-limited", 100, func(call execution.ProviderUsageCall) error {
			return call.MarkProviderUsageAmbiguous(errors.New("fixture provider failure"))
		}, "usage_limited", "pending", 0, 0, true},
		{"admission-released", 100, func(call execution.ProviderUsageCall) error {
			return call.ReleaseProviderUsageBeforeDispatch()
		}, "active", "complete", 0, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
			eng := newP241GoalEngine(t, QueryEngineConfig{Clock: func() time.Time { return now }})
			if _, err := eng.goalService.create(goalCreateRequest{Objective: "project usage settlement", TokenBudget: &test.budget}); err != nil {
				t.Fatal(err)
			}
			events := make(chan QueryEvent, 16)
			emitter := beginP242aGoalTurn(t, eng, events, "projection-turn", false, now)
			t.Cleanup(func() {
				emitter.Close()
				eng.goalService.abandonTurn("projection-turn")
				eng.endPlanTurn("projection-turn")
			})
			reporter := eng.currentGoalProviderUsageReporter()
			call, err := reporter.AdmitProviderUsage(t.Context(), execution.ProviderUsageDescriptor{
				LogicalRoundID: reporter.NewLogicalRoundID(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := test.settle(call); err != nil {
				t.Fatal(err)
			}
			if err := eng.RuntimeStateError(); err != nil {
				t.Fatalf("durable usage settlement was rejected by the runtime projection: %v", err)
			}
			projected := eng.RuntimeSnapshot().Threads[eng.ThreadID()].Goal
			if projected == nil || projected.Status != test.status || projected.UsageCoverage != test.coverage ||
				projected.TokensUsed != test.tokens || projected.UsageLedgerRevision != test.ledger ||
				(projected.PendingUsageAdmission != nil) != test.pending {
				t.Fatalf("projected usage settlement = %#v", projected)
			}
			if projected.Revision != eng.goalService.snapshot().Revision {
				t.Fatal("runtime projection did not reach the durable Goal revision")
			}
			if !emitter.Emit(QueryEvent{Type: EventTerminal, TerminalInfo: &Terminal{Reason: TerminalModelError}}) {
				t.Fatal("settled Goal terminal was not delivered")
			}
			for len(events) > 0 {
				event := <-events
				if event.Type == EventTerminal && event.TerminalInfo.Reason != TerminalModelError {
					t.Fatalf("Goal terminal became %s: %v", event.TerminalInfo.Reason, event.TerminalInfo.Err)
				}
			}
			if err := eng.RuntimeStateError(); err != nil {
				t.Fatalf("Goal terminal projection: %v", err)
			}
		})
	}
}

func TestGoalTerminalProjectionReportsCurrentRejection(t *testing.T) {
	now := time.Date(2026, 10, 9, 2, 0, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{Clock: func() time.Time { return now }})
	if _, err := eng.goalService.create(goalCreateRequest{Objective: "retain the rejection cause"}); err != nil {
		t.Fatal(err)
	}
	events := make(chan QueryEvent, 16)
	emitter := beginP242aGoalTurn(t, eng, events, "rejected-terminal-turn", false, now)
	t.Cleanup(func() {
		emitter.Close()
		eng.endPlanTurn("rejected-terminal-turn")
	})
	// Inject a projection conflict, not a persistence failure. Preserve an
	// earlier diagnostic so reading RuntimeStateError cannot supply this cause.
	first := errors.New("earlier fixture rejection")
	eng.runtimeMu.Lock()
	eng.runtimeStateErr = first
	eng.runtimeMu.Unlock()
	threadID := eng.ThreadID()
	eng.runtimeState.mu.Lock()
	eng.runtimeState.threads[threadID].Goal.Revision += 100
	eng.runtimeState.mu.Unlock()
	if !emitter.Emit(QueryEvent{Type: EventTerminal, TerminalInfo: &Terminal{Reason: TerminalModelError}}) {
		t.Fatal("fail-closed terminal was not delivered")
	}
	for len(events) > 0 {
		event := <-events
		if event.Type != EventTerminal {
			continue
		}
		if event.TerminalInfo.Reason != TerminalPersistenceError || event.TerminalInfo.Err == nil ||
			!strings.Contains(event.TerminalInfo.Err.Error(), "Goal revision") {
			t.Fatalf("terminal did not carry its actual reducer rejection: %#v", event.TerminalInfo)
		}
		if !errors.Is(eng.RuntimeStateError(), first) {
			t.Fatal("current terminal rejection replaced the first runtime diagnostic")
		}
		return
	}
	t.Fatal("missing fail-closed terminal")
}

func TestGoalChildUsageSettlementAfterRootTerminal(t *testing.T) {
	for _, test := range []struct {
		name   string
		budget uint64
		settle func(execution.ProviderUsageCall) error
	}{
		{"recorded", 100, func(call execution.ProviderUsageCall) error {
			return call.CompleteProviderUsage(&schema.TokenUsage{TotalTokens: 25})
		}},
		{"budget-limited", 25, func(call execution.ProviderUsageCall) error {
			return call.CompleteProviderUsage(&schema.TokenUsage{TotalTokens: 25})
		}},
		{"usage-limited", 100, func(call execution.ProviderUsageCall) error {
			return call.MarkProviderUsageAmbiguous(errors.New("fixture child provider failure"))
		}},
		{"admission-released", 100, func(call execution.ProviderUsageCall) error {
			return call.ReleaseProviderUsageBeforeDispatch()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 10, 9, 4, 0, 0, 0, time.UTC)
			eng := newP241GoalEngine(t, QueryEngineConfig{Clock: func() time.Time { return now }})
			if _, err := eng.goalService.create(goalCreateRequest{Objective: "settle late child usage", TokenBudget: &test.budget}); err != nil {
				t.Fatal(err)
			}
			events := make(chan QueryEvent, 16)
			emitter := beginP242aGoalTurn(t, eng, events, "late-usage-turn", false, now)
			t.Cleanup(emitter.Close)
			child := eng.bindGoalUsageReporterForChild(eng.currentGoalExecutionIdentity(), goalUsageSubject{
				SessionID: "child-session", ThreadID: "child-thread", AgentID: "child-agent", AgentGeneration: 7,
			})
			call, err := child.AdmitProviderUsage(t.Context(), execution.ProviderUsageDescriptor{
				LogicalRoundID: child.NewLogicalRoundID(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if !emitter.Emit(QueryEvent{Type: EventTerminal, TerminalInfo: &Terminal{Reason: TerminalCompleted}}) {
				t.Fatal("root terminal was not delivered")
			}
			eng.endPlanTurn("late-usage-turn")
			before := eng.RuntimeSnapshot().Threads[eng.ThreadID()]
			if before.Status != RuntimeThreadCompleted || before.LastTerminal == nil || before.LastTerminal.Reason != TerminalCompleted {
				t.Fatalf("root did not complete before child settlement: %#v", before.LastTerminal)
			}
			now = now.Add(time.Second)
			if err := test.settle(call); err != nil {
				t.Fatal(err)
			}
			if err := eng.RuntimeStateError(); err != nil {
				t.Fatalf("late child usage settlement was rejected: %v", err)
			}
			after := eng.RuntimeSnapshot().Threads[eng.ThreadID()]
			if after.Goal == nil || after.Goal.Revision != eng.goalService.snapshot().Revision || after.LastSequence != before.LastSequence+1 {
				t.Fatal("late child usage did not reach the durable revision and next sequence")
			}
			assertGoalUsagePreservesThreadExecution(t, before, after)
			if items := eng.RuntimeItems(); len(items) != 0 {
				t.Fatalf("diagnostic usage settlement enqueued runtime work: %#v", items)
			}
		})
	}
}

func TestGoalUsageLifecycleReplayAndInvalidEvidence(t *testing.T) {
	base := time.Date(2026, 10, 9, 3, 0, 0, 0, time.UTC)
	created := GoalSnapshot{
		GoalID: "usage-goal", Objective: "replay usage facts", ObjectiveRevision: 1,
		Status: "active", Revision: 1, UsageCoverage: "complete",
		CreatedAt: base, UpdatedAt: base, Available: true,
	}
	started := cloneGoalSnapshot(created)
	started.Revision, started.LastGoalTurnID, started.UpdatedAt = 2, "usage-turn", base.Add(time.Second)
	pending := &GoalUsageAdmissionSnapshot{
		Version: session.PersistedGoalUsageAdmissionVersion, LedgerRevision: 1,
		GoalID: "usage-goal", ObjectiveRevision: 1, RootSessionID: "root", RootThreadID: "root",
		ExecutingSessionID: "root", ExecutingThreadID: "root", GoalTurnID: "usage-turn",
		LogicalRoundID: "round-1", ProviderCallID: "call-1", AdmittedAt: base.Add(2 * time.Second),
	}
	for _, phase := range []GoalLifecyclePhase{
		GoalLifecycleUsageRecorded, GoalLifecycleUsageAdmissionReleased,
		GoalLifecycleUsageLimited, GoalLifecycleBudgetLimited,
	} {
		t.Run(string(phase), func(t *testing.T) {
			settled := cloneGoalSnapshot(started)
			settled.Revision, settled.UpdatedAt = 3, base.Add(3*time.Second)
			switch phase {
			case GoalLifecycleUsageRecorded, GoalLifecycleBudgetLimited:
				settled.TokensUsed, settled.UsageLedgerRevision = 25, 1
				if phase == GoalLifecycleBudgetLimited {
					settled.Status, settled.TokenBudget = "budget_limited", uint64Pointer(25)
				}
			case GoalLifecycleUsageLimited:
				settled.Status, settled.UsageCoverage, settled.PendingUsageAdmission = "usage_limited", "pending", pending
			}
			prefix := []QueryEvent{
				p242aRuntimeGoalEvent(1, "external-goal", "", base, GoalLifecycleCreated, created),
				p242aRuntimeGoalEvent(2, "usage-turn", "usage-turn", started.UpdatedAt, GoalLifecycleTurnStarted, started),
			}
			event := p242aRuntimeGoalEvent(3, "usage-turn", "usage-turn", settled.UpdatedAt, phase, settled)
			trace := append(append([]QueryEvent(nil), prefix...), event)
			live, replay := NewRuntimeStateStore(), NewRuntimeStateStore()
			for _, next := range trace {
				if err := live.Apply(next); err != nil {
					t.Fatal(err)
				}
			}
			if err := replay.Replay(trace); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(live.Snapshot("root"), replay.Snapshot("root")) {
				t.Fatal("usage lifecycle live/replay mismatch")
			}
			for _, state := range []string{"terminal", "another-active-turn", "missing-thread"} {
				t.Run(state, func(t *testing.T) {
					store := NewRuntimeStateStore()
					var preceding []QueryEvent
					if state != "missing-thread" {
						finished := cloneGoalSnapshot(started)
						finished.Revision, finished.LastTerminalSequence = 3, 4
						finished.UpdatedAt = base.Add(2 * time.Second)
						preceding = append(append([]QueryEvent(nil), prefix...),
							p242aRuntimeGoalEvent(3, "usage-turn", "usage-turn", finished.UpdatedAt, GoalLifecycleTurnFinished, finished),
							QueryEvent{
								RuntimeEventEnvelope: RuntimeEventEnvelope{SessionID: "root", ThreadID: "root", TurnID: "usage-turn", Sequence: 4, Timestamp: finished.UpdatedAt},
								Type:                 EventTerminal, TerminalInfo: &Terminal{Reason: TerminalCompleted},
							})
						if state == "another-active-turn" {
							preceding = append(preceding, QueryEvent{
								RuntimeEventEnvelope: RuntimeEventEnvelope{SessionID: "root", ThreadID: "root", TurnID: "new-turn", Sequence: 5, Timestamp: finished.UpdatedAt},
								Type:                 EventStream, StreamEvent: &schema.Message{Role: schema.Assistant, Content: "new turn is streaming"},
							})
						}
					}
					if err := store.Replay(preceding); err != nil {
						t.Fatal(err)
					}
					before := store.Snapshot("root")
					late := p242aRuntimeGoalEvent(uint64(len(preceding)+1), "usage-turn", "usage-turn", settled.UpdatedAt, phase, cloneGoalSnapshot(settled))
					if state != "missing-thread" {
						late.GoalLifecycle.Goal.Revision = 4
						late.GoalLifecycle.Goal.LastTerminalSequence = 4
					}
					if err := store.Apply(late); err != nil {
						t.Fatalf("out-of-band usage lifecycle: %v", err)
					}
					after := store.Snapshot("root")
					thread := after.Threads["root"]
					if thread.Goal == nil || thread.Goal.Revision != late.GoalLifecycle.Goal.Revision || thread.LastSequence != late.Sequence {
						t.Fatal("out-of-band usage projection did not advance")
					}
					if state == "missing-thread" {
						if thread.Status != RuntimeThreadCompleted || thread.ActiveTurnID != "" || !thread.ActiveSince.IsZero() || thread.LastTerminal != nil {
							t.Fatalf("diagnostic usage created a phantom running turn: %#v", thread)
						}
					} else {
						assertGoalUsagePreservesThreadExecution(t, before.Threads["root"], thread)
					}
					if !reflect.DeepEqual(before.Agents, after.Agents) {
						t.Fatal("diagnostic usage changed Agent execution state")
					}
					fresh := NewRuntimeStateStore()
					if err := fresh.Replay(append(preceding, late)); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(after, fresh.Snapshot("root")) {
						t.Fatal("out-of-band usage live/replay mismatch")
					}
				})
			}
			for _, mutation := range []string{"unknown-phase", "missing-turn", "conflicting-evidence", "incorrect-coverage"} {
				t.Run(mutation, func(t *testing.T) {
					store := NewRuntimeStateStore()
					if err := store.Replay(prefix); err != nil {
						t.Fatal(err)
					}
					invalid := p242aRuntimeGoalEvent(3, "usage-turn", "usage-turn", settled.UpdatedAt, phase, cloneGoalSnapshot(settled))
					switch mutation {
					case "unknown-phase":
						invalid.GoalLifecycle.Phase = "unknown-usage"
					case "missing-turn":
						invalid.GoalTurnID, invalid.GoalLifecycle.Goal.LastGoalTurnID = "", ""
					case "incorrect-coverage":
						invalid.GoalLifecycle.Goal.UsageCoverage = "unavailable"
					case "conflicting-evidence":
						if phase == GoalLifecycleUsageLimited {
							invalid.GoalLifecycle.Goal.Status = "active"
						} else if phase == GoalLifecycleBudgetLimited {
							invalid.GoalLifecycle.Goal.TokensUsed = 24
						} else {
							invalid.GoalLifecycle.Goal.PendingUsageAdmission = pending
							invalid.GoalLifecycle.Goal.TokensUsed, invalid.GoalLifecycle.Goal.UsageLedgerRevision = 0, 0
						}
					}
					before := store.Snapshot("root")
					if err := store.Apply(invalid); err == nil {
						t.Fatal("invalid usage lifecycle was accepted")
					}
					if !reflect.DeepEqual(before, store.Snapshot("root")) {
						t.Fatal("rejected usage lifecycle mutated the runtime projection")
					}
				})
			}
			if phase == GoalLifecycleUsageLimited {
				store := NewRuntimeStateStore()
				if err := store.Replay(prefix); err != nil {
					t.Fatal(err)
				}
				invalid := p242aRuntimeGoalEvent(3, "usage-turn", "usage-turn", settled.UpdatedAt, phase, cloneGoalSnapshot(settled))
				invalid.GoalLifecycle.Goal.PendingUsageAdmission = nil
				invalid.GoalLifecycle.Goal.UsageCoverage = "incomplete"
				before := store.Snapshot("root")
				if err := store.Apply(invalid); err == nil {
					t.Fatal("usage limitation without the producer's pending admission was accepted")
				}
				if !reflect.DeepEqual(before, store.Snapshot("root")) {
					t.Fatal("rejected usage limitation mutated the runtime projection")
				}
			}
		})
	}
}

func assertGoalUsagePreservesThreadExecution(t *testing.T, before, after RuntimeThreadSnapshot) {
	t.Helper()
	before.Goal, after.Goal = nil, nil
	before.Revision, after.Revision = 0, 0
	before.LastSequence, after.LastSequence = 0, 0
	before.UpdatedAt, after.UpdatedAt = time.Time{}, time.Time{}
	before.Events, after.Events = nil, nil
	if !reflect.DeepEqual(before, after) {
		t.Fatal("diagnostic usage lifecycle changed thread execution state")
	}
}
