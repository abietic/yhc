package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
)

func TestP49CreateWithoutBudgetStartsImmediately(t *testing.T) {
	eng := newP241GoalEngine(t, QueryEngineConfig{})
	created, err := eng.goalService.create(goalCreateRequest{
		Objective: "run without a Goal token limiter",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != goalStatusActive ||
		created.StatusReasonCode != "" ||
		created.StatusReason != "" ||
		created.TokenBudget != nil {
		t.Fatalf("unbudgeted Goal create = %#v", created)
	}
	assertP241PersistedGoal(t, eng, func(record *session.PersistedGoalState) {
		if record == nil ||
			record.Version != session.PersistedGoalStateVersion ||
			record.Status != string(goalStatusActive) ||
			record.TokenBudget != nil {
			t.Fatalf("persisted unbudgeted Goal = %#v", record)
		}
	})
}

func TestP49ResumeLegacyPausedDraftWithoutBudget(t *testing.T) {
	now := time.Date(2026, 8, 7, 7, 0, 0, 0, time.UTC)
	record := validP241PersistedGoal(now)
	record.Version = session.PersistedGoalStateContinuationVersion
	record.TokenBudget = nil
	restored, warnings, checkpoint := restorePersistedGoalState(
		record,
		"",
		now.Add(time.Minute),
	)
	if !checkpoint || restored == nil || restored.unavailable ||
		restored.Status != goalStatusPaused || len(warnings) == 0 {
		t.Fatalf(
			"legacy paused draft = %#v warnings=%v checkpoint=%v",
			restored,
			warnings,
			checkpoint,
		)
	}
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now.Add(2 * time.Minute) },
	})
	eng.goalMu.Lock()
	eng.goalState = cloneGoalState(restored)
	eng.goalMu.Unlock()
	resumed, err := eng.goalService.resume()
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != goalStatusActive ||
		resumed.TokenBudget != nil ||
		resumed.StatusReasonCode != "" ||
		resumed.Revision != restored.Revision+1 {
		t.Fatalf("resumed legacy unbudgeted Goal = %#v", resumed)
	}
}

func TestP241GoalTransitionMatrixPersistsExactState(t *testing.T) {
	eng := newP241GoalEngine(t, QueryEngineConfig{})

	created, err := eng.goalService.create(goalCreateRequest{
		Objective: "  first\r\nsecond  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != goalStatusActive ||
		created.StatusReasonCode != "" ||
		created.Objective != "first\nsecond" ||
		created.ObjectiveRevision != 1 ||
		created.Revision != 1 ||
		created.TokenBudget != nil ||
		created.GoalID == "" {
		t.Fatalf("created Goal = %#v", created)
	}
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "replacement",
		TokenBudget: p241Uint64(100),
	}); !errors.Is(err, errGoalAlreadyExists) {
		t.Fatalf("unfinished replacement error = %v", err)
	}

	budgeted, err := eng.goalService.setBudget(100)
	if err != nil {
		t.Fatal(err)
	}
	if budgeted.Status != goalStatusActive ||
		budgeted.TokenBudget == nil ||
		*budgeted.TokenBudget != 100 ||
		budgeted.Revision != 2 {
		t.Fatalf("budgeted Goal = %#v", budgeted)
	}
	active, err := eng.goalService.resume()
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != goalStatusActive ||
		active.StatusReasonCode != "" ||
		active.Revision != 2 {
		t.Fatalf("active Goal = %#v", active)
	}
	edited, err := eng.goalService.edit("updated")
	if err != nil {
		t.Fatal(err)
	}
	if edited.Objective != "updated" ||
		edited.ObjectiveRevision != 2 ||
		edited.Revision != 3 {
		t.Fatalf("edited Goal = %#v", edited)
	}
	paused, err := eng.goalService.pause()
	if err != nil {
		t.Fatal(err)
	}
	if paused.Status != goalStatusPaused ||
		paused.StatusReasonCode != goalReasonUserPaused ||
		paused.Revision != 4 {
		t.Fatalf("paused Goal = %#v", paused)
	}
	samePaused, err := eng.goalService.pause()
	if err != nil {
		t.Fatal(err)
	}
	if samePaused.Revision != paused.Revision {
		t.Fatalf("idempotent pause revision = %d, want %d", samePaused.Revision, paused.Revision)
	}

	resumed, err := eng.goalService.resume()
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != goalStatusActive || resumed.Revision != 5 {
		t.Fatalf("resumed Goal = %#v", resumed)
	}
	assertP241PersistedGoal(t, eng, func(record *session.PersistedGoalState) {
		if record == nil ||
			record.Version != session.PersistedGoalStateVersion ||
			record.GoalID != resumed.GoalID ||
			record.Objective != resumed.Objective ||
			record.Status != string(goalStatusActive) ||
			record.Revision != resumed.Revision {
			t.Fatalf("persisted Goal = %#v, want %#v", record, resumed)
		}
	})

	if err := eng.goalService.clear(); err != nil {
		t.Fatal(err)
	}
	if state := eng.goalService.snapshot(); state != nil {
		t.Fatalf("Goal after clear = %#v", state)
	}
	assertP241PersistedGoal(t, eng, func(record *session.PersistedGoalState) {
		if record != nil {
			t.Fatalf("persisted Goal after clear = %#v", record)
		}
	})
}

func TestP241GoalInvalidMutationIsStateNeutral(t *testing.T) {
	eng := newP241GoalEngine(t, QueryEngineConfig{})
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "stable",
		TokenBudget: p241Uint64(10),
	}); err != nil {
		t.Fatal(err)
	}
	before := eng.goalService.snapshot()

	for _, objective := range []string{
		"",
		" \t\n ",
		"contains\x00nul",
		string([]byte{0xff, 0xfe}),
		strings.Repeat("界", 4001),
	} {
		if _, err := eng.goalService.edit(objective); err == nil {
			t.Fatalf("edit accepted invalid objective %q", objective)
		}
		after := eng.goalService.snapshot()
		if after.Revision != before.Revision ||
			after.ObjectiveRevision != before.ObjectiveRevision ||
			after.Objective != before.Objective {
			t.Fatalf("invalid edit mutated state: before=%#v after=%#v", before, after)
		}
	}
	if _, err := eng.goalService.setBudget(0); !errors.Is(err, errGoalBudget) {
		t.Fatalf("zero budget error = %v", err)
	}
	if after := eng.goalService.snapshot(); after.Revision != before.Revision {
		t.Fatalf("zero budget mutated revision to %d", after.Revision)
	}

	eng.goalMu.Lock()
	eng.goalState.Status = goalStatusUsageLimited
	eng.goalState.StatusReasonCode = "usage-unavailable"
	eng.goalState.StatusReason = "usage availability requires revalidation"
	limitedRevision := eng.goalState.Revision
	eng.goalMu.Unlock()
	if _, err := eng.goalService.resume(); !errors.Is(
		err,
		errGoalUsageUnavailable,
	) {
		t.Fatalf("usage-limited resume error = %v", err)
	}
	if after := eng.goalService.snapshot(); after.Revision != limitedRevision ||
		after.Status != goalStatusUsageLimited {
		t.Fatalf("usage-limited resume mutated state: %#v", after)
	}
}

func TestP241GoalAndPlanAreMutuallyExclusive(t *testing.T) {
	planEngine := newP241GoalEngine(t, QueryEngineConfig{
		PermissionMode: permission.ModePlan,
	})
	if _, err := planEngine.goalService.create(goalCreateRequest{
		Objective:   "cannot overlap Plan",
		TokenBudget: p241Uint64(10),
	}); !errors.Is(err, errGoalPlanConflict) {
		t.Fatalf("create in Plan error = %v", err)
	}

	eng := newP241GoalEngine(t, QueryEngineConfig{})
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "active",
		TokenBudget: p241Uint64(10),
	}); err != nil {
		t.Fatal(err)
	}
	transition, changed, err := eng.applyPlanTransition(planTransitionRequest{
		Source:    planTransitionExternal,
		RequestID: "enter-plan-with-goal",
		Mode:      permission.ModePlan,
	})
	if !errors.Is(err, errGoalPlanConflict) || changed || transition != nil {
		t.Fatalf(
			"Plan transition = transition:%#v changed:%v err:%v",
			transition,
			changed,
			err,
		)
	}
	if state := eng.PlanState(); state.Phase != PlanPhaseInactive {
		t.Fatalf("Plan state after rejected transition = %#v", state)
	}

	if _, err := eng.goalService.pause(); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := eng.applyPlanTransition(planTransitionRequest{
		Source:    planTransitionExternal,
		RequestID: "enter-plan-after-pause",
		Mode:      permission.ModePlan,
	}); err != nil || !changed {
		t.Fatalf("enter Plan after pause = changed:%v err:%v", changed, err)
	}
	if _, err := eng.goalService.resume(); !errors.Is(err, errGoalPlanConflict) {
		t.Fatalf("resume in Plan error = %v", err)
	}
}

func TestP241GoalRejectsEphemeralChildAndReviewScopes(t *testing.T) {
	ephemeral := &QueryEngine{}
	ephemeral.goalService = &goalService{engine: ephemeral}
	if _, err := ephemeral.goalService.create(goalCreateRequest{
		Objective:   "ephemeral",
		TokenBudget: p241Uint64(10),
	}); !errors.Is(err, errGoalUnsupportedScope) {
		t.Fatalf("ephemeral create error = %v", err)
	}

	for _, agentID := range []string{"child-agent", "review-agent"} {
		t.Run(agentID, func(t *testing.T) {
			eng := newP241GoalEngine(t, QueryEngineConfig{AgentID: agentID})
			if _, err := eng.goalService.create(goalCreateRequest{
				Objective:   "child",
				TokenBudget: p241Uint64(10),
			}); !errors.Is(err, errGoalUnsupportedScope) {
				t.Fatalf("child create error = %v", err)
			}
		})
	}
}

func TestP241ConcurrentGoalCheckpointAndTransitionsStayCoherent(t *testing.T) {
	eng := newP241GoalEngine(t, QueryEngineConfig{})
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective: "concurrent",
	}); err != nil {
		t.Fatal(err)
	}

	const transitions = 20
	var wg sync.WaitGroup
	errs := make(chan error, transitions*2)
	for index := 0; index < transitions; index++ {
		budget := uint64(index + 1)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := eng.goalService.setBudget(budget)
			errs <- err
		}()
		go func() {
			defer wg.Done()
			errs <- eng.persistSessionCheckpoint("")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	state := eng.goalService.snapshot()
	if state == nil || state.Revision != transitions+1 {
		t.Fatalf("final Goal = %#v", state)
	}
	assertP241PersistedGoal(t, eng, func(record *session.PersistedGoalState) {
		if record == nil ||
			record.GoalID != state.GoalID ||
			record.Revision != state.Revision ||
			record.TokenBudget == nil ||
			state.TokenBudget == nil ||
			*record.TokenBudget != *state.TokenBudget {
			t.Fatalf("persisted/live mismatch: persisted=%#v live=%#v", record, state)
		}
	})
}

func TestP241ConcurrentGoalMutationsSerializeWithClear(t *testing.T) {
	eng := newP241GoalEngine(t, QueryEngineConfig{})
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "serialize controls",
		TokenBudget: p241Uint64(100),
	}); err != nil {
		t.Fatal(err)
	}

	operations := []func() error{
		func() error {
			_, err := eng.goalService.edit("edited concurrently")
			return err
		},
		func() error {
			_, err := eng.goalService.pause()
			return err
		},
		func() error {
			_, err := eng.goalService.resume()
			return err
		},
		func() error {
			_, err := eng.goalService.setBudget(200)
			return err
		},
		eng.goalService.clear,
	}
	start := make(chan struct{})
	errs := make(chan error, len(operations))
	var wg sync.WaitGroup
	for _, operation := range operations {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- operation()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, errGoalNotFound) {
			t.Fatalf("serialized mutation error = %v", err)
		}
	}
	if state := eng.goalService.snapshot(); state != nil {
		t.Fatalf("Goal survived serialized clear: %#v", state)
	}
	assertP241PersistedGoal(t, eng, func(record *session.PersistedGoalState) {
		if record != nil {
			t.Fatalf("persisted Goal survived serialized clear: %#v", record)
		}
	})
}

func TestP241FailedCheckpointDoesNotPublishGoalMutation(t *testing.T) {
	eng := newP241GoalEngine(t, QueryEngineConfig{})
	before, err := eng.goalService.create(goalCreateRequest{
		Objective:   "durable before publish",
		TokenBudget: p241Uint64(100),
	})
	if err != nil {
		t.Fatal(err)
	}
	eng.mu.Lock()
	recorder := eng.transcript
	eng.mu.Unlock()
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	transcriptDir := filepath.Dir(recorder.Path())
	if err := os.Remove(recorder.Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(transcriptDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcriptDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.goalService.edit("must not publish"); err == nil {
		t.Fatal("edit succeeded after transcript close")
	}
	after := eng.goalService.snapshot()
	if after == nil ||
		after.Objective != before.Objective ||
		after.ObjectiveRevision != before.ObjectiveRevision ||
		after.Revision != before.Revision {
		t.Fatalf("failed checkpoint published state: before=%#v after=%#v", before, after)
	}
}

func newP241GoalEngine(
	t *testing.T,
	override QueryEngineConfig,
) *QueryEngine {
	t.Helper()
	cwd := t.TempDir()
	config := override
	if config.SessionID == "" {
		config.SessionID = "p24-root"
	}
	if config.ThreadID == "" {
		config.ThreadID = config.SessionID
	}
	if config.CWD == "" {
		config.CWD = cwd
	}
	if config.TranscriptDir == "" {
		config.TranscriptDir = filepath.Join(cwd, "transcripts")
	}
	if config.Clock == nil {
		now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
		config.Clock = func() time.Time { return now }
	}
	eng := NewQueryEngine(config)
	t.Cleanup(eng.Close)
	return eng
}

func assertP241PersistedGoal(
	t *testing.T,
	eng *QueryEngine,
	assert func(*session.PersistedGoalState),
) {
	t.Helper()
	eng.mu.Lock()
	recorder := eng.transcript
	eng.mu.Unlock()
	loaded, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	metadata := session.ReadSessionMetadataFull(loaded)
	if metadata == nil {
		t.Fatal("missing Session metadata")
	}
	assert(metadata.GoalState)
}

func p241Uint64(value uint64) *uint64 {
	return &value
}
