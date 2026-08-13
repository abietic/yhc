package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"github.com/abietic/yhc/engine"
	enginesession "github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/internal/statepath"
)

var (
	errSessionBusy           = errors.New("session already has an active turn")
	errSessionClosed         = errors.New("session is closed")
	errInteractionNotFound   = errors.New("interaction not found")
	errPlanReviewUnavailable = errors.New("plan review unavailable")
	errPlanReviewTooLarge    = errors.New("plan review too large")
	errPlanReviewChanged     = errors.New("plan review changed")
)

const (
	maxSessionIDBytes  = 256
	maxPlanReviewBytes = 1 << 20
)

func normalizeSessionID(value string) (string, error) {
	id := strings.TrimSpace(value)
	if id == "" || len(id) > maxSessionIDBytes {
		return "", errors.New("session_id must be a bounded safe identifier")
	}
	for index := 0; index < len(id); index++ {
		character := id[index]
		alphanumeric := character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9'
		if alphanumeric || index > 0 &&
			(character == '.' || character == '_' || character == '-') {
			continue
		}
		return "", errors.New("session_id must be a bounded safe identifier")
	}
	return id, nil
}

func isSupportedSessionID(value string) bool {
	_, err := normalizeSessionID(value)
	return err == nil
}

// SessionEngine is the runtime authority owned by one app-server session.
type SessionEngine interface {
	SubmitMessage(context.Context, string) (<-chan engine.QueryEvent, engine.Terminal)
	SubmitRuntimeItem(context.Context, engine.RuntimeItem) (<-chan engine.QueryEvent, engine.Terminal)
	ClaimNextRuntimeItem() (engine.RuntimeItem, bool, error)
	PendingProjectGraphPermissionRequest() (engine.PermissionRequestEvent, bool)
	ResolvePermissionInteraction(string, engine.PermissionInteractionResult) bool
	RequestStop(engine.RuntimeStopMode, string) error
	RuntimeSnapshot() engine.RuntimeSnapshot
	SubscribeAsyncHookEvents() <-chan engine.QueryEvent
	SessionID() string
	ThreadID() string
	AgentID() string
	TranscriptPath() string
	Close()
}

// EngineOptions contains transport-neutral callbacks and runtime identity.
type EngineOptions struct {
	SessionID              string
	ThreadID               string
	CWD                    string
	TranscriptDir          string
	Resume                 bool
	PermissionPrompt       engine.PermissionPromptFn
	RepeatedToolCallPrompt engine.RepeatedToolCallPromptFn
}

// EngineFactory builds one engine after the app-server owns its live-session slot.
type EngineFactory func(context.Context, EngineOptions) (SessionEngine, error)

type session struct {
	mu        sync.Mutex
	closeOnce sync.Once
	wg        sync.WaitGroup
	closeDone chan struct{}
	closeErr  error

	id             string
	threadID       string
	cwd            string
	title          string
	workspaceLabel string
	createdAt      time.Time
	updatedAt      time.Time
	status         string
	lastError      string

	engine      SessionEngine
	lease       *sessionLease
	events      *eventLog
	activity    *activityLog
	permissions *permissionBroker
	transcript  *transcriptPager
	rootCtx     context.Context
	rootCancel  context.CancelFunc

	activeTurnID string
	activePrompt string
	activeCancel context.CancelFunc
	closed       bool
}

func newSession(
	ctx context.Context,
	factory EngineFactory,
	serverID string,
	input CreateSessionRequest,
	admitted *enginesession.SessionInfo,
	eventBuffer int,
	now time.Time,
) (*session, error) {
	cwd, transcriptDir, err := sessionStorageAdmission(input, admitted)
	if err != nil {
		return nil, err
	}
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		sessionID = uuid.NewString()
	} else if sessionID, err = normalizeSessionID(sessionID); err != nil {
		return nil, err
	}
	if input.Resume && admitted == nil {
		return nil, fmt.Errorf("session resume is unavailable until an explicit first-turn attach")
	}
	if input.Resume && strings.TrimSpace(input.SessionID) == "" {
		return nil, fmt.Errorf("resume requires session_id")
	}
	lease, err := acquireSessionLease(transcriptDir, sessionID, serverID)
	if err != nil {
		return nil, err
	}
	permissions := newPermissionBroker()
	sessionCtx, sessionCancel := context.WithCancel(ctx)
	options := EngineOptions{SessionID: sessionID, ThreadID: sessionID, CWD: cwd, TranscriptDir: transcriptDir, Resume: input.Resume, PermissionPrompt: permissions.prompt, RepeatedToolCallPrompt: permissions.repeatedPrompt}
	runtime, err := factory(sessionCtx, options)
	if err != nil {
		sessionCancel()
		permissions.close()
		_ = lease.close()
		return nil, fmt.Errorf("create session engine: %w", err)
	}
	if runtime == nil {
		sessionCancel()
		permissions.close()
		_ = lease.close()
		return nil, fmt.Errorf("create session engine: factory returned nil runtime")
	}
	if actual := strings.TrimSpace(runtime.SessionID()); actual != sessionID {
		sessionCancel()
		runtime.Close()
		permissions.close()
		_ = lease.close()
		return nil, fmt.Errorf("session engine identity mismatch: got %q, want %q", actual, sessionID)
	}
	threadID := sessionID
	if actual := strings.TrimSpace(runtime.ThreadID()); actual != "" {
		threadID = actual
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = baseName(cwd)
	}
	s := &session{id: sessionID, threadID: threadID, cwd: cwd, title: title, workspaceLabel: workspaceLabel(cwd), createdAt: now, updatedAt: now, status: "idle", engine: runtime, lease: lease, events: newEventLog(eventBuffer), activity: newActivityLog(), permissions: permissions, transcript: newTranscriptPager(runtime.TranscriptPath()), rootCtx: sessionCtx, rootCancel: sessionCancel, closeDone: make(chan struct{})}
	s.startAsyncHookPump()
	s.publishSynthetic("session.created", "", map[string]any{"workspace_label": s.workspaceLabel, "resumed": input.Resume})
	return s, nil
}

func sessionStorageAdmission(
	input CreateSessionRequest,
	admitted *enginesession.SessionInfo,
) (string, string, error) {
	if admitted != nil {
		if !input.Resume || admitted.SessionID != strings.TrimSpace(input.SessionID) {
			return "", "", fmt.Errorf("durable session admission identity mismatch")
		}
		cwd, err := validateCWD(admitted.CWD)
		if err != nil {
			return "", "", err
		}
		transcriptDir := strings.TrimSpace(admitted.TranscriptDir)
		if transcriptDir == "" || !filepath.IsAbs(transcriptDir) || admitted.ReadOnly || admitted.NeedsImport {
			return "", "", fmt.Errorf("durable session admission is not canonical and writable")
		}
		return cwd, filepath.Clean(transcriptDir), nil
	}
	cwd, err := validateCWD(input.CWD)
	if err != nil {
		return "", "", err
	}
	roots, err := statepath.ProjectRoots(cwd)
	if err != nil {
		return "", "", fmt.Errorf("resolve canonical session storage: %w", err)
	}
	return cwd, filepath.Join(roots.Canonical, "transcripts"), nil
}

func (s *session) startAsyncHookPump() {
	events := s.engine.SubscribeAsyncHookEvents()
	if events == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			select {
			case event, open := <-events:
				if !open {
					return
				}
				s.publishEngine(event, "")
			case <-s.rootCtx.Done():
				return
			}
		}
	}()
}

func (s *session) summary() SessionSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionSummary{
		ID:             s.id,
		ThreadID:       s.threadID,
		WorkspaceLabel: s.workspaceLabel,
		Status:         s.status,
		ActiveTurnID:   s.activeTurnID,
		CreatedAt:      s.createdAt,
		UpdatedAt:      s.updatedAt,
		LastError:      s.lastError,
	}
}

func (s *session) snapshot() SessionSnapshot {
	runtime := s.engine.RuntimeSnapshot()
	threadID := runtime.ActiveThreadID
	if threadID == "" {
		threadID = s.threadID
	}
	thread, ok := runtime.Threads[threadID]
	if !ok {
		thread = runtime.Threads[s.threadID]
	}
	messages, transcriptErr := s.transcript.latest(256)
	durableLoaded := transcriptErr == nil
	if transcriptErr != nil {
		messages = make([]SnapshotMessage, 0, len(thread.Messages)+1)
		if source, ok := s.engine.(interface{ GetMessages() []*schema.Message }); ok {
			history := source.GetMessages()
			if len(history) > 256 {
				history = history[len(history)-256:]
			}
			for index, message := range history {
				if message == nil || message.Role == schema.System {
					continue
				}
				if isMeta, _ := message.Extra["is_meta"].(bool); isMeta {
					continue
				}
				messages = append(messages, snapshotConversationMessage(message, uint64(index+1)))
			}
		}
	}

	s.mu.Lock()
	activeTurnID := s.activeTurnID
	activePrompt := s.activePrompt
	s.mu.Unlock()
	known := make(map[string]struct{}, len(messages))
	for _, message := range messages {
		if message.ID != "" {
			known[message.ID] = struct{}{}
		}
	}
	runtimeUserOwnsActiveTurn := false
	for _, message := range thread.Messages {
		if message.TurnID == activeTurnID && message.Role == string(schema.User) {
			runtimeUserOwnsActiveTurn = true
		}
		if durableLoaded && message.Completed && strings.TrimSpace(message.TranscriptEntryID) == "" {
			continue
		}
		snapshot := snapshotMessage(message)
		if snapshot.ID == "" {
			messages = append(messages, snapshot)
			continue
		}
		if _, exists := known[snapshot.ID]; !exists {
			messages = append(messages, snapshot)
			known[snapshot.ID] = struct{}{}
		}
	}
	if activeTurnID != "" && activePrompt != "" && !runtimeUserOwnsActiveTurn {
		messages = append(messages, SnapshotMessage{
			ID:        "runtime-prompt:" + activeTurnID,
			TurnID:    activeTurnID,
			Role:      string(schema.User),
			Content:   truncateSnapshotText(activePrompt, 32<<10),
			Completed: true,
			Source:    "runtime",
		})
	}
	if live := thread.LiveMessage; live != nil {
		snapshot := snapshotMessage(*live)
		if snapshot.ID == "" {
			messages = append(messages, snapshot)
		} else if _, exists := known[snapshot.ID]; !exists {
			messages = append(messages, snapshot)
			known[snapshot.ID] = struct{}{}
		}
	}
	return SessionSnapshot{Session: s.summary(), EventCursor: s.events.latestCursor(), Messages: messages, Interactions: s.permissions.interactions(), Activity: s.activity.snapshot()}
}

func snapshotConversationMessage(message *schema.Message, sequence uint64) SnapshotMessage {
	toolCalls := make([]SnapshotToolCall, 0, len(message.ToolCalls))
	for _, call := range message.ToolCalls {
		toolCalls = append(toolCalls, SnapshotToolCall{
			ID:           call.ID,
			Name:         call.Function.Name,
			InputPreview: truncateSnapshotText(call.Function.Arguments, 512),
		})
	}
	return SnapshotMessage{
		Sequence:         sequence,
		Role:             string(message.Role),
		Content:          truncateSnapshotText(message.Content, 32<<10),
		ReasoningContent: truncateSnapshotText(message.ReasoningContent, 8<<10),
		ToolCallID:       message.ToolCallID,
		ToolName:         message.ToolName,
		ToolCalls:        toolCalls,
		Completed:        true,
		Source:           "conversation-fallback",
	}
}

func snapshotMessage(message engine.RuntimeMessageSnapshot) SnapshotMessage {
	toolCalls := make([]SnapshotToolCall, 0, len(message.ToolCalls))
	for _, call := range message.ToolCalls {
		toolCalls = append(toolCalls, SnapshotToolCall{
			ID:           call.ID,
			Name:         call.Name,
			InputPreview: call.InputPreview,
		})
	}
	id := strings.TrimSpace(message.TranscriptEntryID)
	source := "durable"
	if id == "" {
		id = message.ID
		source = "runtime"
	}
	return SnapshotMessage{
		ID:               id,
		TurnID:           message.TurnID,
		Sequence:         message.Sequence,
		Role:             message.Role,
		Content:          truncateSnapshotText(message.Content, 32<<10),
		ReasoningContent: truncateSnapshotText(message.ReasoningContent, 8<<10),
		ToolCallID:       message.ToolCallID,
		ToolName:         message.ToolName,
		ToolCalls:        toolCalls,
		Completed:        message.Completed,
		Timestamp:        message.Timestamp,
		Source:           source,
	}
}

func truncateSnapshotText(value string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "\n…[truncated]"
}

func (s *session) startTurn(input StartTurnRequest) (StartTurnResponse, error) {
	prompt := strings.TrimSpace(input.Prompt)
	if prompt == "" {
		return StartTurnResponse{}, fmt.Errorf("prompt is required")
	}
	if !utf8.ValidString(prompt) {
		return StartTurnResponse{}, fmt.Errorf("prompt must be valid UTF-8")
	}
	if len(prompt) > maxPromptBytes {
		return StartTurnResponse{}, fmt.Errorf("prompt exceeds %d bytes", maxPromptBytes)
	}
	turnID := strings.TrimSpace(input.ClientTurnID)
	if turnID == "" {
		turnID = uuid.NewString()
	} else if _, err := uuid.Parse(turnID); err != nil {
		return StartTurnResponse{}, fmt.Errorf("client_turn_id must be a UUID: %w", err)
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return StartTurnResponse{}, errSessionClosed
	}
	if s.activeTurnID != "" {
		s.mu.Unlock()
		return StartTurnResponse{}, errSessionBusy
	}
	turnCtx, cancel := context.WithCancel(s.rootCtx)
	s.activeTurnID = turnID
	s.activePrompt = prompt
	s.activeCancel = cancel
	s.status = "running"
	s.updatedAt = time.Now().UTC()
	s.lastError = ""
	s.wg.Add(1)
	s.mu.Unlock()

	s.publishSynthetic("user_message", turnID, map[string]any{"content": prompt})
	s.publishSynthetic("turn.accepted", turnID, map[string]any{"turn_id": turnID})
	go s.runTurn(turnCtx, turnID, prompt)
	return StartTurnResponse{SessionID: s.id, TurnID: turnID, Accepted: true}, nil
}

// restorePendingProjectGraph publishes a recovered, callback-backed project
// graph interaction without inventing a user prompt or active prompt.
func (s *session) restorePendingProjectGraph() (*InteractionSnapshot, error) {
	pending, ok := s.engine.PendingProjectGraphPermissionRequest()
	if !ok {
		return nil, nil
	}
	turnID := uuid.NewString()
	prompt := permissionPromptRequest(s.id, s.threadID, s.engine.AgentID(), pending)
	if _, ok := projectInteraction(prompt, turnID); !ok {
		return nil, fmt.Errorf("recovered project graph permission is not projectable")
	}
	s.permissions.prepare(prompt)
	s.permissions.observeEvent(prompt, turnID)
	interaction, ok := s.permissions.interaction(prompt.ToolUseID)
	if !ok {
		return nil, fmt.Errorf("recovered project graph permission is unavailable")
	}
	turnCtx, cancel := context.WithCancel(s.rootCtx)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return nil, errSessionClosed
	}
	s.activeTurnID = turnID
	s.activePrompt = ""
	s.activeCancel = cancel
	s.status = "waiting"
	s.updatedAt = time.Now().UTC()
	s.wg.Add(1)
	s.mu.Unlock()
	s.publishEngine(engine.QueryEvent{
		RuntimeEventEnvelope: engine.RuntimeEventEnvelope{
			SessionID: s.id,
			ThreadID:  s.threadID,
			TurnID:    turnID,
			Timestamp: time.Now().UTC(),
		},
		Type:              engine.EventPermissionRequest,
		PermissionRequest: &pending,
	}, turnID)
	go func() {
		defer s.wg.Done()
		err := s.resolveProjectGraphPermission(turnCtx, turnID, pending, true)
		if err != nil {
			s.finishTurn(turnID, engine.TerminalModelError, err)
			return
		}
		item, claimed, err := s.engine.ClaimNextRuntimeItem()
		if err != nil || !claimed || item.Kind != engine.RuntimeItemPermissionDecision {
			if err == nil {
				err = fmt.Errorf("project graph permission decision was not claimable")
			}
			s.finishTurn(turnID, engine.TerminalModelError, err)
			return
		}
		events, terminal := s.engine.SubmitRuntimeItem(turnCtx, item)
		reason, err := s.driveEvents(turnCtx, turnID, events, terminal)
		s.finishTurn(turnID, reason, err)
	}()
	return &interaction, nil
}

func (s *session) runTurn(ctx context.Context, turnID, prompt string) {
	defer s.wg.Done()
	reason, err := s.drivePendingProjectGraph(ctx, turnID)
	if err == nil {
		events, terminal := s.engine.SubmitMessage(ctx, prompt)
		reason, err = s.driveEvents(ctx, turnID, events, terminal)
	}
	if reason == "" {
		if ctx.Err() != nil {
			reason = engine.TerminalAbortedStreaming
		} else if err != nil {
			reason = engine.TerminalModelError
		} else {
			reason = engine.TerminalCompleted
		}
	}
	s.finishTurn(turnID, reason, err)
}

func (s *session) drivePendingProjectGraph(
	ctx context.Context,
	turnID string,
) (engine.TerminalReason, error) {
	pending, ok := s.engine.PendingProjectGraphPermissionRequest()
	if !ok {
		return "", nil
	}
	if err := s.resolveProjectGraphPermission(ctx, turnID, pending, false); err != nil {
		return "", err
	}
	item, claimed, err := s.engine.ClaimNextRuntimeItem()
	if err != nil {
		return "", err
	}
	if !claimed || item.Kind != engine.RuntimeItemPermissionDecision {
		return "", fmt.Errorf("project graph permission decision was not claimable")
	}
	events, terminal := s.engine.SubmitRuntimeItem(ctx, item)
	return s.driveEvents(ctx, turnID, events, terminal)
}

func (s *session) driveEvents(
	ctx context.Context,
	clientTurnID string,
	events <-chan engine.QueryEvent,
	submitTerminal engine.Terminal,
) (engine.TerminalReason, error) {
	for {
		terminalReason := submitTerminal.Reason
		var terminalErr error
		if submitTerminal.Err != nil {
			terminalErr = submitTerminal.Err
		}
		for event := range events {
			if event.Type == engine.EventPermissionRequest && event.PermissionRequest != nil {
				request := permissionPromptRequest(s.id, s.threadID, s.engine.AgentID(), *event.PermissionRequest)
				if event.ThreadID != "" {
					request.ThreadID = event.ThreadID
				}
				if event.AgentID != "" {
					request.AgentID = event.AgentID
				}
				if event.SessionID != "" {
					request.SessionID = event.SessionID
				}
				s.permissions.observeEvent(request, event.TurnID)
				if event.PermissionRequest.Source == "project_graph" {
					// This session is the ProjectGraph callback owner. Freeze that
					// observation before publishing so the first visible card is
					// already resolvable; wait below is an idempotent duplicate.
					s.permissions.prepare(request)
				}
				if _, ready := s.permissions.awaitInteraction(
					ctx,
					event.PermissionRequest.ToolUseID,
				); !ready {
					continue
				}
			}
			s.publishEngine(event, clientTurnID)
			if event.Type == engine.EventPermissionRequest &&
				event.PermissionRequest != nil &&
				event.PermissionRequest.Source == "project_graph" {
				if err := s.resolveProjectGraphPermission(
					ctx,
					clientTurnID,
					*event.PermissionRequest,
					true,
				); err != nil {
					return terminalReason, err
				}
			}
			if event.Type == engine.EventTerminal && event.TerminalInfo != nil {
				terminalReason = event.TerminalInfo.Reason
				terminalErr = event.TerminalInfo.Err
			}
		}
		if terminalReason != engine.TerminalWaitingInput {
			return terminalReason, terminalErr
		}
		item, ok, err := s.engine.ClaimNextRuntimeItem()
		if err != nil {
			return terminalReason, err
		}
		if !ok || item.Kind != engine.RuntimeItemPermissionDecision {
			return terminalReason, terminalErr
		}
		events, submitTerminal = s.engine.SubmitRuntimeItem(ctx, item)
	}
}

func (s *session) resolveProjectGraphPermission(
	ctx context.Context,
	clientTurnID string,
	request engine.PermissionRequestEvent,
	alreadyPublished bool,
) error {
	prompt := permissionPromptRequest(s.id, s.threadID, s.engine.AgentID(), request)
	if !alreadyPublished {
		s.permissions.observeEvent(prompt, clientTurnID)
		s.permissions.prepare(prompt)
		s.publishEngine(engine.QueryEvent{
			RuntimeEventEnvelope: engine.RuntimeEventEnvelope{
				SessionID: s.id,
				ThreadID:  s.threadID,
				TurnID:    clientTurnID,
				Timestamp: time.Now().UTC(),
			},
			Type:              engine.EventPermissionRequest,
			PermissionRequest: &request,
		}, clientTurnID)
	}
	result := s.permissions.wait(ctx, prompt)
	if !s.engine.ResolvePermissionInteraction(request.ToolUseID, result) {
		return fmt.Errorf("project graph permission request %q is no longer active", request.ToolUseID)
	}
	return nil
}

func (s *session) finishTurn(
	turnID string,
	reason engine.TerminalReason,
	err error,
) {
	status := "idle"
	errText := ""
	if err != nil {
		errText = err.Error()
		status = "error"
	}
	if reason == engine.TerminalWaitingInput {
		status = "waiting"
	}
	s.mu.Lock()
	if s.activeTurnID == turnID {
		s.activeTurnID = ""
		s.activePrompt = ""
		s.activeCancel = nil
	}
	if !s.closed {
		s.status = status
	}
	s.updatedAt = time.Now().UTC()
	s.lastError = errText
	s.mu.Unlock()
	s.publishSynthetic("turn.finished", turnID, map[string]any{
		"reason": reason,
		"error":  errText,
	})
}

func (s *session) cancelTurn(input CancelTurnRequest) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errSessionClosed
	}
	activeTurnID := s.activeTurnID
	cancel := s.activeCancel
	s.mu.Unlock()
	if activeTurnID == "" {
		return fmt.Errorf("session has no active turn")
	}
	if input.TurnID != "" && input.TurnID != activeTurnID {
		return fmt.Errorf("turn %s is not active", input.TurnID)
	}
	mode := engine.RuntimeStopImmediate
	if strings.EqualFold(strings.TrimSpace(input.Mode), string(engine.RuntimeStopGraceful)) {
		mode = engine.RuntimeStopGraceful
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		reason = "desktop user requested cancellation"
	}
	if err := s.engine.RequestStop(mode, reason); err != nil {
		return err
	}
	if mode == engine.RuntimeStopImmediate && cancel != nil {
		cancel()
	}
	s.publishSynthetic("turn.cancel.requested", activeTurnID, map[string]any{
		"mode":   mode,
		"reason": reason,
	})
	return nil
}

func (s *session) resolveInteraction(
	requestID string,
	input ResolveInteractionRequest,
) interactionResolveStatus {
	return s.permissions.resolve(requestID, input)
}

func (s *session) interactionPlanReview(requestID string) (PlanReviewResponse, error) {
	waiter, request, ok := s.permissions.reviewable(requestID)
	if !ok {
		return PlanReviewResponse{}, errInteractionNotFound
	}
	if request.Kind != engine.PermissionInteractionKindPlanApproval || request.PlanApproval == nil {
		return PlanReviewResponse{}, errPlanReviewUnavailable
	}
	file, err := os.Open(request.PlanApproval.PlanFileIdentity)
	if err != nil {
		return PlanReviewResponse{}, errPlanReviewUnavailable
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxPlanReviewBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || !utf8.Valid(data) {
		return PlanReviewResponse{}, errPlanReviewUnavailable
	}
	if len(data) > maxPlanReviewBytes {
		return PlanReviewResponse{}, errPlanReviewTooLarge
	}
	digest := engine.PlanBytesDigest(data)
	if digest != request.PlanApproval.InitialPlanDigest {
		return PlanReviewResponse{}, errPlanReviewChanged
	}
	if !s.permissions.recordPlanReview(
		request.ToolUseID,
		waiter,
		request.PlanApproval.PlanRevision,
		digest,
	) {
		return PlanReviewResponse{}, errInteractionNotFound
	}
	return PlanReviewResponse{
		Content:  string(data),
		Revision: request.PlanApproval.PlanRevision,
		Digest:   digest,
	}, nil
}

func (s *session) close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		go s.closeResources()
	})
	select {
	case <-s.closeDone:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *session) closeResources() {
	defer close(s.closeDone)
	defer func() {
		if err := s.lease.close(); s.closeErr == nil && err != nil {
			s.closeErr = err
		}
	}()
	s.mu.Lock()
	s.closed = true
	s.status = "closed"
	s.activePrompt = ""
	s.activeCancel = nil
	s.updatedAt = time.Now().UTC()
	s.mu.Unlock()

	s.permissions.close()
	s.rootCancel()
	_ = s.engine.RequestStop(engine.RuntimeStopImmediate, "app-server session closed")
	s.wg.Wait()
	s.engine.Close()
	s.publishSynthetic("session.closed", "", map[string]any{})
	s.events.close()
}

func permissionPromptRequest(
	sessionID, threadID, agentID string,
	request engine.PermissionRequestEvent,
) engine.PermissionPromptRequest {
	return engine.PermissionPromptRequest{
		Kind:               request.Kind,
		Attempt:            request.Attempt,
		Source:             request.Source,
		ToolName:           request.ToolName,
		CanonicalToolName:  request.CanonicalToolName,
		ToolUseID:          request.ToolUseID,
		Input:              request.Input,
		Message:            request.Message,
		SessionID:          sessionID,
		ThreadID:           threadID,
		AgentID:            agentID,
		PlanApproval:       request.PlanApproval,
		Presentation:       clonePermissionPresentationForAppserver(request.Presentation),
		DecisionConstraint: request.DecisionConstraint,
	}
}

func clonePermissionPresentationForAppserver(value *engine.PermissionPresentation) *engine.PermissionPresentation {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Evidence = append([]engine.PermissionPresentationEvidence(nil), value.Evidence...)
	copy.GrantScopes = append([]engine.PermissionInteractionDecision(nil), value.GrantScopes...)
	return &copy
}

func (s *session) publishSynthetic(eventType, turnID string, data any) {
	timestamp := time.Now().UTC()
	encoded := marshalEventData(data)
	if _, published := s.events.publish(WireEvent{
		ProtocolVersion: ProtocolVersion,
		Type:            eventType,
		SessionID:       s.id,
		ThreadID:        s.threadID,
		TurnID:          turnID,
		Timestamp:       timestamp,
		Data:            encoded,
	}); !published {
		return
	}
	if entry, ok := projectSyntheticActivity(eventType, turnID, data, timestamp); ok {
		s.publishActivity(entry)
	}
}

func (s *session) publishEngine(event engine.QueryEvent, fallbackTurnID string) {
	timestamp := event.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}
	sessionID := event.SessionID
	if sessionID == "" {
		sessionID = s.id
	}
	threadID := event.ThreadID
	if threadID == "" {
		threadID = s.threadID
	}
	turnID := event.TurnID
	if turnID == "" {
		turnID = fallbackTurnID
	}
	event.TurnID = turnID
	event.Timestamp = timestamp
	wireType := string(event.Type)
	data := queryEventData(event)
	if event.Type == engine.EventPermissionRequest && event.PermissionRequest != nil {
		interaction, ok := s.permissions.interaction(event.PermissionRequest.ToolUseID)
		if !ok {
			return
		}
		wireType = "interaction_requested"
		data = marshalEventData(interaction)
	} else if event.Type == engine.EventPermissionResolved && event.PermissionResolved != nil {
		wireType = "interaction_resolved"
		data = marshalEventData(map[string]string{"request_id": event.PermissionResolved.ToolUseID})
	}
	if _, published := s.events.publish(WireEvent{
		ProtocolVersion: ProtocolVersion,
		Type:            wireType,
		SessionID:       sessionID,
		ThreadID:        threadID,
		TurnID:          turnID,
		AgentID:         event.AgentID,
		Sequence:        event.Sequence,
		Timestamp:       timestamp,
		CausationID:     event.CausationID,
		Data:            data,
	}); !published {
		return
	}
	if entry, ok := projectEngineActivity(event, fallbackTurnID); ok {
		s.publishActivity(entry)
	}
}

func (s *session) publishActivity(entry ActivityEntry) {
	if s.activity == nil {
		return
	}
	normalized, updated := s.activity.upsertEntry(entry)
	if !updated {
		return
	}
	_, _ = s.events.publish(WireEvent{
		ProtocolVersion: ProtocolVersion,
		Type:            "activity",
		SessionID:       s.id,
		ThreadID:        s.threadID,
		TurnID:          normalized.TurnID,
		Timestamp:       normalized.Timestamp,
		Data:            marshalEventData(normalized),
	})
}

type wireMessage struct {
	Role             schema.RoleType   `json:"role"`
	Content          string            `json:"content"`
	Name             string            `json:"name,omitempty"`
	ToolCalls        []schema.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
	ToolName         string            `json:"tool_name,omitempty"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
}

func messageData(message *schema.Message) *wireMessage {
	if message == nil {
		return nil
	}
	return &wireMessage{
		Role:             message.Role,
		Content:          message.Content,
		Name:             message.Name,
		ToolCalls:        message.ToolCalls,
		ToolCallID:       message.ToolCallID,
		ToolName:         message.ToolName,
		ReasoningContent: message.ReasoningContent,
	}
}

func queryEventData(event engine.QueryEvent) json.RawMessage {
	switch event.Type {
	case engine.EventAssistant:
		message := event.AssistantMessage
		if message == nil {
			message = event.Message
		}
		return marshalEventData(map[string]any{
			"message":             messageData(message),
			"transcript_entry_id": event.TranscriptEntryID,
		})
	case engine.EventStream:
		message := event.StreamEvent
		if message == nil {
			message = event.Message
		}
		return marshalEventData(map[string]any{"message": messageData(message)})
	case engine.EventToolResult:
		message := event.ToolResultMessage
		if message == nil {
			message = event.Message
		}
		return marshalEventData(map[string]any{
			"message":             messageData(message),
			"transcript_entry_id": event.TranscriptEntryID,
		})
	case engine.EventTerminal:
		if event.TerminalInfo == nil {
			return marshalEventData(map[string]any{})
		}
		errText := ""
		if event.TerminalInfo.Err != nil {
			errText = event.TerminalInfo.Err.Error()
		}
		return marshalEventData(map[string]any{
			"reason":     event.TerminalInfo.Reason,
			"turn_count": event.TerminalInfo.TurnCount,
			"max_turns":  event.TerminalInfo.MaxTurns,
			"error":      errText,
		})
	case engine.EventToolUseSummary:
		return marshalEventData(event.ToolUseSummary)
	case engine.EventCommandLifecycle:
		return marshalEventData(event.CommandLifecycle)
	case engine.EventCommandResult:
		return marshalEventData(event.CommandResult)
	case engine.EventToolProgress:
		return marshalEventData(event.ToolProgress)
	case engine.EventTaskProgress:
		return marshalEventData(event.TaskProgress)
	case engine.EventAgentLifecycle:
		return marshalEventData(event.AgentLifecycle)
	case engine.EventTaskLifecycle:
		return marshalEventData(event.TaskLifecycle)
	case engine.EventWorktreeLifecycle:
		return marshalEventData(event.WorktreeLifecycle)
	case engine.EventHookStatus:
		return marshalEventData(event.HookStatus)
	case engine.EventHookResponse:
		return marshalEventData(event.HookResponse)
	case engine.EventClassifierStatus:
		return marshalEventData(event.ClassifierStatus)
	case engine.EventPlanStateTransition:
		return marshalEventData(event.PlanStateTransition)
	default:
		return marshalEventData(map[string]any{
			"transcript_entry_id": event.TranscriptEntryID,
		})
	}
}

func marshalEventData(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err == nil {
		return encoded
	}
	fallback, _ := json.Marshal(map[string]string{"encoding_error": err.Error()})
	return fallback
}
