package workboard

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/abietic/yhc/tools"
)

type AdapterConfig struct {
	SessionID   string
	Dir         string
	LeaderScope tools.TodoScope
	LeaderTodos []tools.TodoItem
	Clock       func() time.Time
	NewBoardID  func() string
	FileFailure FailureHook
	Failure     StoreFailureHook
	// ProjectionSwapFailure is a test seam for the post-commit in-memory swap.
	ProjectionSwapFailure func() error
	// DirectoryPreparationAfterInspect is a test seam for the legacy root race.
	DirectoryPreparationAfterInspect func()
	SettlementSnapshot               func([]ExecutionSettlementKey) ([]ExecutionSettlement, error)
	MutationAdmission                func() bool
}

// ExecutionSettlementKey identifies the durable execution attempt that must
// settle before a linked WorkItem may become terminal.
type (
	ExecutionSettlementKey struct {
		AgentID    string
		Generation uint64
	}
	ExecutionSettlement struct {
		Key                                                ExecutionSettlementKey
		Settled, Reserved, Live, CancelPending, Unresolved bool
	}
)

type AdmitExecutionLinkRequest struct {
	BoardID, WorkItemID, AgentID                                    string
	BoardRevision, WorkItemRevision, Generation                     uint64
	ParentSessionID, ParentThreadID, ParentAgentID, ParentToolUseID string
	AdmittedAt                                                      time.Time
}

type LogicalWorkAdapter struct {
	mu sync.Mutex

	config      AdapterConfig
	store       *Store
	mode        AuthorityMode
	record      AuthorityRecord
	backup      LegacyBackup
	events      []tools.TaskLifecycleEvent
	mutationErr error
	projection  *ProjectionReducer
}

// CommittedProjectionUncertainError means the authority record committed but
// the process-local projection could not be installed. Restart reconstructs it
// from durable authority; retrying this adapter is unsafe.
type CommittedProjectionUncertainError struct {
	Cause error
}

func (e *CommittedProjectionUncertainError) Error() string {
	return fmt.Sprintf("workboard authority: %s: %v", e.Code(), e.Cause)
}

func (e *CommittedProjectionUncertainError) Unwrap() error { return e.Cause }

func (e *CommittedProjectionUncertainError) Code() string {
	return "committed_projection_uncertain"
}

func (e *CommittedProjectionUncertainError) RetrySafe() bool { return false }

type LifecycleSnapshot struct {
	Mode      AuthorityMode
	SessionID string
	BoardID   string
	Revision  uint64
	Record    AuthorityRecord
	Backup    LegacyBackup
}

type RecoveryRequest struct {
	SessionID           string
	BoardID             string
	Revision            uint64
	AcknowledgeDataLoss bool
}

type RecoveryResult struct {
	SessionID         string
	PreviousBoardID   string
	PreviousRevision  uint64
	RecoveredBoardID  string
	RecoveredRevision uint64
}

type ForkPreparation struct {
	Mode        AuthorityMode
	LegacyTasks tools.TaskManagerSnapshot
	Record      AuthorityRecord
}

// BindLogicalWorkAdapter takes the TaskManager's exact locked snapshot and
// installs one root-lineage authority before releasing the manager lock.
func BindLogicalWorkAdapter(
	config AdapterConfig,
	manager *tools.TaskManager,
) (*LogicalWorkAdapter, error) {
	if manager == nil {
		return nil, fmt.Errorf("workboard authority: TaskManager is nil")
	}
	var adapter *LogicalWorkAdapter
	err := manager.BindAuthority(func(
		snapshot tools.TaskManagerSnapshot,
	) (tools.TaskAuthority, error) {
		created, createErr := NewLogicalWorkAdapter(config, snapshot)
		if createErr != nil {
			return nil, createErr
		}
		adapter = created
		return created, nil
	})
	if err != nil {
		return nil, err
	}
	return adapter, nil
}

func NewLogicalWorkAdapter(
	config AdapterConfig,
	snapshot tools.TaskManagerSnapshot,
) (*LogicalWorkAdapter, error) {
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.NewBoardID == nil {
		config.NewBoardID = uuid.NewString
	}
	if strings.TrimSpace(config.LeaderScope.SessionID) == "" {
		config.LeaderScope.SessionID = config.SessionID
	}
	store, err := NewStore(StoreConfig{
		Dir:         config.Dir,
		SessionID:   config.SessionID,
		FileFailure: config.FileFailure,
		Failure:     config.Failure,
	})
	if err != nil {
		return nil, err
	}
	directoryObservation, err := observeTranscriptDirectory(config.Dir)
	if err != nil {
		return nil, err
	}
	state, err := store.Inspect()
	if err != nil {
		return nil, err
	}
	directoryIdentity := directoryObservation.info
	if state.Mode == AuthorityModeLegacy {
		if config.DirectoryPreparationAfterInspect != nil {
			config.DirectoryPreparationAfterInspect()
		}
		secured, prepareErr := prepareObservedPrivateTranscriptDirectory(
			config.Dir,
			directoryObservation,
			nil,
		)
		if prepareErr != nil {
			return nil, prepareErr
		}
		directoryIdentity = secured
	}
	if err := store.bindDirectoryIdentity(directoryIdentity); err != nil {
		return nil, err
	}
	adapter := &LogicalWorkAdapter{
		config:     config,
		store:      store,
		mode:       state.Mode,
		events:     cloneTaskLifecycleEvents(snapshot.Events),
		projection: NewProjectionReducer(),
	}
	if state.Mode == AuthorityModeWorkBoard {
		adapter.record = state.Record
		adapter.backup = state.Backup
		if err := adapter.projection.Bootstrap(state.Record); err != nil {
			return nil, err
		}
		if err := adapter.registerTodoScopeLocked(
			config.LeaderScope,
			nil,
		); err != nil {
			return nil, err
		}
		return adapter, nil
	}
	seed, err := seedAuthorityRecord(
		config.SessionID,
		config.NewBoardID(),
		snapshot,
		config.LeaderScope,
		config.LeaderTodos,
	)
	if err != nil {
		return nil, err
	}
	adapter.record = seed
	adapter.backup = backupFromRecord(seed)
	return adapter, nil
}

func (a *LogicalWorkAdapter) ProjectionSnapshot() ProjectionSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.projection.Snapshot()
}

// ExplorerSnapshot freezes the WorkBoard projection and immutable execution
// relations in one authority-lock read.
func (a *LogicalWorkAdapter) ExplorerSnapshot() (
	ProjectionSnapshot,
	[]ExecutionLink,
) {
	if a == nil {
		return ProjectionSnapshot{}, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.projection.Snapshot(),
		append([]ExecutionLink(nil), a.record.ExecutionLinks...)
}

// BindSettlementSnapshot installs the engine-owned, WorkBoard-free runner
// reader used by terminal mutation and Session deletion admission.
func (a *LogicalWorkAdapter) BindSettlementSnapshot(
	snapshot func(
		[]ExecutionSettlementKey,
	) ([]ExecutionSettlement, error),
) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.config.SettlementSnapshot = snapshot
	a.mu.Unlock()
}

// BindMutationAdmission shares the Session deletion gate with every root
// lineage engine using this adapter.
func (a *LogicalWorkAdapter) BindMutationAdmission(admitted func() bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.config.MutationAdmission = admitted
	a.mu.Unlock()
}

func (a *LogicalWorkAdapter) Mode() AuthorityMode {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func (a *LogicalWorkAdapter) RegisterTodoScope(
	scope tools.TodoScope,
	snapshot []tools.TodoItem,
) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.mutationReadyLocked(); err != nil {
		return err
	}
	return a.registerTodoScopeLocked(scope, snapshot)
}

func (a *LogicalWorkAdapter) Todos(
	scope tools.TodoScope,
) ([]tools.TodoItem, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	compatibility, found := todoScopeCompatibility(
		a.record.Compatibility.TodoScopes,
		scope,
	)
	if !found {
		return nil, nil
	}
	if len(compatibility.CurrentItemIDs) == 0 {
		return nil, nil
	}
	items := make(map[string]WorkItem, len(a.record.Board.Items))
	for _, item := range a.record.Board.Items {
		items[item.ID] = item
	}
	todos := make([]tools.TodoItem, 0, len(compatibility.CurrentItemIDs))
	for _, itemID := range compatibility.CurrentItemIDs {
		item, exists := items[itemID]
		if !exists {
			return nil, fmt.Errorf(
				"workboard authority: current Todo item %q is missing",
				itemID,
			)
		}
		todos = append(todos, tools.TodoItem{
			Content:    item.Title,
			Status:     string(item.Status),
			ActiveForm: item.ActiveForm,
		})
	}
	return todos, nil
}

func (a *LogicalWorkAdapter) ReplaceTodos(
	scope tools.TodoScope,
	todos []tools.TodoItem,
) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.mutationReadyLocked(); err != nil {
		return err
	}
	next := cloneAuthorityRecord(a.record)
	if err := replaceTodos(&next, scope, todos); err != nil {
		return err
	}
	next.Board.Revision = a.record.Board.Revision + 1
	if err := a.guardTerminalLinksLocked(a.record, next); err != nil {
		return err
	}
	if err := validateAuthorityRecord(next, a.config.SessionID); err != nil {
		return err
	}
	if err := a.commitProjectedLocked(next); err != nil {
		return err
	}
	return nil
}

func (a *LogicalWorkAdapter) Create(
	subject string,
	description string,
	activeForm string,
	metadata map[string]any,
) (*tools.TaskRecord, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.mutationReadyLocked(); err != nil {
		return nil, err
	}
	next := cloneAuthorityRecord(a.record)
	now := a.config.Clock().UTC()
	id := strconv.Itoa(next.Compatibility.NextTaskID)
	next.Compatibility.NextTaskID++
	rawMetadata, err := canonicalMetadata(metadata)
	if err != nil {
		return nil, fmt.Errorf("workboard authority: Task metadata: %w", err)
	}
	compatibility := TaskCompatibility{
		ID:           id,
		Subject:      subject,
		Description:  description,
		ActiveForm:   activeForm,
		LegacyStatus: string(tools.TaskStatusPending),
		Metadata:     rawMetadata,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	next.Compatibility.Tasks = append(
		next.Compatibility.Tasks,
		compatibility,
	)
	next.Board.Items = append(next.Board.Items, workItemFromTask(
		compatibility,
		len(next.Board.Items),
	))
	next.Board.Revision = a.record.Board.Revision + 1
	normalizeItemOrder(next.Board.Items)
	if err := validateAuthorityRecord(next, a.config.SessionID); err != nil {
		return nil, err
	}
	if err := a.commitProjectedLocked(next); err != nil {
		return nil, err
	}
	task, _ := a.taskLocked(id)
	a.recordTaskEventLocked(tools.TaskLifecycleCreated, task, nil)
	return task, nil
}

// AdmitExecutionLink persists an immutable runner admission. It performs no
// runner callback; dispatch remains an engine responsibility.
func (a *LogicalWorkAdapter) AdmitExecutionLink(request AdmitExecutionLinkRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.mutationReadyLocked(); err != nil {
		return err
	}
	if a.mode != AuthorityModeWorkBoard || request.BoardID != a.record.BoardID {
		return fmt.Errorf("workboard authority: execution-link board identity or revision mismatch")
	}
	link := ExecutionLink{BoardID: request.BoardID, WorkItemID: request.WorkItemID, WorkItemRevision: request.WorkItemRevision, AgentID: request.AgentID, Generation: request.Generation, Actor: "agent_launch_admission", ParentSessionID: request.ParentSessionID, ParentThreadID: request.ParentThreadID, ParentAgentID: request.ParentAgentID, ParentToolUseID: request.ParentToolUseID, AdmittedAt: request.AdmittedAt}
	for _, existing := range a.record.ExecutionLinks {
		if existing.AgentID == link.AgentID && existing.Generation == link.Generation {
			if existing == link {
				return nil
			}
			return fmt.Errorf("workboard authority: execution-link key conflict")
		}
	}
	if request.BoardRevision != a.record.Board.Revision {
		return fmt.Errorf("workboard authority: execution-link board identity or revision mismatch")
	}
	itemIndex := -1
	for i := range a.record.Board.Items {
		if a.record.Board.Items[i].ID == request.WorkItemID {
			itemIndex = i
			break
		}
	}
	if itemIndex < 0 || isTerminalStatus(a.record.Board.Items[itemIndex].Status) || a.record.Board.Items[itemIndex].Revision != request.WorkItemRevision {
		return fmt.Errorf("workboard authority: execution-link WorkItem is not open at expected revision")
	}
	next := cloneAuthorityRecord(a.record)
	if len(next.ExecutionLinks) >= MaxExecutionLinks {
		return fmt.Errorf("workboard authority: execution links exceed limit %d", MaxExecutionLinks)
	}
	next.Version = AuthorityRecordVersionV3
	next.ExecutionLinks = append(next.ExecutionLinks, link)
	next.Board.Revision++
	if err := validateAuthorityRecord(next, a.config.SessionID); err != nil {
		return err
	}
	if err := a.projection.Bootstrap(a.record); err != nil {
		return err
	}
	reservation, err := a.projection.reserve(a.record.BoardID, a.record.Board.Revision, next)
	if err != nil {
		return err
	}
	var committed AuthorityRecord
	if a.record.Version == AuthorityRecordVersion {
		committed, err = a.store.UpgradeExecutionLinks(a.record.BoardID, a.record.Board.Revision, next)
	} else {
		committed, err = a.store.Commit(a.record.BoardID, a.record.Board.Revision, next)
	}
	if err := a.acceptCommitLocked(committed, err); err != nil {
		reservation.Abort()
		return err
	}
	if err := a.projectionSwapFailureLocked(); err != nil {
		reservation.Abort()
		a.mutationErr = err
		return err
	}
	reservation.Commit()
	return nil
}

func (a *LogicalWorkAdapter) guardTerminalLinksLocked(current, next AuthorityRecord) error {
	if len(current.ExecutionLinks) == 0 {
		return nil
	}
	before := make(map[string]WorkItem, len(current.Board.Items))
	for _, item := range current.Board.Items {
		before[item.ID] = item
	}
	terminalized := make(map[string]struct{})
	for _, item := range next.Board.Items {
		prior, ok := before[item.ID]
		if ok && !isTerminalStatus(prior.Status) && isTerminalStatus(item.Status) {
			terminalized[item.ID] = struct{}{}
		}
	}
	if len(terminalized) == 0 {
		return nil
	}
	keys := make([]ExecutionSettlementKey, 0, len(current.ExecutionLinks))
	for _, link := range current.ExecutionLinks {
		if link.BoardID != current.BoardID {
			continue
		}
		if _, ok := terminalized[link.WorkItemID]; !ok {
			continue
		}
		keys = append(keys, ExecutionSettlementKey{
			AgentID: link.AgentID, Generation: link.Generation,
		})
	}
	if len(keys) == 0 {
		return nil
	}
	if a.config.SettlementSnapshot == nil {
		return fmt.Errorf("workboard authority: linked terminal mutation requires settlement snapshot")
	}
	settlements, err := a.config.SettlementSnapshot(keys)
	if err != nil {
		return fmt.Errorf("workboard authority: settlement snapshot: %w", err)
	}
	seen := make(map[ExecutionSettlementKey]ExecutionSettlement, len(settlements))
	for _, settlement := range settlements {
		if _, duplicate := seen[settlement.Key]; duplicate {
			return fmt.Errorf("workboard authority: duplicate settlement key")
		}
		seen[settlement.Key] = settlement
	}
	for _, key := range keys {
		settlement, ok := seen[key]
		if !ok || !settlement.Settled || settlement.Reserved || settlement.Live || settlement.CancelPending || settlement.Unresolved {
			return fmt.Errorf("workboard authority: linked terminal mutation has unsettled execution")
		}
	}
	return nil
}

func (a *LogicalWorkAdapter) Get(id string) (*tools.TaskRecord, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.taskLocked(id)
}

func (a *LogicalWorkAdapter) List() []*tools.TaskRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	tasks := make([]*tools.TaskRecord, 0, len(a.record.Compatibility.Tasks))
	for _, compatibility := range a.record.Compatibility.Tasks {
		task, err := taskRecordFromCompatibility(compatibility)
		if err != nil {
			return nil
		}
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(left, right int) bool {
		return tasks[left].ID < tasks[right].ID
	})
	return tasks
}

func (a *LogicalWorkAdapter) Update(
	input tools.TaskUpdate,
) (*tools.TaskRecord, []string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.updateTaskLocked(input, tools.TaskLifecycleUpdated)
}

func (a *LogicalWorkAdapter) updateTaskLocked(
	input tools.TaskUpdate,
	eventPhase tools.TaskLifecyclePhase,
) (*tools.TaskRecord, []string, error) {
	if err := a.mutationReadyLocked(); err != nil {
		return nil, nil, err
	}
	next := cloneAuthorityRecord(a.record)
	taskIndex := compatibilityTaskIndex(next.Compatibility.Tasks, input.TaskID)
	if taskIndex < 0 {
		return nil, nil, fmt.Errorf("task not found: %s", input.TaskID)
	}
	itemIndex := taskWorkItemIndex(next.Board.Items, input.TaskID)
	if itemIndex < 0 {
		return nil, nil, fmt.Errorf(
			"workboard authority: Task %s has no WorkItem",
			input.TaskID,
		)
	}
	task := &next.Compatibility.Tasks[taskIndex]
	knownTasks := make(map[string]struct{}, len(next.Compatibility.Tasks))
	for _, candidate := range next.Compatibility.Tasks {
		knownTasks[candidate.ID] = struct{}{}
	}
	updatedFields, err := updateTaskCompatibility(task, input, knownTasks)
	if err != nil {
		return nil, nil, err
	}
	task.UpdatedAt = a.config.Clock().UTC()
	previousItem := next.Board.Items[itemIndex]
	next.Board.Items[itemIndex] = workItemFromTask(
		*task,
		previousItem.Order,
	)
	next.Board.Items[itemIndex].Revision = previousItem.Revision
	if len(updatedFields) > 0 ||
		!sameWorkItemContent(previousItem, next.Board.Items[itemIndex]) {
		next.Board.Items[itemIndex].Revision++
	}
	if len(input.AddBlocks) == 0 {
		next.Board.Items[itemIndex].Blocks = append(
			[]string(nil),
			previousItem.Blocks...,
		)
	}
	if len(input.AddBlockedBy) == 0 {
		next.Board.Items[itemIndex].BlockedBy = append(
			[]string(nil),
			previousItem.BlockedBy...,
		)
	}
	next.Board.Revision = a.record.Board.Revision + 1
	if err := a.guardTerminalLinksLocked(a.record, next); err != nil {
		return nil, nil, err
	}
	if err := validateAuthorityRecord(next, a.config.SessionID); err != nil {
		return nil, nil, err
	}
	if err := a.commitProjectedLocked(next); err != nil {
		return nil, nil, err
	}
	updated, _ := a.taskLocked(input.TaskID)
	if len(updatedFields) > 0 {
		a.recordTaskEventLocked(
			eventPhase,
			updated,
			updatedFields,
		)
	}
	return updated, updatedFields, nil
}

func (a *LogicalWorkAdapter) Stop(id string) (*tools.TaskRecord, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	status := tools.TaskStatusKilled
	updated, _, err := a.updateTaskLocked(
		tools.TaskUpdate{
			TaskID: id,
			Status: &status,
		},
		tools.TaskLifecycleStopped,
	)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (a *LogicalWorkAdapter) DrainLifecycleEvents() []tools.TaskLifecycleEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.events) == 0 {
		return nil
	}
	events := cloneTaskLifecycleEvents(a.events)
	a.events = nil
	return events
}

func (a *LogicalWorkAdapter) Output(id string) (string, error) {
	task, found := a.Get(id)
	if !found {
		return "", fmt.Errorf("task not found: %s", id)
	}
	if strings.TrimSpace(task.Output) == "" {
		return fmt.Sprintf("Task #%s has no output yet.", task.ID), nil
	}
	return task.Output, nil
}

// WithStableLifecycle excludes Task/Todo mutation while the caller commits a
// Session lifecycle operation, then proves the same authority identity remains.
func (a *LogicalWorkAdapter) WithStableLifecycle(
	operation func(LifecycleSnapshot) error,
) error {
	if operation == nil {
		return fmt.Errorf("workboard authority: lifecycle operation is nil")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.mutationReadyLocked(); err != nil {
		return err
	}
	snapshot := a.lifecycleSnapshotLocked()
	if err := operation(snapshot); err != nil {
		return err
	}
	if snapshot.Mode != AuthorityModeWorkBoard {
		return nil
	}
	state, err := a.store.Inspect()
	if err != nil {
		return err
	}
	if state.Mode != AuthorityModeWorkBoard ||
		state.Record.Version != snapshot.Record.Version ||
		state.Record.BoardID != snapshot.BoardID ||
		state.Record.Board.Revision != snapshot.Revision ||
		!slices.Equal(
			state.Record.ExecutionLinks,
			snapshot.Record.ExecutionLinks,
		) {
		return fmt.Errorf(
			"workboard authority: board changed during Session lifecycle",
		)
	}
	return nil
}

func (a *LogicalWorkAdapter) WithExclusiveLifecycle(
	operation func(LifecycleSnapshot) error,
) error {
	if operation == nil {
		return fmt.Errorf("workboard authority: lifecycle operation is nil")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.mutationStateReadyLocked(); err != nil {
		return err
	}
	return operation(a.lifecycleSnapshotLocked())
}

func (a *LogicalWorkAdapter) Snapshot() LifecycleSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lifecycleSnapshotLocked()
}

// BeginActivation revalidates a prepared target and swaps this root-lineage
// adapter while excluding every Task/Todo method. The caller must hold the
// returned guard until the QueryEngine identity switch is complete.
func (a *LogicalWorkAdapter) BeginActivation(
	prepared *LogicalWorkAdapter,
) (func(), error) {
	if a == nil || prepared == nil {
		return nil, fmt.Errorf(
			"workboard authority: activation adapter is unavailable",
		)
	}
	a.mu.Lock()
	prepared.mu.Lock()
	state, err := prepared.store.Inspect()
	if err != nil {
		prepared.mu.Unlock()
		a.mu.Unlock()
		return nil, err
	}
	if state.Mode == AuthorityModeWorkBoard {
		prepared.mode = state.Mode
		prepared.record = state.Record
		prepared.backup = state.Backup
		if err := prepared.projection.Bootstrap(state.Record); err != nil {
			prepared.mu.Unlock()
			a.mu.Unlock()
			return nil, err
		}
	} else if prepared.mode == AuthorityModeWorkBoard {
		prepared.mu.Unlock()
		a.mu.Unlock()
		return nil, fmt.Errorf(
			"workboard authority: prepared marker disappeared before activation",
		)
	}
	a.config = prepared.config
	a.store = prepared.store
	a.mode = prepared.mode
	a.record = cloneAuthorityRecord(prepared.record)
	a.backup = cloneLegacyBackup(prepared.backup)
	a.events = cloneTaskLifecycleEvents(prepared.events)
	a.mutationErr = prepared.mutationErr
	a.projection = prepared.projection.clone()
	prepared.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(a.mu.Unlock)
	}, nil
}

func (a *LogicalWorkAdapter) Recover(
	request RecoveryRequest,
) (RecoveryResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.mutationReadyLocked(); err != nil {
		return RecoveryResult{}, err
	}
	if !request.AcknowledgeDataLoss {
		return RecoveryResult{}, fmt.Errorf(
			"workboard authority: recovery requires data-loss acknowledgement",
		)
	}
	if request.SessionID != a.config.SessionID ||
		a.mode != AuthorityModeWorkBoard ||
		request.BoardID != a.record.BoardID ||
		request.Revision != a.record.Board.Revision {
		return RecoveryResult{}, fmt.Errorf(
			"workboard authority: recovery identity or revision mismatch",
		)
	}
	newBoardID := a.config.NewBoardID()
	previousBoardID := a.record.BoardID
	previousRevision := a.record.Board.Revision
	recovered, err := a.store.Recover(
		request.BoardID,
		request.Revision,
		newBoardID,
	)
	if recovered.Version != 0 {
		a.record = recovered
		a.events = nil
	}
	if err != nil {
		if IsDurabilityUncertain(err) {
			a.mutationErr = err
		}
		return RecoveryResult{}, err
	}
	a.projection = NewProjectionReducer()
	if err := a.projection.Bootstrap(recovered); err != nil {
		return RecoveryResult{}, err
	}
	result := RecoveryResult{
		SessionID:         a.config.SessionID,
		PreviousBoardID:   previousBoardID,
		PreviousRevision:  previousRevision,
		RecoveredBoardID:  recovered.BoardID,
		RecoveredRevision: recovered.Board.Revision,
	}
	return result, nil
}

func (a *LogicalWorkAdapter) LegacyTaskSnapshot() tools.TaskManagerSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	tasks := a.listLocked()
	return tools.TaskManagerSnapshot{
		NextID: a.record.Compatibility.NextTaskID,
		Tasks:  tasks,
	}
}

// PrepareFork holds the source lineage gate through a marker-last child
// authority commit. Legacy sources publish no WorkBoard artifacts.
func (a *LogicalWorkAdapter) PrepareFork(
	sourceSessionID string,
	childSessionID string,
	childDir string,
) (ForkPreparation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.mutationReadyLocked(); err != nil {
		return ForkPreparation{}, err
	}
	if strings.TrimSpace(sourceSessionID) == "" ||
		a.config.SessionID != sourceSessionID {
		return ForkPreparation{}, fmt.Errorf(
			"workboard authority: fork source Session changed",
		)
	}
	if a.mode != AuthorityModeWorkBoard {
		return ForkPreparation{
			Mode: AuthorityModeLegacy,
			LegacyTasks: tools.TaskManagerSnapshot{
				NextID: a.record.Compatibility.NextTaskID,
				Tasks:  a.listLocked(),
			},
		}, nil
	}
	childStore, err := NewStore(StoreConfig{
		Dir:       childDir,
		SessionID: childSessionID,
	})
	if err != nil {
		return ForkPreparation{}, err
	}
	if existing, inspectErr := childStore.Inspect(); inspectErr != nil {
		return ForkPreparation{}, inspectErr
	} else if existing.Mode == AuthorityModeWorkBoard {
		return ForkPreparation{
			Mode:   AuthorityModeWorkBoard,
			Record: existing.Record,
		}, nil
	}
	child := cloneAuthorityRecord(a.record)
	child.Version = AuthorityRecordVersion
	child.ExecutionLinks = nil
	child.SessionID = childSessionID
	child.BoardID = a.config.NewBoardID()
	if strings.TrimSpace(child.BoardID) == "" ||
		child.BoardID == a.record.BoardID {
		return ForkPreparation{}, fmt.Errorf(
			"workboard authority: fork requires a fresh BoardID",
		)
	}
	child.Compatibility.TodoScopes = append(
		child.Compatibility.TodoScopes,
		TodoScopeCompatibility{
			SessionID:      childSessionID,
			CurrentItemIDs: []string{},
		},
	)
	backup := backupFromRecord(child)
	state, err := childStore.Cutover(child, backup)
	if err != nil {
		return ForkPreparation{}, err
	}
	return ForkPreparation{
		Mode:   AuthorityModeWorkBoard,
		Record: state.Record,
	}, nil
}

func RemoveForkArtifacts(dir, sessionID string) error {
	store, err := NewStore(StoreConfig{Dir: dir, SessionID: sessionID})
	if err != nil {
		return err
	}
	_, err = store.RemoveOwnedArtifacts()
	return err
}

func (a *LogicalWorkAdapter) ensureAuthoritativeLocked() error {
	if err := a.mutationReadyLocked(); err != nil {
		return err
	}
	if a.mode == AuthorityModeWorkBoard {
		return nil
	}
	backup := backupFromRecord(a.record)
	state, err := a.store.Cutover(a.record, backup)
	if state.Mode == AuthorityModeWorkBoard {
		a.mode = state.Mode
		a.record = state.Record
		a.backup = state.Backup
	}
	if state.Mode == AuthorityModeWorkBoard {
		if err := a.projection.Bootstrap(state.Record); err != nil {
			return err
		}
	}
	if err != nil {
		if IsDurabilityUncertain(err) {
			a.mutationErr = err
		}
		return err
	}
	if err := a.store.fail(StoreStageFirstMutation); err != nil {
		return err
	}
	return nil
}

func (a *LogicalWorkAdapter) commitProjectedLocked(next AuthorityRecord) error {
	if err := a.ensureAuthoritativeLocked(); err != nil {
		return err
	}
	if err := a.projection.Bootstrap(a.record); err != nil {
		return err
	}
	reservation, err := a.projection.reserve(
		a.record.BoardID,
		a.record.Board.Revision,
		next,
	)
	if err != nil {
		return err
	}
	committed, err := a.store.Commit(
		a.record.BoardID,
		a.record.Board.Revision,
		next,
	)
	if err := a.acceptCommitLocked(committed, err); err != nil {
		reservation.Abort()
		return err
	}
	if err := a.projectionSwapFailureLocked(); err != nil {
		reservation.Abort()
		a.mutationErr = err
		return err
	}
	reservation.Commit()
	return nil
}

func (a *LogicalWorkAdapter) projectionSwapFailureLocked() (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &CommittedProjectionUncertainError{
				Cause: fmt.Errorf("projection swap panic: %v", recovered),
			}
		}
	}()
	if a.config.ProjectionSwapFailure == nil {
		return nil
	}
	if hookErr := a.config.ProjectionSwapFailure(); hookErr != nil {
		return &CommittedProjectionUncertainError{Cause: hookErr}
	}
	return nil
}

func (a *LogicalWorkAdapter) acceptCommitLocked(
	committed AuthorityRecord,
	err error,
) error {
	if committed.Version != 0 {
		a.record = committed
	}
	if IsDurabilityUncertain(err) {
		a.mutationErr = err
	}
	return err
}

func (a *LogicalWorkAdapter) mutationReadyLocked() error {
	if a.config.MutationAdmission != nil &&
		!a.config.MutationAdmission() {
		return fmt.Errorf(
			"workboard authority: active Session deletion blocks mutation",
		)
	}
	return a.mutationStateReadyLocked()
}

func (a *LogicalWorkAdapter) mutationStateReadyLocked() error {
	if a.mutationErr == nil {
		return nil
	}
	return fmt.Errorf(
		"workboard authority: mutations are quarantined until restart: %w",
		a.mutationErr,
	)
}

func (a *LogicalWorkAdapter) registerTodoScopeLocked(
	scope tools.TodoScope,
	snapshot []tools.TodoItem,
) error {
	if strings.TrimSpace(scope.SessionID) == "" {
		return fmt.Errorf("workboard authority: Todo scope SessionID is empty")
	}
	if _, exists := todoScopeCompatibility(
		a.record.Compatibility.TodoScopes,
		scope,
	); exists {
		return nil
	}
	if a.mode == AuthorityModeWorkBoard {
		return nil
	}
	return seedTodoScope(&a.record, scope, snapshot)
}

func (a *LogicalWorkAdapter) lifecycleSnapshotLocked() LifecycleSnapshot {
	return LifecycleSnapshot{
		Mode:      a.mode,
		SessionID: a.config.SessionID,
		BoardID:   a.record.BoardID,
		Revision:  a.record.Board.Revision,
		Record:    cloneAuthorityRecord(a.record),
		Backup:    cloneLegacyBackup(a.backup),
	}
}

func (a *LogicalWorkAdapter) taskLocked(
	id string,
) (*tools.TaskRecord, bool) {
	index := compatibilityTaskIndex(a.record.Compatibility.Tasks, id)
	if index < 0 {
		return nil, false
	}
	task, err := taskRecordFromCompatibility(
		a.record.Compatibility.Tasks[index],
	)
	if err != nil {
		return nil, false
	}
	return task, true
}

func (a *LogicalWorkAdapter) listLocked() []*tools.TaskRecord {
	tasks := make([]*tools.TaskRecord, 0, len(a.record.Compatibility.Tasks))
	for _, compatibility := range a.record.Compatibility.Tasks {
		task, err := taskRecordFromCompatibility(compatibility)
		if err == nil {
			tasks = append(tasks, task)
		}
	}
	sort.Slice(tasks, func(left, right int) bool {
		return tasks[left].ID < tasks[right].ID
	})
	return tasks
}

func (a *LogicalWorkAdapter) recordTaskEventLocked(
	phase tools.TaskLifecyclePhase,
	task *tools.TaskRecord,
	fields []string,
) {
	a.events = append(a.events, tools.TaskLifecycleEvent{
		Phase:         phase,
		Task:          cloneToolTask(task),
		UpdatedFields: append([]string(nil), fields...),
	})
}

func seedAuthorityRecord(
	sessionID string,
	boardID string,
	snapshot tools.TaskManagerSnapshot,
	leaderScope tools.TodoScope,
	leaderTodos []tools.TodoItem,
) (AuthorityRecord, error) {
	record := AuthorityRecord{
		Version:   AuthorityRecordVersion,
		SessionID: sessionID,
		BoardID:   boardID,
		Board: Board{
			Revision: 1,
		},
		Compatibility: CompatibilityPayload{
			NextTaskID: snapshot.NextID,
			Tasks:      make([]TaskCompatibility, 0, len(snapshot.Tasks)),
			TodoScopes: make([]TodoScopeCompatibility, 0, 1),
		},
	}
	if record.Compatibility.NextTaskID <= 0 {
		record.Compatibility.NextTaskID = 1
	}
	knownTasks := make(map[string]struct{}, len(snapshot.Tasks))
	for _, task := range snapshot.Tasks {
		if task == nil || strings.TrimSpace(task.ID) == "" {
			return AuthorityRecord{}, fmt.Errorf(
				"workboard authority: legacy Task snapshot has an empty ID",
			)
		}
		knownTasks[task.ID] = struct{}{}
	}
	for _, task := range snapshot.Tasks {
		compatibility, err := taskCompatibilityFromRecord(task, knownTasks)
		if err != nil {
			return AuthorityRecord{}, err
		}
		record.Compatibility.Tasks = append(
			record.Compatibility.Tasks,
			compatibility,
		)
		record.Board.Items = append(
			record.Board.Items,
			workItemFromTask(compatibility, len(record.Board.Items)),
		)
	}
	if err := seedTodoScope(&record, leaderScope, leaderTodos); err != nil {
		return AuthorityRecord{}, err
	}
	normalizeItemOrder(record.Board.Items)
	if err := validateAuthorityRecord(record, sessionID); err != nil {
		return AuthorityRecord{}, err
	}
	return record, nil
}

func seedTodoScope(
	record *AuthorityRecord,
	scope tools.TodoScope,
	todos []tools.TodoItem,
) error {
	if strings.TrimSpace(scope.SessionID) == "" {
		return fmt.Errorf("workboard authority: Todo scope SessionID is empty")
	}
	record.Compatibility.TodoScopes = append(
		record.Compatibility.TodoScopes,
		TodoScopeCompatibility{
			SessionID:      scope.SessionID,
			AgentID:        scope.AgentID,
			CurrentItemIDs: []string{},
		},
	)
	return replaceTodos(record, scope, todos)
}

func replaceTodos(
	record *AuthorityRecord,
	scope tools.TodoScope,
	todos []tools.TodoItem,
) error {
	scopeIndex := todoScopeIndex(record.Compatibility.TodoScopes, scope)
	if scopeIndex < 0 {
		record.Compatibility.TodoScopes = append(
			record.Compatibility.TodoScopes,
			TodoScopeCompatibility{
				SessionID:      scope.SessionID,
				AgentID:        scope.AgentID,
				CurrentItemIDs: []string{},
			},
		)
		scopeIndex = len(record.Compatibility.TodoScopes) - 1
	}
	partition := SourcePartition{
		Kind:      "todo",
		SessionID: scope.SessionID,
		AgentID:   scope.AgentID,
	}
	prior := make([]WorkItem, 0)
	retained := make([]WorkItem, 0, len(record.Board.Items)+len(todos))
	usedIDs := make(map[string]struct{}, len(record.Board.Items)+len(todos))
	for _, item := range record.Board.Items {
		usedIDs[item.ID] = struct{}{}
		if sameTodoPartition(item.Source, partition) {
			prior = append(prior, item)
			continue
		}
		retained = append(retained, item)
	}
	priorByKey := make(map[string][]WorkItem)
	for _, item := range prior {
		key := todoContentKey(item.Title, item.ActiveForm)
		priorByKey[key] = append(priorByKey[key], item)
	}
	incomingCounts := make(map[string]int)
	for _, todo := range todos {
		incomingCounts[todoContentKey(todo.Content, todo.ActiveForm)]++
	}
	matched := make(map[string]struct{})
	currentIDs := make([]string, 0, len(todos))
	allCompleted := len(todos) > 0
	for order, todo := range todos {
		status := Status(todo.Status)
		if !validStatus(status) || status == StatusFailed ||
			status == StatusCancelled {
			return fmt.Errorf(
				"workboard authority: Todo has invalid status %q",
				todo.Status,
			)
		}
		if status != StatusCompleted {
			allCompleted = false
		}
		key := todoContentKey(todo.Content, todo.ActiveForm)
		var previous WorkItem
		hasPrevious := len(priorByKey[key]) == 1 &&
			incomingCounts[key] == 1
		if hasPrevious {
			previous = priorByKey[key][0]
			if isTerminalStatus(previous.Status) &&
				!isTerminalStatus(status) {
				hasPrevious = false
			} else {
				matched[previous.ID] = struct{}{}
			}
		}
		item := WorkItem{
			ID:         previous.ID,
			Revision:   previous.Revision,
			Source:     partition,
			Order:      order,
			Title:      todo.Content,
			ActiveForm: todo.ActiveForm,
			Status:     status,
		}
		if !hasPrevious {
			item.ID = nextTodoWorkItemID(&record.Board, usedIDs)
			item.Revision = 1
			usedIDs[item.ID] = struct{}{}
		} else if !sameWorkItemContent(previous, item) {
			item.Revision++
		}
		retained = append(retained, item)
		currentIDs = append(currentIDs, item.ID)
	}
	for _, previous := range prior {
		if _, ok := matched[previous.ID]; ok {
			continue
		}
		item := cloneWorkItem(previous)
		if !isTerminalStatus(item.Status) {
			item.Status = StatusCancelled
			item.TerminalReason = "legacy_full_replacement_omission"
			item.Revision++
		}
		retained = append(retained, item)
	}
	if allCompleted {
		currentIDs = nil
	}
	record.Board.Items = retained
	normalizeItemOrder(record.Board.Items)
	record.Compatibility.TodoScopes[scopeIndex].CurrentItemIDs = currentIDs
	return nil
}

func taskCompatibilityFromRecord(
	task *tools.TaskRecord,
	known map[string]struct{},
) (TaskCompatibility, error) {
	metadata, err := canonicalMetadata(task.Metadata)
	if err != nil {
		return TaskCompatibility{}, fmt.Errorf(
			"workboard authority: Task %q metadata: %w",
			task.ID,
			err,
		)
	}
	compatibility := TaskCompatibility{
		ID:           task.ID,
		Subject:      task.Subject,
		Description:  task.Description,
		ActiveForm:   task.ActiveForm,
		LegacyStatus: string(task.Status),
		Owner:        task.Owner,
		Blocks:       append([]string(nil), task.Blocks...),
		BlockedBy:    append([]string(nil), task.BlockedBy...),
		Metadata:     metadata,
		Output:       task.Output,
		CreatedAt:    task.CreatedAt,
		UpdatedAt:    task.UpdatedAt,
	}
	for _, dependency := range compatibility.Blocks {
		if _, exists := known[dependency]; !exists {
			compatibility.UnresolvedBlocks = append(
				compatibility.UnresolvedBlocks,
				dependency,
			)
		}
	}
	for _, dependency := range compatibility.BlockedBy {
		if _, exists := known[dependency]; !exists {
			compatibility.UnresolvedBlockedBy = append(
				compatibility.UnresolvedBlockedBy,
				dependency,
			)
		}
	}
	return compatibility, nil
}

func taskRecordFromCompatibility(
	task TaskCompatibility,
) (*tools.TaskRecord, error) {
	metadata := map[string]any(nil)
	if len(task.Metadata) > 0 {
		if err := json.Unmarshal(task.Metadata, &metadata); err != nil {
			return nil, fmt.Errorf(
				"workboard authority: decode Task %q metadata: %w",
				task.ID,
				err,
			)
		}
	}
	return &tools.TaskRecord{
		ID:          task.ID,
		Subject:     task.Subject,
		Description: task.Description,
		ActiveForm:  task.ActiveForm,
		Status:      tools.TaskStatus(task.LegacyStatus),
		Owner:       task.Owner,
		Blocks:      append([]string(nil), task.Blocks...),
		BlockedBy:   append([]string(nil), task.BlockedBy...),
		Metadata:    metadata,
		Output:      task.Output,
		CreatedAt:   task.CreatedAt,
		UpdatedAt:   task.UpdatedAt,
	}, nil
}

func workItemFromTask(task TaskCompatibility, order int) WorkItem {
	item := WorkItem{
		ID:            taskWorkItemID(task.ID),
		Revision:      1,
		Source:        SourcePartition{Kind: "task", LegacyID: task.ID},
		Order:         order,
		Title:         task.Subject,
		Description:   task.Description,
		ActiveForm:    task.ActiveForm,
		Status:        canonicalStatusForLegacy(task.LegacyStatus),
		Owner:         task.Owner,
		Metadata:      append(json.RawMessage(nil), task.Metadata...),
		ResultSummary: task.Output,
	}
	unresolvedBlocks := stringSet(task.UnresolvedBlocks)
	for _, dependency := range task.Blocks {
		if _, unresolved := unresolvedBlocks[dependency]; !unresolved {
			item.Blocks = append(item.Blocks, taskWorkItemID(dependency))
		}
	}
	unresolvedBlockedBy := stringSet(task.UnresolvedBlockedBy)
	for _, dependency := range task.BlockedBy {
		if _, unresolved := unresolvedBlockedBy[dependency]; !unresolved {
			item.BlockedBy = append(
				item.BlockedBy,
				taskWorkItemID(dependency),
			)
		}
	}
	switch task.LegacyStatus {
	case string(tools.TaskStatusKilled):
		item.TerminalReason = "legacy_task_stopped"
	case string(StatusPending),
		string(StatusInProgress),
		string(StatusCompleted),
		string(StatusFailed),
		string(tools.TaskStatusRunning):
	default:
		item.TerminalReason = "legacy_status"
	}
	return item
}

func updateTaskCompatibility(
	task *TaskCompatibility,
	input tools.TaskUpdate,
	knownTasks map[string]struct{},
) ([]string, error) {
	fields := make([]string, 0, 8)
	if input.Subject != nil && *input.Subject != task.Subject {
		task.Subject = *input.Subject
		fields = append(fields, "subject")
	}
	if input.Description != nil && *input.Description != task.Description {
		task.Description = *input.Description
		fields = append(fields, "description")
	}
	if input.ActiveForm != nil && *input.ActiveForm != task.ActiveForm {
		task.ActiveForm = *input.ActiveForm
		fields = append(fields, "active_form")
	}
	if input.Status != nil &&
		string(*input.Status) != task.LegacyStatus {
		status := *input.Status
		if status == tools.TaskStatusRunning {
			status = tools.TaskStatusInProgress
		}
		task.LegacyStatus = string(status)
		fields = append(fields, "status")
	}
	if input.Owner != nil && *input.Owner != task.Owner {
		task.Owner = *input.Owner
		fields = append(fields, "owner")
	}
	if len(input.AddBlocks) > 0 {
		task.Blocks = appendUniqueStrings(task.Blocks, input.AddBlocks...)
		fields = append(fields, "blocks")
	}
	if len(input.AddBlockedBy) > 0 {
		task.BlockedBy = appendUniqueStrings(
			task.BlockedBy,
			input.AddBlockedBy...,
		)
		fields = append(fields, "blocked_by")
	}
	if input.Metadata != nil {
		current := map[string]any{}
		if len(task.Metadata) > 0 {
			_ = json.Unmarshal(task.Metadata, &current)
		}
		for key, value := range input.Metadata {
			if value == nil {
				delete(current, key)
			} else {
				current[key] = value
			}
		}
		metadata, err := canonicalMetadata(current)
		if err != nil {
			return nil, fmt.Errorf(
				"workboard authority: Task %q metadata: %w",
				task.ID,
				err,
			)
		}
		task.Metadata = metadata
		fields = append(fields, "metadata")
	}
	if input.Output != nil && *input.Output != "" {
		if task.Output != "" {
			task.Output += "\n"
		}
		task.Output += *input.Output
		fields = append(fields, "output")
	}
	if len(input.AddBlocks) > 0 {
		task.UnresolvedBlocks = unresolvedTaskDependencies(
			task.Blocks,
			knownTasks,
		)
	}
	if len(input.AddBlockedBy) > 0 {
		task.UnresolvedBlockedBy = unresolvedTaskDependencies(
			task.BlockedBy,
			knownTasks,
		)
	}
	return fields, nil
}

func unresolvedTaskDependencies(
	dependencies []string,
	known map[string]struct{},
) []string {
	unresolved := make([]string, 0)
	for _, dependency := range dependencies {
		if _, exists := known[dependency]; !exists {
			unresolved = append(unresolved, dependency)
		}
	}
	return unresolved
}

func backupFromRecord(record AuthorityRecord) LegacyBackup {
	return LegacyBackup{
		Version:       LegacyBackupVersion,
		SessionID:     record.SessionID,
		BoardID:       record.BoardID,
		Board:         cloneBoard(record.Board),
		Compatibility: cloneCompatibility(record.Compatibility),
	}
}

func compatibilityTaskIndex(tasks []TaskCompatibility, id string) int {
	for index := range tasks {
		if tasks[index].ID == id {
			return index
		}
	}
	return -1
}

func taskWorkItemIndex(items []WorkItem, legacyID string) int {
	for index := range items {
		if items[index].Source.Kind == "task" &&
			items[index].Source.LegacyID == legacyID {
			return index
		}
	}
	return -1
}

func todoScopeCompatibility(
	scopes []TodoScopeCompatibility,
	scope tools.TodoScope,
) (TodoScopeCompatibility, bool) {
	index := todoScopeIndex(scopes, scope)
	if index < 0 {
		return TodoScopeCompatibility{}, false
	}
	found := scopes[index]
	found.CurrentItemIDs = append([]string(nil), found.CurrentItemIDs...)
	return found, true
}

func todoScopeIndex(
	scopes []TodoScopeCompatibility,
	scope tools.TodoScope,
) int {
	for index := range scopes {
		if scopes[index].SessionID == scope.SessionID &&
			scopes[index].AgentID == scope.AgentID {
			return index
		}
	}
	return -1
}

func appendUniqueStrings(existing []string, values ...string) []string {
	seen := stringSet(existing)
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		existing = append(existing, value)
	}
	return existing
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func cloneTaskLifecycleEvents(
	events []tools.TaskLifecycleEvent,
) []tools.TaskLifecycleEvent {
	cloned := make([]tools.TaskLifecycleEvent, 0, len(events))
	for _, event := range events {
		cloned = append(cloned, tools.TaskLifecycleEvent{
			Phase:         event.Phase,
			Task:          cloneToolTask(event.Task),
			UpdatedFields: append([]string(nil), event.UpdatedFields...),
		})
	}
	return cloned
}

func cloneToolTask(task *tools.TaskRecord) *tools.TaskRecord {
	if task == nil {
		return nil
	}
	cloned := *task
	cloned.Blocks = append([]string(nil), task.Blocks...)
	cloned.BlockedBy = append([]string(nil), task.BlockedBy...)
	if task.Metadata != nil {
		cloned.Metadata = make(map[string]any, len(task.Metadata))
		for key, value := range task.Metadata {
			cloned.Metadata[key] = value
		}
	}
	return &cloned
}
