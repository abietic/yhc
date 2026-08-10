package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/abietic/yhc/internal/identity"
)

const (
	defaultOperationTimeout = 30 * time.Second
	finalizeTimeout         = 5 * time.Second
	maxChangedFiles         = 64
	maxChangedFileBytes     = 512
	maxPatchBytes           = 64 * 1024
	managedBranchPrefix     = "yhc/worktree/"
)

var operationLockRegistry sync.Map

type TransitionSink func(context.Context, Transition) error

type ServiceConfig struct {
	ProjectRoot      string
	OperationTimeout time.Duration
	Git              Git
	Clock            func() time.Time
	ID               func() string
	TransitionSink   TransitionSink
}

// Service owns durable worktree lifecycle records and Git side effects. It is
// independent of AgentRunner; P18.1 binds Agent execution to this API.
type Service struct {
	projectRoot      string
	managedRoot      string
	store            *Store
	operationTimeout time.Duration
	git              Git
	clock            func() time.Time
	id               func() string
	transitionSink   TransitionSink
}

func NewService(config ServiceConfig) *Service {
	projectRoot, err := canonicalConfiguredRoot(config.ProjectRoot)
	if err != nil {
		projectRoot = filepath.Clean(config.ProjectRoot)
	}
	timeout := config.OperationTimeout
	if timeout <= 0 {
		timeout = defaultOperationTimeout
	}
	git := config.Git
	if git == nil {
		git = CommandGit{}
	}
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	id := config.ID
	if id == nil {
		id = uuid.NewString
	}
	root := filepath.Join(projectRoot, identity.ProjectDirName, "worktrees", "v1")
	return &Service{
		projectRoot:      projectRoot,
		managedRoot:      filepath.Join(root, "trees"),
		store:            NewStore(filepath.Join(root, "records")),
		operationTimeout: timeout,
		git:              git,
		clock:            clock,
		id:               id,
		transitionSink:   config.TransitionSink,
	}
}

func (s *Service) StoreRoot() string {
	if s == nil || s.store == nil {
		return ""
	}
	return s.store.Root()
}

func (s *Service) ManagedRoot() string {
	if s == nil {
		return ""
	}
	return s.managedRoot
}

func (s *Service) Create(
	ctx context.Context,
	request CreateRequest,
) (Record, error) {
	if s == nil {
		return Record{}, errors.New("worktree: nil lifecycle service")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := request.Owner.Validate(); err != nil {
		return Record{}, err
	}
	sourceDir := strings.TrimSpace(request.SourceDir)
	if sourceDir == "" {
		sourceDir = s.projectRoot
	}
	baseRef := strings.TrimSpace(request.BaseRef)
	if baseRef == "" {
		baseRef = "HEAD"
	}
	sourceMode := request.SourceMode
	if sourceMode == "" {
		sourceMode = SourceRequireClean
	}
	switch sourceMode {
	case SourceRequireClean, SourceIgnoreDirty:
	default:
		return Record{}, fmt.Errorf(
			"worktree: unsupported source mode %q",
			sourceMode,
		)
	}
	id := strings.TrimSpace(s.id())
	if err := validateRecordID(id); err != nil {
		return Record{}, err
	}
	now := s.clock().UTC()
	record := Record{
		Version:   RecordVersion,
		ID:        id,
		Owner:     request.Owner,
		RepoRoot:  s.projectRoot,
		Path:      filepath.Join(s.managedRoot, id),
		Branch:    managedBranchPrefix + id,
		State:     StateCreating,
		Revision:  1,
		CreatedAt: now,
		UpdatedAt: now,
	}

	unlock := operationLock(s.store.recordPath(id))
	defer unlock()
	if err := secureMkdirAll(s.projectRoot, s.store.Root(), 0o700); err != nil {
		return Record{}, categorize(
			ErrorCategoryValidation,
			"prepare durable record directory",
			err,
		)
	}
	if err := secureMkdirAll(s.projectRoot, s.managedRoot, 0o700); err != nil {
		return Record{}, categorize(
			ErrorCategoryValidation,
			"prepare managed worktree directory",
			err,
		)
	}
	created, err := s.store.Create(context.Background(), record)
	if err != nil {
		return Record{}, categorize(ErrorCategoryPersistence, "persist Creating record", err)
	}
	if err := s.emit(context.Background(), Transition{Record: created}); err != nil {
		return s.failCreate(created.ID, ErrorCategoryValidation, "runtime transition rejected", err)
	}

	operationCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
	defer cancel()
	projectRepository, err := s.git.Repository(operationCtx, s.projectRoot)
	if err != nil {
		return s.failCreate(id, categoryForContext(operationCtx, ctx, ErrorCategoryGit), "resolve project repository", err)
	}
	sourceRepository, err := s.git.Repository(operationCtx, sourceDir)
	if err != nil {
		return s.failCreate(id, categoryForContext(operationCtx, ctx, ErrorCategoryGit), "resolve source repository", err)
	}
	if !samePath(projectRepository.CommonDir, sourceRepository.CommonDir) {
		return s.failCreate(
			id,
			ErrorCategoryValidation,
			"source repository identity does not match project",
			nil,
		)
	}
	baseCommit, err := s.git.ResolveCommit(operationCtx, sourceDir, baseRef)
	if err != nil {
		return s.failCreate(id, categoryForContext(operationCtx, ctx, ErrorCategoryGit), "resolve base commit", err)
	}
	sourceReport, sourceCategory, sourceErr := s.inspectDirtyReport(
		operationCtx,
		ctx,
		sourceRepository.Root,
		baseCommit,
		false,
		s.sourceDirtyExcludes(sourceRepository.Root),
	)
	if !pathContained(s.managedRoot, record.Path) {
		return s.failCreate(id, ErrorCategoryValidation, "managed path escapes project root", nil)
	}
	if _, err := os.Lstat(record.Path); err == nil {
		return s.failCreate(id, ErrorCategoryCollision, "managed path already exists", nil)
	} else if !errors.Is(err, os.ErrNotExist) {
		return s.failCreate(id, ErrorCategoryValidation, "inspect managed path", err)
	}
	if commit, exists, branchErr := s.git.BranchCommit(
		operationCtx,
		projectRepository.Root,
		record.Branch,
	); branchErr != nil {
		return s.failCreate(id, categoryForContext(operationCtx, ctx, ErrorCategoryGit), "inspect managed branch", branchErr)
	} else if exists {
		return s.failCreate(
			id,
			ErrorCategoryCollision,
			fmt.Sprintf("managed branch already exists at %s", commit),
			nil,
		)
	}

	prepared, err := s.transition(
		id,
		StateCreating,
		StateCreating,
		func(next *Record) {
			next.RepositoryIdentity = projectRepository.CommonDir
			next.RepoRoot = projectRepository.Root
			next.BaseCommit = baseCommit
			next.SourceDirtyReport = CloneDirtyReport(sourceReport)
		},
	)
	if err != nil {
		return s.failCreate(id, ErrorCategoryPersistence, "persist resolved identity", err)
	}
	record = prepared
	if sourceErr != nil {
		return s.failCreate(
			id,
			sourceCategory,
			"inspect source worktree changes",
			sourceErr,
		)
	}
	if sourceReport != nil &&
		sourceReport.Dirty &&
		sourceMode != SourceIgnoreDirty {
		return s.failCreate(
			id,
			ErrorCategoryDirty,
			"source worktree contains changes; set worktree_source to ignore_dirty to start from committed HEAD and record the omitted files",
			nil,
		)
	}
	if err := s.git.AddWorktree(
		operationCtx,
		record.RepoRoot,
		record.Path,
		record.Branch,
		record.BaseCommit,
	); err != nil {
		return s.failCreate(id, categoryForContext(operationCtx, ctx, ErrorCategoryGit), "create worktree", err)
	}
	inspection, err := s.git.InspectWorktree(operationCtx, record.Path)
	if err != nil {
		return s.failCreate(id, categoryForContext(operationCtx, ctx, ErrorCategoryGit), "validate created worktree", err)
	}
	if err := validateReadyInspection(record, inspection); err != nil {
		return s.failCreate(id, ErrorCategoryValidation, "validate created worktree", err)
	}
	branchCommit, exists, err := s.git.BranchCommit(
		operationCtx,
		record.RepoRoot,
		record.Branch,
	)
	if err != nil {
		return s.failCreate(id, categoryForContext(operationCtx, ctx, ErrorCategoryGit), "validate managed branch", err)
	}
	if !exists || branchCommit != record.BaseCommit {
		return s.failCreate(
			id,
			ErrorCategoryValidation,
			"managed branch does not point to the recorded base",
			nil,
		)
	}
	ready, err := s.transition(id, StateCreating, StateReady, nil)
	if err != nil {
		return Record{}, categorize(ErrorCategoryPersistence, "persist Ready record", err)
	}
	return ready, nil
}

// Remove verifies ownership and clean state before cleanup. CleanupFailed
// records can be retried without discarding their durable metadata.
func (s *Service) Remove(
	ctx context.Context,
	id string,
	owner Owner,
) (Record, error) {
	if s == nil {
		return Record{}, errors.New("worktree: nil lifecycle service")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := owner.Validate(); err != nil {
		return Record{}, err
	}
	unlock := operationLock(s.store.recordPath(id))
	defer unlock()
	record, found, err := s.store.Get(context.Background(), id)
	if err != nil {
		return Record{}, err
	}
	if !found {
		return Record{}, fmt.Errorf("worktree: record %q not found", id)
	}
	if !record.Owner.Equal(owner) {
		return record, errors.New("worktree: cleanup owner does not match durable record")
	}
	if record.State == StateRemoved {
		return record, nil
	}
	if record.State != StateReady &&
		record.State != StateRetained &&
		record.State != StateCleanupFailed {
		return record, fmt.Errorf("worktree: cannot remove record in state %s", record.State)
	}

	operationCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
	defer cancel()
	if record.State == StateCleanupFailed {
		record, err = s.transition(id, StateCleanupFailed, StateRemoving, nil)
		if err != nil {
			return record, err
		}
		return s.finishRemoval(operationCtx, ctx, record)
	}

	clean, report, category, checkErr := s.verifyClean(operationCtx, ctx, record)
	if checkErr != nil || !clean {
		if record.State == StateReady {
			retained, transitionErr := s.transition(
				id,
				StateReady,
				StateRetained,
				func(next *Record) {
					next.LastErrorCategory = category
					next.LastError = boundedError(checkErr)
					next.ResultDirtyReport = CloneDirtyReport(report)
				},
			)
			if transitionErr != nil {
				return record, transitionErr
			}
			record = retained
		}
		if checkErr == nil {
			checkErr = errors.New("worktree contains changes")
		}
		return record, categorize(category, "retain worktree", checkErr)
	}
	removing, err := s.transition(id, record.State, StateRemoving, nil)
	if err != nil {
		return record, err
	}
	return s.finishRemoval(operationCtx, ctx, removing)
}

func (s *Service) Get(ctx context.Context, id string) (Record, bool, error) {
	if s == nil {
		return Record{}, false, errors.New("worktree: nil lifecycle service")
	}
	return s.store.Get(ctx, id)
}

// Discover restores only project-owned record metadata. It performs no Git
// command and grants no continuation or cleanup authority.
func (s *Service) Discover(ctx context.Context) (Discovery, error) {
	if s == nil {
		return Discovery{}, errors.New("worktree: nil lifecycle service")
	}
	records, diagnostics, err := s.store.List(ctx)
	if err != nil {
		return Discovery{}, err
	}
	discovery := Discovery{
		Records:     make([]RecoveryRecord, 0, len(records)),
		Diagnostics: append([]DiscoveryDiagnostic(nil), diagnostics...),
	}
	for _, record := range records {
		recovery := RecoveryRecord{Record: record}
		expectedPath := filepath.Join(s.managedRoot, record.ID)
		switch {
		case !sameCanonicalPath(record.RepoRoot, s.projectRoot):
			recovery.Disposition = RecoveryUnavailable
			recovery.Diagnostic = "record repository root does not match the active project"
		case !s.ownsManagedRecordPath(record.Path, expectedPath):
			recovery.Disposition = RecoveryUnavailable
			recovery.Diagnostic = "record path is outside the project-managed worktree root"
		default:
			exists, pathErr := existingPath(record.Path)
			switch {
			case pathErr != nil:
				recovery.Disposition = RecoveryUnavailable
				recovery.Diagnostic = boundedMessage("inspect recorded worktree path", pathErr)
			case !exists &&
				record.State != StateRemoved &&
				record.State != StateFailed:
				recovery.Disposition = RecoveryUnavailable
				recovery.Diagnostic = "recorded worktree path is unavailable"
			case record.State == StateCreating ||
				record.State == StateRemoving:
				recovery.Disposition = RecoveryPending
				recovery.Diagnostic = "lifecycle operation was interrupted and requires explicit recovery"
			case record.State == StateRemoved || record.State == StateFailed:
				recovery.Disposition = RecoveryTerminal
			default:
				recovery.Disposition = RecoveryInspectOnly
			}
		}
		discovery.Records = append(discovery.Records, recovery)
	}
	return discovery, nil
}

// RecoverForContinuation performs the fresh read-only Git admission required
// before a stopped worktree-isolated Agent can run again. Interrupted
// Creating/Removing operations are classified durably, but this method never
// removes a worktree or branch.
func (s *Service) RecoverForContinuation(
	ctx context.Context,
	id string,
	owner Owner,
) (Record, error) {
	if s == nil {
		return Record{}, errors.New("worktree: nil lifecycle service")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := owner.Validate(); err != nil {
		return Record{}, err
	}
	unlock := operationLock(s.store.recordPath(id))
	defer unlock()
	record, found, err := s.store.Get(context.Background(), id)
	if err != nil {
		return Record{}, err
	}
	if !found {
		return Record{}, fmt.Errorf("worktree: record %q not found", id)
	}
	if !record.Owner.Equal(owner) {
		return record, errors.New(
			"worktree: continuation owner does not match durable record",
		)
	}
	switch record.State {
	case StateReady, StateRetained, StateCleanupFailed, StateCreating, StateRemoving:
	default:
		return record, fmt.Errorf(
			"worktree: cannot recover continuation in state %s",
			record.State,
		)
	}

	operationCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
	defer cancel()
	report, category, inspectErr := s.inspectRecovery(
		operationCtx,
		ctx,
		record,
		true,
	)
	if inspectErr == nil && report != nil && report.Dirty {
		category = ErrorCategoryDirty
		inspectErr = errors.New(
			"recovered worktree contains changes or new commits",
		)
	}
	if record.State == StateCreating {
		if inspectErr != nil {
			return record, categorize(
				category,
				"classify interrupted creation",
				inspectErr,
			)
		}
		recovered, transitionErr := s.transition(
			record.ID,
			StateCreating,
			StateReady,
			func(next *Record) {
				next.ResultDirtyReport = CloneDirtyReport(report)
			},
		)
		if transitionErr != nil {
			return record, transitionErr
		}
		return recovered, nil
	}
	if record.State == StateRemoving {
		classified, transitionErr := s.transition(
			record.ID,
			StateRemoving,
			StateCleanupFailed,
			func(next *Record) {
				next.LastErrorCategory = firstErrorCategory(
					category,
					ErrorCategoryInterrupted,
				)
				next.LastError = boundedMessage(
					"cleanup was interrupted before process restart",
					inspectErr,
				)
				next.ResultDirtyReport = CloneDirtyReport(report)
			},
		)
		if transitionErr != nil {
			return record, transitionErr
		}
		if inspectErr != nil {
			return classified, categorize(
				category,
				"classify interrupted cleanup",
				inspectErr,
			)
		}
		return classified, nil
	}
	if inspectErr != nil {
		return record, categorize(
			category,
			"admit recovered continuation",
			inspectErr,
		)
	}
	return record, nil
}

// RetryCleanup is the explicit restart-safe cleanup entrypoint. It accepts
// only the immutable durable owner and delegates actual deletion to Remove,
// which repeats the clean-state check at the commit boundary.
func (s *Service) RetryCleanup(
	ctx context.Context,
	id string,
	owner Owner,
) (Record, error) {
	if s == nil {
		return Record{}, errors.New("worktree: nil lifecycle service")
	}
	record, found, err := s.store.Get(context.Background(), id)
	if err != nil {
		return Record{}, err
	}
	if !found {
		return Record{}, fmt.Errorf("worktree: record %q not found", id)
	}
	if !record.Owner.Equal(owner) {
		return record, errors.New(
			"worktree: cleanup owner does not match durable record",
		)
	}
	if record.State == StateRemoved {
		return record, nil
	}
	if record.State == StateCreating {
		recovered, recoverErr := s.RecoverForContinuation(
			ctx,
			id,
			owner,
		)
		if recoverErr != nil {
			return recovered, recoverErr
		}
		return s.Remove(ctx, id, owner)
	}
	if record.State == StateCleanupFailed {
		if ctx == nil {
			ctx = context.Background()
		}
		operationCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
		_, category, inspectErr := s.inspectRecovery(
			operationCtx,
			ctx,
			record,
			false,
		)
		cancel()
		if inspectErr != nil {
			return record, categorize(
				category,
				"admit cleanup retry",
				inspectErr,
			)
		}
	}
	if record.State != StateRemoving {
		return s.Remove(ctx, id, owner)
	}

	classified, inspectErr := s.classifyInterruptedRemoval(ctx, record)
	if inspectErr != nil {
		return classified, inspectErr
	}
	return s.Remove(ctx, id, owner)
}

func (s *Service) classifyInterruptedRemoval(
	ctx context.Context,
	record Record,
) (Record, error) {
	unlock := operationLock(s.store.recordPath(record.ID))
	defer unlock()
	current, found, err := s.store.Get(context.Background(), record.ID)
	if err != nil {
		return Record{}, err
	}
	if !found {
		return Record{}, fmt.Errorf(
			"worktree: record %q not found",
			record.ID,
		)
	}
	if current.State != StateRemoving {
		return current, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	operationCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
	defer cancel()
	report, category, inspectErr := s.inspectRecovery(
		operationCtx,
		ctx,
		current,
		false,
	)
	classified, transitionErr := s.transition(
		current.ID,
		StateRemoving,
		StateCleanupFailed,
		func(next *Record) {
			next.LastErrorCategory = firstErrorCategory(
				category,
				ErrorCategoryInterrupted,
			)
			next.LastError = boundedMessage(
				"cleanup was interrupted before process restart",
				inspectErr,
			)
			next.ResultDirtyReport = CloneDirtyReport(report)
		},
	)
	if transitionErr != nil {
		return current, transitionErr
	}
	if inspectErr != nil {
		return classified, categorize(
			category,
			"classify interrupted cleanup",
			inspectErr,
		)
	}
	return classified, nil
}

func (s *Service) inspectRecovery(
	ctx context.Context,
	parentCtx context.Context,
	record Record,
	requirePath bool,
) (*DirtyReport, ErrorCategory, error) {
	return s.inspectRecoveryWithGit(ctx, parentCtx, record, requirePath, s.git)
}

func (s *Service) inspectRecoveryWithGit(
	ctx context.Context,
	parentCtx context.Context,
	record Record,
	requirePath bool,
	git ReadOnlyGit,
) (*DirtyReport, ErrorCategory, error) {
	if !sameCanonicalPath(record.RepoRoot, s.projectRoot) {
		return nil, ErrorCategoryValidation, errors.New(
			"record repository root does not match the active project",
		)
	}
	if !s.ownsManagedRecordPath(
		record.Path,
		filepath.Join(s.managedRoot, record.ID),
	) {
		return nil, ErrorCategoryValidation, errors.New(
			"record path is outside the project-managed worktree root",
		)
	}
	repository, err := git.Repository(ctx, record.RepoRoot)
	if err != nil {
		return nil, categoryForContext(
			ctx,
			parentCtx,
			ErrorCategoryUnknown,
		), err
	}
	if !samePath(repository.Root, record.RepoRoot) ||
		!samePath(repository.CommonDir, record.RepositoryIdentity) {
		return nil, ErrorCategoryValidation, errors.New(
			"record repository identity changed",
		)
	}
	exists, err := existingPath(record.Path)
	if err != nil {
		return nil, ErrorCategoryValidation, err
	}
	if !exists {
		if requirePath {
			return nil, ErrorCategoryValidation, errors.New(
				"recorded worktree path is unavailable",
			)
		}
		commit, branchExists, branchErr := git.BranchCommit(
			ctx,
			record.RepoRoot,
			record.Branch,
		)
		if branchErr != nil {
			return nil, categoryForContext(
				ctx,
				parentCtx,
				ErrorCategoryUnknown,
			), branchErr
		}
		if !branchExists || commit != record.BaseCommit {
			return nil, ErrorCategoryValidation, errors.New(
				"managed branch ownership changed",
			)
		}
		return &DirtyReport{}, "", nil
	}
	inspection, err := git.InspectWorktree(ctx, record.Path)
	if err != nil {
		return nil, categoryForContext(
			ctx,
			parentCtx,
			ErrorCategoryUnknown,
		), err
	}
	if err := validateOwnedInspection(record, inspection); err != nil {
		return nil, ErrorCategoryValidation, err
	}
	report, category, err := s.inspectDirtyReportWithGit(
		ctx,
		parentCtx,
		record.Path,
		record.BaseCommit,
		false,
		nil,
		git,
	)
	if err != nil {
		return report, category, err
	}
	commit, branchExists, err := git.BranchCommit(
		ctx,
		record.RepoRoot,
		record.Branch,
	)
	if err != nil {
		return report, categoryForContext(
			ctx,
			parentCtx,
			ErrorCategoryUnknown,
		), err
	}
	if !branchExists || commit != inspection.Head {
		return report, ErrorCategoryValidation, errors.New(
			"managed branch ownership changed",
		)
	}
	return report, "", nil
}

func (s *Service) ownsManagedRecordPath(
	recordPath string,
	expectedPath string,
) bool {
	if !sameCanonicalPath(recordPath, expectedPath) {
		return false
	}
	candidate := filepath.Clean(recordPath)
	if canonical, err := canonicalExistingPath(recordPath); err == nil {
		candidate = canonical
	}
	return pathContained(s.managedRoot, candidate)
}

func (s *Service) finishRemoval(
	operationCtx context.Context,
	parentCtx context.Context,
	record Record,
) (Record, error) {
	pathExists, err := existingPath(record.Path)
	if err != nil {
		return s.cleanupFailed(record, ErrorCategoryValidation, "inspect managed path", err)
	}
	if pathExists {
		clean, report, category, checkErr := s.verifyClean(
			operationCtx,
			parentCtx,
			record,
		)
		if checkErr != nil || !clean {
			if checkErr == nil {
				checkErr = errors.New("worktree contains changes")
				category = ErrorCategoryDirty
			}
			record.ResultDirtyReport = CloneDirtyReport(report)
			return s.cleanupFailed(record, category, "verify cleanup state", checkErr)
		}
		if err := s.git.RemoveWorktree(operationCtx, record.RepoRoot, record.Path); err != nil {
			return s.cleanupFailed(
				record,
				categoryForContext(operationCtx, parentCtx, ErrorCategoryCleanup),
				"remove worktree",
				err,
			)
		}
	}

	// Once removal may have committed, finish the safety boundary even if the
	// caller cancels. A raced commit advances the branch; restore that branch
	// at the recorded path before reporting CleanupFailed.
	safetyCtx, safetyCancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer safetyCancel()
	commit, exists, err := s.git.BranchCommit(safetyCtx, record.RepoRoot, record.Branch)
	if err != nil {
		return s.cleanupFailed(
			record,
			ErrorCategoryCleanup,
			"inspect cleanup branch",
			err,
		)
	}
	if exists {
		if commit != record.BaseCommit {
			restoreErr := s.restoreRetainedPath(safetyCtx, record)
			record.ResultDirtyReport = &DirtyReport{
				Dirty:          true,
				NewCommits:     1,
				Truncated:      true,
				PatchTruncated: true,
			}
			return s.cleanupFailed(
				record,
				ErrorCategoryDirty,
				"worktree changed during cleanup; restored retained path",
				restoreErr,
			)
		}
		if err := s.git.DeleteBranch(
			safetyCtx,
			record.RepoRoot,
			record.Branch,
			record.BaseCommit,
		); err != nil {
			restoreErr := s.restoreIfBranchAdvanced(safetyCtx, record)
			return s.cleanupFailed(
				record,
				ErrorCategoryCleanup,
				"delete cleanup branch",
				errors.Join(err, restoreErr),
			)
		}
	}
	removed, err := s.transition(record.ID, StateRemoving, StateRemoved, nil)
	if err != nil {
		return record, err
	}
	return removed, nil
}

func (s *Service) restoreIfBranchAdvanced(
	ctx context.Context,
	record Record,
) error {
	commit, exists, err := s.git.BranchCommit(ctx, record.RepoRoot, record.Branch)
	if err != nil || !exists || commit == record.BaseCommit {
		return err
	}
	return s.restoreRetainedPath(ctx, record)
}

func (s *Service) restoreRetainedPath(
	ctx context.Context,
	record Record,
) error {
	if exists, err := existingPath(record.Path); err != nil {
		return err
	} else if exists {
		return nil
	}
	if err := s.git.RestoreWorktree(
		ctx,
		record.RepoRoot,
		record.Path,
		record.Branch,
	); err != nil {
		return err
	}
	inspection, err := s.git.InspectWorktree(ctx, record.Path)
	if err != nil {
		return err
	}
	if err := validateOwnedInspection(record, inspection); err != nil {
		return err
	}
	if inspection.Head == record.BaseCommit {
		return errors.New("restored worktree does not contain the raced commit")
	}
	return nil
}

func (s *Service) verifyClean(
	ctx context.Context,
	parentCtx context.Context,
	record Record,
) (bool, *DirtyReport, ErrorCategory, error) {
	if !pathContained(s.managedRoot, record.Path) {
		return false, nil, ErrorCategoryValidation, errors.New("managed path escapes service root")
	}
	inspection, err := s.git.InspectWorktree(ctx, record.Path)
	if err != nil {
		return false, nil, categoryForContext(ctx, parentCtx, ErrorCategoryUnknown), err
	}
	if err := validateOwnedInspection(record, inspection); err != nil {
		return false, nil, ErrorCategoryValidation, err
	}
	report, category, err := s.inspectDirtyReport(
		ctx,
		parentCtx,
		record.Path,
		record.BaseCommit,
		true,
		nil,
	)
	if err != nil {
		return false, report, category, err
	}
	if report.Dirty {
		return false, report, ErrorCategoryDirty, nil
	}
	branchCommit, exists, err := s.git.BranchCommit(ctx, record.RepoRoot, record.Branch)
	if err != nil {
		return false, report, categoryForContext(ctx, parentCtx, ErrorCategoryUnknown), err
	}
	if !exists || branchCommit != record.BaseCommit {
		return false, report, ErrorCategoryValidation, errors.New("managed branch ownership changed")
	}
	return true, report, "", nil
}

func (s *Service) inspectDirtyReport(
	ctx context.Context,
	parentCtx context.Context,
	path string,
	baseCommit string,
	includePatch bool,
	excludedPaths []string,
) (*DirtyReport, ErrorCategory, error) {
	return s.inspectDirtyReportWithGit(
		ctx,
		parentCtx,
		path,
		baseCommit,
		includePatch,
		excludedPaths,
		s.git,
	)
}

func (s *Service) inspectDirtyReportWithGit(
	ctx context.Context,
	parentCtx context.Context,
	path string,
	baseCommit string,
	includePatch bool,
	excludedPaths []string,
	git ReadOnlyGit,
) (*DirtyReport, ErrorCategory, error) {
	status, err := git.StatusPorcelain(ctx, path)
	if err != nil {
		return nil, categoryForContext(ctx, parentCtx, ErrorCategoryUnknown), err
	}
	changedFiles, changedFilesTruncated, patchIncomplete := changedFilesFromPorcelain(
		status,
		excludedPaths,
	)
	count, err := git.CountCommits(ctx, path, baseCommit)
	if err != nil {
		return nil, categoryForContext(ctx, parentCtx, ErrorCategoryUnknown), err
	}
	report := &DirtyReport{
		Dirty:          len(changedFiles) > 0 || changedFilesTruncated || count > 0,
		NewCommits:     count,
		ChangedFiles:   changedFiles,
		Truncated:      changedFilesTruncated,
		PatchTruncated: patchIncomplete,
	}
	if !report.Dirty || !includePatch {
		return report, "", nil
	}
	patch, patchTruncated, err := git.Diff(
		ctx,
		path,
		baseCommit,
		maxPatchBytes,
	)
	report.Patch = patch
	report.PatchTruncated = report.PatchTruncated || patchTruncated
	if err != nil {
		report.PatchTruncated = true
		return report, ErrorCategoryDirty, err
	}
	return report, "", nil
}

func (s *Service) sourceDirtyExcludes(repositoryRoot string) []string {
	paths := []string{s.store.Root(), s.managedRoot}
	excluded := make([]string, 0, len(paths))
	for _, path := range paths {
		relative, err := filepath.Rel(repositoryRoot, path)
		if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
			continue
		}
		excluded = append(excluded, filepath.ToSlash(relative))
	}
	return excluded
}

func changedFilesFromPorcelain(
	status string,
	excludedPaths []string,
) ([]string, bool, bool) {
	files := make([]string, 0)
	truncated := false
	patchIncomplete := false
	for _, line := range strings.Split(strings.TrimSpace(status), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) < 4 {
			truncated = true
			patchIncomplete = true
			continue
		}
		code := line[:2]
		path := normalizePorcelainPath(
			strings.TrimSpace(line[3:]),
			code,
		)
		if path == "" || statusPathExcluded(path, excludedPaths) {
			continue
		}
		if code == "??" || code == "!!" {
			patchIncomplete = true
		}
		if len(path) > maxChangedFileBytes {
			path = path[:maxChangedFileBytes]
			truncated = true
		}
		if len(files) >= maxChangedFiles {
			truncated = true
			continue
		}
		files = append(files, path)
	}
	return files, truncated, patchIncomplete
}

func normalizePorcelainPath(path, code string) string {
	if strings.ContainsAny(code, "RC") {
		if split := strings.LastIndex(path, " -> "); split >= 0 {
			path = path[split+len(" -> "):]
		}
	}
	if unquoted, err := strconv.Unquote(path); err == nil {
		path = unquoted
	}
	return filepath.ToSlash(strings.TrimSpace(path))
}

func statusPathExcluded(path string, excludedPaths []string) bool {
	for _, excluded := range excludedPaths {
		excluded = strings.TrimSuffix(filepath.ToSlash(excluded), "/")
		if path == excluded || strings.HasPrefix(path, excluded+"/") {
			return true
		}
	}
	return false
}

func (s *Service) failCreate(
	id string,
	category ErrorCategory,
	message string,
	cause error,
) (Record, error) {
	record, err := s.transition(
		id,
		StateCreating,
		StateFailed,
		func(next *Record) {
			next.LastErrorCategory = category
			next.LastError = boundedMessage(message, cause)
		},
	)
	if err != nil {
		return record, errors.Join(categorize(category, message, cause), err)
	}
	return record, categorize(category, message, cause)
}

func (s *Service) cleanupFailed(
	record Record,
	category ErrorCategory,
	message string,
	cause error,
) (Record, error) {
	failed, err := s.transition(
		record.ID,
		StateRemoving,
		StateCleanupFailed,
		func(next *Record) {
			next.LastErrorCategory = category
			next.LastError = boundedMessage(message, cause)
			if record.ResultDirtyReport != nil {
				next.ResultDirtyReport = CloneDirtyReport(
					record.ResultDirtyReport,
				)
			} else if category == ErrorCategoryDirty {
				next.ResultDirtyReport = &DirtyReport{Dirty: true}
			}
		},
	)
	if err != nil {
		return failed, errors.Join(categorize(category, message, cause), err)
	}
	return failed, categorize(category, message, cause)
}

func (s *Service) transition(
	id string,
	expected State,
	nextState State,
	mutate func(*Record),
) (Record, error) {
	ctx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer cancel()
	var from State
	next, err := s.store.Update(ctx, id, func(current Record) (Record, error) {
		if current.State != expected {
			return Record{}, fmt.Errorf(
				"worktree: record %q state %s, want %s",
				id,
				current.State,
				expected,
			)
		}
		from = current.State
		if nextState != current.State && !validTransition(current.State, nextState) {
			return Record{}, fmt.Errorf(
				"worktree: invalid transition %s -> %s",
				current.State,
				nextState,
			)
		}
		current.State = nextState
		current.Revision++
		current.UpdatedAt = s.clock().UTC()
		if nextState == StateReady || nextState == StateRemoving || nextState == StateRemoved {
			current.LastErrorCategory = ""
			current.LastError = ""
		}
		if mutate != nil {
			mutate(&current)
		}
		return current, nil
	})
	if err != nil {
		return Record{}, err
	}
	if err := s.emit(ctx, Transition{From: from, Record: next}); err != nil {
		return next, err
	}
	return next, nil
}

func (s *Service) emit(ctx context.Context, transition Transition) error {
	if s.transitionSink == nil {
		return nil
	}
	return s.transitionSink(ctx, transition)
}

func operationLock(key string) func() {
	value, _ := operationLockRegistry.LoadOrStore(key, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

func validateReadyInspection(record Record, inspection Inspection) error {
	if err := validateOwnedInspection(record, inspection); err != nil {
		return err
	}
	if inspection.Head != record.BaseCommit {
		return fmt.Errorf(
			"created worktree HEAD %s does not match base %s",
			inspection.Head,
			record.BaseCommit,
		)
	}
	return nil
}

func validateOwnedInspection(record Record, inspection Inspection) error {
	canonicalPath, err := canonicalExistingPath(record.Path)
	if err != nil {
		return fmt.Errorf("canonicalize managed worktree: %w", err)
	}
	if !samePath(canonicalPath, record.Path) {
		return errors.New("managed worktree path identity changed")
	}
	if !samePath(inspection.Root, record.Path) {
		return errors.New("worktree repository root does not match managed path")
	}
	if !samePath(inspection.CommonDir, record.RepositoryIdentity) {
		return errors.New("worktree repository common-directory identity changed")
	}
	if inspection.Branch != record.Branch {
		return fmt.Errorf(
			"worktree branch %q does not match owned branch %q",
			inspection.Branch,
			record.Branch,
		)
	}
	return nil
}

func existingPath(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

type lifecycleError struct {
	Category ErrorCategory
	Message  string
	Err      error
}

func (e *lifecycleError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("worktree: %s: %s", e.Category, e.Message)
	}
	return fmt.Sprintf("worktree: %s: %s: %v", e.Category, e.Message, e.Err)
}

func (e *lifecycleError) Unwrap() error {
	return e.Err
}

func categorize(category ErrorCategory, message string, err error) error {
	if category == "" {
		category = ErrorCategoryUnknown
	}
	return &lifecycleError{Category: category, Message: message, Err: err}
}

func categoryForContext(
	operationCtx context.Context,
	parentCtx context.Context,
	fallback ErrorCategory,
) ErrorCategory {
	if parentCtx != nil && errors.Is(parentCtx.Err(), context.Canceled) {
		return ErrorCategoryCancelled
	}
	if parentCtx != nil && errors.Is(parentCtx.Err(), context.DeadlineExceeded) {
		return ErrorCategoryTimeout
	}
	if operationCtx != nil && errors.Is(operationCtx.Err(), context.DeadlineExceeded) {
		return ErrorCategoryTimeout
	}
	if operationCtx != nil && errors.Is(operationCtx.Err(), context.Canceled) {
		return ErrorCategoryCancelled
	}
	return fallback
}

func boundedMessage(message string, err error) string {
	if err == nil {
		return boundedError(errors.New(message))
	}
	return boundedError(fmt.Errorf("%s: %w", message, err))
}

func boundedError(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if len(text) > 2048 {
		return text[:2048]
	}
	return text
}

func firstErrorCategory(
	values ...ErrorCategory,
) ErrorCategory {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ErrorCategoryUnknown
}
