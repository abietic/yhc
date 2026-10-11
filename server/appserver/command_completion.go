package appserver

import (
	"context"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/abietic/yhc/engine/commands"
)

type sessionCommandCompleter interface {
	CompleteCommandInput(context.Context, commands.CompletionRequest) commands.InputCompletion
}

type commandCompletionRequest struct {
	Input  *string `json:"input"`
	Cursor *int    `json:"cursor"`
}

func (s *Server) handleCommandCompletion(w http.ResponseWriter, r *http.Request) {
	owned, ok := s.getSession(r.PathValue("session_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session_not_found", "session not found")
		return
	}
	// Keep drafts in a bounded POST body, never in URLs or durable events.
	r.Body = http.MaxBytesReader(w, r.Body, 32768)
	var input commandCompletionRequest
	if err := decodeJSON(r, &input); err != nil || input.Input == nil || input.Cursor == nil || !utf8.ValidString(*input.Input) || utf8.RuneCountInString(*input.Input) > commands.MaxCompletionRunes || *input.Cursor < 0 || *input.Cursor > utf8.RuneCountInString(*input.Input) || strings.IndexFunc(*input.Input, unicode.IsControl) >= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "valid bounded command input and rune cursor required")
		return
	}
	completer, ok := owned.engine.(sessionCommandCompleter)
	if !ok {
		writeError(w, http.StatusNotImplemented, "command_completion_unavailable", "command completion is unavailable")
		return
	}
	// Completion is local and deterministic. Serialize it with admission/close
	// so the engine cannot be closed or admit a new turn during this snapshot.
	owned.turnAdmissionMu.Lock()
	defer owned.turnAdmissionMu.Unlock()
	owned.mu.Lock()
	blocked := owned.closed || owned.activeTurnID != ""
	owned.mu.Unlock()
	result := commands.InputCompletion{Candidates: []commands.InputCandidate{}}
	if !blocked && r.Context().Err() == nil {
		result = completer.CompleteCommandInput(r.Context(), commands.CompletionRequest{Input: *input.Input, Cursor: *input.Cursor})
	}
	writeJSON(w, http.StatusOK, result)
}
