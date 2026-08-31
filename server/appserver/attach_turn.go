package appserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	enginesession "github.com/abietic/yhc/engine/session"
)

type attachFlight struct {
	clientTurnID string
	promptDigest string
	done         chan struct{}
	once         sync.Once
	response     AttachTurnResponse
	err          error
}

func (f *attachFlight) finish(response AttachTurnResponse, err error) {
	f.once.Do(func() {
		f.response = response
		f.err = err
		close(f.done)
	})
}

func (f *attachFlight) fail(err error) {
	f.finish(AttachTurnResponse{}, err)
}

type attachReceipt struct {
	clientTurnID string
	promptDigest string
	response     AttachTurnResponse
}

func validateAttachTurn(input AttachTurnRequest) (AttachTurnRequest, string, error) {
	input.Prompt = strings.TrimSpace(input.Prompt)
	input.ClientTurnID = strings.TrimSpace(input.ClientTurnID)
	if input.Prompt == "" {
		return AttachTurnRequest{}, "", fmt.Errorf("prompt is required")
	}
	if !utf8.ValidString(input.Prompt) {
		return AttachTurnRequest{}, "", fmt.Errorf("prompt must be valid UTF-8")
	}
	if len(input.Prompt) > maxPromptBytes {
		return AttachTurnRequest{}, "", fmt.Errorf("prompt exceeds %d bytes", maxPromptBytes)
	}
	if input.ClientTurnID == "" {
		return AttachTurnRequest{}, "", fmt.Errorf("client_turn_id is required")
	}
	if _, err := uuid.Parse(input.ClientTurnID); err != nil {
		return AttachTurnRequest{}, "", fmt.Errorf("client_turn_id must be a UUID: %w", err)
	}
	digest := sha256.Sum256([]byte(input.Prompt))
	return input, hex.EncodeToString(digest[:]), nil
}

func (s *Server) handleAttachTurn(w http.ResponseWriter, r *http.Request) {
	var input AttachTurnRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	input, digest, err := validateAttachTurn(input)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	sessionID := r.PathValue("session_id")
	info, err := s.resolveDurableSession(sessionID)
	if err != nil {
		s.writeAttachResolveError(w, err)
		return
	}
	if !isAttachableDurableSession(*info) {
		writeError(
			w,
			http.StatusUnprocessableEntity,
			"durable_session_not_resumable",
			"durable session is not resumable",
		)
		return
	}
	admitted, err := s.admitDurableSession(r.Context(), *info)
	if err != nil {
		if errors.Is(err, enginesession.ErrLegacySessionImportRequired) {
			writeError(
				w,
				http.StatusConflict,
				"legacy_import_required",
				"this legacy session must be imported before it can be attached",
			)
			return
		}
		s.writeAttachResolveError(w, err)
		return
	}
	if s.validateResume != nil {
		if err := s.validateResume(r.Context(), EngineOptions{
			SessionID:     admitted.SessionID,
			ThreadID:      admitted.SessionID,
			CWD:           admitted.CWD,
			TranscriptDir: admitted.TranscriptDir,
			Resume:        true,
		}); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "attach_failed", "durable session validation failed")
			return
		}
	}

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		writeError(w, http.StatusServiceUnavailable, "server_closing", "app-server is shutting down")
		return
	}
	if receipt, ok := s.attachReceipts[sessionID]; ok {
		s.mu.Unlock()
		s.writeAttachReceipt(w, receipt, input.ClientTurnID, digest)
		return
	}
	if _, live := s.sessions[sessionID]; live {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "session_already_attached", "session is already attached")
		return
	}
	if flight := s.activating[sessionID]; flight != nil {
		if flight.clientTurnID != input.ClientTurnID {
			s.mu.Unlock()
			writeError(w, http.StatusConflict, "attach_in_progress", "session attachment is in progress")
			return
		}
		if flight.promptDigest != digest {
			s.mu.Unlock()
			writeError(w, http.StatusConflict, "client_turn_conflict", "client turn id was reused with another prompt")
			return
		}
		s.mu.Unlock()
		s.waitAttachFlight(w, r, flight)
		return
	}
	if s.sessionOccupancyLocked() >= s.maxSessions {
		s.mu.Unlock()
		writeError(w, http.StatusTooManyRequests, "session_limit", "app-server session limit reached")
		return
	}
	flight := &attachFlight{
		clientTurnID: input.ClientTurnID,
		promptDigest: digest,
		done:         make(chan struct{}),
	}
	s.activating[sessionID] = flight
	s.activationWG.Add(1)
	s.mu.Unlock()
	go s.activateAttach(sessionID, admitted, input, flight)
	s.waitAttachFlight(w, r, flight)
}

func isAttachableDurableSession(info enginesession.SessionInfo) bool {
	return isSupportedSessionID(info.SessionID) && strings.TrimSpace(info.CWD) != "" &&
		strings.TrimSpace(info.ParentSessionID) == "" && strings.TrimSpace(info.AgentID) == ""
}

func (s *Server) writeAttachResolveError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errDurableSessionNotFound):
		writeError(w, http.StatusNotFound, "durable_session_not_found", "durable session not found")
	case errors.Is(err, errDurableSessionAmbiguous):
		writeError(w, http.StatusConflict, "durable_session_ambiguous", "durable session is ambiguous")
	case errors.Is(err, errDurableServerClosing):
		writeError(w, http.StatusServiceUnavailable, "server_closing", "app-server is shutting down")
	default:
		writeError(w, http.StatusInternalServerError, "durable_sessions_failed", "could not inspect durable sessions")
	}
}

func (s *Server) writeAttachReceipt(
	w http.ResponseWriter,
	receipt attachReceipt,
	clientTurnID string,
	digest string,
) {
	if receipt.clientTurnID != clientTurnID {
		writeError(w, http.StatusConflict, "session_already_attached", "session is already attached")
		return
	}
	if receipt.promptDigest != digest {
		writeError(w, http.StatusConflict, "client_turn_conflict", "client turn id was reused with another prompt")
		return
	}
	writeJSON(w, http.StatusOK, receipt.response)
}

func (s *Server) waitAttachFlight(w http.ResponseWriter, r *http.Request, flight *attachFlight) {
	select {
	case <-flight.done:
		if flight.err != nil {
			if errors.Is(flight.err, errDurableServerClosing) {
				writeError(w, http.StatusServiceUnavailable, "server_closing", "app-server is shutting down")
			} else {
				writeError(w, http.StatusUnprocessableEntity, "attach_failed", flight.err.Error())
			}
			return
		}
		writeJSON(w, http.StatusOK, flight.response)
	case <-r.Context().Done():
		// The server-owned activation intentionally continues after a lost client.
		return
	}
}

func (s *Server) activateAttach(
	sessionID string,
	info enginesession.SessionInfo,
	input AttachTurnRequest,
	flight *attachFlight,
) {
	defer s.activationWG.Done()
	var owned *session
	var response AttachTurnResponse
	var err error
	defer func() {
		if err == nil && owned == nil {
			err = errors.New("attach activation returned no session")
		}
		s.mu.Lock()
		delete(s.activating, sessionID)
		if err == nil && !s.closing {
			s.sessions[sessionID] = owned
			s.attachReceipts[sessionID] = attachReceipt{
				clientTurnID: input.ClientTurnID,
				promptDigest: flight.promptDigest,
				response:     response,
			}
		} else if err == nil {
			err = errDurableServerClosing
		}
		s.mu.Unlock()
		if err != nil && owned != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_ = owned.close(closeCtx)
			cancel()
		}
		flight.finish(response, err)
	}()

	owned, err = newSession(
		s.ctx,
		s.factory,
		s.id,
		CreateSessionRequest{
			SessionID: sessionID,
			CWD:       info.CWD,
			Title:     durableSessionTitle(info),
			Resume:    true,
		},
		&info,
		s.eventBuffer,
		s.now().UTC(),
	)
	if err != nil {
		return
	}
	if interaction, recoveredErr := owned.restorePendingProjectGraph(); recoveredErr != nil {
		err = recoveredErr
		return
	} else if interaction != nil {
		owned.startRuntimeInputPump()
		response = AttachTurnResponse{
			Status:       "interaction_required",
			Session:      owned.summary(),
			ClientTurnID: input.ClientTurnID,
			Interaction:  interaction,
		}
		return
	}
	started, startErr := owned.startTurn(StartTurnRequest(input))
	if startErr != nil {
		err = startErr
		return
	}
	owned.startRuntimeInputPump()
	response = AttachTurnResponse{
		Status:       "turn_accepted",
		Session:      owned.summary(),
		ClientTurnID: input.ClientTurnID,
		TurnID:       started.TurnID,
	}
}
