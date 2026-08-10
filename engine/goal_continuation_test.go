package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/session"
	"github.com/cloudwego/eino/schema"
)

func TestP243EligibleTerminalPersistsDormantContinuation(t *testing.T) {
	now := time.Date(2026, 7, 29, 5, 0, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	budget := uint64(10_000)
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "finish P24.3",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}
	signal := eng.SubscribeRuntimeItems()
	finishP243EligibleTurn(t, eng, "p24-3-predecessor", &now)

	state := eng.goalService.snapshot()
	if state == nil ||
		state.Status != goalStatusActive ||
		state.Continuation == nil ||
		state.Continuation.Disposition != goalContinuationDispositionPending ||
		state.ContinuationOrdinal != 1 ||
		state.Continuation.ContinuationOrdinal != 1 ||
		state.Continuation.GoalRevision != state.Revision ||
		state.Continuation.PredecessorGoalTurnID != "p24-3-predecessor" ||
		state.Continuation.PredecessorTerminalReason != string(TerminalCompleted) {
		t.Fatalf("Goal continuation cursor = %#v", state)
	}
	items := eng.RuntimeItems()
	if len(items) != 1 ||
		!runtimeGoalContinuationMatchesCursor(items[0], state.Continuation) ||
		items[0].Priority != RuntimePriorityLater ||
		items[0].State != RuntimeItemPending {
		t.Fatalf("dormant Goal continuation items = %#v", items)
	}
	select {
	case <-signal:
		t.Fatal("dormant Goal continuation signalled a production transport")
	default:
	}
	if item, ok, err := eng.ClaimNextRuntimeItem(); err != nil || ok {
		t.Fatalf("generic idle claim returned Goal continuation: item=%#v ok=%v err=%v", item, ok, err)
	}
	coordinator, scope, err := eng.runtimeInputOwner()
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := coordinator.ClaimSafePoint(scope, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 0 {
		t.Fatalf("generic safe point claimed Goal continuation: %#v", claimed)
	}
	assertP241PersistedGoal(t, eng, func(record *session.PersistedGoalState) {
		if record.Version != session.PersistedGoalStateVersion ||
			record.Continuation == nil ||
			record.Continuation.ItemID != state.Continuation.ItemID ||
			record.ContinuationOrdinal != 1 {
			t.Fatalf("persisted Goal continuation = %#v", record)
		}
	})
}

func TestP49OptionalBudgetContinuationRoundTrip(t *testing.T) {
	state, cursor, item, scope := newP49OptionalContinuationFixture(t)
	if cursor.Version != session.PersistedGoalContinuationVersion ||
		cursor.GoalSchemaVersion != session.PersistedGoalStateVersion ||
		cursor.TokenBudget != nil ||
		item.GoalContinuation == nil ||
		item.GoalContinuation.Version != runtimeGoalContinuationVersion ||
		item.GoalContinuation.TokenBudget != nil {
		t.Fatalf("optional-budget continuation = cursor=%#v item=%#v", cursor, item)
	}
	persisted := persistedGoalContinuation(cursor)
	restoredCursor := goalContinuationFromPersisted(persisted)
	if err := validateGoalContinuationCursor(restoredCursor); err != nil {
		t.Fatalf("restored optional-budget cursor: %v", err)
	}
	if !runtimeGoalContinuationMatchesCursor(item, restoredCursor) {
		t.Fatalf("runtime payload does not match restored cursor: %#v", item)
	}

	path := filepath.Join(t.TempDir(), "runtime-inputs.json")
	coordinator, err := NewRuntimeInputCoordinator(
		RuntimeInputCoordinatorConfig{
			SessionID: scope.SessionID,
			ThreadID:  scope.ThreadID,
			Path:      path,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.enqueueDormantGoalContinuation(item); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewRuntimeInputCoordinator(
		RuntimeInputCoordinatorConfig{
			SessionID: scope.SessionID,
			ThreadID:  scope.ThreadID,
			Path:      path,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := recovered.claimGoalContinuation(item.ID, scope)
	if err != nil || !ok {
		t.Fatalf("claim restored optional-budget item: item=%#v ok=%v err=%v", claimed, ok, err)
	}
	if err := validateGoalContinuationAdmission(
		state,
		claimed,
		scope,
		cursor.ContinuationTurnID,
	); err != nil {
		t.Fatalf("admit restored optional-budget continuation: %v", err)
	}
	if duplicate, ok, err := recovered.claimGoalContinuation(item.ID, scope); err != nil || ok {
		t.Fatalf("duplicate optional-budget claim: item=%#v ok=%v err=%v", duplicate, ok, err)
	}
}

func TestP49UnbudgetedGoalContinuesExactlyOnce(t *testing.T) {
	now := time.Date(2026, 8, 7, 8, 0, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective: "continue exactly once without a limiter",
	}); err != nil {
		t.Fatal(err)
	}
	finishP243EligibleTurn(t, eng, "p49-unbudgeted-predecessor", &now)
	state := eng.goalService.snapshot()
	if state.Status != goalStatusActive || state.TokenBudget != nil ||
		state.Continuation == nil ||
		state.Continuation.Version != session.PersistedGoalContinuationVersion ||
		state.Continuation.TokenBudget != nil {
		t.Fatalf("unbudgeted continuation state = %#v", state)
	}
	item, ok, err := eng.claimGoalContinuation()
	if err != nil || !ok || item.GoalContinuation == nil ||
		item.GoalContinuation.Version != runtimeGoalContinuationVersion ||
		item.GoalContinuation.TokenBudget != nil {
		t.Fatalf("unbudgeted continuation claim = item=%#v ok=%v err=%v", item, ok, err)
	}
	if duplicate, duplicateOK, duplicateErr := eng.claimGoalContinuation(); duplicateErr != nil || duplicateOK {
		t.Fatalf(
			"duplicate unbudgeted continuation claim = item=%#v ok=%v err=%v",
			duplicate,
			duplicateOK,
			duplicateErr,
		)
	}
}

func TestP49OptionalBudgetContinuationRejectsMutation(t *testing.T) {
	_, _, base, _ := newP49OptionalContinuationFixture(t)
	budget := uint64(100)
	tests := []struct {
		name   string
		mutate func(*RuntimeItem)
	}{
		{
			name: "budget presence",
			mutate: func(item *RuntimeItem) {
				item.GoalContinuation.TokenBudget = &budget
			},
		},
		{
			name: "payload version",
			mutate: func(item *RuntimeItem) {
				item.GoalContinuation.Version = runtimeGoalContinuationLegacyVersion
			},
		},
		{
			name: "scope",
			mutate: func(item *RuntimeItem) {
				item.Scope.ThreadID = "other-thread"
			},
		},
		{
			name: "item ID",
			mutate: func(item *RuntimeItem) {
				item.ID += "-other"
			},
		},
		{
			name: "checkpoint ID",
			mutate: func(item *RuntimeItem) {
				item.GoalContinuation.CheckpointID += "-other"
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			item := cloneRuntimeItem(base)
			tc.mutate(&item)
			if err := validateRuntimeGoalContinuation(
				item.ID,
				item.Scope,
				*item.GoalContinuation,
			); err == nil {
				t.Fatalf("mutated continuation was accepted: %#v", item)
			}
		})
	}
}

func TestP49LegacyBudgetedContinuationKeepsIdentity(t *testing.T) {
	budget := uint64(1_000)
	terminalAt := time.Date(2026, 8, 7, 5, 0, 0, 0, time.UTC)
	suffix := "5aff43386e7dc0619515b1c82ec3bcd09c01d51583d4a777d69a19f79ecf12cf"
	cursor := &goalContinuationCursor{
		Version:                     session.PersistedGoalContinuationLegacyVersion,
		ItemID:                      "goal-continuation:" + suffix,
		CheckpointID:                "goal-continuation-checkpoint:" + suffix,
		ContinuationTurnID:          "goal-continuation-turn:" + suffix,
		GoalID:                      "goal-legacy",
		GoalSchemaVersion:           session.PersistedGoalStateContinuationVersion,
		ObjectiveRevision:           1,
		GoalRevision:                7,
		GoalStatus:                  string(goalStatusActive),
		RootSessionID:               "root",
		RootThreadID:                "root",
		PredecessorGoalTurnID:       "turn-legacy",
		PredecessorTerminalSequence: 9,
		PredecessorTerminalReason:   string(TerminalCompleted),
		PredecessorTerminalAt:       terminalAt,
		TokenBudget:                 &budget,
		TokensUsed:                  25,
		UsageLedgerRevision:         2,
		ContinuationOrdinal:         3,
		RuntimeRevision:             11,
		Disposition:                 goalContinuationDispositionPending,
	}
	if err := validateGoalContinuationCursor(cursor); err != nil {
		t.Fatalf("legacy continuation identity changed: %v", err)
	}
	item := goalContinuationRuntimeItem(cursor)
	if item.GoalContinuation == nil ||
		item.GoalContinuation.Version != runtimeGoalContinuationLegacyVersion ||
		item.GoalContinuation.TokenBudget == nil ||
		*item.GoalContinuation.TokenBudget != budget {
		t.Fatalf("legacy runtime projection = %#v", item)
	}
	record := &session.PersistedGoalState{
		Version:              session.PersistedGoalStateContinuationVersion,
		GoalID:               cursor.GoalID,
		Objective:            "preserve legacy continuation identity",
		ObjectiveRevision:    cursor.ObjectiveRevision,
		Status:               string(goalStatusActive),
		Revision:             cursor.GoalRevision,
		TokenBudget:          &budget,
		TokensUsed:           cursor.TokensUsed,
		UsageLedgerRevision:  cursor.UsageLedgerRevision,
		ContinuationOrdinal:  cursor.ContinuationOrdinal,
		Continuation:         persistedGoalContinuation(cursor),
		LastGoalTurnID:       cursor.PredecessorGoalTurnID,
		LastTerminalSequence: cursor.PredecessorTerminalSequence,
		CreatedAt:            terminalAt.Add(-time.Minute),
		UpdatedAt:            terminalAt,
	}
	restored, warnings, checkpoint := restorePersistedGoalState(
		record,
		"",
		terminalAt.Add(time.Hour),
	)
	if !checkpoint || restored == nil || restored.unavailable ||
		restored.Continuation == nil ||
		restored.Continuation.ItemID != cursor.ItemID ||
		restored.Continuation.CheckpointID != cursor.CheckpointID ||
		restored.Continuation.ContinuationTurnID != cursor.ContinuationTurnID ||
		len(warnings) == 0 {
		t.Fatalf(
			"legacy continuation restore = %#v warnings=%v checkpoint=%v",
			restored,
			warnings,
			checkpoint,
		)
	}
	rewritten := persistedGoalState(restored)
	if rewritten.Version != session.PersistedGoalStateVersion ||
		rewritten.Continuation == nil ||
		rewritten.Continuation.Version !=
			session.PersistedGoalContinuationLegacyVersion ||
		rewritten.Continuation.ItemID != cursor.ItemID {
		t.Fatalf("rewritten legacy continuation = %#v", rewritten)
	}
}

func newP49OptionalContinuationFixture(
	t *testing.T,
) (*goalState, *goalContinuationCursor, RuntimeItem, RuntimeInputScope) {
	t.Helper()
	terminalAt := time.Date(2026, 8, 7, 5, 30, 0, 0, time.UTC)
	scope := RuntimeInputScope{SessionID: "root", ThreadID: "root"}
	state := &goalState{
		GoalID:               "goal-unbudgeted",
		Objective:            "continue without a token limiter",
		ObjectiveRevision:    1,
		Status:               goalStatusActive,
		Revision:             7,
		TokensUsed:           25,
		UsageLedgerRevision:  2,
		LastGoalTurnID:       "turn-unbudgeted",
		LastTerminalSequence: 9,
		CreatedAt:            terminalAt.Add(-time.Minute),
		UpdatedAt:            terminalAt,
	}
	cursor, err := newGoalContinuationCursor(
		state,
		scope,
		Terminal{Reason: TerminalCompleted},
		state.LastTerminalSequence,
		terminalAt,
		11,
	)
	if err != nil {
		t.Fatal(err)
	}
	state.ContinuationOrdinal = cursor.ContinuationOrdinal
	state.Continuation = cloneGoalContinuationCursor(cursor)
	return state, cursor, goalContinuationRuntimeItem(cursor), scope
}

func TestP243ContinuationEnqueueIsIdempotentAndConflictsFailClosed(t *testing.T) {
	now := time.Date(2026, 7, 29, 5, 15, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	budget := uint64(10_000)
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "prove deterministic continuation identity",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}
	finishP243EligibleTurn(t, eng, "p24-3-idempotent", &now)
	state := eng.goalService.snapshot()
	coordinator, _, err := eng.runtimeInputOwner()
	if err != nil {
		t.Fatal(err)
	}
	item := goalContinuationRuntimeItem(state.Continuation)
	first, err := coordinator.enqueueDormantGoalContinuation(item)
	if err != nil {
		t.Fatal(err)
	}
	second, err := coordinator.enqueueDormantGoalContinuation(item)
	if err != nil {
		t.Fatal(err)
	}
	if !runtimeItemsEqual(first, second) {
		t.Fatalf("idempotent enqueue changed item: first=%#v second=%#v", first, second)
	}
	conflict := cloneRuntimeItem(item)
	conflict.GoalContinuation.TokensUsed++
	if _, err := coordinator.enqueueDormantGoalContinuation(conflict); err == nil ||
		!strings.Contains(err.Error(), "conflicts with an existing payload") {
		t.Fatalf("conflicting deterministic item error = %v", err)
	}
}

func TestP243ClaimAdmissionReceiptAndRejectedRecovery(t *testing.T) {
	now := time.Date(2026, 7, 29, 5, 30, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	budget := uint64(10_000)
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "admit one internal continuation",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}
	finishP243EligibleTurn(t, eng, "p24-3-admit-predecessor", &now)

	item, ok, err := eng.claimGoalContinuation()
	if err != nil || !ok {
		t.Fatalf("claim Goal continuation: ok=%v err=%v item=%#v", ok, err, item)
	}
	turnID := item.GoalContinuation.ContinuationTurnID
	if _, err := eng.beginPlanTurn(turnID); err != nil {
		t.Fatal(err)
	}
	event, identity, err := eng.goalService.beginTurn(
		turnID,
		false,
		&item,
		now.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if identity == nil ||
		event.GoalLifecycle == nil ||
		eng.goalService.snapshot().Continuation.Disposition !=
			goalContinuationDispositionAdmitting {
		t.Fatalf("admitting Goal continuation = identity=%#v event=%#v state=%#v", identity, event, eng.goalService.snapshot())
	}
	message := &schema.Message{
		Role:    schema.User,
		Content: "internal continuation",
		Extra:   runtimeItemMetadata(item),
	}
	if err := eng.recordTranscriptMessages([]*schema.Message{message}); err != nil {
		t.Fatal(err)
	}
	if err := eng.goalService.markContinuationDelivered(
		item.ID,
		turnID,
		now.Add(2*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	eng.goalService.abandonTurn(turnID)
	eng.endPlanTurn(turnID)
	if state := eng.goalService.snapshot(); state == nil ||
		state.Continuation == nil ||
		state.Continuation.Disposition != goalContinuationDispositionDelivered {
		t.Fatalf("delivered Goal continuation = %#v", state)
	}
	if items := eng.RuntimeItems(); len(items) != 0 {
		t.Fatalf("delivered Goal continuation remained pending: %#v", items)
	}

	host := NewQueryEngine(QueryEngineConfig{
		SessionID:     "p24-3-recovery-host",
		ThreadID:      "p24-3-recovery-host",
		CWD:           eng.config.CWD,
		TranscriptDir: eng.config.TranscriptDir,
		Clock:         func() time.Time { return now.Add(time.Hour) },
	})
	t.Cleanup(host.Close)
	resumed, err := host.ResumeSession(t.Context(), eng.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	if !containsSessionWarning(resumed.Warnings, "already delivered continuation") {
		t.Fatalf("delivery recovery warnings = %v", resumed.Warnings)
	}
	if state := host.goalService.snapshot(); state == nil ||
		state.Status != goalStatusPaused ||
		state.Continuation == nil ||
		state.Continuation.Disposition != goalContinuationDispositionDelivered {
		t.Fatalf("delivered recovery state = %#v", state)
	}
	if items := host.RuntimeItems(); len(items) != 0 {
		t.Fatalf("delivered recovery reconstructed item: %#v", items)
	}
}

func TestP243RejectedDispositionNeverRecoversToPending(t *testing.T) {
	now := time.Date(2026, 7, 29, 5, 45, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	budget := uint64(10_000)
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "reject stale continuation",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}
	finishP243EligibleTurn(t, eng, "p24-3-reject-predecessor", &now)
	turnID := "p24-3-user-steering"
	if _, err := eng.beginPlanTurn(turnID); err != nil {
		t.Fatal(err)
	}
	if _, identity, err := eng.goalService.beginTurn(
		turnID,
		true,
		nil,
		now.Add(time.Second),
	); err != nil || identity == nil {
		t.Fatalf("user steering admission: identity=%#v err=%v", identity, err)
	}
	eng.goalService.abandonTurn(turnID)
	eng.endPlanTurn(turnID)
	state := eng.goalService.snapshot()
	if state.Continuation == nil ||
		state.Continuation.Disposition != goalContinuationDispositionRejected {
		t.Fatalf("rejected cursor = %#v", state)
	}
	if items := eng.RuntimeItems(); len(items) != 0 {
		t.Fatalf("rejected item was not settled: %#v", items)
	}

	path := RuntimeInputPersistencePath(eng.transcript.Path())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var envelope runtimeInputEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Items) != 0 {
		t.Fatalf("settled rejection ledger = %#v", envelope.Items)
	}

	recovered, err := NewRuntimeInputCoordinator(
		RuntimeInputCoordinatorConfig{
			SessionID: eng.SessionID(),
			ThreadID:  eng.ThreadID(),
			Path:      path,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	warnings, err := eng.reconcileRestoredGoalContinuation(recovered)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("rejected recovery warnings = %v", warnings)
	}
	if items := recovered.Snapshot(RuntimeInputScope{
		SessionID: eng.SessionID(),
		ThreadID:  eng.ThreadID(),
	}); len(items) != 0 {
		t.Fatalf("rejected cursor reconstructed item: %#v", items)
	}
}

func TestP243ProcessingRecoveryReturnsPendingExactlyBeforeReceipt(t *testing.T) {
	now := time.Date(2026, 7, 29, 5, 50, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	budget := uint64(10_000)
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "recover one unreceipted claim",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}
	finishP243EligibleTurn(t, eng, "p24-3-processing-predecessor", &now)
	item, ok, err := eng.claimGoalContinuation()
	if err != nil || !ok {
		t.Fatalf("initial claim: item=%#v ok=%v err=%v", item, ok, err)
	}
	path := RuntimeInputPersistencePath(eng.transcript.Path())
	recovered, err := NewRuntimeInputCoordinator(
		RuntimeInputCoordinatorConfig{
			SessionID: eng.SessionID(),
			ThreadID:  eng.ThreadID(),
			Path:      path,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := recovered.Snapshot(RuntimeInputScope{
		SessionID: eng.SessionID(),
		ThreadID:  eng.ThreadID(),
	})
	if len(snapshot) != 1 ||
		snapshot[0].ID != item.ID ||
		snapshot[0].State != RuntimeItemPending {
		t.Fatalf("processing recovery = %#v", snapshot)
	}
	claimed, ok, err := recovered.claimGoalContinuation(
		item.ID,
		RuntimeInputScope{
			SessionID: eng.SessionID(),
			ThreadID:  eng.ThreadID(),
		},
	)
	if err != nil || !ok || claimed.ID != item.ID {
		t.Fatalf("recovered claim: item=%#v ok=%v err=%v", claimed, ok, err)
	}
}

func TestP243DurableRejectionRecoveryNeverReleasesToPending(t *testing.T) {
	now := time.Date(2026, 7, 29, 5, 55, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	budget := uint64(10_000)
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "persist rejection before settlement",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}
	finishP243EligibleTurn(t, eng, "p24-3-rejection-predecessor", &now)
	state := eng.goalService.snapshot()
	coordinator, _, err := eng.runtimeInputOwner()
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.rejectGoalContinuation(
		state.Continuation.ItemID,
		"stale-test-claim",
		now.Add(time.Second),
	); err != nil {
		t.Fatal(err)
	}
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
	if items := recovered.Snapshot(RuntimeInputScope{
		SessionID: eng.SessionID(),
		ThreadID:  eng.ThreadID(),
	}); len(items) != 0 {
		t.Fatalf("rejected processing item recovered pending: %#v", items)
	}
}

func TestP243QueueFailureKeepsOneRecoverableCursor(t *testing.T) {
	now := time.Date(2026, 7, 29, 6, 5, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	budget := uint64(10_000)
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "recover queue write failure",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}
	coordinator, _, err := eng.runtimeInputOwner()
	if err != nil {
		t.Fatal(err)
	}
	originalPath := coordinator.path
	brokenPath := t.TempDir()
	coordinator.mu.Lock()
	coordinator.path = brokenPath
	coordinator.mu.Unlock()
	finishP243EligibleTurn(t, eng, "p24-3-queue-failure", &now)
	state := eng.goalService.snapshot()
	if state.Continuation == nil ||
		state.ContinuationOrdinal != 1 ||
		state.Continuation.Disposition != goalContinuationDispositionPending {
		t.Fatalf("queue failure cursor = %#v", state)
	}
	if items := coordinator.Snapshot(eng.runtimeInputScope()); len(items) != 0 {
		t.Fatalf("failed queue write published item: %#v", items)
	}
	if err := eng.RuntimeStateError(); err == nil ||
		!strings.Contains(err.Error(), "persist Goal continuation item") {
		t.Fatalf("queue failure diagnostic = %v", err)
	}
	coordinator.mu.Lock()
	coordinator.path = originalPath
	coordinator.mu.Unlock()
	warnings, err := eng.reconcileRestoredGoalContinuation(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("queue reconciliation warnings = %v", warnings)
	}
	items := coordinator.Snapshot(eng.runtimeInputScope())
	if len(items) != 1 ||
		items[0].ID != state.Continuation.ItemID {
		t.Fatalf("reconciled queue items = %#v", items)
	}
	if eng.goalService.snapshot().ContinuationOrdinal != 1 {
		t.Fatal("queue reconciliation advanced a second cursor")
	}
}

func TestP243PendingCompletionAndNonSuccessDoNotCreateContinuation(t *testing.T) {
	now := time.Date(2026, 7, 29, 6, 10, 0, 0, time.UTC)
	t.Run("pending completion", func(t *testing.T) {
		eng := newP241GoalEngine(t, QueryEngineConfig{
			Clock: func() time.Time { return now },
		})
		budget := uint64(10_000)
		created, err := eng.goalService.create(goalCreateRequest{
			Objective:   "do not relabel completion intent",
			TokenBudget: &budget,
		})
		if err != nil {
			t.Fatal(err)
		}
		events := make(chan QueryEvent, 16)
		emitter := beginP242aGoalTurn(
			t,
			eng,
			events,
			"p24-3-pending-completion",
			false,
			now,
		)
		if _, err := eng.goalService.requestCompletion(
			goalCompletionRequest{
				GoalID:            created.GoalID,
				ObjectiveRevision: created.ObjectiveRevision,
				TurnID:            "p24-3-pending-completion",
			},
			now,
		); err != nil {
			t.Fatal(err)
		}
		if !emitter.Emit(QueryEvent{
			Type:         EventTerminal,
			TerminalInfo: &Terminal{Reason: TerminalCompleted},
		}) {
			t.Fatal("terminal rejected")
		}
		emitter.Close()
		eng.endPlanTurn("p24-3-pending-completion")
		close(events)
		if state := eng.goalService.snapshot(); state.Continuation != nil {
			t.Fatalf("pending completion became continuation: %#v", state)
		}
	})

	t.Run("non-success terminal", func(t *testing.T) {
		eng := newP241GoalEngine(t, QueryEngineConfig{
			Clock: func() time.Time { return now },
		})
		budget := uint64(10_000)
		if _, err := eng.goalService.create(goalCreateRequest{
			Objective:   "stop on model error",
			TokenBudget: &budget,
		}); err != nil {
			t.Fatal(err)
		}
		events := make(chan QueryEvent, 16)
		emitter := beginP242aGoalTurn(
			t,
			eng,
			events,
			"p24-3-model-error",
			false,
			now,
		)
		if !emitter.Emit(QueryEvent{
			Type: EventTerminal,
			TerminalInfo: &Terminal{
				Reason: TerminalModelError,
				Err:    context.DeadlineExceeded,
			},
		}) {
			t.Fatal("terminal rejected")
		}
		emitter.Close()
		eng.endPlanTurn("p24-3-model-error")
		close(events)
		if state := eng.goalService.snapshot(); state.Continuation != nil {
			t.Fatalf("non-success terminal created continuation: %#v", state)
		}
	})
}

func TestP243UserInputAndGoalControlsPermanentlySupersedeCursor(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *QueryEngine, *time.Time)
	}{
		{
			name: "pause",
			mutate: func(t *testing.T, eng *QueryEngine, _ *time.Time) {
				t.Helper()
				if _, err := eng.goalService.pause(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "edit",
			mutate: func(t *testing.T, eng *QueryEngine, _ *time.Time) {
				t.Helper()
				if _, err := eng.goalService.edit("edited objective"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "budget",
			mutate: func(t *testing.T, eng *QueryEngine, _ *time.Time) {
				t.Helper()
				if _, err := eng.goalService.setBudget(20_000); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "cancel",
			mutate: func(t *testing.T, eng *QueryEngine, now *time.Time) {
				t.Helper()
				if err := eng.goalService.pauseForCancellation(
					"test",
					(*now).Add(time.Second),
				); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 7, 29, 6, 15, 0, 0, time.UTC)
			eng := newP241GoalEngine(t, QueryEngineConfig{
				Clock: func() time.Time { return now },
			})
			budget := uint64(10_000)
			if _, err := eng.goalService.create(goalCreateRequest{
				Objective:   "supersede cursor",
				TokenBudget: &budget,
			}); err != nil {
				t.Fatal(err)
			}
			finishP243EligibleTurn(t, eng, "p24-3-control-"+test.name, &now)
			test.mutate(t, eng, &now)
			state := eng.goalService.snapshot()
			if state.Continuation == nil ||
				state.Continuation.Disposition != goalContinuationDispositionRejected {
				t.Fatalf("%s cursor = %#v", test.name, state)
			}
			if items := eng.RuntimeItems(); len(items) != 0 {
				t.Fatalf("%s left continuation item: %#v", test.name, items)
			}
			if _, ok, err := eng.claimGoalContinuation(); err != nil || ok {
				t.Fatalf("%s claim after control: ok=%v err=%v", test.name, ok, err)
			}
		})
	}
}

func TestP243ClaimPauseRaceHasOnePermanentWinner(t *testing.T) {
	now := time.Date(2026, 7, 29, 6, 20, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	budget := uint64(10_000)
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "serialize claim and pause",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}
	finishP243EligibleTurn(t, eng, "p24-3-claim-pause", &now)
	var group sync.WaitGroup
	group.Add(2)
	var claimed RuntimeItem
	var claimOK bool
	var claimErr error
	var pauseErr error
	go func() {
		defer group.Done()
		claimed, claimOK, claimErr = eng.claimGoalContinuation()
	}()
	go func() {
		defer group.Done()
		_, pauseErr = eng.goalService.pause()
	}()
	group.Wait()
	if claimErr != nil || pauseErr != nil {
		t.Fatalf("race errors: claim=%v pause=%v", claimErr, pauseErr)
	}
	state := eng.goalService.snapshot()
	if state.Status != goalStatusPaused ||
		state.Continuation == nil ||
		state.Continuation.Disposition != goalContinuationDispositionRejected {
		t.Fatalf("claim/pause final state = %#v", state)
	}
	if claimOK {
		turnID := claimed.GoalContinuation.ContinuationTurnID
		if _, err := eng.beginPlanTurn(turnID); err != nil {
			t.Fatal(err)
		}
		_, identity, err := eng.goalService.beginTurn(
			turnID,
			false,
			&claimed,
			now.Add(time.Second),
		)
		eng.endPlanTurn(turnID)
		if err != nil {
			t.Fatal(err)
		}
		if identity != nil {
			t.Fatalf("losing claimed item admitted after pause: %#v", identity)
		}
	}
	if items := eng.RuntimeItems(); len(items) != 0 {
		t.Fatalf("claim/pause race left item: %#v", items)
	}
}

func TestP243UserInputAfterClaimPermanentlyRejectsBeforeRelease(t *testing.T) {
	now := time.Date(2026, 7, 29, 6, 21, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	budget := uint64(10_000)
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "prefer explicit input after a continuation claim",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}
	finishP243EligibleTurn(t, eng, "p24-3-post-claim-input", &now)
	claimed, ok, err := eng.claimGoalContinuation()
	if err != nil || !ok {
		t.Fatalf("claim Goal continuation: item=%#v ok=%v err=%v", claimed, ok, err)
	}
	queued, err := eng.EnqueueUserInput(UserTurnInput{
		Display: "explicit user work",
		Prompt:  "explicit user work",
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	events, _ := eng.submitGoalContinuation(context.Background(), claimed)
	collected := drainEngineEvents(t, events)
	if len(collected) == 0 ||
		collected[len(collected)-1].Type != EventTerminal ||
		collected[len(collected)-1].TerminalInfo == nil ||
		collected[len(collected)-1].TerminalInfo.Reason !=
			TerminalPersistenceError ||
		collected[len(collected)-1].TerminalInfo.Err == nil ||
		!strings.Contains(
			collected[len(collected)-1].TerminalInfo.Err.Error(),
			"permanently rejected",
		) {
		t.Fatalf("post-claim supersession events = %#v", collected)
	}
	state := eng.goalService.snapshot()
	if state == nil ||
		state.Continuation == nil ||
		state.Continuation.Disposition != goalContinuationDispositionRejected ||
		state.Continuation.DispositionReason !=
			"superseded-by-explicit-user-input" {
		t.Fatalf("post-claim supersession state = %#v", state)
	}
	items := eng.RuntimeItems()
	if len(items) != 1 ||
		items[0].ID != queued.ID ||
		items[0].Kind != RuntimeItemUserPrompt ||
		items[0].State != RuntimeItemPending {
		t.Fatalf("post-claim supersession ledger = %#v", items)
	}
	assertP241PersistedGoal(t, eng, func(record *session.PersistedGoalState) {
		if record.Continuation == nil ||
			record.Continuation.Disposition !=
				goalContinuationDispositionRejected ||
			record.Continuation.DispositionReason !=
				"superseded-by-explicit-user-input" {
			t.Fatalf("persisted post-claim rejection = %#v", record)
		}
	})

	path := RuntimeInputPersistencePath(eng.transcript.Path())
	reopened, err := NewRuntimeInputCoordinator(
		RuntimeInputCoordinatorConfig{
			SessionID: eng.SessionID(),
			ThreadID:  eng.ThreadID(),
			Path:      path,
			Clock:     func() time.Time { return now.Add(time.Hour) },
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	recovered := reopened.Snapshot(eng.runtimeInputScope())
	if len(recovered) != 1 ||
		recovered[0].ID != queued.ID ||
		recovered[0].Kind != RuntimeItemUserPrompt {
		t.Fatalf("restart recovered stale Goal continuation: %#v", recovered)
	}
}

func TestP243UserInputRejectionCheckpointFailureRemainsRetryable(t *testing.T) {
	now := time.Date(2026, 7, 29, 6, 21, 30, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	budget := uint64(10_000)
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "retry a failed supersession checkpoint",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}
	finishP243EligibleTurn(t, eng, "p24-3-reject-checkpoint", &now)
	before := eng.goalService.snapshot()
	claimed, ok, err := eng.claimGoalContinuation()
	if err != nil || !ok {
		t.Fatalf("claim Goal continuation: item=%#v ok=%v err=%v", claimed, ok, err)
	}
	queued, err := eng.EnqueueUserInput(UserTurnInput{
		Display: "durable explicit input",
		Prompt:  "durable explicit input",
	})
	if err != nil {
		t.Fatal(err)
	}
	transcriptPath := eng.transcript.Path()
	if err := eng.transcript.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(transcriptPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(transcriptPath, 0o700); err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Second)
	events, _ := eng.submitGoalContinuation(context.Background(), claimed)
	collected := drainEngineEvents(t, events)
	if len(collected) == 0 ||
		collected[len(collected)-1].TerminalInfo == nil ||
		collected[len(collected)-1].TerminalInfo.Reason !=
			TerminalPersistenceError ||
		collected[len(collected)-1].TerminalInfo.Err == nil ||
		!strings.Contains(
			collected[len(collected)-1].TerminalInfo.Err.Error(),
			"persist permanent Goal continuation rejection",
		) {
		t.Fatalf("failed rejection checkpoint events = %#v", collected)
	}
	state := eng.goalService.snapshot()
	if state == nil ||
		state.Revision != before.Revision ||
		state.Continuation == nil ||
		state.Continuation.Disposition != goalContinuationDispositionPending ||
		state.Continuation.ItemID != before.Continuation.ItemID {
		t.Fatalf("failed rejection checkpoint state = %#v", state)
	}
	items := eng.RuntimeItems()
	if len(items) != 2 {
		t.Fatalf("retryable rejection items = %#v", items)
	}
	var goalItems, userItems int
	for _, item := range items {
		switch {
		case item.ID == before.Continuation.ItemID &&
			item.Kind == RuntimeItemGoalContinuation &&
			item.State == RuntimeItemPending:
			goalItems++
		case item.ID == queued.ID &&
			item.Kind == RuntimeItemUserPrompt &&
			item.State == RuntimeItemPending:
			userItems++
		}
	}
	if goalItems != 1 || userItems != 1 {
		t.Fatalf(
			"retryable rejection item counts: goal=%d user=%d items=%#v",
			goalItems,
			userItems,
			items,
		)
	}

	reopened, err := NewRuntimeInputCoordinator(
		RuntimeInputCoordinatorConfig{
			SessionID: eng.SessionID(),
			ThreadID:  eng.ThreadID(),
			Path:      RuntimeInputPersistencePath(transcriptPath),
			Clock:     func() time.Time { return now.Add(time.Hour) },
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	recovered := reopened.Snapshot(eng.runtimeInputScope())
	goalItems = 0
	for _, item := range recovered {
		if item.ID == before.Continuation.ItemID &&
			item.Kind == RuntimeItemGoalContinuation &&
			item.State == RuntimeItemPending {
			goalItems++
		}
	}
	if goalItems != 1 {
		t.Fatalf(
			"failed rejection checkpoint did not recover one cursor: %#v",
			recovered,
		)
	}
}

func TestP243ActiveCancellationPersistsPauseBeforeTerminalAftercare(t *testing.T) {
	now := time.Date(2026, 7, 29, 6, 22, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	budget := uint64(10_000)
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "cancel one active Goal turn",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}
	events := make(chan QueryEvent, 16)
	emitter := beginP242aGoalTurn(
		t,
		eng,
		events,
		"p24-3-active-cancel",
		true,
		now,
	)
	now = now.Add(time.Second)
	if err := eng.RequestStop(RuntimeStopImmediate, "test-active-cancel"); err != nil {
		t.Fatal(err)
	}
	paused := eng.goalService.snapshot()
	if paused == nil ||
		paused.Status != goalStatusPaused ||
		paused.StatusReasonCode != goalReasonUserCancelled ||
		!paused.UpdatedAt.Equal(now) {
		t.Fatalf("active cancellation state = %#v", paused)
	}
	now = now.Add(time.Second)
	if !emitter.Emit(QueryEvent{
		Type: EventTerminal,
		TerminalInfo: &Terminal{
			Reason: TerminalAbortedStreaming,
			Err:    context.Canceled,
		},
	}) {
		t.Fatal("cancelled Goal terminal was rejected")
	}
	emitter.Close()
	eng.endPlanTurn("p24-3-active-cancel")
	close(events)
	collected := make([]QueryEvent, 0, cap(events))
	for event := range events {
		collected = append(collected, event)
	}
	if len(collected) == 0 ||
		collected[len(collected)-1].Type != EventTerminal ||
		collected[len(collected)-1].TerminalInfo == nil ||
		collected[len(collected)-1].TerminalInfo.Reason !=
			TerminalAbortedStreaming {
		t.Fatalf("cancelled Goal events = %#v", collected)
	}
	state := eng.goalService.snapshot()
	if state == nil ||
		state.Status != goalStatusPaused ||
		state.StatusReasonCode != goalReasonUserCancelled ||
		state.Continuation != nil ||
		state.LastTerminalSequence != collected[len(collected)-1].Sequence ||
		eng.currentGoalExecutionIdentity() != nil {
		t.Fatalf("cancelled Goal terminal state = %#v", state)
	}
}

func TestP243PermissionInterruptBlocksGoalClaim(t *testing.T) {
	now := time.Date(2026, 7, 29, 6, 25, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	budget := uint64(10_000)
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "respect exact permission interrupt",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}
	finishP243EligibleTurn(t, eng, "p24-3-permission", &now)
	scope := eng.runtimeInputScope()
	request := projectGraphHITLRequest{
		Version:          projectGraphHITLRequestVersion,
		RequestID:        "p24-3-permission-request",
		InterruptID:      "p24-3-interrupt",
		InvocationDigest: strings.Repeat("a", 64),
		PolicyRevision:   strings.Repeat("b", 64),
		ToolName:         "Bash",
		Scope:            scope,
		Kind:             "permission",
	}
	if err := eng.projectGraphCheckpoint.Set(
		context.Background(),
		eng.projectGraphCheckpoint.checkpointID,
		[]byte("opaque"),
	); err != nil {
		t.Fatal(err)
	}
	if err := eng.projectGraphCheckpoint.MarkInterrupt(
		context.Background(),
		request,
	); err != nil {
		t.Fatal(err)
	}
	if item, ok, err := eng.claimGoalContinuation(); err != nil || ok {
		t.Fatalf("permission interrupt allowed Goal claim: item=%#v ok=%v err=%v", item, ok, err)
	}
	if item, ok, err := eng.ClaimNextRuntimeItem(); err != nil || ok {
		t.Fatalf("permission interrupt returned non-decision: item=%#v ok=%v err=%v", item, ok, err)
	}
}

func TestP243VersionTwoMigratesWithoutFabricatingCursor(t *testing.T) {
	now := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	record := validP241PersistedGoal(now)
	record.Version = session.PersistedGoalStateAccountingVersion
	record.Status = string(goalStatusActive)
	record.ContinuationOrdinal = 7
	recorder, cwd, dir := writeP241GoalSession(t, "p24-3-v2", record)
	host := NewQueryEngine(QueryEngineConfig{
		SessionID:     "p24-3-v2-host",
		ThreadID:      "p24-3-v2-host",
		CWD:           cwd,
		TranscriptDir: dir,
		Clock:         func() time.Time { return now.Add(time.Hour) },
	})
	t.Cleanup(host.Close)
	resumed, err := host.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID:      "p24-3-v2",
		CWD:            cwd,
		TranscriptDir:  dir,
		TranscriptPath: recorder.Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSessionWarning(resumed.Warnings, "version 2 to version 4") {
		t.Fatalf("v2 migration warnings = %v", resumed.Warnings)
	}
	state := host.goalService.snapshot()
	if state == nil ||
		state.Status != goalStatusPaused ||
		state.Continuation != nil ||
		state.ContinuationOrdinal != 7 {
		t.Fatalf("v2 migration fabricated cursor: %#v", state)
	}
}

func TestP243UnknownGoalRuntimeItemFailsOldReaderClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-inputs.json")
	envelope := runtimeInputEnvelope{
		Version:      runtimeInputEnvelopeVersion,
		Revision:     1,
		NextSequence: 1,
		Items: []RuntimeItem{{
			Version:    runtimeItemVersion,
			ID:         "unknown-goal-item",
			Kind:       RuntimeItemKind("future_goal_continuation"),
			Priority:   RuntimePriorityLater,
			Scope:      RuntimeInputScope{SessionID: "future-session"},
			Sequence:   1,
			EnqueuedAt: time.Now().UTC(),
			State:      RuntimeItemPending,
			UserPrompt: &RuntimeUserPrompt{Prompt: "must not reinterpret"},
		}},
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRuntimeInputCoordinator(
		RuntimeInputCoordinatorConfig{
			SessionID: "future-session",
			Path:      path,
		},
		nil,
	); err == nil || !strings.Contains(err.Error(), "invalid kind") {
		t.Fatalf("unknown runtime item error = %v", err)
	}
}

func finishP243EligibleTurn(
	t *testing.T,
	eng *QueryEngine,
	turnID string,
	now *time.Time,
) {
	t.Helper()
	events := make(chan QueryEvent, 16)
	emitter := beginP242aGoalTurn(t, eng, events, turnID, true, *now)
	*now = (*now).Add(time.Second)
	if !emitter.Emit(QueryEvent{
		Type: EventTerminal,
		TerminalInfo: &Terminal{
			Reason: TerminalCompleted,
		},
	}) {
		t.Fatal("eligible Goal terminal was rejected")
	}
	emitter.Close()
	eng.endPlanTurn(turnID)
	close(events)
	for range events {
	}
}

func TestP243PublicSubmitRuntimeItemCannotDispatchGoalContinuation(t *testing.T) {
	eng := newP241GoalEngine(t, QueryEngineConfig{})
	events, terminal := eng.SubmitRuntimeItem(context.Background(), RuntimeItem{
		ID:   "not-authorized",
		Kind: RuntimeItemGoalContinuation,
		GoalContinuation: &RuntimeGoalContinuation{
			Version: runtimeGoalContinuationVersion,
		},
	})
	for range events {
	}
	if terminal.Reason != TerminalModelError ||
		terminal.Err == nil ||
		!strings.Contains(terminal.Err.Error(), "no model prompt") {
		t.Fatalf("public Goal dispatch terminal = %#v", terminal)
	}
}
