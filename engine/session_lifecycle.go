package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/abietic/yhc/engine/auth"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

// startNewSessionForCommandTurn creates a durable empty session before
// activating it through the same restore boundary used by /resume.
func (e *QueryEngine) startNewSessionForCommandTurn(
	ctx context.Context,
	turnID string,
) (*session.ResumedSession, error) {
	if e == nil {
		return nil, errors.New("new session requires an engine")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("new session canceled before persistence: %w", err)
	}
	turnID = strings.TrimSpace(turnID)

	e.planMu.Lock()
	if e.planActiveTurnID == "" || e.planActiveTurnID != turnID {
		owner := e.planActiveTurnID
		e.planMu.Unlock()
		return nil, fmt.Errorf(
			"%w: command turn %s does not own the boundary (owner %s)",
			ErrPlanTransitionInFlight,
			turnID,
			owner,
		)
	}
	e.mu.Lock()
	modelName := e.config.Model
	permissionMode := e.config.PermissionMode
	cwd := e.config.CWD
	transcriptDir := e.config.TranscriptDir
	worktreePath := e.config.WorktreePath
	worktreeBranch := e.config.WorktreeBranch
	additionalDirs := append([]string(nil), e.config.AdditionalDirs...)
	clock := e.config.Clock
	e.mu.Unlock()
	e.planMu.Unlock()

	if strings.TrimSpace(transcriptDir) == "" {
		return nil, errors.New("new session requires a transcript directory")
	}
	kernelSelection := initialSessionQueryKernelSelection(nil)
	if kernelSelection.err != nil {
		return nil, fmt.Errorf("select new session query kernel: %w", kernelSelection.err)
	}

	var sessionID string
	var recorder *transcript.Recorder
	for {
		sessionID = generateUUID()
		recorder = transcript.NewRecorder(sessionID, transcriptDir)
		if _, err := os.Stat(recorder.Path()); errors.Is(err, os.ErrNotExist) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("inspect new session transcript: %w", err)
		}
	}
	now := clock().UTC()
	planState := initialPlanState(QueryEngineConfig{
		SessionID:      sessionID,
		PermissionMode: permissionMode,
	})
	metadata := &session.SessionMetadataFull{
		SessionID:                  sessionID,
		ThreadID:                   sessionID,
		Model:                      modelName,
		Provider:                   auth.DetectProvider(modelName),
		Status:                     "idle",
		PermissionMode:             string(permissionMode),
		QueryKernelVersion:         kernelSelection.version,
		QueryKernelStage:           string(kernelSelection.stage),
		QueryKernelIncompatibility: kernelSelection.incompatibility,
		PlanState:                  persistedPlanState(planState),
		WorktreePath:               worktreePath,
		WorktreeBranch:             worktreeBranch,
		AdditionalDirs:             additionalDirs,
		CreatedAt:                  now,
		UpdatedAt:                  now,
		MessageCount:               0,
		IsLeaf:                     true,
		GitBranch:                  detectSessionGitBranch(cwd),
		CWD:                        cwd,
	}
	if err := session.WriteSessionMetadata(recorder, metadata); err != nil {
		discardUncommittedSession(recorder)
		return nil, fmt.Errorf("write new session metadata: %w", err)
	}
	// The fsynced start boundary is the commit marker for the metadata written
	// immediately before it. Cancellation after this point must not create a
	// half-switched in-memory identity, so activation runs to completion.
	if err := recorder.RecordLifecycleBoundaryWithUsage(
		transcript.LifecycleSessionStart,
		nil,
		nil,
		nil,
		transcript.UsageSummary{},
		true,
	); err != nil {
		discardUncommittedSession(recorder)
		return nil, fmt.Errorf("commit new session start: %w", err)
	}
	_ = recorder.Close()

	resumed, err := e.resumeSessionForCommandTurn(
		context.WithoutCancel(ctx),
		sessionID,
		turnID,
	)
	if err != nil {
		_ = os.Remove(recorder.Path())
		return nil, fmt.Errorf("activate committed new session %s: %w", sessionID, err)
	}
	return resumed, nil
}

func discardUncommittedSession(recorder *transcript.Recorder) {
	if recorder == nil {
		return
	}
	_ = recorder.Close()
	_ = os.Remove(recorder.Path())
}
