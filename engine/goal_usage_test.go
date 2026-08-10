package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
)

func TestP242bProjectGraphFailsClosedWhenFinalUsageIsMissing(t *testing.T) {
	model := &canonicalScriptModel{
		responses: []canonicalModelResponse{{
			chunks: []*schema.Message{{
				Role:    schema.Assistant,
				Content: "unaccounted response",
			}},
		}},
	}
	eng := NewQueryEngine(projectGraphEngineConfig(
		t,
		t.TempDir(),
		"p24-2b-missing-final-usage",
		model,
		tools.NewRegistry(),
		&tools.ToolSelection{},
	))
	t.Cleanup(eng.Close)
	budget := uint64(100)
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "fail closed on missing provider usage",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}

	events, synchronous := eng.SubmitMessage(
		context.Background(),
		"produce one response",
	)
	if synchronous.Err != nil {
		t.Fatalf("synchronous submit error = %v", synchronous.Err)
	}
	var terminal *Terminal
	for event := range events {
		if event.Type == EventTerminal {
			terminal = event.TerminalInfo
		}
	}
	if terminal == nil ||
		terminal.Reason != TerminalModelError ||
		!execution.IsProviderUsageTerminalError(terminal.Err) {
		t.Fatalf(
			"missing-usage terminal = %#v, error type=%T value=%v",
			terminal,
			terminal.Err,
			terminal.Err,
		)
	}
	state := eng.goalService.snapshot()
	if state.Status != goalStatusUsageLimited ||
		state.StatusReasonCode != goalReasonUsageCoverageIncomplete ||
		state.PendingUsageAdmission == nil ||
		state.UsageLedgerRevision != 0 ||
		state.TokensUsed != 0 {
		t.Fatalf("missing-usage Goal state = %#v", state)
	}
	if model.callCount != 1 {
		t.Fatalf("model calls = %d, want exactly one", model.callCount)
	}
	loaded, err := eng.transcript.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.GoalUsageRecords) != 0 {
		t.Fatalf("missing usage produced ledger records = %#v", loaded.GoalUsageRecords)
	}
}

func TestP242bNoGoalExposesNoProviderUsageCapability(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{})
	t.Cleanup(eng.Close)
	if reporter := eng.currentGoalProviderUsageReporter(); reporter != nil {
		t.Fatalf("unexpected concrete Goal usage reporter = %#v", reporter)
	}
	if admitter := eng.currentGoalProviderUsageAdmitter(); admitter != nil {
		t.Fatalf("unexpected Goal usage capability = %#v", admitter)
	}
}

func TestP49UnbudgetedUsageNeverBudgetLimits(t *testing.T) {
	eng, reporter, finish := newP49UnbudgetedGoalUsageRuntime(t)
	defer finish()
	call, err := reporter.AdmitProviderUsage(
		context.Background(),
		execution.ProviderUsageDescriptor{
			LogicalRoundID: reporter.NewLogicalRoundID(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := call.CompleteProviderUsage(&schema.TokenUsage{TotalTokens: 25}); err != nil {
		t.Fatal(err)
	}
	state := eng.goalService.snapshot()
	if state.TokenBudget != nil ||
		state.TokensUsed != 25 ||
		state.UsageLedgerRevision != 1 ||
		state.Status != goalStatusActive ||
		state.StatusReasonCode == goalReasonBudgetExhausted {
		t.Fatalf("unbudgeted Goal usage = %#v", state)
	}
}

func TestP49BudgetAddedAfterUsageAppliesCommittedTotal(t *testing.T) {
	eng, reporter, finish := newP49UnbudgetedGoalUsageRuntime(t)
	call, err := reporter.AdmitProviderUsage(
		context.Background(),
		execution.ProviderUsageDescriptor{
			LogicalRoundID: reporter.NewLogicalRoundID(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := call.CompleteProviderUsage(&schema.TokenUsage{TotalTokens: 25}); err != nil {
		t.Fatal(err)
	}
	finish()
	limited, err := eng.goalService.setBudget(25)
	if err != nil {
		t.Fatal(err)
	}
	if limited.TokenBudget == nil || *limited.TokenBudget != 25 ||
		limited.TokensUsed != 25 ||
		limited.Status != goalStatusBudgetLimited ||
		limited.StatusReasonCode != goalReasonBudgetExhausted {
		t.Fatalf("post-usage Goal cap = %#v", limited)
	}
}

func TestP242bGoalProviderUsageAdmissionAggregateAndBudget(t *testing.T) {
	eng, reporter, finish := newP242bGoalRuntime(t, 80)
	defer finish()

	call, err := reporter.AdmitProviderUsage(
		context.Background(),
		execution.ProviderUsageDescriptor{
			LogicalRoundID: reporter.NewLogicalRoundID(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	pending := eng.goalService.snapshot()
	if pending.PendingUsageAdmission == nil ||
		pending.PendingUsageAdmission.ProviderCallID != call.ProviderCallID() ||
		pending.UsageLedgerRevision != 0 ||
		goalUsageCoverage(pending) != "pending" {
		t.Fatalf("durable pending provider admission = %#v", pending)
	}
	loadedPending, err := eng.transcript.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	metadata := session.ReadSessionMetadataFull(loadedPending)
	if metadata == nil ||
		metadata.GoalState == nil ||
		metadata.GoalState.Version != session.PersistedGoalStateVersion ||
		metadata.GoalState.PendingUsageAdmission == nil ||
		metadata.GoalState.PendingUsageAdmission.ProviderCallID !=
			call.ProviderCallID() {
		t.Fatalf("persisted Goal provider admission = %#v", metadata)
	}

	if err := call.CompleteProviderUsage(&schema.TokenUsage{
		PromptTokens: 100,
		PromptTokenDetails: schema.PromptTokenDetails{
			CachedTokens: 40,
		},
		CompletionTokens: 20,
		TotalTokens:      120,
	}); err != nil {
		t.Fatal(err)
	}
	if err := call.CompleteProviderUsage(&schema.TokenUsage{
		TotalTokens: 999,
	}); err != nil {
		t.Fatal(err)
	}
	aggregated := eng.goalService.snapshot()
	if aggregated.TokensUsed != 80 ||
		aggregated.UsageLedgerRevision != 1 ||
		aggregated.PendingUsageAdmission != nil ||
		aggregated.Status != goalStatusBudgetLimited ||
		aggregated.StatusReasonCode != goalReasonBudgetExhausted {
		t.Fatalf("aggregated Goal provider usage = %#v", aggregated)
	}
	loaded, err := eng.transcript.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.GoalUsageRecords) != 1 ||
		loaded.GoalUsageRecords[0].ProviderCallID != call.ProviderCallID() ||
		loaded.GoalUsageRecords[0].BillableTokens != 80 {
		t.Fatalf("Goal usage ledger = %#v", loaded.GoalUsageRecords)
	}
}

func TestP294GoalUsageKeepsExactAttemptAttribution(t *testing.T) {
	eng, reporter, finish := newP242bGoalRuntime(t, 80)
	defer finish()

	call, err := reporter.AdmitProviderUsage(
		context.Background(),
		execution.ProviderUsageDescriptor{
			LogicalRoundID:    reporter.NewLogicalRoundID(),
			LogicalRequestID:  "request-1",
			ModelAttemptID:    "attempt-2",
			ModelAttemptIndex: 1,
			ModelRetryIndex:   2,
			ModelProfile:      "fallback-profile",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	loadedPending, err := eng.transcript.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	metadata := session.ReadSessionMetadataFull(loadedPending)
	if metadata == nil || metadata.GoalState == nil ||
		metadata.GoalState.PendingUsageAdmission == nil {
		t.Fatalf("persisted pending admission = %#v", metadata)
	}
	persistedAdmission := metadata.GoalState.PendingUsageAdmission
	if persistedAdmission.Version != session.PersistedGoalUsageAdmissionVersion ||
		persistedAdmission.LogicalRequestID != "request-1" ||
		persistedAdmission.ModelAttemptID != "attempt-2" ||
		persistedAdmission.ModelAttemptIndex != 1 ||
		persistedAdmission.ModelRetryIndex != 2 ||
		persistedAdmission.ModelProfile != "fallback-profile" {
		t.Fatalf("persisted attempt attribution = %#v", persistedAdmission)
	}
	if err := call.CompleteProviderUsage(&schema.TokenUsage{
		PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7,
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := eng.transcript.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.GoalUsageRecords) != 1 {
		t.Fatalf("usage records = %#v", loaded.GoalUsageRecords)
	}
	record := loaded.GoalUsageRecords[0]
	if record.LogicalRequestID != "request-1" ||
		record.ModelAttemptID != "attempt-2" ||
		record.ModelAttemptIndex != 1 ||
		record.ModelRetryIndex != 2 ||
		record.ModelProfile != "fallback-profile" {
		t.Fatalf("attempt attribution = %#v", record)
	}
}

func TestP49GoalUsageRecoveryRejectsMutatedAttemptIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*transcript.GoalUsageRecord)
	}{
		{
			name: "logical request",
			mutate: func(record *transcript.GoalUsageRecord) {
				record.LogicalRequestID = "request-other"
			},
		},
		{
			name: "model attempt",
			mutate: func(record *transcript.GoalUsageRecord) {
				record.ModelAttemptID = "attempt-other"
			},
		},
		{
			name: "model attempt index",
			mutate: func(record *transcript.GoalUsageRecord) {
				record.ModelAttemptIndex++
			},
		},
		{
			name: "model profile",
			mutate: func(record *transcript.GoalUsageRecord) {
				record.ModelProfile = "other-profile"
			},
		},
		{
			name: "model retry index",
			mutate: func(record *transcript.GoalUsageRecord) {
				record.ModelRetryIndex++
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eng, reporter, finish := newP242bGoalRuntime(t, 100)
			defer finish()
			if _, err := reporter.AdmitProviderUsage(
				context.Background(),
				execution.ProviderUsageDescriptor{
					LogicalRoundID:    reporter.NewLogicalRoundID(),
					LogicalRequestID:  "request-1",
					ModelAttemptID:    "attempt-1",
					ModelAttemptIndex: 1,
					ModelProfile:      "primary-profile",
					ModelRetryIndex:   1,
				},
			); err != nil {
				t.Fatal(err)
			}
			pending := eng.goalService.snapshot().PendingUsageAdmission
			record := p242bGoalUsageRecordForAdmission(pending, 10)
			tc.mutate(&record)
			if err := eng.transcript.RecordGoalUsage(record); err != nil {
				t.Fatal(err)
			}
			loaded, err := eng.transcript.LoadFull()
			if err != nil {
				t.Fatal(err)
			}
			metadata := session.ReadSessionMetadataFull(loaded)
			restored, warnings, checkpoint := restorePersistedGoalStateWithUsage(
				metadata.GoalState,
				"",
				time.Date(2026, 7, 29, 3, 0, 0, 0, time.UTC),
				loaded,
				true,
			)
			if !checkpoint || restored == nil ||
				restored.Status != goalStatusUsageLimited ||
				restored.PendingUsageAdmission == nil ||
				restored.TokensUsed != 0 ||
				restored.UsageLedgerRevision != 0 ||
				len(warnings) == 0 {
				t.Fatalf(
					"mutated attempt recovery = %#v warnings=%v checkpoint=%v",
					restored,
					warnings,
					checkpoint,
				)
			}
		})
	}
}

func TestP242bGoalProviderUsagePreDispatchReleaseAndAmbiguousFailure(
	t *testing.T,
) {
	t.Run("proven pre-dispatch release admits a later call", func(t *testing.T) {
		eng, reporter, finish := newP242bGoalRuntime(t, 100)
		defer finish()
		call, err := reporter.AdmitProviderUsage(
			context.Background(),
			execution.ProviderUsageDescriptor{
				LogicalRoundID: reporter.NewLogicalRoundID(),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := call.ReleaseProviderUsageBeforeDispatch(); err != nil {
			t.Fatal(err)
		}
		released := eng.goalService.snapshot()
		if released.PendingUsageAdmission != nil ||
			released.UsageLedgerRevision != 0 ||
			released.TokensUsed != 0 {
			t.Fatalf("released pre-dispatch admission = %#v", released)
		}
		next, err := reporter.AdmitProviderUsage(
			context.Background(),
			execution.ProviderUsageDescriptor{
				LogicalRoundID: reporter.NewLogicalRoundID(),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := next.CompleteProviderUsage(&schema.TokenUsage{}); err != nil {
			t.Fatal(err)
		}
		if got := eng.goalService.snapshot(); got.UsageLedgerRevision != 1 ||
			got.TokensUsed != 0 {
			t.Fatalf("known-zero Goal usage = %#v", got)
		}
	})

	t.Run("ambiguous dispatch fails closed", func(t *testing.T) {
		eng, reporter, finish := newP242bGoalRuntime(t, 100)
		defer finish()
		call, err := reporter.AdmitProviderUsage(
			context.Background(),
			execution.ProviderUsageDescriptor{
				LogicalRoundID: reporter.NewLogicalRoundID(),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := call.MarkProviderUsageAmbiguous(
			errors.New("stream ended without final metadata"),
		); err != nil {
			t.Fatal(err)
		}
		limited := eng.goalService.snapshot()
		if limited.Status != goalStatusUsageLimited ||
			limited.StatusReasonCode != goalReasonUsageCoverageIncomplete ||
			limited.PendingUsageAdmission == nil {
			t.Fatalf("ambiguous Goal usage state = %#v", limited)
		}
		if _, err := reporter.AdmitProviderUsage(
			context.Background(),
			execution.ProviderUsageDescriptor{
				LogicalRoundID: reporter.NewLogicalRoundID(),
			},
		); err == nil {
			t.Fatal("ambiguous Goal usage admitted another provider call")
		}
	})
}

func TestP242bGoalProviderUsageRecoveryCrashWindows(t *testing.T) {
	t.Run("pending admission without record restores usage limited", func(t *testing.T) {
		eng, reporter, finish := newP242bGoalRuntime(t, 100)
		defer finish()
		if _, err := reporter.AdmitProviderUsage(
			context.Background(),
			execution.ProviderUsageDescriptor{
				LogicalRoundID: reporter.NewLogicalRoundID(),
			},
		); err != nil {
			t.Fatal(err)
		}
		loaded, err := eng.transcript.LoadFull()
		if err != nil {
			t.Fatal(err)
		}
		metadata := session.ReadSessionMetadataFull(loaded)
		restoreAt := time.Date(2026, 7, 29, 3, 0, 0, 0, time.UTC)
		restored, warnings, checkpoint := restorePersistedGoalStateWithUsage(
			metadata.GoalState,
			"",
			restoreAt,
			loaded,
			true,
		)
		if !checkpoint ||
			restored.Status != goalStatusUsageLimited ||
			restored.PendingUsageAdmission == nil ||
			len(warnings) == 0 {
			t.Fatalf(
				"missing-record recovery = %#v warnings=%v checkpoint=%v",
				restored,
				warnings,
				checkpoint,
			)
		}
	})

	t.Run("flushed record reconciles exactly once", func(t *testing.T) {
		eng, reporter, finish := newP242bGoalRuntime(t, 100)
		defer finish()
		call, err := reporter.AdmitProviderUsage(
			context.Background(),
			execution.ProviderUsageDescriptor{
				LogicalRoundID: reporter.NewLogicalRoundID(),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		pending := eng.goalService.snapshot().PendingUsageAdmission
		record := p242bGoalUsageRecordForAdmission(pending, 25)
		if err := eng.transcript.RecordGoalUsage(record); err != nil {
			t.Fatal(err)
		}
		loaded, err := eng.transcript.LoadFull()
		if err != nil {
			t.Fatal(err)
		}
		metadata := session.ReadSessionMetadataFull(loaded)
		restoreAt := time.Date(2026, 7, 29, 3, 0, 0, 0, time.UTC)
		restored, _, checkpoint := restorePersistedGoalStateWithUsage(
			metadata.GoalState,
			"",
			restoreAt,
			loaded,
			true,
		)
		if !checkpoint ||
			restored.TokensUsed != 25 ||
			restored.UsageLedgerRevision != 1 ||
			restored.PendingUsageAdmission != nil ||
			restored.Status != goalStatusPaused {
			t.Fatalf("record-flush recovery = %#v", restored)
		}
		replayed, warnings, replayCheckpoint := restorePersistedGoalStateWithUsage(
			persistedGoalState(restored),
			"",
			restoreAt.Add(time.Minute),
			loaded,
			true,
		)
		if replayCheckpoint ||
			replayed.TokensUsed != 25 ||
			replayed.UsageLedgerRevision != 1 ||
			len(warnings) != 0 {
			t.Fatalf(
				"idempotent recovery = %#v warnings=%v checkpoint=%v",
				replayed,
				warnings,
				replayCheckpoint,
			)
		}
		_ = call
	})
}

func TestP242bGoalProviderUsageDurabilityUncertainFailsClosedInProcess(
	t *testing.T,
) {
	eng, reporter, finish := newP242bGoalRuntime(t, 100)
	defer finish()
	eng.goalService.recordGoalUsageFn = func(
		record transcript.GoalUsageRecord,
	) error {
		if err := eng.transcript.RecordGoalUsage(record); err != nil {
			return err
		}
		return &transcript.DurabilityUncertainError{
			Operation: "injected Goal usage sync",
			Err:       errors.New("injected sync failure"),
		}
	}

	call, err := reporter.AdmitProviderUsage(
		context.Background(),
		execution.ProviderUsageDescriptor{
			LogicalRoundID: reporter.NewLogicalRoundID(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	err = call.CompleteProviderUsage(&schema.TokenUsage{TotalTokens: 10})
	if err == nil || !transcript.IsDurabilityUncertain(err) {
		t.Fatalf("durability-uncertain completion error = %v", err)
	}
	state := eng.goalService.snapshot()
	if !eng.goalService.usageUncertain.Load() ||
		state.Status != goalStatusUsageLimited ||
		state.StatusReasonCode != goalReasonUsageCoverageIncomplete ||
		state.PendingUsageAdmission == nil ||
		state.UsageLedgerRevision != 0 ||
		state.TokensUsed != 0 {
		t.Fatalf("durability-uncertain Goal state = %#v", state)
	}
	loaded, loadErr := eng.transcript.LoadFull()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(loaded.GoalUsageRecords) != 1 ||
		loaded.GoalUsageRecords[0].BillableTokens != 10 {
		t.Fatalf("visible but uncertain Goal usage ledger = %#v", loaded.GoalUsageRecords)
	}
	if next, admitErr := reporter.AdmitProviderUsage(
		context.Background(),
		execution.ProviderUsageDescriptor{
			LogicalRoundID: reporter.NewLogicalRoundID(),
		},
	); next != nil || !errors.Is(admitErr, errGoalUsageCoverageIncomplete) {
		t.Fatalf(
			"post-uncertainty admission call=%#v error=%v",
			next,
			admitErr,
		)
	}
}

func TestP242bGoalHelperSideQueriesAggregateThroughRootUsageService(
	t *testing.T,
) {
	eng, reporter, finish := newP242bGoalRuntime(t, 100)
	defer finish()
	model := &canonicalScriptModel{
		responses: []canonicalModelResponse{
			{
				chunks: []*schema.Message{{
					Role:    schema.Assistant,
					Content: "Ran bounded check",
					ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
						PromptTokens:     6,
						CompletionTokens: 3,
						TotalTokens:      9,
					}},
				}},
			},
			{
				chunks: []*schema.Message{{
					Role: schema.Assistant,
					ToolCalls: []schema.ToolCall{{
						ID:   "explain-goal-call",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "explain_command",
							Arguments: `{"riskLevel":"LOW","explanation":"Prints the working directory.","reasoning":"I need to verify the active directory.","risk":"May expose a local path"}`,
						},
					}},
					ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
						PromptTokens:     4,
						CompletionTokens: 2,
						TotalTokens:      6,
					}},
				}},
			},
		},
	}
	eng.config.ChatModel = model
	summary := generateToolUseSummary(
		context.Background(),
		model,
		[]*schema.ToolCall{{
			ID:   "bounded-tool",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "Bash",
				Arguments: `{"command":"pwd"}`,
			},
		}},
		[]*schema.Message{{
			Role:       schema.Tool,
			ToolCallID: "bounded-tool",
			Content:    "/workspace",
		}},
		nil,
		reporter,
	)
	if summary != "Ran bounded check" {
		t.Fatalf("Goal tool summary = %q", summary)
	}
	explanation, err := eng.GeneratePermissionExplanation(
		context.Background(),
		"Bash",
		map[string]any{"command": "pwd"},
		"Executes a shell command.",
	)
	if err != nil {
		t.Fatal(err)
	}
	if explanation == nil ||
		explanation.Explanation != "Prints the working directory." {
		t.Fatalf("Goal permission explanation = %#v", explanation)
	}
	state := eng.goalService.snapshot()
	if state.TokensUsed != 15 ||
		state.UsageLedgerRevision != 2 ||
		state.PendingUsageAdmission != nil {
		t.Fatalf("Goal helper usage aggregate = %#v", state)
	}
	loaded, err := eng.transcript.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.GoalUsageRecords) != 2 ||
		loaded.GoalUsageRecords[0].BillableTokens != 9 ||
		loaded.GoalUsageRecords[1].BillableTokens != 6 {
		t.Fatalf("Goal helper usage records = %#v", loaded.GoalUsageRecords)
	}
}

func TestP242bRootAndExactChildProviderUsageSerializeAndAggregate(t *testing.T) {
	eng, rootReporter, finish := newP242bGoalRuntime(t, 1_000)
	defer finish()
	rootCall, err := rootReporter.AdmitProviderUsage(
		context.Background(),
		execution.ProviderUsageDescriptor{
			LogicalRoundID: rootReporter.NewLogicalRoundID(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	binding := eng.currentGoalExecutionIdentity()
	childReporter := eng.bindGoalUsageReporterForChild(
		binding,
		goalUsageSubject{
			SessionID:       "child-session",
			ThreadID:        "child-thread",
			AgentID:         "child-agent",
			AgentGeneration: 7,
		},
	)
	type admissionResult struct {
		call execution.ProviderUsageCall
		err  error
	}
	childResult := make(chan admissionResult, 1)
	go func() {
		call, childErr := childReporter.AdmitProviderUsage(
			context.Background(),
			execution.ProviderUsageDescriptor{
				LogicalRoundID: childReporter.NewLogicalRoundID(),
			},
		)
		childResult <- admissionResult{call: call, err: childErr}
	}()
	select {
	case result := <-childResult:
		t.Fatalf("child bypassed root Goal usage gate: %#v", result)
	case <-time.After(50 * time.Millisecond):
	}
	if err := rootCall.CompleteProviderUsage(&schema.TokenUsage{
		TotalTokens: 10,
	}); err != nil {
		t.Fatal(err)
	}
	var childCall execution.ProviderUsageCall
	select {
	case result := <-childResult:
		if result.err != nil {
			t.Fatal(result.err)
		}
		childCall = result.call
	case <-time.After(2 * time.Second):
		t.Fatal("child did not enter after root usage settled")
	}
	if err := childCall.CompleteProviderUsage(&schema.TokenUsage{
		PromptTokens:     20,
		CompletionTokens: 5,
		TotalTokens:      25,
	}); err != nil {
		t.Fatal(err)
	}
	aggregated := eng.goalService.snapshot()
	if aggregated.TokensUsed != 35 ||
		aggregated.UsageLedgerRevision != 2 {
		t.Fatalf("root and child Goal usage aggregate = %#v", aggregated)
	}
	loaded, err := eng.transcript.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.GoalUsageRecords) != 2 ||
		loaded.GoalUsageRecords[1].ExecutingAgentID != "child-agent" ||
		loaded.GoalUsageRecords[1].ExecutingAgentGeneration != 7 {
		t.Fatalf("root and child Goal usage records = %#v", loaded.GoalUsageRecords)
	}
	childReporter.revoke()
	if _, err := childReporter.AdmitProviderUsage(
		context.Background(),
		execution.ProviderUsageDescriptor{LogicalRoundID: "stale-round"},
	); !errors.Is(err, errGoalUsageCapabilityUnavailable) {
		t.Fatalf("revoked child capability error = %v", err)
	}
}

func TestP242bGoalUsageRecoveryRejectsUnknownAndStaleRecords(t *testing.T) {
	eng, reporter, finish := newP242bGoalRuntime(t, 100)
	defer finish()
	if _, err := reporter.AdmitProviderUsage(
		context.Background(),
		execution.ProviderUsageDescriptor{
			LogicalRoundID: reporter.NewLogicalRoundID(),
		},
	); err != nil {
		t.Fatal(err)
	}
	pendingState := eng.goalService.snapshot()
	valid := p242bGoalUsageRecordForAdmission(
		pendingState.PendingUsageAdmission,
		10,
	)
	t.Run("exact duplicate record counts once", func(t *testing.T) {
		restored, changed, err := reconcileGoalUsageState(
			pendingState,
			&transcript.LoadResult{
				GoalUsageRecords: []transcript.GoalUsageRecord{valid, valid},
			},
			time.Date(2026, 7, 29, 3, 0, 0, 0, time.UTC),
		)
		if err != nil ||
			!changed ||
			restored.TokensUsed != 10 ||
			restored.UsageLedgerRevision != 1 {
			t.Fatalf(
				"duplicate usage recovery = %#v changed=%v err=%v",
				restored,
				changed,
				err,
			)
		}
	})
	tests := []struct {
		name   string
		mutate func(*transcript.GoalUsageRecord)
	}{
		{
			name: "unknown record version",
			mutate: func(record *transcript.GoalUsageRecord) {
				record.Version++
			},
		},
		{
			name: "stale objective revision",
			mutate: func(record *transcript.GoalUsageRecord) {
				record.ObjectiveRevision++
			},
		},
		{
			name: "unrelated child generation",
			mutate: func(record *transcript.GoalUsageRecord) {
				record.ExecutingAgentID = "unrelated-agent"
				record.ExecutingAgentGeneration = 99
				record.ExecutingSessionID = "unrelated-session"
				record.ExecutingThreadID = "unrelated-thread"
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			record := valid
			tc.mutate(&record)
			restored, changed, err := reconcileGoalUsageState(
				pendingState,
				&transcript.LoadResult{
					GoalUsageRecords: []transcript.GoalUsageRecord{record},
				},
				time.Date(2026, 7, 29, 3, 0, 0, 0, time.UTC),
			)
			if err == nil ||
				!changed ||
				restored.Status != goalStatusUsageLimited ||
				restored.PendingUsageAdmission == nil ||
				restored.TokensUsed != 0 ||
				restored.UsageLedgerRevision != 0 {
				t.Fatalf(
					"invalid usage recovery = %#v changed=%v err=%v",
					restored,
					changed,
					err,
				)
			}
		})
	}

	t.Run("exhausted durable revision fails without scanning the numeric range", func(t *testing.T) {
		exhausted := cloneGoalState(pendingState)
		exhausted.UsageLedgerRevision = ^uint64(0)
		exhausted.PendingUsageAdmission = nil
		if _, err := inspectGoalUsageLedger(
			exhausted,
			&transcript.LoadResult{},
		); err == nil {
			t.Fatal("exhausted Goal usage cursor was accepted")
		}
	})
}

func TestP242bGoalStateV1MigratesAndV2CarriesAdmission(t *testing.T) {
	eng, _, finish := newP242bGoalRuntime(t, 100)
	defer finish()
	record := persistedGoalState(eng.goalService.snapshot())
	record.Version = session.PersistedGoalStateLegacyVersion
	restored, warnings, checkpoint := restorePersistedGoalState(
		record,
		"",
		time.Date(2026, 7, 29, 3, 0, 0, 0, time.UTC),
	)
	if !checkpoint ||
		restored == nil ||
		len(warnings) == 0 ||
		persistedGoalState(restored).Version !=
			session.PersistedGoalStateVersion {
		t.Fatalf(
			"Goal v1 migration = %#v warnings=%v checkpoint=%v",
			restored,
			warnings,
			checkpoint,
		)
	}
}

func newP242bGoalRuntime(
	t *testing.T,
	budget uint64,
) (*QueryEngine, *goalUsageReporter, func()) {
	t.Helper()
	now := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
	root := t.TempDir()
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "p24-2b-root",
		ThreadID:      "p24-2b-root",
		CWD:           root,
		TranscriptDir: root,
		Clock:         func() time.Time { return now },
	})
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective:   "account exact provider usage",
		TokenBudget: &budget,
	}); err != nil {
		t.Fatal(err)
	}
	turnID := "p24-2b-goal-turn"
	if _, err := eng.beginPlanTurn(turnID); err != nil {
		t.Fatal(err)
	}
	if _, identity, err := eng.goalService.beginTurn(turnID, true, nil, now); err != nil {
		t.Fatal(err)
	} else if identity == nil {
		t.Fatal("Goal turn identity is missing")
	}
	reporter := eng.currentGoalProviderUsageReporter()
	if reporter == nil {
		t.Fatal("Goal provider usage reporter is missing")
	}
	return eng, reporter, func() {
		eng.goalService.abandonTurn(turnID)
		eng.endPlanTurn(turnID)
		eng.Close()
	}
}

func newP49UnbudgetedGoalUsageRuntime(
	t *testing.T,
) (*QueryEngine, *goalUsageReporter, func()) {
	t.Helper()
	now := time.Date(2026, 8, 7, 7, 30, 0, 0, time.UTC)
	eng := newP241GoalEngine(t, QueryEngineConfig{
		Clock: func() time.Time { return now },
	})
	if _, err := eng.goalService.create(goalCreateRequest{
		Objective: "account usage without a Goal token limiter",
	}); err != nil {
		t.Fatal(err)
	}
	turnID := "p49-unbudgeted-goal-turn"
	if _, err := eng.beginPlanTurn(turnID); err != nil {
		t.Fatal(err)
	}
	if _, identity, err := eng.goalService.beginTurn(turnID, true, nil, now); err != nil {
		t.Fatal(err)
	} else if identity == nil {
		t.Fatal("Goal turn identity is missing")
	}
	reporter := eng.currentGoalProviderUsageReporter()
	if reporter == nil {
		t.Fatal("Goal provider usage reporter is missing")
	}
	return eng, reporter, func() {
		eng.goalService.abandonTurn(turnID)
		eng.endPlanTurn(turnID)
	}
}

func p242bGoalUsageRecordForAdmission(
	admission *goalUsageAdmission,
	billable uint64,
) transcript.GoalUsageRecord {
	return transcript.GoalUsageRecord{
		Version:                  transcript.GoalUsageRecordVersion,
		LedgerRevision:           admission.LedgerRevision,
		GoalID:                   admission.GoalID,
		ObjectiveRevision:        admission.ObjectiveRevision,
		RootSessionID:            admission.RootSessionID,
		RootThreadID:             admission.RootThreadID,
		RootAgentID:              admission.RootAgentID,
		ExecutingSessionID:       admission.ExecutingSessionID,
		ExecutingThreadID:        admission.ExecutingThreadID,
		ExecutingAgentID:         admission.ExecutingAgentID,
		ExecutingAgentGeneration: admission.ExecutingAgentGeneration,
		GoalTurnID:               admission.GoalTurnID,
		LogicalRoundID:           admission.LogicalRoundID,
		LogicalRequestID:         admission.LogicalRequestID,
		ModelAttemptID:           admission.ModelAttemptID,
		ModelAttemptIndex:        admission.ModelAttemptIndex,
		ModelProfile:             admission.ModelProfile,
		ModelRetryIndex:          admission.ModelRetryIndex,
		ProviderCallID:           admission.ProviderCallID,
		PromptTokens:             billable,
		TotalTokens:              billable,
		BillableTokens:           billable,
	}
}
