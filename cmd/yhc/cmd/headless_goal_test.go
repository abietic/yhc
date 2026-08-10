package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/tools"
)

type fakeHeadlessGoalRuntime struct {
	sessionID         string
	unavailableReason string
	snapshots         []engine.GoalSnapshot
	items             []engine.RuntimeItem
	turns             [][]engine.QueryEvent
	snapshotIndex     int
	claimIndex        int
	submitIndex       int
	claimErr          error
	stopCalls         int
	stopErr           error
	pendingInput      bool
}

func (f *fakeHeadlessGoalRuntime) SessionID() string {
	return f.sessionID
}

func (f *fakeHeadlessGoalRuntime) GoalCommandAvailability() (bool, string) {
	if f.unavailableReason != "" {
		return false, f.unavailableReason
	}
	return true, ""
}

func (f *fakeHeadlessGoalRuntime) GoalSnapshot() (
	*engine.GoalSnapshot,
	bool,
) {
	if len(f.snapshots) == 0 {
		return nil, false
	}
	index := f.snapshotIndex
	if index >= len(f.snapshots) {
		index = len(f.snapshots) - 1
	}
	snapshot := f.snapshots[index]
	if snapshot.TokenBudget != nil {
		budget := *snapshot.TokenBudget
		snapshot.TokenBudget = &budget
	}
	return &snapshot, true
}

func (f *fakeHeadlessGoalRuntime) PendingProjectGraphPermissionRequest() (
	engine.PermissionRequestEvent,
	bool,
) {
	return engine.PermissionRequestEvent{}, f.pendingInput
}

func (f *fakeHeadlessGoalRuntime) ClaimNextGoalContinuation() (
	engine.RuntimeItem,
	bool,
	error,
) {
	if f.claimErr != nil {
		return engine.RuntimeItem{}, false, f.claimErr
	}
	if f.claimIndex >= len(f.items) {
		return engine.RuntimeItem{}, false, nil
	}
	item := f.items[f.claimIndex]
	f.claimIndex++
	return item, true, nil
}

func (f *fakeHeadlessGoalRuntime) SubmitGoalContinuation(
	_ context.Context,
	_ engine.RuntimeItem,
) (<-chan engine.QueryEvent, engine.Terminal) {
	events := make(chan engine.QueryEvent, 8)
	var terminal engine.Terminal
	if f.submitIndex < len(f.turns) {
		for _, event := range f.turns[f.submitIndex] {
			events <- event
			if event.TerminalInfo != nil {
				terminal = *event.TerminalInfo
			}
		}
	}
	close(events)
	f.submitIndex++
	if f.snapshotIndex < len(f.snapshots)-1 {
		f.snapshotIndex++
	}
	return events, terminal
}

func (f *fakeHeadlessGoalRuntime) RequestStop(
	_ engine.RuntimeStopMode,
	_ string,
) error {
	f.stopCalls++
	if f.stopErr != nil {
		return f.stopErr
	}
	if len(f.snapshots) == 0 {
		return nil
	}
	index := f.snapshotIndex
	if index >= len(f.snapshots) {
		index = len(f.snapshots) - 1
	}
	f.snapshots[index].Status = "paused"
	f.snapshots[index].StatusReasonCode = "user-cancelled"
	f.snapshots[index].StatusReason = "automatic Goal continuation was cancelled by the user"
	return nil
}

func TestP245bHeadlessGoalRunsExactContinuationsUntilDurableComplete(
	t *testing.T,
) {
	runtime := &fakeHeadlessGoalRuntime{
		sessionID: "saved-session",
		snapshots: []engine.GoalSnapshot{
			headlessGoalSnapshot("active", nil, 100),
			headlessGoalSnapshot("active", nil, 200),
			headlessGoalSnapshot("complete", nil, 300),
		},
		items: []engine.RuntimeItem{
			{ID: "goal-item-1", Kind: engine.RuntimeItemGoalContinuation},
			{ID: "goal-item-2", Kind: engine.RuntimeItemGoalContinuation},
		},
		turns: [][]engine.QueryEvent{
			headlessGoalTurn("intermediate", engine.TerminalCompleted, nil),
			headlessGoalTurn("final answer", engine.TerminalCompleted, nil),
		},
	}

	result := driveHeadlessGoal(
		context.Background(),
		runtime,
		4,
		&bytes.Buffer{},
	)
	if result.Status != headlessGoalComplete ||
		result.ExitCode != ExitSuccess ||
		result.Continuations != 2 ||
		result.Output != "final answer" ||
		result.Goal == nil ||
		result.Goal.Status != "complete" ||
		result.Goal.TokenBudget != nil ||
		result.Goal.RemainingTokens != nil {
		t.Fatalf("Goal result = %#v", result)
	}
	if runtime.claimIndex != 2 || runtime.submitIndex != 2 {
		t.Fatalf(
			"claim/submit counts = %d/%d",
			runtime.claimIndex,
			runtime.submitIndex,
		)
	}
}

func TestP245bDedicatedHeadlessGoalResumesAndCompletesDurableCursor(
	t *testing.T,
) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	creator := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:         "p24-5b-durable-source",
		ThreadID:          "p24-5b-durable-thread",
		CWD:               root,
		TranscriptDir:     transcriptDir,
		CommandEntrypoint: commands.EntrypointPlain,
		GoalCapability: &engine.GoalCapabilityConfig{
			Enabled: true,
		},
		ChatModel: &headlessRecoveryModel{responses: []*schema.Message{{
			Role:    schema.Assistant,
			Content: "first durable Goal step",
			ResponseMeta: &schema.ResponseMeta{
				Usage: &schema.TokenUsage{TotalTokens: 8},
			},
		}}},
		ToolRegistry: registry,
		MaxTurns:     4,
	})
	drainP245bEvents(t, creator, "/goal finish the durable headless slice")
	drainP245bEvents(t, creator, "begin the active Goal")
	before, ok := creator.GoalSnapshot()
	if !ok ||
		before.Status != "active" ||
		before.TokenBudget != nil ||
		before.ContinuationOrdinal != 1 {
		t.Fatalf("durable source Goal = %#v", before)
	}
	sessionID := creator.SessionID()
	creator.Close()

	runnerRegistry := tools.NewRegistry()
	tools.RegisterDefaults(runnerRegistry)
	runner := engine.NewQueryEngine(engine.QueryEngineConfig{
		CWD:               root,
		TranscriptDir:     transcriptDir,
		CommandEntrypoint: commands.EntrypointHeadlessGoal,
		GoalCapability:    &engine.GoalCapabilityConfig{Enabled: true},
		ChatModel: &headlessRecoveryModel{responses: []*schema.Message{
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "p24-5b-complete",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      tools.UpdateGoalToolName,
						Arguments: `{"status":"complete"}`,
					},
				}},
				ResponseMeta: &schema.ResponseMeta{
					FinishReason: "tool_calls",
					Usage:        &schema.TokenUsage{TotalTokens: 4},
				},
			},
			{
				Role:    schema.Assistant,
				Content: "durable Goal complete",
				ResponseMeta: &schema.ResponseMeta{
					Usage: &schema.TokenUsage{TotalTokens: 3},
				},
			},
		}},
		ToolRegistry: runnerRegistry,
		MaxTurns:     4,
	})
	t.Cleanup(runner.Close)
	if _, err := runner.ResumeSession(context.Background(), sessionID); err != nil {
		t.Fatalf("resume durable Goal source: %v", err)
	}

	result := driveHeadlessGoal(
		context.Background(),
		runner,
		2,
		&bytes.Buffer{},
	)
	if result.Status != headlessGoalComplete ||
		result.ExitCode != ExitSuccess ||
		result.Continuations != 1 ||
		result.Output != "durable Goal complete" ||
		result.Goal == nil ||
		result.Goal.Status != "complete" ||
		result.Goal.TokenBudget != nil ||
		result.Goal.RemainingTokens != nil {
		t.Fatalf("dedicated durable Goal result = %#v", result)
	}
}

func TestP245bHeadlessGoalMapsDurableClosedStatuses(t *testing.T) {
	tests := []struct {
		status   string
		want     headlessGoalStatus
		exitCode int
	}{
		{status: "complete", want: headlessGoalComplete, exitCode: ExitSuccess},
		{status: "paused", want: headlessGoalPaused, exitCode: ExitFailure},
		{status: "blocked", want: headlessGoalBlocked, exitCode: ExitFailure},
		{
			status:   "budget_limited",
			want:     headlessGoalBudgetLimited,
			exitCode: ExitFailure,
		},
		{
			status:   "usage_limited",
			want:     headlessGoalUsageLimited,
			exitCode: ExitFailure,
		},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			runtime := &fakeHeadlessGoalRuntime{
				sessionID: "saved-session",
				snapshots: []engine.GoalSnapshot{
					headlessGoalSnapshot(test.status, nil, 0),
				},
			}
			result := driveHeadlessGoal(
				context.Background(),
				runtime,
				1,
				&bytes.Buffer{},
			)
			if result.Status != test.want ||
				result.ExitCode != test.exitCode ||
				runtime.claimIndex != 0 {
				t.Fatalf("%s result = %#v", test.status, result)
			}
		})
	}
}

func TestP245bHeadlessGoalMapsExecutionHalts(t *testing.T) {
	t.Run("continuation limit", func(t *testing.T) {
		runtime := &fakeHeadlessGoalRuntime{
			sessionID: "saved-session",
			snapshots: []engine.GoalSnapshot{
				headlessGoalSnapshot("active", nil, 0),
				headlessGoalSnapshot("active", nil, 0),
			},
			items: []engine.RuntimeItem{
				{ID: "goal-item-1", Kind: engine.RuntimeItemGoalContinuation},
			},
			turns: [][]engine.QueryEvent{
				headlessGoalTurn("", engine.TerminalCompleted, nil),
			},
		}
		result := driveHeadlessGoal(
			context.Background(),
			runtime,
			1,
			&bytes.Buffer{},
		)
		if result.Status != headlessGoalContinuationLimited ||
			result.ExitCode != ExitFailure ||
			result.Continuations != 1 {
			t.Fatalf("continuation-limited result = %#v", result)
		}
	})

	t.Run("waiting input", func(t *testing.T) {
		runtime := &fakeHeadlessGoalRuntime{
			sessionID: "saved-session",
			snapshots: []engine.GoalSnapshot{
				headlessGoalSnapshot("active", nil, 0),
				headlessGoalSnapshot("active", nil, 0),
			},
			items: []engine.RuntimeItem{
				{ID: "goal-item-1", Kind: engine.RuntimeItemGoalContinuation},
			},
			turns: [][]engine.QueryEvent{
				headlessGoalTurn(
					"",
					engine.TerminalWaitingInput,
					nil,
				),
			},
		}
		result := driveHeadlessGoal(
			context.Background(),
			runtime,
			2,
			&bytes.Buffer{},
		)
		if result.Status != headlessGoalWaitingInput ||
			result.ErrorCode != "waiting_input" ||
			result.ExitCode != ExitFailure {
			t.Fatalf("waiting-input result = %#v", result)
		}
	})

	t.Run("no cursor", func(t *testing.T) {
		runtime := &fakeHeadlessGoalRuntime{
			sessionID: "saved-session",
			snapshots: []engine.GoalSnapshot{
				headlessGoalSnapshot("active", nil, 0),
			},
		}
		result := driveHeadlessGoal(
			context.Background(),
			runtime,
			1,
			&bytes.Buffer{},
		)
		if result.Status != headlessGoalNotRunnable ||
			result.ErrorCode != "goal_continuation_unavailable" ||
			result.ExitCode != ExitFailure {
			t.Fatalf("not-runnable result = %#v", result)
		}
	})

	t.Run("restored pending interaction", func(t *testing.T) {
		runtime := &fakeHeadlessGoalRuntime{
			sessionID:    "saved-session",
			pendingInput: true,
			snapshots: []engine.GoalSnapshot{
				headlessGoalSnapshot("active", nil, 0),
			},
		}
		result := driveHeadlessGoal(
			context.Background(),
			runtime,
			1,
			&bytes.Buffer{},
		)
		if result.Status != headlessGoalWaitingInput ||
			result.ErrorCode != "waiting_input" ||
			result.ExitCode != ExitFailure {
			t.Fatalf("restored waiting-input result = %#v", result)
		}
	})

	t.Run("missing Goal", func(t *testing.T) {
		runtime := &fakeHeadlessGoalRuntime{sessionID: "saved-session"}
		result := driveHeadlessGoal(
			context.Background(),
			runtime,
			1,
			&bytes.Buffer{},
		)
		if result.Status != headlessGoalNotRunnable ||
			result.ErrorCode != "goal_not_found" ||
			result.ExitCode != ExitFailure ||
			runtime.claimIndex != 0 {
			t.Fatalf("missing Goal result = %#v", result)
		}
	})

	t.Run("unavailable Goal", func(t *testing.T) {
		snapshot := headlessGoalSnapshot("active", nil, 0)
		snapshot.Available = false
		runtime := &fakeHeadlessGoalRuntime{
			sessionID: "saved-session",
			snapshots: []engine.GoalSnapshot{snapshot},
		}
		result := driveHeadlessGoal(
			context.Background(),
			runtime,
			1,
			&bytes.Buffer{},
		)
		if result.Status != headlessGoalNotRunnable ||
			result.ErrorCode != "goal_not_found" ||
			result.ExitCode != ExitFailure ||
			runtime.claimIndex != 0 {
			t.Fatalf("unavailable Goal result = %#v", result)
		}
	})

	t.Run("capability unavailable", func(t *testing.T) {
		runtime := &fakeHeadlessGoalRuntime{
			sessionID:         "saved-session",
			unavailableReason: "Goal capability is disabled",
			snapshots: []engine.GoalSnapshot{
				headlessGoalSnapshot("active", nil, 0),
			},
		}
		result := driveHeadlessGoal(
			context.Background(),
			runtime,
			1,
			&bytes.Buffer{},
		)
		if result.Status != headlessGoalNotRunnable ||
			result.ErrorCode != "goal_unavailable" ||
			result.Goal == nil ||
			runtime.claimIndex != 0 {
			t.Fatalf("capability-unavailable result = %#v", result)
		}
	})
}

func TestP245bHeadlessGoalMapsRuntimeAndPersistenceFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		reason   engine.TerminalReason
		err      error
		wantCode string
	}{
		{
			name:     "max turns",
			reason:   engine.TerminalMaxTurns,
			wantCode: "max_turns",
		},
		{
			name:     "provider",
			reason:   engine.TerminalModelError,
			err:      errors.New("provider failed"),
			wantCode: "runtime_error",
		},
		{
			name:     "persistence",
			reason:   engine.TerminalPersistenceError,
			err:      errors.New("checkpoint failed"),
			wantCode: "runtime_error",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &fakeHeadlessGoalRuntime{
				sessionID: "saved-session",
				snapshots: []engine.GoalSnapshot{
					headlessGoalSnapshot("active", nil, 0),
					headlessGoalSnapshot("active", nil, 0),
				},
				items: []engine.RuntimeItem{{
					ID:   "goal-item-1",
					Kind: engine.RuntimeItemGoalContinuation,
				}},
				turns: [][]engine.QueryEvent{
					headlessGoalTurn("", test.reason, test.err),
				},
			}
			result := driveHeadlessGoal(
				context.Background(),
				runtime,
				2,
				&bytes.Buffer{},
			)
			if result.Status != headlessGoalFailed ||
				result.ErrorCode != test.wantCode ||
				result.ExitCode != ExitFailure ||
				result.Continuations != 1 {
				t.Fatalf("%s result = %#v", test.name, result)
			}
		})
	}

	t.Run("claim persistence", func(t *testing.T) {
		runtime := &fakeHeadlessGoalRuntime{
			sessionID: "saved-session",
			snapshots: []engine.GoalSnapshot{
				headlessGoalSnapshot("active", nil, 0),
			},
			claimErr: errors.New("coordinator checkpoint failed"),
		}
		result := driveHeadlessGoal(
			context.Background(),
			runtime,
			1,
			&bytes.Buffer{},
		)
		if result.Status != headlessGoalFailed ||
			result.ErrorCode != "goal_claim_failed" ||
			result.ExitCode != ExitFailure ||
			result.Continuations != 0 {
			t.Fatalf("claim failure result = %#v", result)
		}
	})
}

func TestP245bHeadlessGoalCancellationUsesDurableStopOwner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runtime := &fakeHeadlessGoalRuntime{
		sessionID: "saved-session",
		snapshots: []engine.GoalSnapshot{
			headlessGoalSnapshot("active", nil, 0),
		},
	}
	result := driveHeadlessGoal(ctx, runtime, 2, &bytes.Buffer{})
	if result.Status != headlessGoalCancelled ||
		result.ExitCode != ExitCancelled ||
		runtime.stopCalls != 1 ||
		result.Goal == nil ||
		result.Goal.Status != "paused" ||
		result.Goal.StatusReasonCode != "user-cancelled" {
		t.Fatalf("cancelled result = %#v, stops = %d", result, runtime.stopCalls)
	}

	runtime = &fakeHeadlessGoalRuntime{
		sessionID: "saved-session",
		snapshots: []engine.GoalSnapshot{
			headlessGoalSnapshot("active", nil, 0),
		},
		stopErr: errors.New("checkpoint failed"),
	}
	result = driveHeadlessGoal(ctx, runtime, 2, &bytes.Buffer{})
	if result.Status != headlessGoalFailed ||
		result.ExitCode != ExitFailure ||
		result.ErrorCode != "goal_cancel_persistence_failed" {
		t.Fatalf("failed cancellation result = %#v", result)
	}
}

func TestP245bHeadlessGoalJSONEnvelopeIsVersionedAndRedacted(t *testing.T) {
	const secret = "sk-goal-secret"
	budget := uint64(1_000)
	result := headlessGoalResult{
		Status:    headlessGoalFailed,
		SessionID: "saved-session",
		Goal: projectHeadlessGoal(pointerToGoalSnapshot(
			headlessGoalSnapshot("active", &budget, 25),
		)),
		Continuations:    2,
		MaxContinuations: 3,
		TerminalReason:   string(engine.TerminalModelError),
		ErrorCode:        "runtime_error",
		Err: sanitizeHeadlessGoalError(
			errors.New("api_key="+secret+" request failed"),
			secret,
		),
		ExitCode: ExitFailure,
	}
	var stdout, stderr bytes.Buffer
	if err := renderHeadlessGoalResult(
		outputFormatJSON,
		&stdout,
		&stderr,
		result,
	); err != nil {
		t.Fatal(err)
	}
	var envelope headlessGoalEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode Goal envelope: %v; %q", err, stdout.String())
	}
	if envelope.SchemaVersion != headlessGoalEnvelopeSchemaVersion ||
		envelope.Kind != headlessGoalEnvelopeKind ||
		envelope.Status != headlessGoalFailed ||
		envelope.SessionID != "saved-session" ||
		envelope.Goal == nil ||
		envelope.Goal.GoalID != "goal-1" ||
		envelope.Continuations != 2 ||
		envelope.MaxContinuations != 3 ||
		envelope.ExitCode != ExitFailure ||
		envelope.Error == nil ||
		envelope.Error.Code != "runtime_error" {
		t.Fatalf("Goal envelope = %#v", envelope)
	}
	if strings.Contains(stdout.String()+stderr.String(), secret) {
		t.Fatalf("Goal envelope leaked secret: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("JSON rendering wrote stderr: %q", stderr.String())
	}

	long := sanitizeHeadlessGoalError(
		errors.New(strings.Repeat("界", maxHeadlessGoalErrorRunes+100)),
	)
	if long == nil ||
		len([]rune(long.Error())) > maxHeadlessGoalErrorRunes ||
		!strings.HasSuffix(long.Error(), "... (truncated)") {
		t.Fatalf("bounded Goal error = %q", long)
	}
}

func TestP245bGoalRunRequiresExplicitSavedSessionAndBound(t *testing.T) {
	for _, args := range [][]string{
		{"goal"},
		{"goal", "run", "--output-format", "json"},
		{
			"goal", "run",
			"--resume", "saved-session",
			"--output-format", "json",
		},
		{
			"goal", "run",
			"--max-continuations", "1",
			"--output-format", "json",
		},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := newRootCommand()
			var stdout, stderr bytes.Buffer
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			root.SetArgs(args)
			err := root.Execute()
			if ExitCode(err) != ExitUsage {
				t.Fatalf("args %v error = %v, exit = %d", args, err, ExitCode(err))
			}
			if len(args) > 1 {
				var envelope headlessGoalEnvelope
				if decodeErr := json.Unmarshal(stdout.Bytes(), &envelope); decodeErr != nil {
					t.Fatalf(
						"args %v output is not JSON: %v; %q",
						args,
						decodeErr,
						stdout.String(),
					)
				}
				if envelope.Kind != headlessGoalEnvelopeKind ||
					envelope.Status != headlessGoalFailed ||
					envelope.ExitCode != ExitUsage ||
					envelope.Error == nil ||
					envelope.Error.Code != "usage_error" {
					t.Fatalf("args %v envelope = %#v", args, envelope)
				}
			}
		})
	}

	root := newRootCommand()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetArgs([]string{"goal", "run", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("goal run help: %v", err)
	}
	help := stdout.String()
	for _, required := range []string{"--resume", "--max-continuations", "--output-format"} {
		if !strings.Contains(help, required) {
			t.Fatalf("goal run help missing %q: %q", required, help)
		}
	}
	for _, forbidden := range []string{"[prompt]", "stdin objective"} {
		if strings.Contains(strings.ToLower(help), forbidden) {
			t.Fatalf("goal run help exposed %q: %q", forbidden, help)
		}
	}

	root = newRootCommand()
	root.SetArgs([]string{"goal", "run", "unexpected-prompt"})
	if err := root.Execute(); ExitCode(err) != ExitUsage {
		t.Fatalf("goal run prompt error = %v, exit = %d", err, ExitCode(err))
	}
}

func TestP245bHeadlessGoalTextDoesNotPromoteTurnCompletion(t *testing.T) {
	result := headlessGoalResult{
		Status:    headlessGoalContinuationLimited,
		Output:    "one turn completed",
		SessionID: "saved-session",
		Goal: projectHeadlessGoal(pointerToGoalSnapshot(
			headlessGoalSnapshot("active", nil, 0),
		)),
		Continuations:    1,
		MaxContinuations: 1,
		ErrorCode:        "continuation_limit",
		Err:              errors.New("goal remains active"),
		ExitCode:         ExitFailure,
	}
	var stdout, stderr bytes.Buffer
	if err := renderHeadlessGoalResult(
		outputFormatText,
		&stdout,
		&stderr,
		result,
	); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Goal run: continuation_limited") ||
		!strings.Contains(stdout.String(), "status=active") ||
		strings.Contains(stdout.String(), "Goal run: complete") {
		t.Fatalf("misleading Goal text output: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "goal remains active") {
		t.Fatalf("missing Goal text failure: %q", stderr.String())
	}
}

func headlessGoalSnapshot(
	status string,
	budget *uint64,
	tokensUsed uint64,
) engine.GoalSnapshot {
	return engine.GoalSnapshot{
		GoalID:               "goal-1",
		Objective:            "finish the accepted slice",
		ObjectiveRevision:    1,
		Status:               status,
		Revision:             4,
		TokenBudget:          budget,
		TokensUsed:           tokensUsed,
		UsageLedgerRevision:  2,
		UsageCoverage:        "complete",
		ContinuationOrdinal:  3,
		LastGoalTurnID:       "goal-turn-3",
		LastTerminalSequence: 11,
		Available:            true,
	}
}

func pointerToGoalSnapshot(snapshot engine.GoalSnapshot) *engine.GoalSnapshot {
	return &snapshot
}

func headlessGoalTurn(
	output string,
	reason engine.TerminalReason,
	err error,
) []engine.QueryEvent {
	events := make([]engine.QueryEvent, 0, 2)
	if output != "" {
		events = append(events, engine.QueryEvent{
			Type: engine.EventAssistant,
			Message: &schema.Message{
				Role:    schema.Assistant,
				Content: output,
			},
		})
	}
	events = append(events, engine.QueryEvent{
		Type: engine.EventTerminal,
		TerminalInfo: &engine.Terminal{
			Reason: reason,
			Err:    err,
		},
	})
	return events
}

func drainP245bEvents(
	t *testing.T,
	eng *engine.QueryEngine,
	input string,
) {
	t.Helper()
	events, _ := eng.SubmitMessage(context.Background(), input)
	var terminal *engine.Terminal
	for event := range events {
		if event.TerminalInfo != nil {
			value := *event.TerminalInfo
			terminal = &value
		}
	}
	if terminal == nil || terminal.Reason != engine.TerminalCompleted {
		t.Fatalf("input %q terminal = %#v", input, terminal)
	}
}
