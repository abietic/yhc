package engine

import "github.com/cloudwego/eino/schema"

// ContinueReason marks why the previous loop iteration continued.
type ContinueReason string

const (
	ContinueNextTurn                ContinueReason = "next_turn"
	ContinueCollapseDrainRetry      ContinueReason = "collapse_drain_retry"
	ContinueReactiveCompactRetry    ContinueReason = "reactive_compact_retry"
	ContinueMaxOutputTokensEscalate ContinueReason = "max_output_tokens_escalate"
	ContinueMaxOutputTokensRecovery ContinueReason = "max_output_tokens_recovery"
	ContinueStopHookBlocking        ContinueReason = "stop_hook_blocking"
	ContinueTokenBudgetContinuation ContinueReason = "token_budget_continuation"
	ContinueMediaRecovery           ContinueReason = "media_recovery"
)

type mediaRecoveryAttempt string

const (
	mediaRecoveryAttemptNone     mediaRecoveryAttempt = ""
	mediaRecoveryAttemptSelected mediaRecoveryAttempt = "selected_route_recovery"
	mediaRecoveryAttemptFallback mediaRecoveryAttempt = "fallback"
)

// TerminalReason marks why the query loop ended.
type TerminalReason string

const (
	TerminalCompleted                  TerminalReason = "completed"
	TerminalBlockingLimit              TerminalReason = "blocking_limit"
	TerminalImageError                 TerminalReason = "image_error"
	TerminalPromptInputError           TerminalReason = "prompt_input_error"
	TerminalPromptTooLong              TerminalReason = "prompt_too_long"
	TerminalAbortedStreaming           TerminalReason = "aborted_streaming"
	TerminalAbortedTools               TerminalReason = "aborted_tools"
	TerminalMaxTurns                   TerminalReason = "max_turns"
	TerminalModelError                 TerminalReason = "model_error"
	TerminalPersistenceError           TerminalReason = "persistence_error"
	TerminalWaitingInput               TerminalReason = "waiting_input"
	TerminalStopHookPrevented          TerminalReason = "stop_hook_prevented"
	TerminalHookStopped                TerminalReason = "hook_stopped"
	TerminalMaxStructuredOutputRetries TerminalReason = "error_max_structured_output_retries"
)

// Terminal is returned when the query loop ends.
type Terminal struct {
	Reason    TerminalReason
	TurnCount int
	MaxTurns  int
	Err       error
}

// AutoCompactTracking carries compaction state across iterations.
type AutoCompactTracking struct {
	Compacted           bool
	TurnID              string
	TurnCounter         int
	ConsecutiveFailures int
}

// MediaRecoveryState is query-local bounded recovery state. ProviderMessages
// is an attempt-local deep clone and must be cleared immediately after its one
// model call.
type MediaRecoveryState struct {
	ProjectionAttempted  bool
	FallbackAttempted    bool
	PendingAttempt       mediaRecoveryAttempt
	CanonicalMessages    []*schema.Message
	ProviderMessages     []*schema.Message
	RouteModel           string
	OmittedImageCount    int
	DerivativeImageCount int
	UsageLogicalRoundID  string
}

// QueryState is the mutable cross-iteration state carried through the eino graph.
type QueryState struct {
	Messages                     []*schema.Message
	SystemPrompt                 *schema.Message
	UserContext                  map[string]string
	SystemContext                map[string]string
	ToolUseContext               *ToolUseContext
	AutoCompactTracking          *AutoCompactTracking
	MaxOutputTokensRecoveryCount int
	HasAttemptedReactiveCompact  bool
	MaxOutputTokensOverride      *int
	PendingToolUseSummary        *ToolUseSummaryPromise
	StopHookActive               bool
	TurnCount                    int
	Transition                   ContinueReason
	NeedsFollowUp                bool
	AssistantMessages            []*schema.Message
	ToolUseBlocks                []*schema.ToolCall
	ToolResults                  []*schema.Message
	ShouldPreventContinuation    bool
	WithheldError                *schema.Message
	WithheldReason               string
	SnipTokensFreed              int
	TaskBudgetRemaining          *int
	Aborted                      bool
	MediaRecovery                MediaRecoveryState
}
