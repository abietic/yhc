package appserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/abietic/yhc/engine"
)

const (
	maxQueuedPromptDisplayRunes = 2048
	runtimeQueueBlockedMessage  = "Queued work needs attention before it can continue."
)

var (
	errRuntimeQueueUnavailable = errors.New("runtime queue is unavailable")
	errQueueRequiresActiveTurn = errors.New("queued prompts require an active turn")
	errQueuedPromptNotPending  = errors.New("queued prompt is not pending")
)

// runtimeQueueController is deliberately optional so embedded app-server
// adapters are not forced to claim server-originated runtime work. Production
// QueryEngine implements this contract.
type runtimeQueueController interface {
	EnqueueUserInputWithID(string, engine.UserTurnInput) (engine.QueuedUserInput, error)
	HasQueuedUserInputAdmission(string, engine.UserTurnInput) (bool, error)
	QueuedPromptState() (engine.QueuedPromptState, error)
	CancelQueuedPrompt(string) (bool, error)
	SubscribeRuntimeItems() <-chan struct{}
}

var _ runtimeQueueController = (*engine.QueryEngine)(nil)

func (s *session) runtimeQueueController() (runtimeQueueController, bool) {
	controller, ok := s.engine.(runtimeQueueController)
	return controller, ok
}

func validatePromptText(value string) (string, error) {
	prompt := strings.TrimSpace(value)
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	if !utf8.ValidString(prompt) {
		return "", fmt.Errorf("prompt must be valid UTF-8")
	}
	if len(prompt) > maxPromptBytes {
		return "", fmt.Errorf("prompt exceeds %d bytes", maxPromptBytes)
	}
	return prompt, nil
}

func validateClientUUID(value, field string) (string, error) {
	id := strings.TrimSpace(value)
	if id == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return "", fmt.Errorf("%s must be a UUID: %w", field, err)
	}
	canonical := parsed.String()
	if len(id) != len(canonical) || !strings.EqualFold(id, canonical) {
		return "", fmt.Errorf("%s must use canonical UUID form", field)
	}
	return canonical, nil
}

func projectQueuedPrompt(snapshot engine.QueuedPromptSnapshot) QueuedPrompt {
	return QueuedPrompt{
		ID:          strings.TrimSpace(snapshot.ID),
		Display:     truncateSnapshotText(snapshot.Display, maxQueuedPromptDisplayRunes),
		EnqueuedAt:  snapshot.EnqueuedAt,
		State:       string(snapshot.State),
		Unavailable: snapshot.Unavailable,
	}
}

func (s *session) queuedPrompts() (QueuedPromptsResponse, error) {
	controller, ok := s.runtimeQueueController()
	if !ok {
		return QueuedPromptsResponse{Items: []QueuedPrompt{}}, errRuntimeQueueUnavailable
	}
	state, err := controller.QueuedPromptState()
	if err != nil {
		return QueuedPromptsResponse{Items: []QueuedPrompt{}}, err
	}
	items := make([]QueuedPrompt, 0, len(state.Items))
	for _, snapshot := range state.Items {
		items = append(items, projectQueuedPrompt(snapshot))
	}
	return QueuedPromptsResponse{Revision: state.Revision, Items: items}, nil
}

func (s *session) readQueuedPrompts() (QueuedPromptsResponse, error) {
	s.turnAdmissionMu.Lock()
	defer s.turnAdmissionMu.Unlock()
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return QueuedPromptsResponse{}, errSessionClosed
	}
	return s.queuedPrompts()
}

func (s *session) enqueuePrompt(
	input QueuePromptRequest,
) (QueuePromptResponse, error) {
	prompt, err := validatePromptText(input.Prompt)
	if err != nil {
		return QueuePromptResponse{}, err
	}
	queueID, err := validateClientUUID(input.ClientQueueID, "client_queue_id")
	if err != nil {
		return QueuePromptResponse{}, err
	}
	controller, ok := s.runtimeQueueController()
	if !ok {
		return QueuePromptResponse{}, errRuntimeQueueUnavailable
	}
	s.turnAdmissionMu.Lock()
	defer s.turnAdmissionMu.Unlock()
	s.mu.Lock()
	closed := s.closed
	active := s.activeTurnID != "" &&
		(s.status == "running" || s.status == "waiting" || s.status == "stopping")
	s.mu.Unlock()
	if closed {
		return QueuePromptResponse{}, errSessionClosed
	}
	userInput := engine.UserTurnInput{
		Display: prompt,
		Prompt:  prompt,
	}
	historical := false
	if !active {
		historical, err = controller.HasQueuedUserInputAdmission(queueID, userInput)
		if err != nil {
			return QueuePromptResponse{}, err
		}
		if !historical {
			return QueuePromptResponse{}, errQueueRequiresActiveTurn
		}
	} else if _, err := controller.EnqueueUserInputWithID(queueID, userInput); err != nil {
		return QueuePromptResponse{}, err
	}
	if !historical {
		s.unblockRuntimeDrain()
	}
	queued, err := s.queuedPrompts()
	if err != nil {
		return QueuePromptResponse{}, err
	}
	s.publishQueuedPrompts(queued)
	if !historical {
		s.signalRuntimeDrain()
	}
	pending := false
	for _, item := range queued.Items {
		if item.ID == queueID {
			pending = true
			break
		}
	}
	return QueuePromptResponse{
		SessionID:  s.id,
		AcceptedID: queueID,
		Pending:    pending,
		Revision:   queued.Revision,
		Items:      queued.Items,
	}, nil
}

func (s *session) cancelQueuedPrompt(id string) (QueuedPromptsResponse, error) {
	queueID, err := validateClientUUID(id, "queue_id")
	if err != nil {
		return QueuedPromptsResponse{}, err
	}
	controller, ok := s.runtimeQueueController()
	if !ok {
		return QueuedPromptsResponse{}, errRuntimeQueueUnavailable
	}
	s.turnAdmissionMu.Lock()
	defer s.turnAdmissionMu.Unlock()
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return QueuedPromptsResponse{}, errSessionClosed
	}
	cancelled, err := controller.CancelQueuedPrompt(queueID)
	if err != nil {
		return QueuedPromptsResponse{}, err
	}
	if !cancelled {
		return QueuedPromptsResponse{}, errQueuedPromptNotPending
	}
	s.unblockRuntimeDrain()
	queued, err := s.queuedPrompts()
	if err != nil {
		return QueuedPromptsResponse{}, err
	}
	s.publishQueuedPrompts(queued)
	s.signalRuntimeDrain()
	return queued, nil
}

func (s *session) publishQueuedPrompts(queued QueuedPromptsResponse) {
	s.publishSynthetic("queue.updated", "", queued)
}

func (s *session) publishCurrentQueuedPrompts() {
	queued, err := s.queuedPrompts()
	if err == nil {
		s.publishQueuedPrompts(queued)
	}
}

func (s *session) startRuntimeInputPump() {
	controller, ok := s.runtimeQueueController()
	if !ok {
		return
	}
	s.runtimePumpOnce.Do(func() {
		ready := controller.SubscribeRuntimeItems()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			for {
				select {
				case _, open := <-ready:
					if !open {
						ready = nil
						continue
					}
					s.tryStartNextRuntimeItem()
				case <-s.runtimeWake:
					s.tryStartNextRuntimeItem()
				case <-s.rootCtx.Done():
					return
				}
			}
		}()
		s.signalRuntimeDrain()
	})
}

func (s *session) signalRuntimeDrain() {
	if s == nil || s.runtimeWake == nil {
		return
	}
	select {
	case s.runtimeWake <- struct{}{}:
	default:
	}
}

func (s *session) unblockRuntimeDrain() {
	s.mu.Lock()
	wasBlocked := s.runtimeDrainBlocked
	s.runtimeDrainBlocked = false
	recovered := false
	if wasBlocked && s.status == "error" && s.lastError == runtimeQueueBlockedMessage {
		s.status = "idle"
		s.lastError = ""
		s.updatedAt = time.Now().UTC()
		recovered = true
	}
	s.mu.Unlock()
	if recovered {
		s.publishSynthetic("queue.rewake_ready", "", map[string]string{
			"code": "runtime_queue_ready",
		})
	}
}

func (s *session) tryStartNextRuntimeItem() {
	s.turnAdmissionMu.Lock()
	defer s.turnAdmissionMu.Unlock()
	s.startNextRuntimeItemLocked()
}

// startNextRuntimeItemLocked runs with turnAdmissionMu held. That admission
// lock remains held across durable claim and active-turn publication, so close
// or a direct turn cannot split claim from active-turn ownership.
func (s *session) startNextRuntimeItemLocked() {
	if _, ok := s.runtimeQueueController(); !ok {
		return
	}
	s.mu.Lock()
	if s.closed || s.activeTurnID != "" || s.status != "idle" || s.runtimeDrainBlocked {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	item, claimed, err := s.engine.ClaimNextRuntimeItem()
	if err != nil {
		s.mu.Lock()
		s.runtimeDrainBlocked = true
		s.status = "error"
		s.lastError = runtimeQueueBlockedMessage
		s.updatedAt = time.Now().UTC()
		s.mu.Unlock()
		s.publishSynthetic("queue.rewake_blocked", "", map[string]string{
			"code":    "runtime_queue_blocked",
			"message": runtimeQueueBlockedMessage,
		})
		return
	}
	if !claimed {
		return
	}
	turnID := uuid.NewString()
	turnCtx, cancel := context.WithCancel(s.rootCtx)
	display := ""
	if item.Kind == engine.RuntimeItemUserPrompt && item.UserPrompt != nil {
		display = strings.TrimSpace(item.UserPrompt.Display)
	}
	s.mu.Lock()
	s.activeTurnID = turnID
	s.activePrompt = display
	s.activeCancel = cancel
	s.immediateCancelTurnID = ""
	s.status = "running"
	s.updatedAt = time.Now().UTC()
	s.lastError = ""
	s.wg.Add(1)
	s.mu.Unlock()

	s.publishCurrentQueuedPrompts()
	if display != "" {
		s.publishSynthetic("user_message", turnID, map[string]any{
			"content":        truncateSnapshotText(display, maxQueuedPromptDisplayRunes),
			"queued_item_id": item.ID,
		})
	}
	s.publishSynthetic("turn.accepted", turnID, map[string]any{
		"turn_id":        turnID,
		"queued_item_id": item.ID,
	})
	go s.runRuntimeItem(turnCtx, turnID, item)
}

func (s *session) runRuntimeItem(
	ctx context.Context,
	turnID string,
	item engine.RuntimeItem,
) {
	defer s.wg.Done()
	events, terminal := s.engine.SubmitRuntimeItem(ctx, item)
	reason, err := s.driveEvents(ctx, turnID, events, terminal)
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

func (s *Server) handleQueuedPrompts(w http.ResponseWriter, r *http.Request) {
	owned, ok := s.getSession(r.PathValue("session_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session_not_found", "session not found")
		return
	}
	queued, err := owned.readQueuedPrompts()
	if errors.Is(err, errRuntimeQueueUnavailable) {
		writeError(w, http.StatusNotImplemented, "runtime_queue_unavailable", "runtime queue is unavailable")
		return
	}
	if errors.Is(err, errSessionClosed) {
		writeError(w, http.StatusGone, "session_closed", "session is closed")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "runtime_queue_failed", "runtime queue could not be read")
		return
	}
	writeJSON(w, http.StatusOK, queued)
}

func (s *Server) handleQueuePrompt(w http.ResponseWriter, r *http.Request) {
	owned, ok := s.getSession(r.PathValue("session_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session_not_found", "session not found")
		return
	}
	var input QueuePromptRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	queued, err := owned.enqueuePrompt(input)
	var conflict *engine.RuntimeInputConflictError
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, queued)
	case errors.Is(err, errRuntimeQueueUnavailable):
		writeError(w, http.StatusNotImplemented, "runtime_queue_unavailable", "runtime queue is unavailable")
	case errors.Is(err, errSessionClosed):
		writeError(w, http.StatusGone, "session_closed", "session is closed")
	case errors.Is(err, errQueueRequiresActiveTurn):
		writeError(w, http.StatusConflict, "queue_requires_active_turn", "queued prompts require an active turn")
	case errors.As(err, &conflict):
		writeError(w, http.StatusConflict, "queue_item_conflict", "client_queue_id conflicts with an existing queued prompt")
	case strings.Contains(err.Error(), "client_queue_id") ||
		strings.Contains(err.Error(), "prompt"):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		writeError(w, http.StatusUnprocessableEntity, "queue_rejected", "queued prompt was rejected")
	}
}

func (s *Server) handleCancelQueuedPrompt(w http.ResponseWriter, r *http.Request) {
	owned, ok := s.getSession(r.PathValue("session_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session_not_found", "session not found")
		return
	}
	queued, err := owned.cancelQueuedPrompt(r.PathValue("queue_id"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, queued)
	case errors.Is(err, errRuntimeQueueUnavailable):
		writeError(w, http.StatusNotImplemented, "runtime_queue_unavailable", "runtime queue is unavailable")
	case errors.Is(err, errSessionClosed):
		writeError(w, http.StatusGone, "session_closed", "session is closed")
	case errors.Is(err, errQueuedPromptNotPending):
		writeError(w, http.StatusConflict, "queue_item_not_pending", "queued prompt is already processing or absent")
	case strings.Contains(err.Error(), "queue_id"):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "runtime_queue_failed", "queued prompt could not be cancelled")
	}
}
