package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/engine/worktree"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

// ErrPersistedAgentMetadataMissing distinguishes the admitted-child crash
// window from corrupt or partially missing durable Agent state. Callers may
// converge the former as replay-only, but must fail closed for the latter.
var ErrPersistedAgentMetadataMissing = errors.New("missing persisted metadata")

// AgentExecutor is the interface that decouples the agent tool from the engine.
// An implementation spawns an isolated sub-agent query loop and returns the result.
type AgentExecutor interface {
	ExecuteAgent(ctx context.Context, opts AgentExecOptions) (*AgentExecResult, error)
}

// AgentWorktreeLifecycle is the narrow mutation boundary consumed by
// AgentRunner. The engine-owned durable worktree service implements it.
type AgentWorktreeLifecycle interface {
	Create(context.Context, worktree.CreateRequest) (worktree.Record, error)
	Remove(context.Context, string, worktree.Owner) (worktree.Record, error)
}

// AgentWorktreeRecoveryLifecycle is an optional restart capability. Normal
// launches depend only on AgentWorktreeLifecycle; continuation must fail
// closed unless the engine-owned service exposes fresh recovery admission.
type AgentWorktreeRecoveryLifecycle interface {
	AgentWorktreeLifecycle
	RecoverForContinuation(
		context.Context,
		string,
		worktree.Owner,
	) (worktree.Record, error)
}

// AgentWorktreeBinding freezes the parent QueryEngine's effective CWD and
// lifecycle service for one launch.
type AgentWorktreeBinding interface {
	AgentWorktreeLifecycle() AgentWorktreeLifecycle
	AgentWorktreeSourceDir() string
}

// AgentWorktreeBindingSnapshot freezes the three fields that must share one
// parent QueryEngine generation.
type AgentWorktreeBindingSnapshot struct {
	Lifecycle       AgentWorktreeLifecycle
	SourceDir       string
	ParentSessionID string
	Generation      uint64
}

// AgentWorktreeSnapshotBinding prevents launch/recovery from combining fields
// from two session-resume generations. Recovery fails closed when an executor
// exposes only the older two-call launch binding.
type AgentWorktreeSnapshotBinding interface {
	AgentWorktreeBindingSnapshot() AgentWorktreeBindingSnapshot
}

// AgentLaunchRecorder is an optional executor capability used by AgentRunner
// to publish canonical launch metadata synchronously before execution starts.
type AgentLaunchRecorder interface {
	RecordAgentLaunch(ctx context.Context, launch AgentLaunchSnapshot) error
}

// AgentLifecycleRecorder publishes non-terminal process lifecycle transitions
// after launch. It is process-local projection only; durable Agent metadata
// remains owned by RunningAgent.
type AgentLifecycleRecorder interface {
	RecordAgentLifecycle(ctx context.Context, lifecycle AgentLaunchSnapshot) error
}

// AgentExecutionAdmissionRecorder is an optional pre-execution durability
// boundary. Implementations may commit execution-specific Session admission
// after Agent identity/worktree binding and before AgentRunner publishes
// durable running metadata or calls ExecuteAgent.
type AgentExecutionAdmissionRecorder interface {
	RecordAgentExecutionAdmission(
		context.Context,
		AgentExecOptions,
		[]*schema.Message,
	) error
}

// AgentExecutionLinkAdmissionRecorder owns the WorkBoard relation commit. It
// is deliberately separate from child Session admission so AgentRunner can
// preserve link -> runtime publication -> child admission ordering.
type AgentExecutionLinkAdmissionRecorder interface {
	RecordAgentExecutionLinkAdmission(
		context.Context,
		AgentExecOptions,
	) error
}

// AgentLaunchSnapshot is the immutable launch boundary shared with runtime
// reducers. It contains references and metadata, never transcript/tool output.
type AgentLaunchSnapshot struct {
	Phase           string
	AgentID         string
	SessionID       string
	ThreadID        string
	ParentSessionID string
	ParentThreadID  string
	ParentAgentID   string
	ParentToolUseID string
	Name            string
	Task            string
	Description     string
	AgentType       string
	Model           string
	PermissionMode  string
	Isolation       string
	CWD             string
	WorktreePath    string
	WorktreeBranch  string
	Worktree        *AgentWorktreeResult
	TranscriptPath  string
	OutputFile      string
	Status          string
	Error           string
	Generation      int64
	StartedAt       time.Time
}

// AgentWorkItemReference is an opaque, revision-bound logical-work admission
// request. AgentRunner assigns the execution identity; the engine-owned
// admission recorder validates this reference against its own WorkBoard.
type AgentWorkItemReference struct {
	BoardID              string    `json:"board_id"`
	WorkItemID           string    `json:"work_item_id"`
	ExpectedItemRevision uint64    `json:"expected_item_revision"`
	AdmittedAt           time.Time `json:"admitted_at,omitempty"`
}

// AgentExecOptions holds the parameters for spawning a sub-agent execution.
type AgentExecOptions struct {
	Task                    string   // The prompt/task description for the sub-agent
	Description             string   // Short human-readable lifecycle description
	Model                   string   // Model override (empty uses default)
	MaxTurns                int      // Maximum conversation turns (0 is unlimited)
	AllowedTools            []string // Scoped tool list (nil means all available)
	SystemPromptSuffix      string   // Additional text appended to the system prompt
	SessionID               string   // Child transcript session identity
	ThreadID                string   // Child runtime conversation identity
	ParentSessionID         string   // Links to the spawning session for tracking
	ParentThreadID          string   // Links to the spawning runtime thread
	ParentAgentID           string   // Links to the spawning Agent for nested Agents
	ToolUseID               string   // Tool use ID that spawned this agent, if available
	SubagentType            string   // The type of specialized agent (e.g., "Explore", "Plan")
	AgentID                 string   // Runtime-assigned local/background agent ID
	Generation              int64    `json:"-"` // Runner-owned execution generation for the current call
	Name                    string   // Addressable name for SendMessage
	TeamName                string   // Team context for spawning
	Mode                    string   // Permission mode (e.g., "plan")
	InheritedPermissionMode string   // Effective permission mode inherited from the spawning parent turn
	Isolation               string   // Isolation mode (e.g., "worktree")
	CWD                     string   // Working directory override
	WorktreeSourceMode      worktree.SourceMode
	TranscriptDir           string // Engine-owned child transcript directory
	MessageID               string // Stable command UUID for optimistic/replay message dedupe
	WorkItem                *AgentWorkItemReference
	ResumeMessages          []*schema.Message
	executionMode           agentExecutionMode
}

type agentExecutionMode uint8

const (
	agentExecutionModeUnknown agentExecutionMode = iota
	agentExecutionModeBackground
	agentExecutionModeForeground
)

// IsBackgroundExecution reports whether AgentRunner admitted this execution
// through an asynchronous entrypoint. Like the foreground marker, this is
// process-local; durable continuation follows the child Session kernel pin.
func (o AgentExecOptions) IsBackgroundExecution() bool {
	return o.executionMode == agentExecutionModeBackground
}

// IsForegroundExecution reports whether AgentRunner admitted this execution
// through the synchronous foreground entrypoint. The marker is deliberately
// process-local: durable continuation is pinned by child session metadata
// rather than by expanding the persisted Agent option schema.
func (o AgentExecOptions) IsForegroundExecution() bool {
	return o.executionMode == agentExecutionModeForeground
}

type inheritedPermissionModeContextKey struct{}

// WithInheritedPermissionMode carries the effective parent turn permission mode
// through context-aware Agent tool execution.
func WithInheritedPermissionMode(ctx context.Context, mode string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, inheritedPermissionModeContextKey{}, mode)
}

// InheritedPermissionModeFromContext returns the effective parent turn mode.
func InheritedPermissionModeFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	mode, _ := ctx.Value(inheritedPermissionModeContextKey{}).(string)
	return mode
}

// AgentExecOutcome identifies how a foreground parent wait settled. A
// backgrounded outcome releases only that wait; it is not a child terminal
// status.
type AgentExecOutcome string

const (
	AgentExecOutcomeCompleted    AgentExecOutcome = "completed"
	AgentExecOutcomeBackgrounded AgentExecOutcome = "backgrounded"
)

// AgentExecResult holds the output of a sub-agent execution or the structured
// result of releasing a foreground wait.
type AgentExecResult struct {
	Result         string   // Final assistant response
	TurnCount      int      // Number of turns consumed
	TokensUsed     int      // Approximate token usage
	ToolsUsed      []string // Names of tools invoked during execution
	Messages       []*schema.Message
	WorktreePath   string
	WorktreeBranch string
	Worktree       *AgentWorktreeResult
	Outcome        AgentExecOutcome
	AgentID        string
	SessionID      string
	ThreadID       string
	Generation     int64
}

// AgentDetachRequest addresses one foreground wait through stable runtime
// identity and its exact owning parent Session.
type AgentDetachRequest struct {
	AgentID         string
	Generation      int64
	ParentSessionID string
}

// AgentDetachResult confirms that the same running child generation has
// released its foreground parent wait.
type AgentDetachResult struct {
	Outcome    AgentExecOutcome
	AgentID    string
	SessionID  string
	ThreadID   string
	Generation int64
	Status     string
}

// AgentWorktreeResult is the bounded Agent-facing projection of one durable
// worktree record. The record store remains cleanup authority.
type AgentWorktreeResult struct {
	RecordID          string                 `json:"record_id"`
	State             worktree.State         `json:"state"`
	Path              string                 `json:"path,omitempty"`
	Branch            string                 `json:"branch,omitempty"`
	BaseCommit        string                 `json:"base_commit,omitempty"`
	SourceDirtyReport *worktree.DirtyReport  `json:"source_dirty_report,omitempty"`
	ResultDirtyReport *worktree.DirtyReport  `json:"result_dirty_report,omitempty"`
	LastErrorCategory worktree.ErrorCategory `json:"last_error_category,omitempty"`
	LastError         string                 `json:"last_error,omitempty"`
}

// ToolActivity describes the most recent visible activity from a background
// sub-agent. It mirrors the reference LocalAgentTask progress shape in a
// bounded Go-friendly form.
type ToolActivity struct {
	ToolName            string
	Input               map[string]any
	ActivityDescription string
	IsSearch            bool
	IsRead              bool
}

// AgentProgress is the pollable progress state for a running or terminal
// background agent.
type AgentProgress struct {
	ToolUseCount     int
	TokenCount       int
	LastActivity     *ToolActivity
	RecentActivities []ToolActivity
	Summary          string // Periodic/model-generated summary; preserved across activity updates.
	ActivitySummary  string // Deterministic fallback derived from the latest bounded activity.
}

// DisplaySummary returns the dedicated summary when present, otherwise the
// deterministic latest-activity fallback.
func (p AgentProgress) DisplaySummary() string {
	if strings.TrimSpace(p.Summary) != "" {
		return p.Summary
	}
	return p.ActivitySummary
}

// AgentNotification is a terminal lifecycle update surfaced to callers that
// poll background agents. Message intentionally follows the reference
// <task-notification> envelope.
type AgentNotification struct {
	AgentID           string
	ToolUseID         string
	Status            string
	Description       string
	OutputFile        string
	Message           string
	CreatedAt         time.Time
	Generation        int64
	TerminalSequence  uint64
	CompletionID      string
	CompletionVersion int
	Legacy            bool
}

const agentCompletionVersion = 1

// AgentCompletionRecord is the durable child-terminal snapshot from which the
// parent can reconstruct an at-least-once delivery after process restart.
type AgentCompletionRecord struct {
	Version          int       `json:"version"`
	CompletionID     string    `json:"completion_id"`
	TerminalSequence uint64    `json:"terminal_sequence"`
	Status           string    `json:"status"`
	Message          string    `json:"message"`
	CreatedAt        time.Time `json:"created_at"`
}

// DeliveryID is the deterministic completion identity shared by the child
// terminal record, parent runtime-input transport, transcript receipt, and
// reducer projection.
func (n AgentNotification) DeliveryID() string {
	if strings.TrimSpace(n.CompletionID) != "" {
		return strings.TrimSpace(n.CompletionID)
	}
	return agentCompletionID(n.AgentID, n.Generation, n.TerminalSequence)
}

// LegacyDeliveryID identifies the pre-P14.1 transport key so an already
// delivered legacy completion is not reinjected during additive migration.
func (n AgentNotification) LegacyDeliveryID() string {
	if !n.Legacy {
		return ""
	}
	return fmt.Sprintf("agent-notification:%s:%d", n.AgentID, n.Generation)
}

func agentCompletionID(agentID string, generation int64, terminalSequence uint64) string {
	payload := fmt.Sprintf(
		"agent-completion/v1\x00%s\x00%d\x00%d",
		strings.TrimSpace(agentID),
		generation,
		terminalSequence,
	)
	digest := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("agent-completion:v1:%x", digest[:])
}

// AgentProgressUsage carries reference-shaped usage counters for a running
// local/background agent task_progress event.
type AgentProgressUsage struct {
	TotalTokens int   `json:"total_tokens"`
	ToolUses    int   `json:"tool_uses"`
	DurationMS  int64 `json:"duration_ms"`
}

// AgentProgressEvent is the SDK/runtime-visible non-terminal progress event
// shape for a running local/background agent.
type AgentProgressEvent struct {
	Type             string             `json:"type"`
	Subtype          string             `json:"subtype"`
	TaskID           string             `json:"task_id"`
	ToolUseID        string             `json:"tool_use_id,omitempty"`
	Description      string             `json:"description"`
	Usage            AgentProgressUsage `json:"usage"`
	LastToolName     string             `json:"last_tool_name,omitempty"`
	Summary          string             `json:"summary,omitempty"`
	RecentActivities []ToolActivity     `json:"recent_activities,omitempty"`
}

type agentForegroundWaitState uint8

const (
	agentForegroundWaitActive agentForegroundWaitState = iota + 1
	agentForegroundWaitDetaching
	agentForegroundWaitParentCanceled
	agentForegroundWaitBackgrounded
	agentForegroundWaitSettled
)

type agentForegroundWaitOutcome struct {
	result *AgentExecResult
	err    error
}

type agentForegroundWait struct {
	generation          int64
	state               agentForegroundWaitState
	parentCancelPending bool
	parentErr           error
	pending             *agentForegroundWaitOutcome
	done                chan agentForegroundWaitOutcome
}

// RunningAgent tracks a sub-agent that is currently executing or has completed.
type RunningAgent struct {
	ID              string            // Unique identifier
	SessionID       string            // Child transcript session identity
	ThreadID        string            // Child runtime conversation identity
	ParentSessionID string            // Spawning session identity
	ParentThreadID  string            // Spawning thread identity
	ParentAgentID   string            // Spawning Agent identity for nested Agents
	Type            string            // Reference-shaped task type, currently "local_agent"
	Task            string            // The prompt/task description
	Description     string            // Short human-readable lifecycle description
	Name            string            // Optional addressable name for SendMessage
	ToolUseID       string            // Tool use ID that spawned this agent, if available
	Status          string            // "running", "completed", "failed", "aborted"
	Messages        []*schema.Message // Conversation history
	Result          string            // Final result once completed
	Error           error             // Error if status is "failed"
	Progress        AgentProgress     // Pollable progress/summary state
	// PendingMessages holds SendMessage payloads queued while the agent is running.
	PendingMessages     []MessagePayload
	PendingMessageCount int
	// acceptedMessageIDs retains process-local idempotency for the current exact
	// generation even after a child drains or acknowledges a queued message.
	acceptedMessageIDs map[string]struct{}
	StartedAt          time.Time // When execution began
	CompletedAt        time.Time // When execution finished
	OutputFile         string    // Path where final output is persisted
	TranscriptPath     string    // Durable transcript path available from launch
	OutputOffset       int64     // Current persisted output byte offset
	Notified           bool      // Whether completion has been surfaced to the model/user
	TerminalSequence   uint64
	Completion         *AgentCompletionRecord
	Options            AgentExecOptions
	WorktreePath       string
	WorktreeBranch     string
	Worktree           *AgentWorktreeResult
	cancel             context.CancelFunc
	runGeneration      int64
	foregroundWait     *agentForegroundWait
	lifecycleRecorder  AgentLifecycleRecorder
	worktreeLifecycle  AgentWorktreeLifecycle
	worktreeOwner      worktree.Owner
	terminalDurable    bool
	predispatch        bool
	completionLegacy   bool
	mu                 *sync.Mutex
}

type persistedAgentMetadata struct {
	AgentID          string                 `json:"agent_id"`
	ThreadID         string                 `json:"thread_id,omitempty"`
	ParentSessionID  string                 `json:"parent_session_id,omitempty"`
	ParentThreadID   string                 `json:"parent_thread_id,omitempty"`
	ParentAgentID    string                 `json:"parent_agent_id,omitempty"`
	Name             string                 `json:"name,omitempty"`
	Description      string                 `json:"description,omitempty"`
	Task             string                 `json:"task,omitempty"`
	ToolUseID        string                 `json:"tool_use_id,omitempty"`
	Status           string                 `json:"status,omitempty"`
	Predispatch      bool                   `json:"predispatch,omitempty"`
	Error            string                 `json:"error,omitempty"`
	Generation       int64                  `json:"generation,omitempty"`
	TerminalSequence uint64                 `json:"terminal_sequence,omitempty"`
	Completion       *AgentCompletionRecord `json:"completion,omitempty"`
	StartedAt        time.Time              `json:"started_at,omitempty"`
	CompletedAt      time.Time              `json:"completed_at,omitempty"`
	OutputFile       string                 `json:"output_file,omitempty"`
	WorktreePath     string                 `json:"worktree_path,omitempty"`
	WorktreeBranch   string                 `json:"worktree_branch,omitempty"`
	Worktree         *AgentWorktreeResult   `json:"worktree,omitempty"`
	Model            string                 `json:"model,omitempty"`
	AgentType        string                 `json:"agent_type,omitempty"`
	PermissionMode   string                 `json:"permission_mode,omitempty"`
	Isolation        string                 `json:"isolation,omitempty"`
	CWD              string                 `json:"cwd,omitempty"`
	TranscriptPath   string                 `json:"transcript_path,omitempty"`
	TranscriptDir    string                 `json:"transcript_dir"`
	SessionID        string                 `json:"session_id"`
	Options          AgentExecOptions       `json:"options"`
}

// AgentExecutionKey identifies one immutable Agent execution generation.
type AgentExecutionKey struct {
	AgentID    string `json:"agent_id"`
	Generation int64  `json:"generation"`
}

// AgentExecutionSettlement is the runner-owned durable settlement projection
// used by WorkBoard terminal mutation and Session deletion admission.
type AgentExecutionSettlement struct {
	Key     AgentExecutionKey `json:"key"`
	State   string            `json:"state"`
	Settled bool              `json:"settled"`
}

type agentLaunchReservation struct {
	Generation         int64
	ParentSessionID    string
	PendingMessages    []MessagePayload
	acceptedMessageIDs map[string]struct{}
}

// AgentRunner manages the lifecycle of one QueryEngine lineage's sub-agent
// executions. Callers must bind it explicitly through context.
type AgentRunner struct {
	executor                 AgentExecutor
	executorGeneration       uint64
	activeAgents             map[string]*RunningAgent
	nameRegistry             map[string]string
	evictedAgents            map[string]string
	steering                 *AgentSteering
	mu                       sync.RWMutex
	executionWG              sync.WaitGroup
	closed                   bool
	maxAgents                int
	outputDir                string
	afterResumeLookupForTest func()
	launchingAgents          map[string]*agentLaunchReservation
	lifecycleAdmission       func() bool
}

// NewAgentRunner creates an AgentRunner with the specified concurrency limit.
func NewAgentRunner(maxAgents int) *AgentRunner {
	if maxAgents <= 0 {
		maxAgents = 4
	}
	return &AgentRunner{
		activeAgents:    make(map[string]*RunningAgent),
		nameRegistry:    make(map[string]string),
		evictedAgents:   make(map[string]string),
		steering:        NewAgentSteering(),
		maxAgents:       maxAgents,
		outputDir:       defaultAgentOutputDir(),
		launchingAgents: make(map[string]*agentLaunchReservation),
	}
}

// defaultAgentOutputDir uses the user cache namespace instead of
// /tmp/eino-agent. The latter is also a common path for a locally built binary;
// when it is a file, creating /tmp/eino-agent/agent-output fails with ENOTDIR.
// A stable cache path also keeps persisted agent metadata available for resume.
func defaultAgentOutputDir() string {
	root := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME"))
	if root == "" {
		var err error
		root, err = os.UserCacheDir()
		if err != nil || strings.TrimSpace(root) == "" {
			root = os.TempDir()
		}
	}
	return filepath.Join(root, "eino-agent", "agent-output")
}

// SetOutputDir overrides where agent lifecycle output files are persisted.
// Tests use this to keep output deterministic and session-local.
func (r *AgentRunner) SetOutputDir(dir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outputDir = dir
}

// DurableTranscriptDir returns the runner-owned child transcript directory.
// Recovery callers may inspect this directory, but AgentRunner remains the
// only owner of Agent metadata registration and continuation.
func (r *AgentRunner) DurableTranscriptDir() string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.agentTranscriptDirLocked()
}

// SetExecutor registers an AgentExecutor implementation that powers sub-agent
// query loops. Without this, the agent tool will fall back to a not-available message.
func (r *AgentRunner) SetExecutor(executor AgentExecutor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.executor = executor
	r.executorGeneration++
}

// SetLifecycleAdmission installs the root Session deletion gate. The callback
// must not call WorkBoard or AgentRunner.
func (r *AgentRunner) SetLifecycleAdmission(admitted func() bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.lifecycleAdmission = admitted
	r.mu.Unlock()
}

func (r *AgentRunner) lifecycleAdmittedLocked() bool {
	return r.lifecycleAdmission == nil || r.lifecycleAdmission()
}

func (r *AgentRunner) lifecycleAdmitted() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lifecycleAdmittedLocked()
}

func validateAgentWorktreeOptions(opts AgentExecOptions) error {
	isolation := strings.TrimSpace(opts.Isolation)
	switch isolation {
	case "", "none", "worktree":
	default:
		return fmt.Errorf(
			"agent_runner: unsupported isolation mode %q",
			isolation,
		)
	}
	if isolation == "worktree" && strings.TrimSpace(opts.CWD) != "" {
		return errors.New(
			"agent_runner: worktree isolation and explicit cwd are mutually exclusive",
		)
	}
	switch opts.WorktreeSourceMode {
	case "", worktree.SourceRequireClean, worktree.SourceIgnoreDirty:
	default:
		return fmt.Errorf(
			"agent_runner: unsupported worktree source mode %q",
			opts.WorktreeSourceMode,
		)
	}
	if isolation != "worktree" && opts.WorktreeSourceMode != "" {
		return errors.New(
			"agent_runner: worktree source mode requires worktree isolation",
		)
	}
	return nil
}

func prepareAgentWorktree(
	ctx context.Context,
	executor AgentExecutor,
	opts AgentExecOptions,
	owner worktree.Owner,
) (AgentWorktreeLifecycle, worktree.Record, error) {
	if strings.TrimSpace(opts.Isolation) != "worktree" {
		return nil, worktree.Record{}, nil
	}
	binding := snapshotAgentWorktreeBinding(executor)
	if binding.Lifecycle == nil {
		return nil, worktree.Record{}, errors.New(
			"agent_runner: worktree isolation requires an engine-owned lifecycle service",
		)
	}
	sourceDir := strings.TrimSpace(binding.SourceDir)
	if sourceDir == "" {
		return nil, worktree.Record{}, errors.New(
			"agent_runner: worktree isolation has no parent execution cwd",
		)
	}
	lifecycle := binding.Lifecycle
	record, err := lifecycle.Create(ctx, worktree.CreateRequest{
		Owner:      owner,
		SourceDir:  sourceDir,
		BaseRef:    "HEAD",
		SourceMode: opts.WorktreeSourceMode,
	})
	if err != nil {
		return lifecycle, record, fmt.Errorf(
			"agent_runner: create worktree isolation: %w",
			err,
		)
	}
	return lifecycle, record, nil
}

func snapshotAgentWorktreeBinding(
	executor AgentExecutor,
) AgentWorktreeBindingSnapshot {
	if binding, ok := executor.(AgentWorktreeSnapshotBinding); ok {
		return binding.AgentWorktreeBindingSnapshot()
	}
	binding, ok := executor.(AgentWorktreeBinding)
	if !ok {
		return AgentWorktreeBindingSnapshot{}
	}
	return AgentWorktreeBindingSnapshot{
		Lifecycle: binding.AgentWorktreeLifecycle(),
		SourceDir: binding.AgentWorktreeSourceDir(),
	}
}

type agentWorktreeRecoveryInput struct {
	AgentID         string
	SessionID       string
	ThreadID        string
	ParentSessionID string
	ParentThreadID  string
	ParentAgentID   string
	ParentToolUseID string
	Worktree        *AgentWorktreeResult
}

type recoveredAgentWorktree struct {
	lifecycle AgentWorktreeLifecycle
	owner     worktree.Owner
	record    worktree.Record
}

func agentWorktreeRecoveryInputFromAgent(
	agent *RunningAgent,
) agentWorktreeRecoveryInput {
	if agent == nil {
		return agentWorktreeRecoveryInput{}
	}
	return agentWorktreeRecoveryInput{
		AgentID:         agent.ID,
		SessionID:       agent.SessionID,
		ThreadID:        agent.ThreadID,
		ParentSessionID: agent.ParentSessionID,
		ParentThreadID:  agent.ParentThreadID,
		ParentAgentID:   agent.ParentAgentID,
		ParentToolUseID: agent.ToolUseID,
		Worktree:        cloneAgentWorktreeResult(agent.Worktree),
	}
}

func agentWorktreeRecoveryInputFromMetadata(
	agentID string,
	metadata persistedAgentMetadata,
) agentWorktreeRecoveryInput {
	return agentWorktreeRecoveryInput{
		AgentID: agentID,
		SessionID: firstNonEmpty(
			metadata.Options.SessionID,
			metadata.SessionID,
			agentID,
		),
		ThreadID: firstNonEmpty(
			metadata.Options.ThreadID,
			metadata.ThreadID,
			agentID,
		),
		ParentSessionID: firstNonEmpty(
			metadata.Options.ParentSessionID,
			metadata.ParentSessionID,
		),
		ParentThreadID: firstNonEmpty(
			metadata.Options.ParentThreadID,
			metadata.ParentThreadID,
		),
		ParentAgentID: firstNonEmpty(
			metadata.Options.ParentAgentID,
			metadata.ParentAgentID,
		),
		ParentToolUseID: firstNonEmpty(
			metadata.Options.ToolUseID,
			metadata.ToolUseID,
		),
		Worktree: cloneAgentWorktreeResult(metadata.Worktree),
	}
}

func recoverAgentWorktreeContinuation(
	ctx context.Context,
	executor AgentExecutor,
	input agentWorktreeRecoveryInput,
) (*recoveredAgentWorktree, error) {
	snapshotBinding, ok := executor.(AgentWorktreeSnapshotBinding)
	if !ok {
		return nil, errors.New(
			"durable recovery requires an atomic worktree binding",
		)
	}
	binding := snapshotBinding.AgentWorktreeBindingSnapshot()
	if binding.Lifecycle == nil {
		return nil, errors.New(
			"durable recovery requires an engine-owned worktree service",
		)
	}
	parentSessionID := strings.TrimSpace(binding.ParentSessionID)
	if parentSessionID == "" ||
		input.ParentSessionID == "" ||
		parentSessionID != input.ParentSessionID {
		return nil, fmt.Errorf(
			"parent session %q does not own recovered Agent session %q",
			parentSessionID,
			input.ParentSessionID,
		)
	}
	recovery, ok := binding.Lifecycle.(AgentWorktreeRecoveryLifecycle)
	if !ok {
		return nil, errors.New(
			"engine-owned worktree service has no recovery admission",
		)
	}
	if input.Worktree == nil ||
		strings.TrimSpace(input.Worktree.RecordID) == "" {
		return nil, errors.New("durable worktree record identity is missing")
	}
	owner := worktree.Owner{
		Kind:            worktree.OwnerAgent,
		ID:              input.AgentID,
		SessionID:       input.SessionID,
		ThreadID:        input.ThreadID,
		ParentSessionID: input.ParentSessionID,
		ParentThreadID:  input.ParentThreadID,
		ParentAgentID:   input.ParentAgentID,
		ParentToolUseID: input.ParentToolUseID,
	}
	record, err := recovery.RecoverForContinuation(
		ctx,
		input.Worktree.RecordID,
		owner,
	)
	if err != nil {
		return nil, err
	}
	after := snapshotBinding.AgentWorktreeBindingSnapshot()
	if after.Generation != binding.Generation ||
		after.ParentSessionID != binding.ParentSessionID {
		return nil, errors.New(
			"worktree recovery binding changed during admission",
		)
	}
	switch record.State {
	case worktree.StateReady,
		worktree.StateRetained,
		worktree.StateCleanupFailed:
	default:
		return nil, fmt.Errorf(
			"recovered worktree %q is not continuation-ready in state %s",
			record.ID,
			record.State,
		)
	}
	return &recoveredAgentWorktree{
		lifecycle: recovery,
		owner:     owner,
		record:    record,
	}, nil
}

func recoveredLifecycle(
	recovered *recoveredAgentWorktree,
) AgentWorktreeLifecycle {
	if recovered == nil {
		return nil
	}
	return recovered.lifecycle
}

func recoveredOwner(
	recovered *recoveredAgentWorktree,
) worktree.Owner {
	if recovered == nil {
		return worktree.Owner{}
	}
	return recovered.owner
}

// HasExecutor reports whether an executor has been registered.
func (r *AgentRunner) HasExecutor() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.executor != nil
}

// GetAgent retrieves a running or completed agent by ID.
func (r *AgentRunner) GetAgent(id string) (*RunningAgent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	agent, ok := r.activeAgents[id]
	return agent, ok
}

// GetAgentSnapshot retrieves a copy of a running or completed agent by ID.
// Callers should prefer this over GetAgent when they only need to inspect
// lifecycle metadata because the returned value is safe to read without
// holding the agent's internal mutex.
func (r *AgentRunner) GetAgentSnapshot(id string) (RunningAgent, bool) {
	r.mu.RLock()
	agent, ok := r.activeAgents[id]
	r.mu.RUnlock()
	if !ok {
		return RunningAgent{}, false
	}

	agent.mu.Lock()
	defer agent.mu.Unlock()
	snapshot := *agent
	snapshot.Messages = append([]*schema.Message(nil), agent.Messages...)
	snapshot.Progress = cloneAgentProgress(agent.Progress)
	snapshot.Options = cloneAgentExecOptions(agent.Options)
	snapshot.Worktree = cloneAgentWorktreeResult(agent.Worktree)
	snapshot.Completion = cloneAgentCompletionRecord(agent.Completion)
	snapshot.PendingMessages = cloneMessagePayloads(agent.PendingMessages)
	snapshot.PendingMessageCount = len(snapshot.PendingMessages)
	snapshot.acceptedMessageIDs = cloneStringSet(agent.acceptedMessageIDs)
	snapshot.cancel = nil
	snapshot.foregroundWait = nil
	snapshot.lifecycleRecorder = nil
	snapshot.worktreeLifecycle = nil
	snapshot.worktreeOwner = worktree.Owner{}
	snapshot.mu = nil
	return snapshot, true
}

// ExecutionGeneration returns the durable execution generation represented by
// this snapshot. The generation is intentionally read-only outside tools.
func (a RunningAgent) ExecutionGeneration() int64 {
	return a.runGeneration
}

// PredispatchFailure reports whether the durable terminal snapshot failed
// before executor installation. Such rows remain inspectable but must not gain
// runtime mutation actions.
func (a RunningAgent) PredispatchFailure() bool {
	return a.predispatch
}

// ActiveCount returns the number of currently running agents.
func (r *AgentRunner) ActiveCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.activeCountLocked()
}

// Shutdown prevents new launches, cancels every running generation, and waits
// for their executors to return. The caller controls the maximum join time.
func (r *AgentRunner) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	r.mu.Lock()
	r.closed = true
	cancels := make([]context.CancelFunc, 0, len(r.activeAgents))
	for _, agent := range r.activeAgents {
		agent.mu.Lock()
		if agent.Status == "running" && agent.cancel != nil {
			cancels = append(cancels, agent.cancel)
		}
		agent.mu.Unlock()
	}
	r.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		r.executionWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *AgentRunner) activeCountLocked() int {
	count := 0
	for _, a := range r.activeAgents {
		a.mu.Lock()
		if a.Status == "running" {
			count++
		}
		a.mu.Unlock()
	}
	return count
}

func (r *AgentRunner) activeCountLockedWithAgentStatus(lockedAgent *RunningAgent, lockedStatus string) int {
	count := 0
	for _, a := range r.activeAgents {
		if a == lockedAgent {
			if lockedStatus == "running" {
				count++
			}
			continue
		}
		a.mu.Lock()
		if a.Status == "running" {
			count++
		}
		a.mu.Unlock()
	}
	return count
}

// RunAgent spawns a foreground sub-agent, tracks its lifecycle, and blocks the
// parent call until either the child finishes or its foreground wait is
// explicitly detached. The executor runs exactly once in a runner-owned
// goroutine so detachment can release the caller without restarting the child.
func RunAgent(ctx context.Context, runner *AgentRunner, opts AgentExecOptions) (*AgentExecResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	opts.executionMode = agentExecutionModeForeground
	running, executor, agentCtx, cancel, execOpts, err := runner.launchAgent(ctx, opts)
	if err != nil {
		return nil, err
	}
	running.mu.Lock()
	generation := running.runGeneration
	wait := running.foregroundWait
	running.mu.Unlock()
	if wait == nil || wait.generation != generation {
		initErr := fmt.Errorf(
			"agent_runner: foreground wait was not initialized for agent %q generation %d",
			running.ID,
			generation,
		)
		_, _ = finishAgentExecution(
			agentCtx,
			running,
			generation,
			cancel,
			nil,
			initErr,
		)
		return nil, initErr
	}
	go func() {
		result, execErr := executor.ExecuteAgent(agentCtx, execOpts)
		result, execErr = finishAgentExecution(
			agentCtx,
			running,
			generation,
			cancel,
			result,
			execErr,
		)
		runner.settleForegroundWait(running, generation, result, execErr)
	}()

	parentDone := ctx.Done()
	for {
		select {
		case outcome := <-wait.done:
			return outcome.result, outcome.err
		case <-parentDone:
			parentDone = nil
			runner.cancelForegroundWait(running, generation, ctx.Err())
		}
	}
}

// RunAgentBackground registers a sub-agent lifecycle entry, starts execution in
// a goroutine, and returns the registered lifecycle metadata immediately.
func RunAgentBackground(ctx context.Context, runner *AgentRunner, opts AgentExecOptions) (RunningAgent, error) {
	opts.executionMode = agentExecutionModeBackground
	running, executor, agentCtx, cancel, execOpts, err := runner.launchAgent(ctx, opts)
	if err != nil {
		return RunningAgent{}, err
	}
	running.mu.Lock()
	generation := running.runGeneration
	running.mu.Unlock()
	snapshot, _ := runner.GetAgentSnapshot(running.ID)

	go func() {
		result, execErr := executor.ExecuteAgent(agentCtx, execOpts)
		_, _ = finishAgentExecution(agentCtx, running, generation, cancel, result, execErr)
	}()

	return snapshot, nil
}

func (r *AgentRunner) launchAgent(ctx context.Context, opts AgentExecOptions) (*RunningAgent, AgentExecutor, context.Context, context.CancelFunc, AgentExecOptions, error) {
	if r == nil {
		return nil, nil, nil, nil, AgentExecOptions{}, fmt.Errorf("agent_runner: runner is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateAgentWorktreeOptions(opts); err != nil {
		return nil, nil, nil, nil, AgentExecOptions{}, err
	}

	agentID := strings.TrimSpace(opts.AgentID)
	if agentID == "" {
		agentID = uuid.New().String()
	}
	threadID := strings.TrimSpace(opts.ThreadID)
	if threadID == "" {
		threadID = uuid.New().String()
	}
	sessionID := strings.TrimSpace(opts.SessionID)
	if sessionID == "" {
		// Keep the historical transcript filename stable while treating session
		// and Agent identity as separate fields in the runtime contract.
		sessionID = agentID
	}
	opts.AgentID = agentID
	opts.ThreadID = threadID
	opts.SessionID = sessionID
	launchCtx := WithAgentRunner(ctx, r)
	executionParent := launchCtx
	if opts.IsForegroundExecution() {
		// Parent cancellation is forwarded only while the foreground wait owns
		// that lease. Detach revokes the lease without restarting the child.
		executionParent = context.WithoutCancel(launchCtx)
	}
	agentCtx, cancel := context.WithCancel(executionParent)
	agentCtx = WithAgentRunner(agentCtx, r)

	r.mu.Lock()
	executor := AgentExecutorFromCtx(ctx)
	if executor == nil {
		executor = r.executor
	}
	if r.closed {
		r.mu.Unlock()
		cancel()
		return nil, nil, nil, nil, AgentExecOptions{}, fmt.Errorf("agent_runner: runner is closed")
	}
	if !r.lifecycleAdmittedLocked() {
		r.mu.Unlock()
		cancel()
		return nil, nil, nil, nil, AgentExecOptions{}, fmt.Errorf(
			"agent_runner: active Session deletion blocks launch",
		)
	}
	if executor == nil {
		r.mu.Unlock()
		cancel()
		return nil, nil, nil, nil, AgentExecOptions{}, fmt.Errorf("agent_runner: no executor registered")
	}
	if _, exists := r.activeAgents[agentID]; exists {
		r.mu.Unlock()
		cancel()
		return nil, nil, nil, nil, AgentExecOptions{}, fmt.Errorf(
			"agent_runner: agent %q already exists",
			agentID,
		)
	}
	if _, exists := r.launchingAgents[agentID]; exists {
		r.mu.Unlock()
		cancel()
		return nil, nil, nil, nil, AgentExecOptions{}, fmt.Errorf(
			"agent_runner: agent %q is already launching",
			agentID,
		)
	}
	if r.activeCountLocked()+len(r.launchingAgents) >= r.maxAgents {
		r.mu.Unlock()
		cancel()
		return nil, nil, nil, nil, AgentExecOptions{}, fmt.Errorf("agent_runner: max concurrent agents reached (%d)", r.maxAgents)
	}
	if err := os.MkdirAll(r.outputDir, 0o700); err != nil {
		r.mu.Unlock()
		cancel()
		return nil, nil, nil, nil, AgentExecOptions{}, fmt.Errorf("agent_runner: prepare output dir: %w", err)
	}
	outputDir := r.outputDir
	r.launchingAgents[agentID] = &agentLaunchReservation{
		Generation:      1,
		ParentSessionID: opts.ParentSessionID,
	}
	r.executionWG.Add(1)
	r.mu.Unlock()

	owner := worktree.Owner{
		Kind:            worktree.OwnerAgent,
		ID:              agentID,
		SessionID:       sessionID,
		ThreadID:        threadID,
		ParentSessionID: opts.ParentSessionID,
		ParentThreadID:  opts.ParentThreadID,
		ParentAgentID:   opts.ParentAgentID,
		ParentToolUseID: opts.ToolUseID,
	}
	lifecycle, worktreeRecord, err := prepareAgentWorktree(
		launchCtx,
		executor,
		opts,
		owner,
	)
	if err != nil {
		cancel()
		r.finishAgentLaunchReservation(agentID)
		r.executionWG.Done()
		return nil, nil, nil, nil, AgentExecOptions{}, err
	}
	worktreeResult := agentWorktreeResultFromRecord(worktreeRecord)
	if worktreeResult != nil {
		opts.CWD = worktreeResult.Path
	}
	opts.TranscriptDir = filepath.Join(outputDir, "transcripts")
	initialMessages := append([]*schema.Message(nil), opts.ResumeMessages...)
	initialMessages = append(initialMessages, &schema.Message{
		Role:    schema.User,
		Content: opts.Task,
	})
	if opts.WorkItem != nil {
		opts.WorkItem.AdmittedAt = time.Now().UTC()
	}
	storedOpts := cloneAgentExecOptions(opts)
	storedOpts.AgentID = agentID
	storedOpts.Generation = 1
	storedOpts.ResumeMessages = nil
	transcriptPath := transcript.NewRecorder(
		sessionID,
		opts.TranscriptDir,
	).Path()
	running := &RunningAgent{
		ID:                agentID,
		SessionID:         sessionID,
		ThreadID:          threadID,
		ParentSessionID:   opts.ParentSessionID,
		ParentThreadID:    opts.ParentThreadID,
		ParentAgentID:     opts.ParentAgentID,
		Type:              "local_agent",
		Task:              opts.Task,
		Description:       firstNonEmpty(opts.Description, opts.Task),
		Name:              opts.Name,
		ToolUseID:         opts.ToolUseID,
		Status:            "running",
		Messages:          initialMessages,
		StartedAt:         time.Now(),
		OutputFile:        filepath.Join(outputDir, agentID+".out"),
		TranscriptPath:    transcriptPath,
		Options:           storedOpts,
		Worktree:          cloneAgentWorktreeResult(worktreeResult),
		cancel:            cancel,
		runGeneration:     1,
		worktreeLifecycle: lifecycle,
		worktreeOwner:     owner,
		mu:                &sync.Mutex{},
	}
	if opts.IsForegroundExecution() {
		running.foregroundWait = &agentForegroundWait{
			generation: running.runGeneration,
			state:      agentForegroundWaitActive,
			done:       make(chan agentForegroundWaitOutcome, 1),
		}
	}
	if recorder, ok := executor.(AgentLifecycleRecorder); ok {
		running.lifecycleRecorder = recorder
	}
	if worktreeResult != nil {
		running.WorktreePath = worktreeResult.Path
		running.WorktreeBranch = worktreeResult.Branch
	}

	if err := r.prepareAgentLaunch(launchCtx, executor, running); err != nil {
		cleanupErr := cleanupPreparedAgentWorktree(
			lifecycle,
			worktreeRecord,
			owner,
		)
		err = errors.Join(err, cleanupErr)
		persistErr := persistLinkedPredispatchAgentFailure(running, err)
		cancel()
		r.finishAgentLaunchReservation(agentID)
		r.executionWG.Done()
		return nil, nil, nil, nil, AgentExecOptions{}, errors.Join(
			err,
			persistErr,
		)
	}

	r.mu.Lock()
	reservation := r.launchingAgents[agentID]
	if r.closed ||
		!r.lifecycleAdmittedLocked() ||
		reservation == nil ||
		reservation.Generation != running.runGeneration {
		delete(r.launchingAgents, agentID)
		r.mu.Unlock()
		cleanupErr := cleanupPreparedAgentWorktree(
			lifecycle,
			worktreeRecord,
			owner,
		)
		cancel()
		r.executionWG.Done()
		return nil, nil, nil, nil, AgentExecOptions{}, errors.Join(
			errors.New("agent_runner: runner lifecycle changed during launch"),
			cleanupErr,
		)
	}
	running.PendingMessages = cloneMessagePayloads(
		reservation.PendingMessages,
	)
	running.PendingMessageCount = len(running.PendingMessages)
	running.acceptedMessageIDs = cloneStringSet(
		reservation.acceptedMessageIDs,
	)
	delete(r.launchingAgents, agentID)
	r.activeAgents[agentID] = running
	delete(r.evictedAgents, agentID)
	if running.Name != "" {
		r.nameRegistry[running.Name] = agentID
	}
	r.registerAgentSteeringLocked(agentID)
	r.mu.Unlock()

	execOpts := cloneAgentExecOptions(opts)
	execOpts.Generation = running.runGeneration
	return running, executor, agentCtx, cancel, execOpts, nil
}

// DetachAgent releases one exact foreground parent wait while preserving the
// same running executor, Agent identity, lineage, and generation.
func (r *AgentRunner) DetachAgent(
	request AgentDetachRequest,
) (AgentDetachResult, error) {
	if r == nil {
		return AgentDetachResult{}, fmt.Errorf("agent_runner: runner is nil")
	}
	if !r.lifecycleAdmitted() {
		return AgentDetachResult{}, fmt.Errorf(
			"agent_runner: active Session deletion blocks detach",
		)
	}
	request.AgentID = strings.TrimSpace(request.AgentID)
	request.ParentSessionID = strings.TrimSpace(request.ParentSessionID)
	if request.AgentID == "" {
		return AgentDetachResult{}, fmt.Errorf("agent_runner: Agent ID is required")
	}
	if request.Generation <= 0 {
		return AgentDetachResult{}, fmt.Errorf(
			"agent_runner: Agent generation must be positive",
		)
	}
	if request.ParentSessionID == "" {
		return AgentDetachResult{}, fmt.Errorf(
			"agent_runner: parent Session identity is required",
		)
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return AgentDetachResult{}, fmt.Errorf("agent_runner: runner is closed")
	}
	running, ok := r.activeAgents[request.AgentID]
	if !ok {
		r.mu.Unlock()
		return AgentDetachResult{}, fmt.Errorf(
			"agent_runner: agent %q not found",
			request.AgentID,
		)
	}
	running.mu.Lock()
	if running.ParentSessionID != request.ParentSessionID {
		running.mu.Unlock()
		r.mu.Unlock()
		return AgentDetachResult{}, fmt.Errorf(
			"agent_runner: agent %q is not owned by parent Session %q",
			request.AgentID,
			request.ParentSessionID,
		)
	}
	if running.runGeneration != request.Generation {
		running.mu.Unlock()
		r.mu.Unlock()
		return AgentDetachResult{}, fmt.Errorf(
			"agent_runner: agent %q generation is %d, not %d",
			request.AgentID,
			running.runGeneration,
			request.Generation,
		)
	}
	if running.Status != "running" {
		status := running.Status
		running.mu.Unlock()
		r.mu.Unlock()
		return AgentDetachResult{}, fmt.Errorf(
			"agent_runner: agent %q is not running (status: %s)",
			request.AgentID,
			status,
		)
	}
	wait := running.foregroundWait
	if wait == nil || wait.generation != request.Generation {
		running.mu.Unlock()
		r.mu.Unlock()
		return AgentDetachResult{}, fmt.Errorf(
			"agent_runner: agent %q generation %d has no foreground wait",
			request.AgentID,
			request.Generation,
		)
	}
	switch wait.state {
	case agentForegroundWaitActive:
	case agentForegroundWaitDetaching:
		running.mu.Unlock()
		r.mu.Unlock()
		return AgentDetachResult{}, fmt.Errorf(
			"agent_runner: agent %q generation %d detach is already in progress",
			request.AgentID,
			request.Generation,
		)
	case agentForegroundWaitBackgrounded:
		running.mu.Unlock()
		r.mu.Unlock()
		return AgentDetachResult{}, fmt.Errorf(
			"agent_runner: agent %q generation %d is already backgrounded",
			request.AgentID,
			request.Generation,
		)
	case agentForegroundWaitParentCanceled:
		running.mu.Unlock()
		r.mu.Unlock()
		return AgentDetachResult{}, fmt.Errorf(
			"agent_runner: parent cancellation already owns agent %q generation %d",
			request.AgentID,
			request.Generation,
		)
	default:
		running.mu.Unlock()
		r.mu.Unlock()
		return AgentDetachResult{}, fmt.Errorf(
			"agent_runner: agent %q generation %d foreground wait is already settled",
			request.AgentID,
			request.Generation,
		)
	}

	wait.state = agentForegroundWaitDetaching
	lifecycle := r.agentLaunchSnapshotLocked(running, "backgrounded", "")
	lifecycle.Status = "running"
	recorder := running.lifecycleRecorder
	result := AgentDetachResult{
		Outcome:    AgentExecOutcomeBackgrounded,
		AgentID:    running.ID,
		SessionID:  running.SessionID,
		ThreadID:   running.ThreadID,
		Generation: running.runGeneration,
		Status:     running.Status,
	}
	running.mu.Unlock()
	r.mu.Unlock()

	var lifecycleErr error
	if recorder != nil {
		if err := recorder.RecordAgentLifecycle(
			context.Background(),
			lifecycle,
		); err != nil {
			lifecycleErr = fmt.Errorf(
				"agent_runner: publish backgrounded lifecycle: %w",
				err,
			)
		}
	}

	var (
		cancel       context.CancelFunc
		parentResult *agentForegroundWaitOutcome
	)
	running.mu.Lock()
	if lifecycleErr != nil {
		switch {
		case wait.pending != nil:
			wait.state = agentForegroundWaitSettled
			parentResult = wait.pending
			wait.pending = nil
		case wait.parentCancelPending:
			wait.state = agentForegroundWaitParentCanceled
			cancel = running.cancel
		default:
			wait.state = agentForegroundWaitActive
		}
	} else {
		wait.state = agentForegroundWaitBackgrounded
		wait.parentCancelPending = false
		wait.parentErr = nil
		wait.pending = nil
		parentResult = &agentForegroundWaitOutcome{
			result: &AgentExecResult{
				Outcome:    result.Outcome,
				AgentID:    result.AgentID,
				SessionID:  result.SessionID,
				ThreadID:   result.ThreadID,
				Generation: result.Generation,
			},
		}
	}
	running.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if parentResult != nil {
		wait.done <- *parentResult
	}
	if lifecycleErr != nil {
		return AgentDetachResult{}, lifecycleErr
	}
	return result, nil
}

func (r *AgentRunner) cancelForegroundWait(
	running *RunningAgent,
	generation int64,
	parentErr error,
) {
	if running == nil {
		return
	}
	running.mu.Lock()
	wait := running.foregroundWait
	if running.runGeneration != generation ||
		wait == nil ||
		wait.generation != generation ||
		running.Status != "running" {
		running.mu.Unlock()
		return
	}
	if wait.state == agentForegroundWaitDetaching {
		wait.parentCancelPending = true
		wait.parentErr = parentErr
		running.mu.Unlock()
		return
	}
	if wait.state != agentForegroundWaitActive {
		running.mu.Unlock()
		return
	}
	wait.state = agentForegroundWaitParentCanceled
	wait.parentErr = parentErr
	cancel := running.cancel
	running.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *AgentRunner) settleForegroundWait(
	running *RunningAgent,
	generation int64,
	result *AgentExecResult,
	err error,
) {
	if running == nil {
		return
	}
	running.mu.Lock()
	defer running.mu.Unlock()
	wait := running.foregroundWait
	if running.runGeneration != generation ||
		wait == nil ||
		wait.generation != generation {
		return
	}
	if result != nil {
		result.Outcome = AgentExecOutcomeCompleted
		result.AgentID = running.ID
		result.SessionID = running.SessionID
		result.ThreadID = running.ThreadID
		result.Generation = generation
	}
	if wait.state == agentForegroundWaitParentCanceled &&
		wait.parentErr != nil &&
		errors.Is(err, context.Canceled) {
		err = fmt.Errorf(
			"agent_runner: sub-agent execution failed: %w",
			wait.parentErr,
		)
	}
	switch wait.state {
	case agentForegroundWaitActive, agentForegroundWaitParentCanceled:
	case agentForegroundWaitDetaching:
		wait.pending = &agentForegroundWaitOutcome{
			result: result,
			err:    err,
		}
		return
	default:
		return
	}
	wait.state = agentForegroundWaitSettled
	wait.done <- agentForegroundWaitOutcome{result: result, err: err}
}

func (r *AgentRunner) finishAgentLaunchReservation(agentID string) {
	r.mu.Lock()
	delete(r.launchingAgents, agentID)
	r.mu.Unlock()
}

func cleanupPreparedAgentWorktree(
	lifecycle AgentWorktreeLifecycle,
	record worktree.Record,
	owner worktree.Owner,
) error {
	if lifecycle == nil || record.ID == "" || record.State != worktree.StateReady {
		return nil
	}
	_, err := lifecycle.Remove(context.Background(), record.ID, owner)
	if err == nil {
		return nil
	}
	return fmt.Errorf("agent_runner: cleanup unlaunched worktree: %w", err)
}

func persistPredispatchAgentFailure(
	running *RunningAgent,
	launchErr error,
) error {
	if running == nil {
		return nil
	}
	running.Status = "failed"
	running.predispatch = true
	running.Error = launchErr
	running.CompletedAt = time.Now()
	running.TerminalSequence++
	running.Completion = buildAgentCompletionRecordLocked(running)
	running.terminalDurable = false
	if err := running.persistDurableState(); err != nil {
		running.Completion = nil
		return fmt.Errorf(
			"agent_runner: persist pre-dispatch terminal metadata: %w",
			err,
		)
	}
	running.terminalDurable = true
	return nil
}

func persistLinkedPredispatchAgentFailure(
	running *RunningAgent,
	launchErr error,
) error {
	if running == nil || running.Options.WorkItem == nil {
		return nil
	}
	return persistPredispatchAgentFailure(running, launchErr)
}

func (r *AgentRunner) prepareAgentLaunch(ctx context.Context, executor AgentExecutor, running *RunningAgent) error {
	if running == nil {
		return fmt.Errorf("agent_runner: cannot prepare nil agent launch")
	}
	phase := "launched"
	if running.runGeneration > 1 {
		phase = "resumed"
	}
	launch := r.agentLaunchSnapshotLocked(running, phase, "")
	recorder, recordsLaunch := executor.(AgentLaunchRecorder)
	if linkAdmission, ok := executor.(AgentExecutionLinkAdmissionRecorder); ok &&
		running.Options.WorkItem != nil {
		if err := linkAdmission.RecordAgentExecutionLinkAdmission(
			ctx,
			cloneAgentExecOptions(running.Options),
		); err != nil {
			return fmt.Errorf(
				"agent_runner: record execution link admission: %w",
				err,
			)
		}
	}
	if recordsLaunch {
		if err := recorder.RecordAgentLaunch(ctx, launch); err != nil {
			return fmt.Errorf("agent_runner: record launch metadata: %w", err)
		}
	}
	if admission, ok := executor.(AgentExecutionAdmissionRecorder); ok {
		if err := admission.RecordAgentExecutionAdmission(
			ctx,
			cloneAgentExecOptions(running.Options),
			cloneSchemaMessages(running.Messages),
		); err != nil {
			if recordsLaunch {
				failed := r.agentLaunchSnapshotLocked(
					running,
					"launch_failed",
					err.Error(),
				)
				failed.Status = "failed"
				_ = recorder.RecordAgentLaunch(ctx, failed)
			}
			return fmt.Errorf(
				"agent_runner: record execution admission: %w",
				err,
			)
		}
	}
	if err := running.persistDurableState(); err != nil {
		if recordsLaunch {
			failed := r.agentLaunchSnapshotLocked(running, "launch_failed", err.Error())
			failed.Status = "failed"
			_ = recorder.RecordAgentLaunch(ctx, failed)
		}
		return fmt.Errorf("agent_runner: persist launch metadata: %w", err)
	}
	return nil
}

func (r *AgentRunner) agentLaunchSnapshotLocked(running *RunningAgent, phase, errorText string) AgentLaunchSnapshot {
	transcriptPath := ""
	if running != nil {
		transcriptPath = running.TranscriptPath
		if transcriptPath == "" {
			sessionID := firstNonEmpty(running.SessionID, running.ID)
			transcriptPath = transcript.NewRecorder(sessionID, r.agentTranscriptDirLocked()).Path()
		}
	}
	return AgentLaunchSnapshot{
		Phase:           phase,
		AgentID:         running.ID,
		SessionID:       running.SessionID,
		ThreadID:        running.ThreadID,
		ParentSessionID: running.ParentSessionID,
		ParentThreadID:  running.ParentThreadID,
		ParentAgentID:   running.ParentAgentID,
		ParentToolUseID: running.ToolUseID,
		Name:            running.Name,
		Task:            running.Task,
		Description:     running.Description,
		AgentType:       running.Options.SubagentType,
		Model:           running.Options.Model,
		PermissionMode:  firstNonEmpty(running.Options.Mode, running.Options.InheritedPermissionMode),
		Isolation:       running.Options.Isolation,
		CWD:             running.Options.CWD,
		WorktreePath:    running.WorktreePath,
		WorktreeBranch:  running.WorktreeBranch,
		Worktree:        cloneAgentWorktreeResult(running.Worktree),
		TranscriptPath:  transcriptPath,
		OutputFile:      running.OutputFile,
		Status:          running.Status,
		Error:           errorText,
		Generation:      running.runGeneration,
		StartedAt:       running.StartedAt,
	}
}

func finishAgentExecution(agentCtx context.Context, running *RunningAgent, generation int64, cancel context.CancelFunc, result *AgentExecResult, err error) (*AgentExecResult, error) {
	if runner := AgentRunnerFromCtx(agentCtx); runner != nil {
		defer runner.executionWG.Done()
	}
	running.mu.Lock()
	if generation != running.runGeneration {
		running.mu.Unlock()
		cancel()
		if err != nil {
			return nil, fmt.Errorf("agent_runner: sub-agent execution failed: %w", err)
		}
		return result, nil
	}
	wasAborted := running.Status == "aborted"
	running.CompletedAt = time.Now()
	if wasAborted && err == nil {
		err = context.Canceled
	}
	if err != nil {
		running.Status = "failed"
		running.Error = err
		// Check if it was a context cancellation (abort).
		if agentCtx.Err() != nil || wasAborted {
			running.Status = "aborted"
		}
	} else {
		if result == nil {
			err = fmt.Errorf("agent_runner: sub-agent returned nil result")
			running.Status = "failed"
			running.Error = err
		} else if writeErr := running.persistOutput(result.Result); writeErr != nil {
			err = writeErr
			running.Status = "failed"
			running.Error = writeErr
		} else {
			running.Status = "completed"
			running.Result = result.Result
			mergeResultProgress(&running.Progress, result)
			if len(result.Messages) > 0 {
				running.Messages = cloneSchemaMessages(result.Messages)
			} else {
				// Record the final assistant message in conversation history.
				running.Messages = append(running.Messages, &schema.Message{
					Role:    schema.Assistant,
					Content: result.Result,
				})
			}
		}
	}
	if worktreeErr := finalizeAgentWorktreeLocked(
		agentCtx,
		running,
		result,
	); worktreeErr != nil {
		if err == nil {
			err = worktreeErr
		} else {
			err = errors.Join(err, worktreeErr)
		}
		running.Error = err
		if running.Status != "aborted" {
			running.Status = "failed"
		}
	}
	running.TerminalSequence++
	running.Completion = buildAgentCompletionRecordLocked(running)
	running.terminalDurable = false
	if persistErr := running.persistDurableState(); persistErr != nil {
		if err == nil {
			err = persistErr
		} else {
			err = errors.Join(err, persistErr)
		}
		running.Error = err
		if running.Status != "aborted" {
			running.Status = "failed"
		}
		running.Completion = nil
	} else {
		running.terminalDurable = true
	}
	running.mu.Unlock()

	// Release the cancel func (no longer needed).
	cancel()
	if runner := AgentRunnerFromCtx(agentCtx); runner != nil {
		runner.unregisterAgentSteeringGeneration(running.ID, generation)
	}

	if err != nil {
		return nil, fmt.Errorf("agent_runner: sub-agent execution failed: %w", err)
	}
	return result, nil
}

func finalizeAgentWorktreeLocked(
	ctx context.Context,
	running *RunningAgent,
	result *AgentExecResult,
) error {
	if running == nil ||
		running.worktreeLifecycle == nil ||
		running.Worktree == nil ||
		running.Worktree.RecordID == "" {
		return nil
	}
	record, removeErr := running.worktreeLifecycle.Remove(
		ctx,
		running.Worktree.RecordID,
		running.worktreeOwner,
	)
	if record.ID == "" {
		if removeErr == nil {
			removeErr = errors.New("worktree lifecycle returned an empty record")
		}
		return fmt.Errorf(
			"agent_runner: finalize worktree %q: %w",
			running.Worktree.RecordID,
			removeErr,
		)
	}
	worktreeResult := agentWorktreeResultFromRecord(record)
	running.Worktree = cloneAgentWorktreeResult(worktreeResult)
	switch record.State {
	case worktree.StateRemoved:
		running.WorktreePath = ""
		running.WorktreeBranch = ""
	case worktree.StateRetained, worktree.StateCleanupFailed:
		running.WorktreePath = record.Path
		running.WorktreeBranch = record.Branch
	default:
		running.WorktreePath = record.Path
		running.WorktreeBranch = record.Branch
		if removeErr != nil {
			return fmt.Errorf(
				"agent_runner: finalize worktree %q in state %s: %w",
				record.ID,
				record.State,
				removeErr,
			)
		}
		return fmt.Errorf(
			"agent_runner: finalize worktree %q returned state %s",
			record.ID,
			record.State,
		)
	}
	if result != nil {
		result.Worktree = cloneAgentWorktreeResult(worktreeResult)
		result.WorktreePath = running.WorktreePath
		result.WorktreeBranch = running.WorktreeBranch
	}
	// Dirty, cancelled, unknown, and cleanup-failed records are successful
	// fail-closed handoffs. The durable state and LastError remain observable.
	return nil
}

// UpdateAgentProgress updates the pollable progress state for a running agent.
// It preserves an existing summary when the caller only provides activity/counts,
// matching the reference behavior that background summaries are not clobbered by
// assistant-message progress updates.
func (r *AgentRunner) UpdateAgentProgress(id string, progress AgentProgress) error {
	agent, err := r.lookupAgent(id)
	if err != nil {
		return err
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if agent.Status != "running" {
		return fmt.Errorf("agent_runner: agent %q is not running (status: %s)", id, agent.Status)
	}
	if agent.Progress.Summary != "" {
		progress.Summary = agent.Progress.Summary
	}
	agent.Progress = NormalizeAgentProgress(progress)
	return nil
}

// PendingAgentNotifications returns terminal lifecycle notifications that have
// not been acknowledged. It does not mutate runner state; the runtime input
// coordinator must durably accept the batch before acknowledgment.
func (r *AgentRunner) PendingAgentNotifications() []AgentNotification {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	agents := make([]*RunningAgent, 0, len(r.activeAgents))
	for _, agent := range r.activeAgents {
		agents = append(agents, agent)
	}
	r.mu.RUnlock()

	notifications := make([]AgentNotification, 0)
	for _, agent := range agents {
		agent.mu.Lock()
		if !isTerminalAgentStatus(agent.Status) ||
			!agent.terminalDurable ||
			agent.Notified {
			agent.mu.Unlock()
			continue
		}
		notification := buildAgentNotificationLocked(agent)
		agent.mu.Unlock()
		notifications = append(notifications, notification)
	}
	return notifications
}

// AgentExecutionSettlements resolves exact generations conservatively. Missing
// or corrupt durable state is unresolved; a later durable generation proves
// every earlier generation settled because continuation is admitted only from
// a durably terminal predecessor.
func (r *AgentRunner) AgentExecutionSettlements(
	keys []AgentExecutionKey,
) []AgentExecutionSettlement {
	results := make([]AgentExecutionSettlement, len(keys))
	if r == nil {
		for i, key := range keys {
			results[i] = AgentExecutionSettlement{
				Key:   key,
				State: "runner_unavailable",
			}
		}
		return results
	}

	r.mu.RLock()
	reservations := make(map[string]int64, len(r.launchingAgents))
	for agentID, reservation := range r.launchingAgents {
		reservations[agentID] = reservation.Generation
	}
	active := make(map[string]*RunningAgent, len(r.activeAgents))
	for agentID, agent := range r.activeAgents {
		active[agentID] = agent
	}
	r.mu.RUnlock()

	for i, key := range keys {
		result := AgentExecutionSettlement{Key: key}
		if strings.TrimSpace(key.AgentID) == "" || key.Generation <= 0 {
			result.State = "invalid"
			results[i] = result
			continue
		}
		if reservations[key.AgentID] == key.Generation {
			result.State = "reserved"
			results[i] = result
			continue
		}
		if agent := active[key.AgentID]; agent != nil {
			agent.mu.Lock()
			generation := agent.runGeneration
			status := agent.Status
			terminalDurable := agent.terminalDurable
			agent.mu.Unlock()
			switch {
			case generation > key.Generation:
				result.State = "superseded"
				result.Settled = true
			case generation == key.Generation &&
				isTerminalAgentStatus(status) &&
				terminalDurable:
				result.State = "terminal_durable"
				result.Settled = true
			case generation == key.Generation &&
				status == "aborted":
				result.State = "cancel_pending"
			case generation == key.Generation:
				result.State = "live"
			default:
				result.State = "future"
			}
			results[i] = result
			continue
		}

		snapshot, err := r.LoadPersistedAgentSnapshot(key.AgentID)
		if err != nil {
			result.State = "durable_state_unavailable"
			results[i] = result
			continue
		}
		switch {
		case snapshot.runGeneration > key.Generation:
			result.State = "superseded"
			result.Settled = true
		case snapshot.runGeneration == key.Generation &&
			isTerminalAgentStatus(snapshot.Status) &&
			snapshot.terminalDurable:
			result.State = "terminal_durable"
			result.Settled = true
		case snapshot.runGeneration == key.Generation &&
			snapshot.Status == "aborted":
			result.State = "cancel_pending"
		case snapshot.runGeneration == key.Generation:
			result.State = "not_terminal"
		default:
			result.State = "future"
		}
		results[i] = result
	}
	return results
}

// SessionExecutionsSettled proves that one parent Session has no reservation
// or non-durable child generation that can write after deletion admission.
func (r *AgentRunner) SessionExecutionsSettled(
	parentSessionID string,
) error {
	if r == nil {
		return nil
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" {
		return fmt.Errorf(
			"agent_runner: Session deletion requires parent Session ID",
		)
	}
	r.mu.RLock()
	reservations := make([]agentLaunchReservation, 0, len(r.launchingAgents))
	for _, reservation := range r.launchingAgents {
		if reservation != nil {
			reservations = append(reservations, *reservation)
		}
	}
	agents := make([]*RunningAgent, 0, len(r.activeAgents))
	for _, agent := range r.activeAgents {
		agents = append(agents, agent)
	}
	r.mu.RUnlock()
	for _, reservation := range reservations {
		if reservation.ParentSessionID == parentSessionID {
			return fmt.Errorf(
				"agent_runner: Session %q has reserved generation",
				parentSessionID,
			)
		}
	}
	for _, agent := range agents {
		agent.mu.Lock()
		owned := agent.ParentSessionID == parentSessionID
		settled := isTerminalAgentStatus(agent.Status) &&
			agent.terminalDurable
		agent.mu.Unlock()
		if owned && !settled {
			return fmt.Errorf(
				"agent_runner: Session %q has unsettled Agent generation",
				parentSessionID,
			)
		}
	}
	return nil
}

// PendingAgentNotificationsForParent reconstructs every durably terminal child
// owned by one exact parent runtime. Notified remains only a process-local
// polling cache: the parent transcript receipt and runtime-input coordinator
// decide whether the at-least-once candidate still needs projection.
func (r *AgentRunner) PendingAgentNotificationsForParent(
	parentSessionID string,
	parentThreadID string,
	parentAgentID string,
) ([]AgentNotification, error) {
	if r == nil {
		return nil, nil
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	parentThreadID = strings.TrimSpace(parentThreadID)
	parentAgentID = strings.TrimSpace(parentAgentID)

	r.mu.RLock()
	active := make([]*RunningAgent, 0, len(r.activeAgents))
	for _, agent := range r.activeAgents {
		active = append(active, agent)
	}
	evicted := make([]string, 0, len(r.evictedAgents))
	for agentID := range r.evictedAgents {
		evicted = append(evicted, agentID)
	}
	r.mu.RUnlock()
	sort.Strings(evicted)

	notifications := make([]AgentNotification, 0, len(active)+len(evicted))
	seen := make(map[string]struct{}, len(active)+len(evicted))
	for _, agent := range active {
		agent.mu.Lock()
		owned := agentOwnedByParent(
			agent,
			parentSessionID,
			parentThreadID,
			parentAgentID,
		)
		if !owned || !isTerminalAgentStatus(agent.Status) || !agent.terminalDurable {
			agent.mu.Unlock()
			continue
		}
		notification := buildAgentNotificationLocked(agent)
		agent.mu.Unlock()
		notifications = append(notifications, notification)
		seen[notification.DeliveryID()] = struct{}{}
	}
	for _, agentID := range evicted {
		snapshot, err := r.LoadPersistedAgentSnapshot(agentID)
		if err != nil {
			return nil, err
		}
		if !agentOwnedByParent(
			&snapshot,
			parentSessionID,
			parentThreadID,
			parentAgentID,
		) || !isTerminalAgentStatus(snapshot.Status) || !snapshot.terminalDurable {
			continue
		}
		notification := buildAgentNotificationLocked(&snapshot)
		if _, duplicate := seen[notification.DeliveryID()]; duplicate {
			continue
		}
		notifications = append(notifications, notification)
		seen[notification.DeliveryID()] = struct{}{}
	}
	sort.SliceStable(notifications, func(i, j int) bool {
		if notifications[i].CreatedAt.Equal(notifications[j].CreatedAt) {
			return notifications[i].DeliveryID() < notifications[j].DeliveryID()
		}
		return notifications[i].CreatedAt.Before(notifications[j].CreatedAt)
	})
	return notifications, nil
}

func agentOwnedByParent(
	agent *RunningAgent,
	parentSessionID string,
	parentThreadID string,
	parentAgentID string,
) bool {
	return agent != nil &&
		strings.TrimSpace(agent.ParentSessionID) == parentSessionID &&
		strings.TrimSpace(agent.ParentThreadID) == parentThreadID &&
		strings.TrimSpace(agent.ParentAgentID) == parentAgentID
}

// AcknowledgeAgentNotifications marks only the generations already accepted by
// the runtime input coordinator.
func (r *AgentRunner) AcknowledgeAgentNotifications(ids []string) {
	if r == nil || len(ids) == 0 {
		return
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			set[id] = struct{}{}
		}
	}
	r.mu.RLock()
	agents := make([]*RunningAgent, 0, len(r.activeAgents))
	for _, agent := range r.activeAgents {
		agents = append(agents, agent)
	}
	r.mu.RUnlock()
	for _, agent := range agents {
		agent.mu.Lock()
		if !isTerminalAgentStatus(agent.Status) {
			agent.mu.Unlock()
			continue
		}
		notification := buildAgentNotificationLocked(agent)
		if _, ok := set[notification.DeliveryID()]; ok {
			agent.Notified = true
		}
		agent.mu.Unlock()
	}
}

// PollAgentNotifications retains the compatibility polling contract while
// delegating to the two-phase pending/ack boundary.
func (r *AgentRunner) PollAgentNotifications() []AgentNotification {
	notifications := r.PendingAgentNotifications()
	ids := make([]string, 0, len(notifications))
	for _, notification := range notifications {
		ids = append(ids, notification.DeliveryID())
	}
	r.AcknowledgeAgentNotifications(ids)
	return notifications
}

// PollAgentProgress returns non-terminal reference-shaped progress snapshots
// for running agents. Unlike terminal notification polling, this is read-only:
// it never marks an agent as notified and never consumes terminal delivery.
func (r *AgentRunner) PollAgentProgress() []AgentProgressEvent {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	agents := make([]*RunningAgent, 0, len(r.activeAgents))
	for _, agent := range r.activeAgents {
		agents = append(agents, agent)
	}
	r.mu.RUnlock()

	type progressEntry struct {
		started time.Time
		id      string
		event   AgentProgressEvent
	}
	now := time.Now()
	entries := make([]progressEntry, 0, len(agents))
	for _, agent := range agents {
		agent.mu.Lock()
		if agent.Status != "running" || !hasAgentProgressLocked(agent.Progress) {
			agent.mu.Unlock()
			continue
		}
		entry := progressEntry{
			started: agent.StartedAt,
			id:      agent.ID,
			event:   buildAgentProgressEventLocked(agent, now),
		}
		agent.mu.Unlock()
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].started.Equal(entries[j].started) {
			return entries[i].id < entries[j].id
		}
		return entries[i].started.Before(entries[j].started)
	})

	progress := make([]AgentProgressEvent, 0, len(entries))
	for _, entry := range entries {
		progress = append(progress, entry.event)
	}
	return progress
}

// QueueAgentMessage queues a SendMessage payload for a running background agent,
// resolved either by ID or by addressable name.
func (r *AgentRunner) QueueAgentMessage(recipient string, msg MessagePayload) (string, error) {
	if !r.lifecycleAdmitted() {
		return "", fmt.Errorf(
			"agent_runner: active Session deletion blocks send",
		)
	}
	agent, err := r.lookupAgent(recipient)
	if err != nil {
		return "", err
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if agent.Status != "running" {
		return "", fmt.Errorf("agent_runner: agent %q is not running (status: %s)", recipient, agent.Status)
	}
	msg = normalizeAgentMessagePayload(msg, agent.ID)
	agent.PendingMessages = append(agent.PendingMessages, msg)
	agent.PendingMessageCount = len(agent.PendingMessages)
	return agent.ID, nil
}

// QueueAgentMessageGeneration queues to one exact live or continuing
// generation. It never resolves names or falls back to the current AgentID.
func (r *AgentRunner) QueueAgentMessageGeneration(
	agentID string,
	generation int64,
	msg MessagePayload,
) (string, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || generation <= 0 {
		return "", fmt.Errorf(
			"agent_runner: exact send requires Agent ID and positive generation",
		)
	}
	r.mu.Lock()
	if !r.lifecycleAdmittedLocked() {
		r.mu.Unlock()
		return "", fmt.Errorf(
			"agent_runner: active Session deletion blocks exact send",
		)
	}
	if reservation := r.launchingAgents[agentID]; reservation != nil {
		if reservation.Generation != generation {
			r.mu.Unlock()
			return "", fmt.Errorf(
				"agent_runner: exact send generation mismatch: want %s/%d, reserved %d",
				agentID,
				generation,
				reservation.Generation,
			)
		}
		msg = normalizeAgentMessagePayload(msg, agentID)
		if rememberAcceptedAgentMessage(
			&reservation.acceptedMessageIDs,
			msg,
		) {
			reservation.PendingMessages = append(
				reservation.PendingMessages,
				msg,
			)
		}
		r.mu.Unlock()
		return agentID, nil
	}
	agent := r.activeAgents[agentID]
	if agent == nil {
		r.mu.Unlock()
		return "", fmt.Errorf(
			"agent_runner: exact send target %s/%d is not live",
			agentID,
			generation,
		)
	}
	agent.mu.Lock()
	r.mu.Unlock()
	defer agent.mu.Unlock()
	if agent.runGeneration != generation {
		return "", fmt.Errorf(
			"agent_runner: exact send generation mismatch: want %s/%d, got %d",
			agentID,
			generation,
			agent.runGeneration,
		)
	}
	if agent.Status != "running" {
		return "", fmt.Errorf(
			"agent_runner: exact send target %s/%d is not running (status: %s)",
			agentID,
			generation,
			agent.Status,
		)
	}
	msg = normalizeAgentMessagePayload(msg, agentID)
	if rememberAcceptedAgentMessage(&agent.acceptedMessageIDs, msg) {
		agent.PendingMessages = append(agent.PendingMessages, msg)
	}
	agent.PendingMessageCount = len(agent.PendingMessages)
	return agentID, nil
}

// HasPendingAgentMessageGeneration reports whether one exact live or
// continuing generation currently owns queued child input.
func (r *AgentRunner) HasPendingAgentMessageGeneration(
	agentID string,
	generation int64,
) bool {
	if r == nil {
		return false
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || generation <= 0 {
		return false
	}
	r.mu.RLock()
	if reservation := r.launchingAgents[agentID]; reservation != nil {
		pending := reservation.Generation == generation &&
			len(reservation.PendingMessages) > 0
		r.mu.RUnlock()
		return pending
	}
	agent := r.activeAgents[agentID]
	if agent == nil {
		r.mu.RUnlock()
		return false
	}
	agent.mu.Lock()
	r.mu.RUnlock()
	defer agent.mu.Unlock()
	return agent.runGeneration == generation &&
		len(agent.PendingMessages) > 0
}

// SendOrResumeAgentMessage queues a SendMessage payload to a running agent or
// auto-resumes a stopped local agent with the message as its next prompt. When
// a known local agent has been evicted from memory, the runner reconstructs the
// bounded resume boundary from persisted agent metadata and transcript history.
func (r *AgentRunner) SendOrResumeAgentMessage(recipient string, msg MessagePayload) (string, string, error) {
	return r.sendOrResumeAgentMessage(recipient, msg, nil)
}

// ContinueAgentGeneration starts exactly the successor of one durably terminal
// generation. Unlike SendOrResumeAgentMessage it never degrades into a send.
func (r *AgentRunner) ContinueAgentGeneration(
	agentID string,
	generation int64,
	msg MessagePayload,
) (string, string, error) {
	if strings.TrimSpace(agentID) == "" || generation <= 0 {
		return "", "", fmt.Errorf(
			"agent_runner: exact continuation requires Agent ID and positive generation",
		)
	}
	return r.sendOrResumeAgentMessage(agentID, msg, &generation)
}

func (r *AgentRunner) sendOrResumeAgentMessage(
	recipient string,
	msg MessagePayload,
	expectedGeneration *int64,
) (string, string, error) {
	agent, evictedID, err := r.lookupAgentForResume(recipient)
	if err != nil {
		return "", "", err
	}
	if evictedID != "" {
		return r.resumeEvictedAgentMessage(
			recipient,
			evictedID,
			msg,
			expectedGeneration,
		)
	}
	if r.afterResumeLookupForTest != nil {
		r.afterResumeLookupForTest()
	}
	r.mu.RLock()
	recoveryExecutor := r.executor
	recoveryExecutorGeneration := r.executorGeneration
	r.mu.RUnlock()
	agent.mu.Lock()
	if wait := agent.foregroundWait; wait != nil &&
		wait.state == agentForegroundWaitDetaching {
		agent.mu.Unlock()
		return "", "", fmt.Errorf(
			"agent_runner: agent %q detach is still in progress",
			recipient,
		)
	}
	needsWorktreeRecovery := isTerminalAgentStatus(agent.Status) &&
		strings.TrimSpace(agent.Options.Isolation) == "worktree"
	recoveryInput := agentWorktreeRecoveryInputFromAgent(agent)
	agent.mu.Unlock()
	var recoveredWorktree *recoveredAgentWorktree
	if needsWorktreeRecovery {
		recoveredWorktree, err = recoverAgentWorktreeContinuation(
			context.Background(),
			recoveryExecutor,
			recoveryInput,
		)
		if err != nil {
			return "", "", fmt.Errorf(
				"agent_runner: recover worktree-isolated agent %q: %w",
				recipient,
				err,
			)
		}
	}
	r.mu.Lock()
	agent.mu.Lock()
	if r.closed || !r.lifecycleAdmittedLocked() {
		agent.mu.Unlock()
		r.mu.Unlock()
		return "", "", fmt.Errorf(
			"agent_runner: runner lifecycle blocks continuation",
		)
	}
	status := agent.Status
	agentID := agent.ID
	if expectedGeneration != nil &&
		(recipient != agentID || agent.runGeneration != *expectedGeneration) {
		actualGeneration := agent.runGeneration
		agent.mu.Unlock()
		r.mu.Unlock()
		return "", "", fmt.Errorf(
			"agent_runner: exact continuation generation mismatch: want %s/%d, got %s/%d",
			recipient,
			*expectedGeneration,
			agentID,
			actualGeneration,
		)
	}
	if wait := agent.foregroundWait; wait != nil &&
		wait.state == agentForegroundWaitDetaching {
		agent.mu.Unlock()
		r.mu.Unlock()
		return "", "", fmt.Errorf(
			"agent_runner: agent %q detach is still in progress",
			recipient,
		)
	}
	msg = normalizeAgentMessagePayload(msg, agentID)
	if registered, ok := r.activeAgents[agentID]; !ok || registered == agent {
		r.activeAgents[agentID] = agent
		delete(r.evictedAgents, agentID)
		if agent.Name != "" {
			r.nameRegistry[agent.Name] = agentID
		}
	}
	executor := r.executor
	activeCount := r.activeCountLockedWithAgentStatus(agent, status)
	if status == "running" {
		if expectedGeneration != nil {
			agent.mu.Unlock()
			r.mu.Unlock()
			return "", "", fmt.Errorf(
				"agent_runner: exact continuation requires terminal generation %s/%d",
				agentID,
				*expectedGeneration,
			)
		}
		agent.PendingMessages = append(agent.PendingMessages, msg)
		agent.PendingMessageCount = len(agent.PendingMessages)
		agent.mu.Unlock()
		r.mu.Unlock()
		return agentID, "queued", nil
	}
	if !isTerminalAgentStatus(status) {
		agent.mu.Unlock()
		r.mu.Unlock()
		return "", "", fmt.Errorf("agent_runner: agent %q is not running (status: %s)", recipient, status)
	}
	if strings.TrimSpace(agent.Options.Isolation) == "worktree" &&
		recoveredWorktree == nil {
		agent.mu.Unlock()
		r.mu.Unlock()
		return "", "", fmt.Errorf(
			"agent_runner: worktree-isolated agent %q requires durable recovery admission before continuation",
			recipient,
		)
	}
	if recoveredWorktree != nil &&
		r.executorGeneration != recoveryExecutorGeneration {
		agent.mu.Unlock()
		r.mu.Unlock()
		return "", "", errors.New(
			"agent_runner: worktree recovery binding changed before continuation",
		)
	}
	if strings.TrimSpace(msg.Content) == "" {
		agent.mu.Unlock()
		r.mu.Unlock()
		return "", "", fmt.Errorf("agent_runner: cannot resume agent %q with empty message", recipient)
	}
	if executor == nil {
		agent.mu.Unlock()
		r.mu.Unlock()
		return "", "", fmt.Errorf("agent_runner: no executor registered")
	}
	if reservation, reserved := r.launchingAgents[agentID]; reserved {
		if expectedGeneration != nil {
			agent.mu.Unlock()
			r.mu.Unlock()
			return "", "", fmt.Errorf(
				"agent_runner: exact continuation %s/%d is already reserved as generation %d",
				agentID,
				*expectedGeneration,
				reservation.Generation,
			)
		}
		reservation.PendingMessages = append(
			reservation.PendingMessages,
			normalizeAgentMessagePayload(msg, agentID),
		)
		agent.mu.Unlock()
		r.mu.Unlock()
		return agentID, "queued", nil
	}
	if activeCount+len(r.launchingAgents) >= r.maxAgents {
		agent.mu.Unlock()
		r.mu.Unlock()
		return "", "", fmt.Errorf("agent_runner: max concurrent agents reached (%d)", r.maxAgents)
	}
	resumeMessages := cloneSchemaMessages(agent.Messages)
	resumeOpts := cloneAgentExecOptions(agent.Options)
	if resumeOpts.AgentID == "" {
		resumeOpts.AgentID = agentID
	}
	if resumeOpts.SessionID == "" {
		resumeOpts.SessionID = firstNonEmpty(agent.SessionID, agentID)
	}
	if resumeOpts.ThreadID == "" {
		resumeOpts.ThreadID = firstNonEmpty(agent.ThreadID, agentID)
	}
	resumeOpts.Task = msg.Content
	resumeOpts.MessageID = agentMessageCommandUUID(msg)
	resumeOpts.ResumeMessages = resumeMessages
	resumeOpts.executionMode = agentExecutionModeBackground
	if resumeOpts.Description == "" {
		resumeOpts.Description = agent.Description
	}
	if resumeOpts.Name == "" {
		resumeOpts.Name = agent.Name
	}
	proposed := *agent
	proposed.mu = &sync.Mutex{}
	if recoveredWorktree != nil {
		resumeOpts.CWD = recoveredWorktree.record.Path
		proposed.Worktree = agentWorktreeResultFromRecord(
			recoveredWorktree.record,
		)
		proposed.WorktreePath = recoveredWorktree.record.Path
		proposed.WorktreeBranch = recoveredWorktree.record.Branch
		proposed.worktreeLifecycle = recoveredWorktree.lifecycle
		proposed.worktreeOwner = recoveredWorktree.owner
	} else {
		proposed.Worktree = cloneAgentWorktreeResult(agent.Worktree)
	}
	agentCtx, cancel := context.WithCancel(WithAgentRunner(context.Background(), r))
	now := time.Now()
	if resumeOpts.WorkItem != nil {
		resumeOpts.WorkItem.AdmittedAt = now.UTC()
	}
	generation := agent.runGeneration + 1
	resumeOpts.Generation = generation
	proposed.Status = "running"
	proposed.SessionID = resumeOpts.SessionID
	proposed.ThreadID = resumeOpts.ThreadID
	proposed.ParentSessionID = resumeOpts.ParentSessionID
	proposed.ParentThreadID = resumeOpts.ParentThreadID
	proposed.ParentAgentID = resumeOpts.ParentAgentID
	proposed.Task = resumeOpts.Task
	proposed.Description = firstNonEmpty(
		resumeOpts.Description,
		agent.Description,
		resumeOpts.Task,
	)
	proposed.Name = resumeOpts.Name
	proposed.StartedAt = now
	proposed.CompletedAt = time.Time{}
	proposed.Result = ""
	proposed.Error = nil
	proposed.Notified = false
	proposed.Completion = nil
	proposed.terminalDurable = false
	proposed.completionLegacy = false
	proposed.PendingMessages = nil
	proposed.PendingMessageCount = 0
	proposed.acceptedMessageIDs = nil
	proposed.runGeneration = generation
	proposed.foregroundWait = nil
	proposed.Messages = append(cloneSchemaMessages(resumeMessages), &schema.Message{
		Role:    schema.User,
		Content: resumeOpts.Task,
		Extra:   agentMessageSchemaExtra(msg),
	})
	storedOpts := cloneAgentExecOptions(resumeOpts)
	storedOpts.ResumeMessages = nil
	proposed.Options = storedOpts
	proposed.cancel = cancel
	reservation := &agentLaunchReservation{
		Generation:      generation,
		ParentSessionID: resumeOpts.ParentSessionID,
	}
	r.launchingAgents[agentID] = reservation
	r.executionWG.Add(1)
	agent.mu.Unlock()
	r.mu.Unlock()

	if err := r.prepareAgentLaunch(agentCtx, executor, &proposed); err != nil {
		persistErr := persistLinkedPredispatchAgentFailure(&proposed, err)
		cancel()
		r.mu.Lock()
		agent.mu.Lock()
		if proposed.Options.WorkItem != nil &&
			r.launchingAgents[agentID] == reservation &&
			agent.runGeneration+1 == generation {
			proposed.PendingMessages = cloneMessagePayloads(
				reservation.PendingMessages,
			)
			proposed.PendingMessageCount = len(proposed.PendingMessages)
			proposed.acceptedMessageIDs = cloneStringSet(
				reservation.acceptedMessageIDs,
			)
			proposed.mu = agent.mu
			*agent = proposed
		}
		delete(r.launchingAgents, agentID)
		agent.mu.Unlock()
		r.mu.Unlock()
		r.executionWG.Done()
		return "", "", errors.Join(err, persistErr)
	}

	r.mu.Lock()
	agent.mu.Lock()
	if r.closed ||
		!r.lifecycleAdmittedLocked() ||
		r.launchingAgents[agentID] != reservation ||
		agent.runGeneration+1 != generation {
		delete(r.launchingAgents, agentID)
		agent.mu.Unlock()
		r.mu.Unlock()
		cancel()
		r.executionWG.Done()
		return "", "", fmt.Errorf(
			"agent_runner: agent %q continuation changed before dispatch",
			agentID,
		)
	}
	proposed.PendingMessages = cloneMessagePayloads(
		reservation.PendingMessages,
	)
	proposed.PendingMessageCount = len(proposed.PendingMessages)
	proposed.acceptedMessageIDs = cloneStringSet(
		reservation.acceptedMessageIDs,
	)
	proposed.mu = agent.mu
	*agent = proposed
	delete(r.launchingAgents, agentID)
	r.registerAgentSteeringLocked(agentID)
	agent.mu.Unlock()
	r.mu.Unlock()

	go func() {
		result, execErr := executor.ExecuteAgent(agentCtx, resumeOpts)
		_, _ = finishAgentExecution(agentCtx, agent, generation, cancel, result, execErr)
	}()
	return resumeOpts.AgentID, "resumed", nil
}

func (r *AgentRunner) resumeEvictedAgentMessage(
	recipient string,
	agentID string,
	msg MessagePayload,
	expectedGeneration *int64,
) (string, string, error) {
	if strings.TrimSpace(msg.Content) == "" {
		return "", "", fmt.Errorf("agent_runner: cannot resume evicted local agent %q (%s): empty message", recipient, agentID)
	}

	r.mu.Lock()
	if r.closed || !r.lifecycleAdmittedLocked() {
		r.mu.Unlock()
		return "", "", fmt.Errorf(
			"agent_runner: runner lifecycle blocks continuation",
		)
	}
	msg = normalizeAgentMessagePayload(msg, agentID)
	if agent, ok := r.activeAgents[agentID]; ok {
		agent.mu.Lock()
		if agent.Status == "running" {
			agent.PendingMessages = append(agent.PendingMessages, msg)
			agent.PendingMessageCount = len(agent.PendingMessages)
			agent.mu.Unlock()
			r.mu.Unlock()
			return agentID, "queued", nil
		}
		agent.mu.Unlock()
		r.mu.Unlock()
		return r.sendOrResumeAgentMessage(agentID, msg, expectedGeneration)
	}
	if reservation, reserved := r.launchingAgents[agentID]; reserved {
		if expectedGeneration != nil {
			r.mu.Unlock()
			return "", "", fmt.Errorf(
				"agent_runner: exact continuation %s/%d is already reserved as generation %d",
				agentID,
				*expectedGeneration,
				reservation.Generation,
			)
		}
		reservation.PendingMessages = append(
			reservation.PendingMessages,
			msg,
		)
		r.mu.Unlock()
		return agentID, "queued", nil
	}
	executor := r.executor
	executorGeneration := r.executorGeneration
	if executor == nil {
		r.mu.Unlock()
		return "", "", fmt.Errorf("agent_runner: no executor registered")
	}
	if r.activeCountLocked()+len(r.launchingAgents) >= r.maxAgents {
		r.mu.Unlock()
		return "", "", fmt.Errorf("agent_runner: max concurrent agents reached (%d)", r.maxAgents)
	}

	metadata, resumeMessages, err := r.loadEvictedAgentStateLocked(agentID)
	if err != nil {
		r.mu.Unlock()
		return "", "", fmt.Errorf("agent_runner: cannot resume evicted local agent %q (%s): %w", recipient, agentID, err)
	}
	if expectedGeneration != nil &&
		(recipient != agentID || metadata.Generation != *expectedGeneration) {
		r.mu.Unlock()
		return "", "", fmt.Errorf(
			"agent_runner: exact continuation generation mismatch: want %s/%d, got %s/%d",
			recipient,
			*expectedGeneration,
			agentID,
			metadata.Generation,
		)
	}
	needsWorktreeRecovery := strings.TrimSpace(
		firstNonEmpty(metadata.Options.Isolation, metadata.Isolation),
	) == "worktree"
	var recoveredWorktree *recoveredAgentWorktree
	if needsWorktreeRecovery {
		r.mu.Unlock()
		recoveredWorktree, err = recoverAgentWorktreeContinuation(
			context.Background(),
			executor,
			agentWorktreeRecoveryInputFromMetadata(agentID, metadata),
		)
		if err != nil {
			return "", "", fmt.Errorf(
				"agent_runner: recover evicted worktree-isolated agent %q: %w",
				recipient,
				err,
			)
		}
		r.mu.Lock()
		if r.closed || !r.lifecycleAdmittedLocked() {
			r.mu.Unlock()
			return "", "", fmt.Errorf(
				"agent_runner: runner lifecycle blocks continuation",
			)
		}
		if _, ok := r.activeAgents[agentID]; ok {
			r.mu.Unlock()
			return r.sendOrResumeAgentMessage(
				agentID,
				msg,
				expectedGeneration,
			)
		}
		if reservation, reserved := r.launchingAgents[agentID]; reserved {
			if expectedGeneration != nil {
				r.mu.Unlock()
				return "", "", fmt.Errorf(
					"agent_runner: exact continuation %s/%d is already reserved as generation %d",
					agentID,
					*expectedGeneration,
					reservation.Generation,
				)
			}
			reservation.PendingMessages = append(
				reservation.PendingMessages,
				msg,
			)
			r.mu.Unlock()
			return agentID, "queued", nil
		}
		if r.executorGeneration != executorGeneration {
			r.mu.Unlock()
			return "", "", errors.New(
				"agent_runner: worktree recovery binding changed before continuation",
			)
		}
		executor = r.executor
		if executor == nil {
			r.mu.Unlock()
			return "", "", fmt.Errorf("agent_runner: no executor registered")
		}
		if r.activeCountLocked()+len(r.launchingAgents) >= r.maxAgents {
			r.mu.Unlock()
			return "", "", fmt.Errorf(
				"agent_runner: max concurrent agents reached (%d)",
				r.maxAgents,
			)
		}
	}
	resumeOpts := cloneAgentExecOptions(metadata.Options)
	resumeOpts.AgentID = agentID
	resumeOpts.SessionID = firstNonEmpty(resumeOpts.SessionID, metadata.SessionID, agentID)
	resumeOpts.ThreadID = firstNonEmpty(resumeOpts.ThreadID, metadata.ThreadID, agentID)
	resumeOpts.ParentSessionID = firstNonEmpty(resumeOpts.ParentSessionID, metadata.ParentSessionID)
	resumeOpts.ParentThreadID = firstNonEmpty(resumeOpts.ParentThreadID, metadata.ParentThreadID)
	resumeOpts.ParentAgentID = firstNonEmpty(resumeOpts.ParentAgentID, metadata.ParentAgentID)
	resumeOpts.Task = msg.Content
	resumeOpts.MessageID = agentMessageCommandUUID(msg)
	resumeOpts.ResumeMessages = cloneSchemaMessages(resumeMessages)
	resumeOpts.executionMode = agentExecutionModeBackground
	if resumeOpts.Description == "" {
		resumeOpts.Description = metadata.Description
	}
	if resumeOpts.Name == "" {
		resumeOpts.Name = metadata.Name
	}
	if recoveredWorktree != nil {
		resumeOpts.CWD = recoveredWorktree.record.Path
		metadata.Worktree = agentWorktreeResultFromRecord(
			recoveredWorktree.record,
		)
		metadata.WorktreePath = recoveredWorktree.record.Path
		metadata.WorktreeBranch = recoveredWorktree.record.Branch
	}

	agentCtx, cancel := context.WithCancel(WithAgentRunner(context.Background(), r))
	now := time.Now()
	if resumeOpts.WorkItem != nil {
		resumeOpts.WorkItem.AdmittedAt = now.UTC()
	}
	generation := max(metadata.Generation+1, 1)
	resumeOpts.Generation = generation
	storedOpts := cloneAgentExecOptions(resumeOpts)
	storedOpts.ResumeMessages = nil
	running := &RunningAgent{
		ID:              agentID,
		SessionID:       resumeOpts.SessionID,
		ThreadID:        resumeOpts.ThreadID,
		ParentSessionID: resumeOpts.ParentSessionID,
		ParentThreadID:  resumeOpts.ParentThreadID,
		ParentAgentID:   resumeOpts.ParentAgentID,
		Type:            "local_agent",
		Task:            resumeOpts.Task,
		Description:     firstNonEmpty(resumeOpts.Description, metadata.Description, resumeOpts.Task),
		Name:            resumeOpts.Name,
		ToolUseID:       firstNonEmpty(resumeOpts.ToolUseID, metadata.ToolUseID),
		Status:          "running",
		Messages: append(cloneSchemaMessages(resumeMessages), &schema.Message{
			Role: schema.User, Content: resumeOpts.Task, Extra: agentMessageSchemaExtra(msg),
		}),
		StartedAt:         now,
		OutputFile:        firstNonEmpty(metadata.OutputFile, r.agentOutputPathLocked(agentID)),
		TranscriptPath:    firstNonEmpty(metadata.TranscriptPath, transcript.NewRecorder(resumeOpts.SessionID, r.agentTranscriptDirLocked()).Path()),
		Options:           storedOpts,
		WorktreePath:      metadata.WorktreePath,
		WorktreeBranch:    metadata.WorktreeBranch,
		Worktree:          cloneAgentWorktreeResult(metadata.Worktree),
		TerminalSequence:  metadata.TerminalSequence,
		worktreeLifecycle: recoveredLifecycle(recoveredWorktree),
		worktreeOwner:     recoveredOwner(recoveredWorktree),
		cancel:            cancel,
		runGeneration:     generation,
		mu:                &sync.Mutex{},
	}
	reservation := &agentLaunchReservation{
		Generation:      generation,
		ParentSessionID: resumeOpts.ParentSessionID,
	}
	r.launchingAgents[agentID] = reservation
	r.executionWG.Add(1)
	r.mu.Unlock()

	if err := r.prepareAgentLaunch(agentCtx, executor, running); err != nil {
		persistErr := persistLinkedPredispatchAgentFailure(running, err)
		cancel()
		r.mu.Lock()
		if running.Options.WorkItem != nil &&
			r.launchingAgents[agentID] == reservation {
			running.PendingMessages = cloneMessagePayloads(
				reservation.PendingMessages,
			)
			running.PendingMessageCount = len(running.PendingMessages)
			running.acceptedMessageIDs = cloneStringSet(
				reservation.acceptedMessageIDs,
			)
			r.activeAgents[agentID] = running
			delete(r.evictedAgents, agentID)
			if running.Name != "" {
				r.nameRegistry[running.Name] = agentID
			}
		}
		delete(r.launchingAgents, agentID)
		r.mu.Unlock()
		r.executionWG.Done()
		return "", "", errors.Join(err, persistErr)
	}

	r.mu.Lock()
	if r.closed ||
		!r.lifecycleAdmittedLocked() ||
		r.launchingAgents[agentID] != reservation {
		delete(r.launchingAgents, agentID)
		r.mu.Unlock()
		cancel()
		r.executionWG.Done()
		return "", "", fmt.Errorf(
			"agent_runner: agent %q continuation changed before dispatch",
			agentID,
		)
	}
	running.PendingMessages = cloneMessagePayloads(
		reservation.PendingMessages,
	)
	running.PendingMessageCount = len(running.PendingMessages)
	running.acceptedMessageIDs = cloneStringSet(
		reservation.acceptedMessageIDs,
	)
	r.activeAgents[agentID] = running
	delete(r.evictedAgents, agentID)
	delete(r.launchingAgents, agentID)
	if running.Name != "" {
		r.nameRegistry[running.Name] = agentID
	}
	r.registerAgentSteeringLocked(agentID)
	r.mu.Unlock()

	go func() {
		result, execErr := executor.ExecuteAgent(agentCtx, resumeOpts)
		_, _ = finishAgentExecution(agentCtx, running, generation, cancel, result, execErr)
	}()
	return agentID, "resumed", nil
}

// DrainAgentMessages atomically drains messages queued for a running agent.
// Sub-agent continuation can call this at tool-round boundaries to mirror the
// reference pendingMessages handoff.
func (r *AgentRunner) DrainAgentMessages(id string) ([]MessagePayload, error) {
	agent, err := r.lookupAgent(id)
	if err != nil {
		return nil, err
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	msgs := cloneMessagePayloads(agent.PendingMessages)
	agent.PendingMessages = nil
	agent.PendingMessageCount = 0
	return msgs, nil
}

// PendingAgentMessages returns detached messages without consuming the source
// outbox. Callers acknowledge only after the session coordinator durably
// accepts the same stable command IDs.
func (r *AgentRunner) PendingAgentMessages(id string) ([]MessagePayload, error) {
	agent, err := r.lookupAgent(id)
	if err != nil {
		return nil, err
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	return cloneMessagePayloads(agent.PendingMessages), nil
}

// AcknowledgeAgentMessages removes the accepted command IDs from one Agent
// outbox while preserving the order of every unacknowledged message.
func (r *AgentRunner) AcknowledgeAgentMessages(
	id string,
	commandUUIDs []string,
) (int, error) {
	agent, err := r.lookupAgent(id)
	if err != nil {
		return 0, err
	}
	set := make(map[string]struct{}, len(commandUUIDs))
	for _, commandUUID := range commandUUIDs {
		if commandUUID = strings.TrimSpace(commandUUID); commandUUID != "" {
			set[commandUUID] = struct{}{}
		}
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	remaining := make([]MessagePayload, 0, len(agent.PendingMessages))
	removed := 0
	for _, message := range agent.PendingMessages {
		if _, ok := set[agentMessageCommandUUID(message)]; ok {
			removed++
			continue
		}
		remaining = append(remaining, message)
	}
	agent.PendingMessages = remaining
	agent.PendingMessageCount = len(remaining)
	return removed, nil
}

// CancelAgentMessage removes one still-pending message by command UUID.
func (r *AgentRunner) CancelAgentMessage(recipient, commandUUID string) (bool, error) {
	if !r.lifecycleAdmitted() {
		return false, fmt.Errorf(
			"agent_runner: active Session deletion blocks input cancellation",
		)
	}
	agent, err := r.lookupAgent(recipient)
	if err != nil {
		return false, err
	}
	commandUUID = strings.TrimSpace(commandUUID)
	if commandUUID == "" {
		return false, nil
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	for i, message := range agent.PendingMessages {
		if agentMessageCommandUUID(message) != commandUUID {
			continue
		}
		agent.PendingMessages = append(agent.PendingMessages[:i], agent.PendingMessages[i+1:]...)
		agent.PendingMessageCount = len(agent.PendingMessages)
		return true, nil
	}
	return false, nil
}

// CancelAgentMessageGeneration linearizes cancellation with child drain under
// one exact generation's message lock. It never resolves a name or a newer
// generation for the caller.
func (r *AgentRunner) CancelAgentMessageGeneration(
	agentID string,
	generation int64,
	commandUUID string,
) (string, error) {
	if r == nil {
		return "stale_generation", nil
	}
	agentID = strings.TrimSpace(agentID)
	commandUUID = strings.TrimSpace(commandUUID)
	if agentID == "" || generation <= 0 || commandUUID == "" {
		return "", fmt.Errorf(
			"agent_runner: exact input cancellation requires Agent ID, positive generation, and MessageID",
		)
	}
	r.mu.Lock()
	if !r.lifecycleAdmittedLocked() {
		r.mu.Unlock()
		return "", fmt.Errorf(
			"agent_runner: active Session deletion blocks exact input cancellation",
		)
	}
	if reservation := r.launchingAgents[agentID]; reservation != nil {
		if reservation.Generation != generation {
			r.mu.Unlock()
			return "stale_generation", nil
		}
		for i, message := range reservation.PendingMessages {
			if agentMessageCommandUUID(message) != commandUUID {
				continue
			}
			reservation.PendingMessages = append(
				reservation.PendingMessages[:i],
				reservation.PendingMessages[i+1:]...,
			)
			r.mu.Unlock()
			return "input_cancelled", nil
		}
		r.mu.Unlock()
		return "input_not_pending", nil
	}
	agent := r.activeAgents[agentID]
	if agent == nil {
		r.mu.Unlock()
		return "stale_generation", nil
	}
	agent.mu.Lock()
	r.mu.Unlock()
	defer agent.mu.Unlock()
	if agent.runGeneration != generation {
		return "stale_generation", nil
	}
	for i, message := range agent.PendingMessages {
		if agentMessageCommandUUID(message) != commandUUID {
			continue
		}
		agent.PendingMessages = append(
			agent.PendingMessages[:i],
			agent.PendingMessages[i+1:]...,
		)
		agent.PendingMessageCount = len(agent.PendingMessages)
		return "input_cancelled", nil
	}
	return "input_not_pending", nil
}

// AbortAgent cancels a running sub-agent by ID.
func (r *AgentRunner) AbortAgent(id string) error {
	if !r.lifecycleAdmitted() {
		return fmt.Errorf(
			"agent_runner: active Session deletion blocks cancel",
		)
	}
	agent, err := r.lookupAgent(id)
	if err != nil {
		return err
	}

	agent.mu.Lock()
	defer agent.mu.Unlock()

	if agent.Status != "running" {
		return fmt.Errorf("agent_runner: agent %q is not running (status: %s)", id, agent.Status)
	}

	// Cancel first, then open a paused checkpoint. The context-aware gate keeps
	// the query from starting another model request before terminal publication.
	agent.cancel()
	if r.steering != nil {
		_ = r.steering.Resume(agent.ID)
	}
	agent.Status = "aborted"
	agent.CompletedAt = time.Now()
	return nil
}

// AbortAgentGeneration cancels exactly one live generation.
func (r *AgentRunner) AbortAgentGeneration(agentID string, generation int64) error {
	agent, err := r.lookupExactLiveAgent(agentID, generation, "cancel")
	if err != nil {
		return err
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if agent.runGeneration != generation || agent.Status != "running" {
		return fmt.Errorf(
			"agent_runner: exact cancel target %s/%d changed",
			agentID,
			generation,
		)
	}
	agent.cancel()
	if r.steering != nil {
		_ = r.steering.Resume(agent.ID)
	}
	agent.Status = "aborted"
	agent.CompletedAt = time.Now()
	return nil
}

// PauseAgent requests a pause at the next query/tool-round boundary.
func (r *AgentRunner) PauseAgent(id string) error {
	if !r.lifecycleAdmitted() {
		return fmt.Errorf(
			"agent_runner: active Session deletion blocks pause",
		)
	}
	agent, err := r.lookupAgent(id)
	if err != nil {
		return err
	}
	agent.mu.Lock()
	status := agent.Status
	agentID := agent.ID
	agent.mu.Unlock()
	if status != "running" {
		return fmt.Errorf("agent_runner: agent %q is not running (status: %s)", id, status)
	}
	if r.steering == nil {
		return fmt.Errorf("agent_runner: steering is unavailable")
	}
	return r.steering.Pause(agentID)
}

// PauseAgentGeneration pauses exactly one live generation.
func (r *AgentRunner) PauseAgentGeneration(agentID string, generation int64) error {
	agent, err := r.lookupExactLiveAgent(agentID, generation, "pause")
	if err != nil {
		return err
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if agent.runGeneration != generation || agent.Status != "running" {
		return fmt.Errorf(
			"agent_runner: exact pause target %s/%d changed",
			agentID,
			generation,
		)
	}
	if r.steering == nil {
		return fmt.Errorf("agent_runner: steering is unavailable")
	}
	return r.steering.Pause(agentID)
}

// ResumeAgent releases a paused query/tool-round boundary.
func (r *AgentRunner) ResumeAgent(id string) error {
	if !r.lifecycleAdmitted() {
		return fmt.Errorf(
			"agent_runner: active Session deletion blocks resume",
		)
	}
	agent, err := r.lookupAgent(id)
	if err != nil {
		return err
	}
	agent.mu.Lock()
	status := agent.Status
	agentID := agent.ID
	agent.mu.Unlock()
	if status != "running" {
		return fmt.Errorf("agent_runner: agent %q is not running (status: %s)", id, status)
	}
	if r.steering == nil {
		return fmt.Errorf("agent_runner: steering is unavailable")
	}
	return r.steering.Resume(agentID)
}

// ResumeAgentGeneration resumes exactly one live generation.
func (r *AgentRunner) ResumeAgentGeneration(agentID string, generation int64) error {
	agent, err := r.lookupExactLiveAgent(agentID, generation, "resume")
	if err != nil {
		return err
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if agent.runGeneration != generation || agent.Status != "running" {
		return fmt.Errorf(
			"agent_runner: exact resume target %s/%d changed",
			agentID,
			generation,
		)
	}
	if r.steering == nil {
		return fmt.Errorf("agent_runner: steering is unavailable")
	}
	return r.steering.Resume(agentID)
}

func (r *AgentRunner) lookupExactLiveAgent(
	agentID string,
	generation int64,
	action string,
) (*RunningAgent, error) {
	agentID = strings.TrimSpace(agentID)
	if r == nil || agentID == "" || generation <= 0 {
		return nil, fmt.Errorf(
			"agent_runner: exact %s requires Agent ID and positive generation",
			action,
		)
	}
	r.mu.RLock()
	agent := r.activeAgents[agentID]
	reservation := r.launchingAgents[agentID]
	admitted := r.lifecycleAdmittedLocked()
	r.mu.RUnlock()
	if !admitted {
		return nil, fmt.Errorf(
			"agent_runner: active Session deletion blocks exact %s",
			action,
		)
	}
	if reservation != nil {
		return nil, fmt.Errorf(
			"agent_runner: exact %s target %s/%d is pre-dispatch",
			action,
			agentID,
			generation,
		)
	}
	if agent == nil {
		return nil, fmt.Errorf(
			"agent_runner: exact %s target %s/%d is not live",
			action,
			agentID,
			generation,
		)
	}
	return agent, nil
}

// AgentSteeringInfo returns a defensive steering snapshot for one Agent.
func (r *AgentRunner) AgentSteeringInfo(id string) (AgentSteeringInfo, bool) {
	if r == nil || r.steering == nil {
		return AgentSteeringInfo{}, false
	}
	return r.steering.GetInfo(id)
}

// IsAgentPaused reports whether a pause request is awaiting or holding a safe
// query/tool-round boundary.
func (r *AgentRunner) IsAgentPaused(id string) bool {
	return r != nil && r.steering != nil && r.steering.IsPaused(id)
}

// IsAgentGenerationPaused reports pause state only while the exact live
// generation still matches.
func (r *AgentRunner) IsAgentGenerationPaused(
	agentID string,
	generation int64,
) bool {
	if r == nil || strings.TrimSpace(agentID) == "" || generation <= 0 {
		return false
	}
	r.mu.RLock()
	agent := r.activeAgents[agentID]
	reservation := r.launchingAgents[agentID]
	r.mu.RUnlock()
	if agent == nil || reservation != nil {
		return false
	}
	agent.mu.Lock()
	matches := agent.runGeneration == generation && agent.Status == "running"
	agent.mu.Unlock()
	return matches && r.steering != nil && r.steering.IsPaused(agentID)
}

// WaitIfAgentPaused blocks at a safe boundary until resume or cancellation.
func (r *AgentRunner) WaitIfAgentPaused(ctx context.Context, id string) (bool, error) {
	if r == nil || r.steering == nil {
		return false, fmt.Errorf("agent_runner: steering is unavailable")
	}
	return r.steering.WaitIfPausedContext(ctx, id)
}

func (r *AgentRunner) registerAgentSteeringLocked(agentID string) {
	if r.steering == nil {
		r.steering = NewAgentSteering()
	}
	// A retained Agent may be resumed before an older executor goroutine has
	// completed its final cleanup. Reset registration for the new generation.
	r.steering.Unregister(agentID)
	r.steering.Register(agentID)
}

func (r *AgentRunner) unregisterAgentSteeringGeneration(agentID string, generation int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	agent, ok := r.activeAgents[agentID]
	if !ok {
		return
	}
	agent.mu.Lock()
	currentGeneration := agent.runGeneration
	status := agent.Status
	agent.mu.Unlock()
	if currentGeneration == generation && status != "running" && r.steering != nil {
		r.steering.Unregister(agentID)
	}
}

func (r *AgentRunner) lookupAgent(idOrName string) (*RunningAgent, error) {
	if r == nil {
		return nil, fmt.Errorf("agent_runner: runner is nil")
	}
	r.mu.RLock()
	if agent, ok := r.activeAgents[idOrName]; ok {
		r.mu.RUnlock()
		return agent, nil
	}
	if agentID, ok := r.nameRegistry[idOrName]; ok {
		agent, exists := r.activeAgents[agentID]
		r.mu.RUnlock()
		if exists {
			return agent, nil
		}
		return nil, fmt.Errorf("agent_runner: agent %q not found", idOrName)
	}
	agents := make([]*RunningAgent, 0, len(r.activeAgents))
	for _, agent := range r.activeAgents {
		agents = append(agents, agent)
	}
	r.mu.RUnlock()
	for _, agent := range agents {
		agent.mu.Lock()
		matches := agent.Name != "" && agent.Name == idOrName
		agent.mu.Unlock()
		if matches {
			return agent, nil
		}
	}
	return nil, fmt.Errorf("agent_runner: agent %q not found", idOrName)
}

func (r *AgentRunner) lookupAgentForResume(idOrName string) (*RunningAgent, string, error) {
	if r == nil {
		return nil, "", fmt.Errorf("agent_runner: runner is nil")
	}
	r.mu.RLock()
	if agent, ok := r.activeAgents[idOrName]; ok {
		r.mu.RUnlock()
		return agent, "", nil
	}
	if agentID, ok := r.nameRegistry[idOrName]; ok {
		agent, exists := r.activeAgents[agentID]
		r.mu.RUnlock()
		if exists {
			return agent, "", nil
		}
		return nil, agentID, nil
	}
	if _, ok := r.evictedAgents[idOrName]; ok {
		r.mu.RUnlock()
		return nil, idOrName, nil
	}
	agents := make([]*RunningAgent, 0, len(r.activeAgents))
	for _, agent := range r.activeAgents {
		agents = append(agents, agent)
	}
	r.mu.RUnlock()
	for _, agent := range agents {
		agent.mu.Lock()
		matches := agent.Name != "" && agent.Name == idOrName
		agent.mu.Unlock()
		if matches {
			return agent, "", nil
		}
	}
	return nil, "", fmt.Errorf("agent_runner: agent %q not found", idOrName)
}

// Cleanup removes completed/errored/aborted agents older than maxAge from tracking.
func (r *AgentRunner) Cleanup(maxAge time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	for id, agent := range r.activeAgents {
		agent.mu.Lock()
		status := agent.Status
		completedAt := agent.CompletedAt
		detaching := agent.foregroundWait != nil &&
			agent.foregroundWait.state == agentForegroundWaitDetaching
		agent.mu.Unlock()

		if detaching {
			continue
		}
		if status == "completed" || status == "failed" || status == "errored" || status == "aborted" {
			if !completedAt.IsZero() && now.Sub(completedAt) > maxAge {
				r.evictedAgents[id] = agent.Name
				delete(r.activeAgents, id)
			}
		}
	}
}

func (r *AgentRunner) agentOutputPathLocked(agentID string) string {
	return filepath.Join(r.outputDir, agentID+".out")
}

func (r *AgentRunner) agentMetadataPathLocked(agentID string) string {
	return filepath.Join(r.outputDir, "agents", agentID+".json")
}

func (r *AgentRunner) agentTranscriptDirLocked() string {
	return filepath.Join(r.outputDir, "transcripts")
}

func (r *AgentRunner) loadEvictedAgentStateLocked(agentID string) (persistedAgentMetadata, []*schema.Message, error) {
	metadataPath := r.agentMetadataPathLocked(agentID)
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return persistedAgentMetadata{}, nil, ErrPersistedAgentMetadataMissing
		}
		return persistedAgentMetadata{}, nil, fmt.Errorf("read persisted metadata: %w", err)
	}
	var metadata persistedAgentMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return persistedAgentMetadata{}, nil, fmt.Errorf("corrupt persisted metadata: %w", err)
	}
	if metadata.AgentID != "" && metadata.AgentID != agentID {
		return persistedAgentMetadata{}, nil, fmt.Errorf("corrupt persisted metadata: agent id mismatch")
	}
	if metadata.Generation <= 0 {
		return persistedAgentMetadata{}, nil, fmt.Errorf(
			"corrupt persisted metadata: execution generation is missing",
		)
	}
	switch metadata.Status {
	case "running", "completed", "failed", "aborted":
	default:
		return persistedAgentMetadata{}, nil, fmt.Errorf(
			"corrupt persisted metadata: invalid status %q",
			metadata.Status,
		)
	}
	if isTerminalAgentStatus(metadata.Status) &&
		metadata.TerminalSequence == 0 &&
		metadata.Completion == nil {
		metadata.TerminalSequence = uint64(max(metadata.Generation, 1))
	}
	if metadata.SessionID == "" {
		metadata.SessionID = agentID
	}
	if metadata.ThreadID == "" {
		metadata.ThreadID = firstNonEmpty(metadata.Options.ThreadID, agentID)
	}
	metadata.Options.AgentID = firstNonEmpty(metadata.Options.AgentID, agentID)
	metadata.Options.SessionID = firstNonEmpty(metadata.Options.SessionID, metadata.SessionID)
	metadata.Options.ThreadID = firstNonEmpty(metadata.Options.ThreadID, metadata.ThreadID)
	metadata.Options.ParentSessionID = firstNonEmpty(metadata.Options.ParentSessionID, metadata.ParentSessionID)
	metadata.Options.ParentThreadID = firstNonEmpty(metadata.Options.ParentThreadID, metadata.ParentThreadID)
	metadata.Options.ParentAgentID = firstNonEmpty(metadata.Options.ParentAgentID, metadata.ParentAgentID)
	metadata.Options.Model = firstNonEmpty(metadata.Options.Model, metadata.Model)
	metadata.Options.SubagentType = firstNonEmpty(metadata.Options.SubagentType, metadata.AgentType)
	metadata.Options.Mode = firstNonEmpty(metadata.Options.Mode, metadata.PermissionMode)
	metadata.Options.Isolation = firstNonEmpty(metadata.Options.Isolation, metadata.Isolation)
	metadata.Options.CWD = firstNonEmpty(metadata.Options.CWD, metadata.CWD)
	if metadata.TranscriptDir == "" {
		if metadata.TranscriptPath != "" {
			metadata.TranscriptDir = filepath.Dir(metadata.TranscriptPath)
		} else {
			metadata.TranscriptDir = r.agentTranscriptDirLocked()
		}
	}
	recorder := transcript.NewRecorder(metadata.SessionID, metadata.TranscriptDir)
	if _, err := os.Stat(recorder.Path()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return persistedAgentMetadata{}, nil, fmt.Errorf("missing persisted transcript")
		}
		return persistedAgentMetadata{}, nil, fmt.Errorf("read persisted transcript: %w", err)
	}
	loaded, err := recorder.LoadFull()
	if err != nil {
		return persistedAgentMetadata{}, nil, fmt.Errorf("corrupt persisted transcript: %w", err)
	}
	if len(loaded.Messages) == 0 {
		return persistedAgentMetadata{}, nil, fmt.Errorf("missing persisted transcript messages")
	}
	return metadata, cloneSchemaMessages(loaded.Messages), nil
}

// LoadPersistedAgentSnapshot reads launch/terminal metadata and transcript from
// disk without registering or resuming the Agent. A fresh runner can use this
// after a process restart to inspect the pre-response launch boundary.
func (r *AgentRunner) LoadPersistedAgentSnapshot(agentID string) (RunningAgent, error) {
	if r == nil {
		return RunningAgent{}, fmt.Errorf("agent_runner: runner is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	metadata, messages, err := r.loadEvictedAgentStateLocked(agentID)
	if err != nil {
		return RunningAgent{}, fmt.Errorf("agent_runner: load persisted agent %q: %w", agentID, err)
	}
	options := cloneAgentExecOptions(metadata.Options)
	options.ResumeMessages = nil
	snapshot := RunningAgent{
		ID:               firstNonEmpty(metadata.AgentID, agentID),
		SessionID:        metadata.SessionID,
		ThreadID:         metadata.ThreadID,
		ParentSessionID:  metadata.ParentSessionID,
		ParentThreadID:   metadata.ParentThreadID,
		ParentAgentID:    metadata.ParentAgentID,
		Type:             "local_agent",
		Task:             metadata.Task,
		Description:      metadata.Description,
		Name:             metadata.Name,
		ToolUseID:        metadata.ToolUseID,
		Status:           metadata.Status,
		predispatch:      metadata.Predispatch,
		Error:            persistedAgentError(metadata.Error),
		Messages:         cloneSchemaMessages(messages),
		StartedAt:        metadata.StartedAt,
		CompletedAt:      metadata.CompletedAt,
		OutputFile:       metadata.OutputFile,
		TranscriptPath:   firstNonEmpty(metadata.TranscriptPath, transcript.NewRecorder(metadata.SessionID, metadata.TranscriptDir).Path()),
		Options:          options,
		WorktreePath:     metadata.WorktreePath,
		WorktreeBranch:   metadata.WorktreeBranch,
		Worktree:         cloneAgentWorktreeResult(metadata.Worktree),
		TerminalSequence: metadata.TerminalSequence,
		runGeneration:    metadata.Generation,
		mu:               &sync.Mutex{},
	}
	if snapshot.Status == "completed" && snapshot.OutputFile != "" {
		if data, readErr := os.ReadFile(snapshot.OutputFile); readErr == nil {
			snapshot.Result = string(data)
			snapshot.OutputOffset = int64(len(data))
		}
	}
	if isTerminalAgentStatus(snapshot.Status) {
		if snapshot.TerminalSequence == 0 {
			snapshot.TerminalSequence = uint64(max(snapshot.runGeneration, 1))
		}
		if metadata.Completion == nil {
			snapshot.Completion = buildAgentCompletionRecordLocked(&snapshot)
			snapshot.Completion.Version = agentCompletionVersion
			snapshot.completionLegacy = true
		} else {
			snapshot.Completion = cloneAgentCompletionRecord(metadata.Completion)
			if err := validateAgentCompletionRecord(&snapshot); err != nil {
				return RunningAgent{}, fmt.Errorf(
					"agent_runner: load persisted agent %q: %w",
					agentID,
					err,
				)
			}
		}
		snapshot.terminalDurable = true
	}
	return snapshot, nil
}

// RegisterPersistedAgent makes one validated durable Agent discoverable for
// explicit continuation without pretending it is live. Session restore calls
// this only for Agent IDs owned by the selected source session; fork metadata
// intentionally contains no such IDs.
func (r *AgentRunner) RegisterPersistedAgent(
	agentID string,
) (RunningAgent, error) {
	snapshot, err := r.LoadPersistedAgentSnapshot(agentID)
	if err != nil {
		return RunningAgent{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, live := r.activeAgents[agentID]; live {
		return snapshot, nil
	}
	r.evictedAgents[agentID] = snapshot.Name
	if snapshot.Name != "" {
		r.nameRegistry[snapshot.Name] = agentID
	}
	return snapshot, nil
}

func (a *RunningAgent) persistOutput(output string) error {
	if a.OutputFile == "" {
		return nil
	}
	data := []byte(output)
	if err := writeAgentFileAtomic(a.OutputFile, data, 0o644); err != nil {
		return fmt.Errorf("persist agent output: write output file: %w", err)
	}
	a.OutputOffset = int64(len(data))
	return nil
}

func (a *RunningAgent) persistDurableState() error {
	if a == nil || a.OutputFile == "" || a.ID == "" {
		return nil
	}
	outputDir := filepath.Dir(a.OutputFile)
	metadataDir := filepath.Join(outputDir, "agents")
	transcriptDir := filepath.Join(outputDir, "transcripts")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		return fmt.Errorf("persist agent metadata: create metadata dir: %w", err)
	}
	sessionID := firstNonEmpty(a.SessionID, a.ID)
	recorder := transcript.NewRecorder(sessionID, transcriptDir)
	defer recorder.Close() //nolint:errcheck
	if a.TranscriptPath == "" {
		a.TranscriptPath = recorder.Path()
	}
	if err := persistAgentTranscriptState(
		recorder,
		cloneSchemaMessages(a.Messages),
	); err != nil {
		return fmt.Errorf("persist agent transcript: %w", err)
	}
	if err := recorder.Flush(); err != nil {
		return fmt.Errorf("persist agent transcript: flush: %w", err)
	}
	metadata := persistedAgentMetadata{
		AgentID:          a.ID,
		ThreadID:         a.ThreadID,
		ParentSessionID:  a.ParentSessionID,
		ParentThreadID:   a.ParentThreadID,
		ParentAgentID:    a.ParentAgentID,
		Name:             a.Name,
		Description:      a.Description,
		Task:             a.Task,
		ToolUseID:        a.ToolUseID,
		Status:           a.Status,
		Predispatch:      a.predispatch,
		Error:            agentErrorText(a.Error),
		Generation:       a.runGeneration,
		TerminalSequence: a.TerminalSequence,
		Completion:       cloneAgentCompletionRecord(a.Completion),
		StartedAt:        a.StartedAt,
		CompletedAt:      a.CompletedAt,
		OutputFile:       a.OutputFile,
		WorktreePath:     a.WorktreePath,
		WorktreeBranch:   a.WorktreeBranch,
		Worktree:         cloneAgentWorktreeResult(a.Worktree),
		Model:            a.Options.Model,
		AgentType:        a.Options.SubagentType,
		PermissionMode:   firstNonEmpty(a.Options.Mode, a.Options.InheritedPermissionMode),
		Isolation:        a.Options.Isolation,
		CWD:              a.Options.CWD,
		TranscriptPath:   a.TranscriptPath,
		TranscriptDir:    transcriptDir,
		SessionID:        a.SessionID,
		Options:          cloneAgentExecOptions(a.Options),
	}
	metadata.Options.ResumeMessages = nil
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("persist agent metadata: marshal: %w", err)
	}
	if err := writeAgentFileAtomic(filepath.Join(metadataDir, a.ID+".json"), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("persist agent metadata: write: %w", err)
	}
	return nil
}

// persistAgentTranscriptState creates the initial Agent-owned message seed,
// then uses append-only checkpoints for later optimistic/terminal rewrites.
// QueryEngine metadata and lifecycle records therefore remain the durable
// authority for a child session's pinned query kernel across continuation.
func persistAgentTranscriptState(
	recorder *transcript.Recorder,
	messages []*schema.Message,
) error {
	if recorder == nil || recorder.Path() == "" {
		return nil
	}
	if _, err := os.Stat(recorder.Path()); errors.Is(err, os.ErrNotExist) {
		return recorder.Replace(messages)
	} else if err != nil {
		return err
	}
	loaded, err := recorder.LoadFull()
	if err != nil {
		return err
	}
	var fileStates map[string]transcript.FileState
	if len(loaded.FileSnapshots) > 0 {
		fileStates = loaded.FileSnapshots[len(loaded.FileSnapshots)-1]
	}
	return recorder.RecordLifecycleBoundary(
		transcript.LifecycleCheckpoint,
		messages,
		loaded.Replacements,
		fileStates,
	)
}

func agentErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func persistedAgentError(message string) error {
	if strings.TrimSpace(message) == "" {
		return nil
	}
	return errors.New(message)
}

func writeAgentFileAtomic(path string, data []byte, mode os.FileMode) (retErr error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		if retErr != nil {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		// Windows does not replace an existing destination with os.Rename.
		if runtime.GOOS != "windows" {
			return err
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return errors.Join(err, removeErr)
		}
		if retryErr := os.Rename(tempPath, path); retryErr != nil {
			return errors.Join(err, retryErr)
		}
	}
	return nil
}

func mergeResultProgress(progress *AgentProgress, result *AgentExecResult) {
	if result == nil {
		return
	}
	if result.TokensUsed > progress.TokenCount {
		progress.TokenCount = result.TokensUsed
	}
	if len(result.ToolsUsed) > progress.ToolUseCount {
		progress.ToolUseCount = len(result.ToolsUsed)
	}
}

func hasAgentProgressLocked(progress AgentProgress) bool {
	return progress.TokenCount > 0 ||
		progress.ToolUseCount > 0 ||
		progress.LastActivity != nil ||
		len(progress.RecentActivities) > 0 ||
		strings.TrimSpace(progress.DisplaySummary()) != ""
}

func buildAgentProgressEventLocked(agent *RunningAgent, now time.Time) AgentProgressEvent {
	progress := agent.Progress
	durationMs := int64(0)
	if !agent.StartedAt.IsZero() {
		if now.Before(agent.StartedAt) {
			durationMs = 0
		} else {
			durationMs = now.Sub(agent.StartedAt).Milliseconds()
		}
	}
	lastToolName := ""
	if progress.LastActivity != nil {
		lastToolName = progress.LastActivity.ToolName
	}
	return AgentProgressEvent{
		Type:        "system",
		Subtype:     "task_progress",
		TaskID:      agent.ID,
		ToolUseID:   agent.ToolUseID,
		Description: agent.Description,
		Usage: AgentProgressUsage{
			TotalTokens: progress.TokenCount,
			ToolUses:    progress.ToolUseCount,
			DurationMS:  durationMs,
		},
		LastToolName:     lastToolName,
		Summary:          progress.DisplaySummary(),
		RecentActivities: cloneAgentProgress(progress).RecentActivities,
	}
}

func buildAgentNotificationLocked(agent *RunningAgent) AgentNotification {
	if agent != nil && agent.Completion != nil {
		return AgentNotification{
			AgentID:           agent.ID,
			ToolUseID:         agent.ToolUseID,
			Status:            agent.Completion.Status,
			Description:       agent.Description,
			OutputFile:        agent.OutputFile,
			Message:           agent.Completion.Message,
			CreatedAt:         agent.Completion.CreatedAt,
			Generation:        agent.runGeneration,
			TerminalSequence:  agent.Completion.TerminalSequence,
			CompletionID:      agent.Completion.CompletionID,
			CompletionVersion: agent.Completion.Version,
			Legacy:            agent.completionLegacy,
		}
	}
	completion := buildAgentCompletionRecordLocked(agent)
	return AgentNotification{
		AgentID:           agent.ID,
		ToolUseID:         agent.ToolUseID,
		Status:            completion.Status,
		Description:       agent.Description,
		OutputFile:        agent.OutputFile,
		Message:           completion.Message,
		CreatedAt:         completion.CreatedAt,
		Generation:        agent.runGeneration,
		TerminalSequence:  completion.TerminalSequence,
		CompletionID:      completion.CompletionID,
		CompletionVersion: completion.Version,
	}
}

func buildAgentCompletionRecordLocked(agent *RunningAgent) *AgentCompletionRecord {
	if agent == nil {
		return nil
	}
	terminalSequence := agent.TerminalSequence
	if terminalSequence == 0 {
		terminalSequence = uint64(max(agent.runGeneration, 1))
	}
	status := notificationStatus(agent.Status)
	summary := fmt.Sprintf("Agent %q completed", agent.Description)
	if status == "failed" { //nolint:staticcheck
		errorText := "Unknown error"
		if agent.Error != nil {
			errorText = agent.Error.Error()
		}
		summary = fmt.Sprintf("Agent %q failed: %s", agent.Description, errorText)
	} else if status == "killed" {
		summary = fmt.Sprintf("Agent %q was stopped", agent.Description)
	}

	var sb strings.Builder
	sb.WriteString("<task-notification>\n")
	fmt.Fprintf(&sb, "<task-id>%s</task-id>\n", agent.ID)
	if agent.ToolUseID != "" {
		fmt.Fprintf(&sb, "<tool-use-id>%s</tool-use-id>\n", agent.ToolUseID)
	}
	fmt.Fprintf(&sb, "<output-file>%s</output-file>\n", agent.OutputFile)
	fmt.Fprintf(&sb, "<status>%s</status>\n", status)
	fmt.Fprintf(&sb, "<summary>%s</summary>", summary)
	if agent.Result != "" {
		fmt.Fprintf(&sb, "\n<result>%s</result>", agent.Result)
	}
	if details := formatAgentWorktreeDetails(agent.Worktree); details != "" {
		fmt.Fprintf(
			&sb,
			"\n<worktree-handoff>%s</worktree-handoff>",
			details,
		)
	}
	if agent.Progress.TokenCount > 0 || agent.Progress.ToolUseCount > 0 || !agent.StartedAt.IsZero() {
		durationMs := int64(0)
		if !agent.StartedAt.IsZero() && !agent.CompletedAt.IsZero() {
			durationMs = agent.CompletedAt.Sub(agent.StartedAt).Milliseconds()
		}
		fmt.Fprintf(&sb, "\n<usage><total_tokens>%d</total_tokens><tool_uses>%d</tool_uses><duration_ms>%d</duration_ms></usage>", agent.Progress.TokenCount, agent.Progress.ToolUseCount, durationMs)
	}
	sb.WriteString("\n</task-notification>")

	return &AgentCompletionRecord{
		Version:          agentCompletionVersion,
		CompletionID:     agentCompletionID(agent.ID, agent.runGeneration, terminalSequence),
		TerminalSequence: terminalSequence,
		Status:           status,
		Message:          sb.String(),
		CreatedAt:        agent.CompletedAt.UTC(),
	}
}

func validateAgentCompletionRecord(agent *RunningAgent) error {
	if agent == nil || agent.Completion == nil {
		return errors.New("persisted completion is missing")
	}
	completion := agent.Completion
	if completion.Version != agentCompletionVersion {
		return fmt.Errorf("persisted completion has unsupported version %d", completion.Version)
	}
	if completion.TerminalSequence == 0 ||
		completion.TerminalSequence != agent.TerminalSequence {
		return errors.New("persisted completion terminal sequence mismatch")
	}
	if completion.Status != notificationStatus(agent.Status) {
		return errors.New("persisted completion terminal status mismatch")
	}
	wantID := agentCompletionID(
		agent.ID,
		agent.runGeneration,
		completion.TerminalSequence,
	)
	if strings.TrimSpace(completion.CompletionID) != wantID {
		return errors.New("persisted completion identity mismatch")
	}
	if strings.TrimSpace(completion.Message) == "" || completion.CreatedAt.IsZero() {
		return errors.New("persisted completion payload is incomplete")
	}
	return nil
}

func cloneAgentCompletionRecord(
	completion *AgentCompletionRecord,
) *AgentCompletionRecord {
	if completion == nil {
		return nil
	}
	cloned := *completion
	return &cloned
}

func notificationStatus(status string) string {
	if status == "aborted" {
		return "killed"
	}
	return status
}

func isTerminalAgentStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "aborted"
}

func cloneAgentProgress(in AgentProgress) AgentProgress {
	return NormalizeAgentProgress(in)
}

func cloneToolActivity(in ToolActivity) ToolActivity {
	return normalizeToolActivity(in)
}

func cloneMessagePayloads(in []MessagePayload) []MessagePayload {
	if len(in) == 0 {
		return nil
	}
	out := make([]MessagePayload, len(in))
	for i, msg := range in {
		out[i] = msg
		if msg.Metadata != nil {
			out[i].Metadata = make(map[string]any, len(msg.Metadata))
			for k, v := range msg.Metadata {
				out[i].Metadata[k] = v
			}
		}
	}
	return out
}

func normalizeAgentMessagePayload(msg MessagePayload, agentID string) MessagePayload {
	msg.To = agentID
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}
	metadata := make(map[string]any, len(msg.Metadata)+1)
	for key, value := range msg.Metadata {
		metadata[key] = value
	}
	if commandUUID, _ := metadata["command_uuid"].(string); strings.TrimSpace(commandUUID) == "" {
		metadata["command_uuid"] = uuid.NewString()
	}
	msg.Metadata = metadata
	return msg
}

func agentMessageSchemaExtra(msg MessagePayload) map[string]any {
	commandUUID := agentMessageCommandUUID(msg)
	if strings.TrimSpace(commandUUID) == "" {
		return nil
	}
	return map[string]any{
		"command_uuid": commandUUID,
		"command_mode": "prompt",
		"provenance":   "send_message",
	}
}

func agentMessageCommandUUID(msg MessagePayload) string {
	commandUUID, _ := msg.Metadata["command_uuid"].(string)
	return strings.TrimSpace(commandUUID)
}

// rememberAcceptedAgentMessage returns false when the command UUID already
// belongs to this exact generation. Messages without a command UUID retain
// their historical append-only behavior.
func rememberAcceptedAgentMessage(
	accepted *map[string]struct{},
	msg MessagePayload,
) bool {
	commandUUID := agentMessageCommandUUID(msg)
	if commandUUID == "" {
		return true
	}
	if *accepted == nil {
		*accepted = make(map[string]struct{})
	}
	if _, exists := (*accepted)[commandUUID]; exists {
		return false
	}
	(*accepted)[commandUUID] = struct{}{}
	return true
}

func cloneStringSet(source map[string]struct{}) map[string]struct{} {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]struct{}, len(source))
	for value := range source {
		cloned[value] = struct{}{}
	}
	return cloned
}

func cloneAgentExecOptions(in AgentExecOptions) AgentExecOptions {
	out := in
	out.AllowedTools = append([]string(nil), in.AllowedTools...)
	if in.WorkItem != nil {
		workItem := *in.WorkItem
		out.WorkItem = &workItem
	}
	out.ResumeMessages = cloneSchemaMessages(in.ResumeMessages)
	return out
}

func agentWorktreeResultFromRecord(
	record worktree.Record,
) *AgentWorktreeResult {
	if strings.TrimSpace(record.ID) == "" {
		return nil
	}
	return &AgentWorktreeResult{
		RecordID:          record.ID,
		State:             record.State,
		Path:              record.Path,
		Branch:            record.Branch,
		BaseCommit:        record.BaseCommit,
		SourceDirtyReport: worktree.CloneDirtyReport(record.SourceDirtyReport),
		ResultDirtyReport: worktree.CloneDirtyReport(record.ResultDirtyReport),
		LastErrorCategory: record.LastErrorCategory,
		LastError:         record.LastError,
	}
}

func cloneAgentWorktreeResult(
	result *AgentWorktreeResult,
) *AgentWorktreeResult {
	if result == nil {
		return nil
	}
	clone := *result
	clone.SourceDirtyReport = worktree.CloneDirtyReport(
		result.SourceDirtyReport,
	)
	clone.ResultDirtyReport = worktree.CloneDirtyReport(
		result.ResultDirtyReport,
	)
	return &clone
}

func cloneSchemaMessages(in []*schema.Message) []*schema.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]*schema.Message, len(in))
	for i, msg := range in {
		if msg == nil {
			continue
		}
		cloned := *msg
		if msg.ToolCalls != nil {
			cloned.ToolCalls = append([]schema.ToolCall(nil), msg.ToolCalls...)
		}
		if msg.Extra != nil {
			cloned.Extra = make(map[string]any, len(msg.Extra))
			for k, v := range msg.Extra {
				cloned.Extra[k] = v
			}
		}
		out[i] = &cloned
	}
	return out
}

type (
	agentRunnerCtxKey   struct{}
	agentExecutorCtxKey struct{}
)

// WithAgentRunner returns a context carrying the given AgentRunner.
func WithAgentRunner(ctx context.Context, r *AgentRunner) context.Context {
	return context.WithValue(ctx, agentRunnerCtxKey{}, r)
}

// WithAgentExecutor scopes nested Agent launches to the calling engine without
// replacing the shared runner's fallback executor.
func WithAgentExecutor(ctx context.Context, executor AgentExecutor) context.Context {
	return context.WithValue(ctx, agentExecutorCtxKey{}, executor)
}

// AgentExecutorFromCtx returns the executor owned by the calling engine.
func AgentExecutorFromCtx(ctx context.Context) AgentExecutor {
	if ctx == nil {
		return nil
	}
	executor, _ := ctx.Value(agentExecutorCtxKey{}).(AgentExecutor)
	return executor
}

// AgentRunnerFromCtx returns only the explicitly bound QueryEngine owner.
func AgentRunnerFromCtx(ctx context.Context) *AgentRunner {
	if ctx == nil {
		return nil
	}
	if r, ok := ctx.Value(agentRunnerCtxKey{}).(*AgentRunner); ok && r != nil {
		return r
	}
	return nil
}
