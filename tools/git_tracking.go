package tools

import (
	"sync"
	"time"
)

// GitOperationType identifies the type of git operation.
type GitOperationType string

const (
	GitOpCommit   GitOperationType = "commit"
	GitOpPush     GitOperationType = "push"
	GitOpCheckout GitOperationType = "checkout"
	GitOpRebase   GitOperationType = "rebase"
	GitOpMerge    GitOperationType = "merge"
	GitOpStash    GitOperationType = "stash"
	GitOpReset    GitOperationType = "reset"
)

// GitOperation records a git operation performed by a tool.
type GitOperation struct {
	Type      GitOperationType `json:"type"`
	CommitID  string           `json:"commitId,omitempty"`
	Branch    string           `json:"branch,omitempty"`
	ToolUseID string           `json:"toolUseId"`
	Timestamp time.Time        `json:"timestamp"`
}

// GitOperationTracker tracks git operations performed during the session.
// Used for undo/rewind support and session metadata.
//
// Reference: src/tools/shared/gitOperationTracking.ts (277 lines)
type GitOperationTracker struct {
	mu         sync.Mutex
	operations []GitOperation
	listeners  []func(GitOperation)
}

// NewGitOperationTracker creates a new tracker.
func NewGitOperationTracker() *GitOperationTracker {
	return &GitOperationTracker{}
}

// Record records a git operation.
func (t *GitOperationTracker) Record(op GitOperation) {
	t.mu.Lock()
	t.operations = append(t.operations, op)
	listeners := make([]func(GitOperation), len(t.listeners))
	copy(listeners, t.listeners)
	t.mu.Unlock()

	for _, cb := range listeners {
		cb(op)
	}
}

// GetOperations returns all recorded operations.
func (t *GitOperationTracker) GetOperations() []GitOperation {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make([]GitOperation, len(t.operations))
	copy(result, t.operations)
	return result
}

// GetLastCommitID returns the most recent commit ID, if any.
func (t *GitOperationTracker) GetLastCommitID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.operations) - 1; i >= 0; i-- {
		if t.operations[i].Type == GitOpCommit && t.operations[i].CommitID != "" {
			return t.operations[i].CommitID
		}
	}
	return ""
}

// OnOperation registers a listener for new operations.
func (t *GitOperationTracker) OnOperation(cb func(GitOperation)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.listeners = append(t.listeners, cb)
}

// Clear removes all recorded operations.
func (t *GitOperationTracker) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.operations = nil
}
