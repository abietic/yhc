package appserver

import (
	"context"
	"encoding/json"
	"net/http"
	"unicode"
	"unicode/utf8"

	"github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/services"
)

type sessionPromptSuggester interface {
	GeneratePromptSuggestionResult(context.Context) (services.PromptSuggestionResult, error)
}

type promptSuggestionRequest struct {
	cancel context.CancelFunc
}

// cancelPromptSuggestionLocked runs with s.mu held. Keep the request installed
// until it actually returns, so a provider that ignores cancellation cannot
// create overlapping auxiliary requests or escape the close join.
func (s *session) cancelPromptSuggestionLocked() {
	if request := s.promptSuggestionRequest; request != nil {
		request.cancel()
	}
}

func (s *session) promptSuggestionsEnabled() bool {
	snapshot, err := config.SettingsSnapshot(s.cwd, "project")
	if err != nil {
		return false
	}
	var enabled bool
	return json.Unmarshal(snapshot.Fields["prompt_suggestions"].EffectiveValue, &enabled) == nil && enabled
}

// promptSuggestion owns one explicit, ephemeral side query. Admission/close
// serialize only the snapshot, never wait behind a provider request. New turns
// cancel it; close cancels and joins it before closing the engine.
func (s *session) promptSuggestion(ctx context.Context) services.PromptSuggestionResult {
	suppressed := func(reason string) services.PromptSuggestionResult {
		return services.PromptSuggestionResult{Status: services.PromptSuggestionSuppressed, Reason: reason}
	}
	suggester, ok := s.engine.(sessionPromptSuggester)
	if !ok || ctx == nil {
		return suppressed("unavailable")
	}
	if err := ctx.Err(); err != nil {
		return services.PromptSuggestionErrorResult(err)
	}
	s.turnAdmissionMu.Lock()
	s.mu.Lock()
	reason := ""
	switch {
	case s.closed:
		reason = "unavailable"
	case s.activeTurnID != "" || s.status != "idle":
		reason = "busy"
	case s.promptSuggestionRequest != nil:
		reason = "in_flight"
	}
	s.mu.Unlock()
	if reason == "" && !s.promptSuggestionsEnabled() {
		reason = "disabled"
	}
	if reason != "" {
		s.turnAdmissionMu.Unlock()
		return suppressed(reason)
	}
	requestCtx, cancel := context.WithCancel(ctx)
	stopRootCancellation := context.AfterFunc(s.rootCtx, cancel)
	request := &promptSuggestionRequest{cancel: cancel}
	s.mu.Lock()
	s.promptSuggestionRequest = request
	s.wg.Add(1)
	s.mu.Unlock()
	s.turnAdmissionMu.Unlock()
	defer s.wg.Done()
	defer cancel()
	defer stopRootCancellation()

	result, err := suggester.GeneratePromptSuggestionResult(requestCtx)
	if err != nil {
		result = services.PromptSuggestionErrorResult(err)
	}
	enabled := s.promptSuggestionsEnabled()
	s.turnAdmissionMu.Lock()
	defer s.turnAdmissionMu.Unlock()
	s.mu.Lock()
	valid := s.promptSuggestionRequest == request && !s.closed && s.activeTurnID == "" && s.status == "idle" && requestCtx.Err() == nil
	if s.promptSuggestionRequest == request {
		s.promptSuggestionRequest = nil
	}
	s.mu.Unlock()
	if err := requestCtx.Err(); err != nil {
		return services.PromptSuggestionErrorResult(err)
	}
	if !valid {
		return services.PromptSuggestionResult{Status: services.PromptSuggestionCancelled}
	}
	if !enabled {
		return suppressed("disabled")
	}
	if result.Status != services.PromptSuggestionAvailable {
		result.Suggestion = ""
		return result
	}
	text := result.Suggestion
	if !utf8.ValidString(text) || len(text) >= 100 || services.FilterSuggestion(text) != "" || containsSuggestionControl(text) {
		return services.PromptSuggestionResult{Status: services.PromptSuggestionFiltered, Reason: "invalid_text"}
	}
	return result
}

func containsSuggestionControl(text string) bool {
	for _, r := range text {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func (s *Server) handlePromptSuggestion(w http.ResponseWriter, r *http.Request) {
	owned, ok := s.getSession(r.PathValue("session_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session_not_found", "session not found")
		return
	}
	// No client conversation, workspace path, model, or provider may enter this
	// boundary. POST requires the existing bearer or same-origin CSRF admission.
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var input *struct{}
	if err := decodeJSON(r, &input); err != nil || input == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "empty suggestion request required")
		return
	}
	result := owned.promptSuggestion(r.Context())
	writeJSON(w, http.StatusOK, struct {
		SessionID string `json:"session_id"`
		services.PromptSuggestionResult
	}{SessionID: owned.id, PromptSuggestionResult: result})
}
