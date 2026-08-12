package engine

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/worktree"
)

const (
	defaultRuntimeEventsPerThread   = 256
	defaultRuntimeMessagesPerThread = 128
	defaultRuntimeToolsPerThread    = 64
	defaultRuntimeInteractions      = 32
	defaultRuntimeThreads           = 128
	defaultRuntimeAgents            = 128
	defaultRuntimeTasks             = 128
	defaultRuntimeWorktrees         = 128
	maxRuntimeEventSummaryRunes     = 512
	maxRuntimeMessageRunes          = 4096
	maxRuntimeReasoningRunes        = 2048
	maxRuntimeToolPreviewRunes      = 2048
	maxRuntimeInteractionRunes      = 1024
	maxRuntimeInteractionInputBytes = 16 * 1024
	maxRuntimeMessageToolCalls      = 16
	maxRuntimeAgentActivities       = 5
)

// RuntimeStoreLimits bounds live runtime data. Durable conversation and tool
// output remain owned by transcript/result storage rather than this read model.
type RuntimeStoreLimits struct {
	EventsPerThread       int
	MessagesPerThread     int
	ToolsPerThread        int
	InteractionsPerThread int
	Threads               int
	Agents                int
	Tasks                 int
	Worktrees             int
}

// RuntimeThreadStatus is the last reduced state of a conversation thread. A
// terminal status may transition back to running only when a new TurnID starts.
type RuntimeThreadStatus string

const (
	RuntimeThreadRunning      RuntimeThreadStatus = "running"
	RuntimeThreadPaused       RuntimeThreadStatus = "paused"
	RuntimeThreadWaitingInput RuntimeThreadStatus = "waiting_input"
	RuntimeThreadCompleted    RuntimeThreadStatus = "completed"
	RuntimeThreadFailed       RuntimeThreadStatus = "failed"
	RuntimeThreadAborted      RuntimeThreadStatus = "aborted"
)

// RuntimeTerminalSnapshot retains terminal identity even after bounded live
// events and messages have been evicted.
type RuntimeTerminalSnapshot struct {
	TurnID    string
	Reason    TerminalReason
	TurnCount int
	MaxTurns  int
	Error     string
	Sequence  uint64
	Timestamp time.Time
}

// RuntimeInteractionSnapshot is an unresolved interactive request. Resolved
// requests are removed from this projection but remain in the event ring.
type RuntimeInteractionSnapshot struct {
	ID                string
	Kind              string
	ToolName          string
	CanonicalToolName string
	Source            string
	Message           string
	Input             map[string]any
	InputTruncated    bool
	Attempt           int
	PlanRevision      uint64
	PlanFile          string
	PlanInitialDigest string
	PlanReturnMode    string
	TurnID            string
	Sequence          uint64
	RequestedAt       time.Time
}

// RuntimeToolCallSnapshot is the bounded tool-call part of a message.
type RuntimeToolCallSnapshot struct {
	ID           string
	Name         string
	InputPreview string
}

// RuntimeMessageSnapshot is a bounded in-memory transcript projection. Full
// message content remains available through the durable transcript recorder.
type RuntimeMessageSnapshot struct {
	ID                string
	TranscriptEntryID string
	ModelAttemptID    string
	TurnID            string
	Sequence          uint64
	Role              string
	Content           string
	ReasoningContent  string
	ToolCallID        string
	ToolName          string
	ToolCalls         []RuntimeToolCallSnapshot
	Completed         bool
	Timestamp         time.Time
}

// RuntimeToolSnapshot tracks one tool invocation without retaining unbounded
// output.
type RuntimeToolSnapshot struct {
	ID              string
	Name            string
	Status          string
	InputPreview    string
	ProgressPreview string
	OutputPreview   string
	TurnID          string
	Sequence        uint64
	UpdatedAt       time.Time
}

// RuntimePlanSnapshot is the reducer-owned projection of the latest committed
// Plan state. Replaying transition events reconstructs it without dispatch.
type RuntimePlanSnapshot struct {
	Phase             PlanPhase
	PermissionMode    string
	PlanFileIdentity  string
	ReturnMode        string
	ApprovalRequestID string
	RequestID         string
	Revision          uint64
	Sequence          uint64
	UpdatedAt         time.Time
}

// RuntimeAgentProgressSnapshot is the canonical bounded progress projection
// used by future TUI selectors.
type RuntimeAgentProgressSnapshot struct {
	ToolUses         int
	TotalTokens      int
	DurationMS       int64
	LastToolName     string
	Summary          string
	RecentActivities []RuntimeAgentActivitySnapshot
}

// RuntimeAgentActivitySnapshot retains only display-safe activity metadata.
type RuntimeAgentActivitySnapshot struct {
	ToolName    string
	Description string
	IsSearch    bool
	IsRead      bool
}

// RuntimeAgentSnapshot joins child-thread identity with parent task progress.
type RuntimeAgentSnapshot struct {
	AgentID                   string
	SessionID                 string
	ThreadID                  string
	ParentSessionID           string
	ParentThreadID            string
	ParentAgentID             string
	ParentToolUseID           string
	Name                      string
	Task                      string
	AgentType                 string
	Model                     string
	PermissionMode            string
	Isolation                 string
	CWD                       string
	WorktreePath              string
	WorktreeBranch            string
	TranscriptPath            string
	OutputFile                string
	Description               string
	Status                    string
	DisplayMode               string
	Error                     string
	Generation                int64
	GoalID                    string
	GoalObjectiveRevision     uint64
	GoalRootSessionID         string
	GoalRootThreadID          string
	GoalRootAgentID           string
	GoalTurnID                string
	DeliveredCompletionID     string
	DeliveredTerminalSequence uint64
	Progress                  RuntimeAgentProgressSnapshot
	StartedAt                 time.Time
	UpdatedAt                 time.Time
	CompletedAt               time.Time
}

// RuntimeExecutionKey identifies one immutable Agent execution generation.
// Agent IDs may be reused by a later generation, so neither field is optional.
type RuntimeExecutionKey struct {
	AgentID    string
	Generation int64
}

// RuntimeExecutionSnapshot is the bounded process-local observation retained
// for the Task Explorer. Durable Agent metadata and transcripts remain owned by
// AgentRunner; this projection grants no execution control.
type RuntimeExecutionSnapshot struct {
	Key                RuntimeExecutionKey
	Agent              RuntimeAgentSnapshot
	ObservationOrdinal uint64
	OrdinalPresent     bool
	ReplayOnly         bool
	Predispatch        bool
}

type runtimeExecutionLineage struct {
	SessionID       string
	ThreadID        string
	ParentSessionID string
	ParentThreadID  string
	ParentAgentID   string
	ParentToolUseID string
}

// RuntimeTaskExplorerSnapshot joins the current compatibility projection with
// immutable execution generations retained by RuntimeStateStore.
type RuntimeTaskExplorerSnapshot struct {
	Runtime                     RuntimeSnapshot
	Executions                  map[RuntimeExecutionKey]RuntimeExecutionSnapshot
	DroppedEvents               uint64
	EvictedExecutionGenerations uint64
	HiddenLiveExecutions        uint64
}

// RuntimeTaskSnapshot is the canonical bounded local task projection.
type RuntimeTaskSnapshot struct {
	TaskID      string
	Subject     string
	Description string
	ActiveForm  string
	Status      string
	Owner       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt time.Time
}

// RuntimeWorktreeSnapshot is the bounded reducer-owned projection of a durable
// lifecycle record. The versioned JSON record remains the cleanup authority.
type RuntimeWorktreeSnapshot struct {
	RecordID            string
	OwnerKind           worktree.OwnerKind
	OwnerID             string
	State               worktree.State
	RecoveryDisposition worktree.RecoveryDisposition
	RecoveryDiagnostic  string
	RecordRevision      uint64
	RepositoryIdentity  string
	RepoRoot            string
	Path                string
	Branch              string
	BaseCommit          string
	LastErrorCategory   worktree.ErrorCategory
	LastError           string
	Sequence            uint64
	UpdatedAt           time.Time
}

// RuntimeEventRecord is the bounded replay/debug form retained by the store.
// Large payloads are represented by Summary and durable-storage references in
// higher-level snapshots, never copied into this ring.
type RuntimeEventRecord struct {
	RuntimeEventEnvelope
	Type           QueryEventType
	Family         RuntimeEventFamily
	ToolName       string
	ToolUseID      string
	Summary        string
	TerminalReason TerminalReason
	Command        string
	CommandAction  commands.CommandAction
	CommandStatus  CommandResultStatus
	CommandError   string
	CommandPrompt  string
	ModelAttemptID string
	ModelProfile   string
	ModelPhase     ModelAttemptPhase
	FailureClass   string
}

// RuntimeThreadSnapshot is a defensive point-in-time thread read model.
type RuntimeThreadSnapshot struct {
	SessionID           string
	ThreadID            string
	AgentID             string
	ParentSessionID     string
	ParentThreadID      string
	ParentAgentID       string
	ParentToolUseID     string
	AgentGeneration     int64
	Status              RuntimeThreadStatus
	ActiveTurnID        string
	Revision            uint64
	LastSequence        uint64
	StartedAt           time.Time
	TurnStartedAt       time.Time
	ActiveDuration      time.Duration
	ActiveSince         time.Time
	UpdatedAt           time.Time
	CompletedAt         time.Time
	DroppedEvents       uint64
	DroppedMessages     uint64
	Events              []RuntimeEventRecord
	Messages            []RuntimeMessageSnapshot
	LiveMessage         *RuntimeMessageSnapshot
	Tools               map[string]RuntimeToolSnapshot
	ToolOrder           []string
	PendingInteractions map[string]RuntimeInteractionSnapshot
	Plan                *RuntimePlanSnapshot
	Goal                *GoalSnapshot
	LastTerminal        *RuntimeTerminalSnapshot
}

// RuntimeSnapshot is the engine-owned canonical runtime read model.
type RuntimeSnapshot struct {
	SessionID       string
	ActiveThreadID  string
	Revision        uint64
	Threads         map[string]RuntimeThreadSnapshot
	Agents          map[string]RuntimeAgentSnapshot
	Tasks           map[string]RuntimeTaskSnapshot
	Worktrees       map[string]RuntimeWorktreeSnapshot
	UnresolvedCount int
	DroppedThreads  uint64
}

// RuntimeThreadTimingSnapshot is the narrow read model for one thread's
// cumulative active execution time across turns. ActiveDuration excludes
// intervals where the canonical thread is waiting for human input, paused, or
// terminal.
type RuntimeThreadTimingSnapshot struct {
	ThreadID       string
	TurnID         string
	Status         RuntimeThreadStatus
	ActiveDuration time.Duration
	ActiveSince    time.Time
}

// Elapsed returns cumulative active thread time as of now. Event timestamps own
// completed intervals; now extends only a currently running interval.
func (s RuntimeThreadTimingSnapshot) Elapsed(now time.Time) time.Duration {
	elapsed := s.ActiveDuration
	if s.Status != RuntimeThreadRunning || s.ActiveSince.IsZero() ||
		!now.After(s.ActiveSince) {
		return elapsed
	}
	active := now.Sub(s.ActiveSince)
	if active > 0 && elapsed > time.Duration(1<<63-1)-active {
		return time.Duration(1<<63 - 1)
	}
	return elapsed + active
}

type runtimeThreadState struct {
	RuntimeThreadSnapshot
}

// RuntimeStateStore folds ordered QueryEvents into bounded thread and Agent
// snapshots. Apply and Replay reject malformed identity or state transitions.
type RuntimeStateStore struct {
	admissionMu                 sync.Mutex
	mu                          sync.RWMutex
	limits                      RuntimeStoreLimits
	revision                    uint64
	droppedThreads              uint64
	executionOrdinal            uint64
	evictedExecutionGenerations uint64
	threads                     map[string]*runtimeThreadState
	agents                      map[string]RuntimeAgentSnapshot
	executions                  map[RuntimeExecutionKey]RuntimeExecutionSnapshot
	hiddenLiveExecutions        map[RuntimeExecutionKey]runtimeExecutionLineage
	tasks                       map[string]RuntimeTaskSnapshot
	worktrees                   map[string]RuntimeWorktreeSnapshot
}

// NewRuntimeStateStore creates a reducer store with conservative defaults.
func NewRuntimeStateStore(limits ...RuntimeStoreLimits) *RuntimeStateStore {
	configured := RuntimeStoreLimits{}
	if len(limits) > 0 {
		configured = limits[0]
	}
	configured = normalizeRuntimeStoreLimits(configured)
	return &RuntimeStateStore{
		limits:  configured,
		threads: make(map[string]*runtimeThreadState),
		agents:  make(map[string]RuntimeAgentSnapshot),
		executions: make(
			map[RuntimeExecutionKey]RuntimeExecutionSnapshot,
		),
		hiddenLiveExecutions: make(
			map[RuntimeExecutionKey]runtimeExecutionLineage,
		),
		tasks:     make(map[string]RuntimeTaskSnapshot),
		worktrees: make(map[string]RuntimeWorktreeSnapshot),
	}
}

func normalizeRuntimeStoreLimits(limits RuntimeStoreLimits) RuntimeStoreLimits {
	if limits.EventsPerThread <= 0 {
		limits.EventsPerThread = defaultRuntimeEventsPerThread
	}
	if limits.MessagesPerThread <= 0 {
		limits.MessagesPerThread = defaultRuntimeMessagesPerThread
	}
	if limits.ToolsPerThread <= 0 {
		limits.ToolsPerThread = defaultRuntimeToolsPerThread
	}
	if limits.InteractionsPerThread <= 0 {
		limits.InteractionsPerThread = defaultRuntimeInteractions
	}
	if limits.Threads <= 0 {
		limits.Threads = defaultRuntimeThreads
	}
	if limits.Agents <= 0 {
		limits.Agents = defaultRuntimeAgents
	}
	if limits.Tasks <= 0 {
		limits.Tasks = defaultRuntimeTasks
	}
	if limits.Worktrees <= 0 {
		limits.Worktrees = defaultRuntimeWorktrees
	}
	return limits
}

// Apply validates and reduces one event atomically.
func (s *RuntimeStateStore) Apply(evt QueryEvent) error {
	if s == nil {
		return fmt.Errorf("runtime state: nil store")
	}
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	return s.apply(evt)
}

// applyNextAgentLifecycle assigns a sequence and active-turn identity inside
// the same admission boundary used by live Graph events. This is reserved for
// externally requested process transitions that cannot borrow the child
// QueryEngine's per-turn sequencer.
func (s *RuntimeStateStore) applyNextAgentLifecycle(evt QueryEvent) error {
	if s == nil {
		return fmt.Errorf("runtime state: nil store")
	}
	if !isRuntimeAgentLifecycleEvent(evt) {
		return fmt.Errorf("runtime state: expected Agent lifecycle event")
	}
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()

	s.mu.RLock()
	thread := s.threads[evt.ThreadID]
	if thread == nil {
		evt.Sequence = 1
	} else {
		evt.Sequence = thread.LastSequence + 1
		if isRuntimeAgentBackgroundedTransition(evt) &&
			thread.ActiveTurnID != "" {
			evt.TurnID = thread.ActiveTurnID
		}
	}
	s.mu.RUnlock()
	return s.apply(evt)
}

func (s *RuntimeStateStore) apply(evt QueryEvent) error {
	if err := validateRuntimeEventEnvelope(evt); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	thread := s.threads[evt.ThreadID]
	previousTurnID := ""
	previousStatus := RuntimeThreadStatus("")
	if thread != nil {
		previousTurnID = thread.ActiveTurnID
		previousStatus = thread.Status
	}
	if err := validateRuntimeTransition(thread, evt); err != nil {
		return err
	}
	if thread != nil && s.isDuplicateAgentCompletionLocked(evt) {
		// Transport replay still consumes the ordered runtime envelope, but it
		// must not append a second parent message/event or mutate the Agent/TUI
		// terminal projection.
		thread.LastSequence = evt.Sequence
		thread.UpdatedAt = evt.Timestamp
		thread.Revision++
		s.revision++
		return nil
	}
	if err := s.validateWorktreeTransitionLocked(evt); err != nil {
		return err
	}
	if err := s.validateGoalTransitionLocked(thread, evt); err != nil {
		return err
	}
	if evt.Type == EventPermissionRequest {
		_, exists := threadInteraction(thread, evt.PermissionRequest.ToolUseID)
		if !exists && thread != nil && len(thread.PendingInteractions) >= s.limits.InteractionsPerThread {
			return fmt.Errorf("runtime state: thread %q has %d unresolved interactions, limit %d", evt.ThreadID, len(thread.PendingInteractions), s.limits.InteractionsPerThread)
		}
	}
	if thread == nil {
		if err := s.ensureThreadCapacityLocked(); err != nil {
			return err
		}
		thread = &runtimeThreadState{RuntimeThreadSnapshot: RuntimeThreadSnapshot{
			SessionID:           evt.SessionID,
			ThreadID:            evt.ThreadID,
			AgentID:             evt.AgentID,
			ParentSessionID:     evt.ParentSessionID,
			ParentThreadID:      evt.ParentThreadID,
			ParentAgentID:       evt.ParentAgentID,
			ParentToolUseID:     evt.ParentToolUseID,
			AgentGeneration:     evt.AgentGeneration,
			Status:              RuntimeThreadRunning,
			StartedAt:           evt.Timestamp,
			Tools:               make(map[string]RuntimeToolSnapshot),
			PendingInteractions: make(map[string]RuntimeInteractionSnapshot),
		}}
		s.threads[evt.ThreadID] = thread
	}

	if isExternalPlanStateTransition(evt) ||
		isExternalGoalLifecycle(evt) {
		thread.ActiveTurnID = ""
		thread.LiveMessage = nil
		if thread.Status == "" || thread.Status == RuntimeThreadRunning {
			thread.Status = RuntimeThreadCompleted
			thread.CompletedAt = evt.Timestamp
		}
	} else if evt.Type == EventWorktreeLifecycle {
		// Worktree operations are out-of-band lifecycle work. They may occur
		// before child launch or while the parent turn remains active, so they
		// never seize or clear the thread's active turn.
		if thread.ActiveTurnID == "" {
			thread.Status = RuntimeThreadCompleted
			thread.CompletedAt = evt.Timestamp
		}
	} else if isRuntimeAgentBackgroundedTransition(evt) {
		// Foreground detachment releases only the parent wait. It must not
		// seize, clear, or complete the child turn that continues to run.
		thread.Status = RuntimeThreadRunning
		thread.CompletedAt = time.Time{}
	} else if evt.Type == EventHookResponse && thread != nil {
		// Background hook completion belongs to its launching turn but must not
		// reopen a thread that has already reached a terminal state.
	} else if isRuntimeAgentLaunchBoundary(evt) {
		thread.ActiveTurnID = ""
		thread.LiveMessage = nil
		thread.CompletedAt = time.Time{}
		thread.Status = runtimeStatusForAgentLifecycle(evt.AgentLifecycle.Status)
		thread.AgentGeneration = evt.AgentGeneration
	} else if evt.Type == EventGoalLifecycle &&
		evt.GoalLifecycle != nil &&
		evt.GoalLifecycle.Phase == GoalLifecycleTurnFinished &&
		isRuntimeTerminalStatus(thread.Status) {
		// Replay remains tolerant of older event captures that reduced durable
		// Goal aftercare after the terminal event. New producers commit it
		// immediately before terminal so terminal stays the final publication.
	} else {
		if thread.ActiveTurnID != evt.TurnID {
			thread.ActiveTurnID = evt.TurnID
			thread.TurnStartedAt = evt.Timestamp
			thread.CompletedAt = time.Time{}
			thread.Status = RuntimeThreadRunning
			thread.LiveMessage = nil
		}
		if isRuntimeAgentLifecycleEvent(evt) {
			thread.Status = runtimeStatusForAgentLifecycle(evt.AgentLifecycle.Status)
		}
	}
	thread.LastSequence = evt.Sequence
	thread.UpdatedAt = evt.Timestamp
	thread.Revision++
	s.revision++

	s.appendEventLocked(thread, runtimeEventRecord(evt))
	s.reduceMessageLocked(thread, evt)
	s.reduceToolLocked(thread, evt)
	s.reduceInteractionLocked(thread, evt)
	s.reducePlanLocked(thread, evt)
	s.reduceGoalLocked(thread, evt)
	s.reduceTerminalLocked(thread, evt)
	s.reduceAgentLocked(thread, evt)
	s.reduceTaskLocked(evt)
	s.reduceWorktreeLocked(evt)
	updateRuntimeThreadTiming(thread, previousTurnID, previousStatus, evt.Timestamp)
	return nil
}

// Replay applies an ordered event slice using the same validation as live
// reduction. A fresh store plus the same events always yields the same snapshot.
func (s *RuntimeStateStore) Replay(events []QueryEvent) error {
	for i, evt := range events {
		if err := s.Apply(evt); err != nil {
			return fmt.Errorf("runtime state: replay event %d: %w", i, err)
		}
	}
	return nil
}

// LastSequence returns the last accepted sequence for a thread.
func (s *RuntimeStateStore) LastSequence(threadID string) uint64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if thread := s.threads[threadID]; thread != nil {
		return thread.LastSequence
	}
	return 0
}

// Snapshot returns a defensive copy. activeThreadID is view context supplied by
// the owning QueryEngine; it does not mutate the reducer.
func (s *RuntimeStateStore) Snapshot(activeThreadID string) RuntimeSnapshot {
	if s == nil {
		return RuntimeSnapshot{ActiveThreadID: activeThreadID, Threads: map[string]RuntimeThreadSnapshot{}, Agents: map[string]RuntimeAgentSnapshot{}, Tasks: map[string]RuntimeTaskSnapshot{}, Worktrees: map[string]RuntimeWorktreeSnapshot{}}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := RuntimeSnapshot{
		ActiveThreadID: activeThreadID,
		Revision:       s.revision,
		Threads:        make(map[string]RuntimeThreadSnapshot, len(s.threads)),
		Agents:         make(map[string]RuntimeAgentSnapshot, len(s.agents)),
		Tasks:          make(map[string]RuntimeTaskSnapshot, len(s.tasks)),
		Worktrees:      make(map[string]RuntimeWorktreeSnapshot, len(s.worktrees)),
		DroppedThreads: s.droppedThreads,
	}
	for id, thread := range s.threads {
		cloned := cloneRuntimeThreadSnapshot(thread.RuntimeThreadSnapshot)
		out.Threads[id] = cloned
		out.UnresolvedCount += len(cloned.PendingInteractions)
	}
	for id, agent := range s.agents {
		out.Agents[id] = cloneRuntimeAgentSnapshot(agent)
	}
	for id, task := range s.tasks {
		out.Tasks[id] = task
	}
	for id, item := range s.worktrees {
		out.Worktrees[id] = item
	}
	if active, ok := out.Threads[activeThreadID]; ok {
		out.SessionID = active.SessionID
	}
	return out
}

// TaskExplorerSnapshot returns one atomic runtime input for the canonical
// explorer selector. It includes the exact current compatibility rows plus
// retained immutable Agent generations, without cloning message/event rings.
func (s *RuntimeStateStore) TaskExplorerSnapshot(
	activeThreadID string,
) RuntimeTaskExplorerSnapshot {
	if s == nil {
		return RuntimeTaskExplorerSnapshot{
			Runtime: RuntimeSnapshot{
				ActiveThreadID: activeThreadID,
				Threads:        map[string]RuntimeThreadSnapshot{},
				Agents:         map[string]RuntimeAgentSnapshot{},
				Tasks:          map[string]RuntimeTaskSnapshot{},
				Worktrees:      map[string]RuntimeWorktreeSnapshot{},
			},
			Executions: map[RuntimeExecutionKey]RuntimeExecutionSnapshot{},
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := RuntimeTaskExplorerSnapshot{
		Runtime:                     s.taskExplorerRuntimeSnapshotLocked(activeThreadID),
		Executions:                  make(map[RuntimeExecutionKey]RuntimeExecutionSnapshot, len(s.executions)),
		EvictedExecutionGenerations: s.evictedExecutionGenerations,
		HiddenLiveExecutions:        uint64(len(s.hiddenLiveExecutions)),
	}
	for _, thread := range s.threads {
		out.DroppedEvents += thread.DroppedEvents
	}
	for key, execution := range s.executions {
		execution.Agent = cloneRuntimeAgentSnapshot(execution.Agent)
		out.Executions[key] = execution
	}
	return out
}

func (s *RuntimeStateStore) taskExplorerRuntimeSnapshotLocked(
	activeThreadID string,
) RuntimeSnapshot {
	out := RuntimeSnapshot{
		ActiveThreadID: activeThreadID,
		Revision:       s.revision,
		Threads:        make(map[string]RuntimeThreadSnapshot, len(s.agents)),
		Agents:         make(map[string]RuntimeAgentSnapshot, len(s.agents)),
		Tasks:          make(map[string]RuntimeTaskSnapshot, len(s.tasks)),
		Worktrees:      make(map[string]RuntimeWorktreeSnapshot, len(s.worktrees)),
	}
	for id, agent := range s.agents {
		out.Agents[id] = cloneRuntimeAgentSnapshot(agent)
		if thread := s.threads[agent.ThreadID]; thread != nil {
			out.Threads[agent.ThreadID] = RuntimeThreadSnapshot{
				SessionID:      thread.SessionID,
				ThreadID:       thread.ThreadID,
				AgentID:        thread.AgentID,
				Status:         thread.Status,
				StartedAt:      thread.StartedAt,
				TurnStartedAt:  thread.TurnStartedAt,
				ActiveDuration: thread.ActiveDuration,
				ActiveSince:    thread.ActiveSince,
				UpdatedAt:      thread.UpdatedAt,
				CompletedAt:    thread.CompletedAt,
				LastSequence:   thread.LastSequence,
				ActiveTurnID:   thread.ActiveTurnID,
			}
		}
	}
	for id, task := range s.tasks {
		out.Tasks[id] = task
	}
	for id, item := range s.worktrees {
		out.Worktrees[id] = item
	}
	if active, ok := s.threads[activeThreadID]; ok {
		out.SessionID = active.SessionID
	}
	return out
}

func updateRuntimeThreadTiming(
	thread *runtimeThreadState,
	previousTurnID string,
	previousStatus RuntimeThreadStatus,
	now time.Time,
) {
	if thread == nil {
		return
	}

	wasActive := previousTurnID != "" &&
		previousStatus == RuntimeThreadRunning &&
		!thread.ActiveSince.IsZero()
	isActive := thread.ActiveTurnID != "" &&
		thread.Status == RuntimeThreadRunning
	continuesInterval := wasActive &&
		isActive &&
		thread.ActiveTurnID == previousTurnID

	if wasActive && !continuesInterval {
		if now.After(thread.ActiveSince) {
			active := now.Sub(thread.ActiveSince)
			if thread.ActiveDuration > time.Duration(1<<63-1)-active {
				thread.ActiveDuration = time.Duration(1<<63 - 1)
			} else {
				thread.ActiveDuration += active
			}
		}
		thread.ActiveSince = time.Time{}
	}

	if !isActive {
		thread.ActiveSince = time.Time{}
		return
	}
	if !continuesInterval || thread.ActiveSince.IsZero() {
		// A new turn preserves the thread total but starts a fresh active
		// interval. A restored projection similarly begins at its first new
		// event instead of inventing historical active time.
		thread.ActiveSince = now
	}
}

func threadInteraction(thread *runtimeThreadState, interactionID string) (RuntimeInteractionSnapshot, bool) {
	if thread == nil {
		return RuntimeInteractionSnapshot{}, false
	}
	interaction, ok := thread.PendingInteractions[interactionID]
	return interaction, ok
}

func (s *RuntimeStateStore) ensureThreadCapacityLocked() error {
	if len(s.threads) < s.limits.Threads {
		return nil
	}
	type candidate struct {
		id        string
		updatedAt time.Time
	}
	candidates := make([]candidate, 0, len(s.threads))
	for id, thread := range s.threads {
		if isRuntimeTerminalStatus(thread.Status) && len(thread.PendingInteractions) == 0 {
			candidates = append(candidates, candidate{id: id, updatedAt: thread.UpdatedAt})
		}
	}
	if len(candidates) == 0 {
		return fmt.Errorf("runtime state: thread capacity %d reached with no terminal thread to evict", s.limits.Threads)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].updatedAt.Equal(candidates[j].updatedAt) {
			return candidates[i].id < candidates[j].id
		}
		return candidates[i].updatedAt.Before(candidates[j].updatedAt)
	})
	delete(s.threads, candidates[0].id)
	s.droppedThreads++
	return nil
}

// ThreadSnapshot is a focused defensive selector.
func (s *RuntimeStateStore) ThreadSnapshot(threadID string) (RuntimeThreadSnapshot, bool) {
	if s == nil {
		return RuntimeThreadSnapshot{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	thread, ok := s.threads[threadID]
	if !ok {
		return RuntimeThreadSnapshot{}, false
	}
	return cloneRuntimeThreadSnapshot(thread.RuntimeThreadSnapshot), true
}

// ThreadTimingSnapshot returns one thread's cumulative active timing without
// cloning its bounded message, event, tool, or interaction projections.
func (s *RuntimeStateStore) ThreadTimingSnapshot(threadID string) (RuntimeThreadTimingSnapshot, bool) {
	if s == nil {
		return RuntimeThreadTimingSnapshot{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	thread, ok := s.threads[threadID]
	if !ok ||
		(thread.ActiveTurnID == "" &&
			thread.ActiveDuration == 0 &&
			thread.ActiveSince.IsZero()) {
		return RuntimeThreadTimingSnapshot{}, false
	}
	return RuntimeThreadTimingSnapshot{
		ThreadID:       thread.ThreadID,
		TurnID:         thread.ActiveTurnID,
		Status:         thread.Status,
		ActiveDuration: thread.ActiveDuration,
		ActiveSince:    thread.ActiveSince,
	}, true
}

// AgentThreadSnapshot returns one Agent and its full bounded thread projection
// under the same read lock. Detail views avoid cloning unrelated thread rings.
func (s *RuntimeStateStore) AgentThreadSnapshot(agentID string) (RuntimeAgentSnapshot, RuntimeThreadSnapshot, uint64, bool) {
	if s == nil || strings.TrimSpace(agentID) == "" {
		return RuntimeAgentSnapshot{}, RuntimeThreadSnapshot{}, 0, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	agent, ok := s.agents[agentID]
	if !ok {
		return RuntimeAgentSnapshot{}, RuntimeThreadSnapshot{}, s.revision, false
	}
	thread := RuntimeThreadSnapshot{}
	if stored := s.threads[agent.ThreadID]; stored != nil {
		thread = cloneRuntimeThreadSnapshot(stored.RuntimeThreadSnapshot)
	}
	return cloneRuntimeAgentSnapshot(agent), thread, s.revision, true
}

// RestoreAgentSnapshot installs a bounded Agent/thread projection from a live
// runner snapshot or durable Agent transcript. Disk restore never carries
// interactive requests; a process-interrupted running Agent becomes aborted
// replay state instead of pretending it can still accept control messages.
func (s *RuntimeStateStore) RestoreAgentSnapshot(agent RuntimeAgentSnapshot, messages []*schema.Message, live bool) error {
	if s == nil {
		return fmt.Errorf("runtime state: nil store")
	}
	agent.AgentID = strings.TrimSpace(agent.AgentID)
	agent.ThreadID = strings.TrimSpace(agent.ThreadID)
	agent.SessionID = strings.TrimSpace(agent.SessionID)
	if agent.AgentID == "" || agent.ThreadID == "" || agent.SessionID == "" {
		return fmt.Errorf("runtime state: restored Agent requires agent, thread, and session IDs")
	}
	now := agent.UpdatedAt
	if now.IsZero() {
		now = agent.CompletedAt
	}
	if now.IsZero() {
		now = agent.StartedAt
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	status := runtimeStatusForAgentLifecycle(agent.Status)
	if !live && !isRuntimeTerminalStatus(status) {
		status = RuntimeThreadAborted
		agent.Status = string(RuntimeThreadAborted)
		if strings.TrimSpace(agent.Error) == "" {
			agent.Error = "Agent execution was interrupted before this process restored the transcript"
		}
		agent.CompletedAt = now
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validateRestoredExecutionIdentityLocked(agent); err != nil {
		return err
	}
	if existing := s.threads[agent.ThreadID]; existing != nil && live {
		// The current process still owns the thread and any actionable request
		// callbacks. Keep that richer state rather than replacing it from disk.
		agent.UpdatedAt = now
		s.agents[agent.AgentID] = cloneRuntimeAgentSnapshot(agent)
		s.recordExecutionObservationLocked(agent.AgentID, true, false)
		s.enforceAgentLimitLocked()
		s.revision++
		return nil
	}
	if _, exists := s.threads[agent.ThreadID]; !exists {
		if err := s.ensureThreadCapacityLocked(); err != nil {
			return err
		}
	}
	thread := &runtimeThreadState{RuntimeThreadSnapshot: RuntimeThreadSnapshot{
		SessionID:           agent.SessionID,
		ThreadID:            agent.ThreadID,
		AgentID:             agent.AgentID,
		ParentSessionID:     agent.ParentSessionID,
		ParentThreadID:      agent.ParentThreadID,
		ParentAgentID:       agent.ParentAgentID,
		ParentToolUseID:     agent.ParentToolUseID,
		AgentGeneration:     agent.Generation,
		Status:              status,
		StartedAt:           agent.StartedAt,
		UpdatedAt:           now,
		CompletedAt:         agent.CompletedAt,
		Tools:               make(map[string]RuntimeToolSnapshot),
		PendingInteractions: make(map[string]RuntimeInteractionSnapshot),
	}}
	for index, message := range messages {
		if message == nil {
			continue
		}
		sequence := uint64(index + 1)
		event := QueryEvent{RuntimeEventEnvelope: RuntimeEventEnvelope{
			SessionID: agent.SessionID, ThreadID: agent.ThreadID,
			TurnID: "restored", Sequence: sequence, Timestamp: now,
		}}
		thread.Messages = append(thread.Messages, newRuntimeMessageSnapshot(event, message, true))
	}
	s.boundMessagesLocked(thread)
	thread.LastSequence = uint64(len(messages))
	thread.Revision = 1
	if isRuntimeTerminalStatus(status) {
		thread.ActiveTurnID = ""
		if thread.CompletedAt.IsZero() {
			thread.CompletedAt = now
		}
	}
	s.threads[agent.ThreadID] = thread
	agent.UpdatedAt = now
	s.agents[agent.AgentID] = cloneRuntimeAgentSnapshot(agent)
	s.recordExecutionObservationLocked(agent.AgentID, live, !live)
	s.enforceAgentLimitLocked()
	s.revision++
	return nil
}

func (s *RuntimeStateStore) validateRestoredExecutionIdentityLocked(
	agent RuntimeAgentSnapshot,
) error {
	if agent.Generation <= 0 {
		return nil
	}
	key := RuntimeExecutionKey{
		AgentID:    agent.AgentID,
		Generation: agent.Generation,
	}
	current, exists := s.executions[key]
	if exists {
		return validateRuntimeExecutionLineage(
			key,
			runtimeExecutionLineageFromAgent(current.Agent),
			agent,
		)
	}
	hidden, exists := s.hiddenLiveExecutions[key]
	if !exists {
		return nil
	}
	return validateRuntimeExecutionLineage(key, hidden, agent)
}

func validateRuntimeExecutionLineage(
	key RuntimeExecutionKey,
	current runtimeExecutionLineage,
	agent RuntimeAgentSnapshot,
) error {
	if current != runtimeExecutionLineageFromAgent(agent) {
		return fmt.Errorf(
			"runtime state: restored execution %q generation %d changed immutable lineage",
			key.AgentID,
			key.Generation,
		)
	}
	return nil
}

func runtimeExecutionLineageFromAgent(
	agent RuntimeAgentSnapshot,
) runtimeExecutionLineage {
	return runtimeExecutionLineage{
		SessionID:       agent.SessionID,
		ThreadID:        agent.ThreadID,
		ParentSessionID: agent.ParentSessionID,
		ParentThreadID:  agent.ParentThreadID,
		ParentAgentID:   agent.ParentAgentID,
		ParentToolUseID: agent.ParentToolUseID,
	}
}

// RestoreWorktreeSnapshots installs durable restart metadata without
// synthesizing lifecycle events or dispatching Git, Agent, model, or tool
// work. The durable record store remains the sole mutation authority.
func (s *RuntimeStateStore) RestoreWorktreeSnapshots(
	records []worktree.RecoveryRecord,
) error {
	if s == nil || len(records) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, recovery := range records {
		record := recovery.Record
		if err := record.Validate(); err != nil {
			return fmt.Errorf(
				"runtime state: restore worktree %q: %w",
				record.ID,
				err,
			)
		}
		next := RuntimeWorktreeSnapshot{
			RecordID:            record.ID,
			OwnerKind:           record.Owner.Kind,
			OwnerID:             record.Owner.ID,
			State:               record.State,
			RecoveryDisposition: recovery.Disposition,
			RecoveryDiagnostic: truncateRuntimeText(
				recovery.Diagnostic,
				maxRuntimeInteractionRunes,
			),
			RecordRevision:     record.Revision,
			RepositoryIdentity: record.RepositoryIdentity,
			RepoRoot:           record.RepoRoot,
			Path:               record.Path,
			Branch:             record.Branch,
			BaseCommit:         record.BaseCommit,
			LastErrorCategory:  record.LastErrorCategory,
			LastError: truncateRuntimeText(
				record.LastError,
				maxRuntimeInteractionRunes,
			),
			UpdatedAt: record.UpdatedAt,
		}
		current, exists := s.worktrees[record.ID]
		if exists {
			if current.OwnerKind != next.OwnerKind ||
				current.OwnerID != next.OwnerID ||
				current.Path != next.Path ||
				current.Branch != next.Branch {
				return fmt.Errorf(
					"runtime state: restored worktree %q immutable identity changed",
					record.ID,
				)
			}
			if current.RecordRevision > next.RecordRevision {
				continue
			}
			if current.RecordRevision == next.RecordRevision &&
				current.Sequence > 0 {
				continue
			}
			if current.RecordRevision == next.RecordRevision &&
				current == next {
				continue
			}
		}
		s.worktrees[record.ID] = next
		changed = true
	}
	if changed {
		s.enforceWorktreeLimitLocked()
		s.revision++
	}
	return nil
}

// RestorePlanSnapshot installs the durable Plan read model without replaying a
// synthetic event or dispatching work. The caller invokes it only after proving
// no Plan callback can survive resume, so reducer-only Plan interactions are
// stale diagnostics and are removed before installing the canonical snapshot.
func (s *RuntimeStateStore) RestorePlanSnapshot(
	sessionID string,
	threadID string,
	agentID string,
	state PlanState,
	mode string,
	updatedAt time.Time,
) error {
	if s == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	threadID = strings.TrimSpace(threadID)
	if sessionID == "" || threadID == "" {
		return fmt.Errorf("runtime state: restored Plan snapshot requires session and thread identity")
	}
	if state.Phase == "" || strings.TrimSpace(state.PlanFileIdentity) == "" {
		return fmt.Errorf("runtime state: restored Plan snapshot is incomplete")
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	thread := s.threads[threadID]
	if thread == nil {
		if err := s.ensureThreadCapacityLocked(); err != nil {
			return err
		}
		thread = &runtimeThreadState{RuntimeThreadSnapshot: RuntimeThreadSnapshot{
			SessionID:           sessionID,
			ThreadID:            threadID,
			AgentID:             agentID,
			Status:              RuntimeThreadCompleted,
			StartedAt:           updatedAt,
			UpdatedAt:           updatedAt,
			Tools:               make(map[string]RuntimeToolSnapshot),
			PendingInteractions: make(map[string]RuntimeInteractionSnapshot),
		}}
		s.threads[threadID] = thread
	} else if thread.SessionID != sessionID {
		return fmt.Errorf(
			"runtime state: thread %q belongs to session %q, not %q",
			threadID,
			thread.SessionID,
			sessionID,
		)
	}
	for id, interaction := range thread.PendingInteractions {
		if interaction.Kind == PermissionInteractionKindPlanApproval ||
			strings.EqualFold(interaction.ToolName, "ExitPlanMode") {
			delete(thread.PendingInteractions, id)
		}
	}
	thread.Plan = &RuntimePlanSnapshot{
		Phase:             state.Phase,
		PermissionMode:    mode,
		PlanFileIdentity:  state.PlanFileIdentity,
		ReturnMode:        string(state.ReturnMode),
		ApprovalRequestID: state.ApprovalRequestID,
		Revision:          state.Revision,
		Sequence:          thread.LastSequence,
		UpdatedAt:         updatedAt,
	}
	thread.UpdatedAt = updatedAt
	thread.Revision++
	s.revision++
	return nil
}

// RestoreGoalSnapshot installs durable Goal read state without synthesizing an
// event or granting continuation authority. Active records have already been
// normalized fail-closed by restorePersistedGoalState before this projection.
func (s *RuntimeStateStore) RestoreGoalSnapshot(
	sessionID string,
	threadID string,
	agentID string,
	state *goalState,
	updatedAt time.Time,
) error {
	if s == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	threadID = strings.TrimSpace(threadID)
	if sessionID == "" || threadID == "" {
		return fmt.Errorf("runtime state: restored Goal snapshot requires session and thread identity")
	}
	if strings.TrimSpace(agentID) != "" && state != nil {
		return fmt.Errorf("runtime state: child Session cannot own a Goal snapshot")
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	thread := s.threads[threadID]
	if thread == nil {
		if err := s.ensureThreadCapacityLocked(); err != nil {
			return err
		}
		thread = &runtimeThreadState{RuntimeThreadSnapshot: RuntimeThreadSnapshot{
			SessionID:           sessionID,
			ThreadID:            threadID,
			AgentID:             agentID,
			Status:              RuntimeThreadCompleted,
			StartedAt:           updatedAt,
			UpdatedAt:           updatedAt,
			Tools:               make(map[string]RuntimeToolSnapshot),
			PendingInteractions: make(map[string]RuntimeInteractionSnapshot),
		}}
		s.threads[threadID] = thread
	} else if thread.SessionID != sessionID || thread.AgentID != agentID {
		return fmt.Errorf(
			"runtime state: thread %q identity conflicts with restored Goal",
			threadID,
		)
	}
	if state == nil {
		thread.Goal = nil
	} else {
		goal := goalSnapshotFromState(state)
		thread.Goal = &goal
	}
	thread.UpdatedAt = updatedAt
	thread.Revision++
	s.revision++
	return nil
}

// ActionableRequestIDs intersects persisted diagnostic IDs with unresolved
// requests that still exist in this process for the same session.
func (s *RuntimeStateStore) ActionableRequestIDs(sessionID string, persisted []string) []string {
	if s == nil || len(persisted) == 0 {
		return nil
	}
	wanted := make(map[string]struct{}, len(persisted))
	for _, id := range persisted {
		if id = strings.TrimSpace(id); id != "" {
			wanted[id] = struct{}{}
		}
	}
	s.mu.RLock()
	matched := make(map[string]struct{})
	for _, thread := range s.threads {
		if thread.SessionID != sessionID && thread.ParentSessionID != sessionID {
			continue
		}
		for id := range thread.PendingInteractions {
			if _, ok := wanted[id]; ok {
				matched[id] = struct{}{}
			}
		}
	}
	s.mu.RUnlock()
	out := make([]string, 0, len(matched))
	for id := range matched {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// HasLivePlanApproval reports whether the reducer still owns the exact
// unresolved Plan request referenced by a durable checkpoint.
func (s *RuntimeStateStore) HasLivePlanApproval(
	sessionID string,
	threadID string,
	record *session.PersistedPlanState,
) bool {
	if s == nil || record == nil ||
		strings.TrimSpace(record.ApprovalRequestID) == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	thread := s.threads[threadID]
	if thread == nil || thread.SessionID != sessionID {
		return false
	}
	request, ok := thread.PendingInteractions[record.ApprovalRequestID]
	return ok &&
		request.Kind == PermissionInteractionKindPlanApproval &&
		request.PlanRevision == record.Revision &&
		request.PlanFile == record.PlanFileIdentity &&
		request.PlanInitialDigest == record.ApprovalInitialDigest &&
		request.PlanReturnMode == record.ReturnMode
}

// UnresolvedRequests returns unresolved requests in deterministic sequence
// order. Resolved requests are intentionally absent.
func (s RuntimeSnapshot) UnresolvedRequests(threadID string) []RuntimeInteractionSnapshot {
	thread, ok := s.Threads[threadID]
	if !ok {
		return nil
	}
	out := make([]RuntimeInteractionSnapshot, 0, len(thread.PendingInteractions))
	for _, request := range thread.PendingInteractions {
		request.Input = cloneRuntimeMap(request.Input)
		out = append(out, request)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sequence == out[j].Sequence {
			return out[i].ID < out[j].ID
		}
		return out[i].Sequence < out[j].Sequence
	})
	return out
}

func validateRuntimeEventEnvelope(evt QueryEvent) error {
	switch {
	case strings.TrimSpace(evt.SessionID) == "":
		return fmt.Errorf("runtime state: event %q has no session ID", evt.Type)
	case strings.TrimSpace(evt.ThreadID) == "":
		return fmt.Errorf("runtime state: event %q has no thread ID", evt.Type)
	case strings.TrimSpace(evt.TurnID) == "":
		return fmt.Errorf("runtime state: event %q has no turn ID", evt.Type)
	case evt.Sequence == 0:
		return fmt.Errorf("runtime state: event %q has zero sequence", evt.Type)
	case evt.Timestamp.IsZero():
		return fmt.Errorf("runtime state: event %q has zero timestamp", evt.Type)
	case evt.Type == "":
		return fmt.Errorf("runtime state: event has no type")
	}
	if err := validateGoalRuntimeIdentity(evt); err != nil {
		return err
	}
	switch evt.Type {
	case EventPermissionRequest:
		if evt.PermissionRequest == nil || strings.TrimSpace(evt.PermissionRequest.ToolUseID) == "" {
			return fmt.Errorf("runtime state: permission request has no tool use ID")
		}
		if evt.PermissionRequest.Kind == PermissionInteractionKindPlanApproval {
			approval := evt.PermissionRequest.PlanApproval
			if approval == nil ||
				approval.RequestID != evt.PermissionRequest.ToolUseID ||
				approval.PlanRevision == 0 ||
				strings.TrimSpace(approval.PlanFileIdentity) == "" {
				return fmt.Errorf(
					"runtime state: Plan approval request has incomplete identity",
				)
			}
		}
	case EventPermissionResolved:
		if evt.PermissionResolved == nil || strings.TrimSpace(evt.PermissionResolved.ToolUseID) == "" {
			return fmt.Errorf("runtime state: permission resolution has no tool use ID")
		}
		if evt.PermissionResolved.Kind == PermissionInteractionKindPlanApproval {
			approval := evt.PermissionResolved.PlanApproval
			if approval == nil ||
				approval.RequestID != evt.PermissionResolved.ToolUseID ||
				approval.PlanRevision == 0 {
				return fmt.Errorf(
					"runtime state: Plan approval resolution has incomplete identity",
				)
			}
		}
	case EventPlanStateTransition:
		transition := evt.PlanStateTransition
		if transition == nil ||
			strings.TrimSpace(transition.RequestID) == "" ||
			transition.Revision == 0 ||
			transition.Phase == "" {
			return fmt.Errorf(
				"runtime state: Plan transition has incomplete identity",
			)
		}
	case EventGoalLifecycle:
		lifecycle := evt.GoalLifecycle
		if lifecycle == nil ||
			lifecycle.Phase == "" ||
			strings.TrimSpace(lifecycle.Goal.GoalID) == "" ||
			lifecycle.Goal.ObjectiveRevision == 0 ||
			lifecycle.Goal.Revision == 0 ||
			!lifecycle.Goal.Available ||
			lifecycle.Goal.GoalID != evt.GoalID ||
			lifecycle.Goal.ObjectiveRevision != evt.GoalObjectiveRevision ||
			evt.SessionID != evt.GoalRootSessionID ||
			evt.ThreadID != evt.GoalRootThreadID ||
			evt.AgentID != evt.GoalRootAgentID {
			return fmt.Errorf(
				"runtime state: Goal lifecycle event has incomplete identity",
			)
		}
		if err := validatePersistedGoalState(
			persistedGoalState(goalStateFromLifecycleEvent(lifecycle)),
		); err != nil {
			return fmt.Errorf(
				"runtime state: Goal lifecycle snapshot is invalid: %w",
				err,
			)
		}
		if err := validateGoalLifecycleEvent(evt); err != nil {
			return err
		}
	case EventCommandResult:
		result := evt.CommandResult
		if result == nil || strings.TrimSpace(result.Command) == "" {
			return fmt.Errorf("runtime state: command result has no command identity")
		}
		switch result.Status {
		case CommandResultSucceeded, CommandResultFailed, CommandResultUnsupported:
		default:
			return fmt.Errorf("runtime state: command %q has invalid result status %q", result.Command, result.Status)
		}
	case EventCanonicalProjection:
		if evt.CanonicalProjection == nil {
			return fmt.Errorf("runtime state: canonical projection event has no payload")
		}
		if err := evt.CanonicalProjection.Validate(); err != nil {
			return fmt.Errorf("runtime state: invalid canonical projection: %w", err)
		}
	case EventTerminal:
		if evt.TerminalInfo == nil {
			return fmt.Errorf("runtime state: terminal event has no terminal info")
		}
	case EventAgentLifecycle:
		if evt.AgentLifecycle == nil ||
			strings.TrimSpace(evt.AgentID) == "" {
			return fmt.Errorf("runtime state: Agent lifecycle event has no Agent metadata")
		}
		if evt.GoalID != "" &&
			(evt.AgentLifecycle.Generation <= 0 ||
				evt.AgentGeneration != evt.AgentLifecycle.Generation) {
			return fmt.Errorf(
				"runtime state: Goal-bound Agent lifecycle has no exact generation",
			)
		}
	case EventWorktreeLifecycle:
		lifecycle := evt.WorktreeLifecycle
		if lifecycle == nil ||
			strings.TrimSpace(lifecycle.RecordID) == "" ||
			lifecycle.OwnerKind != worktree.OwnerAgent ||
			strings.TrimSpace(lifecycle.OwnerID) == "" ||
			lifecycle.OwnerID != evt.AgentID ||
			lifecycle.RecordRevision == 0 ||
			strings.TrimSpace(lifecycle.Path) == "" ||
			strings.TrimSpace(lifecycle.Branch) == "" {
			return fmt.Errorf("runtime state: worktree lifecycle event has incomplete identity")
		}
	}
	return nil
}

func validateGoalRuntimeIdentity(evt QueryEvent) error {
	hasGoalIdentity := evt.GoalID != "" ||
		evt.GoalObjectiveRevision != 0 ||
		evt.GoalRootSessionID != "" ||
		evt.GoalRootThreadID != "" ||
		evt.GoalRootAgentID != "" ||
		evt.GoalTurnID != ""
	if !hasGoalIdentity {
		return nil
	}
	if !validGoalBindingText(evt.GoalID, true) ||
		evt.GoalObjectiveRevision == 0 ||
		!validGoalBindingText(evt.GoalRootSessionID, true) ||
		!validGoalBindingText(evt.GoalRootThreadID, true) ||
		!validGoalBindingText(evt.GoalRootAgentID, false) {
		return fmt.Errorf("runtime state: event %q has incomplete Goal identity", evt.Type)
	}
	if evt.AgentID != "" && evt.AgentGeneration <= 0 {
		return fmt.Errorf(
			"runtime state: Goal-bound child event %q has no Agent generation",
			evt.Type,
		)
	}
	if evt.AgentID == "" && evt.AgentGeneration != 0 {
		return fmt.Errorf(
			"runtime state: Goal-bound root event %q has an Agent generation",
			evt.Type,
		)
	}
	if evt.GoalTurnID != "" {
		if err := validateGoalTurnID(evt.GoalTurnID); err != nil {
			return fmt.Errorf("runtime state: event %q has invalid Goal turn identity", evt.Type)
		}
	}
	return nil
}

func validateGoalLifecycleEvent(evt QueryEvent) error {
	lifecycle := evt.GoalLifecycle
	if lifecycle == nil {
		return fmt.Errorf("runtime state: Goal lifecycle event has no payload")
	}
	goal := lifecycle.Goal
	if goal.LastGoalTurnID != evt.GoalTurnID {
		return fmt.Errorf(
			"runtime state: Goal lifecycle turn identity conflicts with snapshot",
		)
	}
	switch lifecycle.Phase {
	case GoalLifecycleCreated,
		GoalLifecycleObjectiveEdited,
		GoalLifecycleBudgetUpdated,
		GoalLifecycleCleared:
	case GoalLifecyclePaused:
		if goal.Status != string(goalStatusPaused) {
			return fmt.Errorf("runtime state: paused Goal lifecycle has status %q", goal.Status)
		}
	case GoalLifecycleResumed:
		if goal.Status != string(goalStatusActive) {
			return fmt.Errorf("runtime state: resumed Goal lifecycle has status %q", goal.Status)
		}
	case GoalLifecycleTurnStarted,
		GoalLifecyclePermissionWaiting,
		GoalLifecyclePermissionResumed,
		GoalLifecycleChildWaiting,
		GoalLifecycleChildResumed:
		if evt.GoalTurnID == "" {
			return fmt.Errorf("runtime state: Goal turn lifecycle has no Goal turn identity")
		}
	case GoalLifecycleCompletionRequested:
		if evt.GoalTurnID == "" ||
			goal.PendingCompleteTurnID != evt.GoalTurnID ||
			goal.PendingCompleteObjectiveRevision != goal.ObjectiveRevision {
			return fmt.Errorf("runtime state: Goal completion intent has conflicting identity")
		}
	case GoalLifecycleBlockerReported:
		if evt.GoalTurnID == "" ||
			goal.Status == string(goalStatusBlocked) ||
			goal.BlockerKey == "" ||
			len(goal.BlockerTurnIDs) == 0 ||
			goal.BlockerTurnIDs[len(goal.BlockerTurnIDs)-1] != evt.GoalTurnID {
			return fmt.Errorf("runtime state: Goal blocker report has conflicting evidence")
		}
	case GoalLifecycleBlocked:
		if evt.GoalTurnID == "" ||
			goal.Status != string(goalStatusBlocked) ||
			goal.BlockerKey == "" ||
			len(goal.BlockerTurnIDs) != 3 ||
			goal.BlockerTurnIDs[2] != evt.GoalTurnID {
			return fmt.Errorf("runtime state: blocked Goal lifecycle has conflicting evidence")
		}
	case GoalLifecycleTurnFinished:
		if evt.GoalTurnID == "" ||
			evt.Sequence == ^uint64(0) ||
			goal.LastTerminalSequence != evt.Sequence+1 {
			return fmt.Errorf("runtime state: Goal terminal sequence binding is invalid")
		}
	default:
		return fmt.Errorf(
			"runtime state: unknown Goal lifecycle phase %q",
			lifecycle.Phase,
		)
	}
	return nil
}

func validateRuntimeTransition(thread *runtimeThreadState, evt QueryEvent) error {
	if thread == nil {
		if evt.Sequence != 1 {
			return fmt.Errorf("runtime state: first event for thread %q has sequence %d, want 1", evt.ThreadID, evt.Sequence)
		}
		if evt.Type == EventPermissionResolved {
			return fmt.Errorf("runtime state: permission %q resolved before request", evt.PermissionResolved.ToolUseID)
		}
		return nil
	}
	if evt.SessionID != thread.SessionID || evt.AgentID != thread.AgentID ||
		evt.ParentSessionID != thread.ParentSessionID || evt.ParentThreadID != thread.ParentThreadID ||
		evt.ParentAgentID != thread.ParentAgentID || evt.ParentToolUseID != thread.ParentToolUseID {
		return fmt.Errorf("runtime state: immutable identity changed for thread %q", evt.ThreadID)
	}
	if evt.AgentGeneration != thread.AgentGeneration &&
		(evt.GoalID != "" ||
			evt.Type == EventAgentLifecycle && evt.AgentGeneration > 0) {
		if evt.Type != EventAgentLifecycle ||
			evt.AgentLifecycle == nil ||
			(evt.AgentLifecycle.Phase != "launched" &&
				evt.AgentLifecycle.Phase != "resumed") ||
			evt.AgentGeneration <= thread.AgentGeneration ||
			!isRuntimeTerminalStatus(thread.Status) {
			return fmt.Errorf(
				"runtime state: Agent generation changed for thread %q",
				evt.ThreadID,
			)
		}
	}
	wantSequence := thread.LastSequence + 1
	if evt.Sequence != wantSequence {
		return fmt.Errorf("runtime state: thread %q sequence %d, want %d", evt.ThreadID, evt.Sequence, wantSequence)
	}
	if thread.ActiveTurnID != "" &&
		thread.ActiveTurnID != evt.TurnID &&
		!isRuntimeTerminalStatus(thread.Status) &&
		thread.Status != RuntimeThreadWaitingInput &&
		!isExternalPlanStateTransition(evt) &&
		!isExternalWorktreeLifecycle(evt) &&
		!isExternalGoalLifecycle(evt) {
		return fmt.Errorf("runtime state: thread %q started turn %q while turn %q is %s", evt.ThreadID, evt.TurnID, thread.ActiveTurnID, thread.Status)
	}
	if thread.ActiveTurnID == evt.TurnID &&
		isRuntimeTerminalStatus(thread.Status) &&
		evt.Type != EventTerminal &&
		evt.Type != EventPermissionResolved &&
		evt.Type != EventHookResponse &&
		!isExternalPlanStateTransition(evt) &&
		!isExternalWorktreeLifecycle(evt) &&
		!isExternalGoalLifecycle(evt) &&
		(evt.Type != EventGoalLifecycle ||
			evt.GoalLifecycle == nil ||
			evt.GoalLifecycle.Phase != GoalLifecycleTurnFinished) {
		return fmt.Errorf("runtime state: event %q follows terminal state for turn %q", evt.Type, evt.TurnID)
	}
	if evt.Type == EventPermissionResolved {
		if _, ok := thread.PendingInteractions[evt.PermissionResolved.ToolUseID]; !ok {
			return fmt.Errorf("runtime state: permission %q resolved without an unresolved request", evt.PermissionResolved.ToolUseID)
		}
	}
	return nil
}

func isExternalWorktreeLifecycle(evt QueryEvent) bool {
	return evt.Type == EventWorktreeLifecycle && evt.WorktreeLifecycle != nil
}

func (s *RuntimeStateStore) validateWorktreeTransitionLocked(evt QueryEvent) error {
	if evt.Type != EventWorktreeLifecycle {
		return nil
	}
	next := evt.WorktreeLifecycle
	current, exists := s.worktrees[next.RecordID]
	if !exists {
		if next.FromState != "" ||
			next.State != worktree.StateCreating ||
			next.RecordRevision != 1 {
			return fmt.Errorf(
				"runtime state: first worktree event for %q is %s -> %s revision %d",
				next.RecordID,
				next.FromState,
				next.State,
				next.RecordRevision,
			)
		}
		return nil
	}
	if next.OwnerKind != current.OwnerKind || next.OwnerID != current.OwnerID {
		return fmt.Errorf("runtime state: worktree %q owner changed", next.RecordID)
	}
	if next.Path != current.Path || next.Branch != current.Branch {
		return fmt.Errorf("runtime state: worktree %q immutable location changed", next.RecordID)
	}
	if next.RepoRoot != current.RepoRoot &&
		(current.State != worktree.StateCreating ||
			current.RepositoryIdentity != "" ||
			current.BaseCommit != "") {
		return fmt.Errorf("runtime state: worktree %q repository root changed", next.RecordID)
	}
	if current.RepositoryIdentity != "" &&
		next.RepositoryIdentity != current.RepositoryIdentity {
		return fmt.Errorf("runtime state: worktree %q repository identity changed", next.RecordID)
	}
	if current.BaseCommit != "" && next.BaseCommit != current.BaseCommit {
		return fmt.Errorf("runtime state: worktree %q base commit changed", next.RecordID)
	}
	if next.FromState != current.State {
		return fmt.Errorf(
			"runtime state: worktree %q transition starts at %s, want %s",
			next.RecordID,
			next.FromState,
			current.State,
		)
	}
	if next.RecordRevision != current.RecordRevision+1 {
		return fmt.Errorf(
			"runtime state: worktree %q record revision %d, want %d",
			next.RecordID,
			next.RecordRevision,
			current.RecordRevision+1,
		)
	}
	if next.State == current.State {
		if next.State != worktree.StateCreating {
			return fmt.Errorf(
				"runtime state: worktree %q repeats state %s",
				next.RecordID,
				next.State,
			)
		}
		return nil
	}
	if !validRuntimeWorktreeTransition(current.State, next.State) {
		return fmt.Errorf(
			"runtime state: invalid worktree transition %s -> %s",
			current.State,
			next.State,
		)
	}
	return nil
}

func validRuntimeWorktreeTransition(from, to worktree.State) bool {
	switch from {
	case worktree.StateCreating:
		return to == worktree.StateReady || to == worktree.StateFailed
	case worktree.StateReady:
		return to == worktree.StateRetained || to == worktree.StateRemoving
	case worktree.StateRetained:
		return to == worktree.StateRemoving
	case worktree.StateRemoving:
		return to == worktree.StateRemoved || to == worktree.StateCleanupFailed
	case worktree.StateCleanupFailed:
		return to == worktree.StateRemoving
	default:
		return false
	}
}

func isRuntimeTerminalStatus(status RuntimeThreadStatus) bool {
	switch status {
	case RuntimeThreadCompleted,
		RuntimeThreadFailed,
		RuntimeThreadAborted:
		return true
	default:
		return false
	}
}

func (s *RuntimeStateStore) appendEventLocked(thread *runtimeThreadState, record RuntimeEventRecord) {
	thread.Events = append(thread.Events, record)
	if overflow := len(thread.Events) - s.limits.EventsPerThread; overflow > 0 {
		thread.Events = append([]RuntimeEventRecord(nil), thread.Events[overflow:]...)
		thread.DroppedEvents += uint64(overflow)
	}
}

func runtimeEventRecord(evt QueryEvent) RuntimeEventRecord {
	toolName, toolUseID := runtimeEventToolIdentity(evt)
	record := RuntimeEventRecord{
		RuntimeEventEnvelope: evt.RuntimeEventEnvelope,
		Type:                 evt.Type,
		Family:               evt.Family(),
		ToolName:             toolName,
		ToolUseID:            toolUseID,
		Summary:              truncateRuntimeText(runtimeEventSummary(evt), maxRuntimeEventSummaryRunes),
	}
	if evt.ModelAttempt != nil {
		record.ModelAttemptID = evt.ModelAttempt.AttemptID
		record.ModelProfile = evt.ModelAttempt.Profile
		record.ModelPhase = evt.ModelAttempt.Phase
		record.FailureClass = evt.ModelAttempt.FailureClass
	}
	if evt.TerminalInfo != nil {
		record.TerminalReason = evt.TerminalInfo.Reason
	}
	if evt.CommandResult != nil {
		record.Command = evt.CommandResult.Command
		record.CommandAction = evt.CommandResult.Action
		record.CommandStatus = evt.CommandResult.Status
		record.CommandError = truncateRuntimeText(
			evt.CommandResult.Error,
			maxRuntimeEventSummaryRunes,
		)
		record.CommandPrompt = truncateRuntimeText(
			evt.CommandResult.FollowUpPrompt,
			maxRuntimeEventSummaryRunes,
		)
	}
	return record
}

func runtimeEventToolIdentity(evt QueryEvent) (string, string) {
	switch {
	case evt.CanonicalProjection != nil &&
		evt.CanonicalProjection.Tool != nil:
		return evt.CanonicalProjection.Tool.ToolName,
			evt.CanonicalProjection.Tool.ToolCallID
	case evt.ToolProgress != nil:
		return evt.ToolProgress.ToolName, evt.ToolProgress.ToolUseID
	case evt.TaskProgress != nil:
		return evt.TaskProgress.LastToolName, evt.TaskProgress.ToolUseID
	case evt.PermissionRequest != nil:
		return evt.PermissionRequest.ToolName, evt.PermissionRequest.ToolUseID
	case evt.PermissionResolved != nil:
		return "", evt.PermissionResolved.ToolUseID
	case evt.PlanStateTransition != nil:
		return "", evt.PlanStateTransition.RequestID
	case evt.GoalLifecycle != nil:
		return "", evt.GoalLifecycle.Goal.GoalID
	case evt.WorktreeLifecycle != nil:
		return "", evt.WorktreeLifecycle.RecordID
	case evt.ClassifierStatus != nil:
		return evt.ClassifierStatus.ToolName, evt.ClassifierStatus.ToolUseID
	case evt.ToolResultMessage != nil:
		return evt.ToolResultMessage.ToolName, evt.ToolResultMessage.ToolCallID
	default:
		return "", ""
	}
}

func runtimeEventSummary(evt QueryEvent) string {
	switch {
	case evt.CanonicalProjection != nil:
		return string(evt.CanonicalProjection.Kind)
	case evt.AgentLifecycle != nil:
		if evt.AgentLifecycle.Phase == "backgrounded" {
			return "backgrounded"
		}
		return firstNonEmptyString(evt.AgentLifecycle.Error, evt.AgentLifecycle.Description, evt.AgentLifecycle.Task, evt.AgentLifecycle.Phase)
	case evt.PermissionRequest != nil:
		return evt.PermissionRequest.Message
	case evt.PermissionResolved != nil:
		return firstNonEmptyString(evt.PermissionResolved.Message, evt.PermissionResolved.Decision)
	case evt.PlanStateTransition != nil:
		return fmt.Sprintf(
			"%s -> %s (%s, revision %d)",
			evt.PlanStateTransition.FromPhase,
			evt.PlanStateTransition.Phase,
			evt.PlanStateTransition.PermissionMode,
			evt.PlanStateTransition.Revision,
		)
	case evt.GoalLifecycle != nil:
		return fmt.Sprintf(
			"%s (%s, revision %d)",
			evt.GoalLifecycle.Phase,
			evt.GoalLifecycle.Goal.Status,
			evt.GoalLifecycle.Goal.Revision,
		)
	case evt.WorktreeLifecycle != nil:
		return fmt.Sprintf(
			"%s -> %s (record revision %d)",
			evt.WorktreeLifecycle.FromState,
			evt.WorktreeLifecycle.State,
			evt.WorktreeLifecycle.RecordRevision,
		)
	case evt.ModelAttempt != nil:
		return fmt.Sprintf(
			"%s attempt %d profile %s",
			evt.ModelAttempt.Phase,
			evt.ModelAttempt.AttemptIndex,
			evt.ModelAttempt.Profile,
		)
	case evt.CommandResult != nil:
		return firstNonEmptyString(evt.CommandResult.Output, evt.CommandResult.Error, string(evt.CommandResult.Status))
	case evt.ToolProgress != nil:
		return evt.ToolProgress.Content
	case evt.TaskProgress != nil:
		return firstNonEmptyString(evt.TaskProgress.Summary, evt.TaskProgress.Description)
	case evt.TaskLifecycle != nil:
		return firstNonEmptyString(evt.TaskLifecycle.Subject, evt.TaskLifecycle.Description, evt.TaskLifecycle.Status)
	case evt.HookResponse != nil:
		return firstNonEmptyString(evt.HookResponse.StatusMessage, evt.HookResponse.Outcome, evt.HookResponse.HookName)
	case evt.ToolResultMessage != nil:
		return evt.ToolResultMessage.Content
	case evt.AssistantMessage != nil:
		return evt.AssistantMessage.Content
	case evt.StreamEvent != nil:
		return evt.StreamEvent.Content
	case evt.AttachmentMessage != nil:
		return evt.AttachmentMessage.Content
	case evt.Message != nil:
		return evt.Message.Content
	case evt.TerminalInfo != nil:
		if evt.TerminalInfo.Err != nil {
			return string(evt.TerminalInfo.Reason) + ": " + evt.TerminalInfo.Err.Error()
		}
		return string(evt.TerminalInfo.Reason)
	default:
		return ""
	}
}

func (s *RuntimeStateStore) reduceMessageLocked(thread *runtimeThreadState, evt QueryEvent) {
	if evt.Type == EventTombstone {
		attemptID := strings.TrimSpace(evt.TombstoneUUID)
		if evt.ModelAttempt != nil &&
			strings.TrimSpace(evt.ModelAttempt.AttemptID) != "" {
			attemptID = strings.TrimSpace(evt.ModelAttempt.AttemptID)
		}
		if attemptID != "" {
			if thread.LiveMessage != nil &&
				thread.LiveMessage.ModelAttemptID == attemptID {
				thread.LiveMessage = nil
			}
			retained := thread.Messages[:0]
			for _, message := range thread.Messages {
				if message.ModelAttemptID != attemptID {
					retained = append(retained, message)
				}
			}
			thread.Messages = retained
		}
		return
	}
	msg, completed := runtimeMessageForEvent(evt)
	if msg != nil {
		if completed {
			thread.Messages = append(thread.Messages, newRuntimeMessageSnapshot(evt, msg, true))
			thread.LiveMessage = nil
			s.boundMessagesLocked(thread)
		} else if msg.Role == schema.Assistant {
			s.reduceLiveAssistantLocked(thread, evt, msg)
		}
	}
	if evt.Type == EventTerminal && thread.LiveMessage != nil {
		completedMessage := *thread.LiveMessage
		completedMessage.Completed = true
		thread.Messages = append(thread.Messages, completedMessage)
		thread.LiveMessage = nil
		s.boundMessagesLocked(thread)
	}
}

func runtimeMessageForEvent(evt QueryEvent) (*schema.Message, bool) {
	switch evt.Type {
	case EventAssistant:
		if evt.AssistantMessage != nil {
			return evt.AssistantMessage, true
		}
		return evt.Message, false
	case EventStream:
		return evt.StreamEvent, false
	case EventToolResult:
		if evt.ToolResultMessage != nil {
			return evt.ToolResultMessage, true
		}
		return evt.Message, true
	case EventAttachment:
		return evt.AttachmentMessage, true
	case EventCompactBoundary:
		return evt.CompactBoundaryMessage, true
	case EventCommandResult:
		if evt.CommandResult == nil || evt.CommandResult.Output == "" {
			return nil, false
		}
		return &schema.Message{Role: schema.Assistant, Content: evt.CommandResult.Output}, true
	case EventTerminal:
		return evt.Message, true
	default:
		return nil, false
	}
}

func newRuntimeMessageSnapshot(evt QueryEvent, msg *schema.Message, completed bool) RuntimeMessageSnapshot {
	snapshot := RuntimeMessageSnapshot{
		ID:                runtimeMessageID(evt, msg),
		TranscriptEntryID: evt.TranscriptEntryID,
		ModelAttemptID:    runtimeModelAttemptID(evt),
		TurnID:            evt.TurnID,
		Sequence:          evt.Sequence,
		Role:              string(msg.Role),
		Content:           truncateRuntimeText(msg.Content, maxRuntimeMessageRunes),
		ReasoningContent:  truncateRuntimeText(msg.ReasoningContent, maxRuntimeReasoningRunes),
		ToolCallID:        msg.ToolCallID,
		ToolName:          msg.ToolName,
		Completed:         completed,
		Timestamp:         evt.Timestamp,
	}
	for i, call := range msg.ToolCalls {
		if i >= maxRuntimeMessageToolCalls {
			break
		}
		snapshot.ToolCalls = append(snapshot.ToolCalls, RuntimeToolCallSnapshot{
			ID:           call.ID,
			Name:         call.Function.Name,
			InputPreview: truncateRuntimeText(call.Function.Arguments, maxRuntimeToolPreviewRunes),
		})
	}
	return snapshot
}

func runtimeModelAttemptID(evt QueryEvent) string {
	if evt.ModelAttempt == nil {
		return ""
	}
	return strings.TrimSpace(evt.ModelAttempt.AttemptID)
}

func runtimeMessageID(evt QueryEvent, msg *schema.Message) string {
	if msg != nil && msg.Extra != nil {
		for _, key := range []string{"uuid", "message_id", "id", "command_uuid"} {
			if id, ok := msg.Extra[key].(string); ok && strings.TrimSpace(id) != "" {
				return id
			}
		}
	}
	return fmt.Sprintf("%s:%d", evt.ThreadID, evt.Sequence)
}

func (s *RuntimeStateStore) reduceLiveAssistantLocked(thread *runtimeThreadState, evt QueryEvent, msg *schema.Message) {
	attemptID := runtimeModelAttemptID(evt)
	if thread.LiveMessage == nil ||
		thread.LiveMessage.TurnID != evt.TurnID ||
		(attemptID != "" &&
			thread.LiveMessage.ModelAttemptID != attemptID) {
		live := newRuntimeMessageSnapshot(evt, msg, false)
		thread.LiveMessage = &live
		return
	}
	thread.LiveMessage.Content = truncateRuntimeText(thread.LiveMessage.Content+msg.Content, maxRuntimeMessageRunes)
	thread.LiveMessage.ReasoningContent = truncateRuntimeText(thread.LiveMessage.ReasoningContent+msg.ReasoningContent, maxRuntimeReasoningRunes)
	thread.LiveMessage.Timestamp = evt.Timestamp
	if evt.TranscriptEntryID != "" {
		thread.LiveMessage.TranscriptEntryID = evt.TranscriptEntryID
	}
	mergeRuntimeToolCalls(thread.LiveMessage, msg.ToolCalls)
}

func mergeRuntimeToolCalls(message *RuntimeMessageSnapshot, calls []schema.ToolCall) {
	for _, call := range calls {
		found := false
		for i := range message.ToolCalls {
			if message.ToolCalls[i].ID == call.ID && call.ID != "" {
				message.ToolCalls[i].Name = call.Function.Name
				message.ToolCalls[i].InputPreview = truncateRuntimeText(call.Function.Arguments, maxRuntimeToolPreviewRunes)
				found = true
				break
			}
		}
		if !found && len(message.ToolCalls) < maxRuntimeMessageToolCalls {
			message.ToolCalls = append(message.ToolCalls, RuntimeToolCallSnapshot{
				ID:           call.ID,
				Name:         call.Function.Name,
				InputPreview: truncateRuntimeText(call.Function.Arguments, maxRuntimeToolPreviewRunes),
			})
		}
	}
}

func (s *RuntimeStateStore) boundMessagesLocked(thread *runtimeThreadState) {
	if overflow := len(thread.Messages) - s.limits.MessagesPerThread; overflow > 0 {
		thread.Messages = append([]RuntimeMessageSnapshot(nil), thread.Messages[overflow:]...)
		thread.DroppedMessages += uint64(overflow)
	}
}

func (s *RuntimeStateStore) reduceToolLocked(thread *runtimeThreadState, evt QueryEvent) {
	if thread.Tools == nil {
		thread.Tools = make(map[string]RuntimeToolSnapshot)
	}
	msg, _ := runtimeMessageForEvent(evt)
	if msg != nil {
		for _, call := range msg.ToolCalls {
			if strings.TrimSpace(call.ID) == "" {
				continue
			}
			s.upsertToolLocked(thread, RuntimeToolSnapshot{
				ID:           call.ID,
				Name:         call.Function.Name,
				Status:       "running",
				InputPreview: truncateRuntimeText(call.Function.Arguments, maxRuntimeToolPreviewRunes),
				TurnID:       evt.TurnID,
				Sequence:     evt.Sequence,
				UpdatedAt:    evt.Timestamp,
			})
		}
	}
	if evt.ToolProgress != nil && strings.TrimSpace(evt.ToolProgress.ToolUseID) != "" {
		tool := thread.Tools[evt.ToolProgress.ToolUseID]
		tool.ID = evt.ToolProgress.ToolUseID
		tool.Name = firstNonEmptyString(evt.ToolProgress.ToolName, tool.Name)
		tool.Status = "running"
		if evt.ToolProgress.IsFinal {
			tool.Status = "completed"
		}
		tool.ProgressPreview = truncateRuntimeText(evt.ToolProgress.Content, maxRuntimeToolPreviewRunes)
		tool.TurnID = evt.TurnID
		tool.Sequence = evt.Sequence
		tool.UpdatedAt = evt.Timestamp
		s.upsertToolLocked(thread, tool)
	}
	if evt.Type == EventToolResult {
		result := evt.ToolResultMessage
		if result == nil {
			result = evt.Message
		}
		if result != nil && strings.TrimSpace(result.ToolCallID) != "" {
			tool := thread.Tools[result.ToolCallID]
			tool.ID = result.ToolCallID
			tool.Name = firstNonEmptyString(result.ToolName, tool.Name)
			tool.Status = "completed"
			if result.Extra != nil {
				if _, ok := result.Extra["is_error"]; ok {
					tool.Status = "failed"
				}
			}
			tool.OutputPreview = truncateRuntimeText(result.Content, maxRuntimeToolPreviewRunes)
			tool.TurnID = evt.TurnID
			tool.Sequence = evt.Sequence
			tool.UpdatedAt = evt.Timestamp
			s.upsertToolLocked(thread, tool)
		}
	}
}

func (s *RuntimeStateStore) upsertToolLocked(thread *runtimeThreadState, tool RuntimeToolSnapshot) {
	if _, exists := thread.Tools[tool.ID]; !exists {
		thread.ToolOrder = append(thread.ToolOrder, tool.ID)
	}
	thread.Tools[tool.ID] = tool
	for len(thread.ToolOrder) > s.limits.ToolsPerThread {
		index := 0
		for i, id := range thread.ToolOrder {
			status := thread.Tools[id].Status
			if status == "completed" || status == "failed" {
				index = i
				break
			}
		}
		id := thread.ToolOrder[index]
		delete(thread.Tools, id)
		thread.ToolOrder = append(thread.ToolOrder[:index], thread.ToolOrder[index+1:]...)
	}
}

func (s *RuntimeStateStore) reduceInteractionLocked(thread *runtimeThreadState, evt QueryEvent) {
	if thread.PendingInteractions == nil {
		thread.PendingInteractions = make(map[string]RuntimeInteractionSnapshot)
	}
	switch evt.Type {
	case EventPermissionRequest:
		kind := strings.TrimSpace(evt.PermissionRequest.Kind)
		if kind == "" {
			kind = "permission"
		}
		input, truncated := boundedRuntimeInput(evt.PermissionRequest.Input)
		var planRevision uint64
		var planFile string
		var planInitialDigest string
		var planReturnMode string
		if evt.PermissionRequest.PlanApproval != nil {
			planRevision = evt.PermissionRequest.PlanApproval.PlanRevision
			planFile = evt.PermissionRequest.PlanApproval.PlanFileIdentity
			planInitialDigest = evt.PermissionRequest.PlanApproval.InitialPlanDigest
			planReturnMode = string(
				evt.PermissionRequest.PlanApproval.ReturnMode,
			)
		}
		thread.PendingInteractions[evt.PermissionRequest.ToolUseID] = RuntimeInteractionSnapshot{
			ID:                evt.PermissionRequest.ToolUseID,
			Kind:              kind,
			ToolName:          evt.PermissionRequest.ToolName,
			CanonicalToolName: evt.PermissionRequest.CanonicalToolName,
			Source:            evt.PermissionRequest.Source,
			Message:           truncateRuntimeText(evt.PermissionRequest.Message, maxRuntimeInteractionRunes),
			Input:             input,
			InputTruncated:    truncated,
			Attempt:           evt.PermissionRequest.Attempt,
			PlanRevision:      planRevision,
			PlanFile:          planFile,
			PlanInitialDigest: planInitialDigest,
			PlanReturnMode:    planReturnMode,
			TurnID:            evt.TurnID,
			Sequence:          evt.Sequence,
			RequestedAt:       evt.Timestamp,
		}
		thread.Status = RuntimeThreadWaitingInput
	case EventPermissionResolved:
		delete(thread.PendingInteractions, evt.PermissionResolved.ToolUseID)
		if len(thread.PendingInteractions) == 0 &&
			(!isRuntimeTerminalStatus(thread.Status) ||
				thread.Status == RuntimeThreadWaitingInput) {
			thread.Status = RuntimeThreadRunning
			thread.CompletedAt = time.Time{}
		}
	}
}

func (s *RuntimeStateStore) reducePlanLocked(
	thread *runtimeThreadState,
	evt QueryEvent,
) {
	if evt.Type != EventPlanStateTransition ||
		evt.PlanStateTransition == nil {
		return
	}
	transition := evt.PlanStateTransition
	thread.Plan = &RuntimePlanSnapshot{
		Phase:             transition.Phase,
		PermissionMode:    string(transition.PermissionMode),
		PlanFileIdentity:  transition.PlanFileIdentity,
		ReturnMode:        string(transition.ReturnMode),
		ApprovalRequestID: transition.ApprovalRequestID,
		RequestID:         transition.RequestID,
		Revision:          transition.Revision,
		Sequence:          evt.Sequence,
		UpdatedAt:         evt.Timestamp,
	}
}

func (s *RuntimeStateStore) validateGoalTransitionLocked(
	thread *runtimeThreadState,
	evt QueryEvent,
) error {
	if evt.Type != EventGoalLifecycle || evt.GoalLifecycle == nil {
		if evt.GoalID == "" || evt.AgentID == "" {
			return nil
		}
		current, exists := s.agents[evt.AgentID]
		if !exists || current.Generation < evt.AgentGeneration {
			return nil
		}
		if current.Generation > evt.AgentGeneration ||
			current.GoalID != evt.GoalID ||
			current.GoalObjectiveRevision != evt.GoalObjectiveRevision ||
			current.GoalRootSessionID != evt.GoalRootSessionID ||
			current.GoalRootThreadID != evt.GoalRootThreadID ||
			current.GoalRootAgentID != evt.GoalRootAgentID ||
			current.GoalTurnID != evt.GoalTurnID {
			return fmt.Errorf(
				"runtime state: stale or conflicting Goal-bound Agent generation %q",
				evt.AgentID,
			)
		}
		return nil
	}
	next := evt.GoalLifecycle.Goal
	if thread == nil || thread.Goal == nil {
		return nil
	}
	current := thread.Goal
	if evt.GoalLifecycle.Phase == GoalLifecycleCleared {
		if current.GoalID != next.GoalID ||
			current.ObjectiveRevision != next.ObjectiveRevision ||
			current.Revision != next.Revision {
			return fmt.Errorf("runtime state: stale Goal clear")
		}
		return nil
	}
	if current.GoalID != next.GoalID {
		if evt.GoalLifecycle.Phase != GoalLifecycleCreated ||
			current.Status != string(goalStatusComplete) {
			return fmt.Errorf("runtime state: Goal identity changed without completed replacement")
		}
		return nil
	}
	if next.Revision <= current.Revision {
		return fmt.Errorf(
			"runtime state: Goal revision %d is not newer than %d",
			next.Revision,
			current.Revision,
		)
	}
	if next.ObjectiveRevision < current.ObjectiveRevision ||
		next.ObjectiveRevision > current.ObjectiveRevision+1 {
		return fmt.Errorf(
			"runtime state: Goal objective revision changed from %d to %d",
			current.ObjectiveRevision,
			next.ObjectiveRevision,
		)
	}
	if next.ObjectiveRevision > current.ObjectiveRevision &&
		evt.GoalLifecycle.Phase != GoalLifecycleObjectiveEdited {
		return fmt.Errorf("runtime state: Goal objective changed outside edit transition")
	}
	if next.RootActiveTimeMillis < current.RootActiveTimeMillis {
		return fmt.Errorf("runtime state: Goal active time moved backwards")
	}
	return nil
}

func (s *RuntimeStateStore) reduceGoalLocked(
	thread *runtimeThreadState,
	evt QueryEvent,
) {
	if evt.Type != EventGoalLifecycle || evt.GoalLifecycle == nil {
		return
	}
	if evt.GoalLifecycle.Phase == GoalLifecycleCleared {
		thread.Goal = nil
		return
	}
	goal := cloneGoalSnapshot(evt.GoalLifecycle.Goal)
	thread.Goal = &goal
}

func isExternalPlanStateTransition(evt QueryEvent) bool {
	if evt.Type != EventPlanStateTransition ||
		evt.PlanStateTransition == nil {
		return false
	}
	switch evt.PlanStateTransition.Source {
	case string(planTransitionExternal),
		string(planTransitionUserConfirmed):
		// Both sources are emitted by the external execution-control adapter,
		// outside a query turn. User confirmation changes the authorization
		// contract for bypass mode; it does not make the transition turn-owned.
		return true
	default:
		return false
	}
}

func isExternalGoalLifecycle(evt QueryEvent) bool {
	if evt.Type != EventGoalLifecycle || evt.GoalLifecycle == nil {
		return false
	}
	switch evt.GoalLifecycle.Phase {
	case GoalLifecycleCreated,
		GoalLifecycleObjectiveEdited,
		GoalLifecyclePaused,
		GoalLifecycleResumed,
		GoalLifecycleBudgetUpdated,
		GoalLifecycleCleared:
		return true
	default:
		return false
	}
}

func (s *RuntimeStateStore) reduceTerminalLocked(thread *runtimeThreadState, evt QueryEvent) {
	if evt.Type != EventTerminal || evt.TerminalInfo == nil {
		return
	}
	terminal := &RuntimeTerminalSnapshot{
		TurnID:    evt.TurnID,
		Reason:    evt.TerminalInfo.Reason,
		TurnCount: evt.TerminalInfo.TurnCount,
		MaxTurns:  evt.TerminalInfo.MaxTurns,
		Sequence:  evt.Sequence,
		Timestamp: evt.Timestamp,
	}
	if evt.TerminalInfo.Err != nil {
		terminal.Error = truncateRuntimeText(evt.TerminalInfo.Err.Error(), maxRuntimeInteractionRunes)
	}
	thread.LastTerminal = terminal
	thread.CompletedAt = evt.Timestamp
	thread.Status = runtimeStatusForTerminal(evt.TerminalInfo.Reason)
}

func runtimeStatusForTerminal(reason TerminalReason) RuntimeThreadStatus {
	switch reason {
	case TerminalWaitingInput:
		return RuntimeThreadWaitingInput
	case TerminalCompleted:
		return RuntimeThreadCompleted
	case TerminalAbortedStreaming, TerminalAbortedTools:
		return RuntimeThreadAborted
	default:
		return RuntimeThreadFailed
	}
}

func (s *RuntimeStateStore) reduceAgentLocked(thread *runtimeThreadState, evt QueryEvent) {
	updatedAgents := make(map[string]struct{}, 2)
	if strings.TrimSpace(evt.AgentID) != "" {
		agent := s.agents[evt.AgentID]
		agent.AgentID = evt.AgentID
		agent.SessionID = evt.SessionID
		agent.ThreadID = evt.ThreadID
		agent.ParentSessionID = evt.ParentSessionID
		agent.ParentThreadID = evt.ParentThreadID
		agent.ParentAgentID = evt.ParentAgentID
		agent.ParentToolUseID = evt.ParentToolUseID
		if evt.AgentGeneration > agent.Generation &&
			evt.GoalID == "" {
			agent.GoalID = ""
			agent.GoalObjectiveRevision = 0
			agent.GoalRootSessionID = ""
			agent.GoalRootThreadID = ""
			agent.GoalRootAgentID = ""
			agent.GoalTurnID = ""
		}
		if evt.AgentGeneration > 0 {
			agent.Generation = evt.AgentGeneration
		}
		if evt.GoalID != "" {
			agent.GoalID = evt.GoalID
			agent.GoalObjectiveRevision = evt.GoalObjectiveRevision
			agent.GoalRootSessionID = evt.GoalRootSessionID
			agent.GoalRootThreadID = evt.GoalRootThreadID
			agent.GoalRootAgentID = evt.GoalRootAgentID
			agent.GoalTurnID = evt.GoalTurnID
		}
		agent.Status = string(thread.Status)
		agent.UpdatedAt = evt.Timestamp
		if isRuntimeTerminalStatus(thread.Status) {
			agent.CompletedAt = evt.Timestamp
		}
		s.agents[evt.AgentID] = agent
		updatedAgents[evt.AgentID] = struct{}{}
	}
	if evt.AgentLifecycle != nil && strings.TrimSpace(evt.AgentID) != "" {
		lifecycle := evt.AgentLifecycle
		agent := s.agents[evt.AgentID]
		fillRuntimeAgentText(&agent.Name, lifecycle.Name, maxRuntimeEventSummaryRunes)
		fillRuntimeAgentText(&agent.Task, lifecycle.Task, maxRuntimeInteractionRunes)
		fillRuntimeAgentText(&agent.Description, lifecycle.Description, maxRuntimeInteractionRunes)
		fillRuntimeAgentText(&agent.AgentType, lifecycle.AgentType, maxRuntimeEventSummaryRunes)
		fillRuntimeAgentText(&agent.Model, lifecycle.Model, maxRuntimeEventSummaryRunes)
		fillRuntimeAgentText(&agent.PermissionMode, lifecycle.PermissionMode, maxRuntimeEventSummaryRunes)
		fillRuntimeAgentText(&agent.Isolation, lifecycle.Isolation, maxRuntimeEventSummaryRunes)
		fillRuntimeAgentText(&agent.CWD, lifecycle.CWD, maxRuntimeInteractionRunes)
		fillRuntimeAgentText(&agent.WorktreePath, lifecycle.WorktreePath, maxRuntimeInteractionRunes)
		fillRuntimeAgentText(&agent.WorktreeBranch, lifecycle.WorktreeBranch, maxRuntimeEventSummaryRunes)
		fillRuntimeAgentText(&agent.TranscriptPath, lifecycle.TranscriptPath, maxRuntimeInteractionRunes)
		fillRuntimeAgentText(&agent.OutputFile, lifecycle.OutputFile, maxRuntimeInteractionRunes)
		fillRuntimeAgentText(&agent.Error, lifecycle.Error, maxRuntimeInteractionRunes)
		if strings.TrimSpace(lifecycle.Status) != "" {
			agent.Status = lifecycle.Status
		}
		if lifecycle.Generation > 0 {
			agent.Generation = lifecycle.Generation
		}
		if !lifecycle.StartedAt.IsZero() {
			agent.StartedAt = lifecycle.StartedAt
		}
		agent.UpdatedAt = evt.Timestamp
		if isRuntimeTerminalAgentStatus(agent.Status) {
			agent.CompletedAt = evt.Timestamp
		} else {
			agent.CompletedAt = time.Time{}
		}
		s.agents[evt.AgentID] = agent
		updatedAgents[evt.AgentID] = struct{}{}
	}
	if evt.TaskProgress != nil && strings.TrimSpace(evt.TaskProgress.TaskID) != "" {
		agent := s.agents[evt.TaskProgress.TaskID]
		agent.AgentID = evt.TaskProgress.TaskID
		if evt.AgentID != evt.TaskProgress.TaskID {
			agent.ParentSessionID = evt.SessionID
			agent.ParentThreadID = evt.ThreadID
			agent.ParentAgentID = evt.AgentID
			agent.ParentToolUseID = evt.TaskProgress.ToolUseID
		}
		agent.Description = truncateRuntimeText(evt.TaskProgress.Description, maxRuntimeInteractionRunes)
		agent.Status = "running"
		agent.Progress = RuntimeAgentProgressSnapshot{
			ToolUses:         evt.TaskProgress.Usage.ToolUses,
			TotalTokens:      evt.TaskProgress.Usage.TotalTokens,
			DurationMS:       evt.TaskProgress.Usage.DurationMS,
			LastToolName:     evt.TaskProgress.LastToolName,
			Summary:          truncateRuntimeText(evt.TaskProgress.Summary, maxRuntimeEventSummaryRunes),
			RecentActivities: runtimeAgentActivities(evt.TaskProgress.RecentActivities),
		}
		agent.UpdatedAt = evt.Timestamp
		s.agents[agent.AgentID] = agent
		updatedAgents[agent.AgentID] = struct{}{}
	}
	if evt.AttachmentMessage != nil && evt.AttachmentMessage.Extra != nil {
		extra := evt.AttachmentMessage.Extra
		mode, _ := extra["command_mode"].(string)
		agentID, _ := extra["task_notification_agent_id"].(string)
		if mode == "task-notification" && strings.TrimSpace(agentID) != "" {
			agent := s.agents[agentID]
			agent.AgentID = agentID
			agent.ParentSessionID = evt.SessionID
			agent.ParentThreadID = evt.ThreadID
			agent.ParentAgentID = evt.AgentID
			agent.Status, _ = extra["task_notification_status"].(string)
			agent.Description, _ = extra["task_notification_description"].(string)
			agent.Description = truncateRuntimeText(agent.Description, maxRuntimeInteractionRunes)
			agent.ParentToolUseID, _ = extra["task_notification_tool_use_id"].(string)
			agent.DeliveredCompletionID, _ = extra["agent_completion_id"].(string)
			agent.DeliveredTerminalSequence = runtimeUint64Extra(
				extra["agent_completion_terminal_sequence"],
			)
			agent.UpdatedAt = evt.Timestamp
			if agent.Status != "running" {
				agent.CompletedAt = evt.Timestamp
			}
			s.agents[agentID] = agent
			updatedAgents[agentID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(updatedAgents))
	for agentID := range updatedAgents {
		ids = append(ids, agentID)
	}
	sort.Strings(ids)
	for _, agentID := range ids {
		s.recordExecutionObservationLocked(agentID, true, false)
	}
	s.enforceAgentLimitLocked()
}

func (s *RuntimeStateStore) isDuplicateAgentCompletionLocked(
	evt QueryEvent,
) bool {
	if s == nil || evt.AttachmentMessage == nil ||
		evt.AttachmentMessage.Extra == nil {
		return false
	}
	extra := evt.AttachmentMessage.Extra
	mode, _ := extra["command_mode"].(string)
	completionID, _ := extra["agent_completion_id"].(string)
	agentID, _ := extra["task_notification_agent_id"].(string)
	if mode != "task-notification" ||
		strings.TrimSpace(completionID) == "" ||
		strings.TrimSpace(agentID) == "" {
		return false
	}
	return s.agents[agentID].DeliveredCompletionID == completionID
}

func runtimeUint64Extra(value any) uint64 {
	switch typed := value.(type) {
	case uint64:
		return typed
	case uint:
		return uint64(typed)
	case int:
		if typed > 0 {
			return uint64(typed)
		}
	case int64:
		if typed > 0 {
			return uint64(typed)
		}
	case float64:
		if typed > 0 {
			return uint64(typed)
		}
	}
	return 0
}

func fillRuntimeAgentText(target *string, value string, maxRunes int) {
	if target == nil || strings.TrimSpace(value) == "" {
		return
	}
	*target = truncateRuntimeText(value, maxRunes)
}

func isRuntimeAgentLifecycleEvent(evt QueryEvent) bool {
	return evt.Type == EventAgentLifecycle && evt.AgentLifecycle != nil
}

func isRuntimeAgentLaunchBoundary(evt QueryEvent) bool {
	if !isRuntimeAgentLifecycleEvent(evt) {
		return false
	}
	switch strings.TrimSpace(evt.AgentLifecycle.Phase) {
	case "launched", "resumed", "launch_failed":
		return true
	case "paused", "resumed_control", "backgrounded":
		return false
	default:
		// Backward compatibility for launch records written before Phase became
		// mandatory in the runtime contract.
		return strings.HasPrefix(evt.TurnID, "agent-launch:")
	}
}

func isRuntimeAgentBackgroundedTransition(evt QueryEvent) bool {
	return isRuntimeAgentLifecycleEvent(evt) &&
		strings.TrimSpace(evt.AgentLifecycle.Phase) == "backgrounded"
}

func isRuntimeTerminalAgentStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "failed", "aborted", "killed":
		return true
	default:
		return false
	}
}

func runtimeStatusForAgentLifecycle(status string) RuntimeThreadStatus {
	switch strings.TrimSpace(status) {
	case "paused":
		return RuntimeThreadPaused
	case "failed":
		return RuntimeThreadFailed
	case "aborted", "killed":
		return RuntimeThreadAborted
	case "completed":
		return RuntimeThreadCompleted
	default:
		return RuntimeThreadRunning
	}
}

func (s *RuntimeStateStore) reduceTaskLocked(evt QueryEvent) {
	if evt.TaskLifecycle == nil || strings.TrimSpace(evt.TaskLifecycle.TaskID) == "" {
		return
	}
	lifecycle := evt.TaskLifecycle
	task := s.tasks[lifecycle.TaskID]
	task.TaskID = lifecycle.TaskID
	if lifecycle.Subject != "" {
		task.Subject = truncateRuntimeText(lifecycle.Subject, maxRuntimeInteractionRunes)
	}
	if lifecycle.Description != "" {
		task.Description = truncateRuntimeText(lifecycle.Description, maxRuntimeInteractionRunes)
	}
	if lifecycle.ActiveForm != "" {
		task.ActiveForm = truncateRuntimeText(lifecycle.ActiveForm, maxRuntimeInteractionRunes)
	}
	if lifecycle.Status != "" {
		task.Status = lifecycle.Status
	}
	if lifecycle.Owner != "" {
		task.Owner = truncateRuntimeText(lifecycle.Owner, maxRuntimeEventSummaryRunes)
	}
	updatedAt := lifecycle.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = evt.Timestamp
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = updatedAt
	}
	if task.UpdatedAt.IsZero() || updatedAt.After(task.UpdatedAt) {
		task.UpdatedAt = updatedAt
	}
	if isTerminalRuntimeTaskStatus(task.Status) {
		task.CompletedAt = updatedAt
	} else {
		task.CompletedAt = time.Time{}
	}
	s.tasks[task.TaskID] = task
	s.enforceTaskLimitLocked()
}

func (s *RuntimeStateStore) reduceWorktreeLocked(evt QueryEvent) {
	if evt.Type != EventWorktreeLifecycle || evt.WorktreeLifecycle == nil {
		return
	}
	lifecycle := evt.WorktreeLifecycle
	s.worktrees[lifecycle.RecordID] = RuntimeWorktreeSnapshot{
		RecordID:            lifecycle.RecordID,
		OwnerKind:           lifecycle.OwnerKind,
		OwnerID:             lifecycle.OwnerID,
		State:               lifecycle.State,
		RecoveryDisposition: worktree.RecoveryInspectOnly,
		RecordRevision:      lifecycle.RecordRevision,
		RepositoryIdentity:  lifecycle.RepositoryIdentity,
		RepoRoot:            lifecycle.RepoRoot,
		Path:                lifecycle.Path,
		Branch:              lifecycle.Branch,
		BaseCommit:          lifecycle.BaseCommit,
		LastErrorCategory:   lifecycle.LastErrorCategory,
		LastError: truncateRuntimeText(
			lifecycle.LastError,
			maxRuntimeInteractionRunes,
		),
		Sequence:  evt.Sequence,
		UpdatedAt: evt.Timestamp,
	}
	s.enforceWorktreeLimitLocked()
}

func isTerminalRuntimeTaskStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "failed", "aborted", "killed", "stopped":
		return true
	default:
		return false
	}
}

func (s *RuntimeStateStore) enforceTaskLimitLocked() {
	for len(s.tasks) > s.limits.Tasks {
		ids := make([]string, 0, len(s.tasks))
		for id := range s.tasks {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			left, right := s.tasks[ids[i]], s.tasks[ids[j]]
			leftTerminal := isTerminalRuntimeTaskStatus(left.Status)
			rightTerminal := isTerminalRuntimeTaskStatus(right.Status)
			if leftTerminal != rightTerminal {
				return leftTerminal
			}
			if left.UpdatedAt.Equal(right.UpdatedAt) {
				return ids[i] < ids[j]
			}
			return left.UpdatedAt.Before(right.UpdatedAt)
		})
		delete(s.tasks, ids[0])
	}
}

func (s *RuntimeStateStore) enforceAgentLimitLocked() {
	for len(s.agents) > s.limits.Agents {
		ids := make([]string, 0, len(s.agents))
		for id := range s.agents {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			left, right := s.agents[ids[i]], s.agents[ids[j]]
			leftTerminal := left.Status != "" && left.Status != "running" && left.Status != string(RuntimeThreadWaitingInput)
			rightTerminal := right.Status != "" && right.Status != "running" && right.Status != string(RuntimeThreadWaitingInput)
			if leftTerminal != rightTerminal {
				return leftTerminal
			}
			if left.UpdatedAt.Equal(right.UpdatedAt) {
				return ids[i] < ids[j]
			}
			return left.UpdatedAt.Before(right.UpdatedAt)
		})
		delete(s.agents, ids[0])
	}
}

func (s *RuntimeStateStore) recordExecutionObservationLocked(
	agentID string,
	ordinalPresent bool,
	replayOnly bool,
) {
	agent, ok := s.agents[agentID]
	if !ok || strings.TrimSpace(agent.AgentID) == "" || agent.Generation <= 0 {
		return
	}
	key := RuntimeExecutionKey{
		AgentID:    agent.AgentID,
		Generation: agent.Generation,
	}
	delete(s.hiddenLiveExecutions, key)
	execution := s.executions[key]
	execution.Key = key
	execution.Agent = cloneRuntimeAgentSnapshot(agent)
	execution.ReplayOnly = replayOnly
	if ordinalPresent {
		s.executionOrdinal++
		execution.ObservationOrdinal = s.executionOrdinal
		execution.OrdinalPresent = true
	}
	s.executions[key] = execution
	s.enforceExecutionLimitLocked()
}

func (s *RuntimeStateStore) enforceExecutionLimitLocked() {
	for len(s.executions) > s.limits.Agents {
		keys := make([]RuntimeExecutionKey, 0, len(s.executions))
		for key := range s.executions {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			left, right := s.executions[keys[i]], s.executions[keys[j]]
			leftTerminal := isRuntimeTerminalAgentStatus(left.Agent.Status)
			rightTerminal := isRuntimeTerminalAgentStatus(right.Agent.Status)
			if leftTerminal != rightTerminal {
				return leftTerminal
			}
			if left.OrdinalPresent != right.OrdinalPresent {
				return !left.OrdinalPresent
			}
			if left.ObservationOrdinal != right.ObservationOrdinal {
				return left.ObservationOrdinal < right.ObservationOrdinal
			}
			if keys[i].AgentID != keys[j].AgentID {
				return keys[i].AgentID < keys[j].AgentID
			}
			return keys[i].Generation < keys[j].Generation
		})
		removed := s.executions[keys[0]]
		if !isRuntimeTerminalAgentStatus(removed.Agent.Status) {
			s.hiddenLiveExecutions[keys[0]] = runtimeExecutionLineageFromAgent(removed.Agent)
		}
		delete(s.executions, keys[0])
		s.evictedExecutionGenerations++
	}
}

func (s *RuntimeStateStore) enforceWorktreeLimitLocked() {
	for len(s.worktrees) > s.limits.Worktrees {
		ids := make([]string, 0, len(s.worktrees))
		for id := range s.worktrees {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			left, right := s.worktrees[ids[i]], s.worktrees[ids[j]]
			leftTerminal := left.State == worktree.StateRemoved ||
				left.State == worktree.StateFailed
			rightTerminal := right.State == worktree.StateRemoved ||
				right.State == worktree.StateFailed
			if leftTerminal != rightTerminal {
				return leftTerminal
			}
			if left.UpdatedAt.Equal(right.UpdatedAt) {
				return ids[i] < ids[j]
			}
			return left.UpdatedAt.Before(right.UpdatedAt)
		})
		delete(s.worktrees, ids[0])
	}
}

func cloneRuntimeThreadSnapshot(in RuntimeThreadSnapshot) RuntimeThreadSnapshot {
	out := in
	out.Events = append([]RuntimeEventRecord(nil), in.Events...)
	out.Messages = make([]RuntimeMessageSnapshot, len(in.Messages))
	for i, message := range in.Messages {
		out.Messages[i] = cloneRuntimeMessageSnapshot(message)
	}
	if in.LiveMessage != nil {
		live := cloneRuntimeMessageSnapshot(*in.LiveMessage)
		out.LiveMessage = &live
	}
	out.Tools = make(map[string]RuntimeToolSnapshot, len(in.Tools))
	for id, tool := range in.Tools {
		out.Tools[id] = tool
	}
	out.ToolOrder = append([]string(nil), in.ToolOrder...)
	out.PendingInteractions = make(map[string]RuntimeInteractionSnapshot, len(in.PendingInteractions))
	for id, request := range in.PendingInteractions {
		request.Input = cloneRuntimeMap(request.Input)
		out.PendingInteractions[id] = request
	}
	if in.LastTerminal != nil {
		terminal := *in.LastTerminal
		out.LastTerminal = &terminal
	}
	if in.Plan != nil {
		plan := *in.Plan
		out.Plan = &plan
	}
	if in.Goal != nil {
		goal := cloneGoalSnapshot(*in.Goal)
		out.Goal = &goal
	}
	return out
}

func cloneRuntimeAgentSnapshot(in RuntimeAgentSnapshot) RuntimeAgentSnapshot {
	out := in
	out.Progress.RecentActivities = append([]RuntimeAgentActivitySnapshot(nil), in.Progress.RecentActivities...)
	return out
}

func runtimeAgentActivities(in []TaskProgressActivity) []RuntimeAgentActivitySnapshot {
	if len(in) > maxRuntimeAgentActivities {
		in = in[len(in)-maxRuntimeAgentActivities:]
	}
	out := make([]RuntimeAgentActivitySnapshot, 0, len(in))
	for _, activity := range in {
		out = append(out, RuntimeAgentActivitySnapshot{
			ToolName:    truncateRuntimeText(activity.ToolName, maxRuntimeEventSummaryRunes),
			Description: truncateRuntimeText(activity.Description, maxRuntimeEventSummaryRunes),
			IsSearch:    activity.IsSearch,
			IsRead:      activity.IsRead,
		})
	}
	return out
}

func cloneRuntimeMessageSnapshot(in RuntimeMessageSnapshot) RuntimeMessageSnapshot {
	out := in
	out.ToolCalls = append([]RuntimeToolCallSnapshot(nil), in.ToolCalls...)
	return out
}

func boundedRuntimeInput(input map[string]any) (map[string]any, bool) {
	if len(input) == 0 {
		return nil, false
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string]any, len(input))
	truncated := false
	for _, key := range keys {
		value := cloneRuntimeValue(input[key])
		out[key] = value
		encoded, err := json.Marshal(out)
		if err != nil || len(encoded) > maxRuntimeInteractionInputBytes {
			delete(out, key)
			truncated = true
		}
	}
	return out, truncated
}

func cloneRuntimeMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = cloneRuntimeValue(value)
	}
	return out
}

func cloneRuntimeValue(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return truncateRuntimeText(fmt.Sprint(value), maxRuntimeInteractionRunes)
	}
	var cloned any
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return truncateRuntimeText(fmt.Sprint(value), maxRuntimeInteractionRunes)
	}
	return cloned
}

func truncateRuntimeText(value string, maxRunes int) string {
	if maxRunes <= 0 || value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
