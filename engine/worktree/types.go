package worktree

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const RecordVersion = 1

type SourceMode string

const (
	SourceRequireClean SourceMode = "require_clean"
	SourceIgnoreDirty  SourceMode = "ignore_dirty"
)

type State string

const (
	StateCreating      State = "Creating"
	StateReady         State = "Ready"
	StateRetained      State = "Retained"
	StateRemoving      State = "Removing"
	StateRemoved       State = "Removed"
	StateFailed        State = "Failed"
	StateCleanupFailed State = "CleanupFailed"
)

type ErrorCategory string

const (
	ErrorCategoryCancelled   ErrorCategory = "cancelled"
	ErrorCategoryTimeout     ErrorCategory = "timeout"
	ErrorCategoryGit         ErrorCategory = "git"
	ErrorCategoryValidation  ErrorCategory = "validation"
	ErrorCategoryPersistence ErrorCategory = "persistence"
	ErrorCategoryCollision   ErrorCategory = "collision"
	ErrorCategoryDirty       ErrorCategory = "dirty"
	ErrorCategoryUnknown     ErrorCategory = "unknown"
	ErrorCategoryCleanup     ErrorCategory = "cleanup"
	ErrorCategoryInterrupted ErrorCategory = "interrupted"
)

// RecoveryDisposition describes restart-time availability without extending
// the durable Git lifecycle state machine. Discovery is metadata-only; an
// explicit recovery or cleanup operation must still perform fresh admission.
type RecoveryDisposition string

const (
	RecoveryInspectOnly RecoveryDisposition = "inspect_only"
	RecoveryPending     RecoveryDisposition = "recovery_pending"
	RecoveryUnavailable RecoveryDisposition = "unavailable"
	RecoveryTerminal    RecoveryDisposition = "terminal"
)

// RecoveryRecord is the bounded restart projection of one durable record.
// Diagnostic never grants mutation authority.
type RecoveryRecord struct {
	Record      Record
	Disposition RecoveryDisposition
	Diagnostic  string
}

// DiscoveryDiagnostic reports a rejected record file without interpreting it
// as lifecycle authority.
type DiscoveryDiagnostic struct {
	RecordID string
	Message  string
}

// Discovery is deterministic and bounded by the versioned record directory.
type Discovery struct {
	Records     []RecoveryRecord
	Diagnostics []DiscoveryDiagnostic
}

type OwnerKind string

const OwnerAgent OwnerKind = "agent"

// Owner is the durable mutation authority and runtime lineage for one
// worktree. Display names are deliberately excluded from ownership.
type Owner struct {
	Kind            OwnerKind `json:"kind"`
	ID              string    `json:"id"`
	SessionID       string    `json:"session_id"`
	ThreadID        string    `json:"thread_id"`
	ParentSessionID string    `json:"parent_session_id,omitempty"`
	ParentThreadID  string    `json:"parent_thread_id,omitempty"`
	ParentAgentID   string    `json:"parent_agent_id,omitempty"`
	ParentToolUseID string    `json:"parent_tool_use_id,omitempty"`
}

func (o Owner) Validate() error {
	if o.Kind != OwnerAgent {
		return fmt.Errorf("worktree: unsupported owner kind %q", o.Kind)
	}
	if strings.TrimSpace(o.ID) == "" {
		return errors.New("worktree: owner ID is required")
	}
	if strings.TrimSpace(o.SessionID) == "" {
		return errors.New("worktree: owner session ID is required")
	}
	if strings.TrimSpace(o.ThreadID) == "" {
		return errors.New("worktree: owner thread ID is required")
	}
	return nil
}

func (o Owner) Equal(other Owner) bool {
	return o == other
}

// DirtyReport is a bounded, durable handoff. Patch may omit ignored or
// untracked content; PatchTruncated makes that loss explicit.
type DirtyReport struct {
	Dirty          bool     `json:"dirty"`
	NewCommits     int      `json:"new_commits,omitempty"`
	ChangedFiles   []string `json:"changed_files,omitempty"`
	Truncated      bool     `json:"truncated,omitempty"`
	Patch          string   `json:"patch,omitempty"`
	PatchTruncated bool     `json:"patch_truncated,omitempty"`
}

func CloneDirtyReport(report *DirtyReport) *DirtyReport {
	if report == nil {
		return nil
	}
	clone := *report
	clone.ChangedFiles = append([]string(nil), report.ChangedFiles...)
	return &clone
}

// Record is the versioned durable authority for one managed worktree.
type Record struct {
	Version            int           `json:"version"`
	ID                 string        `json:"id"`
	Owner              Owner         `json:"owner"`
	RepositoryIdentity string        `json:"repository_identity,omitempty"`
	RepoRoot           string        `json:"repo_root"`
	Path               string        `json:"path"`
	Branch             string        `json:"branch"`
	BaseCommit         string        `json:"base_commit,omitempty"`
	State              State         `json:"state"`
	SourceDirtyReport  *DirtyReport  `json:"source_dirty_report,omitempty"`
	ResultDirtyReport  *DirtyReport  `json:"result_dirty_report,omitempty"`
	Revision           uint64        `json:"revision"`
	CreatedAt          time.Time     `json:"created_at"`
	UpdatedAt          time.Time     `json:"updated_at"`
	LastErrorCategory  ErrorCategory `json:"last_error_category,omitempty"`
	LastError          string        `json:"last_error,omitempty"`
}

func (r Record) Validate() error {
	if r.Version != RecordVersion {
		return fmt.Errorf("worktree: unsupported record version %d", r.Version)
	}
	if strings.TrimSpace(r.ID) == "" {
		return errors.New("worktree: record ID is required")
	}
	if err := r.Owner.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.RepoRoot) == "" {
		return errors.New("worktree: repository root is required")
	}
	if strings.TrimSpace(r.Path) == "" {
		return errors.New("worktree: managed path is required")
	}
	if strings.TrimSpace(r.Branch) == "" {
		return errors.New("worktree: managed branch is required")
	}
	if !knownState(r.State) {
		return fmt.Errorf("worktree: unknown state %q", r.State)
	}
	if r.Revision == 0 {
		return errors.New("worktree: record revision is required")
	}
	if r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
		return errors.New("worktree: record timestamps are required")
	}
	switch r.State {
	case StateReady, StateRetained, StateRemoving, StateRemoved, StateCleanupFailed:
		if r.RepositoryIdentity == "" || r.BaseCommit == "" {
			return fmt.Errorf(
				"worktree: %s record has incomplete repository identity",
				r.State,
			)
		}
	}
	return nil
}

func knownState(state State) bool {
	switch state {
	case StateCreating,
		StateReady,
		StateRetained,
		StateRemoving,
		StateRemoved,
		StateFailed,
		StateCleanupFailed:
		return true
	default:
		return false
	}
}

func validTransition(from, to State) bool {
	switch from {
	case "":
		return to == StateCreating
	case StateCreating:
		return to == StateReady || to == StateFailed
	case StateReady:
		return to == StateRetained || to == StateRemoving
	case StateRetained:
		return to == StateRemoving
	case StateRemoving:
		return to == StateRemoved || to == StateCleanupFailed
	case StateCleanupFailed:
		return to == StateRemoving
	default:
		return false
	}
}

type Transition struct {
	From   State
	Record Record
}

type CreateRequest struct {
	Owner      Owner
	SourceDir  string
	BaseRef    string
	SourceMode SourceMode
}
