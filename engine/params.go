package engine

import (
	"context"
	"sync"
	"time"

	"github.com/abietic/yhc/engine/budget"
	"github.com/abietic/yhc/engine/compact"
	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/internal/providerorigin"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/skills"
	"github.com/abietic/yhc/engine/storage"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// QuerySource identifies the origin of the query (main thread, agent, compact, etc.).
type QuerySource string

const (
	QuerySourceMain    QuerySource = "repl_main_thread"
	QuerySourceSDK     QuerySource = "sdk"
	QuerySourceAgent   QuerySource = "agent"
	QuerySourceCompact QuerySource = "compact"
)

// CanUseToolFn is the live external decision owner when no structured
// PermissionPromptFn is configured. When PermissionPromptFn is present, the
// engine-owned permission lifecycle remains the sole interactive owner.
type CanUseToolFn func(
	ctx context.Context,
	toolName string,
	input map[string]any,
	toolCtx *ToolUseContext,
) (allowed bool, reason string)

// RepeatedToolCallPromptFn requests a one-call override for the third
// consecutive identical tool call. It must never persist a permission rule.
type RepeatedToolCallPromptFn func(
	ctx context.Context,
	toolName string,
	toolUseID string,
	attempt int,
	toolCtx *ToolUseContext,
) (allowed bool, reason string)

// TaskBudget tracks the API task_budget across compaction boundaries.
type TaskBudget struct {
	Total     int
	Remaining int
}

// QueryDeps holds injectable dependencies.
type QueryDeps struct {
	UUID      func() string
	CallModel func(
		ctx context.Context,
		chatModel model.BaseChatModel,
		messages []*schema.Message,
		systemPrompt *schema.Message,
		tools []*schema.ToolInfo,
		opts execution.CallModelOptions,
	) (*execution.CallModelResult, error)
	Transcript *transcript.Recorder
	// RecordFileStateSnapshot lets the QueryEngine checkpoint owner wrap the
	// incremental writer without moving transcript authority into tool code.
	RecordFileStateSnapshot func(
		map[string]transcript.FileState,
	) error
	// ReportFileStateSnapshotFailure runs only after the incremental writer
	// returns. It must preserve the successful tool result and avoid I/O.
	ReportFileStateSnapshotFailure func(error)
	ProviderUsage                  execution.ProviderUsageAdmitter
}

// ToolExecutor executes a tool by name with the given JSON input and returns the result.
type ToolExecutor func(ctx context.Context, toolName, jsonInput string) (string, error)

// PermissionModeTransition commits one successful Plan tool transition after
// its canonical result is accepted. It returns a publication callback so the
// structured state event follows the corresponding tool result.
type PermissionModeTransition func(
	toolCtx *ToolUseContext,
	mode permission.Mode,
	requestID string,
) (*ToolUseContext, func(), error)

// AgentPauseCheckpointFn blocks only at query/tool-round boundaries and emits
// acknowledged steering transitions through the child query event stream.
type AgentPauseCheckpointFn func(
	ctx context.Context,
	agentID string,
	emit func(AgentLifecycleEvent),
) (bool, error)

// RuntimeItemCollector transfers pending external outbox records into the
// session coordinator and acknowledges the source only after durable enqueue.
type RuntimeItemCollector func(context.Context) error

// RuntimeItemAdmission revalidates a claimed durable prompt against the
// current selected route before it becomes model-visible.
type RuntimeItemAdmission func(context.Context, RuntimeItem) error

// QueryParams holds immutable parameters for a single query() call.
// Mirrors query.ts:181-199.
type QueryParams struct {
	Messages                  []*schema.Message
	SystemPrompt              *schema.Message
	SessionID                 string
	UserContext               map[string]string
	SystemContext             map[string]string
	CanUseTool                CanUseToolFn
	RepeatedToolCallPrompt    RepeatedToolCallPromptFn
	ToolUseContext            *ToolUseContext
	FallbackModel             string
	QuerySource               QuerySource
	MaxOutputTokensOverride   *int
	MaxTurns                  *int
	SkipCacheWrite            bool
	TaskBudget                *TaskBudget
	TokenBudgetTracker        *budget.TokenBudget
	Deps                      *QueryDeps
	ChatModel                 model.BaseChatModel
	modelCall                 *modelCallIdentity
	modelResolver             ModelResolver
	commandEntrypoint         string
	retryBaseDelay            time.Duration
	SummaryModel              model.BaseChatModel // compatibility owner for compaction and memory side calls
	ToolUseSummaryModel       model.BaseChatModel // role-routed model used only by root tool-use summaries
	toolUseSummaryCall        *modelCallIdentity
	EmitToolUseSummaries      bool // feature gate for root tool use summary generation
	InputCoordinator          *RuntimeInputCoordinator
	CollectRuntimeItems       RuntimeItemCollector
	AdmitRuntimeItem          RuntimeItemAdmission
	ProjectGraphCheckpoint    *projectGraphCheckpointStore
	ProjectGraphHITLEnabled   bool
	RuntimePermissionDecision *RuntimePermissionDecision
	ToolRegistry              *tools.Registry
	ToolExecutor              ToolExecutor
	TransitionPermissionMode  PermissionModeTransition
	CancelToolInteraction     func(toolUseID string) bool
	HookExecutor              *hooks.Executor
	ResultStorage             *storage.ResultStorage
	MemoryStore               *compact.MemoryStore // session memory for prefetch injection
	SkillRegistry             *skills.SkillRegistry
	// AgentPauseCheckpoint acknowledges a requested pause before the next model
	// request, then waits for resume or cancellation. It is never invoked in the
	// middle of model streaming or tool execution.
	AgentPauseCheckpoint AgentPauseCheckpointFn
	// AgentProgressDrainer drains non-terminal local/background agent progress
	// snapshots. Query emits these as SDK-shaped task_progress events without
	// consuming terminal task-notification delivery.
	AgentProgressDrainer func() []tools.AgentProgressEvent
	// TaskLifecycleDrainer drains local TaskCreate/TaskUpdate/TaskStop mutations
	// after tool execution so the engine/TUI lifecycle can observe task-list state.
	TaskLifecycleDrainer func() []tools.TaskLifecycleEvent
	// JSONSchema when set, indicates this query requires structured output via
	// SyntheticOutput tool. The loop terminates if the model exceeds
	// MaxStructuredOutputRetries without producing valid output.
	// Reference: QueryEngine.ts:1004-1048.
	JSONSchema                 map[string]any
	MaxStructuredOutputRetries int
	repeatedToolGuard          *repeatedToolCallGuard
	modelDispatchGuard         func(currentModel string) error
	modelCompactionGuard       func(currentModel string) error
	promptRouteGuard           func(currentModel string) error
	mediaRecovery              *mediaRecoveryContext
	commitProviderOrigin       func(providerorigin.Origin) error
}

// ToolUseContext carries tool execution context through the loop.
type ToolUseContext struct {
	AgentID                 string
	SessionID               string
	ThreadID                string
	Options                 *ToolUseOptions
	AbortController         *AbortController
	ReadFileState           *FileStateCache
	ContentReplacementState *ContentReplacementState
	Messages                []*schema.Message
	QueryTracking           *QueryTracking
	PlanMode                bool // when true, non-read-only tools are blocked
}

// ToolUseOptions holds tool-related configuration.
type ToolUseOptions struct {
	Tools                   []*schema.ToolInfo
	MainLoopModel           string
	ModelSetting            string // user-specified model setting (e.g. "opusplan", "haiku", etc.)
	ThinkingConfig          *ThinkingConfig
	PermissionMode          permission.Mode
	ToolChoice              string
	ForcedToolName          string
	IsNonInteractiveSession bool
	AgentDefinitions        *AgentDefinitions
	AppendSystemPrompt      string
	RefreshTools            func() []*schema.ToolInfo
	MaxBudgetUSD            *float64
	EffortValue             string
	CWD                     string // working directory for file operations
	PlanFilePath            string // path to plan file for post-compact reinjection
}

// QueryTracking tracks the chain of nested agent calls.
type QueryTracking struct {
	ChainID string
	Depth   int
}

// CacheSafeParams holds parameters needed for compaction that are safe to cache.
type CacheSafeParams struct {
	SystemPrompt        *schema.Message
	UserContext         map[string]string
	SystemContext       map[string]string
	ToolUseContext      *ToolUseContext
	ForkContextMessages []*schema.Message
}

// ThinkingConfig controls model thinking behavior.
type ThinkingConfig struct {
	Type         string // "adaptive", "enabled", "disabled"
	BudgetTokens *int   // optional token budget for "enabled" mode
}

// AgentDefinitions holds agent definitions available to the query.
type AgentDefinitions struct {
	ActiveAgents      []AgentDefinition
	AllAgents         []AgentDefinition
	AllowedAgentTypes []string
}

// AgentDefinition describes an available sub-agent.
type AgentDefinition struct {
	Name        string
	Description string
	AgentType   string
	MaxTurns    int
	Model       string
}

// AbortController manages cancellation.
type AbortController struct {
	Ctx    context.Context
	Cancel context.CancelFunc
	Reason string
}

func (a *AbortController) Abort() {
	a.Reason = "user_abort"
	a.Cancel()
}

func (a *AbortController) Aborted() bool {
	return a.Ctx.Err() != nil
}

// FileStateCache tracks which files the model has seen/edited.
type FileStateCache struct {
	mu         sync.RWMutex
	ReadFiles  map[string]bool
	EditFiles  map[string]bool
	WriteFiles map[string]bool
}

// NewFileStateCache creates an empty FileStateCache.
func NewFileStateCache() *FileStateCache {
	return &FileStateCache{
		ReadFiles:  make(map[string]bool),
		EditFiles:  make(map[string]bool),
		WriteFiles: make(map[string]bool),
	}
}

// ContentReplacementState tracks tool result budget replacements across turns.
// Mirrors toolResultStorage.ts:390-393.
type ContentReplacementState struct {
	// SeenIDs tracks all tool_use_ids that have passed through budget enforcement.
	// Once in SeenIDs, the tool result's fate is frozen (either replaced or kept).
	SeenIDs map[string]bool
	// Replacements maps tool_use_id → replacement text for results that were replaced.
	Replacements map[string]string
}

// NewContentReplacementState creates a fresh state.
func NewContentReplacementState() *ContentReplacementState {
	return &ContentReplacementState{
		SeenIDs:      make(map[string]bool),
		Replacements: make(map[string]string),
	}
}

// ContentReplacementRecord records a single tool result replacement.
type ContentReplacementRecord struct {
	ToolUseID    string
	Replaced     bool
	Replacements int
}

// ToolUseSummaryPromise represents an in-flight tool use summary generation.
// Mirrors the reference's Promise<ToolUseSummaryMessage | null> pattern.
type ToolUseSummaryPromise struct {
	Settled    bool
	Summary    string
	ToolUseIDs []string
	done       chan struct{} // closed when the result is ready
}

// Wait blocks until the summary is resolved (or context canceled).
func (p *ToolUseSummaryPromise) Wait() {
	if p.done != nil {
		<-p.done
	}
}

// Resolve marks the promise as settled with the given summary.
func (p *ToolUseSummaryPromise) Resolve(summary string) {
	p.Summary = summary
	p.Settled = true
	if p.done != nil {
		close(p.done)
	}
}

// NewToolUseSummaryPromise creates a pending promise.
func NewToolUseSummaryPromise(toolUseIDs []string) *ToolUseSummaryPromise {
	return &ToolUseSummaryPromise{
		ToolUseIDs: toolUseIDs,
		done:       make(chan struct{}),
	}
}

// CallModelOptions holds parameters for the model call.
type CallModelOptions struct {
	SystemPrompt     *schema.Message
	UserContext      map[string]string
	ThinkingConfig   *ThinkingConfig
	Tools            []*schema.ToolInfo
	Signal           context.Context
	Model            string
	FastMode         bool
	ToolChoice       string
	ForcedToolName   string
	IsNonInteractive bool
	FallbackModel    string
	QuerySource      QuerySource
	Agents           []AgentDefinition
	MaxOutputTokens  *int
	AgentID          string
	TaskBudget       *TaskBudget
	QueryTracking    *QueryTracking
	EffortValue      string
	AdvisorModel     string
	SkipCacheWrite   bool
}
