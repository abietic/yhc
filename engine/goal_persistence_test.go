package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/cloudwego/eino/schema"
)

func TestP241ColdActiveGoalNormalizesToDurablePause(t *testing.T) {
	now := time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC)
	record := validP241PersistedGoal(now)
	record.Status = string(goalStatusActive)
	recorder, cwd, dir := writeP241GoalSession(t, "active-goal", record)

	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "current",
		ThreadID:      "current",
		CWD:           cwd,
		TranscriptDir: dir,
		Clock: func() time.Time {
			return now.Add(time.Hour)
		},
	})
	eng.agentRunner.SetOutputDir(filepath.Join(t.TempDir(), "agent-output"))
	t.Cleanup(eng.Close)
	resumed, err := eng.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID:      "active-goal",
		CWD:            cwd,
		TranscriptDir:  dir,
		TranscriptPath: recorder.Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	state := eng.goalService.snapshot()
	if state == nil ||
		state.Status != goalStatusPaused ||
		state.StatusReasonCode != goalReasonColdContinuationUnavailable ||
		state.Revision != record.Revision+1 ||
		state.GoalID != record.GoalID {
		t.Fatalf("cold-normalized Goal = %#v", state)
	}
	if !containsSessionWarning(
		resumed.Warnings,
		"no eligible durable continuation cursor",
	) {
		t.Fatalf("resume warnings = %v", resumed.Warnings)
	}
	assertP241PersistedGoal(t, eng, func(persisted *session.PersistedGoalState) {
		if persisted == nil ||
			persisted.Status != string(goalStatusPaused) ||
			persisted.StatusReasonCode != goalReasonColdContinuationUnavailable ||
			persisted.Revision != record.Revision+1 {
			t.Fatalf("persisted cold normalization = %#v", persisted)
		}
	})
}

func TestP241StagedColdGoalNormalizationPersistsOnCommit(t *testing.T) {
	now := time.Date(2026, 7, 28, 13, 30, 0, 0, time.UTC)
	record := validP241PersistedGoal(now)
	record.Status = string(goalStatusActive)
	recorder, _, dir := writeP241GoalSession(t, "staged-active-goal", record)
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}

	eng := newP234aRestoreStagingEngine(t, dir, "staging-host")
	if _, err := eng.ResumeSession(t.Context(), "staged-active-goal"); err != nil {
		t.Fatal(err)
	}
	if state := eng.goalService.snapshot(); state == nil ||
		state.Status != goalStatusPaused ||
		state.StatusReasonCode != goalReasonColdContinuationUnavailable {
		t.Fatalf("staged normalized Goal = %#v", state)
	}
	loadedBefore, err := transcript.NewRecorder(
		"staged-active-goal",
		dir,
	).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	before := session.ReadSessionMetadataFull(loadedBefore)
	if before == nil ||
		before.GoalState == nil ||
		before.GoalState.Status != string(goalStatusActive) {
		t.Fatalf("staged resume mutated durable Goal = %#v", before)
	}

	if err := eng.CommitRestoreStaging(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	loadedAfter, err := transcript.NewRecorder(
		"staged-active-goal",
		dir,
	).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	after := session.ReadSessionMetadataFull(loadedAfter)
	if after == nil ||
		after.GoalState == nil ||
		after.GoalState.Status != string(goalStatusPaused) ||
		after.GoalState.StatusReasonCode != goalReasonColdContinuationUnavailable ||
		after.GoalState.Revision != record.Revision+1 {
		t.Fatalf("committed normalized Goal = %#v", after)
	}
}

func TestP241StagedCommitRetriesAcrossRuntimeAndGoalOwners(t *testing.T) {
	now := time.Date(2026, 7, 28, 13, 45, 0, 0, time.UTC)
	record := validP241PersistedGoal(now)
	record.Status = string(goalStatusActive)
	recorder, _, dir := writeP241GoalSession(t, "staged-goal-failure", record)
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "staged-goal-failure.jsonl")
	originalTranscript, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath := RuntimeInputPersistencePath(path)
	coordinator, err := NewRuntimeInputCoordinator(
		RuntimeInputCoordinatorConfig{
			SessionID: "staged-goal-failure",
			ThreadID:  "staged-goal-failure",
			Path:      ledgerPath,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	item := runtimePromptTestItem(
		"recover-with-goal",
		RuntimePriorityNext,
		false,
	)
	item.Scope = RuntimeInputScope{
		SessionID: "staged-goal-failure",
		ThreadID:  "staged-goal-failure",
	}
	if _, err := coordinator.Enqueue(item); err != nil {
		t.Fatal(err)
	}
	if _, found, err := coordinator.claimByID(item.ID); err != nil || !found {
		t.Fatalf("claim runtime item: found=%v err=%v", found, err)
	}
	ledgerBefore, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}

	eng := newP234aRestoreStagingEngine(t, dir, "staging-failure-host")
	if _, err := eng.ResumeSession(t.Context(), "staged-goal-failure"); err != nil {
		t.Fatal(err)
	}
	if err := eng.GetTranscript().Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := eng.CommitRestoreStaging(); !errors.Is(
		err,
		ErrRestoreStagingTransition,
	) {
		t.Fatalf("commit error = %v", err)
	}
	if err := eng.ensureRestoreStagingCommitted(); !errors.Is(
		err,
		ErrRestoreStagingTransition,
	) {
		t.Fatalf("staging state after checkpoint failure = %v", err)
	}
	if err := eng.AbortRestoreStaging(); !errors.Is(
		err,
		ErrRestoreStagingTransition,
	) {
		t.Fatalf("abort after commit start = %v", err)
	}
	ledgerAfterFailure, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(ledgerAfterFailure) == string(ledgerBefore) {
		t.Fatal("runtime-input recovery did not commit before Goal retry")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, originalTranscript, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := eng.CommitRestoreStaging(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	assertP241PersistedGoal(t, eng, func(persisted *session.PersistedGoalState) {
		if persisted == nil ||
			persisted.Status != string(goalStatusPaused) ||
			persisted.StatusReasonCode != goalReasonColdContinuationUnavailable {
			t.Fatalf("retried staged Goal = %#v", persisted)
		}
	})
	items := eng.RuntimeItems()
	if len(items) != 1 ||
		items[0].ID != item.ID ||
		items[0].State != RuntimeItemPending {
		t.Fatalf("retried runtime items = %#v", items)
	}
}

func TestP241ColdTerminalAndLimitedGoalsRemainInert(t *testing.T) {
	now := time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC)
	for _, status := range []goalStatus{
		goalStatusPaused,
		goalStatusBlocked,
		goalStatusUsageLimited,
		goalStatusBudgetLimited,
		goalStatusComplete,
	} {
		t.Run(string(status), func(t *testing.T) {
			record := validP241PersistedGoal(now)
			record.Status = string(status)
			if status == goalStatusBudgetLimited {
				record.TokensUsed = *record.TokenBudget
			}
			state, warnings, checkpointRequired := restorePersistedGoalState(
				record,
				"",
				now.Add(time.Hour),
			)
			if len(warnings) != 0 || checkpointRequired {
				t.Fatalf(
					"restore diagnostics = warnings:%v checkpoint:%v",
					warnings,
					checkpointRequired,
				)
			}
			if state == nil ||
				state.unavailable ||
				state.Status != status ||
				state.Revision != record.Revision {
				t.Fatalf("restored Goal = %#v", state)
			}
		})
	}
}

func TestP241CompleteGoalSurvivesSessionRestart(t *testing.T) {
	now := time.Date(2026, 7, 28, 14, 30, 0, 0, time.UTC)
	record := validP241PersistedGoal(now)
	record.Status = string(goalStatusComplete)
	recorder, cwd, dir := writeP241GoalSession(t, "complete-goal", record)

	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "current",
		ThreadID:      "current",
		CWD:           cwd,
		TranscriptDir: dir,
		Clock:         func() time.Time { return now.Add(time.Hour) },
	})
	eng.agentRunner.SetOutputDir(filepath.Join(t.TempDir(), "agent-output"))
	t.Cleanup(eng.Close)
	resumed, err := eng.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID:      "complete-goal",
		CWD:            cwd,
		TranscriptDir:  dir,
		TranscriptPath: recorder.Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.Warnings) != 0 {
		t.Fatalf("resume warnings = %v", resumed.Warnings)
	}
	state := eng.goalService.snapshot()
	if state == nil ||
		state.Status != goalStatusComplete ||
		state.GoalID != record.GoalID ||
		state.Revision != record.Revision {
		t.Fatalf("restarted complete Goal = %#v", state)
	}
	assertP241PersistedGoal(t, eng, func(persisted *session.PersistedGoalState) {
		if persisted == nil ||
			persisted.Status != string(goalStatusComplete) ||
			persisted.Revision != record.Revision {
			t.Fatalf("persisted complete Goal = %#v", persisted)
		}
	})
}

func TestP241UnknownAndCorruptGoalStateFailClosed(t *testing.T) {
	now := time.Date(2026, 7, 28, 15, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		mutate     func(*session.PersistedGoalState)
		reasonCode string
	}{
		{
			name: "unknown version",
			mutate: func(record *session.PersistedGoalState) {
				record.Version = session.PersistedGoalStateVersion + 1
			},
			reasonCode: goalReasonUnsupportedVersion,
		},
		{
			name: "empty objective",
			mutate: func(record *session.PersistedGoalState) {
				record.Objective = ""
			},
			reasonCode: goalReasonCorruptState,
		},
		{
			name: "zero active budget",
			mutate: func(record *session.PersistedGoalState) {
				record.Status = string(goalStatusActive)
				record.TokenBudget = p241Uint64(0)
			},
			reasonCode: goalReasonCorruptState,
		},
		{
			name: "duplicate blocker turn",
			mutate: func(record *session.PersistedGoalState) {
				record.BlockerKey = "same-blocker"
				record.BlockerTurnIDs = []string{"turn-1", "turn-1"}
			},
			reasonCode: goalReasonCorruptState,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := validP241PersistedGoal(now)
			test.mutate(record)
			state, warnings, checkpointRequired := restorePersistedGoalState(
				record,
				"",
				now.Add(time.Hour),
			)
			if state == nil ||
				!state.unavailable ||
				state.Status != goalStatusPaused ||
				state.StatusReasonCode != test.reasonCode {
				t.Fatalf("unavailable Goal = %#v", state)
			}
			if len(warnings) != 1 {
				t.Fatalf("warnings = %v", warnings)
			}
			if checkpointRequired {
				t.Fatal("unavailable Goal requested destructive normalization")
			}
			if persisted := persistedGoalState(state); persisted == nil ||
				persisted.Version != record.Version {
				t.Fatalf("preserved unavailable record = %#v", persisted)
			}
		})
	}
}

func TestP241UnavailableGoalCanOnlyBeCleared(t *testing.T) {
	now := time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC)
	record := validP241PersistedGoal(now)
	record.Version++
	recorder, cwd, dir := writeP241GoalSession(t, "unknown-goal", record)
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "current",
		ThreadID:      "current",
		CWD:           cwd,
		TranscriptDir: dir,
		Clock:         func() time.Time { return now.Add(time.Hour) },
	})
	eng.agentRunner.SetOutputDir(filepath.Join(t.TempDir(), "agent-output"))
	t.Cleanup(eng.Close)
	if _, err := eng.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID:      "unknown-goal",
		CWD:            cwd,
		TranscriptDir:  dir,
		TranscriptPath: recorder.Path(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.goalService.edit("replacement"); !errors.Is(err, errGoalUnavailable) {
		t.Fatalf("edit unavailable error = %v", err)
	}
	if _, err := eng.goalService.resume(); !errors.Is(err, errGoalUnavailable) {
		t.Fatalf("resume unavailable error = %v", err)
	}
	if err := eng.goalService.clear(); err != nil {
		t.Fatal(err)
	}
	assertP241PersistedGoal(t, eng, func(persisted *session.PersistedGoalState) {
		if persisted != nil {
			t.Fatalf("Goal after clear = %#v", persisted)
		}
	})
}

func TestP241ChildGoalMetadataIsIgnored(t *testing.T) {
	now := time.Date(2026, 7, 28, 17, 0, 0, 0, time.UTC)
	state, warnings, checkpointRequired := restorePersistedGoalState(
		validP241PersistedGoal(now),
		"child-agent",
		now,
	)
	if state != nil || len(warnings) != 1 ||
		!strings.Contains(warnings[0], "child or review") ||
		!checkpointRequired {
		t.Fatalf(
			"child restore = state:%#v warnings:%v checkpoint:%v",
			state,
			warnings,
			checkpointRequired,
		)
	}
}

func TestP241GoalMetadataIsAdditiveForOlderReaders(t *testing.T) {
	now := time.Date(2026, 7, 28, 18, 0, 0, 0, time.UTC)
	data, err := json.Marshal(session.SessionMetadataFull{
		SessionID: "session",
		ThreadID:  "thread",
		GoalState: validP241PersistedGoal(now),
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	var legacy struct {
		SessionID string `json:"session_id"`
		ThreadID  string `json:"thread_id"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.SessionID != "session" || legacy.ThreadID != "thread" {
		t.Fatalf("legacy metadata = %#v", legacy)
	}
}

func TestP241MalformedGoalShapePreservesOuterSessionMetadata(t *testing.T) {
	now := time.Date(2026, 7, 28, 18, 30, 0, 0, time.UTC)
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	data, err := json.Marshal(map[string]any{
		"session_id":                "malformed-goal",
		"thread_id":                 "malformed-goal",
		"cwd":                       cwd,
		"permission_mode":           string(permission.ModeDefault),
		"query_kernel_version":      queryKernelVersionProjectGraph,
		"query_kernel_canary_stage": string(queryKernelStageFull),
		"goal_state":                "malformed nested state",
		"created_at":                now,
		"updated_at":                now,
		"message_count":             2,
	})
	if err != nil {
		t.Fatal(err)
	}
	var metadata session.SessionMetadataFull
	if err = json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.SessionID != "malformed-goal" ||
		metadata.ThreadID != "malformed-goal" ||
		metadata.GoalState == nil ||
		!metadata.GoalState.HasInvalidEncoding() {
		t.Fatalf("contained metadata = %#v", metadata)
	}

	state, warnings, checkpointRequired := restorePersistedGoalState(
		metadata.GoalState,
		"",
		now,
	)
	if state == nil ||
		!state.unavailable ||
		state.StatusReasonCode != goalReasonCorruptState ||
		len(warnings) != 1 ||
		!strings.Contains(warnings[0], "invalid encoding") ||
		checkpointRequired {
		t.Fatalf(
			"malformed restore = state:%#v warnings:%v checkpoint:%v",
			state,
			warnings,
			checkpointRequired,
		)
	}

	roundTrip, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(roundTrip, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["goal_state"]) != `"malformed nested state"` {
		t.Fatalf("preserved nested Goal = %s", raw["goal_state"])
	}

	recorder := transcript.NewRecorder("malformed-goal", dir)
	if err := recorder.Replace([]*schema.Message{
		{Role: schema.User, Content: "prompt"},
		{Role: schema.Assistant, Content: "response"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordMetadata("session_metadata_full", string(data)); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "current",
		ThreadID:      "current",
		CWD:           cwd,
		TranscriptDir: dir,
		Clock:         func() time.Time { return now.Add(time.Hour) },
	})
	eng.agentRunner.SetOutputDir(filepath.Join(t.TempDir(), "agent-output"))
	t.Cleanup(eng.Close)
	resumed, err := eng.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID:      "malformed-goal",
		CWD:            cwd,
		TranscriptDir:  dir,
		TranscriptPath: recorder.Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.SessionID != "malformed-goal" ||
		!containsSessionWarning(resumed.Warnings, "invalid encoding") {
		t.Fatalf("resumed Session = %#v warnings=%v", resumed.Metadata, resumed.Warnings)
	}
	if state := eng.goalService.snapshot(); state == nil ||
		!state.unavailable ||
		state.StatusReasonCode != goalReasonCorruptState {
		t.Fatalf("contained malformed Goal = %#v", state)
	}
	assertP241PersistedGoal(t, eng, func(persisted *session.PersistedGoalState) {
		if persisted == nil || !persisted.HasInvalidEncoding() {
			t.Fatalf("persisted malformed Goal = %#v", persisted)
		}
	})
}

func TestP49LegacyPendingGoalUsageAdmissionRemainsUnavailable(t *testing.T) {
	now := time.Date(2026, 8, 7, 4, 0, 0, 0, time.UTC)
	legacyAdmission := json.RawMessage(`{"version":1,"ledger_revision":1,"goal_id":"goal-1","objective_revision":1,"root_session_id":"root","root_thread_id":"root","executing_session_id":"root","executing_thread_id":"root","goal_turn_id":"turn-1","logical_round_id":"round-1","provider_call_id":"call-1","admitted_at":"2026-08-07T04:00:00Z"}`)
	rawState := []byte(`{"version":` +
		fmt.Sprint(session.PersistedGoalStateVersion) +
		`,"goal_id":"goal-1","objective":"finish the accepted slice","objective_revision":1,"status":"paused","revision":7,"token_budget":100,"last_goal_turn_id":"turn-1","pending_usage_admission":` +
		string(legacyAdmission) +
		`,"created_at":"2026-08-07T04:00:00Z","updated_at":"2026-08-07T04:00:00Z"}`)
	var record session.PersistedGoalState
	if err := json.Unmarshal(rawState, &record); err != nil {
		t.Fatal(err)
	}

	restored, warnings, checkpoint := restorePersistedGoalStateWithUsage(
		&record,
		"",
		now.Add(time.Minute),
		&transcript.LoadResult{},
		true,
	)
	if checkpoint || restored == nil || !restored.unavailable ||
		restored.StatusReasonCode != goalReasonUnsupportedVersion ||
		len(warnings) == 0 {
		t.Fatalf(
			"legacy pending admission restore = %#v warnings=%v checkpoint=%v",
			restored,
			warnings,
			checkpoint,
		)
	}
	preserved, err := json.Marshal(persistedGoalState(restored))
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Pending json.RawMessage `json:"pending_usage_admission"`
	}
	if err := json.Unmarshal(preserved, &envelope); err != nil {
		t.Fatal(err)
	}
	if string(envelope.Pending) != string(legacyAdmission) {
		t.Fatalf(
			"legacy admission bytes changed:\n got: %s\nwant: %s",
			envelope.Pending,
			legacyAdmission,
		)
	}
}

func TestP49GoalStateV4AllowsUnbudgetedActive(t *testing.T) {
	if session.PersistedGoalStateVersion != 4 {
		t.Fatalf("current Goal state version = %d, want 4", session.PersistedGoalStateVersion)
	}
	now := time.Date(2026, 8, 7, 6, 0, 0, 0, time.UTC)
	record := validP241PersistedGoal(now)
	record.Status = string(goalStatusActive)
	record.TokenBudget = nil
	if err := validatePersistedGoalState(record); err != nil {
		t.Fatalf("v4 active unbudgeted state: %v", err)
	}
}

func TestP49LegacyActiveNilBudgetFailsClosed(t *testing.T) {
	now := time.Date(2026, 8, 7, 6, 15, 0, 0, time.UTC)
	versions := []uint16{
		session.PersistedGoalStateLegacyVersion,
		session.PersistedGoalStateAccountingVersion,
		session.PersistedGoalStateContinuationVersion,
	}
	for _, version := range versions {
		t.Run(fmt.Sprintf("version-%d", version), func(t *testing.T) {
			record := validP241PersistedGoal(now)
			record.Version = version
			record.Status = string(goalStatusActive)
			record.TokenBudget = nil
			restored, warnings, checkpoint := restorePersistedGoalState(
				record,
				"",
				now.Add(time.Hour),
			)
			if checkpoint || restored == nil || !restored.unavailable ||
				len(warnings) == 0 {
				t.Fatalf(
					"legacy active nil restore = %#v warnings=%v checkpoint=%v",
					restored,
					warnings,
					checkpoint,
				)
			}
		})
	}
}

func validP241PersistedGoal(now time.Time) *session.PersistedGoalState {
	return &session.PersistedGoalState{
		Version:           session.PersistedGoalStateVersion,
		GoalID:            "goal-1",
		Objective:         "finish the accepted slice",
		ObjectiveRevision: 1,
		Status:            string(goalStatusPaused),
		Revision:          7,
		TokenBudget:       p241Uint64(100),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

func writeP241GoalSession(
	t *testing.T,
	sessionID string,
	goal *session.PersistedGoalState,
) (*transcript.Recorder, string, string) {
	t.Helper()
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "transcripts")
	recorder := transcript.NewRecorder(sessionID, dir)
	if err := recorder.Replace([]*schema.Message{
		{Role: schema.User, Content: "prompt"},
		{Role: schema.Assistant, Content: "response"},
	}); err != nil {
		t.Fatal(err)
	}
	now := goal.CreatedAt
	writeProjectGraphRootTestMetadata(t, recorder, &session.SessionMetadataFull{
		SessionID:      sessionID,
		ThreadID:       sessionID,
		CWD:            cwd,
		PermissionMode: string(permission.ModeDefault),
		GoalState:      goal,
		CreatedAt:      now,
		UpdatedAt:      now,
		MessageCount:   2,
	})
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	return recorder, cwd, dir
}
