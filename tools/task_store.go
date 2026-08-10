package tools

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusRunning    TaskStatus = "running"
	TaskStatusCompleted  TaskStatus = "completed"
	TaskStatusFailed     TaskStatus = "failed"
	TaskStatusKilled     TaskStatus = "killed"
)

type TaskRecord struct {
	ID          string         `json:"id"`
	Subject     string         `json:"subject"`
	Description string         `json:"description"`
	ActiveForm  string         `json:"active_form,omitempty"`
	Status      TaskStatus     `json:"status"`
	Owner       string         `json:"owner,omitempty"`
	Blocks      []string       `json:"blocks,omitempty"`
	BlockedBy   []string       `json:"blocked_by,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Output      string         `json:"output,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type TaskLifecyclePhase string

const (
	TaskLifecycleCreated TaskLifecyclePhase = "created"
	TaskLifecycleUpdated TaskLifecyclePhase = "updated"
	TaskLifecycleStopped TaskLifecyclePhase = "stopped"
)

type TaskLifecycleEvent struct {
	Phase         TaskLifecyclePhase
	Task          *TaskRecord
	UpdatedFields []string
}

type TaskUpdate struct {
	TaskID       string
	Subject      *string
	Description  *string
	ActiveForm   *string
	Status       *TaskStatus
	Owner        *string
	AddBlocks    []string
	AddBlockedBy []string
	Metadata     map[string]any
	Output       *string
}

// TaskAuthority is the single Task compatibility owner after a TaskManager is
// bound to a root-lineage LogicalWorkAdapter.
type TaskAuthority interface {
	Create(
		subject string,
		description string,
		activeForm string,
		metadata map[string]any,
	) (*TaskRecord, error)
	Get(string) (*TaskRecord, bool)
	List() []*TaskRecord
	Update(TaskUpdate) (*TaskRecord, []string, error)
	Stop(string) (*TaskRecord, error)
	DrainLifecycleEvents() []TaskLifecycleEvent
	Output(string) (string, error)
}

// TaskManagerSnapshot is the exact locked legacy state handed to the
// root-lineage authority during one-shot binding.
type TaskManagerSnapshot struct {
	NextID int
	Tasks  []*TaskRecord
	Events []TaskLifecycleEvent
}

type TaskManager struct {
	mu        sync.RWMutex
	next      int
	tasks     map[string]*TaskRecord
	events    []TaskLifecycleEvent
	authority TaskAuthority
}

func NewTaskManager() *TaskManager {
	return &TaskManager{next: 1, tasks: make(map[string]*TaskRecord)}
}

func (m *TaskManager) Create(subject, description, activeForm string, metadata map[string]any) *TaskRecord {
	task, _ := m.CreateWithError(subject, description, activeForm, metadata)
	return task
}

// CreateWithError preserves Create's legacy behavior while allowing an
// authoritative persistence failure to reach QueryEngine-bound tools.
func (m *TaskManager) CreateWithError(
	subject string,
	description string,
	activeForm string,
	metadata map[string]any,
) (*TaskRecord, error) {
	m.mu.Lock()
	if m.authority != nil {
		authority := m.authority
		m.mu.Unlock()
		return authority.Create(subject, description, activeForm, metadata)
	}
	defer m.mu.Unlock()
	id := fmt.Sprintf("%d", m.next)
	m.next++
	now := time.Now().UTC()
	task := &TaskRecord{
		ID:          id,
		Subject:     subject,
		Description: description,
		ActiveForm:  activeForm,
		Status:      TaskStatusPending,
		Metadata:    cloneMap(metadata),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	m.tasks[id] = task
	m.recordEventLocked(TaskLifecycleCreated, task, nil)
	return cloneTask(task), nil
}

func (m *TaskManager) Get(id string) (*TaskRecord, bool) {
	m.mu.RLock()
	if m.authority != nil {
		authority := m.authority
		m.mu.RUnlock()
		return authority.Get(id)
	}
	defer m.mu.RUnlock()
	t, ok := m.tasks[id]
	if !ok {
		return nil, false
	}
	return cloneTask(t), true
}

func (m *TaskManager) List() []*TaskRecord {
	m.mu.RLock()
	if m.authority != nil {
		authority := m.authority
		m.mu.RUnlock()
		return authority.List()
	}
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.tasks))
	for id := range m.tasks {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]*TaskRecord, 0, len(ids))
	for _, id := range ids {
		out = append(out, cloneTask(m.tasks[id]))
	}
	return out
}

func (m *TaskManager) Update(input TaskUpdate) (*TaskRecord, []string, error) {
	m.mu.Lock()
	if m.authority != nil {
		authority := m.authority
		m.mu.Unlock()
		return authority.Update(input)
	}
	defer m.mu.Unlock()
	t, updatedFields, err := m.updateLocked(input)
	if err != nil {
		return nil, nil, err
	}
	if len(updatedFields) > 0 {
		m.recordEventLocked(TaskLifecycleUpdated, t, updatedFields)
	}
	return cloneTask(t), updatedFields, nil
}

func (m *TaskManager) updateLocked(input TaskUpdate) (*TaskRecord, []string, error) {
	t, ok := m.tasks[input.TaskID]
	if !ok {
		return nil, nil, fmt.Errorf("task not found: %s", input.TaskID)
	}
	updatedFields := make([]string, 0, 8)
	if input.Subject != nil && *input.Subject != t.Subject {
		t.Subject = *input.Subject
		updatedFields = append(updatedFields, "subject")
	}
	if input.Description != nil && *input.Description != t.Description {
		t.Description = *input.Description
		updatedFields = append(updatedFields, "description")
	}
	if input.ActiveForm != nil && *input.ActiveForm != t.ActiveForm {
		t.ActiveForm = *input.ActiveForm
		updatedFields = append(updatedFields, "active_form")
	}
	if input.Status != nil && *input.Status != t.Status {
		t.Status = normalizeTaskStatus(*input.Status)
		updatedFields = append(updatedFields, "status")
	}
	if input.Owner != nil && *input.Owner != t.Owner {
		t.Owner = *input.Owner
		updatedFields = append(updatedFields, "owner")
	}
	if len(input.AddBlocks) > 0 {
		t.Blocks = appendUnique(t.Blocks, input.AddBlocks...)
		updatedFields = append(updatedFields, "blocks")
	}
	if len(input.AddBlockedBy) > 0 {
		t.BlockedBy = appendUnique(t.BlockedBy, input.AddBlockedBy...)
		updatedFields = append(updatedFields, "blocked_by")
	}
	if input.Metadata != nil {
		if t.Metadata == nil {
			t.Metadata = map[string]any{}
		}
		for k, v := range input.Metadata {
			if v == nil {
				delete(t.Metadata, k)
			} else {
				t.Metadata[k] = v
			}
		}
		updatedFields = append(updatedFields, "metadata")
	}
	if input.Output != nil && *input.Output != "" {
		if t.Output != "" {
			t.Output += "\n"
		}
		t.Output += *input.Output
		updatedFields = append(updatedFields, "output")
	}
	t.UpdatedAt = time.Now().UTC()
	return t, updatedFields, nil
}

func (m *TaskManager) Stop(id string) (*TaskRecord, error) {
	m.mu.Lock()
	if m.authority != nil {
		authority := m.authority
		m.mu.Unlock()
		return authority.Stop(id)
	}
	defer m.mu.Unlock()
	status := TaskStatusKilled
	updated, fields, err := m.updateLocked(TaskUpdate{TaskID: id, Status: &status})
	if err != nil {
		return nil, err
	}
	if len(fields) > 0 {
		m.recordEventLocked(TaskLifecycleStopped, updated, fields)
	}
	return cloneTask(updated), nil
}

func (m *TaskManager) DrainLifecycleEvents() []TaskLifecycleEvent {
	m.mu.Lock()
	if m.authority != nil {
		authority := m.authority
		m.mu.Unlock()
		return authority.DrainLifecycleEvents()
	}
	defer m.mu.Unlock()
	if len(m.events) == 0 {
		return nil
	}
	out := make([]TaskLifecycleEvent, 0, len(m.events))
	for _, evt := range m.events {
		out = append(out, TaskLifecycleEvent{
			Phase:         evt.Phase,
			Task:          cloneTask(evt.Task),
			UpdatedFields: append([]string(nil), evt.UpdatedFields...),
		})
	}
	m.events = nil
	return out
}

func (m *TaskManager) recordEventLocked(phase TaskLifecyclePhase, task *TaskRecord, updatedFields []string) {
	m.events = append(m.events, TaskLifecycleEvent{
		Phase:         phase,
		Task:          cloneTask(task),
		UpdatedFields: append([]string(nil), updatedFields...),
	})
}

func (m *TaskManager) Output(id string) (string, error) {
	m.mu.RLock()
	authority := m.authority
	m.mu.RUnlock()
	if authority != nil {
		return authority.Output(id)
	}
	t, ok := m.Get(id)
	if !ok {
		return "", fmt.Errorf("task not found: %s", id)
	}
	if strings.TrimSpace(t.Output) == "" {
		return fmt.Sprintf("Task #%s has no output yet.", t.ID), nil
	}
	return t.Output, nil
}

// BindAuthority atomically snapshots the legacy manager and installs exactly
// one authority before any later Task method can observe an unbound gap.
func (m *TaskManager) BindAuthority(
	build func(TaskManagerSnapshot) (TaskAuthority, error),
) error {
	if build == nil {
		return fmt.Errorf("workboard authority: Task authority builder is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.authority != nil {
		return fmt.Errorf("workboard authority: TaskManager is already bound")
	}
	snapshot := taskManagerSnapshotLocked(m)
	authority, err := build(snapshot)
	if err != nil {
		return err
	}
	if authority == nil {
		return fmt.Errorf("workboard authority: Task authority is nil")
	}
	m.authority = authority
	return nil
}

// AuthorityBound reports whether every Task method routes to the bound owner.
func (m *TaskManager) AuthorityBound() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.authority != nil
}

// BoundAuthority returns the installed owner so child runtimes that inherit a
// compatibility facade can reuse the same root-lineage authority.
func (m *TaskManager) BoundAuthority() (TaskAuthority, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.authority, m.authority != nil
}

func taskManagerSnapshotLocked(m *TaskManager) TaskManagerSnapshot {
	snapshot := TaskManagerSnapshot{
		NextID: m.next,
		Tasks:  make([]*TaskRecord, 0, len(m.tasks)),
		Events: make([]TaskLifecycleEvent, 0, len(m.events)),
	}
	ids := make([]string, 0, len(m.tasks))
	for id := range m.tasks {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		snapshot.Tasks = append(snapshot.Tasks, cloneTask(m.tasks[id]))
	}
	for _, event := range m.events {
		snapshot.Events = append(snapshot.Events, TaskLifecycleEvent{
			Phase:         event.Phase,
			Task:          cloneTask(event.Task),
			UpdatedFields: append([]string(nil), event.UpdatedFields...),
		})
	}
	return snapshot
}

func normalizeTaskStatus(status TaskStatus) TaskStatus {
	switch status {
	case TaskStatusRunning:
		return TaskStatusInProgress
	case TaskStatusPending, TaskStatusInProgress, TaskStatusCompleted, TaskStatusFailed, TaskStatusKilled:
		return status
	default:
		return status
	}
}

func cloneTask(in *TaskRecord) *TaskRecord {
	if in == nil {
		return nil
	}
	cp := *in
	cp.Blocks = append([]string(nil), in.Blocks...)
	cp.BlockedBy = append([]string(nil), in.BlockedBy...)
	cp.Metadata = cloneMap(in.Metadata)
	return &cp
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func appendUnique(existing []string, values ...string) []string {
	seen := make(map[string]struct{}, len(existing))
	for _, value := range existing {
		seen[value] = struct{}{}
	}
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		existing = append(existing, value)
	}
	return existing
}
