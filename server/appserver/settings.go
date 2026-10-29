package appserver

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/abietic/yhc/engine/config"
)

// settingsContext accepts only a server-owned session, never a renderer path.
func (s *Server) settingsContext(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "user"
	}
	if scope != "user" && scope != "project" {
		writeError(w, http.StatusBadRequest, "settings_invalid", "choose user or project scope")
		return "", "", false
	}
	cwd := ""
	if id := r.URL.Query().Get("session_id"); id != "" {
		owned, ok := s.getSession(id)
		if !ok {
			writeError(w, http.StatusNotFound, "session_not_found", "open a live session to configure its workspace")
			return "", "", false
		}
		cwd = owned.cwd
	}
	if scope == "project" && cwd == "" {
		writeError(w, http.StatusBadRequest, "settings_workspace_required", "open a live session to configure its workspace")
		return "", "", false
	}
	return cwd, scope, true
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	cwd, scope, ok := s.settingsContext(w, r)
	if !ok {
		return
	}
	snapshot, err := config.SettingsSnapshot(cwd, scope)
	if err != nil {
		writeSettingsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	cwd, scope, ok := s.settingsContext(w, r)
	if !ok {
		return
	}
	var input struct {
		Revision string                     `json:"revision"`
		Changes  map[string]json.RawMessage `json:"changes"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "settings_invalid", "invalid settings request")
		return
	}
	snapshot, err := config.SettingsPatchContext(r.Context(), cwd, scope, input.Revision, input.Changes)
	if err != nil {
		writeSettingsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func writeSettingsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, config.ErrSettingsConflict):
		writeError(w, http.StatusConflict, "settings_conflict", "settings changed elsewhere; reload before saving")
	case errors.Is(err, config.ErrSettingsInvalid):
		// The shared service reports allowlisted field names and fixed guidance,
		// never submitted values, secrets, or filesystem error text.
		writeError(w, http.StatusUnprocessableEntity, "settings_invalid", err.Error())
	case errors.Is(err, config.ErrSettingsBusy):
		writeError(w, http.StatusConflict, "settings_busy", "another process is saving settings; try again")
	default:
		writeError(w, http.StatusUnprocessableEntity, "settings_unavailable", "settings could not be accessed safely")
	}
}
