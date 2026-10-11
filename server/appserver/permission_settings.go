package appserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/permission"
)

// Optional to keep SessionEngine adapters independent of settings ownership.
type sessionPermissionSettingsController interface {
	PermissionRuleSettings(string) (permission.RuleSettings, error)
	ApplyPermissionRuleSettings(context.Context, string, string, []permission.RuleSettingsChange) (engine.PermissionRuleSettingsResult, error)
}

var errPermissionSettingsUnavailable = errors.New("permission settings adapter unavailable")

type permissionSettingsResponse struct {
	engine.PermissionRuleSettingsResult
	SessionID string `json:"session_id"`
}

type permissionSettingsRequest struct {
	Revision string                          `json:"revision"`
	Changes  []permission.RuleSettingsChange `json:"changes"`
}

// Scope and an owned session are the only context selectors. The renderer
// cannot supply paths, callbacks, general settings JSON, or durable identities.
func (s *Server) permissionSettingsContext(w http.ResponseWriter, r *http.Request) (string, *session, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, 400, "settings_invalid", "invalid permission settings context")
		return "", nil, false
	}
	for key, values := range query {
		if (key != "scope" && key != "session_id") || len(values) != 1 || values[0] == "" {
			writeError(w, 400, "settings_invalid", "invalid permission settings context")
			return "", nil, false
		}
	}
	scope := query.Get("scope")
	if scope == "" {
		scope = "user"
	}
	if scope != "user" && scope != "project" && scope != "local" {
		writeError(w, 400, "settings_invalid", "choose user, project or local scope")
		return "", nil, false
	}
	var owned *session
	if id := query.Get("session_id"); id != "" {
		var ok bool
		owned, ok = s.getSession(id)
		if !ok {
			writeError(w, 404, "session_not_found", "open a live session to configure its rules")
			return "", nil, false
		}
	}
	if owned == nil && scope != "user" {
		writeError(w, 400, "settings_workspace_required", "open a live session to configure workspace rules")
		return "", nil, false
	}
	return scope, owned, true
}

func (s *Server) handleGetPermissionSettings(w http.ResponseWriter, r *http.Request) {
	s.handlePermissionSettings(w, r, false)
}

func (s *Server) handleUpdatePermissionSettings(w http.ResponseWriter, r *http.Request) {
	s.handlePermissionSettings(w, r, true)
}

func (s *Server) handlePermissionSettings(w http.ResponseWriter, r *http.Request, update bool) {
	scope, owned, ok := s.permissionSettingsContext(w, r)
	if !ok {
		return
	}
	var input permissionSettingsRequest
	if update {
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, 400, "settings_invalid", "invalid permission rule changes")
			return
		}
	}
	result, err := s.permissionSettingsResult(r.Context(), scope, owned, update, input)
	if err != nil {
		writePermissionSettingsError(w, err)
		return
	}
	response := permissionSettingsResponse{PermissionRuleSettingsResult: result}
	if owned != nil {
		response.SessionID = owned.id
	}
	// No ownership/admission locks are held while writing to a slow client.
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) permissionSettingsResult(ctx context.Context, scope string, owned *session, update bool, input permissionSettingsRequest) (engine.PermissionRuleSettingsResult, error) {
	var controller sessionPermissionSettingsController
	if owned != nil {
		// Match ordinary turn admission and close ordering, then let QueryEngine
		// enforce its own Plan/turn boundary. Never reload other live engines.
		owned.turnAdmissionMu.Lock()
		defer owned.turnAdmissionMu.Unlock()
	}
	// DELETE and Shutdown retire map ownership under the write lock. This
	// read lease covers inspection, persistence and live reload, not body I/O.
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closing || (owned != nil && s.sessions[owned.id] != owned) {
		return engine.PermissionRuleSettingsResult{}, errSessionClosed
	}
	if owned != nil {
		owned.mu.Lock()
		closed, active := owned.closed, owned.activeTurnID != ""
		owned.mu.Unlock()
		if closed {
			return engine.PermissionRuleSettingsResult{}, errSessionClosed
		}
		var ok bool
		controller, ok = owned.engine.(sessionPermissionSettingsController)
		if !ok {
			return engine.PermissionRuleSettingsResult{}, errPermissionSettingsUnavailable
		}
		if update && active {
			return engine.PermissionRuleSettingsResult{}, engine.ErrPermissionSettingsActiveTurn
		}
	}
	if err := ctx.Err(); err != nil {
		return engine.PermissionRuleSettingsResult{}, err
	}
	var result engine.PermissionRuleSettingsResult
	var err error
	if update {
		if controller != nil {
			result, err = controller.ApplyPermissionRuleSettings(ctx, scope, input.Revision, input.Changes)
		} else {
			result.RuleSettings, err = permission.RuleSettingsPatchContext(ctx, "", scope, input.Revision, input.Changes)
		}
	} else if controller != nil {
		result.RuleSettings, err = controller.PermissionRuleSettings(scope)
	} else {
		result.RuleSettings, err = permission.RuleSettingsSnapshot("", scope)
	}
	return result, err
}

func writePermissionSettingsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errSessionClosed):
		writeError(w, 404, "session_not_found", "open a live session to configure its rules")
	case errors.Is(err, errPermissionSettingsUnavailable):
		writeError(w, 501, "permission_settings_unavailable", "permission settings are unavailable for this runtime")
	case errors.Is(err, engine.ErrPermissionSettingsActiveTurn):
		writeError(w, 409, "permission_settings_active_turn", "wait for execution and Plan approval to finish before saving rules")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(w, 408, "settings_cancelled", "permission settings request was cancelled before saving")
	case errors.Is(err, config.ErrSettingsConflict):
		writeError(w, 409, "settings_conflict", "settings changed elsewhere; reload before saving")
	case errors.Is(err, config.ErrSettingsBusy):
		writeError(w, 409, "settings_busy", "another process is saving settings; try again")
	case errors.Is(err, config.ErrSettingsInvalid):
		writeError(w, 422, "settings_invalid", "invalid permission rules or changes; check configuration before retrying")
	default:
		writeError(w, 422, "settings_unavailable", "permission settings could not be accessed safely")
	}
}
