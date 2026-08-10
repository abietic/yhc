package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
)

const (
	headlessGoalEnvelopeSchemaVersion = 1
	headlessGoalEnvelopeKind          = "goal_run"
	maxHeadlessGoalErrorRunes         = 2_048
)

type headlessGoalStatus string

const (
	headlessGoalComplete            headlessGoalStatus = "complete"
	headlessGoalPaused              headlessGoalStatus = "paused"
	headlessGoalBlocked             headlessGoalStatus = "blocked"
	headlessGoalBudgetLimited       headlessGoalStatus = "budget_limited"
	headlessGoalUsageLimited        headlessGoalStatus = "usage_limited"
	headlessGoalWaitingInput        headlessGoalStatus = "waiting_input"
	headlessGoalNotRunnable         headlessGoalStatus = "not_runnable"
	headlessGoalContinuationLimited headlessGoalStatus = "continuation_limited"
	headlessGoalCancelled           headlessGoalStatus = "cancelled"
	headlessGoalFailed              headlessGoalStatus = "failed"
)

type headlessGoalOptions struct {
	Runtime          runtimeFlags
	Resume           string
	MaxContinuations uint64
	OutputFormat     string
	Stdout           io.Writer
	Stderr           io.Writer
}

type headlessGoalRuntime interface {
	SessionID() string
	GoalCommandAvailability() (bool, string)
	GoalSnapshot() (*engine.GoalSnapshot, bool)
	PendingProjectGraphPermissionRequest() (
		engine.PermissionRequestEvent,
		bool,
	)
	ClaimNextGoalContinuation() (engine.RuntimeItem, bool, error)
	SubmitGoalContinuation(
		context.Context,
		engine.RuntimeItem,
	) (<-chan engine.QueryEvent, engine.Terminal)
	RequestStop(engine.RuntimeStopMode, string) error
}

type headlessGoalResult struct {
	Status           headlessGoalStatus
	Output           string
	SessionID        string
	Goal             *headlessGoalProjection
	Continuations    uint64
	MaxContinuations uint64
	TerminalReason   string
	ErrorCode        string
	Err              error
	ExitCode         int
}

type headlessGoalProjection struct {
	GoalID                           string                                `json:"goal_id"`
	Objective                        string                                `json:"objective"`
	ObjectiveRevision                uint64                                `json:"objective_revision"`
	Status                           string                                `json:"status"`
	StatusReasonCode                 string                                `json:"status_reason_code,omitempty"`
	StatusReason                     string                                `json:"status_reason,omitempty"`
	Revision                         uint64                                `json:"revision"`
	TokenBudget                      *uint64                               `json:"token_budget"`
	TokensUsed                       uint64                                `json:"tokens_used"`
	RemainingTokens                  *uint64                               `json:"remaining_tokens"`
	UsageLedgerRevision              uint64                                `json:"usage_ledger_revision"`
	UsageCoverage                    string                                `json:"usage_coverage"`
	PendingUsageAdmission            *headlessGoalUsageAdmissionProjection `json:"pending_usage_admission,omitempty"`
	RootActiveTimeMillis             int64                                 `json:"root_active_time_millis"`
	ContinuationOrdinal              uint64                                `json:"continuation_ordinal"`
	LastGoalTurnID                   string                                `json:"last_goal_turn_id,omitempty"`
	LastTerminalSequence             uint64                                `json:"last_terminal_sequence"`
	PendingCompleteTurnID            string                                `json:"pending_complete_turn_id,omitempty"`
	PendingCompleteObjectiveRevision uint64                                `json:"pending_complete_objective_revision,omitempty"`
	BlockerKey                       string                                `json:"blocker_key,omitempty"`
	BlockerTurnIDs                   []string                              `json:"blocker_turn_ids,omitempty"`
	CreatedAt                        time.Time                             `json:"created_at"`
	UpdatedAt                        time.Time                             `json:"updated_at"`
	Available                        bool                                  `json:"available"`
}

type headlessGoalUsageAdmissionProjection struct {
	Version                  uint16    `json:"version"`
	LedgerRevision           uint64    `json:"ledger_revision"`
	GoalID                   string    `json:"goal_id"`
	ObjectiveRevision        uint64    `json:"objective_revision"`
	RootSessionID            string    `json:"root_session_id"`
	RootThreadID             string    `json:"root_thread_id"`
	RootAgentID              string    `json:"root_agent_id,omitempty"`
	ExecutingSessionID       string    `json:"executing_session_id"`
	ExecutingThreadID        string    `json:"executing_thread_id"`
	ExecutingAgentID         string    `json:"executing_agent_id,omitempty"`
	ExecutingAgentGeneration int64     `json:"executing_agent_generation"`
	GoalTurnID               string    `json:"goal_turn_id"`
	LogicalRoundID           string    `json:"logical_round_id"`
	ProviderCallID           string    `json:"provider_call_id"`
	AdmittedAt               time.Time `json:"admitted_at"`
}

type headlessGoalEnvelope struct {
	SchemaVersion    int                     `json:"schema_version"`
	Kind             string                  `json:"kind"`
	Status           headlessGoalStatus      `json:"status"`
	Reason           string                  `json:"reason"`
	Output           string                  `json:"output,omitempty"`
	SessionID        string                  `json:"session_id,omitempty"`
	Goal             *headlessGoalProjection `json:"goal,omitempty"`
	Continuations    uint64                  `json:"continuations"`
	MaxContinuations uint64                  `json:"max_continuations"`
	TerminalReason   string                  `json:"terminal_reason,omitempty"`
	ExitCode         int                     `json:"exit_code"`
	Error            *headlessEnvelopeError  `json:"error,omitempty"`
}

func newGoalCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "goal",
		Short: "Operate a saved Goal through explicit non-interactive workflows",
		Args:  exactArgs(0),
		RunE: func(*cobra.Command, []string) error {
			return usageErrorf("goal requires a subcommand")
		},
	}
	command.AddCommand(newGoalRunCommand())
	return command
}

func newGoalRunCommand() *cobra.Command {
	options := &headlessGoalOptions{OutputFormat: string(outputFormatText)}
	command := &cobra.Command{
		Use:   "run",
		Short: "Run pending continuations for an existing saved Goal",
		Long: "Resume one saved session and execute only its exact durable Goal " +
			"continuations. This command never reads an objective from stdin and " +
			"never creates or edits Goal state.",
		Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			options.Runtime.captureExplicit(cmd)
			options.Stdout = cmd.OutOrStdout()
			options.Stderr = cmd.ErrOrStderr()
			return runHeadlessGoal(cmd.Context(), *options)
		},
	}
	bindRuntimeFlags(command.Flags(), &options.Runtime)
	command.Flags().StringVar(
		&options.Resume,
		"resume",
		"",
		"Required saved session ID containing the Goal",
	)
	command.Flags().Uint64Var(
		&options.MaxContinuations,
		"max-continuations",
		0,
		"Required positive bound on submitted Goal continuations",
	)
	command.Flags().StringVar(
		&options.OutputFormat,
		"output-format",
		string(outputFormatText),
		"Output format (text or json)",
	)
	return command
}

func runHeadlessGoal(ctx context.Context, options headlessGoalOptions) error {
	format, err := parseOutputFormat(options.OutputFormat)
	if err != nil {
		return renderHeadlessGoalFailure(
			formatForError(options.OutputFormat),
			options,
			err,
			"usage_error",
			ExitUsage,
		)
	}
	options = normalizeHeadlessGoalWriters(options)
	options.Resume = strings.TrimSpace(options.Resume)
	if options.Resume == "" {
		return renderHeadlessGoalFailure(
			format,
			options,
			usageErrorf("--resume requires a saved session ID"),
			"usage_error",
			ExitUsage,
		)
	}
	if options.MaxContinuations == 0 {
		return renderHeadlessGoalFailure(
			format,
			options,
			usageErrorf("--max-continuations must be positive"),
			"usage_error",
			ExitUsage,
		)
	}
	resumeSource, err := prepareConfiguredSessionResume(ctx, options.Resume, false)
	if err != nil {
		return renderHeadlessGoalFailure(
			format,
			options,
			err,
			errorCodeForFailure(err, "session_error"),
			ExitCode(err),
		)
	}

	engineCfg, _, _, err := buildEngineConfig(ctx, options.Runtime, options.Stderr)
	if err != nil {
		return renderHeadlessGoalFailure(
			format,
			options,
			err,
			errorCodeForFailure(err, "runtime_error"),
			ExitCode(err),
		)
	}
	configureHeadlessPermissions(&engineCfg, options.Stderr)
	engineCfg.CommandEntrypoint = commands.EntrypointHeadlessGoal

	eng := engine.NewQueryEngine(engineCfg)
	emitExecutionContainmentStartupDiagnostic(options.Stderr, eng)
	defer eng.Close()
	if err := resumeConfiguredSession(ctx, eng, resumeSource, options.Stderr); err != nil {
		return renderHeadlessGoalFailure(
			format,
			options,
			err,
			errorCodeForFailure(err, "session_error"),
			ExitCode(err),
		)
	}

	result := driveHeadlessGoal(
		ctx,
		eng,
		options.MaxContinuations,
		options.Stderr,
	)
	result.SessionID = eng.SessionID()
	result.Err = sanitizeHeadlessGoalError(result.Err, options.Runtime.apiKey)
	if err := renderHeadlessGoalResult(
		format,
		options.Stdout,
		options.Stderr,
		result,
	); err != nil {
		return err
	}
	if result.ExitCode != ExitSuccess {
		return renderedExitError(result.ExitCode, result.Err)
	}
	return nil
}

func driveHeadlessGoal(
	ctx context.Context,
	runtime headlessGoalRuntime,
	maxContinuations uint64,
	stderr io.Writer,
) headlessGoalResult {
	result := headlessGoalResult{
		Status:           headlessGoalNotRunnable,
		MaxContinuations: maxContinuations,
		ExitCode:         ExitFailure,
	}
	if runtime == nil {
		return failHeadlessGoalResult(
			result,
			"goal_runtime_unavailable",
			errors.New("goal runtime is unavailable"),
		)
	}
	result.SessionID = runtime.SessionID()
	if available, reason := runtime.GoalCommandAvailability(); !available {
		if snapshot, ok := runtime.GoalSnapshot(); ok {
			result.Goal = projectHeadlessGoal(snapshot)
		}
		if strings.TrimSpace(reason) == "" {
			reason = "goal execution is unavailable"
		}
		result.ErrorCode = "goal_unavailable"
		result.Err = errors.New(reason)
		return result
	}

	for {
		if ctx != nil && ctx.Err() != nil {
			return cancelHeadlessGoal(runtime, result, ctx.Err())
		}
		snapshot, available := runtime.GoalSnapshot()
		if !available || snapshot == nil || !snapshot.Available {
			result.Status = headlessGoalNotRunnable
			result.ErrorCode = "goal_not_found"
			result.Err = errors.New("saved session has no available Goal")
			result.Goal = projectHeadlessGoal(snapshot)
			return result
		}
		result.Goal = projectHeadlessGoal(snapshot)
		if halted := classifyDurableGoalStatus(&result, snapshot.Status); halted {
			return result
		}
		if result.Continuations >= maxContinuations {
			result.Status = headlessGoalContinuationLimited
			result.ErrorCode = "continuation_limit"
			result.Err = fmt.Errorf(
				"goal remains active after %d continuation(s)",
				result.Continuations,
			)
			result.ExitCode = ExitFailure
			return result
		}

		item, ok, err := runtime.ClaimNextGoalContinuation()
		if err != nil {
			return failHeadlessGoalResult(result, "goal_claim_failed", err)
		}
		if !ok {
			if _, waiting := runtime.PendingProjectGraphPermissionRequest(); waiting {
				result.Status = headlessGoalWaitingInput
				result.ErrorCode = "waiting_input"
				result.Err = errors.New(
					"goal execution requires interactive input",
				)
				result.ExitCode = ExitFailure
				return result
			}
			result.Status = headlessGoalNotRunnable
			result.ErrorCode = "goal_continuation_unavailable"
			result.Err = errors.New(
				"active Goal has no eligible durable continuation",
			)
			result.ExitCode = ExitFailure
			return result
		}

		events, _ := runtime.SubmitGoalContinuation(ctx, item)
		result.Continuations++
		turn := collectHeadlessEvents(ctx, stderr, events)
		result.Output = turn.Output
		result.TerminalReason = turn.TerminalReason

		if turn.Status == string(headlessGoalCancelled) ||
			turn.ExitCode == ExitCancelled {
			return cancelHeadlessGoal(runtime, result, turn.Err)
		}

		snapshot, available = runtime.GoalSnapshot()
		if !available || snapshot == nil || !snapshot.Available {
			result.Goal = projectHeadlessGoal(snapshot)
			return failHeadlessGoalResult(
				result,
				"goal_state_unavailable",
				errors.New("goal state became unavailable after continuation"),
			)
		}
		result.Goal = projectHeadlessGoal(snapshot)
		if halted := classifyDurableGoalStatus(&result, snapshot.Status); halted {
			return result
		}
		if engine.TerminalReason(turn.TerminalReason) ==
			engine.TerminalWaitingInput {
			result.Status = headlessGoalWaitingInput
			result.ErrorCode = "waiting_input"
			result.Err = errors.New("goal execution requires interactive input")
			result.ExitCode = ExitFailure
			return result
		}
		if turn.ExitCode != ExitSuccess || turn.Err != nil {
			code := turn.ErrorCode
			if code == "" {
				code = "goal_turn_failed"
			}
			return failHeadlessGoalResult(result, code, turn.Err)
		}
	}
}

func classifyDurableGoalStatus(
	result *headlessGoalResult,
	status string,
) bool {
	if result == nil {
		return true
	}
	switch strings.TrimSpace(status) {
	case "active":
		return false
	case "complete":
		result.Status = headlessGoalComplete
		result.ErrorCode = ""
		result.Err = nil
		result.ExitCode = ExitSuccess
	case "paused":
		result.Status = headlessGoalPaused
		result.ExitCode = ExitFailure
	case "blocked":
		result.Status = headlessGoalBlocked
		result.ExitCode = ExitFailure
	case "budget_limited":
		result.Status = headlessGoalBudgetLimited
		result.ExitCode = ExitFailure
	case "usage_limited":
		result.Status = headlessGoalUsageLimited
		result.ExitCode = ExitFailure
	default:
		result.Status = headlessGoalFailed
		result.ErrorCode = "goal_status_invalid"
		result.Err = fmt.Errorf("unsupported durable Goal status %q", status)
		result.ExitCode = ExitFailure
	}
	return true
}

func cancelHeadlessGoal(
	runtime headlessGoalRuntime,
	result headlessGoalResult,
	cause error,
) headlessGoalResult {
	if err := runtime.RequestStop(
		engine.RuntimeStopImmediate,
		"headless-goal-cancelled",
	); err != nil {
		return failHeadlessGoalResult(
			result,
			"goal_cancel_persistence_failed",
			fmt.Errorf("persist Goal cancellation: %w", err),
		)
	}
	if snapshot, available := runtime.GoalSnapshot(); available && snapshot != nil {
		result.Goal = projectHeadlessGoal(snapshot)
	}
	if cause == nil {
		cause = errors.New("goal execution cancelled")
	}
	result.Status = headlessGoalCancelled
	result.ErrorCode = "cancelled"
	result.Err = cause
	result.ExitCode = ExitCancelled
	return result
}

func failHeadlessGoalResult(
	result headlessGoalResult,
	code string,
	err error,
) headlessGoalResult {
	if err == nil {
		err = errors.New("goal execution failed")
	}
	result.Status = headlessGoalFailed
	result.ErrorCode = code
	result.Err = err
	result.ExitCode = ExitFailure
	return result
}

func projectHeadlessGoal(
	snapshot *engine.GoalSnapshot,
) *headlessGoalProjection {
	if snapshot == nil {
		return nil
	}
	var remaining *uint64
	if snapshot.TokenBudget != nil {
		value := uint64(0)
		if *snapshot.TokenBudget > snapshot.TokensUsed {
			value = *snapshot.TokenBudget - snapshot.TokensUsed
		}
		remaining = &value
	}
	return &headlessGoalProjection{
		GoalID:              snapshot.GoalID,
		Objective:           snapshot.Objective,
		ObjectiveRevision:   snapshot.ObjectiveRevision,
		Status:              snapshot.Status,
		StatusReasonCode:    snapshot.StatusReasonCode,
		StatusReason:        snapshot.StatusReason,
		Revision:            snapshot.Revision,
		TokenBudget:         cloneUint64Value(snapshot.TokenBudget),
		TokensUsed:          snapshot.TokensUsed,
		RemainingTokens:     remaining,
		UsageLedgerRevision: snapshot.UsageLedgerRevision,
		UsageCoverage:       snapshot.UsageCoverage,
		PendingUsageAdmission: projectHeadlessGoalUsageAdmission(
			snapshot.PendingUsageAdmission,
		),
		RootActiveTimeMillis:             snapshot.RootActiveTimeMillis,
		ContinuationOrdinal:              snapshot.ContinuationOrdinal,
		LastGoalTurnID:                   snapshot.LastGoalTurnID,
		LastTerminalSequence:             snapshot.LastTerminalSequence,
		PendingCompleteTurnID:            snapshot.PendingCompleteTurnID,
		PendingCompleteObjectiveRevision: snapshot.PendingCompleteObjectiveRevision,
		BlockerKey:                       snapshot.BlockerKey,
		BlockerTurnIDs:                   append([]string(nil), snapshot.BlockerTurnIDs...),
		CreatedAt:                        snapshot.CreatedAt,
		UpdatedAt:                        snapshot.UpdatedAt,
		Available:                        snapshot.Available,
	}
}

func projectHeadlessGoalUsageAdmission(
	admission *engine.GoalUsageAdmissionSnapshot,
) *headlessGoalUsageAdmissionProjection {
	if admission == nil {
		return nil
	}
	return &headlessGoalUsageAdmissionProjection{
		Version:                  admission.Version,
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
		ProviderCallID:           admission.ProviderCallID,
		AdmittedAt:               admission.AdmittedAt,
	}
}

func cloneUint64Value(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func sanitizeHeadlessGoalError(
	err error,
	exactSecrets ...string,
) error {
	safe := sanitizeHeadlessError(err, exactSecrets...)
	if safe == nil {
		return nil
	}
	message := safe.Error()
	if utf8.RuneCountInString(message) <= maxHeadlessGoalErrorRunes {
		return safe
	}
	const suffix = "... (truncated)"
	runes := []rune(message)
	keep := maxHeadlessGoalErrorRunes - utf8.RuneCountInString(suffix)
	return errors.New(string(runes[:keep]) + suffix)
}

func normalizeHeadlessGoalWriters(
	options headlessGoalOptions,
) headlessGoalOptions {
	if options.Stdout == nil {
		options.Stdout = os.Stdout
	}
	if options.Stderr == nil {
		options.Stderr = os.Stderr
	}
	return options
}

func renderHeadlessGoalResult(
	format outputFormat,
	stdout io.Writer,
	stderr io.Writer,
	result headlessGoalResult,
) error {
	if format == outputFormatJSON {
		envelope := headlessGoalEnvelope{
			SchemaVersion:    headlessGoalEnvelopeSchemaVersion,
			Kind:             headlessGoalEnvelopeKind,
			Status:           result.Status,
			Reason:           headlessGoalResultReason(result),
			Output:           result.Output,
			SessionID:        result.SessionID,
			Goal:             result.Goal,
			Continuations:    result.Continuations,
			MaxContinuations: result.MaxContinuations,
			TerminalReason:   result.TerminalReason,
			ExitCode:         result.ExitCode,
		}
		if result.Err != nil {
			envelope.Error = &headlessEnvelopeError{
				Code:    result.ErrorCode,
				Message: result.Err.Error(),
			}
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(envelope)
	}
	if result.Output != "" {
		if _, err := io.WriteString(stdout, result.Output); err != nil {
			return fmt.Errorf("write Goal output: %w", err)
		}
		if !strings.HasSuffix(result.Output, "\n") {
			if _, err := io.WriteString(stdout, "\n"); err != nil {
				return fmt.Errorf("write Goal output terminator: %w", err)
			}
		}
	}
	if _, err := fmt.Fprintf(
		stdout,
		"Goal run: %s (reason=%s, session %s, continuations %d/%d)\n",
		result.Status,
		headlessGoalResultReason(result),
		result.SessionID,
		result.Continuations,
		result.MaxContinuations,
	); err != nil {
		return fmt.Errorf("write Goal summary: %w", err)
	}
	if result.Goal != nil {
		budget := "unbounded"
		if result.Goal.TokenBudget != nil {
			budget = fmt.Sprintf("%d", *result.Goal.TokenBudget)
		}
		if _, err := fmt.Fprintf(
			stdout,
			"Goal: %s revision=%d status=%s tokens=%d/%s coverage=%s\n",
			result.Goal.GoalID,
			result.Goal.Revision,
			result.Goal.Status,
			result.Goal.TokensUsed,
			budget,
			result.Goal.UsageCoverage,
		); err != nil {
			return fmt.Errorf("write Goal state: %w", err)
		}
		if result.Goal.StatusReason != "" {
			if _, err := fmt.Fprintf(
				stdout,
				"Reason: %s\n",
				result.Goal.StatusReason,
			); err != nil {
				return fmt.Errorf("write Goal reason: %w", err)
			}
		}
	}
	if result.Err != nil {
		if _, err := fmt.Fprintf(stderr, "Error: %s\n", result.Err); err != nil {
			return fmt.Errorf("write Goal error: %w", err)
		}
	}
	return nil
}

func headlessGoalResultReason(result headlessGoalResult) string {
	if strings.TrimSpace(result.ErrorCode) != "" {
		return result.ErrorCode
	}
	if result.Goal != nil &&
		strings.TrimSpace(result.Goal.StatusReasonCode) != "" {
		return result.Goal.StatusReasonCode
	}
	return string(result.Status)
}

func renderHeadlessGoalFailure(
	format outputFormat,
	options headlessGoalOptions,
	err error,
	code string,
	exitCode int,
) error {
	options = normalizeHeadlessGoalWriters(options)
	safeErr := sanitizeHeadlessGoalError(err, options.Runtime.apiKey)
	status := headlessGoalFailed
	if exitCode == ExitCancelled {
		status = headlessGoalCancelled
	}
	result := headlessGoalResult{
		Status:           status,
		SessionID:        strings.TrimSpace(options.Resume),
		MaxContinuations: options.MaxContinuations,
		ErrorCode:        code,
		Err:              safeErr,
		ExitCode:         exitCode,
	}
	if renderErr := renderHeadlessGoalResult(
		format,
		options.Stdout,
		options.Stderr,
		result,
	); renderErr != nil {
		return renderErr
	}
	return renderedExitError(exitCode, safeErr)
}
