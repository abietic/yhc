package engine

import (
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/worktree"
)

// QueryEvent is the union type for all events yielded by the query loop.
type QueryEvent struct {
	RuntimeEventEnvelope

	Type QueryEventType

	// Generic message field for assistant/tool_result/stream events
	Message *schema.Message

	// TranscriptEntryID is the exact physical-record/message-index identity
	// attached only after the corresponding message has been durably encoded.
	// It is projection provenance, not model input or lifecycle authority.
	TranscriptEntryID string

	// For assistant messages
	AssistantMessage *schema.Message

	// For tool results
	ToolResultMessage *schema.Message

	// For attachments
	AttachmentMessage *schema.Message

	// For compact boundaries
	CompactBoundaryMessage *schema.Message

	// For tombstones
	TombstoneUUID string

	// ModelAttempt carries bounded non-secret identity for model-attempt
	// lifecycle events and attempt-owned assistant projections.
	ModelAttempt *ModelAttemptEvent

	// For user interruption
	InterruptionToolUse bool

	// For max turns
	MaxTurnsInfo *MaxTurnsInfo

	// For stream events
	StreamEvent *schema.Message

	// For tool use summaries
	ToolUseSummary *ToolUseSummaryEvent

	// For command lifecycle notifications
	CommandLifecycle *CommandLifecycleEvent

	// For typed slash-command outcomes after the canonical owner has applied
	// the action exactly once.
	CommandResult *CommandResultEvent

	// For tool progress updates (streaming progress during execution)
	ToolProgress *ToolProgressEvent

	// For local/background agent task progress snapshots
	TaskProgress *TaskProgressEvent

	// For local/background Agent launch and launch-failure metadata
	AgentLifecycle *AgentLifecycleEvent

	// For local TaskCreate/TaskUpdate/TaskStop task-list lifecycle snapshots
	TaskLifecycle *TaskLifecycleEvent

	// For engine-owned durable worktree state transitions.
	WorktreeLifecycle *WorktreeLifecycleEvent

	// For permission requests requiring interactive user input
	PermissionRequest *PermissionRequestEvent

	// For permission decisions that resolve a prior interactive request
	PermissionResolved *PermissionResolvedEvent

	// For hook status updates (spinner text during hook execution)
	HookStatus *HookStatusEvent

	// For asynchronously completed shell hook responses.
	HookResponse *HookResponseEvent

	// For auto-mode classifier lifecycle/progress status.
	ClassifierStatus *ClassifierStatusEvent

	// PermissionReview is an advisory shadow lifecycle projection. It carries
	// no action arguments, binding material, policy bytes, or secrets.
	PermissionReview *PermissionReviewEvent

	// For QueryEngine-owned Plan phase changes.
	PlanStateTransition *PlanStateTransitionEvent

	// GoalLifecycle is the ordered, presentation-free projection of one
	// QueryEngine-owned Goal transition. Durable Goal state remains the
	// lifecycle authority; reducers may only replay this detached snapshot.
	GoalLifecycle *GoalLifecycleEvent

	// For terminal (final event when the query loop ends)
	TerminalInfo *Terminal

	// CanonicalProjection is the versioned engine-owned projection lifecycle
	// envelope. The embedded RuntimeEventEnvelope remains the identity and
	// ordering owner.
	CanonicalProjection *CanonicalProjectionEvent

	// compactBoundaryMessages is an engine-private exact active projection.
	// It is never serialized or exposed as model input. The outer QueryEngine
	// history owner uses it to mirror a boundary that was already committed.
	compactBoundaryMessages  []*schema.Message
	compactBoundaryCommitted bool
}

// QueryEventType enumerates all event types.
type QueryEventType string

const (
	EventStreamRequestStart QueryEventType = "stream_request_start"
	EventAssistant          QueryEventType = "assistant"
	EventToolResult         QueryEventType = "tool_result"
	EventAttachment         QueryEventType = "attachment"
	EventCompactBoundary    QueryEventType = "compact_boundary"
	EventTombstone          QueryEventType = "tombstone"
	EventUserInterruption   QueryEventType = "user_interruption"
	EventMaxTurnsReached    QueryEventType = "max_turns_reached"
	EventStream             QueryEventType = "stream_event"
	EventToolUseSummary     QueryEventType = "tool_use_summary"
	EventCommandLifecycle   QueryEventType = "command_lifecycle"
	EventCommandResult      QueryEventType = "command_result"
	EventToolProgress       QueryEventType = "tool_progress"
	EventTaskProgress       QueryEventType = "task_progress"
	EventAgentLifecycle     QueryEventType = "agent_lifecycle"
	EventTaskLifecycle      QueryEventType = "task_lifecycle"
	EventWorktreeLifecycle  QueryEventType = "worktree_lifecycle"
	// Permission request/resolved events are emitted by the engine-owned
	// interaction coordinator for every interactive entrypoint.
	EventPermissionRequest   QueryEventType = "permission_request"
	EventPermissionResolved  QueryEventType = "permission_resolved"
	EventTerminal            QueryEventType = "terminal"
	EventHookStatus          QueryEventType = "hook_status"
	EventHookResponse        QueryEventType = "hook_response"
	EventClassifierStatus    QueryEventType = "classifier_status"
	EventPermissionReview    QueryEventType = "permission_review"
	EventPlanStateTransition QueryEventType = "plan_state_transition"
	EventGoalLifecycle       QueryEventType = "goal_lifecycle"
	EventCanonicalProjection QueryEventType = "canonical_projection"
	EventModelAttempt        QueryEventType = "model_attempt"
)

// RuntimeEventEnvelope identifies and orders an event independently of its
// payload. It is embedded additively so existing QueryEvent consumers remain
// source-compatible while runtime/TUI consumers can route and replay events.
type RuntimeEventEnvelope struct {
	SessionID       string
	ThreadID        string
	TurnID          string
	AgentID         string
	ParentSessionID string
	ParentThreadID  string
	ParentAgentID   string
	ParentToolUseID string
	Sequence        uint64
	Timestamp       time.Time
	CausationID     string
	AgentGeneration int64
	// Goal identity is additive execution attribution. Goal lifecycle events
	// still carry the complete detached snapshot; ordinary root/child events
	// carry only this exact immutable binding.
	GoalID                string
	GoalObjectiveRevision uint64
	GoalRootSessionID     string
	GoalRootThreadID      string
	GoalRootAgentID       string
	GoalTurnID            string
}

// RuntimeEventFamily groups the existing event vocabulary into stable reducer
// families without replacing the more specific QueryEventType values.
type RuntimeEventFamily string

const (
	RuntimeFamilyThread      RuntimeEventFamily = "thread"
	RuntimeFamilyTurnMessage RuntimeEventFamily = "turn_message"
	RuntimeFamilyTool        RuntimeEventFamily = "tool"
	RuntimeFamilyAgent       RuntimeEventFamily = "agent"
	RuntimeFamilyInteractive RuntimeEventFamily = "interactive"
	RuntimeFamilyPlanUsage   RuntimeEventFamily = "plan_usage"
	RuntimeFamilyTerminal    RuntimeEventFamily = "terminal"
	RuntimeFamilyOther       RuntimeEventFamily = "other"
)

// Family returns the reducer family for an event type.
func (e QueryEvent) Family() RuntimeEventFamily {
	if e.Type == EventCanonicalProjection &&
		e.CanonicalProjection != nil &&
		e.CanonicalProjection.Kind == CanonicalProjectionAssistantDelta {
		return RuntimeFamilyTurnMessage
	}
	switch e.Type {
	case EventStreamRequestStart:
		return RuntimeFamilyThread
	case EventAssistant, EventStream, EventAttachment, EventTombstone,
		EventModelAttempt:
		return RuntimeFamilyTurnMessage
	case EventCommandResult:
		return RuntimeFamilyTurnMessage
	case EventToolResult, EventToolProgress, EventToolUseSummary,
		EventCanonicalProjection:
		return RuntimeFamilyTool
	case EventTaskProgress, EventAgentLifecycle, EventTaskLifecycle, EventWorktreeLifecycle:
		return RuntimeFamilyAgent
	case EventPermissionRequest, EventPermissionResolved:
		return RuntimeFamilyInteractive
	case EventPermissionReview:
		return RuntimeFamilyInteractive
	case EventCompactBoundary, EventMaxTurnsReached, EventPlanStateTransition,
		EventGoalLifecycle:
		return RuntimeFamilyPlanUsage
	case EventTerminal, EventUserInterruption:
		return RuntimeFamilyTerminal
	default:
		return RuntimeFamilyOther
	}
}

// IsLosslessRuntimeEvent reports whether an event must never be coalesced or
// dropped by runtime adapters. The engine emitter delivers these events even
// after the request context has been cancelled.
func IsLosslessRuntimeEvent(eventType QueryEventType) bool {
	switch eventType {
	case EventAgentLifecycle,
		EventCommandResult,
		EventPermissionRequest,
		EventPermissionResolved,
		EventPlanStateTransition,
		EventGoalLifecycle,
		EventWorktreeLifecycle,
		EventCanonicalProjection,
		EventModelAttempt,
		EventTerminal:
		return true
	default:
		return false
	}
}

type ModelAttemptPhase string

const (
	ModelAttemptCandidateSkipped ModelAttemptPhase = "candidate_skipped"
	ModelAttemptStarted          ModelAttemptPhase = "started"
	ModelAttemptRetryWait        ModelAttemptPhase = "retry_wait"
	ModelAttemptDiscarded        ModelAttemptPhase = "discarded"
	ModelAttemptFailed           ModelAttemptPhase = "failed"
	ModelAttemptCommitted        ModelAttemptPhase = "committed"
)

type ModelAttemptOutputDisposition string

const (
	ModelAttemptOutputNeverStarted ModelAttemptOutputDisposition = "never_started"
	ModelAttemptOutputDiscarded    ModelAttemptOutputDisposition = "discarded"
	ModelAttemptOutputCommitted    ModelAttemptOutputDisposition = "committed"
)

// ModelAttemptEvent is a bounded safe attempt projection. It intentionally
// excludes account, endpoint, authentication, raw response, and prompt data.
type ModelAttemptEvent struct {
	LogicalRequestID    string
	LogicalRoundID      string
	AttemptID           string
	AttemptIndex        int
	Role                string
	Profile             string
	Provider            string
	APIModel            string
	RouteIdentityDigest string
	Phase               ModelAttemptPhase
	FailureClass        string
	AdmissionCode       string
	RetryCount          int
	SwitchCount         int
	ProviderCallCount   int
	LatencyMS           int64
	OutputDisposition   ModelAttemptOutputDisposition
}

// CommandResultStatus is the renderer-neutral outcome of one slash command.
type CommandResultStatus string

const (
	CommandResultSucceeded   CommandResultStatus = "succeeded"
	CommandResultFailed      CommandResultStatus = "failed"
	CommandResultUnsupported CommandResultStatus = "unsupported"
)

// CommandResultEvent is emitted after the canonical command owner has
// completed. FollowUpPrompt is consumed only by interactive entrypoints after
// the command turn terminates; it never grants a second mutation owner.
type CommandResultEvent struct {
	Command        string
	Action         commands.CommandAction
	Status         CommandResultStatus
	Output         string
	Error          string
	FollowUpPrompt string
}

// PlanStateTransitionEvent is the structured, replayable projection of one
// committed QueryEngine Plan-state transition.
type PlanStateTransitionEvent struct {
	FromPhase         PlanPhase
	Phase             PlanPhase
	PermissionMode    permission.Mode
	PlanFileIdentity  string
	ReturnMode        permission.Mode
	ApprovalRequestID string
	RequestID         string
	Revision          uint64
	Source            string
}

// WorktreeLifecycleEvent is the bounded reducer projection of one committed
// durable worktree record revision. The record store remains the recovery
// authority; replaying this payload never performs Git or filesystem work.
type WorktreeLifecycleEvent struct {
	RecordID           string
	OwnerKind          worktree.OwnerKind
	OwnerID            string
	FromState          worktree.State
	State              worktree.State
	RecordRevision     uint64
	RepositoryIdentity string
	RepoRoot           string
	Path               string
	Branch             string
	BaseCommit         string
	LastErrorCategory  worktree.ErrorCategory
	LastError          string
}

// IsCoalescibleRuntimeEvent reports high-frequency events that projections may
// replace with a newer event carrying the same identity/causation key.
func IsCoalescibleRuntimeEvent(eventType QueryEventType) bool {
	switch eventType {
	case EventStream, EventToolProgress, EventTaskProgress, EventHookStatus, EventClassifierStatus:
		return true
	default:
		return false
	}
}

// ClassifierStatusPhase is the bounded lifecycle emitted while auto-mode
// classifier work is in progress. Fast paths that skip classifier work do not
// emit these phases.
type ClassifierStatusPhase string

const (
	ClassifierStatusChecking  ClassifierStatusPhase = "checking"
	ClassifierStatusCompleted ClassifierStatusPhase = "completed"
	ClassifierStatusCleared   ClassifierStatusPhase = "cleared"
)

// ClassifierStatusEvent carries structured status for TUI shimmer/progress.
// Completed events include the classifier decision derived from the permission
// result; cleared events bound the UI checking state and should stop shimmer.
type ClassifierStatusEvent struct {
	ToolName  string
	ToolUseID string
	Phase     ClassifierStatusPhase
	Decision  string
	Reason    string
	Message   string
	UpdatedAt time.Time
}

// MaxTurnsInfo holds max turns event data.
type MaxTurnsInfo struct {
	MaxTurns  int
	TurnCount int
}

// ToolUseSummaryEvent holds tool use summary data.
// Mirrors reference messages.ts:5105-5116.
type ToolUseSummaryEvent struct {
	Summary             string
	PrecedingToolUseIDs []string
	UUID                string
	Timestamp           string
}

// CommandLifecyclePhase represents started vs completed.
type CommandLifecyclePhase string

const (
	CommandLifecycleStarted   CommandLifecyclePhase = "started"
	CommandLifecycleCompleted CommandLifecyclePhase = "completed"
)

// CommandLifecycleEvent notifies that a queued command started/completed processing.
// Mirrors query.ts:1600-1643.
type CommandLifecycleEvent struct {
	CommandUUID string
	Phase       CommandLifecyclePhase
}

// ToolProgressEvent carries streaming progress from a running tool.
// Mirrors reference tool progress streaming for Bash and Agent tools.
type ToolProgressEvent struct {
	ToolName  string // which tool is reporting progress
	ToolUseID string // identifies the specific tool invocation
	Content   string // incremental progress text
	IsFinal   bool   // true when the tool has completed
}

// TaskProgressUsage carries reference-shaped local/background agent usage.
type TaskProgressUsage struct {
	TotalTokens int
	ToolUses    int
	DurationMS  int64
}

// TaskProgressActivity is the bounded, input-free activity shape carried by
// runtime events. Full tool input/output remains in tool/transcript storage.
type TaskProgressActivity struct {
	ToolName    string
	Description string
	IsSearch    bool
	IsRead      bool
}

// AgentLifecycleEvent carries bounded launch metadata before the first model
// response. Transcript/output fields are paths, never copied payloads.
type AgentLifecycleEvent struct {
	Phase          string
	Name           string
	Task           string
	Description    string
	AgentType      string
	Model          string
	PermissionMode string
	Isolation      string
	CWD            string
	WorktreePath   string
	WorktreeBranch string
	TranscriptPath string
	OutputFile     string
	Status         string
	Error          string
	Generation     int64
	StartedAt      time.Time
}

// TaskProgressEvent carries a reference-shaped SDK system task_progress event
// for a running local/background agent.
type TaskProgressEvent struct {
	Type             string
	Subtype          string
	TaskID           string
	ToolUseID        string
	Description      string
	Usage            TaskProgressUsage
	LastToolName     string
	Summary          string
	RecentActivities []TaskProgressActivity
}

// TaskLifecycleEvent carries a bounded snapshot of local task-list mutations
// caused by TaskCreate, TaskUpdate, and TaskStop tool executions.
type TaskLifecycleEvent struct {
	Phase         string
	TaskID        string
	Subject       string
	Description   string
	ActiveForm    string
	Status        string
	Owner         string
	UpdatedFields []string
	UpdatedAt     time.Time
}

// PermissionRequestEvent is emitted by the engine-owned coordinator when a
// tool begins waiting for an interactive permission decision.
type PermissionRequestEvent struct {
	ToolName           string                       // the tool requesting permission
	CanonicalToolName  string                       // validated registry identity for presentation and replay
	ToolUseID          string                       // the specific tool invocation ID
	Input              map[string]any               // the tool's input parameters
	Message            string                       // human-readable description of what the tool wants to do
	Source             string                       // producer that owns interaction semantics
	Kind               string                       // permission, question, plan_approval, or repeated_tool
	Attempt            int                          // repeated identical call attempt, when Kind is repeated_tool
	PlanApproval       *PlanApprovalRequest         // immutable Plan request identity, when Kind is plan_approval
	Presentation       *PermissionPresentation      // bounded ordinary-permission display projection
	DecisionConstraint PermissionDecisionConstraint // adapter choices permitted for this request
}

// PermissionResolvedEvent closes a previously emitted permission request.
// Resolved requests remain in the bounded event ring for replay but are removed
// from the runtime snapshot's unresolved-request projection.
type PermissionResolvedEvent struct {
	ToolUseID    string
	Decision     string
	Reason       string
	Message      string
	Kind         string
	Attempt      int
	PlanApproval *PlanApprovalDecision
}

// HookStatusEvent is emitted when a shell hook with a StatusMessage is executing.
// The UI can display this as spinner text to inform the user what hook is running.
type HookStatusEvent struct {
	HookName      string // the hook identifier or command
	StatusMessage string // the human-readable status text
	Phase         string // "running" or "completed"
}

// HookResponseEvent is the bounded runtime projection of an asynchronously
// completed shell hook. Full output remains in the model attachment rather
// than the TUI event stream.
type HookResponseEvent struct {
	HookID        string
	HookName      string
	HookEvent     string
	ToolName      string
	StatusMessage string
	Outcome       string
	ExitCode      int
	AsyncRewake   bool
	Phase         string
	DurationMS    int64
}
