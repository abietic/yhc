package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/abietic/yhc/engine/auth"
	"github.com/abietic/yhc/engine/compact"
	"github.com/abietic/yhc/engine/containment"
	promptctx "github.com/abietic/yhc/engine/context"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/internal/workboard"
	"github.com/abietic/yhc/engine/memdir"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/skills"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/engine/worktree"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// SubAgentExecutor implements tools.AgentExecutor by spawning a nested QueryEngine
// with isolated conversation state. The parent tool call blocks until the sub-agent
// query loop completes (synchronous within the caller's goroutine).
type SubAgentExecutor struct {
	ChatModel              model.BaseChatModel
	ToolRegistry           *tools.Registry
	CWD                    string
	MemoryProjectRoot      string
	EnablePersistentMemory bool
	PermissionMode         permission.Mode
	// SubagentModel is an optional smaller/cheaper model for read-only sub-agents.
	// If nil, uses the same ChatModel as the parent.
	SubagentModel         model.BaseChatModel
	SubagentModelSelector string
	// ParentModelName is the name of the parent model (for routing decisions).
	ParentModelName          string
	ModelResolver            ModelResolver
	PromptCapabilityResolver PromptCapabilityResolver
	// ParentApprovals is shared with the root session lineage so session-scoped
	// grants apply consistently to parent and child engines.
	ParentApprovals *permission.ApprovalTracker
	// ParentFileState is cloned from the parent's file read/write history
	// so the sub-agent inherits file access context.
	ParentFileState *FileStateCache
	// ParentCanUseTool is forwarded from the parent so permission denials
	// propagate back to the parent's denial tracking.
	ParentCanUseTool CanUseToolFn
	// ParentPermissionPrompt routes child presentation through the same
	// interactive surface while the child engine retains request ownership.
	ParentPermissionPrompt PermissionPromptFn
	PermissionRegistry     *PermissionCoordinatorRegistry
	PermissionProjectRoot  string
	RootSessionID          string
	OwnerSessionID         string
	// ParentRepeatedToolCallPrompt keeps the child guard query-local while
	// routing a third-call override back to the owning interactive surface.
	ParentRepeatedToolCallPrompt RepeatedToolCallPromptFn
	// ParentAbort is the parent's context; if cancelled, the sub-agent aborts.
	ParentAbort context.Context
	// AgentDefinitions is the merged built-in, user, and project agent catalog.
	AgentDefinitions map[string]BuiltInAgentDef
	// RuntimeState is shared with the parent engine so child events reduce into
	// the same canonical multi-thread snapshot.
	RuntimeState *RuntimeStateStore
	// ToolSelection and SimpleTools preserve the parent's model-visible tool
	// policy when a child QueryEngine is created.
	ToolSelection     *tools.ToolSelection
	SimpleTools       bool
	AgentRunner       *tools.AgentRunner
	TaskManager       *tools.TaskManager
	MCPManager        *tools.MCPToolManager
	ExecutionPolicy   *containment.Snapshot
	ExecutionBindings *containment.Bindings
	SkillRegistry     *skills.SkillRegistry
	WebFetchModel     model.BaseChatModel
	WorktreeService   *worktree.Service

	agentDefinitionsMu       sync.RWMutex
	workBoardShadow          *workboard.Shadow
	logicalWorkAdapter       *workboard.LogicalWorkAdapter
	sessionDeletionGate      *sessionDeletionGate
	worktreeBindingMu        sync.RWMutex
	worktreeGeneration       uint64
	goalBindingMu            sync.Mutex
	goalBindings             map[string]goalExecutionIdentity
	goalBindingSnapshot      func() *goalExecutionIdentity
	parentModelCallSnapshot  func() *modelCallIdentity
	beginGoalChildWait       func(string, int64) func()
	goalUsageReporterFactory func(
		*goalExecutionIdentity,
		goalUsageSubject,
	) *goalUsageReporter
	admittedModelCallsMu sync.Mutex
	admittedModelCalls   map[string]roleModelCall
}

// NewSubAgentExecutor creates a SubAgentExecutor with the given dependencies.
func NewSubAgentExecutor(chatModel model.BaseChatModel, registry *tools.Registry, cwd string) *SubAgentExecutor {
	defs, _ := LoadAgentDefinitions(cwd)
	policy := containment.DisabledCompatibilitySnapshot(cwd, containment.EntrypointEmbedded)
	bindings, _ := ambientBindingsForPolicy(policy)
	return &SubAgentExecutor{
		ChatModel:          chatModel,
		ToolRegistry:       registry,
		CWD:                cwd,
		ExecutionPolicy:    policy,
		ExecutionBindings:  bindings,
		MemoryProjectRoot:  cwd,
		AgentDefinitions:   defs,
		goalBindings:       make(map[string]goalExecutionIdentity),
		admittedModelCalls: make(map[string]roleModelCall),
	}
}

// AgentWorktreeLifecycle binds AgentRunner to this QueryEngine's durable
// project service without making the runner a second mutation owner.
func (e *SubAgentExecutor) AgentWorktreeLifecycle() tools.AgentWorktreeLifecycle {
	return e.AgentWorktreeBindingSnapshot().Lifecycle
}

// AgentWorktreeSourceDir returns this parent QueryEngine's effective CWD. It is
// stable even when another engine or the process has a different CWD.
func (e *SubAgentExecutor) AgentWorktreeSourceDir() string {
	return e.AgentWorktreeBindingSnapshot().SourceDir
}

// AgentWorktreeBindingSnapshot atomically freezes service, source, owner
// session, and generation for AgentRunner launch/recovery.
func (e *SubAgentExecutor) AgentWorktreeBindingSnapshot() tools.AgentWorktreeBindingSnapshot {
	if e == nil {
		return tools.AgentWorktreeBindingSnapshot{}
	}
	e.worktreeBindingMu.RLock()
	defer e.worktreeBindingMu.RUnlock()
	return tools.AgentWorktreeBindingSnapshot{
		Lifecycle:       e.WorktreeService,
		SourceDir:       e.CWD,
		ParentSessionID: e.OwnerSessionID,
		Generation:      e.worktreeGeneration,
	}
}

func (e *SubAgentExecutor) bindAgentWorktreeRuntime(
	service *worktree.Service,
	sourceDir string,
	parentSessionID string,
) {
	if e == nil {
		return
	}
	e.worktreeBindingMu.Lock()
	e.WorktreeService = service
	e.CWD = sourceDir
	e.OwnerSessionID = parentSessionID
	e.worktreeGeneration++
	e.worktreeBindingMu.Unlock()
}

// RecordAgentLaunch implements tools.AgentLaunchRecorder. AgentRunner calls it
// synchronously before executor entry so runtime metadata exists before the
// first child model response.
func (e *SubAgentExecutor) RecordAgentLaunch(_ context.Context, launch tools.AgentLaunchSnapshot) error {
	return e.recordAgentLifecycle(launch)
}

// RecordAgentExecutionLinkAdmission commits one immutable WorkBoard relation
// before runtime launch publication. The adapter owns validation, upgrade, and
// marker-last durability; this method never holds an AgentRunner mutex.
func (e *SubAgentExecutor) RecordAgentExecutionLinkAdmission(
	ctx context.Context,
	opts tools.AgentExecOptions,
) error {
	if opts.WorkItem == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if e == nil || e.logicalWorkAdapter == nil {
		return fmt.Errorf(
			"subagent: linked execution requires WorkBoard authority",
		)
	}
	if opts.Generation <= 0 {
		return fmt.Errorf(
			"subagent: linked execution requires positive generation",
		)
	}
	snapshot := e.logicalWorkAdapter.Snapshot()
	if snapshot.Mode != workboard.AuthorityModeWorkBoard ||
		snapshot.BoardID != opts.WorkItem.BoardID {
		return fmt.Errorf(
			"subagent: linked execution Board identity is not owned by this QueryEngine",
		)
	}
	return e.logicalWorkAdapter.AdmitExecutionLink(
		workboard.AdmitExecutionLinkRequest{
			BoardID:          opts.WorkItem.BoardID,
			BoardRevision:    snapshot.Revision,
			WorkItemID:       opts.WorkItem.WorkItemID,
			WorkItemRevision: opts.WorkItem.ExpectedItemRevision,
			AgentID:          opts.AgentID,
			Generation:       uint64(opts.Generation),
			ParentSessionID:  opts.ParentSessionID,
			ParentThreadID:   opts.ParentThreadID,
			ParentAgentID:    opts.ParentAgentID,
			ParentToolUseID:  opts.ToolUseID,
			AdmittedAt:       opts.WorkItem.AdmittedAt.UTC(),
		},
	)
}

// RecordAgentLifecycle implements tools.AgentLifecycleRecorder for additive
// process-only transitions such as foreground wait detachment.
func (e *SubAgentExecutor) RecordAgentLifecycle(
	_ context.Context,
	lifecycle tools.AgentLaunchSnapshot,
) error {
	return e.recordAgentLifecycle(lifecycle)
}

func (e *SubAgentExecutor) recordAgentLifecycle(
	launch tools.AgentLaunchSnapshot,
) error {
	if e == nil || e.RuntimeState == nil {
		return nil
	}
	timestamp := time.Now().UTC()
	if launch.Phase == "launched" && !launch.StartedAt.IsZero() {
		timestamp = launch.StartedAt.UTC()
	}
	turnPrefix := "agent-launch"
	if launch.Phase == "backgrounded" {
		turnPrefix = "agent-lifecycle:backgrounded"
	}
	envelope := RuntimeEventEnvelope{
		SessionID:       launch.SessionID,
		ThreadID:        launch.ThreadID,
		TurnID:          fmt.Sprintf("%s:%s:%d", turnPrefix, launch.AgentID, launch.Generation),
		AgentID:         launch.AgentID,
		ParentSessionID: launch.ParentSessionID,
		ParentThreadID:  launch.ParentThreadID,
		ParentAgentID:   launch.ParentAgentID,
		ParentToolUseID: launch.ParentToolUseID,
		Timestamp:       timestamp,
		CausationID:     launch.ParentToolUseID,
		AgentGeneration: launch.Generation,
	}
	if binding := e.goalBindingForLaunch(launch); binding != nil {
		binding.decorate(&envelope)
	}
	event := QueryEvent{
		RuntimeEventEnvelope: envelope,
		Type:                 EventAgentLifecycle,
		AgentLifecycle: &AgentLifecycleEvent{
			Phase:          launch.Phase,
			Name:           launch.Name,
			Task:           launch.Task,
			Description:    launch.Description,
			AgentType:      launch.AgentType,
			Model:          launch.Model,
			PermissionMode: launch.PermissionMode,
			Isolation:      launch.Isolation,
			CWD:            launch.CWD,
			WorktreePath:   launch.WorktreePath,
			WorktreeBranch: launch.WorktreeBranch,
			TranscriptPath: launch.TranscriptPath,
			OutputFile:     launch.OutputFile,
			Status:         launch.Status,
			Error:          launch.Error,
			Generation:     launch.Generation,
			StartedAt:      launch.StartedAt,
		},
	}
	err := e.RuntimeState.applyNextAgentLifecycle(event)
	if launch.Phase == "launch_failed" ||
		(err != nil &&
			(launch.Phase == "launched" || launch.Phase == "resumed")) {
		e.releaseGoalBinding(launch.AgentID, launch.Generation)
	}
	return err
}

func goalAgentGenerationKey(agentID string, generation int64) string {
	return fmt.Sprintf("%s:%d", strings.TrimSpace(agentID), generation)
}

func (e *SubAgentExecutor) goalBindingForLaunch(
	launch tools.AgentLaunchSnapshot,
) *goalExecutionIdentity {
	if e == nil || strings.TrimSpace(launch.AgentID) == "" ||
		launch.Generation <= 0 {
		return nil
	}
	key := goalAgentGenerationKey(launch.AgentID, launch.Generation)
	e.goalBindingMu.Lock()
	binding, exists := e.goalBindings[key]
	e.goalBindingMu.Unlock()
	if exists {
		return cloneGoalExecutionIdentity(&binding)
	}
	if launch.Phase != "launched" && launch.Phase != "resumed" {
		return nil
	}
	if e.goalBindingSnapshot == nil {
		return nil
	}
	frozen := e.goalBindingSnapshot()
	if frozen == nil || !frozen.valid() {
		return nil
	}
	e.goalBindingMu.Lock()
	if existing, loaded := e.goalBindings[key]; loaded {
		binding = existing
	} else {
		binding = *frozen
		e.goalBindings[key] = binding
	}
	e.goalBindingMu.Unlock()
	return cloneGoalExecutionIdentity(&binding)
}

func (e *SubAgentExecutor) frozenGoalBinding(
	agentID string,
	generation int64,
) *goalExecutionIdentity {
	if e == nil {
		return nil
	}
	key := goalAgentGenerationKey(agentID, generation)
	e.goalBindingMu.Lock()
	defer e.goalBindingMu.Unlock()
	binding, ok := e.goalBindings[key]
	if !ok {
		return nil
	}
	return cloneGoalExecutionIdentity(&binding)
}

func (e *SubAgentExecutor) releaseGoalBinding(
	agentID string,
	generation int64,
) {
	if e == nil {
		return
	}
	e.goalBindingMu.Lock()
	delete(e.goalBindings, goalAgentGenerationKey(agentID, generation))
	e.goalBindingMu.Unlock()
}

// RecordAgentExecutionAdmission durably pins a newly admitted child Session to
// ProjectGraph after AgentRunner assigns exact identity/worktree scope and
// before ExecuteAgent can reach the model. Existing Sessions retain their
// durable kernel selection so continuation never changes traversal owners.
func (e *SubAgentExecutor) RecordAgentExecutionAdmission(
	ctx context.Context,
	opts tools.AgentExecOptions,
	initialMessages []*schema.Message,
) error {
	stage, executionKind, admitted := childExecutionAdmission(opts)
	if !admitted {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(opts.AgentID) == "" ||
		strings.TrimSpace(opts.SessionID) == "" ||
		strings.TrimSpace(opts.ThreadID) == "" ||
		strings.TrimSpace(opts.TranscriptDir) == "" {
		return fmt.Errorf(
			"subagent: %s execution admission requires agent, session, thread, and transcript identities",
			executionKind,
		)
	}
	cwd := strings.TrimSpace(opts.CWD)
	if cwd == "" {
		cwd = e.AgentWorktreeBindingSnapshot().SourceDir
	}
	recorder := transcript.NewRecorder(opts.SessionID, opts.TranscriptDir)
	roleRoutingEnabled := e.modelRoleAdmissionEnabled()
	if _, err := os.Stat(recorder.Path()); err == nil {
		loaded, loadErr := recorder.LoadFull()
		if loadErr != nil {
			return fmt.Errorf(
				"subagent: load %s execution admission: %w",
				executionKind,
				loadErr,
			)
		}
		metadata := session.ReadSessionMetadataFull(loaded)
		if metadata == nil {
			return fmt.Errorf(
				"subagent: existing %s Session has incompatible execution admission: missing durable metadata",
				executionKind,
			)
		}
		var admittedCall roleModelCall
		legacyRoleUpgrade := false
		switch {
		case strings.TrimSpace(metadata.ModelRole) == "" &&
			metadata.ModelBinding == nil:
			if roleRoutingEnabled {
				legacyRoleUpgrade = true
				admittedCall, loadErr = e.resolveLegacyChildModelCall(
					opts,
					initialMessages,
				)
			}
		case strings.TrimSpace(metadata.ModelRole) != "" &&
			metadata.ModelBinding != nil:
			admittedCall, loadErr = e.reAdmitPersistedChildModelCall(
				ctx,
				metadata,
			)
		default:
			loadErr = fmt.Errorf(
				"persisted model role and binding must be present together",
			)
		}
		if loadErr != nil {
			return fmt.Errorf(
				"subagent: admit existing %s model role: %w",
				executionKind,
				loadErr,
			)
		}
		if roleRoutingEnabled &&
			(admittedCall.Identity == nil ||
				admittedCall.Identity.binding == nil) {
			return fmt.Errorf(
				"subagent: existing %s model role has no durable binding",
				executionKind,
			)
		}
		if admittedCall.Identity != nil &&
			admittedCall.Identity.Role !=
				modelRoleForAgentType(opts.SubagentType) {
			return fmt.Errorf(
				"subagent: existing %s Session has incompatible Agent/model role",
				executionKind,
			)
		}
		if admittedCall.Identity != nil {
			switch {
			case legacyRoleUpgrade:
				if strings.TrimSpace(metadata.AgentRole) != "" &&
					!strings.EqualFold(metadata.AgentRole, opts.SubagentType) {
					return fmt.Errorf(
						"subagent: existing %s Session has incompatible Agent role",
						executionKind,
					)
				}
				if strings.TrimSpace(metadata.AgentName) != "" &&
					metadata.AgentName != opts.Name {
					return fmt.Errorf(
						"subagent: existing %s Session has incompatible Agent name",
						executionKind,
					)
				}
			case !strings.EqualFold(metadata.AgentRole, opts.SubagentType):
				return fmt.Errorf(
					"subagent: existing %s Session has incompatible Agent role",
					executionKind,
				)
			case metadata.AgentName != opts.Name:
				return fmt.Errorf(
					"subagent: existing %s Session has incompatible Agent name",
					executionKind,
				)
			}
		}
		if !childExecutionAdmissionMatches(
			metadata,
			opts,
			cwd,
			stage,
		) {
			return fmt.Errorf(
				"subagent: existing %s Session has incompatible execution admission",
				executionKind,
			)
		}
		goalBinding := persistedGoalBinding(
			e.frozenGoalBinding(opts.AgentID, opts.Generation),
		)
		if metadata.AgentGeneration > opts.Generation ||
			(metadata.AgentGeneration == opts.Generation &&
				!samePersistedGoalBinding(metadata.GoalBinding, goalBinding)) {
			return fmt.Errorf(
				"subagent: existing %s Session has stale or conflicting Goal generation binding",
				executionKind,
			)
		}
		metadata.AgentGeneration = opts.Generation
		metadata.GoalBinding = goalBinding
		if strings.TrimSpace(metadata.AgentName) == "" {
			metadata.AgentName = opts.Name
		}
		if admittedCall.Identity != nil {
			metadata.AgentRole = opts.SubagentType
			metadata.ModelRole = admittedCall.Identity.Role
			metadata.ModelBinding = admittedCall.Identity.binding.Clone()
			metadata.Model = admittedCall.Identity.APIModel
			metadata.Provider = admittedCall.Identity.Provider
		}
		metadata.Status = "running"
		metadata.UpdatedAt = time.Now().UTC()
		metadata.MessageCount = len(loaded.Messages)
		if err := session.WriteSessionMetadata(recorder, metadata); err != nil {
			return fmt.Errorf(
				"subagent: update %s execution admission: %w",
				executionKind,
				err,
			)
		}
		if err := recorder.Close(); err != nil {
			return fmt.Errorf(
				"subagent: commit %s execution admission update: %w",
				executionKind,
				err,
			)
		}
		e.storeAdmittedChildModelCall(opts, admittedCall)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf(
			"subagent: inspect %s execution admission: %w",
			executionKind,
			err,
		)
	}
	var admittedCall roleModelCall
	if roleRoutingEnabled {
		resolvedCall, resolveErr := e.resolveNewChildModelCall(
			ctx,
			opts,
			initialMessages,
		)
		if resolveErr != nil {
			return fmt.Errorf(
				"subagent: admit %s model role: %w",
				executionKind,
				resolveErr,
			)
		}
		admittedCall = resolvedCall
		if admittedCall.Identity == nil ||
			admittedCall.Identity.binding == nil {
			return fmt.Errorf(
				"subagent: %s model role has no durable binding",
				executionKind,
			)
		}
	}

	if err := os.MkdirAll(opts.TranscriptDir, 0o700); err != nil {
		return fmt.Errorf(
			"subagent: prepare %s execution admission: %w",
			executionKind,
			err,
		)
	}
	reservation, err := os.OpenFile(
		recorder.Path(),
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf(
				"subagent: %s execution admission was created concurrently",
				executionKind,
			)
		}
		return fmt.Errorf(
			"subagent: reserve %s execution admission: %w",
			executionKind,
			err,
		)
	}
	if err := reservation.Close(); err != nil {
		_ = os.Remove(recorder.Path())
		return fmt.Errorf(
			"subagent: close %s execution admission reservation: %w",
			executionKind,
			err,
		)
	}

	created := true
	defer func() {
		_ = recorder.Close()
		if created {
			_ = os.Remove(recorder.Path())
		}
	}()
	if err := recorder.Replace(initialMessages); err != nil {
		return fmt.Errorf(
			"subagent: seed %s execution admission: %w",
			executionKind,
			err,
		)
	}
	now := time.Now().UTC()
	goalBinding := e.frozenGoalBinding(opts.AgentID, opts.Generation)
	admittedModel := opts.Model
	admittedProvider := auth.DetectProvider(opts.Model)
	admittedAgentRole := ""
	admittedModelRole := ""
	var admittedBinding *session.PersistedModelBinding
	if admittedCall.Identity != nil {
		admittedModel = admittedCall.Identity.APIModel
		admittedProvider = admittedCall.Identity.Provider
		admittedAgentRole = opts.SubagentType
		admittedModelRole = admittedCall.Identity.Role
		admittedBinding = admittedCall.Identity.binding.Clone()
	}
	metadata := &session.SessionMetadataFull{
		SessionID:                  opts.SessionID,
		ParentSessionID:            opts.ParentSessionID,
		ParentThreadID:             opts.ParentThreadID,
		ParentAgentID:              opts.ParentAgentID,
		ParentToolUseID:            opts.ToolUseID,
		Model:                      admittedModel,
		Provider:                   admittedProvider,
		ThreadID:                   opts.ThreadID,
		AgentID:                    opts.AgentID,
		AgentGeneration:            opts.Generation,
		AgentName:                  opts.Name,
		AgentRole:                  admittedAgentRole,
		ModelRole:                  admittedModelRole,
		GoalBinding:                persistedGoalBinding(goalBinding),
		ModelBinding:               admittedBinding,
		Status:                     "running",
		PermissionMode:             string(e.resolvePermissionMode(opts)),
		QueryKernelVersion:         queryKernelVersionProjectGraph,
		QueryKernelStage:           string(stage),
		QueryKernelIncompatibility: "",
		PlanState: persistedPlanState(initialPlanState(QueryEngineConfig{
			SessionID:      opts.SessionID,
			PermissionMode: e.resolvePermissionMode(opts),
		})),
		CreatedAt:    now,
		UpdatedAt:    now,
		MessageCount: len(initialMessages),
		TokenUsage:   compact.EstimateTokenCount(initialMessages),
		IsLeaf:       true,
		GitBranch:    detectSessionGitBranch(cwd),
		CWD:          cwd,
	}
	if err := session.WriteSessionMetadata(recorder, metadata); err != nil {
		return fmt.Errorf(
			"subagent: write %s execution admission: %w",
			executionKind,
			err,
		)
	}
	usage := transcript.UsageSummary{}
	for _, message := range initialMessages {
		usage.ObserveMessage(message)
	}
	if err := recorder.RecordLifecycleBoundaryWithUsage(
		transcript.LifecycleSessionStart,
		initialMessages,
		nil,
		nil,
		usage,
		true,
	); err != nil {
		return fmt.Errorf(
			"subagent: commit %s execution admission: %w",
			executionKind,
			err,
		)
	}
	if err := recorder.Close(); err != nil {
		return fmt.Errorf(
			"subagent: close %s execution admission: %w",
			executionKind,
			err,
		)
	}
	created = false
	e.storeAdmittedChildModelCall(opts, admittedCall)
	return nil
}

func childExecutionAdmission(
	opts tools.AgentExecOptions,
) (queryKernelStage, string, bool) {
	switch {
	case opts.IsForegroundExecution():
		return queryKernelStageForegroundChild, "foreground", true
	case opts.IsBackgroundExecution():
		return queryKernelStageBackgroundChild, "background", true
	default:
		return queryKernelStageUnset, "", false
	}
}

func childExecutionAdmissionMatches(
	metadata *session.SessionMetadataFull,
	opts tools.AgentExecOptions,
	cwd string,
	requestedStage queryKernelStage,
) bool {
	if metadata == nil ||
		metadata.SessionID != opts.SessionID ||
		metadata.ThreadID != opts.ThreadID ||
		metadata.AgentID != opts.AgentID ||
		metadata.AgentGeneration <= 0 ||
		opts.Generation <= 0 ||
		metadata.ParentSessionID != opts.ParentSessionID ||
		metadata.ParentThreadID != opts.ParentThreadID ||
		metadata.ParentAgentID != opts.ParentAgentID ||
		metadata.ParentToolUseID != opts.ToolUseID ||
		metadata.CWD != cwd {
		return false
	}
	if opts.IsForegroundExecution() {
		return metadata.QueryKernelVersion == queryKernelVersionProjectGraph &&
			metadata.QueryKernelStage == string(requestedStage)
	}

	// A background continuation preserves every supported durable ProjectGraph
	// selection. Retired Legacy and unpinned Sessions fail admission rather
	// than being silently reinterpreted or replayed through another kernel.
	if strings.TrimSpace(metadata.QueryKernelVersion) == "" {
		return false
	}
	selection := persistedSessionQueryKernelSelection(
		metadata.QueryKernelVersion,
		metadata.QueryKernelStage,
		metadata.QueryKernelIncompatibility,
	)
	return selection.err == nil
}

// BuiltInAgentDef defines a built-in agent type with its capabilities and constraints.
// Mirrors the reference implementation's AgentType definitions in src/agent/agentTypes.ts.
type BuiltInAgentDef struct {
	// Name is the display name for the agent type.
	Name string
	// WhenToUse describes when the model should spawn this agent type.
	// This text is included in the Agent tool's parameter descriptions.
	WhenToUse string
	// Tools is the positive allowlist of tool names this agent can use.
	// Empty means "all tools" (minus DisallowedTools and Agent).
	Tools []string
	// DisallowedTools is the negative list of tools explicitly denied.
	// Applied after Tools allowlist.
	DisallowedTools []string
	// Model overrides the model for this agent type ("" = inherit parent).
	Model string
	// PermissionMode is the default permission mode for this agent.
	PermissionMode string
	// OmitClaudeMd skips loading CLAUDE.md content into the system prompt.
	// Used for one-shot agents that don't need project context.
	OmitClaudeMd bool
	// MaxTurns is the default max turns for this agent type.
	MaxTurns int
	// ReadOnly marks this agent as having no file mutation capabilities.
	ReadOnly bool
	// SystemPrompt is populated for custom agents. Built-ins use their dynamic
	// role prompt for compatibility with the existing implementation.
	SystemPrompt string
	// Source identifies built-in, user, or project configuration.
	Source string
	// FilePath is the source markdown path for custom definitions.
	FilePath string
	// Memory enables persistent custom-agent memory in one validated scope.
	Memory memdir.AgentMemoryScope
	// PendingSnapshotUpdate never overwrites local memory implicitly.
	PendingSnapshotUpdate *AgentSnapshotUpdate
}

// AgentSnapshotUpdate describes a newer project seed awaiting a keep/replace
// decision from an interactive caller.
type AgentSnapshotUpdate struct {
	SnapshotTimestamp string
}

// BuiltInAgentDefs maps agent type names to their definitions.
// Mirrors the reference's builtinAgentTypes with WhenToUse descriptions
// from getAvailableAgents() in src/agent/agentTypes.ts.
var BuiltInAgentDefs = map[string]BuiltInAgentDef{
	"Explore": {
		Name: "Explore",
		WhenToUse: "Fast agent specialized for exploring codebases. Use this when you need to quickly find files " +
			"by patterns (eg. \"src/components/**/*.tsx\"), search code for keywords (eg. \"API endpoints\"), " +
			"or answer questions about the codebase. When calling, specify thoroughness: " +
			"\"quick\" for basic searches, \"medium\" for moderate exploration, " +
			"or \"very thorough\" for comprehensive analysis across multiple locations and naming conventions.",
		Tools:           []string{"Read", "Glob", "Grep", "Bash"},
		DisallowedTools: []string{"Agent", "ExitPlanMode", "Edit", "Write", "NotebookEdit"},
		OmitClaudeMd:    true,
		MaxTurns:        0,
		ReadOnly:        true,
		Source:          agentSourceBuiltIn,
		SystemPrompt:    exploreAgentSystemPrompt,
	},
	"Plan": {
		Name: "Plan",
		WhenToUse: "Software architect agent for designing implementation plans. Use this when you need to plan " +
			"the implementation strategy for a task. Returns step-by-step plans, identifies critical files, " +
			"and considers architectural trade-offs.",
		Tools:           []string{"Read", "Glob", "Grep", "Bash"},
		DisallowedTools: []string{"Agent", "ExitPlanMode", "Edit", "Write", "NotebookEdit"},
		OmitClaudeMd:    true,
		MaxTurns:        0,
		ReadOnly:        true,
		Source:          agentSourceBuiltIn,
		SystemPrompt:    planAgentSystemPrompt,
	},
	"general-purpose": {
		Name: "general-purpose",
		WhenToUse: "General-purpose agent for researching complex questions, searching for code, " +
			"and executing multi-step tasks. When you are searching for a keyword or file and are " +
			"not confident that you will find the right match in the first few tries use this agent " +
			"to perform the search for you.",
		Tools:           nil, // all tools
		DisallowedTools: []string{"Agent"},
		OmitClaudeMd:    false,
		MaxTurns:        0,
		ReadOnly:        false,
		Source:          agentSourceBuiltIn,
		SystemPrompt:    generalPurposeAgentSystemPrompt,
	},
	"verification": {
		Name: "verification",
		WhenToUse: "Use this agent to verify that implementation work is correct before reporting completion. " +
			"Invoke after non-trivial tasks (3+ file edits, backend/API changes, infrastructure changes). " +
			"Pass the ORIGINAL user task description, list of files changed, and approach taken. " +
			"The agent runs builds, tests, linters, and checks to produce a PASS/FAIL/PARTIAL verdict with evidence.",
		Tools:           nil, // all tools
		DisallowedTools: []string{"Agent", "ExitPlanMode", "Edit", "Write", "NotebookEdit"},
		OmitClaudeMd:    false,
		MaxTurns:        0,
		ReadOnly:        true,
		Source:          agentSourceBuiltIn,
		SystemPrompt:    verificationAgentSystemPrompt,
	},
}

// GetBuiltInAgentDefs returns a copy of the built-in agent definitions map.
// Used by the Agent tool to generate parameter descriptions.
func GetBuiltInAgentDefs() map[string]BuiltInAgentDef {
	result := make(map[string]BuiltInAgentDef, len(BuiltInAgentDefs))
	for k, v := range BuiltInAgentDefs {
		result[k] = v
	}
	return result
}

// ExecuteAgent implements tools.AgentExecutor. It creates an isolated QueryEngine,
// runs a complete query loop, and returns the final assistant response.
func (e *SubAgentExecutor) ExecuteAgent(ctx context.Context, opts tools.AgentExecOptions) (*tools.AgentExecResult, error) {
	if e.ChatModel == nil {
		return nil, fmt.Errorf("subagent: no chat model configured")
	}
	if opts.Generation <= 0 {
		// Preserve the historical direct-executor test/embedding contract only
		// when no Goal identity could be attributed. AgentRunner always
		// supplies an exact positive generation before executor entry.
		if e.goalBindingSnapshot != nil &&
			e.goalBindingSnapshot() != nil {
			return nil, fmt.Errorf(
				"subagent: Goal-bound execution requires a positive Agent generation",
			)
		}
		opts.Generation = 1
	}
	admittedCall, hasAdmittedCall := e.admittedChildModelCall(opts)
	_, executionKind, runnerAdmitted := childExecutionAdmission(opts)
	if runnerAdmitted &&
		!hasAdmittedCall &&
		e.modelRoleAdmissionEnabled() {
		return nil, fmt.Errorf(
			"subagent: %s execution has no admitted model role",
			executionKind,
		)
	}
	if hasAdmittedCall {
		defer e.releaseAdmittedChildModelCall(opts)
	}
	goalBinding := e.frozenGoalBinding(opts.AgentID, opts.Generation)
	defer e.releaseGoalBinding(opts.AgentID, opts.Generation)
	var goalUsageReporter *goalUsageReporter
	if goalBinding != nil && e.goalUsageReporterFactory != nil {
		goalUsageReporter = e.goalUsageReporterFactory(
			goalBinding,
			goalUsageSubject{
				SessionID:       opts.SessionID,
				ThreadID:        opts.ThreadID,
				AgentID:         opts.AgentID,
				AgentGeneration: opts.Generation,
			},
		)
		if goalUsageReporter != nil {
			defer goalUsageReporter.revoke()
		}
	}
	if opts.IsForegroundExecution() && e.beginGoalChildWait != nil {
		finishWait := e.beginGoalChildWait(opts.AgentID, opts.Generation)
		defer finishWait()
	}

	// If the parent context is set, derive from it so parent abort propagates.
	if e.ParentAbort != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		go func() {
			select {
			case <-e.ParentAbort.Done():
				cancel()
			case <-ctx.Done():
			}
		}()
	}

	// Resolve CWD — allow per-agent override.
	cwd := e.AgentWorktreeBindingSnapshot().SourceDir
	if opts.CWD != "" {
		cwd = opts.CWD
	}
	skillRegistry := e.SkillRegistry
	if skillRegistry == nil {
		skillRegistry = tools.SkillRegistryFromCtx(ctx)
	}
	if strings.TrimSpace(opts.Isolation) == "worktree" && skillRegistry != nil {
		var err error
		skillRegistry, err = skillRegistry.ForProjectDirectory(cwd)
		if err != nil {
			return nil, fmt.Errorf(
				"subagent: load worktree project skills: %w",
				err,
			)
		}
	}
	permissionProjectRoot := e.PermissionProjectRoot
	if strings.TrimSpace(opts.Isolation) == "worktree" {
		permissionProjectRoot = cwd
	}

	// Resolve max turns with defaults per agent specialization.
	maxTurns := opts.MaxTurns
	if maxTurns < 0 {
		return nil, fmt.Errorf("sub-agent max turns must be zero (unlimited) or positive")
	}
	if maxTurns == 0 {
		maxTurns = e.defaultMaxTurns(opts.SubagentType)
	}
	permMode := e.resolvePermissionMode(opts)

	// Build scoped tool list.
	scopedTools := e.buildScopedTools(opts.AllowedTools, opts.SubagentType)
	scopedTools = withAgentSkillToolDescription(scopedTools, skillRegistry)
	childRules, _ := permission.LoadPermissionRules(cwd)
	childRules = append(childRules, toolSelectionDenyRules(e.ToolRegistry, e.ToolSelection)...)
	promptTools := filterPlanModelVisibleTools(
		assembleModelVisibleTools(
			e.ToolRegistry,
			scopedTools,
			e.ToolSelection,
			e.SimpleTools,
			permission.NewRulesEngine(childRules),
		),
		permMode == permission.ModePlan,
		e.ToolRegistry,
	)

	// Build system prompt for the sub-agent.
	systemPrompt := e.buildSystemPrompt(opts.SubagentType, opts.SystemPromptSuffix, cwd, promptTools)

	// Create isolated hooks for the sub-agent.
	hookExec := hooks.NewExecutor()

	// Wire the parent's CanUseTool so permission denials propagate back.
	var canUseTool CanUseToolFn
	if e.ParentCanUseTool != nil {
		canUseTool = e.ParentCanUseTool
	}

	// Consume the role call frozen by durable execution admission. Direct
	// executor embeddings retain the compatibility seam without a fixed
	// provider-specific model name.
	chatModel := e.ChatModel
	modelName := e.ParentModelName
	modelRole := ""
	var modelBinding *session.PersistedModelBinding
	if hasAdmittedCall {
		if admittedCall.Model != nil {
			chatModel = admittedCall.Model
		}
		modelName = admittedCall.Identity.Selector
		modelRole = admittedCall.Identity.Role
		modelBinding = admittedCall.Identity.binding.Clone()
	} else if e.isReadOnlyAgentType(opts.SubagentType) &&
		e.SubagentModel != nil {
		chatModel = e.SubagentModel
		modelName = firstNonEmptyString(
			e.SubagentModelSelector,
			e.ParentModelName,
		)
	}

	// Create a minimal QueryEngineConfig for the sub-agent.
	agentRunner := e.AgentRunner
	if agentRunner == nil {
		agentRunner = tools.AgentRunnerFromCtx(ctx)
	}
	taskManager := e.TaskManager
	if taskManager == nil {
		taskManager = tools.TaskManagerFromCtx(ctx)
	}
	mcpManager := e.MCPManager
	if mcpManager == nil {
		mcpManager = tools.MCPManagerFromCtx(ctx)
	}
	childBindings, err := e.childExecutionBindings(ctx, cwd, opts.AgentID)
	if err != nil {
		return nil, fmt.Errorf("derive child execution bindings: %w", err)
	}
	engineCfg := QueryEngineConfig{
		SessionID:                   opts.SessionID,
		ThreadID:                    opts.ThreadID,
		ParentSessionID:             opts.ParentSessionID,
		ParentThreadID:              opts.ParentThreadID,
		ParentAgentID:               opts.ParentAgentID,
		ParentToolUseID:             opts.ToolUseID,
		CWD:                         cwd,
		TranscriptDir:               opts.TranscriptDir,
		InitialTurnTranscriptStored: true,
		MemoryProjectRoot:           e.MemoryProjectRoot,
		EnablePersistentMemory:      e.EnablePersistentMemory,
		AgentID:                     opts.AgentID,
		AgentName:                   opts.Name,
		AgentRole:                   opts.SubagentType,
		AgentGeneration:             opts.Generation,
		goalBinding:                 cloneGoalExecutionIdentity(goalBinding),
		goalUsageReporter:           goalUsageReporter,
		RuntimeState:                e.RuntimeState,
		CustomSystemPrompt:          systemPrompt,
		MaxTurns:                    maxTurns,
		ChatModel:                   chatModel,
		Model:                       modelName,
		ModelRole:                   modelRole,
		ModelBinding:                modelBinding,
		ModelResolver:               e.ModelResolver,
		PromptCapabilityResolver:    e.PromptCapabilityResolver,
		ToolRegistry:                e.ToolRegistry,
		Tools:                       scopedTools,
		ToolSelection:               e.ToolSelection,
		SimpleTools:                 e.SimpleTools,
		HookExecutor:                hookExec,
		PermissionMode:              permMode,
		ExecutionBindings:           childBindings,
		CanUseTool:                  canUseTool,
		PermissionPrompt:            e.ParentPermissionPrompt,
		PermissionRegistry:          e.PermissionRegistry,
		PermissionProjectRoot:       permissionProjectRoot,
		RootSessionID:               e.RootSessionID,
		ApprovalTracker:             e.ParentApprovals,
		RepeatedToolCallPrompt:      e.ParentRepeatedToolCallPrompt,
		AgentRunner:                 agentRunner,
		TaskManager:                 taskManager,
		workBoardShadow:             e.workBoardShadow,
		logicalWorkAdapter:          e.logicalWorkAdapter,
		sessionDeletionGate:         e.sessionDeletionGate,
		MCPManager:                  mcpManager,
		SkillRegistry:               skillRegistry,
		WebFetchModel:               e.WebFetchModel,
		FileStateCache:              cloneFileStateCache(e.ParentFileState),
	}

	// Create an isolated query engine for this sub-agent execution. Foreground
	// construction additionally fails closed on an unpinned transcript.
	// Background construction follows the durable selection written by
	// AgentRunner admission, including historical Legacy/foreground pins.
	var eng *QueryEngine
	if opts.IsForegroundExecution() {
		eng = newForegroundChildQueryEngine(engineCfg)
	} else {
		eng = NewQueryEngine(engineCfg)
	}
	defer eng.Close()
	if len(opts.ResumeMessages) > 0 {
		eng.SetResumedMessages(opts.ResumeMessages)
	}
	progress := eng.configureSubagentProgress(
		opts.ResumeMessages,
		firstNonEmptyString(opts.Description, opts.Task),
		opts.ToolUseID,
		func(snapshot tools.AgentProgress) { publishSubagentProgress(ctx, opts.AgentID, snapshot) },
	)

	// Fire agent start hooks.
	hookExec.ExecuteAgentStart(ctx, &hooks.AgentHookContext{
		AgentID:   opts.AgentID,
		AgentType: opts.SubagentType,
		Event:     hooks.AgentHookEventStart,
	})

	// Run the sub-agent query loop. Resumed detail input keeps the command UUID
	// assigned at enqueue time so optimistic and persisted rows converge.
	var events <-chan QueryEvent
	if strings.TrimSpace(opts.MessageID) != "" {
		events, _ = eng.SubmitMessageWithMetadata(ctx, opts.Task, map[string]any{
			"command_uuid": opts.MessageID,
			"command_mode": "prompt",
			"provenance":   "send_message",
		})
	} else {
		events, _ = eng.SubmitMessage(ctx, opts.Task)
	}

	// Collect results from the event stream.
	var resultBuilder strings.Builder
	turnCount := 0
	toolsUsed := make(map[string]struct{})
	var terminal *Terminal

	for evt := range events {
		switch evt.Type {
		case EventAssistant:
			if evt.AssistantMessage != nil {
				resultBuilder.WriteString(evt.AssistantMessage.Content)
			} else if evt.Message != nil && evt.Message.Role == schema.Assistant {
				resultBuilder.WriteString(evt.Message.Content)
			}
		case EventToolResult:
			// Track tool usage.
			if evt.ToolResultMessage != nil && evt.ToolResultMessage.ToolName != "" {
				toolsUsed[evt.ToolResultMessage.ToolName] = struct{}{}
			}
		case EventStreamRequestStart:
			turnCount++
		case EventTerminal:
			if evt.TerminalInfo != nil {
				captured := *evt.TerminalInfo
				terminal = &captured
			}
		}
	}

	// Build unique tools list.
	usedList := make([]string, 0, len(toolsUsed))
	for name := range toolsUsed {
		usedList = append(usedList, name)
	}
	sort.Strings(usedList)

	messages := eng.GetMessages()

	// Record sub-agent transcript for resume support.
	if eng.transcript != nil {
		_ = eng.transcript.Flush()
	}

	if terminal != nil && terminal.Reason != TerminalCompleted && terminal.Reason != TerminalMaxTurns {
		if terminal.Reason == TerminalAbortedStreaming || terminal.Reason == TerminalAbortedTools {
			return nil, context.Canceled
		}
		if terminal.Err != nil {
			return nil, fmt.Errorf("subagent: query ended with %s: %w", terminal.Reason, terminal.Err)
		}
		return nil, fmt.Errorf("subagent: query ended with %s", terminal.Reason)
	}

	result := resultBuilder.String()
	if result == "" {
		// Fall back to last assistant message from the engine.
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == schema.Assistant && messages[i].Content != "" {
				result = messages[i].Content
				break
			}
		}
	}

	if result == "" {
		result = "(sub-agent produced no output)"
	}

	return &tools.AgentExecResult{
		Result:     result,
		TurnCount:  turnCount,
		TokensUsed: progress.TokenCount(),
		ToolsUsed:  usedList,
		Messages:   messages,
	}, nil
}

func (e *SubAgentExecutor) childExecutionPolicy(
	ctx context.Context,
	cwd string,
	childIdentity string,
) (*containment.Snapshot, error) {
	bindings, err := e.childExecutionBindings(ctx, cwd, childIdentity)
	if err != nil {
		return nil, err
	}
	return bindings.Guest().Policy(), nil
}

func (e *SubAgentExecutor) childExecutionBindings(
	ctx context.Context,
	cwd string,
	childIdentity string,
) (*containment.Bindings, error) {
	if e.ExecutionBindings != nil {
		return DeriveChildExecutionBindings(ctx, e.ExecutionBindings, cwd, childIdentity)
	}
	parentPolicy := e.ExecutionPolicy
	if parentPolicy == nil {
		parentPolicy, _ = containment.FromContext(ctx)
	}
	if parentPolicy == nil {
		return nil, fmt.Errorf("parent execution identity is unavailable")
	}
	childPolicy, err := parentPolicy.DeriveExactChildFor(cwd, childIdentity)
	if err != nil {
		return nil, err
	}
	return ambientBindingsForPolicy(childPolicy)
}

func withAgentSkillToolDescription(
	scopedTools []*schema.ToolInfo,
	registry *skills.SkillRegistry,
) []*schema.ToolInfo {
	if registry == nil {
		return scopedTools
	}
	result := append([]*schema.ToolInfo(nil), scopedTools...)
	for index, info := range result {
		if info != nil && info.Name == "Skill" {
			result[index] = tools.SkillToolForRegistry(registry).Info
		}
	}
	return result
}

func (e *SubAgentExecutor) resolvePermissionMode(opts tools.AgentExecOptions) permission.Mode {
	if opts.Mode != "" {
		if parsed, ok := parsePermissionMode(opts.Mode); ok {
			return parsed
		}
	}
	if def, ok := e.lookupAgentDefinition(opts.SubagentType); ok && def.PermissionMode != "" {
		if parsed, valid := parsePermissionMode(def.PermissionMode); valid {
			return parsed
		}
	}
	if opts.InheritedPermissionMode != "" {
		if parsed, ok := parsePermissionMode(opts.InheritedPermissionMode); ok {
			return parsed
		}
	}
	if e.PermissionMode != "" {
		return e.PermissionMode
	}
	return permission.ModeDefault
}

// buildScopedTools returns the filtered tool list based on allowed tool names,
// disallowed tools from the agent definition, and the agent type.
// Read-only agent types get a restricted set; DisallowedTools are always removed.
func (e *SubAgentExecutor) buildScopedTools(allowedTools []string, agentType string) []*schema.ToolInfo {
	// Goal tools are root-turn authorities intercepted by the parent
	// QueryEngine. They must not enter a child allowlist or system prompt even
	// though their declarations live in the shared registry.
	allTools := filterGoalToolInfos(e.ToolRegistry.List())

	// Look up the built-in definition for disallowed tools.
	def, hasDef := e.lookupAgentDefinition(agentType)

	// Build disallowed set from definition.
	disallowed := make(map[string]struct{})
	if hasDef {
		for _, name := range def.DisallowedTools {
			disallowed[name] = struct{}{}
		}
	}
	// Always disallow Agent to prevent infinite recursion.
	disallowed["Agent"] = struct{}{}

	// Read-only agent types only get non-mutating tools.
	if e.isReadOnlyAgentType(agentType) {
		readOnlyTools := []string{"Read", "Glob", "Grep", "WebFetch", "WebSearch"}
		// If the definition has a specific tools list, use that instead.
		if hasDef && len(def.Tools) > 0 {
			readOnlyTools = def.Tools
		}
		allowed := make(map[string]struct{}, len(readOnlyTools))
		for _, name := range readOnlyTools {
			allowed[name] = struct{}{}
		}
		filtered := make([]*schema.ToolInfo, 0, len(readOnlyTools))
		for _, t := range allTools {
			if _, ok := allowed[t.Name]; !ok {
				continue
			}
			if _, deny := disallowed[t.Name]; deny {
				continue
			}
			filtered = append(filtered, t)
		}
		return filtered
	}

	if len(allowedTools) == 0 {
		// Use definition's Tools list if present; otherwise all tools.
		if hasDef && len(def.Tools) > 0 {
			allowed := make(map[string]struct{}, len(def.Tools))
			for _, name := range def.Tools {
				allowed[name] = struct{}{}
			}
			filtered := make([]*schema.ToolInfo, 0, len(def.Tools))
			for _, t := range allTools {
				if _, ok := allowed[t.Name]; !ok {
					continue
				}
				if _, deny := disallowed[t.Name]; deny {
					continue
				}
				filtered = append(filtered, t)
			}
			return filtered
		}
		// No allowlist — include all except disallowed.
		filtered := make([]*schema.ToolInfo, 0, len(allTools))
		for _, t := range allTools {
			if _, deny := disallowed[t.Name]; deny {
				continue
			}
			filtered = append(filtered, t)
		}
		return filtered
	}

	// Build allowlist set from caller-provided list.
	allowed := make(map[string]struct{}, len(allowedTools))
	for _, name := range allowedTools {
		allowed[strings.TrimSpace(name)] = struct{}{}
	}

	filtered := make([]*schema.ToolInfo, 0, len(allowed))
	for _, t := range allTools {
		if _, ok := allowed[t.Name]; !ok {
			continue
		}
		if _, deny := disallowed[t.Name]; deny {
			continue
		}
		filtered = append(filtered, t)
	}
	return filtered
}

// buildSystemPrompt composes the sub-agent system prompt based on agent type.
// Respects OmitClaudeMd from the built-in definition — when true, skips
// ComposeBaseSystemPrompt (which injects CLAUDE.md content) and uses raw role text.
// Mirrors the reference's agent-type-specific system prompts.
func (e *SubAgentExecutor) buildSystemPrompt(agentType, suffix, cwd string, tools []*schema.ToolInfo) string {
	roleDesc := e.agentTypeDescription(agentType)

	// Build available tools summary.
	var toolNames []string
	for _, t := range tools {
		toolNames = append(toolNames, t.Name)
	}

	var sb strings.Builder
	sb.WriteString(roleDesc)
	sb.WriteString("\n\n")
	fmt.Fprintf(&sb, "Working directory: %s\n", cwd)
	if len(toolNames) > 0 {
		fmt.Fprintf(&sb, "Available tools: %s\n", strings.Join(toolNames, ", "))
	}

	// Check if this agent type should skip CLAUDE.md injection.
	def, hasDef := e.lookupAgentDefinition(agentType)
	omitClaudeMd := hasDef && def.OmitClaudeMd

	var base string
	if omitClaudeMd {
		// Skip CLAUDE.md — use the raw agent prompt directly.
		base = sb.String()
	} else {
		base = promptctx.ComposeBaseSystemPrompt(sb.String(), "")
	}

	if suffix != "" {
		base += "\n\n" + suffix
	}
	if hasDef && def.Memory != "" && e.EnablePersistentMemory {
		if memoryPrompt, err := memdir.BuildAgentMemoryPrompt(def.Name, def.Memory, e.MemoryProjectRoot); err == nil && strings.TrimSpace(memoryPrompt) != "" {
			base += "\n\n" + memoryPrompt
		}
	}
	return base
}

// agentTypeDescription returns the role description for a given agent type.
// Mirrors the reference's agent type definitions.
func (e *SubAgentExecutor) agentTypeDescription(agentType string) string {
	if def, ok := e.lookupAgentDefinition(agentType); ok && def.SystemPrompt != "" {
		return def.SystemPrompt
	}
	switch strings.ToLower(agentType) {
	case "explore":
		return "You are a codebase exploration specialist. Your role is to quickly find files, search code, and answer questions about the codebase. You have read-only access. Do NOT modify any files."
	case "plan":
		return "You are a software architect agent for designing implementation plans. Your role is to analyze the codebase and produce step-by-step implementation plans. You have read-only access. Do NOT modify any files."
	case "general-purpose":
		return "You are a general-purpose agent for researching complex questions, searching for code, and executing multi-step tasks."
	default:
		return "You are a sub-agent assistant. Complete the assigned task efficiently using the tools available to you. Focus on the specific task and return a clear, concise result."
	}
}

// isReadOnlyAgentType returns whether the agent type is read-only (no file mutations).
func (e *SubAgentExecutor) isReadOnlyAgentType(agentType string) bool {
	if def, ok := e.lookupAgentDefinition(agentType); ok {
		return def.ReadOnly
	}
	switch strings.ToLower(agentType) {
	case "explore", "plan":
		return true
	default:
		return false
	}
}

// defaultMaxTurns returns the default max turns for a given agent type.
// Consults BuiltInAgentDefs first, then falls back to hardcoded defaults.
func (e *SubAgentExecutor) defaultMaxTurns(agentType string) int {
	if def, ok := e.lookupAgentDefinition(agentType); ok {
		return def.MaxTurns
	}
	return 0
}

func (e *SubAgentExecutor) lookupAgentDefinition(agentType string) (BuiltInAgentDef, bool) {
	e.agentDefinitionsMu.RLock()
	defer e.agentDefinitionsMu.RUnlock()
	return e.lookupAgentDefinitionLocked(agentType)
}

func (e *SubAgentExecutor) lookupAgentDefinitionLocked(agentType string) (BuiltInAgentDef, bool) {
	defs := e.AgentDefinitions
	if len(defs) == 0 {
		defs = BuiltInAgentDefs
	}
	if def, ok := defs[agentType]; ok {
		return def, true
	}
	for name, def := range defs {
		if strings.EqualFold(name, agentType) {
			return def, true
		}
	}
	return BuiltInAgentDef{}, false
}

// InitializeAgentMemorySnapshots seeds empty user-scope memories and records
// newer project snapshots as pending updates.
func (e *SubAgentExecutor) InitializeAgentMemorySnapshots() []error {
	if e == nil || !e.EnablePersistentMemory {
		return nil
	}
	e.agentDefinitionsMu.Lock()
	defer e.agentDefinitionsMu.Unlock()
	return initializeAgentMemorySnapshots(e.AgentDefinitions, e.MemoryProjectRoot)
}

// AgentSnapshotResolution is an explicit non-merge decision for a pending
// project snapshot.
type AgentSnapshotResolution string

const (
	AgentSnapshotKeep    AgentSnapshotResolution = "keep"
	AgentSnapshotReplace AgentSnapshotResolution = "replace"
)

// ResolveAgentMemorySnapshot applies one explicit keep/replace decision and
// clears pending metadata only after durable success.
func (e *SubAgentExecutor) ResolveAgentMemorySnapshot(agentType string, resolution AgentSnapshotResolution) error {
	if e == nil {
		return fmt.Errorf("subagent executor is unavailable")
	}
	e.agentDefinitionsMu.Lock()
	defer e.agentDefinitionsMu.Unlock()
	name, def, ok := e.agentDefinitionEntryLocked(agentType)
	if !ok || def.Memory == "" {
		return fmt.Errorf("agent %q has no persistent memory", agentType)
	}
	if def.PendingSnapshotUpdate == nil {
		return fmt.Errorf("agent %q has no pending memory snapshot", agentType)
	}
	timestamp := def.PendingSnapshotUpdate.SnapshotTimestamp
	var err error
	switch resolution {
	case AgentSnapshotKeep:
		err = memdir.MarkAgentMemorySnapshotSynced(def.Name, def.Memory, e.MemoryProjectRoot, timestamp)
	case AgentSnapshotReplace:
		err = memdir.ReplaceAgentMemoryFromSnapshot(def.Name, def.Memory, e.MemoryProjectRoot, timestamp)
	default:
		return fmt.Errorf("unknown agent snapshot resolution %q", resolution)
	}
	if err != nil {
		return err
	}
	def.PendingSnapshotUpdate = nil
	e.AgentDefinitions[name] = def
	return nil
}

// PendingAgentMemorySnapshots returns a defensive snapshot keyed by custom
// agent name for TUI, ACP, or library presentation.
func (e *SubAgentExecutor) PendingAgentMemorySnapshots() map[string]AgentSnapshotUpdate {
	result := make(map[string]AgentSnapshotUpdate)
	if e == nil {
		return result
	}
	e.agentDefinitionsMu.RLock()
	defer e.agentDefinitionsMu.RUnlock()
	for name, def := range e.AgentDefinitions {
		if def.PendingSnapshotUpdate != nil {
			result[name] = *def.PendingSnapshotUpdate
		}
	}
	return result
}

func (e *SubAgentExecutor) agentDefinitionEntryLocked(agentType string) (string, BuiltInAgentDef, bool) {
	if def, ok := e.AgentDefinitions[agentType]; ok {
		return agentType, def, true
	}
	for name, def := range e.AgentDefinitions {
		if strings.EqualFold(name, agentType) {
			return name, def, true
		}
	}
	return "", BuiltInAgentDef{}, false
}

// parsePermissionMode converts a mode string to a permission.Mode.
func parsePermissionMode(mode string) (permission.Mode, bool) {
	switch strings.ToLower(mode) {
	case "plan":
		return permission.ModePlan, true
	case "default":
		return permission.ModeDefault, true
	case "auto":
		return permission.ModeAuto, true
	case "acceptedits", "accept_edits":
		return permission.ModeAcceptEdits, true
	case "bypasspermissions", "bypass_permissions":
		return permission.ModeBypassPermissions, true
	default:
		return permission.ModeDefault, false
	}
}

// cloneFileStateCache deep copies a FileStateCache.
func cloneFileStateCache(src *FileStateCache) *FileStateCache {
	if src == nil {
		return nil
	}
	src.mu.RLock()
	defer src.mu.RUnlock()
	dst := &FileStateCache{
		ReadFiles:  make(map[string]bool, len(src.ReadFiles)),
		EditFiles:  make(map[string]bool, len(src.EditFiles)),
		WriteFiles: make(map[string]bool, len(src.WriteFiles)),
	}
	for k, v := range src.ReadFiles {
		dst.ReadFiles[k] = v
	}
	for k, v := range src.EditFiles {
		dst.EditFiles[k] = v
	}
	for k, v := range src.WriteFiles {
		dst.WriteFiles[k] = v
	}
	return dst
}
