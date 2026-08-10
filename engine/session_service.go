package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"github.com/abietic/yhc/engine/auth"
	"github.com/abietic/yhc/engine/compact"
	"github.com/abietic/yhc/engine/internal/workboard"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/tools"
)

// SessionService is the engine-owned facade for session discovery, resume,
// fork, rename, and export. Entrypoints may project these outcomes, but they
// must not implement a second storage or live-state mutation path.
type SessionService struct {
	engine *QueryEngine

	forkMu           sync.Mutex
	forkActivationMu sync.Mutex
	forkResults      map[string]*SessionForkResult
	logicalWorkSeeds map[string]tools.TaskManagerSnapshot
	branchFn         func(session.BranchOptions) (*session.BranchResult, error)
	activateForkFn   func(
		context.Context,
		session.ResumeOptions,
		string,
	) (*session.ResumedSession, error)
	removeFn func(string) error
}

type sessionServiceSnapshot struct {
	sessionID         string
	cwd               string
	transcriptDir     string
	catalogPath       string
	legacyCatalogPath string
	clock             func() time.Time
	recorder          *transcript.Recorder
}

// SessionRenameResult identifies the durable metadata mutation performed by
// Rename.
type SessionRenameResult struct {
	SessionID string
	Name      string
}

// SessionExportResult identifies the canonical persisted transcript export.
type SessionExportResult struct {
	SessionID    string
	Path         string
	MessageCount int
}

type SessionWorkBoardRecoveryRequest struct {
	SessionID           string
	BoardID             string
	Revision            uint64
	AcknowledgeDataLoss bool
}

type SessionWorkBoardRecoveryResult struct {
	SessionID         string
	PreviousBoardID   string
	PreviousRevision  uint64
	RecoveredBoardID  string
	RecoveredRevision uint64
}

func (s *SessionService) RecoverWorkBoard(
	ctx context.Context,
	request SessionWorkBoardRecoveryRequest,
) (*SessionWorkBoardRecoveryResult, error) {
	if s == nil || s.engine == nil {
		return nil, errors.New("session service is unavailable")
	}
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.BoardID = strings.TrimSpace(request.BoardID)
	if request.SessionID == "" ||
		request.BoardID == "" ||
		request.Revision == 0 {
		return nil, errors.New(
			"WorkBoard recovery requires exact SessionID, BoardID, and revision",
		)
	}
	if !request.AcknowledgeDataLoss {
		return nil, errors.New(
			"WorkBoard recovery requires data-loss acknowledgement",
		)
	}
	info, active, err := s.mutationTarget(ctx, request.SessionID)
	if err != nil {
		return nil, err
	}
	var adapter *workboard.LogicalWorkAdapter
	if active {
		s.engine.mu.Lock()
		adapter = s.engine.logicalWorkAdapter
		s.engine.mu.Unlock()
	}
	if adapter == nil {
		sourceDir := info.TranscriptDir
		if sourceDir == "" {
			sourceDir = filepath.Dir(info.TranscriptPath)
		}
		adapter, err = workboard.NewLogicalWorkAdapter(
			workboard.AdapterConfig{
				SessionID: request.SessionID,
				Dir:       sourceDir,
				LeaderScope: tools.TodoScope{
					SessionID: request.SessionID,
					AgentID:   info.AgentID,
				},
				Clock: s.snapshot().clock,
			},
			tools.TaskManagerSnapshot{NextID: 1},
		)
		if err != nil {
			return nil, err
		}
	}
	recovered, err := adapter.Recover(workboard.RecoveryRequest{
		SessionID:           request.SessionID,
		BoardID:             request.BoardID,
		Revision:            request.Revision,
		AcknowledgeDataLoss: true,
	})
	if err != nil {
		return nil, err
	}
	return &SessionWorkBoardRecoveryResult{
		SessionID:         recovered.SessionID,
		PreviousBoardID:   recovered.PreviousBoardID,
		PreviousRevision:  recovered.PreviousRevision,
		RecoveredBoardID:  recovered.RecoveredBoardID,
		RecoveredRevision: recovered.RecoveredRevision,
	}, nil
}

func (s *SessionService) Delete(
	ctx context.Context,
	sessionID string,
) (*session.DeleteResult, error) {
	if s == nil || s.engine == nil {
		return nil, errors.New("session service is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session ID is required")
	}
	info, active, resolveErr := s.mutationTarget(ctx, sessionID)
	if resolveErr != nil && errors.Is(resolveErr, os.ErrNotExist) {
		snapshot := s.snapshot()
		info = session.SessionInfo{
			SessionID:     sessionID,
			CWD:           snapshot.cwd,
			TranscriptDir: snapshot.transcriptDir,
		}
		active = false
	} else if resolveErr != nil {
		return nil, resolveErr
	}
	sourceDir := info.TranscriptDir
	if sourceDir == "" {
		sourceDir = filepath.Dir(info.TranscriptPath)
	}
	remove := func() (*session.DeleteResult, error) {
		if active {
			s.engine.mu.Lock()
			recorder := s.engine.transcript
			s.engine.mu.Unlock()
			if recorder != nil {
				if err := recorder.Close(); err != nil {
					return nil, fmt.Errorf(
						"close active session before delete: %w",
						err,
					)
				}
			}
		}
		return session.DeleteSession(session.DeleteOptions{
			SessionID:  sessionID,
			Dir:        sourceDir,
			ProjectDir: info.CWD,
		})
	}
	s.engine.mu.Lock()
	activeAdapter := s.engine.logicalWorkAdapter
	s.engine.mu.Unlock()
	if active && activeAdapter != nil {
		deletionGate := s.engine.sessionDeletionGate
		if deletionGate == nil ||
			!deletionGate.deleting.CompareAndSwap(false, true) {
			return nil, errors.New(
				"active Session deletion is already admitted",
			)
		}
		var result *session.DeleteResult
		err := activeAdapter.WithExclusiveLifecycle(func(
			snapshot workboard.LifecycleSnapshot,
		) error {
			if snapshot.SessionID != sessionID {
				return errors.New(
					"active Session changed before WorkBoard deletion",
				)
			}
			s.engine.mu.Lock()
			activeSessionID := s.engine.config.SessionID
			activeTranscriptDir := s.engine.config.TranscriptDir
			s.engine.mu.Unlock()
			if activeSessionID != sessionID ||
				canonicalSessionDirectory(activeTranscriptDir) !=
					canonicalSessionDirectory(sourceDir) {
				return errors.New(
					"active Session changed before deletion",
				)
			}
			if err := guardSessionExecutionDeletion(
				snapshot,
				s.engine.agentRunner,
			); err != nil {
				return err
			}
			var removeErr error
			result, removeErr = remove()
			if result != nil && result.TranscriptRemoved {
				s.engine.mu.Lock()
				s.engine.transcript = nil
				s.engine.logicalWorkErr = fmt.Errorf(
					"workboard authority: active Session was deleted",
				)
				s.engine.mu.Unlock()
			}
			return removeErr
		})
		if err != nil &&
			(result == nil || !result.TranscriptRemoved) {
			deletionGate.deleting.Store(false)
		}
		return result, err
	}
	return remove()
}

func guardSessionExecutionDeletion(
	snapshot workboard.LifecycleSnapshot,
	runner *tools.AgentRunner,
) error {
	if runner == nil {
		if len(snapshot.Record.ExecutionLinks) == 0 {
			return nil
		}
		return errors.New(
			"active Session deletion cannot resolve linked executions",
		)
	}
	if err := runner.SessionExecutionsSettled(snapshot.SessionID); err != nil {
		return err
	}
	keys := make([]tools.AgentExecutionKey, len(snapshot.Record.ExecutionLinks))
	for i, link := range snapshot.Record.ExecutionLinks {
		if link.Generation > math.MaxInt64 {
			return errors.New(
				"active Session deletion found out-of-range execution generation",
			)
		}
		keys[i] = tools.AgentExecutionKey{
			AgentID:    link.AgentID,
			Generation: int64(link.Generation),
		}
	}
	for _, settlement := range runner.AgentExecutionSettlements(keys) {
		if !settlement.Settled {
			return fmt.Errorf(
				"active Session deletion found unsettled execution %s/%d (%s)",
				settlement.Key.AgentID,
				settlement.Key.Generation,
				settlement.State,
			)
		}
	}
	return nil
}

// SessionForkRequest selects one durable source and idempotent fork operation.
// A nil Source targets the engine's active session.
type SessionForkRequest struct {
	Source      *session.SessionInfo
	BranchName  string
	OperationID string
}

// SessionForkResult identifies one fully committed child transcript.
type SessionForkResult struct {
	Branch      *session.BranchResult
	Info        session.SessionInfo
	OperationID string
	logicalWork workboard.ForkPreparation
}

// SessionService returns the session facade owned by this QueryEngine.
func (e *QueryEngine) SessionService() *SessionService {
	if e == nil {
		return nil
	}
	return e.sessionService
}

// Query returns one bounded session page after refreshing the current root in
// the shared catalog.
func (s *SessionService) Query(ctx context.Context, query session.SessionQuery) (*session.SessionPage, error) {
	if s == nil || s.engine == nil {
		return nil, errors.New("session service is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	snapshot := s.snapshot()
	if query.CWD == "" {
		query.CWD = snapshot.cwd
	}
	if query.TranscriptDir == "" {
		query.TranscriptDir = snapshot.transcriptDir
	}
	if query.CatalogPath == "" {
		query.CatalogPath = snapshot.catalogPath
	}
	if query.LegacyCatalogPath == "" {
		query.LegacyCatalogPath = snapshot.legacyCatalogPath
	}
	now := time.Now()
	if snapshot.clock != nil {
		now = snapshot.clock()
	}
	if err := session.RegisterSessionRoot(
		query.CatalogPath,
		query.CWD,
		query.TranscriptDir,
		now,
	); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	return session.QuerySessions(query)
}

// Resume restores the latest session in the current transcript store when
// sessionID is empty. Explicit IDs are resolved across every discoverable CWD
// root and rejected when more than one source has the same ID.
func (s *SessionService) Resume(ctx context.Context, sessionID string) (*session.ResumedSession, error) {
	return s.resumeForTurn(ctx, sessionID, "")
}

// Resolve returns the exact durable source selected by an explicit session ID.
// Duplicate IDs across transcript roots fail before any mutation.
func (s *SessionService) Resolve(ctx context.Context, sessionID string) (session.SessionInfo, error) {
	if s == nil || s.engine == nil {
		return session.SessionInfo{}, errors.New("session service is unavailable")
	}
	return s.resolveSessionInfo(ctx, sessionID, s.snapshot())
}

// ResumeInfo restores the exact transcript store and working directory carried
// by a selected session row.
func (s *SessionService) ResumeInfo(ctx context.Context, info session.SessionInfo) (*session.ResumedSession, error) {
	if strings.TrimSpace(info.SessionID) == "" {
		return nil, errors.New("session ID is required")
	}
	snapshot := s.snapshot()
	if err := s.requireWritableSessionInfo(ctx, info, snapshot); err != nil {
		return nil, err
	}
	sourceDir := info.TranscriptDir
	if sourceDir == "" && info.TranscriptPath != "" {
		sourceDir = filepath.Dir(info.TranscriptPath)
	}
	if sourceDir == "" {
		sourceDir = snapshot.transcriptDir
	}
	projectDir := info.CWD
	if projectDir == "" {
		projectDir = snapshot.cwd
	}
	return s.resumeWithOptionsForTurn(ctx, session.ResumeOptions{
		SessionID:        info.SessionID,
		SessionDir:       sourceDir,
		ProjectDir:       projectDir,
		ValidateMessages: true,
	}, "")
}

// ImportLegacyAndResumeInfo performs the explicit stopped-producer bundle
// transaction and then re-enters the ordinary canonical ResumeInfo path. It
// never constructs a legacy-specific runtime.
func (s *SessionService) ImportLegacyAndResumeInfo(
	ctx context.Context,
	info session.SessionInfo,
	confirmLegacyStopped bool,
) (*session.ResumedSession, error) {
	if s == nil || s.engine == nil {
		return nil, errors.New("session service is unavailable")
	}
	requirement := session.RequireCanonicalSession(info)
	if requirement == nil {
		return s.ResumeInfo(ctx, info)
	}
	if !errors.Is(requirement, session.ErrLegacySessionImportRequired) {
		return nil, requirement
	}
	if !confirmLegacyStopped {
		return nil, session.ErrSessionImportAttestationRequired
	}
	target, ok := session.LegacySessionImportTarget(requirement)
	if !ok {
		return nil, requirement
	}
	userRoots, err := session.DefaultSessionImportUserRoots()
	if err != nil {
		return nil, err
	}
	snapshot := s.snapshot()
	now := time.Now().UTC()
	if snapshot.clock != nil {
		now = snapshot.clock().UTC()
	}
	_, err = session.ImportSessionForResume(ctx, session.ImportRequest{
		Target:               target,
		UserRoots:            userRoots,
		ConfirmLegacyStopped: true,
		Now:                  now,
	})
	if err != nil && !errors.Is(err, session.ErrSessionImportAlreadyCommitted) {
		return nil, err
	}
	admitted, err := session.AdmitSessionResume(ctx, session.ResumeAdmissionRequest{
		SessionID:         target.SessionID,
		CWD:               target.CWD,
		CatalogPath:       snapshot.catalogPath,
		LegacyCatalogPath: snapshot.legacyCatalogPath,
		UserRoots:         userRoots,
	})
	if err != nil {
		return nil, err
	}
	return s.ResumeInfo(ctx, admitted)
}

// ReplaySnapshot returns one immutable, revision-bound view of the exact
// durable session row without activating or persisting a QueryEngine.
func (s *SessionService) ReplaySnapshot(
	ctx context.Context,
	info session.SessionInfo,
) (*session.SessionReplaySnapshot, error) {
	if s == nil || s.engine == nil {
		return nil, errors.New("session service is unavailable")
	}
	if strings.TrimSpace(info.SessionID) == "" {
		return nil, errors.New("session ID is required")
	}
	snapshot := s.snapshot()
	sourceDir := info.TranscriptDir
	if sourceDir == "" && info.TranscriptPath != "" {
		sourceDir = filepath.Dir(info.TranscriptPath)
	}
	if sourceDir == "" {
		sourceDir = snapshot.transcriptDir
	}
	projectDir := info.CWD
	if projectDir == "" {
		projectDir = snapshot.cwd
	}
	return session.LoadSessionReplaySnapshot(ctx, session.ResumeOptions{
		SessionID:  info.SessionID,
		SessionDir: sourceDir,
		ProjectDir: projectDir,
	})
}

func (s *SessionService) resumeForTurn(
	ctx context.Context,
	sessionID string,
	turnID string,
) (*session.ResumedSession, error) {
	if s == nil || s.engine == nil {
		return nil, errors.New("session service is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	snapshot := s.snapshot()
	if sessionID != "" {
		info, err := s.resolveSessionInfo(ctx, sessionID, snapshot)
		if err != nil {
			return nil, err
		}
		if info.ReadOnly || info.NeedsImport {
			return nil, session.RequireCanonicalSession(info)
		}
		if err := s.requireCommittedResumeInfo(ctx, info, snapshot); err != nil {
			return nil, err
		}
		sourceDir := info.TranscriptDir
		if sourceDir == "" {
			sourceDir = filepath.Dir(info.TranscriptPath)
		}
		projectDir := info.CWD
		if projectDir == "" {
			projectDir = snapshot.cwd
		}
		return s.resumeWithOptionsForTurn(ctx, session.ResumeOptions{
			SessionID:        info.SessionID,
			SessionDir:       sourceDir,
			ProjectDir:       projectDir,
			ValidateMessages: true,
		}, turnID)
	}
	return s.resumeWithOptionsForTurn(ctx, session.ResumeOptions{
		SessionDir:       snapshot.transcriptDir,
		ProjectDir:       snapshot.cwd,
		ValidateMessages: true,
	}, turnID)
}

func (s *SessionService) resumeWithOptionsForTurn(
	ctx context.Context,
	opts session.ResumeOptions,
	turnID string,
) (*session.ResumedSession, error) {
	if s == nil || s.engine == nil {
		return nil, errors.New("session service is unavailable")
	}
	return s.engine.resumeSessionWithOptionsForTurn(ctx, opts, turnID)
}

func (s *SessionService) resolveSessionInfo(
	ctx context.Context,
	sessionID string,
	snapshot sessionServiceSnapshot,
) (session.SessionInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return session.SessionInfo{}, fmt.Errorf("resolve session: %w", err)
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return session.SessionInfo{}, errors.New("session ID is required")
	}

	info, err := session.ResolveSession(session.SessionQuery{
		Scope:             session.SessionScopeCWD,
		CWD:               snapshot.cwd,
		TranscriptDir:     snapshot.transcriptDir,
		CatalogPath:       snapshot.catalogPath,
		LegacyCatalogPath: snapshot.legacyCatalogPath,
	}, sessionID)
	if err != nil {
		return session.SessionInfo{}, fmt.Errorf("resolve session %s: %w", sessionID, err)
	}
	return info, nil
}

func (s *SessionService) requireWritableSessionInfo(
	ctx context.Context,
	info session.SessionInfo,
	snapshot sessionServiceSnapshot,
) error {
	if info.ReadOnly || info.NeedsImport {
		return session.RequireCanonicalSession(info)
	}
	if info.HasResolvedSource() {
		return s.requireCommittedResumeInfo(ctx, info, snapshot)
	}
	resolved, err := s.resolveSessionInfo(ctx, info.SessionID, snapshot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Exact caller-supplied canonical rows remain supported even before
			// they are registered in a catalog.
			return nil
		}
		return err
	}
	if resolved.ReadOnly || resolved.NeedsImport {
		return session.RequireCanonicalSession(resolved)
	}
	return s.requireCommittedResumeInfo(ctx, resolved, snapshot)
}

func (s *SessionService) requireCommittedResumeInfo(
	ctx context.Context,
	info session.SessionInfo,
	snapshot sessionServiceSnapshot,
) error {
	if !info.HasResolvedSource() {
		return nil
	}
	userRoots, _ := session.DefaultSessionImportUserRoots()
	return session.AdmitResolvedSessionResume(
		ctx,
		session.ResolvedResumeAdmissionRequest{
			Info:        info,
			CatalogPath: snapshot.catalogPath,
			UserRoots:   userRoots,
		},
	)
}

// CreateFork commits one complete child transcript without activating it.
// The source transcript and live engine state are never mutated.
func (s *SessionService) CreateFork(
	ctx context.Context,
	request SessionForkRequest,
) (*SessionForkResult, error) {
	if s == nil || s.engine == nil {
		return nil, errors.New("session service is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("create fork: %w", err)
	}
	if request.Source != nil {
		if err := s.requireWritableSessionInfo(ctx, *request.Source, s.snapshot()); err != nil {
			return nil, err
		}
	}
	operationID := strings.TrimSpace(request.OperationID)
	if operationID == "" {
		operationID = uuid.NewString()
	}

	s.forkMu.Lock()
	defer s.forkMu.Unlock()

	if cached := s.forkResults[operationID]; cached != nil {
		return cached, nil
	}
	source, sourceMetadata, liveMessageCount, err := s.forkSource(request.Source)
	if err != nil {
		return nil, err
	}
	loaded, err := session.InspectFull(source)
	if err != nil {
		return nil, fmt.Errorf("inspect fork source: %w", err)
	}
	if len(loaded.Messages) == 0 {
		return nil, errors.New("cannot fork an empty session")
	}
	if liveMessageCount >= 0 && liveMessageCount != len(loaded.Messages) {
		return nil, fmt.Errorf(
			"active session is not at a durable fork boundary: live=%d persisted=%d",
			liveMessageCount,
			len(loaded.Messages),
		)
	}
	if sourceMetadata == nil {
		sourceMetadata = session.ReadSessionMetadataFull(loaded)
	}
	var sourceKernelMetadata session.SessionMetadata
	if sourceMetadata != nil {
		sourceKernelMetadata.QueryKernelVersion = sourceMetadata.QueryKernelVersion
		sourceKernelMetadata.QueryKernelStage = sourceMetadata.QueryKernelStage
		sourceKernelMetadata.QueryKernelIncompatibility = sourceMetadata.QueryKernelIncompatibility
	}
	sourceKernelSelection := resumedSessionQueryKernelSelection(
		sourceKernelMetadata,
	)
	if sourceKernelSelection.err != nil {
		return nil, fmt.Errorf(
			"fork session with invalid query kernel selection: %w",
			sourceKernelSelection.err,
		)
	}
	sourceDir := source.TranscriptDir
	if sourceDir == "" {
		sourceDir = filepath.Dir(source.TranscriptPath)
	}
	branchName := strings.TrimSpace(request.BranchName)
	if branchName == "" {
		branches, listErr := session.ListBranches(source.SessionID, sourceDir)
		if listErr != nil {
			return nil, fmt.Errorf("list existing forks: %w", listErr)
		}
		branchName = fmt.Sprintf("branch-%d", len(branches)+1)
	}
	newSessionID := uuid.NewSHA1(
		uuid.NameSpaceOID,
		[]byte(source.StableKey()+"\x00"+operationID),
	).String()
	logicalWork, err := s.prepareLogicalWorkFork(
		source,
		newSessionID,
		sourceDir,
	)
	if err != nil {
		return nil, fmt.Errorf("prepare fork WorkBoard: %w", err)
	}
	branchFn := s.branchFn
	if branchFn == nil {
		branchFn = session.BranchSession
	}
	snapshot := s.snapshot()
	branch, err := branchFn(session.BranchOptions{
		SourceSessionID:  source.SessionID,
		MessageIndex:     len(loaded.Messages),
		NewSessionID:     newSessionID,
		Dir:              sourceDir,
		ProjectDir:       source.CWD,
		BranchName:       branchName,
		OperationID:      operationID,
		Metadata:         sourceMetadata,
		PlanFileIdentity: tools.GetPlanFilePath(newSessionID, ""),
		Clock:            snapshot.clock,
	})
	if err != nil {
		if logicalWork.Mode == workboard.AuthorityModeWorkBoard {
			if cleanupErr := workboard.RemoveForkArtifacts(
				sourceDir,
				newSessionID,
			); cleanupErr != nil {
				return nil, errors.Join(
					err,
					fmt.Errorf(
						"cleanup prepared fork WorkBoard: %w",
						cleanupErr,
					),
				)
			}
		}
		return nil, err
	}
	result := &SessionForkResult{
		Branch: branch,
		Info: session.SessionInfo{
			SessionID:       branch.NewSessionID,
			CWD:             source.CWD,
			TranscriptDir:   sourceDir,
			TranscriptPath:  branch.TranscriptPath,
			ParentSessionID: source.SessionID,
			ParentThreadID:  firstSessionValue(source.ThreadID, source.SessionID),
			ParentAgentID:   source.AgentID,
			BranchName:      branch.BranchName,
			ThreadID:        branch.NewSessionID,
			Model:           source.Model,
			Provider:        source.Provider,
			ModelBinding:    source.ModelBinding,
		},
		OperationID: operationID,
		logicalWork: logicalWork,
	}
	if s.forkResults == nil {
		s.forkResults = make(map[string]*SessionForkResult)
	}
	s.forkResults[operationID] = result
	return result, nil
}

func (s *SessionService) forkSource(
	selected *session.SessionInfo,
) (session.SessionInfo, *session.SessionMetadataFull, int, error) {
	if selected != nil {
		info := *selected
		if strings.TrimSpace(info.SessionID) == "" {
			return session.SessionInfo{}, nil, -1, errors.New("fork source session ID is required")
		}
		if info.TranscriptDir == "" && info.TranscriptPath != "" {
			info.TranscriptDir = filepath.Dir(info.TranscriptPath)
		}
		return info, nil, -1, nil
	}

	e := s.engine
	e.planMu.Lock()
	planState := e.planState
	e.mu.Lock()
	if e.transcript == nil || strings.TrimSpace(e.config.SessionID) == "" {
		e.mu.Unlock()
		e.planMu.Unlock()
		return session.SessionInfo{}, nil, -1, errors.New("active session has no transcript recorder")
	}
	recorder := e.transcript
	messages := append([]*schema.Message(nil), e.messages...)
	if len(messages) == 0 {
		e.mu.Unlock()
		e.planMu.Unlock()
		return session.SessionInfo{}, nil, -1, errors.New("cannot fork an empty conversation")
	}
	kernelSelectionErr := e.queryKernelSelection.err
	binding := e.modelBinding.Clone()
	resolvedModel := e.config.Model
	resolvedProvider := auth.DetectProvider(resolvedModel)
	if binding != nil && binding.ValidateV1() == nil {
		resolvedModel = binding.APIModel
		resolvedProvider = binding.Provider
	}
	metadata := &session.SessionMetadataFull{
		SessionID:          e.config.SessionID,
		ParentSessionID:    e.config.ParentSessionID,
		ParentThreadID:     e.config.ParentThreadID,
		ParentAgentID:      e.config.ParentAgentID,
		Model:              resolvedModel,
		Provider:           resolvedProvider,
		ModelBinding:       binding,
		ThreadID:           e.config.ThreadID,
		AgentID:            e.config.AgentID,
		AgentGeneration:    e.config.AgentGeneration,
		Status:             e.sessionStatus,
		PermissionMode:     string(e.config.PermissionMode),
		QueryKernelVersion: e.queryKernelSelection.version,
		QueryKernelStage: string(
			e.queryKernelSelection.stage,
		),
		QueryKernelIncompatibility: e.queryKernelSelection.incompatibility,
		PlanState:                  persistedPlanState(planState),
		WorktreePath:               e.config.WorktreePath,
		WorktreeBranch:             e.config.WorktreeBranch,
		AdditionalDirs:             append([]string(nil), e.config.AdditionalDirs...),
		CreatedAt:                  e.sessionStartedAt,
		UpdatedAt:                  e.config.Clock().UTC(),
		MessageCount:               len(messages),
		TokenUsage:                 compact.EstimateTokenCount(messages),
		IsLeaf:                     true,
		GitBranch:                  e.sessionGitBranch,
		CWD:                        e.config.CWD,
	}
	info := session.SessionInfo{
		SessionID:      e.config.SessionID,
		CWD:            e.config.CWD,
		TranscriptDir:  e.config.TranscriptDir,
		TranscriptPath: e.transcript.Path(),
		ThreadID:       e.config.ThreadID,
		AgentID:        e.config.AgentID,
		Model:          resolvedModel,
		Provider:       metadata.Provider,
		ModelBinding:   session.SafeModelBindingProjection(binding),
	}
	e.mu.Unlock()
	e.planMu.Unlock()
	if kernelSelectionErr != nil {
		return session.SessionInfo{}, nil, -1, fmt.Errorf(
			"fork session with invalid query kernel selection: %w",
			kernelSelectionErr,
		)
	}
	if err := recorder.Flush(); err != nil {
		return session.SessionInfo{}, nil, -1, fmt.Errorf("flush fork source: %w", err)
	}
	return info, metadata, len(messages), nil
}

func (s *SessionService) prepareLogicalWorkFork(
	source session.SessionInfo,
	childSessionID string,
	sourceDir string,
) (workboard.ForkPreparation, error) {
	e := s.engine
	e.mu.Lock()
	activeSessionID := e.config.SessionID
	activeAdapter := e.logicalWorkAdapter
	clock := e.config.Clock
	e.mu.Unlock()
	if source.SessionID == activeSessionID && activeAdapter != nil {
		return activeAdapter.PrepareFork(
			source.SessionID,
			childSessionID,
			sourceDir,
		)
	}
	adapter, err := workboard.NewLogicalWorkAdapter(
		workboard.AdapterConfig{
			SessionID: source.SessionID,
			Dir:       sourceDir,
			LeaderScope: tools.TodoScope{
				SessionID: source.SessionID,
				AgentID:   source.AgentID,
			},
			Clock: clock,
		},
		tools.TaskManagerSnapshot{NextID: 1},
	)
	if err != nil {
		return workboard.ForkPreparation{}, err
	}
	return adapter.PrepareFork(
		source.SessionID,
		childSessionID,
		sourceDir,
	)
}

func (s *SessionService) logicalWorkSeed(
	sessionID string,
) (tools.TaskManagerSnapshot, bool) {
	if s == nil {
		return tools.TaskManagerSnapshot{}, false
	}
	s.forkMu.Lock()
	defer s.forkMu.Unlock()
	snapshot, exists := s.logicalWorkSeeds[sessionID]
	return snapshot, exists
}

func (s *SessionService) forkAndActivateForTurn(
	ctx context.Context,
	request SessionForkRequest,
	turnID string,
) (*session.ResumedSession, *SessionForkResult, error) {
	if s == nil || s.engine == nil {
		return nil, nil, errors.New("session service is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.forkActivationMu.Lock()
	defer s.forkActivationMu.Unlock()
	created, err := s.CreateFork(ctx, request)
	if err != nil {
		return nil, nil, err
	}
	if created.logicalWork.Mode == workboard.AuthorityModeLegacy {
		s.forkMu.Lock()
		if s.logicalWorkSeeds == nil {
			s.logicalWorkSeeds = make(
				map[string]tools.TaskManagerSnapshot,
			)
		}
		s.logicalWorkSeeds[created.Info.SessionID] = created.logicalWork.LegacyTasks
		s.forkMu.Unlock()
		defer func() {
			s.forkMu.Lock()
			delete(s.logicalWorkSeeds, created.Info.SessionID)
			s.forkMu.Unlock()
		}()
	}
	sourceDir := created.Info.TranscriptDir
	if sourceDir == "" {
		sourceDir = filepath.Dir(created.Info.TranscriptPath)
	}
	resume := s.activateForkFn
	if resume == nil {
		resume = s.resumeWithOptionsForTurn
	}
	resumed, err := resume(
		context.WithoutCancel(ctx),
		session.ResumeOptions{
			SessionID:        created.Info.SessionID,
			SessionDir:       sourceDir,
			ProjectDir:       created.Info.CWD,
			ValidateMessages: true,
		},
		turnID,
	)
	if err == nil {
		return resumed, created, nil
	}
	if rollbackErr := s.DiscardFork(created); rollbackErr != nil {
		return nil, created, errors.Join(
			fmt.Errorf("activate committed fork: %w", err),
			fmt.Errorf("fork rollback failed: %w", rollbackErr),
		)
	}
	return nil, created, fmt.Errorf("activate committed fork: %w", err)
}

// ForkInfo commits and activates a child of one selected session row.
func (s *SessionService) ForkInfo(
	ctx context.Context,
	info session.SessionInfo,
	branchName string,
) (*session.ResumedSession, *SessionForkResult, error) {
	return s.Fork(ctx, SessionForkRequest{
		Source:     &info,
		BranchName: branchName,
	})
}

// Fork commits and activates one selected child. Activation validates that the
// committed child is resumable; failure compensates only the operation-owned
// child before returning.
func (s *SessionService) Fork(
	ctx context.Context,
	request SessionForkRequest,
) (*session.ResumedSession, *SessionForkResult, error) {
	return s.forkAndActivateForTurn(ctx, request, "")
}

// DiscardFork compensates a committed child that was never externally
// activated. It validates the operation marker before removing the transcript.
func (s *SessionService) DiscardFork(created *SessionForkResult) error {
	if created == nil || created.Branch == nil {
		return nil
	}
	info, err := os.Lstat(created.Branch.TranscriptPath)
	if err != nil {
		return fmt.Errorf("inspect fork before rollback: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("refusing to remove a fork target that is not a regular file")
	}
	loaded, err := transcript.NewRecorder(
		created.Branch.NewSessionID,
		filepath.Dir(created.Branch.TranscriptPath),
	).LoadFull()
	if err != nil {
		return fmt.Errorf("load fork before rollback: %w", err)
	}
	operationID := ""
	parentSessionID := ""
	for _, metadata := range loaded.Metadata {
		switch metadata.Key {
		case "fork_operation_id":
			operationID = metadata.Value
		case "parent_session_id":
			parentSessionID = metadata.Value
		}
	}
	expectedParentID := firstSessionValue(
		created.Info.ParentSessionID,
		created.Branch.ParentSessionID,
	)
	fullMetadata := session.ReadSessionMetadataFull(loaded)
	if operationID == "" || operationID != created.OperationID {
		return errors.New("refusing to remove fork without the matching operation marker")
	}
	if expectedParentID == "" ||
		parentSessionID != expectedParentID ||
		fullMetadata == nil ||
		fullMetadata.SessionID != created.Branch.NewSessionID ||
		fullMetadata.ParentSessionID != expectedParentID {
		return errors.New("refusing to remove fork without matching child lineage")
	}
	removeFn := s.removeFn
	if removeFn == nil {
		removeFn = os.Remove
	}
	if err := removeFn(created.Branch.TranscriptPath); err != nil {
		return err
	}
	if created.logicalWork.Mode == workboard.AuthorityModeWorkBoard {
		if err := workboard.RemoveForkArtifacts(
			filepath.Dir(created.Branch.TranscriptPath),
			created.Branch.NewSessionID,
		); err != nil {
			return fmt.Errorf(
				"fork transcript removed but WorkBoard cleanup failed: %w",
				err,
			)
		}
	}
	s.forkMu.Lock()
	for key, result := range s.forkResults {
		if result == created {
			delete(s.forkResults, key)
		}
	}
	s.forkMu.Unlock()
	if runtime.GOOS != "windows" {
		directory, openErr := os.Open(filepath.Dir(created.Branch.TranscriptPath))
		if openErr != nil {
			return fmt.Errorf("fork removed but rollback durability is uncertain: %w", openErr)
		}
		defer directory.Close() //nolint:errcheck
		if syncErr := directory.Sync(); syncErr != nil {
			return fmt.Errorf("fork removed but rollback durability is uncertain: %w", syncErr)
		}
	}
	return nil
}

// Rename appends a durable custom-title metadata record. An empty session ID
// targets the currently active session.
func (s *SessionService) Rename(
	ctx context.Context,
	sessionID string,
	name string,
) (*SessionRenameResult, error) {
	if s == nil || s.engine == nil {
		return nil, errors.New("session service is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("rename session: %w", err)
	}
	sessionID = strings.TrimSpace(sessionID)
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("session name is required")
	}

	info, active, err := s.mutationTarget(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	recorder, closeRecorder, err := s.recorderForTarget(info, active)
	if err != nil {
		return nil, err
	}
	if err := recorder.RecordMetadata("customTitle", name); err != nil {
		if closeRecorder {
			_ = recorder.Close()
		}
		return nil, fmt.Errorf("persist session name: %w", err)
	}
	if closeRecorder {
		if err := recorder.Close(); err != nil {
			return nil, fmt.Errorf("close renamed session transcript: %w", err)
		}
	} else if err := recorder.Flush(); err != nil {
		return nil, fmt.Errorf("flush session name: %w", err)
	}
	return &SessionRenameResult{SessionID: info.SessionID, Name: name}, nil
}

// Export writes the active persisted transcript for a session as canonical
// Markdown. An empty session ID targets the currently active session.
func (s *SessionService) Export(
	ctx context.Context,
	sessionID string,
	filename string,
) (*SessionExportResult, error) {
	if s == nil || s.engine == nil {
		return nil, errors.New("session service is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("export session: %w", err)
	}
	sessionID = strings.TrimSpace(sessionID)
	info, active, err := s.mutationTarget(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	recorder, closeRecorder, err := s.recorderForTarget(info, active)
	if err != nil {
		return nil, err
	}
	if closeRecorder {
		if err := recorder.Close(); err != nil {
			return nil, fmt.Errorf("close session transcript before export: %w", err)
		}
	} else if err := recorder.Flush(); err != nil {
		return nil, fmt.Errorf("flush active session before export: %w", err)
	}
	exported, err := session.ExportSession(session.ExportOptions{
		SessionID:        info.SessionID,
		Dir:              info.TranscriptDir,
		ProjectDir:       info.CWD,
		Format:           session.ExportMarkdown,
		IncludeToolCalls: true,
		IncludeMetadata:  true,
	})
	if err != nil {
		return nil, fmt.Errorf("export session %s: %w", info.SessionID, err)
	}
	target := s.exportPath(info.SessionID, filename, s.snapshot())
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("export session: %w", err)
	}
	if err := writeSessionExportFile(target, []byte(exported.Content)); err != nil {
		return nil, fmt.Errorf("write session export: %w", err)
	}
	return &SessionExportResult{
		SessionID:    info.SessionID,
		Path:         target,
		MessageCount: exported.MessageCount,
	}, nil
}

func writeSessionExportFile(path string, data []byte) error {
	return writeSessionExportFileWithReplace(path, data, replaceSessionExportFile)
}

func writeSessionExportFileWithReplace(
	path string,
	data []byte,
	replace func(string, string) error,
) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("session export target is not a regular file: %s", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+identity.CommandName+"-export-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0o644); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replace(tempPath, path); err != nil {
		return err
	}
	return nil
}

func (s *SessionService) mutationTarget(
	ctx context.Context,
	sessionID string,
) (session.SessionInfo, bool, error) {
	snapshot := s.snapshot()
	if strings.TrimSpace(sessionID) == "" {
		if snapshot.sessionID == "" || snapshot.recorder == nil {
			return session.SessionInfo{}, false, errors.New("active session has no transcript recorder")
		}
		return session.SessionInfo{
			SessionID:      snapshot.sessionID,
			CWD:            snapshot.cwd,
			TranscriptDir:  snapshot.transcriptDir,
			TranscriptPath: snapshot.recorder.Path(),
		}, true, nil
	}
	info, err := s.resolveSessionInfo(ctx, sessionID, snapshot)
	if err != nil {
		return session.SessionInfo{}, false, err
	}
	if info.ReadOnly || info.NeedsImport {
		return session.SessionInfo{}, false, session.RequireCanonicalSession(info)
	}
	current := s.snapshot()
	active := current.sessionID == info.SessionID &&
		canonicalSessionDirectory(current.transcriptDir) ==
			canonicalSessionDirectory(info.TranscriptDir)
	return info, active, nil
}

func (s *SessionService) recorderForTarget(
	info session.SessionInfo,
	active bool,
) (*transcript.Recorder, bool, error) {
	if active {
		s.engine.mu.Lock()
		recorder := s.engine.transcript
		sessionID := s.engine.config.SessionID
		transcriptDir := s.engine.config.TranscriptDir
		s.engine.mu.Unlock()
		if recorder == nil ||
			sessionID != info.SessionID ||
			canonicalSessionDirectory(transcriptDir) !=
				canonicalSessionDirectory(info.TranscriptDir) {
			return nil, false, errors.New("active session changed before operation")
		}
		if recorder.Path() == "" {
			return nil, false, errors.New("active session has no transcript recorder")
		}
		return recorder, false, nil
	}
	sourceDir := info.TranscriptDir
	if sourceDir == "" {
		sourceDir = filepath.Dir(info.TranscriptPath)
	}
	recorder := transcript.NewRecorder(info.SessionID, sourceDir)
	path := info.TranscriptPath
	if path == "" {
		path = recorder.Path()
	}
	if _, err := os.Stat(path); err != nil {
		return nil, false, fmt.Errorf("stat session transcript: %w", err)
	}
	return recorder, true, nil
}

func (s *SessionService) exportPath(
	sessionID string,
	filename string,
	snapshot sessionServiceSnapshot,
) string {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		now := time.Now()
		if snapshot.clock != nil {
			now = snapshot.clock()
		}
		filename = fmt.Sprintf(
			identity.CommandName+"-session-%s-%s.md",
			safeSessionFilename(sessionID),
			now.UTC().Format("20060102-150405"),
		)
	}
	if filepath.Ext(filename) == "" {
		filename += ".md"
	} else if !strings.EqualFold(filepath.Ext(filename), ".md") {
		filename = strings.TrimSuffix(filename, filepath.Ext(filename)) + ".md"
	}
	if !filepath.IsAbs(filename) {
		filename = filepath.Join(snapshot.cwd, filename)
	}
	return filepath.Clean(filename)
}

func (s *SessionService) snapshot() sessionServiceSnapshot {
	if s == nil || s.engine == nil {
		return sessionServiceSnapshot{}
	}
	s.engine.mu.Lock()
	defer s.engine.mu.Unlock()
	return sessionServiceSnapshot{
		sessionID:         s.engine.config.SessionID,
		cwd:               s.engine.config.CWD,
		transcriptDir:     s.engine.config.TranscriptDir,
		catalogPath:       s.engine.config.SessionCatalogPath,
		legacyCatalogPath: s.engine.config.LegacySessionCatalogPath,
		clock:             s.engine.config.Clock,
		recorder:          s.engine.transcript,
	}
}

func safeSessionFilename(sessionID string) string {
	var builder strings.Builder
	for _, char := range sessionID {
		switch {
		case char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9',
			char == '-',
			char == '_':
			builder.WriteRune(char)
		}
		if builder.Len() == 32 {
			break
		}
	}
	if builder.Len() == 0 {
		return "session"
	}
	return builder.String()
}
