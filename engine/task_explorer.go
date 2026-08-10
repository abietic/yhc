package engine

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/abietic/yhc/engine/internal/workboard"
	"github.com/abietic/yhc/tools"
)

const (
	maxTaskExplorerWorkItems  = 128
	maxTaskExplorerExecutions = 128
	maxTaskExplorerLinks      = 128
	maxTaskExplorerAttention  = 128
	maxTaskExplorerPage       = 100
	maxTaskExplorerTextRunes  = 512
)

var (
	// ErrTaskExplorerNavigationUnavailable reports that current catalog or
	// transcript facts cannot form a supported Ctrl+T navigation target.
	ErrTaskExplorerNavigationUnavailable = errors.New(
		"task explorer navigation is unavailable",
	)
	// ErrTaskExplorerNavigationStale reports that the requested generation or
	// the snapshots used to resolve it are no longer current.
	ErrTaskExplorerNavigationStale = errors.New(
		"task explorer navigation target is stale",
	)
)

type TaskExplorerAction string

const (
	TaskExplorerActionInspect     TaskExplorerAction = "inspect"
	TaskExplorerActionSwitch      TaskExplorerAction = "switch"
	TaskExplorerActionSend        TaskExplorerAction = "send"
	TaskExplorerActionCancelInput TaskExplorerAction = "cancel_input"
	TaskExplorerActionPause       TaskExplorerAction = "pause"
	TaskExplorerActionResume      TaskExplorerAction = "resume"
	TaskExplorerActionCancel      TaskExplorerAction = "cancel"
	TaskExplorerActionContinue    TaskExplorerAction = "continue"
)

const (
	maxTaskExplorerActionPayloadBytes = 64 * 1024
	maxTaskExplorerMessageIDBytes     = 512
)

type TaskExplorerActionRequest struct {
	RequestID       string
	BoardID         string
	BoardRevision   uint64
	RuntimeRevision uint64
	AgentID         string
	Generation      int64
	MessageID       string
	Action          TaskExplorerAction
	Payload         string
}

// TaskExplorerNavigationTarget is the complete engine-resolved identity used
// by Ctrl+T. Generation remains execution-owned and is never inferred from the
// thread catalog.
type TaskExplorerNavigationTarget struct {
	SessionID  string
	ThreadID   string
	AgentID    string
	Generation int64
	Mode       ThreadAttachmentMode
}

type TaskExplorerActionResult struct {
	RequestID        string
	BoardID          string
	BoardRevision    uint64
	RuntimeRevision  uint64
	AgentID          string
	Generation       int64
	MessageID        string
	Action           TaskExplorerAction
	Outcome          string
	Conflict         string
	Message          string
	SessionID        string
	ThreadID         string
	NavigationTarget *TaskExplorerNavigationTarget
	NewGeneration    int64
}

type TaskExplorerExecutionPhase string

const (
	TaskExplorerExecutionRunning      TaskExplorerExecutionPhase = "running"
	TaskExplorerExecutionWaitingInput TaskExplorerExecutionPhase = "waiting_input"
	TaskExplorerExecutionPaused       TaskExplorerExecutionPhase = "paused"
	TaskExplorerExecutionReplayOnly   TaskExplorerExecutionPhase = "replay_only"
	TaskExplorerExecutionCompleted    TaskExplorerExecutionPhase = "completed"
	TaskExplorerExecutionFailed       TaskExplorerExecutionPhase = "failed"
	TaskExplorerExecutionCancelled    TaskExplorerExecutionPhase = "cancelled"
	TaskExplorerExecutionUnknown      TaskExplorerExecutionPhase = "unknown"
)

type TaskExplorerLinkState string

const (
	TaskExplorerLinkValid   TaskExplorerLinkState = "valid"
	TaskExplorerLinkMissing TaskExplorerLinkState = "missing"
	TaskExplorerLinkStale   TaskExplorerLinkState = "stale"
)

type TaskExplorerRevision struct {
	Board   uint64
	Runtime uint64
}

type WorkExecutionLink struct {
	BoardID    string
	WorkItemID string
	AgentID    string
	Generation int64
}

type TaskExplorerWorkItem struct {
	BoardID        string
	WorkItemID     string
	Revision       uint64
	Order          int
	Status         string
	Title          string
	Description    string
	ActiveForm     string
	Owner          string
	ResultSummary  string
	TerminalReason string
	Blocks         []string
	BlockedBy      []string
	LinkedLive     bool
	Attention      []string
	ExecutionKeys  []RuntimeExecutionKey
	AllowedActions []TaskExplorerAction
}

type TaskExplorerExecution struct {
	Key                RuntimeExecutionKey
	SessionID          string
	ThreadID           string
	ParentSessionID    string
	ParentThreadID     string
	ParentAgentID      string
	ParentToolUseID    string
	TranscriptPath     string
	Name               string
	Task               string
	Description        string
	Activity           string
	Status             string
	DisplayMode        string
	LastToolName       string
	ToolUseCount       int
	TokenCount         int
	Phase              TaskExplorerExecutionPhase
	ObservationOrdinal uint64
	OrdinalPresent     bool
	ReplayOnly         bool
	Predispatch        bool
	Attention          []string
	AllowedActions     []TaskExplorerAction
}

type TaskExplorerLink struct {
	WorkExecutionLink
	State             TaskExplorerLinkState
	UnavailableReason string
	AllowedActions    []TaskExplorerAction
}

type TaskExplorerAttention struct {
	Category   string
	WorkItemID string
	AgentID    string
	Generation int64
	Sequence   uint64
	Reason     string
}

type TaskExplorerDiagnostic struct {
	Sequence uint64
	Kind     string
	ItemID   string
	Message  string
}

type TaskExplorerHiddenCounts struct {
	WorkItems                   map[string]int
	Executions                  map[string]int
	Links                       int
	Attention                   map[string]int
	WorkBoardOutsidePrimary     int
	RuntimeEventsDropped        uint64
	ExecutionGenerationsEvicted uint64
	HiddenLiveExecutions        uint64
}

type TaskExplorerSnapshot struct {
	Available         bool
	UnavailableReason string
	SessionID         string
	BoardID           string
	Revision          TaskExplorerRevision
	WorkItems         []TaskExplorerWorkItem
	Executions        []TaskExplorerExecution
	Links             []TaskExplorerLink
	Attention         []TaskExplorerAttention
	Diagnostics       []TaskExplorerDiagnostic
	Hidden            TaskExplorerHiddenCounts
}

type TaskExplorerWorkItemPage struct {
	Rows       []TaskExplorerWorkItem
	NextOffset int
	HasMore    bool
}

type taskExplorerSelection struct {
	snapshot TaskExplorerSnapshot
	runtime  RuntimeSnapshot
	catalog  RuntimeThreadCatalogSnapshot
}

func joinRuntimeDisplayModes(
	runtime RuntimeSnapshot,
	runner tools.RuntimeTaskSnapshot,
) RuntimeSnapshot {
	for _, candidate := range runner.Agents {
		agent, ok := runtime.Agents[candidate.ID]
		if !ok ||
			strings.TrimSpace(string(candidate.DisplayMode)) == "" ||
			agent.Generation <= 0 ||
			candidate.Generation <= 0 ||
			agent.Generation != candidate.Generation {
			continue
		}
		agent.DisplayMode = string(candidate.DisplayMode)
		runtime.Agents[candidate.ID] = agent
	}
	return runtime
}

// TaskExplorerSnapshot returns the canonical bounded read model.
func (e *QueryEngine) TaskExplorerSnapshot() TaskExplorerSnapshot {
	return e.taskExplorerSelection(nil).snapshot
}

func (e *QueryEngine) taskExplorerSelection(
	links []WorkExecutionLink,
) taskExplorerSelection {
	if e == nil {
		return taskExplorerSelection{
			snapshot: unavailableTaskExplorerSnapshot("engine_unavailable"),
			runtime: RuntimeSnapshot{
				Threads:   map[string]RuntimeThreadSnapshot{},
				Agents:    map[string]RuntimeAgentSnapshot{},
				Tasks:     map[string]RuntimeTaskSnapshot{},
				Worktrees: map[string]RuntimeWorktreeSnapshot{},
			},
			catalog: RuntimeThreadCatalogSnapshot{},
		}
	}
	e.mu.Lock()
	threadID := e.config.ThreadID
	adapter := e.logicalWorkAdapter
	agentRunner := e.agentRunner
	e.mu.Unlock()

	runtime := e.runtimeState.TaskExplorerSnapshot(threadID)
	catalog := e.runtimeState.ThreadCatalogSnapshot(threadID)
	runner := tools.RuntimeTaskSnapshotFrom(agentRunner, nil)
	runtime.Runtime = joinRuntimeDisplayModes(runtime.Runtime, runner)
	joinTaskExplorerDisplayModes(&runtime, runner)
	if adapter == nil {
		return taskExplorerSelection{
			snapshot: unavailableTaskExplorerSnapshot("workboard_unavailable"),
			runtime:  runtime.Runtime,
			catalog:  catalog,
		}
	}
	projection, durableLinks := adapter.ExplorerSnapshot()
	if links == nil {
		links = make([]WorkExecutionLink, len(durableLinks))
		for i, link := range durableLinks {
			links[i] = WorkExecutionLink{
				BoardID:    link.BoardID,
				WorkItemID: link.WorkItemID,
				AgentID:    link.AgentID,
				Generation: int64(link.Generation),
			}
		}
	}
	joinPredispatchTaskExplorerExecutions(&runtime, agentRunner, links)
	snapshot := selectTaskExplorerSnapshot(
		projection,
		runtime,
		links,
	)
	declareTaskExplorerActions(
		&snapshot,
		runtime.Runtime,
		catalog,
		agentRunner,
	)
	return taskExplorerSelection{
		snapshot: snapshot,
		runtime:  runtime.Runtime,
		catalog:  catalog,
	}
}

func declareTaskExplorerActions(
	snapshot *TaskExplorerSnapshot,
	runtime RuntimeSnapshot,
	catalog RuntimeThreadCatalogSnapshot,
	runner *tools.AgentRunner,
) {
	if snapshot == nil {
		return
	}
	if !snapshot.Available {
		for index := range snapshot.Executions {
			snapshot.Executions[index].AllowedActions = []TaskExplorerAction{
				TaskExplorerActionInspect,
			}
		}
		return
	}
	selection := taskExplorerSelection{
		snapshot: *snapshot,
		runtime:  runtime,
		catalog:  catalog,
	}
	for i := range snapshot.Executions {
		row := &snapshot.Executions[i]
		actions := []TaskExplorerAction{TaskExplorerActionInspect}
		if _, err := resolveTaskExplorerNavigationTarget(
			selection,
			row.Key.AgentID,
			row.Key.Generation,
		); err == nil {
			actions = append(actions, TaskExplorerActionSwitch)
		}
		settlement := runner.AgentExecutionSettlements([]tools.AgentExecutionKey{{
			AgentID:    row.Key.AgentID,
			Generation: row.Key.Generation,
		}})
		if len(settlement) != 1 {
			row.AllowedActions = actions
			continue
		}
		if row.Predispatch || row.ReplayOnly {
			row.AllowedActions = actions
			continue
		}
		switch settlement[0].State {
		case "live":
			if !row.ReplayOnly {
				actions = append(
					actions,
					TaskExplorerActionSend,
					TaskExplorerActionCancel,
				)
				if runner.HasPendingAgentMessageGeneration(
					row.Key.AgentID,
					row.Key.Generation,
				) {
					actions = append(
						actions,
						TaskExplorerActionCancelInput,
					)
				}
				if runner.IsAgentGenerationPaused(
					row.Key.AgentID,
					row.Key.Generation,
				) {
					actions = append(actions, TaskExplorerActionResume)
				} else {
					actions = append(actions, TaskExplorerActionPause)
				}
			}
		case "terminal_durable", "superseded":
			actions = append(actions, TaskExplorerActionContinue)
		}
		row.AllowedActions = actions
	}
	for i := range snapshot.Links {
		link := &snapshot.Links[i]
		link.AllowedActions = []TaskExplorerAction{TaskExplorerActionInspect}
		for _, execution := range snapshot.Executions {
			if execution.Key.AgentID == link.AgentID &&
				execution.Key.Generation == link.Generation {
				link.AllowedActions = append(
					[]TaskExplorerAction(nil),
					execution.AllowedActions...,
				)
				break
			}
		}
	}
}

// ResolveTaskExplorerNavigationTarget returns one exact current Ctrl+T target.
// It performs no transcript, model, tool, Agent, permission, or persistence
// work.
func (e *QueryEngine) ResolveTaskExplorerNavigationTarget(
	agentID string,
	generation int64,
) (TaskExplorerNavigationTarget, error) {
	return resolveTaskExplorerNavigationTarget(
		e.taskExplorerSelection(nil),
		agentID,
		generation,
	)
}

func resolveTaskExplorerNavigationTarget(
	selection taskExplorerSelection,
	agentID string,
	generation int64,
) (TaskExplorerNavigationTarget, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || generation <= 0 {
		return TaskExplorerNavigationTarget{}, fmt.Errorf(
			"%w: exact execution key is required",
			ErrTaskExplorerNavigationUnavailable,
		)
	}
	snapshot := selection.snapshot
	if !snapshot.Available {
		return TaskExplorerNavigationTarget{}, fmt.Errorf(
			"%w: %s",
			ErrTaskExplorerNavigationUnavailable,
			firstNonEmptyString(
				snapshot.UnavailableReason,
				"Task Explorer snapshot is unavailable",
			),
		)
	}
	if snapshot.Revision.Runtime != selection.runtime.Revision ||
		snapshot.Revision.Runtime != selection.catalog.Revision {
		return TaskExplorerNavigationTarget{}, fmt.Errorf(
			"%w: runtime and catalog revisions changed",
			ErrTaskExplorerNavigationStale,
		)
	}
	var row *TaskExplorerExecution
	for index := range snapshot.Executions {
		candidate := &snapshot.Executions[index]
		if candidate.Key.AgentID != agentID ||
			candidate.Key.Generation != generation {
			continue
		}
		if row != nil {
			return TaskExplorerNavigationTarget{}, fmt.Errorf(
				"%w: exact execution key is duplicated",
				ErrTaskExplorerNavigationUnavailable,
			)
		}
		row = candidate
	}
	if row == nil {
		return TaskExplorerNavigationTarget{}, fmt.Errorf(
			"%w: exact execution generation no longer resolves",
			ErrTaskExplorerNavigationStale,
		)
	}
	if row.Predispatch || row.ReplayOnly ||
		row.Phase == TaskExplorerExecutionReplayOnly {
		return TaskExplorerNavigationTarget{}, fmt.Errorf(
			"%w: exact execution is not a current live attachment",
			ErrTaskExplorerNavigationUnavailable,
		)
	}
	row.SessionID = strings.TrimSpace(row.SessionID)
	row.ThreadID = strings.TrimSpace(row.ThreadID)
	row.TranscriptPath = strings.TrimSpace(row.TranscriptPath)
	if row.SessionID == "" || row.ThreadID == "" ||
		row.TranscriptPath == "" {
		return TaskExplorerNavigationTarget{}, fmt.Errorf(
			"%w: exact execution has no readable child transcript",
			ErrTaskExplorerNavigationUnavailable,
		)
	}
	current, ok := selection.runtime.Agents[agentID]
	if !ok || current.Generation != generation ||
		strings.TrimSpace(current.AgentID) != agentID ||
		strings.TrimSpace(current.SessionID) != row.SessionID ||
		strings.TrimSpace(current.ThreadID) != row.ThreadID {
		return TaskExplorerNavigationTarget{}, fmt.Errorf(
			"%w: current Agent generation or lineage changed",
			ErrTaskExplorerNavigationStale,
		)
	}
	currentTranscriptPath := strings.TrimSpace(current.TranscriptPath)
	if currentTranscriptPath == "" {
		return TaskExplorerNavigationTarget{}, fmt.Errorf(
			"%w: current Agent transcript is unavailable",
			ErrTaskExplorerNavigationUnavailable,
		)
	}
	var entry *RuntimeThreadCatalogEntry
	for index := range selection.catalog.Threads {
		candidate := &selection.catalog.Threads[index]
		if strings.TrimSpace(candidate.ThreadID) != row.ThreadID {
			continue
		}
		if entry != nil {
			return TaskExplorerNavigationTarget{}, fmt.Errorf(
				"%w: child thread catalog identity is duplicated",
				ErrTaskExplorerNavigationUnavailable,
			)
		}
		entry = candidate
	}
	if entry == nil {
		return TaskExplorerNavigationTarget{}, fmt.Errorf(
			"%w: child thread is absent from the current catalog",
			ErrTaskExplorerNavigationUnavailable,
		)
	}
	if strings.TrimSpace(entry.SessionID) != row.SessionID ||
		strings.TrimSpace(entry.AgentID) != agentID ||
		strings.TrimSpace(entry.TranscriptPath) != currentTranscriptPath ||
		entry.Mode != ThreadModeLiveAttach {
		return TaskExplorerNavigationTarget{}, fmt.Errorf(
			"%w: child thread catalog facts do not match the exact execution",
			ErrTaskExplorerNavigationUnavailable,
		)
	}
	return TaskExplorerNavigationTarget{
		SessionID:  row.SessionID,
		ThreadID:   row.ThreadID,
		AgentID:    agentID,
		Generation: generation,
		Mode:       entry.Mode,
	}, nil
}

func taskExplorerNavigationConflict(err error) (string, string) {
	if errors.Is(err, ErrTaskExplorerNavigationStale) {
		return "navigation_stale", err.Error()
	}
	return "navigation_unavailable", err.Error()
}

// ApplyTaskExplorerAction re-resolves one exact displayed generation before
// every side effect. Board revision and generation authorize the request;
// runtime revision is returned only as a correlation/refresh hint.
func (e *QueryEngine) ApplyTaskExplorerAction(
	request TaskExplorerActionRequest,
) TaskExplorerActionResult {
	selection := e.taskExplorerSelection(nil)
	snapshot := selection.snapshot
	result := TaskExplorerActionResult{
		RequestID:       request.RequestID,
		BoardID:         snapshot.BoardID,
		BoardRevision:   snapshot.Revision.Board,
		RuntimeRevision: snapshot.Revision.Runtime,
		AgentID:         request.AgentID,
		Generation:      request.Generation,
		MessageID:       request.MessageID,
		Action:          request.Action,
	}
	conflict := func(code, message string) TaskExplorerActionResult {
		result.Outcome = "conflict"
		result.Conflict = code
		result.Message = message
		return result
	}
	if e == nil || e.sessionDeletionGate == nil ||
		!e.sessionDeletionGate.open() {
		return conflict(
			"session_deleting",
			"active Session deletion blocks explorer actions",
		)
	}
	if strings.TrimSpace(request.RequestID) == "" ||
		strings.TrimSpace(request.AgentID) == "" ||
		request.Generation <= 0 {
		return conflict(
			"invalid_request",
			"request ID and exact execution key are required",
		)
	}
	if !snapshot.Available {
		return conflict(
			"explorer_unavailable",
			snapshot.UnavailableReason,
		)
	}
	if request.BoardID != snapshot.BoardID ||
		request.BoardRevision != snapshot.Revision.Board {
		return conflict(
			"stale_board",
			"WorkBoard identity or revision changed",
		)
	}
	var row *TaskExplorerExecution
	for i := range snapshot.Executions {
		candidate := &snapshot.Executions[i]
		if candidate.Key.AgentID == request.AgentID &&
			candidate.Key.Generation == request.Generation {
			row = candidate
			break
		}
	}
	if row == nil {
		if request.Action == TaskExplorerActionCancelInput {
			result.Outcome = "stale_generation"
			return result
		}
		return conflict(
			"missing_execution",
			"exact execution generation no longer resolves",
		)
	}
	if len(request.Payload) > maxTaskExplorerActionPayloadBytes {
		return conflict(
			"payload_too_large",
			"action payload exceeds 64 KiB",
		)
	}
	if len(request.MessageID) > maxTaskExplorerMessageIDBytes {
		return conflict(
			"message_id_too_large",
			"MessageID exceeds 512 bytes",
		)
	}
	var navigationTarget *TaskExplorerNavigationTarget
	if request.Action == TaskExplorerActionSwitch {
		target, err := resolveTaskExplorerNavigationTarget(
			selection,
			request.AgentID,
			request.Generation,
		)
		if err != nil {
			code, message := taskExplorerNavigationConflict(err)
			return conflict(code, message)
		}
		navigationTarget = &target
	}
	if request.Action == TaskExplorerActionCancelInput &&
		!row.ReplayOnly &&
		!row.Predispatch {
		// A displayed cancellation can legitimately race a child drain. Keep
		// exact-generation cancellation admissible long enough for the runner
		// to return input_not_pending instead of degrading into a generic
		// unsupported-action conflict.
	} else if !taskExplorerActionAllowed(
		row.AllowedActions,
		request.Action,
	) {
		return conflict(
			"unsupported_action",
			fmt.Sprintf(
				"action %q is unavailable for exact execution generation",
				request.Action,
			),
		)
	}
	result.SessionID = row.SessionID
	result.ThreadID = row.ThreadID
	switch request.Action {
	case TaskExplorerActionInspect:
		result.Outcome = "inspected"
	case TaskExplorerActionSwitch:
		result.NavigationTarget = navigationTarget
		result.Outcome = "switched"
	case TaskExplorerActionSend:
		if strings.TrimSpace(request.Payload) == "" {
			return conflict("invalid_payload", "send payload is empty")
		}
		_, err := e.agentRunner.QueueAgentMessageGeneration(
			request.AgentID,
			request.Generation,
			tools.MessagePayload{
				Content: request.Payload,
				Metadata: map[string]any{
					"command_uuid": request.RequestID,
				},
			},
		)
		if err != nil {
			return conflict("action_conflict", err.Error())
		}
		result.MessageID = request.RequestID
		result.Outcome = "sent"
	case TaskExplorerActionCancelInput:
		if strings.TrimSpace(request.MessageID) == "" {
			return conflict(
				"invalid_request",
				"cancel_input requires MessageID",
			)
		}
		outcome, err := e.agentRunner.CancelAgentMessageGeneration(
			request.AgentID,
			request.Generation,
			request.MessageID,
		)
		if err != nil {
			return conflict("action_conflict", err.Error())
		}
		result.Outcome = outcome
	case TaskExplorerActionPause:
		if err := e.agentRunner.PauseAgentGeneration(
			request.AgentID,
			request.Generation,
		); err != nil {
			return conflict("action_conflict", err.Error())
		}
		result.Outcome = "paused"
	case TaskExplorerActionResume:
		if err := e.agentRunner.ResumeAgentGeneration(
			request.AgentID,
			request.Generation,
		); err != nil {
			return conflict("action_conflict", err.Error())
		}
		result.Outcome = "resumed"
	case TaskExplorerActionCancel:
		if err := e.agentRunner.AbortAgentGeneration(
			request.AgentID,
			request.Generation,
		); err != nil {
			return conflict("action_conflict", err.Error())
		}
		result.Outcome = "cancel_requested"
	case TaskExplorerActionContinue:
		if strings.TrimSpace(request.Payload) == "" {
			return conflict("invalid_payload", "continue payload is empty")
		}
		_, disposition, err := e.agentRunner.ContinueAgentGeneration(
			request.AgentID,
			request.Generation,
			tools.MessagePayload{Content: request.Payload},
		)
		if err != nil {
			return conflict("action_conflict", err.Error())
		}
		if disposition != "resumed" {
			return conflict(
				"action_conflict",
				"exact continuation did not reserve a new generation",
			)
		}
		result.Outcome = "continued"
		result.NewGeneration = request.Generation + 1
	default:
		return conflict("unsupported_action", "unknown explorer action")
	}
	return result
}

func taskExplorerActionAllowed(
	allowed []TaskExplorerAction,
	action TaskExplorerAction,
) bool {
	for _, candidate := range allowed {
		if candidate == action {
			return true
		}
	}
	return false
}

// TaskExplorerArchivePage exposes a bounded terminal WorkItem page without
// changing the primary 128-row selector budget.
func (e *QueryEngine) TaskExplorerArchivePage(
	offset int,
	limit int,
) TaskExplorerWorkItemPage {
	if e == nil {
		return TaskExplorerWorkItemPage{}
	}
	e.mu.Lock()
	adapter := e.logicalWorkAdapter
	e.mu.Unlock()
	if adapter == nil {
		return TaskExplorerWorkItemPage{}
	}
	projection := adapter.ProjectionSnapshot()
	if !projection.Available {
		return TaskExplorerWorkItemPage{}
	}
	return taskExplorerArchivePage(projection.Record, offset, limit)
}

func taskExplorerArchivePage(
	record workboard.AuthorityRecord,
	offset int,
	limit int,
) TaskExplorerWorkItemPage {
	rows, _ := taskExplorerWorkItemRows(record, nil, nil)
	terminal := rows[:0]
	for _, row := range rows {
		if taskExplorerWorkItemTerminal(row.Status) {
			terminal = append(terminal, row)
		}
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(terminal) {
		return TaskExplorerWorkItemPage{}
	}
	if limit <= 0 || limit > maxTaskExplorerPage {
		limit = maxTaskExplorerPage
	}
	end := min(offset+limit, len(terminal))
	return TaskExplorerWorkItemPage{
		Rows:       cloneTaskExplorerWorkItems(terminal[offset:end]),
		NextOffset: end,
		HasMore:    end < len(terminal),
	}
}

func selectTaskExplorerSnapshot(
	projection workboard.ProjectionSnapshot,
	runtime RuntimeTaskExplorerSnapshot,
	inputLinks []WorkExecutionLink,
) TaskExplorerSnapshot {
	if !projection.Available {
		return unavailableTaskExplorerRuntimeSnapshot(
			"workboard_not_authoritative",
			runtime,
		)
	}
	out := TaskExplorerSnapshot{
		Available: true,
		SessionID: projection.SessionID,
		BoardID:   projection.BoardID,
		Revision: TaskExplorerRevision{
			Board:   projection.Revision,
			Runtime: runtime.Runtime.Revision,
		},
		Hidden: newTaskExplorerHiddenCounts(),
	}

	executions, executionByKey, executionAttention := taskExplorerExecutions(runtime)
	links, validLinks, linkAttention := taskExplorerLinks(
		projection.BoardID,
		projection.Record.Board.Items,
		executionByKey,
		inputLinks,
	)
	workItems, workAttention := taskExplorerWorkItemRows(
		projection.Record,
		validLinks,
		executionByKey,
	)
	diagnostics, diagnosticAttention := taskExplorerDiagnostics(projection)

	allAttention := make(
		[]TaskExplorerAttention,
		0,
		len(workAttention)+len(executionAttention)+len(linkAttention)+len(diagnosticAttention)+2,
	)
	allAttention = append(allAttention, diagnosticAttention...)
	allAttention = append(allAttention, workAttention...)
	allAttention = append(allAttention, executionAttention...)
	allAttention = append(allAttention, linkAttention...)
	if runtime.HiddenLiveExecutions > 0 {
		allAttention = append(allAttention, TaskExplorerAttention{
			Category: "live_execution_overflow",
			Reason:   "live execution rows exceed the primary explorer bound",
		})
	}
	if runtime.DroppedEvents > 0 || runtime.EvictedExecutionGenerations > 0 {
		allAttention = append(allAttention, TaskExplorerAttention{
			Category: "runtime_truncated",
			Reason:   "process-local runtime observations were bounded",
		})
	}
	sortTaskExplorerAttention(allAttention)

	out.WorkItems, out.Hidden.WorkItems = boundTaskExplorerWorkItems(workItems)
	out.Executions, out.Hidden.Executions = boundTaskExplorerExecutions(executions)
	out.Links, out.Hidden.Links = boundTaskExplorerLinks(links)
	out.Attention, out.Hidden.Attention = boundTaskExplorerAttention(allAttention)
	out.Diagnostics = diagnostics
	out.Hidden.WorkBoardOutsidePrimary = max(
		0,
		len(projection.Record.Board.Items)-len(out.WorkItems),
	)
	out.Hidden.RuntimeEventsDropped = runtime.DroppedEvents
	out.Hidden.ExecutionGenerationsEvicted = runtime.EvictedExecutionGenerations
	out.Hidden.HiddenLiveExecutions = runtime.HiddenLiveExecutions
	return out
}

func unavailableTaskExplorerSnapshot(reason string) TaskExplorerSnapshot {
	return TaskExplorerSnapshot{
		UnavailableReason: reason,
		Hidden:            newTaskExplorerHiddenCounts(),
	}
}

func unavailableTaskExplorerRuntimeSnapshot(
	reason string,
	runtime RuntimeTaskExplorerSnapshot,
) TaskExplorerSnapshot {
	out := unavailableTaskExplorerSnapshot(reason)
	out.Revision.Runtime = runtime.Runtime.Revision
	executions, _, attention := taskExplorerExecutions(runtime)
	if runtime.HiddenLiveExecutions > 0 {
		attention = append(attention, TaskExplorerAttention{
			Category: "live_execution_overflow",
			Reason:   "live execution rows exceed the primary explorer bound",
		})
	}
	if runtime.DroppedEvents > 0 || runtime.EvictedExecutionGenerations > 0 {
		attention = append(attention, TaskExplorerAttention{
			Category: "runtime_truncated",
			Reason:   "process-local runtime observations were bounded",
		})
	}
	sortTaskExplorerAttention(attention)
	out.Executions, out.Hidden.Executions = boundTaskExplorerExecutions(executions)
	out.Attention, out.Hidden.Attention = boundTaskExplorerAttention(attention)
	out.Hidden.RuntimeEventsDropped = runtime.DroppedEvents
	out.Hidden.ExecutionGenerationsEvicted = runtime.EvictedExecutionGenerations
	out.Hidden.HiddenLiveExecutions = runtime.HiddenLiveExecutions
	return out
}

func newTaskExplorerHiddenCounts() TaskExplorerHiddenCounts {
	return TaskExplorerHiddenCounts{
		WorkItems:  map[string]int{},
		Executions: map[string]int{},
		Attention:  map[string]int{},
	}
}

func taskExplorerExecutions(
	runtime RuntimeTaskExplorerSnapshot,
) (
	[]TaskExplorerExecution,
	map[RuntimeExecutionKey]TaskExplorerExecution,
	[]TaskExplorerAttention,
) {
	rows := make([]TaskExplorerExecution, 0, len(runtime.Executions))
	byKey := make(map[RuntimeExecutionKey]TaskExplorerExecution, len(runtime.Executions))
	attention := make([]TaskExplorerAttention, 0)
	for key, observation := range runtime.Executions {
		agent := observation.Agent
		phase := taskExplorerExecutionPhase(agent.Status, observation.ReplayOnly)
		row := TaskExplorerExecution{
			Key:                key,
			SessionID:          agent.SessionID,
			ThreadID:           agent.ThreadID,
			ParentSessionID:    agent.ParentSessionID,
			ParentThreadID:     agent.ParentThreadID,
			ParentAgentID:      agent.ParentAgentID,
			ParentToolUseID:    agent.ParentToolUseID,
			TranscriptPath:     truncateTaskExplorerText(agent.TranscriptPath),
			Name:               truncateTaskExplorerText(agent.Name),
			Task:               truncateTaskExplorerText(agent.Task),
			Description:        truncateTaskExplorerText(agent.Description),
			Activity:           truncateTaskExplorerText(agent.Progress.Summary),
			Status:             truncateTaskExplorerText(agent.Status),
			DisplayMode:        truncateTaskExplorerText(agent.DisplayMode),
			LastToolName:       truncateTaskExplorerText(agent.Progress.LastToolName),
			ToolUseCount:       agent.Progress.ToolUses,
			TokenCount:         agent.Progress.TotalTokens,
			Phase:              phase,
			ObservationOrdinal: observation.ObservationOrdinal,
			OrdinalPresent:     observation.OrdinalPresent,
			ReplayOnly:         observation.ReplayOnly,
			Predispatch:        observation.Predispatch,
			AllowedActions:     []TaskExplorerAction{TaskExplorerActionInspect},
		}
		switch taskExplorerExecutionTerminalAttention(agent.Status) {
		case "execution_failed":
			row.Attention = []string{"execution_failed"}
		case "execution_cancelled":
			row.Attention = []string{"execution_cancelled"}
		}
		for _, category := range row.Attention {
			attention = append(attention, TaskExplorerAttention{
				Category:   category,
				AgentID:    key.AgentID,
				Generation: key.Generation,
				Reason:     row.Status,
			})
		}
		rows = append(rows, row)
		byKey[key] = row
	}
	sort.Slice(rows, func(i, j int) bool {
		return taskExplorerExecutionLess(rows[i], rows[j])
	})
	return rows, byKey, attention
}

func taskExplorerLinks(
	boardID string,
	items []workboard.WorkItem,
	executions map[RuntimeExecutionKey]TaskExplorerExecution,
	input []WorkExecutionLink,
) (
	[]TaskExplorerLink,
	map[string][]RuntimeExecutionKey,
	[]TaskExplorerAttention,
) {
	itemIDs := make(map[string]struct{}, len(items))
	for _, item := range items {
		itemIDs[item.ID] = struct{}{}
	}
	links := append([]WorkExecutionLink(nil), input...)
	sort.Slice(links, func(i, j int) bool {
		if links[i].BoardID != links[j].BoardID {
			return links[i].BoardID < links[j].BoardID
		}
		if links[i].WorkItemID != links[j].WorkItemID {
			return links[i].WorkItemID < links[j].WorkItemID
		}
		if links[i].AgentID != links[j].AgentID {
			return links[i].AgentID < links[j].AgentID
		}
		return links[i].Generation < links[j].Generation
	})
	rows := make([]TaskExplorerLink, 0, len(links))
	valid := make(map[string][]RuntimeExecutionKey)
	attention := make([]TaskExplorerAttention, 0)
	seen := make(map[WorkExecutionLink]struct{}, len(links))
	for _, link := range links {
		row := TaskExplorerLink{WorkExecutionLink: link}
		key := RuntimeExecutionKey{
			AgentID: link.AgentID, Generation: link.Generation,
		}
		_, itemExists := itemIDs[link.WorkItemID]
		_, executionExists := executions[key]
		_, duplicate := seen[link]
		seen[link] = struct{}{}
		switch {
		case duplicate || link.BoardID != boardID || !itemExists:
			row.State = TaskExplorerLinkMissing
			row.UnavailableReason = "missing_link_target"
		case !executionExists && taskExplorerHasOtherGeneration(executions, key):
			row.State = TaskExplorerLinkStale
			row.UnavailableReason = "stale_execution_link"
		case !executionExists:
			row.State = TaskExplorerLinkMissing
			row.UnavailableReason = "missing_link_target"
		default:
			row.State = TaskExplorerLinkValid
			row.AllowedActions = []TaskExplorerAction{TaskExplorerActionInspect}
			valid[link.WorkItemID] = append(valid[link.WorkItemID], key)
		}
		if row.State != TaskExplorerLinkValid {
			attention = append(attention, TaskExplorerAttention{
				Category:   row.UnavailableReason,
				WorkItemID: link.WorkItemID,
				AgentID:    link.AgentID,
				Generation: link.Generation,
				Reason:     row.UnavailableReason,
			})
		}
		rows = append(rows, row)
	}
	return rows, valid, attention
}

func taskExplorerWorkItemRows(
	record workboard.AuthorityRecord,
	validLinks map[string][]RuntimeExecutionKey,
	executions map[RuntimeExecutionKey]TaskExplorerExecution,
) ([]TaskExplorerWorkItem, []TaskExplorerAttention) {
	unresolved := make(map[string]bool, len(record.Compatibility.Tasks))
	for _, task := range record.Compatibility.Tasks {
		unresolved[task.ID] = len(task.UnresolvedBlocks) > 0 ||
			len(task.UnresolvedBlockedBy) > 0
	}
	rows := make([]TaskExplorerWorkItem, 0, len(record.Board.Items))
	attention := make([]TaskExplorerAttention, 0)
	for _, item := range record.Board.Items {
		row := TaskExplorerWorkItem{
			BoardID:        record.BoardID,
			WorkItemID:     item.ID,
			Revision:       item.Revision,
			Order:          item.Order,
			Status:         string(item.Status),
			Title:          truncateTaskExplorerText(item.Title),
			Description:    truncateTaskExplorerText(item.Description),
			ActiveForm:     truncateTaskExplorerText(item.ActiveForm),
			Owner:          truncateTaskExplorerText(item.Owner),
			ResultSummary:  truncateTaskExplorerText(item.ResultSummary),
			TerminalReason: truncateTaskExplorerText(item.TerminalReason),
			Blocks:         append([]string(nil), item.Blocks...),
			BlockedBy:      append([]string(nil), item.BlockedBy...),
			ExecutionKeys:  append([]RuntimeExecutionKey(nil), validLinks[item.ID]...),
			AllowedActions: []TaskExplorerAction{TaskExplorerActionInspect},
		}
		if len(item.BlockedBy) > 0 {
			row.Attention = append(row.Attention, "blocked_dependency")
		}
		if item.Source.Kind == "task" && unresolved[item.Source.LegacyID] {
			row.Attention = append(row.Attention, "legacy_unresolved_dependency")
		}
		for _, key := range row.ExecutionKeys {
			if execution, ok := executions[key]; ok &&
				taskExplorerExecutionLive(execution.Phase) {
				row.LinkedLive = true
			}
		}
		for _, category := range row.Attention {
			attention = append(attention, TaskExplorerAttention{
				Category:   category,
				WorkItemID: item.ID,
				Reason:     category,
			})
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		return taskExplorerWorkItemLess(rows[i], rows[j])
	})
	return rows, attention
}

func taskExplorerDiagnostics(
	projection workboard.ProjectionSnapshot,
) ([]TaskExplorerDiagnostic, []TaskExplorerAttention) {
	out := make(
		[]TaskExplorerDiagnostic,
		0,
		len(projection.Record.Diagnostics)+len(projection.Diagnostics),
	)
	attention := make([]TaskExplorerAttention, 0, cap(out))
	appendDiagnostic := func(sequence uint64, kind, itemID, message string) {
		row := TaskExplorerDiagnostic{
			Sequence: sequence,
			Kind:     truncateTaskExplorerText(kind),
			ItemID:   truncateTaskExplorerText(itemID),
			Message:  truncateTaskExplorerText(message),
		}
		out = append(out, row)
		attention = append(attention, TaskExplorerAttention{
			Category:   "workboard_diagnostic",
			WorkItemID: row.ItemID,
			Sequence:   row.Sequence,
			Reason:     row.Message,
		})
	}
	for _, diagnostic := range projection.Record.Diagnostics {
		appendDiagnostic(
			diagnostic.Sequence,
			diagnostic.Kind,
			diagnostic.ItemID,
			diagnostic.Message,
		)
	}
	for _, diagnostic := range projection.Diagnostics {
		appendDiagnostic(
			diagnostic.Sequence,
			diagnostic.Kind,
			"",
			diagnostic.Message,
		)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sequence != out[j].Sequence {
			return out[i].Sequence < out[j].Sequence
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].ItemID != out[j].ItemID {
			return out[i].ItemID < out[j].ItemID
		}
		return out[i].Message < out[j].Message
	})
	if overflow := len(out) - maxTaskExplorerAttention; overflow > 0 {
		out = append([]TaskExplorerDiagnostic(nil), out[overflow:]...)
	}
	return out, attention
}

func boundTaskExplorerWorkItems(
	rows []TaskExplorerWorkItem,
) ([]TaskExplorerWorkItem, map[string]int) {
	hidden := map[string]int{}
	if len(rows) <= maxTaskExplorerWorkItems {
		return cloneTaskExplorerWorkItems(rows), hidden
	}
	for _, row := range rows[maxTaskExplorerWorkItems:] {
		hidden[row.Status]++
	}
	return cloneTaskExplorerWorkItems(rows[:maxTaskExplorerWorkItems]), hidden
}

func boundTaskExplorerExecutions(
	rows []TaskExplorerExecution,
) ([]TaskExplorerExecution, map[string]int) {
	hidden := map[string]int{}
	if len(rows) <= maxTaskExplorerExecutions {
		return cloneTaskExplorerExecutions(rows), hidden
	}
	for _, row := range rows[maxTaskExplorerExecutions:] {
		hidden[string(row.Phase)]++
	}
	return cloneTaskExplorerExecutions(rows[:maxTaskExplorerExecutions]), hidden
}

func boundTaskExplorerLinks(
	rows []TaskExplorerLink,
) ([]TaskExplorerLink, int) {
	if len(rows) <= maxTaskExplorerLinks {
		return cloneTaskExplorerLinks(rows), 0
	}
	return cloneTaskExplorerLinks(rows[:maxTaskExplorerLinks]),
		len(rows) - maxTaskExplorerLinks
}

func boundTaskExplorerAttention(
	rows []TaskExplorerAttention,
) ([]TaskExplorerAttention, map[string]int) {
	hidden := map[string]int{}
	if len(rows) <= maxTaskExplorerAttention {
		return append([]TaskExplorerAttention(nil), rows...), hidden
	}
	for _, row := range rows[maxTaskExplorerAttention:] {
		hidden[row.Category]++
	}
	return append([]TaskExplorerAttention(nil), rows[:maxTaskExplorerAttention]...), hidden
}

func sortTaskExplorerAttention(rows []TaskExplorerAttention) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Category != rows[j].Category {
			return rows[i].Category < rows[j].Category
		}
		if rows[i].WorkItemID != rows[j].WorkItemID {
			return rows[i].WorkItemID < rows[j].WorkItemID
		}
		if rows[i].AgentID != rows[j].AgentID {
			return rows[i].AgentID < rows[j].AgentID
		}
		if rows[i].Generation != rows[j].Generation {
			return rows[i].Generation < rows[j].Generation
		}
		if rows[i].Sequence != rows[j].Sequence {
			return rows[i].Sequence < rows[j].Sequence
		}
		return rows[i].Reason < rows[j].Reason
	})
}

func taskExplorerWorkItemLess(
	left TaskExplorerWorkItem,
	right TaskExplorerWorkItem,
) bool {
	if (len(left.Attention) > 0) != (len(right.Attention) > 0) {
		return len(left.Attention) > 0
	}
	if left.LinkedLive != right.LinkedLive {
		return left.LinkedLive
	}
	leftBlocked := len(left.BlockedBy) > 0
	rightBlocked := len(right.BlockedBy) > 0
	if leftBlocked != rightBlocked {
		return leftBlocked
	}
	leftRank := taskExplorerWorkItemStatusRank(left.Status)
	rightRank := taskExplorerWorkItemStatusRank(right.Status)
	if leftRank != rightRank {
		return leftRank < rightRank
	}
	if taskExplorerWorkItemTerminal(left.Status) &&
		left.Revision != right.Revision {
		return left.Revision > right.Revision
	}
	if left.Order != right.Order {
		return left.Order < right.Order
	}
	return left.WorkItemID < right.WorkItemID
}

func taskExplorerExecutionLess(
	left TaskExplorerExecution,
	right TaskExplorerExecution,
) bool {
	leftAttention := len(left.Attention) == 0
	rightAttention := len(right.Attention) == 0
	if leftAttention != rightAttention {
		return !leftAttention
	}
	leftRank := taskExplorerExecutionPhaseRank(left.Phase)
	rightRank := taskExplorerExecutionPhaseRank(right.Phase)
	if leftRank != rightRank {
		return leftRank < rightRank
	}
	if left.OrdinalPresent != right.OrdinalPresent {
		return left.OrdinalPresent
	}
	if left.ObservationOrdinal != right.ObservationOrdinal {
		return left.ObservationOrdinal > right.ObservationOrdinal
	}
	if left.Key.AgentID != right.Key.AgentID {
		return left.Key.AgentID < right.Key.AgentID
	}
	return left.Key.Generation < right.Key.Generation
}

func taskExplorerExecutionPhase(
	status string,
	replayOnly bool,
) TaskExplorerExecutionPhase {
	normalized := strings.ToLower(strings.TrimSpace(status))
	if replayOnly {
		return TaskExplorerExecutionReplayOnly
	}
	switch normalized {
	case string(RuntimeThreadFailed):
		return TaskExplorerExecutionFailed
	case "cancelled", "canceled", "killed", "stopped",
		string(RuntimeThreadAborted):
		return TaskExplorerExecutionCancelled
	}
	switch normalized {
	case "running", "started", "in_progress":
		return TaskExplorerExecutionRunning
	case string(RuntimeThreadWaitingInput), "waiting":
		return TaskExplorerExecutionWaitingInput
	case string(RuntimeThreadPaused):
		return TaskExplorerExecutionPaused
	case "completed", "succeeded", "success":
		return TaskExplorerExecutionCompleted
	default:
		return TaskExplorerExecutionUnknown
	}
}

func taskExplorerExecutionTerminalAttention(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case string(RuntimeThreadFailed):
		return "execution_failed"
	case "cancelled", "canceled", "killed", "stopped",
		string(RuntimeThreadAborted):
		return "execution_cancelled"
	default:
		return ""
	}
}

func taskExplorerExecutionPhaseRank(phase TaskExplorerExecutionPhase) int {
	switch phase {
	case TaskExplorerExecutionRunning:
		return 0
	case TaskExplorerExecutionWaitingInput:
		return 1
	case TaskExplorerExecutionPaused:
		return 2
	case TaskExplorerExecutionReplayOnly:
		return 3
	case TaskExplorerExecutionCompleted:
		return 4
	case TaskExplorerExecutionFailed:
		return 5
	case TaskExplorerExecutionCancelled:
		return 6
	default:
		return 7
	}
}

func taskExplorerWorkItemStatusRank(status string) int {
	switch status {
	case string(workboard.StatusInProgress):
		return 0
	case string(workboard.StatusPending):
		return 1
	case string(workboard.StatusCompleted),
		string(workboard.StatusFailed),
		string(workboard.StatusCancelled):
		return 2
	default:
		return 3
	}
}

func taskExplorerWorkItemTerminal(status string) bool {
	switch status {
	case string(workboard.StatusCompleted),
		string(workboard.StatusFailed),
		string(workboard.StatusCancelled):
		return true
	default:
		return false
	}
}

func taskExplorerExecutionLive(phase TaskExplorerExecutionPhase) bool {
	switch phase {
	case TaskExplorerExecutionRunning,
		TaskExplorerExecutionWaitingInput,
		TaskExplorerExecutionPaused:
		return true
	default:
		return false
	}
}

func taskExplorerHasOtherGeneration(
	executions map[RuntimeExecutionKey]TaskExplorerExecution,
	key RuntimeExecutionKey,
) bool {
	for candidate := range executions {
		if candidate.AgentID == key.AgentID &&
			candidate.Generation != key.Generation {
			return true
		}
	}
	return false
}

func joinTaskExplorerDisplayModes(
	runtime *RuntimeTaskExplorerSnapshot,
	runner tools.RuntimeTaskSnapshot,
) {
	if runtime == nil {
		return
	}
	for _, candidate := range runner.Agents {
		key := RuntimeExecutionKey{
			AgentID: candidate.ID, Generation: candidate.Generation,
		}
		execution, ok := runtime.Executions[key]
		if !ok || strings.TrimSpace(string(candidate.DisplayMode)) == "" {
			continue
		}
		execution.Agent.DisplayMode = string(candidate.DisplayMode)
		runtime.Executions[key] = execution
	}
}

func joinPredispatchTaskExplorerExecutions(
	runtime *RuntimeTaskExplorerSnapshot,
	runner *tools.AgentRunner,
	links []WorkExecutionLink,
) {
	if runtime == nil || runner == nil {
		return
	}
	if runtime.Executions == nil {
		runtime.Executions = make(
			map[RuntimeExecutionKey]RuntimeExecutionSnapshot,
		)
	}
	for _, link := range links {
		key := RuntimeExecutionKey{
			AgentID: link.AgentID, Generation: link.Generation,
		}
		if _, exists := runtime.Executions[key]; exists {
			continue
		}
		durable, err := runner.LoadPersistedAgentSnapshot(link.AgentID)
		if err != nil ||
			durable.ExecutionGeneration() != link.Generation ||
			!durable.PredispatchFailure() {
			continue
		}
		runtime.Executions[key] = RuntimeExecutionSnapshot{
			Key:         key,
			Agent:       runtimeAgentSnapshotFromRunner(durable),
			Predispatch: true,
		}
	}
}

func truncateTaskExplorerText(value string) string {
	runes := []rune(value)
	if len(runes) <= maxTaskExplorerTextRunes {
		return value
	}
	return string(runes[:maxTaskExplorerTextRunes])
}

func cloneTaskExplorerWorkItems(
	rows []TaskExplorerWorkItem,
) []TaskExplorerWorkItem {
	out := make([]TaskExplorerWorkItem, len(rows))
	for index, row := range rows {
		row.Blocks = append([]string(nil), row.Blocks...)
		row.BlockedBy = append([]string(nil), row.BlockedBy...)
		row.Attention = append([]string(nil), row.Attention...)
		row.ExecutionKeys = append([]RuntimeExecutionKey(nil), row.ExecutionKeys...)
		row.AllowedActions = append([]TaskExplorerAction(nil), row.AllowedActions...)
		out[index] = row
	}
	return out
}

func cloneTaskExplorerExecutions(
	rows []TaskExplorerExecution,
) []TaskExplorerExecution {
	out := make([]TaskExplorerExecution, len(rows))
	for index, row := range rows {
		row.Attention = append([]string(nil), row.Attention...)
		row.AllowedActions = append([]TaskExplorerAction(nil), row.AllowedActions...)
		out[index] = row
	}
	return out
}

func cloneTaskExplorerLinks(rows []TaskExplorerLink) []TaskExplorerLink {
	out := make([]TaskExplorerLink, len(rows))
	for index, row := range rows {
		row.AllowedActions = append([]TaskExplorerAction(nil), row.AllowedActions...)
		out[index] = row
	}
	return out
}
