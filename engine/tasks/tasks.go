// Package tasks implements the task execution framework with typed task runners.
// This is a partial port of the reference implementation's src/tasks/ directory,
// providing the core task lifecycle (pending → running → completed/failed/cancelled),
// typed task runners (DreamTask, ShellTask, AgentTask), and a TaskManager for
// concurrent task management.
package tasks

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DreamModelFn is the function signature for the model call used by DreamTask.
// It takes a context, prompt, and model name, and returns the model's response.
// This is set at startup to avoid circular imports between tasks and engine.
// DreamModelFn is set by engine construction. Protected by GlobalCallbackMu.
var (
	DreamModelFn     func(ctx context.Context, prompt, model string) (string, error)
	GlobalCallbackMu sync.Mutex
)

// AgentExecutorFn is the function signature for sub-agent execution used by AgentTask.
// It takes a context, prompt, model, max turns, and allowed tools, and returns
// the agent's output. This is set at startup to avoid circular imports.
// Mirrors LocalAgentTask execution from the reference implementation.
// AgentExecutorFn is set by engine construction. Protected by GlobalCallbackMu.
var AgentExecutorFn func(ctx context.Context, prompt, model string, maxTurns int, allowedTools []string) (string, error)

// TaskType identifies the kind of task being executed.
// Mirrors the reference implementation's task type discriminators.
type TaskType string

const (
	TaskTypeDream    TaskType = "dream"
	TaskTypeAgent    TaskType = "agent"
	TaskTypeShell    TaskType = "shell"
	TaskTypeTeammate TaskType = "teammate"
	TaskTypeRemote   TaskType = "remote"
	TaskTypeWorkflow TaskType = "workflow"
	TaskTypeMonitor  TaskType = "monitor_mcp"
)

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
	TaskStatusCancelled TaskStatus = "cancelled"
	TaskStatusKilled    TaskStatus = "killed"
)

// IsTerminalTaskStatus returns true if the status represents a completed lifecycle.
// Mirrors the reference's isTerminalTaskStatus guard.
func IsTerminalTaskStatus(s TaskStatus) bool {
	switch s {
	case TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled, TaskStatusKilled:
		return true
	default:
		return false
	}
}

// Task is the interface all task types must implement.
// It provides lifecycle management (Start/Cancel/Kill/Wait) and status inspection.
type Task interface {
	ID() string
	Type() TaskType
	Status() TaskStatus
	Start(ctx context.Context) error
	Cancel() error
	// Kill immediately terminates the task with SIGKILL semantics.
	// Unlike Cancel (graceful), Kill sends SIGKILL to the process tree (for
	// shell tasks) and sets status to TaskStatusKilled.
	// Mirrors the reference's task.kill() contract.
	Kill() error
	Result() *TaskResult
	Wait(ctx context.Context) (*TaskResult, error)
}

// TaskResult holds the outcome of a completed task.
type TaskResult struct {
	Output   string
	Error    error
	ExitCode int
	Duration time.Duration
	Metadata map[string]any
}

// BaseTask provides shared state and lifecycle primitives embedded by all
// concrete task types. It implements the common parts of the Task interface.
type BaseTask struct {
	id        string
	taskType  TaskType
	status    TaskStatus
	mu        sync.Mutex
	cancel    context.CancelFunc
	result    *TaskResult
	done      chan struct{}
	startedAt time.Time
	// Notified tracks whether the user has been notified about task completion.
	// Mirrors the reference's TaskStateBase.notified field.
	Notified bool
	// ToolUseID is the tool_use ID that spawned this task.
	ToolUseID string
	// description is a human-readable description of what the task does.
	// Mirrors the reference's TaskStateBase.description field.
	description string
	// outputFile is the path to disk file for persisting long output.
	// Mirrors the reference's TaskStateBase.outputFile field.
	outputFile string
	// outputOffset tracks the current write position in the output file.
	// Mirrors the reference's TaskStateBase.outputOffset field.
	outputOffset int64
	// totalPausedMs tracks accumulated pause time in milliseconds.
	// Mirrors the reference's TaskStateBase.totalPausedMs field.
	totalPausedMs int64
}

// taskTypePrefix returns the single-char ID prefix for a task type.
// Mirrors the reference's type-prefixed ID alphabet.
func taskTypePrefix(t TaskType) string {
	switch t {
	case TaskTypeShell:
		return "b"
	case TaskTypeAgent:
		return "a"
	case TaskTypeRemote:
		return "r"
	case TaskTypeTeammate:
		return "t"
	case TaskTypeWorkflow:
		return "w"
	case TaskTypeMonitor:
		return "m"
	case TaskTypeDream:
		return "d"
	default:
		return "x"
	}
}

// base36Alphabet is the character set for task ID encoding.
// Matches the reference implementation's base-36 alphabet.
const base36Alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

// generateTaskID creates a type-prefixed random task ID using base-36 encoding.
// Produces shorter IDs than hex (e.g., "b1k7m3x9q2" vs "b3a7f2e1c9d04b8a").
// Mirrors the reference's randomBytes → base36 encoding.
func generateTaskID(t TaskType) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	// Convert bytes to a big.Int, then encode as base-36.
	n := new(big.Int).SetBytes(b)
	base := big.NewInt(36)
	mod := new(big.Int)
	var result []byte
	for n.Sign() > 0 {
		n.DivMod(n, base, mod)
		result = append(result, base36Alphabet[mod.Int64()])
	}
	// Pad to at least 9 chars for consistent length.
	for len(result) < 9 {
		result = append(result, '0')
	}
	return taskTypePrefix(t) + string(result)
}

func newBaseTask(taskType TaskType) BaseTask {
	return BaseTask{
		id:       generateTaskID(taskType),
		taskType: taskType,
		status:   TaskStatusPending,
		done:     make(chan struct{}),
	}
}

func (t *BaseTask) ID() string {
	return t.id
}

func (t *BaseTask) Type() TaskType {
	return t.taskType
}

func (t *BaseTask) Status() TaskStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status
}

func (t *BaseTask) Result() *TaskResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.result
}

func (t *BaseTask) Wait(ctx context.Context) (*TaskResult, error) {
	select {
	case <-t.done:
		t.mu.Lock()
		defer t.mu.Unlock()
		return t.result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *BaseTask) Cancel() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status != TaskStatusRunning && t.status != TaskStatusPending {
		return fmt.Errorf("task %s cannot be cancelled (status: %s)", t.id, t.status)
	}
	t.status = TaskStatusCancelled
	if t.cancel != nil {
		t.cancel()
	}
	t.result = &TaskResult{
		Error:    context.Canceled,
		ExitCode: -1,
		Duration: time.Since(t.startedAt),
	}
	select {
	case <-t.done:
	default:
		close(t.done)
	}
	return nil
}

// Kill immediately terminates the task and sets status to TaskStatusKilled.
// This is the default implementation; ShellTask overrides with process-group SIGKILL.
// Mirrors the reference's kill() which always uses immediate termination.
func (t *BaseTask) Kill() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if IsTerminalTaskStatus(t.status) {
		return nil // Already done.
	}
	t.status = TaskStatusKilled
	if t.cancel != nil {
		t.cancel()
	}
	t.result = &TaskResult{
		Error:    fmt.Errorf("task killed"),
		ExitCode: 137, // SIGKILL exit code convention
		Duration: time.Since(t.startedAt),
	}
	select {
	case <-t.done:
	default:
		close(t.done)
	}
	return nil
}

// Description returns the human-readable task description.
func (t *BaseTask) Description() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.description
}

// SetDescription sets the human-readable task description.
func (t *BaseTask) SetDescription(desc string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.description = desc
}

// OutputFile returns the path to the disk output file (empty if in-memory only).
func (t *BaseTask) OutputFile() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.outputFile
}

// SetOutputFile sets the disk output file path and creates the file.
func (t *BaseTask) SetOutputFile(path string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Ensure parent directory exists.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	t.outputFile = path
	t.outputOffset = 0
	return nil
}

// OutputOffset returns the current write offset in the output file.
func (t *BaseTask) OutputOffset() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.outputOffset
}

// AppendOutput writes data to the disk output file if configured,
// otherwise returns without effect (in-memory output is handled via complete()).
// Mirrors the reference's streaming output persistence for long-running tasks.
func (t *BaseTask) AppendOutput(data []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.outputFile == "" {
		return nil
	}
	f, err := os.OpenFile(t.outputFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open output file: %w", err)
	}
	defer f.Close() //nolint:errcheck
	n, err := f.Write(data)
	t.outputOffset += int64(n)
	if err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// TotalPausedMs returns the accumulated pause time in milliseconds.
func (t *BaseTask) TotalPausedMs() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.totalPausedMs
}

// AddPausedMs adds pause time in milliseconds.
func (t *BaseTask) AddPausedMs(ms int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.totalPausedMs += ms
}

// complete is a helper to transition the task to a terminal state.
func (t *BaseTask) complete(output string, err error, exitCode int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status == TaskStatusCancelled || t.status == TaskStatusKilled {
		return
	}
	if err != nil {
		t.status = TaskStatusFailed
	} else {
		t.status = TaskStatusCompleted
	}
	t.result = &TaskResult{
		Output:   output,
		Error:    err,
		ExitCode: exitCode,
		Duration: time.Since(t.startedAt),
	}
	select {
	case <-t.done:
	default:
		close(t.done)
	}
}

// DreamTask represents a background analysis/thinking task.
// In the reference implementation, this corresponds to auto-dream
// (memory consolidation subagent). Currently a placeholder pending
// model call wiring.
type DreamTask struct {
	BaseTask
	Prompt string
	Model  string
}

// NewDreamTask creates a new dream (background thinking) task.
func NewDreamTask(prompt, model string) *DreamTask {
	return &DreamTask{
		BaseTask: newBaseTask(TaskTypeDream),
		Prompt:   prompt,
		Model:    model,
	}
}

// Start begins dream task execution. Calls the model for background
// thinking/consolidation if DreamModelFn is configured.
func (t *DreamTask) Start(ctx context.Context) error {
	t.mu.Lock()
	if t.status != TaskStatusPending {
		t.mu.Unlock()
		return fmt.Errorf("dream task %s already started (status: %s)", t.id, t.status)
	}
	ctx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	t.status = TaskStatusRunning
	t.startedAt = time.Now()
	t.mu.Unlock()

	go func() {
		defer cancel()

		// If no model function is wired, complete with a note.
		if DreamModelFn == nil {
			t.complete(
				fmt.Sprintf("[dream] prompt=%q model=%s (no model function configured)", t.Prompt, t.Model),
				nil, 0,
			)
			return
		}

		// Call the model for consolidation/thinking.
		result, err := DreamModelFn(ctx, t.Prompt, t.Model)
		if err != nil {
			// Check if cancelled.
			if ctx.Err() != nil {
				t.mu.Lock()
				if t.status == TaskStatusRunning {
					t.status = TaskStatusCancelled
					t.result = &TaskResult{
						Error:    ctx.Err(),
						ExitCode: -1,
						Duration: time.Since(t.startedAt),
					}
					select {
					case <-t.done:
					default:
						close(t.done)
					}
				}
				t.mu.Unlock()
				return
			}
			t.complete("", err, 1)
			return
		}

		t.complete(result, nil, 0)
	}()
	return nil
}

// ShellTask represents a shell command execution task.
// This mirrors LocalShellTask from the reference implementation.
type ShellTask struct {
	BaseTask
	Command string
	WorkDir string
	Env     map[string]string
	Timeout time.Duration
	// AgentID tracks which agent spawned this task (for orphan cleanup).
	// Mirrors the reference's LocalShellTaskState.agentId field.
	AgentID string
	// cmd holds the running process reference for SIGKILL delivery.
	cmd *exec.Cmd
}

// NewShellTask creates a new shell command task.
func NewShellTask(command, workDir string) *ShellTask {
	return &ShellTask{
		BaseTask: newBaseTask(TaskTypeShell),
		Command:  command,
		WorkDir:  workDir,
	}
}

// Start begins shell command execution in a background goroutine.
func (t *ShellTask) Start(ctx context.Context) error {
	t.mu.Lock()
	if t.status != TaskStatusPending {
		t.mu.Unlock()
		return fmt.Errorf("shell task %s already started (status: %s)", t.id, t.status)
	}
	t.status = TaskStatusRunning
	t.startedAt = time.Now()
	t.mu.Unlock()

	go func() {
		var execCtx context.Context
		var cancelTimeout context.CancelFunc
		if t.Timeout > 0 {
			execCtx, cancelTimeout = context.WithTimeout(ctx, t.Timeout)
		} else {
			execCtx, cancelTimeout = context.WithCancel(ctx)
		}

		t.mu.Lock()
		t.cancel = cancelTimeout
		t.mu.Unlock()

		defer cancelTimeout()

		cmd := exec.CommandContext(execCtx, "sh", "-c", t.Command)
		// Create a new process group so Kill() can SIGKILL the entire tree.
		setProcessGroup(cmd)
		if t.WorkDir != "" {
			cmd.Dir = t.WorkDir
		}
		if len(t.Env) > 0 {
			cmd.Env = append(cmd.Environ(), mapToEnvSlice(t.Env)...)
		}

		t.mu.Lock()
		t.cmd = cmd
		t.mu.Unlock()

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		exitCode := 0
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}

		output := stdout.String()
		if stderr.Len() > 0 {
			if output != "" {
				output += "\n"
			}
			output += stderr.String()
		}

		// If output is large and outputFile is configured, persist to disk.
		// Threshold: 64KB mirrors the reference's large-output offloading.
		const outputDiskThreshold = 64 * 1024
		if len(output) > outputDiskThreshold && t.outputFile != "" {
			_ = t.AppendOutput([]byte(output))
			// Keep a truncated preview in memory.
			output = output[:outputDiskThreshold] + fmt.Sprintf("\n... [truncated, full output in %s]", t.outputFile)
		}

		if err != nil && execCtx.Err() == context.Canceled {
			// Task was cancelled via context
			t.mu.Lock()
			if t.status == TaskStatusRunning {
				t.status = TaskStatusCancelled
				t.result = &TaskResult{
					Output:   output,
					Error:    context.Canceled,
					ExitCode: -1,
					Duration: time.Since(t.startedAt),
				}
				select {
				case <-t.done:
				default:
					close(t.done)
				}
			}
			t.mu.Unlock()
			return
		}

		var resultErr error
		if exitCode != 0 {
			resultErr = fmt.Errorf("command exited with code %d", exitCode)
		}
		t.complete(output, resultErr, exitCode)
	}()
	return nil
}

// Kill overrides BaseTask.Kill() to send SIGKILL to the entire process group.
// Sets Notified=true to suppress the "exit code 137" notification noise.
// Mirrors the reference's treeKill(pid, 'SIGKILL') behavior.
func (t *ShellTask) Kill() error {
	t.mu.Lock()
	if IsTerminalTaskStatus(t.status) {
		t.mu.Unlock()
		return nil
	}
	t.status = TaskStatusKilled
	t.Notified = true // Suppress "exit code 137" notification noise.
	cmd := t.cmd
	t.mu.Unlock()

	// Send SIGKILL to the process group (negative PID = process group).
	if cmd != nil && cmd.Process != nil {
		killProcessGroup(cmd)
	}

	// Cancel the context to unblock cmd.Wait().
	t.mu.Lock()
	if t.cancel != nil {
		t.cancel()
	}
	t.result = &TaskResult{
		Error:    fmt.Errorf("task killed"),
		ExitCode: 137, // SIGKILL exit code
		Duration: time.Since(t.startedAt),
	}
	select {
	case <-t.done:
	default:
		close(t.done)
	}
	t.mu.Unlock()
	return nil
}

// AgentTask represents a sub-agent execution task.
// This mirrors LocalAgentTask from the reference implementation.
// Currently a placeholder pending sub-agent executor wiring.
type AgentTask struct {
	BaseTask
	AgentPrompt  string
	MaxTurns     int
	AllowedTools []string
	Model        string
}

// NewAgentTask creates a new sub-agent task.
func NewAgentTask(prompt, model string) *AgentTask {
	return &AgentTask{
		BaseTask:    newBaseTask(TaskTypeAgent),
		AgentPrompt: prompt,
		Model:       model,
		MaxTurns:    0,
	}
}

// Start begins agent task execution. If AgentExecutorFn is configured, it
// spawns a real sub-agent; otherwise completes with a placeholder message.
// Mirrors LocalAgentTask from the reference implementation.
func (t *AgentTask) Start(ctx context.Context) error {
	t.mu.Lock()
	if t.status != TaskStatusPending {
		t.mu.Unlock()
		return fmt.Errorf("agent task %s already started (status: %s)", t.id, t.status)
	}
	ctx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	t.status = TaskStatusRunning
	t.startedAt = time.Now()
	t.mu.Unlock()

	go func() {
		defer cancel()

		// If no executor function is wired, complete with a placeholder.
		if AgentExecutorFn == nil {
			t.complete(
				fmt.Sprintf("[agent] prompt=%q model=%s maxTurns=%d tools=%v (executor not configured)",
					t.AgentPrompt, t.Model, t.MaxTurns, t.AllowedTools),
				nil,
				0,
			)
			return
		}

		// Execute the sub-agent.
		result, err := AgentExecutorFn(ctx, t.AgentPrompt, t.Model, t.MaxTurns, t.AllowedTools)
		if err != nil {
			// Check if cancelled.
			if ctx.Err() != nil {
				t.mu.Lock()
				if t.status == TaskStatusRunning {
					t.status = TaskStatusCancelled
					t.result = &TaskResult{
						Error:    ctx.Err(),
						ExitCode: -1,
						Duration: time.Since(t.startedAt),
					}
					select {
					case <-t.done:
					default:
						close(t.done)
					}
				}
				t.mu.Unlock()
				return
			}
			t.complete("", err, 1)
			return
		}

		t.complete(result, nil, 0)
	}()
	return nil
}

// TaskManager manages concurrent task execution with a configurable
// concurrency limit. It provides Submit/Get/Cancel/List/WaitAll operations.
type TaskManager struct {
	tasks         map[string]Task
	mu            sync.RWMutex
	maxConcurrent int
	running       int
}

// NewTaskManager creates a TaskManager with the specified maximum concurrent tasks.
func NewTaskManager(maxConcurrent int) *TaskManager {
	if maxConcurrent <= 0 {
		maxConcurrent = 8
	}
	return &TaskManager{
		tasks:         make(map[string]Task),
		maxConcurrent: maxConcurrent,
	}
}

// Submit registers a task and starts it if the concurrency limit allows.
func (m *TaskManager) Submit(task Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running >= m.maxConcurrent {
		return fmt.Errorf("task manager: max concurrent tasks reached (%d)", m.maxConcurrent)
	}

	m.tasks[task.ID()] = task
	m.running++

	go func() {
		ctx := context.Background()
		_ = task.Start(ctx)
		// Wait for completion to decrement running count.
		_, _ = task.Wait(context.Background())
		m.mu.Lock()
		m.running--
		m.mu.Unlock()
	}()

	return nil
}

// Get returns a task by ID.
func (m *TaskManager) Get(id string) (Task, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tasks[id]
	return t, ok
}

// Cancel cancels a task by ID.
func (m *TaskManager) Cancel(id string) error {
	m.mu.RLock()
	task, ok := m.tasks[id]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("task manager: task %q not found", id)
	}
	return task.Cancel()
}

// CancelAll cancels all running or pending tasks.
func (m *TaskManager) CancelAll() error {
	m.mu.RLock()
	tasks := make([]Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		tasks = append(tasks, t)
	}
	m.mu.RUnlock()

	var errs []string
	for _, t := range tasks {
		status := t.Status()
		if status == TaskStatusRunning || status == TaskStatusPending {
			if err := t.Cancel(); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("task manager: cancel errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// List returns all registered tasks.
func (m *TaskManager) List() []Task {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		result = append(result, t)
	}
	return result
}

// WaitAll blocks until all tasks have completed or the context is cancelled.
func (m *TaskManager) WaitAll(ctx context.Context) error {
	m.mu.RLock()
	tasks := make([]Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		tasks = append(tasks, t)
	}
	m.mu.RUnlock()

	for _, t := range tasks {
		if _, err := t.Wait(ctx); err != nil {
			return err
		}
	}
	return nil
}

// StopTask stops a running task by ID using Kill() (immediate SIGKILL for shell tasks).
// The timeout parameter is how long to wait for the task to exit after the kill signal.
// Returns *StopTaskResult on success, *StopTaskError for validation failures.
// Mirrors src/tasks/stopTask.ts — explicit stop always uses kill semantics.
func (m *TaskManager) StopTask(id string, timeout time.Duration) (*StopTaskResult, error) {
	m.mu.RLock()
	task, ok := m.tasks[id]
	m.mu.RUnlock()

	if !ok {
		return nil, &StopTaskError{
			Message: fmt.Sprintf("no task found with ID: %s", id),
			Code:    StopTaskNotFound,
		}
	}

	status := task.Status()
	if IsTerminalTaskStatus(status) {
		return nil, nil // Already done.
	}
	if status != TaskStatusRunning {
		return nil, &StopTaskError{
			Message: fmt.Sprintf("task %s is not running (status: %s)", id, status),
			Code:    StopTaskNotRunning,
		}
	}

	// Kill (not cancel) — immediate termination, status → killed.
	if err := task.Kill(); err != nil {
		return nil, err
	}

	// Wait with timeout for the process to exit.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, _ = task.Wait(ctx)

	// Evict disk output after kill.
	m.EvictTaskOutput(id)

	// Build result — command for shell tasks, description for others.
	result := &StopTaskResult{
		TaskID:   id,
		TaskType: task.Type(),
	}
	switch t := task.(type) {
	case *ShellTask:
		result.Command = t.Command
	default:
		if bt, ok := task.(*AgentTask); ok {
			result.Command = bt.Description()
		} else if bt, ok := task.(*DreamTask); ok {
			result.Command = bt.Description()
		}
	}
	return result, nil
}

// KillAllShellTasks kills all running shell tasks with SIGKILL.
// Mirrors src/tasks/LocalShellTask/killShellTasks.ts.
func (m *TaskManager) KillAllShellTasks() {
	m.mu.RLock()
	tasks := make([]Task, 0)
	for _, t := range m.tasks {
		if t.Type() == TaskTypeShell && !IsTerminalTaskStatus(t.Status()) {
			tasks = append(tasks, t)
		}
	}
	m.mu.RUnlock()

	for _, t := range tasks {
		_ = t.Kill()
		m.EvictTaskOutput(t.ID())
	}
}

// KillShellTasksForAgent kills all running shell tasks spawned by the given agent.
// Called from the sub-agent executor's finally block to prevent zombie processes
// from outliving the agent that started them.
// Mirrors src/tasks/LocalShellTask/killShellTasks.ts:killShellTasksForAgent().
func (m *TaskManager) KillShellTasksForAgent(agentID string) {
	if agentID == "" {
		return
	}
	m.mu.RLock()
	var targets []*ShellTask
	for _, t := range m.tasks {
		st, ok := t.(*ShellTask)
		if ok && st.AgentID == agentID && st.Status() == TaskStatusRunning {
			targets = append(targets, st)
		}
	}
	m.mu.RUnlock()

	for _, st := range targets {
		_ = st.Kill()
		m.EvictTaskOutput(st.ID())
	}
}

// StopTaskResult holds the result of a successful StopTask call.
// Mirrors the reference's stopTask return value.
type StopTaskResult struct {
	TaskID   string
	TaskType TaskType
	Command  string // shell command or task description
}

// ---------------------------------------------------------------------------
// StopTaskError — typed error for stop validation failures.
// Mirrors src/tasks/stopTask.ts StopTaskError with error codes.
// ---------------------------------------------------------------------------

// StopTaskErrorCode classifies why a task cannot be stopped.
type StopTaskErrorCode string

const (
	StopTaskNotFound        StopTaskErrorCode = "not_found"
	StopTaskNotRunning      StopTaskErrorCode = "not_running"
	StopTaskUnsupportedType StopTaskErrorCode = "unsupported_type"
)

// StopTaskError is returned when a stop request fails validation.
type StopTaskError struct {
	Message string
	Code    StopTaskErrorCode
}

func (e *StopTaskError) Error() string {
	return e.Message
}

// EvictTaskOutput deletes the on-disk output file for a task and resets the
// output fields. Called after Kill to reclaim disk space.
// Mirrors the reference's evictTaskOutput(taskId) from utils/task/diskOutput.ts.
func (m *TaskManager) EvictTaskOutput(id string) {
	m.mu.RLock()
	task, ok := m.tasks[id]
	m.mu.RUnlock()
	if !ok {
		return
	}

	// Access the BaseTask to clear output fields.
	switch t := task.(type) {
	case *ShellTask:
		t.mu.Lock()
		path := t.outputFile
		t.outputFile = ""
		t.outputOffset = 0
		t.mu.Unlock()
		if path != "" {
			_ = os.Remove(path)
		}
	case *AgentTask:
		t.mu.Lock()
		path := t.outputFile
		t.outputFile = ""
		t.outputOffset = 0
		t.mu.Unlock()
		if path != "" {
			_ = os.Remove(path)
		}
	case *DreamTask:
		t.mu.Lock()
		path := t.outputFile
		t.outputFile = ""
		t.outputOffset = 0
		t.mu.Unlock()
		if path != "" {
			_ = os.Remove(path)
		}
	}
}

// Cleanup removes completed/failed/cancelled tasks older than the given age.
func (m *TaskManager) Cleanup(maxAge time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	for id, t := range m.tasks {
		if !IsTerminalTaskStatus(t.Status()) {
			continue
		}
		// Use BaseTask startedAt + duration as approximate end time.
		if bt, ok := t.(*ShellTask); ok && bt.startedAt.Add(maxAge).Before(cutoff) {
			delete(m.tasks, id)
		} else if bt, ok := t.(*AgentTask); ok && bt.startedAt.Add(maxAge).Before(cutoff) {
			delete(m.tasks, id)
		} else if bt, ok := t.(*DreamTask); ok && bt.startedAt.Add(maxAge).Before(cutoff) {
			delete(m.tasks, id)
		}
	}
}

// mapToEnvSlice converts a map to KEY=VALUE environment variable slice.
func mapToEnvSlice(env map[string]string) []string {
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}
