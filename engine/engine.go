package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/abietic/yhc/engine/budget"
	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/compact"
	enginecfg "github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/containment"
	promptctx "github.com/abietic/yhc/engine/context"
	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/internal/mediastore"
	"github.com/abietic/yhc/engine/internal/providerorigin"
	"github.com/abietic/yhc/engine/internal/workboard"
	"github.com/abietic/yhc/engine/memdir"
	modelcaps "github.com/abietic/yhc/engine/model"
	"github.com/abietic/yhc/engine/notify"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/services"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/skills"
	"github.com/abietic/yhc/engine/storage"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/engine/worktree"
	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

var resolveEmptyCWDTranscriptDir = defaultEmptyCWDTranscriptDir

func defaultEmptyCWDTranscriptDir() string {
	return filepath.Join(identity.ProjectDirName, "transcripts")
}

// QueryEngine owns the query lifecycle and session state for a conversation.
// One QueryEngine per conversation. Each SubmitMessage() starts a new turn.
// Mirrors QueryEngine.ts:184-1177.
type QueryEngine struct {
	config                       QueryEngineConfig
	mu                           sync.Mutex
	planMu                       sync.Mutex
	goalMu                       sync.Mutex
	goalControlMu                sync.Mutex
	goalProviderBoundary         sync.RWMutex
	mediaLifecycleMu             sync.RWMutex
	mediaLifecycleClosed         bool
	planState                    PlanState
	goalState                    *goalState
	goalTurnRuntime              *goalTurnRuntime
	goalService                  *goalService
	planActiveTurnID             string
	messages                     []*schema.Message
	totalUsage                   *schema.TokenUsage
	usageSummary                 transcript.UsageSummary
	abortController              *AbortController
	toolRegistry                 *tools.Registry
	transcript                   *transcript.Recorder
	transcriptCheckpointRequired bool
	fileStateSnapshotWriter      func(map[string]transcript.FileState) error
	initialTurnTranscriptStored  bool
	permissionDenials            []permission.Denial
	denialTracking               *permission.DenialTrackingState
	wrappedCanUseTool            CanUseToolFn
	contentReplacementState      *ContentReplacementState
	fileStateCache               *FileStateCache
	commandRegistry              *commands.Registry
	pluginDirs                   []string
	skillRegistry                *skills.SkillRegistry
	approvalTracker              *permission.ApprovalTracker
	permissionRules              *permission.RulesEngine
	permissionRegistry           *PermissionCoordinatorRegistry
	permissionCoordinator        *PermissionCoordinator
	permissionProjectIdentity    PermissionProjectIdentity
	permissionEngineID           string
	permissionRootSessionID      string
	permissionReviewAudit        *reviewAuditDispatcher
	resultStorage                *storage.ResultStorage
	deprecationWarning           string
	reasoningEffort              string
	modelBinding                 *session.PersistedModelBinding
	modelDispatchBlock           *ModelDispatchBlock
	modelCheckpointWriter        func(*session.PersistedModelBinding, string, string) error
	settingsWatcher              *enginecfg.SettingsWatcher
	runtimeState                 *RuntimeStateStore
	agentTranscriptSelector      *agentTranscriptSelector
	runtimeMu                    sync.Mutex
	runtimeSequences             map[string]uint64
	runtimeStateErr              error
	inputCoordinator             *RuntimeInputCoordinator
	inputCoordinatorErr          error
	projectGraphCheckpoint       *projectGraphCheckpointStore
	projectGraphCheckpointErr    error
	projectGraphHITLEnabled      bool
	subagentProgress             *subagentProgressTracker
	subagentProgressObserver     func(tools.AgentProgress)
	subagentProgressDescription  string
	subagentProgressToolUseID    string
	subagentProgressSeedPending  bool
	sessionState                 *session.StateMachine
	activityTracker              *session.ActivityTracker
	sessionService               *SessionService
	worktreeLifecycle            *worktree.Service
	worktreeRecoveryWarnings     []string
	backgroundServices           *services.BackgroundServices
	memoryStore                  *compact.MemoryStore
	agentRunner                  *tools.AgentRunner
	taskManager                  *tools.TaskManager
	workBoardShadow              *workboard.Shadow
	logicalWorkAdapter           *workboard.LogicalWorkAdapter
	logicalWorkErr               error
	shellManager                 *tools.ShellManager
	mcpManager                   *tools.MCPToolManager
	executionPolicy              *containment.Snapshot
	executionBindings            *containment.Bindings
	ownsAgentRunner              bool
	ownsShellManager             bool
	ownsMCPManager               bool
	webFetchModel                model.BaseChatModel
	subagentExecutor             *SubAgentExecutor
	subagentExecutorInstalled    bool
	sessionStartedAt             time.Time
	sessionGitBranch             string
	sessionStatus                string
	queryKernelSelection         sessionQueryKernelSelection
	asyncHookEvents              chan QueryEvent
	asyncHookDone                chan struct{}
	asyncHookMu                  sync.Mutex
	asyncHookWG                  sync.WaitGroup
	asyncHookClosed              bool
	asyncHookSubscribed          bool
	administrationOnly           bool
	restoreStaging               *restoreStagingLifecycle
	pendingRestoreActivation     *restoreStagingActivation
	supportedInvocationPolicy    bool
	closeOnce                    sync.Once
	permissionReviewMu           sync.Mutex
	permissionReviewWG           sync.WaitGroup
	permissionReviewClosed       bool
	permissionReviewPending      map[string]pendingPermissionReview
	permissionReviewSeen         map[string]struct{}
	permissionReviewSeenOrder    []string
	permissionReviewUserIntent   []string
	promptRouteGeneration        uint64
	promptRouteClosed            bool
	activePromptMedia            map[*turnMediaStore]struct{}
	sessionDeletionGate          *sessionDeletionGate
}

type sessionDeletionGate struct {
	deleting atomic.Bool
}

func (g *sessionDeletionGate) open() bool {
	return g == nil || !g.deleting.Load()
}

// QueryEngineConfig holds configuration for the QueryEngine.
type QueryEngineConfig struct {
	SessionID                   string
	ThreadID                    string
	ParentSessionID             string
	ParentThreadID              string
	ParentAgentID               string
	ParentToolUseID             string
	TranscriptDir               string
	InitialTurnTranscriptStored bool // AgentRunner durably stored the first child turn before executor entry.
	SessionCatalogPath          string
	LegacySessionCatalogPath    string // optional immutable default catalog used only for discovery
	CWD                         string
	MemoryProjectRoot           string // stable root shared by parent and child engines
	EnablePersistentMemory      bool
	Tools                       []*schema.ToolInfo
	ToolRegistry                *tools.Registry
	ToolSelection               *tools.ToolSelection
	SimpleTools                 bool
	ChatModel                   model.BaseChatModel
	CustomSystemPrompt          string
	AppendSystemPrompt          string
	Model                       string
	// ModelRole is the fixed provider-routing role for this engine. Empty means
	// main for root engines and preserves legacy child behavior.
	ModelRole string
	// ModelBinding seeds a child engine from its already committed execution
	// admission. The engine re-admits this exact binding before provider use.
	ModelBinding              *session.PersistedModelBinding
	FallbackModel             string
	ModelResolver             ModelResolver
	PromptCapabilityResolver  PromptCapabilityResolver
	MaxTurns                  int
	MaxBudgetUSD              float64
	TaskBudget                *TaskBudget
	TokenBudgetTracker        *budget.TokenBudget
	JSONSchema                map[string]any
	Clock                     func() time.Time
	CanUseTool                CanUseToolFn
	PermissionPrompt          PermissionPromptFn
	PermissionRegistry        *PermissionCoordinatorRegistry
	PermissionProjectRoot     string
	RootSessionID             string
	ApprovalTracker           *permission.ApprovalTracker
	RepeatedToolCallPrompt    RepeatedToolCallPromptFn
	PermissionMode            permission.Mode
	ExecutionPolicy           *containment.Snapshot
	ExecutionBindings         *containment.Bindings
	SandboxSelection          *SandboxSelection
	HookExecutor              *hooks.Executor
	SummaryModel              model.BaseChatModel         // small/fast model for tool use summaries
	SummaryModelSelector      string                      // truthful selector for trusted summary compatibility injection
	SubagentModel             model.BaseChatModel         // smaller model for read-only sub-agents (explore, plan)
	SubagentModelSelector     string                      // truthful selector for trusted Explore/Plan compatibility injection
	EmitToolUseSummaries      bool                        // feature gate for tool use summary generation
	NotifyManager             *notify.NotificationManager // notification dispatcher
	SkillRegistry             *skills.SkillRegistry
	AgentRunner               *tools.AgentRunner
	TaskManager               *tools.TaskManager
	WorkBoardShadow           bool // trusted, off-by-default P31.1a rollout switch
	logicalWorkAdapter        *workboard.LogicalWorkAdapter
	sessionDeletionGate       *sessionDeletionGate
	ShellManager              *tools.ShellManager
	MCPManager                *tools.MCPToolManager
	OwnsMCPManager            bool
	WebFetchModel             model.BaseChatModel
	FileStateCache            *FileStateCache
	AgentID                   string // non-empty when this engine is running a local/background sub-agent
	AgentName                 string // original addressable Agent name, when configured
	AgentRole                 string // original Agent type that owns prompt/tool policy
	AgentGeneration           int64  // current durable child execution generation
	goalBinding               *goalExecutionIdentity
	goalUsageReporter         *goalUsageReporter
	RuntimeState              *RuntimeStateStore
	AdditionalDirs            []string // extra working directories added via /add-dir
	PluginDirs                []string // explicit plugin roots; defaults to user and project plugin directories
	DisableBundledWorkflows   bool     // keep core commands while omitting the embedded prompt-workflow pack
	WorktreePath              string
	WorktreeBranch            string
	EnableLongSessionServices bool // interactive main-thread memory, extraction, and auto-dream
	CommandEntrypoint         commands.Entrypoint
	WorkspaceCommandRunner    WorkspaceCommandRunner
	WorkspaceCommandTimeout   time.Duration
	ApprovalReviewShadow      bool
	ApprovalReviewer          permission.ApprovalReviewer
	ApprovalReviewerRoute     permission.ApprovalReviewerRoute
	ApprovalReviewTimeout     time.Duration
	ApprovalReviewAudit       permission.ReviewAuditSink
	GoalCapability            *GoalCapabilityConfig
	workBoardShadow           *workboard.Shadow
}

// GoalCapabilityConfig carries the production capability decision into the
// engine. Nil preserves the P24.3 internal seam for focused compatibility
// tests; production construction always supplies an explicit enabled/disabled
// value.
type GoalCapabilityConfig struct {
	Enabled            bool
	DefaultTokenBudget *uint64
	ACPNegotiated      bool
}

// NewQueryEngine creates a new QueryEngine.
func NewQueryEngine(config QueryEngineConfig) *QueryEngine {
	return newQueryEngineWithOptions(config, queryEngineConstructionOptions{})
}

type queryEngineConstructionOptions struct {
	foregroundChild bool
	administration  bool
	restoreStaging  bool
}

func newForegroundChildQueryEngine(config QueryEngineConfig) *QueryEngine {
	return newQueryEngineWithOptions(config, queryEngineConstructionOptions{
		foregroundChild: true,
	})
}

// NewRestoreStagingQueryEngine constructs a QueryEngine whose first restore is
// non-persisting until CommitRestoreStaging succeeds. Callers must abort the
// staging owner when delivery or setup fails.
func NewRestoreStagingQueryEngine(config QueryEngineConfig) *QueryEngine {
	return newQueryEngineWithOptions(config, queryEngineConstructionOptions{
		restoreStaging: true,
	})
}

func newQueryEngineWithOptions(
	config QueryEngineConfig,
	construction queryEngineConstructionOptions,
) *QueryEngine {
	supportedInvocationPolicy := config.CommandEntrypoint != "" ||
		strings.TrimSpace(config.AgentID) != ""
	policyInstalled := invocationPolicyBoundaryFor(
		config.CanUseTool,
		config.PermissionPrompt,
	) == invocationPolicyInstalled ||
		config.PermissionMode == permission.ModeAuto &&
			supportedInvocationPolicy
	if config.ToolRegistry == nil && policyInstalled {
		config.ToolRegistry = tools.NewRegistry()
		tools.RegisterDefaults(config.ToolRegistry)
	}
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	if config.HookExecutor == nil {
		config.HookExecutor = hooks.NewExecutor()
	}
	config.Clock = clock
	config.goalBinding = cloneGoalExecutionIdentity(config.goalBinding)
	config.GoalCapability = cloneGoalCapabilityConfig(config.GoalCapability)
	if config.SessionID == "" {
		config.SessionID = fmt.Sprintf("session-%d", clock().UnixNano())
	}
	if config.ThreadID == "" {
		config.ThreadID = config.SessionID
	}
	if config.RootSessionID == "" {
		config.RootSessionID = config.SessionID
	}
	if config.CommandEntrypoint == "" {
		config.CommandEntrypoint = commands.EntrypointHeadless
	}
	bindExecutionPolicy(&config)
	if config.PermissionProjectRoot == "" {
		config.PermissionProjectRoot = config.CWD
	}
	if config.TranscriptDir == "" {
		if config.CWD == "" {
			config.TranscriptDir = resolveEmptyCWDTranscriptDir()
		} else {
			config.TranscriptDir = filepath.Join(config.CWD, identity.ProjectDirName, "transcripts")
		}
	}
	if config.MemoryProjectRoot == "" {
		config.MemoryProjectRoot = config.CWD
	}
	if config.MaxTurns < 0 {
		panic("engine: MaxTurns must be zero (unlimited) or positive")
	}
	if config.WorkspaceCommandRunner == nil {
		config.WorkspaceCommandRunner = execWorkspaceCommandRunner{}
	}
	if config.WorkspaceCommandTimeout <= 0 {
		config.WorkspaceCommandTimeout = workspaceCommandTimeout
	}

	runtimeState := config.RuntimeState
	if runtimeState == nil {
		runtimeState = NewRuntimeStateStore()
	}
	memoryDir := filepath.Join(config.TranscriptDir, config.SessionID)
	memoryLimit := 100
	if config.EnableLongSessionServices {
		memoryDir = filepath.Join(config.TranscriptDir, "memory")
		memoryLimit = 500
	}
	memoryStore := compact.NewMemoryStore(memoryDir, memoryLimit)
	fileStateCache := config.FileStateCache
	if fileStateCache == nil {
		fileStateCache = NewFileStateCache()
	}
	approvalTracker := config.ApprovalTracker
	loadApprovalTracker := approvalTracker == nil
	if approvalTracker == nil {
		approvalTracker = permission.NewApprovalTracker()
	}
	permissionRegistry := config.PermissionRegistry
	if permissionRegistry == nil {
		permissionRegistry = defaultPermissionCoordinatorRegistry
	}
	config.ApprovalTracker = approvalTracker
	config.PermissionRegistry = permissionRegistry
	agentRunner := config.AgentRunner
	ownsAgentRunner := agentRunner == nil
	if agentRunner == nil {
		agentRunner = tools.NewAgentRunner(4)
	}
	deletionGate := config.sessionDeletionGate
	if deletionGate == nil {
		deletionGate = &sessionDeletionGate{}
		config.sessionDeletionGate = deletionGate
	}
	agentRunner.SetLifecycleAdmission(deletionGate.open)
	taskManager := config.TaskManager
	if taskManager == nil {
		taskManager = tools.NewTaskManager()
	}
	var logicalWorkAdapter *workboard.LogicalWorkAdapter
	var logicalWorkErr error
	logicalWorkScope := tools.TodoScope{
		SessionID: config.SessionID,
		AgentID:   config.AgentID,
	}
	boundTaskAuthority, taskAuthorityBound := taskManager.BoundAuthority()
	switch {
	case config.logicalWorkAdapter != nil:
		logicalWorkAdapter = config.logicalWorkAdapter
		if !taskManager.AuthorityBound() {
			logicalWorkErr = fmt.Errorf(
				"workboard authority: shared TaskManager is not bound",
			)
		} else {
			logicalWorkErr = logicalWorkAdapter.RegisterTodoScope(
				logicalWorkScope,
				nil,
			)
		}
	case construction.administration || construction.restoreStaging:
		store, storeErr := workboard.NewStore(workboard.StoreConfig{
			Dir:       config.TranscriptDir,
			SessionID: config.SessionID,
		})
		if storeErr == nil {
			_, storeErr = store.Inspect()
		}
		logicalWorkErr = storeErr
	case taskAuthorityBound:
		var ok bool
		logicalWorkAdapter, ok = boundTaskAuthority.(*workboard.LogicalWorkAdapter)
		if !ok {
			logicalWorkErr = fmt.Errorf(
				"workboard authority: shared TaskManager has an incompatible authority",
			)
		} else {
			logicalWorkErr = logicalWorkAdapter.RegisterTodoScope(
				logicalWorkScope,
				nil,
			)
		}
	default:
		logicalWorkAdapter, logicalWorkErr = workboard.BindLogicalWorkAdapter(
			workboard.AdapterConfig{
				SessionID:          config.SessionID,
				Dir:                config.TranscriptDir,
				LeaderScope:        logicalWorkScope,
				Clock:              config.Clock,
				SettlementSnapshot: workboardSettlementSnapshot(agentRunner),
				MutationAdmission:  deletionGate.open,
			},
			taskManager,
		)
	}
	if logicalWorkAdapter != nil {
		logicalWorkAdapter.BindSettlementSnapshot(
			workboardSettlementSnapshot(agentRunner),
		)
		logicalWorkAdapter.BindMutationAdmission(deletionGate.open)
	}
	workBoardShadow := config.workBoardShadow
	if workBoardShadow == nil &&
		config.WorkBoardShadow &&
		strings.TrimSpace(config.AgentID) == "" &&
		!construction.administration &&
		!construction.restoreStaging {
		workBoardShadow = workboard.NewShadow(workboard.Config{
			SessionID: config.SessionID,
			Dir:       config.TranscriptDir,
		})
	}
	shellManager := config.ShellManager
	ownsShellManager := shellManager == nil
	guestBinding := config.ExecutionBindings.Guest()
	if shellManager == nil {
		var err error
		shellManager, err = tools.NewShellManagerWithGuestBinding(guestBinding)
		if err != nil {
			panic(fmt.Sprintf("engine: initialize Guest shell binding: %v", err))
		}
	} else if err := shellManager.BindGuestBinding(guestBinding); err != nil {
		panic(fmt.Sprintf("engine: bind Guest shell identity: %v", err))
	}
	if guestBinding.Availability() == containment.BindingAvailable && guestBinding.AdapterFamily() == containment.AdapterDarwinSeatbelt {
		proof := shellManager.GuestExecutionProof()
		if proof.BindingDigest != guestBinding.Digest() || proof.PolicyDigest != guestBinding.PolicyDigest() {
			panic("engine: Guest execution proof does not match binding")
		}
	}
	mcpManager := config.MCPManager
	ownsMCPManager := mcpManager == nil || config.OwnsMCPManager
	stdioMCPBinding := config.ExecutionBindings.StdioMCP()
	if mcpManager == nil {
		if construction.administration || construction.restoreStaging {
			mcpManager = tools.NewMCPToolManager()
		} else {
			mcpManager, _ = tools.InitMCPManagerWithBinding(context.Background(), config.CWD, config.ToolRegistry, stdioMCPBinding)
		}
	}
	if mcpManager == nil {
		mcpManager = tools.NewMCPToolManager()
	}
	if digest := mcpManager.ExecutionBindingDigest(); digest == "" {
		if policyDigest := mcpManager.ExecutionPolicyDigest(); policyDigest != "" && policyDigest != stdioMCPBinding.PolicyDigest() {
			panic("engine: stdio MCP binding replacement rejected")
		}
		if err := mcpManager.BindExecutionBinding(stdioMCPBinding); err != nil {
			panic(fmt.Sprintf("engine: bind stdio MCP identity: %v", err))
		}
	} else if digest != stdioMCPBinding.Digest() {
		panic("engine: stdio MCP binding replacement rejected")
	}
	if err := config.HookExecutor.BindExecutionBinding(config.ExecutionBindings.ShellHooks()); err != nil {
		panic(fmt.Sprintf("engine: bind shell-hook identity: %v", err))
	}
	eng := &QueryEngine{
		config:                      config,
		messages:                    make([]*schema.Message, 0),
		totalUsage:                  &schema.TokenUsage{},
		usageSummary:                transcript.UsageSummary{},
		toolRegistry:                config.ToolRegistry,
		transcript:                  transcript.NewRecorder(config.SessionID, config.TranscriptDir),
		initialTurnTranscriptStored: config.InitialTurnTranscriptStored,
		permissionDenials:           make([]permission.Denial, 0),
		denialTracking:              permission.NewDenialTrackingState(),
		contentReplacementState:     NewContentReplacementState(),
		fileStateCache:              fileStateCache,
		skillRegistry:               config.SkillRegistry,
		approvalTracker:             approvalTracker,
		permissionRegistry:          permissionRegistry,
		permissionEngineID:          generateUUID(),
		permissionRootSessionID:     config.RootSessionID,
		planState:                   initialPlanState(config),
		resultStorage:               storage.NewResultStorage(filepath.Join(config.TranscriptDir, config.SessionID)),
		runtimeState:                runtimeState,
		runtimeSequences:            make(map[string]uint64),
		sessionState:                session.NewStateMachine(),
		activityTracker:             session.NewActivityTracker(),
		memoryStore:                 memoryStore,
		agentRunner:                 agentRunner,
		taskManager:                 taskManager,
		workBoardShadow:             workBoardShadow,
		logicalWorkAdapter:          logicalWorkAdapter,
		logicalWorkErr:              logicalWorkErr,
		shellManager:                shellManager,
		mcpManager:                  mcpManager,
		executionPolicy:             config.ExecutionPolicy,
		executionBindings:           config.ExecutionBindings,
		ownsAgentRunner:             ownsAgentRunner,
		ownsShellManager:            ownsShellManager,
		ownsMCPManager:              ownsMCPManager,
		sessionStartedAt:            clock().UTC(),
		sessionGitBranch:            detectSessionGitBranch(config.CWD),
		sessionStatus:               "idle",
		queryKernelSelection: sessionQueryKernelSelection{
			version: queryKernelVersionProjectGraph,
			stage:   queryKernelStageFull,
			err: fmt.Errorf(
				"session project Graph query kernel selection is not initialized",
			),
		},
		asyncHookEvents:           make(chan QueryEvent, 64),
		asyncHookDone:             make(chan struct{}),
		permissionReviewPending:   make(map[string]pendingPermissionReview),
		permissionReviewSeen:      make(map[string]struct{}),
		promptRouteGeneration:     1,
		activePromptMedia:         make(map[*turnMediaStore]struct{}),
		sessionDeletionGate:       deletionGate,
		administrationOnly:        construction.administration,
		restoreStaging:            newRestoreStagingLifecycle(construction.restoreStaging),
		supportedInvocationPolicy: supportedInvocationPolicy,
	}
	if config.ApprovalReviewAudit != nil {
		eng.permissionReviewAudit = newReviewAuditDispatcher(
			reviewAuditDispatcherOptions{
				Capacity: permissionReviewAuditQueueCapacity,
				Sink:     config.ApprovalReviewAudit,
			},
		)
	}
	if config.ModelBinding != nil {
		eng.initializeAdmittedModelBinding(config.ModelBinding)
	} else {
		eng.initializeModelBinding()
	}
	eng.sessionService = &SessionService{engine: eng}
	eng.goalService = &goalService{engine: eng}
	eng.worktreeLifecycle = eng.newWorktreeLifecycleService(
		config.MemoryProjectRoot,
	)
	if !construction.administration && !construction.restoreStaging {
		eng.worktreeRecoveryWarnings = eng.restoreWorktreeRecoveryMetadata(
			context.Background(),
		)
	}
	eng.permissionCoordinator, eng.permissionProjectIdentity = permissionRegistry.acquire(
		config.PermissionProjectRoot,
		eng.permissionEngineID,
	)
	config.HookExecutor.SetAsyncShellCompletionHandler(eng.handleAsyncShellHookCompletion)
	if eng.skillRegistry == nil && !construction.administration {
		eng.skillRegistry, _ = skills.LoadDefaultSkills(config.CWD)
	}
	if eng.skillRegistry != nil && eng.toolRegistry != nil {
		eng.toolRegistry.Register(tools.SkillToolForRegistry(eng.skillRegistry))
	}
	eng.webFetchModel = config.WebFetchModel
	if eng.webFetchModel == nil {
		eng.webFetchModel = tools.WebFetchSideModel
	}
	if config.SessionCatalogPath != "" && !construction.restoreStaging {
		_ = session.RegisterSessionRoot(
			config.SessionCatalogPath,
			config.CWD,
			config.TranscriptDir,
			clock(),
		)
	}
	if !construction.administration {
		eng.reloadPermissionRules()
		// Load persisted approvals from disk.
		if loadApprovalTracker {
			if approvalsPath, err := permission.ApprovalStorePath(config.PermissionProjectRoot); err == nil {
				_ = eng.approvalTracker.LoadFrom(approvalsPath)
			}
		}
	}
	eng.wrappedCanUseTool = eng.wrapCanUseTool(config.CanUseTool)

	// Load and register shell hooks from .claude/hooks.json (or .eino-agent/hooks.json).
	if config.HookExecutor != nil &&
		!construction.administration &&
		!construction.restoreStaging {
		shellCfg, _ := hooks.LoadShellHooks(config.CWD)
		if shellCfg != nil && (len(shellCfg.PreToolHooks) > 0 || len(shellCfg.PostToolHooks) > 0 || len(shellCfg.UserPromptHooks) > 0) {
			config.HookExecutor.RegisterShellHooks(shellCfg)
		}
	}

	var loadedTranscript *transcript.LoadResult
	if loaded, err := eng.transcript.LoadFull(); err == nil {
		loadedTranscript = loaded
		eng.restoreUsageSummary(loaded.Usage)
		if len(loaded.Messages) > 0 {
			repaired, _ := repairLoadedMessagesWithInterruption(loaded.Messages)
			promptErr := eng.transcript.RebindPromptRecords(
				loaded.Messages,
				repaired,
			)
			originErr := eng.transcript.RebindAssistantOrigins(
				loaded.Messages,
				repaired,
			)
			if promptErr != nil || originErr != nil {
				eng.messages = append(
					[]*schema.Message(nil),
					loaded.Messages...,
				)
			} else {
				eng.messages = repaired
			}
		}
		if len(loaded.Replacements) > 0 {
			eng.contentReplacementState = ReconstructContentReplacementState(
				eng.messages, loaded.Replacements,
			)
		}
		if len(loaded.FileSnapshots) > 0 {
			eng.reconstructFileStateCache(loaded.FileSnapshots)
		}
	}
	inputCoordinator, inputCoordinatorErr := NewRuntimeInputCoordinator(
		RuntimeInputCoordinatorConfig{
			SessionID: config.SessionID,
			ThreadID:  config.ThreadID,
			AgentID:   config.AgentID,
			Path: RuntimeInputPersistencePath(
				eng.transcript.Path(),
			),
			Clock:                    config.Clock,
			DeliveryLookup:           eng.transcript.RuntimeItemDeliveryCoverage,
			DeferRecoveryPersistence: construction.restoreStaging,
			mediaStore: mediastore.New(
				eng.transcript.Path() + ".media",
			),
		},
		runtimeItemIDsFromLoadedTranscript(loadedTranscript),
	)
	eng.inputCoordinator = inputCoordinator
	eng.inputCoordinatorErr = inputCoordinatorErr
	eng.projectGraphCheckpoint, eng.projectGraphCheckpointErr = newProjectGraphCheckpointStore(
		projectGraphCheckpointPath(eng.transcript.Path()),
		RuntimeInputScope{
			SessionID: config.SessionID,
			ThreadID:  config.ThreadID,
			AgentID:   config.AgentID,
		},
		config.Clock,
	)
	if !construction.administration {
		eng.ensureCommandRegistry()
		eng.pluginDirs = resolvePromptCommandDirs(config.PluginDirs, config.CWD)
		_, _ = eng.ReloadPromptCommands()
	}

	// Check for model deprecation and store warning for callers.
	if eng.config.Model != "" {
		resolvedModel := eng.config.Model
		if eng.modelBinding != nil &&
			eng.modelBinding.ValidateV1() == nil {
			resolvedModel = eng.modelBinding.APIModel
		}
		eng.deprecationWarning = modelcaps.CheckDeprecation(resolvedModel)
	}

	// Start settings hot-reload watcher.
	if !construction.administration && !construction.restoreStaging {
		eng.settingsWatcher = enginecfg.NewSettingsWatcher(config.PermissionProjectRoot, 0, func(newSettings *enginecfg.Settings) {
			// Reload permission rules on settings change.
			eng.reloadPermissionRules()
		})
		eng.settingsWatcher.Start()
	}

	// Register the sub-agent executor so the Agent tool can spawn nested query engines.
	if config.ChatModel != nil && config.ToolRegistry != nil {
		subExec := NewSubAgentExecutor(config.ChatModel, config.ToolRegistry, config.CWD)
		subExec.MemoryProjectRoot = config.MemoryProjectRoot
		subExec.EnablePersistentMemory = config.EnablePersistentMemory
		subExec.PermissionMode = config.PermissionMode
		subExec.ParentCanUseTool = config.CanUseTool
		subExec.ParentPermissionPrompt = config.PermissionPrompt
		subExec.ParentRepeatedToolCallPrompt = config.RepeatedToolCallPrompt
		subExec.ParentApprovals = eng.approvalTracker
		subExec.PermissionRegistry = eng.permissionRegistry
		subExec.PermissionProjectRoot = config.PermissionProjectRoot
		subExec.RootSessionID = config.RootSessionID
		subExec.ParentModelName = eng.config.Model
		subExec.parentModelCallSnapshot = eng.modelCallIdentitySnapshot
		subExec.ModelResolver = config.ModelResolver
		subExec.PromptCapabilityResolver = config.PromptCapabilityResolver
		subExec.RuntimeState = eng.runtimeState
		subExec.ToolSelection = config.ToolSelection
		subExec.SimpleTools = config.SimpleTools
		subExec.AgentRunner = eng.agentRunner
		subExec.TaskManager = eng.taskManager
		subExec.workBoardShadow = eng.workBoardShadow
		subExec.logicalWorkAdapter = eng.logicalWorkAdapter
		subExec.sessionDeletionGate = deletionGate
		subExec.MCPManager = eng.mcpManager
		subExec.ExecutionPolicy = eng.executionPolicy
		subExec.ExecutionBindings = eng.executionBindings
		subExec.SkillRegistry = eng.skillRegistry
		subExec.WebFetchModel = eng.webFetchModel
		subExec.goalBindingSnapshot = eng.currentGoalExecutionIdentity
		subExec.goalUsageReporterFactory = eng.bindGoalUsageReporterForChild
		if strings.TrimSpace(config.AgentID) == "" {
			subExec.beginGoalChildWait = eng.goalService.beginForegroundChildWait
		}
		subExec.bindAgentWorktreeRuntime(
			eng.worktreeLifecycle,
			config.CWD,
			config.SessionID,
		)
		subExec.ParentFileState = eng.fileStateCache
		if !construction.restoreStaging {
			_ = subExec.InitializeAgentMemorySnapshots()
		}
		if config.SubagentModel != nil {
			subExec.SubagentModel = config.SubagentModel
			subExec.SubagentModelSelector = config.SubagentModelSelector
		}
		if eng.ownsAgentRunner || !eng.agentRunner.HasExecutor() {
			eng.agentRunner.SetExecutor(subExec)
			eng.subagentExecutorInstalled = true
		}
		eng.subagentExecutor = subExec

		// Wire dynamic agent type descriptions into the Agent tool's prompt.
		agentTypes := make([]tools.AgentTypeInfo, 0, len(subExec.AgentDefinitions))
		for _, def := range subExec.AgentDefinitions {
			agentTypes = append(agentTypes, tools.AgentTypeInfo{
				Name:      def.Name,
				WhenToUse: def.WhenToUse,
				Tools:     def.Tools,
			})
		}
		tools.SetAgentTypeDescriptionsForRegistry(eng.toolRegistry, agentTypes)
	}

	// Start background services (session memory, auto-dream).
	if config.ChatModel != nil &&
		config.AgentID == "" &&
		config.EnableLongSessionServices &&
		!construction.restoreStaging {
		eng.backgroundServices = newEngineBackgroundServices(
			config.ChatModel,
			config.TranscriptDir,
			config.SessionID,
			eng.memoryStore,
			eng.callBackgroundProvider,
		)
		eng.backgroundServices.Start()
	}
	if construction.administration {
		// Administration hosts do not submit turns. Keep the project-owned
		// version/stage identity without compiling the shared Graph; an explicit
		// resume or fork activation validates the selected durable session below.
		eng.queryKernelSelection = sessionQueryKernelSelection{
			version: queryKernelVersionProjectGraph,
			stage:   queryKernelStageFull,
		}
	} else if construction.foregroundChild {
		eng.queryKernelSelection = initialForegroundChildSessionQueryKernelSelection(
			config.AgentID,
			loadedTranscript,
		)
	} else {
		eng.queryKernelSelection = initialSessionQueryKernelSelection(
			loadedTranscript,
		)
	}
	eng.projectGraphHITLEnabled = projectGraphDurableInterruptEnabled(
		config.PermissionPrompt,
		eng.queryKernelSelection,
	)
	return eng
}

func workboardSettlementSnapshot(
	runner *tools.AgentRunner,
) func(
	[]workboard.ExecutionSettlementKey,
) ([]workboard.ExecutionSettlement, error) {
	return func(
		keys []workboard.ExecutionSettlementKey,
	) ([]workboard.ExecutionSettlement, error) {
		runnerKeys := make([]tools.AgentExecutionKey, len(keys))
		for i, key := range keys {
			if key.Generation > math.MaxInt64 {
				return nil, fmt.Errorf(
					"workboard authority: execution generation exceeds runner range",
				)
			}
			runnerKeys[i] = tools.AgentExecutionKey{
				AgentID:    key.AgentID,
				Generation: int64(key.Generation),
			}
		}
		runnerSettlements := runner.AgentExecutionSettlements(runnerKeys)
		settlements := make(
			[]workboard.ExecutionSettlement,
			len(runnerSettlements),
		)
		for i, settlement := range runnerSettlements {
			state := settlement.State
			settlements[i] = workboard.ExecutionSettlement{
				Key: workboard.ExecutionSettlementKey{
					AgentID: settlement.Key.AgentID,
					Generation: uint64(
						settlement.Key.Generation,
					),
				},
				Settled:       settlement.Settled,
				Reserved:      state == "reserved",
				Live:          state == "live",
				CancelPending: state == "cancel_pending",
				Unresolved: state == "invalid" ||
					state == "runner_unavailable" ||
					state == "durable_state_unavailable" ||
					state == "future" ||
					state == "not_terminal",
			}
		}
		return settlements, nil
	}
}

// SubmitMessage sends a user prompt and returns an event channel.
// This is the primary public API.
func (e *QueryEngine) SubmitMessage(
	ctx context.Context,
	prompt string,
) (<-chan QueryEvent, Terminal) {
	e.recordPermissionReviewUserIntent(prompt)
	return e.submitMessage(ctx, prompt, nil, nil)
}

// SubmitPromptInput admits a literal, ordered typed prompt. Unlike string
// entrypoints, it never interprets slash commands or @model overrides.
func (e *QueryEngine) SubmitPromptInput(
	ctx context.Context,
	input UntrustedPromptInput,
) (<-chan QueryEvent, Terminal) {
	admitted, err := e.admitPromptInput(ctx, input, "")
	if err != nil {
		return closedPromptInputError(err)
	}
	return e.submitMessageWithRuntimeItem(
		ctx,
		admitted.textForHook(),
		nil,
		nil,
		nil,
		admitted,
	)
}

// PendingAgentMemorySnapshots returns custom-agent seed updates awaiting an
// explicit keep or replace decision.
func (e *QueryEngine) PendingAgentMemorySnapshots() map[string]AgentSnapshotUpdate {
	if e == nil || e.subagentExecutor == nil {
		return map[string]AgentSnapshotUpdate{}
	}
	return e.subagentExecutor.PendingAgentMemorySnapshots()
}

// ResolveAgentMemorySnapshot applies an explicit pending snapshot decision.
func (e *QueryEngine) ResolveAgentMemorySnapshot(agentType string, resolution AgentSnapshotResolution) error {
	if e == nil || e.subagentExecutor == nil {
		return fmt.Errorf("agent memory snapshots are unavailable")
	}
	return e.subagentExecutor.ResolveAgentMemorySnapshot(agentType, resolution)
}

// UserImage is an immutable image snapshot submitted with a user prompt.
// Base64Data contains raw base64 without a data-URL prefix. Name and Path are
// untrusted entrypoint provenance and never enter model-visible or durable
// prompt metadata.
type UserImage struct {
	Name       string
	Path       string
	MIMEType   string
	Base64Data string
}

// SubmitMessageWithImages submits a multimodal user turn while preserving the
// string-only SubmitMessage API for all existing callers.
func (e *QueryEngine) SubmitMessageWithImages(
	ctx context.Context,
	prompt string,
	images []UserImage,
) (<-chan QueryEvent, Terminal) {
	images = append([]UserImage(nil), images...)
	if err := validateUserImages(images); err != nil {
		terminal := Terminal{Reason: TerminalImageError, Err: err}
		events := make(chan QueryEvent, 1)
		events <- QueryEvent{Type: EventTerminal, TerminalInfo: &terminal}
		close(events)
		return events, terminal
	}
	if len(images) == 0 ||
		commands.IsCommand(strings.TrimSpace(prompt)) {
		e.recordPermissionReviewUserIntent(prompt)
		return e.submitMessage(ctx, prompt, nil, images)
	}
	processed := e.processUserInput(prompt)
	parts := make([]UntrustedPromptPart, 0, len(images)+1)
	if processed.Prompt != "" {
		parts = append(parts, NewPromptTextPart(processed.Prompt))
	}
	for _, image := range images {
		parts = append(parts, NewPromptImagePart(
			image.Base64Data,
			image.MIMEType,
			PromptImageDetailAuto,
		))
	}
	admitted, err := e.admitPromptInput(
		ctx,
		NewUntrustedPromptInput(parts...),
		processed.ModelOverride,
	)
	if err != nil {
		return closedPromptInputError(err)
	}
	return e.submitMessageWithRuntimeItem(
		ctx,
		admitted.textForHook(),
		nil,
		nil,
		nil,
		admitted,
	)
}

func closedPromptInputError(err error) (<-chan QueryEvent, Terminal) {
	terminal := Terminal{Reason: TerminalPromptInputError, Err: err}
	events := make(chan QueryEvent, 1)
	events <- QueryEvent{Type: EventTerminal, TerminalInfo: &terminal}
	close(events)
	return events, terminal
}

// SubmitMessageWithMetadata submits a user message with stable transport
// metadata. Agent resume uses command_uuid to reconcile optimistic/replayed
// transcript rows; ordinary callers should use SubmitMessage.
func (e *QueryEngine) SubmitMessageWithMetadata(
	ctx context.Context,
	prompt string,
	extra map[string]any,
) (<-chan QueryEvent, Terminal) {
	e.recordPermissionReviewUserIntent(prompt)
	return e.submitMessage(ctx, prompt, extra, nil)
}

func (e *QueryEngine) submitMessage(
	ctx context.Context,
	prompt string,
	extra map[string]any,
	images []UserImage,
) (<-chan QueryEvent, Terminal) {
	return e.submitMessageWithRuntimeItem(ctx, prompt, extra, images, nil, nil)
}

func (e *QueryEngine) submitMessageWithRuntimeItem(
	ctx context.Context,
	prompt string,
	extra map[string]any,
	images []UserImage,
	runtimeItem *RuntimeItem,
	admittedPrompt *AdmittedPromptInput,
) (<-chan QueryEvent, Terminal) {
	ownsAdmittedPrompt := admittedPrompt != nil
	defer func() {
		if ownsAdmittedPrompt {
			e.releaseAdmittedPrompt(admittedPrompt)
		}
	}()
	events := make(chan QueryEvent, 256)
	e.mu.Lock()
	logicalWorkErr := e.logicalWorkErr
	e.mu.Unlock()
	if logicalWorkErr != nil {
		terminal := Terminal{
			Reason: TerminalModelError,
			Err:    logicalWorkErr,
		}
		events <- QueryEvent{Type: EventTerminal, TerminalInfo: &terminal}
		close(events)
		return events, terminal
	}
	if err := e.ensureRestoreStagingCommitted(); err != nil {
		terminal := Terminal{
			Reason: TerminalModelError,
			Err:    err,
		}
		events <- QueryEvent{Type: EventTerminal, TerminalInfo: &terminal}
		close(events)
		return events, terminal
	}
	turnID := generateUUID()
	initialRuntimeItemID := runtimeItemIDFromMetadata(extra)
	isDurableRuntimePrompt := runtimeItem != nil &&
		runtimeItem.Kind == RuntimeItemUserPrompt &&
		runtimeItem.UserPrompt != nil &&
		runtimeItem.UserPrompt.durablePrompt != nil
	if isDurableRuntimePrompt {
		turnID = strings.TrimSpace(
			runtimeItem.UserPrompt.durablePrompt.TurnID,
		)
	}
	isGraphResume := runtimeItem != nil &&
		runtimeItem.Kind == RuntimeItemPermissionDecision &&
		runtimeItem.PermissionDecision != nil
	isGoalContinuation := runtimeItem != nil &&
		runtimeItem.Kind == RuntimeItemGoalContinuation &&
		runtimeItem.GoalContinuation != nil
	if isGoalContinuation {
		turnID = strings.TrimSpace(
			runtimeItem.GoalContinuation.ContinuationTurnID,
		)
	}
	emitter := newTurnEventEmitter(ctx, e, events, turnID)
	if runtimeItem != nil {
		initialRuntimeItemID = strings.TrimSpace(runtimeItem.ID)
	}

	isCommand := admittedPrompt == nil &&
		!isGraphResume &&
		initialRuntimeItemID == "" &&
		commands.IsCommand(strings.TrimSpace(prompt))
	var turnKernel queryKernel
	if !isNewSessionEscapeCommand(isCommand, prompt) {
		var kernelErr error
		turnKernel, kernelErr = e.queryKernelForTurn(ctx)
		if kernelErr != nil {
			e.releaseRuntimeItem(initialRuntimeItemID)
			terminal := Terminal{
				Reason: TerminalModelError,
				Err:    kernelErr,
			}
			go func() {
				defer close(events)
				// Kernel admission must precede command dispatch, input
				// overrides, reducer mutation, and transcript writes. /new is
				// the sole exception because it creates a fresh Graph Session
				// without mutating the rejected source transcript.
				if isCommand {
					events <- QueryEvent{
						Type: EventCommandResult,
						CommandResult: e.commandAdmissionFailure(
							prompt,
							kernelErr,
						),
					}
				}
				events <- QueryEvent{
					Type:         EventTerminal,
					TerminalInfo: &terminal,
				}
			}()
			return events, terminal
		}
	}

	if isCommand {
		if _, err := e.beginPlanTurn(turnID); err != nil {
			terminal := Terminal{
				Reason: TerminalModelError,
				Err:    err,
			}
			result := e.commandAdmissionFailure(prompt, err)
			go func() {
				defer close(events)
				// The command was never admitted as a runtime turn. Publish an
				// unsequenced typed rejection to the caller without corrupting
				// the active turn's reducer identity or sequence.
				events <- QueryEvent{
					Type:          EventCommandResult,
					CommandResult: result,
				}
				events <- QueryEvent{
					Type:         EventTerminal,
					TerminalInfo: &terminal,
				}
			}()
			return events, terminal
		}
		go func() {
			defer close(events)
			defer emitter.Close()
			cleanedUp := false
			defer func() {
				if cleanedUp {
					return
				}
				e.activityTracker.Stop(session.ActivityAPICall)
				e.sessionState.TransitionToIdle()
				e.endPlanTurn(turnID)
			}()
			e.sessionState.TransitionToRunning()
			e.activityTracker.Start(session.ActivityAPICall)

			execution := e.executeCommand(ctx, prompt, turnID, emitter.Emit)
			if execution.attachment != nil {
				emitter.Emit(*execution.attachment)
			}
			emitter.Emit(QueryEvent{
				Type:          EventCommandResult,
				CommandResult: execution.result,
			})
			// Terminal visibility is the caller's follow-up boundary. Release
			// runtime admission and observable session activity before
			// publishing it, while retaining the submit-time event identity.
			e.activityTracker.Stop(session.ActivityAPICall)
			e.sessionState.TransitionToIdle()
			e.endPlanTurn(turnID)
			cleanedUp = true
			emitter.Emit(QueryEvent{
				Type:         EventTerminal,
				TerminalInfo: &Terminal{Reason: TerminalCompleted},
			})
		}()
		return events, Terminal{Reason: TerminalCompleted}
	}

	if admittedPrompt == nil && !isGraphResume && !isGoalContinuation {
		// Process input through the pipeline (slash commands, overrides, budget).
		processed := e.processUserInput(prompt)

		// Apply model override if present.
		if processed.ModelOverride != "" {
			e.mu.Lock()
			if e.config.Model != processed.ModelOverride {
				e.promptRouteGeneration++
			}
			e.config.Model = processed.ModelOverride
			e.mu.Unlock()
		}

		// Use the processed prompt (with overrides stripped).
		prompt = processed.Prompt
	}

	planTurn, planTurnErr := e.beginPlanTurn(turnID)
	if planTurnErr != nil {
		e.releaseRuntimeItem(initialRuntimeItemID)
		terminal := Terminal{
			Reason: TerminalModelError,
			Err:    planTurnErr,
		}
		go func() {
			defer close(events)
			// This input was not admitted as a runtime turn. Keep the
			// rejection out of the active turn's reducer and sequence.
			events <- QueryEvent{
				Type:         EventTerminal,
				TerminalInfo: &terminal,
			}
		}()
		return events, terminal
	}
	if admittedPrompt != nil {
		if err := e.activateAdmittedPrompt(
			admittedPrompt,
			planTurn,
		); err != nil {
			e.releaseRuntimeItem(initialRuntimeItemID)
			terminal := Terminal{
				Reason: TerminalPromptInputError,
				Err:    err,
			}
			go func() {
				defer close(events)
				defer emitter.Close()
				defer e.endPlanTurn(turnID)
				emitter.Emit(QueryEvent{
					Type:         EventTerminal,
					TerminalInfo: &terminal,
				})
			}()
			return events, terminal
		}
	}
	if isGraphResume && turnKernel.kind() != queryKernelProjectGraph {
		e.releaseRuntimeItem(initialRuntimeItemID)
		terminal := Terminal{
			Reason: TerminalModelError,
			Err: fmt.Errorf(
				"project graph permission decision cannot resume query kernel %q",
				turnKernel.kind(),
			),
		}
		go func() {
			defer close(events)
			defer emitter.Close()
			defer e.endPlanTurn(turnID)
			emitter.Emit(QueryEvent{
				Type:         EventTerminal,
				TerminalInfo: &terminal,
			})
		}()
		return events, terminal
	}
	if isGraphResume {
		// A ProjectGraph permission decision resumes the same logical Goal
		// turn through a fresh query transport turn. Rebind the retained
		// process-local Goal identity without advancing Goal revision or
		// treating the decision as user steering.
		emitter.BindGoal(e.currentGoalExecutionIdentity())
	}
	if !isGraphResume &&
		turnKernel.kind() == queryKernelProjectGraph &&
		e.projectGraphCheckpoint != nil {
		if _, waiting := e.projectGraphCheckpoint.ActiveInterrupt(); waiting {
			e.releaseRuntimeItem(initialRuntimeItemID)
			terminal := Terminal{
				Reason: TerminalWaitingInput,
				Err: fmt.Errorf(
					"project graph interaction must be resolved before a new prompt",
				),
			}
			go func() {
				defer close(events)
				defer emitter.Close()
				defer e.endPlanTurn(turnID)
				emitter.Emit(QueryEvent{
					Type:         EventTerminal,
					TerminalInfo: &terminal,
				})
			}()
			return events, terminal
		}
	}
	if !isGraphResume && admittedPrompt == nil {
		if err := ctx.Err(); err != nil {
			e.releaseRuntimeItem(initialRuntimeItemID)
			terminal := Terminal{
				Reason: TerminalAbortedStreaming,
				Err:    err,
			}
			go func() {
				defer close(events)
				defer emitter.Close()
				defer e.endPlanTurn(turnID)
				emitter.Emit(QueryEvent{
					Type:         EventTerminal,
					TerminalInfo: &terminal,
				})
			}()
			return events, terminal
		}
		goalEvent, goalIdentity, goalErr := e.goalService.beginTurn(
			turnID,
			!isGoalContinuation,
			runtimeItem,
			emitter.clock().UTC(),
		)
		if goalErr != nil {
			if !errors.Is(
				goalErr,
				errGoalContinuationPermanentlyRejected,
			) {
				e.releaseRuntimeItem(initialRuntimeItemID)
			}
			terminal := Terminal{
				Reason: TerminalPersistenceError,
				Err:    goalErr,
			}
			go func() {
				defer close(events)
				defer emitter.Close()
				defer e.endPlanTurn(turnID)
				emitter.Emit(QueryEvent{
					Type:         EventTerminal,
					TerminalInfo: &terminal,
				})
			}()
			return events, terminal
		}
		if goalIdentity != nil {
			emitter.BindGoal(goalIdentity)
			if !emitter.Emit(goalEvent) {
				e.releaseRuntimeItem(initialRuntimeItemID)
				terminal := Terminal{
					Reason: TerminalPersistenceError,
					Err:    fmt.Errorf("goal turn projection was rejected"),
				}
				go func() {
					defer close(events)
					defer emitter.Close()
					defer e.endPlanTurn(turnID)
					emitter.Emit(QueryEvent{
						Type:         EventTerminal,
						TerminalInfo: &terminal,
					})
				}()
				return events, terminal
			}
		}
	}

	e.mu.Lock()
	baseMessages := append([]*schema.Message(nil), e.messages...)
	turnAbortController := newAbortControllerFromContext(ctx)
	e.abortController = turnAbortController
	inputCoordinator := e.inputCoordinator
	runtimeInputScope := RuntimeInputScope{
		SessionID: e.config.SessionID,
		ThreadID:  e.config.ThreadID,
		AgentID:   e.config.AgentID,
	}
	e.mu.Unlock()

	if admittedPrompt != nil {
		ownsAdmittedPrompt = false
	}
	go func() {
		defer close(events)
		defer emitter.Close()
		defer e.endPlanTurn(turnID)
		if admittedPrompt != nil {
			defer e.releaseAdmittedPrompt(admittedPrompt)
		}
		defer e.finishRuntimeInputTurn(
			turnAbortController,
			inputCoordinator,
			runtimeInputScope,
		)
		bufferedHookStatuses := make([]QueryEvent, 0)
		queryCtx := containment.WithSnapshot(ctx, e.executionPolicy)
		queryCtx = hooks.WithHookTurnID(queryCtx, turnID)
		queryCtx = hooks.WithHookStatusEmitter(queryCtx, func(hookCommand, statusMessage, phase string) {
			event := QueryEvent{
				Type: EventHookStatus,
				HookStatus: &HookStatusEvent{
					HookName:      hookCommand,
					StatusMessage: statusMessage,
					Phase:         phase,
				},
			}
			if admittedPrompt != nil {
				bufferedHookStatuses = append(bufferedHookStatuses, event)
				return
			}
			emitter.Emit(event)
		})
		e.sessionState.TransitionToRunning()
		e.activityTracker.Start(session.ActivityAPICall)
		defer e.sessionState.TransitionToIdle()
		defer e.activityTracker.Stop(session.ActivityAPICall)
		if initialRuntimeItemID != "" {
			emitter.Emit(QueryEvent{
				Type: EventCommandLifecycle,
				CommandLifecycle: &CommandLifecycleEvent{
					CommandUUID: initialRuntimeItemID,
					Phase:       CommandLifecycleStarted,
				},
			})
		}

		var promptHookAttachments []*schema.Message
		var promptHookResult *hooks.UserPromptSubmitHookResult
		if !isGraphResume &&
			!isGoalContinuation &&
			!isAsyncHookRewakeMetadata(extra) {
			promptHookResult = e.config.HookExecutor.ExecuteUserPromptSubmit(queryCtx, prompt)
		}
		if hookResult := promptHookResult; hookResult != nil {
			if !hookResult.Reject &&
				strings.TrimSpace(hookResult.UpdatedPrompt) != "" {
				if admittedPrompt == nil {
					prompt = hookResult.UpdatedPrompt
				} else {
					rewritten, err := admittedPrompt.withHookRewrite(
						hookResult.UpdatedPrompt,
					)
					if err != nil {
						e.releaseRuntimeItem(initialRuntimeItemID)
						terminal := &Terminal{
							Reason: TerminalPromptInputError,
							Err:    err,
						}
						emitter.Emit(QueryEvent{
							Type:         EventTerminal,
							TerminalInfo: terminal,
						})
						return
					}
					admittedPrompt = rewritten
					prompt = admittedPrompt.textForHook()
				}
			}
			promptHookAttachments = append(promptHookAttachments, hookResult.Attachments...)
			if strings.TrimSpace(hookResult.AdditionalContext) != "" {
				promptHookAttachments = append(promptHookAttachments, &schema.Message{
					Role: schema.User, Content: hookResult.AdditionalContext,
					Extra: map[string]any{"is_meta": true, "attachment_kind": "user_prompt_hook_context"},
				})
			}
		}

		if admittedPrompt != nil {
			currentModel := ""
			if admittedPrompt.binding != nil {
				currentModel = admittedPrompt.binding.selectedModelSpec
			}
			if err := e.checkAdmittedPromptRoute(
				admittedPrompt,
				currentModel,
			); err != nil {
				e.releaseRuntimeItem(initialRuntimeItemID)
				terminal := &Terminal{
					Reason: TerminalPromptInputError,
					Err:    err,
				}
				emitter.Emit(QueryEvent{
					Type:         EventTerminal,
					TerminalInfo: terminal,
				})
				return
			}
			if err := ctx.Err(); err != nil {
				e.releaseRuntimeItem(initialRuntimeItemID)
				terminal := &Terminal{
					Reason: TerminalAbortedStreaming,
					Err:    err,
				}
				emitter.Emit(QueryEvent{
					Type:         EventTerminal,
					TerminalInfo: terminal,
				})
				return
			}
			goalEvent, goalIdentity, goalErr := e.goalService.beginTurn(
				turnID,
				true,
				nil,
				emitter.clock().UTC(),
			)
			if goalErr != nil {
				e.releaseRuntimeItem(initialRuntimeItemID)
				terminal := &Terminal{
					Reason: TerminalPersistenceError,
					Err:    goalErr,
				}
				emitter.Emit(QueryEvent{
					Type:         EventTerminal,
					TerminalInfo: terminal,
				})
				return
			}
			if goalIdentity != nil {
				emitter.BindGoal(goalIdentity)
				if !emitter.Emit(goalEvent) {
					e.releaseRuntimeItem(initialRuntimeItemID)
					terminal := &Terminal{
						Reason: TerminalPersistenceError,
						Err:    fmt.Errorf("goal turn projection was rejected"),
					}
					emitter.Emit(QueryEvent{
						Type:         EventTerminal,
						TerminalInfo: terminal,
					})
					return
				}
			}
			for _, event := range bufferedHookStatuses {
				emitter.Emit(event)
			}
			e.recordPermissionReviewUserIntent(admittedPrompt.textForHook())
		}

		if promptHookResult != nil && promptHookResult.Reject {
			reason := strings.TrimSpace(promptHookResult.RejectReason)
			if reason == "" {
				reason = "rejected by user-prompt-submit hook"
			}
			rejectionExtra := cloneMessageExtra(extra)
			if rejectionExtra == nil {
				rejectionExtra = make(map[string]any)
			}
			rejectionExtra["is_meta"] = true
			rejectionExtra["attachment_kind"] = "user_prompt_hook_rejection"
			rejection := &schema.Message{
				Role: schema.User, Content: reason, Extra: rejectionExtra,
			}
			emitter.Emit(QueryEvent{
				Type:              EventAttachment,
				AttachmentMessage: rejection,
			})
			if err := e.recordTranscriptMessages([]*schema.Message{rejection}); err != nil {
				e.releaseRuntimeItem(initialRuntimeItemID)
				terminal := &Terminal{
					Reason: TerminalPersistenceError,
					Err:    fmt.Errorf("persist user prompt rejection: %w", err),
				}
				emitter.Emit(QueryEvent{
					Type:         EventTerminal,
					TerminalInfo: terminal,
				})
				return
			}
			if initialRuntimeItemID != "" {
				emitter.Emit(QueryEvent{
					Type: EventCommandLifecycle,
					CommandLifecycle: &CommandLifecycleEvent{
						CommandUUID: initialRuntimeItemID,
						Phase:       CommandLifecycleCompleted,
					},
				})
			}
			terminal := &Terminal{Reason: TerminalHookStopped}
			emitter.Emit(QueryEvent{
				Type:         EventTerminal,
				TerminalInfo: terminal,
			})
			return
		}

		var userMsg *schema.Message
		if !isGraphResume {
			if admittedPrompt == nil {
				userMsg = newUserMessage(prompt, extra, images)
			} else {
				var err error
				userMsg, err = admittedPrompt.message(extra)
				if err != nil {
					e.releaseRuntimeItem(initialRuntimeItemID)
					terminal := &Terminal{
						Reason: TerminalPromptInputError,
						Err:    err,
					}
					emitter.Emit(QueryEvent{
						Type:         EventTerminal,
						TerminalInfo: terminal,
					})
					return
				}
			}
			baseMessages = append(baseMessages, userMsg)
			baseMessages = append(baseMessages, promptHookAttachments...)
		}
		history := newConversationHistory(baseMessages)
		history.observeAssistant = e.observeProviderUsageMessage

		userContext := promptctx.BuildUserContext(e.config.CWD, e.config.Clock())
		systemContext := promptctx.BuildSystemContext(queryCtx, e.config.CWD)

		// Load CLAUDE.md content from project/user/managed paths.
		// Mirrors reference prompts.ts getSystemPrompt() which includes CLAUDE.md
		// between the base prompt and dynamic context.
		claudeMdContent, _ := promptctx.LoadClaudeMdContent(e.config.CWD)

		basePrompt := promptctx.ComposeBaseSystemPrompt(
			e.config.CustomSystemPrompt,
			e.config.AppendSystemPrompt,
		)
		if e.config.EnablePersistentMemory {
			if memoryPrompt, memoryErr := memdir.BuildUnifiedMemoryPrompt(e.config.MemoryProjectRoot); memoryErr == nil && strings.TrimSpace(memoryPrompt) != "" {
				basePrompt += "\n\n" + memoryPrompt
			}
		}
		// Insert CLAUDE.md content between base prompt and append prompt.
		if claudeMdContent != "" {
			basePrompt = basePrompt + "\n\n" + claudeMdContent
		}

		systemPrompt := &schema.Message{
			Role:    schema.System,
			Content: basePrompt,
		}

		toolCtx := &ToolUseContext{
			AgentID:   e.config.AgentID,
			SessionID: e.config.SessionID,
			ThreadID:  e.config.ThreadID,
			PlanMode:  planPhaseRequiresContainment(planTurn.State.Phase),
			Options: &ToolUseOptions{
				Tools:              e.modelVisibleTools(),
				MainLoopModel:      e.config.Model,
				EffortValue:        e.ReasoningEffort(),
				PermissionMode:     planTurn.Mode,
				AppendSystemPrompt: e.config.AppendSystemPrompt,
				RefreshTools:       e.modelVisibleTools,
				PlanFilePath:       planTurn.State.PlanFileIdentity,
				MaxBudgetUSD: func(v float64) *float64 {
					if v <= 0 {
						return nil
					}
					return &v
				}(e.config.MaxBudgetUSD),
			},
			AbortController:         turnAbortController,
			ReadFileState:           e.fileStateCache,
			Messages:                baseMessages,
			ContentReplacementState: e.contentReplacementState,
		}

		turnMessages := make([]*schema.Message, 0, 1+len(promptHookAttachments))
		if userMsg != nil {
			turnMessages = append(turnMessages, userMsg)
			turnMessages = append(turnMessages, promptHookAttachments...)
		}
		persistedMessageCount := len(baseMessages) - len(turnMessages)
		e.mu.Lock()
		transcriptNeedsCheckpoint := e.transcriptCheckpointRequired
		var fileStateSnapshotRepairRequired atomic.Bool
		initialTurnTranscriptStored := e.initialTurnTranscriptStored
		if !isGraphResume && initialTurnTranscriptStored {
			e.initialTurnTranscriptStored = false
		}
		e.mu.Unlock()
		var transcriptPersistenceErr error
		durableRichPrompt := admittedPrompt != nil &&
			admittedPrompt.requiresDurablePrompt() &&
			!isGraphResume &&
			!initialTurnTranscriptStored
		if durableRichPrompt {
			transcriptPersistenceErr = func() error {
				if err := e.beginMediaLifecycleWrite(); err != nil {
					return err
				}
				defer e.endMediaLifecycleWrite()
				var durableRecordErr error
				durableRecord := runtimeItemDurablePrompt(runtimeItem)
				if isDurableRuntimePrompt {
					durableRecord, durableRecordErr = durablePromptWithAdmittedText(
						durableRecord,
						admittedPrompt,
					)
				} else {
					durableRecord, durableRecordErr = e.persistAdmittedPromptMedia(
						ctx,
						turnID,
						admittedPrompt,
					)
				}
				persistErr := durableRecordErr
				if durableRecordErr != nil && isDurableRuntimePrompt {
					e.releaseRuntimeItem(initialRuntimeItemID)
				}
				if persistErr == nil {
					persistErr = e.recordDurableUserPrompt(
						durableRecord,
						userMsg,
						admittedPrompt,
					)
				}
				if persistErr == nil {
					if transcriptNeedsCheckpoint {
						persistErr = e.recordTranscriptBoundary(
							transcript.LifecycleCheckpoint,
							baseMessages,
						)
					} else if len(promptHookAttachments) > 0 {
						persistErr = e.recordTranscriptMessages(
							promptHookAttachments,
						)
					}
				}
				return persistErr
			}()
		} else if isGraphResume {
			transcriptPersistenceErr = nil
			persistedMessageCount = len(baseMessages)
		} else if initialTurnTranscriptStored {
			// AgentRunner already replaced the child transcript with resume
			// history plus this first user turn before executor entry. Do not
			// append the same prompt a second time.
			transcriptPersistenceErr = nil
		} else if transcriptNeedsCheckpoint {
			transcriptPersistenceErr = e.recordTranscriptBoundary(
				transcript.LifecycleCheckpoint,
				baseMessages,
			)
		} else {
			transcriptPersistenceErr = e.recordTranscriptMessages(turnMessages)
		}
		if transcriptPersistenceErr == nil {
			persistedMessageCount = len(baseMessages)
			transcriptNeedsCheckpoint = false
		} else {
			transcriptNeedsCheckpoint = true
		}
		if durableRichPrompt && transcriptPersistenceErr != nil {
			terminal := &Terminal{
				Reason: TerminalPersistenceError,
				Err: fmt.Errorf(
					"persist durable user prompt: %w",
					transcriptPersistenceErr,
				),
			}
			emitter.Emit(QueryEvent{
				Type:         EventTerminal,
				TerminalInfo: terminal,
			})
			return
		}
		for _, attachment := range promptHookAttachments {
			if attachment != nil {
				emitter.Emit(QueryEvent{
					Type:              EventAttachment,
					AttachmentMessage: attachment,
				})
			}
		}
		if isGoalContinuation && transcriptPersistenceErr == nil {
			transcriptPersistenceErr = e.goalService.markContinuationDelivered(
				initialRuntimeItemID,
				turnID,
				emitter.clock().UTC(),
			)
			if transcriptPersistenceErr != nil {
				transcriptNeedsCheckpoint = true
			}
		}
		if isGoalContinuation && transcriptPersistenceErr != nil {
			terminal := Terminal{
				Reason: TerminalPersistenceError,
				Err: fmt.Errorf(
					"persist Goal continuation receipt: %w",
					transcriptPersistenceErr,
				),
			}
			emitter.Emit(QueryEvent{
				Type:         EventTerminal,
				TerminalInfo: &terminal,
			})
			return
		}

		e.resetPermissionDenials()

		maxTurns := e.config.MaxTurns
		querySource := QuerySourceSDK
		if e.config.AgentID != "" {
			querySource = QuerySourceAgent
		}
		toolUseSummary := e.toolUseSummaryModelCall(ctx)

		params := QueryParams{
			Messages:                baseMessages,
			SystemPrompt:            systemPrompt,
			SessionID:               e.config.SessionID,
			UserContext:             userContext,
			SystemContext:           systemContext,
			CanUseTool:              e.wrappedCanUseTool,
			RepeatedToolCallPrompt:  e.config.RepeatedToolCallPrompt,
			ToolUseContext:          toolCtx,
			FallbackModel:           e.config.FallbackModel,
			QuerySource:             querySource,
			MaxTurns:                &maxTurns,
			TaskBudget:              e.config.TaskBudget,
			TokenBudgetTracker:      e.config.TokenBudgetTracker,
			ChatModel:               e.config.ChatModel,
			modelCall:               e.modelCallIdentitySnapshot(),
			modelResolver:           e.config.ModelResolver,
			commandEntrypoint:       string(e.config.CommandEntrypoint),
			SummaryModel:            e.config.SummaryModel,
			ToolUseSummaryModel:     toolUseSummary.Model,
			toolUseSummaryCall:      toolUseSummary.Identity,
			EmitToolUseSummaries:    e.config.EmitToolUseSummaries,
			InputCoordinator:        inputCoordinator,
			ProjectGraphCheckpoint:  e.projectGraphCheckpoint,
			ProjectGraphHITLEnabled: e.projectGraphHITLEnabled,
			RuntimePermissionDecision: func() *RuntimePermissionDecision {
				if !isGraphResume {
					return nil
				}
				decision := *runtimeItem.PermissionDecision
				decision.Result = clonePermissionInteractionResult(
					runtimeItem.PermissionDecision.Result,
				)
				return &decision
			}(),
			CollectRuntimeItems: func(ctx context.Context) error {
				return e.collectRuntimeItemsWith(
					ctx,
					inputCoordinator,
					runtimeInputScope,
				)
			},
			AdmitRuntimeItem: e.preflightClaimedRuntimeItem,
			ToolRegistry:     e.toolRegistry,
			ToolExecutor:     e.toolExecutor,
			TransitionPermissionMode: func(
				current *ToolUseContext,
				mode permission.Mode,
				requestID string,
			) (*ToolUseContext, func(), error) {
				return e.transitionPermissionModeForTurn(
					turnID,
					emitter.Emit,
					current,
					mode,
					requestID,
				)
			},
			CancelToolInteraction: func(toolUseID string) bool {
				return e.ResolvePermissionInteraction(
					toolUseID,
					PermissionInteractionResult{
						Decision: PermissionCancelled,
						Message:  "permission request cancelled",
					},
				)
			},
			HookExecutor:         e.config.HookExecutor,
			ResultStorage:        e.resultStorage,
			MemoryStore:          e.memoryStore,
			SkillRegistry:        e.skillRegistry,
			AgentPauseCheckpoint: e.waitAtAgentPauseCheckpoint,
			AgentProgressDrainer: func() []tools.AgentProgressEvent {
				if e.agentRunner == nil {
					return nil
				}
				return e.agentRunner.PollAgentProgress()
			},
			TaskLifecycleDrainer: e.taskManager.DrainLifecycleEvents,
			Deps: &QueryDeps{
				Transcript:              e.transcript,
				RecordFileStateSnapshot: e.recordFileStateSnapshot,
				ReportFileStateSnapshotFailure: func(error) {
					fileStateSnapshotRepairRequired.Store(true)
				},
				ProviderUsage: e.currentGoalProviderUsageAdmitter(),
			},
		}
		params.modelDispatchGuard = e.checkModelDispatch
		params.modelCompactionGuard = e.checkModelCompaction
		params.commitProviderOrigin = func(origin providerorigin.Origin) error {
			messages := history.Messages()
			var assistant *schema.Message
			for index := len(messages) - 1; index >= 0; index-- {
				if messages[index] != nil && messages[index].Role == schema.Assistant {
					assistant = messages[index]
					break
				}
			}
			if assistant == nil {
				return errors.New(
					"completed provider response has no canonical assistant message",
				)
			}
			if err := e.transcript.StageAssistantOrigin(assistant, origin); err != nil {
				return err
			}
			fullCheckpoint := transcriptNeedsCheckpoint ||
				fileStateSnapshotRepairRequired.Load() ||
				len(messages) < persistedMessageCount
			var err error
			if fullCheckpoint {
				err = e.recordTranscriptBoundary(
					transcript.LifecycleCheckpoint,
					messages,
				)
			} else {
				err = e.recordTranscriptMessages(
					messages[persistedMessageCount:],
				)
			}
			if err != nil {
				transcriptNeedsCheckpoint = true
				transcriptPersistenceErr = err
				return err
			}
			persistedMessageCount = len(messages)
			transcriptNeedsCheckpoint = false
			if fullCheckpoint {
				fileStateSnapshotRepairRequired.Store(false)
			}
			transcriptPersistenceErr = nil
			if err := e.persistSessionCheckpointMessages("running", messages); err != nil {
				transcriptNeedsCheckpoint = true
				transcriptPersistenceErr = err
				return err
			}
			return nil
		}
		if admittedPrompt != nil && admittedPrompt.binding != nil {
			params.promptRouteGuard = func(currentModel string) error {
				return e.checkAdmittedPromptRoute(
					admittedPrompt,
					currentModel,
				)
			}
			params.mediaRecovery = e.bindMediaRecoveryContext(
				turnID,
				userMsg,
				admittedPrompt,
			)
			if params.mediaRecovery != nil {
				params.mediaRecovery.commitBoundary = func(
					commitCtx context.Context,
					messages []*schema.Message,
				) error {
					if err := commitCtx.Err(); err != nil {
						return err
					}
					if err := e.recordTranscriptBoundary(
						transcript.LifecycleCompact,
						messages,
					); err != nil {
						return err
					}
					// The successful fsync is the active-context commit point.
					// Mirror it before returning even if cancellation races the
					// later presentation event.
					history.Replace(messages)
					persistedMessageCount = len(messages)
					transcriptNeedsCheckpoint = false
					fileStateSnapshotRepairRequired.Store(false)
					transcriptPersistenceErr = nil
					return nil
				}
			}
		}

		compactBoundaryCheckpointPending := false
		terminal := queryWithKernel(queryCtx, params, func(evt QueryEvent) {
			eventTranscriptPersisted := false
			if compactBoundaryCheckpointPending && evt.Type != EventCompactBoundary {
				checkpointMessages := history.Messages()
				err := e.recordTranscriptBoundary(
					transcript.LifecycleCompact,
					checkpointMessages,
				)
				if err == nil {
					persistedMessageCount = len(checkpointMessages)
					transcriptNeedsCheckpoint = false
					fileStateSnapshotRepairRequired.Store(false)
					transcriptPersistenceErr = nil
					_ = e.persistSessionCheckpointMessages("running", checkpointMessages)
					compactBoundaryCheckpointPending = false
				} else {
					transcriptNeedsCheckpoint = true
					transcriptPersistenceErr = err
					// An uncertain error means the complete boundary line may
					// already be visible. Do not append a second compact
					// boundary; one full checkpoint repair will
					// deterministically select the intended active state.
					compactBoundaryCheckpointPending = shouldRetryCompactBoundary(err)
				}
			}

			history.Observe(evt)
			if evt.Type == EventCompactBoundary {
				e.observeCompactBoundaryUsageMessage(evt.CompactBoundaryMessage)
			}
			if evt.Type == EventToolResult &&
				e.backgroundServices != nil &&
				!e.goalProviderUsageRequired() {
				e.backgroundServices.RecordToolCall(backgroundMessageStrings(history.Messages()))
			}
			if evt.Type == EventCompactBoundary &&
				evt.compactBoundaryCommitted {
				compactBoundaryCheckpointPending = false
				persistedMessageCount = len(history.Messages())
				transcriptNeedsCheckpoint = false
				fileStateSnapshotRepairRequired.Store(false)
				transcriptPersistenceErr = nil
				_ = e.persistSessionCheckpointMessages(
					"running",
					history.Messages(),
				)
			} else if evt.Type == EventCompactBoundary {
				compactBoundaryCheckpointPending = true
			} else if !compactBoundaryCheckpointPending &&
				shouldCheckpointTranscript(evt) {
				checkpointMessages := history.Messages()
				var err error
				fullCheckpoint := transcriptNeedsCheckpoint ||
					fileStateSnapshotRepairRequired.Load() ||
					evt.Type == EventMaxTurnsReached ||
					len(checkpointMessages) < persistedMessageCount
				if fullCheckpoint {
					err = e.recordTranscriptBoundary(
						transcript.LifecycleCheckpoint,
						checkpointMessages,
					)
				} else {
					err = e.recordTranscriptMessages(
						checkpointMessages[persistedMessageCount:],
					)
				}
				if err == nil {
					persistedMessageCount = len(checkpointMessages)
					transcriptNeedsCheckpoint = false
					if fullCheckpoint {
						fileStateSnapshotRepairRequired.Store(false)
					}
					transcriptPersistenceErr = nil
					eventTranscriptPersisted = true
					_ = e.persistSessionCheckpointMessages("running", checkpointMessages)
				} else {
					transcriptNeedsCheckpoint = true
					transcriptPersistenceErr = err
				}
			}
			if eventTranscriptPersisted {
				e.attachTranscriptEntryIdentity(&evt)
			}
			emitter.Emit(evt)
		}, turnKernel)

		finalMessages := history.Messages()
		var finalTranscriptErr error
		if compactBoundaryCheckpointPending {
			finalTranscriptErr = e.recordTranscriptBoundary(
				transcript.LifecycleCompact,
				finalMessages,
			)
			if finalTranscriptErr == nil {
				fileStateSnapshotRepairRequired.Store(false)
			}
		} else if transcriptNeedsCheckpoint ||
			fileStateSnapshotRepairRequired.Load() ||
			len(finalMessages) < persistedMessageCount {
			finalTranscriptErr = e.recordTranscriptBoundary(
				transcript.LifecycleCheckpoint,
				finalMessages,
			)
			if finalTranscriptErr == nil {
				fileStateSnapshotRepairRequired.Store(false)
			}
		} else {
			finalTranscriptErr = e.recordTranscriptMessages(
				finalMessages[persistedMessageCount:],
			)
		}
		if finalTranscriptErr == nil {
			transcriptPersistenceErr = nil
			if checkpointErr := e.persistSessionCheckpointMessages(
				string(terminal.Reason),
				finalMessages,
			); checkpointErr != nil {
				transcriptPersistenceErr = checkpointErr
				terminal = Terminal{
					Reason:    TerminalPersistenceError,
					TurnCount: terminal.TurnCount,
					MaxTurns:  terminal.MaxTurns,
					Err: fmt.Errorf(
						"persist final session checkpoint: %w",
						checkpointErr,
					),
				}
			}
		} else {
			transcriptPersistenceErr = finalTranscriptErr
			terminal = Terminal{
				Reason:    TerminalPersistenceError,
				TurnCount: terminal.TurnCount,
				MaxTurns:  terminal.MaxTurns,
				Err: fmt.Errorf(
					"persist final transcript checkpoint: %w",
					finalTranscriptErr,
				),
			}
		}

		e.mu.Lock()
		e.messages = finalMessages
		e.transcriptCheckpointRequired = transcriptPersistenceErr != nil
		e.mu.Unlock()

		if transcriptPersistenceErr == nil &&
			turnKernel.kind() == queryKernelProjectGraph &&
			e.projectGraphCheckpoint != nil &&
			terminal.Reason != TerminalWaitingInput {
			if checkpointErr := e.projectGraphCheckpoint.Delete(
				context.WithoutCancel(queryCtx),
				e.projectGraphCheckpoint.checkpointID,
			); checkpointErr != nil {
				transcriptPersistenceErr = checkpointErr
				terminal = Terminal{
					Reason:    TerminalPersistenceError,
					TurnCount: terminal.TurnCount,
					MaxTurns:  terminal.MaxTurns,
					Err: fmt.Errorf(
						"delete completed project graph checkpoint: %w",
						checkpointErr,
					),
				}
			}
		}
		if isGraphResume && initialRuntimeItemID != "" {
			if transcriptPersistenceErr == nil {
				if settleErr := inputCoordinator.Settle(initialRuntimeItemID); settleErr != nil {
					transcriptPersistenceErr = settleErr
					terminal = Terminal{
						Reason:    TerminalPersistenceError,
						TurnCount: terminal.TurnCount,
						MaxTurns:  terminal.MaxTurns,
						Err: fmt.Errorf(
							"settle project graph decision: %w",
							settleErr,
						),
					}
				}
			} else {
				e.releaseRuntimeItem(initialRuntimeItemID)
			}
		}

		if terminal.Reason != TerminalWaitingInput {
			// Post-loop: run stop hooks for memory extraction and auto-dream.
			// Mirrors reference query.ts post-terminal stop hook phase.
			stopHookCtx := &hooks.StopHookContext{
				Reason:    mapTerminalToStopReason(terminal.Reason),
				Messages:  finalMessages,
				TurnCount: terminal.TurnCount,
				ModelName: e.config.Model,
				SessionID: e.config.SessionID,
			}
			if last := lastAssistantContent(finalMessages); last != "" {
				stopHookCtx.FinalResponse = last
			}
			if result, err := hooks.RunStopHooks(queryCtx, stopHookCtx); err == nil && result != nil {
				// Persist extracted memories.
				if len(result.MemoryEntries) > 0 {
					entries := make([]compact.MemoryEntry, 0, len(result.MemoryEntries))
					for _, entry := range result.MemoryEntries {
						entries = append(entries, compact.MemoryEntry{
							Content:   entry.Content,
							Source:    entry.Source,
							CreatedAt: entry.Timestamp,
						})
					}
					_ = e.memoryStore.AddAll(entries)
				}
			}
			if e.backgroundServices != nil &&
				!e.goalProviderUsageRequired() {
				e.backgroundServices.RecordTurn(
					backgroundMessageStrings(finalMessages),
				)
			}
		}

		if initialRuntimeItemID != "" && transcriptPersistenceErr == nil {
			emitter.Emit(QueryEvent{
				Type: EventCommandLifecycle,
				CommandLifecycle: &CommandLifecycleEvent{
					CommandUUID: initialRuntimeItemID,
					Phase:       CommandLifecycleCompleted,
				},
			})
		}

		// Emit terminal as the final event so callers can observe why the loop ended.
		emitter.Emit(QueryEvent{Type: EventTerminal, TerminalInfo: &terminal})

		// Send completion notification.
		if e.config.NotifyManager != nil &&
			terminal.Reason != TerminalWaitingInput {
			notify.NotifyCompletion(e.config.NotifyManager, e.config.SessionID, "Turn completed")
		}
	}()

	return events, Terminal{}
}

func isAsyncHookRewakeMetadata(extra map[string]any) bool {
	kind, _ := extra["attachment_kind"].(string)
	rewake, _ := extra["async_rewake"].(bool)
	return kind == "async_hook_response" && rewake
}

func runtimeItemIDFromMetadata(extra map[string]any) string {
	id, _ := extra["runtime_item_id"].(string)
	return strings.TrimSpace(id)
}

func (e *QueryEngine) releaseRuntimeItem(id string) {
	if e == nil || e.inputCoordinator == nil || strings.TrimSpace(id) == "" {
		return
	}
	if _, err := e.inputCoordinator.Release(id); err != nil {
		e.mu.Lock()
		e.inputCoordinatorErr = err
		e.mu.Unlock()
	}
}

// replacementRecords extracts all content replacement records from the engine's state
// for inclusion in transcript checkpoints.
func (e *QueryEngine) replacementRecords() []transcript.Replacement {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.contentReplacementState == nil ||
		len(e.contentReplacementState.Replacements) == 0 {
		return nil
	}
	records := make([]transcript.Replacement, 0, len(e.contentReplacementState.Replacements))
	for id, replacement := range e.contentReplacementState.Replacements {
		records = append(records, transcript.Replacement{
			ToolUseID:   id,
			Replacement: replacement,
		})
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].ToolUseID < records[j].ToolUseID
	})
	return records
}

func (e *QueryEngine) recordTranscriptBoundary(
	kind transcript.LifecycleBoundaryKind,
	messages []*schema.Message,
) error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	recorder := e.transcript
	fileStateCache := e.fileStateCache
	e.mu.Unlock()
	if recorder == nil {
		return nil
	}
	usage := e.providerUsageSummary()
	commit := func() error {
		return recorder.RecordLifecycleBoundaryWithUsage(
			kind,
			messages,
			e.replacementRecords(),
			snapshotFileStateCache(fileStateCache),
			usage,
			true,
		)
	}
	var err error
	if kind == transcript.LifecycleCompact &&
		e.logicalWorkAdapter != nil {
		err = e.logicalWorkAdapter.WithStableLifecycle(func(
			workboard.LifecycleSnapshot,
		) error {
			return commit()
		})
	} else {
		err = commit()
	}
	if err != nil {
		return err
	}
	e.settleRuntimeItemsFromMessages(messages)
	return nil
}

func (e *QueryEngine) recordTranscriptMessages(messages []*schema.Message) error {
	if e == nil || len(messages) == 0 {
		return nil
	}
	e.mu.Lock()
	recorder := e.transcript
	e.mu.Unlock()
	if recorder == nil {
		return nil
	}
	for _, message := range messages {
		itemID := durableRuntimePromptDeliveryItemID(message)
		if record, ok := e.inputCoordinator.durablePromptRecord(itemID); ok {
			if err := recorder.RecordRuntimeUserPrompt(
				record,
				message,
				itemID,
			); err != nil {
				return err
			}
			continue
		}
		if err := recorder.RecordMessages([]*schema.Message{message}); err != nil {
			return err
		}
	}
	if err := recorder.Flush(); err != nil {
		return err
	}
	e.settleRuntimeItemsFromMessages(messages)
	return nil
}

func (e *QueryEngine) recordFileStateSnapshot(
	files map[string]transcript.FileState,
) error {
	if e == nil || len(files) == 0 {
		return nil
	}
	e.mu.Lock()
	recorder := e.transcript
	writer := e.fileStateSnapshotWriter
	e.mu.Unlock()
	if writer != nil {
		return writer(files)
	}
	if recorder == nil {
		return nil
	}
	return recorder.RecordFileHistorySnapshot(files)
}

func durableRuntimePromptDeliveryItemID(message *schema.Message) string {
	if message == nil || message.Extra == nil {
		return ""
	}
	runtimeKind, _ := message.Extra["runtime_item_kind"].(string)
	attachmentKind, _ := message.Extra["attachment_kind"].(string)
	if runtimeKind != string(RuntimeItemUserPrompt) ||
		attachmentKind != "queued_command" {
		return ""
	}
	return runtimeItemIDFromMetadata(message.Extra)
}

func (e *QueryEngine) attachTranscriptEntryIdentity(evt *QueryEvent) {
	if e == nil || evt == nil {
		return
	}
	message, _ := runtimeMessageForEvent(*evt)
	if message == nil {
		return
	}
	e.mu.Lock()
	recorder := e.transcript
	e.mu.Unlock()
	if recorder == nil {
		return
	}
	if identity, ok := recorder.LatestMessageEntryIdentity(message); ok {
		evt.TranscriptEntryID = identity.Key()
	}
}

func (e *QueryEngine) settleRuntimeItemsFromMessages(messages []*schema.Message) {
	if e == nil || e.inputCoordinator == nil {
		return
	}
	ids := runtimeItemIDsFromMessages(messages)
	if len(ids) == 0 {
		return
	}
	values := make([]string, 0, len(ids))
	for id := range ids {
		values = append(values, id)
	}
	sort.Strings(values)
	if err := e.inputCoordinator.Settle(values...); err != nil {
		e.mu.Lock()
		if e.inputCoordinatorErr == nil {
			e.inputCoordinatorErr = err
		}
		e.mu.Unlock()
	}
}

func snapshotFileStateCache(cache *FileStateCache) map[string]transcript.FileState {
	if cache == nil {
		return nil
	}
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	snapshot := make(map[string]transcript.FileState)
	for path := range cache.ReadFiles {
		state := snapshot[path]
		state.Path = path
		state.WasRead = true
		snapshot[path] = state
	}
	for path := range cache.EditFiles {
		state := snapshot[path]
		state.Path = path
		state.WasEdit = true
		snapshot[path] = state
	}
	for path := range cache.WriteFiles {
		state := snapshot[path]
		state.Path = path
		state.WasWrite = true
		snapshot[path] = state
	}
	return snapshot
}

func (e *QueryEngine) toolExecutor(ctx context.Context, toolName, jsonInput string) (string, error) {
	if reason, unavailable := tools.UnavailableBuiltinToolReason(strings.TrimSpace(toolName)); unavailable {
		return fmt.Sprintf("tool unavailable: %s: %s", strings.TrimSpace(toolName), reason), nil
	}
	if e.toolRegistry == nil {
		return "tool registry not configured", nil
	}
	var impl tools.ToolImpl
	binding, hasBinding := permissionDispatchActionFromContext(ctx)
	if hasBinding {
		input, err := parseToolInput(jsonInput)
		if err != nil {
			return "", fmt.Errorf(
				"permission dispatch input for %s is invalid: %w",
				toolName,
				err,
			)
		}
		currentAction, err := e.buildPermissionActionDescriptor(
			toolName,
			input,
			binding.toolCtx,
		)
		if err != nil ||
			!samePermissionActionBinding(binding.action, currentAction) ||
			binding.action.CanonicalInput != currentAction.CanonicalInput {
			if err != nil {
				return "", fmt.Errorf(
					"permission action changed before dispatch: %w",
					err,
				)
			}
			return "", fmt.Errorf("permission action or policy changed before dispatch")
		}
	} else {
		var ok bool
		impl, ok = e.toolRegistry.Get(toolName)
		if !ok {
			return "tool not found: " + toolName, nil
		}
	}
	if isGoalToolName(toolName) {
		return e.executeGoalTool(toolName, jsonInput)
	}
	// Inject per-engine singletons into tool execution context.
	ctx = tools.WithSessionID(ctx, e.config.SessionID)
	ctx = tools.WithThreadID(ctx, e.config.ThreadID)
	ctx = tools.WithExecutionCWD(ctx, e.config.CWD)
	ctx = containment.WithSnapshot(ctx, e.executionPolicy)
	if e.config.AgentID != "" {
		ctx = tools.WithAgentID(ctx, e.config.AgentID)
	}
	ctx = tools.WithAgentRunner(ctx, e.agentRunner)
	ctx = tools.WithTaskManager(ctx, e.taskManager)
	ctx = tools.WithLogicalWorkAuthority(ctx, e.logicalWorkAdapter)
	ctx = tools.WithWorkBoardShadowObserver(ctx, e.workBoardShadow)
	ctx = tools.WithShellManager(ctx, e.shellManager)
	if e.subagentExecutor != nil {
		ctx = tools.WithAgentExecutor(ctx, e.subagentExecutor)
	}
	if e.mcpManager != nil {
		ctx = tools.WithMCPManager(ctx, e.mcpManager)
	}
	if e.skillRegistry != nil {
		ctx = tools.WithSkillRegistry(ctx, e.skillRegistry)
	}
	if e.webFetchModel != nil {
		ctx = tools.WithWebFetchModel(ctx, e.webFetchModel)
	}
	ctx = execution.WithProviderUsageScope(
		ctx,
		e.currentGoalProviderUsageAdmitter(),
		e.goalProviderUsageRequired(),
	)
	ctx = tools.WithMediaSupport(ctx, modelcaps.GetCapabilities(e.config.Model).SupportsImages)
	if hasBinding {
		lease, err := e.toolRegistry.AcquireExecution(
			toolName,
			binding.action.CanonicalToolName,
			binding.action.CapabilityGeneration,
		)
		if err != nil {
			return "", fmt.Errorf(
				"permission action changed before dispatch: %w",
				err,
			)
		}
		defer lease.Cancel()
		return lease.Execute(ctx, jsonInput)
	}
	// Prefer context-aware execution when available (supports progress streaming).
	if impl.ExecuteCtx != nil {
		return impl.ExecuteCtx(ctx, jsonInput)
	}
	if impl.Execute == nil {
		return "tool has no execute function: " + toolName, nil
	}
	return impl.Execute(jsonInput)
}

// Interrupt aborts the current turn.
func (e *QueryEngine) Interrupt() {
	_ = e.RequestStop(RuntimeStopImmediate, "interrupt")
	if e.config.HookExecutor != nil {
		e.config.HookExecutor.CancelAsyncShellHooks()
	}
}

// Close releases resources owned by the engine (background watchers, etc.).
func (e *QueryEngine) Close() {
	if e == nil {
		return
	}
	persist := e.closePersistenceForLifecycle()
	if !persist {
		e.mu.Lock()
		e.pendingRestoreActivation = nil
		e.mu.Unlock()
	}
	e.closeWithPersistence(persist)
}

func (e *QueryEngine) closeWithPersistence(persist bool) {
	e.closeOnce.Do(func() {
		e.mediaLifecycleMu.Lock()
		e.mediaLifecycleClosed = true
		e.mediaLifecycleMu.Unlock()
		e.mu.Lock()
		e.promptRouteClosed = true
		e.promptRouteGeneration++
		activePromptMedia := make([]*turnMediaStore, 0, len(e.activePromptMedia))
		for store := range e.activePromptMedia {
			activePromptMedia = append(activePromptMedia, store)
			delete(e.activePromptMedia, store)
		}
		e.mu.Unlock()
		for _, store := range activePromptMedia {
			store.destroy()
		}
		e.cancelPermissionReviews()
		if e.permissionReviewAudit != nil {
			shutdownCtx, cancel := context.WithTimeout(
				context.Background(),
				permissionReviewAuditCloseTimeout,
			)
			e.permissionReviewAudit.Close(shutdownCtx)
			cancel()
		}
		if e.permissionRegistry != nil {
			e.permissionRegistry.release(e.permissionProjectIdentity, e.permissionEngineID)
		}
		if e.backgroundServices != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = e.backgroundServices.Shutdown(shutdownCtx)
			cancel()
		}
		e.asyncHookMu.Lock()
		e.asyncHookClosed = true
		close(e.asyncHookDone)
		e.asyncHookMu.Unlock()
		if e.config.HookExecutor != nil {
			e.config.HookExecutor.CancelAsyncShellHooks()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = e.config.HookExecutor.ShutdownAsyncShellHooks(shutdownCtx)
			cancel()
			e.config.HookExecutor.SetAsyncShellCompletionHandler(nil)
		}
		e.asyncHookWG.Wait()
		close(e.asyncHookEvents)
		if persist && !e.administrationOnly {
			_ = e.persistSessionCheckpoint("")
		}
		if persist && e.transcript != nil {
			_ = e.transcript.Close()
		}
		if e.settingsWatcher != nil {
			e.settingsWatcher.Stop()
		}
		if e.ownsAgentRunner && e.agentRunner != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = e.agentRunner.Shutdown(shutdownCtx)
			cancel()
		}
		if e.ownsShellManager && e.shellManager != nil {
			_ = e.shellManager.KillAll()
		}
		if e.ownsMCPManager && e.mcpManager != nil {
			_ = e.mcpManager.DisconnectAll()
		}
	})
}

func newAbortControllerFromContext(parent context.Context) *AbortController {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &AbortController{Ctx: ctx, Cancel: cancel}
}

func cloneMessageExtra(extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(extra))
	for key, value := range extra {
		cloned[key] = value
	}
	return cloned
}

func shouldCheckpointTranscript(evt QueryEvent) bool {
	switch evt.Type {
	case EventToolResult, EventUserInterruption:
		return true
	case EventAttachment:
		return evt.AttachmentMessage != nil
	case EventMaxTurnsReached:
		return evt.MaxTurnsInfo != nil
	case EventAssistant:
		return evt.AssistantMessage != nil
	default:
		return false
	}
}

func shouldRetryCompactBoundary(err error) bool {
	return err != nil && !transcript.IsDurabilityUncertain(err)
}

// mapTerminalToStopReason converts an engine TerminalReason to a hooks StopReason.
func mapTerminalToStopReason(reason TerminalReason) hooks.StopReason {
	switch reason {
	case TerminalCompleted:
		return hooks.StopReasonEndTurn
	case TerminalMaxTurns:
		return hooks.StopReasonMaxTurns
	case TerminalAbortedStreaming, TerminalAbortedTools:
		return hooks.StopReasonInterrupt
	case TerminalHookStopped, TerminalStopHookPrevented:
		return hooks.StopReasonStopTool
	case TerminalModelError, TerminalPersistenceError, TerminalPromptTooLong, TerminalImageError, TerminalPromptInputError, TerminalBlockingLimit:
		return hooks.StopReasonError
	default:
		return hooks.StopReasonEndTurn
	}
}

// lastAssistantContent returns the text content of the last assistant message
// in the slice, or "" if none exists.
func lastAssistantContent(msgs []*schema.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i] != nil && msgs[i].Role == schema.Assistant && msgs[i].Content != "" {
			return msgs[i].Content
		}
	}
	return ""
}

func backgroundMessageStrings(messages []*schema.Message) []string {
	const maxMessages = 80
	if len(messages) > maxMessages {
		messages = messages[len(messages)-maxMessages:]
	}
	result := make([]string, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content == "" && len(message.ToolCalls) > 0 {
			names := make([]string, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				if call.Function.Name != "" {
					names = append(names, call.Function.Name)
				}
			}
			content = "tools: " + strings.Join(names, ", ")
		}
		if content == "" {
			continue
		}
		if len(content) > 2000 {
			content = content[:2000] + "..."
		}
		result = append(result, string(message.Role)+": "+content)
	}
	return result
}

func newEngineBackgroundServices(
	chatModel model.BaseChatModel,
	transcriptDir string,
	sessionID string,
	store *compact.MemoryStore,
	callProvider func(
		context.Context,
		model.BaseChatModel,
		[]*schema.Message,
		...model.Option,
	) (*schema.Message, error),
) *services.BackgroundServices {
	return services.NewBackgroundServices(services.BackgroundServicesConfig{
		ModelFn: func(ctx context.Context, systemPrompt string, messages []string) (string, error) {
			modelMessages := []*schema.Message{{Role: schema.System, Content: systemPrompt}}
			for _, message := range messages {
				modelMessages = append(modelMessages, &schema.Message{Role: schema.User, Content: message})
			}
			if callProvider == nil {
				return "", fmt.Errorf(
					"long-session provider entry is unavailable",
				)
			}
			response, err := callProvider(ctx, chatModel, modelMessages)
			if err != nil {
				return "", err
			}
			if response == nil {
				return "", nil
			}
			return response.Content, nil
		},
		MemoryDir: filepath.Join(transcriptDir, sessionID), TranscriptDir: transcriptDir,
		SessionID: sessionID, MemoryStore: store,
	})
}

func (e *QueryEngine) callBackgroundProvider(
	ctx context.Context,
	chatModel model.BaseChatModel,
	messages []*schema.Message,
	opts ...model.Option,
) (*schema.Message, error) {
	if e == nil || chatModel == nil {
		return nil, fmt.Errorf("long-session provider entry is unavailable")
	}
	e.goalProviderBoundary.RLock()
	defer e.goalProviderBoundary.RUnlock()
	if e.goalProviderUsageRequired() {
		return nil, fmt.Errorf(
			"long-session provider entry is disabled while an unfinished Goal requires exact provider accounting",
		)
	}
	return chatModel.Generate(ctx, messages, opts...)
}

func (e *QueryEngine) rebindLongSessionServices() {
	e.mu.Lock()
	oldServices := e.backgroundServices
	e.backgroundServices = nil
	transcriptDir := e.config.TranscriptDir
	sessionID := e.config.SessionID
	enabled := e.config.EnableLongSessionServices && e.config.AgentID == "" && e.config.ChatModel != nil
	chatModel := e.config.ChatModel
	e.mu.Unlock()

	if oldServices != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = oldServices.Shutdown(ctx)
		cancel()
	}
	memoryDir := filepath.Join(transcriptDir, sessionID)
	memoryLimit := 100
	if enabled {
		memoryDir = filepath.Join(transcriptDir, "memory")
		memoryLimit = 500
	}
	store := compact.NewMemoryStore(memoryDir, memoryLimit)
	var background *services.BackgroundServices
	if enabled {
		background = newEngineBackgroundServices(
			chatModel,
			transcriptDir,
			sessionID,
			store,
			e.callBackgroundProvider,
		)
		background.Start()
	}
	e.mu.Lock()
	e.memoryStore = store
	e.backgroundServices = background
	e.mu.Unlock()
}

// GetMessages returns the full conversation history.
func (e *QueryEngine) GetMessages() []*schema.Message {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.messages) == 0 {
		return make([]*schema.Message, 0)
	}
	return append(make([]*schema.Message, 0, len(e.messages)), e.messages...)
}

// RuntimeTaskSnapshot returns the bounded read-only /tasks snapshot visible to
// this runtime: local/background Agent runner entries plus local task-list
// records. It intentionally does not drain notifications or lifecycle events.
func (e *QueryEngine) RuntimeTaskSnapshot() tools.RuntimeTaskSnapshot {
	return tools.RuntimeTaskSnapshotFrom(e.agentRunner, e.taskManager)
}

// GetTaskManager returns the task store owned by this root engine lineage.
func (e *QueryEngine) GetTaskManager() *tools.TaskManager {
	if e == nil {
		return nil
	}
	return e.taskManager
}

// GetMCPManager returns the MCP scope owned or borrowed by this engine.
func (e *QueryEngine) GetMCPManager() *tools.MCPToolManager {
	if e == nil {
		return nil
	}
	return e.mcpManager
}

// GetDeprecationWarning returns a non-empty string if the configured model is deprecated.
func (e *QueryEngine) GetDeprecationWarning() string {
	return e.deprecationWarning
}

// PermissionMode returns the engine's current permission mode.
func (e *QueryEngine) PermissionMode() permission.Mode {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.config.PermissionMode == "" {
		return permission.ModeDefault
	}
	return e.config.PermissionMode
}

func applyPermissionModeToToolContext(toolCtx *ToolUseContext, mode permission.Mode) {
	if toolCtx == nil {
		return
	}
	toolCtx.PlanMode = mode == permission.ModePlan
	if toolCtx.Options != nil {
		toolCtx.Options.PermissionMode = mode
	}
}

// GetPermissionDenials returns the permission denials recorded for the current/last turn.
func (e *QueryEngine) GetPermissionDenials() []permission.Denial {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.permissionDenials) == 0 {
		return make([]permission.Denial, 0)
	}
	out := make([]permission.Denial, 0, len(e.permissionDenials))
	for _, denial := range e.permissionDenials {
		out = append(out, permission.Denial{
			ToolName:  denial.ToolName,
			ToolUseID: denial.ToolUseID,
			Input:     cloneInputMap(denial.Input),
		})
	}
	return out
}

// ShouldFallbackToPrompting returns true when accumulated permission denials
// exceed threshold limits (3 consecutive or 20 total), indicating the system
// should fall back to interactive prompting instead of auto-denying.
// Mirrors src/utils/permissions/denialTracking.ts:shouldFallbackToPrompting.
func (e *QueryEngine) ShouldFallbackToPrompting() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.denialTracking.ShouldFallbackToPrompting()
}

// GetDenialTrackingState returns a snapshot of the denial tracking counters.
func (e *QueryEngine) GetDenialTrackingState() (consecutiveDenials, totalDenials int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.denialTracking.ConsecutiveDenials, e.denialTracking.TotalDenials
}

// GeneratePermissionExplanation generates a structured explanation for a
// permission-denied tool use. SDK consumers can call this when presenting
// permission denials to users (equivalent to Ctrl+E in the reference UI).
// Returns nil if the explainer fails.
func (e *QueryEngine) GeneratePermissionExplanation(
	ctx context.Context,
	toolName string,
	toolInput map[string]any,
	toolDescription string,
) (*permission.Explanation, error) {
	e.mu.Lock()
	messages := append([]*schema.Message(nil), e.messages...)
	e.mu.Unlock()
	providerUsage, err := e.providerUsageForPotentialGoalCall()
	if err != nil {
		return nil, fmt.Errorf(
			"permission explanation provider accounting: %w",
			err,
		)
	}

	return permission.GenerateExplanation(ctx, permission.GenerateExplanationParams{
		ToolName:        toolName,
		ToolInput:       toolInput,
		ToolDescription: toolDescription,
		Messages:        messages,
		ChatModel:       e.config.ChatModel,
		ProviderUsage:   providerUsage,
	})
}

func (e *QueryEngine) resetPermissionDenials() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.permissionDenials = e.permissionDenials[:0]
}

func (e *QueryEngine) reloadPermissionRules() {
	rules, _ := permission.LoadPermissionRules(e.config.PermissionProjectRoot)
	rules = append(rules, toolSelectionDenyRules(e.toolRegistry, e.config.ToolSelection)...)
	e.mu.Lock()
	e.permissionRules = permission.NewRulesEngine(rules)
	e.mu.Unlock()
}

func (e *QueryEngine) permissionRulesSnapshot() *permission.RulesEngine {
	e.mu.Lock()
	rules := e.permissionRules
	e.mu.Unlock()
	if rules == nil {
		return permission.NewRulesEngine(nil)
	}
	return rules
}

// modelVisibleTools projects the complete runtime registry into the tool pool
// supplied to the model. The projection is intentionally recomputed only at
// query/turn boundaries through ToolUseOptions.RefreshTools.
func (e *QueryEngine) modelVisibleTools() []*schema.ToolInfo {
	active := planPhaseRequiresContainment(e.PlanState().Phase)
	var projected []*schema.ToolInfo
	if e.toolRegistry == nil {
		projected = filterPlanModelVisibleTools(
			append([]*schema.ToolInfo(nil), e.config.Tools...),
			active,
			nil,
		)
	} else {
		projected = assembleModelVisibleTools(
			e.toolRegistry,
			e.config.Tools,
			e.config.ToolSelection,
			e.config.SimpleTools,
			e.permissionRulesSnapshot(),
		)
		projected = filterPlanModelVisibleTools(projected, active, e.toolRegistry)
	}
	projected = filterUnavailableBuiltinToolInfos(projected)
	if !e.goalModelToolsVisible() {
		projected = filterGoalToolInfos(projected)
	}
	return projected
}

func filterGoalToolInfos(infos []*schema.ToolInfo) []*schema.ToolInfo {
	filtered := make([]*schema.ToolInfo, 0, len(infos))
	for _, info := range infos {
		if info == nil || isGoalToolName(info.Name) {
			continue
		}
		filtered = append(filtered, info)
	}
	return filtered
}

func filterUnavailableBuiltinToolInfos(infos []*schema.ToolInfo) []*schema.ToolInfo {
	filtered := make([]*schema.ToolInfo, 0, len(infos))
	for _, info := range infos {
		if info == nil {
			continue
		}
		if _, unavailable := tools.UnavailableBuiltinToolReason(strings.TrimSpace(info.Name)); unavailable {
			continue
		}
		filtered = append(filtered, info)
	}
	return filtered
}

func assembleModelVisibleTools(
	registry *tools.Registry,
	scopedTools []*schema.ToolInfo,
	selection *tools.ToolSelection,
	simple bool,
	rules *permission.RulesEngine,
) []*schema.ToolInfo {
	var allowedNames []string
	if scopedTools != nil {
		allowedNames = make([]string, 0, len(scopedTools))
		for _, info := range scopedTools {
			if info != nil && info.Name != "" {
				allowedNames = append(allowedNames, info.Name)
			}
		}
	}
	var builtInNames []string
	if selection != nil {
		builtInNames = tools.ResolveToolSelection(registry, *selection)
	}
	return tools.AssembleToolPool(registry, tools.ToolPoolOptions{
		AllowedNames: allowedNames,
		BuiltInNames: builtInNames,
		Simple:       simple,
		BlanketDeniedFn: func(name string) bool {
			return rules.IsToolBlanketDenied(name)
		},
	})
}

// toolSelectionDenyRules converts --tools into runtime blanket-deny rules for
// unselected built-ins. MCP tools remain independently controlled, matching the
// reference's base-tools semantics.
func toolSelectionDenyRules(registry *tools.Registry, selection *tools.ToolSelection) []permission.PermissionRule {
	if registry == nil || selection == nil {
		return nil
	}
	selectedNames := tools.ResolveToolSelection(registry, *selection)
	selected := make(map[string]struct{}, len(selectedNames))
	for _, name := range selectedNames {
		selected[strings.TrimSpace(name)] = struct{}{}
	}

	defaultNames := tools.GetToolsForPreset(registry, tools.PresetDefault)
	rules := make([]permission.PermissionRule, 0, len(defaultNames))
	for _, name := range defaultNames {
		if _, ok := selected[name]; ok {
			continue
		}
		rules = append(rules, permission.PermissionRule{
			ToolName: name,
			Action:   permission.ActionDeny,
			Source:   "cli-tools",
		})
	}
	return rules
}

func (e *QueryEngine) wrapCanUseTool(inner CanUseToolFn) CanUseToolFn {
	if invocationPolicyBoundaryFor(inner, e.config.PermissionPrompt) == invocationPolicyNoPolicyInstalled &&
		!e.supportedAutoInvocationPolicyRequired() {
		return nil
	}
	return func(ctx context.Context, toolName string, input map[string]any, toolCtx *ToolUseContext) (bool, string) {
		outcome := e.evaluateInvocationPolicy(ctx, inner, toolName, input, toolCtx)
		return outcome.Allowed, outcome.Reason
	}
}

func (e *QueryEngine) supportedAutoInvocationPolicyRequired() bool {
	if e == nil || e.config.PermissionMode != permission.ModeAuto {
		return false
	}
	return e.supportedInvocationPolicy
}

// evaluateInvocationPolicy is the QueryEngine-owned decision seam. It keeps
// the legacy branch order and side effects while projecting every branch onto
// one internal decision vocabulary before wrapCanUseTool adapts it back to the
// established bool/reason callback contract.
func (e *QueryEngine) evaluateInvocationPolicy(
	ctx context.Context,
	inner CanUseToolFn,
	toolName string,
	input map[string]any,
	toolCtx *ToolUseContext,
) invocationPolicyOutcome {
	actionDescriptor, actionErr := e.buildPermissionActionDescriptor(
		toolName,
		input,
		toolCtx,
	)
	if actionErr != nil {
		return denyInvocationPolicy(actionErr.Error())
	}
	setSettledPermissionAction(ctx, &actionDescriptor)
	if !actionDescriptor.Selected {
		return denyInvocationPolicy("tool excluded by --tools selection")
	}
	input = actionDescriptor.Input
	mode := actionDescriptor.Mode
	planActive := actionDescriptor.PlanActive
	canonicalToolName := actionDescriptor.CanonicalToolName
	planDecision := evaluatePlanToolPolicy(planToolPolicyRequest{
		Active:    planActive,
		ToolName:  canonicalToolName,
		Input:     input,
		SessionID: toolContextSessionID(toolCtx, e.config.SessionID),
		AgentID:   toolContextAgentID(toolCtx, e.config.AgentID),
		Registry:  e.toolRegistry,
	})
	if !planDecision.Allowed {
		return denyInvocationPolicy(planDecision.Reason)
	}
	if planActive && canonicalToolName == "ExitPlanMode" {
		// Exit approval is never satisfied by a generic allow rule, a
		// persisted grant, a hook allow, or an auto-allow mode. It requires
		// one coordinator-owned typed decision for this exact Plan revision.
		allowed, reason := e.promptForTool(
			ctx,
			nil,
			toolName,
			input,
			toolCtx,
			&actionDescriptor,
		)
		return requireHumanInvocationPolicy(allowed, reason)
	}
	// Plan containment is evaluated first. Explicit deny remains
	// authoritative, including for a registered alias of Write/Edit.
	// An admitted exact Plan-file capability then bypasses only ordinary
	// allow/ask, modes, grants, prompting, and classifier behavior.
	rules := e.permissionRulesSnapshot()
	ruleDecision := evaluatePermissionRuleDecision(
		rules,
		e.config.PermissionProjectRoot,
		toolName,
		canonicalToolName,
		input,
	)
	if ruleDecision.Matched && ruleDecision.Action == permission.ActionDeny {
		return denyInvocationPolicy("permission rule denied tool use")
	}
	if canonicalToolName == "EnterPlanMode" &&
		e.config.CommandEntrypoint == commands.EntrypointAppServer {
		// EnterPlanMode only moves the runtime into the more restrictive Plan
		// lifecycle. It is not an authorization request. Plan policy and the
		// explicit deny rule above remain authoritative.
		return allowInvocationPolicy()
	}
	if planDecision.hasExactFileCapability() {
		return allowInvocationPolicy()
	}
	if ruleDecision.Matched &&
		ruleDecision.Action == permission.ActionAsk {
		allowed, reason := e.promptForTool(
			ctx,
			inner,
			toolName,
			input,
			toolCtx,
			&actionDescriptor,
		)
		return e.recordPromptPolicyOutcome(
			ctx,
			actionDescriptor,
			allowed,
			reason,
			false,
		)
	}
	if ruleDecision.Matched &&
		ruleDecision.Action == permission.ActionAllow {
		if mode != permission.ModeAuto ||
			autoRuleAuthorizes(ruleDecision) {
			return allowInvocationPolicy()
		}
	}

	// In bypass mode, skip the inner permission check entirely and auto-allow.
	// Mirrors permissions.ts step 2a.
	if mode.ShouldAutoAllow() {
		return allowInvocationPolicy()
	}
	if actionDescriptor.InternalStateDefaultSafe {
		return allowInvocationPolicy()
	}

	// In DontAsk mode, tools not explicitly allowed by rules or declared
	// default-safe by their contract are auto-denied.
	// This prevents the pipeline from reaching the inner callback (which would
	// block waiting for user input). Mirrors permissions.ts dontAsk behavior.
	if mode == permission.ModeDontAsk {
		return denyInvocationPolicy("permission denied (dont-ask mode: no interactive prompting)")
	}

	if e.approvalAuthorizesAction(actionDescriptor) {
		return allowInvocationPolicy()
	}
	_, scopedPath, _ := permissionInvocation(
		e.config.PermissionProjectRoot,
		canonicalToolName,
		input,
	)
	if isMemoryReadTool(toolName) && e.memoryPathAllowed(scopedPath) {
		return allowInvocationPolicy()
	}

	// The reference grants local filesystem reads and searches inside the
	// working directory in normal modes. Outside paths still require a prompt.
	workingDirs := e.GetWorkingDirectories()
	if isAllowedWorkingDirectoryRead(workingDirs, canonicalToolName, input) {
		return allowInvocationPolicy()
	}

	if mode != permission.ModePlan && isMemoryWriteTool(canonicalToolName) && e.memoryPathAllowed(scopedPath) {
		return allowInvocationPolicy()
	}

	// In acceptEdits mode, auto-allow contained file writes and edits.
	// Mirrors filesystem.ts:1360-1375.
	if mode == permission.ModeAcceptEdits {
		if permission.AcceptEditsCheck(canonicalToolName, input, e.config.CWD, workingDirs[1:]...) {
			return allowInvocationPolicy()
		}
	}

	// In auto mode, use the contained Write/Edit acceptEdits fast-path before
	// calling the expensive classifier/inner check.
	// Excludes Agent tool (its checkPermissions returns allow in acceptEdits,
	// which would silently bypass the classifier).
	// Mirrors permissions.ts:593-655.
	if mode == permission.ModeAuto {
		if permission.AcceptEditsCheck(canonicalToolName, input, e.config.CWD, workingDirs[1:]...) {
			return allowInvocationPolicy()
		}
	}

	if mode == permission.ModeAuto {
		if requiresHuman, reason := actionDescriptor.requiresHumanCapabilityInAuto(); requiresHuman {
			allowed, promptReason := e.promptForTool(
				ctx,
				inner,
				toolName,
				input,
				toolCtx,
				&actionDescriptor,
			)
			if strings.TrimSpace(promptReason) == "" {
				promptReason = reason
			}
			return e.recordPromptPolicyOutcome(
				ctx,
				actionDescriptor,
				allowed,
				promptReason,
				false,
			)
		}
	}

	// The reviewer is a best-effort, non-authoritative shadow. It starts only
	// after all deterministic Auto gates and before the legacy fallback.
	reviewAuditRef := e.launchPermissionReview(ctx, actionDescriptor, toolCtx)
	ctx = withPermissionReviewAuditRef(ctx, reviewAuditRef)

	reviewFallback := false
	if mode == permission.ModeAuto &&
		projectGraphHITLProbeFromContext(ctx) == nil &&
		e.config.ChatModel != nil &&
		!e.denialTracking.ShouldFallbackToPrompting() {
		reviewFallback = true
		ReportClassifierStatusChecking(ctx, toolName)
		providerUsage, usageErr := e.providerUsageForPotentialGoalCall()
		if usageErr != nil {
			ReportClassifierStatusCleared(ctx, toolName)
			return reviewInvocationPolicy(
				false,
				fmt.Sprintf(
					"auto classifier provider accounting: %v",
					usageErr,
				),
			)
		}
		decision, classifyErr := permission.ClassifyToolUse(ctx, &permission.ClassifierConfig{
			ChatModel:     e.config.ChatModel,
			ProviderUsage: providerUsage,
		}, canonicalToolName, input, e.GetMessages())
		if classifyErr != nil || decision == permission.ClassifierAsk {
			ReportClassifierStatusCleared(ctx, toolName)
		} else {
			result := permission.PermissionResult{
				ToolName: toolName,
				Reason:   permission.ReasonClassifier,
			}
			switch decision {
			case permission.ClassifierAllow:
				e.recordPermissionReviewComparison(
					ctx,
					reviewAuditRef,
					"legacy_classifier",
					"allow",
				)
				result.Decision = permission.DecisionAllow
				result.Message = "auto classifier approved"
				ReportClassifierStatusCompleted(ctx, result)
				e.denialTracking.RecordSuccess()
				return reviewInvocationPolicy(true, "")
			case permission.ClassifierDeny:
				e.recordPermissionReviewComparison(
					ctx,
					reviewAuditRef,
					"legacy_classifier",
					"deny",
				)
				result.Decision = permission.DecisionDeny
				result.Message = "auto classifier denied"
				ReportClassifierStatusCompleted(ctx, result)
				e.denialTracking.RecordDenialWithDetails(toolName, cloneInputMap(input), permission.ReasonClassifier, result.Message)
				e.mu.Lock()
				e.permissionDenials = append(e.permissionDenials, permission.Denial{
					ToolName:  sdkCompatToolName(toolName),
					ToolUseID: currentToolUseID(ctx),
					Input:     cloneInputMap(input),
				})
				e.mu.Unlock()
				return reviewInvocationPolicy(false, result.Message)
			}
		}
	}

	// Fallback-to-prompting: when auto mode denial thresholds are exceeded,
	// annotate the context so the inner callback knows to prompt the user
	// interactively instead of auto-classifying.
	// Mirrors permissions.ts:879-901.
	callCtx := ctx
	if mode == permission.ModeAuto && e.denialTracking.ShouldFallbackToPrompting() {
		callCtx = permission.WithFallbackPrompting(ctx)
		// Notify via hooks that auto-mode is falling back to prompting.
		if e.config.HookExecutor != nil {
			e.config.HookExecutor.ExecuteNotification(
				containment.WithSnapshot(ctx, e.executionPolicy), "warning",
				"Auto-mode falling back to interactive prompting due to repeated denials",
				map[string]any{"consecutive_denials": e.denialTracking.ConsecutiveDenials})
		}
	}

	allowed, reason := e.promptForTool(
		callCtx,
		inner,
		toolName,
		input,
		toolCtx,
		&actionDescriptor,
	)
	return e.recordPromptPolicyOutcome(
		ctx,
		actionDescriptor,
		allowed,
		reason,
		reviewFallback,
	)
}

func (e *QueryEngine) recordPromptPolicyOutcome(
	ctx context.Context,
	action PermissionActionDescriptor,
	allowed bool,
	reason string,
	reviewFallback bool,
) invocationPolicyOutcome {
	if probe := projectGraphHITLProbeFromContext(ctx); probe != nil &&
		probe.captured != nil {
		if reviewFallback {
			return reviewInvocationPolicy(allowed, reason)
		}
		return requireHumanInvocationPolicy(allowed, reason)
	}
	input := action.Input
	if updated := updatedInputFromContext(ctx); updated != nil {
		input = updated
	}
	e.mu.Lock()
	if !allowed {
		e.permissionDenials = append(e.permissionDenials, permission.Denial{
			ToolName:  sdkCompatToolName(action.RequestedToolName),
			ToolUseID: currentToolUseID(ctx),
			Input:     cloneInputMap(input),
		})
		// Only track denials for auto mode (fallback-to-prompting threshold).
		// Mirrors permissions.ts:879-901.
		if action.Mode.IsDenialTrackingEnabled() {
			e.denialTracking.RecordDenial()
		}
	} else if action.Mode.IsDenialTrackingEnabled() {
		e.denialTracking.RecordSuccess()
	}
	e.mu.Unlock()
	if reviewFallback {
		return reviewInvocationPolicy(allowed, reason)
	}
	return requireHumanInvocationPolicy(allowed, reason)
}

func (e *QueryEngine) memoryPathAllowed(path string) bool {
	if !e.config.EnablePersistentMemory || strings.TrimSpace(path) == "" {
		return false
	}
	return memdir.IsSafeAutoMemPathForProject(path, e.config.MemoryProjectRoot) ||
		memdir.IsSafeTeamMemPath(path) ||
		memdir.IsSafeAgentMemoryPathForProject(path, e.config.MemoryProjectRoot)
}

func isMemoryReadTool(toolName string) bool {
	switch sdkCompatToolName(toolName) {
	case "Read", "Grep", "Glob":
		return true
	default:
		return false
	}
}

func isMemoryWriteTool(toolName string) bool {
	switch sdkCompatToolName(toolName) {
	case "Write", "Edit":
		return true
	default:
		return false
	}
}

func (e *QueryEngine) toolAllowedBySelectionNames(
	requestedToolName string,
	canonicalToolName string,
	origin tools.ToolOrigin,
) bool {
	selection := e.config.ToolSelection
	if selection == nil ||
		origin == tools.ToolOriginMCP ||
		tools.IsMCPToolName(requestedToolName) ||
		tools.IsMCPToolName(canonicalToolName) {
		return true
	}
	if e.toolRegistry == nil && selection.Preset != "" {
		return true
	}
	for _, selected := range tools.ResolveToolSelection(e.toolRegistry, *selection) {
		if selected == requestedToolName || selected == canonicalToolName {
			return true
		}
	}
	return false
}

func (e *QueryEngine) promptForTool(
	ctx context.Context,
	inner CanUseToolFn,
	toolName string,
	input map[string]any,
	toolCtx *ToolUseContext,
	initialAction *PermissionActionDescriptor,
) (bool, string) {
	if initialAction == nil {
		action, err := e.buildPermissionActionDescriptor(toolName, input, toolCtx)
		if err != nil {
			return false, err.Error()
		}
		initialAction = &action
	}
	isPlanApproval := initialAction.CanonicalToolName == "ExitPlanMode"
	graphHITL := projectGraphHITLProbeFromContext(ctx) != nil ||
		projectGraphHITLExecutionFromContext(ctx) != nil
	if graphHITL &&
		e.config.PermissionPrompt == nil &&
		inner != nil &&
		!isPlanApproval {
		// CanUseTool remains the external decision owner for ordinary tools when
		// no structured PermissionPrompt is configured. It is not a second
		// policy layer behind a persisted Graph decision, so Graph calls it live
		// rather than converting or bypassing its result. ExitPlanMode is
		// excluded because its revision-bound decision must stay structured.
		return e.invokeLegacyPermissionCallback(
			ctx,
			inner,
			toolName,
			input,
			toolCtx,
			*initialAction,
		)
	}
	if e.config.PermissionPrompt == nil && !graphHITL {
		if isPlanApproval {
			return false, "structured Plan approval prompting not available"
		}
		if inner == nil {
			return false, "interactive permission prompting not available"
		}
		return e.invokeLegacyPermissionCallback(
			ctx,
			inner,
			toolName,
			input,
			toolCtx,
			*initialAction,
		)
	}
	emit := permissionPromptEmitter(ctx)
	requestID := currentToolUseID(ctx)
	var planApproval *PlanApprovalRequest
	if isPlanApproval {
		state := e.PlanState()
		if graphHITL &&
			state.Phase == PlanPhaseAwaitingApproval &&
			state.ApprovalRequestID == requestID {
			planApproval = &PlanApprovalRequest{
				RequestID:         requestID,
				PlanRevision:      state.Revision,
				PlanFileIdentity:  state.PlanFileIdentity,
				InitialPlanDigest: state.ApprovalInitialDigest,
				ReturnMode:        state.ReturnMode,
			}
		} else {
			var approvalErr error
			planApproval, approvalErr = e.beginPlanApproval(requestID, emit)
			if approvalErr != nil {
				return false, approvalErr.Error()
			}
		}
	}
	sessionID := e.config.SessionID
	threadID := e.config.ThreadID
	agentID := e.config.AgentID
	if toolCtx != nil {
		if strings.TrimSpace(toolCtx.SessionID) != "" {
			sessionID = toolCtx.SessionID
		}
		if strings.TrimSpace(toolCtx.ThreadID) != "" {
			threadID = toolCtx.ThreadID
		}
		if strings.TrimSpace(toolCtx.AgentID) != "" {
			agentID = toolCtx.AgentID
		}
	}
	grantEvaluator := e.permissionGrantEvaluator(*initialAction, toolCtx)
	if planApproval != nil {
		// Plan approval is never a coalescing candidate. A grant committed by
		// another ordinary permission request cannot answer this exact revision.
		grantEvaluator = nil
	}
	request := PermissionPromptRequest{
		Kind:              permissionPromptKind(toolName, planApproval),
		Source:            "coordinator",
		ToolName:          toolName,
		CanonicalToolName: initialAction.CanonicalToolName,
		ToolUseID:         currentToolUseID(ctx),
		Input:             cloneInputMap(input),
		Message:           e.SessionApprovalDescription(toolName, input),
		SessionScope:      e.SessionApprovalDescription(toolName, input),
		ProjectIdentity:   e.permissionProjectIdentity,
		RootSessionID:     e.permissionRootSessionID,
		SessionID:         sessionID,
		ThreadID:          threadID,
		AgentID:           agentID,
		ToolContext:       toolCtx,
		PlanApproval:      planApproval,
		Presentation: permissionPresentationForAction(
			permissionPromptKind(toolName, planApproval),
			*initialAction,
		),
		action: initialAction,
	}
	if graphHITL {
		return e.resolveProjectGraphHITLPermission(ctx, request, emit)
	}
	result := e.permissionCoordinator.request(
		ctx,
		e.permissionEngineID,
		request,
		e.config.PermissionPrompt,
		emit,
		func(winner PermissionInteractionResult) PermissionInteractionResult {
			if planApproval != nil {
				return e.settlePlanApproval(
					planApproval,
					winner,
					emit,
				)
			}
			return e.settlePermissionInteraction(
				*initialAction,
				toolCtx,
				winner,
			)
		},
		grantEvaluator,
	)
	if planApproval != nil {
		result = e.ensurePlanApprovalSettled(planApproval, result, emit)
		if result.Allowed() {
			dispatchAction, actionErr := e.buildPermissionActionDescriptor(
				initialAction.RequestedToolName,
				initialAction.Input,
				toolCtx,
			)
			if actionErr != nil {
				result = PermissionInteractionResult{
					Decision: PermissionDeny,
					Message: "Plan permission dispatch action rebuild failed: " +
						actionErr.Error(),
				}
			} else {
				result.UpdatedInput = dispatchAction.Input
				result.settledAction = &dispatchAction
			}
		}
		if result.PlanApproval != nil {
			SetPlanApprovalDecision(ctx, result.PlanApproval)
		}
	}
	if result.UpdatedInput != nil {
		SetUpdatedInput(ctx, result.UpdatedInput)
	}
	if result.settledAction != nil {
		setSettledPermissionAction(ctx, result.settledAction)
	}
	e.recordPermissionReviewHumanComparison(ctx, *initialAction, result)
	return result.Allowed(), result.Message
}

func permissionPromptKind(toolName string, planApproval *PlanApprovalRequest) string {
	if planApproval != nil {
		return PermissionInteractionKindPlanApproval
	}
	if strings.EqualFold(strings.TrimSpace(toolName), "AskUserQuestion") {
		return PermissionInteractionKindQuestion
	}
	return PermissionInteractionKindPermission
}

func (e *QueryEngine) invokeLegacyPermissionCallback(
	ctx context.Context,
	inner CanUseToolFn,
	toolName string,
	input map[string]any,
	toolCtx *ToolUseContext,
	initialAction PermissionActionDescriptor,
) (bool, string) {
	if inner == nil {
		return false, "interactive permission prompting not available"
	}
	var updatedInput map[string]any
	callbackCtx := withUpdatedInputPtr(ctx, &updatedInput)
	allowed, reason := inner(callbackCtx, toolName, cloneInputMap(input), toolCtx)
	result := PermissionInteractionResult{
		Decision: PermissionDeny,
		Message:  reason,
	}
	if allowed {
		result.Decision = PermissionAllowOnce
		result.UpdatedInput = updatedInput
	}
	result = e.settlePermissionInteraction(initialAction, toolCtx, result)
	if result.UpdatedInput != nil {
		SetUpdatedInput(ctx, result.UpdatedInput)
	}
	if result.settledAction != nil {
		setSettledPermissionAction(ctx, result.settledAction)
	}
	return result.Allowed(), result.Message
}

func (e *QueryEngine) settlePermissionInteraction(
	initialAction PermissionActionDescriptor,
	toolCtx *ToolUseContext,
	result PermissionInteractionResult,
) PermissionInteractionResult {
	result = normalizePermissionInteractionResult(result)
	if !result.Allowed() {
		return result
	}
	finalInput := initialAction.Input
	if result.UpdatedInput != nil {
		finalInput = result.UpdatedInput
	}
	finalAction, err := e.buildPermissionActionDescriptor(
		initialAction.RequestedToolName,
		finalInput,
		toolCtx,
	)
	if err != nil {
		return PermissionInteractionResult{
			Decision:     PermissionDeny,
			Message:      "permission final action rebuild failed: " + err.Error(),
			UpdatedInput: cloneInputMap(finalInput),
		}
	}
	bindingMatches := samePermissionActionAuthorityBinding(
		initialAction,
		finalAction,
	)
	if initialAction.CanonicalInput == finalAction.CanonicalInput {
		bindingMatches = samePermissionActionBinding(
			initialAction,
			finalAction,
		)
	}
	if !bindingMatches {
		return PermissionInteractionResult{
			Decision:      PermissionDeny,
			Message:       "permission action or policy changed while awaiting confirmation",
			UpdatedInput:  finalAction.Input,
			settledAction: &finalAction,
		}
	}
	if !finalAction.Selected {
		return PermissionInteractionResult{
			Decision:      PermissionDeny,
			Message:       "tool excluded by --tools selection",
			UpdatedInput:  finalAction.Input,
			settledAction: &finalAction,
		}
	}
	planDecision := evaluatePlanToolPolicy(planToolPolicyRequest{
		Active:    finalAction.PlanActive,
		ToolName:  finalAction.CanonicalToolName,
		Input:     finalAction.Input,
		SessionID: finalAction.SessionID,
		AgentID:   finalAction.AgentID,
		Registry:  e.toolRegistry,
	})
	if !planDecision.Allowed {
		return PermissionInteractionResult{
			Decision:      PermissionDeny,
			Message:       planDecision.Reason,
			UpdatedInput:  finalAction.Input,
			settledAction: &finalAction,
		}
	}
	ruleDecision := evaluatePermissionRuleDecision(
		e.permissionRulesSnapshot(),
		e.config.PermissionProjectRoot,
		finalAction.RequestedToolName,
		finalAction.CanonicalToolName,
		finalAction.Input,
	)
	if ruleDecision.Matched &&
		ruleDecision.Action == permission.ActionDeny {
		return PermissionInteractionResult{
			Decision:      PermissionDeny,
			Message:       "permission rule denied final tool use",
			UpdatedInput:  finalAction.Input,
			settledAction: &finalAction,
		}
	}
	preCommitPolicy := e.effectivePolicySnapshot(toolCtx)
	if preCommitPolicy.ID() != finalAction.PolicySnapshotID {
		return PermissionInteractionResult{
			Decision:      PermissionDeny,
			Message:       "permission policy changed before grant settlement",
			UpdatedInput:  finalAction.Input,
			settledAction: &finalAction,
		}
	}
	result.UpdatedInput = finalAction.Input
	result = e.commitPermissionInteraction(finalAction, result)
	if !result.Allowed() {
		return result
	}
	postCommitPolicy := e.effectivePolicySnapshot(toolCtx)
	if !e.permissionCommitTransitionExpected(
		preCommitPolicy,
		postCommitPolicy,
		finalAction,
		result.Decision,
	) {
		return PermissionInteractionResult{
			Decision:      PermissionDeny,
			Message:       "permission policy changed while settling grant",
			UpdatedInput:  finalAction.Input,
			settledAction: &finalAction,
		}
	}
	dispatchAction, err := e.buildPermissionActionDescriptor(
		initialAction.RequestedToolName,
		finalAction.Input,
		toolCtx,
	)
	if err != nil {
		return PermissionInteractionResult{
			Decision:     PermissionDeny,
			Message:      "permission dispatch action rebuild failed: " + err.Error(),
			UpdatedInput: finalAction.Input,
		}
	}
	expectedDispatchBinding := finalAction
	expectedDispatchBinding.PolicySnapshotID = postCommitPolicy.ID()
	if !samePermissionActionBinding(
		expectedDispatchBinding,
		dispatchAction,
	) {
		return PermissionInteractionResult{
			Decision:      PermissionDeny,
			Message:       "permission action changed while settling confirmation",
			UpdatedInput:  dispatchAction.Input,
			settledAction: &dispatchAction,
		}
	}
	dispatchPlanDecision := evaluatePlanToolPolicy(planToolPolicyRequest{
		Active:    dispatchAction.PlanActive,
		ToolName:  dispatchAction.CanonicalToolName,
		Input:     dispatchAction.Input,
		SessionID: dispatchAction.SessionID,
		AgentID:   dispatchAction.AgentID,
		Registry:  e.toolRegistry,
	})
	if !dispatchAction.Selected || !dispatchPlanDecision.Allowed {
		reason := dispatchPlanDecision.Reason
		if !dispatchAction.Selected {
			reason = "tool excluded by --tools selection"
		}
		return PermissionInteractionResult{
			Decision:      PermissionDeny,
			Message:       reason,
			UpdatedInput:  dispatchAction.Input,
			settledAction: &dispatchAction,
		}
	}
	dispatchRuleDecision := evaluatePermissionRuleDecision(
		e.permissionRulesSnapshot(),
		e.config.PermissionProjectRoot,
		dispatchAction.RequestedToolName,
		dispatchAction.CanonicalToolName,
		dispatchAction.Input,
	)
	if dispatchRuleDecision.Matched &&
		dispatchRuleDecision.Action == permission.ActionDeny {
		return PermissionInteractionResult{
			Decision:      PermissionDeny,
			Message:       "permission rule denied settled tool use",
			UpdatedInput:  dispatchAction.Input,
			settledAction: &dispatchAction,
		}
	}
	result.UpdatedInput = dispatchAction.Input
	result.settledAction = &dispatchAction
	return result
}

func (e *QueryEngine) commitPermissionInteraction(
	action PermissionActionDescriptor,
	result PermissionInteractionResult,
) PermissionInteractionResult {
	switch result.Decision {
	case PermissionAllowSession:
		if err := e.ApproveForSession(action.CanonicalToolName, action.Input); err != nil {
			return PermissionInteractionResult{
				Decision:      PermissionDeny,
				Message:       "session approval failed: " + err.Error(),
				UpdatedInput:  action.Input,
				settledAction: &action,
			}
		}
	case PermissionAllowAlways:
		if err := e.PersistPermissionRule(action.CanonicalToolName, action.Input); err != nil {
			return PermissionInteractionResult{
				Decision:      PermissionDeny,
				Message:       "always-allow failed: " + err.Error(),
				UpdatedInput:  action.Input,
				settledAction: &action,
			}
		}
	}
	return result
}

func (e *QueryEngine) permissionGrantEvaluator(
	initialAction PermissionActionDescriptor,
	toolCtx *ToolUseContext,
) func(permissionCoalescingGrant) (PermissionActionDescriptor, bool) {
	return func(
		grant permissionCoalescingGrant,
	) (PermissionActionDescriptor, bool) {
		if e == nil {
			return PermissionActionDescriptor{}, false
		}
		action, err := e.buildPermissionActionDescriptor(
			initialAction.RequestedToolName,
			initialAction.Input,
			toolCtx,
		)
		if err != nil ||
			!action.Selected ||
			action.CanonicalToolName != initialAction.CanonicalToolName ||
			action.CapabilityGeneration != initialAction.CapabilityGeneration ||
			action.Mode != initialAction.Mode ||
			action.PlanActive != initialAction.PlanActive ||
			action.PlanRevision != initialAction.PlanRevision ||
			action.PlanFileIdentity != initialAction.PlanFileIdentity {
			return PermissionActionDescriptor{}, false
		}
		ruleDecision := evaluatePermissionRuleDecision(
			e.permissionRulesSnapshot(),
			e.config.PermissionProjectRoot,
			action.RequestedToolName,
			action.CanonicalToolName,
			action.Input,
		)
		if ruleDecision.Matched &&
			ruleDecision.Action != permission.ActionAllow {
			return PermissionActionDescriptor{}, false
		}
		command, scopedPath, fingerprint := permissionInvocation(
			e.config.PermissionProjectRoot,
			action.CanonicalToolName,
			action.Input,
		)

		switch grant.Decision {
		case PermissionAllowSession:
			if action.Mode == permission.ModeDontAsk {
				return PermissionActionDescriptor{}, false
			}
			if !grant.SessionKey.MatchesInvocation(
				action.CanonicalToolName,
				command,
				scopedPath,
				fingerprint,
			) ||
				!e.approvalTracker.IsApprovedInvocationForRootSession(
					action.CanonicalToolName,
					command,
					scopedPath,
					fingerprint,
					e.permissionRootSessionID,
				) {
				return PermissionActionDescriptor{}, false
			}
			if action.Mode != permission.ModeAuto {
				return action, true
			}
			return action, grant.SessionKey.ExactCommand ||
				grant.SessionKey.ExactPath ||
				grant.SessionKey.InputFingerprint != "" ||
				(grant.SessionKey.RecursivePath &&
					action.PathWithinRoots &&
					isContainedReadAction(action.CanonicalToolName))

		case PermissionAllowAlways:
			sourceRule := permission.NewRulesEngine([]permission.PermissionRule{grant.AlwaysRule})
			sourceDecision := evaluatePermissionRuleDecision(
				sourceRule,
				e.config.PermissionProjectRoot,
				action.CanonicalToolName,
				action.CanonicalToolName,
				action.Input,
			)
			if !sourceDecision.Matched ||
				sourceDecision.Action != permission.ActionAllow ||
				!sourceDecision.ToolExact ||
				!sourceDecision.InputExact {
				return PermissionActionDescriptor{}, false
			}
			// The source commit is durable before the coordinator scans. Only an
			// exact candidate may reload it: refreshing an unrelated pending engine
			// would mutate that engine's policy snapshot before its own settlement.
			e.reloadPermissionRules()
			committedDecision := evaluatePermissionRuleDecision(
				e.permissionRulesSnapshot(),
				e.config.PermissionProjectRoot,
				action.RequestedToolName,
				action.CanonicalToolName,
				action.Input,
			)
			if !committedDecision.Matched ||
				committedDecision.Action != permission.ActionAllow {
				return PermissionActionDescriptor{}, false
			}
			if action.Mode == permission.ModeAuto {
				return action, autoRuleAuthorizes(committedDecision)
			}
			return action, true
		default:
			return PermissionActionDescriptor{}, false
		}
	}
}

// activePermissionMode returns the effective permission mode, preferring
// the ToolUseContext options over the engine config default.
func (e *QueryEngine) activePermissionMode(toolCtx *ToolUseContext) permission.Mode {
	if planPhaseRequiresContainment(e.PlanState().Phase) {
		return permission.ModePlan
	}
	if toolCtx != nil && toolCtx.Options != nil && toolCtx.Options.PermissionMode != "" {
		return toolCtx.Options.PermissionMode
	}
	return e.PermissionMode()
}

func sdkCompatToolName(name string) string {
	if strings.EqualFold(strings.TrimSpace(name), "Agent") {
		return "Task"
	}
	return name
}

type toolUseIDContextKey struct{}

type canonicalToolNameContextKey struct{}

func withToolUseID(ctx context.Context, toolUseID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, toolUseIDContextKey{}, toolUseID)
}

func currentToolUseID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(toolUseIDContextKey{}).(string); ok {
		return v
	}
	return ""
}

func withCanonicalToolName(ctx context.Context, toolName string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, canonicalToolNameContextKey{}, strings.TrimSpace(toolName))
}

func currentCanonicalToolName(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	toolName, _ := ctx.Value(canonicalToolNameContextKey{}).(string)
	return strings.TrimSpace(toolName)
}

// ToolUseIDFromContext returns the stable tool call identity visible to
// interactive adapters such as the TUI permission callback.
func ToolUseIDFromContext(ctx context.Context) string {
	return currentToolUseID(ctx)
}

type updatedInputPtrKey struct{}

type planApprovalDecisionPtrKey struct{}

// withUpdatedInputPtr stores a pointer to a map[string]any in the context.
// The CanUseTool callback can write updated input through this pointer so the
// engine picks it up as PermissionResult.UpdatedInput.
func withUpdatedInputPtr(ctx context.Context, ptr *map[string]any) context.Context {
	return context.WithValue(ctx, updatedInputPtrKey{}, ptr)
}

// SetUpdatedInput stores updated tool input via the context pointer.
// Called by the TUI permission callback to pass modified input (e.g., user
// answers for AskUserQuestion) back to the engine.
func SetUpdatedInput(ctx context.Context, input map[string]any) {
	if ptr, ok := ctx.Value(updatedInputPtrKey{}).(*map[string]any); ok && ptr != nil {
		*ptr = input
	}
}

func updatedInputFromContext(ctx context.Context) map[string]any {
	if ctx == nil {
		return nil
	}
	ptr, ok := ctx.Value(updatedInputPtrKey{}).(*map[string]any)
	if !ok || ptr == nil || *ptr == nil {
		return nil
	}
	return cloneInputMap(*ptr)
}

func withPlanApprovalDecisionPtr(
	ctx context.Context,
	ptr **PlanApprovalDecision,
) context.Context {
	return context.WithValue(ctx, planApprovalDecisionPtrKey{}, ptr)
}

// SetPlanApprovalDecision delivers one coordinator-validated decision to the
// canonical ExitPlanMode execution path without treating model-owned tool
// input as approval authority.
func SetPlanApprovalDecision(
	ctx context.Context,
	decision *PlanApprovalDecision,
) {
	if ctx == nil || decision == nil {
		return
	}
	ptr, ok := ctx.Value(planApprovalDecisionPtrKey{}).(**PlanApprovalDecision)
	if !ok || ptr == nil {
		return
	}
	*ptr = clonePlanApprovalDecision(decision)
}

// ResolvePermission delivers a user's permission decision for a pending
// interactive permission request. Returns false if the request was already
// resolved or not found. This is called by the UI layer in response to
// an EventPermissionRequest event.
// Mirrors the resolveOnce pattern from PermissionContext.ts.
func (e *QueryEngine) ResolvePermission(toolUseID string, decision permission.PermissionDecision, reason string) bool {
	result := PermissionInteractionResult{Decision: PermissionDeny, Message: reason}
	if decision == permission.DecisionAllow {
		result.Decision = PermissionAllowOnce
	}
	return e.ResolvePermissionInteraction(toolUseID, result)
}

// ResolvePermissionInteraction resolves an event-driven request through the
// same atomic terminal claim used by structured adapters.
func (e *QueryEngine) ResolvePermissionInteraction(toolUseID string, result PermissionInteractionResult) bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	checkpoint := e.projectGraphCheckpoint
	coordinator := e.inputCoordinator
	coordinatorErr := e.inputCoordinatorErr
	e.mu.Unlock()
	if checkpoint != nil {
		if active, ok := checkpoint.ActiveInterrupt(); ok {
			if strings.TrimSpace(toolUseID) != active.RequestID ||
				coordinator == nil ||
				coordinatorErr != nil {
				return false
			}
			result = normalizePermissionInteractionResult(
				clonePermissionInteractionResult(result),
			)
			item := RuntimeItem{
				ID:         projectGraphPermissionDecisionItemID(active),
				Kind:       RuntimeItemPermissionDecision,
				Priority:   RuntimePriorityNow,
				Scope:      active.Scope,
				IsMeta:     true,
				Origin:     "project_graph",
				Provenance: "eino-compose-stateful-interrupt",
				PermissionDecision: &RuntimePermissionDecision{
					Version:          projectGraphHITLDecisionVersion,
					RequestID:        active.RequestID,
					InterruptID:      active.InterruptID,
					InvocationDigest: active.InvocationDigest,
					PolicyRevision:   active.PolicyRevision,
					Result:           result,
				},
			}
			_, err := coordinator.EnqueueBounded(item, 1)
			return err == nil
		}
	}
	if e.permissionCoordinator == nil {
		return false
	}
	return e.permissionCoordinator.resolve(e.permissionEngineID, toolUseID, result, "external")
}

type conversationHistory struct {
	messages         []*schema.Message
	pendingAssistant *schema.Message
	observeAssistant func(*schema.Message)
}

func newConversationHistory(base []*schema.Message) *conversationHistory {
	copied := append([]*schema.Message(nil), base...)
	return &conversationHistory{messages: copied}
}

func (h *conversationHistory) Observe(evt QueryEvent) {
	switch evt.Type {
	case EventAssistant:
		if evt.AssistantMessage != nil {
			h.flushPendingAssistant()
			h.messages = append(h.messages, evt.AssistantMessage)
			if h.observeAssistant != nil {
				h.observeAssistant(evt.AssistantMessage)
			}
			return
		}
		if evt.Message != nil {
			if evt.Message.Role == schema.Tool {
				h.flushPendingAssistant()
				h.messages = append(h.messages, evt.Message)
				return
			}
			if h.pendingAssistant == nil {
				h.pendingAssistant = &schema.Message{Role: schema.Assistant}
			}
			mergeAssistantChunk(h.pendingAssistant, evt.Message)
		}
	case EventToolResult:
		h.flushPendingAssistant()
		if evt.ToolResultMessage != nil {
			h.messages = append(h.messages, evt.ToolResultMessage)
		} else if evt.Message != nil {
			h.messages = append(h.messages, evt.Message)
		}
	case EventAttachment:
		h.flushPendingAssistant()
		if evt.AttachmentMessage != nil {
			h.messages = append(h.messages, evt.AttachmentMessage)
		}
	case EventCompactBoundary:
		h.flushPendingAssistant()
		if len(evt.compactBoundaryMessages) > 0 {
			h.messages = append(
				[]*schema.Message(nil),
				evt.compactBoundaryMessages...,
			)
			return
		}
		if evt.CompactBoundaryMessage != nil {
			if isCompactBoundaryStart(evt.CompactBoundaryMessage) {
				h.messages = []*schema.Message{evt.CompactBoundaryMessage}
				return
			}
			h.messages = append(h.messages, evt.CompactBoundaryMessage)
		}
	case EventMaxTurnsReached:
		h.flushPendingAssistant()
		// Complete and normalize every assistant tool call before appending the
		// max-turn marker. Without this, the next SubmitMessage can send an
		// orphaned tool call or an empty tool_call_id to the provider.
		h.messages = repairLoadedMessages(h.messages)
		if msg := newMaxTurnsAttachmentMessage(evt.MaxTurnsInfo); msg != nil {
			h.messages = append(h.messages, msg)
		}
	case EventStreamRequestStart:
		h.flushPendingAssistant()
	case EventTombstone:
		h.pendingAssistant = nil
	case EventUserInterruption:
		h.pendingAssistant = nil
	}
}

func newMaxTurnsAttachmentMessage(info *MaxTurnsInfo) *schema.Message {
	if info == nil {
		return nil
	}
	return &schema.Message{
		Role:    schema.User,
		Content: fmt.Sprintf("Reached maximum number of turns (%d)", info.MaxTurns),
		Extra: map[string]any{
			"is_meta":         true,
			"attachment_kind": "max_turns_reached",
			"max_turns":       info.MaxTurns,
			"turn_count":      info.TurnCount,
		},
	}
}

func (h *conversationHistory) Messages() []*schema.Message {
	h.flushPendingAssistant()
	return append([]*schema.Message(nil), h.messages...)
}

func (h *conversationHistory) Replace(messages []*schema.Message) {
	if h == nil {
		return
	}
	h.pendingAssistant = nil
	h.messages = append([]*schema.Message(nil), messages...)
}

func (h *conversationHistory) flushPendingAssistant() {
	if h.pendingAssistant == nil {
		return
	}
	message := h.pendingAssistant
	h.messages = append(h.messages, message)
	if h.observeAssistant != nil {
		h.observeAssistant(message)
	}
	h.pendingAssistant = nil
}

func isCompactBoundaryStart(msg *schema.Message) bool {
	if msg == nil || msg.Role != schema.System || msg.Extra == nil {
		return false
	}
	subtype, _ := msg.Extra["subtype"].(string)
	return subtype == "compact_boundary"
}

func mergeAssistantChunk(dst, chunk *schema.Message) {
	if dst == nil || chunk == nil {
		return
	}
	if chunk.Role != "" {
		dst.Role = chunk.Role
	}
	if chunk.Content != "" {
		dst.Content += chunk.Content
	}
	if chunk.ReasoningContent != "" {
		dst.ReasoningContent += chunk.ReasoningContent
	}
	if len(chunk.AssistantGenMultiContent) > 0 {
		dst.AssistantGenMultiContent = append(dst.AssistantGenMultiContent, chunk.AssistantGenMultiContent...)
	}
	if chunk.Extra != nil {
		if messageID, ok := chunk.Extra[assistantMessageIDExtraKey]; ok {
			if dst.Extra == nil {
				dst.Extra = make(map[string]any)
			}
			dst.Extra[assistantMessageIDExtraKey] = messageID
		}
	}
	mergeAssistantResponseMeta(dst, chunk)
	for _, tc := range chunk.ToolCalls {
		mergeAssistantToolCall(dst, tc)
	}
}

func mergeAssistantResponseMeta(dst, chunk *schema.Message) {
	if dst == nil || chunk == nil || chunk.ResponseMeta == nil {
		return
	}
	if dst.ResponseMeta == nil {
		dst.ResponseMeta = &schema.ResponseMeta{}
	}
	if chunk.ResponseMeta.FinishReason != "" {
		dst.ResponseMeta.FinishReason = chunk.ResponseMeta.FinishReason
	}
	usage := chunk.ResponseMeta.Usage
	if usage == nil {
		return
	}
	if dst.ResponseMeta.Usage == nil {
		copied := *usage
		dst.ResponseMeta.Usage = &copied
		return
	}
	current := dst.ResponseMeta.Usage
	current.PromptTokens = max(current.PromptTokens, usage.PromptTokens)
	current.CompletionTokens = max(current.CompletionTokens, usage.CompletionTokens)
	current.TotalTokens = max(current.TotalTokens, usage.TotalTokens)
	current.PromptTokenDetails.CachedTokens = max(current.PromptTokenDetails.CachedTokens, usage.PromptTokenDetails.CachedTokens)
	current.CompletionTokensDetails.ReasoningTokens = max(
		current.CompletionTokensDetails.ReasoningTokens,
		usage.CompletionTokensDetails.ReasoningTokens,
	)
}

func mergeAssistantToolCall(dst *schema.Message, incoming schema.ToolCall) {
	key := assistantToolCallKey(incoming)
	if key == "" {
		return
	}
	for i := range dst.ToolCalls {
		if assistantToolCallKey(dst.ToolCalls[i]) != key {
			continue
		}
		if incoming.ID != "" {
			dst.ToolCalls[i].ID = incoming.ID
		}
		if incoming.Type != "" {
			dst.ToolCalls[i].Type = incoming.Type
		}
		if incoming.Function.Name != "" {
			dst.ToolCalls[i].Function.Name = incoming.Function.Name
		}
		if incoming.Function.Arguments != "" && incoming.Function.Arguments != "{}" {
			dst.ToolCalls[i].Function.Arguments = incoming.Function.Arguments
		} else if dst.ToolCalls[i].Function.Arguments == "" {
			dst.ToolCalls[i].Function.Arguments = incoming.Function.Arguments
		}
		if dst.ToolCalls[i].ID == "" {
			dst.ToolCalls[i].ID = dst.ToolCalls[i].Function.Name
		}
		if dst.ToolCalls[i].Type == "" {
			dst.ToolCalls[i].Type = "function"
		}
		if dst.ToolCalls[i].Function.Arguments == "" {
			dst.ToolCalls[i].Function.Arguments = "{}"
		}
		return
	}
	if incoming.ID == "" {
		incoming.ID = incoming.Function.Name
	}
	if incoming.Type == "" {
		incoming.Type = "function"
	}
	if incoming.Function.Arguments == "" {
		incoming.Function.Arguments = "{}"
	}
	dst.ToolCalls = append(dst.ToolCalls, incoming)
}

func assistantToolCallKey(tc schema.ToolCall) string {
	if tc.ID != "" {
		return tc.ID
	}
	if tc.Function.Name != "" {
		return "name:" + tc.Function.Name
	}
	return ""
}

// ResumedMessages holds messages restored from a previous session for resume.
type ResumedMessages struct {
	SessionID string
	Messages  []*schema.Message
}

// SetResumedMessages replaces the engine's conversation history with restored messages.
// Used for session resume to continue a previous conversation.
func (e *QueryEngine) SetResumedMessages(messages []*schema.Message) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if messages != nil {
		e.messages = append([]*schema.Message(nil), messages...)
	}
}

// TruncateToMessage truncates the conversation history to include only messages
// up to (but not including) the message at the given index. This effectively
// branches the conversation by removing all messages from that point forward,
// allowing the user to resubmit from an earlier point.
// Used by the /rewrite feature to edit and resubmit a prior user message.
func (e *QueryEngine) TruncateToMessage(messageIndex int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if messageIndex < 0 || messageIndex > len(e.messages) {
		return fmt.Errorf("message index %d out of range [0, %d]", messageIndex, len(e.messages))
	}
	e.messages = append([]*schema.Message(nil), e.messages[:messageIndex]...)
	return nil
}

// ResumeLastSession loads the most recent session from the transcript directory and
// restores its messages into the engine. Returns the resumed session metadata or an error.
// Mirrors the reference session resume flow: locate session → load → validate → set messages
// → reconstruct state (file cache, content replacements, model override).
func (e *QueryEngine) ResumeLastSession(ctx context.Context) (*session.ResumedSession, error) {
	return e.SessionService().Resume(ctx, "")
}

// ResumeSession restores a specific session by ID.
func (e *QueryEngine) ResumeSession(ctx context.Context, sessionID string) (*session.ResumedSession, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("session ID is required")
	}
	return e.SessionService().Resume(ctx, sessionID)
}

// ListResumableSessions returns saved sessions sorted by most recent activity.
func (e *QueryEngine) ListResumableSessions(limit int) ([]session.SessionInfo, error) {
	page, err := e.ListSessionPage(session.SessionQuery{Scope: session.SessionScopeCWD, Limit: limit})
	if err != nil {
		return nil, err
	}
	return page.Sessions, nil
}

// ListSessionPage queries saved sessions through the bounded cursor API. The
// current project root is refreshed before cross-project discovery.
func (e *QueryEngine) ListSessionPage(query session.SessionQuery) (*session.SessionPage, error) {
	if e == nil {
		return nil, fmt.Errorf("query engine is nil")
	}
	return e.SessionService().Query(context.Background(), query)
}

func (e *QueryEngine) resumeSessionForCommandTurn(
	ctx context.Context,
	sessionID string,
	turnID string,
) (*session.ResumedSession, error) {
	opts := session.ResumeOptions{
		SessionID:        sessionID,
		SessionDir:       e.config.TranscriptDir,
		ProjectDir:       e.config.CWD,
		ValidateMessages: true,
	}
	return e.resumeSessionWithOptionsForTurn(ctx, opts, turnID)
}

func (e *QueryEngine) resumeSessionWithOptionsForTurn(
	ctx context.Context,
	opts session.ResumeOptions,
	turnID string,
) (*session.ResumedSession, error) {
	stagedRestore, releaseRestore, err := e.beginRestoreLifecycle()
	if err != nil {
		return nil, err
	}
	defer releaseRestore()

	turnID = strings.TrimSpace(turnID)
	e.goalProviderBoundary.Lock()
	providerBoundaryLocked := true
	defer func() {
		if providerBoundaryLocked {
			e.goalProviderBoundary.Unlock()
		}
	}()
	e.planMu.Lock()
	planLocked := true
	defer func() {
		if planLocked {
			e.planMu.Unlock()
		}
	}()
	if e.planActiveTurnID != "" && e.planActiveTurnID != turnID {
		return nil, fmt.Errorf(
			"%w: turn %s owns the boundary",
			ErrPlanTransitionInFlight,
			e.planActiveTurnID,
		)
	}
	if turnID != "" && e.planActiveTurnID != turnID {
		return nil, fmt.Errorf(
			"%w: command turn %s does not own the boundary",
			ErrPlanTransitionInFlight,
			turnID,
		)
	}

	resumed, err := session.ResumeSession(ctx, opts)
	if err != nil {
		return nil, err
	}
	sourceDir := opts.SessionDir
	if sourceDir == "" {
		sourceDir = e.config.TranscriptDir
	}
	logicalWorkSeed := tools.TaskManagerSnapshot{NextID: 1}
	if forkSeed, exists := e.sessionService.logicalWorkSeed(
		resumed.SessionID,
	); exists {
		logicalWorkSeed = forkSeed
	} else if e.logicalWorkAdapter != nil &&
		e.config.SessionID == resumed.SessionID {
		logicalWorkSeed = e.logicalWorkAdapter.LegacyTaskSnapshot()
	}
	preparedLogicalWork, err := workboard.NewLogicalWorkAdapter(
		workboard.AdapterConfig{
			SessionID: resumed.SessionID,
			Dir:       sourceDir,
			LeaderScope: tools.TodoScope{
				SessionID: resumed.SessionID,
				AgentID:   resumed.Metadata.AgentID,
			},
			Clock: e.config.Clock,
		},
		logicalWorkSeed,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"resume WorkBoard authority preflight: %w",
			err,
		)
	}
	preparedLogicalWork.BindSettlementSnapshot(
		workboardSettlementSnapshot(e.agentRunner),
	)
	preparedLogicalWork.BindMutationAdmission(e.sessionDeletionGate.open)
	recorder := transcript.NewRecorder(resumed.SessionID, sourceDir)
	loaded, loadErr := recorder.LoadFullContext(ctx)
	if loadErr != nil {
		return nil, fmt.Errorf("load resumed execution context: %w", loadErr)
	}
	if err := recorder.RebindPromptRecords(
		loaded.Messages,
		resumed.Messages,
	); err != nil {
		return nil, fmt.Errorf("bind resumed durable prompts: %w", err)
	}
	if err := recorder.RebindAssistantOrigins(
		loaded.Messages,
		resumed.Messages,
	); err != nil {
		return nil, fmt.Errorf("bind resumed assistant origins: %w", err)
	}
	restoredInputCoordinator, coordinatorErr := NewRuntimeInputCoordinator(
		RuntimeInputCoordinatorConfig{
			SessionID: resumed.SessionID,
			ThreadID: firstSessionValue(
				resumed.Metadata.ThreadID,
				resumed.SessionID,
			),
			AgentID: resumed.Metadata.AgentID,
			Path: RuntimeInputPersistencePath(
				recorder.Path(),
			),
			Clock:                    e.config.Clock,
			DeliveryLookup:           recorder.RuntimeItemDeliveryCoverage,
			DeferRecoveryPersistence: stagedRestore,
			mediaStore: mediastore.New(
				recorder.Path() + ".media",
			),
		},
		runtimeItemIDsFromLoadedTranscript(loaded),
	)
	if coordinatorErr != nil {
		return nil, fmt.Errorf(
			"restore runtime input coordinator: %w",
			coordinatorErr,
		)
	}
	restoredKernelSelection := resumedSessionQueryKernelSelection(
		resumed.Metadata,
	)
	if restoredKernelSelection.err != nil {
		return nil, restoredKernelSelection.err
	}
	restoredThreadID := firstSessionValue(
		resumed.Metadata.ThreadID,
		resumed.SessionID,
	)
	var restoredGraphCheckpoint *projectGraphCheckpointStore
	var restoredGraphInterrupt *projectGraphHITLRequest
	if restoredKernelSelection.kernel != nil &&
		restoredKernelSelection.kernel.kind() == queryKernelProjectGraph {
		restoredGraphCheckpoint, err = newProjectGraphCheckpointStore(
			projectGraphCheckpointPath(recorder.Path()),
			RuntimeInputScope{
				SessionID: resumed.SessionID,
				ThreadID:  restoredThreadID,
				AgentID:   resumed.Metadata.AgentID,
			},
			e.config.Clock,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"restore project graph checkpoint: %w",
				err,
			)
		}
		if active, ok := restoredGraphCheckpoint.ActiveInterrupt(); ok {
			if !persistedProjectGraphInterruptMatches(
				resumed.Metadata.GraphInterrupt,
				active,
			) {
				return nil, fmt.Errorf(
					"restore project graph checkpoint: session metadata identity mismatch",
				)
			}
			restoredGraphInterrupt = &active
		} else if resumed.Metadata.GraphInterrupt != nil {
			return nil, fmt.Errorf(
				"restore project graph checkpoint: session metadata has no opaque checkpoint",
			)
		}
	} else if resumed.Metadata.GraphInterrupt != nil {
		return nil, fmt.Errorf(
			"restore session: legacy query kernel cannot own a project graph interrupt",
		)
	}
	restoreContext := e.resolveResumedExecutionContext(opts, resumed)
	preparedGuestExecution, err := e.prepareResumedGuestExecution(
		ctx,
		restoreContext.cwd,
		resumed.SessionID,
	)
	if err != nil {
		return nil, err
	}
	resumed.Warnings = append(resumed.Warnings, restoreContext.warnings...)
	var restoredShellHooks *hooks.ShellHookConfig
	if !e.administrationOnly {
		var hookErr error
		restoredShellHooks, hookErr = hooks.LoadShellHooks(restoreContext.cwd)
		if hookErr != nil {
			return nil, fmt.Errorf(
				"restore project shell hooks: %w",
				hookErr,
			)
		}
	}
	samePermissionRuntime := e.permissionCoordinator != nil &&
		e.permissionCoordinator.ProjectIdentity().key() ==
			ResolvePermissionProjectIdentity(restoreContext.cwd).key()

	// A persisted request ID is only actionable while the original callback is
	// still present in this process and the final restored project keeps the
	// same coordinator. Compute this before any replay projection.
	if samePermissionRuntime && e.runtimeState != nil {
		runtimeActionable := e.runtimeState.ActionableRequestIDs(
			resumed.SessionID, resumed.Metadata.PendingRequestIDs,
		)
		resumed.ActionableRequestIDs = e.permissionCoordinator.actionableRequestIDs(
			e.permissionEngineID,
			resumed.SessionID,
			runtimeActionable,
		)
	}
	if restoredGraphInterrupt != nil {
		resumed.ActionableRequestIDs = appendUniqueSorted(
			resumed.ActionableRequestIDs,
			restoredGraphInterrupt.RequestID,
		)
	}
	if stale := len(resumed.Metadata.PendingRequestIDs) - len(resumed.ActionableRequestIDs); stale > 0 {
		resumed.Warnings = append(resumed.Warnings, fmt.Sprintf(
			"ignored %d persisted request reference(s) without a live runtime callback", stale,
		))
	}

	restoredStatus := firstSessionValue(resumed.Metadata.Status, "idle")
	if restoredGraphInterrupt != nil {
		restoredStatus = string(TerminalWaitingInput)
	}
	if restoredStatus == "running" && len(resumed.ActionableRequestIDs) == 0 {
		restoredStatus = "interrupted"
		resumed.Warnings = append(resumed.Warnings, "the previous process ended during an active turn; restored the durable checkpoint as interrupted")
	}
	restoredGitBranch := resumed.Metadata.GitBranch
	if restoredGitBranch == "" {
		restoredGitBranch = detectSessionGitBranch(restoreContext.cwd)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("resume canceled before activation: %w", err)
	}
	e.mu.Lock()
	fallbackPermissionMode := e.config.PermissionMode
	e.mu.Unlock()
	preserveDurableGraphPlanApproval := restoredGraphInterrupt != nil &&
		restoredGraphInterrupt.Kind == PermissionInteractionKindPlanApproval &&
		restoredGraphInterrupt.PlanApproval != nil &&
		resumed.Metadata.PlanState != nil &&
		resumed.Metadata.PlanState.ApprovalRequestID ==
			restoredGraphInterrupt.RequestID &&
		resumed.Metadata.PlanState.Revision ==
			restoredGraphInterrupt.PlanApproval.PlanRevision
	preserveLivePlanApproval := preserveDurableGraphPlanApproval ||
		(validLivePersistedPlanApproval(
			resumed.Metadata.PlanState,
			resumed.SessionID,
			resumed.Metadata.AgentID,
		) && samePermissionRuntime &&
			e.runtimeState.HasLivePlanApproval(
				resumed.SessionID,
				restoredThreadID,
				resumed.Metadata.PlanState,
			) &&
			e.permissionCoordinator.hasLivePlanApproval(
				e.permissionEngineID,
				resumed.SessionID,
				restoredThreadID,
				resumed.Metadata.PlanState,
			))
	if !stagedRestore && !preserveLivePlanApproval {
		if cancelled := e.permissionCoordinator.cancelPlanApprovals(
			e.permissionEngineID,
			resumed.SessionID,
			restoredThreadID,
		); cancelled > 0 {
			resumed.Warnings = append(resumed.Warnings, fmt.Sprintf(
				"cancelled %d live Plan approval(s) that could not survive session restore",
				cancelled,
			))
		}
	}
	restoredPlanState, restoredPermissionMode, planWarnings := restorePersistedPlanState(
		resumed.Metadata.PlanState,
		resumed.Metadata.PermissionMode,
		resumed.SessionID,
		resumed.Metadata.AgentID,
		fallbackPermissionMode,
		preserveLivePlanApproval,
	)
	resumed.Warnings = append(resumed.Warnings, planWarnings...)
	e.mu.Lock()
	restoreClock := e.config.Clock
	e.mu.Unlock()
	if restoreClock == nil {
		restoreClock = time.Now
	}
	restoredGoalState, goalWarnings, goalCheckpointRequired := restorePersistedGoalStateWithUsage(
		resumed.Metadata.GoalState,
		resumed.Metadata.AgentID,
		restoreClock().UTC(),
		loaded,
		true,
	)
	resumed.Warnings = append(resumed.Warnings, goalWarnings...)
	restoredGoalBinding, goalBindingWarnings := restoreGoalBinding(
		resumed.Metadata.GoalBinding,
		resumed.Metadata.AgentID,
	)
	resumed.Warnings = append(resumed.Warnings, goalBindingWarnings...)
	restoredUsage := transcript.UsageSummary{}
	if loaded != nil {
		restoredUsage = loaded.Usage
	}
	if err := validateResumedModelRole(resumed.Metadata); err != nil {
		return nil, err
	}
	modelAdmission := e.admitResumedModelBinding(
		ctx,
		resumed.Metadata.ModelBinding,
		resumed.Metadata.Model,
		resumed.TokenEstimate,
	)
	resumed.Warnings = append(
		resumed.Warnings,
		modelAdmission.warnings...,
	)

	var releaseLogicalWorkActivation func()
	if !stagedRestore && !e.administrationOnly {
		switch {
		case e.logicalWorkAdapter != nil:
			releaseLogicalWorkActivation, err = e.logicalWorkAdapter.BeginActivation(preparedLogicalWork)
			if err != nil {
				return nil, fmt.Errorf(
					"activate WorkBoard authority: %w",
					err,
				)
			}
		default:
			err = e.taskManager.BindAuthority(func(
				tools.TaskManagerSnapshot,
			) (tools.TaskAuthority, error) {
				return preparedLogicalWork, nil
			})
			if err != nil {
				return nil, err
			}
			e.logicalWorkAdapter = preparedLogicalWork
		}
	}
	if releaseLogicalWorkActivation != nil {
		defer func() {
			if releaseLogicalWorkActivation != nil {
				releaseLogicalWorkActivation()
			}
		}()
	}
	if e.ownsShellManager && e.shellManager != nil {
		_ = e.shellManager.KillAll()
	}
	e.goalMu.Lock()
	e.mu.Lock()
	if preparedGuestExecution != nil && preparedGuestExecution.replace {
		e.shellManager = preparedGuestExecution.shellManager
		e.executionBindings = preparedGuestExecution.bindings
		e.executionPolicy = preparedGuestExecution.bindings.Guest().Policy()
		e.config.ExecutionBindings = preparedGuestExecution.bindings
		e.config.ExecutionPolicy = e.executionPolicy
	}
	e.messages = append([]*schema.Message(nil), resumed.Messages...)
	e.transcriptCheckpointRequired = false
	e.totalUsage = &schema.TokenUsage{
		PromptTokens:     restoredUsage.PromptTokens,
		CompletionTokens: restoredUsage.CompletionTokens,
		TotalTokens:      restoredUsage.TotalTokens,
	}
	e.usageSummary = restoredUsage
	e.contentReplacementState = NewContentReplacementState()
	e.fileStateCache = NewFileStateCache()
	e.permissionDenials = make([]permission.Denial, 0)
	e.denialTracking = permission.NewDenialTrackingState()
	e.sessionState = session.NewStateMachine()
	e.activityTracker = session.NewActivityTracker()
	// P31.1a is observation-only and deliberately does not participate in
	// resume, fork, or new-session activation. Retaining the old lineage
	// observer here would write later mutations to the source Session sidecar.
	e.workBoardShadow = nil
	e.config.WorkBoardShadow = false
	e.logicalWorkErr = nil
	e.config.SessionID = resumed.SessionID
	e.config.ThreadID = restoredThreadID
	e.config.AgentID = resumed.Metadata.AgentID
	e.config.AgentGeneration = resumed.Metadata.AgentGeneration
	e.config.AgentName = resumed.Metadata.AgentName
	e.config.AgentRole = resumed.Metadata.AgentRole
	e.config.ModelRole = resumed.Metadata.ModelRole
	e.config.goalBinding = cloneGoalExecutionIdentity(restoredGoalBinding)
	e.config.goalUsageReporter = nil
	e.config.ParentSessionID = resumed.Metadata.ParentSessionID
	e.config.ParentThreadID = resumed.Metadata.ParentThreadID
	e.config.ParentAgentID = resumed.Metadata.ParentAgentID
	e.config.ParentToolUseID = resumed.Metadata.ParentToolUseID
	// Session activation is a route-generation boundary even when the restored
	// model spelling matches the previous session.
	e.promptRouteGeneration++
	e.config.Model = firstSessionValue(
		modelAdmission.selector,
		resumed.Metadata.Model,
		e.config.Model,
	)
	e.modelBinding = modelAdmission.binding.Clone()
	e.modelDispatchBlock = cloneModelDispatchBlock(modelAdmission.block)
	e.reasoningEffort = modelAdmission.reasoning
	e.config.PermissionMode = restoredPermissionMode
	e.config.CWD = restoreContext.cwd
	e.config.MemoryProjectRoot = restoreContext.cwd
	e.config.PermissionProjectRoot = restoreContext.cwd
	e.config.RootSessionID = resumed.SessionID
	e.config.WorktreePath = restoreContext.worktreePath
	e.config.WorktreeBranch = restoreContext.worktreeBranch
	e.config.AdditionalDirs = restoreContext.additionalDirs
	e.config.TranscriptDir = sourceDir
	e.sessionStartedAt = resumed.Metadata.CreatedAt
	if e.sessionStartedAt.IsZero() {
		e.sessionStartedAt = e.config.Clock().UTC()
	}
	e.sessionGitBranch = restoredGitBranch
	e.sessionStatus = restoredStatus
	e.queryKernelSelection = restoredKernelSelection
	e.projectGraphHITLEnabled = projectGraphDurableInterruptEnabled(
		e.config.PermissionPrompt,
		restoredKernelSelection,
	)
	if stagedRestore {
		e.inputCoordinator = nil
	} else {
		e.inputCoordinator = restoredInputCoordinator
	}
	e.inputCoordinatorErr = nil
	e.projectGraphCheckpoint = restoredGraphCheckpoint
	e.projectGraphCheckpointErr = nil
	deprecationModel := e.config.Model
	if e.modelBinding != nil &&
		e.modelBinding.ValidateV1() == nil {
		deprecationModel = e.modelBinding.APIModel
	}
	e.deprecationWarning = modelcaps.CheckDeprecation(deprecationModel)
	if e.subagentExecutor != nil {
		e.subagentExecutor.workBoardShadow = nil
		e.subagentExecutor.TaskManager = e.taskManager
		e.subagentExecutor.logicalWorkAdapter = e.logicalWorkAdapter
		e.subagentExecutor.MemoryProjectRoot = restoreContext.cwd
		e.subagentExecutor.PermissionProjectRoot = restoreContext.cwd
		e.subagentExecutor.RootSessionID = resumed.SessionID
		e.subagentExecutor.PermissionMode = e.config.PermissionMode
		e.subagentExecutor.ParentModelName = e.config.Model
		e.subagentExecutor.ExecutionBindings = e.executionBindings
		e.subagentExecutor.ExecutionPolicy = e.executionPolicy
		e.subagentExecutor.ParentFileState = e.fileStateCache
		e.subagentExecutor.goalBindingSnapshot = e.currentGoalExecutionIdentity
		e.subagentExecutor.goalUsageReporterFactory = e.bindGoalUsageReporterForChild
		if strings.TrimSpace(e.config.AgentID) == "" {
			e.subagentExecutor.beginGoalChildWait = e.goalService.beginForegroundChildWait
		} else {
			e.subagentExecutor.beginGoalChildWait = nil
		}
	}

	if loaded != nil {
		// Reconstruct content replacement state from replacement records.
		if len(loaded.Replacements) > 0 {
			e.contentReplacementState = ReconstructContentReplacementState(
				resumed.Messages, loaded.Replacements,
			)
		}
		// Reconstruct file state cache from file history snapshots.
		if len(loaded.FileSnapshots) > 0 {
			e.reconstructFileStateCache(loaded.FileSnapshots)
		}
	}

	// Update result storage to point to the resumed session directory.
	e.resultStorage = storage.NewResultStorage(
		filepath.Join(sourceDir, resumed.SessionID),
	)
	if e.transcript != nil && e.transcript != recorder {
		_ = e.transcript.Close()
	}
	e.transcript = recorder
	e.planState = restoredPlanState
	e.goalState = restoredGoalState
	e.goalTurnRuntime = nil
	e.planActiveTurnID = turnID
	e.mu.Unlock()
	e.goalMu.Unlock()
	if releaseLogicalWorkActivation != nil {
		releaseLogicalWorkActivation()
		releaseLogicalWorkActivation = nil
	}
	e.goalProviderBoundary.Unlock()
	providerBoundaryLocked = false

	activation := &restoreStagingActivation{
		result:                   resumed,
		sessionID:                resumed.SessionID,
		threadID:                 restoredThreadID,
		metadata:                 cloneRestoreSessionMetadata(resumed.Metadata),
		restoreContext:           restoreContext,
		restoredShellHooks:       restoredShellHooks,
		restoredGraphInterrupt:   restoredGraphInterrupt,
		restoredPlanState:        restoredPlanState,
		restoredPermissionMode:   restoredPermissionMode,
		preserveLivePlanApproval: preserveLivePlanApproval,
		restoredInputCoordinator: restoredInputCoordinator,
		initializeAgentMemory:    stagedRestore,
		cancelPlanApprovals:      stagedRestore && !preserveLivePlanApproval,
		persistGoalCheckpoint:    stagedRestore && goalCheckpointRequired,
		logicalWorkAdapter:       preparedLogicalWork,
	}
	if stagedRestore {
		e.mu.Lock()
		e.pendingRestoreActivation = activation
		e.mu.Unlock()
		e.planMu.Unlock()
		planLocked = false
		return resumed, nil
	}

	e.activateResumedRuntime(context.WithoutCancel(ctx), activation)
	if e.administrationOnly {
		// The provider-free CLI needs the canonical durable restore and kernel
		// validation above, but it must not activate project runtime dependencies.
		// Retain Resume's target-session checkpoint semantics while skipping MCP,
		// skills, hooks, watchers, worktree recovery, Agent replay, and long-lived
		// services. Close separately skips the synthetic host checkpoint.
		e.planMu.Unlock()
		planLocked = false
		if checkpointErr := e.persistSessionCheckpoint(""); checkpointErr != nil {
			resumed.Warnings = append(
				resumed.Warnings,
				"could not persist restored Session checkpoint: "+checkpointErr.Error(),
			)
		} else if continuationWarnings, continuationErr := e.reconcileRestoredGoalContinuation(
			restoredInputCoordinator,
		); continuationErr != nil {
			resumed.Warnings = append(
				resumed.Warnings,
				"Goal continuation recovery failed closed: "+continuationErr.Error(),
			)
		} else {
			resumed.Warnings = append(resumed.Warnings, continuationWarnings...)
		}
		return resumed, nil
	}
	e.planMu.Unlock()
	planLocked = false
	if checkpointErr := e.persistSessionCheckpoint(""); checkpointErr != nil {
		resumed.Warnings = append(
			resumed.Warnings,
			"could not persist restored Session checkpoint: "+checkpointErr.Error(),
		)
	} else if continuationWarnings, continuationErr := e.reconcileRestoredGoalContinuation(
		restoredInputCoordinator,
	); continuationErr != nil {
		resumed.Warnings = append(
			resumed.Warnings,
			"Goal continuation recovery failed closed: "+continuationErr.Error(),
		)
	} else {
		resumed.Warnings = append(resumed.Warnings, continuationWarnings...)
	}

	return resumed, nil
}

// SessionID returns the engine's session identifier.
func (e *QueryEngine) SessionID() string {
	return e.config.SessionID
}

// ThreadID returns the engine's stable runtime conversation identifier.
func (e *QueryEngine) ThreadID() string {
	return e.config.ThreadID
}

// AgentID returns the engine's agent ID (non-empty for sub-agents).
func (e *QueryEngine) AgentID() string {
	return e.config.AgentID
}

// GetTranscript returns the engine's transcript recorder.
func (e *QueryEngine) GetTranscript() *transcript.Recorder {
	return e.transcript
}

// GetTokenBudget returns the engine's token budget tracker.
func (e *QueryEngine) GetTokenBudget() *budget.TokenBudget {
	return e.config.TokenBudgetTracker
}

// GetTotalUsage returns the cumulative token usage for this session.
func (e *QueryEngine) GetTotalUsage() (promptTokens, completionTokens int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.totalUsage == nil {
		return 0, 0
	}
	return e.totalUsage.PromptTokens, e.totalUsage.CompletionTokens
}

func (e *QueryEngine) observeProviderUsageMessage(message *schema.Message) {
	if e == nil || message == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.usageSummary.ObserveMessage(message)
	e.syncLegacyTotalUsageLocked()
}

func (e *QueryEngine) observeCompactBoundaryUsageMessage(message *schema.Message) {
	if !isCompactBoundaryStart(message) {
		return
	}
	e.observeProviderUsageMessage(message)
}

func (e *QueryEngine) restoreUsageSummary(summary transcript.UsageSummary) {
	if e == nil {
		return
	}
	e.usageSummary = summary
	e.syncLegacyTotalUsageLocked()
}

func (e *QueryEngine) providerUsageSummary() transcript.UsageSummary {
	if e == nil {
		return transcript.UsageSummary{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.usageSummary
}

func (e *QueryEngine) syncLegacyTotalUsageLocked() {
	if e.totalUsage == nil {
		e.totalUsage = &schema.TokenUsage{}
	}
	e.totalUsage.PromptTokens = e.usageSummary.PromptTokens
	e.totalUsage.CompletionTokens = e.usageSummary.CompletionTokens
	e.totalUsage.TotalTokens = e.usageSummary.TotalTokens
}

// GetContextUsage returns the latest main-loop provider input-token usage as a
// percentage of an authoritative model context window. Compaction usage is
// included in cumulative accounting but invalidates this active-context fact
// until the next main-loop response. The method returns 0,0 when either fact
// is unavailable; callers must not substitute message-size estimates.
func (e *QueryEngine) GetContextUsage() (percent, tokens int) {
	e.mu.Lock()
	model := e.config.Model
	binding := e.modelBinding.Clone()
	contextPromptTokens := e.usageSummary.CurrentContextPromptTokens
	usageKnown := e.usageSummary.CurrentContextUsageKnown
	e.mu.Unlock()

	if !usageKnown || contextPromptTokens < 0 || model == "" {
		return 0, 0
	}
	var window int
	if binding != nil {
		if binding.ValidateV1() != nil {
			return 0, 0
		}
		if binding.ContextWindowTokens == nil {
			return 0, 0
		}
		window = *binding.ContextWindowTokens
	} else {
		var windowKnown bool
		window, windowKnown = modelcaps.KnownContextWindow(model)
		if !windowKnown {
			return 0, 0
		}
	}
	if window <= 0 {
		return 0, 0
	}
	tokens = contextPromptTokens
	percent = tokens * 100 / window
	if percent > 100 {
		percent = 100
	}
	return percent, tokens
}

// reconstructFileStateCache rebuilds the FileStateCache from file history snapshots
// loaded from the transcript. Each snapshot is a map[string]FileState recorded at
// a point in time. We merge all snapshots to get the cumulative file interaction state.
// Must be called with e.mu held.
func (e *QueryEngine) reconstructFileStateCache(snapshots []map[string]transcript.FileState) {
	if e.fileStateCache == nil {
		e.fileStateCache = NewFileStateCache()
		if e.subagentExecutor != nil {
			e.subagentExecutor.ParentFileState = e.fileStateCache
		}
	}
	e.fileStateCache.mu.Lock()
	defer e.fileStateCache.mu.Unlock()
	for _, snap := range snapshots {
		for _, fs := range snap {
			if fs.WasRead {
				e.fileStateCache.ReadFiles[fs.Path] = true
			}
			if fs.WasEdit {
				e.fileStateCache.EditFiles[fs.Path] = true
			}
			if fs.WasWrite {
				e.fileStateCache.WriteFiles[fs.Path] = true
			}
		}
	}
}

// GetApprovalTracker returns the engine's permission approval tracker.
// SDK consumers can use this to check/add/revoke approvals.
func (e *QueryEngine) GetApprovalTracker() *permission.ApprovalTracker {
	return e.approvalTracker
}

// ApproveForSession records the narrow parameter scope represented by this invocation.
func (e *QueryEngine) ApproveForSession(toolName string, input map[string]any) error {
	key, _, err := sessionApprovalKey(e.config.PermissionProjectRoot, toolName, input)
	if err != nil {
		return err
	}
	e.approvalTracker.ApproveForRootSession(key, "user", e.permissionRootSessionID)
	return nil
}

// SessionApprovalDescription describes the scope used by ApproveForSession.
func (e *QueryEngine) SessionApprovalDescription(toolName string, input map[string]any) string {
	_, description, err := sessionApprovalKey(e.config.PermissionProjectRoot, toolName, input)
	if err != nil {
		return "this exact tool input"
	}
	return description
}

// ApproveAndPersist records a permission approval and persists it to disk.
// This is the primary API for "always allow" decisions.
func (e *QueryEngine) ApproveAndPersist(key permission.ApprovalKey, reason string) error {
	if !key.IsScoped() {
		return fmt.Errorf("cannot persist an unscoped approval for %s", key.ToolName)
	}
	approvalsPath, err := permission.ApprovalStorePath(e.config.PermissionProjectRoot)
	if err != nil {
		return errors.New("resolve persistent approval store failed")
	}
	return e.approvalTracker.ApproveAndSave(key, reason, approvalsPath)
}

// PersistPermissionRule persists a permission rule to the local settings file
// and reloads the in-memory rules engine so it takes effect immediately.
// This is the "Always allow" codepath triggered from the TUI.
func (e *QueryEngine) PersistPermissionRule(toolName string, input map[string]any) error {
	projectRoot := e.permissionProjectIdentity.Root
	if projectRoot == "" {
		projectRoot = e.config.PermissionProjectRoot
	}
	exactRule, err := permission.BuildExactRuleFromInvocation(
		toolName,
		input,
		projectRoot,
	)
	if err != nil {
		return err
	}
	if err := permission.PersistPermissionRules(
		projectRoot,
		[]string{exactRule.Value},
		permission.ActionAllow,
		permission.DestLocalSettings,
	); err != nil {
		return err
	}
	// Reload rules in-memory so subsequent checks pass immediately.
	e.reloadPermissionRules()
	return nil
}

// GetModelName returns the currently configured model name.
func (e *QueryEngine) GetModelName() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.config.Model
}

// GetModelRegistry returns the global model capability registry for TUI display.
// The registry is pre-populated with well-known models grouped by provider.
func (e *QueryEngine) GetModelRegistry() *modelcaps.ModelRegistry {
	return modelcaps.DefaultRegistry()
}

// GetCWD returns the engine's configured working directory.
func (e *QueryEngine) GetCWD() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.config.CWD
}

// GetWorkingDirectories returns all working directories (CWD + additional).
func (e *QueryEngine) GetWorkingDirectories() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	dirs := []string{e.config.CWD}
	dirs = append(dirs, e.config.AdditionalDirs...)
	return dirs
}

// GetTranscriptDir returns the engine's transcript storage directory.
func (e *QueryEngine) GetTranscriptDir() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.config.TranscriptDir
}

// LongSessionServicesEnabled reports whether this interactive engine owns
// project memory, extraction, auto-dream, and persisted tip rotation.
func (e *QueryEngine) LongSessionServicesEnabled() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.config.EnableLongSessionServices
}

// GetToolNames returns the names of all registered tools.
func (e *QueryEngine) GetToolNames() []string {
	if e.toolRegistry == nil {
		return nil
	}
	infos := e.toolRegistry.List()
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	return names
}
